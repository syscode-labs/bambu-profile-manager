package service_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/parser"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
)

func loadSet(t *testing.T, dir string) resolver.Set {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	set := resolver.Set{}
	for _, m := range matches {
		p, err := parser.Load(m)
		if err != nil {
			t.Fatalf("load %s: %v", m, err)
		}
		set[p.Name] = p
	}
	return set
}

func newID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// TestAcceptanceFlowImportRebindPublishVerify exercises the design.md §22
// primary acceptance test end to end, against real fixture data, entirely
// within t.TempDir() — never the real local BambuStudio directory. Studio
// recognition can't happen unattended (decisions.md #4), so this test
// stands in for it with ManualDetector{Confirmed: true}, simulating "the
// user reopened Studio and confirmed".
func TestAcceptanceFlowImportRebindPublishVerify(t *testing.T) {
	ctx := context.Background()
	fixtureBase := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")

	sourceSet := resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source")) {
		sourceSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source", "system")) {
		sourceSet[k] = v
	}
	targetSet := resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "target-system")) {
		targetSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source", "system")) {
		if _, ok := targetSet[k]; !ok {
			targetSet[k] = v
		}
	}

	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	bambuDir := t.TempDir()
	svc := &service.Service{
		Repo:     repo,
		Adapter:  &bambuadapter.LocalAdapter{Dir: bambuDir, IsStudioRunning: func() (bool, error) { return false, nil }},
		Detector: reconcile.ManualDetector{Confirmed: true},
		NewID:    newID,
	}

	const leafName = "Syscode - AmazonBasics ABS 0.6"

	// 1. Export from the "source install" and 2. import into a clean DB —
	// design.md §22 steps 4-6 ("remove access to the source directory;
	// import the bundle into a clean database").
	var bundleBuf bytes.Buffer
	if _, err := svc.ExportBundle(ctx, &bundleBuf, sourceSet, leafName); err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundleBuf.Bytes()), int64(bundleBuf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	profile, version, err := svc.ImportBundle(ctx, zr)
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	if version.Revision != 1 {
		t.Fatalf("first imported version has revision %d, want 1", version.Revision)
	}

	// 3. Rebind to P1S and 4. publish — steps 7-11.
	leaf := sourceSet[leafName]
	candidates := []string{"Bambu ABS @BBL P1S", "Bambu ABS @BBL P1S 0.4 nozzle"}
	before := reconcile.InfoFields{"updated_time": "100", "setting_id": ""}
	after := reconcile.InfoFields{"updated_time": "200", "setting_id": "PFUSnew"}

	result, err := svc.RebindAndPublish(ctx, sourceSet, targetSet, leaf, candidates, profile.ID, "", before, after)
	if err != nil {
		t.Fatalf("RebindAndPublish: %v", err)
	}

	if result.Deployment.State != reconcile.StateActive {
		t.Fatalf("final deployment state = %s, want ACTIVE (history: %+v)", result.Deployment.State, result.Deployment.History)
	}
	if !result.Deployment.IsRoundTripVerified() {
		t.Fatal("Deployment.IsRoundTripVerified() = false at ACTIVE")
	}

	// The published file must actually exist under bambuDir (design.md §4:
	// "do not report success merely because files were written" — but it
	// must, at minimum, have been written).
	obs, err := svc.Adapter.Observe(ctx, filepath.Join(bambuDir, leafName+".json"))
	if err != nil {
		t.Fatalf("Observe published file: %v", err)
	}
	if obs.Fields["inherits"] != "Bambu ABS @BBL P1S 0.4 nozzle" {
		t.Fatalf("published profile inherits=%v, want the real (non-naive) P1S parent", obs.Fields["inherits"])
	}
}

