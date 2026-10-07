# Tasks

Slices:
- **A:** archives (`internal/archive`);
- **H:** hashing (`internal/content`);
- **R:** relations and Compare (`internal/relations`);
- **V:** review lists and Gems (`internal/review`);
- **Q:** search filter, read API, and viewer (`internal/search`, `internal/web/api`, `internal/viewer`);
- **U:** interface (`web/ui`).

Merge order:
1. Foundation (group 1).
2. A, R, and V in parallel. R and V test on content seeded from the corpus's ground truth (task 1.7), not on hashing.
3. H, after A.
4. Q, after H, R, and V.
5. U can start after group 1. Its tests mock the API; its browser checks are in group 8.

Each group adds its section of `docs/operator.md` at its `<!-- owner: X -->` marker.

## 1. Foundation (coordinator)

- [x] 1.1 Add the R2 domain vocabulary of the Interfaces section:
  - `MemberID` and `Ref`, with `ParseRef` and `String`;
  - the restored `ArchiveFormat`, `ArchiveState`, and `MemberKind`;
  - `ContentState` and `DupFilter`.

  Paths: `internal/domain`. Verify with tests that:
  - `ParseRef` accepts `123` and `m45`, and refuses `m`, `m0`, `-1`, `12x`, and `M45`;
  - every value round-trips through its string.
- [x] 1.2 Write `migrations/0002_content.sql` exactly as in the Interfaces section. Paths: `migrations/`, `internal/store`. Verify with store tests that:
  - an R1 database (baseline only, with seeded entries, decisions, and tags) migrates to 0002 and keeps every row;
  - every new `CHECK` rejects a bad value;
  - `TestForeignKeysIntoEntriesAreIndexed` passes with the new tables;
  - removing a source deletes its `file_content`, `archives`, members, `relations`, `dir_dups`, review rows, and coverage, and keeps the other source's.
- [x] 1.3 Add `[hashing]`, `[archives]`, and `[duplicates]` to `internal/config`, with the ranges and defaults of server-config, to `check-config`, and to `deploy/examples/precious.toml`. Paths: `internal/config`, `cmd/precious`, `deploy/examples`. Verify with config tests:
  - each bound and default;
  - `archives.max_ratio = 1` fails naming the key;
  - `[copies]` is still refused;
  - the docs test finds every new key in the configuration reference.
- [x] 1.4 Change the scanner (D4, D5), paths `internal/index`:
  - compare `ctime_ns` when both values are non-zero;
  - delete the `file_content` and `archives` rows of every updated entry in the same batch;
  - add `(*Handler).OnScanDone`, called after a successful finish.

  Verify with index tests that:
  - a change-time-only change updates the row and deletes its digest row;
  - an unchanged rescan writes nothing;
  - the hook runs once per successful scan and never for a failed or cancelled one;
  - R1.6 and R1.17 still pass.
- [x] 1.5 Pin the read shapes (D16), paths `internal/search`, `internal/web/api`, `web/ui/src/api`, `web/ui/src/test/fixtures.ts`:
  - add the new EntryRow fields to `search.Row`, `search.Columns`, `ScanRow`, `entryRow`, the UI types, and the fixtures, with null values;
  - add `Query.Dup` with parsing and validation (`elsewhere` needs `within`), but no filter SQL.

  Verify that every existing Go test and Vitest test passes, and that `dup=elsewhere` without `within` is 400.
- [x] 1.6 Add `decisions.NewSelection` (Interfaces), which stores the given query JSON and explicit IDs with the R1 expiry and kept counts. Paths: `internal/decisions`. Verify with a test that a selection made from IDs has the same count, bytes, kept, and expiry behavior as `create-selection`.
- [x] 1.7 Extend the corpus (D19), paths `internal/corpus`, `tools/gencorpus`, `internal/index/indextest`:
  - add the tar.gz, gzip, and bzip2 fixtures, the equal-size pair, and the zip with a stored video;
  - add `duplicates`, `relations`, `gems`, and `members` to the ground truth;
  - add synthfs-only large-file fixtures;
  - add `indextest.SeedContent`, which writes `contents`, `file_content`, `archives`, and `archive_members` from the ground truth as a complete hashing run would.

  Verify with tests that:
  - the generator's digests equal `sha256sum` of the written files, and members' digests equal their unpacked bytes;
  - every declared relation pair exists in the tree;
  - the corpus stays within its size test;
  - `SeedContent` on the seeded corpus gives the ground truth's duplicate groups by SQL.
