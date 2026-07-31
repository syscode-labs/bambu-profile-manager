# Bambu Profile Manager — Agent Implementation Plan

## 1. Objective

Build a local-first profile management application for Bambu Studio that can:

1. Discover filament profiles from the user’s local Bambu Studio profile library.
2. Resolve the complete inheritance/dependency chain of a selected filament profile.
3. Export the profile as a portable, self-contained bundle.
4. Re-import that bundle independently of the original Bambu installation.
5. Rebind the profile to another printer/nozzle combination.
6. Publish the rebound profile back into Bambu Studio.
7. Verify, after Bambu sync and Studio restart/reload, that the effective profile settings remain semantically identical to the expected result.

The critical product capability is **correct round-trip synchronisation back into Bambu Studio**.

This is a profile-management tool, not a filament inventory system.

---

## 2. Product Definition

> A local Go web application for dependency-aware Bambu Studio profile export, rebinding, publication, and round-trip verification.

Primary workflow:

```text
Scan local Bambu profiles
        ↓
Select filament profile
        ↓
Resolve full dependency closure
        ↓
Export portable profile bundle
        ↓
Choose target printer/nozzle
        ↓
Map or replace printer dependencies
        ↓
Review semantic diff
        ↓
Publish to Bambu Studio
        ↓
Observe Bambu sync/reload
        ↓
Re-read and resolve deployed profile
        ↓
Verify semantic equivalence
```

---

## 3. Strict Scope

### In scope

- Bambu Studio user filament profiles.
- System and user parent profiles required to resolve filament inheritance.
- Printer and nozzle compatibility metadata.
- Local profile discovery and indexing.
- Recursive dependency resolution.
- Portable profile bundles.
- Import and restoration.
- Cross-printer/nozzle rebinding.
- Semantic diffing.
- Controlled publication into Bambu Studio.
- Detection of Bambu-side changes.
- Round-trip verification after sync/restart.
- Version history and rollback.
- SQLite storage.
- Optional PostgreSQL adapter interface, but not required for the first release.
- REST/HTML UI with WebSocket status events.
- Bounded backend caching for expensive derived reads.
- Docker deployment.

### Explicitly out of scope

Do not implement:

- Physical filament inventory.
- Spool tracking.
- AMS slot management.
- RFID.
- Remaining weight.
- Purchase history.
- Humidity monitoring.
- Usage accounting.
- Printer control.
- General print-farm management.
- Generic slicer settings editing beyond what is required for profile portability.
- Google Sheets sync in the first release.
- Direct Bambu Cloud API integration in the first release.
- OrcaSlicer support in the first release.
- Multi-user SaaS behaviour.
- Distributed deployment.
- Redis.
- Kubernetes deployment.

Any agent proposing these as first-release requirements is expanding scope incorrectly.

---

## 4. Correctness Contract

A publication is successful only if all of the following are true:

1. Bambu Studio recognises the generated profile as a valid user profile.
2. The profile is selectable for the intended printer and nozzle.
3. Every referenced dependency resolves.
4. The effective resolved settings match the expected rebound profile.
5. Bambu sync/reload completes without invalidating the profile.
6. The profile remains present after Studio restart or profile reload.
7. The post-sync profile resolves to the same semantic configuration.
8. A small reference model can be sliced with the profile.

Do not report success merely because files were written.

The UI must distinguish:

```text
Saved locally
Published to Bambu directory
Recognised by Studio
Sync observed
Round-trip verified
Semantic settings identical
```

Anything below `ROUND_TRIP_VERIFIED` is unverified.

---

## 5. High-Level Architecture

```text
Browser
 ├── HTML/REST
 │    ├── profile browsing
 │    ├── import/export
 │    ├── rebind commands
 │    ├── semantic diffs
 │    └── deployment actions
 │
 └── WebSocket
      ├── scan progress
      ├── dependency resolution progress
      ├── deployment state
      ├── filesystem change events
      ├── sync observation
      └── verification result

Go application
 ├── HTTP/UI layer
 ├── application services
 ├── profile parser
 ├── dependency resolver
 ├── semantic normaliser
 ├── rebind engine
 ├── bundle importer/exporter
 ├── Bambu adapter
 ├── reconciliation engine
 ├── cache
 ├── repository interface
 └── SQLite implementation

Persistent data
 ├── /data/profiles.db
 ├── /data/backups/
 ├── /library/*.profilepack
 └── /bambu-user/  bind-mounted Bambu user directory
```

