# Tasks

## 1. Foundation (one slice, merged first)

- [ ] 1.1 Write migration `0006_organize.sql` (`sources.write_enabled`, `actions`, `action_items`, exactly as in design Interfaces), with a store schema test of the version, columns, checks, and cascades. Owns `migrations/`, `internal/store/*schema_test.go`. Verify: `go test -race ./internal/store`.
- [ ] 1.2 Add `[sources] allow_writes` (boolean, default `true`) to `internal/config`, with its validation. Add it to `deploy/examples/precious.toml` and to the configuration table in `docs/operator.md`, and check the `check-config` output. Owns `internal/config`, `deploy/examples`, `cmd/precious/checkconfig*`. Verify: `go test -race ./internal/config ./cmd/precious`.
- [ ] 1.3 Add the domain codes of design Interfaces to `internal/domain` and their 409 statuses to `internal/web/apierr`, with a test of the status map. Owns those two packages. Verify: `go test -race ./internal/domain ./internal/web/apierr`.
- [ ] 1.4 Add `no_replace_rename` to `fsaccess.Capabilities`. Set it from the Linux type table (design D2) and to false in the portable backend. Update every test's exact capability JSON and key lists, and the capabilities table in `docs/operator.md`. Owns `internal/fsaccess` (capabilities) and the tests that pin capability JSON. Verify: a table test for the scenario "Which filesystems have a no-replace rename"; `go test -race ./...`.
- [ ] 1.5 Add `fsaccess.Writer` (design D3):
  - Linux: `renameat2` with `RENAME_NOREPLACE`, `mkdirat`, and `unlinkat` with `AT_REMOVEDIR`, on the raw descriptors of two `Dir` handles, mapping errors to `ErrExist`, `ErrNoReplaceUnsupported`, `ErrCrossDevice`, `ErrNotEmpty`, `ErrReadOnly`, and `ErrPermission`;
  - the portable backend: `ErrNoReplaceUnsupported`;
  - synthfs: the same methods and errors;
  - instrument: `OpRename`, `OpMkdir`, and `OpRmdir`, with `InjectError` and `SetBeforeCall`;
  - a guard test that fails when any non-test package outside `internal/executor` and `internal/fsaccess` calls a `Writer` method.

  Update the package doc. Owns `internal/fsaccess/...`. Verify:
  - unit tests on synthfs;
  - `e2e && linux` tests on a temporary directory: renaming onto an existing name fails with `ErrExist` and leaves both files, renaming across a mount fails with `ErrCrossDevice`, and `Rmdir` of a non-empty folder fails with `ErrNotEmpty`.
- [ ] 1.6 Read `write_enabled` into `sources.Source.WriteEnabled`, and add `sources.WritesUnavailable` and `sources.CheckWrites` (design Interfaces), with unit tests of each reason and its order. Owns `internal/sources/writes.go` and its test. Verify: `go test -race ./internal/sources`.

## 2. Index, executor, sources, and interface (parallel slices after group 1)

- [ ] 2.1 Index slice. In `internal/index`, implement `MoveEntry`, `InsertFolder`, `RemoveFolder`, and `Refolder.Refold` (design D6), with one fold function shared with the scan's `finish`. Add `Handler.DeferWhile` and `DeferOrganizing`, so a scan defers at start while organizing runs (design D10). In `internal/decisions`, implement `Reinherit`. Extend the `EXPLAIN QUERY PLAN` guards to the new statements. Owns `internal/index`, `internal/decisions/reinherit*.go`, and `docs/operator.md` (the paragraph on how the index follows a move). Verify: `go test -race ./internal/index ./internal/decisions`.
- [ ] 2.2 Test of R3.5 at the index level:
  - a property test runs random renames and moves of files and folders on the corpus through `MoveEntry`, `Reinherit`, and `Refold`, then a full rescan, which adds no entry, marks none missing, and writes no aggregate, `dir_stats`, or classification change;
  - moved entries keep their IDs, own decisions, tags, overrides, `file_content`, and `archives` rows, and their effective decisions come from the new parent.

  Owns `internal/index/move_test.go`. Verify: `go test -race ./internal/index`, within 60 s.
