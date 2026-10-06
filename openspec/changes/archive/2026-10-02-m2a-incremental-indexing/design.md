# Design

## Context

See proposal.md for the motivation and the specs for required behavior. The code facts below shape the approach (checked on `master` at `e4163c7`):

- **Frontier state is per job.**
  - Nodes carry `probe_state`/`probe_scan_job_id` and `listing_state`/`listing_job_id`. A scan's `next()` takes the lowest node ID pending for its job.
  - A source scan marks the root and every active expanded directory for listing. Each listing marks every child directory probe-pending for the job.
  - `RequestListing` marks one directory, and refuses the source root.
  - `PendingWork` re-queues a succeeding scan that still has pending rows.
- **Listings and probes always write.**
  - A listing updates every seen child row (`last_observed_at`, `last_run_id`) and inserts one `reconciliation_runs` row.
  - A probe always inserts a descriptor row and a classification row.
  - Nothing is ever deactivated because it was not seen (`discovery/doc.go`, `TestA5EntryMissingOnRescan`).
- **Identity is checked by name and kind only.**
  - A same-name, same-kind entry is updated in place, even when its inode changed, so owner assertions carry over to a different object.
  - Reactivation compares name, kind, device, and inode.
  - The chain opener expects each node's recorded `dev`/`ino`.
- **The late-result check is vacuous in two places.** `classify.Apply` compares the `Observed` revisions it is given, but `discovery.commitDecision` and `inspection.reclassify` read `Observed` inside the committing transaction. The aggregate commit checks only the epoch and the atomic state.
- **Device keys are fixed at enqueue time.** `sources.Registry.DeviceKey` answers `source:<id>` until `identity_dev` is recorded, and `dev:<n>` afterwards (G2).
- **There is no scheduler.** `jobs.available_at` exists, but nothing future-dates it. `serve` runs only the availability ticker (1 min) and the checkpoint ticker (5 min). `clock.Clock` has only `Now()`.
- **Some tests are too slow.** Race durations measured on `master`:
  - `internal/discovery`: 152 s, of which `TestNodeBudgetPausesAndResumes` takes 117 s;
  - `internal/intent`: 61 s, of which `TestCollapseFiftyThousandDescendants` takes 59 s;
  - `internal/scenario`: 49 s, of which `TestNoHiddenCatalogProperty` takes 37 s.
- **`nodes` cannot be rebuilt.** Migrations may not contain `PRAGMA`, so schema changes are `ADD COLUMN` or new tables.

## Goals / Non-Goals

**Goals:**
- Polling alone converges the active frontier. Each pass costs O(changed entries) in history writes and O(listed entries) in filesystem calls, never O(hidden descendants).
- Every asynchronous result is compared and swapped against the state it observed: epoch, observation revision, intent revision, and dirty version.
- Schema and reconciliation primitives land in the foundation, so slices only add code.

**Non-Goals:**
- A notification backend, watch budgets, or event coalescing windows (ADR 0002, M3a).
- Priority classes or fair scheduling between sources (M3a).
- Pruning old jobs, runs, or descriptors.
- Automatic aggregate re-measurement.
- Any filesystem mutation.

## Decisions

### D1. One scope per active directory, keyed by node (§5.5.1, §8.3)

Table `dirty_scopes` has one row per directory node; the node ID is the scope key. The kind of work is derived from the node's state when the scope is dispatched:
- an expanded node, including the root, is **listed**;
- any other active non-boundary directory is **probed** within its shallow budget (boundary refresh).

Rows of inactive nodes and mount boundaries are ignored by every query (`JOIN nodes … active = 1 AND mount_boundary = 0`) and are kept.

Rejected:
- Separate listing and probe scope keys per node: an inventory-mode change would need a row migration.
- Schedule columns on `nodes`: `nodes` cannot be rebuilt, and the scope would widen every node row.
- A row per hint: §5.5.3 requires coalescing.

### D2. Versions: hints bump, deadlines do not (§4.6, §5.5.5)

- **Hints bump.** A hint increments `dirty_version`, adds its reason to `reasons` (a sorted JSON set), sets `first_hint_at` if unset, sets `last_hint_at`, and makes the scope due now. M2a has three hint reasons:
  - `metadata_changed`: a parent listing saw the directory's kind-preserving metadata change;
  - `owner_refresh`: `refresh-scope`;
  - `epoch_changed`: `source confirm`.
- **Deadlines do not.** A deadline, an owner refine, or a retry only makes the scope due (`Request`); it leaves `dirty_version` alone.
- **Workers capture the version.** A worker reads `dirty_version` when it starts observing a scope.
- **Settling** (in the same transaction that commits the observation):
  - On a complete outcome, it sets `clean_version = max(clean_version, started)`. If `dirty_version > started`, the scope stays dirty and due now.
  - On a clean outcome, it clears `reasons` and `first_hint_at` and sets `due_at = at + interval`. The interval is `NULL` when the schedule is disabled.
  - On `partial` or `error`, it increments `failures`, sets `due_at = at + min(interval, failure_backoff × 2^(failures−1))`, and keeps the scope dirty if it was.
  - On `stale`, the scope is due now.
  - Every outcome sets `last_pass_at`. A complete one also sets `last_complete_at`.

Rejected:
- Bumping on deadlines: every 24 h tick would invalidate in-flight walks.
- Wall-clock "dirty since" comparisons instead of versions: ordering across one transaction's timestamp is ambiguous.

