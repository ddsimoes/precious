# Design

## Context

See proposal.md for the motivation, the specs for the required behavior, and ADR 0005 and ADR 0006 for the two departures from the source spec. These code facts on `master` at `15346a7` shape the approach:

- **Nothing can read a file.**
  - `fsaccess.Dir` offers `Self`, `ReadBatch`, `Lstat`, `OpenDir`, `Readlink`, `FSInfo`, and `Close`. Its package doc says no method reads content.
  - The OS backend (`os.go`) opens directories through `os.Root` with an identity check (`sameObject` on kind, device, inode) and a re-`Lstat` against symlink swaps.
  - `synthfs` files have size and modification time but no content. `instrument.Recorder` has no read operation.
  - `discovery.content_bytes` is validated to 0, and docs say curator never reads content (operator.md:493, 503, 833, 918, 969, 1579, 1603).
- **No change time.** `EntryInfo` and `nodes` carry device, inode, link count, size, and modification time, but not `st_ctim`.
- **Walking inside a unit without nodes already exists.** The aggregate walk (`inspection/walk.go`) is an iterative DFS of `ReadBatch(256)` plus `Lstat`. Every call is bracketed by `rt.FSCall` and the job yields between batches. Its result is only counts. `inspection.loadUnit` and `openSteps` reach a unit by its recorded (name, device, inode) chain, and accept only atomic units.
- **Jobs.** `jobs.kind` is free text. Single-flight is `Tx.EnqueueOnce` with a `scope_key` (unique per kind among active jobs). `ClassBulk` exists, with `Runtime.Yield`. A single watched call longer than `jobs.call_watchdog` (30 s) marks the source unresponsive. Progress is `map[string]int64`.
- **Dispositions are a bare label.** `nodes.disposition` has no reason or evidence column. `intent.SetDisposition` applies the protection and inbox gates and writes audit `disposition_set`. A node marked `cleanup_candidate` by the owner leaves every review queue.
- **Writes are serialized** on one connection, and transactions should stay at a few hundred rows (`store.go:125`).
- **Schema rules.** The latest migration is `0005`. Migrations are additive, with no table rebuilds.

## Goals / Non-Goals

**Goals:**
- One filesystem walk per search, and reads only where a match is possible. Coverage is never better than what was read.
- Every claim of equal content rests on two complete SHA-256 reads of identity-checked regular files.
- A repeated search costs a listing plus reads of changed files.
- The search works on a read-only mount, with no classifier and no network.

**Non-Goals:**
- Archive members, cross-source searches (M4b), near-copies (M6), keepers and removal (M5).
- Path-aware manifests and multiset relations (§11.2, M6).
- Automatic or scheduled searches.
- Showing digests to the owner. They are internal evidence, not a feature.

## Decisions

### D1. Package and import direction (§8.2)

```text
domain, store, jobs, fsaccess, sources, config, clock, reconcile, protection <- compare
compare <- commands        (find-copies; evidence checks inside set-disposition)
compare <- web/explorer    (pages, read API, inspector section, marked-queue freshness)
compare <- cmd/curator     (handler registration)
intent: ends open evidence rows by SQL; imports nothing new
inventory: reads disposition_evidence rows by SQL for the marked queue; does not import compare
```

- `compare` is the package §8.2 names for "selective hashes and relationship analysis". It does not import `inventory`, `intent`, `discovery`, or `inspection`.
- Slices own separate files in `compare`: `list.go` (W), `hash.go` (H), `relate.go` and `results.go` (E), `evidence.go` (M), and `reader.go` (U). Foundation owns `compare.go`, `freshness.go`, and `search.go`. The coordinator owns `handler.go`.
- Rejected:
  - Extending `inspection`'s walker. It records counts only, accepts only atomic units, and serves peeks and walks whose contract is "read no content".
  - Putting hashing in `discovery`. A scan must stay a content-free observation pass (§5.2), and its tests assert zero content bytes.

### D2. The search snapshot (ADR 0005, §4.1, §8.3)

- A search lists its folders into `copy_dirs` and `copy_files`, keyed by search and attempt. These rows are analysis data. No read model outside `compare` joins them, so they never reach the explorer, totals, queues, or descriptors.
- Each `copy_dirs` row holds its parent, raw name, DFS `pre` and `post` numbers (its subtree is `pre..post`), and its state (`listed` or a gap kind). It also holds the linked node, when the directory is an active cataloged node, with that node's observation revision and dirty version at listing.
- Each `copy_files` row holds its directory and `pre`, raw name, kind (`file`, `symlink`, `special`), size, modification and change time, device, inode, link count, link text for symlinks, the linked file node and its revision, check state, content, and sample digest.
- Rejected:
  - Nodes for atomic interiors (ADR 0005).
  - A per-search JSON manifest blob. Results, freshness checks, and the hash phase need indexed access by size, directory range, and identity.

### D3. Scope and single flight (§11.1, ADR 0005)

- A search covers one source: its root, or 1 to `discovery.page_size` listed folders.
- A folder must be an active directory of that source that is not a mount boundary. It may be the root, an expanded directory, or an atomic unit.
- A folder listed below another listed folder is dropped. The response lists the folders kept.
- Kind `copy_search` uses ScopeKey `copies:<source>` and `EnqueueOnce`. A request while a search is active returns that search with `coalesced: true`, even if its folders differ, and the response shows the active search's folders.
- Rejected:
  - Several active searches per source. Two jobs would read the same device and race on the digest cache and supersession.
  - Searches across sources. One job holds one device key (`jobs.Spec.SourceID`), and cross-source results need a second job design. This is deferred to M4b.

### D4. Listing (§5.4, §5.5.2)

- The lister is `compare/list.go`. It opens each folder through its recorded chain: device and inode per ancestor, as `openSteps` does. The chain loader accepts the root, expanded directories, and atomic units.
- It then runs an iterative DFS: `ReadBatch(discovery.list_batch_size)`, one `Lstat` per entry, `Readlink` for symlinks, and `OpenDir` with the listed identity for subdirectories. Every call is bracketed by `rt.FSCall`.
- **Gaps instead of entries:** mount boundaries, unreadable directories, special files, and listing failures.
- **Node linking.** When a listed directory is linked to a node, its batch's names are looked up among that node's active children in one read query per batch. An entry matching an active child of the same kind is linked, with its revision. Below an atomic unit nothing links.
- **Batching.** Rows are written in batches of at most `discovery.list_batch_size` per transaction. `post` numbers are written when a directory is left.
- **Budget.** The search stops at `copies.entry_budget` entries, and the unlisted directories become `budget` gaps.
- **Restart.** Each listing start increments `copy_searches.attempt`. Rows of earlier attempts are ignored and deleted later (D14). The `Lstat` of every directory entry is the same lstat discovery issues, so a listing reads no content.
- Rejected:
  - Using catalog rows instead of a listing for expanded areas. The catalog may lag the disk, and the hash phase must compare against what it is about to open.
  - Linking by device and inode. Hard links and inode reuse make that ambiguous (§5.5.6).

