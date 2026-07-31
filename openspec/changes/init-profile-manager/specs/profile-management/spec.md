# Capability: Profile Management

## Requirements

### Correctness contract (design.md §4)
A publication is successful only if:
1. Bambu Studio recognises the generated profile as a valid user profile
2. The profile is selectable for the intended printer/nozzle
3. Every referenced dependency resolves
4. Effective resolved settings match the expected rebound profile
5. Bambu sync/reload completes without invalidating the profile
6. The profile survives Studio restart/reload
7. The post-sync profile resolves to the same semantic configuration
8. A small reference model can be sliced with the profile

File existence alone is never sufficient to claim success. Deployment status must be one of: `Saved locally` → `Published to Bambu directory` → `Recognised by Studio` → `Sync observed` → `Round-trip verified` → `Semantic settings identical`. Anything below `ROUND_TRIP_VERIFIED` is unverified.

### Domain model (design.md §7)
`Profile`, `ProfileVersion` (immutable), `ProfileDependency`, `PrinterBinding`, `Deployment`, `DeploymentObservation`, `VerificationResult`, `DomainEvent`, `IndexedFile`.

### Dependency resolution (design.md §12)
Recursive `inherits` resolution; detect missing/circular/duplicate/renamed parents and excessive depth; preserve unknown JSON fields; produce explicit/inherited/effective values, dependency graph, semantic hash.

### Sync/reconciliation state machine (design.md §16)
States: `DRAFT → VALIDATED → STAGED → INSTALLED_LOCALLY → OBSERVED_BY_STUDIO → SYNC_OBSERVED → ROUND_TRIP_VERIFIED → ACTIVE`. Failure states: `REJECTED_BY_STUDIO`, `DEPENDENCY_MISSING`, `IDENTITY_COLLISION`, `SYNC_TIMEOUT`, `CLOUD_MODIFIED`, `SEMANTIC_MISMATCH`, `SLICE_VALIDATION_FAILED`. Never auto-overwrite a cloud-modified profile; retain the previous known-good revision until the new one reaches `ROUND_TRIP_VERIFIED`.

Full detail: `../../design.md`.
