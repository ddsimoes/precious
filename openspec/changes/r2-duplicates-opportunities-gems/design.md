# Design

## Context

See `proposal.md` for why. R1 left these facts that shape R2:

- **The index.** `entries` holds `size`, `mtime_ns`, `ctime_ns`, `dev`, and `ino`, but no content data. The scanner never opens a file, and a test enforces it, so hashing is separate work. The scanner writes a row only when it changed (R1 D7), and its change test ignores ctime.
- **Measured limits.**
  - Scanning is CPU-bound in SQLite: 35–55 µs per entry (R1 A13). Each `entries` index adds to that cost, and R1.14 passed cold with a ratio of 1.23 (A15).
  - The database takes about 780 B per entry.
  - The reference server is a 4-core Celeron with 4 GB, on a RAIDZ1 of hard disks.
- **The `curator-m4b` engine (git tag) solved most of R2 for per-search snapshots:**
  - archive reading with budgets (`compare/unpack`, pure, 1.3k LOC with tests);
  - hashing with samples, a digest cache, and identity-checked chunked reads (`compare/hash.go`, `chunks.go`, `members.go`);
  - folder relations over compact pre-order arrays (`compare/relate.go`, about 50 bytes per file).

  Its listing, searches, freshness, evidence, and reader are tied to snapshots and to v0.2 intent.
- **The runner** has device-bound classes (interactive, reconciliation, and bulk; bulk ages to interactive after 5 minutes), CPU pools without a device, single-flight `EnqueueOnce` by scope key, and `Yield` between work units (R1 D15).
- **The owner's archive** (ADR 0005–0007) holds 1.49 M entries and 780 GiB. About 1.15 M of its small files share a size. Its archives total 356 GB: 185 GB of tar.gz, 91 GB of zip, 36 GB of single-file gzip, and 22 GB of 7z or rar.

## Goals / Non-Goals

**Goals:**
- An R1 database upgrades in place. No rescan is needed.
- Hashing never blocks a scan, a page, or a decision, and duplicate figures appear while it runs.
- Pages stay within the §12 targets at 2 million entries, R1.10 included, and R1.14 does not regress.
- Every uniqueness claim is exact about what was checked (I7).

**Non-Goals:**
- Searching archive members by name.
- Recomputing relations incrementally.
- Sampling archive members.
- Opening formats beyond the standard library.
- Any move or organizing action.
- Probabilities.
- A keeper concept (D2).

## Decisions

### D1. Code restored from `curator-m4b`, and package layout (§12)

- **`internal/archive`**
  - Restored from `compare/unpack`, with its tests and `testdata`, with the R2 domain types (D7). The behavior of formats, budgets, outcomes, path rules, and the one level of nesting is unchanged.
  - Its API is extended in two ways:
    - Zip members report their compression method, so that the viewer can serve a range of a stored member (D17).
    - Tar hard links name their target member.
- **`internal/content`**
  - Hashing jobs (D4, D5), restored from the core of `hash.go`, `chunks.go`, and `members.go`: samples, full reads, identity checks, hard links read once, and batched commits.
  - The listing of zip archives, restored from the streamed pass of `archive.go`.
  - Coverage queries and opening archive members.
- **`internal/relations`**
  - The snapshot, `partner`, `Relate`, `maximal`, `lift`, and `Candidates`, restored from `relate.go`. They now load their arrays from the index (D9).
  - The `relate` job.
  - Compare (D11).
- **`internal/review`:** opportunity cards, review rows, and Gems (D12–D14).
- **Not restored:** `list.go`, `start.go`, `search.go`, `state.go` (epochs and attempts), `freshness.go`, `evidence.go`, `results.go`, `reader.go`, and the v0.2 scenario harness.
- **Scrubbing.** Restored tests and fixtures pass the coordinator's personal-information scan. Owner-specific folder names in the old tests become neutral names, as they did in R1.
- **Rejected:**
  - Rewriting from scratch. The restored code carries tested budgets, such as a zip bomb stopping before 128 MiB, and edge cases, such as hard links, gaps, and lifted wrappers.
  - Restoring the snapshot pipeline. §16 asks for results over the index, with no separate snapshot.

### D2. No keeper: duplicates are information (§6.7, §11.6, R2.4; owner's decision, 2026-10-06)

- **What changes.**
  - The owner explores duplicates when they choose, and decides with the existing controls, one by one or in bulk.
  - Precious never picks a copy, never writes a suggestion from a duplicate, and never merges tags.
  - `precious-spec-v0.3.md` is updated: I4, §6.7, §10.1(4), §10.2, §10.5, §11.6, §11.7, and §14 (R2, the revised R2.4, and R3). The cleanup-time wording now reads "a copy that stays outside the plan", not "the keeper".
- **Bulk over copies** uses Search's new duplicate filter: "copies outside this folder" (D15), then select all, then the usual confirmation. The bulk path stays the R1 one, which skips keeps.
- **Rejected:**
  - A keeper that sets keep on the chosen copy. The owner did not want one action that does several things.
  - Keeper rules ("keep the copy under `/Fotos`"). The search filter plus select-all gives the same result, through a list the owner sees first.

### D3. Content state per file (§6.4, §7)

- **One row per present non-empty regular file**, in `file_content` (Interfaces):
  - `state` ∈ `unique_size`, `pending`, `sampled`, `hashed`, `changed`, `unreadable`;
  - the identity the last read observed;
  - an optional 64 KiB-sample digest;
  - `content_id` into `contents` (SHA-256, size).

  A side table keeps the scanner's 28 positional `entries` columns and its hot path untouched.