### D3. Lazy seeding, spread by node ID (§5.5.3)

- Writers never create scope rows explicitly. `Hint`, `Request`, and `Settle` upsert.
- Each scheduler pass seeds the missing rows of active directories in an anti-join `INSERT`. A seeded row is due at `now + (fnv64(node_id) mod interval)`. This also covers every directory that existed before the upgrade.

Rejected:
- Creating rows in every writer: a missed path is silently never reconciled.
- Random jitter: tests would not be deterministic.

### D4. The scheduler is `internal/watch`, and it dispatches into the source's scan (§5.5.3, §8.2)

`watch.Scheduler.Tick(ctx)` runs one pass. `serve` calls it once right after the runner starts, then every `watch.DefaultTick` (1 min). Tests call `Tick` with a fake clock.

Per configured source, one `jobs.Tx` does the following:
1. **Skip** the source with a recorded hold reason when it is:
   - unconfigured, not `available`, or never scanned (epoch 0 or no root node);
   - holding a paused active scan;
   - within `failure_backoff` of a scan that ended with a source-level code (`source_unavailable`, `source_identity_changed`, `source_epoch_changed`, `internal`).
2. **Seed** missing scope rows (D3).
3. **Find due scopes:** `due_at <= now`, and `dispatched_job_id` not equal to the active job. Stop if none.
4. **Get the job:** `Tx.ActiveScan`. If there is none, enqueue `FrontierScanSpec`. A paused job was already skipped in step 1.
5. **Dispatch** with `discovery.DispatchDue`. It marks every due expanded scope listing-pending and every other due scope probe-pending for the job, as one set-based `UPDATE` of each kind, and records `dispatched_job_id`.

Effects:
- A job receives a scope at most once. A hint that arrives after dispatch waits for the next job, so a continuously changing directory cannot starve the others (§5.5.4).
- Observation-level failures, such as `permission_denied` or `observation_incomplete`, back off per scope only.

Rejected:
- A new job kind `reconcile`: it would run beside the scan of the same source, and `jobs_one_active_scan` exists to prevent that.
- One long-lived job per source: jobs must end, and the UI and leases assume they do.
- A timer inside `jobs.Runner`: scheduling policy does not belong in the runner.
- Resuming paused jobs: the node budget is an owner gate.

### D5. Frontier listings probe only what changed (§5.5.5, A26)

In a `frontier`-scope scan, a listed child directory becomes probe-pending only if it is new, reactivated, or changed, where changed means `differs()` is true. A changed child directory also gets a `metadata_changed` hint in the same batch transaction. An expanded child is then re-listed through its decision, as in M2.

A `source` scan (`start-scan`) still re-probes every child directory, as the existing spec requires.

Rejected: probing every child of every listed directory on each pass. That is O(children) probes every 15 min.

### D6. Unchanged evidence adds no history (§4.6, A26)

- **Probes.** A probe is unchanged when all of these hold:
  - it is not escalated;
  - its digest equals that of the node's latest non-escalated descriptor;
  - the node's `observation_revision` equals that descriptor's `node_revision`;
  - the node's current rule suggestion has the current `policy.Version()`.

  An unchanged probe writes no descriptor or classification row and does not escalate. It sets `descriptors.confirmed_at` on that descriptor, settles the scope `complete`, and leaves the probe state `probed`.
- **Listings.**
  - Unchanged children are never rewritten. A child row is written only when it is new, reactivated, changed, replaced, rechecked present, or newly probe-pending in a source scan.
  - A listing that ends `complete` with coverage `complete`, and that created, changed, reactivated, tombstoned nothing and wrote no issue, merges into the node's previous run when that run is complete in the same epoch. It deletes its own row, then sets `prev.confirmed_at = finished` and `prev.confirmations += 1`, and points the node's `last_run_id` back to `prev`. No row references the deleted run, because nothing was written.
- **Displayed last observation.** A row's displayed last observation is `max(nodes.last_observed_at, parent's latest complete-coverage run's max(finished_at, confirmed_at))`. That listing `lstat`ed the child, and any change would have rewritten it.

Rejected:
- Writing new rows on every pass: that is millions of descriptor, classification, and run rows per year.
- Pruning old rows: rows are referenced by foreign keys, and pruning loses the history.
- Touching every child's timestamp: on a 15-minute schedule, that write amplification would dominate the WAL.

### D7. Missing entries: seen-set, rooted recheck, compare-and-swap tombstone (§5.5.4, A28)

- **Seen-set.** While listing, the run keeps an in-memory set of `uint64` keys: the first 8 bytes of SHA-256 over each raw name. A collision can only keep an entry active, which is the safe direction. A paused or crashed listing restarts from scratch, as in M2, and rebuilds the set.
- **Candidates.** Only a run that read to EOF (`complete`, any coverage) looks for candidates: active children of the parent whose key is not in the set, read in pages.
- **Recheck.** Candidates are `Lstat`ed through the listing's open directory handle, in batches of `list_batch_size`, outside any write transaction:
  - `absent` makes the candidate tombstone-eligible;
  - present means the scope outcome becomes `stale`, so it is due again;
  - `unreadable` or `changed_during_observation` means no tombstone, and the outcome is `partial`;
  - `unavailable` goes through `checkSource`: the source is marked unavailable, the job stops with `source_unavailable`, and the scope is left unsettled.
