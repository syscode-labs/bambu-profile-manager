package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
)

// targetProfileFromVersion reconstructs the RawProfile that was actually
// published from a stored ProfileVersion's ResolvedJSON. Its Name MUST come
// from the stored fields, not from the storage.Profile row's Name: a rebind
// published under a --target-name publishes a DIFFERENT filename than the
// source profile it started from (RebindAndPublish's targetName override).
// Using the Profile row's name here once pointed at the SOURCE profile's
// real, pre-existing .info file — which already has a setting_id from
// ordinary use — producing a false-positive "Studio recognized it" on every
// check, exactly what decisions.md #6 forbids. (Found by running the real
// CLI against a live Bambu Studio directory: the reported ACTIVE state
// didn't match the target profile's .info file, which didn't exist.)
func targetProfileFromVersion(resolvedJSON []byte) (*domain.RawProfile, error) {
	var fields map[string]any
	if err := json.Unmarshal(resolvedJSON, &fields); err != nil {
		return nil, fmt.Errorf("decode stored version: %w", err)
	}
	name, _ := fields["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("stored version has no name field")
	}
	return &domain.RawProfile{Name: name, Fields: fields}, nil
}

func newUUID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func cmdPublish(args []string) {
	dbPath := flagValue(args, "--db")
	if dbPath == "" {
		dbPath = "bambupm.db"
	}
	userDir := flagValue(args, "--user-dir")
	systemDirs := flagValues(args, "--system-dir")
	name := flagValue(args, "--name")
	candidates := flagValues(args, "--target")
	targetName := flagValue(args, "--target-name")
	if userDir == "" || name == "" || len(candidates) == 0 {
		fmt.Fprintln(os.Stderr, "publish: --user-dir, --name, and at least one --target are required")
		os.Exit(2)
	}

	set, err := loadDirs(append([]string{userDir}, systemDirs...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "publish:", err)
		os.Exit(1)
	}
	leaf, ok := set[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "publish: %q not found under given directories\n", name)
		os.Exit(1)
	}

	repo, err := sqlite.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "publish:", err)
		os.Exit(1)
	}
	defer repo.Close()

	ctx := context.Background()
	profile, err := repo.Profiles().GetByName(ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		profile, err = repo.Profiles().Create(ctx, name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "publish: profile:", err)
		os.Exit(1)
	}

	svc := &service.Service{
		Repo:     repo,
		Adapter:  &bambuadapter.LocalAdapter{Dir: userDir}, // IsStudioRunning nil -> real pgrep check
		Detector: reconcile.RewriteDetector{},               // the detector decisions.md #5 flagged unverified
		NewID:    newUUID,
	}

	result, err := svc.RebindAndPublish(ctx, set, set, leaf, candidates, profile.ID, targetName,
		reconcile.InfoFields{}, reconcile.InfoFields{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "publish:", err)
	}
	if result == nil || result.Deployment == nil {
		os.Exit(1)
	}

	fmt.Printf("deployment: %s\n", result.Deployment.ID)
	fmt.Printf("state:      %s\n", result.Deployment.State)
	if result.Rebind != nil {
		fmt.Printf("strategy:   %s\n", result.Rebind.Strategy)
		if result.Rebind.Strategy == "map_to_target_parent" {
			fmt.Printf("parent:     %s\n", result.Rebind.MatchedCandidate)
		}
	}
	if result.PublishedPath != "" {
		fmt.Printf("published:  %s\n", result.PublishedPath)
	}
	for _, tr := range result.Deployment.History {
		fmt.Printf("  %s -> %s (%s)\n", tr.From, tr.To, tr.Reason)
	}

	if result.Deployment.State == reconcile.StateInstalledLocally {
		fmt.Fprintf(os.Stderr, "\nNow reopen Bambu Studio, confirm the profile appears, then run:\n"+
			"  bambupm check-recognition --db %s --deployment-id %s --user-dir %s\n", dbPath, result.Deployment.ID, userDir)
	}
	if err != nil {
		os.Exit(1)
	}
}

func cmdCheckRecognition(args []string) {
	dbPath := flagValue(args, "--db")
	if dbPath == "" {
		dbPath = "bambupm.db"
	}
	deploymentID := flagValue(args, "--deployment-id")
	userDir := flagValue(args, "--user-dir")
	if deploymentID == "" || userDir == "" {
		fmt.Fprintln(os.Stderr, "check-recognition: --deployment-id and --user-dir are required")
		os.Exit(2)
	}

	repo, err := sqlite.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-recognition:", err)
		os.Exit(1)
	}
	defer repo.Close()
	ctx := context.Background()

	dep, err := repo.Deployments().Get(ctx, deploymentID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-recognition: load deployment:", err)
		os.Exit(1)
	}
	if _, err := repo.Profiles().Get(ctx, dep.ProfileID); err != nil {
		fmt.Fprintln(os.Stderr, "check-recognition: load profile:", err)
		os.Exit(1)
	}
	versions, err := repo.Versions().List(ctx, dep.ProfileID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-recognition: load versions:", err)
		os.Exit(1)
	}
	var resolvedJSON []byte
	for _, v := range versions {
		if v.Revision == dep.Revision {
			resolvedJSON = v.ResolvedJSON
			break
		}
	}
	if resolvedJSON == nil {
		fmt.Fprintf(os.Stderr, "check-recognition: no stored version for revision %d\n", dep.Revision)
		os.Exit(1)
	}
	targetProfile, err := targetProfileFromVersion(resolvedJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-recognition:", err)
		os.Exit(1)
	}
	publishedPath := filepath.Join(userDir, targetProfile.Name+".json")
	infoPath := filepath.Join(userDir, targetProfile.Name+".info")

	afterInfo := reconcile.InfoFields{}
	if b, err := os.ReadFile(infoPath); err == nil {
		afterInfo = reconcile.ParseInfo(b)
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "check-recognition: read .info:", err)
		os.Exit(1)
	}

	svc := &service.Service{Repo: repo, Adapter: &bambuadapter.LocalAdapter{Dir: userDir}, Detector: reconcile.RewriteDetector{}}
	result, err := svc.CheckRecognition(ctx, deploymentID, targetProfile, publishedPath, reconcile.InfoFields{}, afterInfo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-recognition:", err)
	}
	if result == nil || result.Deployment == nil {
		os.Exit(1)
	}

	fmt.Printf("state: %s\n", result.Deployment.State)
	for _, tr := range result.Deployment.History {
		fmt.Printf("  %s -> %s (%s)\n", tr.From, tr.To, tr.Reason)
	}
	if result.Deployment.State == reconcile.StateInstalledLocally {
		fmt.Fprintln(os.Stderr, "\nNot recognized yet (no .info change detected). Re-run this command after Studio has had a chance to sync.")
	}
	if err != nil {
		os.Exit(1)
	}
}
