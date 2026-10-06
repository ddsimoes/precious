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
- **The scanner** compares `ctime_ns` when both values are non-zero, with the same tolerance as `mtime_ns` (R1 D8: resolution, and on local-time filesystems a one-hour shift, which moves both times). An update of an entry whose size, times, or identity changed deletes its `file_content` and `archives` rows in the same batch (one prepared statement each, only on such updates), so a cached digest is valid exactly while the identity it was read under is the row's. An update that only rewrites the classification (a rules change) or brings a missing entry back with the same facts keeps them.
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
- **The algorithm** is m4b's: `partner`, `Relate`, `maximal`, `lift`, and freeable bytes (renamed redundant bytes), with these changes (agreed during implementation, 2026-10-06):
  - `overlap` needs at least 50% of one side's bytes ("a large share", §6.4). m4b used 10% for searches the owner started.
  - There is no reporting floor: ranking by bytes keeps small relations at the bottom.
  - There are no file results: duplicate groups cover single files (D12).
  - **Naming `same` sides.** m4b's `lift` names a side by its highest equivalent folder (the folders of a chain where each holds nothing but the next). For `same`, both sides are named by their deepest equivalent folder instead, stopping at an archive (a folder holding only an archive still names the archive). Otherwise the Winamp copy, alone in `HD antigo/backup pc velho/Arquivos de programas`, would relate as that parent, against the spec scenario and the ground truth. `inside` and `overlap` keep `lift`.
  - **Ancestors are no partners.** m4b proposed an ancestor of folder A as A's partner when one of A's keys occurred among that ancestor's own files, so a copy kept beside its folder gave "`ISOs/copia` inside `ISOs`". Such occurrences are skipped (regression test `TestAncestorIsNoPartner`).
  - **Overlap orientation.** An overlap found from either side is one pair, oriented by the larger matched share, so an overlap whose larger-share side is already `inside` the other is that same pair and is not listed twice.
  - **Only-one-side counts** are the side's non-empty files and file members whose key does not occur on the other side; gaps are not counted (Compare, D11, proves gaps by size on request).
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
  - `duplicated_bytes` sums the files in the subtree that have another copy anywhere. `candidate_bytes` leaves out unreadable files, so a folder whose only gaps are unreadable files reads as checked; coverage still counts them as unreadable (B17).
  - Children and the treemap `LEFT JOIN` it by primary key.
- **A file's percent duplicated** is 0 or 100, from its row.
- **Member folders** inside an archive compute theirs on read, because archives are small next to the index. They count `sampled` as checked, as entry folders do (B17).
- **No sort by percent duplicated.** The spec asks for a column and a coloring. A sort needs a fifth child index.
- **Treemap color bands:** 0%, < 25%, < 50%, < 75%, ≥ 75%, and not checked (`checked_bytes < candidate_bytes`).

### D11. Compare (§11.6)

- **Sides.** Two refs, each a folder, an archive entry, or a member folder, where neither contains the other. Otherwise the response is `400 invalid_request`.
- **The computation** runs per request in one read transaction, loading both sides' files with their relative paths, sizes, and content states.
- **Buckets:**
  - **identical:** the content occurs on both sides;
  - **different:** the same relative path with different content;
  - **only on one side:** the content does not occur on the other side and is not in different;
  - **unchecked:** a `pending`, `changed`, or `unreadable` file (or one hashing has not planned yet) whose size occurs on the other side, and a checked file whose size occurs on the other side only among such files (its absence there is not proven, I7; decided during implementation, 2026-10-06).

  A size missing from the other side proves "only here" without hashing. Regular files and file members take part (symlinks and special files do not), and empty files match each other; a tar hard-link member has its target's content. Identical items pair a content's files on both sides in path order (an extra copy is an item with one file); the counted bytes of a paired item are its left file's size, or its right file's when it has no left.