func TestRebindAndPublishStopsAtInstalledLocallyWithoutRecognition(t *testing.T) {
	ctx := context.Background()
	fixtureBase := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")
	sourceSet := resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source")) {
		sourceSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source", "system")) {
		sourceSet[k] = v
	}
	targetSet := loadSet(t, filepath.Join(fixtureBase, "target-system"))
	for k, v := range sourceSet {
		if _, ok := targetSet[k]; !ok {
			targetSet[k] = v
		}
	}

	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	svc := &service.Service{
		Repo:     repo,
		Adapter:  &bambuadapter.LocalAdapter{Dir: t.TempDir(), IsStudioRunning: func() (bool, error) { return false, nil }},
		Detector: reconcile.ManualDetector{Confirmed: false}, // Studio hasn't "recognized" it yet
		NewID:    newID,
	}

	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]
	profile, err := repo.Profiles().Create(ctx, leaf.Name)
	if err != nil {
		t.Fatalf("Create profile: %v", err)
	}
	result, err := svc.RebindAndPublish(ctx, sourceSet, targetSet, leaf,
		[]string{"Bambu ABS @BBL P1S 0.4 nozzle"}, profile.ID, "",
		reconcile.InfoFields{}, reconcile.InfoFields{})
	if err != nil {
		t.Fatalf("RebindAndPublish: %v", err)
	}
	if result.Deployment.State != reconcile.StateInstalledLocally {
		t.Fatalf("state = %s, want INSTALLED_LOCALLY (must not advance past what's actually confirmed, decisions.md #6)", result.Deployment.State)
	}
}

func TestRollbackRepublishesLastKnownGoodRevision(t *testing.T) {
	ctx := context.Background()
	fixtureBase := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")

	sourceSet := resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source")) {
		sourceSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source", "system")) {
		sourceSet[k] = v
	}
	targetSet := loadSet(t, filepath.Join(fixtureBase, "target-system"))
	for k, v := range sourceSet {
		if _, ok := targetSet[k]; !ok {
			targetSet[k] = v
		}
	}

	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	bambuDir := t.TempDir()
	svc := &service.Service{
		Repo:     repo,
		Adapter:  &bambuadapter.LocalAdapter{Dir: bambuDir, IsStudioRunning: func() (bool, error) { return false, nil }},
		Detector: reconcile.ManualDetector{Confirmed: true},
		NewID:    newID,
	}

	const leafName = "Syscode - AmazonBasics ABS 0.6"
	leaf := sourceSet[leafName]

	// Import (revision 1) then publish a rebind to ACTIVE (revision 2) —
	// this becomes the "last known good" state Rollback should restore.
	var bundleBuf bytes.Buffer
	if _, err := svc.ExportBundle(ctx, &bundleBuf, sourceSet, leafName); err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundleBuf.Bytes()), int64(bundleBuf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	profile, _, err := svc.ImportBundle(ctx, zr)
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}

	candidates := []string{"Bambu ABS @BBL P1S 0.4 nozzle"}
	info := reconcile.InfoFields{"updated_time": "1", "setting_id": "id-1"}
	goodResult, err := svc.RebindAndPublish(ctx, sourceSet, targetSet, leaf, candidates, profile.ID, "", info, info)
	if err != nil {
		t.Fatalf("RebindAndPublish (establishing known-good): %v", err)
	}
	if goodResult.Deployment.State != reconcile.StateActive {
		t.Fatalf("known-good publish reached %s, want ACTIVE (setup broken, not what this test checks)", goodResult.Deployment.State)
	}
	goodRevision := goodResult.Deployment.Revision

	// Now roll back. There's nothing "bad" to undo in this test beyond
	// proving the mechanism: republish revision `goodRevision`'s stored
	// resolved profile as a new deployment, and it must independently reach
	// ACTIVE again.
	rollbackResult, err := svc.Rollback(ctx, profile.ID, info, info)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rollbackResult.Deployment.State != reconcile.StateActive {
		t.Fatalf("rollback deployment state = %s, want ACTIVE (history: %+v)", rollbackResult.Deployment.State, rollbackResult.Deployment.History)
	}
	if rollbackResult.Deployment.Revision != goodRevision+1 {
		t.Fatalf("rollback deployment revision = %d, want %d (a new revision, not overwriting the old one)", rollbackResult.Deployment.Revision, goodRevision+1)
	}

	obs, err := svc.Adapter.Observe(ctx, filepath.Join(bambuDir, leafName+".json"))
	if err != nil {
		t.Fatalf("Observe published file: %v", err)
	}
	if obs.Fields["inherits"] != "Bambu ABS @BBL P1S 0.4 nozzle" {
		t.Fatalf("rolled-back file inherits=%v, want the restored known-good parent", obs.Fields["inherits"])
	}

	deployments, err := repo.Deployments().ListByProfile(ctx, profile.ID)
	if err != nil {
		t.Fatalf("ListByProfile: %v", err)
	}
	if len(deployments) != 2 {
		t.Fatalf("stored %d deployments, want 2 (the original publish + the rollback)", len(deployments))
	}
}

