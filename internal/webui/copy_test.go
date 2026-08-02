package webui_test

import (
	"context"
	"encoding/json"
	"errors"
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

// TestCopyPreviewOffersUnverifiedPublishWhenNoCandidateFound covers the
// escape hatch for when bpm genuinely cannot find a safe parent (real
// case found live: a 0.20mm-layer-height process profile has no compatible
// parent for a 0.2mm-nozzle printer anywhere in Bambu's own catalog, since
// that combination doesn't physically exist) — user asked to still be able
// to publish standalone if they explicitly acknowledge it's unverified,
// rather than being fully blocked.
func TestCopyPreviewOffersFamilyCandidatesWithDiffWhenNoVerifiedMatch(t *testing.T) {
	ts, liveDir, _ := newTestServerWithLiveDir(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// "P1P" isn't present anywhere in this fixture set (confirmed by
	// TestFindCandidateParentsNoMatchForWrongMaterial), so this is a
	// genuine zero-verified-candidate case — but the profile's own real
	// material family (fdm_filament_abs) does have real, existing sibling
	// profiles bpm can offer instead of a dead end.
	previewResp, err := http.PostForm(ts.URL+"/copy/preview", map[string][]string{
		"name":          {"Syscode - AmazonBasics ABS 0.6"},
		"printer_token": {"P1P"},
	})
	if err != nil {
		t.Fatalf("POST /copy/preview: %v", err)
	}
	defer previewResp.Body.Close()
	previewBody, _ := io.ReadAll(previewResp.Body)
	previewStr := string(previewBody)
	if !strings.Contains(previewStr, "isn't listed as compatible with any base profile") {
		t.Fatalf("preview missing the not-verified warning: %s", previewStr)
	}
	if !strings.Contains(previewStr, "Bambu ABS @BBL X1C") {
		t.Fatalf("preview missing a real same-family candidate: %s", previewStr)
	}
	if !strings.Contains(previewStr, `name="confirm_unverified"`) {
		t.Fatalf("preview missing the confirm-and-add-compatibility checkbox: %s", previewStr)
	}
	if !strings.Contains(previewStr, "would change from your current profile") {
		t.Fatalf("preview missing the per-candidate settings diff: %s", previewStr)
	}
	if !strings.Contains(previewStr, `name="printer_canonical" value="P1P"`) {
		t.Fatalf("preview missing the printer_canonical hidden field: %s", previewStr)
	}

	// Submitting without the checkbox must still be rejected, even though
	// the parent itself is real (server-side backstop, not just the
	// client-side "required" attribute).
	blockedResp, err := client.PostForm(ts.URL+"/copy/publish", map[string][]string{
		"name":              {"Syscode - AmazonBasics ABS 0.6"},
		"parent":            {"Bambu ABS @BBL X1C"},
		"printer_canonical": {"P1P"},
		"confirm_name":      {"Should Not Publish @P1P"},
	})
	if err != nil {
		t.Fatalf("POST /copy/publish (no checkbox): %v", err)
	}
	defer blockedResp.Body.Close()
	if blockedResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("publish without confirm_unverified status = %d, want 400", blockedResp.StatusCode)
	}

	const newName = "Unverified Copy @P1P"
	publishResp, err := client.PostForm(ts.URL+"/copy/publish", map[string][]string{
		"name":               {"Syscode - AmazonBasics ABS 0.6"},
		"parent":             {"Bambu ABS @BBL X1C"},
		"printer_canonical":  {"P1P"},
		"confirm_unverified": {"on"},
		"confirm_name":       {newName},
	})
	if err != nil {
		t.Fatalf("POST /copy/publish: %v", err)
	}
	defer publishResp.Body.Close()
	if publishResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /copy/publish status = %d, want 200", publishResp.StatusCode)
	}
	publishBody, _ := io.ReadAll(publishResp.Body)
	if !strings.Contains(string(publishBody), "Added") || !strings.Contains(string(publishBody), "P1P") {
		t.Fatalf("publish result did not show the added-compatibility note: %s", publishBody)
	}

	published, err := os.ReadFile(filepath.Join(liveDir, newName+".json"))
	if err != nil {
		t.Fatalf("read published file: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(published, &fields); err != nil {
		t.Fatalf("parse published file: %v", err)
	}
	if fields["inherits"] != "Bambu ABS @BBL X1C" {
		t.Fatalf("published inherits = %v, want the chosen family parent", fields["inherits"])
	}
	cp, _ := fields["compatible_printers"].([]any)
	var found bool
	for _, v := range cp {
		if v == "P1P" {
			found = true
		}
	}
	if !found {
		t.Fatalf("published compatible_printers = %v, want it to include the added P1P", cp)
	}
}

// TestCopyPublishRejectsSelfReferencingInherits reproduces a real bug found
// live: the "New profile name" field's suggested default doesn't depend on
// which candidate is picked, so if a profile with that exact name already
// exists (e.g. left over from an earlier attempt with the same
// source+target), it legitimately shows up as a same-family candidate too.
// Picking that card while leaving the name field unedited submits
// parent == confirm_name — publishing a profile that inherits from itself,
// which Bambu Studio silently drops rather than display. Must be rejected
// server-side, not just relied on as an unlikely coincidence.
func TestCopyPublishRejectsSelfReferencingInherits(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.PostForm(ts.URL+"/copy/publish", map[string][]string{
		"name":         {"Syscode - AmazonBasics ABS 0.6"},
		"parent":       {"Same Name @P1S"},
		"confirm_name": {"Same Name @P1S"},
	})
	if err != nil {
		t.Fatalf("POST /copy/publish: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (self-referencing inherits must be rejected)", resp.StatusCode)
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
	if !strings.Contains(string(publishBody), `title="INSTALLED_LOCALLY"`) || !strings.Contains(string(publishBody), ">AWAITING EXPLICIT MANUAL SAVE IN STUDIO<") {
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
	if !strings.Contains(string(detailBody), "Open Bambu Studio") || !strings.Contains(string(detailBody), "/api/launch-studio") {
		t.Fatalf("deployment detail missing the Open Bambu Studio button: %s", detailBody)
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

// TestDeploymentDetailPollsStatusFragmentClientSide covers the real
// complaint found live: the backend already rechecks INSTALLED_LOCALLY
// deployments every few seconds (TestBackgroundPollerDetectsRecognitionWithoutManualCheck),
// but the already-open deployment page was static HTML from page load, so a
// user watching it saw nothing change without a manual reload/click. The
// page must embed a poll script pointed at /api/deployment-status/{id}, and
// that endpoint must independently reflect the current state.
func TestDeploymentDetailPollsStatusFragmentClientSide(t *testing.T) {
	ts, liveDir, _ := newTestServerWithLiveDir(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	const newName = "Fragment Poll Test @P1S"
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

	detailResp, err := http.Get(ts.URL + "/deployments/" + deploymentID)
	if err != nil {
		t.Fatalf("GET /deployments/%s: %v", deploymentID, err)
	}
	defer detailResp.Body.Close()
	detailBody, _ := io.ReadAll(detailResp.Body)
	detailStr := string(detailBody)
	if !strings.Contains(detailStr, `id="deployment-status"`) {
		t.Fatalf("deployment detail missing the pollable status div: %s", detailStr)
	}
	// html/template's JS-context escaper legally escapes "/" as "\/" inside
	// the <script> block's string literal, so check the unescaped form.
	if !strings.Contains(strings.ReplaceAll(detailStr, `\/`, "/"), "/api/deployment-status/"+deploymentID) {
		t.Fatalf("deployment detail missing the poll script's fetch target: %s", detailStr)
	}
	if !strings.Contains(detailStr, "setInterval") {
		t.Fatalf("deployment detail missing the poll script itself: %s", detailStr)
	}

	fragResp, err := http.Get(ts.URL + "/api/deployment-status/" + deploymentID)
	if err != nil {
		t.Fatalf("GET /api/deployment-status/%s: %v", deploymentID, err)
	}
	defer fragResp.Body.Close()
	fragBody, _ := io.ReadAll(fragResp.Body)
	fragStr := string(fragBody)
	if !strings.Contains(fragStr, `title="INSTALLED_LOCALLY"`) {
		t.Fatalf("status fragment missing the current state badge: %s", fragStr)
	}
	if strings.Contains(fragStr, "<html") || strings.Contains(fragStr, "<body") {
		t.Fatalf("status fragment should be a bare fragment, not a full page: %s", fragStr)
	}

	// Simulate Studio saving, then confirm the SAME fragment endpoint (no
	// page reload) reflects the new state once checked.
	infoPath := filepath.Join(liveDir, newName+".info")
	if err := os.WriteFile(infoPath, []byte("updated_time = 12345\nsetting_id = PFUStest\n"), 0o644); err != nil {
		t.Fatalf("write .info: %v", err)
	}
	if _, err := client.Post(ts.URL+"/deployments/"+deploymentID+"/check", "application/x-www-form-urlencoded", nil); err != nil {
		t.Fatalf("POST /deployments/%s/check: %v", deploymentID, err)
	}

	fragResp2, err := http.Get(ts.URL + "/api/deployment-status/" + deploymentID)
	if err != nil {
		t.Fatalf("GET /api/deployment-status/%s (after check): %v", deploymentID, err)
	}
	defer fragResp2.Body.Close()
	fragBody2, _ := io.ReadAll(fragResp2.Body)
	if !strings.Contains(string(fragBody2), `title="ACTIVE"`) {
		t.Fatalf("status fragment did not reflect ACTIVE after recognition: %s", fragBody2)
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
	if !strings.Contains(string(publishBody), ">AWAITING EXPLICIT MANUAL SAVE IN STUDIO<") {
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

// TestCopyProcessPreviewSuggestsClosestCandidateWhenAmbiguous covers the
// heuristic added live: FindCandidateParentsByCompatiblePrinters can return
// several equally "compatible" base profiles (e.g. real "Standard" vs
// "High Quality" system leaves under the same nozzle), leaving the user to
// guess which one the source profile was actually built on. bpm now
// picks the one requiring the fewest setting changes and marks it
// "Suggested", first in the list.
func TestCopyProcessPreviewSuggestsClosestCandidateWhenAmbiguous(t *testing.T) {
	ts, _, processDir, _ := newTestServerWithProcessDir(t)

	// A second same-family, same-target-printer candidate that diverges
	// from the leaf ("Syscode - 0.20mm Standard @BBL X1C", wall_loops=4,
	// sparse_infill_density=25%) in far more fields than the real
	// "0.20mm Standard @BBL A1" candidate does (which only differs in
	// wall_loops=2) — a worse match that must rank second.
	decoy := `{
		"type": "process",
		"name": "0.20mm Odd @BBL A1",
		"inherits": "fdm_process_single_0.20",
		"from": "system",
		"setting_id": "GP999",
		"instantiation": "true",
		"wall_loops": "9",
		"sparse_infill_density": "80%",
		"top_shell_layers": "7",
		"bottom_shell_layers": "6",
		"line_width": "0.6",
		"compatible_printers": ["Bambu Lab A1 0.4 nozzle"]
	}`
	if err := os.WriteFile(filepath.Join(processDir, "0.20mm Odd @BBL A1.json"), []byte(decoy), 0o644); err != nil {
		t.Fatalf("write decoy candidate: %v", err)
	}

	previewResp, err := http.PostForm(ts.URL+"/copy/process/preview", map[string][]string{
		"name":          {"Syscode - 0.20mm Standard @BBL X1C"},
		"printer_token": {"A1 0.4|Bambu Lab A1 0.4 nozzle"},
	})
	if err != nil {
		t.Fatalf("POST /copy/process/preview: %v", err)
	}
	defer previewResp.Body.Close()
	previewBody, _ := io.ReadAll(previewResp.Body)
	previewStr := string(previewBody)

	if !strings.Contains(previewStr, "Suggested") {
		t.Fatalf("preview missing the Suggested badge: %s", previewStr)
	}
	goodIdx := strings.Index(previewStr, "0.20mm Standard @BBL A1")
	badIdx := strings.Index(previewStr, "0.20mm Odd @BBL A1")
	if goodIdx == -1 || badIdx == -1 {
		t.Fatalf("preview missing one of the two candidates: %s", previewStr)
	}
	if goodIdx > badIdx {
		t.Fatalf("closer candidate (0.20mm Standard @BBL A1) should be listed first, ahead of the worse match: %s", previewStr)
	}
	suggestedIdx := strings.Index(previewStr, "Suggested")
	if suggestedIdx < goodIdx || suggestedIdx > badIdx {
		t.Fatalf("Suggested badge should be attached to the closer candidate's card: %s", previewStr)
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
	if !strings.Contains(string(formBody), `<optgroup label="Filament">`) || !strings.Contains(string(formBody), `<optgroup label="Process (print)">`) {
		t.Fatalf("compare form missing Filament/Process optgroups: %s", formBody)
	}

	resp, err := http.Get(ts.URL + "/compare?item=" + url.QueryEscape("liveprocess:Syscode - 0.20mm Standard @BBL X1C") +
		"&item=" + url.QueryEscape("liveprocess:0.20mm Standard @BBL A1"))
	if err != nil {
		t.Fatalf("GET /compare?item=liveprocess:...: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, ">process<") {
		t.Fatalf("compare result missing the process-kind badge: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "Wall Loops") {
		t.Fatalf("compare result missing settings for process profiles: %s", bodyStr)
	}
}

// TestIndexProcessShowsLiveProfilesAndLinksToDetailAndCompare covers
// browsing process profiles from the Profiles page (previously filament
// only): the /process tab must list them, link into a live detail view,
// and that view must link into Compare using the liveprocess: prefix.
func TestIndexProcessShowsLiveProfilesAndLinksToDetailAndCompare(t *testing.T) {
	ts, _, _, _ := newTestServerWithProcessDir(t)

	resp, err := http.Get(ts.URL + "/process")
	if err != nil {
		t.Fatalf("GET /process: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "Syscode - 0.20mm Standard @BBL X1C") {
		t.Fatalf("process index missing a live profile: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "/live/process/") {
		t.Fatalf("process index did not link into the process live view: %s", bodyStr)
	}

	liveResp, err := http.Get(ts.URL + "/live/process/" + url.PathEscape("Syscode - 0.20mm Standard @BBL X1C"))
	if err != nil {
		t.Fatalf("GET /live/process/...: %v", err)
	}
	defer liveResp.Body.Close()
	if liveResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /live/process/... status = %d, want 200", liveResp.StatusCode)
	}
	liveBody, _ := io.ReadAll(liveResp.Body)
	if !strings.Contains(string(liveBody), `/compare?item=liveprocess:`) {
		t.Fatalf("process live view missing a Compare link with the liveprocess: prefix: %s", liveBody)
	}
	if !strings.Contains(string(liveBody), `href="/copy/process"`) {
		t.Fatalf("process live view missing a Copy link to /copy/process: %s", liveBody)
	}
}

// TestIndexProcessRoutes404WhenNotConfigured guards the "not wired up"
// path, same as the copy/preview routes.
func TestIndexProcessRoutes404WhenNotConfigured(t *testing.T) {
	ts, _, _ := newTestServerWithLiveDir(t) // no ProcessSvc

	resp, err := http.Get(ts.URL + "/process")
	if err != nil {
		t.Fatalf("GET /process: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
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
	// Real bug found live: the blocked page's own content already explains
	// Studio is running, but it also had the live-poll banner slot
	// (id="studio-warning") turned on, showing the same message twice.
	if strings.Contains(string(publishBody), `id="studio-warning"`) {
		t.Fatalf("blocked-publish page shows a duplicate live-poll warning on top of its own content: %s", publishBody)
	}

	if _, err := os.Stat(filepath.Join(liveDir, "Blocked @P1S.json")); err == nil {
		t.Fatal("file was published even though Studio was reported running")
	}
}

// TestStudioStatusEndpointReflectsLiveChange covers the page-level poller
// (shell.go fetches this every 5s): the banner must reflect Studio's
// current state on each call, not just what was true when the page first
// loaded — simulated here with a checker whose answer changes between two
// requests to the same running server.
func TestStudioStatusEndpointReflectsLiveChange(t *testing.T) {
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	running := true
	svc := &service.Service{
		Repo:       repo,
		Adapter:    &bambuadapter.LocalAdapter{Dir: t.TempDir(), IsStudioRunning: func() (bool, error) { return running, nil }},
		Detector:   reconcile.RewriteDetector{},
		NewID:      newTestID,
		BackupsDir: t.TempDir(),
	}
	srv := &webui.Server{Svc: svc}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/studio-status")
	if err != nil {
		t.Fatalf("GET /api/studio-status: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Bambu Studio appears to be running") {
		t.Fatalf("expected running warning, got: %s", body)
	}

	running = false
	resp, err = http.Get(ts.URL + "/api/studio-status")
	if err != nil {
		t.Fatalf("GET /api/studio-status: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "appears to be running") {
		t.Fatalf("expected empty response once Studio closed, got: %s", body)
	}
}

// TestLaunchStudioEndpoint covers the "Open Bambu Studio" button's backend:
// a bare POST that shells out to launch the real app. LaunchStudio is
// injected so the test never actually runs `open -a`.
func TestLaunchStudioEndpoint(t *testing.T) {
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	svc := &service.Service{Repo: repo, NewID: newTestID}
	var called bool
	launchErr := error(nil)
	srv := &webui.Server{Svc: svc, LaunchStudio: func() error { called = true; return launchErr }}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/launch-studio", "", nil)
	if err != nil {
		t.Fatalf("POST /api/launch-studio: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !called {
		t.Fatal("LaunchStudio was not invoked")
	}

	launchErr = errors.New("open: no such app")
	resp, err = http.Post(ts.URL+"/api/launch-studio", "", nil)
	if err != nil {
		t.Fatalf("POST /api/launch-studio (failure case): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the launch fails", resp.StatusCode)
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
// after shipping: the landing page only listed profiles bpm's own db
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