- **Planning** is a database-only pass, run at the start of every hashing job and serialized by a mutex in the service:
  - It groups present files and listed file members by size, counting hard links (same `dev` and `ino` on a source with stable identity) once.
  - It sets `unique_size` on singletons and `pending` on the others, keeping `hashed` and `sampled` rows whose group did not change.
  - A `sampled` file whose group gains a file with an equal sample goes back to `pending`.
  - The pass is a full `GROUP BY size`, about 1–2 s at 2 million rows. It replaces an `entries(size)` index, which would cost every scan write (R1.14).
- **Hard links** are read once. Their entries share the result, and they are one copy in groups and figures (m4b D10).
- **Rejected:**
  - Columns on `entries`. They are positional in the scanner, and an extra index slows R1.14.
  - Deriving "unique by size" on read. It needs that index.

### D4. Reading order and safety (§7, I1, I9)

- **Order within a job:**
  1. list the source's unlisted zip archives (central directory only);
  2. plan again if any listing added members;
  3. read files of at least 1 MiB and every tar-family archive, by size descending, one queue. Files of at least 16 MiB read three 64 KiB samples first (offsets 0, size/2 − 32 KiB, size − 64 KiB) and are read in full only on a sample collision;
  4. small files inside folder pairs that `relations.Candidates` finds on a provisional snapshot keyed by size (≥ 50% of matching bytes), in directory order;
  5. all remaining small candidates.
- **Reads:**
  - Every read uses `fsaccess.Dir.OpenFile` with the row's identity (`O_NOATIME`, swap detection), in `read_chunk_bytes` chunks, each wrapped in `rt.FSCall`.
  - A digest is kept only when every byte was read, `EOF` came at the size, and the closing `fstat` equals the expected identity. Otherwise the file becomes `changed`.
  - A permission or I/O failure makes the file `unreadable`.
- **Commits** hold at most 64 files and re-check each `entries` row (I9, Transaction boundaries).
- **`rt.Yield`** runs after each commit and every `yield_bytes` read, even inside a file.
- **Cancel:** the current file is abandoned, and committed work is kept.
- **The scanner** compares `ctime_ns` when both values are non-zero. An updated row deletes its `file_content` and `archives` rows in the same batch (one prepared statement each, only on updates), so a cached digest is valid exactly while its row exists.
- **Rejected:**
  - Hashing every candidate strictly by size. Folder relations would wait for all of the 1.15 M small files.
  - Keeping digests across changes by comparing keys at read time. The scanner ignored ctime, so a restored mtime would hide a change (m4b `TestRestoredModificationTimeDoesNotHideAChange`).

### D5. Jobs and triggers (§7, §12)

| Kind | Class or pool | Single flight | Started by |
|---|---|---|---|
| `hash` | `ClassBulk` (device) | scope `hash:<source>` | after each successful scan, for every online source, through the index hook; at server start for every online source; `start-hash` |
| `hash_now` | `ClassInteractive` (device) | scope `hash_now:<source>`, coalescing the subtrees | `check-now` |
| `relate` | pool `relate`, capacity 1 | scope `relate` | `hash` checkpoints (every `duplicates.refresh_interval`) and ends; after each scan; at start when dirty |

- **Planning in each job.** A hashing job for a source with no work plans and ends in seconds. Enqueuing every online source after any scan is how sizes shared across sources are found (§7).
- **Dirty relations.** `relate` reads `review_state.dirty` at its start and, when it ends, runs again if a refresh was requested meanwhile. `EnqueueOnce` coalesces into a running job, so without this flag a refresh requested during a run would be lost.
- **The index hook.** The scanner takes `index.Handler.OnScanDone(func(ctx, domain.SourceID))`, which `serve` wires to `content.AfterScan`, so `index` imports neither `content` nor `relations`.
- **Rejected:**
  - A separate global planning job. It would need a job to wait for another.
  - Relations inside the hashing job. That is CPU work holding a device slot, and two sources' jobs would each recompute.

### D6. Late results and generations (I9, §7)

- **A hashing commit** re-checks, inside its transaction, that the entry is present with the size, `mtime_ns`, `ctime_ns`, and `ino` the read observed, and that its `file_content` row still exists. Otherwise the result is dropped.
- **An archive listing** is written as `archives.state = 'listing'` with the identity first, then members in batches. Each batch re-checks the row, and the last batch flips it to `complete`. Readers ignore `listing`. A scanner update deletes the row, and the next batch abandons the archive.
- **The `relate` job** builds generation `g+1` of `relations` and `review_rows` in batches, inserting only rows whose entries still exist. It then flips `review_state.gen` in one transaction, so readers never see two generations, and deletes `g` in batches. `dir_dups` is upserted in place, writing only changed rows: a reader may see a mix for a moment, and the figures converge.
- **Decisions are never stored in derived rows.** Open rows and decided states are joined live (D12), so a decision is visible at once.
- **Rejected:** locking out scans while relations are computed. A relate pass takes minutes at 2 million entries.

### D7. Archives as read-only entries (§6.4, §11.2, §11.12, ADR 0007)

- **Which archives are opened.**
  - zip, tar, tar.gz/tgz, tar.bz2/tbz/tbz2, `.gz`, and `.bz2`, by name rule and confirmed by signature (m4b `Classify`).
  - Not opened: other formats, encrypted zips, and archives inside archives. Office and `jar` files stay plain files.