### D5. Reading files (§5.4, §12.2, A11, A20)

- **The open call.** `Dir.OpenFile(name, expect)` validates a single-component name and refuses `expect.Kind != EntryFile` before any call (`ErrNotRegular`, empty outcome). The OS backend first opens the entry with `openat(dirfd, name, O_PATH|O_NOFOLLOW|O_CLOEXEC)` on the directory's own descriptor, which opens no object. It checks identity with `fstat`, then reopens for reading through `/proc/self/fd/<n>` with `O_RDONLY|O_NONBLOCK|O_CLOEXEC|O_NOCTTY|O_NOATIME` (once more without `O_NOATIME` on `EPERM`), and `fstat`s again. A FIFO, socket, or device is therefore never opened at all. (Apply-time refinement: the first draft used `os.Root.OpenFile`, whose final-component symlink handling must not apply.)
- **Identity check.** It then `fstat`s the handle. Anything other than a regular file with `expect`'s device, inode, size, modification time, and change time, on the directory's device and not a mount point, closes the handle and fails as `changed_during_observation`.
- **File mount points.** `Lstat` now also sets `MountBoundary` for a regular file that is a mount point, or that is on another device than its directory. The lister records such a file as a `mount_boundary` gap. Discovery ignores `MountBoundary` on files, as it does today.
- **The `File` handle.** `File.ReadAt` reads at most `copies.read_chunk_bytes` per call, one `rt.FSCall("ReadAt")` each, and ctx is checked between chunks. `File.Stat` is the closing `fstat`. A file is complete when the bytes read equal the listed size, a read at that size returns EOF, and the closing `fstat` equals the listed identity.
- **Writing nothing.** No handle is ever opened for writing. Content buffers are reused and never logged or stored.
- Rejected:
  - `mmap`. A truncation under a mapping raises `SIGBUS`, and the mapping hides read errors.
  - Opening by full relative path with `openat2(RESOLVE_BENEATH)`. Directory handles already give per-component identity checks, and `OpenDir` keeps that contract.

### D6. Change time (§5.5.6)

- `EntryInfo.Ctime` comes from `st_ctim`. It is the zero time when the platform or a test filesystem has none.
- `synthfs` sets the change time at creation and advances it on every content, size, modification-time, permission, or link change.
- The cache key (D9) and the identity check (D5) include it. A file with a zero change time is never served from the cache.
- Rejected: size plus modification time only. Copy and restore tools set modification times, and such a file would keep a stale digest.

### D7. Check order (§11.1, A10)

1. `prepare`:
   - empty files become `empty`;
   - files whose size no other regular file in the search has become `distinct_size`;
   - files with a valid cache entry (D9) become `cached`.
2. Large files, at or above `copies.large_file_bytes` and still `pending`, by size descending:
   - at or above `copies.sample_from_bytes`, samples first (D8);
   - then a full SHA-256.
   - Identical (device, inode) pairs are read once (D10).
3. A provisional relate (D11) runs over the snapshot. Unread small files count by size alone. `relate.Candidates` returns the `pre..post` ranges of every folder pair whose provisional share reaches 50%.
4. Small files, below `copies.large_file_bytes`, `pending`, with a size peer, and inside a candidate range, by `pre` (directory order).
5. Remaining `pending` files become `not_checked`. The final relate writes the results.

- Rejected:
  - Hashing every same-size file. On the surveyed archive, 1.15 million small files share sizes. Their bytes are few, but their seeks would dominate.
  - Skipping small files. Folders made of small files, such as code trees and app backups, could then never be found inside anything.

### D8. Samples (§11.1)

- Three 64 KiB samples, at offset 0, at `size/2 - 32 KiB`, and at `size - 64 KiB`, hashed together with SHA-256. Same-size files are grouped by sample digest, and a file alone in its group becomes `distinct_sample`.
- Sample digests are stored only on the file row of the current search and are never cached or compared across searches.
- Rejected:
  - Caching sample digests. They cannot prove anything, and the cache would need a second validity rule.
  - Sampling files below 16 MiB. Three samples would read 12% or more of the file, so a full read is nearly as cheap.

### D9. The digest cache (§5.5.6, §8.3)

- `contents (id, algorithm, digest, size)` holds one row per distinct content.
- `file_digests` maps a physical file to its content, keyed by `(source_id, source_epoch, dev, ino)`. It is valid while `size`, `mtime_ns`, and `ctime_ns` equal the current listing's values.
- A digest is written only from a complete, stable read (D5) in the same transaction as the file row.
- An unstable read deletes any cache row for that identity.
- Rejected:
  - Keying by path. A rename would force a re-read, and a replacement at the same path with equal metadata could inherit a digest.
  - Keying without the epoch. After a source is reconfirmed, device and inode numbers may belong to different files.

### D10. Hard links (§8.3, §9.2)

- Files with equal `(dev, ino)` in one search share one read and one content.
- A match whose two paths are the same inode counts toward `inside`, `same`, and the matched bytes, but adds nothing to freeable bytes.
- Size grouping counts distinct inodes, so two links to one file are not a size collision on their own.
- Rejected: treating links as copies. Removing one frees nothing, and they are not independent backups (§11.1).

### D11. Relations (§11.1, §11.2, ADR 0006)

- **Keys.** Every non-empty regular file has one:
  - its content, when `hashed` or `cached`;
  - "unique", when `distinct_size` or `distinct_sample`, and never matching;
  - a gap, when `unstable`, `not_checked`, or `mount_boundary`.
- **Other entries.** Symlinks have the key of their link text. Special files and directory gaps are gaps. Empty files are ignored.
- **Inside.** Folder A is inside folder B, where B is neither an ancestor nor a descendant of A, when A has no gap and every key under A occurs under B. This is set semantics, so one copy in B covers repeats in A.
- **Same and overlap.** A and B are the `same` when each is inside the other. Otherwise A overlaps B by matched bytes over A's bytes, when that share is at least 10%.
- **Algorithm.**
  - For each content, record the sorted `pre` numbers of its occurrences. A key is under B when one occurrence's `pre` falls in `B.pre..B.post` (binary search).
  - Candidate partners for A are the folders on the ancestor chains of the other occurrences of A's three largest keys, minus A's own ancestors and descendants.
  - Exact matched bytes are computed for each candidate, and the best partner wins: highest matched bytes, then (apply-time refinement) a folder holding more than A before one that is the same as A, then the deepest folder. Two copies of one original are each reported inside the original, not paired with each other.