// TestRetryAfterStudioRunningReusesTheSameRevision exercises decisions.md
// #4's expected flow: publish refuses because Bambu Studio is open, the
// user closes it, the caller retries. The retry must land on the same
// revision as the failed attempt and succeed, not hit
// profile_versions' UNIQUE(profile_id, revision) constraint.
func TestRetryAfterStudioRunningReusesTheSameRevision(t *testing.T) {
	ctx := context.Background()
	fixtureBase := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")
	sourceSet := resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source")) {
		sourceSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source", "system")) {
		sourceSet[k] = v
	}
	targetSet := loadSet(t, filepath.Join(fixtureBase, "target-system"))
	for k, v := range sourceSet {
		if _, ok := targetSet[k]; !ok {
			targetSet[k] = v
		}
	}

	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	studioRunning := true
	svc := &service.Service{
		Repo:     repo,
		Adapter:  &bambuadapter.LocalAdapter{Dir: t.TempDir(), IsStudioRunning: func() (bool, error) { return studioRunning, nil }},
		Detector: reconcile.ManualDetector{Confirmed: true},
		NewID:    newID,
	}

	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]
	profile, err := repo.Profiles().Create(ctx, leaf.Name)
	if err != nil {
		t.Fatalf("Create profile: %v", err)
	}
	candidates := []string{"Bambu ABS @BBL P1S 0.4 nozzle"}
	info := reconcile.InfoFields{}

	first, err := svc.RebindAndPublish(ctx, sourceSet, targetSet, leaf, candidates, profile.ID, "", info, info)
	if err == nil || !errors.Is(err, bambuadapter.ErrStudioRunning) {
		t.Fatalf("first attempt error = %v, want ErrStudioRunning", err)
	}
	if first.Deployment.State != reconcile.StateStaged {
		t.Fatalf("first attempt state = %s, want STAGED (Studio-running isn't a failure state, just \"hasn't happened yet\" — decisions.md #4)", first.Deployment.State)
	}
	firstRevision := first.Deployment.Revision

	studioRunning = false // user closed Bambu Studio
	second, err := svc.RebindAndPublish(ctx, sourceSet, targetSet, leaf, candidates, profile.ID, "", info, info)
	if err != nil {
		t.Fatalf("retry after closing Studio: %v", err)
	}
	if second.Deployment.State != reconcile.StateActive {
		t.Fatalf("retry state = %s, want ACTIVE (history: %+v)", second.Deployment.State, second.Deployment.History)
	}
	if second.Deployment.Revision != firstRevision {
		t.Fatalf("retry used revision %d, want it to reuse the failed attempt's revision %d", second.Deployment.Revision, firstRevision)
	}
}

