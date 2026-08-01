package webui_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/domain"
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

// flattenProcessFixturesInto copies the process (print) fixture set into one
// flat directory the same way flattenFixturesInto does for filament — a
// deliberately different family shape (see testdata/fixtures/x1c-to-p1s/process):
// no per-exact-printer system leaf, printers share one leaf via
// compatible_printers instead.
func flattenProcessFixturesInto(t *testing.T, dest string) {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s", "process")
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

// newTestServerWithProcessDir builds on newTestServerWithLiveDir, adding a
// second, wholly separate Service/Repo/db for process (print) profiles —
// mirrors cmdServe's production wiring (webui.Server's doc comment on
// ProcessSvc: process and filament deployments must never share IDs).
func newTestServerWithProcessDir(t *testing.T) (ts *httptest.Server, filamentDir, processDir string, srv *webui.Server) {
	t.Helper()
	ts, filamentDir, srv = newTestServerWithLiveDir(t)

	processRepo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { processRepo.Close() })

	processDir = t.TempDir()
	flattenProcessFixturesInto(t, processDir)

	srv.ProcessSvc = &service.Service{
		Repo:       processRepo,
		Adapter:    &bambuadapter.LocalAdapter{Dir: processDir, IsStudioRunning: func() (bool, error) { return false, nil }},
		Detector:   reconcile.RewriteDetector{},
		NewID:      newTestID,
		BackupsDir: t.TempDir(),
	}
	srv.ProcessUserDir = processDir
	return ts, filamentDir, processDir, srv
}

func newTestServerWithLiveDir(t *testing.T) (*httptest.Server, string, *webui.Server) {
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
	return ts, liveDir, srv
}