- **Cost.** Work is bounded by the sum of files over folders, which is the files times the mean depth, times 3 candidates, times log k. Folders with fewer bytes than `copies.report_min_bytes` are skipped.
- **Provisional pass.** It treats each unread small file's size as its key. It only selects candidates and never writes results.
- Rejected:
  - Multiset or path-aware relations. They answer "is this the same tree", which is M6 (§11.2). The owner's question is "is everything here already over there".
  - All-pairs comparison. The cost is quadratic in folders (§11.2: "use indexed candidates").

### D12. Ranking and maximality

- **Freeable bytes** are the bytes of A whose content also occurs under B as a different inode. Results are ranked by them. `same` results are reported once, with the folder whose path sorts first as the copy.
- **Reporting floor.** A folder result is omitted when its matched bytes are below `copies.report_min_bytes`. Matched bytes, not freeable bytes, are compared, so a hard-linked copy that frees nothing is still reported, with 0 freeable bytes (spec "Hard-linked copy frees nothing"). (Apply-time fix: the spec text first said "freeing less than", which contradicted that scenario.)
- **Maximality.** Results are emitted top-down. A folder is skipped when an ancestor's emitted result names the same partner or one of the partner's ancestors.
- **File results.** A file at or above `copies.report_min_bytes` with a copy outside its own folder, not explained by an emitted folder result, gets a `file` result naming its largest-path-order other copy.
- **Limits.** At most 10,000 results per search. A search that hits the limit records it in `stop_reason` detail, keeping the highest-ranked results.
- Rejected: listing every subfolder. The ranked list would repeat one copy hundreds of times, which is the noise the owner rejected.

### D13. Freshness at read time (§5.5.7)

- A result is `source_changed` when the source epoch differs from the search's, and `superseded` when a newer complete or partial search of the source exists.
- Otherwise it is `changed` when, for either of its folders or files, any of these holds:
  - a linked `copy_dirs` or `copy_files` row in the folder's `pre..post` range points to a node that is inactive, or whose observation revision differs from the recorded one;
  - the nearest linked ancestor of the folder (its atomic unit, or its own node) changed its observation revision or dirty version, or became inactive.
- Otherwise it is `current`.
- The check runs per displayed result and in the evidence check (D17). It uses the partial index on linked rows by `(search_id, pre)`.
- Rejected:
  - Triggers or listing hooks that mark results stale. They would couple discovery to `compare` for a value that can be read when needed.
  - Freshness by time ("older than N days"). It would claim a change that never happened, or miss one that did.

### D14. Retention and supersession

- When a search ends `complete` or `partial`, every older search of the source becomes `superseded`.
- The snapshot rows of older searches and older attempts are deleted in batches of 5,000 rows, one transaction each, with a `Yield` between them. They are deleted at the end of the new search's job, and at the start of the next search of the source if a crash cut that short.
- Search and result rows are kept. Results carry denormalized paths (raw bytes) and node IDs, so history renders without the snapshot.
- Rejected:
  - Keeping every snapshot. The surveyed archive yields about 1.3 million file rows per search.
  - Per-scope retention. Two overlapping scopes would show two truths for one folder.

### D15. Restart and cancellation (§8.4)

- **Restart.** An interrupted attempt is re-queued by the runner. The new attempt increments `attempt`, lists again, and takes every digest committed before the interruption from the cache.
- **Cancel during listing.** The search ends `cancelled`, with no results.
- **Cancel after listing.** The handler finishes the current file (or abandons it, if mid-read) and runs the final relate with `context.WithoutCancel`. Unchecked files are gaps, and the search ends `partial` with stop reason `cancelled`. The job ends `cancelled`, as the runner maps it.
- **Source epoch change.** The search ends `failed` with `source_epoch_changed`, and its results stay history.
- Rejected: durable traversal state for resuming a listing. A listing costs minutes, and the cache already preserves the expensive part.

### D16. Fairness (§8.4, A36)

- `copy_search` is registered as `ClassBulk`.
- **Listing** yields after each listing batch.
- **Hashing** yields after every `copies.yield_bytes` read, after each committed batch of at most 64 files, and between sample groups.
- **Waiting.** A yielded job may hold the current file's handle and one directory chain open while it waits.
- Rejected: a separate class for hashing. It would need a new credit and change the 4:2:1 contract that A36 tests.

### D17. Evidence on marks (§10.1, §4.2)

- **The request.** A `set-disposition` item may carry `copy_result_id`, only with `cleanup_candidate`.
- **The check.** Inside the command's transaction, before `SetDisposition`, `compare.CheckEvidence` requires:
  - the result exists;
  - its relation is `inside`, `same`, or `file`;
  - the item's node is the result's copy side, or either side of a `same` or `file` result;
  - the side is a node, not a path inside a unit;
  - D13 reads `current`.
- **Failures.** `invalid_request` for the relation, side, or unit cases; `not_found` for an unknown result; `stale_evidence` when not `current`.
  - Single item: the error is the response.
  - Bulk: the item is reported `stale_evidence` (or `not_found`, or the request fails `invalid_request`, since that is a malformed body), and the other items proceed.
- **Recording.** After `SetDisposition`, `compare.RecordEvidence` inserts the evidence for every item applied with a result. `intent.SetDisposition` itself ends every open `disposition_evidence` row of each applied node first, so any later disposition change ends the evidence.
- **Audit.** The `disposition_set` audit detail gains `copy_result_id` when present.
- Rejected:
  - A separate `mark-copy` command. It would duplicate the protection and inbox gates, audit, and bulk semantics of `set-disposition`.
  - Binding evidence to the intent revision. Unrelated intent changes (an override, a pin elsewhere in the path) would silently drop it.

### D18. The marked queue (§9.1)

- `inventory.QueueMarked = "marked"` selects active nodes of any kind with disposition `cleanup_candidate`, keyed by node ID like the other queues. It joins the open evidence row.
- The review page shows the evidence's relation, other path, and matched bytes, plus the freshness, which the explorer computes through `compare.ResultFreshness`. The default bulk disposition on this tab is `unreviewed` (unmark).
- Rejected: a filter on the explorer. The owner's cleanup list belongs with the other review lists, and M5 plans will start from it.

### D19. Pages (§9.1)

- **`/copies`.** Per source: the latest searches, a "Find copies in the whole source" control, and the running search's progress (live `reload` mode).
- **`/copies/{id}`.** The search summary, gaps with up to 20 examples, and the ranked results, 100 per page. Each result has a mark button (`data-command="set-disposition"` with `data-copy-result-id`) when its copy side is a node. Otherwise it shows a hint linking the nearest linked ancestor: "refine `X`" when that ancestor is atomic, or "refresh `X`" when it is expanded and the copy appeared after its last listing.
- **Inspector.** A Copies section for directories and files that appear in the latest search, plus a "Find copies in this folder" control on directories.
- **Header.** A `Copies` link between Inboxes and Classifier.
- **Wording.** No page says "only copy". Distinct files are "no other copy in this search". Pages state that changes deep inside atomic folders are seen only by searching again.

