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

func TestGroupedFieldsBucketsByCategory(t *testing.T) {
	fields := map[string]any{
		"filament_vendor":    "AmazonBasics", // Basic
		"nozzle_temperature": "250",          // Temperature
		"fan_speed":          "100",          // Cooling
		"random_advanced_x":  "1",            // Advanced
	}
	groups := groupedFields(fields)
	titles := map[string]bool{}
	for _, g := range groups {
		titles[g.Title] = true
	}
	for _, want := range []string{"Basic", "Temperature", "Cooling", "Advanced"} {
		if !titles[want] {
			t.Errorf("groupedFields missing category %q, got groups: %+v", want, groups)
		}
	}
}

func TestPrettyLabelTitleCases(t *testing.T) {
	if got := prettyLabel("nozzle_temperature"); got != "Nozzle Temperature" {
		t.Fatalf("prettyLabel = %q, want %q", got, "Nozzle Temperature")
	}
}

func TestCompareItemsMarksDiffersAcrossThree(t *testing.T) {
	a := map[string]any{"nozzle_temperature": "250", "same": "x"}
	b := map[string]any{"nozzle_temperature": "260", "same": "x"}
	c := map[string]any{"nozzle_temperature": "250", "same": "x"}
	groups := compareItems([]map[string]any{a, b, c})

	var found bool
	for _, g := range groups {
		for _, row := range g.Rows {
			if row.Label == "Nozzle Temperature" {
				found = true
				if !row.Differ {
					t.Fatalf("nozzle_temperature row not marked as differing: %+v", row)
				}
				if len(row.Values) != 3 {
					t.Fatalf("expected 3 values, got %d: %+v", len(row.Values), row.Values)
				}
			}
			if row.Label == "Same" && row.Differ {
				t.Fatalf("'same' row incorrectly marked as differing: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("nozzle_temperature row not found in compare output")
	}
}

func TestCompareItemsMarksNotSetWhenMissing(t *testing.T) {
	a := map[string]any{"only_in_a": "x"}
	b := map[string]any{}
	groups := compareItems([]map[string]any{a, b})
	var found bool
	for _, g := range groups {
		for _, row := range g.Rows {
			if row.Label == "Only In A" {
				found = true
				if row.Values[1] != "(not set)" {
					t.Fatalf("Values[1] = %q, want (not set)", row.Values[1])
				}
			}
		}
	}
	if !found {
		t.Fatal("only_in_a row not found")
	}
}
