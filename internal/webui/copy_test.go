package webui_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
	"github.com/syscod3/bambu-profile-manager/internal/webui"
)

func newTestID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// flattenFixturesInto copies every real fixture profile (source chain +
// target-system) into one flat directory, simulating a live Bambu Studio
// user filament directory that also contains the relevant system profiles
// (LoadDirs scans non-recursively, so everything needs to be siblings).
func flattenFixturesInto(t *testing.T, dest string) {
	t.Helper()
	base := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")
	dirs := []string{
		filepath.Join(base, "source"),
		filepath.Join(base, "source", "system"),
		filepath.Join(base, "target-system"),
	}
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, m := range matches {
			b, err := os.ReadFile(m)
			if err != nil {
				t.Fatalf("read %s: %v", m, err)
			}
			if err := os.WriteFile(filepath.Join(dest, filepath.Base(m)), b, 0o644); err != nil {
				t.Fatalf("write %s: %v", filepath.Base(m), err)
			}
		}
	}
}

func newTestServerWithLiveDir(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	liveDir := t.TempDir()
	flattenFixturesInto(t, liveDir)
	backupsDir := t.TempDir()

	svc := &service.Service{
		Repo:       repo,
		Adapter:    &bambuadapter.LocalAdapter{Dir: liveDir, IsStudioRunning: func() (bool, error) { return false, nil }},
		Detector:   reconcile.RewriteDetector{}, // matches cmdServe's production wiring
		NewID:      newTestID,
		BackupsDir: backupsDir,
	}
	srv := &webui.Server{Svc: svc, UserDir: liveDir}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, liveDir
}

func TestCopyFormListsLiveProfiles(t *testing.T) {
	ts, _ := newTestServerWithLiveDir(t)
	resp, err := http.Get(ts.URL + "/copy")
	if err != nil {
		t.Fatalf("GET /copy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Syscode - AmazonBasics ABS 0.6") {
		t.Fatalf("copy form missing the live profile: %s", body)
	}
}

func TestCopyPreviewToPublishToCheckRecognitionEndToEnd(t *testing.T) {
	ts, liveDir := newTestServerWithLiveDir(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Preview: single unambiguous match in this fixture set.
	previewResp, err := http.PostForm(ts.URL+"/copy/preview", map[string][]string{
		"name":          {"Syscode - AmazonBasics ABS 0.6"},
		"printer_token": {"P1S"},
	})
	if err != nil {
		t.Fatalf("POST /copy/preview: %v", err)
	}
	defer previewResp.Body.Close()
	previewBody, _ := io.ReadAll(previewResp.Body)
	if !strings.Contains(string(previewBody), "Bambu ABS @BBL P1S 0.4 nozzle") {
		t.Fatalf("preview missing the matched parent: %s", previewBody)
	}
	if !strings.Contains(string(previewBody), `action="/copy/publish"`) {
		t.Fatalf("preview missing the publish form: %s", previewBody)
	}

	// Publish.
	const newName = "Test Copy @P1S"
	publishResp, err := client.PostForm(ts.URL+"/copy/publish", map[string][]string{
		"name":         {"Syscode - AmazonBasics ABS 0.6"},
		"parent":       {"Bambu ABS @BBL P1S 0.4 nozzle"},
		"confirm_name": {newName},
	})
	if err != nil {
		t.Fatalf("POST /copy/publish: %v", err)
	}
	defer publishResp.Body.Close()
	if publishResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /copy/publish status = %d, want 200", publishResp.StatusCode)
	}
	publishBody, _ := io.ReadAll(publishResp.Body)
	if !strings.Contains(string(publishBody), "<h1>INSTALLED_LOCALLY</h1>") {
		t.Fatalf("publish result's final state is not INSTALLED_LOCALLY (no .info exists yet for a brand-new profile, so recognition can't have happened): %s", publishBody)
	}
	if !strings.Contains(string(publishBody), "Backup taken") {
		t.Fatalf("publish result missing the automatic backup note: %s", publishBody)
	}

	if _, err := os.Stat(filepath.Join(liveDir, newName+".json")); err != nil {
		t.Fatalf("published file missing on disk: %v", err)
	}

	// Extract the deployment ID from the result page's detail link.
	idx := strings.Index(string(publishBody), "/deployments/")
	if idx == -1 {
		t.Fatalf("publish result missing a deployment link: %s", publishBody)
	}
	rest := string(publishBody)[idx+len("/deployments/"):]
	deploymentID := rest[:strings.IndexAny(rest, "\"'")]

	// Deployment detail page should offer the check-recognition action.
	detailResp, err := http.Get(ts.URL + "/deployments/" + deploymentID)
	if err != nil {
		t.Fatalf("GET /deployments/%s: %v", deploymentID, err)
	}
	defer detailResp.Body.Close()
	detailBody, _ := io.ReadAll(detailResp.Body)
	if !strings.Contains(string(detailBody), "Check recognition") {
		t.Fatalf("deployment detail missing check-recognition action: %s", detailBody)
	}

	// Simulate Studio having saved the profile (bumps .info — the confirmed
	// real trigger), then check recognition.
	infoPath := filepath.Join(liveDir, newName+".info")
	if err := os.WriteFile(infoPath, []byte("updated_time = 12345\nsetting_id = PFUStest\n"), 0o644); err != nil {
		t.Fatalf("write .info: %v", err)
	}

	checkResp, err := client.Post(ts.URL+"/deployments/"+deploymentID+"/check", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /deployments/%s/check: %v", deploymentID, err)
	}
	defer checkResp.Body.Close()
	if checkResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST check status = %d, want 303 redirect", checkResp.StatusCode)
	}

	finalResp, err := http.Get(ts.URL + "/deployments/" + deploymentID)
	if err != nil {
		t.Fatalf("GET /deployments/%s (final): %v", deploymentID, err)
	}
	defer finalResp.Body.Close()
	finalBody, _ := io.ReadAll(finalResp.Body)
	if !strings.Contains(string(finalBody), "ACTIVE") {
		t.Fatalf("deployment did not reach ACTIVE after check-recognition: %s", finalBody)
	}
}