- [ ] 2.3 Add a slow-tagged test that moves a folder holding 100,000 entries through `MoveEntry` plus `Refold` within 10 s on the development machine. Owns `internal/index/move_slow_test.go`. Verify: `go test -tags slow -run TestMoveAtScale ./internal/index`.
- [ ] 2.4 Executor slice. Write `internal/executor` (design D3–D5, D10, D15's checks, Interfaces):
  - the `organize` job, which serves the queued actions of a source in order;
  - the intent and outcome transactions;
  - preflight through rooted handles, with identity checks;
  - the error table;
  - reconciliation at attempt start and in `Startup`;
  - `manual_recovery`;
  - `no_safe_rename`, which turns writes off with an audit event;
  - cancellation, and deferring while a scan runs;
  - `OrganizeActive` and `Enqueue`.

  Index updates go through the `executor.Index` interface, with a fake in the tests. Owns `internal/executor`, and the `docs/operator.md` section on how Precious changes a disk (guarantees, recovery). Verify: `go test -race ./internal/executor`.
- [ ] 2.5 Test of R3.1 in the executor:
  - with synthfs and an instrument hook, a file created at the destination of the second item, after intent and before its rename, ends that item `conflict` with both files unchanged, while the other items are moved;
  - a rename onto an existing name never replaces it.

  Repeat the first case in an `e2e && linux` test on a temporary directory with the real `renameat2`. Owns `internal/executor/r3_1*_test.go`. Verify: `go test -race ./internal/executor` and `scripts/e2e-docker.sh -run R3_1`.
- [ ] 2.6 Test of R3.2 in the executor:
  - with `Hooks`, a crash after the intent and before the step leads, on a new executor over the same database, to exactly one rename recorded by instrument;
  - a crash after the step and before the outcome leads to no second rename, with the item done and the index updated;
  - an ambiguous case (both names present, or neither) leads to `manual_recovery` with the findings, and planning answers `recovery_needed`.

  Owns `internal/executor/r3_2*_test.go`. Verify: `go test -race ./internal/executor`.
- [ ] 2.7 Test of R3.7 in the executor: writes turned off after `run-action` and before the first item lead to no rename or mkdir recorded by instrument, and the item ends `not_permitted`. The same holds for `allow_writes = false`. Owns `internal/executor/r3_7_test.go`. Verify: `go test -race ./internal/executor`.
- [ ] 2.8 Sources slice:
  - the `set-source-writes` command, with its audit event;
  - `writes` in `GET /api/sources`;
  - `no_replace_rename` in the source JSON;
  - `remove-source`'s refusals (design D15);
  - `kind=directory` in `GET /api/entries/{id}/children`.

  Owns `internal/sources` (commands, http, manage) and `internal/web/api` (children), plus the `docs/operator.md` Sources section and API rows. Verify: `go test -race ./internal/sources ./internal/web/api`.
- [ ] 2.9 Test of R3.7 at the source level:
  - `allow_writes = false` gives `forbidden_by_config`, and enabling fails with `409 writes_unavailable`;
  - a read-only mount gives `read_only`;
  - turning writes on and off writes one audit event each;
  - the scenario "A move in progress blocks removal";
  - the scenario "Folders only".

  Owns `internal/sources/r3_7_test.go` and `internal/web/api/children_kind_test.go`. Verify: `go test -race ./internal/sources ./internal/web/api`.
- [ ] 2.10 Interface slice:
  - Sources: the Changes by Precious row, with its confirmation dialog;
  - the detail panel's Organize section: Rename, Move to…, New folder, Rescue kept items;
  - the destination chooser (`kind=directory`);
  - the preview dialog, paged;
  - Search bulk Move to…;
  - Compare's merge action;
  - the History page and its navigation item, with undo and recovery;
  - `refreshAfterMove`, and `organize` job events;
  - the new error codes in the catalog.

  Everything is built against design Interfaces. Owns `web/ui/src`, and the interface paragraphs of `docs/operator.md`. Verify: `cd web/ui && npm run -s lint && npx vitest run && npm run -s build`.
- [ ] 2.11 Interface tests (Vitest):
  - R3.7: cancelling the confirmation sends no command, and an unavailable source shows its reason with no control;
  - a one-item rename runs at once and offers Undo;
  - R3.4: a bulk move shows every item, conflict, refused item, and the kept-lost warning, and sends `run-action` only after confirming;
  - History lists actions, undoes one, and resolves a recovery;
  - Compare offers the merge only on the two only-on-one-side groups;
  - the catalog holds no banned term.

  Owns `web/ui/src/**/*.test.tsx`. Verify: `npx vitest run`.

## 3. Organizing (after group 2)

- [ ] 3.1 Organize slice. Write `internal/organize`:
  - `plan-move`, `plan-rename`, `plan-create-folder`, `plan-rescue`, `plan-merge`, and `plan-undo` (design D8, D9, D11–D14);
  - `run-action` and `resolve-recovery`;
  - the history read API;
  - the `executor.Index` adapter over index, decisions, `Refolder`, and `relations.RequestRefresh`;
  - the wiring in `cmd/precious/serve.go` (executor registration, `Startup`, and the scan's `DeferWhile`).

  Owns `internal/organize`, `cmd/precious/serve.go` (wiring), and the `docs/operator.md` Organizing section and its API and error rows. Verify: `go test -race ./internal/organize ./cmd/precious`.
- [ ] 3.2 Test of R3.4: a selection is planned into a folder, and a new matching file is indexed before the run. The plan lists every selected entry, conflict, and refused item, and the run moves exactly the planned runnable items, leaving the new file in place. Owns `internal/organize/r3_4_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.3 Test of R3.3:
  - an undone move of `Fotos/2004` returns it with the same IDs, and the original reads `undone_by`;
  - undoing a rename whose old name was taken since plans a conflict, and with `destination_id` it moves the file there under its old name and leaves the newcomer untouched;
  - an expired undo plan leaves the action undoable.

  Owns `internal/organize/r3_3_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.4 Test of R3.6: on the corpus, after hashing, `plan-merge` from `Fotos - Copia` into `Fotos` is run, followed by hashing and relations. `Fotos/2006/Praia/DSC_editada.JPG` exists, Compare shows an empty `only_right` group, and no file in `Fotos - Copia` lacks a copy in `Fotos`. Owns `internal/organize/r3_6_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.5 Test of R3.5 end to end, through commands and the real executor on synthfs: a folder with an own keep, a tag, an override, and hashed files is moved, then rescanned. IDs, decisions, tags, the override, and digests are kept, nothing is new or missing, and totals are unchanged. Owns `internal/organize/r3_5_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.6 Tests of the plan rules:
  - every refusal reason;
  - collisions on a case-insensitive source;
  - `decision_after` and `kept_lost`;
  - the scenario "Rescue before discarding";
  - folding of nested targets;
  - the 10,000-item cap;
  - expiry;
  - `409 name_taken` for a single rename;
  - `409 writes_disabled` and `409 recovery_needed`.

  Owns `internal/organize/plan_test.go`. Verify: `go test -race ./internal/organize`.

## 4. Integration

- [ ] 4.1 Add Playwright tests after the last existing one:
  - turning writes on through the dialog (R3.7);
  - renaming and undoing in the detail panel;
  - a bulk move from Search with its preview (R3.4);
  - a rescue;
  - Compare's merge of `Fotos - Copia` into `Fotos` (R3.6);
  - History.

  Verify: `npx playwright test` passes headless.
- [ ] 4.2 Run the full verification:
  - gofmt, `go vet ./...`, and `go vet -tags e2e,slow ./...`;
  - `go test -race ./...`;
  - `make test-slow` (with `TestMoveAtScale`);
  - `make cross`;
  - UI lint, Vitest, and build;
  - Playwright;
  - `scripts/e2e-docker.sh`;
  - `openspec validate --strict`.
- [ ] 4.3 Smoke-check the built binary with a throwaway script on a copy of an r2d database:
  - the migration applies;
  - writes turn on for a corpus source;
  - a rename, a move, an undo, and a merge run;
  - a rescan finds no new or missing entry.

  Then delete the script.
- [ ] 4.4 Deploy on the reference server and have the owner turn writes on for the corpus source only. He tries a rename, a move with undo, and a merge, and signs off, recorded in the design addendum.
