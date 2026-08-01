# Tasks

Derived from design.md §20-21 (Agent Workstreams / Execution Order).

## Phase 0 — Technical spike
- [x] Agent A: document real Bambu profile directory layout, `.json`/`.info` relationship, inheritance rules, sync transformations; produce first compatibility fixtures — done in `findings.md` + `testdata/fixtures/x1c-to-p1s/` (real chain: leaf → `Bambu ABS @BBL X1C` → `Bambu ABS @base` → `fdm_filament_abs` → `fdm_filament_common`)
- [x] Agent B (partial): domain model (`internal/domain`), parser (`internal/parser`), recursive name-based resolver (`internal/resolver`), semantic normaliser/hash (`internal/normalize`), golden test against the real fixture chain — passing
- [ ] Agent B (remaining): fuzz tests (malformed JSON, unicode, deep graphs, duplicate names, renamed parents)
- Exit gate: `semantic(source) == semantic(reimport(export(source)))` for one real fixture — resolver+hash half proven (deterministic hash on real chain); full export/reimport round trip needs Phase 1's bundle work.

## Phase 1 — Portable core
- [x] Agent C: repository interfaces, SQLite impl + migrations, immutable versions, contract tests — `internal/storage`, `internal/storage/sqlite`, `internal/storage/storagetest` (Bindings/Deployments repos added when Phase 2/3 need them, per §24 narrow-slice guardrail)
- [x] Agent D: `.profilepack` schema, manifest, dependency-closure export, checksums, lossless round-trip tests — `internal/bundle`
- [x] Exit gate: discover → resolve → export → delete source → import → resolve identically — proven in `internal/bundle/roundtrip_test.go` against the real ABS fixture chain (set discarded between export and import)

## Phase 2 — Rebinding
- [ ] Agent E: source/target binding model, target-parent mapping, chain cloning/flattening, semantic diff
- Exit gate: X1C 0.4 → P1S 0.4 fixture passes; target resolves independently

## Phase 3 — Bambu round trip (highest priority)
- [ ] Agent F: Bambu adapter (discover/validate/stage/publish/observe/verify), atomic publish + backups, reconciliation state machine, rollback
- Exit gate: import → rebind → publish → Studio recognition → sync/reload → post-sync re-read → semantic equality. Do not begin broad UI work before this passes.

## Phase 4 — UI polish and operational support
- [ ] Agent G: templ/HTMX UI (profile browser, detail, dependency graph, rebind, deployments, rollback) — plain REST + page reloads, built starting in Phase 1, not deferred (see decisions.md #7)
- [ ] Agent H: Ristretto cache, revision-based keys, dependency-aware invalidation, singleflight, WebSocket hub, bounded queues — **deferred until real usage shows a need**, not built up front (decisions.md #7)
- [ ] Agent I: multi-stage Dockerfile (amd64/arm64), compose example, CI (unit/integration/fuzz + Bambu compatibility job — CI job may be allowed-failure, but the app itself never claims verification it didn't do, decisions.md #6), release artifacts
- Exit gate: `docker compose up` runs a working local instance against a bind-mounted Bambu directory

## Deferred (post-v1)
- PostgreSQL adapter, Google Sheets projection, OrcaSlicer support, experimental Bambu Cloud adapter, multi-instance deployment

## Primary Acceptance Test (design.md §22, fixture swapped per decisions.md #8)
- [ ] "Syscode - AmazonBasics ABS 0.6" @ X1C → P1S: full discover→export→import→rebind→publish→sync→verify→slice loop, `expected semantic profile == post-sync semantic profile`
