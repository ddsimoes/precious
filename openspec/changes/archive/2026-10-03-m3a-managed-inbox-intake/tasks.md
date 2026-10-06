# Tasks

## 1. Foundation: schema, shared signatures, and primitives (coordinator, on `master`)

- [x] 1.1 Write `migrations/0005_managed_inbox.sql` exactly as in the design's Interfaces schema (D20). Paths: `migrations/`, `internal/store/*_test.go`. Verify with store tests that:
  - an M3-shaped database migrates with every existing row intact;
  - a second open intake item for the same inbox and node is rejected, while an ended one plus an open one is accepted;
  - a second active inbox on one node, and a second active destination on one node, are rejected;
  - invalid intake states, readiness bases, blockers, file kinds, rule match kinds, and priorities outside 0–1000 are rejected;
  - `routing_state` holds exactly one row with revision 0.
- [x] 1.2 Add the domain vocabulary (Interfaces: `InboxID`, `IntakeItemID`, `IntakeState` with `Open()`, `ReadinessBasis`, `IntakeBlocker`, `RoutingBlocker`, `DirtyNotification`, `DirtyNotificationOverflow`, `DecisionReasonInboxArrival`, `NotificationMode`, `NotificationReason`), and extend `DirtyReason.Valid`. Paths: `internal/domain`. Verify with table tests: `Open()` is true exactly for the seven open states, and every new value round-trips through `Valid`/parsing.
- [x] 1.3 Add `[inbox]` and `[watch]` to the configuration (D19). Paths: `internal/config`, `deploy/examples/read-only-no-model.toml`, and the `docs/operator.md` configuration-reference rows. Verify with:
  - tests for **Quiet interval too short**, **Invalid temporary-name pattern**, a coalescing window longer than `watch.max_delay`, a negative `watch.budget`, `watch.backend = "inotify"` rejected, and `stalled_after` below `quiet_interval`;
  - `TestOperatorDocsConfigReference`, `TestExampleConfig`, and `TestEveryExampleConfigLoads`.
- [x] 1.4 Extend `internal/jobs` with the Interfaces signatures (D13, D7): `Class`, `RegisterClass` (`Register` keeps `ClassReconciliation`), `KindIntake`, `Runtime.Yield` (here it returns `ctx.Err()` at once), and `Tx.WakeOnce`. Add `discovery.IntakeSpec(source) jobs.Spec` (kind `intake`, ScopeKey `intake:<source>`) and the `discovery.WatchHook` interface with a nil-safe `Deps.Watches` field. Paths: `internal/jobs`, `internal/discovery/intake_spec.go`, `internal/discovery/scan.go` (Deps field only). Verify with runner tests:
  - `WakeOnce` coalesces onto a queued job and moves its later `available_at` to now without changing attempts;
  - it creates the job when none is active;
  - a deferred job woken by it is claimed at once;
  - plus the full race suite unchanged.
- [x] 1.5 Add inbox grants and arrival requests (D11):
  - in `permission`: `CurrentInbox`, `SetInbox`, `SentInbox`, and `Grant.Inbox`/`Grant.Task`;
  - in `requests`: `CreateArrival` and `CancelArrival`;
  - in `contract`: `Readiness` with `omitempty` on `OutboundDirectory` and `OutboundFile`;
  - in `tasks`: `Readiness` on `FileDescriptor` and the directory builder.

  Paths: `internal/classify/{permission,requests,contract,tasks}`. Verify with:
  - tests that an inbox grant has its own revision and conflicts independently of the source grant;
  - `CreateArrival` reuses an open request of the node and task, and records the item and generation;
  - `CancelArrival` cancels pending rows only and leaves running ones;
  - an empty `Readiness` leaves the rendered bodies, the recorded contract fixtures, and `CacheKey` byte-identical to M3, while a set one changes the key.
