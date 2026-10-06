# Tasks

## 1. Foundation: schema, shared signatures, and primitives

- [x] 1.1 (coordinator) Write `migrations/0007_archive_copies.sql` exactly as in the design's Interfaces schema (D18). Paths: `migrations/`, `internal/store/archives_schema_test.go`. Verify with store tests (an `m4Only` helper over files 0001–0006) that:
  - an M4 database with searches, roots, and results migrates with every row intact;
  - each search gets one `copy_search_sources` row with its epoch, each root its node's source, and each result both sources;
  - invalid archive formats, archive states, member kinds, inner kinds, and `copy_files.archive_state` values are rejected.
- [x] 1.2 (coordinator) Add the domain vocabulary from the Interfaces: `ArchiveFormat`, `ArchiveState` with `Opened`, `MemberKind`, and `InnerKind`. Paths: `internal/domain/archives.go`. Verify with table tests that every value round-trips, and that `Opened` is true exactly for `complete` and `cached`.
- [x] 1.3 (coordinator) Add the archive budgets to `[copies]` with the D17 defaults and ranges, including `archive_max_entries` ≤ `entry_budget`. Paths: `internal/config`, `deploy/examples/read-only-no-model.toml`, and the `docs/operator.md` configuration-reference rows. Verify with:
  - tests for **Archive ratio budget out of range**, each range bound, and the entry-budget cross-check;
  - **Defaults load**;
  - `TestOperatorDocsConfigReference`, `TestExampleConfig`, and `TestEveryExampleConfigLoads`.
- [x] 1.4 (slice J) Add `UseSource` to `jobs.Runtime` and implement it in the runner (D13): same-key no-op, release and wait as a waiter of the job's class, placeholder exclusivity, `jobs.device_key` update, watchdog source, and cancel while waiting. Update every `jobs.Runtime` test fake in the repository so it compiles; a fake records the call and returns nil. Paths: `internal/jobs/{contract,runtime,arbiter,devices,runner}.go` and tests, plus the fakes. Verify with `go test -race ./internal/jobs/` that:
  - **Two sources on one device**: the move keeps the slot and records no wait;
  - a move to a busy device waits until its slot frees, and the first device's waiting interactive job is granted at once;
  - a cancel while waiting returns `ctx.Err()` with no slot held, and the job finishes cleanly;
  - an overdue `FSCall` after a move marks the new source unresponsive;
  - every existing jobs test passes unchanged.
- [x] 1.5 (coordinator) Refactor the `compare` foundation for several sources and add the shared archive primitives:
  - `Search.Sources` with `Epoch`, `Root.Source`, `LoadSearch` from `copy_search_sources` and the roots;
  - `CheckLive` over every source epoch;
  - the `Sources` interface, with `List` and `Hash` taking it. The handler adapts by implementing `Sources` with `UseSource` and `BeginInspection`, so existing single-source behavior is unchanged;
  - `PhaseArchives` and the archive progress keys;
  - `compare/unpack/format.go` with `Classify` and `MemberName` (D2).

  Paths: `internal/compare/{compare,search,state,handler}.go`, `internal/compare/unpack/format.go`, and the compare test helpers. Verify with:
  - every existing `compare`, `cmd/curator`, and `scenario` test passing;
  - a `LoadSearch` test over two sources;
  - a test for **One of two sources replaced**: `CheckLive` fails with `source_epoch_changed` when either epoch changes;
  - a `Classify` and `MemberName` table test covering the names in **Formats opened and not opened**, `.tgz.old`, upper case, `e.sql.gz` → `e.sql`, and `.tar.gz` not being treated as plain gzip.
- [x] 1.6 (coordinator) Add the docs skeleton: in `docs/operator.md`, add `<!-- owner: X -->` markers where each slice documents its work (Finding copies: archives, budgets, cache; across sources; reading archive results; archive contents in the inspector; marks on archives; the find-copies request). Verify that the docs tests pass.

## 2. Archive readers (slice U, `internal/compare/unpack`)

- [x] 2.1 Implement `OpenZip`, `Zip.Members`, and `Zip.Open` (D3–D6):
  - the entry count is checked before the directory is parsed;
  - path cleaning and collision detection;
  - encrypted, unsupported, and corrupt outcomes, each naming the first offending member;
  - symlink and directory members;
  - cumulative ratio, byte, and time budgets across member reads.

  Paths: `internal/compare/unpack/{zip,paths,limits}.go` and tests, with archives built in memory. Verify with tests for:
  - **M4b-4 Zip bomb stops at the ratio budget**: a 1 MiB zip of 1 GiB of zeros stops before 128 MiB, `partial`, naming the ratio budget;
  - **Entry budget**: no member listed;
  - **Checksum mismatch**: a member with a wrong CRC-32 reads as `corrupt`;
  - **Name says zip, bytes do not**: `unsupported`;
  - traversal (`../../etc/passwd` and an absolute path), duplicate (`a/b.txt` and `a//b.txt`), file-and-folder collision, encryption flag, and LZMA method, each giving its state and member;
  - a backslash name kept as one component, and raw non-UTF-8 names kept byte for byte.
