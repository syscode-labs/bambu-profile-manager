package webui

import "testing"

func TestProfileSummarySkipsMissingFields(t *testing.T) {
	fields := map[string]any{"filament_vendor": "AmazonBasics", "filament_type": "ABS"}
	got := profileSummary(fields)
	if len(got) != 2 {
		t.Fatalf("profileSummary = %+v, want 2 entries (vendor, type)", got)
	}
}

func TestProfileColorExtractsHex(t *testing.T) {
	fields := map[string]any{"default_filament_colour": []any{"#DC0C26"}}
	if got := profileColor(fields); got != "#DC0C26" {
		t.Fatalf("profileColor = %q, want #DC0C26", got)
	}
}

func TestProfileColorEmptyWhenMissing(t *testing.T) {
	if got := profileColor(map[string]any{}); got != "" {
		t.Fatalf("profileColor = %q, want empty", got)
	}
}

func TestDiffFieldsOnlyReturnsDifferences(t *testing.T) {
	a := map[string]any{"nozzle_temperature": "250", "name": "A", "same": "x"}
	b := map[string]any{"nozzle_temperature": "260", "name": "B", "same": "x"}
	diffs, same := diffFields(a, b)
	if same != 1 {
		t.Fatalf("same = %d, want 1 (the 'same' field)", same)
	}
	if len(diffs) != 2 {
		t.Fatalf("diffs = %+v, want 2 entries", diffs)
	}
}

func TestDiffFieldsHandlesMissingKeyOnOneSide(t *testing.T) {
	a := map[string]any{"only_in_a": "x"}
	b := map[string]any{}
	diffs, _ := diffFields(a, b)
	if len(diffs) != 1 || diffs[0].B != "(not set)" {
		t.Fatalf("diffs = %+v, want one entry with B=(not set)", diffs)
	}
}