- [x] 1.6 Change `inventory.IndexHealth.Notifications` to the `Notifications` struct, with an `Options.NotificationStatus` provider whose nil default reports `polling_only`/`backend_off`. Add `explorer.Deps.NotificationStatus`, render the existing polling-only text from the struct, and add the `docs/operator.md` section skeletons with owner markers for slices J, W, I, R, C, and U. Paths: `internal/inventory/health.go`, `internal/web/explorer/{explorer,health}.go`, `docs/operator.md`. Verify with the existing health tests and the full race suite passing unchanged.

## 2. Slice J: priority classes and fair device sharing

- [x] 2.1 Implement the device arbiter and the real `Runtime.Yield` in the runner (D13). Paths: `internal/jobs`. Verify with runner tests over fake handlers:
  - the grant sequence with three classes waiting follows 4:2:1 (counted grants, not time);
  - FIFO order within a class;
  - a job waiting more than 5 min (fake clock) is granted as interactive;
  - a placeholder job never yields;
  - a pool job's `Yield` returns at once;
  - cancellation while blocked in `Yield` ends the job `cancelled`;
  - a yielded job's lease keeps being renewed, and a restart while a job is yielded recovers it through the lease path with one more attempt.
- [x] 2.2 Call `rt.Yield` after each frontier probe and each listing batch of the scan, and after each walk batch of the aggregate walker. Paths: the `Yield` call sites only in `internal/discovery/scan.go` and `listing.go`, plus `internal/inspection/walk.go`. Verify with:
  - **A yielded scan continues**, with an interactive fake job enqueued 10 times during a 2,000-entry listing;
  - a test task for **A36 inbox work during a bulk walk** at runner level: an interactive fake job enqueued during a 100,000-entry synthfs walk runs before the walk ends, and the walk's measurement equals an uninterrupted run's;
  - a test task for **A36 continuous arrivals do not starve reconciliation**: an interactive job re-enqueued after every grant, and the scan still receiving at least 1 of every 7 grants and completing.
- [x] 2.3 Fill the "Priority and fairness" docs section (classes, weights, aging, and what yielding means for job progress). Paths: `docs/operator.md`. Verify with the docs tests.

## 3. Slice W: notifications

- [x] 3.1 Add `github.com/fsnotify/fsnotify` v1.10.1, the fsnotify `watch.Backend` (one watcher per source; `ErrWatchLimit` for `ENOSPC`/`EMFILE`), and `watch.FakeBackend` (D14). Paths: `go.mod`, `go.sum`, `internal/watch/backend*.go`. Verify with:
  - a Linux test over a temp directory: create, write, and remove events arrive for the watched directory and none for an unwatched subdirectory;
  - FakeBackend tests for overflow, a dropped watch, and the limit error.
- [x] 3.2 Implement `watch.Watcher`: `Sync` (watch order and budget, never atomic or inactive nodes), `EnsureWatched`, the coalescer flush (`Hint`, intake `WakeOnce` for inbox scopes, `Scheduler.Nudge`), overflow and failed flushes via `MarkSource`, dropped watches, network-filesystem detection, and `Status`. Paths: `internal/watch`. Verify with tests over FakeBackend and a fake clock for:
  - **Event storm coalesces** and **Atomic units are not watched**;
  - a test task for **A27 watch overflow**: a coverage gap with reason `notification_overflow`, every scope hinted, and recorded FS calls showing no access below any atomic unit's immediate entries after the passes;
  - a test task for **A27 watch limit reached**: `degraded`, `os_watch_limit`, 200 unwatched, and an addition in an unwatched directory found by its scheduled pass;
  - **Network filesystem is polled**.
- [x] 3.3 Show the notification state on the overview, the index-health page, and the API from `NotificationStatus`. Paths: `internal/web/explorer/health*.go`, `web/templates/{health,overview}.html`. Verify with page and API tests for **A27 polling-only status is visible** (backend off) and **Degraded notifications are visible**.
- [x] 3.4 Fill the "Notifications" docs subsection under Incremental reconciliation (backend, budget order, coalescing, overflow, limits, network filesystems, `watch.backend = "off"`), and update the index-health field table. Paths: `docs/operator.md`. Verify with the docs tests.