- **Tombstone transaction.** One per batch. It re-checks:
  - the epoch;
  - the parent's boundary (`boundaryHolds`);
  - that the parent's `dev`/`ino` still equal the opened handle's identity;
  - that the parent's scope `dirty_version` equals the version captured at `openRun`;
  - for each candidate, `active = 1` and an unchanged `observation_revision`.

  If any check fails, it writes nothing and the outcome is `stale`. If all hold, it writes:
  - `active = 0`, `inactive_reason = 'missing'`, `inactive_at = now`, and `access_outcome = 'absent'`;
  - on a directory candidate's active subtree, `ancestor_gone` with frontier state cleared;
  - `reconciliation_runs.nodes_tombstoned` incremented.

Rejected:
- Per-child "seen" marks: they write every child.
- A `TEMP` table on the writer connection: it couples the listing to the single writer.
- Tombstoning on partial runs.
- Tombstoning without a recheck.

### D8. Identity at a name (§5.5.6, A31)

- **Continuity.** A node continues while the entry at (parent, raw name) has the same kind and inode. A mount boundary must also keep its device, because filesystem roots share inode numbers.
- **Device numbers.** For non-boundary nodes the device is evidence, not identity: a different `dev` with an unchanged inode updates the row without a revision bump or hint. The chain opener expects the scan root's current device for non-boundary nodes instead of the recorded one, so a re-plugged disk whose `st_dev` changed but whose fsid did not keeps working.
- **Replacement.** A different inode or kind, or a special file now at the name, tombstones the old node as `replaced` and its active subtree as `ancestor_gone`.
  - A new node is inserted, or an inactive one with matching identity is reactivated.
  - The new node takes protection from the pins (`protection.State`) and no other assertion.
- **Inode reuse at the same name** is indistinguishable from an in-place change by polling. It counts as continuity, and the descriptor change flags any override (owner-intent).

Rejected:
- Name-only continuity, as in M2: it transfers assertions to a different object.
- `dev`+`ino`: every remount would replace the whole catalog.
- `statx` birth time: it is unavailable on NFS, FAT, and older kernels. It can be added later as extra evidence.

### D9. Inactive reasons

`nodes.inactive_reason` and `nodes.inactive_at` are set at every deactivation site:

| Site | Reason |
|---|---|
| Collapse (descendants) | `collapsed` |
| Replacement | `replaced` |
| Verified absence | `missing` |
| Beneath a replaced or missing node | `ancestor_gone` |

Reactivation clears both columns. The migration sets `unrecorded` on existing inactive rows.

Rejected: a separate tombstone table. History and reactivation already live on the node row.

### D10. Probe results bind to what they observed (§5.5.5, A30)

`loadTarget` captures `classify.Observed{Epoch: scan epoch, ObservationRevision, IntentRevision}` and the scope's `dirty_version`. `commitDecision` passes that captured `Observed` to `classify.Apply` instead of reading it in-transaction, and compares the captured dirty version itself.

When the result is stale:
- the descriptor is kept;
- the suggestion is stored `stale`;
- the inventory mode is left unchanged, and the probe stays pending for the job, so the latest state is re-probed at once in the same job. This extends D24 of M2: a directory may be probed again after a stale result.
- the scope is settled `stale`.

Rejected:
- Discarding the descriptor: it is real evidence.
- Waiting for the next pass: it leaves a known-stale classification for up to 24 h.

### D11. Aggregate commits bind to what they observed (§5.5.2, §5.5.5, A29, A30)

- **What is captured.** `loadUnit` captures the observation revision (as before) and the scope's `dirty_version`, stored in `aggregates.dirty_version`.
- **Commit checks:**
  - an epoch mismatch discards the measurement, as before;
  - an inactive or non-atomic unit stores the row as history (`applied = 0`), as before;
  - a revision or dirty-version mismatch also stores it with `applied = 0` and does a `reconcile.Request` on the unit.
- **Freshness.** `domain.MeasurementCurrent` gains the measured and the current observation revisions. A measurement is current only if the revision is unchanged (A29 "known relevant change"). Every caller passes the node's revision.

Rejected: comparing only epoch and age. A changed boundary would keep a measurement current for up to 24 h.

### D12. Walk risk persists until disproved (G4, §3 "Unknown is not empty or safe")

- **New function.** `classify.LoadRiskEvidence(node, epoch)` returns one `AggregateEvidence` built from every `applied = 1` measurement of the node in that epoch, starting at the newest complete one, or all of them if none is complete:
  - indicators are unioned and renumbered `a1…`, keeping the D34 example bounds;
  - `ID`, `FinishedAt`, and coverage come from the newest measurement;
  - `Stale` reports whether that newest one is no longer current.
- **Callers.** `discovery.commitDecision` and `inspection.reclassify` use it for rule input. Totals and size classes still use only current measurements. `Explain` names a stale source measurement and its time.

Rejected:
- Only the current measurement: risk would vanish by aging (G4).
- A union over all time: a complete walk could never disprove risk.
- Keeping evidence across epochs: it would describe another filesystem.

### D13. Override evidence compares same-budget probes (G3)

- `descriptors.escalated` marks escalated probes.
- `set-classification` records the digest of the latest non-escalated descriptor.
- `commitDecision` flags `evidence_changed` only from non-escalated descriptors.

