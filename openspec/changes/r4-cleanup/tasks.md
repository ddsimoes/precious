# Tasks

## 1. Foundation (one slice, merged first)

- [x] 1.1 Migration `0007_cleanup.sql`, exactly as in design Interfaces: it rebuilds `actions` and `action_items` (items dropped first), adds their new columns, `sources.quarantine_entry_id`, `purge_checks`, `purge_check_items`, and `purge_check_files`. Owns `migrations/` and `internal/store/*schema_test.go`. Verify with a store test:
  - a version-6 database with actions and items migrates with every row kept;
  - the new checks reject bad values;
  - cascades hold, and the foreign keys into `entries` are indexed.

  Run `go test -race ./internal/store`.
- [x] 1.2 The 409 codes of design Interfaces in `internal/domain` and `internal/web/apierr`, with a test of the status map. Verify: `go test -race ./internal/domain ./internal/web/apierr`.
- [x] 1.3 `fsaccess.Writer.CreateExclusive` and `Unlink`, and `ErrIsDir` (design D12):
  - Linux, portable, synthfs, and instrument (`OpCreate`, `OpUnlink`);
  - the package doc;
  - the guard test.

  Owns `internal/fsaccess/...`. Verify with unit tests on synthfs and `e2e && linux` tests on a tmpfs:
  - creating onto an existing name gives `ErrExist` and leaves it unchanged;
  - a written file is complete, synced, and has the parent's mode bits;
  - unlinking a folder gives `ErrIsDir`;
  - a file on a read-only remount gives `ErrReadOnly`.
- [x] 1.4 Quarantine in `internal/index` (design D1, D2, D13; ADR 0011):
  - `QuarantineName`, `NotQuarantined` and `InQuarantine` (precomputed blob literals), `IsQuarantinePath`, `DeleteSubtree`, and `DeleteEntries`;
  - the scan walks the quarantine, but the top folder's fold leaves it out;
  - `Refold` leaves it out at the top too;
  - `validName` stays name-agnostic.

  Owns `internal/index` and the `docs/operator.md` paragraph on scans and the quarantine. Verify with tests:
  - the rendered predicate run against a nested quarantined row and a sibling `.precious-quarantine.x`: excluded and kept, respectively;
  - the scenario "A rescan after quarantining": entries moved into quarantine rows with `MoveEntry`, then a rescan, keep their IDs and state, nothing is added or missing, the top folder's totals exclude them, and the quarantine folder's row folds them;
  - a file added by hand under the quarantine is indexed by the next scan.

  Run `go test -race ./internal/index`.
- [x] 1.5 `content.HashEntry`, `(*content.Service).HashArchive`, and `HashMember` (design D9). Owns those additions in `internal/content`. Verify with tests:
  - a full read of a 20 MiB file gives its SHA-256;
  - a changed file gives `invalid_entry_state`;
  - `HashArchive` yields every file member of a zip and of a tar.gz, reading each archive once (instrument);
  - `HashMember` hashes one member;
  - nothing is written to the database.

  Run `go test -race ./internal/content`.
- [x] 1.6 Shared contracts for the slices:
  - `executor.Index.ApplyPurge` and `ApplyUnlink`, added to the interface and implemented in organize's adapter;
  - `executor.Options.Content`;
  - the new op and state constants;
  - `internal/cleanup/stale.MarkStale` (a path range over set items, recorded files, and recorded copies).

  Verify with a test that `MarkStale` marks a check when a recorded copy's folder path is passed, and leaves checks of other paths and sources alone. Run `go test -race ./...`.

## 2. Parallel slices (after group 1)