- [x] 1.8 Add the R2 skeleton of `docs/operator.md`:
  - sections for hashing (H), archives (A), duplicates and Compare (R), opportunities and Gems (V), the read API (Q), and the interface (U), each with its owner marker;
  - an upgrade note: back up first, the migration, rollback by restoring the backup.

  Paths: `docs/operator.md`. Verify with the docs tests.

## 2. Archives (slice A)

- [x] 2.1 Restore `curator-m4b:internal/compare/unpack` as `internal/archive`, with its tests and `testdata`. Paths: `internal/archive`. Verify:
  - `go test -race ./internal/archive/` passes in under 60 s, including the zip and bzip2 bomb tests, the path rules, and the cut-off tar.gz;
  - the coordinator's personal-information scan of the restored files finds nothing.
- [x] 2.2 Extend `Member` with `Stored` and `LinkTo`, and add `Zip.Section` for stored members (D17). Paths: `internal/archive`. Verify with tests that:
  - a stored member's section reads its exact bytes at any offset;
  - a deflate member has no section;
  - a tar hard link names its earlier target.
- [x] 2.3 Write the archives section of `docs/operator.md`: formats, budgets, outcomes, what stays unopened, and the in-memory rule. Verify with the docs tests.

## 3. Hashing (slice H)

- [x] 3.1 Implement planning and coverage (D3, D8): size groups across sources and listed members, hard links counted once, the states, and coverage recomputed and kept by deltas. Paths: `internal/content`. Verify with tests that:
  - 990 unique sizes give `unique_size` and no read;
  - hard links are one copy;
  - a size shared across sources makes both files `pending`;
  - a `sampled` file whose group gains an equal sample goes back to `pending`;
  - coverage equals a direct `GROUP BY` after every step.
- [x] 3.2 Implement the `hash` and `hash_now` jobs (D4, D5, D6): reading order, samples, chunked identity-checked reads, hard links read once, 64-file commits with the I9 re-check, yields, cancel, and refresh requests at checkpoints and at the end. Paths: `internal/content`. Verify with synthfs and `instrument` tests that:
  - a second run opens no file;
  - different samples read at most 192 KiB each;
  - equal samples with different content are not one group;
  - a file changing during a read gets no digest;
  - an unreadable file is counted as unreadable;
  - a rescan committing during a read drops the result;
  - a cancelled job's successor reads only unchecked files;
  - the reading order follows D4, with candidate folders before other small files;
  - `hash_now` reads its subtrees first.
- [x] 3.3 Implement zip listing, member hashing by shared size, and tar-family streaming (D7), with `listing` and `complete` states and identity re-checks. Paths: `internal/content`. Verify with tests that:
  - an unchanged archive is not re-read;
  - a changed archive is re-listed after a rescan;
  - a 64 MiB tar.gz with 1,000 members is read once;
  - a budget leaves the archive `partial` with no members;
  - a nested zip stays closed;
  - unique-size zip members cost no reads.
- [x] 3.4 Implement `OpenMember` (D17): archive identity checks shared with the viewer, stored sections, in-memory inflate up to `view_max_bytes`, streaming beyond it, and tar members. Paths: `internal/content`. Verify with tests for:
  - each case;
  - a changed archive returning `invalid_entry_state`;
  - no file written under the state directory or `TMPDIR`.
- [x] 3.5 Register `start-hash` and `check-now`, together with `AfterScan` and `Startup`. Paths: `internal/content`. Verify with command tests:
  - coalescing;
  - 409 `source_offline`;
  - 404;
  - 400 for a file ref or three refs;
  - `AfterScan` enqueuing every online source.
