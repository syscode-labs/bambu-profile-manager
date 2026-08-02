package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/rebind"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
)

// defaultBackupsDir keeps point-in-time snapshots colocated with the db but
// outside the live Bambu Studio directory, so Studio never has a reason to
// see them.
func defaultBackupsDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "backups")
}

// defaultProcessDBPath derives a sibling db path for process (print)
// deployments — kept in a wholly separate db/Repo from filament's so
// process and filament profile IDs never collide (see webui.Server's doc
// comment on ProcessSvc).
func defaultProcessDBPath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + "-process" + ext
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
		dbPath = "bpm.db"
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

	set, err := resolver.LoadDirs(append([]string{userDir}, systemDirs...))
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

	backupsDir := flagValue(args, "--backups-dir")
	if backupsDir == "" {
		backupsDir = defaultBackupsDir(dbPath)
	}
	svc := &service.Service{
		Repo:       repo,
		Adapter:    &bambuadapter.LocalAdapter{Dir: userDir}, // IsStudioRunning nil -> real pgrep check
		Detector:   reconcile.RewriteDetector{},               // the detector decisions.md #5 flagged unverified
		NewID:      newUUID,
		BackupsDir: backupsDir,
	}

	result, err := svc.RebindAndPublish(ctx, set, set, leaf, candidates, profile.ID, targetName, "",
		reconcile.InfoFields{}, reconcile.InfoFields{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "publish:", err)
	}
	if result == nil || result.Deployment == nil {
		os.Exit(1)
	}

	fmt.Printf("deployment: %s\n", result.Deployment.ID)
	fmt.Printf("state:      %s\n", result.Deployment.State)
	if result.Snapshot != "" {
		fmt.Printf("backup:     %s (restore with: bpm backups restore --db %s --backups-dir %s --user-dir %s --name %s)\n",
			result.Snapshot, dbPath, backupsDir, userDir, result.Snapshot)
	}
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
			"  bpm check-recognition --db %s --deployment-id %s --user-dir %s\n", dbPath, result.Deployment.ID, userDir)
	}
	if err != nil {
		os.Exit(1)
	}
}

