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

	"github.com/syscod3/bambu-profile-manager/internal/domain"
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

// RootAncestorName returns the name of the top of leaf's inherits chain —
// the profile with no further parent. For filament profiles this is the
// material-family template (e.g. "fdm_filament_abs"), independent of any
// printer/nozzle-specific ancestor in between. Used to match "same
// material, different printer" candidates without the caller enumerating
// them by hand.
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
		parent, ok := set[cur.Inherits]
		if !ok {
			return "", fmt.Errorf("%w: %q (parent of %q)", ErrMissingParent, cur.Inherits, cur.Name)
		}
		cur = parent
	}
}
