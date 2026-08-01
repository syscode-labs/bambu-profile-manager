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

// processVariantSuffix strips a process root's per-diameter/layer-height
// variant (e.g. "fdm_process_single_0.20" or
// "fdm_process_single_0.10_nozzle_0.2") down to its "single"/"dual" family
// ("fdm_process_single") — real bug found live: RootAncestorName's
// granularity is correct for filament, where each material sits exactly one
// hop below "fdm_process_common"-shaped commons (fdm_filament_pla is the
// root, full stop). Process profiles have an *extra* hop the same generic
// walk can't see — every nozzle diameter/layer-height combo gets its own
// root node (fdm_process_single_0.20, fdm_process_single_0.10_nozzle_0.2,
// ...) that all in turn inherit "fdm_process_single_common". Comparing
// RootAncestorName's result directly meant copying a process profile to a
// different nozzle diameter (the very case this feature exists for) could
// never find its real, compatible-printers-verified parent — it always fell
// through to the broad, unfiltered same-family fallback instead, which is
// how a same-family-but-wrong-material profile (e.g. a TPU leaf) could get
// picked instead of the correct standard one. Confirmed against the real
// system catalog: every process root name is "fdm_process_single_..." or
// "fdm_process_dual_...", so stripping the trailing variant leaves exactly
// those two families — filament root names never match the prefix, so this
// is a no-op for them (falls back to plain equality).
var processVariantSuffix = regexp.MustCompile(`^(fdm_process_(?:single|dual))_.+$`)

func processRootFamily(rootName string) string {
	if m := processVariantSuffix.FindStringSubmatch(rootName); m != nil {
		return m[1]
	}
	return rootName
}

// FindCandidateParentsByCompatiblePrinters searches targetSet for profiles
// under the same family root as leaf (see resolver.RootAncestorName) whose
// *resolved* compatible_printers field lists targetPrinterName exactly.
//
// This exists because process/print profiles don't follow filament's
// one-system-leaf-per-exact-printer-model convention: Bambu shares one leaf
// across a whole printer family via compatible_printers instead (confirmed
// against the real system catalog — there is no "@BBL P1S" process profile
// anywhere; P1S reuses X1C's leaf, which lists both in compatible_printers).
// FindCandidateParents' name-token matching would report zero candidates in
// that case even though a usable parent exists — this is the authoritative
// mechanism Bambu Studio itself uses to decide what a printer can select
// (compatible_printers is a real field on filament profiles too, but
// filament's naming convention happens to be 1:1 per printer so the simpler
// token match already gives the right answer there).
func FindCandidateParentsByCompatiblePrinters(sourceSet, targetSet resolver.Set, leaf *domain.RawProfile, targetPrinterName string) ([]string, error) {
	rootName, err := resolver.RootAncestorName(sourceSet, leaf)
	if err != nil {
		return nil, fmt.Errorf("rebind: find candidates by compatible printers: source root ancestor: %w", err)
	}
	rootFamily := processRootFamily(rootName)

	var candidates []string
	for name, p := range targetSet {
		if name == leaf.Name {
			continue
		}
		candidateRoot, err := resolver.RootAncestorName(targetSet, p)
		if err != nil || processRootFamily(candidateRoot) != rootFamily {
			continue // broken chain, or a different family — not a usable candidate
		}
		effective, _, err := resolver.Resolve(targetSet, p)
		if err != nil {
			continue
		}
		if compatiblePrintersInclude(effective.Fields, targetPrinterName) {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

// IsAlreadyCompatible reports whether leaf, resolved against sourceSet,
// already lists targetPrinterName in its effective compatible_printers —
// meaning there's nothing to copy at all, the profile already applies to
// that printer as-is.
func IsAlreadyCompatible(sourceSet resolver.Set, leaf *domain.RawProfile, targetPrinterName string) (bool, error) {
	effective, _, err := resolver.Resolve(sourceSet, leaf)
	if err != nil {
		return false, fmt.Errorf("rebind: is already compatible: resolve: %w", err)
	}
	return compatiblePrintersInclude(effective.Fields, targetPrinterName), nil
}

// FindCandidateParentsBySameFamily searches targetSet for every profile
// under the same family root as leaf (see resolver.RootAncestorName),
// regardless of compatible_printers. Used as a fallback when nothing is
// verified compatible with the target printer: rather than refuse outright,
// the caller can offer these as real, existing base profiles the user picks
// from — none of them are confirmed to already work on that printer, but
// AddCompatiblePrinter can patch the chosen one so it does (see its doc
// comment). Never picks one itself — same "list candidates, caller decides"
// contract as FindCandidateParents/FindCandidateParentsByCompatiblePrinters.
func FindCandidateParentsBySameFamily(sourceSet, targetSet resolver.Set, leaf *domain.RawProfile) ([]string, error) {
	rootName, err := resolver.RootAncestorName(sourceSet, leaf)
	if err != nil {
		return nil, fmt.Errorf("rebind: find candidates by family: source root ancestor: %w", err)
	}

	var candidates []string
	for name, p := range targetSet {
		if name == leaf.Name {
			continue
		}
		// Abstract shared templates (fdm_filament_abs, fdm_process_common,
		// ...) trivially satisfy "same root ancestor as themselves" — real
		// selectable profiles mark instantiation:"true"; Bambu's own
		// internal-only templates mark it "false" (confirmed against the
		// real system catalog). Absent entirely (typical for a plain user
		// leaf) still counts as selectable.
		if inst, ok := p.Fields["instantiation"].(string); ok && inst == "false" {
			continue
		}
		candidateRoot, err := resolver.RootAncestorName(targetSet, p)
		if err != nil || candidateRoot != rootName {
			continue
		}
		candidates = append(candidates, name)
	}
	sort.Strings(candidates)
	return candidates, nil
}

// AddCompatiblePrinter patches target's own compatible_printers field so it
// explicitly includes printerName, unioned with parentName's *resolved*
// (inherited) compatible_printers — used when the user picks a same-family
// parent from FindCandidateParentsBySameFamily that Bambu's own catalog
// does not yet list as compatible with printerName, and explicitly confirms
// they want it made compatible rather than just guessing blind. parentName
// is looked up in targetSet (it must be the same parent Rebind mapped
// target onto). Overriding on target itself (rather than editing the
// shared parent) keeps every other profile inheriting that parent
// unaffected.
func AddCompatiblePrinter(targetSet resolver.Set, target *domain.RawProfile, parentName, printerName string) error {
	parent, ok := targetSet[parentName]
	if !ok {
		return fmt.Errorf("rebind: add compatible printer: parent %q not found", parentName)
	}
	effective, _, err := resolver.Resolve(targetSet, parent)
	if err != nil {
		return fmt.Errorf("rebind: add compatible printer: resolve parent: %w", err)
	}

	existing, _ := effective.Fields["compatible_printers"].([]any)
	seen := make(map[string]bool, len(existing)+1)
	merged := make([]any, 0, len(existing)+1)
	for _, v := range existing {
		if str, ok := v.(string); ok && !seen[str] {
			seen[str] = true
			merged = append(merged, str)
		}
	}
	if !seen[printerName] {
		merged = append(merged, printerName)
	}
	target.Fields["compatible_printers"] = merged
	return nil
}

func compatiblePrintersInclude(fields map[string]any, targetPrinterName string) bool {
	raw, ok := fields["compatible_printers"]
	if !ok {
		return false
	}
	arr, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, v := range arr {
		if s, ok := v.(string); ok && s == targetPrinterName {
			return true
		}
	}
	return false
}
