// Package reconcile implements the sync/reconciliation state machine
// (design.md §16). The transition table is the enforcement mechanism for
// decisions.md #6: the app must never claim ROUND_TRIP_VERIFIED (or reach
// ACTIVE) without actually passing through it — Advance rejects any
// transition not in the table, so there is no code path that skips ahead.
package reconcile

import (
	"fmt"
	"time"
)

type State string

const (
	StateDraft             State = "DRAFT"
	StateValidated         State = "VALIDATED"
	StateStaged            State = "STAGED"
	StateInstalledLocally  State = "INSTALLED_LOCALLY"
	StateObservedByStudio  State = "OBSERVED_BY_STUDIO"
	StateSyncObserved      State = "SYNC_OBSERVED"
	StateRoundTripVerified State = "ROUND_TRIP_VERIFIED"
	StateActive            State = "ACTIVE"

	StateRejectedByStudio      State = "REJECTED_BY_STUDIO"
	StateDependencyMissing     State = "DEPENDENCY_MISSING"
	StateIdentityCollision     State = "IDENTITY_COLLISION"
	StateSyncTimeout           State = "SYNC_TIMEOUT"
	StateCloudModified         State = "CLOUD_MODIFIED"
	StateSemanticMismatch      State = "SEMANTIC_MISMATCH"
	StateSliceValidationFailed State = "SLICE_VALIDATION_FAILED"
)

var failureStates = map[State]bool{
	StateRejectedByStudio:      true,
	StateDependencyMissing:     true,
	StateIdentityCollision:     true,
	StateSyncTimeout:           true,
	StateCloudModified:         true,
	StateSemanticMismatch:      true,
	StateSliceValidationFailed: true,
}

func (s State) IsFailure() bool { return failureStates[s] }

// transitions is the only allowed set of state -> next-state moves.
var transitions = map[State][]State{
	StateDraft:             {StateValidated, StateDependencyMissing},
	StateValidated:         {StateStaged, StateIdentityCollision},
	StateStaged:            {StateInstalledLocally},
	StateInstalledLocally:  {StateObservedByStudio, StateRejectedByStudio, StateSyncTimeout},
	StateObservedByStudio:  {StateSyncObserved, StateCloudModified},
	StateSyncObserved:      {StateRoundTripVerified, StateSemanticMismatch, StateCloudModified},
	StateRoundTripVerified: {StateActive, StateSliceValidationFailed},
	// StateActive and every failure state are terminal for this deployment;
	// a mismatch or failure starts a new Deployment revision rather than
	// mutating this one (design.md §16: "use immutable deployment revisions").
}

// Transition is one immutable step in a Deployment's history.
type Transition struct {
	From   State
	To     State
	At     time.Time
	Reason string
}

// Deployment tracks one profile revision's progress through the state
// machine (design.md §7's Deployment + §16).
type Deployment struct {
	ID        string
	ProfileID string
	Revision  int
	State     State
	History   []Transition
}

// New starts a Deployment in DRAFT.
func New(id, profileID string, revision int) *Deployment {
	return &Deployment{ID: id, ProfileID: profileID, Revision: revision, State: StateDraft}
}

// Advance moves the deployment to `to`, recording the transition, or
// returns an error if `to` isn't reachable from the current state.
func (d *Deployment) Advance(to State, reason string) error {
	allowed := transitions[d.State]
	ok := false
	for _, s := range allowed {
		if s == to {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("reconcile: illegal transition %s -> %s (deployment %s rev %d)", d.State, to, d.ID, d.Revision)
	}
	d.History = append(d.History, Transition{From: d.State, To: to, At: time.Now().UTC(), Reason: reason})
	d.State = to
	return nil
}

// IsRoundTripVerified reports whether this deployment has actually reached
// ROUND_TRIP_VERIFIED or ACTIVE — the only states where the app may tell the
// user their profile is confirmed safe (decisions.md #6).
func (d *Deployment) IsRoundTripVerified() bool {
	return d.State == StateRoundTripVerified || d.State == StateActive
}
