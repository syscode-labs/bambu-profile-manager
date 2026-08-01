package rebind_test

import (
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/rebind"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

func TestFindCandidateParentsSingleRealMatch(t *testing.T) {
	sourceSet, targetSet := fixtureSets(t)
	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]

	got, err := rebind.FindCandidateParents(sourceSet, targetSet, leaf, "P1S")
	if err != nil {
		t.Fatalf("FindCandidateParents: %v", err)
	}
	want := []string{"Bambu ABS @BBL P1S 0.4 nozzle"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("FindCandidateParents = %v, want %v", got, want)
	}
}

func TestFindCandidateParentsNoMatchForWrongMaterial(t *testing.T) {
	sourceSet, targetSet := fixtureSets(t)
	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]

	// "P1P" isn't present in the fixture set at all, but this also proves
	// the token match doesn't fire on the substring "P1S" contains ("P1").
	got, err := rebind.FindCandidateParents(sourceSet, targetSet, leaf, "P1P")
	if err != nil {
		t.Fatalf("FindCandidateParents: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("FindCandidateParents = %v, want none", got)
	}
}

func TestFindCandidateParentsAmbiguousReturnsAllMatches(t *testing.T) {
	sourceSet, targetSet := fixtureSets(t)
	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]

	// Inject a second plausible P1S ABS parent sharing the same root
	// ancestor as the real one, to prove ambiguity surfaces both rather
	// than silently picking one.
	real := targetSet["Bambu ABS @BBL P1S 0.4 nozzle"]
	decoy := &domain.RawProfile{
		Name:     "Bambu ABS HF @BBL P1S 0.4 nozzle",
		Inherits: real.Inherits, // same root ancestor as the real candidate
		Fields:   map[string]any{"name": "Bambu ABS HF @BBL P1S 0.4 nozzle", "inherits": real.Inherits},
	}
	targetSet[decoy.Name] = decoy

	got, err := rebind.FindCandidateParents(sourceSet, targetSet, leaf, "P1S")
	if err != nil {
		t.Fatalf("FindCandidateParents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("FindCandidateParents = %v, want 2 ambiguous matches", got)
	}
}

// processFixtureSet mirrors the real shape found in Bambu Studio's process
// (print) catalog: unlike filament, there is no per-exact-printer system
// leaf — printers share one leaf via compatible_printers (confirmed: the
// real system catalog has no "@BBL P1S" process profile at all; P1S reuses
// X1C's, which lists both).
func processFixtureSet(t *testing.T) resolver.Set {
	t.Helper()
	common := &domain.RawProfile{Name: "fdm_process_common", Fields: map[string]any{"name": "fdm_process_common"}}
	singleCommon := &domain.RawProfile{Name: "fdm_process_single_common", Inherits: "fdm_process_common",
		Fields: map[string]any{"name": "fdm_process_single_common", "inherits": "fdm_process_common"}}
	layerHeight := &domain.RawProfile{Name: "fdm_process_single_0.20", Inherits: "fdm_process_single_common",
		Fields: map[string]any{"name": "fdm_process_single_0.20", "inherits": "fdm_process_single_common"}}
	x1c := &domain.RawProfile{Name: "0.20mm Standard @BBL X1C", Inherits: "fdm_process_single_0.20", Fields: map[string]any{
		"name": "0.20mm Standard @BBL X1C", "inherits": "fdm_process_single_0.20", "from": "system",
		"compatible_printers": []any{"Bambu Lab X1 Carbon 0.4 nozzle", "Bambu Lab P1S 0.4 nozzle", "Bambu Lab X1E 0.4 nozzle"},
	}}
	a1 := &domain.RawProfile{Name: "0.20mm Standard @BBL A1", Inherits: "fdm_process_single_0.20", Fields: map[string]any{
		"name": "0.20mm Standard @BBL A1", "inherits": "fdm_process_single_0.20", "from": "system",
		"compatible_printers": []any{"Bambu Lab A1 0.4 nozzle"},
	}}
	// A different nozzle diameter gets its own root node in the real catalog
	// (fdm_process_single_0.10_nozzle_0.2, sibling of fdm_process_single_0.20,
	// both under fdm_process_single_common) — mirrors the real bug: a naive
	// exact-root-name match would treat this as a different family and never
	// offer it as a candidate for a same-family, different-diameter copy.
	nozzle02 := &domain.RawProfile{Name: "fdm_process_single_0.10_nozzle_0.2", Inherits: "fdm_process_single_common",
		Fields: map[string]any{"name": "fdm_process_single_0.10_nozzle_0.2", "inherits": "fdm_process_single_common"}}
	x1c02 := &domain.RawProfile{Name: "0.10mm Standard @BBL X1C 0.2 nozzle", Inherits: "fdm_process_single_0.10_nozzle_0.2", Fields: map[string]any{
		"name": "0.10mm Standard @BBL X1C 0.2 nozzle", "inherits": "fdm_process_single_0.10_nozzle_0.2", "from": "system",
		"compatible_printers": []any{"Bambu Lab X1 Carbon 0.2 nozzle", "Bambu Lab P1S 0.2 nozzle"},
	}}
	leaf := &domain.RawProfile{Name: "My Custom @BBL X1C", Inherits: "0.20mm Standard @BBL X1C", Fields: map[string]any{
		"name": "My Custom @BBL X1C", "inherits": "0.20mm Standard @BBL X1C", "from": "User", "wall_loops": "4",
	}}
	set := resolver.Set{}
	for _, p := range []*domain.RawProfile{common, singleCommon, layerHeight, x1c, a1, nozzle02, x1c02, leaf} {
		set[p.Name] = p
	}
	return set
}