func TestBackupsListAndRestoreEndToEnd(t *testing.T) {
	ts, liveDir := newTestServerWithLiveDir(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Publish once to generate a backup.
	if _, err := client.PostForm(ts.URL+"/copy/publish", map[string][]string{
		"name":         {"Syscode - AmazonBasics ABS 0.6"},
		"parent":       {"Bambu ABS @BBL P1S 0.4 nozzle"},
		"confirm_name": {"Backup Test @P1S"},
	}); err != nil {
		t.Fatalf("POST /copy/publish: %v", err)
	}

	listResp, err := http.Get(ts.URL + "/backups")
	if err != nil {
		t.Fatalf("GET /backups: %v", err)
	}
	defer listResp.Body.Close()
	listBody, _ := io.ReadAll(listResp.Body)
	if !strings.Contains(string(listBody), `action="/backups/`) {
		t.Fatalf("backups list missing a restore form: %s", listBody)
	}

	// Extract the snapshot name from the restore form's action.
	idx := strings.Index(string(listBody), `action="/backups/`)
	rest := string(listBody)[idx+len(`action="/backups/`):]
	snapshotName := rest[:strings.Index(rest, "/restore")]

	// Corrupt a pre-existing (untouched-by-publish) file, then restore.
	// "leaf.json" is the on-disk filename for "Syscode - AmazonBasics ABS
	// 0.6" in this fixture set (the profile's internal "name" field differs
	// from its filename — see flattenFixturesInto).
	target := filepath.Join(liveDir, "leaf.json")
	original, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if err := os.WriteFile(target, []byte("corrupted"), 0o644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	restoreResp, err := client.Post(ts.URL+"/backups/"+snapshotName+"/restore", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST restore: %v", err)
	}
	defer restoreResp.Body.Close()
	if restoreResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("restore status = %d, want 303", restoreResp.StatusCode)
	}

	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored content does not match original snapshot")
	}
}

// TestStudioRunningWarningShownAndPublishBlocked proves the UI actually
// warns before publishing and refuses (with a clear callout, not a raw
// error) when Bambu Studio looks like it's running — the gap flagged after
// the web UI first shipped without any such warning.
func TestStudioRunningWarningShownAndPublishBlocked(t *testing.T) {
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	liveDir := t.TempDir()
	flattenFixturesInto(t, liveDir)

	svc := &service.Service{
		Repo:       repo,
		Adapter:    &bambuadapter.LocalAdapter{Dir: liveDir, IsStudioRunning: func() (bool, error) { return true, nil }},
		Detector:   reconcile.RewriteDetector{},
		NewID:      newTestID,
		BackupsDir: t.TempDir(),
	}
	srv := &webui.Server{Svc: svc, UserDir: liveDir}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	formResp, err := http.Get(ts.URL + "/copy")
	if err != nil {
		t.Fatalf("GET /copy: %v", err)
	}
	defer formResp.Body.Close()
	formBody, _ := io.ReadAll(formResp.Body)
	if !strings.Contains(string(formBody), "Bambu Studio appears to be running") {
		t.Fatalf("copy form did not warn that Studio is running: %s", formBody)
	}

	previewResp, err := http.PostForm(ts.URL+"/copy/preview", map[string][]string{
		"name":          {"Syscode - AmazonBasics ABS 0.6"},
		"printer_token": {"P1S"},
	})
	if err != nil {
		t.Fatalf("POST /copy/preview: %v", err)
	}
	defer previewResp.Body.Close()
	previewBody, _ := io.ReadAll(previewResp.Body)
	if !strings.Contains(string(previewBody), "Bambu Studio appears to be running") {
		t.Fatalf("preview page did not warn that Studio is running: %s", previewBody)
	}

	publishResp, err := http.PostForm(ts.URL+"/copy/publish", map[string][]string{
		"name":         {"Syscode - AmazonBasics ABS 0.6"},
		"parent":       {"Bambu ABS @BBL P1S 0.4 nozzle"},
		"confirm_name": {"Blocked @P1S"},
	})
	if err != nil {
		t.Fatalf("POST /copy/publish: %v", err)
	}
	defer publishResp.Body.Close()
	if publishResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("publish while Studio running status = %d, want 422", publishResp.StatusCode)
	}
	publishBody, _ := io.ReadAll(publishResp.Body)
	if !strings.Contains(string(publishBody), "Bambu Studio is running") {
		t.Fatalf("blocked-publish page missing the clear callout: %s", publishBody)
	}

	if _, err := os.Stat(filepath.Join(liveDir, "Blocked @P1S.json")); err == nil {
		t.Fatal("file was published even though Studio was reported running")
	}
}
