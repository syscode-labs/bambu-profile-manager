package main

import "testing"

// TestTargetProfileFromVersionUsesStoredNameNotProfileRow guards against a
// real bug found running check-recognition against a live Bambu Studio
// directory: it must derive the published filename from the stored
// version's own "name" field (the rebound/target name), never from
// whatever the caller happens to have lying around under a different name
// (e.g. the source Profile row's name). Getting this wrong means checking
// a different file's .info entirely — silently producing a false-positive
// ACTIVE state.
func TestTargetProfileFromVersionUsesStoredNameNotProfileRow(t *testing.T) {
	resolvedJSON := []byte(`{"name":"Renamed Target Profile","filament_type":"ABS"}`)
	got, err := targetProfileFromVersion(resolvedJSON)
	if err != nil {
		t.Fatalf("targetProfileFromVersion: %v", err)
	}
	if got.Name != "Renamed Target Profile" {
		t.Fatalf("Name = %q, want %q (the source profile's name must never leak in here)", got.Name, "Renamed Target Profile")
	}
}

func TestTargetProfileFromVersionRejectsMissingName(t *testing.T) {
	if _, err := targetProfileFromVersion([]byte(`{"filament_type":"ABS"}`)); err == nil {
		t.Fatal("targetProfileFromVersion accepted JSON with no name field, want an error")
	}
}
