package webui

import (
	"fmt"
	"sort"
)

// summaryField is one recognizable setting shown on the profile detail
// page's summary card, Bambu-Studio-style (vendor/type/temps up front)
// instead of a raw JSON dump. Field names confirmed against real resolved
// profile fixtures (see openspec/changes/init-profile-manager/findings.md).
type summaryField struct{ Label, Value string }

func fieldString(fields map[string]any, key string) string {
	v, ok := fields[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

func profileSummary(fields map[string]any) []summaryField {
	var out []summaryField
	add := func(label, key string) {
		if v := fieldString(fields, key); v != "" && v != "[]" {
			out = append(out, summaryField{label, v})
		}
	}
	add("Vendor", "filament_vendor")
	add("Type", "filament_type")
	add("Nozzle temp", "nozzle_temperature")
	add("Nozzle temp (first layer)", "nozzle_temperature_initial_layer")
	add("Bed temp", "hot_plate_temp")
	add("Bed temp (first layer)", "hot_plate_temp_initial_layer")
	return out
}

// profileColor extracts a hex color from default_filament_colour (observed
// shape: a single-element array, e.g. ["#DC0C26"]) for a visual swatch.
func profileColor(fields map[string]any) string {
	v, ok := fields["default_filament_colour"].([]any)
	if !ok || len(v) == 0 {
		return ""
	}
	s, _ := v[0].(string)
	return s
}

// kv is one row of the full, alphabetized settings table — the fallback
// for anything not surfaced on the summary card.
type kv struct{ Key, Value string }

func sortedFields(fields map[string]any) []kv {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, len(keys))
	for i, k := range keys {
		out[i] = kv{Key: k, Value: fmt.Sprint(fields[k])}
	}
	return out
}

// fieldDiff is one differing setting between two resolved profiles/versions.
type fieldDiff struct {
	Key  string
	A, B string
}

// diffFields returns only the fields that differ between a and b (sorted by
// key), plus how many fields were identical and hidden — a compare view is
// most useful showing what changed, not re-listing everything that didn't.
func diffFields(a, b map[string]any) (diffs []fieldDiff, sameCount int) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		av, bv := fmt.Sprint(a[k]), fmt.Sprint(b[k])
		if _, ok := a[k]; !ok {
			av = "(not set)"
		}
		if _, ok := b[k]; !ok {
			bv = "(not set)"
		}
		if av == bv {
			sameCount++
			continue
		}
		diffs = append(diffs, fieldDiff{Key: k, A: av, B: bv})
	}
	return diffs, sameCount
}
