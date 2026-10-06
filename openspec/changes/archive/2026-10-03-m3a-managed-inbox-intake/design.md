# Design

## Context

See proposal.md for the motivation and the specs for the required behavior. These code facts on `master` at `073d233` shape the approach:

- **No role concept.**
  - Nothing in code, migrations, or docs knows an inbox.
  - `intent.Collapse` and `set-disposition` refuse only on mode, root, and protection grounds.
  - `classify.Recompute` may suggest `cleanup_candidate` for any atomic unit.
- **One scan per source, in node-ID order.**
  - `DispatchDue` hands a due scope to a scan job at most once per job (`dispatched_job_id IS NOT ?2`).
  - The frontier takes the lowest pending node ID.
  - `watch.DefaultTick` is 1 min, and `reconciliation.expanded_interval` has a 1-min minimum. A 5-s inbox poll cannot ride the scan.
- **No priorities.**
  - The runner claims `ORDER BY available_at, id`.
  - Scans and aggregate walks hold their device slot until they finish.
  - `jobs.Spec` has no availability time. Only `*jobs.Defer` returned by a handler sets one.
- **New children are found in one place.** In `batchWriter.upsertChild` (listing.go), the final INSERT or the reactivation branch is the §5.5.5 "new child in an expanded container" hook. A kind or inode change goes through `deactivateSubtree` with reason `replaced`.
- **Loose files have only a name hint.**
  - `nodes.file_kind` is written at listing from `observe.Policy.FileKindOf`.
  - `classifications.category` is directory-only, and the table has no task column.
  - The inference handler cancels every non-`directory_category` request (handler.go:260), and `Preview` is directory-only.
  - `tasks.OutboundFile` and `contract.ItemFile` exist but have no caller outside `eval`.
- **Grants are per source.** `classifier_permissions` is keyed by `source_id`. `requests.Want` is directory-only and reads that grant.
- **The descriptor digest already includes immediate entry sizes** (`ObservedEntry.Size`), and excludes IDs, revisions, and timestamps.
- **The polling view of notifications.** `inventory.NotificationsPollingOnly` is a constant, the health JSON has `notifications.mode` only, and `go.mod` has no watcher dependency.
- **Live pages reload on job events.** In "reload" mode, `app.js` reloads on a new job of a shown source and on a shown job's terminal state.
- **Schema rules.** The latest migration is `0004`. `nodes`, `classifications`, and `inference_requests` are referenced by foreign keys and cannot be rebuilt, so changes are `ADD COLUMN` or new tables.

## Goals / Non-Goals

**Goals:**
- Inbox work reuses discovery's listing, probe, identity, and coverage code. There is no second scanner (§5.5.1).
- Every readiness, triage, request, and proposal transition is conditional on the item generation it observed, in the same transaction as its write.
- Polling alone satisfies every inbox scenario. Notifications only shorten latency.
- The fairness arbiter is the only place that decides which job gets a device next.

**Non-Goals:**
- Any filesystem write, plan, or organization state (M5).
- The staged-producer handoff protocol (M5), owner-written classification rules (M5), and package splitting (M5).
- Watches on atomic boundaries (M4).
- Per-inbox overrides of readiness, polling, or budget settings. One `[inbox]` section applies to every inbox (D9).

## Decisions

### D1. Packages and import direction (§8.2)

```text
domain, store, config, reconcile, classify/requests, classify/permission ← inbox
domain, store ← inbox/routing
inbox ← discovery            (enrollment and readiness hooks inside listing and probe transactions)
inbox ← intent               (role lock; set-inbox-role lives in intent next to refine and collapse)
inbox, inbox/routing ← commands
inbox.Proposer ← inbox/routing implements it; cmd/curator and the scenario harness wire it
inference.IntakeSink ← *inbox.Service implements it; cmd/curator wires it
fsnotify ← watch             (the backend; no other package imports fsnotify)
discovery.WatchHook ← *watch.Watcher implements it; cmd/curator wires it (watch already imports discovery)
```

- `inbox` does not import `discovery`, `intent`, `inference`, or `routing`. `classify` reads the role lock with SQL and imports none of these packages either.
- `inventory` reads every new table directly for the read models, as it does today.
- Rejected:
  - Putting intake state in `discovery`. The commands and the inference apply path would then import the scanner.
  - Putting `routing` inside `inbox`. Two slices would edit one package, and §8.2 already separates "routing suggestions" as their own concern.

### D2. The role lives in `inboxes`, not on `nodes` (§4.2, §4.5, §8.3)

- A directory is an inbox when an `inboxes` row with `removed_at IS NULL` references it. A partial unique index enforces one active row per node.
- Removing the role sets `removed_at` and keeps the row, so ended items and audit events still resolve their inbox.
- Re-adding the role creates a new inbox ID.
- Rejected: a `nodes.role` column. It would duplicate the inbox row, and the table cannot be rebuilt to add a CHECK.

### D3. `set-inbox-role` requires an active expanded directory (§4.5, A40)

- The target is a cataloged node, given with its expected intent revision, or a peek token.
- A peek-token target always lies inside an atomic unit, so it is refused with `invalid_node_state` naming the outermost atomic ancestor to refine.
- An atomic target is refused with "refine it first".
- The command never expands anything itself. Refinement stays the explicit, separately audited `refine-node`.
- Nesting is checked against active inboxes: an inbox may not be inside another, or above one.
- A mount boundary, a file, a symlink, and an inactive node are refused.
- The command increments the node's intent revision (§4.6: the role is a workflow decision) and writes audit kind `inbox_role`.
- Rejected:
  - Implicitly refining an atomic target or its ancestors. A40 requires the refinement to be explicit, and §4.5 forbids secretly expanding siblings.
  - A path-only refinement chain. It is not needed when every step is an ordinary `refine-node`.

### D4. The role lock (§4.5, §9.4.5, A40)

- `inbox.LockedBy(ctx, q, node)` reports the active inbox at or below `node`. It walks the inbox nodes' ancestor chains, which are few, rather than the node's subtree.
- `intent.Collapse` refuses a locked node with `invalid_node_state` naming the inbox.
- `set-disposition cleanup_candidate` refuses one with `protected` naming the inbox. Bulk requests exclude and report it, as for protection.
- `classify.Recompute` never suggests `cleanup_candidate` for a locked node or for a node with an open intake item. Such a suggestion becomes `review`.
- Removing the role (D17) lifts the lock in the same transaction.
- Rejected: a recursive protection pin on the inbox. §9.4.5 forbids it, because it would block the approved moves of its children in M5.

### D5. Intake items and their lifecycle (§4.5, §9.4.1)

- Open states: `observed`, `settling`, `queued`, `classifying`, `needs_review`, `proposed`, and `failed`. Ended states: `ignored`, `missing`, and `superseded`. `planned`, `organizing`, and `organized` arrive with M5.
- The partial unique index `intake_items_one_open (inbox_id, node_id)` over the open states enforces one open item per occurrence.
- Item kinds:
  - **Directory or regular file:** settles normally.
  - **Symlink:** a review-only item with blocker `symlink`. It goes straight to `needs_review`, is never settled or sent, and never gets a proposal.
  - **Special entry:** no item. Nodes cannot represent a FIFO, socket, or device, so the entry stays the `special_file` issue of the inbox's listing, which the inbox page and API show; it is never opened. A special entry that replaces an enrolled node at the same name ends that item `superseded`. The `special` kind and `special_entry` blocker in the schema stay unused (apply-time decision).
