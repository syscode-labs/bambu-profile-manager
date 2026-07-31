# Research Findings (2026-08-01)

Gathered before grilling the plan: one web-research pass (Bambu Studio format, prior art) + one read-only local scan of `~/Library/Application Support/BambuStudio/`.

## Confirmed locally
- `inherits` chains are **name-based**, not ID-based. Confirmed on-disk: `Syscode - AmazonBasics ABS 0.6.json` → `Bambu ABS @BBL X1C` → `Bambu ABS @base` → `fdm_filament_abs` (3 hops to root system template).
- `.info` companion files exist 1:1 with user `.json` profiles. Format is `key = value`, not JSON: `user_id`, `setting_id` (cloud `GFS...` ID, separate from the name-based `inherits` target), `base_id`, `updated_time`.
- Both X1C and P1S machine profiles exist locally — the plan's two-printer rebind scenario is realistic for this user.
- The plan's example filament, **Fiberon PET-CF17, does not exist locally** — closest is a distinct `Generic PETG-CF @BBL X1C - Copy.json`. §22's acceptance test needs a real substitute or the user needs to add that filament first.
- `system/` (BBL + Prusa vendor trees, read-only) vs `user/<numeric-account-id>/` (writable) split confirmed; `user/default/` is empty.

## From web research (not directly verified against source, cite with that caveat)
- **Cloud sync is a documented uncontrolled third-party writer.** A Bambu forum root-cause thread describes cloud sync silently reverting/corrupting local profile JSON (malformed array entries, stale-section overwrites) with no user notification. This breaks a naive "diff against cached copy" round-trip check — need `.info` timestamp/hash staleness detection at minimum, and possibly a "verify sync is disabled/paused" precondition.
- **No evidence Bambu Studio hot-reloads or coordinates writes with external processes** touching its profile directory while running (GitHub #1833 shows silent overwrite-with-no-notification behavior in the other direction — app-side updates clobbering without telling the user). fsnotify-based reconciliation should assume no cooperation; external writes may need Studio closed, and every write needs a post-hoc re-read to confirm it stuck.
- **A CLI slice path exists**: `--slice <plate>`, `--load-settings`, `--load-filaments`, `--export-3mf`, `--export-slicedata` (see BambuStudio wiki Command-Line-Usage). No confirmed direct G-code export flag. Docs suggest `--load-settings`/`--load-filaments` want a flattened full config, not the delta/`inherits` format the rest of the plan is built around — likely needs the resolver's flattened output, not raw profile files, fed to the CLI.
- No existing OSS tool does export/rebind/republish with round-trip verification end-to-end — the plan fills a real gap rather than duplicating one.

## Assumptions the plan should revise
1. §12.1 order ("locate by durable identifier, fall back to name matching") is backwards — name matching is the primary resolution path for `inherits`, not a fallback.
2. §17 fsnotify reconciliation assumes cooperative coexistence with a running Studio instance; treat that as unverified and risky until tested empirically.
3. §4.8 / §19's "slice a small reference model" is scriptable via the CLI flags above, but only after flattening — the plan's automation of this step needs a spike, not an assumption.
4. Cloud sync as a third writer (§16 already has `CLOUD_MODIFIED` as a failure state, so this isn't unanticipated) is reportedly bad enough (data corruption, not just divergence) that the plan should ask whether cloud sync can just be disabled for this workflow rather than reconciled against.