- [x] 3.6 Write the test for **R2.1**: hashing the corpus gives exactly the ground truth's duplicate groups, members included, and the published coverage grows during the run and ends at 100% with nothing unreadable. Verify with `go test -race -run R2_1 ./internal/content/`.
- [x] 3.7 Write an e2e test (build tag `e2e`): scanning and hashing the corpus on a read-only tmpfs, archives included, leaves every entry's times and the mount unchanged. Paths: `internal/content/*_e2e_test.go`. Verify that it passes in privileged Docker `golang:1.27.1` through `scripts/e2e-docker.sh`.
- [x] 3.8 Write the hashing section of `docs/operator.md`: when it runs, the reading order, coverage, `check-now`, and the cost on large archives. Verify with the docs tests.

## 4. Relations and Compare (slice R)

- [x] 4.1 Restore `relate.go`'s snapshot, `partner`, `Relate`, `maximal`, `lift`, and `Candidates` into `internal/relations`, loading the snapshot from the index (D9), with `overlap` at 50%, and restore its relation tests. Paths: `internal/relations`. Verify:
  - the restored tests pass on index-seeded worlds: renamed and rearranged photos, a gap preventing inside, a hard-linked copy freeing nothing, one line per copy, an archive next to its unpacked copy, a folder inside an archive, and twin copies;
  - the personal-information scan finds nothing.
- [x] 4.2 Implement the `relate` job (D5, D6, D10): the dirty flag, the generation swap, inserts guarded by existence, the `dir_dups` upsert of changed rows, pruning of `contents`, and the `after` hook. Paths: `internal/relations`. Verify with tests that:
  - readers never see two generations;
  - a refresh requested during a run causes a second run;
  - a source removed mid-run leaves no orphan rows;
  - `dir_dups` equals a direct computation on the seeded corpus.
- [x] 4.3 Write the test for **R2.3**: on the corpus seeded with `SeedContent`, the two zips are `same` as their unpacked folders, and the Winamp copy is `same` as the original. Verify with `go test -race -run R2_3 ./internal/relations/`.
- [x] 4.4 Implement `Compare` (D11): buckets, size-based proofs, the wrapper rule, the 400 for containment, and paging. Paths: `internal/relations`. Verify with tests for:
  - the zip against `Fotos/2005`;
  - `site_antigo` against its copy (`contato.php` different);
  - a gap counted as unchecked;
  - a folder against its subfolder giving 400.
- [x] 4.5 Write the test for **R2.2**: Compare of `Fotos` with `Fotos - Copia` on the seeded corpus lists `2006/Praia/DSC_editada.JPG` only on the right, the three missing photos only on the left, and nothing unchecked. Verify with `go test -race -run R2_2 ./internal/relations/`.
- [x] 4.6 Write a slow test (tag `slow`): `relate` over a 2,000,000-entry seeded tree finishes within 3 minutes and 1.5 GB resident, and a Compare of two 100,000-file sides answers within 2 s (D20). Verify with `go test -tags slow -run Relate ./internal/relations/`, and record the numbers in the design addendum.
- [x] 4.7 Write the duplicates and Compare section of `docs/operator.md`: the relation kinds, percent duplicated, Compare's groups, and why "not checked" appears. Verify with the docs tests.

## 5. Review lists and Gems (slice V)

- [x] 5.1 Implement `Refresh` (D12, D14): rows of the seven cards and the three Gems sections per generation, outermost rows, and `review_row_sources`. Paths: `internal/review`. Verify with tests on the seeded corpus that:
  - `Backup_PC_2004/C/WINDOWS` is one row of `programs`;
  - no entry counts twice in a card;
  - empty folders and zero-byte files are in `leftovers`;
  - duplicates rows exclude the files of listed relations.
- [x] 5.2 Implement `Cards`, `Rows`, and `Resolve`, with open rows joined live and the per-source filter. Paths: `internal/review`. Verify with tests that:
  - deciding a row removes it and shrinks its card;
  - `decided` lists it;
  - a duplicates row stays open while two copies are undecided;
  - a source filter counts only rows that touch that source.
