# Tasks

## 1. Foundation: schema, shared signatures, and primitives (coordinator, on `master`)

- [x] 1.1 Write `migrations/0003_incremental_indexing.sql` per design D23: the `dirty_scopes` table and its two indexes, plus the new `nodes`, `reconciliation_runs`, `descriptors`, `aggregates`, and `sources` columns with their CHECKs and backfills. Paths: `migrations/`, `internal/store/*_test.go`. Verify with store tests that:
  - apply `0003` to an M2-shaped database seeded with inactive nodes, escalated descriptors, and a late-commit aggregate, then assert `unrecorded`, `escalated = 1`, and `applied = 0`;
  - reject an invalid `inactive_reason` or `last_outcome`.
- [x] 1.2 Add `domain.DirtyReason`, `ScopeKind`, `ScopeOutcome`, and `InactiveReason`. Change `domain.MeasurementCurrent` to take the measured and current observation revisions (D11), and update its callers in `internal/inventory/measurement.go` and `internal/discovery/decision.go` to pass the node's revision. Paths: `internal/domain`, plus those two call sites. Verify with a `MeasurementCurrent` table test that includes a revision mismatch, and with the full race suite passing.
- [x] 1.3 Add `[reconciliation]` with `expanded_interval`, `atomic_interval`, and `failure_backoff` (D19): defaults, range validation (zero allowed for the two intervals), `check-config` output, `deploy/examples/read-only-no-model.toml`, and the `docs/operator.md` configuration table. Paths: `internal/config`, `deploy/examples`, the `docs/operator.md` configuration reference. Verify with validation table tests for each bound, `TestExampleConfig`, and `TestOperatorDocsConfigReference`.
- [x] 1.4 Create `internal/reconcile` with exactly the Interfaces signatures (D1–D3, D15). It imports only `domain` and the standard library. Paths: `internal/reconcile`. Verify with unit tests over a real store for:
  - **Hints coalesce**;
  - every `Settle` outcome, including the capped exponential backoff and a disabled interval;
  - **Hint during a pass**: the scope stays dirty when `dirty_version > started`;
  - `Seed` spreading 900 scopes deterministically across one interval (**Work spread over the interval**);
  - `MarkSource` seeding missing rows and opening a gap;
  - rows of inactive and mount-boundary nodes ignored.
- [x] 1.5 Move device keys to claim time (D14, G2):
  - Remove `jobs.Spec.DeviceKey` and `sources.Registry.DeviceKey`/`Service.DeviceKey`.
  - Drop the `deviceKey` parameters of `discovery.SourceScanSpec`/`FrontierScanSpec`, `inspection.AggregateSpec`/`EnqueueAggregate`, and `intent.RefineRequest`, along with every command's plumbing.
  - Compute the key in the runner's claim, with placeholder exclusivity in `deviceSlots`.
  - Export `Tx.ActiveScan`.

  Paths: `internal/jobs`, `internal/sources`, `internal/discovery/scan.go`, `internal/inspection`, `internal/intent/refine.go`, `internal/commands`. Verify with a runner test for **Device limit before the first observation**, the existing **Single worker per device** test, and the full race suite.
- [x] 1.6 Add the discovery primitives:
  - `RequestListing` accepts the source root;
  - `RequestProbe` and `RequestSubtree`;
  - `WidenScan` rewrites the payload scope, and `complete()` decides `last_scan_at` from the payload scope (D18);
  - `Deps.Schedule`;
  - basic scope settling, which captures `reconcile.Version` at `openRun` and in `loadTarget` and calls `reconcile.Settle` (`complete`, `partial`, or `error`) in `closeListing` and `commitDecision`.

  Paths: `internal/discovery/frontier.go`, `scan.go`, `listing.go` (settle call only), `decision.go` (settle call only). Verify with tests for:
  - a root listing in a frontier job that leaves `last_scan_at` unchanged;
  - `RequestProbe` doing nothing for a node already probed by the job;
  - `RequestSubtree` marking nothing beneath an atomic directory;
  - a completed listing and probe leaving the scope clean, with `due_at = at + interval`.