func TestIsAlreadyCompatibleInheritsFromParent(t *testing.T) {
	set := processFixtureSet(t)
	leaf := set["My Custom @BBL X1C"]

	// The leaf never declares compatible_printers itself — it's inherited
	// from its "@BBL X1C" parent, which already lists P1S.
	ok, err := rebind.IsAlreadyCompatible(set, leaf, "Bambu Lab P1S 0.4 nozzle")
	if err != nil {
		t.Fatalf("IsAlreadyCompatible: %v", err)
	}
	if !ok {
		t.Fatal("IsAlreadyCompatible = false, want true (P1S is in the inherited compatible_printers list)")
	}

	ok, err = rebind.IsAlreadyCompatible(set, leaf, "Bambu Lab A1 0.4 nozzle")
	if err != nil {
		t.Fatalf("IsAlreadyCompatible: %v", err)
	}
	if ok {
		t.Fatal("IsAlreadyCompatible = true, want false (A1 is not in X1C's compatible_printers)")
	}
}

func TestFindCandidateParentsByCompatiblePrintersMatchesRealCatalogShape(t *testing.T) {
	set := processFixtureSet(t)
	leaf := set["My Custom @BBL X1C"]

	got, err := rebind.FindCandidateParentsByCompatiblePrinters(set, set, leaf, "Bambu Lab A1 0.4 nozzle")
	if err != nil {
		t.Fatalf("FindCandidateParentsByCompatiblePrinters: %v", err)
	}
	want := []string{"0.20mm Standard @BBL A1"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("FindCandidateParentsByCompatiblePrinters = %v, want %v", got, want)
	}
}

// TestFindCandidateParentsByCompatiblePrintersMatchesAcrossNozzleDiameters
// is the real-world case that was live-broken: copying a process profile to
// a different nozzle diameter of the same printer (X1C 0.6 -> X1C 0.2
// nozzle) must find the real, catalog-verified parent for that diameter,
// not fall through to the unfiltered same-family fallback (which is how a
// wrong-material profile got picked by hand instead, live).
func TestFindCandidateParentsByCompatiblePrintersMatchesAcrossNozzleDiameters(t *testing.T) {
	set := processFixtureSet(t)
	leaf := set["My Custom @BBL X1C"]

	got, err := rebind.FindCandidateParentsByCompatiblePrinters(set, set, leaf, "Bambu Lab X1 Carbon 0.2 nozzle")
	if err != nil {
		t.Fatalf("FindCandidateParentsByCompatiblePrinters: %v", err)
	}
	want := []string{"0.10mm Standard @BBL X1C 0.2 nozzle"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("FindCandidateParentsByCompatiblePrinters = %v, want %v", got, want)
	}
}