---

## 6. Technology Stack

### Required

- Go.
- `net/http` or `chi`.
- `templ` for server-side templates.
- HTMX for UI interactions.
- Minimal Alpine.js only where necessary.
- `database/sql`.
- SQLite using a pure-Go driver such as `modernc.org/sqlite`.
- `github.com/coder/websocket`.
- `github.com/dgraph-io/ristretto/v2`.
- `golang.org/x/sync/singleflight`.
- `golang.org/x/sync/errgroup`.
- `fsnotify`.
- `slog`.
- Docker multi-stage build.
- Multi-architecture images for `amd64` and `arm64`.

### Avoid

- React.
- Node.js runtime.
- Electron.
- A separate frontend service.
- Unbounded goroutine creation.
- Unbounded WebSocket queues.
- Using Google Sheets as the authoritative database.
- Writing business logic directly in HTTP handlers.

---

## 7. Domain Model

Minimum entities:

```text
Profile
ProfileVersion
ProfileDependency
PrinterBinding
Deployment
DeploymentObservation
VerificationResult
DomainEvent
IndexedFile
```

Suggested concepts:

### Profile

Represents a canonical local profile independent of a specific deployment.

### ProfileVersion

Immutable revision containing:

- original source JSON;
- resolved effective JSON;
- source metadata;
- semantic hash;
- parser version;
- dependency snapshot.

### ProfileDependency

Represents an `inherits` relationship or another required profile reference.

### PrinterBinding

Maps a canonical filament profile to a target:

- printer;
- nozzle;
- target parent;
- compatibility list;
- target-specific overrides.

### Deployment

Represents one generated Bambu user profile revision and its expected semantic hash.

### DeploymentObservation

Represents what was observed later in the Bambu directory after Studio or cloud sync modified it.

### VerificationResult

Contains:

- expected semantic hash;
- observed semantic hash;
- structured setting diff;
- dependency status;
- Studio recognition status;
- slice smoke-test status.

---

## 8. Storage Design

Create repository interfaces based on use cases, not generic SQL wrappers.

Example:

```go
type Repository interface {
    Profiles() ProfileRepository
    Versions() ProfileVersionRepository
    Dependencies() DependencyRepository
    Bindings() BindingRepository
    Deployments() DeploymentRepository
    Events() EventRepository

    WithinTransaction(
        ctx context.Context,
        fn func(ctx context.Context, tx Repository) error,
    ) error

    Close() error
}
```

Implement SQLite first.

Keep PostgreSQL possible through a separate adapter:

```text
internal/storage/
├── storage.go
├── sqlite/
└── postgres/     deferred
```

Do not force SQLite and PostgreSQL to share byte-for-byte identical SQL.

Use application-generated UUIDv7 or ULID identifiers.

SQLite configuration:

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
```

---

## 9. Cache Design

Use cache-aside.

Database remains authoritative.

Cache:

- resolved dependency closures;
- flattened profiles;
- semantic diffs;
- target-parent mappings;
- profile-list projections;
- parsed system profiles.

Do not cache:

- uncommitted writes;
- mutable domain objects;
- transactions;
- deployment locks;
- credentials.

Use revision-based keys:

```text
profile:{profileID}:revision:{revision}
resolved:{profileID}:{revision}:{bindingID}:{bindingRevision}
diff:{sourceRevision}:{targetRevision}
```

Use `singleflight` to suppress duplicate calculations.

Mutation order:

```text
BEGIN TRANSACTION
  update profile
  insert immutable revision
  update dependencies
  insert domain event
COMMIT

