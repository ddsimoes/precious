# Tasks

## 1. Foundation: schema, shared signatures, and primitives

- [x] 1.1 (coordinator) Write `migrations/0006_find_copies.sql` exactly as in the design's Interfaces schema (D21). Paths: `migrations/`, `internal/store/copies_schema_test.go`. Verify with store tests (an `m3aOnly` helper over files 0001–0005) that:
  - an M3a database migrates with every existing row intact;
  - a second open `disposition_evidence` row for one node is rejected, while an ended one plus an open one is accepted;
  - invalid search states, stop reasons, check states, relations, file kinds, and digests whose length is not 32 are rejected;
  - a `copy_files` row with `check_state = 'hashed'` and no `content_id`, or `pending` with one, is rejected;
  - a `copy_results` row with both or neither of `copy_node_id` and `copy_unit_id` is rejected.
- [x] 1.2 (coordinator) Add the domain vocabulary from the Interfaces (`CopySearchID`, `CopyResultID` with `String` and parsing, `CopySearchState`, `FileCheck`, `CopyGap`, `CopyRelation`, `CopyFreshness`). Paths: `internal/domain/copies.go`. Verify with table tests: every value round-trips through its parser, and a malformed ID parses to `not_found`.
- [x] 1.3 (slice FS) Add file reads to `fsaccess` (D5, D6):
  - `EntryInfo.Ctime`;
  - `Lstat` sets `MountBoundary` on a regular file that is a mount point or on another device than its directory;
  - the `File` interface and `Dir.OpenFile` with `ErrNotRegular`;
  - the OS backend's `O_NOFOLLOW|O_NONBLOCK|O_NOATIME` open with an `EPERM` retry, and the closing identity check.

  Update the package doc. Paths: `internal/fsaccess/{fsaccess,os,errors}.go` and tests. Verify with tests over a real temp directory:
  - reads at offsets return the written bytes, and `Stat` matches `Lstat` including `Ctime`;
  - a test task for **A11 symlink swapped in before a read**: `changed_during_observation`, and no byte of the outside target is returned;
  - a test task for **A11 FIFO swapped in before a read**: the open returns at once with `changed_during_observation`;
  - a file rewritten between `Lstat` and `OpenFile` fails the identity check;
  - `OpenFile` with a directory, symlink, or multi-component name is refused before any system call;
  - the `EPERM` retry path is exercised with a root-owned, world-readable file, skipped when the tests run as root (root may always use `O_NOATIME`).
- [x] 1.4 (slice FS) Give `synthfs` file content (`Content`, `Seed`, `Patch`, `Ctime`, and default content generated from the inode, without allocating the whole file) and change times. Add `OpOpenFile`, `OpReadAt`, `OpFileStat`, and `BytesRead` to `instrument.Recorder`, with `InjectError` and `SetBeforeCall` working for the new operations. Paths: `internal/fsaccess/synthfs`, `internal/fsaccess/instrument`. Verify with tests that:
  - equal seed and size give equal bytes, and different inodes give different bytes;
  - a 1 GiB generated file reads at any offset without allocating it;
  - `Patch` and `Size` advance `Ctime`;
  - `BytesRead` counts exactly the bytes returned;
  - a `SetBeforeCall` hook on `OpReadAt` can patch the file mid-read;
  - every existing `synthfs` and `instrument` test passes unchanged.
- [x] 1.5 (slice FC) Add `[copies]` to the configuration with the D20 defaults and ranges. Paths: `internal/config`, `deploy/examples/read-only-no-model.toml`, and the `docs/operator.md` configuration-reference rows. Verify with:
  - tests for **Sample threshold below the large-file threshold**, `yield_bytes` below `read_chunk_bytes`, and each range bound;
  - **Defaults load**;
  - `TestOperatorDocsConfigReference`, `TestExampleConfig`, and `TestEveryExampleConfigLoads`.
- [x] 1.6 (coordinator) Create `internal/compare` with its foundation files:
  - `compare.go`: package doc, `KindCopySearch`, `Payload`, `ScopeKey`, `Deps`, `New`, `Queryer`, progress keys;
  - `search.go`: `Search`, `Root`, `PreRange`, `LoadSearch`;
  - `freshness.go`: `RangeFreshness`, `ResultFreshness` (D13).

  Also add `intent.OutcomeStaleEvidence`, and add `docs/operator.md` skeletons with owner markers for slices W, H, E, M, and U (a `## Finding copies` section, Read API rows, Pages, audit rows). Paths: `internal/compare`, `internal/intent/disposition.go` (constant only), `docs/operator.md`. Verify with freshness tests over seeded rows:
  - `current`;
  - `changed` for a linked file node with a new revision, a deactivated linked directory, and an atomic unit whose dirty version moved;
  - `superseded` when a newer partial search exists;
  - `source_changed` after an epoch bump;
  - the full race suite passing unchanged.

