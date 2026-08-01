// bambupm is a minimal CLI over internal/service. It only wires the
// read/reversible operations (scan, export) to a real Bambu Studio
// directory by default — rebind/publish need an explicit --allow-publish
// flag, since a mistake there writes into a live, shared Bambu Studio
// installation (see internal/bambuadapter's package doc).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
	"github.com/syscod3/bambu-profile-manager/internal/webui"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "scan":
		cmdScan(os.Args[2:])
	case "resolve":
		cmdResolve(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	case "publish":
		cmdPublish(os.Args[2:])
	case "check-recognition":
		cmdCheckRecognition(os.Args[2:])
	case "copy":
		cmdCopy(os.Args[2:])
	case "backups":
		if len(os.Args) < 3 {
			usage()
			os.Exit(2)
		}
		switch os.Args[2] {
		case "list":
			cmdBackupsList(os.Args[3:])
		case "restore":
			cmdBackupsRestore(os.Args[3:])
		default:
			usage()
			os.Exit(2)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `bambupm — Bambu Studio profile manager

Usage:
  bambupm scan --dir <bambu-user-filament-dir>
      List profiles found in a Bambu Studio user filament directory.

  bambupm resolve --dir <dir> --name "<profile name>"
      Resolve a profile's full inheritance chain and print the effective
      (flattened) fields as JSON. --dir may be passed more than once to
      search several directories (e.g. user + system profile dirs).

  bambupm serve --db <path> --addr :8080 [--user-dir <dir>] [--system-dir <dir> [...]]
                [--machine-dir <dir> [...]] [--backups-dir <dir>]
      Start the local web UI. List/detail/import always work. Pass
      --user-dir (and --system-dir for target-parent matching) to also
      enable Copy-to-another-printer and Backups. --machine-dir (your
      real printer profile directories, both user and system) populates
      the "target printer" dropdown with your actual printers instead
      of guessed tokens.

  bambupm publish --db <path> --user-dir <dir> --system-dir <dir> [...]
                  --name "<profile name>" --target <candidate> [...]
                  [--target-name "<new profile name>"]
      Rebind a profile and publish it. Bambu Studio must be closed. Stops
      at INSTALLED_LOCALLY (recognition can't happen while Studio is
      closed) — reopen Studio, then run check-recognition.

  bambupm check-recognition --db <path> --deployment-id <id> --user-dir <dir>
      Resume a deployment sitting at INSTALLED_LOCALLY: read the profile's
      current .info file and check whether Bambu Studio picked it up.
      Trigger confirmed empirically: an explicit Save on the profile in
      Studio — reopening, selecting, or slicing alone do not bump .info.

  bambupm copy --db <path> --user-dir <dir> --system-dir <dir> [...]
               --name "<profile name>" --to-printer <token>
               [--confirm-name "<new name>"]
      Copy a filament profile to another printer without picking a target
      parent by hand: auto-matches by material family + printer token
      (e.g. "P1S"), refuses and lists candidates if ambiguous. Without
      --confirm-name, only previews the match and a suggested name —
      nothing is published until you confirm (or edit) that name and
      re-run with it. Takes an automatic backup before publishing.

  bambupm backups list --db <path> [--backups-dir <dir>]
      List point-in-time snapshots taken before each publish.

  bambupm backups restore --db <path> --user-dir <dir> --name <snapshot>
      Restore a snapshot into the live Bambu directory. Non-destructive:
      overwrites files present in the snapshot, never deletes files added
      since.`)
}

func cmdScan(args []string) {
	dir := flagValue(args, "--dir")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "scan: --dir is required")
		os.Exit(2)
	}
	a := &bambuadapter.LocalAdapter{Dir: dir}
	found, err := a.Discover(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}
	for _, p := range found {
		fmt.Println(p.Name)
	}
}

func cmdResolve(args []string) {
	dirs := flagValues(args, "--dir")
	name := flagValue(args, "--name")
	if len(dirs) == 0 || name == "" {
		fmt.Fprintln(os.Stderr, "resolve: --dir (>=1) and --name are required")
		os.Exit(2)
	}

	set, err := resolver.LoadDirs(dirs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		os.Exit(1)
	}

	leaf, ok := set[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "resolve: %q not found under given --dir(s)\n", name)
		os.Exit(1)
	}
	effective, chain, err := resolver.Resolve(set, leaf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "chain: %v\n", chain)
	b, err := json.MarshalIndent(effective.Fields, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve: marshal:", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
}

func cmdServe(args []string) {
	dbPath := flagValue(args, "--db")
	if dbPath == "" {
		dbPath = "bambupm.db"
	}
	addr := flagValue(args, "--addr")
	if addr == "" {
		addr = ":8080"
	}
	userDir := flagValue(args, "--user-dir")
	systemDirs := flagValues(args, "--system-dir")
	machineDirs := flagValues(args, "--machine-dir")
	backupsDir := flagValue(args, "--backups-dir")
	if backupsDir == "" {
		backupsDir = defaultBackupsDir(dbPath)
	}

	repo, err := sqlite.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
	defer repo.Close()

	svc := &service.Service{Repo: repo, NewID: newUUID}
	if userDir != "" {
		svc.Adapter = &bambuadapter.LocalAdapter{Dir: userDir}
		svc.Detector = reconcile.RewriteDetector{}
		svc.BackupsDir = backupsDir
	}
	srv := &webui.Server{Svc: svc, UserDir: userDir, SystemDirs: systemDirs, MachineDirs: machineDirs}

	fmt.Fprintf(os.Stderr, "bambupm web UI listening on %s (db: %s)\n", addr, dbPath)
	if userDir == "" {
		fmt.Fprintln(os.Stderr, "note: --user-dir not set — list/detail/import work, but Copy/Backups pages need it")
	} else {
		go srv.PollRecognition(context.Background(), 5*time.Second)
	}
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func flagValues(args []string, name string) []string {
	var out []string
	for i, a := range args {
		if a == name && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}