- [x] 5.3 Register `select-list`. Paths: `internal/review`. Verify with tests that:
  - it gives an R1 selection of the open rows' entries;
  - a bulk discard on it skips kept entries and reports them;
  - `duplicates` and unknown lists are 400.
- [x] 5.4 Write the test for **R2.5**: on the seeded corpus, every card's bytes equal the sum of its rows' bytes read through all pages, for all sources and for one source. Verify with `go test -race -run R2_5 ./internal/review/`.
- [x] 5.5 Write the test for **R2.6**: Gems on the seeded corpus lists the ground truth's unique personal photos and documents oldest first, `Meu orcamento casamento.xls` in the rescue section, and `DSC_editada.JPG` in the only-in-copy section, and never a not-checked file. Verify with `go test -race -run R2_6 ./internal/review/`.
- [x] 5.6 Write a slow test (tag `slow`): at 2,000,000 entries, a review-list page and a Gems page answer in p95 < 300 ms, and all seven cards in p95 < 1 s (D20). Verify with `go test -tags slow -run Review ./internal/review/`, and record the numbers in the design addendum.
- [x] 5.7 Write the opportunities and Gems section of `docs/operator.md`: each card's definition and basis, open rows, select-all, and Gems' sections and coverage. Verify with the docs tests.

## 6. Search filter, read API, and viewer (slice Q)

- [x] 6.1 Implement the `dup` filter SQL (D15). Paths: `internal/search`. Verify with tests that:
  - `copies`, `unique`, and `unchecked` match `SeedContent`'s states;
  - `elsewhere` within `Fotos - Copia` lists every photo but `DSC_editada.JPG`;
  - selections round-trip the filter.
- [x] 6.2 Fill the EntryRow fields, extend the detail with `content`, `relations`, `archive`, and `coverage`, and add `/copies`, plus children and treemap for archives and member folders (D16). Paths: `internal/web/api`. Verify with handler tests on the seeded corpus: copies of `curriculo.doc`, the relation of `emule-0.47c` with its zip, a member's row and detail, and paging inside an archive.
- [x] 6.3 Add `GET /api/home` coverage, cards, and hashing jobs, and add `GET /api/opportunities`, `/opportunities/{list}`, `/gems`, and `/compare`, with their errors. Paths: `internal/web/api`. Verify with handler tests of each shape, a 404 for an unknown list, and a 400 for a containment Compare.
- [x] 6.4 Write the test for **R2.4** at the API: discarding `Documentos/curriculo (1).doc` through `set-decision`, after reading its group, leaves every other copy's decision, triage, and tags unchanged. A `set-decision` naming a member is 400. Verify with `go test -race -run R2_4 ./internal/web/api/`.
- [x] 6.5 Write the test for **R2.7**: with coverage seeded at 80%, the detail of a photo with a unique size says it has no other copy, with that share, and a pending photo reads as not checked. Verify with `go test -race -run R2_7 ./internal/web/api/`.
- [x] 6.6 Serve members through `/content` and `/text` with `OpenMember` (D17). Paths: `internal/viewer`. Verify with tests for:
  - the type and the sandbox CSP of a member JPEG;
  - a 206 for a range of the stored video;
  - an `.html` member served as an attachment;
  - a changed archive returning 409.
- [x] 6.7 Write the test for **R2.8**: the viewer serves a photo inside `Downloads/fotos_2005_do_pendrive.zip`, and the state directory, `TMPDIR`, and the source list exactly the same files before and after. Verify with `go test -race -run R2_8 ./internal/viewer/`.
- [x] 6.8 Run the R1.10 slow test again with the `dir_dups` join and the new fields. Verify with `go test -tags slow -run R1_10 ./internal/web/api/`: children p95 < 300 ms and treemap p95 < 500 ms. Record the numbers in the design addendum.
- [x] 6.9 Write the read-API section of `docs/operator.md`: the new endpoints, `dup`, member refs, and the viewer's rules for members. Verify with the docs tests.

## 7. Interface (slice U)