## 4. Slice I: inbox role, intake lifecycle, readiness, and triage

- [x] 4.1 Create `internal/inbox` (D5, D6, D8, D10, D11, D17):
  - item lifecycle: `ObserveChild`, `ObserveProbe`, `Gone`, `Ready`, `Triage`, `Refresh` through a `Proposer`;
  - readiness, eligibility (`Want`), `Classified` (implementing `inference.IntakeSink`), `LockedBy`, `Activate`, `Remove`, and the item commands' service methods.

  Paths: `internal/inbox`, `inventorytest` seeders for inboxes and items. Verify with tests over a real store and a fake clock:
  - **Generations change only with evidence** (**Unchanged polls cost nothing**);
  - **Heuristic readiness for files** at quiet-interval boundaries;
  - temporary names, partial listing, access errors, and an unavailable source never making an item ready;
  - **Replacement at the same name**;
  - **Stalled copy**;
  - a `Want` table including **An inbox needs its own grant** and **Only unknown, ready arrivals are sent**;
  - `Classified` with stale, failed, applied, and not-applied results using a fake `Proposer`.
- [x] 4.2 Add the intake job to `discovery`: `NewIntakeHandler` (D7), enrollment hooks in listing, probe, and tombstone transactions, `DispatchDue` excluding inbox scopes, arrival decisions gated on readiness with mode forced atomic (D10), `Yield` between units, and `Defer` to the next due time. Also give inbox scopes the poll interval in `reconcile`. Paths: `internal/discovery`, `internal/reconcile`. Verify with discovery tests over synthfs and recorded FS calls for:
  - a test task for **A32 repeated events for one copy**, **A32 slow file copy**, and **A32 temporary-name arrival**;
  - a test task for **A33 quiet top level while nested writes continue**: per-poll FS calls within the shallow budget, and zero descendants cataloged;
  - **Inbox polled on its own interval** and **Inbox scopes go to the intake job**;
  - **Large installation is one fast item** and **Mixed arrival is not expanded**;
  - a test task for **A41 the same arrival is not duplicated across a restart**: a runner restart with 50 settling items.
- [x] 4.3 Add the role lock and `set-inbox-role` (D3, D4): `intent.SetInboxRole`, the collapse and `cleanup_candidate` refusals, and the role lock in `classify.Recompute` triage. Paths: `internal/intent`, `internal/classify/triage.go`, `internal/commands/set_inbox_role.go`. Verify with:
  - a test task for **A40 inbox role inside an atomic ancestor** (peek-token target);
  - **Nested inboxes refused** and **Role changes are audited**;
  - a test task for **A40 attempted collapse of an inbox and its ancestor**, **A40 collapse above an inbox**, and **A40 inbox cannot become a cleanup candidate** (single-node and bulk);
  - **Existing children left cataloged**;
  - an arrival's suggested triage being `review` where M3 would give `cleanup_candidate`.
- [x] 4.4 Add the commands `pause-inbox`, `mark-intake-ready`, `retry-intake`, and `ignore-intake`, with bodies and errors as in Interfaces. Paths: `internal/commands`. Verify with command tests for:
  - **Change after mark ready**;
  - **A paused inbox sends nothing**: pending rows cancelled, and nothing becomes ready while paused;
  - **Retry after a provider failure**: one new request row for the same generation, and the item `classifying`;
  - an ignored item leaving intake;
  - `revision_conflict` and `invalid_node_state` cases;
  - `TestEveryCommandNameRegisteredOnce`.
- [x] 4.5 Fill the "Managed inbox" docs section: role and lock, activation choices, lifecycle states, readiness bases and blockers, commands with doc-tested JSON examples, and pausing. Paths: `docs/operator.md`, a docs test in `internal/commands`. Verify with the docs tests.