- [x] 1.7 Record inactive reasons at the existing deactivation sites (D9):
  - collapse sets `collapsed` on the descendants;
  - kind replacement sets `replaced` and `ancestor_gone`;
  - reactivation clears both columns.

  Paths: `internal/intent/collapse.go`, `deactivateSubtree` in `internal/discovery/listing.go`, the reactivation statement. Verify with collapse and kind-replacement tests that assert the reasons and times.
- [x] 1.8 Add `classify.LoadRiskEvidence` and the new `AggregateEvidence` fields (D12): the union of indicators from the newest complete `applied = 1` measurement of the epoch onward, renumbered `a#` citations, the `Stale` flag, and `From`. Paths: `internal/classify/apply.go` (or a new `risk.go`). Verify with unit tests for:
  - a complete measurement resetting the union;
  - partial measurements adding indicators;
  - an `applied = 0` row ignored;
  - another epoch ignored;
  - evidence that went stale reporting `Stale`.
- [x] 1.9 Make commands self-register (D17). Each command file registers its decoder in `init()`, and `ServeHTTP` looks names up in the registry. Paths: `internal/commands`. Verify with a test that every exported command name is registered exactly once and an unknown name answers 404, and with every existing command test passing unchanged.
- [x] 1.10 Bring every package's race run under 60 s (D21):
  - add the `slow` build tag;
  - make `TestNodeBudgetPausesAndResumes` default to budget 500 over 600 entries, with the 50,000/60,000 version under `slow`;
  - move `TestCollapseFiftyThousandDescendants` under `slow`;
  - run property seeds 1–4 by default and 5–12 under `slow`.

  Paths: `internal/discovery`, `internal/intent`, and `internal/scenario` test files. Verify with `go test -race -count=1 -json ./...` that no package's elapsed time exceeds 60 s, and that `go test -race -tags slow` passes for those three packages.
- [x] 1.11 Write ADR `docs/adr/0002-notification-backend-in-m3a.md` (D22). Add the new `docs/operator.md` section skeletons with owner markers: "Incremental reconciliation" (D), "Missing entries and replacements" (A), and "Index health and history" (E). Verify the files exist and the docs tests pass.
- [x] 1.12 Commit the foundation and run `gofmt -l .`, `go vet ./...`, and `go test -race ./...` on `master`, all clean. Write the worker brief with the Interfaces section, the owned paths of each slice, and the transaction-boundary table.

## 2. Slice A: listing reconciliation (`internal/discovery` listing files)

- [x] 2.1 Implement D5 and D6 for listings:
  - in frontier scope, probe only new, reactivated, or changed child directories, and `Hint(metadata_changed)` changed ones in the batch transaction;
  - never rewrite unchanged children, in either scope.

  Paths: `internal/discovery/listing.go`, `inventory.go`. Verify with tests over the instrumented filesystem:
  - a frontier listing of 40 unchanged child directories probes none of them;
  - a changed child is probed and hinted;
  - a source scan still probes every child directory.
- [x] 2.2 Implement run coalescing and `reconciliation_runs.coverage_state`, `confirmed_at`, and `confirmations` (D6). Paths: `internal/discovery/listing.go`. Verify with the **A26 unchanged listing adds no run** test, plus a test that a listing with one changed entry writes a new run.
- [x] 2.3 Implement missing-entry detection per D7: the seen-set, candidate paging, the rooted recheck in batches outside write transactions, and the compare-and-swap tombstone transaction, including the `ancestor_gone` subtree and `nodes_tombstoned`. Paths: new `internal/discovery/missing.go`, `listing.go`. Verify with the **Verified missing file**, **Missing directory**, and **Entry seen only by the recheck** tests, plus a hook test where a hint between the listing and the tombstone transaction tombstones nothing and settles `stale`.
- [x] 2.4 Write the A28 tests: **A28 incomplete parent listing** (synthfs fails `ReadBatch` after 300 entries) and **A28 offline disk during a missing-entry check** (the recheck `Lstat` fails `unavailable`; the source becomes unavailable and nodes stay active). Paths: `internal/discovery/*_test.go`. Verify that both pass.
- [x] 2.5 Implement identity at a name per D8:
  - kind plus inode continuity, with the device compared only for mount boundaries;
  - device evidence updates that bump no revision;
  - the chain opener expecting the scan root's current device;
  - replacement, including a special file now at the name, as `replaced` with `ancestor_gone` beneath;
  - reactivation by the same identity.

  Paths: `internal/discovery/listing.go`, `inventory.go`, `frontier.go` (opener). Verify with the **Same filesystem, new device number** and **Entry returns after it was missing** tests.