- [x] 7.1 Home (D16, §11.1): the coverage figure with not-checked and unreadable counts, live hashing progress through events (`hash`, `hash_now`, and `relate` kinds added to `events.ts`), and the opportunity cards linking to their lists. Paths: `web/ui/src/home`, `web/ui/src/app/events.ts`, `web/ui/src/api`. Verify with Vitest using mocked responses and events.
- [x] 7.2 Opportunities and the review list (D12, D13):
  - cards ranked by bytes;
  - rows with summary lines through translation keys;
  - duplicates rows expanding into copies with the R1 decision controls;
  - the "show decided" toggle;
  - select-all with the R1 confirmation and bulk report, absent from duplicates;
  - the keyboard: K, D, L, J or ↓, ↑, and Enter, ignored in inputs and dialogs.

  Paths: `web/ui/src/opportunities`. Verify with Vitest for the keys, the toggle, and the missing select-all in duplicates.
- [x] 7.3 Compare (D11):
  - the two sides from `?left=&right=`;
  - the five groups with counts and bytes, paged lists, and decision controls on rows;
  - "Check now";
  - "Compare with…" in the detail panel, choosing a second folder through the Map or Search.

  Paths: `web/ui/src/compare`, `web/ui/src/detail`. Verify with Vitest for the chooser flow and for the URL round trip.
- [x] 7.4 Gems: three sections with the checked share, decision controls, and select-all. Paths: `web/ui/src/gems`. Verify with Vitest.
- [x] 7.5 The Map, Search, and the detail panel:
  - the percent-duplicated column (hide rank above triage);
  - the `duplication` color mode with its bands and legend;
  - drilling into archives, with member rows and no decision controls on members;
  - the `dup` filter in Search;
  - copies and relations sections, the SHA-256 in the technical details, and the coverage wording of claims.

  Paths: `web/ui/src/map`, `web/ui/src/search`, `web/ui/src/detail`, `web/ui/src/components`. Verify with Vitest, and with `npm run lint` and `npm run build`, whose inline check passes.
- [x] 7.6 Add the English strings for every new screen. Verify that the vocabulary test passes, so no banned internal term is used.
- [x] 7.7 Write the interface section of `docs/operator.md`: Home, Opportunities, review keys, Compare, Gems, and the Map's duplication view. Verify with the docs tests.

## 8. Integration (coordinator)

- [x] 8.1 Wire `serve`:
  - `content.NewService`, its jobs, and its commands;
  - `index` `OnScanDone` → `content.AfterScan`;
  - the `relate` handler with `review.Refresh` as its after hook;
  - `review` commands;
  - `Startup` after `runner.Start`.

  Paths: `cmd/precious`. Verify with a serve test over the corpus: log in, add the source, scan, wait for hashing and `relate`, and check that `/api/opportunities` and `/api/gems` are non-empty and that a hashing job started without a request.
- [x] 8.2 Write the upgrade test: start the R2 server on a database made by the R1 baseline with a scanned source, decisions, and tags. Verify that the migration is recorded, IDs, decisions, and tags are kept, and hashing starts (state-store).
- [x] 8.3 Extend the Playwright suite in `web/ui/e2e` over the built binary and the generated corpus, with one test each:
  - **R2.1:** coverage shown during hashing, then duplicate groups found;
  - **R2.2:** Compare of `Fotos` with `Fotos - Copia`;
  - **R2.3:** the zip shown as the same as its folder, in the panel and in Compare;
  - **R2.4:** discarding one copy from the duplicates list leaves the others' decisions and tags;
  - **R2.5:** each card's bytes equal its list, summed over all pages;
  - **R2.6:** Gems;
  - **R2.7:** the coverage wording in claims;
  - **R2.8:** a photo inside the zip in the viewer;
  - browsing inside an archive;
  - review keys;
  - layout of Opportunities, a review list, Compare, and Gems at 1366×768 and 1920×1080.

  Verify that `npx playwright test` passes headless with no CSP violation or console error.
- [x] 8.4 Finish `docs/operator.md`:
  - remove the owner markers;
  - add the R2 introduction and upgrade notes;
  - update the README's feature list and status for R2.

  Verify that `grep -n 'owner:' docs/operator.md` returns nothing and that the docs tests pass.
