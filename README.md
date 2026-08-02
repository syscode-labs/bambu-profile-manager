<p align="center">
  <img src="internal/webui/assets/logo.png" alt="Bambu Profile Manager logo" width="96">
</p>

# Bambu Profile Manager

[![CI](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/ci.yml)
[![Gitleaks](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/gitleaks.yml/badge.svg)](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/gitleaks.yml)
[![Release](https://img.shields.io/github/v/release/syscode-labs/bambu-profile-manager)](https://github.com/syscode-labs/bambu-profile-manager/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> Experimental — under active development.

Bambu Studio stores your filament and print settings as profiles that inherit from each other. Want to use a profile you tuned on one printer on a different printer or nozzle? Normally you'd copy it by hand and hope you picked the right base to inherit from. `bpm` (short for **b**ambu **p**rofile **m**anager) does that copy for you, then checks that Bambu Studio actually loaded it — not just that a file landed on disk.

## Stack

| Component | Role |
|---|---|
| Go | Application language |
| `modernc.org/sqlite` | Pure-Go SQLite driver — no C toolchain needed to build or cross-compile |
| `net/http` + `html/template` | Web UI: plain server-rendered pages, no JavaScript build step |
| Docker (multi-stage) | Packaging, amd64/arm64 |

## Getting started

Pick whichever install method is easiest for you.

**Homebrew (macOS/Linux):**

```bash
brew install syscode-labs/public/bpm
bpm serve --db bpm.db --addr :8080
```

**From source (requires Go 1.25+):**

```bash
git clone https://github.com/syscode-labs/bambu-profile-manager.git
cd bambu-profile-manager
make install   # builds bpm and puts it on your PATH
bpm serve --db bpm.db --addr :8080
```

Run `make help` to see every other command (`build`, `test`, `run`, `docker`, `clean`).

**Docker:**

```bash
docker compose up --build
```

Whichever way you start it, open `http://localhost:8080`. Listing, viewing, and importing profiles works right away. To copy profiles between printers or restore a backup, add `--user-dir`/`--system-dir` (filament) and `--process-user-dir`/`--process-system-dir` (process/print) pointing at your real Bambu Studio directories — see the in-app **Help** page for exactly where those live and what each flag does.

## Day-to-day usage

Everything below also works through the web UI — the CLI is for scripting or running headless.

Scan a Bambu Studio profile directory:

```bash
bpm scan --dir "$HOME/Library/Application Support/BambuStudio/user/<your-account-id>/filament"
```

Copy a filament profile to another printer, picking the right base profile for you:

```bash
bpm copy --db bpm.db \
  --user-dir "$HOME/Library/Application Support/BambuStudio/user/<your-account-id>/filament" \
  --system-dir "$HOME/Library/Application Support/BambuStudio/system/BBL/filament" \
  --name "My Custom Filament" --to-printer P1S
```

This only shows you what it *would* do and a suggested name — nothing is published until you re-run with `--confirm-name`. Publishing needs Bambu Studio closed, takes an automatic backup first, and stops once the file is written — reopen Studio, save the profile once, then run `check-recognition` to confirm it took. The in-app **Help** page walks through why that one manual step can't be skipped.

See the CLI reference below, or run `bpm` with no arguments, for everything else (export/import bundles, resolve a profile's effective settings, manage backups).

## Known limitations

- **Running via Docker, the "is Bambu Studio open?" safety check can't see your host machine.** It only sees processes inside the container, so it will never notice Bambu Studio running on your actual computer. Close Studio yourself before publishing if you're using the Docker image.
- Confirming that Bambu Studio picked up a change needs a human to close and reopen Studio — there's no way to automate that part.

<details>
<summary>CLI reference</summary>

```
bpm scan --dir <bambu-user-filament-dir>
    List profiles found in a Bambu Studio user filament directory.

bpm resolve --dir <dir> [--dir <dir> ...] --name "<profile name>"
    Resolve a profile's full inheritance chain and print the effective
    (flattened) fields as JSON.

bpm serve --db <path> --addr :8080 [--user-dir <dir>] [--system-dir <dir> [...]]
          [--machine-dir <dir> [...]] [--backups-dir <dir>]
          [--process-user-dir <dir>] [--process-system-dir <dir> [...]]
    Start the local web UI. List/detail/import always work. Pass --user-dir
    (and --system-dir) to also enable Copy and Backups. --machine-dir
    populates the "target printer" dropdown with your real printers.
    --process-user-dir/--process-system-dir enable copying process (print)
    profiles the same way, at /copy/process.

bpm publish --db <path> --user-dir <dir> --system-dir <dir> [...]
            --name "<profile name>" --target <candidate> [...]
            [--target-name "<new profile name>"]
    Rebind a profile and publish it. Bambu Studio must be closed. Stops at
    INSTALLED_LOCALLY — reopen Studio, then run check-recognition.

bpm check-recognition --db <path> --deployment-id <id> --user-dir <dir>
    Resume a deployment sitting at INSTALLED_LOCALLY: read the profile's
    current .info file and check whether Bambu Studio picked it up.

bpm copy --db <path> --user-dir <dir> --system-dir <dir> [...]
         --name "<profile name>" --to-printer <token>
         [--confirm-name "<new name>"]
    Copy a filament profile to another printer without picking a target
    parent by hand. Without --confirm-name, only previews the match.

bpm backups list --db <path> [--backups-dir <dir>]
    List point-in-time snapshots taken before each publish.

bpm backups restore --db <path> --user-dir <dir> --name <snapshot>
    Restore a snapshot into the live Bambu directory. Non-destructive.
```

</details>

<details>
<summary>Project structure</summary>

```
cmd/bpm/               CLI entry point
internal/domain/       Core types (RawProfile, Profile, ProfileVersion, DomainEvent)
internal/parser/       Loads Bambu Studio profile JSON, preserving unknown fields
internal/resolver/     Resolves the `inherits` chain (name-based, see decisions.md #3)
internal/normalize/    Canonical form + semantic hash
internal/storage/      Repository interfaces + SQLite implementation + contract tests
internal/bundle/       .profilepack export/import
internal/rebind/       Cross-printer/nozzle rebinding, filament and process
internal/bambuadapter/ Bambu Studio filesystem adapter (discover/stage/publish/observe/verify)
internal/reconcile/    Sync/reconciliation state machine + OBSERVED_BY_STUDIO detector
internal/service/      Wires the above together into the full flow
internal/webui/        Web UI: Copy, Compare, Backups, Help
```

Full design and decision history: `openspec/changes/init-profile-manager/` (`proposal.md`, `design.md`, `findings.md`, `decisions.md`, `tasks.md`).

</details>
