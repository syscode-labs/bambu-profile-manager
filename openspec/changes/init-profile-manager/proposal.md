# Proposal: Bambu Profile Manager v1

## Why

Bambu Studio filament profiles have no portable, dependency-aware export/rebind/round-trip path. Users cannot move a tuned filament profile to another printer/nozzle and confirm it still resolves to the same semantic settings after Bambu re-syncs it. Manual JSON editing risks silent drift and broken inheritance chains.

## What Changes

- Local-first Go web app: scan Bambu Studio profile library, resolve inheritance closure, export portable `.profilepack` bundles, import independently, rebind to another printer/nozzle, publish back into Bambu Studio, and verify round-trip semantic equality after Studio sync/restart.
- SQLite-backed storage with immutable profile/deployment revisions and a domain event log.
- HTMX/templ UI with WebSocket progress/status events; REST remains authoritative for all mutations.
- Explicit sync/reconciliation state machine ending in `ROUND_TRIP_VERIFIED`; no success claims on file-write alone.
- Docker multi-arch (amd64/arm64) deployment via bind-mounted Bambu user directory.

## Out of Scope (v1)

Filament inventory (spools, AMS, RFID, weight, humidity, purchase history), printer control, print-farm management, OrcaSlicer, direct Bambu Cloud API writes, Google Sheets sync, PostgreSQL, Redis, Kubernetes, multi-user/SaaS.

## Source

Full architecture, domain model, and agent workstream breakdown: `design.md` (verbatim from `~/Downloads/bambu-profile-manager-agent-plan.md`).

Research against real local Bambu Studio config and web sources, plus resolved grill decisions: `findings.md`, `decisions.md`. Where they conflict with `design.md`, `decisions.md` wins.
