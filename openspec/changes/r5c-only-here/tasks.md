# Tasks

## 1. Foundation (merged first; 1.1–1.3 independent)

- [x] 1.1 Migration `0009_places.sql`, exactly as in design Interfaces (D12). Owns `migrations/`, `internal/store/places_schema_test.go`, and the `docs/operator.md` section "Upgrading from R5b". Verify with store tests:
  - a version-8 database migrates to version 9, and `review_state.dirty` is then 1;
  - each CHECK rejects a negative value;
  - deleting an entry cascades its `entry_places` row;
  - the tests that pin the schema version follow.

  Run `go test -race ./internal/store`.
- [x] 1.2 `domain.Place`, `PlaceAmount`, `PlaceSums`, `Place(complete)`, `OnlyHereBytes`, `SplitArchive`, and `IsDisposable` (design D1, D5, D14, Interfaces). Owns `internal/domain/places.go` and its tests. Verify with table tests:
  - **`Place`:** D1's order; disposable sums never decide; no files gives `none`; `complete=false` turns `other_source` and `none` into `unreadable` and keeps the rest.
  - **`SplitArchive`:**
    - the shares always sum to the size;
    - a 5 GiB archive with 6 GiB of members in one place gives exact shares;
    - a 4 GiB archive split 3:1 gives exact shares;
    - a 1 KB archive of 1 GB members gives at least 1 byte to each place with member bytes;
    - the file goes to the first place in D1's order;
    - no member bytes gives an empty split.
  - **`IsDisposable`:** every combination of `domain.Categories`, `domain.FileKinds` (with an unknown category), and triage, against `FileFamily`.

  Run `go test -race ./internal/domain`.
- [x] 1.3 The front end's types, fetchers, and colors (design Interfaces, Front end):
  - in `api/entries.ts`, the `Place`, `PlaceAmount`, and `PlaceSums` types, the optional `EntryRow` fields, `member_places`, `places_updating`, `ChildSort` `'only_here'`, and the `fetchChildren`, `fetchTreemap`, and `fetchEntry` options with their query keys;
  - in `map/colors.ts`, `'places'` in `ColorMode`, `placeColors`, `colorOf`, and `legendOf`;
  - the `map.colorBy.places` and `map.place.*` keys.

  Owns `web/ui/src/api/entries.ts`, `web/ui/src/map/colors.ts`, those keys of `en.ts`, and their vitest tests. Verify that:
  - the fetchers send `places=1`, `hide=other_source`, and `sort=only_here` only when asked;
  - the query keys differ by option;
  - `colorOf` gives each place its color, and not computed and disposable their own;
  - the legend names each place;
  - `vocabulary.test.ts` passes.

  Run `npm test` in `web/ui`.

## 2. Places in the index (after 1; 2.1, 2.2, and 2.3 independent)