### D20. Configuration (§5.1)

`[copies]`:

| Key | Default | Range |
|---|---|---|
| `large_file_bytes` | 1 MiB | 4 KiB to 1 GiB |
| `sample_from_bytes` | 16 MiB | at least `large_file_bytes`, at most 1 TiB |
| `read_chunk_bytes` | 1 MiB | 64 KiB to 16 MiB |
| `yield_bytes` | 64 MiB | at least `read_chunk_bytes`, at most 4 GiB |
| `entry_budget` | 5,000,000 | 1 to 100,000,000 |
| `report_min_bytes` | 10,000,000 | 0 to 1 TiB |

- Fixed in code: three 64 KiB samples, a 50% candidate share, a 10% overlap floor, 10,000 results, and 64 files per hash commit. Each is documented.
- Rejected: configurable shares and sample counts. They change the meaning of results, not just their cost.

### D21. Migration `0006_find_copies.sql` (§8.3)

- New tables: `copy_searches`, `copy_search_roots`, `copy_dirs`, `copy_files`, `contents`, `file_digests`, `copy_results`, and `disposition_evidence`. Nothing existing is altered.
- `nodes` stays untouched. The change time lives only in snapshot and cache rows.
- Rejected: a `nodes.ctime_ns` column. Discovery does not need it, and adding it would make every listing write a column it never reads.

### D22. Error codes (§9.3)

- No new code.
  - Folder refusals are `invalid_node_state`.
  - Evidence that is no longer current is `stale_evidence`, which already exists and maps to 409.
- The bulk outcome `stale_evidence` is new: `intent.OutcomeStaleEvidence`.

### D23. Scope choices confirmed with the owner (explore session, 2026-10-03)

- Folders before archives, with archives next (ADR 0006).
- Precious finds overlapping folders itself, in one owner-started job, instead of comparing pairs the owner names.
- A result can mark its copy as `cleanup_candidate` with evidence. Nothing on disk changes.
- Defaults the owner accepted:
  - content-based "inside";
  - one line per copy;
  - kept results with "changed since checked";
  - big files first, small files only between likely copies;
  - a fresh byte check before any removal (M5).

## Interfaces

### Schema (`0006_find_copies.sql`)

```sql
CREATE TABLE copy_searches (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id),
  job_id INTEGER REFERENCES jobs(id),
  source_epoch INTEGER NOT NULL,
  attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  state TEXT NOT NULL CHECK (state IN ('queued', 'listing', 'checking', 'comparing',
    'complete', 'partial', 'failed', 'cancelled')),
  stop_reason TEXT CHECK (stop_reason IN ('entry_budget', 'cancelled', 'result_limit',
    'source_unavailable', 'source_epoch_changed', 'error')),
  detail TEXT,
  counts TEXT NOT NULL DEFAULT '{}',          -- JSON, see below
  freeable_bytes INTEGER NOT NULL DEFAULT 0,
  superseded_by INTEGER REFERENCES copy_searches(id),
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER
);
CREATE INDEX copy_searches_source ON copy_searches (source_id, id);

CREATE TABLE copy_search_roots (
  search_id INTEGER NOT NULL REFERENCES copy_searches(id),
  node_id INTEGER NOT NULL REFERENCES nodes(id),
  path BLOB NOT NULL,                          -- raw source-relative path at request time
  PRIMARY KEY (search_id, node_id)
);

CREATE TABLE copy_dirs (
  id INTEGER PRIMARY KEY,
  search_id INTEGER NOT NULL REFERENCES copy_searches(id),
  attempt INTEGER NOT NULL,
  parent_id INTEGER REFERENCES copy_dirs(id), -- NULL for a search root
  root_node_id INTEGER NOT NULL REFERENCES nodes(id),
  name BLOB NOT NULL,
  pre INTEGER NOT NULL,
  post INTEGER,                                -- NULL until the directory is left
  state TEXT NOT NULL CHECK (state IN ('listed', 'mount_boundary', 'unreadable',
    'listing_failed', 'budget')),
  dev INTEGER, ino INTEGER,
  node_id INTEGER REFERENCES nodes(id),        -- linked active node, else NULL
  node_revision INTEGER,                       -- its observation_revision at listing
  node_dirty INTEGER                           -- its dirty_version at listing
);
CREATE UNIQUE INDEX copy_dirs_pre ON copy_dirs (search_id, attempt, pre);
CREATE INDEX copy_dirs_linked ON copy_dirs (search_id, attempt, pre) WHERE node_id IS NOT NULL;

CREATE TABLE copy_files (
  id INTEGER PRIMARY KEY,
  search_id INTEGER NOT NULL REFERENCES copy_searches(id),
  attempt INTEGER NOT NULL,
  dir_id INTEGER NOT NULL REFERENCES copy_dirs(id),
  pre INTEGER NOT NULL,                        -- the directory's pre
  name BLOB NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('file', 'symlink', 'special')),
  size INTEGER, mtime_ns INTEGER, ctime_ns INTEGER,
  dev INTEGER, ino INTEGER, nlink INTEGER,
  link_text BLOB,
  node_id INTEGER REFERENCES nodes(id),
  node_revision INTEGER,
  check_state TEXT NOT NULL CHECK (check_state IN ('pending', 'empty', 'distinct_size',
    'distinct_sample', 'hashed', 'cached', 'unstable', 'unreadable', 'not_checked',
    'link', 'special', 'mount_boundary')),
  content_id INTEGER REFERENCES contents(id),
  sample_digest BLOB CHECK (sample_digest IS NULL OR length(sample_digest) = 32),
  detail TEXT,
  CHECK ((content_id IS NOT NULL) = (check_state IN ('hashed', 'cached')))
);
CREATE INDEX copy_files_size ON copy_files (search_id, attempt, size) WHERE kind = 'file';
CREATE INDEX copy_files_pre ON copy_files (search_id, attempt, pre);
CREATE INDEX copy_files_linked ON copy_files (search_id, attempt, pre) WHERE node_id IS NOT NULL;

CREATE TABLE contents (
  id INTEGER PRIMARY KEY,
  algorithm TEXT NOT NULL CHECK (algorithm IN ('sha256')),
  digest BLOB NOT NULL CHECK (length(digest) = 32),
  size INTEGER NOT NULL CHECK (size >= 0)
);
CREATE UNIQUE INDEX contents_digest ON contents (algorithm, digest, size);

CREATE TABLE file_digests (
  source_id TEXT NOT NULL REFERENCES sources(id),
  source_epoch INTEGER NOT NULL,
  dev INTEGER NOT NULL, ino INTEGER NOT NULL,
  size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, ctime_ns INTEGER NOT NULL,
  content_id INTEGER NOT NULL REFERENCES contents(id),
  verified_at INTEGER NOT NULL,
  PRIMARY KEY (source_id, source_epoch, dev, ino)
);

CREATE TABLE copy_results (
  id INTEGER PRIMARY KEY,
  search_id INTEGER NOT NULL REFERENCES copy_searches(id),
  rank INTEGER NOT NULL,
  relation TEXT NOT NULL CHECK (relation IN ('inside', 'same', 'overlap', 'file')),
  copy_path BLOB NOT NULL, copy_kind TEXT NOT NULL CHECK (copy_kind IN ('directory', 'file')),
  copy_node_id INTEGER REFERENCES nodes(id),   -- the copy itself when cataloged
  copy_unit_id INTEGER REFERENCES nodes(id),   -- nearest linked ancestor when not cataloged
  copy_dir_pre INTEGER, copy_dir_post INTEGER, -- snapshot range (NULL once pruned)
  other_path BLOB NOT NULL, other_kind TEXT NOT NULL CHECK (other_kind IN ('directory', 'file')),
  other_node_id INTEGER REFERENCES nodes(id),
  other_unit_id INTEGER REFERENCES nodes(id),
  other_dir_pre INTEGER, other_dir_post INTEGER,
  matched_bytes INTEGER NOT NULL, freeable_bytes INTEGER NOT NULL,
  matched_files INTEGER NOT NULL, total_bytes INTEGER NOT NULL, total_files INTEGER NOT NULL,
  gap_files INTEGER NOT NULL, gap_bytes INTEGER NOT NULL,
  CHECK ((copy_node_id IS NULL) <> (copy_unit_id IS NULL))
);
CREATE UNIQUE INDEX copy_results_rank ON copy_results (search_id, rank);
CREATE INDEX copy_results_copy_node ON copy_results (copy_node_id) WHERE copy_node_id IS NOT NULL;
CREATE INDEX copy_results_other_node ON copy_results (other_node_id) WHERE other_node_id IS NOT NULL;

CREATE TABLE disposition_evidence (
  id INTEGER PRIMARY KEY,
  node_id INTEGER NOT NULL REFERENCES nodes(id),
  kind TEXT NOT NULL CHECK (kind IN ('copy')),
  copy_result_id INTEGER REFERENCES copy_results(id),
  detail TEXT NOT NULL,                        -- JSON, see below
  created_at INTEGER NOT NULL,
  ended_at INTEGER
);
CREATE UNIQUE INDEX disposition_evidence_open ON disposition_evidence (node_id) WHERE ended_at IS NULL;
```