- **Zip.** Only its central directory is read at listing. A member is hashed later, by member size, when its size is shared (`archive_members.state` as in D3, never sampled).
- **The tar family** is streamed once, in size order with the large files (D4). Every file member is hashed in that pass, and the archive file's own digest is kept when the whole file was read with an unchanged identity.
- **Budgets** come from `[archives]` (server-config). Reaching one leaves the archive `partial` with no members, and it is never reused (m4b D6).
- **Members** live in `archive_members`, never in `entries`. Their totals are computed at listing, and folder aggregates count only the archive file's packed size, so nothing is counted twice.
- **Addressing.** The API addresses a member as `m<id>` (`domain.Ref`), never by path (I6).
- **Decisions and tags.** Members have neither: they read the archive's effective decision (owner-intent).
- **Rejected:**
  - Members as `entries` rows. The kind `CHECK`, the totals, the FTS index, and the decisions would all need exceptions.
  - Search over members. That is 2 million more FTS rows on the owner's archive, and the spec does not ask for it.

### D8. Duplicate groups and coverage definitions (§6.4, I7)

- **A group** is a `content_id` with at least two physical copies among present files and complete-archive members, on any source, offline sources included.
  - A copy is one entry, or one hard-link set.
  - A member is a copy; its archive is not.
- **Redundant bytes** = size × (copies − 1).
- **Coverage**, per source and in total:
  - **candidate:** every row except `unique_size`;
  - **checked:** `hashed`, plus `sampled`, meaning a distinct sample within its size;
  - **not checked:** `pending` and `changed`;
  - **unreadable:** `unreadable`.

  Coverage is kept in `content_coverage` and updated by deltas inside each hashing commit. Planning recomputes it, which repairs any drift from scanner deletions.
- **A "no other copy" claim** is shown only for `unique_size`, for `sampled`, or for `hashed` with a single copy. It always carries the global checked share, because a copy can be on any source. Unopened archives (7z, rar, partial) count as plain files, and the operator docs say so.

### D9. Relations over the index (§6.4, §7)

- **The snapshot.** The `relate` job loads the present entries' `id`, `parent_id`, `kind`, `size`, and content key, and complete archives' members, for every source in one read transaction. It builds m4b's pre-order arrays in memory (directories by DFS over `parent_id`), with archives spliced in as folders.
- **Keys:**
  - `hashed` and `sampled` rows give content keys (`sampled` is unique);
  - `unique_size` is unique;
  - `pending`, `changed`, `unreadable`, an unreadable folder, and a mount boundary are gaps;
  - symlinks match by link text;
  - empty files are ignored.
- **The algorithm** is m4b's, unchanged: `partner`, `Relate`, `maximal`, `lift`, and freeable bytes (renamed redundant bytes).
  - `overlap` needs at least 50% of one side's bytes ("a large share", §6.4). m4b used 10% for searches the owner started.
  - There is no reporting floor: ranking by bytes keeps small relations at the bottom.
- **What is stored.** Each relation stores its sides, kind, bytes, and only-one-side counts. Compare (D11) computes the file lists on request.
- **Sides of a relation:**
  - `inside`: `a` is the contained side;
  - `same`: `a` is the archive side, otherwise the later path;
  - `overlap`: `a` is the side with the larger matched share.
- **Rejected:**
  - Storing per-file only-one-side lists. They would mean millions of rows that Compare recomputes cheaply.
  - A byte floor (m4b: 10 MB). It would hide the corpus's folders, and the slow test bounds the cost without one (D20).

### D10. Folder duplication figures (§6.3, §11.2)

- **`dir_dups(entry_id, candidate_bytes, checked_bytes, duplicated_bytes, duplicated_files)`.**
  - It is computed bottom-up by the `relate` job from the same snapshot.
  - `duplicated_bytes` sums the files in the subtree that have another copy anywhere.
  - Children and the treemap `LEFT JOIN` it by primary key.
- **A file's percent duplicated** is 0 or 100, from its row.
- **Member folders** inside an archive compute theirs on read, because archives are small next to the index.
- **No sort by percent duplicated.** The spec asks for a column and a coloring. A sort needs a fifth child index.
- **Treemap color bands:** 0%, < 25%, < 50%, < 75%, ≥ 75%, and not checked (`checked_bytes < candidate_bytes`).

### D11. Compare (§11.6)

- **Sides.** Two refs, each a folder, an archive entry, or a member folder, where neither contains the other. Otherwise the response is `400 invalid_request`.
- **The computation** runs per request in one read transaction, loading both sides' files with their relative paths, sizes, and content states.
- **Buckets:**
  - **identical:** the content occurs on both sides;
  - **different:** the same relative path with different content;
  - **only on one side:** the content does not occur on the other side and is not in different;
  - **unchecked:** a `pending`, `changed`, or `unreadable` file whose size occurs on the other side.

  A size missing from the other side proves "only here" without hashing.
- **Wrapper folders.** When exactly one side has a single top folder and its contents align better with the other side, that wrapper is dropped from the relative paths (m4b `lift`). For example, `emule-0.47c/` inside the zip lines up with the unpacked folder.
- **"Check now"** calls `check-now` with both refs.
- **[target]** Two sides of 100,000 files each answer within 2 s on the development machine (slow test).

### D12. Opportunity cards (§11.4, R2.5)

| Card | Rows | Bytes | Basis |
|---|---|---|---|
| `duplicates` | `relations` of kind `same` or `inside` (after `maximal`), plus duplicate groups with a copy outside every listed relation | redundant bytes | same content |
| `unpacked_archives` | archives that are side `a` of a `same` or `inside` relation with a folder | the archive file's size | same content |
| `system_junk` | outermost entries of category `system_junk` | `total_bytes` | rules |
| `installers` | `installer_download` and `download_collection` | `total_bytes` | rules |
| `programs` | `application_installation` and `os_installation` | `total_bytes` | rules |
| `caches` | `cache`, `temporary_data`, and `generated_artifacts`, except rule `partial_download` | `total_bytes` | rules |
| `leftovers` | rule `partial_download`, empty folders (no files, not unreadable, not a mount boundary), and zero-byte files | `total_bytes` | rules |