// TestImportSameBundleTwiceDoesNotConflict guards against the same root
// cause the retry test above covers: nothing should trust a fixed revision
// number across repeated calls when nothing was actually persisted for it,
// or nothing should re-derive a number already taken.
func TestImportSameBundleTwiceDoesNotConflict(t *testing.T) {
	ctx := context.Background()
	fixtureBase := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")
	sourceSet := resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source")) {
		sourceSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source", "system")) {
		sourceSet[k] = v
	}

	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	svc := &service.Service{Repo: repo}

	const leafName = "Syscode - AmazonBasics ABS 0.6"
	var bundleBuf bytes.Buffer
	if _, err := svc.ExportBundle(ctx, &bundleBuf, sourceSet, leafName); err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	bundleBytes := bundleBuf.Bytes()

	openZip := func() *zip.Reader {
		zr, err := zip.NewReader(bytes.NewReader(bundleBytes), int64(len(bundleBytes)))
		if err != nil {
			t.Fatalf("zip.NewReader: %v", err)
		}
		return zr
	}

	_, v1, err := svc.ImportBundle(ctx, openZip())
	if err != nil {
		t.Fatalf("first ImportBundle: %v", err)
	}
	_, v2, err := svc.ImportBundle(ctx, openZip())
	if err != nil {
		t.Fatalf("second ImportBundle (same bundle again): %v", err)
	}
	if v1.Revision == v2.Revision {
		t.Fatalf("both imports got revision %d, want distinct revisions", v1.Revision)
	}
}

// TestCheckRecognitionResumesAfterInstalledLocally is the real-world flow
// this whole two-call split exists for: publish while Studio is closed
// (recognition can't happen yet), then separately check recognition once
// the user has reopened Studio — without re-staging or re-publishing.
func TestCheckRecognitionResumesAfterInstalledLocally(t *testing.T) {
	ctx := context.Background()
	fixtureBase := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s")
	sourceSet := resolver.Set{}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source")) {
		sourceSet[k] = v
	}
	for k, v := range loadSet(t, filepath.Join(fixtureBase, "source", "system")) {
		sourceSet[k] = v
	}
	targetSet := loadSet(t, filepath.Join(fixtureBase, "target-system"))
	for k, v := range sourceSet {
		if _, ok := targetSet[k]; !ok {
			targetSet[k] = v
		}
	}

	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	svc := &service.Service{
		Repo:     repo,
		Adapter:  &bambuadapter.LocalAdapter{Dir: t.TempDir(), IsStudioRunning: func() (bool, error) { return false, nil }},
		Detector: reconcile.ManualDetector{Confirmed: false}, // nothing to observe yet
		NewID:    newID,
	}

	leaf := sourceSet["Syscode - AmazonBasics ABS 0.6"]
	profile, err := repo.Profiles().Create(ctx, leaf.Name)
	if err != nil {
		t.Fatalf("Create profile: %v", err)
	}

	first, err := svc.RebindAndPublish(ctx, sourceSet, targetSet, leaf,
		[]string{"Bambu ABS @BBL P1S 0.4 nozzle"}, profile.ID, "",
		reconcile.InfoFields{}, reconcile.InfoFields{})
	if err != nil {
		t.Fatalf("RebindAndPublish: %v", err)
	}
	if first.Deployment.State != reconcile.StateInstalledLocally {
		t.Fatalf("state = %s, want INSTALLED_LOCALLY", first.Deployment.State)
	}

	// Simulate the user reopening Studio, which (per decisions.md #5's
	// RewriteDetector) would bump the .info file. Swap the Service's
	// Detector to confirm, as if it now fired.
	svc.Detector = reconcile.ManualDetector{Confirmed: true}

	second, err := svc.CheckRecognition(ctx, first.Deployment.ID, first.TargetProfile, first.PublishedPath,
		reconcile.InfoFields{}, reconcile.InfoFields{"updated_time": "1"})
	if err != nil {
		t.Fatalf("CheckRecognition: %v", err)
	}
	if second.Deployment.State != reconcile.StateActive {
		t.Fatalf("state after CheckRecognition = %s, want ACTIVE (history: %+v)", second.Deployment.State, second.Deployment.History)
	}

	// The deployment loaded fresh from storage must carry forward the
	// history recorded by the first call, not start over.
	sawInstalledLocally := false
	for _, tr := range second.Deployment.History {
		if tr.To == reconcile.StateInstalledLocally {
			sawInstalledLocally = true
		}
	}
	if !sawInstalledLocally {
		t.Fatalf("CheckRecognition's deployment history lost the earlier INSTALLED_LOCALLY transition: %+v", second.Deployment.History)
	}
}
