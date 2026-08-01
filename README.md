# Bambu Profile Manager

> Experimental — under active development. Publish/rebind against a live Bambu Studio installation is not yet exposed as a command; only the safe, reversible parts (scanning, resolving, importing bundles, browsing in the web UI) are wired up so far.

A local tool for taking a filament profile you've tuned in Bambu Studio, exporting it as a portable bundle, moving it to a different printer or nozzle, and publishing it back — with a built-in check that the round trip actually preserved your settings, instead of just assuming a file write succeeded.

## Stack

| Component | Role |
|---|---|
| Go | Application language |
| `modernc.org/sqlite` | Pure-Go SQLite driver — no C toolchain needed to build or cross-compile |
| `net/http` + `html/template` | Web UI: server-rendered pages, plain page reloads |
| Docker (multi-stage) | Packaging, amd64/arm64 |

## Getting started

Requires Go 1.25+.

```bash
go build ./...
go test ./...
```

Run the web UI locally:

```bash
go run ./cmd/bambupm serve --db bambupm.db --addr :8080
```

Then open `http://localhost:8080`.

Or with Docker:

```bash
docker compose up --build
```

## Day-to-day usage

Scan a Bambu Studio profile directory:

```bash
go run ./cmd/bambupm scan --dir "$HOME/Library/Application Support/BambuStudio/user/<your-account-id>/filament"
```

Resolve a profile's full inheritance chain to its effective (flattened) settings:

```bash
go run ./cmd/bambupm resolve \
  --dir "$HOME/Library/Application Support/BambuStudio/user/<your-account-id>/filament" \
  --dir "$HOME/Library/Application Support/BambuStudio/system/BBL/filament" \
  --name "My Custom Filament"
```

Start the web UI to browse imported profiles and import a `.profilepack` bundle:

```bash
go run ./cmd/bambupm serve
```

Export/rebind/publish are implemented in `internal/service` and covered by its tests, but not yet exposed as CLI commands — publishing into a live Bambu Studio installation requires the user present (Bambu Studio must be closed during publish, then reopened to confirm recognition), which doesn't fit a plain one-shot command yet. See `openspec/changes/init-profile-manager/tasks.md` for what's left.

## Known limitations

- **The Studio-running safety check can't see host processes from inside a container.** `internal/bambuadapter`'s publish guard shells out to `pgrep` to refuse publishing while Bambu Studio is open (see `openspec/changes/init-profile-manager/decisions.md` #4). From inside Docker, that check only sees processes in the container's own PID namespace — it will never detect Bambu Studio running on the host. Don't run publish operations through the Docker image until this is resolved; run them natively on the host, where the check is meaningful.
- Round-trip verification against a real Bambu Studio installation needs a human to close and reopen Studio — it isn't automatable end to end (see `openspec/changes/init-profile-manager/decisions.md`).

<details>
<summary>CLI reference</summary>

```
bambupm scan --dir <bambu-user-filament-dir>
    List profiles found in a Bambu Studio user filament directory.

bambupm resolve --dir <dir> [--dir <dir> ...] --name "<profile name>"
    Resolve a profile's full inheritance chain and print the effective
    (flattened) fields as JSON.

bambupm serve [--db <path>] [--addr <addr>]
    Start the local web UI. --db defaults to bambupm.db, --addr to :8080.
```

</details>

<details>
<summary>Project structure</summary>

```
cmd/bambupm/         CLI entry point
internal/domain/      Core types (RawProfile, Profile, ProfileVersion, DomainEvent)
internal/parser/      Loads Bambu Studio profile JSON, preserving unknown fields
internal/resolver/     Resolves the `inherits` chain (name-based, see decisions.md #3)
internal/normalize/   Canonical form + semantic hash
internal/storage/      Repository interfaces + SQLite implementation + contract tests
internal/bundle/       .profilepack export/import
internal/rebind/       Cross-printer/nozzle rebinding
internal/bambuadapter/ Bambu Studio filesystem adapter (discover/stage/publish/observe/verify)
internal/reconcile/    Sync/reconciliation state machine + OBSERVED_BY_STUDIO detector
internal/service/      Wires the above together into the full flow
internal/webui/        Web UI
```

Full design and decision history: `openspec/changes/init-profile-manager/` (`proposal.md`, `design.md`, `findings.md`, `decisions.md`, `tasks.md`).

</details>