- **"Outermost"** means no ancestor is a row of the same card, so no byte counts twice in one card. Different cards may overlap.
- **Open rows.** An entry row is open while its `eff_decision` is `undecided`. A duplicates row is open while at least two of its copies are undecided (D2).
- **Storage.** `review_rows` stores rows per generation. For duplicates rows, `review_row_sources` maps a row to every source holding one of its copies.
- **A card** = `SUM(bytes)` over its open rows, the same SQL as the list's filter, so R2.5 holds by construction and is still tested end to end.
- **Ranking** is by bytes.
- **Rejected:** counting decided rows in cards. The cards answer "what is left to look at".

### D13. Review lists (§11.5)

- **Summary line.** The client builds it from structured fields through translation keys: category, oldest and newest year, files, bytes, and up to two notable signals (`contains_vcs`, `database_present`, an indicator).
- **Keys:** `K` keep, `D` discard, `L` later, `J` or ↓ next, `↑` previous, and `Enter` to open the detail panel. They are ignored in inputs, selects, and dialogs, as the R1 Escape handler is.
- **Duplicates rows** expand into their copies, each with the R1 decision controls. The keys act on the focused copy.
- **Select all** uses `select-list` (Interfaces), which resolves the open rows' entry IDs into an R1 selection. The R1 confirmation and bulk report follow. It is refused for `duplicates`.

### D14. Gems (§11.7, R2.6)

- **`gems_unique`:** files of kind image, video, audio, or document whose `FileFamily` is personal, outside any group of family programs or disposable, and whose content state is unique (D8). Ordered by `mtime_ns` ascending.
- **`gems_rescue`:** the `dir_stats.indicators` of groups of family programs or disposable, with their copy state.
- **`gems_only_in_copy`:** the only-one-side files of `overlap` relations that have no other copy, grouped by relation.
- **Coverage.** Each section states the global checked share. Not-checked files are counted, never listed.

### D15. Search's duplicate filter (§11.3)

- **`dup=copies|elsewhere|unique|unchecked`**, repeatable like the other list filters.
  - `elsewhere` needs `within`, otherwise `400 invalid_request`. It is an `EXISTS` over another copy whose path is outside the `within` range or on another source.
  - Selections store the query JSON as in R1, so "select all" works unchanged.

### D16. API row and detail fields (§11.8)

- **EntryRow** gains `content_state`, `copies`, `candidate_bytes`, `checked_bytes`, `duplicated_bytes`, `archive_state`, and `archive_id` (Interfaces).
- **The detail** gains `content`, `relations`, `archive`, and `coverage`.
- **Pinning.** The foundation adds these fields to `search.Row`, `search.Columns`, `ScanRow`, and the UI types with zero values. Slices fill them; no slice changes a signature.

### D17. Viewer for members (§11.12, R2.8)

- **Identity.** The archive file is opened through `viewer.openAt`'s identity checks, which move into `content.OpenArchive` and are shared with hashing. A mismatch with the row or the listing is `409 invalid_entry_state`.
- **Zip:**
  - a stored member is served from an `io.SectionReader`, with ranges;
  - a deflate member up to `archives.view_max_bytes` is inflated into memory and served with `http.ServeContent`, with ranges;
  - a larger deflate member is streamed with no `Accept-Ranges`.
- **Tar family:** the archive is streamed to the member, which is then served as for a large zip member. Budgets apply.
- **Headers and text.** The response uses the same type table and headers as R1 D12, and `/text` uses the same decoding.
- **Nothing touches disk** (R2.8). The e2e test lists the state directory, `TMPDIR`, and the source before and after.

### D18. Configuration (§12)

- **New sections:** `[hashing]`, `[archives]`, and `[duplicates]`, as specified in server-config.
- **Constants by spec:** the 16 MiB sample threshold, the three 64 KiB samples, the 1 MiB large-file bound, and 50% for `overlap`.
- **`[copies]`** stays a refused v0.2 section.

### D19. Regression corpus additions (§15)

- **New fixtures:**
  - `Projetos/site_antigo_2006.tar.gz`, which is the same as `Projetos/site_antigo`;
  - `Documentos/notas_2007.txt.gz`, a single-file gzip;
  - a bzip2 file taken from m4b's `testdata`;
  - two files of equal size and different content in `Downloads`;
  - a zip with a stored video, for the range scenario.
- **Synthfs-only fixtures** cover files of at least 16 MiB with equal and different samples, because a written corpus is capped at 40 MiB.
- **`ground_truth.json`** gains:
  - `duplicates`: groups of paths, where a member is written `archive.zip!member/path`;
  - `relations`: expected pairs and kinds;
  - `gems`: per section;
  - `members`: per archive.

  All of it is computed by the generator from the bytes it writes, except the relations, which are declared.
- **Tests** read the ground truth, never hard-coded lists.

### D20. Performance targets and measurements (§12)

- **[target]** At 2 million entries, review-list and Gems pages answer in p95 < 300 ms, Opportunities with seven cards in p95 < 1 s, and Compare as in D11. `relate` completes within 3 minutes and 1.5 GB of resident memory on the development machine. All of these are slow tests.
- **Regression checks.** R1.10 runs again with the `dir_dups` join and the new row fields. `walkbench` runs warm on the development machine to show that the scanner's ctime comparison and invalidation do not regress R1.14.
- **Measured on the owner's server and recorded in the addendum:**
  - the first hashing run of the owner's dataset: time, bytes read, and archives listed;
  - one `relate` pass.

