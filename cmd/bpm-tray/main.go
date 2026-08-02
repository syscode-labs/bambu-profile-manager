// bpm-tray is a macOS menu bar wrapper around bpm serve: no terminal window,
// no flags to remember — it auto-discovers your real Bambu Studio
// directories and just runs, with a menu to open the web UI or quit.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"fyne.io/systray"
	"github.com/google/uuid"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
	"github.com/syscod3/bambu-profile-manager/internal/webui"

	"net/http"
)

const addr = ":8080"

func main() {
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetIcon(webui.LogoPNG)
	systray.SetTooltip("Bambu Profile Manager")

	mOpen := systray.AddMenuItem("Open bpm", "Open the web UI in your browser")
	mStatus := systray.AddMenuItem("Starting…", "")
	mStatus.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop bpm and quit")

	go func() {
		if err := startServer(); err != nil {
			mStatus.SetTitle("Failed to start: " + err.Error())
			return
		}
		mStatus.SetTitle("Running at " + addr)
	}()

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				exec.Command("open", "http://localhost"+addr).Start()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// startServer wires up the same server cmd/bpm's `serve` builds, but with
// zero flags: db and backups live under ~/Library/Application Support/bpm,
// and Bambu Studio's real directories are auto-discovered rather than
// passed explicitly — a menu bar app has no terminal to pass flags into.
func startServer() error {
	appDir, err := supportDir()
	if err != nil {
		return err
	}
	dbPath := filepath.Join(appDir, "bpm.db")

	repo, err := sqlite.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	svc := &service.Service{Repo: repo, NewID: newUUID}
	srv := &webui.Server{Svc: svc}

	if userDir, systemDir, ok := discoverBambuDir("filament"); ok {
		svc.Adapter = &bambuadapter.LocalAdapter{Dir: userDir}
		svc.Detector = reconcile.RewriteDetector{}
		svc.BackupsDir = filepath.Join(appDir, "backups")
		srv.UserDir = userDir
		srv.SystemDirs = []string{systemDir}
	}
	if machineUserDir, machineSystemDir, ok := discoverBambuDir("machine"); ok {
		srv.MachineDirs = []string{machineUserDir, machineSystemDir}
	}
	if processUserDir, processSystemDir, ok := discoverBambuDir("process"); ok {
		processRepo, err := sqlite.Open(filepath.Join(appDir, "bpm-process.db"))
		if err != nil {
			return fmt.Errorf("open process db: %w", err)
		}
		srv.ProcessSvc = &service.Service{
			Repo:       processRepo,
			Adapter:    &bambuadapter.LocalAdapter{Dir: processUserDir},
			Detector:   reconcile.RewriteDetector{},
			NewID:      newUUID,
			BackupsDir: filepath.Join(appDir, "backups-process"),
		}
		srv.ProcessUserDir = processUserDir
		srv.ProcessSystemDirs = []string{processSystemDir}
	}

	if srv.UserDir != "" || srv.ProcessUserDir != "" {
		go srv.PollRecognition(context.Background(), 5*time.Second)
	}

	go func() {
		if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
			fmt.Fprintln(os.Stderr, "bpm-tray: serve:", err)
		}
	}()
	return nil
}

// supportDir returns ~/Library/Application Support/bpm, creating it if
// needed — a menu bar app has no fixed working directory to default a
// relative "bpm.db" against, unlike the CLI's `serve`.
func supportDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Application Support", "bpm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// discoverBambuDir finds Bambu Studio's real user + system directories for
// kind ("filament", "process", or "machine") under the standard macOS
// install location. The user directory is named after a numeric account ID
// bpm can't guess, so this picks the first account-ID folder that actually
// has a kind subdirectory — matching how a single-user desktop app is
// expected to be used (one real Bambu Studio account signed in).
func discoverBambuDir(kind string) (userDir, systemDir string, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false
	}
	base := filepath.Join(home, "Library", "Application Support", "BambuStudio")
	systemDir = filepath.Join(base, "system", "BBL", kind)
	userBase := filepath.Join(base, "user")

	entries, err := os.ReadDir(userBase)
	if err != nil {
		return "", "", false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "default" {
			continue
		}
		candidate := filepath.Join(userBase, e.Name(), kind)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, systemDir, true
		}
	}
	return "", "", false
}

func newUUID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