func cmdCheckRecognition(args []string) {
	dbPath := flagValue(args, "--db")
	if dbPath == "" {
		dbPath = "bpm.db"
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
	targetProfile, err := service.TargetProfileFromVersion(resolvedJSON)
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
	// nil: this command has no --system-dir flag to build a full resolver.Set
	// from, so it keeps today's raw-fields comparison rather than partially
	// resolving against --user-dir alone.
	result, err := svc.CheckRecognition(ctx, deploymentID, targetProfile, publishedPath, nil, reconcile.InfoFields{}, afterInfo)
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

// suggestName is copy's default target-profile name: source name + the
// printer token, so it's distinguishable from the source in Studio's list.
// The user is expected to confirm or edit this (--confirm-name) before
// anything publishes — copy never publishes on its own.
func suggestName(sourceName, printerToken string) string {
	return fmt.Sprintf("%s @%s", sourceName, printerToken)
}

func cmdCopy(args []string) {
	dbPath := flagValue(args, "--db")
	if dbPath == "" {
		dbPath = "bpm.db"
	}
	userDir := flagValue(args, "--user-dir")
	systemDirs := flagValues(args, "--system-dir")
	name := flagValue(args, "--name")
	printerToken := flagValue(args, "--to-printer")
	confirmName := flagValue(args, "--confirm-name")
	if userDir == "" || name == "" || printerToken == "" {
		fmt.Fprintln(os.Stderr, "copy: --user-dir, --name, and --to-printer are required")
		os.Exit(2)
	}

	set, err := resolver.LoadDirs(append([]string{userDir}, systemDirs...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "copy:", err)
		os.Exit(1)
	}
	leaf, ok := set[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "copy: %q not found under given directories\n", name)
		os.Exit(1)
	}

	candidates, err := rebind.FindCandidateParents(set, set, leaf, printerToken)
	if err != nil {
		fmt.Fprintln(os.Stderr, "copy:", err)
		os.Exit(1)
	}

	switch len(candidates) {
	case 0:
		fmt.Fprintf(os.Stderr, "copy: no matching parent found for printer %q with %q's material. "+
			"Nothing safe to auto-map — use `bpm publish --target <name>` with an explicit parent if you know one.\n", printerToken, name)
		os.Exit(1)
	case 1:
		// fall through
	default:
		fmt.Fprintf(os.Stderr, "copy: %d plausible parents found for printer %q, refusing to guess:\n", len(candidates), printerToken)
		for _, c := range candidates {
			fmt.Fprintf(os.Stderr, "  - %s\n", c)
		}
		fmt.Fprintln(os.Stderr, "Use `bpm publish --target <name>` with the one you want.")
		os.Exit(1)
	}

	suggested := suggestName(name, printerToken)
	if confirmName == "" {
		fmt.Printf("match:    %s\n", candidates[0])
		fmt.Printf("suggested target name: %s\n", suggested)
		fmt.Fprintf(os.Stderr, "\nNothing published yet. Re-run with --confirm-name %q (or your own edited name) to publish.\n", suggested)
		return
	}

	repo, err := sqlite.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "copy:", err)
		os.Exit(1)
	}
	defer repo.Close()
	ctx := context.Background()

	profile, err := repo.Profiles().GetByName(ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		profile, err = repo.Profiles().Create(ctx, name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "copy: profile:", err)
		os.Exit(1)
	}

	backupsDir := flagValue(args, "--backups-dir")
	if backupsDir == "" {
		backupsDir = defaultBackupsDir(dbPath)
	}
	svc := &service.Service{
		Repo:       repo,
		Adapter:    &bambuadapter.LocalAdapter{Dir: userDir},
		Detector:   reconcile.RewriteDetector{},
		NewID:      newUUID,
		BackupsDir: backupsDir,
	}

	result, err := svc.RebindAndPublish(ctx, set, set, leaf, candidates, profile.ID, confirmName, "",
		reconcile.InfoFields{}, reconcile.InfoFields{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "copy:", err)
	}
	if result == nil || result.Deployment == nil {
		os.Exit(1)
	}

	fmt.Printf("deployment: %s\n", result.Deployment.ID)
	fmt.Printf("state:      %s\n", result.Deployment.State)
	fmt.Printf("parent:     %s\n", candidates[0])
	fmt.Printf("name:       %s\n", confirmName)
	if result.Snapshot != "" {
		fmt.Printf("backup:     %s\n", result.Snapshot)
	}
	for _, tr := range result.Deployment.History {
		fmt.Printf("  %s -> %s (%s)\n", tr.From, tr.To, tr.Reason)
	}
	if result.Deployment.State == reconcile.StateInstalledLocally {
		fmt.Fprintf(os.Stderr, "\nNow reopen Bambu Studio, select the new profile, and Save it once to trigger recognition, then run:\n"+
			"  bpm check-recognition --db %s --deployment-id %s --user-dir %s\n", dbPath, result.Deployment.ID, userDir)
	}
	if err != nil {
		os.Exit(1)
	}
}

func cmdBackupsList(args []string) {
	dbPath := flagValue(args, "--db")
	if dbPath == "" {
		dbPath = "bpm.db"
	}
	backupsDir := flagValue(args, "--backups-dir")
	if backupsDir == "" {
		backupsDir = defaultBackupsDir(dbPath)
	}
	svc := &service.Service{BackupsDir: backupsDir}
	list, err := svc.ListBackups()
	if err != nil {
		fmt.Fprintln(os.Stderr, "backups list:", err)
		os.Exit(1)
	}
	if len(list) == 0 {
		fmt.Println("no backups yet")
		return
	}
	for _, b := range list {
		fmt.Printf("%s  %s\n", b.Name, b.At.Format("2006-01-02 15:04:05 MST"))
	}
}

func cmdBackupsRestore(args []string) {
	dbPath := flagValue(args, "--db")
	if dbPath == "" {
		dbPath = "bpm.db"
	}
	backupsDir := flagValue(args, "--backups-dir")
	if backupsDir == "" {
		backupsDir = defaultBackupsDir(dbPath)
	}
	userDir := flagValue(args, "--user-dir")
	name := flagValue(args, "--name")
	if userDir == "" || name == "" {
		fmt.Fprintln(os.Stderr, "backups restore: --user-dir and --name are required")
		os.Exit(2)
	}
	svc := &service.Service{Adapter: &bambuadapter.LocalAdapter{Dir: userDir}, BackupsDir: backupsDir}
	if err := svc.RestoreBackup(name); err != nil {
		fmt.Fprintln(os.Stderr, "backups restore:", err)
		os.Exit(1)
	}
	fmt.Printf("restored %s into %s\n", name, userDir)
}
