# Tasks

## 1. Foundation (one slice, merged first)

- [x] 1.1 Migration `0006_organize.sql`: `sources.write_enabled`, `actions`, and `action_items`, exactly as in design Interfaces. Owns `migrations/` and `internal/store/*schema_test.go`. Verify with a store schema test of the version, columns, checks, and cascades: `go test -race ./internal/store`.
- [x] 1.2 `[sources] allow_writes` (boolean, default `true`) in `internal/config`, with its validation, a row in `deploy/examples/precious.toml`, a row in the configuration table of `docs/operator.md`, and the `check-config` output. Owns `internal/config`, `deploy/examples`, and `cmd/precious/checkconfig*`. Verify: `go test -race ./internal/config ./cmd/precious`.
- [x] 1.3 The domain codes of design Interfaces in `internal/domain`, and their 409 statuses in `internal/web/apierr`. Owns those two packages. Verify with a test of the status map: `go test -race ./internal/domain ./internal/web/apierr`.
- [x] 1.4 `no_replace_rename` in `fsaccess.Capabilities`, from the Linux type table (design D2), and false in the portable backend. Update every test's exact capability JSON and key lists, and the capabilities table in `docs/operator.md`. Owns `internal/fsaccess` (capabilities) and the tests that pin capability JSON. Verify: a table test for the scenario "Which filesystems have a no-replace rename"; `go test -race ./...`.
- [x] 1.5 `fsaccess.Writer` (design D3):
  - **Linux.**
    - `renameat2` with `RENAME_NOREPLACE`.
    - `mkdirat` with 0700, then `fchmodat` to the parent's `mode & 02777`.
    - `unlinkat` with `AT_REMOVEDIR`.
    - `fsync` of a folder.
    - All on the raw descriptors of `Dir` handles, with errors mapped to `ErrExist`, `ErrNoReplaceUnsupported`, `ErrCrossDevice`, `ErrNotEmpty`, `ErrReadOnly`, and `ErrPermission`.
  - **Portable:** `ErrNoReplaceUnsupported`.
  - **synthfs:** the same methods and errors.
  - **instrument:** `OpRename`, `OpMkdir`, `OpRmdir`, and `OpSync`, with `InjectError` and `SetBeforeCall`.
  - **Guard test:** it fails when any non-test package outside `internal/executor` and `internal/fsaccess` calls a `Writer` method.
  - **Package doc:** updated.

  Owns `internal/fsaccess/...`. Verify:
  - unit tests on synthfs;
  - `e2e && linux` tests on a temporary directory: renaming onto an existing name fails with `ErrExist` and leaves both files; renaming across a mount fails with `ErrCrossDevice`; `Rmdir` of a non-empty folder fails with `ErrNotEmpty`; a folder made under umask 0077 inside a 0755 parent ends 0755.
- [x] 1.6 `sources.Source.WriteEnabled`, read from `write_enabled`, plus `sources.WritesUnavailable` and `sources.CheckWrites` (design Interfaces). Owns `internal/sources/writes.go` and its test. Verify with unit tests of each reason and its order: `go test -race ./internal/sources`.

## 2. Index, executor, sources, and interface (parallel slices after group 1)

- [ ] 2.1 Index slice:
  - in `internal/index`: `MoveEntry` (design D6 steps 1–5, including the `dir_stats` JSON path rewrite and `entry_names`), `MissingIntentAt`, `InsertFolder` and `RemoveFolder` (with their `entry_names` rows), and `Refolder.Refold`, with one fold function shared with the scan's `finish`;
  - indicators ordered by path in both the scan and the refold;
  - `Handler.DeferWhile` and `DeferOrganizing` (design D10);
  - in `internal/decisions`: `Reinherit`;
  - the `EXPLAIN QUERY PLAN` guards, extended to the new statements.

  Owns `internal/index`, `internal/decisions/reinherit*.go`, and the `docs/operator.md` paragraph on how the index follows a move. Verify: `go test -race ./internal/index ./internal/decisions`.
- [ ] 2.2 Test of R3.5 at the index level:
  - a property test runs random renames and moves of files and folders on the corpus through `MoveEntry`, `Reinherit`, and `Refold`, then a full rescan. The rescan adds no entry, marks none missing, and writes no aggregate, `dir_stats`, classification, or `entry_names` change;
  - moved entries keep their IDs, own decisions, tags, overrides, `file_content`, and `archives` rows, and their effective decisions come from the new parent;
  - a missing row with owner intent at the destination is reported by `MissingIntentAt`, and one without intent is deleted with its name rows.

  Owns `internal/index/move_test.go`. Verify: `go test -race ./internal/index`, within 60 s.