### D21. Deferrals

- **A short change after R2, before R3:** owner category overrides, owner-marked groups, and scheduled rescans. The owner chose R2 option A on 2026-10-06, which moved them out of R1 D20's R2.
- **R3:** moving the files unique to one copy into another (§11.6), and Gems' move action (§11.7).
- **R6:** probabilities.
- **R7:** version families, nested archives, and more formats.
- **R8:** native platform paths.
- **Later, not scheduled:** searching archive members by name.

## Interfaces

### Schema (`migrations/0002_content.sql`)

```sql
CREATE TABLE contents (
  id     INTEGER PRIMARY KEY,
  sha256 BLOB NOT NULL UNIQUE CHECK (length(sha256) = 32),
  size   INTEGER NOT NULL CHECK (size > 0)
);
CREATE TABLE file_content (
  entry_id   INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  source_id  TEXT NOT NULL,                       -- the entry's source (denormalized for coverage)
  state      TEXT NOT NULL CHECK (state IN ('unique_size','pending','sampled','hashed','changed','unreadable')),
  size       INTEGER NOT NULL,
  mtime_ns   INTEGER, ctime_ns INTEGER, ino INTEGER,   -- identity the last read observed
  sample     BLOB CHECK (sample IS NULL OR length(sample) = 32),
  content_id INTEGER REFERENCES contents(id),
  checked_at INTEGER,
  CHECK ((state = 'hashed') = (content_id IS NOT NULL)),
  CHECK (state <> 'sampled' OR sample IS NOT NULL)
);
CREATE INDEX file_content_by_content ON file_content(content_id) WHERE content_id IS NOT NULL;
CREATE INDEX file_content_by_source  ON file_content(source_id, state, size);
CREATE TABLE content_coverage (
  source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  candidate_files INTEGER NOT NULL, candidate_bytes INTEGER NOT NULL,
  checked_files INTEGER NOT NULL,   checked_bytes INTEGER NOT NULL,
  unchecked_files INTEGER NOT NULL, unchecked_bytes INTEGER NOT NULL,
  unreadable_files INTEGER NOT NULL, unreadable_bytes INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE archives (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  format   TEXT NOT NULL CHECK (format IN ('zip','tar','tar_gzip','tar_bzip2','gzip','bzip2')),
  state    TEXT NOT NULL CHECK (state IN ('listing','complete','partial','rejected','encrypted','corrupt','unsupported','changed','unreadable')),
  detail   TEXT,                                   -- {"budget":"ratio"} | {"member_b64":"…"} | {"error":"…"}
  size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, ctime_ns INTEGER, ino INTEGER,
  members INTEGER NOT NULL DEFAULT 0, unpacked_bytes INTEGER NOT NULL DEFAULT 0,
  listed_at INTEGER
);
CREATE TABLE archive_members (
  id          INTEGER PRIMARY KEY,
  archive_id  INTEGER NOT NULL REFERENCES archives(entry_id) ON DELETE CASCADE,
  parent_id   INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,   -- NULL: top level
  name        BLOB NOT NULL,
  path        BLOB NOT NULL,                       -- '/'-joined inside the archive
  kind        TEXT NOT NULL CHECK (kind IN ('directory','file','symlink','special')),
  size        INTEGER NOT NULL DEFAULT 0, mtime_ns INTEGER, link_text BLOB,
  total_bytes INTEGER NOT NULL DEFAULT 0, total_files INTEGER NOT NULL DEFAULT 0,
  locator     INTEGER,                             -- zip central-directory index
  stored      INTEGER NOT NULL DEFAULT 0,          -- zip method store (ranges, D17)
  link_member INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,  -- tar hard link target
  state       TEXT CHECK (state IN ('unique_size','pending','hashed','unreadable')),  -- file members only
  content_id  INTEGER REFERENCES contents(id),
  UNIQUE (archive_id, path),
  CHECK ((kind = 'file') = (state IS NOT NULL))
);
CREATE INDEX archive_members_children ON archive_members(archive_id, parent_id, name);
CREATE INDEX archive_members_parent   ON archive_members(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX archive_members_link     ON archive_members(link_member) WHERE link_member IS NOT NULL;
CREATE INDEX archive_members_by_size  ON archive_members(size) WHERE kind = 'file';
CREATE INDEX archive_members_content  ON archive_members(content_id) WHERE content_id IS NOT NULL;
CREATE TABLE relations (
  id   INTEGER PRIMARY KEY,
  gen  INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('same','inside','overlap')),
  a_entry  INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  a_member INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,
  b_entry  INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  b_member INTEGER REFERENCES archive_members(id) ON DELETE CASCADE,
  matched_bytes INTEGER NOT NULL, redundant_bytes INTEGER NOT NULL,
  a_bytes INTEGER NOT NULL, a_files INTEGER NOT NULL, b_bytes INTEGER NOT NULL, b_files INTEGER NOT NULL,
  a_only_files INTEGER NOT NULL, a_only_bytes INTEGER NOT NULL,
  b_only_files INTEGER NOT NULL, b_only_bytes INTEGER NOT NULL
);
CREATE INDEX relations_a        ON relations(a_entry);
CREATE INDEX relations_b        ON relations(b_entry);
CREATE INDEX relations_a_member ON relations(a_member) WHERE a_member IS NOT NULL;
CREATE INDEX relations_b_member ON relations(b_member) WHERE b_member IS NOT NULL;
CREATE TABLE dir_dups (
  entry_id INTEGER PRIMARY KEY REFERENCES entries(id) ON DELETE CASCADE,
  candidate_bytes INTEGER NOT NULL, checked_bytes INTEGER NOT NULL,
  duplicated_bytes INTEGER NOT NULL, duplicated_files INTEGER NOT NULL
);
CREATE TABLE review_rows (
  id       INTEGER PRIMARY KEY,
  gen      INTEGER NOT NULL,
  list     TEXT NOT NULL CHECK (list IN ('duplicates','unpacked_archives','system_junk','installers',
             'programs','caches','leftovers','gems_unique','gems_rescue','gems_only_in_copy')),
  source_id   TEXT REFERENCES sources(id) ON DELETE CASCADE,         -- NULL for duplicates rows
  entry_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,
  relation_id INTEGER REFERENCES relations(id) ON DELETE CASCADE,
  content_id  INTEGER REFERENCES contents(id),
  group_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,      -- gems_rescue: the group
  bytes INTEGER NOT NULL, files INTEGER NOT NULL,
  sort_key INTEGER NOT NULL,                       -- bytes (cards) or mtime_ns (gems_unique)
  CHECK ((entry_id IS NOT NULL) + (relation_id IS NOT NULL) + (content_id IS NOT NULL) = 1)
);
CREATE INDEX review_rows_list     ON review_rows(gen, list, sort_key, id);
CREATE INDEX review_rows_source   ON review_rows(gen, list, source_id, sort_key, id) WHERE source_id IS NOT NULL;
CREATE INDEX review_rows_entry    ON review_rows(entry_id) WHERE entry_id IS NOT NULL;
CREATE INDEX review_rows_group    ON review_rows(group_id) WHERE group_id IS NOT NULL;
CREATE INDEX review_rows_relation ON review_rows(relation_id) WHERE relation_id IS NOT NULL;
CREATE TABLE review_row_sources (
  row_id INTEGER NOT NULL REFERENCES review_rows(id) ON DELETE CASCADE,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  PRIMARY KEY (source_id, row_id)) WITHOUT ROWID;
CREATE INDEX review_row_sources_row ON review_row_sources(row_id);
CREATE TABLE review_state (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  gen INTEGER NOT NULL, dirty INTEGER NOT NULL, computed_at INTEGER);
INSERT INTO review_state VALUES (1, 0, 1, NULL);
```

