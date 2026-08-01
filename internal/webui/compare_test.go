package webui

import (
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

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

func TestFormatValueUnwrapsIdenticalArrayWithTempUnit(t *testing.T) {
	got := formatValue("nozzle_temperature", []any{"250", "250"})
	if got != "250 °C" {
		t.Fatalf("formatValue = %q, want %q (unwrapped, deduped, unit added)", got, "250 °C")
	}
}

func TestFormatValueJoinsDistinctArrayValues(t *testing.T) {
	got := formatValue("nozzle_temperature", []any{"250", "260"})
	if got != "250 / 260 °C" {
		t.Fatalf("formatValue = %q, want %q (distinct values joined)", got, "250 / 260 °C")
	}
}

func TestFormatValueNoUnitForNonTemperatureField(t *testing.T) {
	got := formatValue("default_filament_colour", []any{"#DC0C26"})
	if got != "#DC0C26" {
		t.Fatalf("formatValue = %q, want %q (no unit, unwrapped)", got, "#DC0C26")
	}
}

func TestFormatValuePlainScalarUnchanged(t *testing.T) {
	if got := formatValue("filament_type", "ABS"); got != "ABS" {
		t.Fatalf("formatValue = %q, want %q", got, "ABS")
	}
}

func TestShortPrinterTokenExceptions(t *testing.T) {
	cases := map[string]string{
		"Bambu Lab X1 Carbon": "X1C",
		"Bambu Lab A1 mini":   "A1M",
		"Bambu Lab H2D Pro":   "H2DP",
		"Bambu Lab P1S":       "P1S", // falls through to the strip-prefix rule
	}
	for model, want := range cases {
		if got := shortPrinterToken(model, ""); got != want {
			t.Errorf("shortPrinterToken(%q, \"\") = %q, want %q", model, got, want)
		}
	}
}

func TestShortPrinterTokenAppendsNozzle(t *testing.T) {
	if got := shortPrinterToken("Bambu Lab P1S", "0.4"); got != "P1S 0.4" {
		t.Fatalf("shortPrinterToken = %q, want %q", got, "P1S 0.4")
	}
}

func TestDiscoverRealPrintersResolvesInheritedModel(t *testing.T) {
	// Mirrors the real shape found in Bambu Studio: a user machine profile
	// (e.g. "... - Obsidian HF") that itself carries no printer_model field,
	// inheriting from a system template that does.
	template := &domain.RawProfile{
		Name: "Bambu Lab X1 Carbon 0.4 nozzle",
		Fields: map[string]any{
			"name": "Bambu Lab X1 Carbon 0.4 nozzle", "printer_model": "Bambu Lab X1 Carbon",
			"nozzle_diameter": []any{"0.4"},
		},
	}
	userProfile := &domain.RawProfile{
		Name: "Bambu Lab X1 Carbon 0.4 nozzle - Obsidian HF",
		Inherits: "Bambu Lab X1 Carbon 0.4 nozzle",
		Fields: map[string]any{"name": "Bambu Lab X1 Carbon 0.4 nozzle - Obsidian HF", "inherits": "Bambu Lab X1 Carbon 0.4 nozzle"},
	}
	set := resolver.Set{template.Name: template, userProfile.Name: userProfile}

	printers := discoverRealPrinters(set)
	var found *printerOption
	for i := range printers {
		if printers[i].Name == userProfile.Name {
			found = &printers[i]
		}
	}
	if found == nil {
		t.Fatalf("discoverRealPrinters did not include %q: %+v", userProfile.Name, printers)
	}
	if found.Token != "X1C 0.4" {
		t.Fatalf("Token = %q, want %q (resolved via inheritance, not name parsing)", found.Token, "X1C 0.4")
	}
}
