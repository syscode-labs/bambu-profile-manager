package rebind

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

// FindCandidateParents searches targetSet for profiles plausible as leaf's
// rebind target: same material family as leaf (their inherits chains reach
// the same root ancestor — e.g. both eventually inherit "fdm_filament_abs")
// and whose name mentions printerToken as a whole word (e.g. "P1S", so it
// doesn't also match "P1P"). This exists so a caller doesn't have to type
// out target-parent guesses by hand (see rebind.go's package doc on why
// Rebind itself never guesses): Rebind still only accepts a candidate list,
// this just computes one automatically. Multiple matches are returned as-is
// — callers must treat len > 1 as ambiguous and ask the user, never pick
// one themselves (design.md §13, decisions.md #3's spirit extended here).
func FindCandidateParents(sourceSet, targetSet resolver.Set, leaf *domain.RawProfile, printerToken string) ([]string, error) {
	rootName, err := resolver.RootAncestorName(sourceSet, leaf)
	if err != nil {
		return nil, fmt.Errorf("rebind: find candidates: source root ancestor: %w", err)
	}

	tokenRe, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(printerToken) + `\b`)
	if err != nil {
		return nil, fmt.Errorf("rebind: find candidates: invalid printer token %q: %w", printerToken, err)
	}

	var candidates []string
	for name, p := range targetSet {
		if name == leaf.Name {
			continue
		}
		if !tokenRe.MatchString(name) {
			continue
		}
		candidateRoot, err := resolver.RootAncestorName(targetSet, p)
		if err != nil {
			continue // broken chain in the target set — not a usable candidate
		}
		if candidateRoot != rootName {
			continue
		}
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)
	return candidates, nil
}
