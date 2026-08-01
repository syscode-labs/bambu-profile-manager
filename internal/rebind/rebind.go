// Package rebind implements cross-printer/nozzle profile rebinding
// (design.md §13). Finding the "equivalent target parent" is fundamentally
// a naming-convention guess (confirmed against real profiles: Bambu's own
// naming isn't a consistent token substitution — "Bambu ABS @BBL X1C" maps
// to "Bambu ABS @BBL P1S 0.4 nozzle", not "Bambu ABS @BBL P1S"). Rather than
// embed that guess here, callers supply an ordered list of candidate target
// parent names to try; this package only ever picks a candidate that exists
// verbatim in targetSet, or falls back to flattening. It never picks among
// multiple existing candidates itself — design.md §13: "must never silently
// guess when multiple parent mappings are plausible."
package rebind

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/normalize"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

type Strategy string

const (
	// StrategyMapToTargetParent: an equivalent parent was found by name in
	// targetSet; the rebound profile inherits from it directly.
	StrategyMapToTargetParent Strategy = "map_to_target_parent"
	// StrategyFlatten: no candidate target parent exists; the rebound
	// profile is the fully-resolved source, standing alone (inherits "").
	StrategyFlatten Strategy = "flatten"
	// StrategyManualRequired: more than one supplied candidate matched an
	// existing target profile — ambiguous, refuse to guess.
	StrategyManualRequired Strategy = "manual_required"
)

// FieldDiff is one field that differs between the source's effective
// profile and the rebound target's effective profile.
type FieldDiff struct {
	Source any `json:"source,omitempty"`
	Target any `json:"target,omitempty"`
}

// Result is a proposed rebind, not yet published.
type Result struct {
	Strategy        Strategy
	TargetProfile   *domain.RawProfile // ready to add to targetSet / publish
	MatchedCandidate string             // set only for StrategyMapToTargetParent
	Candidates       []string           // set only for StrategyManualRequired: which of the supplied candidates matched
	Diff            map[string]FieldDiff
}

// Rebind produces a candidate rebound profile for leaf (resolved against
// sourceSet) targeting a different printer/nozzle. targetParentCandidates
// is an ordered, caller-supplied list of profile names to look for in
// targetSet as the equivalent parent (design.md §13 "find the equivalent
// target parent where possible").
func Rebind(sourceSet, targetSet resolver.Set, leaf *domain.RawProfile, targetParentCandidates []string) (*Result, error) {
	sourceEffective, _, err := resolver.Resolve(sourceSet, leaf)
	if err != nil {
		return nil, fmt.Errorf("rebind: resolve source: %w", err)
	}

	var matched []string
	for _, c := range targetParentCandidates {
		if _, ok := targetSet[c]; ok {
			matched = append(matched, c)
		}
	}

	var result *Result
	switch len(matched) {
	case 0:
		result, err = flatten(leaf, sourceEffective)
	case 1:
		result, err = mapToParent(targetSet, leaf, matched[0])
	default:
		result = &Result{Strategy: StrategyManualRequired, Candidates: matched}
	}
	if err != nil {
		return nil, err
	}

	if result.Strategy != StrategyManualRequired {
		targetEffective, _, err := resolver.Resolve(mergedSet(targetSet, result.TargetProfile), result.TargetProfile)
		if err != nil {
			return nil, fmt.Errorf("rebind: resolve target: %w", err)
		}
		result.Diff, err = diff(sourceEffective.Fields, targetEffective.Fields)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func mapToParent(targetSet resolver.Set, leaf *domain.RawProfile, parentName string) (*Result, error) {
	target := &domain.RawProfile{
		Name:     leaf.Name,
		Inherits: parentName,
		Fields:   cloneFields(leaf.Fields, parentName),
	}
	return &Result{Strategy: StrategyMapToTargetParent, TargetProfile: target, MatchedCandidate: parentName}, nil
}

func flatten(leaf *domain.RawProfile, sourceEffective *domain.RawProfile) (*Result, error) {
	fields := map[string]any{}
	for k, v := range sourceEffective.Fields {
		fields[k] = v
	}
	fields["name"] = leaf.Name
	delete(fields, "inherits")
	target := &domain.RawProfile{Name: leaf.Name, Inherits: "", Fields: fields}
	return &Result{Strategy: StrategyFlatten, TargetProfile: target}, nil
}

func cloneFields(fields map[string]any, inherits string) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = v
	}
	out["inherits"] = inherits
	return out
}

func mergedSet(base resolver.Set, extra *domain.RawProfile) resolver.Set {
	out := make(resolver.Set, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out[extra.Name] = extra
	return out
}

// diff reports every field whose canonical (volatile-stripped) value
// differs between source and target effective profiles.
func diff(source, target map[string]any) (map[string]FieldDiff, error) {
	sc, err := normalize.Canonical(source)
	if err != nil {
		return nil, fmt.Errorf("rebind: diff: canonicalize source: %w", err)
	}
	tc, err := normalize.Canonical(target)
	if err != nil {
		return nil, fmt.Errorf("rebind: diff: canonicalize target: %w", err)
	}
	var sm, tm map[string]any
	if err := unmarshal(sc, &sm); err != nil {
		return nil, err
	}
	if err := unmarshal(tc, &tm); err != nil {
		return nil, err
	}

	out := map[string]FieldDiff{}
	seen := map[string]bool{}
	for k, sv := range sm {
		seen[k] = true
		tv, ok := tm[k]
		if !ok || !equalJSON(sv, tv) {
			out[k] = FieldDiff{Source: sv, Target: valueOrNil(ok, tv)}
		}
	}
	for k, tv := range tm {
		if seen[k] {
			continue
		}
		out[k] = FieldDiff{Target: tv}
	}
	return out, nil
}

func valueOrNil(ok bool, v any) any {
	if !ok {
		return nil
	}
	return v
}

func unmarshal(b []byte, out *map[string]any) error {
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("rebind: diff: decode canonical json: %w", err)
	}
	return nil
}

func equalJSON(a, b any) bool {
	return reflect.DeepEqual(a, b)
}