- **Paging.** Items of the requested bucket sort by relative path; the cursor is an offset into that order, recomputed per request. `Compare` returns `CompareResult{Summary map[Bucket]Count; Items []CompareItem{Path, Left, Right *domain.Ref}; NextCursor}`; an empty bucket lists no items, an unknown bucket or a bad cursor is `400 invalid_request`, and an unknown side (or a member of an archive that is not complete) is `404 not_found`.
- **Wrapper folders.** When exactly one side has a single top folder and its contents align better with the other side, that wrapper is dropped from the relative paths (m4b `lift`). For example, `emule-0.47c/` inside the zip lines up with the unpacked folder.
- **"Check now"** calls `check-now` with both refs.
- **Opening group.** Without a `bucket`, the page asks for the summary alone, then opens the first bucket that holds files, in the order only left, only right, different, unchecked, identical (B22).
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
- **Duplicates group bytes** (agreed 2026-10-06). A group row's bytes are size × its physical copies outside every listed relation, less one when none of its copies is inside a listed relation. The relation row already counts the redundancy of the copies inside it and leaves one of them standing, so a three-copy group with two copies inside `A same B` adds one copy's size, and no byte counts twice.
- **Outermost and existence.** Outermost is computed per card over the matching entries' raw paths, missing entries and source roots excluded. An empty folder is present, holds no file, has no unreadable folder or mount boundary below it, and is no mount boundary itself.
- **Paging.** Card lists page by `sort_key` (= bytes) then `id`, both descending; Gems sections by `sort_key` then `id`, both ascending. A cursor is the last row's `sort_key` and `id`.
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
- **Row encoding** (agreed 2026-10-06):
  - `gems_unique`: `entry_id` the file, `sort_key` its `mtime_ns`, rows inserted by `mtime_ns` then raw path;
  - `gems_rescue`: `entry_id` the indicator, `group_id` its outermost programs or disposable group (indicators are taken from every such group's `dir_stats.indicators` and deduplicated), `sort_key` the group's rank (largest group first), indicators by raw path within a group;
  - `gems_only_in_copy`: `entry_id` the file, `group_id` the overlap side folder holding it, `sort_key` the relation's ID (so `Rows` returns it), files by raw path within a relation, each file once. Review computes these with its own SQL (the unique files below an overlap side folder; a unique file is on no other side), not with Compare. Archive members cannot be `entry_id`, so archive sides add none.
- **Decisions.** Gems rows are listed whatever their decision; a decided Gems page is empty.

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
  group_id    INTEGER REFERENCES entries(id) ON DELETE CASCADE,      -- gems_rescue: the group; gems_only_in_copy: the overlap side
  bytes INTEGER NOT NULL, files INTEGER NOT NULL,
  sort_key INTEGER NOT NULL,                       -- bytes (cards), mtime_ns (gems_unique), group rank (gems_rescue), relation id (gems_only_in_copy)
  CHECK ((entry_id IS NOT NULL) + (relation_id IS NOT NULL) + (content_id IS NOT NULL) = 1)
);
CREATE INDEX review_rows_list     ON review_rows(gen, list, sort_key, id);
CREATE INDEX review_rows_source   ON review_rows(gen, list, source_id, sort_key, id) WHERE source_id IS NOT NULL;
CREATE INDEX review_rows_entry    ON review_rows(entry_id) WHERE entry_id IS NOT NULL;
CREATE INDEX review_rows_group    ON review_rows(group_id) WHERE group_id IS NOT NULL;
CREATE INDEX review_rows_relation ON review_rows(relation_id) WHERE relation_id IS NOT NULL;
CREATE INDEX review_rows_content  ON review_rows(content_id) WHERE content_id IS NOT NULL;
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

Every foreign key into `entries` is indexed, as R1 A2 requires and `TestForeignKeysIntoEntriesAreIndexed` checks. Every foreign key into `contents` is indexed too, so pruning a `contents` row checks its referencing rows by index. Removing a source cascades through `entries` and `sources`. Unreferenced `contents` rows are pruned by `relate` in batches of 5,000.

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
func NewService(st *store.Store, src *sources.Service, clk clock.Clock, h config.Hashing, a config.Archives,
    d config.Duplicates) *Service                               // d: refresh_interval of the hash checkpoints (D5)
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
// The identity-checked open shared with the viewer (D17); slice Q replaces viewer.openAt with OpenAt.
type Row struct{ Path []byte; Size int64; MtimeNs, CtimeNs, Ino sql.NullInt64 }   // the entries row's facts
func Matches(r Row, info fsaccess.EntryInfo, caps fsaccess.Capabilities) bool     // the scanner's unchanged test, ctime included
func OpenAt(root fsaccess.Dir, r Row, caps fsaccess.Capabilities) (fsaccess.File, fsaccess.EntryInfo, error)  // 409 invalid_entry_state
type Archive struct{ Entry domain.EntryID; Source domain.SourceID; Format domain.ArchiveFormat; Name []byte; Row Row
    File fsaccess.File; Info fsaccess.EntryInfo }                                  // Close closes file and root