- `failed` is used only when the arrival itself cannot be observed, after the source's failure backoff. A provider failure leaves the item in `needs_review` with a detail (D11).
- Rejected: one row per lifecycle transition. The current state, generation, and basis suffice in M3a. Audit events record the owner's transitions.

### D6. Enrollment and identity (§4.5, §5.5.5 row 1, §5.5.6)

- **Where the hook runs.** Discovery calls `inbox.ObserveChild` inside the listing batch transaction, for every entry of a directory that is an active inbox. Discovery checks once per listing run whether the directory is an inbox.
- **What enrolls.** Enrollment happens on insert, on reactivation, and on a replacement at the same name; the old open item then ends `superseded`. A new node in an inbox is enrolled whatever its category, unless the inbox's activation left it out (`enroll_existing: false` covers only the children active at activation).
- **Updates.** An existing node with an open item updates that item's fingerprint (D8).
- **Endings.** `inbox.Gone` runs inside the transactions that deactivate nodes:
  - tombstone `missing` ends the item `missing`;
  - `replaced` ends it `superseded`;
  - `collapsed` cannot happen while the lock holds;
  - `ancestor_gone` (the inbox itself vanished) leaves the item open and shows its inbox unavailable.
- **Renames.** A rename observed by polling, such as `x.part` to `x`, is a disappearance plus an arrival. The old item ends `missing`, and the new name starts a new item. §5.5.6 requires a correlated move event to merge identities, and a polling listing has none.
- **Rejected:**
  - Merging by equal device and inode inside one listing. Inode reuse within a poll interval would transfer an item to unrelated content.
  - Using the move cookie from notifications. Polling must reach the same result without notifications, so a cookie may hint but never decide.

### D7. One `intake` job per source (§5.5.1, §8.4, §9.4.1)

- **The job.**
  - Kind `jobs.KindIntake = "intake"` with ScopeKey `intake:<source>`, enqueued with `Tx.WakeOnce`.
  - It is a device-key job of class `interactive` (D13) and runs discovery's listing and probe code with its own job ID.
  - Its handler, `discovery.NewIntakeHandler`, lives in `discovery`.
- **One pass:**
  1. list each due inbox boundary of an unpaused inbox;
  2. probe each due settling directory arrival;
  3. call `inbox.Ready`, which evaluates readiness and returns the items that became ready at their current generation;
  4. decide each ready directory once through the ordinary decision path (D10);
  5. call `inbox.Triage` for each ready item: it sets the file kind, creates a request (D11), or runs the proposer (D12);
  6. call `inbox.Refresh`, which re-proposes stale proposals through `Proposer.Refresh`;
  7. return `*jobs.Defer{Until: earliest next due scope}`.

  Steps 3 to 6 run in bounded batches with a `Yield` between them (D16). These `inbox` functions are internal to slice I, which owns both `inbox` and the intake job in `discovery`.
- **Ending, waking, and failure.**
  - The job succeeds only when the source has no unpaused inbox.
  - Watcher flushes, `set-inbox-role`, `pause-inbox`, `mark-intake-ready`, and `retry-intake` wake it.
  - Source unavailability defers it by the failure backoff instead of failing it. The inbox then shows `unavailable`.
- **Who processes a scope.** Ownership follows dispatch: a node's `listing_job_id` and `probe_scan_job_id` name the job that will process it, the last dispatch wins, and each job's frontier selects only its own items. `DispatchDue` excludes the scopes of unpaused inbox boundaries and of settling arrivals. An owner `start-scan` or `refresh-scope` may still make the scan list an inbox through the same code and hooks.
- **Live pages.** The job defers once per poll, which publishes events. `app.js` ignores `intake` job events on every page except the inbox page. There it reloads only when the job's cumulative progress counter `items_changed` for that source grows.
- **Rejected:**
  - Inbox work inside the scan. The scan takes a scope at most once per job, in node order, and A36 would wait for a bulk scan to end.
  - Listing inboxes from the scheduler tick. It would hold no device slot and keep no durable state.
  - A new job per poll. That is about 17,000 job rows per source per day.

### D8. Readiness (§9.4.2)

- **Fingerprints.**
  - A file's is `(dev, ino, size, mtime_ns)` from the inbox listing.
  - A directory's is its own `(dev, ino, mtime_ns)` plus its latest descriptor digest, which covers immediate entry sizes.
- **Observing.**
  - An observation that differs from the stored fingerprint advances `generation`, sets `last_change_at` to the observation time, and clears any basis. A `queued`, `classifying`, `needs_review`, or `proposed` item returns to `settling`, and `inbox` cancels its unsent request with `requests.CancelArrival`.
  - An equal observation changes nothing but `last_observed_at`.
- **Becoming ready.**
  - An item becomes `heuristic_stable` at an equal observation with `t − last_change_at ≥ quiet_interval`.
  - The observation that last saw a change and this one are the "two unchanged observations at least 5 s apart" of §9.4.2, because `quiet_interval` is validated to be at least 5 s.
  - Readiness is decided only at an observation, never by a timer. Restarts lose nothing: the times are durable, and the next poll decides.
- **Never ready.**
  - A partial listing, an access error, or an unavailable source is not an observation for readiness. It sets the blocker `listing_incomplete`, `access_error`, or `source_unavailable` until a complete observation.
  - A name matching `inbox.temporary_patterns` (`path.Match` on the raw name bytes) keeps blocker `temporary_name`.
- **Stalled.** "Stalled" is derived at read time: settling and `now − first_observed_at ≥ stalled_after`. It is not stored.
- **Rejected:**
  - Separate `stable_gap` and `quiet_interval` settings. With the gap at most the quiet interval, the gap never binds.
  - Whole-tree stability passes. §9.4.2 makes them a separate, optional request, and A33 forbids inferring completion.

### D9. One `[inbox]` section for every inbox (§4.5, §5.5.3)

- `poll_interval`, `quiet_interval`, `temporary_patterns`, and `stalled_after` apply to every inbox. Probes use the `[discovery]` shallow budgets.
- An inbox row stores only its own state: paused, revision, and grants (D11).
- Rejected: per-inbox copies of these settings. No scenario needs them, and they would add validation, UI, and revision handling without changing any behavior the spec requires.

### D10. Triage of arrivals (§9.4.3, §5.3)

- **Before ready.** Probes of an arrival that is not ready store their descriptors, but take no decision: no rules, no `Want`, no mode change.
- **At readiness.** `inbox.Pass` marks the item `queued`. Discovery's intake pass then decides each ready directory once for its generation: the rules over the latest descriptor run through the ordinary `commitDecision`, with decision reason `inbox_arrival` and the mode forced `atomic`.
- **Files.** A file is decided from its name hint. `intake_items.file_kind` is set from `nodes.file_kind` with source `rule`. No classification row is written for a file rule.
- **Then:**
  - if the arrival qualifies for a model request (D11), it goes to `classifying`;
  - otherwise the proposer runs (D12), and the item becomes `proposed` or `needs_review`;
  - `decided_generation` records the generation that was decided, so a later poll does not decide it again.
- **Rejected:**
  - Running the rules on every probe. That appends history for every step of a copy, and triages before quiet, which violates A32.
  - Rule rows for files. The name hint is the deterministic detector, and is already on the node.