- [x] 2.1 The relate pass (design D3, D4, D5, D14).
  - **Baseline first:** commit the extended slow fixture (a second source, and archives with members) on its own. Record the previous build's pass time and peak RSS on it at that commit.
  - **Copy counting:** `physOf` keyed with the source, `dd.srcOf`, `dd.arcSrc`, and `arcFile`'s content state.
  - **Folder sums:** the per-directory place and disposable sums, in `addFile` (after the `arcAt` test) and in `keyMembers`.
    - `loadFiles` reads each file's `category`, `file_kind`, and `triage`.
    - Members are classified through `Handler.SetClassifier`, wired in `cmd/precious/serve.go`.
    - A disposable complete archive counts whole as disposable.
  - **Rows:** `entryPlaces()` on the final snapshot only, with archive rows through `domain.SplitArchive`, and rows only for entry folders and archive folders.
  - **Writer:** `writeEntryPlaces`, through one generic diff writer that `writeDirDups` also uses.

  Owns:
  - `internal/relations` (`snapshot.go`, `job.go`, new `places.go`, and tests);
  - the relate wiring lines of `cmd/precious/serve.go`;
  - the `docs/operator.md` subsection "Where copies are" under "Duplicates and Compare".

  Verify with unit tests over a synthetic world of three sources, one of them offline. Each case below checks the stored row:
  - **Single files:**
    - only copy by size;
    - copies on the same source;
    - a copy inside an archive on the same source;
    - a hard link that is not a copy;
    - hard-linked archive names;
    - two sources with the same (dev, ino);
    - a tar link member and its target, which are one copy;
    - a copy on an offline source;
    - a quarantined copy that is no copy;
    - a file with no content row, which is unchecked;
    - an archive member with a copy on another source.
  - **Archives:**
    - a backup beside its folder;
    - the same archive on two sources;
    - one unique member;
    - a 5 GiB archive;
    - an archive not completely listed;
    - an archive whose members have no bytes;
    - a nested archive member;
    - a `Thumbs.db` inside a zip of copied photos;
    - a disposable archive.
  - **Folders:**
    - a folder with one only copy;
    - a folder wholly elsewhere;
    - an unreadable subfolder or a mount boundary below a folder adds nothing to its sums, and its readable files are counted (completeness itself is the read API's, 3.1);
    - `Thumbs.db` beside copied photos;
    - a recovered chunk with triage review.

  Also add:
  - `TestEntryPlacesEqualADirectComputation` (design D13), over the seeded corpus plus a second source holding copies of some of its files and a byte-identical copy of one of its zips. Plain SQL places each file and member; Go sums them and splits archives.
  - "An unchanged index writes nothing", checked with temporary triggers.
  - The existing `dir_dups` tests, unchanged.
  - In `relations/slow_test.go` (tag `slow`), on the extended fixture: the pass must stay within 15% of the recorded baseline in time and peak RSS, with both figures recorded in the Addendum, and a second pass must write nothing.

  Run `go test -race ./internal/relations`, then `go test -race -tags slow -run Slow ./internal/relations`.
- [x] 2.2 `search.PlaceSQL`, `DisposableSQL`, `MemberPlaceSQL`, `OfflineOnlySQL`, and `MemberOfflineOnlySQL` (design D1, D2, D6, D11, D14).
  - `DisposableSQL` is generated through `domain.IsDisposable`.
  - `otherCopy` may take alias parameters, as long as what `dupFilter` produces is unchanged.

  Owns `internal/search/places.go` and its tests. Verify over a seeded three-source index, with each file's expected place written by hand:
  - every single-file case of 2.1, for entries and for members, including hard-linked archive names;
  - `NULL` for missing and quarantined entries;
  - `none` for an empty file, a symlink, and a special file;
  - `unchecked` for a file with no content row;
  - the not-connected mark:
    - true when every other-source copy is on an offline or unavailable source;
    - false when one is online;
    - false when the file has no other-source copy;
  - `DisposableSQL` against `domain.IsDisposable` for every combination of category, file kind, and triage;
  - the existing `dup=` tests, unchanged.

  Run `go test -race ./internal/search`.
- [x] 2.3 `content.PreferredCopies` and `relations.CoveredBy` (design D9). Owns `internal/content/preferred.go` and `internal/relations/read.go` (additions only), with their tests. Verify:
  - **`PreferredCopies`:**
    - for other source, an online other-source copy wins over an offline one, and an offline one is named when it is the only one;
    - for this source, a loose copy wins over a member;
    - the file's own hard-link names and its tar link group are never named;
    - `more` is the number of physical copies minus 2, by D1's definition, including hard-linked archive names;
    - a member copy carries its archive;
    - one query serves a page of 200 references.
  - **`CoveredBy`:**
    - `same` from either side;
    - `inside` only for side `a`;
    - `overlap` never;
    - the most matched bytes win;
    - only the current generation counts.

  Run `go test -race ./internal/content ./internal/relations`.

## 3. Read API (after 2; 3.2 after 3.1)

- [x] 3.1 Children, treemap, and entry (design D2, D5, D6, D9, D10, D11, D14, Interfaces):
  - **Children:**
    - the `places`, `hide`, and `sort=only_here` parameters;
    - the set-based sort and hide evaluation;
    - the cursor, which carries the second key, the filter, and the `not_computed` sentinel;
    - the row fields, with `not_computed`;
    - `other_copy` and `covered_by` from the batched reads.
  - **Archive members:** the same parameters and fields in `memberPage` and `memberSubtree`.
    - The archive's own coverage is computed live once per page (`PlaceSQL` and `OfflineOnlySQL` on the archive's entry).
    - `via_archive` and the archive's mark go only on members placed through that coverage.
    - Members are classified with the policy.
  - **Entry:** `GET /api/entries/{id}?places=1`, with `member_places` for an archive (also on the treemap's `entry`) and `places_updating`.

  Owns `internal/web/api` (`children.go`, `rows.go`, `members.go`, `entries.go`, and tests) and the `docs/operator.md` read-endpoint entries. Verify:
  - **Errors:** each one in Interfaces is a 400 `invalid_request`.
  - **Golden:** without `places=1`, every children, treemap, entry, and search response on the corpus is byte-identical to the previous build's.
  - **Sorting:** `sort=only_here` pages 250 children as 200 and 50, with no duplicate or omission, in key order:
    - `not_computed` rows come first when descending and last when ascending;
    - ties are broken by unchecked bytes;
    - a missing folder and a missing archive have place `null` and sort last.
  - **Hiding:** `hide=other_source` keeps exactly D10's places, drops disposable files and missing entries (missing folders and archives included), and keeps `not_computed` rows.
  - **Completeness (I7, "A folder not read completely"):** in children, entry, and treemap, one folder per trigger, each with all its readable files copied to another source. Each reads `unreadable`, and hide keeps it. The triggers:
    - `state = 'unreadable'`;
    - `partial`;
    - its own `mount_boundary`;
    - an unreadable folder below it;
    - a mount boundary below it.
  - **Members:**
    - a complete zip's members carry their places and `via_archive`, and member folders carry `places`;
    - with every member having its own copy on another source while the zip itself is unique, no member has `via_archive`;
    - when the zip has a copy on an offline source only, a member placed through it carries the zip's mark;
    - a `Thumbs.db` member is `disposable`.
  - **The mark and the flag:** `place_offline` is true for a file whose only other-source copy is on an offline source, and false once that source is online. `disposable` is set on a `Thumbs.db` file.
  - **`places_updating`:** true while relations are dirty or a relate job is queued or running, and false after the pass.
  - **Agreement:** on the seeded three-source world after a relate pass, for every folder, its child files' live places plus its child folders' and archives' stored `places` sum to its own stored `places`.
  - **Speed** (tag `slow`), in a new `places_slow_test.go` with design D10's fixture:
    - the cold first page under 1 s at p95 with `sort=only_here`, and with `hide` plus `sort=bytes`;
    - the 100,000-file folder and the 50,000-member archive level recorded;
    - `r1_10_slow_test.go` still passes.

    If the gate fails, stop and report (design D10).

  Run `go test -race ./internal/web/api`, then the slow tests.
- [x] 3.2 Tests of scenario R2.7 for places ("R2.7 Not checked is not an only copy" and "R2.7 Only-copy figures name the checked share"). Owns `internal/web/api/r2_7_places_test.go`. Verify on a world where hashing has checked 80% of the content that could have a copy across all sources, with one file not yet planned:
  - no unchecked or unplanned file reads `only_copy` in children, treemap, or entry;
  - the entry response's all-sources coverage is 0.8;
  - the folder's `places.unchecked` holds its unchecked files, archive members included.

  Run `go test -race ./internal/web/api`.

## 4. The Map and the panel (after 1.3 and 3.1; 4.1 and 4.2 independent)

- [x] 4.1 The Map's Only here mode (design D7, D8, D10):
  - the toolbar's mode toggle and hide option;
  - the totals line, with the coverage note and "Updating…" from `places_updating`;
  - the `place` column, which replaces `duplicated` in the mode, with its track, bar, and texts. "{{n}} copies" comes from `other_copy.more + 2`, and the other-source text notes any disposable files;
  - the row emphasis classes, with disposable rows muted;
  - the second line, holding `other_copy` (with `via_archive` and the not-connected note) or `covered_by` with its Compare link;
  - the `only_here` sort on the column header;
  - the address rules of D7, with the treemap fetching places whenever `color=places`;
  - the `map.places.*` keys.

  Owns `web/ui/src/map/MapPage.tsx`, `web/ui/src/map/Treemap.tsx`, `web/ui/src/components/EntryTable.tsx`, the `map.places.*` keys of `en.ts`, their vitest tests, and the `docs/operator.md` section "Map". Verify with vitest, including the test "R2.7 Only-copy figures name the checked share" over a fixture at 80% coverage:
  - each place's row class and words, `not_computed` included;
  - a folder's bar widths;
  - the column's width, and that it is the last to hide at a narrow width;
  - turning the mode on sets `color=places`; turning it off clears `places`, `hide`, `sort=only_here`, and `color=places`;
  - drilling keeps the parameters;
  - `sort=only_here` without `places` falls back to `bytes`;
  - the not-connected note;
  - "Updating…";
  - the Compare link's decimal IDs.

  Run `npm test` in `web/ui`.
- [x] 4.2 The detail panel's places:
  - a folder's files and bytes per place and disposable, with the all-sources checked share;
  - an archive's `member_places`, labelled as unpacked sizes;
  - a file's place sentence above its copies;
  - the `detail.places.*` keys.

  Owns `web/ui/src/detail/*`, the `detail.places.*` keys of `en.ts`, their tests, and the `docs/operator.md` section "The detail panel". Verify with vitest:
  - the two panel scenarios of the delta spec;
  - an archive's panel;
  - a missing entry, which shows no place.

  Run `npm test` in `web/ui`.

## 5. Browser tests (after 4)

- [ ] 5.1 The e2e fixture and Playwright tests (design D13):
  - **Choose the files first.** List the corpus files and folders that no test in `precious.spec.ts` moves, renames, quarantines, purges, or redates. Take the fixture's copies only from that list, and write the list in the spec file.
  - **Fixture:** `global-setup.ts` writes `disk/copies`, holding copies of three such photos (one of them twice), a byte-identical copy of one such photo folder, and one file of its own.
  - **Projects:** `playwright.config.ts` gets two projects. The existing one ignores `only-here.spec.ts` (`testIgnore`). The new one matches that file only (`testMatch`) and depends on the existing one.
  - **Spec file:** `only-here.spec.ts` adds `disk/copies` as a source, waits for hashing and the relate pass, and removes the source in `afterAll`.

  Owns `web/ui/e2e/*` and `web/ui/playwright.config.ts`. Write one test per browser-checkable inventory-explorer scenario of the delta spec:
  - emphasis follows the place;
  - the mode stays on through drilling and reload;
  - inside an archive;
  - hiding what is safe elsewhere;
  - sorting by what is only on this source;
  - color by where copies are;
  - a copy on the same source;
  - a copy inside an archive;
  - a folder copied whole, through its Compare link;
  - both panel scenarios.

  R2.7 and the unconnected source are covered by 3.1, 3.2, and 4.1 (design D13). Also add the `layout.spec.ts` fit test of the Map in the mode at each viewport. Run `npm run e2e` in `web/ui`, and check that the existing tests still pass unchanged.

## 6. Integration (after 5)

- [ ] 6.1 Run the full verification:
  - gofmt and `go vet ./...`;
  - `go test -race ./...`, with no package over about 60 s;
  - the slow tests of 2.1 and 3.1;
  - `npm test` and `npm run e2e` in `web/ui`;
  - `scripts/e2e-docker.sh`;
  - builds for linux, darwin, and windows on amd64 and arm64.

  Fix anything that fails, on its owning slice's terms.
- [ ] 6.2 Smoke-test the built binary, running as a non-root user, on a copy of the reference server's database:
  - **Baseline:** on one version-8 copy, force `review_state.dirty = 1`, and time the previous binary's pass and its peak memory.
  - **The new pass:** on a second copy, start the new binary. Time the migration and its startup pass; they must stay within 15% of the baseline in time and memory. Record both in the Addendum.
  - **Check the figures:** the external disk's root `places` must equal a throwaway read-only count by D1, D5, and D14, kept out of the repository.
  - **Walk through it in a browser:**
    - the mode at the disk's root and two levels down;
    - the 31,163-child folder with sort and hide, timed cold;
    - an archive;
    - a Compare link;
    - the panel.
- [ ] 6.3 Independent reviews of the change. Fix each finding with a test that fails before the fix, and record the findings in the Addendum.
- [ ] 6.4 Open a PR, wait for CI, then deploy to the test server: back up the database and keep the previous binary (design Migration Plan).
- [ ] 6.5 The owner checks the mode in his browser on his sources and signs off. Record the sign-off in the design Addendum and set ADR 0013 to accepted.
