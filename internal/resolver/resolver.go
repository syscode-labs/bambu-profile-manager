// Package resolver resolves a Bambu Studio profile's `inherits` chain into
// an effective (flattened) profile.
//
// Resolution is name-based: `inherits` refers to another profile's `name`
// field, not a stable ID (confirmed against real Bambu Studio profiles —
// see openspec/changes/init-profile-manager/decisions.md #3).
package resolver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/parser"
)

var (
	ErrMissingParent = errors.New("resolver: missing parent")
	ErrCircular      = errors.New("resolver: circular inheritance")
)

// Set indexes profiles by name for inherits lookups. Callers build this from
// every system + user profile the leaf might transitively depend on.
type Set map[string]*domain.RawProfile

// Resolve walks leaf's inherits chain and returns the effective (flattened)
// profile plus the dependency chain from root to leaf (by name).
func Resolve(set Set, leaf *domain.RawProfile) (effective *domain.RawProfile, chain []string, err error) {
	seen := map[string]bool{}

	var walk func(p *domain.RawProfile) (map[string]any, error)
	walk = func(p *domain.RawProfile) (map[string]any, error) {
		if seen[p.Name] {
			return nil, fmt.Errorf("%w: %s", ErrCircular, p.Name)
		}
		seen[p.Name] = true

		merged := map[string]any{}
		if p.Inherits != "" {
			parent, ok := set[p.Inherits]
			if !ok {
				return nil, fmt.Errorf("%w: %q (parent of %q)", ErrMissingParent, p.Inherits, p.Name)
			}
			parentMerged, err := walk(parent)
			if err != nil {
				return nil, err
			}
			for k, v := range parentMerged {
				merged[k] = v
			}
		}
		for k, v := range p.Fields {
			merged[k] = v // child explicit values always win over inherited ones
		}
		chain = append(chain, p.Name)
		return merged, nil
	}

	fields, err := walk(leaf)
	if err != nil {
		return nil, nil, err
	}
	return &domain.RawProfile{Name: leaf.Name, Inherits: leaf.Inherits, Fields: fields}, chain, nil
}

// commonRootSuffix marks the shared base every Bambu profile family
// ultimately inherits from — confirmed against real system profiles:
// filament materials (ABS, PLA, PC, TPU, ...) all inherit "fdm_filament_common"
// directly; process/print profiles have an extra shared layer
// ("fdm_process_single_common"/"fdm_process_dual_common") above the
// literal universal root "fdm_process_common". Bambu names every one of
// these shared layers with a "_common" suffix and nothing else in either
// catalog does (checked against the full real system directory for both
// types) — so instead of hardcoding one exact root name, RootAncestorName
// stops one level short of the first ancestor whose name ends this way.
// Walking past it collapses every family into one match, which is useless
// for telling them apart (e.g. every material would match every other).
const commonRootSuffix = "_common"

// RootAncestorName returns the name of the family ancestor at the top of
// leaf's inherits chain — e.g. "fdm_filament_abs" for a filament profile,
// "fdm_process_single_0.20" for a process profile — independent of any
// printer/nozzle-specific ancestor in between, and independent of the
// shared common root every profile in that catalog eventually inherits
// (see commonRootSuffix's doc comment). Used to match "same family,
// different printer" candidates without the caller enumerating them by
// hand. If the chain never reaches a "_common" ancestor (a different
// vendor's template layout, or a standalone profile), this falls back to
// the literal top of the chain.
func RootAncestorName(set Set, leaf *domain.RawProfile) (string, error) {
	seen := map[string]bool{}
	cur := leaf
	for {
		if seen[cur.Name] {
			return "", fmt.Errorf("%w: %s", ErrCircular, cur.Name)
		}
		seen[cur.Name] = true
		if cur.Inherits == "" {
			return cur.Name, nil
		}
		if strings.HasSuffix(cur.Inherits, commonRootSuffix) {
			return cur.Name, nil
		}
		parent, ok := set[cur.Inherits]
		if !ok {
			return "", fmt.Errorf("%w: %q (parent of %q)", ErrMissingParent, cur.Inherits, cur.Name)
		}
		cur = parent
	}
}

// LoadDirs scans every *.json file directly under each dir (non-recursive,
// matching bambuadapter.Discover) into one Set keyed by name. Shared by
// cmd/bpm and internal/webui so both build a Set from the same real
// Bambu Studio directories the same way.
func LoadDirs(dirs []string) (Set, error) {
	set := Set{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("resolver: load dirs: read %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			p, err := parser.Load(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			set[p.Name] = p
		}
	}
	return set, nil
}
