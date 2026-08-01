package service_test

import (
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/service"
)

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
	got, err := service.TargetProfileFromVersion(resolvedJSON)
	if err != nil {
		t.Fatalf("TargetProfileFromVersion: %v", err)
	}
	if got.Name != "Renamed Target Profile" {
		t.Fatalf("Name = %q, want %q (the source profile's name must never leak in here)", got.Name, "Renamed Target Profile")
	}
}

func TestTargetProfileFromVersionRejectsMissingName(t *testing.T) {
	if _, err := service.TargetProfileFromVersion([]byte(`{"filament_type":"ABS"}`)); err == nil {
		t.Fatal("TargetProfileFromVersion accepted JSON with no name field, want an error")
	}
}