func TestFindCandidateParentsByCompatiblePrintersNoMatch(t *testing.T) {
	set := processFixtureSet(t)
	leaf := set["My Custom @BBL X1C"]

	got, err := rebind.FindCandidateParentsByCompatiblePrinters(set, set, leaf, "Bambu Lab H2S 0.4 nozzle")
	if err != nil {
		t.Fatalf("FindCandidateParentsByCompatiblePrinters: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("FindCandidateParentsByCompatiblePrinters = %v, want none", got)
	}
}

func TestFindCandidateParentsBySameFamilyExcludesAbstractTemplates(t *testing.T) {
	set := processFixtureSet(t)
	// Mark the shared root abstract, like Bambu's real fdm_process_single_0.20
	// does (instantiation:"false") — real bug found while building this: the
	// root ancestor of a leaf's own chain trivially satisfies "same root as
	// itself", so without this filter it would show up as a pickable parent
	// even though it's an internal-only template no one selects directly.
	set["fdm_process_single_0.20"].Fields["instantiation"] = "false"
	leaf := set["My Custom @BBL X1C"]

	got, err := rebind.FindCandidateParentsBySameFamily(set, set, leaf)
	if err != nil {
		t.Fatalf("FindCandidateParentsBySameFamily: %v", err)
	}
	want := []string{"0.20mm Standard @BBL A1", "0.20mm Standard @BBL X1C"}
	if len(got) != len(want) {
		t.Fatalf("FindCandidateParentsBySameFamily = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("FindCandidateParentsBySameFamily = %v, want %v", got, want)
		}
	}
	for _, name := range got {
		if name == "fdm_process_single_0.20" {
			t.Fatal("FindCandidateParentsBySameFamily included the abstract root template")
		}
	}
}

func TestAddCompatiblePrinterUnionsWithParentsResolvedList(t *testing.T) {
	set := processFixtureSet(t)
	target := &domain.RawProfile{Name: "New Copy", Fields: map[string]any{"name": "New Copy"}}

	if err := rebind.AddCompatiblePrinter(set, target, "0.20mm Standard @BBL A1", "Bambu Lab H2S 0.4 nozzle"); err != nil {
		t.Fatalf("AddCompatiblePrinter: %v", err)
	}
	got, _ := target.Fields["compatible_printers"].([]any)
	want := []any{"Bambu Lab A1 0.4 nozzle", "Bambu Lab H2S 0.4 nozzle"}
	if len(got) != len(want) {
		t.Fatalf("compatible_printers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("compatible_printers = %v, want %v", got, want)
		}
	}
}

func TestAddCompatiblePrinterNoOpWhenAlreadyListed(t *testing.T) {
	set := processFixtureSet(t)
	target := &domain.RawProfile{Name: "New Copy", Fields: map[string]any{"name": "New Copy"}}

	// X1C's parent already lists P1S — adding it again must not duplicate.
	if err := rebind.AddCompatiblePrinter(set, target, "0.20mm Standard @BBL X1C", "Bambu Lab P1S 0.4 nozzle"); err != nil {
		t.Fatalf("AddCompatiblePrinter: %v", err)
	}
	got, _ := target.Fields["compatible_printers"].([]any)
	count := 0
	for _, v := range got {
		if v == "Bambu Lab P1S 0.4 nozzle" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("compatible_printers = %v, want exactly one P1S entry, got %d", got, count)
	}
}

func TestFindCandidateParentsFeedsRebindDirectly(t *testing.T) {
	sourceSet, targetSet := fixtureSets(t)
	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]

	candidates, err := rebind.FindCandidateParents(sourceSet, targetSet, leaf, "P1S")
	if err != nil {
		t.Fatalf("FindCandidateParents: %v", err)
	}
	result, err := rebind.Rebind(sourceSet, targetSet, leaf, candidates)
	if err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	if result.Strategy != rebind.StrategyMapToTargetParent {
		t.Fatalf("Strategy = %v, want StrategyMapToTargetParent", result.Strategy)
	}
	if result.MatchedCandidate != "Bambu ABS @BBL P1S 0.4 nozzle" {
		t.Fatalf("MatchedCandidate = %q, want the real P1S parent", result.MatchedCandidate)
	}
}
