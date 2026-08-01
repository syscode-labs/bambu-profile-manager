package resolver

import (
	"path/filepath"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/normalize"
	"github.com/syscod3/bambu-profile-manager/internal/parser"
)

// loadFixtureSet loads every profile under testdata/fixtures/x1c-to-p1s/source
// (real Bambu Studio profiles pulled from this machine's local library, see
// openspec/changes/init-profile-manager/decisions.md #8) into a resolver Set.
func loadFixtureSet(t *testing.T) (Set, *domain.RawProfile) {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s", "source")

	names := []string{
		"leaf.json",
		filepath.Join("system", "Bambu ABS @BBL X1C.json"),
		filepath.Join("system", "Bambu ABS @base.json"),
		filepath.Join("system", "fdm_filament_abs.json"),
		filepath.Join("system", "fdm_filament_common.json"),
	}

	set := Set{}
	var leaf *domain.RawProfile
	for i, n := range names {
		p, err := parser.Load(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("load %s: %v", n, err)
		}
		set[p.Name] = p
		if i == 0 {
			leaf = p
		}
	}
	return set, leaf
}

func TestResolveRealFixtureChain(t *testing.T) {
	set, leaf := loadFixtureSet(t)

	effective, chain, err := Resolve(set, leaf)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	wantChain := []string{
		"fdm_filament_common",
		"fdm_filament_abs",
		"Bambu ABS @base",
		"Bambu ABS @BBL X1C",
		"Syscode - AmazonBasics ABS 0.6",
	}
	if len(chain) != len(wantChain) {
		t.Fatalf("chain length = %d, want %d: got %v", len(chain), len(wantChain), chain)
	}
	for i, name := range wantChain {
		if chain[i] != name {
			t.Errorf("chain[%d] = %q, want %q (full chain: %v)", i, chain[i], name, chain)
		}
	}

	// The leaf never set filament_type explicitly; it must come from an ancestor.
	if _, ok := effective.Fields["filament_type"]; !ok {
		t.Errorf("effective profile missing filament_type, inherited value was dropped")
	}

	// Resolving twice must produce the same semantic hash (determinism).
	h1, err := normalize.Hash(effective.Fields)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	effective2, _, err := Resolve(set, leaf)
	if err != nil {
		t.Fatalf("Resolve (2nd): %v", err)
	}
	h2, err := normalize.Hash(effective2.Fields)
	if err != nil {
		t.Fatalf("Hash (2nd): %v", err)
	}
	if h1 != h2 {
		t.Errorf("semantic hash not stable across repeated resolution: %s != %s", h1, h2)
	}
}

func TestResolveMissingParent(t *testing.T) {
	set := Set{}
	leaf := &domain.RawProfile{Name: "orphan", Inherits: "does-not-exist", Fields: map[string]any{"name": "orphan", "inherits": "does-not-exist"}}
	if _, _, err := Resolve(set, leaf); err == nil {
		t.Fatal("expected error for missing parent, got nil")
	}
}

func TestResolveCircular(t *testing.T) {
	a := &domain.RawProfile{Name: "a", Inherits: "b", Fields: map[string]any{"name": "a", "inherits": "b"}}
	b := &domain.RawProfile{Name: "b", Inherits: "a", Fields: map[string]any{"name": "b", "inherits": "a"}}
	set := Set{"a": a, "b": b}
	if _, _, err := Resolve(set, a); err == nil {
		t.Fatal("expected error for circular inheritance, got nil")
	}
}

func TestRootAncestorNameOnRealFixtureChain(t *testing.T) {
	set, leaf := loadFixtureSet(t)
	root, err := RootAncestorName(set, leaf)
	if err != nil {
		t.Fatalf("RootAncestorName: %v", err)
	}
	if root != "fdm_filament_common" {
		t.Fatalf("root = %q, want %q (the real chain's top: leaf -> ... -> fdm_filament_abs -> fdm_filament_common)", root, "fdm_filament_common")
	}
}

func TestRootAncestorNameStandalone(t *testing.T) {
	leaf := &domain.RawProfile{Name: "standalone", Fields: map[string]any{"name": "standalone"}}
	root, err := RootAncestorName(Set{}, leaf)
	if err != nil {
		t.Fatalf("RootAncestorName: %v", err)
	}
	if root != "standalone" {
		t.Fatalf("root = %q, want %q (no parent -> itself is the root)", root, "standalone")
	}
}