Rejected: a budget-free digest. An escalated probe examines more entries, so the digests differ anyway.

### D14. Device keys at claim time (G2, §5.1)

- **Callers stop choosing keys.** `jobs.Spec.DeviceKey` and `sources.Registry.DeviceKey` are removed, along with every caller's plumbing.
- **The runner computes the key at claim time,** in the claim query: `'dev:' || sources.identity_dev`, or the placeholder `'source:' || jobs.source_id` while `identity_dev` is `NULL`.
- **A placeholder is exclusive.** A job claimed under a placeholder makes its source exclusive. `deviceSlots` tracks such sources, and the claim excludes every job of an exclusive source, and every placeholder job whose source has any running job, until it finishes.
- `jobs.device_key` records the claimed key.

Rejected:
- Re-keying queued jobs when the identity is recorded: a running job still escapes the limit.
- Making every source exclusive: it changes the meaning of `workers_per_device > 1`.

### D15. Coverage gaps (§5.5.3, A27)

- **Opening.** `sources.Service.Confirm` calls `reconcile.MarkSource(source, epoch_changed)` in its epoch transaction. It seeds missing rows, hints every row of the source, and sets `sources.coverage_gap_at` and `coverage_gap_reason`.
- **Closing.** The scheduler clears the gap once no active scope of the source has `last_complete_at < coverage_gap_at`.
- **Restarts are not gaps.** Due times are durable, so overdue scopes show as lag.

Rejected: treating every restart as a gap. That would re-list everything on every restart.

### D16. `refresh-scope` (§5.5.7, §9.3)

- **One transaction.** The command checks the node and its state, hints the affected scopes, calls `StartScanWith(FrontierScanSpec)`, and then calls `RequestListing`, `RequestProbe`, or `RequestSubtree`. The owner's command resumes a paused job, as `refine-node` does.
- **No expected revision.** A refresh changes no owner intent, so it carries no expected revision.
- **Single node only.**

Rejected: a bulk form. The subtree mode already covers "everything under here".

### D17. Commands register themselves

Each command file calls `register(name, (*handler).decodeX)` in `init()`. `ServeHTTP` looks the name up in the registry, and unknown names still answer 404. There is no behavior change.

Rejected: an explicit table in `commands.go`. It would still be a shared edit point for every slice.

### D18. Root listing in frontier jobs

`RequestListing` accepts the source root. `WidenScan` now rewrites the job's payload scope to `source`. `complete()` decides from the payload scope, not from the root's `listing_job_id`, whether to set `last_scan_at`. A scheduled root listing therefore never counts as a scan.

Rejected: keeping D24's root refusal. The root's direct entries would then never be polled.

### D19. Configuration (§5.5.3)

New section `[reconciliation]`:

| Key | Default | Allowed |
|---|---|---|
| `expanded_interval` | 15m | 0, or 1m–24h |
| `atomic_interval` | 24h | 0, or 1h–720h |
| `failure_backoff` | 5m | 10s–1h |

A zero interval disables that schedule; hints and refreshes still work. The scheduler tick is the constant `watch.DefaultTick` (1 min), and `serve` lets tests inject it.

Rejected:
- A configurable tick: it adds no operator value.
- Watcher keys now: there is no backend to configure.

### D20. G1 is solved by scopes; the runner is unchanged

A listing or probe left pending on a failed or cancelled job keeps its scope due, because nothing settled it. The next pass re-dispatches it, and per-scope backoff limits repeats.

Rejected: consulting `PendingWork` on the error path. The re-queued attempt fails again the same way.

### D21. Tests stay under 60 s per package

- **New build tag `slow`.** It holds the spec-sized fixtures and the extra property seeds. Final verification runs `go test -race -tags slow ./...`.
- **Shrunk default tests:**
  - `TestNodeBudgetPausesAndResumes` uses budget 500 over 600 entries by default;
  - the 50,000-descendant collapse test moves to `slow`;
  - property seeds 1–4 run by default, and seeds 5–12 only under `slow`.

Rejected: a longer per-package budget. Merge cycles would grow back to 3 min.

### D22. Notifications are deferred (§5.5.1, §14)

The M2a exit condition requires that "polling works alone". ADR `docs/adr/0002-notification-backend-in-m3a.md` records the following:
- the fsnotify/inotify backend, the watch budget, and the coalescing window move to M3a, where inbox latency needs them;
- A27's overflow and watch-limit cases move with them.

Index health reports notifications as `polling_only`.

Rejected: shipping an inotify watcher now. It would be a second change source before polling is proven, the order §14 and the instructions to the coding agent require.

### D23. Migration `0003_incremental_indexing.sql` (§8.3)

The migration is additive only. It makes these changes:
- **New table** `dirty_scopes`:
  - columns `node_id` (PK, references `nodes`), `source_id`, `dirty_version`, `clean_version`, `reasons` (default `'[]'`), `first_hint_at`, `last_hint_at`, `due_at` (nullable), `failures`, `dispatched_job_id`, `last_pass_at`, `last_complete_at`, and `last_outcome` (CHECK: complete, partial, error, stale);
  - indexes `dirty_scopes_due(source_id, due_at) WHERE due_at IS NOT NULL` and `dirty_scopes_dirty(source_id, first_hint_at) WHERE dirty_version > clean_version`.
