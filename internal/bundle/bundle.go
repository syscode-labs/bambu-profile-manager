// Package bundle implements the portable .profilepack format (design.md
// §11): a ZIP containing every profile in a filament's dependency closure,
// its resolved effective form, and checksums, so it stays usable even if
// the original Bambu Studio installation is gone.
package bundle

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/normalize"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

// ParserVersion is recorded in every bundle's manifest so a future parser
// can tell which version produced it (design.md §11).
const ParserVersion = "v0"

// SourceEntry maps a profile's name to the file it's stored under in
// source/, since names can contain characters that aren't safe filenames.
type SourceEntry struct {
	Name string `json:"name"`
	File string `json:"file"`
}

// Manifest is manifest.json inside the bundle.
type Manifest struct {
	LeafName      string        `json:"leaf_name"`
	ProfileID     string        `json:"profile_id,omitempty"`
	Revision      int           `json:"revision,omitempty"`
	SemanticHash  string        `json:"semantic_hash"`
	ParserVersion string        `json:"parser_version"`
	Chain         []string      `json:"chain"` // root -> leaf, by name
	Source        []SourceEntry `json:"source"`
}

// Export writes leafName's full dependency closure (resolved from set) as a
// .profilepack to w.
func Export(w io.Writer, set resolver.Set, leafName string, profileID string, revision int) error {
	leaf, ok := set[leafName]
	if !ok {
		return fmt.Errorf("bundle: export: %q not found in profile set", leafName)
	}

	effective, chain, err := resolver.Resolve(set, leaf)
	if err != nil {
		return fmt.Errorf("bundle: export: resolve %q: %w", leafName, err)
	}
	semanticHash, err := normalize.Hash(effective.Fields)
	if err != nil {
		return fmt.Errorf("bundle: export: hash: %w", err)
	}
	resolvedJSON, err := normalize.Canonical(effective.Fields)
	if err != nil {
		return fmt.Errorf("bundle: export: canonicalize: %w", err)
	}

	zw := zip.NewWriter(w)

	sums := map[string]string{}
	manifest := Manifest{
		LeafName:      leafName,
		ProfileID:     profileID,
		Revision:      revision,
		SemanticHash:  semanticHash,
		ParserVersion: ParserVersion,
		Chain:         chain,
	}

	for i, name := range chain {
		p := set[name]
		b, err := json.MarshalIndent(p.Fields, "", "  ")
		if err != nil {
			return fmt.Errorf("bundle: export: marshal %q: %w", name, err)
		}
		file := fmt.Sprintf("source/%02d.json", i)
		if err := writeZipFile(zw, file, b, sums); err != nil {
			return err
		}
		manifest.Source = append(manifest.Source, SourceEntry{Name: name, File: file})
	}

	if err := writeZipFile(zw, "resolved/complete.json", resolvedJSON, sums); err != nil {
		return err
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("bundle: export: marshal manifest: %w", err)
	}
	if err := writeZipFile(zw, "manifest.json", manifestJSON, sums); err != nil {
		return err
	}

	checksumsJSON, err := json.MarshalIndent(sums, "", "  ")
	if err != nil {
		return fmt.Errorf("bundle: export: marshal checksums: %w", err)
	}
	cw, err := zw.Create("checksums.json")
	if err != nil {
		return fmt.Errorf("bundle: export: create checksums.json: %w", err)
	}
	if _, err := cw.Write(checksumsJSON); err != nil {
		return fmt.Errorf("bundle: export: write checksums.json: %w", err)
	}

	return zw.Close()
}

func writeZipFile(zw *zip.Writer, name string, b []byte, sums map[string]string) error {
	sum := sha256.Sum256(b)
	sums[name] = hex.EncodeToString(sum[:])
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("bundle: create %s: %w", name, err)
	}
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("bundle: write %s: %w", name, err)
	}
	return nil
}

// Imported is the result of a successful bundle Import.
type Imported struct {
	Manifest  Manifest
	Set       resolver.Set // every profile in the closure, keyed by name
	Leaf      *domain.RawProfile
	Effective *domain.RawProfile // re-resolved from the imported set
}

// Import reads a .profilepack, re-resolves its dependency closure from the
// bundled source files alone (no filesystem access outside the archive),
// and verifies checksums plus the semantic hash match what Export recorded.
// This is the round-trip half of the Phase 0/1 exit gate (design.md §12,
// §19): resolve(import(export(source))) must equal the original.
func Import(r *zip.Reader) (*Imported, error) {
	files := map[string][]byte{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("bundle: import: open %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("bundle: import: read %s: %w", f.Name, err)
		}
		files[f.Name] = b
	}

	manifestB, ok := files["manifest.json"]
	if !ok {
		return nil, fmt.Errorf("bundle: import: missing manifest.json")
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestB, &manifest); err != nil {
		return nil, fmt.Errorf("bundle: import: decode manifest.json: %w", err)
	}

	checksumsB, ok := files["checksums.json"]
	if !ok {
		return nil, fmt.Errorf("bundle: import: missing checksums.json")
	}
	var sums map[string]string
	if err := json.Unmarshal(checksumsB, &sums); err != nil {
		return nil, fmt.Errorf("bundle: import: decode checksums.json: %w", err)
	}
	if err := verifyChecksums(files, sums); err != nil {
		return nil, err
	}

	set := resolver.Set{}
	for _, entry := range manifest.Source {
		b, ok := files[entry.File]
		if !ok {
			return nil, fmt.Errorf("bundle: import: manifest references missing file %s (profile %q)", entry.File, entry.Name)
		}
		var fields map[string]any
		if err := json.Unmarshal(b, &fields); err != nil {
			return nil, fmt.Errorf("bundle: import: decode %s: %w", entry.File, err)
		}
		name, _ := fields["name"].(string)
		inherits, _ := fields["inherits"].(string)
		set[entry.Name] = &domain.RawProfile{Name: name, Inherits: inherits, Fields: fields}
	}

	leaf, ok := set[manifest.LeafName]
	if !ok {
		return nil, fmt.Errorf("bundle: import: leaf %q not present in bundled source files", manifest.LeafName)
	}

	effective, chain, err := resolver.Resolve(set, leaf)
	if err != nil {
		return nil, fmt.Errorf("bundle: import: re-resolve: %w", err)
	}
	if !sameChain(chain, manifest.Chain) {
		return nil, fmt.Errorf("bundle: import: resolved chain %v does not match manifest chain %v", chain, manifest.Chain)
	}

	gotHash, err := normalize.Hash(effective.Fields)
	if err != nil {
		return nil, fmt.Errorf("bundle: import: hash: %w", err)
	}
	if gotHash != manifest.SemanticHash {
		return nil, fmt.Errorf("bundle: import: semantic hash mismatch: bundle claims %s, re-resolved to %s", manifest.SemanticHash, gotHash)
	}

	return &Imported{Manifest: manifest, Set: set, Leaf: leaf, Effective: effective}, nil
}

func verifyChecksums(files map[string][]byte, sums map[string]string) error {
	var bad []string
	for name, want := range sums {
		b, ok := files[name]
		if !ok {
			bad = append(bad, fmt.Sprintf("%s: missing", name))
			continue
		}
		sum := sha256.Sum256(b)
		got := hex.EncodeToString(sum[:])
		if got != want {
			bad = append(bad, fmt.Sprintf("%s: checksum mismatch", name))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("bundle: import: checksum verification failed: %s", strings.Join(bad, "; "))
	}
	return nil
}

func sameChain(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