invalidate affected cache keys
publish WebSocket event
```

Dependency-aware invalidation is required: when a parent changes, invalidate all descendants.

---

## 10. WebSocket Design

WebSockets are for live state and progress only.

REST remains authoritative for:

- imports;
- exports;
- edits;
- rebind operations;
- deployments;
- reads.

One endpoint is sufficient:

```text
GET /api/v1/events
```

Use typed events:

```json
{
  "id": "evt_...",
  "sequence": 1842,
  "type": "deployment.round_trip_verified",
  "timestamp": "2026-08-01T00:00:00Z",
  "entity_id": "deployment_...",
  "revision": 7,
  "payload": {}
}
```

Required event categories:

```text
library.scan.started
library.scan.progress
library.scan.completed
profile.updated
dependency.missing
dependency.invalidated
rebind.completed
deployment.started
deployment.installed
deployment.drifted
sync.observed
verification.completed
verification.failed
library.refresh_required
```

Use:

- bounded per-client queues;
- one writer goroutine per client;
- slow-client disconnection;
- progress-event coalescing;
- persistent domain events for replay;
- memory-only transient progress events.

---

## 11. Portable Bundle Format

Use a ZIP-based format such as:

```text
Fiberon-PET-CF17.profilepack
```

Contents:

```text
manifest.json
source/
  selected.json
  parent-1.json
  parent-2.json
resolved/
  complete.json
bindings/
  source.json
  target-template.json
checksums.json
```

The bundle must include:

- original selected profile;
- complete custom dependency closure;
- snapshots or references for required system parents;
- resolved flattened fallback;
- printer/nozzle compatibility;
- parser version;
- source Bambu Studio version where known;
- checksums;
- canonical profile ID;
- immutable revision.

Export must remain usable even if the original Bambu installation is removed.

---

## 12. Dependency Resolution

Implement recursive `inherits` resolution.

Required behaviour:

1. Locate parent by durable identifier where available.
2. Fall back to name matching only with explicit ambiguity handling.
3. Resolve transitive parents.
4. Merge parent to child in deterministic order.
5. Preserve unknown JSON fields.
6. Detect:
   - missing parents;
   - circular inheritance;
   - duplicate names;
   - renamed parents;
   - excessive depth;
   - incompatible profile types.
7. Produce:
   - original explicit values;
   - inherited values;
   - fully resolved effective values;
   - dependency graph;
   - semantic hash.

Never discard fields merely because the current parser does not understand them.

---

## 13. Rebinding Engine

Primary operation:

```text
Source: X1 Carbon / 0.4 mm
Target: P1S / 0.4 mm
```

The engine must:

1. Change compatible printer/nozzle metadata.
2. Inspect the entire source dependency chain.
3. Find the equivalent target parent where possible.
4. Compare source and target parent effective values.
5. Preserve material-owned tuning.
6. Adopt target-printer-owned defaults where appropriate.
7. Mark ambiguous values for review.
8. Offer flattening when no safe parent mapping exists.
9. Generate a semantic diff before publication.

Supported strategies:

```text
Map to equivalent target parent
Clone custom dependency chain
Flatten into standalone target profile
Manually select target parent
```

The app must never silently guess when multiple parent mappings are plausible.

---

## 14. Semantic Normalisation

Correctness must be semantic, not byte-for-byte.

Build a normaliser that:

- resolves dependencies;
- canonicalises numeric values;
- canonicalises arrays where order is not meaningful;
- preserves order where it is meaningful;
- ignores volatile metadata;
- ignores JSON property order;
- ignores file paths;
- ignores timestamps;
- excludes cloud-generated identifiers from semantic equality;
- includes all settings that affect slicing or profile compatibility.

Output:

```text
canonical semantic JSON
semantic hash
structured setting map with provenance
```

Every resolved setting should record its origin:

```text
explicit child value
custom parent
system parent
target parent
generated override
```

---

## 15. Bambu Publication Adapter

Treat Bambu Studio as an external system with a versioned adapter.

Interface:

```go
type BambuAdapter interface {
    Discover(ctx context.Context) ([]DiscoveredProfile, error)
    Validate(ctx context.Context, candidate CandidateProfile) error
    Stage(ctx context.Context, deployment Deployment) error
    Publish(ctx context.Context, deployment Deployment) error
    Observe(ctx context.Context, deploymentID string) (Observation, error)
    Verify(ctx context.Context, deployment Deployment, observation Observation) (VerificationResult, error)
}
```

Initial strategy:

1. Generate valid Bambu user profile files.
2. Stage them in a controlled location.
3. Back up any colliding files.
4. Publish atomically.
5. Wait for filesystem stability.
6. Re-index the Bambu profile directory.
7. Determine whether Studio observed or transformed the profile.
8. Resolve the resulting profile.
9. Compare semantic hashes.

Do not implement direct Bambu Cloud API writes in the first release.

Direct cloud publication may later be added behind a separate experimental adapter.

---

## 16. Sync/Reconciliation State Machine

Use explicit states:

```text
DRAFT
VALIDATED
STAGED
INSTALLED_LOCALLY
OBSERVED_BY_STUDIO
SYNC_OBSERVED
ROUND_TRIP_VERIFIED
ACTIVE
```

Failure states:

```text
REJECTED_BY_STUDIO
DEPENDENCY_MISSING
IDENTITY_COLLISION
SYNC_TIMEOUT
CLOUD_MODIFIED
SEMANTIC_MISMATCH
SLICE_VALIDATION_FAILED
```

Never automatically overwrite a cloud-modified profile in a loop.

On mismatch, present:

```text
Show semantic diff
Accept Bambu version
Publish local profile as a new revision
Keep both revisions
Restore previous known-good revision
```

Use immutable deployment revisions.

Do not delete the previous working revision until the new revision reaches `ROUND_TRIP_VERIFIED`.

---

## 17. Filesystem Observation

Use `fsnotify`, but do not process every event immediately.

Required debounce/stability behaviour:

```text
file event detected
    ↓