- [x] 2.2 Implement `Stream` for tar, tar gzip, tar bzip2, gzip, and bzip2 (D3–D6):
  - tar member types, with hard links to earlier members and `corrupt` for a later target;
  - sparse members unpacked to their logical size;
  - multistream gzip;
  - single-file members named by `MemberName`;
  - budgets checked per chunk, the time budget through `Limits.Now`;
  - `ctx` checked between chunks.

  Paths: `internal/compare/unpack/stream.go` and tests. Verify with tests that:
  - a `.tar.gz` cut off in the middle is `corrupt`, naming the member being read;
  - a tar with `..` is `rejected`;
  - the members' bytes and kinds match what was written, for each format;
  - a bzip2 bomb stops at the ratio budget;
  - the time budget stops a slow stream as `partial` with a fake clock;
  - the package's `go test -race` takes under 20 s.

## 3. Archive phase and cache (slice O; after group 2 merges)

- [x] 3.1 Implement `OpenArchives` (D7, D8):
  - per source through `Sources.Use`, largest first;
  - cache reuse by full identity, and `opening` rows of earlier attempts deleted first;
  - identity-checked chunked reads with yields;
  - streamed members hashed into `archive_entries` in batches of at most 1,000, folder sizes at completion, and the whole-file digest into `file_digests`;
  - zip listings without content;
  - outcomes on `copy_files`, counts, examples, and progress;
  - the unstable path;
  - the search entry budget counting members;
  - cancel abandoning the current archive.

  Paths: `internal/compare/archive.go` and tests. Verify with tests for:
  - **One pass over a large backup**: a 64 MiB `.tar.gz` with 1,000 members; the recorded reads cover its bytes exactly once, and every member and the file have digests;
  - **M4b-5 Repeated search skips an unchanged archive**: only the rewritten one of three is read;
  - **M4b-8 Restart during an archive's read**: the interrupted archive's `opening` rows are deleted and it is read from the start, and the archives completed before are not read;
  - **Archive read in chunks**: every read at most 1 MiB, and a cancel mid-archive leaves nothing cached for it;
  - **Formats opened and not opened**: `f.7z` and `g.rar` get no `OpenFile`;
  - **Archive inside an archive**: the member `old/site.zip` of `bkp.tar.gz` gets a digest as one file member and no rows of its own members;
  - a file rewritten during its read becoming `unstable` with no rows kept;
  - an epoch change between batches failing the search.
- [x] 3.2 Implement `PruneArchives` (D7 retention) in batches of 5,000 rows with a `Yield` between them. Paths: `internal/compare/archive.go` and tests. Verify with a test that it deletes unreferenced rows of an older epoch, a superseded opening of the same file, and an archive missing from a complete whole-source search, and that it keeps every row a `copy_files` row references and the latest opening of each file.
- [x] 3.3 Document archives in `## Finding copies` (formats and names, outcomes, budgets, one pass versus zip directory, the cache and its size) at the O markers. Paths: `docs/operator.md`. Verify with the docs tests.

## 4. Members in the checks (slice H; after group 2 merges)

- [x] 4.1 Extend `Prepare` and `Finish` with members (D9): size groups over files and the members of opened archives, opened archive rows excluded from file checks and from gap counts. Paths: `internal/compare/hash.go`, `internal/compare/members.go`, and tests. Verify with tests that:
  - a file whose size occurs only as a member is not `distinct_size`;
  - an opened archive's file row ends neither `not_checked` nor counted as a gap;
  - an archive that failed to open is checked as a plain file.
- [x] 4.2 Hash zip members in `Hash` (D9): large members first, small members only in candidate ranges, identity-checked opens through `unpack.OpenZip`, digest commits of at most 64, and member failures turning the archive `corrupt`, `partial`, or `unstable` for the search. Paths: `internal/compare/members.go` and tests. Verify with tests for:
  - **Unique-size zip members cost no reads**: the recorded reads cover the zip's directory only;
  - a large member sharing a size with a file being read and matched;
  - a CRC mismatch found while hashing making the zip `corrupt`, and a plain file for the rest of the search;
  - small members read only when their archive's directory is in a candidate range;
  - cached member digests reused by a second search without a read.