- [x] 2.1 Readers A (design Reader exclusion, slice 2.1):
  - `search` (`buildFilter` with the `InQuarantine` opt-in, and `dup.go`);
  - `web/api` (children, treemap, `onlyFolder`, members, and the entry detail's `in_quarantine`);
  - `decisions.Totals`, and Home's quarantine bucket;
  - the source JSON's `quarantine`.

  Owns those read paths, and the docs on views and Home. Verify with a test of the scenario "Home after quarantining a folder" through the API (Map, Search with count and select-all, Home, sources) and the existing plan guards: `go test -race ./internal/search ./internal/web/api ./internal/decisions ./internal/sources`.
- [x] 2.2 Readers B (design Reader exclusion, slice 2.2): `content`, `review`, and `relations`. Owns those read paths, and the docs on copies and cards. Verify with tests of the scenario "Quarantining one of two copies" (content `Copies`, a review refresh, a relate pass), and of review cards leaving a quarantined row out, with the existing plan guards: `go test -race ./internal/content ./internal/review ./internal/relations`.
- [ ] 2.3 Executor slice (design D3–D5, D10–D13). Owns `internal/executor` and the `docs/operator.md` section on how Precious changes a disk:
  - the ops `record`, `unlink`, `purge`, and `verify`;
  - the cleanup order (mkdir, rename, record) and the whole-item re-check at the first step (draft identity, own decision, inclusive keeps, D5 copies verified outside transactions);
  - the reserved-name refusal for renames into a source's top;
  - the quarantine-path guards on the new primitives;
  - purge (verify the whole tree, then delete), with per-file comparisons, hard-link tolerance, and freed bytes;
  - `MarkStale` in every outcome;
  - the D4 reconciliation rules, and purge replay only after its re-checks.

  Verify: `go test -race ./internal/executor`.
- [ ] 2.4 Executor tests, driving actions by inserting `queued` rows:
  - R4.2: a planned file modified after drafting ends `changed`, also after a rescan between draft and run, and the rest run;
  - R4.1 at run time: a keep set below the item, or on the item itself, ends it `blocked` or `changed` with nothing made on disk;
  - R4.4 at run time: a staying copy changed before the run ends the item `no_verified_copy`; two duplicate-ground plans on two sources cannot both quarantine the last copies;
  - R4.8 at purge: a file or a relied-on copy changed on disk after the check makes `verify` stop with nothing deleted and the check stale; an unrecorded file found late in an item's tree leaves the whole item untouched;
  - two set files sharing an inode purge without a false `changed`;
  - a crash midway through a purge step, reconciled, deletes the rest once; with writes turned off meanwhile, it deletes nothing more and records what was deleted;
  - a crash after a rename into quarantine and before its outcome, resolved, is indexed by the scan at its quarantine path;
  - the scenarios "Deletion stays inside the quarantine" and "An origin record never replaces a file".

  Owns `internal/executor/r4_*_test.go`. Verify: `go test -race ./internal/executor`.
- [ ] 2.5 The check job (design D7, D8, D10's end transaction). Owns `internal/cleanup/check*.go` (not commands) and the docs on the pre-delete check:
  - `KindPurgeCheck` and `Service.Register`;
  - listing the set, hashing in full, the copy search and verification, verdicts and classes;
  - progress and cancellation.

  Verify: `go test -race ./internal/cleanup/...`.
- [ ] 2.6 Check tests:
  - R4.6 (scenario "A purge set with copies and unique files", on the corpus with a duplicate, a unique photo, and a zip): every file and member is read exactly once (instrument), symlinks and empty folders are recorded `no_content`, and the exact counts and bytes are right;
  - the scenario "A copy on an offline disk";
  - an item holding an unreadable folder is reported `unreadable`;
  - a check marked stale while running, or whose item was restored before it ended, ends `stale`, not `ready`.

  Owns `internal/cleanup/check_test.go`. Verify: `go test -race ./internal/cleanup/...`.
- [x] 2.7 Interface slice (design D16, Interfaces), built against the contract with stubs. Owns `web/ui/src` and the interface paragraphs of `docs/operator.md`:
  - the Cleanup page and its navigation item: plans; a draft for a source; the preview with blocked items and their kept entries, the summary, approve and run, and export;
  - the quarantine browser, with restore (destination chooser on conflict) and the selection of a purge set;
  - the check report: verdicts, classes, per-file and group confirmations, live progress, stale notice, and purge with the freed space and the ZFS note;
  - "Draft a cleanup plan" on review lists;
  - Home's quarantine bucket;
  - the detail panel's quarantine notice;
  - `purge_check` job events;
  - the catalog.

  Verify: `cd web/ui && npm run -s lint && npx vitest run && npm run -s build`.
- [x] 2.8 Interface tests (Vitest):
  - drafting from a source shows blocked items with their kept entries (R4.1 display) and runs only on approve;
  - restore with a conflict asks for a destination (R4.3 display);
  - the check report gates purge until the junk group and each valuable file are confirmed (R4.7 display);
  - a stale check offers a new check;
  - purge shows the freed space and, on ZFS, the snapshot note (R4.5 display);
  - export links to the CSV;
  - no banned term.

  Owns `web/ui/src/**/*.test.tsx`. Verify: `npx vitest run`.

## 3. Cleanup service (after group 2)

- [ ] 3.1 `internal/cleanup` commands and reads, owning `internal/cleanup` (commands, reads, and plans), the small additions in `decisions`, `organize`, `content`, and `sources`, `cmd/precious/serve.go`, and the `docs/operator.md` Cleanup section with its commands, reads, and errors:
  - `plan-cleanup`, with its scope, folding, refusals, draft-time identity, light summary, duplicates rules, `quarantine_name_taken`, and the 30,000-step cap (design D1, D3–D5);
  - `plan-restore`, with the quarantine-parent and destination refusals (D6);
  - `check-purge`, `confirm-purge`, and `plan-purge` (D7, D8, D11);
  - `run-action`'s purge gate, and `MarkStale` in `set-decision`, `set-category`, and `set-group` (D10);
  - the D13 rules in `decisions`, `organize`, and `check-now`: `in_quarantine`, moving out, and the destination refusals;
  - `plan-undo`'s refusal of cleanup kinds;
  - `remove-source`'s `quarantine_not_empty` (D14);
  - `sources.quarantine_entry_id` and its `name_taken` flag;
  - the quarantine, checks, kept, and `export.csv` reads (D16);
  - the wiring.

  Verify: `go test -race ./internal/cleanup/... ./internal/organize ./internal/decisions ./internal/sources ./cmd/precious`.
- [ ] 3.2 Test of R4.1: a discarded folder holding a kept file is listed `blocked` with the file among its kept entries. After running, no entry of it is in quarantine and the other item is. Owns `internal/cleanup/r4_1_test.go`.
- [ ] 3.3 Test of R4.3: a restore to the free original path keeps the IDs and removes the record and item folder. A restore whose path was taken asks for a destination, moves the item there, and leaves the newcomer unchanged. Owns `internal/cleanup/r4_3_test.go`.
- [ ] 3.4 Test of R4.4: a plan from the duplicates list with both copies of `curriculo.doc` discarded plans one and refuses `last_copy`, and running it leaves one copy outside quarantine. Both discarded sides of a relation give `both_sides`. Owns `internal/cleanup/r4_4_test.go`.
- [ ] 3.5 Test of R4.5: a purge on a source whose filesystem type is `zfs`, through commands and the real executor on synthfs, deletes the files and reports `deleted_files`, `deleted_bytes`, and `freed_bytes`. The action JSON carries what the interface needs for the snapshot note. Owns `internal/cleanup/r4_5_test.go`.
- [ ] 3.6 Test of R4.7: `plan-purge` answers `409 purge_not_allowed`, naming the unconfirmed photos, until the junk group and each photo are confirmed, or until the photo's item is restored, or the photo moved out, and the set checked again. Owns `internal/cleanup/r4_7_test.go`.
- [ ] 3.7 Test of R4.8 through commands and the real executor:
  - a `set-decision` on a relied-on copy's folder makes `plan-purge` and `run-action` answer `409 check_stale`;
  - a restore of an item of the set, and a move out of one of its files, each make the check stale;
  - a copy modified on disk only, with no rescan, stops the purge at `verify` with nothing deleted.

  Owns `internal/cleanup/r4_8_test.go`.
- [ ] 3.8 Test of R4.9: the export of a drafted plan with a blocked item has the header and one row per item, with `holds_kept`, and formula-like cells are neutralized. Owns `internal/cleanup/r4_9_test.go`.
- [ ] 3.9 Tests of the remaining scenarios through the API:
  - "Drafting from the discards of a source";
  - "A keep set after drafting";
  - "A quarantined folder keeps its intent";
  - "A quarantined file cannot be moved in bulk or into quarantine", and "Moving a file out of quarantine";
  - `plan-undo` refusing a cleanup action;
  - `plan-cleanup` refusing with `quarantine_name_taken` when the owner has such a folder;
  - "Drafting from the system junk list";
  - "From a review list to quarantine";
  - `remove-source` refusing with `quarantine_not_empty`.

  Owns `internal/cleanup/scenarios_test.go`.

## 4. Integration

- [ ] 4.1 Playwright tests after the last R3 test:
  - drafting a plan for the corpus, with a blocked folder (R4.1), then approving;
  - the quarantine browser, and a restore (R4.3);
  - a check with its report, the junk-group and per-file confirmations (R4.6, R4.7), and the purge with its freed space (R4.5);
  - the export (R4.9);
  - Home's quarantine bucket.

  Verify: `npx playwright test` passes headless.
- [ ] 4.2 Full verification:
  - gofmt, `go vet ./...`, and `go vet -tags e2e,slow ./...`;
  - `go test -race ./...`;
  - `make test-slow`;
  - `make cross`;
  - UI lint, Vitest, and build;
  - Playwright;
  - `scripts/e2e-docker.sh`;
  - `openspec validate --strict`.
- [ ] 4.3 A smoke check of the built binary, with a throwaway script on a copy of an r3 database:
  - the migration applies, and History keeps its actions;
  - a plan with a blocked item;
  - a quarantine, then a restore;
  - a check, confirmations, and a purge;
  - the export;
  - a rescan finds nothing new or missing.

  Then delete the script.
- [ ] 4.4 Deploy on the reference server. The owner tries, on the corpus source only:
  - a plan;
  - a restore;
  - a check;
  - a purge.

  He signs off, recorded in the design addendum.