## 2. Slice W: starting a search and listing

- [x] 2.1 Implement `Service.Start` (D3) and the `find-copies` command (Interfaces: Commands). Paths: `internal/compare/start.go`, `internal/commands/find_copies.go` with a `Deps.Compare` field (nil = unknown command). Verify with command tests for:
  - **Search a whole source**;
  - **Second request while one runs**;
  - **Folder that cannot be searched**;
  - nested folders reduced to the outer one in `roots`;
  - `unknown_source`, `source_unconfigured`, and duplicate IDs;
  - an idempotent replay returning the same search.
- [x] 2.2 Implement `Service.List` (D4): chain open for root, expanded, and atomic folders, the DFS lister, node linking per batch, gaps, `pre`/`post`, the entry budget, `attempt`, and the epoch check in every batch write. Paths: `internal/compare/list.go`. Verify with `synthfs` plus `instrument` tests for:
  - **Search inside an atomic unit** (10,000 entries, node count unchanged, unit atomic);
  - **Gaps recorded, nothing crossed** (no `OpenDir` through the symlink, FIFO, mount, or unreadable directory, and no `OpOpenFile` at all);
  - linked entries carry the node revisions and dirty versions;
  - **Budget stop** at list level: `entry_budget`, unlisted directories as `budget` gaps;
  - an epoch change between batches fails with `source_epoch_changed`;
  - a second `List` call increments `attempt` and leaves the earlier attempt's rows unread;
  - ranges: every file's `pre` lies in its folder's `pre..post`.
- [x] 2.3 Fill the "Starting a search" and "What a search lists" parts of `## Finding copies` (scope, single flight, gaps, the snapshot not being inventory, ADR 0005), with a JSON request and response example. Add a docs test that decodes the example through the command decoder. Paths: `docs/operator.md`, `internal/commands/find_copies_docs_test.go`. Verify with the docs tests.

## 3. Slice H: checking files

- [x] 3.1 Implement `Service.Prepare` (D7 step 1, D9, D10): empty, `distinct_size` over distinct inodes, and cache hits (never for a zero change time). Paths: `internal/compare/hash.go`. Verify with tests over seeded snapshots and `synthfs` for:
  - **Unique sizes cost no reads**;
  - two hard links to one file are not a size collision on their own;
  - a cache row with a different change time is not used, which is the test for **Restored modification time does not hide a change**;
  - a cache row from an older epoch is not used.
- [x] 3.2 Implement `Service.Hash` and `Service.Finish` (D5, D7 to D10, D16):
  - sample groups and full SHA-256 reads with the identity check before and after;
  - chunked `ReadAt` under `rt.FSCall`;
  - yields every `yield_bytes` and per commit batch;
  - commits of at most 64 files with the epoch check;
  - `file_digests` written only from stable reads and deleted on an unstable one;
  - one read per inode.

  Paths: `internal/compare/hash.go`. Verify with tests for:
  - a test task for **A10 sampled-hash collision is not a copy** at hash level (two 32 MiB files, equal samples, both read fully, different contents);
  - **Different samples skip the full read** (1 GiB generated files, `BytesRead` equal to the samples only);
  - **Equal names and sizes are not enough**;
  - a test task for **A10 file changing during a check is blocked** (a hook patches the file mid-read: `unstable`, no content, no cache row);
  - a test task for **A10 file changed since listing is blocked**;
  - **Large file read in chunks** (64 reads for 64 MiB), with a cancel between chunks returning before the next read;
  - a selection by `Ranges` reading only files inside them;
  - `Finish` turning every remaining `pending` file into `not_checked`.
- [x] 3.3 Fill the "How files are checked" part of `## Finding copies`: unique sizes, samples, full reads, the cache and its key, unstable files, hard links, chunking and yielding, and the `O_NOATIME`/read-only mount note. Paths: `docs/operator.md`. Verify with the docs tests.

## 4. Slice E: relations and results

