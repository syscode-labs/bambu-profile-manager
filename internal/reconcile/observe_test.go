package reconcile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
)

// TestParseInfoAgainstRealFixture parses the real .info fixture captured
// from this machine's Bambu Studio library (findings.md's confirmed
// format: user_id, setting_id, base_id, updated_time).
func TestParseInfoAgainstRealFixture(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s", "source", "leaf.info")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fields := reconcile.ParseInfo(b)

	for _, key := range []string{"setting_id", "base_id", "updated_time"} {
		if fields[key] == "" {
			t.Errorf("ParseInfo missing/empty %q, got %+v", key, fields)
		}
	}
	if fields["user_id"] != "REDACTED" {
		t.Errorf("user_id = %q, want the redacted fixture value", fields["user_id"])
	}
}

func TestRewriteDetectorObservesUpdatedTimeBump(t *testing.T) {
	before := reconcile.InfoFields{"updated_time": "100", "setting_id": "PFUS1"}
	after := reconcile.InfoFields{"updated_time": "200", "setting_id": "PFUS1"}
	if !(reconcile.RewriteDetector{}).Observed(before, after) {
		t.Fatal("RewriteDetector did not detect updated_time bump")
	}
}

func TestRewriteDetectorObservesNewSettingID(t *testing.T) {
	before := reconcile.InfoFields{"updated_time": "100", "setting_id": ""}
	after := reconcile.InfoFields{"updated_time": "100", "setting_id": "PFUS1"}
	if !(reconcile.RewriteDetector{}).Observed(before, after) {
		t.Fatal("RewriteDetector did not detect setting_id going from unset to set")
	}
}

func TestRewriteDetectorNoChangeNotObserved(t *testing.T) {
	before := reconcile.InfoFields{"updated_time": "100", "setting_id": "PFUS1"}
	after := reconcile.InfoFields{"updated_time": "100", "setting_id": "PFUS1"}
	if (reconcile.RewriteDetector{}).Observed(before, after) {
		t.Fatal("RewriteDetector reported observed with no change")
	}
}

func TestManualDetectorHonorsExplicitConfirmation(t *testing.T) {
	empty := reconcile.InfoFields{}
	if (reconcile.ManualDetector{Confirmed: false}).Observed(empty, empty) {
		t.Fatal("ManualDetector{Confirmed: false} reported observed")
	}
	if !(reconcile.ManualDetector{Confirmed: true}).Observed(empty, empty) {
		t.Fatal("ManualDetector{Confirmed: true} did not report observed")
	}
}
