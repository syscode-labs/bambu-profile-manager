# Tasks

Derived from design.md §20-21 (Agent Workstreams / Execution Order).

## Phase 0 — Technical spike
- [ ] Agent A: document real Bambu profile directory layout, `.json`/`.info` relationship, inheritance rules, sync transformations; produce first compatibility fixtures
- [ ] Agent B: domain model, parser, recursive resolver, semantic normaliser/hash, golden + fuzz tests
- Exit gate: `semantic(source) == semantic(reimport(export(source)))` for one real fixture. Stop if format can't be resolved reliably.

## Phase 1 — Portable core
- [ ] Agent C: repository interfaces, SQLite impl + migrations, immutable versions, contract tests
- [ ] Agent D: `.profilepack` schema, manifest, dependency-closure export, checksums, lossless round-trip tests
- Exit gate: discover → resolve → export → delete source → import → resolve identically

## Phase 2 — Rebinding
- [ ] Agent E: source/target binding model, target-parent mapping, chain cloning/flattening, semantic diff
- Exit gate: X1C 0.4 → P1S 0.4 fixture passes; target resolves independently

## Phase 3 — Bambu round trip (highest priority)
- [ ] Agent F: Bambu adapter (discover/validate/stage/publish/observe/verify), atomic publish + backups, reconciliation state machine, rollback
- Exit gate: import → rebind → publish → Studio recognition → sync/reload → post-sync re-read → semantic equality. Do not begin broad UI work before this passes.

## Phase 4 — UI and operational support
- [ ] Agent G: templ/HTMX UI (profile browser, detail, dependency graph, rebind, deployments, rollback)
- [ ] Agent H: Ristretto cache, revision-based keys, dependency-aware invalidation, singleflight, WebSocket hub, bounded queues
- [ ] Agent I: multi-stage Dockerfile (amd64/arm64), compose example, CI (unit/integration/fuzz + Bambu compatibility job), release artifacts
- Exit gate: `docker compose up` runs a working local instance against a bind-mounted Bambu directory

## Deferred (post-v1)
- PostgreSQL adapter, Google Sheets projection, OrcaSlicer support, experimental Bambu Cloud adapter, multi-instance deployment

## Primary Acceptance Test (design.md §22)
- [ ] Fiberon PET-CF17 @ X1C 0.4 → P1S 0.4: full discover→export→import→rebind→publish→sync→verify→slice loop, `expected semantic profile == post-sync semantic profile`