- [x] 4.3 Document member checks in "How files are checked" at the H marker. Paths: `docs/operator.md`. Verify with the docs tests.

## 5. Relations and results (slice E)

- [x] 5.1 Splice opened archives into `LoadSnapshot` and extend `Relate` (D10): member keys, unpacked byte counts, freeable rules, the archive as the copy in archive and folder pairs, members as the copy in file results, candidate ranges of spliced folders, and hard links across sources. Paths: `internal/compare/relate.go` and tests that seed `archives` and `archive_entries` rows directly. Verify with tests for:
  - **M4b-1 Backup archive next to its unpacked copy**;
  - **M4b-2 Older snapshot inside a newer one**;
  - **M4b-2 Zip of some photos inside the photo folder**;
  - **Folder inside an archive**;
  - **Archive frees its packed size**;
  - **Part of an archive frees nothing**;
  - a `corrupt` archive taking part only as a plain file;
  - an unread zip member counting as unique or as a gap by its size;
  - **M4b-7 Folder inside a folder on another source** at the relate level;
  - every existing relate test passing unchanged.
- [x] 5.2 Write the new result columns and sides (D11), supersede older searches that share a source, prune snapshots by shared sources, and compute freshness per side source. Paths: `internal/compare/{results,freshness}.go` and tests. Verify with tests for:
  - **Archive rewritten after the search**: a scan's new size on the cataloged archive makes its result `changed`;
  - **Newer search of one source**: an older two-source search reads `superseded`;
  - a side inside an archive stored with no node and with the archive's node as its unit;
  - a `source_changed` side when only the other source's epoch changed.
- [x] 5.3 Document archive results at the E marker: relations with archives, which side is the copy, and freeable bytes. Paths: `docs/operator.md`. Verify with the docs tests.

## 6. Searches across sources (slice X)

- [x] 6.1 Implement `Start(scope)` and the `find-copies` body (D12, D14): whole sources and folders, dropping covered folders, single flight by any shared source, `copy_search_sources` with epochs, and `Enqueue` with the first source. Paths: `internal/compare/start.go`, `internal/commands/find_copies.go`, and tests. Verify with tests for:
  - **Search a whole source**;
  - **Search two sources**;
  - **Second request while one runs**;
  - **Folder that cannot be searched**;
  - an unknown source returning 404 `unknown_source`;
  - the M4 body `{"source_id": …}` returning 400.
- [x] 6.2 List across sources (D12): roots by source ID, `Sources.Use` before each source's first call, `pre` numbering continuing across sources, and `archive_state` set from `unpack.Classify`. Paths: `internal/compare/list.go` and tests. Verify with tests that:
  - the fake runtime records `UseSource` before the first filesystem call of each source;
  - ranges nest correctly across two roots on two sources;
  - `.zip` and `.tgz.old` rows are `pending`, `.7z` rows are `not_opened`, and `.jar` rows have no archive state.
- [x] 6.3 Document searches across sources and the new `find-copies` body at the X markers. Paths: `docs/operator.md`. Verify with the docs tests.

## 7. Read side and pages (slice V)

- [x] 7.1 Extend the reader (D11, D12, D15):
  - sides with source and inner path;
  - search sources, archive outcomes, and examples;
  - `Searches` through `copy_search_sources`;
  - `NodeResults` from the latest complete or partial search covering the source;
  - `ArchiveContents` with cursor byte `0x55`.

  Paths: `internal/compare/{reader,archive_reader}.go` and tests. Verify with tests for:
  - **Side inside an archive through the API**;
  - **M4b-6 Contents of an opened archive**;
  - **M4b-6 Archive changed since it was opened**;
  - **Archive not looked inside**;
  - **Rejected archive**;
  - **Inspector of a copy** for a two-source search;
  - a malformed entry cursor rejected.
- [x] 7.2 Serve and render it:
  - `GET /api/nodes/{id}/archive`;
  - `SearchJSON` and `SideJSON` additions;
  - the inspector's Contents section with folder navigation;
  - archive sides with a badge, inner path, source, and the inner-copy hint on `/copies/{id}`;
  - the archive outcomes block;
  - the multi-source form on `/copies`;
  - the D14 body in `app.js`.

  Paths: `internal/web/explorer`, `web/templates`, `web/static/app.js`, and tests. Verify with tests for:
  - **Archive and its unpacked folder**;
  - **Copy inside an archive**;
  - **Archive outcomes are visible**;
  - **Search several sources**;
  - the endpoint's 404, 409, and 400 cases;
  - the self-contained HTML test covering the new markup.