The paths in `copy_results` and `copy_search_roots` are raw source-relative bytes. `pre..post` ranges are relative to the search's final attempt.

JSON columns that other slices read:
- `copy_searches.counts`, written by W, H, and E, and read by U and integration:
  - `{"entries", "dirs", "files", "symlinks", "bytes", "files_read", "bytes_read", "cache_hits", "candidates_large", "candidates_small"}`;
  - `{"gaps": {"mount_boundary", "unreadable", "special", "listing_failed", "unstable", "not_checked", "budget"}}`;
  - `{"gap_examples": [{"kind", "path_b64", "detail"}]}`, at most 20.
  - Keys absent before their phase read as 0.
- `disposition_evidence.detail`, written by M and read by `inventory` and U: `{"search_id": "7", "relation": "inside", "copy_path_b64": "…", "other_path_b64": "…", "other_node_id": "31" | null, "matched_bytes": 105000000000, "freeable_bytes": …, "matched_files": 6214, "total_bytes": …, "total_files": …, "checked_at": 1790000000000}`.

### Go signatures (foundation unless marked)

```go
// internal/domain
type CopySearchID int64 // String(); ParseCopySearchID(s) -> CodeNotFound on malformed
type CopyResultID int64 // String(); ParseCopyResultID(s)
type CopySearchState string // CopySearchQueued, Listing, Checking, Comparing, Complete, Partial, Failed, Cancelled
type FileCheck string       // CheckPending, CheckEmpty, CheckDistinctSize, CheckDistinctSample, CheckHashed,
                            // CheckCached, CheckUnstable, CheckUnreadable, CheckNotChecked, CheckLink,
                            // CheckSpecial, CheckMountBoundary
type CopyGap string         // GapMountBoundary, GapUnreadable, GapSpecial, GapListingFailed, GapUnstable,
                            // GapNotChecked, GapBudget
type CopyRelation string    // RelationInside, RelationSame, RelationOverlap, RelationFile
type CopyFreshness string   // FreshCurrent, FreshChanged, FreshSuperseded, FreshSourceChanged

// internal/fsaccess (slice FS; all three implementations)
type EntryInfo struct { /* existing fields */ Ctime time.Time } // st_ctim; zero when unknown
type File interface {
	Stat() (EntryInfo, error)                // fstat of the open handle
	ReadAt(p []byte, off int64) (int, error) // io.ReaderAt semantics; at most len(p)
	Close() error
}
// Dir gains: OpenFile(name []byte, expect EntryInfo) (File, error)  (design D5)
var ErrNotRegular error // pre-filesystem refusal, empty outcome
// internal/fsaccess/synthfs
func (n *Node) Content(b []byte) *Node   // explicit bytes; sets size
func (n *Node) Seed(s uint64) *Node      // generated bytes of the node's size; equal seed+size = equal bytes
func (n *Node) Patch(off int64, b []byte) *Node // in-place overwrite; advances ctime
func (n *Node) Ctime(t time.Time) *Node
// Default content: generated from the node's inode, so distinct files differ.
// internal/fsaccess/instrument
const OpOpenFile, OpReadAt, OpFileStat Op = "OpenFile", "ReadAt", "FileStat" // Close reuses OpClose
func (r *Recorder) BytesRead() int64

// internal/config (slice FC)
type Copies struct {
	LargeFileBytes  int64 `toml:"large_file_bytes"`
	SampleFromBytes int64 `toml:"sample_from_bytes"`
	ReadChunkBytes  int   `toml:"read_chunk_bytes"`
	YieldBytes      int64 `toml:"yield_bytes"`
	EntryBudget     int64 `toml:"entry_budget"`
	ReportMinBytes  int64 `toml:"report_min_bytes"`
}
// Config gains `Copies Copies toml:"copies"`.

// internal/intent
const OutcomeStaleEvidence Outcome = "stale_evidence"
// Slice M: SetDisposition ends open disposition_evidence rows of each applied node (no signature change).

// internal/inventory (slice M)
const QueueMarked Queue = "marked"
type DispositionEvidence struct { Kind string; Result domain.CopyResultID; Detail json.RawMessage; CreatedAt time.Time }
// NodeDetail gains Evidence *DispositionEvidence, filled by Review(QueueMarked) and Reader.View.

// internal/compare (foundation)
const KindCopySearch jobs.Kind = "copy_search"
const PayloadVersion = 1
type Payload struct { SearchID string `json:"search_id"` }
func ScopeKey(source domain.SourceID) string // "copies:<source>"
type Deps struct {
	Store     *store.Store
	Registry  sources.Registry
	Clock     clock.Clock
	Config    config.Copies
	ListBatch int // discovery.list_batch_size
	MaxRoots  int // discovery.page_size
	Logger    *slog.Logger
}
type Service struct{ /* unexported */ }
func New(d Deps) *Service // panics on missing Store, Registry, or Clock
type Queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}
type Search struct { ID domain.CopySearchID; Source domain.SourceID; Epoch int64; Attempt int64; Roots []Root; Job domain.JobID }
type Root struct { Node domain.NodeID; Path []byte }
type PreRange struct { Pre, Post int64 }
func LoadSearch(ctx context.Context, q Queryer, id domain.CopySearchID) (Search, error)
func ResultFreshness(ctx context.Context, q Queryer, id domain.CopyResultID) (domain.CopyFreshness, error)
func RangeFreshness(ctx context.Context, q Queryer, search domain.CopySearchID, r PreRange) (domain.CopyFreshness, error)
// Progress keys (int64): "phase" (1 listing, 2 large, 3 comparing, 4 small, 5 results), "entries",
// "files_to_check", "bytes_to_check", "files_checked", "bytes_read", "cache_hits", "unstable".

// slice W (list.go, start.go)
type Started struct { Search domain.CopySearchID; Job jobs.Record; Coalesced bool; Roots []Root }
func (s *Service) Start(ctx context.Context, tx *jobs.Tx, source domain.SourceID, folders []domain.NodeID) (Started, error)
type ListStop string // "", "entry_budget", "source_unavailable"
type Listed struct { Entries, Dirs, Files, Bytes int64; Stop ListStop }
func (s *Service) List(ctx context.Context, rt jobs.Runtime, sc sources.Scan, search *Search) (Listed, error)
// increments search.Attempt, sets state listing; returns ctx.Err() when cancelled

// slice H (hash.go)
type Selection struct { MinSize, MaxSize int64; Ranges []PreRange } // MaxSize 0 = no bound; nil Ranges = all
type Hashed struct { FilesRead, BytesRead, CacheHits, Unstable, Unreadable int64 }
func (s *Service) Prepare(ctx context.Context, search Search) (Hashed, error) // empty, distinct_size, cached
func (s *Service) Hash(ctx context.Context, rt jobs.Runtime, sc sources.Scan, search Search, sel Selection) (Hashed, error)
func (s *Service) Finish(ctx context.Context, search Search) (int64, error) // remaining pending -> not_checked

// slice E (relate.go, results.go)
type Snapshot struct{ /* unexported arrays */ }
func LoadSnapshot(ctx context.Context, q Queryer, search Search, provisional bool) (*Snapshot, error)
type Relation struct {
	Relation domain.CopyRelation
	Copy, Other Side
	MatchedBytes, FreeableBytes, MatchedFiles, TotalBytes, TotalFiles, GapFiles, GapBytes int64
}
type Side struct { Path []byte; Kind domain.NodeKind; Node, Unit domain.NodeID; Range *PreRange }
type RelateOptions struct { ReportMinBytes int64 }
func Relate(snap *Snapshot, opts RelateOptions) (rels []Relation, limited bool)
func Candidates(snap *Snapshot) []PreRange
func (s *Service) WriteResults(ctx context.Context, rt jobs.Runtime, search Search, rels []Relation,
	state domain.CopySearchState, stop string) error // also supersedes and prunes (D14)

// slice M (evidence.go)
type Evidence struct { Result domain.CopyResultID; Detail json.RawMessage }
func CheckEvidence(ctx context.Context, tx *sql.Tx, node domain.NodeID, result domain.CopyResultID) (Evidence, error)
func RecordEvidence(ctx context.Context, tx *sql.Tx, node domain.NodeID, ev Evidence, now time.Time) error

// slice U (reader.go)
type Reader struct{ /* unexported */ }
func NewReader(st *store.Store, clk clock.Clock) *Reader
func (r *Reader) Searches(ctx context.Context, source domain.SourceID, after *SearchCursor, limit int) (SearchPage, error)
func (r *Reader) Search(ctx context.Context, id domain.CopySearchID) (SearchDetail, error)
func (r *Reader) Results(ctx context.Context, id domain.CopySearchID, after *ResultCursor, limit int) (ResultPage, error)
func (r *Reader) NodeResults(ctx context.Context, node domain.NodeID) ([]ResultView, error)

// coordinator (handler.go)
func NewHandler(s *Service) jobs.Handler
```