- **`nodes`:** `inactive_reason` (CHECK: collapsed, replaced, missing, ancestor_gone, unrecorded) and `inactive_at`.
- **`reconciliation_runs`:** `coverage_state`, `nodes_tombstoned`, `confirmed_at`, and `confirmations`.
- **`descriptors`:** `escalated` and `confirmed_at`.
- **`aggregates`:** `applied` (default 1) and `dirty_version`.
- **`sources`:** `coverage_gap_at` and `coverage_gap_reason`.

Backfills:
- `inactive_reason = 'unrecorded'` where `active = 0`;
- `escalated = 1` where the payload's `coverage.budget.entry_budget` exceeds the node's minimum descriptor budget;
- `applied = 0` for aggregates newer than their node's `latest_aggregate_id`, or whose node has none.

These are best-effort reconstructions for pre-M2a rows, and the docs say so.

## Interfaces

### Import direction (new edges only)

```text
domain ← reconcile ← {discovery, inspection, intent, sources, commands, inventory, watch}
classify ← {discovery, inspection}           (LoadRiskEvidence)
discovery, jobs, sources, store, config, clock ← watch ← cmd/curator
```

`internal/reconcile` imports only `domain`, `database/sql`, `encoding/json`, `hash/fnv`, and `time`. Nothing imports `watch` except `cmd/curator`.

### Go signatures (foundation unless marked)

```go
// internal/domain
type DirtyReason string    // "metadata_changed", "owner_refresh", "epoch_changed"
type ScopeKind string      // "listing", "boundary"
type ScopeOutcome string   // "complete", "partial", "error", "stale"
type InactiveReason string // "collapsed", "replaced", "missing", "ancestor_gone", "unrecorded"
func MeasurementCurrent(measuredEpoch, measuredRevision, sourceEpoch, nodeRevision int64,
	finishedAt, now time.Time, staleAfter time.Duration) bool

// internal/reconcile
type Schedule struct{ Expanded, Atomic, FailureBackoff time.Duration }
func (s Schedule) Interval(mode domain.InventoryMode) time.Duration // Expanded for expanded, else Atomic; 0 = off
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
func Version(ctx context.Context, q Queryer, node domain.NodeID) (int64, error) // 0 when untracked
func Hint(ctx context.Context, tx *sql.Tx, node domain.NodeID, r domain.DirtyReason, now time.Time) error
func HintSubtree(ctx context.Context, tx *sql.Tx, root domain.NodeID, r domain.DirtyReason, now time.Time) (int64, error)
func Request(ctx context.Context, tx *sql.Tx, node domain.NodeID, now time.Time) error
type Result struct {
	Node    domain.NodeID
	Started int64 // dirty_version captured when observation began
	Outcome domain.ScopeOutcome
	Mode    domain.InventoryMode // the node's mode after the pass; picks the interval
	At      time.Time
}
func Settle(ctx context.Context, tx *sql.Tx, s Schedule, r Result) error
func MarkSource(ctx context.Context, tx *sql.Tx, source domain.SourceID, r domain.DirtyReason, now time.Time) error
func Seed(ctx context.Context, tx *sql.Tx, source domain.SourceID, s Schedule, now time.Time) (int64, error)

// internal/discovery
func SourceScanSpec(source domain.SourceID) jobs.Spec   // deviceKey parameter removed
func FrontierScanSpec(source domain.SourceID) jobs.Spec // deviceKey parameter removed
func RequestListing(ctx context.Context, tx *sql.Tx, node domain.NodeID, job domain.JobID) error // now accepts the root
func RequestProbe(ctx context.Context, tx *sql.Tx, node domain.NodeID, job domain.JobID) error
	// active non-boundary directory that is not expanded; no-op when this job already has it pending or probed
func RequestSubtree(ctx context.Context, tx *sql.Tx, root domain.NodeID, job domain.JobID) (listings, probes int64, err error)
	// root must be an active expanded directory; never marks anything beneath an atomic directory
func DispatchDue(ctx context.Context, tx *sql.Tx, source domain.SourceID, job domain.JobID, now time.Time) (listings, probes int64, err error) // slice D
// Deps gains: Schedule reconcile.Schedule

// internal/jobs
type Spec struct { // DeviceKey removed
	Kind           Kind
	PayloadVersion int
	Payload        json.RawMessage
	SourceID       domain.SourceID
	ScopeKey       string
}
func (t *Tx) ActiveScan(source domain.SourceID) (Record, bool, error) // exported activeScan
// Record.DeviceKey now reports the key of the latest claim.

// internal/sources: Registry and Service lose DeviceKey.
// Service.Confirm also calls reconcile.MarkSource.

// internal/inspection
func AggregateSpec(node domain.NodeID, source domain.SourceID) jobs.Spec
func EnqueueAggregate(ctx context.Context, tx *jobs.Tx, node domain.NodeID) (jobs.Record, bool, error)

// internal/intent
type RefineRequest struct{ NodeRef } // DeviceKey removed; Refine also calls reconcile.Request

// internal/classify
func LoadRiskEvidence(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, node domain.NodeID, epoch int64, now time.Time, staleAfter time.Duration) (*AggregateEvidence, error) // nil when none
// AggregateEvidence gains: FinishedAt time.Time; Stale bool; From []int64 (unioned measurement IDs)

// internal/commands
func register(name string, decode func(h *handler, body []byte) (operation, error)) // panics on a duplicate
const RefreshScope = "refresh-scope" // slice D

// internal/watch (slice D)
const DefaultTick = time.Minute
type Deps struct {
	Store    *store.Store
	Jobs     *jobs.Runner
	Schedule reconcile.Schedule
	Clock    clock.Clock
	Logger   *slog.Logger
}
func NewScheduler(d Deps) (*Scheduler, error)
func (s *Scheduler) Tick(ctx context.Context) error

// internal/inventory (slice E)
type ScopeStatus struct {
	Kind                                domain.ScopeKind
	Dirty                               bool
	DirtySince                          *time.Time
	Reasons                             []domain.DirtyReason
	LastPassAt, LastCompleteAt, NextDueAt *time.Time
	LastOutcome                         domain.ScopeOutcome
	Failures                            int
	PendingJob                          domain.JobID // 0 when not dispatched to an active job
}
// Node gains: InactiveReason domain.InactiveReason; InactiveAt *time.Time; Scope *ScopeStatus (directories only).
// Node.LastObservedAt becomes the D6 displayed value.
// Options gains Schedule reconcile.Schedule.
func (r *Reader) History(ctx context.Context, id domain.NodeID, after *Cursor, limit int) (Children, error)
func (r *Reader) IndexHealth(ctx context.Context, source domain.SourceID, after *ScopeCursor, limit int) (IndexHealth, error)
```