### D11. Inbox grants and arrival requests (§7.5, §7.7, §9.4.3)

- **Grants.**
  - `inbox_classifier_permissions` is keyed by `(inbox_id, task)`, with the same columns and semantics as `classifier_permissions`.
  - `set-classifier-policy` takes `inbox_id` and `task` instead of `source_id`. The policy's task must equal `task`.
  - The source grant never covers an open arrival. Children left out at activation, and nodes beneath a refined arrival, are ordinary nodes under the source grant.
- **Eligibility (`inbox.Want`).** The item is ready, the inbox is unpaused, and the inbox grant for the arrival's task names a policy of that task with at least one permitted profile. In addition:
  - for a directory, the current rule suggestion is `unknown` because no rule matched or rules conflicted, and there is no override;
  - for a file, `nodes.file_kind` is `unknown`;
  - no open request exists for the item, and none was created for this generation unless the owner retried.
- **Request rows.** `requests.CreateArrival` inserts the request with `intake_item_id` and `observed_intake_generation`, trigger `automatic`, or `owner` for `retry-intake`.
- **The handler.** It accepts `file_kind` requests only for open file arrivals, and loads the grant through `permission.CurrentInbox` when `intake_item_id` is set. It builds:
  - for a file, `tasks.OutboundFile` with `Readiness`;
  - for a directory, the existing directory descriptor with `Readiness`.