### Commands

All commands keep the M1 envelope:
- an `Idempotency-Key`;
- strict JSON;
- `400 invalid_request` for malformed bodies;
- `404 not_found` and `404 unknown_source`;
- IDs as decimal strings.

- **`find-copies`** (W), single request.
  - Request: `{"source_id": "disk", "node_ids": ["12", "34"]}`. `node_ids` may be omitted or empty for the whole source; at most `discovery.page_size` entries; duplicates are `400`.
  - 202: `{"job_id": "51", "state": "queued", "coalesced": false, "search_id": "7", "roots": [{"node_id": "12", "path": "/local/fotos", "path_b64": "…"}]}`.
  - 409 `source_unconfigured` and 409 `invalid_node_state`, naming the node.
- **`set-disposition`** (M) items gain `"copy_result_id": "88"` (optional, omitted from `canonical()` when absent, so existing idempotency digests are unchanged).
  - Allowed only with `"disposition": "cleanup_candidate"`; otherwise `400 invalid_request`.
  - Single item: `404 not_found` (unknown result), `400 invalid_request` (wrong side, a unit path, or a relation `overlap`), `409 stale_evidence`.
  - Bulk:
    - the outcome enum adds `stale_evidence`, and an unknown result is the item outcome `not_found`;
    - a wrong-side or overlap result fails the whole request with `400`, since it is a malformed request, not a state race.

### Read endpoints (U)

- **`GET /api/copy-searches?source=&cursor=`**: `{"items": [SearchJSON], "next": cursor | null}`, newest first, 50 per page.
  - `SearchJSON`: `{"search_id", "source_id", "job_id", "state", "stop_reason", "detail", "roots": [{"node_id", "path", "path_b64"}], "created_at", "started_at", "finished_at", "counts": {…}, "gaps": {…}, "freeable_bytes", "result_count", "freshness": "current" | "superseded" | "source_changed"}`.
