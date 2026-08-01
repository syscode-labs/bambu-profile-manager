package reconcile_test

import (
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
)

func TestHappyPathToActiveRequiresRoundTripVerified(t *testing.T) {
	d := reconcile.New("dep-1", "profile-1", 1)

	steps := []reconcile.State{
		reconcile.StateValidated,
		reconcile.StateStaged,
		reconcile.StateInstalledLocally,
		reconcile.StateObservedByStudio,
		reconcile.StateSyncObserved,
		reconcile.StateRoundTripVerified,
		reconcile.StateActive,
	}
	for _, s := range steps {
		if err := d.Advance(s, "test"); err != nil {
			t.Fatalf("Advance(%s): %v", s, err)
		}
	}
	if !d.IsRoundTripVerified() {
		t.Fatal("IsRoundTripVerified() = false after reaching ACTIVE")
	}
	if len(d.History) != len(steps) {
		t.Fatalf("History has %d entries, want %d", len(d.History), len(steps))
	}
}

func TestCannotSkipToActive(t *testing.T) {
	d := reconcile.New("dep-1", "profile-1", 1)
	if err := d.Advance(reconcile.StateActive, "shortcut"); err == nil {
		t.Fatal("Advance(DRAFT -> ACTIVE) succeeded, want rejection (decisions.md #6: never fake verification)")
	}
	if d.State != reconcile.StateDraft {
		t.Fatalf("State = %s after rejected transition, want unchanged DRAFT", d.State)
	}
}

func TestCannotSkipRoundTripVerified(t *testing.T) {
	d := reconcile.New("dep-1", "profile-1", 1)
	for _, s := range []reconcile.State{
		reconcile.StateValidated,
		reconcile.StateStaged,
		reconcile.StateInstalledLocally,
		reconcile.StateObservedByStudio,
		reconcile.StateSyncObserved,
	} {
		if err := d.Advance(s, "test"); err != nil {
			t.Fatalf("Advance(%s): %v", s, err)
		}
	}
	if err := d.Advance(reconcile.StateActive, "shortcut from SYNC_OBSERVED"); err == nil {
		t.Fatal("Advance(SYNC_OBSERVED -> ACTIVE) succeeded, want rejection: must pass through ROUND_TRIP_VERIFIED")
	}
	if d.IsRoundTripVerified() {
		t.Fatal("IsRoundTripVerified() = true without ever reaching ROUND_TRIP_VERIFIED")
	}
}

func TestFailurePathIsReachableAndTerminal(t *testing.T) {
	d := reconcile.New("dep-1", "profile-1", 1)
	if err := d.Advance(reconcile.StateDependencyMissing, "missing parent"); err != nil {
		t.Fatalf("Advance(DEPENDENCY_MISSING): %v", err)
	}
	if !d.State.IsFailure() {
		t.Fatalf("State.IsFailure() = false for %s", d.State)
	}
	if err := d.Advance(reconcile.StateValidated, "retry"); err == nil {
		t.Fatal("Advance out of a failure state succeeded, want rejection (start a new Deployment revision instead)")
	}
}