wait for quiet period
    ↓
ensure all relevant files parse
    ↓
rebuild affected index
    ↓
resolve dependencies
    ↓
compare semantic state
```

Track indexed files by:

```text
absolute path
size
mtime
content hash
parser version
last observed state
```

Do not rely on modification time alone.

---

## 18. UI Pages

Keep the UI small.

### Profiles

- list all discovered profiles;
- search/filter;
- show type, parent, printer compatibility, source, validation state.

### Profile detail

- explicit values;
- inherited values;
- resolved values;
- value provenance;
- revision history;
- semantic hash.

### Dependency graph

- parent/child graph;
- missing dependencies;
- ambiguous matches;
- repair action.

### Export/import

- export portable bundle;
- inspect bundle;
- import into library;
- restore previous revision.

### Rebind

- source printer/nozzle;
- target printer/nozzle;
- target parent;
- semantic diff;
- unresolved values;
- publish action.

### Deployments

- state-machine status;
- expected vs observed;
- Studio recognition;
- sync observation;
- semantic verification;
- rollback.

### Settings

- Bambu directory;
- database path;
- backup path;
- cache limit;
- scanner interval;
- logging level.

---

## 19. Testing Strategy

### Unit tests

- JSON parsing.
- Unknown-field preservation.
- Parent merge precedence.
- Cycle detection.
- Missing-parent detection.
- Semantic normalisation.
- Hash stability.
- Rebind classification.
- Target-parent mapping.
- Cache invalidation.
- Event creation.

### Golden-file tests

Maintain fixtures:

```text
testdata/
├── simple-system-parent/
├── custom-parent-chain/
├── deep-inheritance/
├── missing-parent/
├── circular-inheritance/
├── duplicate-name/
├── x1c-to-p1s/
├── x1c-to-a1/
├── nozzle-04-to-06/
├── cloud-modified/
└── unknown-fields/
```

Each fixture should contain:

```text
input/
expected-dependencies.json
expected-resolved.json
expected-semantic.json
expected-export/
expected-target/
```

### Fuzz/property tests

Important invariants:

```text
resolve(export(import(bundle))) == original semantic profile
```

```text
resolve(child, parent) ==
merge(resolve(parent), child explicit overrides)
```

Fuzz:

- malformed JSON;
- unexpected arrays;
- numeric strings;
- Unicode;
- duplicate names;
- very deep graphs;
- unknown fields;
- renamed parents;
- partial metadata.

### Repository contract tests

Create a common suite for all repository implementations.

Run it against SQLite.

PostgreSQL must pass the same suite when implemented later.

### Filesystem reconciliation tests

Simulate:

- Bambu changing a deployed file;
- Bambu deleting it;
- Bambu renaming it;
- metadata-only changes;
- semantic changes;
- dependency disappearance;
- partial writes;
- repeated sync events.

### WebSocket/cache tests

Verify:

- no event is sent before commit;
- slow clients are disconnected;
- progress events are coalesced;
- critical events cause refresh/replay;
- stale cache keys are not reused after revision changes;
- parent updates invalidate descendants.

### Bambu compatibility tests

Create a separate compatibility suite:

1. Generate a candidate profile.
2. Load or import it into a pinned Bambu Studio environment where possible.
3. Verify recognition.
4. Slice a tiny reference model.
5. Restart/reload Studio.
6. Re-read the resulting profile.
7. Resolve and compare semantic hashes.

Treat latest-Bambu compatibility as a scheduled or allowed-failure job until stabilised.

---

## 20. Agent Workstreams

## Agent A — Bambu Format Investigation

Deliverables:

- documented profile directory layout;
- sample sanitised user profiles;
- sample system profiles;
- `.json` and `.info` relationship;
- identifier and naming behaviour;
- inheritance resolution rules;
- compatibility field behaviour;
- known sync transformations;
- first compatibility fixture set.

Do not build UI.

Exit gate:

- at least one real custom filament resolves correctly;
- its dependency graph is documented;
- unknown fields are identified and preserved.

---

## Agent B — Domain and Resolver

Deliverables:

- domain model;
- parser;
- dependency graph;
- recursive resolver;
- value provenance;
- semantic normaliser;
- semantic hashing;
- golden tests;
- fuzz tests.

Exit gate:

```text
resolve(export(import(bundle))) == original semantic profile
```

for all fixture classes.

---

## Agent C — Storage and Versioning

Deliverables:

- repository interfaces;
- SQLite implementation;
- migrations;
- immutable profile versions;
- deployment records;
- domain event log;
- transaction boundaries;
- repository contract tests;
- backup/restore support.

Do not implement PostgreSQL yet.

Exit gate:

- full contract suite passes;
- interrupted writes cannot leave partial profile revisions.

---

## Agent D — Bundle Import/Export

Deliverables:

- `.profilepack` schema;
- manifest;
- dependency closure export;
- flattened fallback;
- checksums;
- import validation;
- collision handling;
- lossless round-trip tests.

Exit gate:

- exported bundle works after deleting the source directory;
- imported bundle resolves identically.

---

## Agent E — Rebinding Engine

Deliverables:

- source/target binding model;
- target-parent mapping;
- custom-chain cloning;
- standalone flattening;
- material/printer/ambiguous classification;
- semantic diff;
- unresolved-decision model.

Exit gate:

- X1C 0.4 to P1S 0.4 fixture passes;
- no source-printer dependency remains unless explicitly retained;
- target result resolves independently.

---

## Agent F — Bambu Publication and Reconciliation

This is the highest-priority workstream.

Deliverables:

- Bambu adapter interface;
- local discovery;
- staging;
- atomic publication;
- backups;
- filesystem stability detection;
- deployment observations;
- post-sync re-read;
- semantic verification;
- state machine;
- rollback;
- mismatch handling.

Exit gate:

A real profile can complete:

```text
import
→ rebind
→ publish
→ Studio recognition
→ sync/reload
→ post-sync re-read
→ semantic equality
```

Do not mark this work complete based only on file creation.

---

## Agent G — Local Web UI

Deliverables:

- `templ`/HTMX UI;
- profile browser;
- profile detail;
- dependency graph;
- bundle import/export;
- rebind page;
- deployment state;
- semantic diff;
- rollback controls;
- WebSocket progress.

Exit gate:

A user can complete the main workflow without editing files manually.

---

## Agent H — Cache, Events, and Background Jobs

Deliverables:

- Ristretto cache wrapper;
- revision-based keys;
- dependency-aware invalidation;
- `singleflight`;
- bounded job queue;
- WebSocket hub;
- event replay;
- graceful shutdown;
- metrics.

Exit gate:

- no unbounded queues;
- no success events before transaction commit;
- slow browser clients cannot block profile scans or deployments.

---

## Agent I — Docker, CI, and Release Engineering

Deliverables:

- multi-stage Dockerfile;
- `amd64` and `arm64` images;
- Docker Compose example;
- bind-mount documentation;
- health/readiness endpoints;
- CI unit/integration/fuzz jobs;
- Bambu compatibility job;
- release artifact generation;
- reproducible version metadata.

Exit gate:

```text
docker compose up
```

starts a working local instance using SQLite and a bind-mounted Bambu directory.

---

## 21. Execution Order

### Phase 0 — Technical spike

Agents A and B.

Goal:

- prove real profile discovery;
- prove dependency resolution;
- prove semantic flattening;
- collect real fixtures.

Stop if the format cannot be resolved reliably.

### Phase 1 — Portable core

Agents B, C, and D.

Goal:

```text
discover
→ resolve
→ export
→ delete source
→ import
→ resolve identically
```

### Phase 2 — Rebinding

Agent E.

Goal:

```text
source profile
→ select target printer/nozzle
→ map dependencies
→ semantic diff
→ generate independent target profile
```

### Phase 3 — Bambu round trip

Agent F, supported by A and B.

Goal:

```text
publish
→ observe Studio
→ sync/reload
→ re-read
→ semantic verification
→ rollback
```

Do not begin broad UI work until this phase demonstrates feasibility.

### Phase 4 — UI and operational support

Agents G, H, and I.

Goal:

- usable local web UI;
- live status;
- caching;
- Docker;
- CI;
- release packaging.

### Deferred phase

Only after the Bambu round trip is stable:

- PostgreSQL implementation;
- Google Sheets projection;
- OrcaSlicer;
- experimental direct Bambu Cloud adapter;
- remote multi-instance deployment.

---

## 22. Primary Acceptance Test

Use one real custom filament profile.

Example:

```text
Source: Fiberon PET-CF17 @ X1C 0.4
Target: P1S 0.4
```

Test:

1. Discover it from the local Bambu directory.
2. Resolve every dependency.
3. Record the source semantic profile.
4. Export a portable bundle.
5. Remove access to the source directory.
6. Import the bundle into a clean database.
7. Rebind it to P1S 0.4.
8. Review and approve the semantic diff.
9. Generate the target Bambu profile.
10. Publish it as a new immutable revision.
11. Confirm Studio recognises it.
12. Trigger or observe Bambu sync.
13. Restart/reload Studio.
14. Re-read the resulting profile.
15. Resolve all dependencies again.
16. Compare expected and observed semantic profiles.
17. Slice a small reference model.
18. Mark the deployment active only if all checks pass.
19. Retain the previous known-good revision until verification succeeds.

Pass condition:

```text
expected semantic profile == post-sync semantic profile
```

and the profile can be used by Bambu Studio for the intended target printer/nozzle.

---

## 23. Definition of Done for Version 1

Version 1 is complete only when:

- profiles are scanned from a real local Bambu user directory;
- inheritance is resolved with provenance;
- portable bundles include all dependencies;
- bundles can be restored without the original directory;
- a profile can be rebound to another printer/nozzle;
- the resulting effective-setting diff is visible;
- Bambu publication is atomic and backed up;
- Studio recognition is observed;
- post-sync/restart state is re-read;
- semantic equality is verified;
- mismatches produce actionable diffs;
- rollback restores a known-good revision;
- the application runs locally in Docker;
- SQLite remains authoritative;
- no inventory features exist.

---

## 24. Agent Guardrails

Agents must:

- prefer a narrow working vertical slice over broad abstractions;
- preserve unknown Bambu fields;
- avoid claiming sync success without round-trip verification;
- use immutable revisions;
- write contract and golden tests before broadening scope;
- keep Bambu-specific logic behind an adapter;
- keep application logic independent from HTTP handlers;
- use bounded queues and caches;
- use transactions for profile/revision/event writes;
- document assumptions discovered from real fixtures.

Agents must not:

- introduce spool inventory;
- introduce AMS management;
- rely on direct Bambu Cloud APIs for version 1;
- silently overwrite cloud-modified profiles;
- use filename existence as proof of successful sync;
- delete the last known-good Bambu profile before verifying the replacement;
- discard unknown JSON fields;
- block releases on PostgreSQL, Google Sheets, Orca, or multi-user support.

---

## 25. First Task to Assign

Assign Agents A and B first.

Their immediate deliverable is a technical spike that takes one real local Bambu filament profile and produces:

```text
source profile
dependency graph
resolved effective profile
semantic normal form
portable bundle
round-trip reimport result
```

Required proof:

```text
semantic(source) == semantic(reimport(export(source)))
```

No UI is required for this first gate.