- [x] 4.1 Implement `LoadSnapshot`, `Relate`, and `Candidates` (D11, D12). Paths: `internal/compare/relate.go`. Verify with tests over seeded snapshots for:
  - **Renamed and rearranged photos are found inside**;
  - **A gap prevents an inside claim**;
  - **Two identical folders**;
  - **Hard-linked copy frees nothing**;
  - **One line per copy**;
  - **A loose duplicate archive**;
  - symlinks matching by link text;
  - the 10% overlap floor and the `report_min_bytes` floor;
  - `Candidates` returning the ranges of `code` and `code-copy` but not `notes` (the selection half of **Copied folder of small files is checked**);
  - a 200,000-file, 20,000-folder snapshot relating within the package budget, with the spec-size 1,300,000-file case behind `//go:build slow`.
- [x] 4.2 Implement `Service.WriteResults` (D12, D14, D15):
  - ranks, denormalized paths and node or unit IDs, counts and gaps JSON, `freeable_bytes`, and the 10,000-result limit;
  - superseding older searches;
  - batched pruning with yields;
  - the final state and stop reason.

  Paths: `internal/compare/results.go`. Verify with tests that:
  - an older search becomes `superseded` and its snapshot rows are gone while its results remain readable;
  - the limit keeps the highest ranks and records `result_limit`;
  - an epoch change before the final transaction ends the search `failed` with history-only results (**Source replaced during a search** at store level);
  - a crash between result batches leaves the search non-terminal, so the next attempt rewrites them.
- [x] 4.3 Fill the "Reading the results" part of `## Finding copies`: inside, same, overlap, file results, freeable bytes, why a gap blocks "inside", one line per copy, "no other copy in this search", and freshness states, including deep changes inside atomic folders. Paths: `docs/operator.md`. Verify with the docs tests.

## 5. Slice M: evidence on marks and the marked queue

- [x] 5.1 Implement `CheckEvidence` and `RecordEvidence` (D17), end open evidence inside `intent.SetDisposition`, and accept `copy_result_id` in `set-disposition` items (single and bulk, audit detail, `canonical()` unchanged when absent). Update the `app.js` `set-disposition` payload to send `data-copy-result-id`. Paths: `internal/compare/evidence.go`, `internal/intent/disposition.go`, `internal/commands/owner.go`, `web/static/app.js`. Verify with command tests for:
  - **Mark a copy with its evidence**;
  - **Stale evidence refused**;
  - **Stale evidence in a bulk request**;
  - **Protection still refuses**;
  - **Evidence for the wrong node or disposition**;
  - **A copy inside an atomic unit**;
  - **Evidence ends with the mark**;
  - an existing request's idempotency digest being byte-identical to M3a's.
- [x] 5.2 Add the `marked` queue (D18): `inventory.QueueMarked`, `NodeDetail.Evidence`, `GET /api/review?queue=marked` with evidence and freshness, the review page tab with default bulk disposition `unreviewed`, and the evidence line in the inspector's owner decisions. Paths: `internal/inventory/review.go`, `internal/web/explorer/review_*.go`, `web/templates/{review,node}.html`. Verify with page and API tests for:
  - **Marked copy listed with its evidence**;
  - **Decided units leave the queues** still passing;
  - a marked file node listed;
  - the self-contained HTML test covering the new tab.
- [x] 5.3 Document copy evidence in `## Owner decisions` (a request example with `copy_result_id` and the bulk `stale_evidence` outcome), the `marked` queue in `## Review queues`, and the audit detail's `copy_result_id`. Paths: `docs/operator.md`. Verify with `TestOwnerDecisionsDocExamples` and the docs tests.

## 6. Slice U: Copies pages and read API

