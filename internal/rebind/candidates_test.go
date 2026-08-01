package rebind_test

import (
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/rebind"
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