- [x] 8.5 Run the final verification:
  - `gofmt -l .` is empty;
  - `go vet ./...` and `go vet -tags e2e,slow ./...` are clean;
  - `go test -race ./...` passes, with every package under 60 s;
  - `make test-slow` passes, R1.10 included;
  - `make cross` passes (R1.15);
  - `npm run lint`, `npm test`, and `npm run build` pass;
  - the Playwright suite passes;
  - the e2e tests pass in privileged Docker;
  - `openspec validate r2-duplicates-opportunities-gems --strict` passes;
  - `walkbench` warm on the development machine shows no regression against the R1 A13 numbers.
- [x] 8.6 Smoke-check the built binary with a throwaway script, with no network access:
  1. `check-config`;
  2. `serve` against a copy of an R1 database;
  3. add a generated corpus and scan it;
  4. wait for hashing;
  5. compare `/api/opportunities` with the ground truth;
  6. fetch a member photo;
  7. take a backup and check its integrity.

  Verify that it reports success, then delete the script.
- [x] 8.7 On the reference server, redeploy the smoke instance with the R2 binary over its R1 database, let it hash the owner's dataset, and run one `relate`. Record in the design addendum the hashing time, bytes read, archives listed, `relate` time and memory, and the database size (D20).
- [ ] 8.8 The owner smoke-tests R2 in a real browser on the regression corpus and the owner's dataset: Home coverage, Opportunities and review lists with the keyboard, Compare, Gems, the duplicates in the Map and Search, and a member photo. Verify with the owner's sign-off, with any findings recorded in the design addendum.

## 9. Findings of the owner's walkthrough (option A, chosen 2026-10-06)

- [x] 9.1 Detail panel: an `overlap` reads by side, side `a` as "Most of it is also in B" and side `b` as "Most of A is also in it" (B16). Verify with Vitest for both sides.
- [x] 9.2 A folder's candidate bytes leave out unreadable files, and member folders count `sampled` as checked (B17, duplicates). Verify with a Go test: a folder whose only unchecked file is unreadable reads as checked, while coverage still counts the file as unreadable.
- [x] 9.3 The preview of an unreadable file says Precious could not read it (B18, inventory-explorer). Verify with Vitest.
- [x] 9.4 Zip member names that are not valid UTF-8 are displayed decoded from code page 850 at every display site, with the raw bytes kept (B19, archive-contents). Verify with Go tests of the decoder and of a member row built from a raw-named zip.
- [x] 9.5 Epoch modification times are unknown: the folder aggregates, the by-year unknown bucket, null API times, and the UI wording (B20, file-index). Verify with Go tests (aggregates, by-year sums, API nulls, a rescan refreshing older aggregates) and Vitest (the unknown year label).
- [x] 9.6 After a key decision removes the selected row, the next row is selected (B21, review-lists). Verify with Vitest (D then K decides two rows) and the Playwright review-keys test.
- [x] 9.7 J on the last loaded row loads and selects the next page (B21, review-lists). Verify with Vitest over two pages.
- [x] 9.8 Compare without a bucket opens the first bucket that holds files (B22, duplicates). Verify with Vitest (an inside pair opens on only right, a same pair on identical) and the Playwright Compare tests.
- [x] 9.9 `unpacked_archives` rows name their folder and link Compare (B23, review-lists). Verify with a Go API test and Vitest.
- [x] 9.10 Duplicates relation rows count files as the Map does; group rows show no file count (B24, review-lists). Verify with the Go API test and Vitest.
- [x] 9.11 Wording: lower-case signals in summaries, the count first on a card with no bytes, and "Not checked or unreadable" in Search (B25, review-lists). Verify with Vitest.
- [x] 9.12 Copies carry their own decision, shown as the active button in a group's copies (B26). Verify with Go tests of `content.Copies` and the API, and Vitest.
- [x] 9.13 Merge, then run the verification of task 8.5. Redeploy the smoke instance and rescan the owner's source, so that folder dates and duplicates figures refresh. Then check A1–A12 on the owner's dataset.