Every foreign key into `entries` is indexed, as R1 A2 requires and `TestForeignKeysIntoEntriesAreIndexed` checks. Removing a source cascades through `entries` and `sources`. Unreferenced `contents` rows are pruned by `relate` in batches of 5,000.

### Import direction

`domain` ← `archive` ← `content` → `relations` ← `review`. `web/api` imports `content`, `relations`, `review`, `search`, and `decisions`. `viewer` imports `content`. `index` imports none of them: invalidation is two SQL statements, and the after-scan hook is a function. `cmd/precious` wires everything.

### Go signatures (foundation unless marked)

```go
// internal/domain
type MemberID int64
type Ref struct{ Entry EntryID; Member MemberID }          // Member != 0: an archive member
func ParseRef(s string) (Ref, error)                       // "123" | "m45"; anything else: invalid_request
func (r Ref) String() string
type ArchiveFormat string; type ArchiveState string; type MemberKind string   // restored values (D7)
type ContentState string                                   // unique_size pending sampled hashed changed unreadable
type DupFilter string                                      // copies elsewhere unique unchecked

// internal/config
type Hashing struct{ ReadChunkBytes, YieldBytes int64 }
type Archives struct{ MaxMembers, MaxUnpackedBytes, MaxRatio, ViewMaxBytes int64; MaxTime Duration }
type Duplicates struct{ RefreshInterval Duration }

// internal/index (foundation)
func (h *Handler) OnScanDone(fn func(ctx context.Context, src domain.SourceID))   // before Register

// internal/decisions (foundation)
func (s *Service) NewSelection(ctx context.Context, tx *sql.Tx, query json.RawMessage, ids []domain.EntryID) (Selection, error)

// internal/search (foundation: fields; slice Q: the filter SQL)
// Query gains Dup []domain.DupFilter (`dup`); Row gains the D16 fields.

// internal/archive (slice A)
type Limits struct{ MaxEntries, MaxBytes, MaxRatio int64; Deadline time.Time; Now func() time.Time }
type Member struct{ Path [][]byte; Kind domain.MemberKind; Size int64; Mtime time.Time; LinkText []byte; Index int; Stored bool; LinkTo [][]byte }
type Stop struct{ State domain.ArchiveState; Detail string; Member []byte }   // error
func Classify(name []byte) (domain.ArchiveFormat, bool)
func OpenZip(r io.ReaderAt, size int64, lim Limits) (*Zip, error)
func (z *Zip) Members() []Member
func (z *Zip) Open(index int) (io.ReadCloser, error)
func (z *Zip) Section(index int) (*io.SectionReader, bool)                     // stored members only
func Stream(ctx context.Context, f domain.ArchiveFormat, name []byte, r io.Reader, lim Limits,
    visit func(m Member, data io.Reader) error) error

// internal/content (slice H)
const KindHash, KindHashNow jobs.Kind = "hash", "hash_now"
func NewService(st *store.Store, src *sources.Service, clk clock.Clock, h config.Hashing, a config.Archives) *Service
func (s *Service) Register(r *jobs.Runner)                     // hash (ClassBulk), hash_now (ClassInteractive)
func (s *Service) AfterScan(ctx context.Context, src domain.SourceID)   // index hook
func (s *Service) Startup(ctx context.Context) error
func RegisterCommands(h *commands.Handler, s *Service)         // start-hash, check-now
type Coverage struct{ CandidateFiles, CandidateBytes, CheckedFiles, CheckedBytes,
    UncheckedFiles, UncheckedBytes, UnreadableFiles, UnreadableBytes int64 }
func CoverageOf(ctx context.Context, q store.Queryer, src domain.SourceID) (Coverage, error)   // "" = all
type Copy struct{ Ref domain.Ref; SourceID domain.SourceID; Path string; PathB64 []byte
    ArchiveID *domain.EntryID; HardLink, Offline bool; EffDecision domain.Decision }
func Copies(ctx context.Context, q store.Queryer, ref domain.Ref, cursor string, limit int) ([]Copy, int, string, error)
type Opened struct{ Size int64; ModTime time.Time; Content io.ReadCloser; Seeker io.ReadSeeker /* nil: no ranges */ }
func (s *Service) OpenMember(ctx context.Context, q store.Queryer, ref domain.Ref) (Opened, error)   // 409 invalid_entry_state, source_offline

// internal/relations (slice R)
const KindRelate jobs.Kind = "relate"
func NewHandler(st *store.Store, clk clock.Clock, cfg config.Duplicates, after func(ctx context.Context, gen int64) error) *Handler
func (h *Handler) Register(r *jobs.Runner)                     // pool "relate", capacity 1
func RequestRefresh(tx *jobs.Tx) error                         // sets dirty, EnqueueOnce(scope "relate")
type Range struct{ Source domain.SourceID; From, To []byte }   // a folder's descendant path range
func Candidates(ctx context.Context, q store.Queryer, src domain.SourceID) ([][2]Range, error)  // provisional pairs (D4)
type Relation struct{ ID int64; Kind string; A, B domain.Ref; MatchedBytes, RedundantBytes int64
    AOnlyFiles, AOnlyBytes, BOnlyFiles, BOnlyBytes int64 }
func RelationsOf(ctx context.Context, q store.Queryer, ref domain.Ref, limit int) ([]Relation, error)
type Bucket string                                             // only_left only_right identical different unchecked
func Compare(ctx context.Context, q store.Queryer, left, right domain.Ref, bucket Bucket, cursor string, limit int) (CompareResult, error)

// internal/review (slice V)
type List string                                               // the review_rows.list values
func Refresh(ctx context.Context, st *store.Store, gen int64) error   // relations' after hook
type Card struct{ List List; Bytes, Rows int64; Basis string }  // basis: rules | content
func Cards(ctx context.Context, q store.Queryer, src domain.SourceID) ([]Card, error)
func Rows(ctx context.Context, q store.Queryer, list List, src domain.SourceID, decided bool, cursor string, limit int) (Page, error)
func Resolve(ctx context.Context, q store.Queryer, list List, src domain.SourceID, max int) ([]domain.EntryID, error)
func RegisterCommands(h *commands.Handler, d *decisions.Service)  // select-list
```