- [x] 6.1 Implement `compare.Reader` (Interfaces): searches, search detail with gap examples, ranked results with freshness and mark or refine information, and node results for the inspector, with opaque cursors. Paths: `internal/compare/reader.go`. Verify with tests over seeded rows for:
  - result order and paging;
  - **Change after the search** (a seeded revision bump reads `changed`);
  - **Deep change inside an atomic unit** (a change below the unit's listing stays `current`);
  - `mark` null for a unit-held copy;
  - a refine hint for an atomic holder and a refresh hint for an expanded one.
- [x] 6.2 Add the read API, `/copies`, `/copies/{id}`, the inspector's Copies section with the "Find copies in this folder" control, the header link, and the `find-copies` payload and navigation in `app.js` (D19). Paths: `internal/web/explorer/copies*.go`, `web/templates/{copies,copy_search,node,layout}.html`, `web/static/app.js`. Verify with page and API tests for:
  - **Results page**;
  - **Copy inside an atomic unit**;
  - **Gaps are visible**;
  - **Results through the API**;
  - **Unknown search**;
  - **Inspector of a copy**;
  - a malformed cursor returning 400;
  - the self-contained HTML test covering the new pages;
  - the header-link assertion updated.
- [x] 6.3 Document the Copies pages under `## Using the web interface` and add the three read endpoints to the Read API table. Paths: `docs/operator.md`. Verify with the docs tests.

## 7. Integration (coordinator)

- [x] 7.1 Write `compare.NewHandler` (design D7, D15):
  - load, `BeginInspection`, and the epoch check;
  - `List`, `Prepare`, large `Hash`, provisional `Candidates`, small `Hash`, `Finish`, and the final `Relate` and `WriteResults`, with progress phases;
  - on a cancel after listing, `partial` results with `context.WithoutCancel`.

  Wire `serve`: `RegisterClass(KindCopySearch, …, ClassBulk)`, `commands.Deps.Compare`, the explorer's reader, and the `inspection`-free import graph. Paths: `internal/compare/handler.go`, `cmd/curator`. Verify with a `cmd/curator` test that a served temp tree with a copied folder finds it `inside` through the real filesystem, and that a cancel after listing leaves a `partial` search with results.
- [x] 7.2 Add `withCopies()` to the scenario harness, and scenario tests over HTTP:
  - test tasks for **A10 sampled-hash collision is not a copy**, **A10 file changing during a check is blocked**, and **A10 file changed since listing is blocked**, end to end;
  - **Renamed and rearranged photos are found inside**;
  - **Copied folder of small files is checked**;
  - **Repeating a search reads only changes** (`BytesRead`);
  - **Interrupted searches resume through the cache** (**Restart during hashing**);
  - **Budget stop**;
  - **Source replaced during a search**;
  - **Inbox work during a copy search**;
  - **Only copy searches read content** (a scan, intake poll, probe, walk, and peek record no `OpOpenFile`);
  - a mark from a result appearing in the `marked` queue.

  Paths: `internal/scenario`. Verify with `go test -race ./internal/scenario/` under 60 s.
- [x] 7.3 Add e2e tests for **A11 bind-mounted file** (privileged Docker `golang:1.27.1`) and **A20 copy search on a read-only mount**. Paths: `internal/fsaccess/*_e2e_test.go`, `internal/compare/*_e2e_test.go`. Verify both pass in privileged Docker.
- [x] 7.4 Finish `docs/operator.md`:
  - remove every owner marker;
  - set the intro to M4 and revise every statement that curator never reads file content;
  - add the copy-search row to the class table and the audit rows;
  - add "Upgrading from M3a to M4" (migration `0006`, snapshot size, rollback);
  - link ADR 0005 and ADR 0006.

  Verify that `grep -n 'owner:' docs/operator.md` returns only prose, and that the docs tests pass.
- [x] 7.5 Run the final verification:
  - `gofmt -l .` empty;
  - `go vet ./...` and `go vet -tags e2e,slow,live ./...`;
  - `go test -race ./...` with every package under 60 s;
  - `go test -race -tags slow ./...`;
  - the e2e tests in privileged Docker `golang:1.27.1` for `fsaccess`, `sources`, `discovery`, and `compare`;
  - a headless-Chrome pass over `/copies`, `/copies/{id}`, the inspector's Copies section, the review page's `marked` tab, and the find-copies, mark, and unmark controls under the production CSP.

  Verify that all are clean.
- [x] 7.6 Smoke-check the built binary with a throwaway script over a synthetic tree shaped like the surveyed archive:
  - three photo trees with renamed and rearranged copies;
  - an archive next to its extracted copy;
  - a code tree and its copy;
  - unique files, a hard link, a symlink, an unreadable directory, and a `.part` file.

  The script must:
  - run `check-config` on each example;
  - `serve`, start `find-copies` on the whole source, and observe progress;
  - check that both photo copies are `inside` the original, the code copy is `inside`, the hard link frees nothing, and the gaps are listed;
  - mark a copy with its evidence and see it in the `marked` queue;
  - rewrite one file and see the result `changed`;
  - run a second search that reads only the rewritten file (`bytes_read`);
  - restart mid-hash and see no duplicate reads of committed files;
  - run `curator backup` and check its integrity.

  Verify that the script reports success with no network access, then delete it.