- [x] 2.6 Write the A31 tests over synthfs: **A31 replacement at the same name**, **A31 hardlinks stay separate**, **A31 inode reuse under another name**, and **A31 external rename**. Add an `e2e`-tagged real-disk test that renames and replaces directories in a temp dir and checks the same outcomes. Paths: `internal/discovery/*_test.go`. Verify with `go test -race ./internal/discovery/` and `go test -tags e2e -run A31 ./internal/discovery/`.
- [x] 2.7 Refine listing settles: a `boundary_changed`, cancelled, or paused listing leaves the scope unsettled, and a compare-and-swap failure settles `stale`. Replace `TestA5EntryMissingOnRescan` and the "never deactivated" statement in `doc.go`. Paths: `internal/discovery`. Verify with settle-outcome tests and a race run of `internal/discovery` under 60 s.
- [x] 2.8 Write the `docs/operator.md` "Missing entries and replacements" section and update "Rescans", covering the verified-absence conditions, tombstone and history semantics, identity at a name, hardlinks, and renames. Verify each documented outcome against the 2.3–2.6 tests.

## 3. Slice B: probe reconciliation (`internal/discovery` decision files, set-classification)

- [x] 3.1 Write `descriptors.escalated`, and implement the unchanged-probe test with `descriptors.confirmed_at` (D6). Paths: `internal/discovery/decision.go`, `probe.go`. Verify with a test that 10 unchanged atomic units probed again add no descriptor or classification row and do not escalate, and that a rule-set version change still re-evaluates.
- [x] 3.2 Implement G3 (D13):
  - `commitDecision` flags `evidence_changed` only from non-escalated descriptors;
  - `set-classification` records the digest of the latest non-escalated descriptor.

  Paths: `internal/discovery/decision.go`, `internal/intent/classification.go`. Verify with **Escalated probe does not flag**, and with the existing **Directory contents changed after override** test.
- [x] 3.3 Implement D10. `loadTarget` captures `Observed` and the dirty version, and `commitDecision` passes them to `classify.Apply`. A stale result keeps the descriptor, stores the suggestion `stale`, and leaves the probe pending for an immediate re-probe in the same job. Paths: `internal/discovery/frontier.go` (`loadTarget`), `decision.go`. Verify with the **A30 refresh hint during a probe** test, which parks the probe on a gated filesystem call and commits `reconcile.Hint` meanwhile.
- [x] 3.4 Feed rule evaluation in `commitDecision` from `classify.LoadRiskEvidence` instead of the current-only aggregate (D12). Paths: `internal/discovery/decision.go`. Verify with a test that a unit whose measurement is older than the stale-after period keeps its walk-derived trait across a probe.
- [x] 3.5 Settle probe scopes with `complete`, `stale`, or `error`. Paths: `internal/discovery/decision.go`. Verify with settle-outcome tests and a race run of `internal/discovery` under 60 s.
- [x] 3.6 Update the `docs/operator.md` "Policy decisions and their reasons" and "Category overrides" sections: unchanged probes add no history, overrides compare same-budget probes, and late probe results are stale. Verify the statements against the 3.1–3.3 tests.

## 4. Slice C: inspection (`internal/inspection`, `classify` explanations)

