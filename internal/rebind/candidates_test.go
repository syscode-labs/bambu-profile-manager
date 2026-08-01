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
	leaf := &domain.RawProfile{Name: "My Custom @BBL X1C", Inherits: "0.20mm Standard @BBL X1C", Fields: map[string]any{
		"name": "My Custom @BBL X1C", "inherits": "0.20mm Standard @BBL X1C", "from": "User", "wall_loops": "4",
	}}
	set := resolver.Set{}
	for _, p := range []*domain.RawProfile{common, singleCommon, layerHeight, x1c, a1, leaf} {
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
