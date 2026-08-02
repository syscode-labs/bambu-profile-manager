# Bambu Profile Manager

[![CI](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/ci.yml)
[![Gitleaks](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/gitleaks.yml/badge.svg)](https://github.com/syscode-labs/bambu-profile-manager/actions/workflows/gitleaks.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> Experimental — under active development.

A local tool for managing Bambu Studio **filament** and **process (print)** profiles: copy one to another printer or nozzle without hand-editing an `inherits` chain, and get a built-in check that Bambu Studio actually picked up the result — a file landing on disk isn't treated as success until Studio itself confirms it. Ships as a single Go binary with a local web UI, or as a scriptable CLI.

## Stack

| Component | Role |
|---|---|
| Go | Application language |
| `modernc.org/sqlite` | Pure-Go SQLite driver — no C toolchain needed to build or cross-compile |
| `net/http` + `html/template` | Web UI: server-rendered pages, plain page reloads, no build step |
| Docker (multi-stage) | Packaging, amd64/arm64 |

## Getting started

Requires Go 1.25+.

```bash
go build ./...
go test ./...
```

Run the web UI locally:

```bash
go run ./cmd/bpm serve --db bpm.db --addr :8080
```

Then open `http://localhost:8080`. List/import/detail work immediately; add `--user-dir`/`--system-dir` (and `--process-user-dir`/`--process-system-dir` for process profiles) pointed at your real Bambu Studio directories to unlock Copy, Compare's live tab, and Backups — see `bpm serve --help`-equivalent usage below, or the in-app **Help** page.

Or with Docker:

```bash
docker compose up --build
```

`docker-compose.yml` has commented bind mounts and flags for your real Bambu Studio directories — uncomment and fill in your account ID to enable the same Copy/Compare/Backups features as a native run.

## Day-to-day usage

Everything below also works through the web UI — the CLI is for scripting/headless use.

Scan a Bambu Studio profile directory:

```bash
go run ./cmd/bpm scan --dir "$HOME/Library/Application Support/BambuStudio/user/<your-account-id>/filament"
```

Copy a filament profile to another printer, auto-matching the base profile by material + printer:

```bash
go run ./cmd/bpm copy --db bpm.db \
  --user-dir "$HOME/Library/Application Support/BambuStudio/user/<your-account-id>/filament" \
  --system-dir "$HOME/Library/Application Support/BambuStudio/system/BBL/filament" \
  --name "My Custom Filament" --to-printer P1S
```

Previews and prints a suggested name; nothing publishes until you re-run with `--confirm-name`. Publishing requires Bambu Studio closed, takes an automatic backup first, and stops at `INSTALLED_LOCALLY` until you reopen Studio and Save — then resume with `check-recognition`. See the in-app **Help** page for the full lifecycle explanation.

Export/import portable `.profilepack` bundles, resolve a profile's effective settings, or manage backups — see the CLI reference below, or run `go run ./cmd/bpm` with no arguments for the full flag-by-flag usage text.

## Known limitations

- **The Studio-running safety check can't see host processes from inside a container.** `internal/bambuadapter`'s publish guard shells out to `pgrep` to refuse publishing while Bambu Studio is open (see `openspec/changes/init-profile-manager/decisions.md` #4). From inside Docker, that check only sees processes in the container's own PID namespace — it will never detect Bambu Studio running on the host. Close Studio yourself before publishing when running via Docker.
- Round-trip verification against a real Bambu Studio installation needs a human to close and reopen Studio — it isn't automatable end to end (see `openspec/changes/init-profile-manager/decisions.md`).

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