## 5. Slice R: destinations, routing rules, and proposals

- [x] 5.1 Create `internal/inbox/routing` (D12) implementing `inbox.Proposer`. Paths: `internal/inbox/routing`. Verify with tests over seeded items, nodes, destinations, and rules for:
  - **Higher priority wins** and **Equal-priority conflict goes to review**;
  - **Source project proposed** and **No matching rule**;
  - **Rule removed after a proposal**, with the basis making it stale before `Refresh`;
  - a test task for **A37 destination conflicts and changes**: each of the five blockers, with no node or filesystem change recorded.
- [x] 5.2 Add the commands `set-routing-destination` and `set-routing-rule`, with audit and the routing revision. Paths: `internal/commands`. Verify with command tests for **Destination inside an inbox refused**, **Atomic destination refused**, removing a destination removing its rules, an identical rule rejected, and `revision_conflict`.
- [x] 5.3 Fill the "Destinations and routing rules" docs section with doc-tested JSON examples and the blocker table. Paths: `docs/operator.md`, a docs test in `internal/commands`. Verify with the docs tests.

## 6. Slice C: classifying inbox arrivals

- [x] 6.1 Add the inbox form of `set-classifier-policy` (D11): an exactly-one-target check, the task match, audit keys, and `CancelArrival` on revoke or narrowing. Paths: `internal/commands/set_classifier_policy.go`. Verify with command tests for **Inbox grant revoked after enqueue**, a task mismatch rejected, and the source grant unchanged.
- [x] 6.2 Extend `internal/classify/inference` for arrival requests (D11):
  - `file_kind` handling for open file arrivals, with grant checks through `CurrentInbox`;
  - `Readiness` in outbound descriptors;
  - file results stored in `classifications.file_kind` and applied to the item;
  - the CAS on `observed_intake_generation`;
  - calls to `Deps.Intake.Classified`;
  - `ORDER BY intake_item_id IS NULL, id`.

  Paths: `internal/classify/inference`. Verify with handler tests through the `fake` adapter and a fake `IntakeSink` for:
  - **Arrival changed while its request was in flight**;
  - **Arrival ahead of a backlog**;
  - a file answer applied and not applied (`apply_categories`, `min_probability_bp`);
  - a revoked inbox grant before send;
  - a refused answer stored with a NULL kind;
  - the M3 handler tests still passing.
- [x] 6.3 Let the payload preview accept file arrivals, and add the model-classification docs for inbox grants, file requests, and request order. Paths: `internal/classify/inference/select.go`, `docs/operator.md`. Verify with a preview test for a file arrival (no credential, `readiness` present), `invalid_node_state` for an ordinary file, and the docs tests.

## 7. Slice U: inbox pages and read API

- [x] 7.1 Add the `inventory` read models (Interfaces read endpoints): inbox list with counts, stalled count, and metrics; items paging with state filter and stale detection from `proposal_basis`; the routing read model; and an intake summary for the inspector. Paths: `internal/inventory`, `inventorytest`. Verify with reader tests over seeded rows: counts, metrics over the last 100 ready items, stale proposals, and the `organization` reasons with and without a read-only source.
- [x] 7.2 Add the pages and endpoints:
  - endpoints `GET /api/inboxes`, `GET /api/inboxes/{id}/items`, and `GET /api/routing`;
  - pages `/inboxes` and `/inboxes/{id}`, the overview inbox lines, the inspector "Intake" section, and the inbox grant forms on `/classifier`;
  - the `app.js` controls, and the `inbox` live mode that ignores `intake` events elsewhere (D7).

  Paths: `internal/web/explorer`, `web/templates`, `web/static/app.js`. Verify with page and API tests for:
  - a test task for **A39 arrivals reviewable without models**;
  - **Items filtered by state**, **Provisional directory arrival**, and **Inbox grant previewed on a sample arrival** (through a fake previewer);
  - `404 not_found` and `400 invalid_request`;
  - a malicious arrival name rendered inert;
  - no inline script under the CSP.