- **Apply.**
  - The CAS adds `observed_intake_generation = item.generation` (and, for files, the node's observation revision, epoch, and intent revision; files have no dirty scope).
  - A current file answer whose label is in the policy's `apply_categories` and meets `min_probability_bp` becomes `intake_items.file_kind` with source `model`. Directory answers apply through `classify.Apply` as in M3.
  - Every answer, current or stale, is stored as a model row; file rows use the new `classifications.file_kind` column with a NULL `category`.
  - Then the handler calls `IntakeSink.Classified`.
- **Ordering.** Within a source's classify job, the next request is `ORDER BY intake_item_id IS NULL, id`.
- **Cancellation.**
  - Pause, role removal, revocation of an inbox grant, and a change of the item cancel its pending requests (`requests.CancelArrival`).
  - A sent request completes, and its stale answer is history only.
- **Outbound.** `contract.OutboundDirectory` and `contract.OutboundFile` gain `Readiness string` with `json:"readiness,omitempty"`. Recorded fixtures and existing cache keys do not change, and `privacy-v1` stays, because no new private data is sent.
- **Rejected:**
  - Arrivals under the source grant. §7.5 calls for a separate inbox fast-triage policy.
  - One grant per inbox covering both tasks. A policy has one task, and the owner consents per payload shape.
  - A new trigger value `inbox`. The CHECK cannot be changed without rebuilding `inference_requests`, and `intake_item_id` already says it.

### D12. Destinations, rules, and proposals (§9.4.4)

- **Tables.**
  - `routing_destinations` has an active row per node, and `routing_rules` holds the rules.
  - `routing_state` holds the single routing revision. Every destination or rule change increments it.
- **Matching.**
  - A rule matches an item when its kind and value equal the item's effective category (directory) or effective file kind (file), and its `inbox_id` is NULL or the item's inbox.
  - The highest-priority matching rules decide. Several of them naming different destinations is `rule_conflict`. None is `no_rule`.
- **Validation (`Propose`).** It reads the destination node in the same transaction and adds blockers:
  - `destination_unavailable`: the node is inactive or replaced, or the destination row was removed;
  - `destination_atomic`: the node is not expanded;
  - `destination_in_inbox`: the node is an inbox or lies inside one;
  - `cross_source`: the node's source differs from the item's;
  - `name_conflict`: an active child of the destination has the arrival's raw name.

  The proposal is valid only without blockers.
- **What is stored.** `routing.Propose` writes the item's `proposal_*` columns, and the `proposal_basis` JSON records every input revision. `routing.Refresh` re-runs `Propose` once per intake pass for every triaged open item whose basis differs from the current values. Readers mark a proposal stale whenever the basis differs, so the window before `Refresh` never shows a stale proposal as valid.
- **Limits.**
  - `name_conflict` uses the catalog. A same-name entry that appeared on disk since the destination's last listing is found by M5's execution-time precondition, not here.
  - Owner tags are not a match input in M3a, because no tag exists (proposal: deferred to M5).
- **Rejected:**
  - Per-inbox destination lists. Rules already scope by inbox.
  - A proposals history table. M5 plans will record approved proposals.
  - Picking a date-based or model-named destination. §9.4.4 forbids it.

### D13. Priority classes and the device arbiter (§8.4, A36)

- **Classes.** A job's class is a function of its kind, registered with `Runner.RegisterClass`:
  - `intake` is `ClassInteractive`;
  - `scan` is `ClassReconciliation` (the default of `Register`);
  - `aggregate` is `ClassBulk`.

  Pool kinds have no class.
- **The arbiter.** One arbiter per device key decides who gets a free slot, among queued claimable jobs and running jobs blocked in `Yield`:
  - Weighted round robin: the arbiter keeps credits of 4, 2, and 1 per class, refilled when every waiting class has spent its credit.
  - Within a class, the oldest wait goes first: `available_at` for queued jobs, and the yield time for yielded ones.
  - A job waiting longer than 5 minutes counts as interactive.
- **Yield.**
  - `Runtime.Yield(ctx)` is called with no transaction open, at work-unit boundaries: after each frontier probe, each listing batch, and each walk batch.
  - It returns at once unless the arbiter would grant the device to another waiting job.
  - Otherwise it releases the slot and blocks until the arbiter grants the slot back. The job stays `running` with its lease renewed, keeps its in-memory state, and gets back `ctx.Err()` on cancellation.
  - Placeholder jobs, which started before the device was known, never yield. Pool jobs return at once.
- **State.** Arbiter state is in memory. A restart resets credits, and claims then re-establish order from the durable `available_at`.
- **Rejected:**
  - Strict priority. A stream of arrivals would starve reconciliation (§8.4).
  - Preempting by cancelling and re-queueing. A walk would restart from scratch, because its traversal is in memory.
  - An extra device slot for interactive work. It breaks the per-device I/O budget (§8.4, §5.1).
  - A `priority` column on `jobs`. The class follows from the kind, and aging from `available_at`.

### D14. Notifications through fsnotify (§5.5.1, §5.5.3, A27)

- **The backend.** `github.com/fsnotify/fsnotify` v1.10.1, behind `watch.Backend`. Production uses one `fsnotify.Watcher`, and therefore one inotify instance, per source, so an overflow is attributable to its source. Tests use `watch.FakeBackend`.
- **The watched set.** The watcher's `Sync` recomputes the set from the database:
  - inbox boundaries of unpaused inboxes first;
  - then roots and active expanded directories, by depth;
  - never atomic, inactive, or mount-boundary nodes;
  - within a process-wide budget, `watch.budget`.

  `Sync` adds missing watches and removes surplus ones. It runs on each scheduler tick. A listing pass also calls `Deps.Watches.EnsureWatched(dir)` before reading a directory, so events during the pass hint the scope (§5.5.3). `discovery.WatchHook` is that interface, and it is nil when no watcher runs.
- **Coalescing.** An event in a watched directory adds that directory's node to the pending set. The coalescer flushes when 500 ms have passed since the scope's last event, or 2 s since its first. A flush is one write transaction:
  1. `reconcile.Hint(node, notification)` for each pending scope;
  2. a wake of the intake job for inbox scopes;
  3. a scheduler nudge (`Scheduler.Nudge(ctx, source)`) that dispatches the source's due scopes as a tick would.
- **Events are only hints.** Event names are never trusted: the hint goes to the watched directory's own scope.
- **Lost notifications.**
  - `ErrEventOverflow`, or a flush that fails, calls `reconcile.MarkSource(source, notification_overflow)`.
  - A dropped watch (`IN_IGNORED`, or the directory removed) hints the directory, and `Sync` re-adds it when it is still active.
- **Health per source.**
  - The mode is `polling_only` when the backend is off (reason `backend_off`), when the filesystem is NFS, SMB/CIFS, or FUSE according to statfs (reason `network_filesystem`), or when inotify cannot be initialized (reason `unsupported`).
  - Otherwise it is `degraded` when watches are missing, with reason `watch_budget` or `os_watch_limit` (an `ENOSPC` or `EMFILE` from `Add`), or while a `notification_overflow` gap is open. Otherwise it is `watching`.
- **Symlinks.** fsnotify adds by path and follows a symlink swapped in at that path. A watch only produces hints, and the pass then re-reads through rooted access, so a misplaced watch costs a spurious pass and never a read outside the source.
- **Rejected:**
  - Raw inotify through `golang.org/x/sys/unix`, which would offer `IN_DONT_FOLLOW` and adds by file descriptor. It means more code to own for no behavior the specs require, and §5.5.1 names fsnotify.
  - Recursive watches, or watches under atomic units. §5.5.2 forbids them.
  - Persisting watch state. A restart rebuilds it (D15).

### D15. Restart and downtime with notifications (§5.5.3, A27)

- A restart opens no coverage gap. Events lost while the server was down are covered by the durable polling schedule: overdue scopes are dispatched at the first pass (M2a).
- Watches are rebuilt by the first `Sync`.
- Rejected: marking every source at start. It forces a full frontier pass on every restart, although polling never relied on events.

### D16. The intake pass and A41 bounds (§8.4, §9.4.3)

- **Bounded listing.** An inbox is listed at most once per due time. A hint makes it due at once, and the coalescer bounds hints to one per window, so the listing count per poll interval is at most `1 + interval / coalesce window`.
- **Bounded requests.** Each item gets at most one automatic request per generation (D11), and `requests.CreateArrival` reuses an open request. Spend reservations go through the M3 ledger and caps unchanged.
- **Bounded passes.** A pass processes at most `discovery.list_batch_size` items in readiness and triage between `Yield` calls, so 1,000 arrivals cannot monopolize the device.

### D17. Removing the role and pausing (§4.5, §9.1)

- **Removing the role** (`set-inbox-role` with `role: "normal"`), in one transaction:
  - ends every open item `ignored` with `end_reason = 'inbox_removed'`;
  - cancels their pending requests;
  - revokes the inbox grants, setting `policy_id` to NULL;
  - sets `removed_at` and increments the node's intent revision.

  Nothing is refused, because M3a has no `planned` or `organizing` item. M5 adds that refusal.
- **Pausing** (`pause-inbox`) increments the inbox revision and cancels pending requests. While paused, `DispatchDue` treats the inbox as an ordinary expanded directory. Enrollment still happens when it is listed, but readiness and triage do not advance. Resuming wakes the intake job.

### D18. Organization is shown as unavailable (§9.4.5, A39)

- Every item JSON carries `"organization": {"available": false, "reasons": [...]}`.
  - The reasons always include `not_in_this_release`.
  - They add `source_read_only` when the source's last observed mount is read-only.
- M5 replaces the first reason with its real preconditions.

### D19. Configuration (§5.5.3, §9.4.2)

| Key | Default | Range |
|---|---|---|
| `inbox.poll_interval` | `5s` | 1 s to 1 min |
| `inbox.quiet_interval` | `10s` | 5 s to 1 h |
| `inbox.stalled_after` | `24h` | 1 min to 720 h; at least `quiet_interval` |
| `inbox.temporary_patterns` | `["*.part", "*.partial", "*.tmp", "*.crdownload"]` | at most 64 entries; each a valid `path.Match` pattern |
| `watch.backend` | `auto` | `auto` or `off` |
| `watch.coalesce` | `500ms` | 10 ms to 10 s |
| `watch.max_delay` | `2s` | `coalesce` to 1 min |
| `watch.budget` | `8192` | 0 to 1048576 |

- The jobs fairness weights (4:2:1) and the aging time (5 min) are constants, documented in operator.md. §8.4 asks for bounded classes and fairness, not tunable ones.
- Rejected: `inbox.stable_gap` (D8).

### D20. Migration `0005_managed_inbox.sql` (§8.3)

- New tables: `inboxes`, `intake_items`, `inbox_classifier_permissions`, `routing_state` (one row, `revision` 0), `routing_destinations`, and `routing_rules`.
- New columns:
  - `classifications.file_kind`, nullable, with a CHECK over the nine kinds;
  - `inference_requests.intake_item_id`, referencing `intake_items`;
  - `inference_requests.observed_intake_generation`.
- Indexes: `inboxes_one_active`, `intake_items_one_open`, `intake_items_inbox_state (inbox_id, state, id)`, `intake_items_node`, `routing_destinations_one_active`, and `inference_requests_intake`.
- Additive only, like 0002 to 0004. The full DDL is under Interfaces.

### D21. Error codes (§9.3)

- No new code.
  - Role and nesting refusals, collapse of a locked node, and destination refusals are `invalid_node_state`.
  - `cleanup_candidate` on a locked node is `protected`.
  - Expected inbox revision, item generation, and routing revision mismatches are `revision_conflict`.
- Rejected: `inbox_locked`. The existing codes already distinguish what the client must do.

### D22. Scope choices confirmed with the owner

The owner chose these at proposal time:
- Owner-written classification rules, rules from corrections, and owner tags move to M5.
- The staged-producer protocol moves to M5 with A34, so `handoff_confirmed` comes only from `mark-intake-ready`.
- Only inbox arrivals are sent as `file_kind` requests. Ordinary loose files keep their name hint (§5.2, §6).

## Interfaces

### Schema (`0005_managed_inbox.sql`)

```sql
CREATE TABLE inboxes (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id),
  node_id INTEGER NOT NULL REFERENCES nodes(id),
  paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
  enroll_existing INTEGER NOT NULL CHECK (enroll_existing IN (0, 1)),
  created_at INTEGER NOT NULL,
  removed_at INTEGER
);
CREATE UNIQUE INDEX inboxes_one_active ON inboxes (node_id) WHERE removed_at IS NULL;

CREATE TABLE intake_items (
  id INTEGER PRIMARY KEY,
  inbox_id INTEGER NOT NULL REFERENCES inboxes(id),
  node_id INTEGER NOT NULL REFERENCES nodes(id),
  name BLOB NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('directory', 'file', 'symlink', 'special')),
  state TEXT NOT NULL CHECK (state IN ('observed', 'settling', 'queued', 'classifying', 'needs_review',
    'proposed', 'failed', 'ignored', 'missing', 'superseded')),
  end_reason TEXT,                -- inbox_removed | owner_ignored | missing | replaced
  detail TEXT,                    -- last triage or failure note shown to the owner
  generation INTEGER NOT NULL DEFAULT 1 CHECK (generation >= 1),
  readiness_basis TEXT CHECK (readiness_basis IN ('heuristic_stable', 'handoff_confirmed')),
  blocker TEXT CHECK (blocker IN ('temporary_name', 'listing_incomplete', 'access_error',
    'source_unavailable', 'symlink', 'special_entry')),
  fp_dev INTEGER, fp_ino INTEGER, fp_size INTEGER, fp_mtime_ns INTEGER, fp_digest BLOB,
  first_observed_at INTEGER NOT NULL,
  last_observed_at INTEGER NOT NULL,
  last_change_at INTEGER NOT NULL,
  ready_at INTEGER,
  decided_generation INTEGER,
  requested_generation INTEGER,
  provisional INTEGER NOT NULL DEFAULT 0 CHECK (provisional IN (0, 1)),
  file_kind TEXT CHECK (file_kind IN ('image', 'video', 'audio', 'document', 'source_file',
    'archive', 'installer', 'executable', 'unknown')),
  file_kind_source TEXT CHECK (file_kind_source IN ('rule', 'model')),
  first_suggestion_at INTEGER,
  proposal_destination_id INTEGER REFERENCES routing_destinations(id),
  proposal_rule_id INTEGER REFERENCES routing_rules(id),
  proposal_basename BLOB,
  proposal_valid INTEGER NOT NULL DEFAULT 0 CHECK (proposal_valid IN (0, 1)),
  proposal_blockers TEXT NOT NULL DEFAULT '[]',
  proposal_basis TEXT,
  arrived_at INTEGER NOT NULL,
  ended_at INTEGER,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX intake_items_one_open ON intake_items (inbox_id, node_id)
  WHERE state NOT IN ('ignored', 'missing', 'superseded');
CREATE INDEX intake_items_inbox_state ON intake_items (inbox_id, state, id);
CREATE INDEX intake_items_node ON intake_items (node_id);

CREATE TABLE inbox_classifier_permissions (
  inbox_id INTEGER NOT NULL REFERENCES inboxes(id),
  task TEXT NOT NULL CHECK (task IN ('directory_category', 'file_kind')),
  policy_id TEXT,
  profiles TEXT NOT NULL DEFAULT '[]',
  evidence_scope TEXT CHECK (evidence_scope IN ('metadata')),
  revision INTEGER NOT NULL CHECK (revision >= 1),
  changed_at INTEGER NOT NULL,
  PRIMARY KEY (inbox_id, task)
);

CREATE TABLE routing_state (id INTEGER PRIMARY KEY CHECK (id = 1), revision INTEGER NOT NULL);
INSERT INTO routing_state (id, revision) VALUES (1, 0);
CREATE TABLE routing_destinations (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id),
  node_id INTEGER NOT NULL REFERENCES nodes(id),
  label TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 200),
  created_at INTEGER NOT NULL,
  removed_at INTEGER
);
CREATE UNIQUE INDEX routing_destinations_one_active ON routing_destinations (node_id) WHERE removed_at IS NULL;
CREATE TABLE routing_rules (
  id INTEGER PRIMARY KEY,
  inbox_id INTEGER REFERENCES inboxes(id),
  match_kind TEXT NOT NULL CHECK (match_kind IN ('category', 'file_kind')),
  match_value TEXT NOT NULL,
  destination_id INTEGER NOT NULL REFERENCES routing_destinations(id),
  priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 1000),
  created_at INTEGER NOT NULL,
  removed_at INTEGER
);

ALTER TABLE classifications ADD COLUMN file_kind TEXT CHECK (file_kind IS NULL OR file_kind IN
  ('image', 'video', 'audio', 'document', 'source_file', 'archive', 'installer', 'executable', 'unknown'));
ALTER TABLE inference_requests ADD COLUMN intake_item_id INTEGER REFERENCES intake_items(id);
ALTER TABLE inference_requests ADD COLUMN observed_intake_generation INTEGER;
CREATE INDEX inference_requests_intake ON inference_requests (intake_item_id) WHERE intake_item_id IS NOT NULL;
```

JSON columns that other slices read:
- `intake_items.proposal_blockers`: an array of `{"code": RoutingBlocker, "detail": string, "rule_ids": ["7", "9"]}`. `rule_ids` is present only for `rule_conflict`. It is `[]` when the item has no blocker or no proposal yet.
- `intake_items.proposal_basis`: `{"generation": 3, "routing_revision": 12, "destination_node_id": "45", "destination_observation_revision": 7, "destination_intent_revision": 2}`. It is NULL before the first `Propose`. Readers mark a proposal stale when any value differs from the current one.
- `inbox_classifier_permissions.profiles`: a sorted JSON array of profile IDs, as for `classifier_permissions`.

### Go signatures (foundation unless marked)

```go
// internal/domain
type InboxID int64
type IntakeItemID int64
type IntakeState string // IntakeObserved … IntakeSuperseded; func (IntakeState) Open() bool
type ReadinessBasis string // ReadinessHeuristicStable, ReadinessHandoffConfirmed
type IntakeBlocker string  // BlockerTemporaryName, BlockerListingIncomplete, BlockerAccessError,
                           // BlockerSourceUnavailable, BlockerSymlink, BlockerSpecialEntry
type RoutingBlocker string // RoutingNoRule, RoutingRuleConflict, RoutingNameConflict,
                           // RoutingDestinationUnavailable, RoutingDestinationAtomic,
                           // RoutingDestinationInInbox, RoutingCrossSource
const DirtyNotification DirtyReason = "notification"
const DirtyNotificationOverflow DirtyReason = "notification_overflow"
const DecisionReasonInboxArrival DecisionReason = "inbox_arrival"
type NotificationMode string // NotificationsWatching, NotificationsDegraded, NotificationsPollingOnly
type NotificationReason string // "watch_budget", "os_watch_limit", "network_filesystem",
                               // "backend_off", "unsupported", "notification_overflow"

// internal/jobs
type Class int // ClassReconciliation (zero value), ClassInteractive, ClassBulk
const KindIntake Kind = "intake"
func (r *Runner) RegisterClass(kind Kind, h Handler, class Class) // Register = RegisterClass(kind, h, ClassReconciliation)
type Runtime interface {
	Progress(counters map[string]int64)
	FSCall(op string) (done func())
	// Yield offers the job's device at a work-unit boundary, with no transaction open (design D13).
	Yield(ctx context.Context) error
}
// WakeOnce is EnqueueOnce that also makes a queued job with a later available_at available now.
func (t *Tx) WakeOnce(spec Spec) (rec Record, coalesced bool, err error)
// F0's Runner.Yield returns ctx.Err() at once; slice J adds the arbiter behind the same signature.

// internal/config
type Inbox struct { PollInterval, QuietInterval, StalledAfter Duration; TemporaryPatterns []string }
type Watch struct { Backend string; Coalesce, MaxDelay Duration; Budget int }
// Config gains `Inbox Inbox toml:"inbox"` and `Watch Watch toml:"watch"`.

// internal/classify/permission
type Grant struct { /* existing fields */ Inbox domain.InboxID; Task contract.TaskID } // zero for source grants
func CurrentInbox(ctx context.Context, q Queryer, inbox domain.InboxID, task contract.TaskID) (Grant, error)
func SetInbox(ctx context.Context, tx *sql.Tx, g Grant, expected int64, now time.Time) (Grant, error)
func SentInbox(ctx context.Context, q Queryer, inbox domain.InboxID, task contract.TaskID, revision int64) (int64, error)

// internal/classify/requests
// CreateArrival is Create for an intake item's generation; it reuses an open request of the node and task.
func CreateArrival(ctx context.Context, tx *sql.Tx, item domain.IntakeItemID, generation int64,
	task contract.TaskID, t Trigger, policy string, now time.Time) (id int64, created bool, err error)
// CancelArrival cancels item's pending (unsent) requests; sent ones run to completion.
func CancelArrival(ctx context.Context, tx *sql.Tx, item domain.IntakeItemID, now time.Time) (int64, error)

// internal/classify/contract
// OutboundDirectory and OutboundFile gain: Readiness string `json:"readiness,omitempty"`
// internal/classify/tasks
// FileDescriptor and the directory builder gain: Readiness domain.ReadinessBasis

// internal/inventory
type Notifications struct {
	Mode           domain.NotificationMode
	Reason         domain.NotificationReason // "" when watching
	Watched, Unwatched, Budget int
	Overflows      int64
	LastOverflowAt *time.Time
}
// IndexHealth.Notifications changes from string to Notifications. Options gains
// NotificationStatus func(domain.SourceID) Notifications; nil = polling_only/backend_off.
// explorer.Deps gains the same NotificationStatus field and passes it to inventory.

// internal/discovery
type WatchHook interface { EnsureWatched(ctx context.Context, dir domain.NodeID) }
// Deps gains Watches WatchHook (nil = no watcher) and Intake *inbox.Service (slice I; nil = no intake).

// internal/inbox (slice I)
type Proposer interface {
	// Propose recomputes item's proposal columns from its triage, the rules, and the destinations, in tx.
	Propose(ctx context.Context, tx *sql.Tx, item domain.IntakeItemID, now time.Time) (valid bool, err error)
	// Refresh re-proposes every triaged open item of source whose proposal_basis differs from the current
	// values, and returns the items whose validity changed.
	Refresh(ctx context.Context, tx *sql.Tx, source domain.SourceID, now time.Time) (map[domain.IntakeItemID]bool, error)
}
func LockedBy(ctx context.Context, q Queryer, node domain.NodeID) (Lock, bool, error)
type Lock struct { Inbox domain.InboxID; Node domain.NodeID }
// *Service also implements inference.IntakeSink.

// internal/classify/inference (slice C)
type IntakeSink interface {
	Classified(ctx context.Context, tx *sql.Tx, item domain.IntakeItemID, generation int64, r ArrivalResult, now time.Time) error
}
type ArrivalResult struct {
	Request int64
	Stale   bool   // CAS failed: the item is not changed by the result
	Failed  bool   // ended without an answer (attempts exhausted, not permitted, misconfigured)
	Applied bool   // the label became effective (category on the node, or the item's file kind)
	Label   string // "" when none
	Profile string
	Detail  string
}
// Deps gains Intake IntakeSink (nil = file and arrival requests are cancelled as not eligible).

// internal/inbox/routing (slice R)
func New(d Deps) *Service // *Service implements inbox.Proposer

// internal/watch (slice W)
type Backend interface {
	Open(source domain.SourceID, root string) (SourceWatcher, error)
}
type SourceWatcher interface {
	Add(rel []byte) (WatchID, error) // ErrWatchLimit for ENOSPC/EMFILE
	Remove(id WatchID) error
	Events() <-chan Event            // Event{Watch WatchID; Overflow bool; Dropped bool}
	Close() error
}
func NewWatcher(d WatcherDeps) *Watcher // Start(ctx), Sync(ctx), EnsureWatched(node), Status(source) inventory.Notifications
func (s *Scheduler) Nudge(ctx context.Context, source domain.SourceID) error
```

### Commands (all single-item; none takes a list)

All commands share the M1 envelope: an `Idempotency-Key`, strict JSON, `400 invalid_request` for malformed or contradictory bodies, `404 not_found` for an unknown node, item, inbox, destination, or rule, and `409 revision_conflict` on a stale expected value. IDs are decimal strings.

- **`set-inbox-role`** (I).
  - Request: `{"target": {"node_id": "12", "expected_intent_revision": 4} | {"peek_token": "…"}, "role": "inbox" | "normal", "enroll_existing": true}`. `enroll_existing` is required for `inbox` and forbidden for `normal`.
  - 200: `{"node_id": "12", "inbox_id": "3" | null, "role": "inbox", "intent_revision": 5, "enrolled": 3, "ended_items": 0, "cancelled_requests": 0}`.
  - 409 `invalid_node_state` with a detail naming the refusal and the node or inbox it concerns.
- **`pause-inbox`** (I).
  - Request: `{"inbox_id": "3", "paused": true, "expected_revision": 2}`.
  - 200: `{"inbox_id": "3", "paused": true, "revision": 3, "cancelled_requests": 5}`.
- **`mark-intake-ready`**, **`retry-intake`**, and **`ignore-intake`** (I).
  - Request: `{"item_id": "88", "expected_generation": 3}`.
  - 200: `{"item_id": "88", "state": "queued", "generation": 3, "readiness_basis": "handoff_confirmed" | null, "request_id": "41" | null}`. `request_id` is set only by `retry-intake`.
  - 409 `invalid_node_state` for an ended item, for `retry-intake` outside `needs_review` or `failed`, and for any item of a paused inbox.
- **`set-routing-destination`** (R).
  - Request: `{"action": "add", "node_id": "45", "label": "Projects", "expected_routing_revision": 11}`, `{"action": "relabel", "destination_id": "3", "label": "…", "expected_routing_revision": 11}`, or `{"action": "remove", "destination_id": "3", "expected_routing_revision": 11}`.
  - 200: `{"destination_id": "3", "routing_revision": 12, "removed_rules": 0}`. Removing a destination also removes its rules.
  - 409 `invalid_node_state` for a node that is not an active expanded directory, is an inbox, or lies inside one.
  - 400 `invalid_request` for a duplicate add, or a label that is empty or longer than 200 characters.
- **`set-routing-rule`** (R).
  - Request: `{"action": "add", "match": {"category": "source_project"} | {"file_kind": "document"}, "inbox_id": "2" | null, "destination_id": "3", "priority": 10, "expected_routing_revision": 12}`, or `{"action": "remove", "rule_id": "7", "expected_routing_revision": 12}`.
  - 200: `{"rule_id": "7", "routing_revision": 13}`.
  - 400 `invalid_request` for an unknown category or file kind, a priority outside 0 to 1000, or an identical active rule.
- **`set-classifier-policy`** (C) adds the form `{"inbox_id": "3", "task": "file_kind", "policy_id": "files" | null, "profiles": [...], "evidence_scope": "metadata", "expected_revision": 0}`.
  - Exactly one of `source_id` and `inbox_id` is allowed. The policy's task must equal `task`.
  - The response adds `inbox_id` and `task`. The audit detail adds the keys `inbox_id` and `task`.

### Read endpoints (U, except as marked)

- **`GET /api/inboxes`**: `{"items": [{"inbox_id", "source_id", "node_id", "name", "name_b64", "state": "active" | "paused" | "unavailable", "paused", "revision", "grants": {"directory_category": GrantJSON | null, "file_kind": GrantJSON | null}, "counts": {state: n, …}, "stalled": n, "metrics": {"settling_ms": {"p50", "max"} | null, "first_suggestion_ms": {"p50", "max"} | null}}]}`.
  - Metrics cover the last 100 items that became ready.
  - The response carries no cursor, because inboxes are few.
- **`GET /api/inboxes/{id}/items?state=&cursor=`**: `{"items": [ItemJSON], "next": cursor | null}`. Pages hold 100 items in `id` order.
  - `state` is one of the ten states. Anything else is `400 invalid_request`.
  - `ItemJSON` is `{"item_id", "node_id", "name", "name_b64", "kind", "state", "end_reason", "generation", "arrived_at", "last_change_at", "stalled", "readiness": {"basis", "blocker", "provisional"}, "triage": {"category", "category_source", "file_kind", "file_kind_source", "suggested_triage"}, "classifier": {"profile", "status", "stale"} | null, "proposal": {"destination_id", "destination_node_id", "label", "basename", "basename_b64", "rule_id", "valid", "stale", "blockers": [...]} | null, "organization": {"available": false, "reasons": [...]}, "detail"}`.
- **`GET /api/routing`**: `{"revision": 12, "destinations": [{"destination_id", "node_id", "name", "name_b64", "label", "state": "ok" | "unavailable" | "atomic" | "in_inbox"}], "rules": [{"rule_id", "match": {…}, "inbox_id", "destination_id", "priority"}]}`.
- **`GET /api/sources/{id}/index-health`** (W): `notifications` becomes `{"mode", "reason", "watched", "unwatched", "budget", "overflows", "last_overflow_at"}`.
- **`GET /api/nodes/{id}/classifier-preview?profile=`** (C) also accepts a file node with an open item. Any other file is `409 invalid_node_state`.
- **Pages** (U): `/inboxes`, `/inboxes/{id}`, and the overview's inbox lines. On the node inspector, an "Intake" section for arrivals and inboxes. On `/classifier`, the inbox grant forms. Inbox pages are live in the `inbox` mode (D7).

## Transaction boundaries

| Path (transaction) | Re-checked inside the writing transaction | Another command commits just before | …just after |
|---|---|---|---|
| Intake listing batch (`commitBatch` + `ObserveChild`) | M2a's epoch and `boundaryHolds`; the inbox row still active; each open item's fingerprint and generation | role removal: no active inbox, so no enrollment and the listing is ordinary. Pause: enrollment only | role removal ends the items just touched. `mark-intake-ready` with the old generation gets `revision_conflict` |
| Arrival probe (`commitProbe` + `ObserveProbe`) | probe CAS (M2a); the item still open and its generation | a change bumped the generation: this probe compares against it | a later change: the next probe advances the generation |
| `inbox.Ready` and `inbox.Triage` (one `jobs.Tx` per batch) | the item still open and its inbox unpaused; its generation not yet decided; the node's observation and intent revisions; `inbox.Want` reads the grant revision; `CreateArrival` | pause: nothing advances. Revocation: no request, rules-only triage | change: `CancelArrival` cancels the request at the next observation, and a request already sent ends stale |
| Classify build and reserve (M3 + D11) | the M3 checks, plus `CurrentInbox` grant revision and item generation for arrival requests | revocation or pause: cancelled, nothing sent | the in-flight attempt completes, and its result is stale |
| Apply (M3 + D11) | M3's CAS plus `observed_intake_generation = generation`; `IntakeSink.Classified` in the same transaction; `Propose` | a change: stale, and the item stays settling | the owner ignores the item: `Classified` finds it ended and records nothing |
| `routing.Refresh` and `Propose` | destination node active, expanded, outside inboxes, same source; routing revision; same-name active children; item generation | rule removed: the item has no rule | rule changed: the basis no longer matches, the proposal reads stale, and the next pass recomputes it |
| `set-inbox-role` | expected intent revision; node active and expanded; no active inbox above or below; for removal, `CancelArrival` per open item and grant revocation | collapse of an ancestor: the node is inactive, `invalid_node_state` | a listing commits: the hook sees the inbox, or not, consistently in its own transaction |
| `pause-inbox`, item commands | expected inbox revision or item generation; item open | a change bumped the generation: `revision_conflict` | the intake pass sees the new basis or pause at its next batch |
| `set-routing-destination` and `set-routing-rule` | expected routing revision; destination node checks | role set on the node: refused | proposals become stale by the basis |
| Watcher flush | `Hint` is idempotent per dirty version; inactive nodes are skipped | a collapse: the scope is gone, and the hint is skipped | the listing pass sees the higher dirty version and is not marked clean (M2a) |
| Arbiter grant (in memory) + runner claim transaction | the claim transaction re-checks the job is queued and claimable; a yielded job holds no transaction | a cancel: the job is not claimed | a cancel during `Yield`: `ctx.Err()`, and the job ends cancelled |

## Risks / Trade-offs

- **[A 5-s poll on a large inbox lists many entries.]** Batches are bounded, and inbox listings yield. Operator docs recommend keeping inboxes small and moving cataloged arrivals out (M5).
- **[An event per poll from the intake job.]** `app.js` ignores `intake` events outside the inbox page (D7). Event retention prunes them like other events.
- **[In-memory arbiter state.]** A restart resets credits and may briefly favour one class. Aging bounds any wait to 5 minutes.
- **[A yielded walk keeps file descriptors open while it waits.]** At most one directory handle per waiting job is held. Waits are bounded by the rotation.
- **[A symlink swapped at a watched path]** makes a watch follow it outside the source (D14). It produces hints only. A pass reads through rooted access and never follows it.
- **[Inode reuse across polls]** may make a quickly replaced arrival look unchanged, when size and mtime are equal too. That case is accepted as quiet. M5 re-validates before any move.
- **[`name_conflict` uses the catalog]** (D12). The UI states that conflicts are checked against the last listing of the destination.

## Migration Plan

1. Back up with `curator backup`.
2. On first start, migration `0005` runs. It is additive, and existing rows need no backfill. The M3 binary refuses the newer schema.
3. Without any inbox, behavior is that of M3, except two things. Jobs on one device now share it by class, and notifications are on by default (`watch.backend = "auto"`). Set `watch.backend = "off"` to keep pure polling.
4. To use an inbox, the owner refines the path to the directory, sets the role on the inbox page, then adds destinations and rules. Optionally the owner grants the inbox a policy per task, after reading the payload preview.
5. **Rollback:** stop the server, restore the backup, and start the M3 binary.

## Addendum: decisions made during apply

Added at archive (2026-10-03). These decisions were made or confirmed while M3a was implemented, and the code on `master` follows them. Two departures from `directory-first-curator-spec-v0.2.md` are recorded as ADRs: D30 in `docs/adr/0003-inbox-special-entries-are-issues.md`, and the scope choices of D22 in `docs/adr/0004-m3a-scope-deferred-to-m5.md`. D28 corrects this change's own spec delta, not the source spec. D23 and D40 depart from the wording of the Interfaces section and are recorded here.

### Vocabulary and configuration

**D23. `DecisionReasonInboxArrival` is an untyped string constant**, like the other decision reasons. The Interfaces section writes `DecisionReason` as a type, which does not exist in the code.

**D24. Notification status.**
- The reason constants are `NotificationWatchBudget`, `NotificationOSWatchLimit`, `NotificationNetworkFS`, `NotificationBackendOff`, `NotificationUnsupported`, and `NotificationOverflowReason` (value `notification_overflow`).
- `inventory.PollingOnly()` (mode `polling_only`, reason `backend_off`) is every source's status when no watcher runs.
- The index-health `notifications` object has `mode`, `reason` (null while watching), `watched`, `unwatched`, `budget`, `overflows`, and `last_overflow_at`.

**D25. Configuration details.** `watch.max_delay` has a 10 ms floor, and the coalesce/max_delay relation is reported on `watch.coalesce`. `github.com/fsnotify/fsnotify` v1.10.1 is the only new dependency.

### Jobs and fairness

**D26. Round order and credit.** Within a round, classes with credit left are served interactive, reconciliation, bulk (IIIIRRB). A lone waiting class spends no credit. A contested `Yield` where the yielding job ranks first counts as a grant to it, so jobs of one class alternate per work unit.

**D27. Yield points.**
- The scan yields after every frontier step (a probe or a listing step) and after each listing batch. The aggregate walk yields after each non-empty batch.
- A cancelled `Yield` returns without the slot, so the handler's wind-down (commit, closing handles) does no filesystem reads.
- `WakeOnce` clears a deferral's terminal detail.
- Inspection's guard is not a `jobs.Runtime`, so the aggregate walker reaches `Yield` through the runtime it is given.

### Notifications

**D28. A notification hint makes its scope due at once, like any hint.** After the dispatched pass, the next due time follows the ordinary interval; the watcher never writes `due_at`. The scenario "Event storm coalesces" was corrected to say so.

**D29. Watcher lifecycle.** `NewWatcher` with a nil backend uses fsnotify. With `watch.backend = "off"` the watcher stays inert, and its status is `PollingOnly()`. Serve runs `Watcher.Sync` after each scheduler tick, `Start` after `runner.Start`, and `Close` before the runner stops.

### Intake

**D30. Special entries in an inbox get no intake item** (`nodes.kind` cannot hold them). They stay the inbox listing's `special_file` issue, shown on the inbox page and as `special_entries` `{name, name_b64, kind, detail}` in `GET /api/inboxes`. See ADR 0003.

**D31. Enrollment and readiness.**
- Mount boundaries inside an inbox are not enrolled. Symlinks enter `needs_review` directly.
- A file becomes ready only at the end of a complete inbox listing (`ListingDone`). A directory becomes ready at its probe, and its first digest is a baseline.
- `failed` means the item's own entry failed and was not observed for `reconciliation.failure_backoff`. Source unavailability only blocks readiness (`source_unavailable`).

**D32. Pausing.** Items enrolled while paused stay `observed`. Pausing returns a `classifying` item whose pending request was cancelled to `queued`, so it is triaged again after resume.

**D33. One request per settled generation.** An item stays `queued` while another request of its node and task is still open. Retry triages a `needs_review` item again (a file's kind resets to its name hint) and creates one owner request when the policy allows it. Retry returns a `failed` item to settling.

**D34. Scheduling the intake job.**
- The scheduler pass creates the intake job of a source with an unpaused inbox before the hold check. `anyDue` delegates to `discovery.AnyScanDue`, so due inbox scopes never enqueue empty scans.
- `begin` closes only interrupted runs of the same job or of inactive jobs, because scan and intake list concurrently.
- A listing of a root inbox ends the intake run with a deferral of "now", because the root handle's listing is consumed.
- `items_changed` counts state, generation, and basis changes, enrollments, endings, and changes in proposal validity.

**D35. Cancellations outside the handler never reach `Classified`.** These are inbox-grant revocation and `CancelArrival`. The intake pass moves a `classifying` item whose generation has no open arrival request on through the proposer.

**D36. The role.**
- `intent.SetInboxRole` takes the `*inbox.Service` as an argument.
- Removing the role is allowed when the inbox directory has vanished.
- Bulk `set-disposition` and the cleanup selection preview exclude the inbox lock with reason `inbox` and its `inbox_id`; the pin fields are null.

**D37. Serve wiring.** The inbox and inference services need each other, so serve passes the inbox a `routingRef` that forwards to the inference routing view once the classifier is built. The M2a serve reconciliation test runs with `watch.backend = "off"`, so it still proves polling alone.

### Classifier

**D38. `ArrivalResult` lives in `classify/requests`,** not in `inference`, so `inbox` implements `inference.IntakeSink` without importing `inference`.

**D39. Arrival requests and grant accounting.**
- `permission.Sent` excludes attempts whose request has an `intake_item_id`, because arrival attempts store the inbox grant revision in the same `permission_revision` column. `SentInbox` counts them through `intake_items.inbox_id`.
- `CancelArrival` writes a detail and clears `not_before`, like the tick's cancel path.
- `tasks.Outbound(d, ...Option)` passes directory readiness through `WithReadiness`. An empty readiness keeps M3's request bytes and cache keys.

**D40. Inbox grants (departs from the Interfaces section's `null`).**
- The inbox form of `set-classifier-policy` revokes with `"policy_id": ""`, like the source form. A null or absent `policy_id` is a 400.
- A source revoke or narrowing cancels only requests without an `intake_item_id`, and keeps classify jobs that still carry open arrival requests, because the source grant never covers arrivals.

**D41. File answers are applied by `inference.applyFile`,** because `classify.Apply` needs a category. For an applied answer it writes `intake_items.file_kind`, `file_kind_source`, and `first_suggestion_at` before `Classified`.

### Routing and pages

**D42. `routing.Refresh` recomputes every triaged item that has a proposal,** and rewrites only changed rows. Node deactivation, an ancestor becoming an inbox, and a new same-name child bump no destination revision. Readers also check destination liveness at read time: a stored valid proposal is shown as stale, and a stored blocker stays while its condition holds.

**D43. Inbox page.**
- The `settling` group holds observed and settling items; the `needs review` group holds needs_review and failed items. An empty `state=` lists all.
- A removed inbox stays readable with a banner; only a missing ID is a 404.
- Paging is per group (`?group=&cursor=`).
- A grant is shown as `{policy_id, profiles, revision}`, or null without a row.

**D44. Live updates.** `explorer.Deps.StalledAfter` carries `inbox.stalled_after`. The inbox page reloads when the intake job's `items_changed` grows. Classification results that arrive through the classify job show the live notice instead.

### Tests

**D45. Spec sizes run under `-tags slow`.** They are the job-runner A36 tests at 100,000 entries and 500 directories, the scenario A36 at 500 directories, A41 at 200 files, and the A27 watch limit at 100 of 300 directories. Default runs scale down so each package stays under 60 s with `-race`: 20,000 entries and 30 directories in the job-runner tests, 1,000 walk entries, 8 directories, and 16 files in the scenarios, and 10 of 30 watches.

**D46. Scenario harness.**
- A send on `watchTick` returns before the step reads the clock. A flush therefore sends twice before advancing the clock and twice after.
- The continuous-arrival A36 scenario reads the device's grant order from the recorder's call log, since one worker holds the device, instead of stepping a gate. Its pending scopes are atomic units probed by a subtree refresh, and arrivals are injected from an instrument hook.