- [x] 4.1 Implement D11:
  - capture the unit's dirty version at walk start and store it in `aggregates.dirty_version`;
  - on a revision or dirty-version mismatch at commit, store `applied = 0` and call `reconcile.Request`;
  - on an inactive or non-atomic unit, store `applied = 0`.

  Paths: `internal/inspection/aggregate.go`, `inspection.go`. Verify with the **A30 boundary change during a walk** test (a refresh hint while the walk is gated) and the **A30 ancestor replaced during a walk** test (the gate commits the D8 deactivation of the ancestor's subtree).
- [x] 4.2 Write the **Boundary change makes a measurement stale** test: bump the unit's observation revision as a listing would, and assert the measurement is stale before its stale-after period. Paths: `internal/inspection/*_test.go`. Verify that it passes, along with the existing freshness tests.
- [x] 4.3 Make `reclassify` use `classify.LoadRiskEvidence` (D12). Paths: `internal/inspection/aggregate.go`. Verify with the **Partial walk does not remove risk** and **Risk outlives the measurement's freshness** tests, plus a test that a newer complete walk without the indicator removes the trait. Confirm the existing A4 walk test passes.
- [x] 4.4 Make `Explain` name a stale source measurement and its time, keeping "only names were checked". Paths: `internal/classify/explain.go`. Verify with explanation tests, and with the forbidden-claims corpus test passing.
- [x] 4.5 Update the `docs/operator.md` "Aggregate walks" and "Preservation risk" sections: staleness after a boundary change, risk persisting until a complete walk, and history-only late results. Verify against the 4.1–4.3 tests.

## 5. Slice D: scheduler and `refresh-scope` (`internal/watch`, dispatch, command)

- [x] 5.1 Add `discovery.DispatchDue` in a new file, `internal/discovery/dispatch.go` (D4 step 5). It makes set-based listing and probe marks and records `dispatched_job_id`. Verify with tests that:
  - due expanded directories and the root are listing-pending;
  - due atomic and undecided directories are probe-pending;
  - inactive, boundary, and already-dispatched scopes are untouched.
- [x] 5.2 Implement `watch.Scheduler` per D4 and D15: holds, seeding, finding or enqueueing the frontier job, dispatch, the source failure backoff, and gap closing. Paths: `internal/watch`. Verify with fake-clock tests over a real runner and synthfs for:
  - **Budget-paused scan is not resumed**;
  - **Unavailable source does not block others**;
  - **A busy directory cannot starve the source**;
  - **Startup does not scan a new source**;
  - a source-level failure delaying the next dispatch.
- [x] 5.3 Add the `refresh-scope` command per D16 and the Interfaces endpoint contract. Paths: new `internal/commands/refresh_scope.go`. Verify with:
  - **Refresh an atomic unit**, **Subtree refresh respects atomic boundaries**, **Refresh of a file**, and **Refresh joins a running scan**;
  - a 400 for a bad `scope`;
  - idempotent replay.
- [x] 5.4 Write the **A27 coverage gap after reconfirmation** test. `Confirm` a changed identity, then run ticks until every scope settles. Assert the gap opens with `epoch_changed` and closes, and that the instrumented filesystem shows no access below any atomic unit's immediate entries. Paths: `internal/watch/*_test.go`. Verify that it passes, with `internal/watch` under 60 s.
- [x] 5.5 Write the `docs/operator.md` "Incremental reconciliation" section: scopes, the schedule and its spread, hints, dispatch and holds, backoff, `refresh-scope`, coverage gaps, and polling only. Verify every documented hold reason and outcome against the 5.2–5.4 tests.

## 6. Slice E: read side and UI (`internal/inventory`, `internal/web/explorer`, `web/`)

- [x] 6.1 Add `ScopeStatus`, the inactive fields, and the D6 displayed last observation to `inventory.Node` and the node JSON. Paths: `internal/inventory`, `internal/web/explorer/api.go`. Verify with the **Node carries freshness fields** and **Unchanged entry shows its latest confirmation** tests over seeded runs and scopes.
- [x] 6.2 Add `Reader.History`, `GET /api/nodes/{id}/children?state=history` (with 400 for any other `state`), and the `/nodes/{id}/history` page. Paths: `internal/inventory`, `internal/web/explorer`, `web/templates`. Verify with the **A28 history remains available** test (a seeded tombstone missing from children and totals, present in history), plus cursor paging over duplicate names.
- [x] 6.3 Add `Reader.IndexHealth`, `GET /api/sources/{id}/index-health` matching the Interfaces JSON, the `/sources/{id}/health` page, and the overview's per-source summary. Paths: `internal/inventory`, `internal/web/explorer`, `web/templates`. Verify with:
  - **A27 polling-only status is visible**;
  - **Failing scope is listed**;
  - a JSON shape test;
  - a test per `held` reason;
  - 404 for an unknown source.
- [x] 6.4 Show the scope in the inspector, with "boundary-only monitoring; nested changes may be unobserved" for atomic directories, plus the "Refresh boundary" and "Reconcile subtree" controls posting `refresh-scope` from `app.js`. Paths: `web/templates`, `web/static/app.js`. Verify with the **A29 boundary-only statement** test, and with template tests that the controls appear only in valid node states.
- [x] 6.5 Make the inventory reader use `protection.Blocking` and delete `blockingPin` (G5). Paths: `internal/inventory/selection.go`. Verify that the selection-summary and protected-exclusion tests pass unchanged.
- [x] 6.6 Write the `docs/operator.md` "Index health and history" section, and update "Pages", "Reading sizes and coverage honestly", and the read-API table. Verify that every field and page named in the docs exists in the 6.1–6.4 tests.

## 7. Integration (coordinator)

- [x] 7.1 Wire `serve`:
  - construct `watch.Scheduler` from `[reconciliation]`;
  - run one `Tick` after `runner.Start` and then every `watch.DefaultTick`, injectable through `serveDeps`;
  - pass `Schedule` to the discovery and inventory dependencies.

  Verify with `cmd/curator` tests in which a fake tick drives a scheduled pass.
- [x] 7.2 Write the scenario test **A26 new, changed, and deleted direct entries with notifications disabled**: the full stack, scheduler ticks only, no commands. Paths: `internal/scenario`. Verify that it passes.
- [x] 7.3 Write the scenario test **A26 unchanged items are not reclassified**. It combines listing and probe passes, and checks instrumented filesystem counts and row counts. Verify that it passes.
- [x] 7.4 Write the scenario test **A27 process downtime**: kill, advance the clock two hours, restart, read the index-health lag, tick, and observe convergence, with each atomic unit probed only within its budget. Verify that it passes.
- [x] 7.5 Write the scenario test **A29 deep internal edit without boundary evidence**: walk, a deep change, a boundary refresh that records no new evidence, the measurement still current, then stale after the period, with no access below immediate entries. Verify that it passes.
- [x] 7.6 Extend `TestNoHiddenCatalogProperty` with scheduled passes, refreshes, on-disk deletions and replacements, and tombstones. This verifies **Invariant after a mixed sequence**. The default seeds must stay under the 60 s budget, with the extra seeds under `slow`. Verify with `go test -race ./internal/scenario/` and `-tags slow`.
- [x] 7.7 Write the `docs/operator.md` section "Upgrading from M2 (directory policy) to M2a (incremental indexing)": migration `0003`, scope seeding, `unrecorded` history, the heuristic backfills, and rollback. Remove the owner markers. Link ADR 0002. Verify that the docs tests pass and no marker remains.
- [x] 7.8 Run the final verification:
  - `gofmt -l .` empty;
  - `go vet ./...` and `go vet -tags e2e,slow ./...`;
  - `go test -race ./...` with every package under 60 s;
  - `go test -race -tags slow ./...`;
  - the `e2e` mount tests in the privileged `golang:1.27.1` container;
  - a headless-browser pass over the index-health page, history, freshness, and the refresh controls under the production CSP.

  Verify that all are clean.
- [x] 7.9 Smoke-check the built binary:
  - `check-config` on the example;
  - `serve` with `expanded_interval = "1m"`;
  - `start-scan`, then add, change, delete, and replace entries on disk, and wait for one scheduled pass;
  - assert the new node, the revision bump, the `missing` and `replaced` tombstones in history, a clean index health with `polling_only`, `refresh-scope` on an atomic unit, and `curator backup` passing its integrity check.

  Verify that the throwaway script reports success, then delete it.