- **`GET /api/copy-searches/{id}`**: `SearchJSON` plus `"gap_examples": [{"kind", "path", "path_b64", "detail"}]`.
- **`GET /api/copy-searches/{id}/results?cursor=`**: `{"items": [ResultJSON], "next": cursor | null}`, by rank, 100 per page.
  - `ResultJSON`: `{"result_id", "rank", "relation", "copy": SideJSON, "other": SideJSON, "matched_bytes", "freeable_bytes", "matched_files", "total_bytes", "total_files", "gaps": {"files", "bytes"}, "freshness", "mark": {"node_id", "intent_revision", "disposition"} | null}`.
  - `SideJSON`: `{"path", "path_b64", "kind", "node_id" | null, "unit_node_id" | null}`.
  - `mark` is null when the copy side is not a node.
- **`GET /api/review?queue=marked`** (M): items are `nodeDetailJSON` plus `"evidence": {"kind": "copy", "result_id", "freshness", "detail": {…}} | null`.
- **Pages:** `/copies` and `/copies/{id}` (U, live `reload`), the inspector's Copies section (U), the review page's `marked` tab (M), and the header link (U).

## Transaction boundaries

| Path (transaction) | Re-checked inside the writing transaction | Another command commits just before | …just after |
|---|---|---|---|
| `find-copies` (`jobs.Tx`) | source configured; each folder active, a directory, not a mount boundary, in the source; `EnqueueOnce` on `copies:<source>`; the search row and roots inserted with the current epoch | a collapse deactivates a folder: `invalid_node_state`. Another search starts: coalesced | a collapse or replacement: the listing's chain open fails, and the folder is a `listing_failed` gap |
| Listing batch (`store.Write`) | source epoch equals the search's; the search attempt is current; linked node IDs are those read before the batch (a read, not a lock) | a scan changes a linked node: it is recorded with its new revision or not linked | the change makes D13 read `changed` |
| Hash commit (`store.Write`, ≤ 64 files) | source epoch; file rows belong to the current attempt; `contents` upsert by digest; a `file_digests` replace only from a stable read | epoch change: the search fails, nothing committed for the batch | a file rewritten after its read: the cache entry no longer matches (ctime), and the next search reads it |
| `WriteResults` (batches of results, then one final transaction) | source epoch; attempt; state not already terminal; in the final transaction, older searches superseded and the search state set | epoch change: `failed`, results kept as history | a new `find-copies`: it starts only after this search's job ends (single flight) |
| Prune (`store.Write`, ≤ 5,000 rows) | rows belong to superseded searches or older attempts | none matter: these rows are never read | a new search's rows carry a newer attempt or search ID |
| `set-disposition` with evidence (`jobs.Tx`) | `CheckEvidence`: result exists, relation, side, D13 `current`; the intent revision; protection; inbox lock; then `SetDisposition` ends old evidence and `RecordEvidence` inserts | a scan changes the copy: `stale_evidence`. A newer search finishes: superseded, so `stale_evidence` | the evidence is stored. The marked queue then shows the result's new freshness |
| Reads (`store.Read`) | one snapshot per request | n/a | n/a |

## Risks / Trade-offs

- **[A first search of a large archive takes hours]** (about 445 GB of same-size bytes on the surveyed archive). → Big files are checked first, progress is visible, a cancel after listing still yields results, and a restart resumes through the cache.
- **[Database growth.]** About 1.3 million snapshot rows (on the order of 200 MB with indexes) for the surveyed archive. → Only the latest search keeps its snapshot (D14). The upgrade note states the size.
- **[Memory for relations.]** About 50 bytes per file in arrays: about 70 MB for 1.3 million files. → Folders below `report_min_bytes` are skipped. The cost is documented with the entry budget.
- **[atime updates on writable mounts.]** → `O_NOATIME` when the process owns the files. Docs recommend read-only mounts, as for every other observation.
- **[Deep changes inside atomic units stay `current`]** (ADR 0005). → Pages state it. M5 re-verifies bytes before any removal.
- **[Filesystems without change times]** never serve from the cache. → Slower repeated searches, never wrong ones.
- **[The provisional share can miss a copy of a folder made of small files]** when less than half of it matches. → Its files stay `not_checked` gaps, so no claim is made. Only the overlap figure is lower than the truth.
- **[SHA-256 collisions]** are not a practical risk. → M5 compares bytes before acting anyway.

## Migration Plan

1. Back up with `curator backup`.
2. On first start, migration `0006` runs. It only adds tables. The M3a binary refuses the newer schema.
3. Nothing changes until the owner starts a copy search. Scans, probes, walks, and peeks still read no content.
4. **Rollback:** stop the server, restore the backup, and start the M3a binary.

## Addendum: decisions made during apply

Added at archive (2026-10-03). These decisions were made or confirmed while M4 was implemented, and the code on `master` follows them. None departs from `directory-first-curator-spec-v0.2.md`: the M4 departures are ADR 0005 (copy searches read inside atomic units) and ADR 0006 (folders first, archives in M4b), both recorded at proposal. D35 was checked against §11.1 and the hard-link rule of §5.5: names of one inode stay separate occurrences, and they free no bytes.

### Files, mounts, and configuration

**D24. Opening a file (refines D5).** `OpenFile` opens the entry with `O_PATH|O_NOFOLLOW` on its directory's descriptor and checks the identity with `fstat`. It then reopens through `/proc/self/fd/<n>` with `O_RDONLY|O_NONBLOCK|O_NOATIME`, retried without `O_NOATIME` on `EPERM`, and checks `fstat` again. A FIFO, socket, or device is therefore never opened at all. The `EPERM`-retry test is skipped when tests run as root; task 1.3 had that condition inverted.

**D25. A bind-mounted file is a mount boundary everywhere.** It reports `mount_boundary: true` in copy listings, probe descriptors, and peeks (A11). The pre-M4 synthfs expectation for a file on another device was flipped on purpose. Test-only additions: `instrument.Call.Off`/`Bytes`, synthfs `FailReadAfter`, and `Unreadable()` on files.

**D26. Configuration (refines D20, D12).** `copies.sample_from_bytes` is at least 1 MiB as well as at least `copies.large_file_bytes`, so three 64 KiB samples read at most about 19% of a file. `copies.report_min_bytes` compares **matched** bytes, not freeable bytes. The spec text "freeing less than" contradicted "Hard-linked copy frees nothing", and the spec and D12 were corrected.

### Listing and checking

