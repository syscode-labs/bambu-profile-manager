# Grill Decisions (2026-08-01)

Resolved against `findings.md`. These override/refine `design.md` where they conflict.

1. **Cloud sync stays on.** User cannot/won't disable it. Phase 3's full reconciliation state machine (diff, mismatch UI, `CLOUD_MODIFIED` handling, drift detection) is load-bearing for v1, not optional — design.md §16 stands as-is.
2. **Repo renamed** `bambu-filament-manager` → `bambu-profile-manager` (GitHub + local dir) to match the plan's own product definition (§2) and avoid inviting inventory-feature scope drift (§3/§24). Done.
3. **§12.1 inheritance resolution order is wrong — fix it.** `inherits` is name-keyed (confirmed on-disk; `setting_id` is a separate cloud-sync ID). Name-based resolution against the known profile set (system + user) is the PRIMARY path; duplicate-name/renamed-parent detection is first-class logic, not a fallback edge case.
4. **Bambu Studio must be closed during publish.** No evidence of safe live coexistence or hot-reload (GitHub #1833 shows silent overwrite with no notification). The publish flow requires the user to quit Studio before the tool writes, then reopen it — replaces §17's live-fsnotify-while-running assumption for the publish path. (fsnotify may still be useful for detecting file state stability while writing.)
5. **`OBSERVED_BY_STUDIO` detection: infer from Studio's own rewrite.** After the user reopens Studio, watch for Studio rewriting the `.json`/`.info` (updated_time bump, setting_id assignment) as the automatic signal. **Flagged as an open risk** — this behavior is unverified; confirm empirically during the Phase 0/3 spike before relying on it. Fallback if it doesn't hold: manual user confirmation in the UI.
6. **Round-trip verification must never be faked.** The app only marks a deployment `ROUND_TRIP_VERIFIED` when it actually confirmed the match. §19's "allowed-failure" framing applies only to the CI *job* (infrastructure flakiness tolerance), never to what the running app is allowed to claim to the user.
7. **UI from the start, infra deferred.** Keep the HTMX/templ web UI in early phases (not CLI-only) — but drop Ristretto cache, singleflight, WebSocket progress hub, and event replay until Phase 4, once real usage shows they're needed. Early phases use plain REST + page reloads. This revises §6/§9/§10's "build it all up front" framing and §21's phase grouping stays the same, but Phase 4's cache/WebSocket work is confirmed deferred, not parallel.
8. **Acceptance-test fixture swapped.** §22's "Fiberon PET-CF17 @ X1C → P1S" is hypothetical (not present locally). Primary acceptance-test fixture is the real local profile: **"Syscode - AmazonBasics ABS 0.6" @ X1C → P1S** (confirmed 3-hop inheritance chain, confirmed both printers exist locally).

## Carried-forward open risks (from findings.md, not yet resolved)
- CLI slicing (`--slice`, `--load-settings`, `--load-filaments`, `--export-3mf`) needs an empirical spike — settings may need to be flattened before feeding to the CLI, and no direct G-code export flag is confirmed.
- `.info` file schema beyond `user_id`/`setting_id`/`base_id`/`updated_time` is not fully characterized — extend understanding during Phase 0.