func OpenArchive(ctx context.Context, q store.Queryer, src *sources.Service, id domain.EntryID) (*Archive, error)
    // complete archive, entry row == listing identity == disk; 404, 409 invalid_entry_state, source_offline

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
type Row struct{ ID int64; List List; Source domain.SourceID; Entry domain.EntryID; Relation, Content int64
    Copy domain.Ref /* a content row's lowest present copy */; Group domain.EntryID; Bytes, Files, SortKey int64 }
type Page struct{ Items []Row; NextCursor string }
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
  A member row has `"id":"m45"`, `"decision":null`, `"eff_decision"` (the archive's), and `"tag_ids":[]`. A zip member whose name is not valid UTF-8 has `"name"` and `"path"` decoded from code page 850, and the raw bytes in `"name_b64"` and `"path_b64"` (B19).
  `"mtime"`, `"newest"`, and `"oldest"` are null for a time at or before the epoch, and by-year items are `{"year":int|null,"bytes","files"}`, the unknown year (null) last (B20).
- **`GET /api/entries/{ref}`** gains these fields:
  - `"content":{"state","sha256":hex|null,"checked_at","copies":[CopyJSON ≤ 20],"copies_count"}|null`;
  - `"relations":[RelationJSON ≤ 20]`;
  - `"archive":{"format","state","detail","members","unpacked_bytes"}|null`;
  - `"coverage":CoverageJSON` (global).

  The JSON shapes are:
  - CopyJSON: `{"ref","source_id","path","path_b64","archive_id","hard_link","offline","decision","eff_decision"}`, where `decision` is the copy's own decision, or null when it follows its folder or is a member (B26);
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
  - RowJSON is `{"id","bytes","files","entry":EntryRow|null,"relation":RelationJSON|null,"copies":[CopyJSON]|null,"summary":{"category","years":[from,to],"files","bytes","signals":[…]}}`. A relation row's `files` is side a's file count without archive members (B24). An `unpacked_archives` row has both `entry` (the archive) and `relation` (self `a`, other the folder holding its content; B23).
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

## Addendum: measurements

### Review lists and Gems at 2 million entries (task 5.6)

`go test -tags slow -run Review -v ./internal/review/` on the development machine (Intel Core i9-13980HX), 2026-10-06. The generated index has 2,000,001 entries in one source; review rows: 270,100 duplicates (100 same relations, 270,000 duplicate groups), 720,000 `gems_unique`, 60,000 `gems_only_in_copy`, 600 `gems_rescue`, and 200 rows in each rule card.

| Measurement | Result | Target |
|---|---|---|
| Seven cards, all sources and one source (20 runs) | p95 690 ms, max 708 ms | p95 < 1 s |
| Review-list and Gems pages of 50 rows, open and decided, all sources and one source (136 pages) | p95 0.6 ms, max 699 ms | p95 < 300 ms |
| `review.Refresh` of one generation | 41 s | within `relate`'s 3 minutes |

The slowest page is the decided duplicates page when no duplicates row is decided: it evaluates every row's open state (about 2 µs per duplicate group) before finding that none is closed. The cards cost the same evaluation once per row.

### Relations and Compare at 2 million entries (task 4.6)

`go test -tags slow -run Relate ./internal/relations/` on the development machine, 2026-10-06. The index had 2,019,621 entries: 19,821 folders, about 2.0 M hashed files, and about 1.8 M contents.

| Measurement | Result | Target |
|---|---|---|
| One whole `relate` job: load, relate, write relations and `dir_dups` | 4.24 s | 3 minutes |
| Peak resident memory of the test process, seeding included | 430 MiB (36 MiB after seeding) | 1.5 GB |
| Compare of two sides of 99,990 files each | 0.65 s | 2 s |

Together with `review.Refresh` (41 s, task 5.6), one refresh of 2 million entries takes under a minute on this machine.

### R1.10 again, with the R2 joins (task 6.8)

`go test -tags slow -run R1_10 ./internal/web/api/` on the development machine, 2026-10-06, after scanning 2,000,000 entries (1 m 40 s). Every file was seeded as hashed with two copies per content, and every folder got `dir_dups`, so each row runs the copy subqueries.

| Measurement | Result | Target |
|---|---|---|
| Children pages of 200 rows, sorted by bytes | p50 7.5 ms, p95 10.8 ms, max 14.2 ms | p95 < 300 ms |
| Treemap levels | p50 1.75 ms, p95 9.5 ms, max 32.5 ms | p95 < 500 ms |

### Scanner regression check (task 8.5)

`walkbench`, warm, on the development machine, 2026-10-06, after the scanner gained ctime comparison and digest invalidation (B1):

| Tree | Entries | Walk | Scan | Ratio | R1 (A13) |
|---|---|---|---|---|---|
| `/usr/share` | 178,818 | 0.72 s | 9.09 s | 12.69 | ratio 12.6 |
| `/usr` | ≈391,600 | 1.73 s | 20.2 s | 11.73 | walk 1.86 s, scan 23.9 s, ratio 12.8 |

There is no regression. A first scan writes no content rows, so the invalidation statements never run on it.

### Reference server (task 8.7)

The owner's reference server: a Linux container with 4 cores of a low-power 2.0 GHz x86 CPU and 4 GiB of memory, with the data on HDDs. It ran the R2 binary on the R1 database of the owner's dataset, upgraded in place after a backup, 2026-10-06. The index: one source with 158,203 files (837 GB) in 2,919 folders.

**The first hashing run** (one `hash` job, started by the upgrade):

| Measurement | Result |
|---|---|
| Duration | 10,747 s (2 h 59 min) |
| Candidates (files of a shared size and archive members) | 1,287,928, 521.3 GB |
| Checked at the end | 1,287,567 files and members, 521.3 GB; 9 unreadable (4.2 MB); 352 left unchecked (B15) |
| Bytes read | 467.3 GB, 43.5 MB/s on average; 64 large files settled by samples (7.8 GB) |
| Archives | 1,718 archive files: 1,679 complete with 1,218,841 members (1,190,283 files), 2 partial at a budget, 31 unreadable, 1 corrupt, 2 rejected, 3 unsupported |
| CPU of the server process, `relate` included | 9,096 s, 0.85 of a core on average |
| Peak resident memory of the server process, 16 `relate` passes included | 1.04 GiB (the container's peak, 1.79 GB, counts the page cache) |
| Database | 98.8 MB before the upgrade, 459 MB after |

The B15 binary then restarted the server. Its startup job read the 352 candidates left unchecked in 323 s (267 MB read, the members' zips included), and coverage ended with every candidate checked except the 9 unreadable files.

**`relate`.** During the run, the hashing checkpoints started 16 passes, from 4.5 s (2,921 folders, before any archive was listed) to 63.4 s (33,156 folders, archive folders included). The pass after the startup job, with the disks idle, took 64.4 s; that server process peaked at 771 MiB resident, its hashing job included. Both are within the targets that D20 sets on the development machine (3 minutes and 1.5 GB).

**What the owner sees:**
- relations: 1,147 `same` (1.26 GB redundant), 280 `overlap` (18.1 GB), and 319 `inside` (232.7 GB);
- cards: 8,563 duplicates rows (247.8 GB); 1,232 caches (5.3 GB); 7 installers (65.7 GB); 624 leftovers (4.4 GB); 5 programs (1.9 GB); 43 system junk; 19 unpacked archives;
- Gems: 48,302 unique (296.8 GB), 68,470 only in a copy (360.8 GB), and 3 to rescue.

## Addendum: decisions made during implementation

- **B1.** The scanner deletes a file's `file_content` and `archives` rows only when the file's own facts change: size, mtime, ctime, or identity. A classification-only update, such as a new rules version, keeps digests, and so does a missing file that returns with the same facts. ctime is compared with the same tolerance as mtime, because vfat's ctime moves with its mtime. (Task 1.4.)
- **B2.** `review_rows_content` indexes the foreign key into `contents`, so pruning `contents` stays cheap.
- **B3.** `content.NewService` takes `config.Duplicates` for the pace of hashing checkpoints, instead of a setter that could be forgotten.
- **B4.** Archive types and reading:
  - `archive.Classify` returns `("", false)` for 7z, rar, xz, and zst, which are then hashed as plain files.
  - A tar hard link is reported as `Kind = file`, with `LinkTo` naming its earlier target, and is stored with `link_member`.
  - `Zip.Section` covers only stored members whose local header and sizes check out.
- **B5.** Relation naming and partners:
  - A `same` relation names both sides by their deepest equivalent folder, stopping at an archive. m4b's highest-folder `lift` named the corpus's Winamp copy by its parent folder.
  - `inside` and `overlap` keep `lift`.
  - A file in an ancestor of a folder is never proposed as that folder's partner, which m4b got wrong ("ISOs/copia inside ISOs").
  - Stored relations hold no file results. An overlap is one pair, oriented by the larger matched share.
- **B6.** Compare counts a checked file as unchecked when its size occurs on the other side only among unchecked or unreadable files, because its absence there is not proven (I7).
- **B7.** Review rows:
  - `gems_only_in_copy` is computed in SQL as the unique files below an overlap side, without `relations.Compare`.
  - Gems rows ignore decisions.
  - Sort keys: `gems_unique` by `mtime_ns`, `gems_rescue` by group rank, `gems_only_in_copy` by relation.
  - A duplicate group's row counts only the copies outside every listed relation's `a` side, so no byte counts twice beside a relation row.
- **B8.** `check-now` coalesces folders into the active `hash_now` job's payload, which the job re-reads until nothing is new. A folder that arrives after the job's last read is still covered by the source's regular hashing job, but not first.
- **B9.** A file that became `changed` or `unreadable` is not retried by later hashing jobs until a rescan updates its entry. A `chmod` or an edit moves ctime, so the rescan sends the file back to `pending`.
- **B10.** Test seeding: `indextest.Attach` gives the seeded view of a source indexed by a real scan, and `SeedContent` follows D7 for streamed archives.
- **B11.** Read API:
  - `copies` in an EntryRow counts the physical copies including the row itself; it is 1 for `unique_size` and `sampled`, and null while unchecked;
  - `archive_state` is null while an archive is still being listed;
  - a member's path reads `archive!member/path`, and its detail lists the archive and its member folders as ancestors;
  - a malformed ref stays 404, as in R1.
- **B12.** `set-decision` and `set-tags` answer 400 `invalid_request` for a member ref (`m45`); any other malformed ID stays 404 `not_found`, as in R1.
- **B13.** The viewer opens files through `content.OpenAt`, so the viewer and hashing share one identity-checked walk. `viewer.Register` takes the `*content.Service` for members.
- **B14.** Hashing never blocks `remove-source` (found by the browser suite, task 8.3; coordinator's decision, 2026-10-06). Only an active scan of the source is `409 job_active`, as source-registry specifies. The source's other active jobs (`hash`, `hash_now`) are cancelled in the removal's transaction, a running attempt as soon as it commits, and their rows go away with the source; the attempt commits nothing more for the removed entries (I9). The same transaction calls `relations.RequestRefresh`, so the other sources' relations, `dir_dups`, and review rows drop the removed copies at the next `relate`.
- **B15.** A hashing pass reads, before it ends, the candidates that its own tar-family listings create (found on the reference server, task 8.7). After the large queue lists streamed archives, the pass plans again. It then finds the zips with pending members again, and stops skipping the archive files it has just listed. A zip's pending members are read every time the zip comes up, and only listing is once per attempt. Before this fix, the first run on the owner's dataset ended `succeeded` with 352 candidates unchecked: 351 zip members whose sizes a later tar.gz listing shared, and one tar.gz whose own copy was a member of a larger tar.gz listed just before it. Coverage counted them as unchecked, so no claim was wrong (I7), but they waited for the next scan.

**From the UI walkthrough on the owner's dataset (task 8.8, 2026-10-06).** Two agents walked every screen on the reference server, and the coordinator checked their claims against the code. The owner chose to fix, before R2 closes, the findings that make figures disagree or break the keyboard flow (tasks 9.1–9.12). Findings that change what a list means wait for the owner's decisions: Gems' sections, the installers card holding whole download folders, the Map's "duplicated" wording, and the relation counts versus Compare's. Search and Map improvements go to the short change after R2.

- **B16.** An `overlap`'s detail text depends on the side (task 9.1). Side `a`, which has the larger matched share, at least half of it matched, reads "Most of it is also in B". Side `b` reads "Most of A is also in it". One text for both sides was false on the larger side: a 39 GiB camera folder "shared most of its content" with a folder holding 995 MiB of it.
- **B17.** A folder's `candidate_bytes` leave out unreadable files (task 9.2). Unreadable files never become checked, so nine unreadable files kept the folders above them at "49% so far, not everything is checked" for good, while Home said everything was checked. Coverage (D8) still counts them as unreadable, and relations still treat them as gaps (D9, I7). Member folders computed on read also count `sampled` as checked, as entry folders do.
- **B18.** The preview of an entry whose content state or state is `unreadable` says that Precious could not read it, not that it changed on disk (task 9.3). The server answers both with 409 `invalid_entry_state`, and the UI chooses the text from the entry's state.
- **B19.** A zip member's name that is not valid UTF-8 is displayed decoded from code page 850, each `/`-separated component on its own (task 9.4). Zip tools on Portuguese Windows write names in the OEM code page without the UTF-8 flag. APPNOTE names CP437, which turns `õ`, `ã`, and `Õ` into symbols, so CP850 is the better reading for this owner. The stored bytes stay raw, and so do the `*_b64` fields and every identity check (I6), so no listing changes and no migration is needed. Tar names are not decoded, because tars from Linux use other encodings. Decoding at listing time was rejected: it would change stored names, against I6.
- **B20.** A modification time at or before the epoch is unknown (task 9.5). The scanner leaves it out of folder `newest`/`oldest`, and by-year files it under year 0. The API shows that year as `null`, last, so the sums still equal the totals. The API also returns null for such times, while the stored `mtime_ns` stays as the platform gave it. Folders indexed before this change are brought up to date by their next scan. Only the epoch itself is treated this way: it was the one bad value on the owner's dataset, and a 1975 date is not proven wrong.
- **B21.** In review lists, after a key decision removes the selected row, the row that takes its place is selected, so K, K, K decides consecutive rows (task 9.6). J on the last loaded row loads the next page and selects its first row (task 9.7). Before, focus dropped to the page and the next key did nothing until J.
- **B22.** Compare without a `bucket` opens the first bucket that holds files, in this order: only left, only right, different, unchecked, identical (task 9.8). Every `inside` and `same` pair used to open on an empty "only on the left".
- **B23.** An `unpacked_archives` row stores the folder holding the archive's content as `group_id` (task 9.9). The API returns that relation with the row, so the row names the folder and links Compare. `review_rows`' one-of check rules out storing `relation_id` beside `entry_id`.
- **B24.** A duplicates relation row counts files as the Map does, without archive members (task 9.10). `fotos-b` showed 18,605 files in its row and 15,936 in the Map. A group row shows its copies and no file count.
- **B25.** Wording (task 9.11):
  - review summaries list their signals as lower-case nouns ("holds personal material and camera photos");
  - a card whose open rows hold no bytes leads with its row count;
  - Search's `dup=unchecked` choice reads "Not checked or unreadable", which is what it returns (D15).
- **B26.** Copies carry their own decision, so a duplicate group's copies show the active decision button as relation sides do (task 9.12). `eff_decision` alone could not tell an own decision from one inherited from a folder.