- [x] 7.3 Update the docs: the Pages section (inbox list and page, inspector intake section, classifier page grants) and the Read API table rows. Paths: `docs/operator.md`. Verify with the docs tests.

## 8. Integration (coordinator)

- [x] 8.1 Wire `serve`:
  - the watcher, with backend `auto` or `off`, and `Status` into explorer;
  - `RegisterClass(KindIntake, …, ClassInteractive)` and `RegisterClass(inspection.KindAggregate, …, ClassBulk)`;
  - `inbox.New` with `routing.New` as its `Proposer`;
  - `inference.Deps.Intake`;
  - `discovery.Deps.{Intake,Watches}`;
  - the new command services.

  The scheduler tick wakes intake jobs for sources with unpaused inboxes. Paths: `cmd/curator`. Verify with a `cmd/curator` test: a file created in an inbox of a served temp tree is enrolled through a real inotify event before the next scheduled poll, and with `watch.backend = "off"` it is enrolled by the 5-s poll.
- [x] 8.2 Wire the scenario harness (fake watcher, intake job, routing, sink) and add scenario test tasks for **A32** (all three scenarios end to end) and **A33**. Paths: `internal/scenario`. Verify with `go test -race -run 'A32|A33' ./internal/scenario/`.
- [x] 8.3 Add scenario test tasks for **A36** (both scenarios, with the real scan, walk, and intake jobs on one device) and **A41** (rapid arrivals, hint storm, restart; counted listings and provider calls within the reservation). Paths: `internal/scenario`. Verify with `go test -race -run 'A36|A41' ./internal/scenario/`.
- [x] 8.4 Add scenario test tasks for:
  - **A37** end to end with routing commands;
  - **A39**, both the inbox-intake and the inbox-routing scenarios, on a read-only synthfs mount with notifications off and no classifier;
  - **A40**, every scenario through the HTTP commands.

  Paths: `internal/scenario`. Verify with `go test -race -run 'A37|A39|A40' ./internal/scenario/`.
- [x] 8.5 Add a scenario test task for **A27**: overflow and watch limit through the fake watcher, with the active frontier reconciled and no access below atomic units (recorded FS calls). Paths: `internal/scenario`. Verify with `go test -race -run A27 ./internal/scenario/`.
- [x] 8.6 Finish `docs/operator.md`: remove every owner marker, add "Upgrading from M3 to M3a", and update the intro and every sentence that says notifications or inboxes do not exist. Verify that `grep -n 'owner:' docs/operator.md` returns only prose, and that the docs tests pass.
- [x] 8.7 Run the final verification:
  - `gofmt -l .` empty;
  - `go vet ./...` and `go vet -tags e2e,slow,live ./...`;
  - `go test -race ./...` with every package under 60 s;
  - `go test -race -tags slow ./...`;
  - the e2e tests in privileged Docker `golang:1.27.1` for `fsaccess`, `sources`, and `discovery`;
  - a headless-Chrome pass over the inbox list and page, the overview inbox lines, the inspector's intake section, the classifier inbox grant form, and the pause, mark-ready, retry, ignore, and refine controls under the production CSP.

  Verify that all are clean.
- [x] 8.8 Smoke-check the built binary with a throwaway script:
  - `check-config` on each example;
  - `serve` over a synthetic tree with the backend `auto`;
  - set the inbox role on an `Incoming` directory;
  - copy a file slowly and a `.part` file, and observe settling, readiness, and no early triage;
  - mark ready;
  - add destinations and rules, and observe proposals and blockers;
  - grant the inbox a `file_kind` policy on the local Ollama profile, and observe exactly one request for an `unknown`-kind file;
  - pause and resume;
  - collapse refused above the inbox;
  - restart mid-settle with no duplicate items;
  - `curator backup` integrity.

  Verify that the script reports success with no metered calls, then delete it.