func TestCopyFormListsLiveProfiles(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t)
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
	ts, liveDir, _ := newTestServerWithLiveDir(t)
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
	if !strings.Contains(string(publishBody), `title="INSTALLED_LOCALLY"`) || !strings.Contains(string(publishBody), ">AWAITING SAVE IN STUDIO<") {
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
	if !strings.Contains(string(detailBody), "can't be automated") {
		t.Fatalf("deployment detail missing the Save-in-Studio explanation: %s", detailBody)
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
	if !strings.Contains(string(finalBody), `title="ACTIVE"`) || !strings.Contains(string(finalBody), ">VERIFIED<") {
		t.Fatalf("deployment did not reach ACTIVE after check-recognition: %s", finalBody)
	}
}

// TestBackgroundPollerDetectsRecognitionWithoutManualCheck covers the
// "why do I have to click Check too" complaint: once Studio has saved the
// profile (the one step that genuinely can't be automated — see decisions.md
// #5), the deployment should reach ACTIVE on its own via PollRecognition,
// with no POST to /deployments/{id}/check at all.
func TestBackgroundPollerDetectsRecognitionWithoutManualCheck(t *testing.T) {
	ts, liveDir, srv := newTestServerWithLiveDir(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	const newName = "Poller Test @P1S"
	publishResp, err := client.PostForm(ts.URL+"/copy/publish", map[string][]string{
		"name":         {"Syscode - AmazonBasics ABS 0.6"},
		"parent":       {"Bambu ABS @BBL P1S 0.4 nozzle"},
		"confirm_name": {newName},
	})
	if err != nil {
		t.Fatalf("POST /copy/publish: %v", err)
	}
	defer publishResp.Body.Close()
	publishBody, _ := io.ReadAll(publishResp.Body)
	idx := strings.Index(string(publishBody), "/deployments/")
	if idx == -1 {
		t.Fatalf("publish result missing a deployment link: %s", publishBody)
	}
	rest := string(publishBody)[idx+len("/deployments/"):]
	deploymentID := rest[:strings.IndexAny(rest, "\"'")]

	// Simulate Studio saving the profile (bumps .info) — no manual check call.
	infoPath := filepath.Join(liveDir, newName+".info")
	if err := os.WriteFile(infoPath, []byte("updated_time = 12345\nsetting_id = PFUStest\n"), 0o644); err != nil {
		t.Fatalf("write .info: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.PollRecognition(ctx, 20*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(ts.URL + "/deployments/" + deploymentID)
		if err != nil {
			t.Fatalf("GET /deployments/%s: %v", deploymentID, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), ">VERIFIED<") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment did not reach ACTIVE via background polling within 2s: %s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCopyProcessAlreadyCompatibleNeedsNoCopy covers the real-world common
// case for process profiles: the user's leaf inherits from "0.20mm Standard
// @BBL X1C", whose compatible_printers already includes P1S (confirmed
// against the real system catalog — there is no separate "@BBL P1S" process
// profile at all). Nothing should be published; the preview must say so.
func TestCopyProcessAlreadyCompatibleNeedsNoCopy(t *testing.T) {
	ts, _, _, _ := newTestServerWithProcessDir(t)

	resp, err := http.PostForm(ts.URL+"/copy/process/preview", map[string][]string{
		"name":          {"Syscode - 0.20mm Standard @BBL X1C"},
		"printer_token": {"P1S 0.4|Bambu Lab P1S 0.4 nozzle"},
	})
	if err != nil {
		t.Fatalf("POST /copy/process/preview: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "Nothing to copy") {
		t.Fatalf("preview did not report already-compatible: %s", bodyStr)
	}
	if strings.Contains(bodyStr, `action="/copy/process/publish"`) {
		t.Fatalf("preview offered a publish form when nothing should be copied: %s", bodyStr)
	}
}

// TestCopyProcessPreviewToPublishToCheckRecognitionEndToEnd covers the case
// where the target printer genuinely isn't covered by the source's
// compatible_printers (A1 vs X1C's list) — FindCandidateParentsByCompatiblePrinters
// must find the real "@BBL A1" sibling under the same family root, not by
// name-token matching (there'd be nothing to match: process profile names
// don't follow filament's one-per-exact-printer convention).
func TestCopyProcessPreviewToPublishToCheckRecognitionEndToEnd(t *testing.T) {
	ts, _, processDir, _ := newTestServerWithProcessDir(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	previewResp, err := http.PostForm(ts.URL+"/copy/process/preview", map[string][]string{
		"name":          {"Syscode - 0.20mm Standard @BBL X1C"},
		"printer_token": {"A1 0.4|Bambu Lab A1 0.4 nozzle"},
	})
	if err != nil {
		t.Fatalf("POST /copy/process/preview: %v", err)
	}
	defer previewResp.Body.Close()
	previewBody, _ := io.ReadAll(previewResp.Body)
	if !strings.Contains(string(previewBody), "0.20mm Standard @BBL A1") {
		t.Fatalf("preview missing the matched parent: %s", previewBody)
	}
	if !strings.Contains(string(previewBody), `action="/copy/process/publish"`) {
		t.Fatalf("preview missing the process publish form: %s", previewBody)
	}

	const newName = "Test Process Copy @A1"
	publishResp, err := client.PostForm(ts.URL+"/copy/process/publish", map[string][]string{
		"name":         {"Syscode - 0.20mm Standard @BBL X1C"},
		"parent":       {"0.20mm Standard @BBL A1"},
		"confirm_name": {newName},
	})
	if err != nil {
		t.Fatalf("POST /copy/process/publish: %v", err)
	}
	defer publishResp.Body.Close()
	if publishResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /copy/process/publish status = %d, want 200", publishResp.StatusCode)
	}
	publishBody, _ := io.ReadAll(publishResp.Body)
	if !strings.Contains(string(publishBody), ">AWAITING SAVE IN STUDIO<") {
		t.Fatalf("publish result's final state is not INSTALLED_LOCALLY: %s", publishBody)
	}

	if _, err := os.Stat(filepath.Join(processDir, newName+".json")); err != nil {
		t.Fatalf("published process file missing on disk: %v", err)
	}

	idx := strings.Index(string(publishBody), "/deployments/process/")
	if idx == -1 {
		t.Fatalf("publish result missing a process deployment link: %s", publishBody)
	}
	rest := string(publishBody)[idx+len("/deployments/process/"):]
	deploymentID := rest[:strings.IndexAny(rest, "\"'")]

	infoPath := filepath.Join(processDir, newName+".info")
	if err := os.WriteFile(infoPath, []byte("updated_time = 99999\nsetting_id = GPtest\n"), 0o644); err != nil {
		t.Fatalf("write .info: %v", err)
	}

	checkResp, err := client.Post(ts.URL+"/deployments/process/"+deploymentID+"/check", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /deployments/process/%s/check: %v", deploymentID, err)
	}
	defer checkResp.Body.Close()
	if checkResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST check status = %d, want 303 redirect", checkResp.StatusCode)
	}

	finalResp, err := http.Get(ts.URL + "/deployments/process/" + deploymentID)
	if err != nil {
		t.Fatalf("GET /deployments/process/%s (final): %v", deploymentID, err)
	}
	defer finalResp.Body.Close()
	finalBody, _ := io.ReadAll(finalResp.Body)
	if !strings.Contains(string(finalBody), ">VERIFIED<") {
		t.Fatalf("process deployment did not reach ACTIVE after check-recognition: %s", finalBody)
	}
}

// TestCopyFormProcessRoutes404WhenNotConfigured guards the "not wired up"
// path: without --process-user-dir (the default), the process routes must
// 404, not panic on a nil ProcessSvc.
func TestCopyFormProcessRoutes404WhenNotConfigured(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t) // no ProcessSvc

	resp, err := http.Get(ts.URL + "/copy/process")
	if err != nil {
		t.Fatalf("GET /copy/process: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestProfilePreviewFragmentProcessKind covers the hover-preview button on
// the process copy form (/copy/process) — same fragment endpoint as
// filament, just resolved against ProcessUserDir/ProcessSystemDirs via
// ?kind=process.
func TestProfilePreviewFragmentProcessKind(t *testing.T) {
	ts, _, _, _ := newTestServerWithProcessDir(t)

	resp, err := http.Get(ts.URL + "/api/profile-preview?kind=process&name=" + url.QueryEscape("Syscode - 0.20mm Standard @BBL X1C"))
	if err != nil {
		t.Fatalf("GET /api/profile-preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Syscode - 0.20mm Standard @BBL X1C") {
		t.Fatalf("fragment missing profile name: %s", body)
	}
}

// TestProfilePreviewFragmentProcessKindNotConfigured guards the "not wired
// up" path: ?kind=process without --process-user-dir must 404, not panic
// on a nil ProcessSvc.
func TestProfilePreviewFragmentProcessKindNotConfigured(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t) // no ProcessSvc

	resp, err := http.Get(ts.URL + "/api/profile-preview?kind=process&name=" + url.QueryEscape("anything"))
	if err != nil {
		t.Fatalf("GET /api/profile-preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestCompareLiveProcessProfiles covers process profiles showing up in
// Compare alongside filament — a distinct "liveprocess:" key so a
// same-named filament and process profile can never collide.
func TestCompareLiveProcessProfiles(t *testing.T) {
	ts, _, _, _ := newTestServerWithProcessDir(t)

	formResp, err := http.Get(ts.URL + "/compare")
	if err != nil {
		t.Fatalf("GET /compare: %v", err)
	}
	defer formResp.Body.Close()
	formBody, _ := io.ReadAll(formResp.Body)
	if !strings.Contains(string(formBody), "liveprocess:Syscode - 0.20mm Standard @BBL X1C") {
		t.Fatalf("compare form missing a live process profile option: %s", formBody)
	}

	resp, err := http.Get(ts.URL + "/compare?item=" + url.QueryEscape("liveprocess:Syscode - 0.20mm Standard @BBL X1C") +
		"&item=" + url.QueryEscape("liveprocess:0.20mm Standard @BBL A1"))
	if err != nil {
		t.Fatalf("GET /compare?item=liveprocess:...: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "(process)") {
		t.Fatalf("compare result missing the (process) marker: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Wall Loops") {
		t.Fatalf("compare result missing settings for process profiles: %s", bodyStr)
	}
}

func TestBackupsListAndRestoreEndToEnd(t *testing.T) {
	ts, liveDir, _ := newTestServerWithLiveDir(t)
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

// TestCompareAcrossThreeDifferentProfiles proves /compare works across any
// profiles (not just revisions of the same one), up to 3 at once, and
// highlights rows that differ while leaving identical rows unhighlighted.
func TestCompareAcrossThreeDifferentProfiles(t *testing.T) {
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	svc := &service.Service{Repo: repo}
	srv := &webui.Server{Svc: svc}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	ctx := context.Background()
	mk := func(name string, resolved string) (string, int) {
		p, err := repo.Profiles().Create(ctx, name)
		if err != nil {
			t.Fatalf("Create profile %s: %v", name, err)
		}
		v, err := repo.Versions().Create(ctx, domain.ProfileVersion{
			ProfileID: p.ID, Revision: 1,
			SourceJSON: []byte(`{}`), ResolvedJSON: []byte(resolved), SemanticHash: "h-" + name,
		})
		if err != nil {
			t.Fatalf("Create version for %s: %v", name, err)
		}
		return p.ID, v.Revision
	}

	id1, rev1 := mk("Profile One", `{"nozzle_temperature":"250","filament_type":"ABS"}`)
	id2, rev2 := mk("Profile Two", `{"nozzle_temperature":"260","filament_type":"ABS"}`)
	id3, rev3 := mk("Profile Three", `{"nozzle_temperature":"250","filament_type":"ABS"}`)

	url := fmt.Sprintf("%s/compare?item=%s:%d&item=%s:%d&item=%s:%d", ts.URL, id1, rev1, id2, rev2, id3, rev3)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET compare: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET compare status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{"Profile One", "Profile Two", "Profile Three", "Nozzle Temperature", "250", "260"} {
		if !strings.Contains(bodyStr, want) {
			t.Fatalf("compare page missing %q: %s", want, bodyStr)
		}
	}
	if !strings.Contains(bodyStr, "bg-amber-50") {
		t.Fatalf("compare page did not highlight the differing row: %s", bodyStr)
	}
}

// TestIndexShowsUntrackedLiveProfiles guards against the real gap found
// after shipping: the landing page only listed profiles bambupm's own db
// had recorded (via Import/Copy), not everything actually present in the
// user's Bambu Studio directory. With --user-dir set, untracked profiles
// must still appear, marked as such, and be viewable read-only via /live.
// TestProfilePreviewFragmentReturnsSummary covers the copy form's hover
// preview button: /api/profile-preview?name=... must return a bare HTML
// fragment (no page shell) with the profile's summary fields.
func TestProfilePreviewFragmentReturnsSummary(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t)

	resp, err := http.Get(ts.URL + "/api/profile-preview?name=" + url.QueryEscape("Syscode - AmazonBasics ABS 0.6"))
	if err != nil {
		t.Fatalf("GET /api/profile-preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "Syscode - AmazonBasics ABS 0.6") {
		t.Fatalf("fragment missing profile name: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "<html") || strings.Contains(bodyStr, "<nav") {
		t.Fatalf("fragment should not include the page shell: %s", bodyStr)
	}
}

func TestProfilePreviewFragmentNotFound(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t)

	resp, err := http.Get(ts.URL + "/api/profile-preview?name=" + url.QueryEscape("Does Not Exist"))
	if err != nil {
		t.Fatalf("GET /api/profile-preview: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestCompareLiveUntrackedProfiles covers a real gap: nearly every profile
// in a real Bambu Studio library is untracked (no ProfileVersion row — see
// TestIndexShowsUntrackedLiveProfiles for the same issue on the landing
// page), so /compare must offer live profiles too, not just db revisions.
func TestCompareLiveUntrackedProfiles(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t)

	formResp, err := http.Get(ts.URL + "/compare")
	if err != nil {
		t.Fatalf("GET /compare: %v", err)
	}
	defer formResp.Body.Close()
	formBody, _ := io.ReadAll(formResp.Body)
	if !strings.Contains(string(formBody), "live:Syscode - AmazonBasics ABS 0.6") {
		t.Fatalf("compare form missing a live profile option: %s", formBody)
	}

	resp, err := http.Get(ts.URL + "/compare?item=" + url.QueryEscape("live:Syscode - AmazonBasics ABS 0.6") +
		"&item=" + url.QueryEscape("live:Bambu ABS @BBL X1C"))
	if err != nil {
		t.Fatalf("GET /compare?item=live:...: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "(live)") {
		t.Fatalf("compare result missing the (live) marker: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Nozzle Temperature") {
		t.Fatalf("compare result missing settings for live profiles: %s", bodyStr)
	}
}

func TestIndexShowsUntrackedLiveProfiles(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Syscode - AmazonBasics ABS 0.6") {
		t.Fatalf("index missing the untracked live profile: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "not tracked") {
		t.Fatalf("index did not mark the untracked profile as such: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "/live/") {
		t.Fatalf("index did not link the untracked profile to /live/: %s", bodyStr)
	}

	liveResp, err := http.Get(ts.URL + "/live/Syscode%20-%20AmazonBasics%20ABS%200.6")
	if err != nil {
		t.Fatalf("GET /live/...: %v", err)
	}
	defer liveResp.Body.Close()
	if liveResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /live/... status = %d, want 200", liveResp.StatusCode)
	}
	liveBody, _ := io.ReadAll(liveResp.Body)
	if !strings.Contains(string(liveBody), "Not tracked yet") {
		t.Fatalf("live profile page missing the not-tracked explanation: %s", liveBody)
	}
}