### Database columns read by another slice

| Column | Written by | Read by | Shape |
|---|---|---|---|
| `dirty_scopes.*` | reconcile (foundation), discovery (A/B), watch (D) | inventory (E), watch (D) | `reasons`: sorted JSON array of `DirtyReason` strings, e.g. `["metadata_changed","owner_refresh"]`; times in Unix ms |
| `nodes.inactive_reason`, `inactive_at` | discovery (A), intent (foundation) | inventory (E) | text enum, Unix ms |
| `reconciliation_runs.coverage_state`, `confirmed_at` | discovery (A) | inventory (E) | `complete`/`partial`/`error`, `NULL` before M2a; Unix ms |
| `descriptors.escalated`, `confirmed_at` | discovery (B) | intent (foundation), inventory (E) | 0/1; Unix ms |
| `aggregates.applied`, `dirty_version` | inspection (C) | classify (foundation), inventory (E) | 0/1; integer |
| `sources.coverage_gap_at`, `coverage_gap_reason` | reconcile (foundation), watch (D) | inventory (E) | Unix ms; `DirtyReason` text |

### Endpoints

**`POST /api/commands/refresh-scope`** (slice D)
- Body: `{"node_id":"12","scope":"boundary"}`. `scope` is `boundary` or `subtree`; unknown fields are rejected. The usual `Idempotency-Key` applies.
- Success: 202 `{"job_id":"7","state":"queued","coalesced":false}`, the same `commands.Accepted` shape as `start-scan`.
- Errors, in check order:
  1. a malformed body or bad `scope`: 400 `invalid_request`;
  2. an unknown node: 404 `not_found`;
  3. a source that is no longer configured: 409 `source_unconfigured`;
  4. an inactive node, file, symlink, or mount boundary, or `subtree` on a directory that is not expanded: 409 `invalid_node_state`.
- Single node only: there is no bulk form. Source availability is not checked here; the job reports `source_unavailable`, as for `start-scan`.

**`GET /api/sources/{id}/index-health?cursor=`** (slice E)
- An unknown source answers 404 `not_found`.
- Success: 200 with the body below.

```json
{"source_id":"old-disk",
 "notifications":{"mode":"polling_only"},
 "schedule":{"expanded_interval":"15m0s","atomic_interval":"24h0m0s","failure_backoff":"5m0s"},
 "held":null,
 "counts":{"scopes":812,"dirty":3,"overdue":5,"failing":1,"pending":4,"never_completed":0},
 "lag_ms":184000,
 "oldest_dirty_since":"2026-10-02T15:04:05Z",
 "last_pass_at":"2026-10-02T15:10:00Z",
 "coverage_gap":null,
 "scopes":[{"node_id":"41","name":"Docs","name_b64":"RG9jcw==","kind":"listing","dirty":true,
            "reasons":["metadata_changed"],"next_due_at":"…","last_outcome":"partial","failures":2,
            "last_pass_at":"…","last_complete_at":null}],
 "next":null}
```

Field notes:
- `held` is `null` or `{"reason":"unavailable|needs_reconfirmation|unconfigured|never_scanned|paused|failure_backoff","detail":"…","until":"…"|null}`.
- `coverage_gap` is `null` or `{"reason":"epoch_changed","since":"…"}`.
- `lag_ms` is `now − min(due_at)` over due scopes, or 0.
- `scopes` lists dirty, overdue, or failing scopes, ordered by `(due_at, node_id)`, page size `discovery.page_size`.

**`GET /api/nodes/{id}/children?state=history&cursor=`** (slice E)
- Returns the same item shape as the current children, for inactive children only, ordered by `(name bytes, id)`.
- The parameter `state` is `current` (the default) or `history`. Any other value answers 400 `invalid_request`.

**Node JSON** (every node endpoint, slice E) gains:
- `"active"`;
- `"inactive_reason"` (string or null) and `"inactive_at"` (time or null);
- `"scope"`: null for non-directories, otherwise `{"kind","dirty","dirty_since","reasons","last_pass_at","last_complete_at","next_due_at","last_outcome","failures","pending_job_id"}`;
- `"last_observed_at"` now carries the D6 displayed value.