- [ ] 2.3 A slow-tagged test that moves a folder holding 100,000 entries through `MoveEntry` and `Refold` within 10 s on the development machine. Owns `internal/index/move_slow_test.go`. Verify: `go test -tags slow -run TestMoveAtScale ./internal/index`.
- [ ] 2.4 Path guards for content (design D18): the hashing `apply` guard and the archive listing's `entryLive` also compare the path that was read with the entry's current path. Owns the guards in `internal/content`. Verify with a test of the scenario "Hashing a moved folder's files": a batch loaded before its folder's path changes commits no `changed` row; `go test -race ./internal/content`.
- [ ] 2.5 Executor slice. Write `internal/executor` (design D3–D5, D7, D9 Cancelling, D10, D11's `reversed_by`, D14's run-time re-check, Interfaces):
  - one `organize` job per action, which sweeps and waits its turn;
  - the intent, step, sync, confirm, and outcome sequence;
  - preflight through rooted handles, comparing identity under capabilities and devices from the live handles;
  - the re-checks for `contains_mount`, `name_taken_by_missing`, `would_lose_keep`, and `already_undone`;
  - the error table;
  - reconciliation inside the job, after the scan check;
  - `Startup`, which only sweeps and enqueues;
  - an index failure leading to `manual_recovery`;
  - `no_safe_rename`, which turns writes off with an audit event;
  - `CancelAction`, deferring while a scan runs, `OrganizeActive`, and `Enqueue`.

  Index updates go through `executor.Index`, with a fake in the tests. Owns `internal/executor` and the `docs/operator.md` section on how Precious changes a disk (guarantees, recovery). Verify: `go test -race ./internal/executor`.
- [ ] 2.6 Test of R3.1 in the executor, driving actions by inserting `queued` rows:
  - with synthfs and an instrument hook, a file created at the destination of the second item, after intent and before its rename, ends that item `conflict`, with both files unchanged and the other items moved;
  - a rename onto an existing name never replaces it;
  - the same first case in an `e2e && linux` test on a temporary directory, with the real `renameat2`.

  Owns `internal/executor/r3_1*_test.go`. Verify: `go test -race ./internal/executor` and `scripts/e2e-docker.sh -run R3_1`.
- [ ] 2.7 Test of R3.2 in the executor, driving actions by inserting `queued` rows:
  - with `Hooks`, a crash after the intent and before the step leads, on a new executor over the same database, to exactly one rename recorded by instrument;
  - a crash after the step and before the outcome leads to no second rename, with the item done and the index updated;
  - instrument records the folder syncs before the outcome;
  - an ambiguous case (both names present, or neither) leads to `manual_recovery` with the findings;
  - a failing index update leads to `manual_recovery`;
  - a scan that is running delays reconciliation.

  Owns `internal/executor/r3_2*_test.go`. Verify: `go test -race ./internal/executor`.
- [ ] 2.8 Test of R3.7 in the executor:
  - writes turned off after an action is queued and before its first item lead to no rename, mkdir, or rmdir recorded by instrument, and the item ends `not_permitted`;
  - the same holds for `allow_writes = false`;
  - a cancelled queued action is swept to `stopped` and never runs.

  Owns `internal/executor/r3_7_test.go`. Verify: `go test -race ./internal/executor`.
- [ ] 2.9 Sources slice:
  - the `set-source-writes` command, with its audit event;
  - `writes` in `GET /api/sources`;
  - `no_replace_rename` in the source JSON;
  - `remove-source`'s refusals (design D15);
  - `kind=directory` in `GET /api/entries/{id}/children`.

  Owns `internal/sources` (commands, http, manage), `internal/web/api` (children), and the `docs/operator.md` Sources section and API rows. Verify: `go test -race ./internal/sources ./internal/web/api`.
- [ ] 2.10 Test of R3.7 at the source level:
  - `allow_writes = false` gives `forbidden_by_config`, and enabling fails with `409 writes_unavailable`;
  - a read-only mount gives `read_only`;
  - turning writes on and off writes one audit event each;
  - the scenario "A move in progress blocks removal", with action rows inserted directly;
  - the scenario "Folders only".

  Owns `internal/sources/r3_7_test.go` and `internal/web/api/children_kind_test.go`. Verify: `go test -race ./internal/sources ./internal/web/api`.
- [x] 2.11 Interface slice, all built against design Interfaces:
  - Sources: the Changes by Precious row, with its confirmation dialog;
  - the detail panel's Organize section: Rename, Move to…, New folder, Rescue kept items;
  - the destination chooser (`kind=directory`);
  - the preview dialog, paged, with refused reasons and the kept-lost warning;
  - Search bulk Move to…;
  - Compare's merge action;
  - the History page and its navigation item, with undo, cancel, and recovery;
  - `refreshAfterMove`, and `organize` job events;
  - the new error codes and item reasons in the catalog.

  Owns `web/ui/src`, and the interface paragraphs of `docs/operator.md`. Verify: `cd web/ui && npm run -s lint && npx vitest run && npm run -s build`.