### Commands (`POST /api/commands/{name}`, `Idempotency-Key` required, envelope unchanged)

| Command | Request | Success | Errors |
|---|---|---|---|
| `start-hash` (H) | `{"source_id"}` | 202 `{"job_id","state","coalesced"}` | 404 unknown_source; 409 source_offline |
| `check-now` (H) | `{"entry_ids":["12","m45"]}`: 1 or 2 folder, archive, or member-folder refs | 202 `{"jobs":[{"job_id","state","coalesced"}]}`, one per source | 400 (0 or more than 2 refs, a file ref); 404; 409 source_offline |
| `select-list` (V) | `{"list":"system_junk","source_id":"…"?}` | 201 as `create-selection` | 400 (`duplicates`, unknown list); 404 unknown_source |
| `set-decision`, `set-tags` | unchanged; a member ref (`m…`) is 400 `invalid_request` | | |

No R2 command writes an audit event, because none changes an owner decision. `set-decision` and `set-tags` keep theirs.

### Read endpoints (Q unless marked)

- **EntryRow** gains these fields (null when they do not apply):
  `"content_state"` (files and file members), `"copies"`, `"candidate_bytes"`, `"checked_bytes"`, `"duplicated_bytes"` (folders, from `dir_dups`), `"archive_state"` (archive files, null when unlisted), and `"archive_id"` (members).
  A member row has `"id":"m45"`, `"decision":null`, `"eff_decision"` (the archive's), and `"tag_ids":[]`.
- **`GET /api/entries/{ref}`** gains these fields:
  - `"content":{"state","sha256":hex|null,"checked_at","copies":[CopyJSON ≤ 20],"copies_count"}|null`;
  - `"relations":[RelationJSON ≤ 20]`;
  - `"archive":{"format","state","detail","members","unpacked_bytes"}|null`;
  - `"coverage":CoverageJSON` (global).

  The JSON shapes are:
  - CopyJSON: `{"ref","source_id","path","path_b64","archive_id","hard_link","offline","eff_decision"}`;
  - RelationJSON: `{"id","kind","self":"a"|"b","other":EntryRow,"matched_bytes","redundant_bytes","only_here":{"files","bytes"},"only_there":{"files","bytes"}}`;
  - CoverageJSON: `{"candidate":{"files","bytes"},"checked":…,"unchecked":…,"unreadable":…}`.
- **`GET /api/entries/{ref}/copies?cursor=&limit=`** → `{"items":[CopyJSON],"next_cursor","count"}`.
- **`GET /api/entries/{ref}/children`** and **`/treemap`** accept a complete archive or a member folder. Sorting by bytes, files, newest, or name happens in SQL without an index, which is bounded by one archive.
- **`GET /api/home`** gains these fields:
  - `"coverage":CoverageJSON`;
  - `"cards":[CardJSON]`;
  - `"hashing":[{"source_id","job_id","kind","state","progress"}]`.
- **`GET /api/opportunities?source=`** → `{"cards":[CardJSON],"coverage","computed_at"}`, where CardJSON is `{"list","bytes","rows","basis"}`.
- **`GET /api/opportunities/{list}?source=&decided=0|1&cursor=&limit=`** → `{"card":CardJSON,"items":[RowJSON],"next_cursor"}`.
  - RowJSON is `{"id","bytes","files","entry":EntryRow|null,"relation":RelationJSON|null,"copies":[CopyJSON]|null,"summary":{"category","years":[from,to],"files","bytes","signals":[…]}}`.
  - With `decided=1`, the list holds the rows that are no longer open.
  - An unknown list is 404 `not_found`.
- **`GET /api/gems?section=unique|rescue|only_in_copy&source=&cursor=&limit=`** → `{"section","items":[{"entry":EntryRow,"group":EntryRow|null,"relation":RelationJSON|null}],"next_cursor","coverage"}`.
- **`GET /api/compare?left=&right=&bucket=&cursor=&limit=`** → `{"left":EntryRow,"right":EntryRow,"summary":{"only_left":{"files","bytes"},"only_right":…,"identical":…,"different":…,"unchecked":…},"items":[{"path","path_b64","left":EntryRow|null,"right":EntryRow|null}],"next_cursor"}`.
  - 400 if one side contains the other, or a side is a file.
  - 404 if a side is unknown.
- **`GET /api/search`** gains `dup`.
- **`GET /api/entries/{ref}/content`** and **`/text`** serve members (D17).
- **Job progress keys:**
  - `hash`: `phase` (1 listing, 2 large, 3 small), `candidate_files`, `candidate_bytes`, `checked_files`, `checked_bytes`, `read_bytes`, `archives_listed`, and `unreadable`;
  - `relate`: `phase` and `folders`.

## Transaction boundaries

| Path (transaction) | Re-checked inside the writing transaction | A command commits just before | A command commits just after |
|---|---|---|---|
| Planning batch (H) | Rows come from `INSERT … SELECT` over present `entries`, so sizes are current | A scan update deleted a `file_content` row: planning recreates it as `pending` or `unique_size` | A scan update deletes the row; the hashing jobs enqueued after that scan plan again |
| Hashing commit (H) | The entry is present with the observed size, `mtime_ns`, `ctime_ns`, and `ino`, and its `file_content` row exists. Otherwise the result is dropped (I9). Coverage deltas apply here | A rescan updated the entry: the result is dropped, and the file is `pending` at the next plan | The rescan deletes the digest just written (invalidation) |
| Archive listing batch (H) | `archives` row in state `listing`, with the identity of the read | A rescan updated the archive: the row is gone, so the batch abandons the archive | The rescan deletes the listing and members by cascade |
| `relate` generation (R, V) | Rows are inserted only when their entries and members exist; one transaction flips `review_state.gen` | `remove-source` or a kind change: the rows of deleted entries are skipped | `set-decision` is read live by the cards, nothing is stale. A source removal cascades away the new generation's rows |
| `dir_dups` upsert (R) | The entry exists (`INSERT … SELECT … WHERE EXISTS`) | — | A deleted folder cascades away |
| `select-list` (V) | The open rows of the current generation are resolved here, joined with the current `eff_decision` | A keep set just before excludes that row from "open" and is skipped by the bulk decision anyway | A later `relate` generation does not change the selection (explicit IDs, as in R1) |
| `check-now` and `start-hash` (H) | The source is `online`; `EnqueueOnce` by scope | A `remove-source` before: 404 | — |
| `set-decision` and `set-tags` (D, unchanged) | As in R1. A member ref fails before the transaction | — | — |

## Risks / Trade-offs

- **[The first hashing run on the owner's dataset takes hours]** It streams 185 GB of tar.gz, 36 GB of gzip, and every shared-size file. → Hashing runs in the background, yields to scans and pages, keeps committed work across restarts, and `check-now` serves Compare first. The duration is recorded on the reference server (D20).
- **[Single-file gzip is streamed just to learn its member's size]** → It is accepted in R2. The gzip trailer's size field is a later optimization.
- **[`relate` memory or time at 2 million entries]** → A slow test bounds both (D20), and the owner-server pass is recorded. If either fails, a byte floor for folders (m4b's `report_min_bytes`) is the documented fallback, recorded as a decision.
- **[Card sums evaluate open rows live]** → They use the indexed `review_rows` per generation and copy lookups by `content_id`, under a slow-test target (D20).
- **[A ctime comparison rewrites rows after `chmod` or `chown` churn]** → Such rows are rare on archives. Each update costs one row and one digest.
- **[Unopened formats (7z, rar) and unreadable files limit "no other copy"]** → Coverage and the operator docs name them, and claims carry the checked share (I7).
- **[Not on FAT: hard-link detection]** Without stable identity, every entry is its own copy. That is conservative.

## Migration Plan

1. Take a backup with `precious backup`, then install the R2 binary. At startup, `0002_content.sql` applies to the R1 database, and every online source gets a hashing job.
2. **Rollback.** The R1 binary refuses a newer schema (`ErrSchemaTooNew`), so restore the backup taken in step 1. The operator docs say this.