**D27. Listing outcomes.**
- An `Lstat` failure other than "absent" records the entry as an `unreadable` gap with an unknown size. A symlink whose `Readlink` fails is an `unreadable` gap too.
- A directory cut by the entry budget is a `budget` gap, and its unlisted entries are absent. After a budget stop or a lost source, the folders not yet listed get root rows in state `budget` or `listing_failed`. `OpenDir` failing as `unavailable` while the root still answers is `listing_failed`.
- Symlink rows are linked to cataloged symlink nodes too, so a changed cataloged symlink makes freshness `changed`.
- Each listing attempt resets `copy_searches.counts`; `entries` counts every recorded row below the roots.

**D28. Checking details.**
- `copy_files.ctime_ns = 0` means "no change time", and such a file is never served from the cache.
- `Prepare`, `Hash`, and `Finish` take the job's `Runtime` so their batched writes can yield; `Prepare` and `Finish` write at most `PruneBatch` rows per transaction. Counts describe the final attempt.
- Sampling is skipped for a size group that already has a known content.
- A failure to open a file's directory chain or the file itself applies to that row only, since another link may still be readable. A failure seen on the open file, and every success, apply to every pending row of the inode.
- The end-of-file check rides on the last short chunk, else one 1-byte read at the recorded size.
- `Hash` returns `source_unavailable` without marking the source; the handler marks it. A root whose catalog chain ends at another identity than the snapshot's root row counts as changed.

### Relations and results

**D29. Result sides.** A file side stores its directory's `pre` in `*_dir_pre` and NULL in `*_dir_post` (`Side.Range` is nil and `Side.DirPre` carries the directory). Freshness checks the file's own row, found by `pre` and base name, and the nearest linked ancestor-or-self of its directory, so a sibling's change does not mark it `changed`.

**D30. Which side is the copy (refines D12).** In a `same` pair, the cataloged (markable) side is the copy when only one side is a node; otherwise the path-later side is the copy and the path-first side the reference. A `file` result follows the same rule: per content, the path-first occurrence is the reference, and every other occurrence of at least `report_min_bytes` that no emitted folder result explains gets its own result naming it.

**D31. Partner choice (refines D11).** A folder's best partner has the most matched bytes, then the most matched entries. Next comes a folder that holds more than the copy before one that is the same as it. Then the deepest, then the path-first. Without the third rule, two copies of one original were paired with each other and never reported inside the original; the binary smoke found this on the surveyed-archive shape. Pathological ties are bounded (64 tops per key, 16 chains): the bounds change only which deepest partner is named, never whether a claim is true.

**D32. One line per copy (refines D12 maximality).**
- Sides are named by their highest equivalent folder: a folder whose ancestors up to it hold nothing else.
- Strong relations (`inside`, `same`) are decided before overlaps, shallowest pair first. An overlap holds back only overlaps, so a part of an overlapping folder that is entirely inside the partner keeps its own line. Without this, an ancestor's overlap hid a descendant's `same`.
- A candidate is also skipped when an emitted pair holds it in reverse orientation; the exact reverse of an emitted pair (the larger folder's overlap line) stays.
- Emitted strong results join their folders into groups (union-find). A candidate is held when an ancestor-or-self of each side belongs to one group. So two folders related through a third are not reported subfolder by subfolder. The binary smoke found this defect.
- At equal bytes, `inside`, `same`, and `file` rank before `overlap`. Ranks are 1-based.

**D33. Freeable bytes.** `copy_searches.freeable_bytes` adds the `inside`, `same`, and `file` results, counting each file once. A duplicate under the other side of an emitted folder result is explained by it and not counted again.

**D34. Relate and candidates.** `Candidates(snap, opts)` applies the `report_min_bytes` floor to the folder it starts from. When `Relate` reports that it hit the result limit, the search ends `partial` with stop reason `result_limit`. `WriteResults` recounts gaps just before its final transaction. A provisional pending file never matches a cached copy. `Prune` and `StopResultLimit` are exported; a prune error after the final commit is logged, not returned.

**D35. Hard links without a read (refines D10, D11).** A file that is `distinct_size` or `distinct_sample` and has more than one link is keyed by its device and inode. Its names then match each other without any read, the match counts toward `inside`, and it is never freeable. Before this, a folder of hard links to files whose size no other inode shares never matched, and "Hard-linked copy frees nothing" failed end to end. The relate unit test had set those files up as `hashed`, which the checking step never produces. Raw keys now carry their kind in three bits.

### Marks, pages, and the handler

**D36. Marks with evidence.**
- `intent.DispositionRequest.Refused` is added: items refused by an evidence check keep their position, and the request keeps bulk semantics.
- A result whose search has not finished is `not_found`.
- A copy held by an expanded directory gets "not cataloged yet; refresh the folder and search again", because refine is invalid there.
- `set-disposition` uses its own operation type, since it needs the transaction's time.
- The inspector's evidence line gets its freshness from the explorer. Two inventory tests that pinned the three-queue list now include `marked`.

**D37. Pages.**
- `/copies/{id}` shows the mark button only when the result is markable now: the copy is a node, the relation can back evidence, and the result is `current`. The API's `mark` is non-null whenever the copy is a node.
- `/copies` lists the source's other active jobs, so their events update in place instead of reloading the page.
- The inspector's Copies section shows at most 100 results by rank. Its hint is "refine" for a copy inside an atomic unit and "refresh" otherwise.
- Cursor version bytes are `0x53` for searches and `0x54` for results.

**D38. Owner cancel and shutdown (refines D15).** The handler tells an owner's cancel from a shutdown by `jobs.cancel_requested`. An owner cancel after listing publishes the results found so far (`partial`, stop `cancelled`), and that wrap-up does not yield. A shutdown or a lost lease leaves the search to its next attempt. `Prune` runs after each listing, so a prune that a crash cut short is finished.

### Tests

**D39. Scenario timing.** With the M4 scenarios, the scenario package took 61–66 s under `-race`. SQLite's transpiled C library guards `malloc` with one process-wide mutex, so the package's parallel tests serialize on it (87% of mutex contention), and more parallelism does not help. The harness now prepares its `quiesce` query once per process. Property seeds 3 and 4 moved to the `slow` tag (D21's convention), so the default run keeps seeds 1 and 2. The package runs in 55–58 s.

**D40. Deterministic scenarios.**
- A deferred job that falls due only because the clock moved does not bump the runner's queue generation, so a yielding job sees it at the runner's next tick. That is existing runner behavior: harmless in production (1 s tick, 5 s inbox poll), but racy in a gated test with a 10 ms tick. "Inbox work during a copy search" therefore wakes the intake job with `Tx.WakeOnce`, as a notification does. `discovery.WakeIntake` leaves an active deferred job alone by design.
- synthfs does not move a directory's modification time when a file is added, so tests that rely on it set it.
- Fixture choices: the small-file copy test pairs file sizes only within one folder, so the provisional size key does not pair folders by accident. The restart test uses enough pairs that a 64-file batch commits before the kill.
- The relate test fixture writes `nlink` as the number of names that share an inode.
