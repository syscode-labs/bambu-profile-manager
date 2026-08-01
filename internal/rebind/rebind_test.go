package rebind_test

import (
	"path/filepath"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/parser"
	"github.com/syscod3/bambu-profile-manager/internal/rebind"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

// loadSet loads every *.json directly under dir (non-recursive) into a
// resolver.Set, keyed by each profile's name.
func loadSet(t *testing.T, dir string) resolver.Set {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	set := resolver.Set{}
	for _, m := range matches {
		p, err := parser.Load(m)
		if err != nil {
			t.Fatalf("load %s: %v", m, err)
		}
		set[p.Name] = p
	}
	return set
}

func fixtureSets(t *testing.T) (sourceSet, targetSet resolver.Set) {
	t.Helper()
	base := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")

	sourceSet = resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(base, "source")) {
		sourceSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(base, "source", "system")) {
		sourceSet[k] = v
	}

	targetSet = resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(base, "target-system")) {
		targetSet[k] = v
	}
	// The rest of the chain (Bambu ABS @base, fdm_filament_abs,
	// fdm_filament_common) is printer-independent and shared with source.
	for k, v := range loadSet(t, filepath.Join(base, "source", "system")) {
		if _, ok := targetSet[k]; !ok {
			targetSet[k] = v
		}
	}

	return sourceSet, targetSet
}

func TestRebindMapsToRealTargetParent(t *testing.T) {
	sourceSet, targetSet := fixtureSets(t)
	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]
	if leaf == nil {
		t.Fatal("fixture leaf not found")
	}

	// Real Bambu naming isn't a simple token swap (design.md comment in
	// rebind.go): "Bambu ABS @BBL X1C" -> try the naive swap first, then
	// the real convention with a nozzle suffix.
	candidates := []string{
		"Bambu ABS @BBL P1S",
		"Bambu ABS @BBL P1S 0.4 nozzle",
	}

	result, err := rebind.Rebind(sourceSet, targetSet, leaf, candidates)
	if err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	if result.Strategy != rebind.StrategyMapToTargetParent {
		t.Fatalf("Strategy = %v, want StrategyMapToTargetParent (candidates: %v)", result.Strategy, result.Candidates)
	}
	if result.MatchedCandidate != "Bambu ABS @BBL P1S 0.4 nozzle" {
		t.Fatalf("MatchedCandidate = %q, want the real (non-naive) profile name", result.MatchedCandidate)
	}
	if result.TargetProfile.Inherits != result.MatchedCandidate {
		t.Fatalf("TargetProfile.Inherits = %q, want %q", result.TargetProfile.Inherits, result.MatchedCandidate)
	}
}

func TestRebindFlattensWhenNoCandidateExists(t *testing.T) {
	sourceSet, targetSet := fixtureSets(t)
	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]

	result, err := rebind.Rebind(sourceSet, targetSet, leaf, []string{"Nonexistent Parent Name"})
	if err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	if result.Strategy != rebind.StrategyFlatten {
		t.Fatalf("Strategy = %v, want StrategyFlatten", result.Strategy)
	}
	if result.TargetProfile.Inherits != "" {
		t.Fatalf("flattened profile still inherits from %q, want standalone", result.TargetProfile.Inherits)
	}
	if _, ok := result.TargetProfile.Fields["filament_type"]; !ok {
		t.Fatal("flattened profile lost an inherited field (filament_type)")
	}
}

func TestRebindRefusesToGuessAmongMultipleCandidates(t *testing.T) {
	sourceSet, targetSet := fixtureSets(t)
	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]

	// Both candidates resolve to real profiles in targetSet (the base
	// parent + the actual P1S parent) — an intentionally ambiguous case.
	candidates := []string{"Bambu ABS @base", "Bambu ABS @BBL P1S 0.4 nozzle"}

	result, err := rebind.Rebind(sourceSet, targetSet, leaf, candidates)
	if err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	if result.Strategy != rebind.StrategyManualRequired {
		t.Fatalf("Strategy = %v, want StrategyManualRequired when multiple candidates match", result.Strategy)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("Candidates = %v, want both matches surfaced for manual choice", result.Candidates)
	}
}