**HTML pages** (slice E): `GET /sources/{id}/health` and `GET /nodes/{id}/history`.

## Transaction boundaries

| Path (transaction) | Re-checked inside the writing transaction | Another command commits just before | …just after |
|---|---|---|---|
| Scheduler dispatch (`watch.Tick`, one `jobs.Tx` per source) | source configured, available, epoch > 0, root exists; active scan not paused; each scope due, not dispatched to this job; node active, non-boundary; kind of work from current mode | hint: scope due now, included. Collapse: dispatched as a probe. Tombstone or deactivation: skipped by the join | hint: version moves past the dispatch, but the worker captures the version when it starts, so a hint before start is covered and one after keeps the scope dirty. A job finishing before it sees the work is re-queued by `PendingWork` |
| Listing batch (`commitBatch`) | epoch; `boundaryHolds` (active, expanded, listed by this job, intent revision); for changed child directories: `Hint(metadata_changed)` in the same transaction | collapse or refine of the parent: `boundary_changed`, nothing written | the next batch's `boundaryHolds` fails the same way |
| Listing close and tombstones (`closeListing`, D7) | epoch; `boundaryHolds`; parent `dev`/`ino` match the opened handle; parent scope `dirty_version` = captured; each candidate active with an unchanged revision; `Settle` | hint: no tombstones, outcome `stale`, scope due. A candidate reactivated or changed by another writer is skipped | hint: the scope is dirty again (`dirty_version > started`), so another pass follows. A tombstoned node can be reactivated by identity |
| Probe commit (`commitDecision`, D6, D10, D13) | epoch; node active, probe-pending for this job; captured `Observed` vs current; captured dirty version vs current; unchanged-probe test; `Settle` | intent change or hint: suggestion `stale`, the probe stays pending and is re-run in this job | intent change: intent recomputes the effective classification itself. Hint: the scope is dirty, so a later pass follows |
| Aggregate commit (D11, D12) | epoch (discard on mismatch); unit active and atomic; captured revision and dirty version; `LoadRiskEvidence` in the same transaction | refresh or boundary change: the row is stored `applied = 0`, then `Request`. Collapse: history | the next probe's revision bump makes the measurement not current (D11) |
| `refresh-scope` (D16) | node exists; source configured; node active, kind and mode valid for `scope`; hints; `StartScanWith`; `Request*` | collapse: a boundary refresh becomes a probe, and `subtree` answers 409. Tombstone: 409 | collapse: the listing's `boundaryHolds` cancels it. The work's captured version includes the hint |
| `refine-node` (existing) | as in M2, plus `reconcile.Request` | as in M2 | as in M2. A failed job leaves the scope due (D20) |
| `collapse-node` (existing) | as in M2, plus `inactive_reason = 'collapsed'` on the descendants | as in M2 | the descendants' scope rows are ignored, and the node's own scope is next dispatched as a probe |
| `source confirm` (D15) | epoch increment and `MarkSource` in one transaction | — | in-flight jobs fail their next `checkEpoch`, and the scheduler dispatches every scope |
| Runner claim (D14) | key computed from `sources.identity_dev`; slot and exclusivity under `r.mu` | identity recorded: the claim gets `dev:<n>` | identity recorded during a placeholder job: that job stays exclusive until it finishes |

## Risks / Trade-offs

- [Every pass with due work creates a job row; up to 1,440 per source per day] → The rows are small, and events are pruned. Job pruning is out of scope and noted for later.
- [Scopes scanned together come due together after `start-scan`] → First due times are spread by D3, and later passes drift apart by their own durations. The lag stays visible either way.
- [Inode reuse at the same name looks like an in-place change] → The descriptor change flags overrides. `statx` birth time can be added later.
- [A permanently failing scope keeps a coverage gap open] → This is intended. Index health names the scope.
- [A long aggregate walk holds the device slot, so reconciliation of that device waits] → This is unchanged from M2, and the lag is visible. Fairness comes in M3a.
- [The seen-set takes about 8 bytes per entry; a 1,000,000-entry directory needs about 16 MB] → Accepted. Listing batches already bound the database work.
- [The backfills of `escalated` and `applied` are heuristic] → They affect only pre-M2a history. The docs state it.

## Migration Plan

1. Back up with `curator backup`.
2. On first start, migration `0003` runs. It is additive, so the M2 binary refuses the newer schema.
3. The first scheduler pass seeds scopes spread over each interval. No scan is needed.
4. After the upgrade, an entry deleted earlier is tombstoned when its parent is next listed completely. Inactive rows from before the upgrade show reason `unrecorded`.
5. **Rollback:** stop the server, restore the backup, and start the M2 binary.

## Addendum: decisions made during apply

Added at archive (2026-10-02). These decisions were made or confirmed while M2a was implemented, and the code on `master` follows them. None deviates from `directory-first-curator-spec-v0.2.md`. The one deviation of M2a, deferring notifications to M3a, is D22 and is recorded in `docs/adr/0002-notification-backend-in-m3a.md`.

### Discovery and listing

**D24. A second root listing in one attempt is deferred (D18).** The granted root handle is consumed by the attempt's first root listing. If the root is requested again in the same attempt, it leaves the frontier and its scope becomes due again, so the next pass lists it.
- Rejected: failing the job. A refresh that arrives after the root listing would end a healthy job.

