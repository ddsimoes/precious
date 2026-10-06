# Proposal

## Why

After M2, the inventory changes only when the owner runs `start-scan`. Each scan re-lists everything and writes new history rows even when nothing changed. Entries that disappear stay "current" forever. This change is milestone **M2a, incremental indexing** (§14, §5.5). It keeps the observed frontier current by polling alone, so that **A26, A28, A29, A30, and A31** pass. **A27** passes for process downtime and for coverage gaps. Its notification-overflow and watch-limit cases move to M3a together with the notification backend; ADR 0002 records the move. A1–A5, A7–A9, A11, A15, A17, and A20 keep passing.

## What Changes

- **Durable dirty scopes and scheduled reconciliation (§5.5.1–§5.5.3, A26, A27):**
  - Every active directory has one reconciliation scope. An expanded directory's scope is its immediate listing; an atomic directory's scope is its bounded shallow probe ("boundary refresh").
  - Hints coalesce into a monotonic dirty version and a set of reasons. A polling scheduler dispatches due scopes to the source's scan job, spread over the target intervals (default 15 min for expanded directories and 24 h for atomic ones).
  - Lag, overdue scopes, failing scopes, and the polling-only notification status are visible.
  - Reconfirming a source identity marks every scope of the source dirty.
- **Safe missing entries (§5.5.4, A28):** a child is tombstoned only after a complete listing of its parent, a rooted recheck that confirms it is absent, and no invalidation during the pass. Tombstoned and replaced nodes stay browsable as history.
- **Selective invalidation (§5.5.5, A26, A29):**
  - A listing re-probes only new, reactivated, or changed child directories.
  - An unchanged descriptor or listing adds no history rows.
  - A known boundary change makes the unit's aggregate measurement stale. Scheduled refresh never descends into an atomic unit.
- **Results bound to what they observed (§5.5.5, A30):** probes and aggregate walks commit as effective only if the epoch, observation revision, intent revision, and dirty version are unchanged since they started. Otherwise they are kept as history, and the scope stays dirty.
- **Occurrence identity (§5.5.6, A31):** a different inode at the same name is a new occurrence. Hardlinks, inode reuse, and renames never merge nodes or carry owner assertions over; path pins still apply.
- **M2 gaps (M2 design addendum):**
  - **G1:** a stranded frontier item is re-dispatched.
  - **G2:** per-device worker limits hold before a source's identity is first recorded.
  - **G3:** override evidence is compared only between probes of the same budget.
  - **G4:** walk-derived risk persists until a complete walk disproves it.
  - **G5:** one shared function decides which protection pin blocks a node.
- **UI and API (§5.5.7, §9.3):**
  - New command `refresh-scope` with options `boundary` and `subtree`.
  - New reads: `GET /api/sources/{id}/index-health` and `GET /api/nodes/{id}/children?state=history`.
  - An index-health page, and freshness per scope in the explorer and the inspector.
- **BREAKING (internal):** migration `0003` adds dirty scopes and tombstone, run, descriptor, and aggregate columns. `jobs.Spec` and `sources.Registry` drop `DeviceKey`, and commands register themselves.

Deferred, with owning milestone:
- The notification backend (`fsnotify`/inotify), the watch budget, the coalescing window, the inbox polling interval, and A27's overflow and watch-limit cases → **M3a**.
- Priority classes and fairness between interactive, reconciliation, and bulk work (§8.4, A36) → **M3a**.
- Superseding model evidence on configuration changes, and `reclassify-scope` → **M3**.
- Content-association invalidation, hashing, and `verify selected contents` → **M4**. Explicitly enabled periodic deep verification is also deferred to M4.
- Application-owned moves through the journal, `location_revision`, `evidence_dependencies`, and dirtying of source and destination parents (A45) → **M5**.
- Uncertain rename relationships between a tombstone and a new arrival → **M6**.

## Capabilities

### New Capabilities
- `incremental-reconciliation`: dirty scopes, hint coalescing, the polling schedule and its dispatch, lag and coverage gaps, `refresh-scope`, and convergence under change.

### Modified Capabilities
- `directory-discovery`: scheduled work after the first scan; tombstones after verified absence; identity at a name; no history for unchanged evidence; probe results bound to the state they observed.
- `inventory-boundaries`: identity for reactivation; the no-hidden-catalog invariant covers reconciliation and tombstones.
- `directory-inspection`: aggregate staleness after a boundary change (A29); risk that persists until disproved (G4); walk results bound to the state they observed (A30).
- `owner-intent`: evidence-changed flags compare same-budget probes only (G3).
- `job-runner`: device limits before identity is known (G2); scheduled and refresh work joins the one active scan.
- `inventory-explorer`: index health, freshness per scope, history listing, new read endpoints.

## Impact

- **New packages:**
  - `internal/reconcile`: scope bookkeeping.
  - `internal/watch`: the polling scheduler.
- **Changed packages:** `discovery`, `inspection`, `classify`, `jobs`, `sources`, `intent`, `commands`, `inventory`, `web/explorer`, `domain`, `config`, and `cmd/curator` (the scheduler ticker).
- **Configuration:** new `[reconciliation]` keys.
- **Database:** migration `0003`.
- **Docs:** `docs/operator.md`, and ADR `docs/adr/0002`.
- **Tests:** the `slow` build tag for oversized fixtures and property seeds.
- **Dependencies:** none new.