- [x] 2.12 Interface tests (Vitest):
  - R3.7: cancelling the confirmation sends no command, and an unavailable source shows its reason with no control;
  - a one-item rename runs at once and offers Undo;
  - R3.4: a bulk move shows every item, conflict, and refused item with its reason, and sends `run-action` only after confirming;
  - a single move that loses a keep shows the warning;
  - History lists actions, undoes one, cancels a queued one, and resolves a recovery;
  - Compare offers the merge only on the two only-on-one-side groups;
  - the catalog holds no banned term.

  Owns `web/ui/src/**/*.test.tsx`. Verify: `npx vitest run`.

## 3. Organizing (after group 2)

- [ ] 3.1 Organize slice. Write `internal/organize`:
  - the `plan-move`, `plan-rename`, `plan-create-folder`, `plan-rescue`, `plan-merge`, and `plan-undo` commands (design D7, D8, D9, D11–D14, D17);
  - the `run-action`, `cancel-action`, and `resolve-recovery` commands;
  - the history read API;
  - the `executor.Index` adapter over index, decisions, `Refolder`, and `relations.RequestRefresh`;
  - `relations.Wrappers`;
  - the wiring in `cmd/precious/serve.go`: executor registration, `Startup`, and the scan's `DeferWhile`.

  Owns `internal/organize`, `internal/relations/wrappers.go`, `cmd/precious/serve.go` (wiring), and the `docs/operator.md` Organizing section with its API and error rows. Verify: `go test -race ./internal/organize ./internal/relations ./cmd/precious`.
- [ ] 3.2 Test of R3.4: a selection is planned into a folder, and a new matching file is indexed before the run. The plan lists every selected entry, conflict, and refused item, and the run moves exactly the planned runnable items and leaves the new file in place. Owns `internal/organize/r3_4_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.3 Test of R3.3:
  - an undone move of `Fotos/2004` returns it with the same IDs, and its items read `reversed_by`;
  - undoing a rename whose old name was taken since plans a conflict, and with `destination_id` it moves the file there under its old name and leaves the newcomer untouched;
  - an undo that stopped early leaves its unreversed items undoable;
  - an expired undo plan leaves the action undoable.

  Owns `internal/organize/r3_3_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.4 Test of R3.6:
  - on the corpus, after hashing, `plan-merge` from `Fotos - Copia` into `Fotos` runs, followed by hashing and relations. `Fotos/2006/Praia/DSC_editada.JPG` exists, Compare shows an empty `only_right` group, and no file in `Fotos - Copia` lacks a copy in `Fotos`;
  - a merge into a side with a wrapper folder lands under the wrapper.

  Owns `internal/organize/r3_6_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.5 Test of R3.5 end to end, through commands and the real executor on synthfs: a folder with an own keep, a tag, an override, and hashed files is moved, then rescanned. IDs, decisions, tags, the override, and digests are kept, nothing is new or missing, and totals are unchanged. Owns `internal/organize/r3_5_test.go`. Verify: `go test -race ./internal/organize`.
- [ ] 3.6 Tests of the plan rules:
  - every refusal reason, `contains_mount` and `name_taken_by_missing` included;
  - collisions on a case-insensitive source;
  - `would_lose_keep` in bulk, and `kept_lost` for a single move;
  - the scenario "Rescue before discarding";
  - folding of nested targets;
  - the 10,000-item cap;
  - expiry;
  - `409 name_taken` for a single rename, and `400` for a case-only rename on a case-insensitive source;
  - `409 writes_disabled` and `409 recovery_needed` when planning;
  - the scenario "Cancelling a queued move".

  Owns `internal/organize/plan_test.go`. Verify: `go test -race ./internal/organize`.

## 4. Integration

- [ ] 4.1 Playwright tests after the last existing one:
  - turning writes on through the dialog (R3.7);
  - renaming and undoing in the detail panel;
  - a bulk move from Search with its preview (R3.4);
  - a rescue;
  - Compare's merge of `Fotos - Copia` into `Fotos` (R3.6);
  - History.

  Verify: `npx playwright test` passes headless.
- [ ] 4.2 Full verification:
  - gofmt, `go vet ./...`, and `go vet -tags e2e,slow ./...`;
  - `go test -race ./...`;
  - `make test-slow`, with `TestMoveAtScale`;
  - `make cross`;
  - UI lint, Vitest, and build;
  - Playwright;
  - `scripts/e2e-docker.sh`;
  - `openspec validate --strict`.
- [ ] 4.3 A smoke check of the built binary, with a throwaway script on a copy of an r2d database:
  - the migration applies;
  - writes turn on for a corpus source;
  - a rename, a move, an undo, and a merge run;
  - a rescan finds no new or missing entry.

  Then delete the script.
- [ ] 4.4 Deploy on the reference server. The owner turns writes on for the corpus source only, tries a rename, a move with undo, and a merge, and signs off, recorded in the design addendum.