**D25. `WidenScan` is idempotent.** It rewrites the job payload's scope to `source` and returns false for a job whose stored scope is already `source`.

**D26. A widened job re-probes what its frontier listings skipped (D5).** When a frontier job is widened to the whole source, `markExpanded` also marks probe-pending every active, non-boundary child directory of a directory the job already listed and has neither pending nor probed.
- Rejected: keeping D5's "probe only what changed" for widened jobs. A source scan must leave every directory decided, which the Rescans requirement demands.

**D27. Frontier jobs record the root's identity (D7, D8).** `begin()` calls `recordRoot` in frontier scope too, from the granted handle, updating only the columns that changed. After a source confirm, a scheduled root listing then has the identity it needs to verify missing entries.

**D28. Identity checks use the granted root's device (D7, D8).** The chain opener expects the scan root's device (`s.root.Self().Dev`) for nodes that are not mount boundaries. The tombstone compare-and-swap compares the parent's inode with the opened handle; the device is evidence only.
- Rejected: comparing the device recorded on each node. A disk reattached with a new device number would block every tombstone until the next full scan.

**D29. An expanded directory's scope is settled by its listing, not its probe.** A probe of a directory that ends expanded does not settle the scope.
- Rejected: settling on the probe. The scope would look clean before its listing ran.

**D30. Owner refreshes re-probe (D16).**
- `RequestProbe` and `RequestSubtree` skip only a node already pending in the joined job. A directory the job has already probed is probed again, so "Refresh joins a running scan" holds whatever the joined scan had done.
- `refine-node` calls `reconcile.Request`, so the refined directory's scope is marked like any other owner request.

### Probes and classification

**D31. The unchanged-probe test (D6, D13).**
- A probe counts as unchanged only when its descriptor matches and the current rule suggestion's `descriptor_id` is at least the latest non-escalated descriptor's ID. A probe whose escalation never completed takes the full path.
- An unchanged probe still runs `classify.Recompute`, because the protection of a reactivated node can change, and then decides the mode without escalation. It writes no descriptor or classification row.

**D32. A stale probe changes nothing (D10).** If the dirty version moved during a probe, the node row is left untouched: mode, `latest_descriptor_id`, coverage, and override flags. The node stays probe-pending and is probed again in the same job. `classify.Suggestion.Stale` lets the caller store the suggestion as stale history after its own compare-and-swap fails.

**D33. Walk risk evidence (D12).** "Newest" means highest ID. A path found by several walks is one example that cites the union of their signals.

### Inspection

**D34. What binds an aggregate walk (D11).**
- Only the source epoch, the observation revision, the active atomic state, and the dirty version bind a walk. A change of the intent revision alone still links the measurement, and the unit is reclassified at the current intent revision.
- On an epoch mismatch, the measurement is stored as history (`applied = 0`, its own epoch) instead of being discarded. §5.5.5 lets stale results stay in history; they cannot change the effective classification. M2 discarded them.

**D35. Measurement freshness has one source (D12).** `NewAggregateHandler` reads `Config.AggregateStaleAfter` (zero means the default), and the inspector's cited aggregate uses `classify.LoadRiskEvidence`, so the page and the classifier agree on staleness.

### Scheduler and read side

**D36. Holds are derived from rows (D4).** No column stores a hold. The first match wins:
1. unconfigured;
2. `needs_reconfirmation`;
3. unavailable, including availability not checked yet;
4. `never_scanned`;
5. paused, naming `pause_reason`;
6. `failure_backoff`: the latest terminal scan job (highest ID) failed with a source-level code and `now < finished_at + failure_backoff`. A later success or cancel releases it.
- Rejected: a hold column. It would need its own transitions, and it could disagree with the rows it summarizes.

**D37. `DispatchDue` is once per job for listings too (D4).** It skips listings the job has already done or has pending (`listing_job_id = job`), and it refuses a job that is not a queued or running scan of the source.

**D38. Closing coverage gaps (D15).** A scope that never completed a pass counts as not reconciled. A gap closes only in passes of sources that are not held. Holds are evaluated before `Seed`.

**D39. `refresh-scope` writes no audit event (D16).** It changes no owner intent; the job and its runs record it. `audit_events` holds security actions and owner decisions.

**D40. Read-side details.**
- `PendingJob` and the pending counts come from frontier marks, not `dispatched_job_id`.
- `scope` is null for mount boundaries and inactive nodes.
- A NULL `inactive_reason` is reported as `unrecorded`.

### Jobs

**D41. Device key format (D14).** The claim-time device key is `'dev:' || identity_dev`, the signed decimal of the stored integer. `jobs.Tx.StartScan` no longer takes a device key.

### Verification

**D42. How the tests prove behavior (D6, D21).**
- Unchanged probes and listings write no rows, so tests count probes by filesystem calls. They count listings as the job's runs plus the confirmations merged into earlier runs.
- Discovery, scenario, and watch tests run in parallel; each test owns its store, synthetic filesystem, clock, and runner.
- The no-hidden-catalog property test also drives scheduled passes, `refresh-scope`, and deletions and replacements on disk. It accepts three expected failures:
  - a walk of a unit whose chain was replaced fails `stale_evidence`;
  - a scan whose listed directory vanished ends `observation_incomplete`;
  - a peek of a deleted or replaced unit is refused.