- [x] 7.3 Document the inspector's Contents section, the Copies page changes, and the new endpoint in the Read API table at the V markers. Paths: `docs/operator.md`. Verify with the docs tests.

## 8. Marks on archives (slice M)

- [x] 8.1 Refuse evidence whose side lies inside an archive, and record sources and inner paths in the evidence detail (D16). Show them in the marked queue. Paths: `internal/compare/evidence.go`, `internal/inventory`, and tests. Verify with tests for:
  - **Mark an archive with its evidence**;
  - **A copy inside an archive**, as a single item (400 naming the archive) and in a bulk request (the whole request is 400);
  - every existing evidence test passing unchanged.
- [x] 8.2 Document marking archives at the M marker. Paths: `docs/operator.md`. Verify with the docs tests.

## 9. Integration (coordinator)

- [x] 9.1 Extend `compare.NewHandler`:
  - `Sources` opened per source after `UseSource`, with the epoch checked;
  - phases `List`, `PruneArchives`, `OpenArchives`, `Prepare`, large `Hash`, `Candidates`, small `Hash`, `Finish`, `Relate`, and `WriteResults`;
  - a cancel during or after the archive phase publishing `partial`.

  Paths: `internal/compare/handler.go`, `cmd/curator`. Verify with a `cmd/curator` test: a served temp tree with a real `.tar.gz` built by `archive/tar`, next to its unpacked folder, reports the archive `same` as the folder, with the archive as the copy.
- [x] 9.2 Add scenario tests over HTTP, two sources on two synthetic devices where needed:
  - **M4b-1 Backup archive next to its unpacked copy**;
  - **M4b-2 Older snapshot inside a newer one**;
  - **M4b-3 Name, size, and CRC are not enough**, with a forged CRC-32 collision;
  - **M4b-4 Unsafe and damaged archives**, all five in one search, with no folder result resting on them;
  - **M4b-5 Repeated search skips an unchanged archive**, by `BytesRead`;
  - **M4b-7 Folder inside a folder on another source**;
  - **M4b-7 Inbox work on both devices during a cross-source search**;
  - **M4b-8 Restart during an archive's read**.

  Paths: `internal/scenario`. Verify with `go test -race ./internal/scenario/` under 60 s, moving property seeds behind the `slow` tag if needed.
- [x] 9.3 Add an e2e test for **Archives are unpacked in memory only**: a copy search over a read-only tmpfs holding a `.zip` and a `.tar.gz`. Assert no mutating call, and that no file appears outside the state directory's database files (the state directory and `TMPDIR` are listed before and after). Paths: `internal/compare/*_e2e_test.go`. Verify that it passes in privileged Docker `golang:1.27.1`.
- [x] 9.4 Finish `docs/operator.md`:
  - remove every owner marker;
  - set the intro to M4b;
  - add "Upgrading from M4 to M4b" (migration `0007`, archive rows, the first search's extra reads, rollback);
  - link ADR 0007.

  Verify that `grep -n 'owner:' docs/operator.md` returns only prose, and that the docs tests pass.
- [x] 9.5 Run the final verification:
  - `gofmt -l .` empty;
  - `go vet ./...` and `go vet -tags e2e,slow,live ./...`;
  - `go test -race ./...` with every package under 60 s;
  - `go test -race -tags slow ./...`;
  - the e2e tests in privileged Docker `golang:1.27.1` for `fsaccess`, `sources`, `discovery`, and `compare`;
  - a headless-Chrome pass, under the production CSP, over the inspector's Contents section, `/copies/{id}` with archive sides and outcomes, the multi-source form, and marking an archive.

  Verify that all are clean.
- [x] 9.6 Smoke-check the built binary with a throwaway script on two sources:
  - a `.tar.gz` backup next to its unpacked folder;
  - two snapshot archives, one inside the other;
  - a zip of some photos;
  - a zip bomb, a traversal zip, an encrypted zip, a truncated `.tar.gz`, and a `.7z`;
  - a folder copied across the two sources.

  The script must:
  - run `check-config`;
  - `serve`, and start a search across both sources;
  - check each result and outcome;
  - restart mid-archive and see the search complete;
  - mark the archive and see it in the `marked` queue;
  - open its inspector contents;
  - run a second search that reads no archive bytes (`archive_bytes_read`);
  - run `curator backup` and check its integrity.

  Verify that it reports success with no network access, then delete it.
