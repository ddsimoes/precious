# Design

## Context

See `proposal.md` for why. M4 (`openspec/changes/archive/2026-10-03-m4-find-copies/design.md`, cited below as "M4 Dn") built the copy-search pipeline in `internal/compare`: list → prepare → hash large → provisional relate → hash small → finish → relate → write results. One search covers one source and runs as one bulk job bound to that source's device. Archives are plain files in it.

Constraints that shape this design:
- The job runner gives each job one device key, computed at claim time from `jobs.Spec.SourceID` (M4 D3 deferred cross-source searches for this reason).
- SQLite tables cannot change a `CHECK` constraint without a rebuild, and `copy_results` is referenced by `disposition_evidence`. Migration `0007` therefore only adds tables and columns.
- The relate snapshot (M4 D11) works on in-memory directory indices. The database `pre` numbers serve only result ranges and freshness (M4 D13).
- The standard library reads zip (store and deflate), tar (including PAX, GNU long names, and sparse members), gzip (multistream), and bzip2. Nothing else is added (ADR 0007).

## Goals / Non-Goals

**Goals:**
- Every supported archive in a search's scope takes part in relations by its unpacked content, within §11.3's budgets, and no claim rests on an archive that was not read completely.
- The largest archives are read at most once per change.
- A search may span sources while the job holds one device at a time.

**Non-Goals:**
- Unpacking anything to disk, even temporarily.
- Path-aware equality of an archive and a folder (M6).
- Opening archives outside copy searches, for example from a peek or a scan.
- Re-verifying bytes before an action (M5).

## Decisions

### D1. Packages and import direction (§8.2)

```text
standard library, domain                          <- compare/unpack   (format readers, paths, budgets; no database, no filesystem)
compare/unpack, domain, store, jobs, fsaccess, ... <- compare          (M4's imports plus unpack)
compare <- commands, web/explorer, cmd/curator                         (unchanged)
jobs: Runtime gains UseSource; no new import
```

- `internal/compare/unpack` is a pure package over `io.Reader` and `io.ReaderAt`. It can be tested with archives built in memory.
- File ownership: `unpack/` (slice U, except `format.go`, which is foundation); `compare/archive.go` (O); `compare/members.go` and the member parts of `hash.go` (H); `relate.go` and `results.go` (E); `start.go` and `list.go` (X); `reader.go`, `archive_reader.go`, and `web/explorer` (V); `evidence.go` and `inventory`'s marked queue (M); `jobs/runtime.go`, `jobs/arbiter.go`, and `jobs/devices.go` (J); `handler.go` (coordinator).
- Rejected: archive readers inside `compare` itself. The readers parse untrusted input and deserve tests that need no database or filesystem fake.

### D2. Formats and detection (§11.3, ADR 0007)

- **The name rule.** `unpack.Classify(name)` strips one trailing `.old`, `.bak`, or `.orig`, then matches the extension, ASCII case-insensitively:
  - zip: `.zip`;
  - tar: `.tar`;
  - tar gzip: `.tar.gz`, `.tgz`;
  - tar bzip2: `.tar.bz2`, `.tbz`, `.tbz2`;
  - gzip: `.gz` (but not the tar variants);
  - bzip2: `.bz2`.
  It returns `not_opened` for `.7z`, `.rar`, `.xz`, `.txz`, `.zst`, and `.tzst`, and "not an archive" otherwise.
- **The lister marks candidates.** It sets `copy_files.archive_state` to `pending` or `not_opened` from the name alone, so the archive phase selects rows by an index rather than scanning names.
- **Signatures confirm the format** on open:
  - zip: the end-of-central-directory record;
  - gzip: `1f 8b`;
  - bzip2: `BZh`;
  - tar: a header whose checksum is valid. This covers v7 headers without `ustar`.
  A mismatch is `unsupported` with detail "not a <format> archive".
- **A single-file gzip or bzip2 archive** holds one member. Its name is the file name without the compression suffix, after the `.old`-style suffix is stripped: `e.sql.gz` holds `e.sql`. The gzip header's stored name is ignored, because it is untrusted.
- Rejected:
  - Sniffing the first bytes of every file. It opens 1.3 million files on the surveyed tree.
  - The extension alone. A renamed non-archive would fail mid-parse and be reported `corrupt`, which misleads.

### D3. Two reading modes

- **Zip: random access.** The central directory gives the member list. Members are unpacked on demand by the hash phase (D9) through `zip.Reader` over a chunked `io.ReaderAt` (D8).
- **Streamed: tar, tar gzip, tar bzip2, gzip, bzip2.** The archive is read once, front to back. Every member is hashed in that pass, together with the SHA-256 of the raw file bytes.
- Plain `.tar` is streamed too. It held 0.4 GB in the survey, and offsets break for sparse members.
- Rejected:
  - Listing a tar gzip in one pass and hashing it in a second. That reads the 105 GB backup twice.
  - Spooling a stream to a temporary file for random access. It writes untrusted content to disk (ADR 0007).

### D4. Member paths and safety (§11.3)

- **Splitting.** A path splits on `/` only. Empty and `.` components are dropped. Components keep their raw bytes; zip names are not decoded from CP437 or UTF-8.
- **Rejected archives.** An archive is `rejected`, naming the first offending member, when:
  - a path starts with `/`;
  - a component is `..`;
  - two members have the same cleaned path;
  - a member is a file at a path that another member uses as a folder.
- A backslash is an ordinary byte, as it is on Linux. A Windows zip with `dir\file.txt` therefore holds one file under that name, which content-based relations do not care about.
- **Implied folders.** Folders implied by member paths are created.
- **Collision detection** keeps a 128-bit truncated SHA-256 per cleaned path: 16 bytes per member, 32 MB at 2 million members. A false collision would only reject an archive, with negligible probability.
- Rejected: Go's `zipinsecurepath` and `tarinsecurepath` settings. They depend on GODEBUG, treat backslashes and Windows reserved names as insecure, and return an error without naming the member.

### D5. Outcomes (§11.3)

| Cause | `archives.state` |
|---|---|
| zip general-purpose flag bit 0 (encryption) on any member | `encrypted` |
| zip method other than store (0) or deflate (8); a multi-disk zip; a signature mismatch | `unsupported` |
| a CRC or size mismatch at the end of a member; a format error; an unexpected end of data; a gzip or bzip2 stream error; a tar hard link to a member not seen before it | `corrupt` |
| a budget reached (D6) | `partial` |
| a path rule (D4) | `rejected` |
| everything read | `complete` |

- **Tar member types:**
  - regular and GNU sparse members are files (sparse holes unpack as zeros, as the logical file does);
  - directories are directories;
  - symlinks keep their link text;
  - hard links keep their target path and take the target member's content;
  - character devices, block devices, FIFOs, and any other type are `special`.
- **Zip member kinds:** a name ending in `/` is a directory; a Unix mode with the symlink bit is a symlink, whose data is its link text; everything else is a file.
- Rejected: comparing the members of an encrypted or damaged archive that could still be read. §11.3 requires distinguishing these containers, and a claim would rest on contents that cannot be trusted.

### D6. Budgets (§11.3)

- **The four budgets** apply to one opening, and also to a zip's later member reads (D9):
  - entries: the zip end-of-central-directory count is checked before the directory is parsed;
  - unpacked bytes;
  - ratio: unpacked bytes may not exceed `archive_max_ratio` × packed bytes read + 64 MiB, checked at every chunk;
  - time: a deadline from the injected clock, checked between chunks.
- **Ratio per opening, not per member.** A mostly-zero disk image inside a backup does not trip the opening's cumulative ratio. A deflate bomb, at up to about 1,000:1, trips it within a few megabytes of packed input.
- **The search's entry budget** (`copies.entry_budget`) counts members as well as listed entries. When it runs out, the remaining archives are `partial` with detail "search entry budget", and the search ends `partial` with `entry_budget`.
- **Nesting** is fixed at one level: an archive member is never opened.
- **A partial opening** keeps its outcome but no member rows. It is not reused, because budgets may have changed.
- Rejected:
  - A per-member ratio (it fails the disk-image case above).
  - A configurable nesting depth: nested zips inside a stream need random access, so a temporary file or an unbounded buffer.

### D7. The archive cache (§5.5.6, ADR 0007)

- **Tables.** `archives` holds one row per opening of an archive file identity `(source_id, source_epoch, dev, ino, size, mtime_ns, ctime_ns)`. `archive_entries` holds its members, with content IDs once hashed.
- **Reuse.** A listed archive whose identity matches a row in state `complete`, `rejected`, `encrypted`, `corrupt`, or `unsupported` takes that row without a read, with outcome `cached` for `complete`. The change time must be non-zero, as for M4 D9.
- **No reuse** for `partial`, for an unstable read (nothing is kept), or for `opening`. An `opening` row is left by an interrupted attempt; the next archive phase of its source deletes it.
- **Zip member digests** live on `archive_entries.content_id`. They stay valid exactly as long as their archive row.
- **Retention.** Prune (slice O) deletes an `archives` row and its entries in batches of 5,000 when no `copy_files` row references it and either:
  - its epoch is older than the source's;
  - a newer row exists for the same `(source_id, source_epoch, dev, ino)`;
  - a complete search of the whole source did not list that `(dev, ino)`.
- The latest row per file identity therefore survives for the inspector (D15), even after the search that made it is superseded.
- Rejected:
  - Member rows per search, as for files (M4 D2). The 105 GB backup would be copied into every search snapshot.
  - Keying by path: same reasons as M4 D9.

### D8. The archive phase (slice O)

- **When it runs:** after listing and before prepare. Per source, in source ID order, it calls `Sources.Use` and then opens the `pending` archives of that source, largest first.
- **Opening a file:** `fsaccess.Dir.OpenFile` with the listed identity (M4 D5). The file is read through a sequential reader built on `File.ReadAt`:
  - at most `copies.read_chunk_bytes` per call, one `rt.FSCall("ReadAt")` each;
  - a `ctx` check between chunks;
  - `rt.Yield` after every `copies.yield_bytes` of packed bytes;
  - a closing `fstat` that must equal the listed identity.
- **Streamed archives:**
  - Entry rows are written in batches of at most 1,000 per transaction, with each file member's content (upserted into `contents`).
  - At the end, folder entries get the sum of the unpacked bytes below them.
  - If the whole file was read and the closing `fstat` matches, the raw-bytes digest goes into `file_digests` in the completing transaction.
- **Zip archives:** the central directory is read and its entries are written without content. The archive is `complete` when the listing finishes.
- **Completion** is one transaction. It sets `archives.state`, and on this search's `copy_files` row `archive_state` and `archive_id`, and it merges the counts.
- **An unstable read** (identity changed at open or at close) deletes the opening's rows. The file row becomes `check_state = 'unstable'`, `archive_state = 'unstable'`.
- **Progress** uses `PhaseArchives`, `archives_total`, `archives_done`, and `archive_bytes_read`. **Counts** use `archives`, `archive_examples`, `members`, `member_bytes`, and `archive_bytes_read` (Interfaces).
- **Cancel** follows M4 D15 after listing: the current archive is abandoned, its `opening` rows are deleted, and the search publishes `partial`.

### D9. Members in the checks (slice H; refines M4 D7)

- **Prepare** builds size groups over two kinds of entries:
  - the search's regular files, by distinct inode, excluding rows whose `archive_state` is `complete` or `cached` (those archives are folders now, not files);
  - the file members of `complete` and `cached` archives, where a hard-linked member counts once per archive.
- So a file whose size occurs only among members is not `distinct_size`.
- **Finish** turns `pending` into `not_checked` only for rows that are not opened archives. Gap counts exclude opened archive rows.
- **Zip members** without content are unpacked under M4 D7's order when their size occurs elsewhere in the search:
  - large members (at or above `copies.large_file_bytes`), largest first;
  - small members only when their archive's directory `pre` lies in a candidate range;
  - no samples, because a deflated member cannot be read in the middle.
  Each zip is opened as in D8 (identity-checked, chunked) and parsed with `unpack.OpenZip`. Digests are committed to `archive_entries.content_id` in batches of at most 64.
- **A member read that fails** marks the archive for this search:
  - corrupt on a CRC or size mismatch;
  - partial at a budget;
  - unstable on an identity change.
  The `archives` row takes the state in the same transaction, except for unstable, which deletes it. The archive is then a plain file for the rest of the search: its file row stays `pending` and ends `not_checked`.
- **Unread members** take keys at relate time, by the file rules: a unique size never matches, and anything else is a `not_checked` gap. There are no per-search member rows (D7).
- Rejected: hashing every zip member at listing. That discards M4's main saving, unique sizes cost nothing, on 91 GB of zips.

### D10. Archives in relations (slice E; refines M4 D11, D12)

- **Splicing.** `LoadSnapshot` splices each `complete` or `cached` archive into the in-memory tree as a folder named like the archive file. It is placed under the archive's directory, after that directory's own subdirectories, with its member folders below it. Its `pre` is the archive's directory `pre`.
- The archive's file entry then contributes no key of its own. An archive in any other state is a plain file entry, by M4's rules.
- **Member keys:**
  - a file with content uses its content;
  - an unread file uses its size, as unique or as a gap (D9);
  - symlinks use their link text;
  - `special` members are gaps;
  - hard-linked members within one archive form an inode group.
- **Matched and total bytes** count members at their unpacked size.
- **Freeable bytes.** An archive side frees its file size when every member's key occurs under the partner. A folder side adds that size for each archive below it that is fully matched. A side inside an archive frees nothing.
- **Which side is the copy:**
  - in a `same` pair of an archive and a folder, the archive (owner's choice);
  - between two folders or two archives, M4 D30 applies;
  - in a file result, an occurrence inside an archive is the copy whenever an occurrence outside archives exists. This matches the owner's preference to keep unpacked files.
- **Candidate ranges** of a spliced folder are its archive directory's `pre..pre`. They select that directory's archives in D9, which is a superset and therefore safe.
- **Hard links** match across sources by `(dev, ino)` (M4 D10), so two sources on one filesystem see one inode.
- Rejected: counting an opened archive both as a folder and as a file. Its whole-file key would make its directory need an identical archive file elsewhere, which hides the "unpacked elsewhere" case the owner asked for.

### D11. Result sides (slice E)

- **The side's file.** A side in or of an archive stores the archive file as `copy_path`, `copy_kind = 'file'`, and `copy_dir_pre` = the archive directory's `pre`. `copy_inner_kind` is `archive`, `directory`, or `file`, and `copy_inner_path` is the cleaned path inside the archive (empty for `archive`). The `other_*` columns work the same way.
- **Node and unit.** An `archive` side has `copy_node_id` = the archive's node when the archive file is cataloged; otherwise `copy_unit_id` is the nearest linked ancestor. A `directory` or `file` inner side has no node: its unit is the archive's node, or the nearest linked ancestor.
- **Freshness** of every archive side is the freshness of the archive's file row, through M4 D13's file-side check on `copy_dir_pre` and the name. A rewrite of a cataloged archive therefore reads `changed`.
- **Sources.** `copy_source_id` and `other_source_id` name each side's source. `source_changed` applies when either side's source epoch differs from the search's epoch for that source.

### D12. Searches across sources (slice X, coordinator)

- **Scope.** A scope is a set of whole sources and a set of folders, from one or more sources. A folder of a source that is also listed whole is dropped, as is a folder below another listed folder (M4 D3). The response lists the roots kept.
- **Single flight** is checked in `find-copies`'s `jobs.Tx`. If any named source belongs to a search in a non-terminal state, that search is returned with `coalesced: true`. The job is enqueued with `Tx.Enqueue`, without a scope key.
- `copy_search_sources` records each source with its epoch at start. `copy_searches.source_id` and `source_epoch` keep the first source by ID. That source is the job's `SourceID`, and so its first device.
- **Phases** that touch the filesystem (list, archives, hash large, hash small) run per source in source ID order, after `Sources.Use(source)`. The handler's `Sources` opens each source's scan at first use in an attempt (`BeginInspection` after `UseSource`), checks its epoch, and closes all scans at the end.
- **Supersession.** A search that ends `complete` or `partial` supersedes every older search that shares a source with it (refines M4 D14). Snapshot pruning follows supersession.
- **The inspector's Copies section** reads the latest `complete` or `partial` search covering the node's source. This matches M4's code; the main spec said "complete", and the delta corrects it.
- Rejected:
  - One job per source with a barrier between phases: several jobs, cancels, and progress records for one search.
  - A job that holds every device at once: it would starve intake on all of them (A36).
  - A scope key per source: `EnqueueOnce` takes one key.

### D13. `Runtime.UseSource` (slice J; §8.4, A36)

- **The call.** `UseSource(ctx, source)` computes the source's claim key exactly as a claim does (`dev:<identity_dev>`, or the placeholder `source:<id>` while the device is unrecorded; `jobs/runner.go`, `claimKey`).
- If the key equals the attempt's current key, it returns nil at once.
- Otherwise it releases the attempt's slot and becomes a waiter for the new key, in the job's class and as of now. The arbiter grants it like any waiter (4:2:1 rotation, aging). On the grant it records the key in `jobs.device_key` and the attempt's slot, and returns.
- **A placeholder key** also requires that no other running attempt holds that source, as a placeholder claim does.
- **Cancellation.** If `ctx` ends while waiting, it returns `ctx.Err()` holding no slot. The runner's finish then releases nothing.
- **Watchdog.** After a move, `FSCall` watches against the new source: an overdue call marks that source unresponsive.
- Rejected: changing a job's source in the database so that it is claimed again. That needs a new attempt and loses the handler's in-memory state.

### D14. `find-copies` request (slice X; refines M4 Commands)

- **Request:** `{"source_ids": ["disk", "old-disk"], "node_ids": ["12"]}`.
  - At least one list is non-empty.
  - `source_ids` holds configured sources, without duplicates.
  - `node_ids` holds at most `discovery.page_size` IDs, without duplicates.
- The M4 body `{"source_id": …}` is removed (clean cutover). The only caller is `app.js`.
- **Response 202:** `{"job_id", "state", "coalesced", "search_id", "sources": [...], "roots": [{"source_id", "node_id", "path", "path_b64"}]}`.
- **Errors:** as in M4, plus `404 unknown_source` for an unknown ID in `source_ids`.

### D15. Archive contents for the inspector (slice V)

- **Finding the row.** `Reader.ArchiveContents(node, folder, cursor, limit)` reads the node, which must be an active file, and its source's current epoch. It takes the latest `archives` row for `(source, epoch, node.dev, node.ino)`.
- **No row:** `never_opened`.
- **`changed_since`** is true when `node.size` or `node.mtime_ns` differs from the row. The catalog keeps no change time.
- **Listing.** Folder `0` is the archive's top level. Items are the folder's children, ordered by raw name and paged by name (cursor version byte `0x55`), 100 per page. Folders carry their unpacked bytes below.
- **Other states** show their outcome, detail, and offending member, with no items.
- **Display.** The inspector's Contents section shows on file nodes that `unpack.Classify` names an archive, or that have a row.

### D16. Evidence (slice M; refines M4 D17)

- **The check.** `CheckEvidence` refuses a side with `inner_kind` `directory` or `file` (`invalid_request`: "the copy is a part of `<archive>` and cannot be marked on its own"). An `archive` side is marked through the archive file's node, like a file.
- **The detail** gains `copy_source_id`, `other_source_id`, and `copy_inner` and `other_inner` (`{"kind", "path_b64"}` or null).
- **The marked queue** shows an `other_inner` path after the archive path.

### D17. Configuration (§5.1; refines M4 D20)

| Key | Default | Range |
|---|---|---|
| `archive_max_entries` | 1,000,000 | 1 to 100,000,000, and at most `entry_budget` |
| `archive_max_bytes` | 1 TiB | 1 MiB to 1 PiB |
| `archive_max_ratio` | 100 | 2 to 100,000 |
| `archive_max_time` | 4h | 1m to 7d |

- Fixed in code, each documented:
  - the 64 MiB ratio grace;
  - one nesting level;
  - 1,000 entry rows per transaction;
  - 64 member digests per commit;
  - the `.old`, `.bak`, `.orig` suffixes.
- Rejected: a configurable extension list. A new extension needs a reader, not a setting.

### D18. Migration `0007_archive_copies.sql` (§8.3)

- **New tables:** `archives`, `archive_entries`, `copy_search_sources`.
- **New columns:**
  - `copy_search_roots.source_id`;
  - `copy_files.archive_state` and `archive_id`;
  - six `copy_results` columns: `copy_source_id`, `other_source_id`, `copy_inner_kind`, `copy_inner_path`, `other_inner_kind`, `other_inner_path`.
- **Existing rows are filled:** one `copy_search_sources` row per search, root sources from their nodes, and result sources from their search.
- Nothing is rebuilt or dropped.
- Rejected: rebuilding `copy_results` to extend `copy_kind`. Its rows are referenced by `disposition_evidence`, and SQLite rebuilds under foreign keys need the pragma outside the migration transaction.

### D19. Error codes (§9.3)

No new code:
- an inspector archive request for a directory or symlink node is `409 invalid_node_state`;
- an unknown entry is `404 not_found`;
- a malformed cursor or entry is `400 invalid_request`;
- evidence for an inner side is `400 invalid_request`.

### D20. Choices confirmed with the owner (2026-10-04)

- Formats: standard library only (zip, tar, tar gzip, tar bzip2, gzip, bzip2). 7z and rar are counted as not opened.
- Every copy search opens every supported archive in its scope.
- An archive's inspector shows its contents as the last search found them.
- In a `same` pair of an archive and a folder, the archive is the copy.
- Defaults recorded here for review: nested archives are never opened (D6); jar, docx, and similar are plain files (D2); a file result prefers an occurrence outside archives as the kept side (D10).

## Interfaces

### Schema (`0007_archive_copies.sql`)

```sql
CREATE TABLE copy_search_sources (
    search_id    INTEGER NOT NULL REFERENCES copy_searches (id),
    source_id    TEXT NOT NULL REFERENCES sources (id),
    source_epoch INTEGER NOT NULL,
    PRIMARY KEY (search_id, source_id)
);
CREATE INDEX copy_search_sources_source ON copy_search_sources (source_id, search_id);
INSERT INTO copy_search_sources (search_id, source_id, source_epoch)
    SELECT id, source_id, source_epoch FROM copy_searches;

ALTER TABLE copy_search_roots ADD COLUMN source_id TEXT REFERENCES sources (id);
UPDATE copy_search_roots SET source_id = (SELECT n.source_id FROM nodes n WHERE n.id = copy_search_roots.node_id);

-- One opening of one archive file identity (design D7).
CREATE TABLE archives (
    id             INTEGER PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources (id),
    source_epoch   INTEGER NOT NULL,
    dev            INTEGER NOT NULL,
    ino            INTEGER NOT NULL,
    size           INTEGER NOT NULL,
    mtime_ns       INTEGER NOT NULL,
    ctime_ns       INTEGER NOT NULL,
    format         TEXT NOT NULL CHECK (format IN ('zip', 'tar', 'tar_gzip', 'tar_bzip2', 'gzip', 'bzip2')),
    state          TEXT NOT NULL CHECK (state IN ('opening', 'complete', 'partial', 'rejected',
                       'encrypted', 'corrupt', 'unsupported')),
    detail         TEXT,
    member_path    BLOB,               -- first offending member, cleaned
    entries        INTEGER NOT NULL DEFAULT 0,
    unpacked_bytes INTEGER NOT NULL DEFAULT 0,
    search_id      INTEGER NOT NULL REFERENCES copy_searches (id),
    attempt        INTEGER NOT NULL,
    opened_at      INTEGER NOT NULL,
    finished_at    INTEGER
);
CREATE INDEX archives_identity ON archives (source_id, source_epoch, dev, ino, id);

CREATE TABLE archive_entries (
    id         INTEGER PRIMARY KEY,
    archive_id INTEGER NOT NULL REFERENCES archives (id),
    parent_id  INTEGER REFERENCES archive_entries (id), -- NULL: top level
    name       BLOB NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('directory', 'file', 'symlink', 'hardlink', 'special')),
    size       INTEGER,  -- file: unpacked size; directory: unpacked bytes below
    mtime_ns   INTEGER,
    link_text  BLOB,     -- symlink text; hard link: the target's cleaned path
    link_id    INTEGER REFERENCES archive_entries (id), -- hard link target
    locator    INTEGER,  -- zip: central-directory index
    content_id INTEGER REFERENCES contents (id)
);
CREATE INDEX archive_entries_parent ON archive_entries (archive_id, parent_id, name);
CREATE INDEX archive_entries_size ON archive_entries (archive_id, size) WHERE kind = 'file';
-- Apply-time addition: the self-referencing foreign keys need their own
-- indexes, or every deleted entry scans the table.
CREATE INDEX archive_entries_parent_ref ON archive_entries (parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX archive_entries_link_ref ON archive_entries (link_id) WHERE link_id IS NOT NULL;

ALTER TABLE copy_files ADD COLUMN archive_state TEXT CHECK (archive_state IN ('pending', 'complete',
    'cached', 'partial', 'rejected', 'encrypted', 'corrupt', 'unsupported', 'unstable', 'not_opened'));
ALTER TABLE copy_files ADD COLUMN archive_id INTEGER REFERENCES archives (id);
CREATE INDEX copy_files_archives ON copy_files (search_id, attempt) WHERE archive_state IS NOT NULL;
CREATE INDEX copy_files_archive_ref ON copy_files (archive_id) WHERE archive_id IS NOT NULL;

ALTER TABLE copy_results ADD COLUMN copy_source_id TEXT REFERENCES sources (id);
ALTER TABLE copy_results ADD COLUMN other_source_id TEXT REFERENCES sources (id);
ALTER TABLE copy_results ADD COLUMN copy_inner_kind TEXT CHECK (copy_inner_kind IN ('archive', 'directory', 'file'));
ALTER TABLE copy_results ADD COLUMN copy_inner_path BLOB;
ALTER TABLE copy_results ADD COLUMN other_inner_kind TEXT CHECK (other_inner_kind IN ('archive', 'directory', 'file'));
ALTER TABLE copy_results ADD COLUMN other_inner_path BLOB;
UPDATE copy_results SET
    copy_source_id = (SELECT s.source_id FROM copy_searches s WHERE s.id = copy_results.search_id),
    other_source_id = (SELECT s.source_id FROM copy_searches s WHERE s.id = copy_results.search_id);
```

- New code writes `copy_search_roots.source_id`, `copy_results.copy_source_id`, and `other_source_id` on every row and treats them as required.
- `copy_inner_path` is set exactly when `copy_inner_kind` is; the same holds for `other_*`.

JSON columns that other slices read:
- **`copy_searches.counts`** gains these keys, written by O and H and read by V and integration:
  - `{"archives": {"pending", "complete", "cached", "partial", "rejected", "encrypted", "corrupt", "unsupported", "unstable", "not_opened"}}`;
  - `{"archive_examples": [{"outcome", "source_id", "path_b64", "member_b64" | null, "detail"}]}`, at most 20;
  - `"members"`, `"member_bytes"`, `"archive_bytes_read"`;
  - `gap_examples` items gain `"source_id"`.
  - Keys absent before their phase read as 0.
- **`disposition_evidence.detail`** gains `"copy_source_id"`, `"other_source_id"`, and `"copy_inner"` and `"other_inner"`: `{"kind": "archive" | "directory" | "file", "path_b64"}` or null. Written by M, read by `inventory` and V.

### Go signatures (foundation unless marked)

```go
// internal/domain
type ArchiveFormat string // ArchiveZip "zip", ArchiveTar "tar", ArchiveTarGzip "tar_gzip",
                          // ArchiveTarBzip2 "tar_bzip2", ArchiveGzip "gzip", ArchiveBzip2 "bzip2"
type ArchiveState string  // ArchiveOpening, ArchiveComplete, ArchivePartial, ArchiveRejected, ArchiveEncrypted,
                          // ArchiveCorrupt, ArchiveUnsupported (archives.state), plus ArchivePending, ArchiveCached,
                          // ArchiveUnstable, ArchiveNotOpened (copy_files.archive_state only)
func (s ArchiveState) Opened() bool // complete or cached: the archive is a folder in relations
type MemberKind string    // MemberDirectory, MemberFile, MemberSymlink, MemberHardlink, MemberSpecial
type InnerKind string     // InnerArchive "archive", InnerDirectory "directory", InnerFile "file"

// internal/config
type Copies struct {
	// existing fields unchanged
	ArchiveMaxEntries int64    `toml:"archive_max_entries"`
	ArchiveMaxBytes   int64    `toml:"archive_max_bytes"`
	ArchiveMaxRatio   int64    `toml:"archive_max_ratio"`
	ArchiveMaxTime    Duration `toml:"archive_max_time"`
}

// internal/jobs (interface: foundation; runner implementation: slice J)
type Runtime interface {
	// existing methods unchanged
	// UseSource moves the job to source's device (design D13). Call it only
	// at a work-unit boundary with no transaction open.
	UseSource(ctx context.Context, source domain.SourceID) error
}

// internal/compare/unpack (format.go: foundation; the rest: slice U)
type Class int // NotArchive, Supported, NotOpened
func Classify(name []byte) (domain.ArchiveFormat, Class)
func MemberName(archiveName []byte) []byte // gzip, bzip2: the single member's name (design D2)
type Limits struct {
	MaxEntries, MaxBytes, MaxRatio int64
	Deadline                       time.Time
	Now                            func() time.Time
}
const RatioGrace = 64 << 20
type Member struct {
	Path     [][]byte // cleaned components (design D4)
	Kind     domain.MemberKind
	Size     int64     // unpacked; 0 unless Kind is MemberFile
	Mtime    time.Time // zero when absent
	LinkText []byte    // symlink text; hard link: the target's cleaned path joined by '/'
	Index    int       // zip: central-directory index; -1 otherwise
}
// Stop ends an opening with a state other than complete; Member is the
// first offending member's cleaned path joined by '/', or nil.
type Stop struct {
	State  domain.ArchiveState
	Detail string
	Member []byte
}
func (s *Stop) Error() string
// Stream reads a streamed-format archive from r, which yields its packed
// bytes in order. It calls visit once per member in archive order. data
// holds a file member's unpacked bytes and is nil otherwise; Stream drains
// what visit leaves unread. It returns nil when complete, a *Stop for an
// archive outcome, or ctx.Err() or visit's error.
func Stream(ctx context.Context, f domain.ArchiveFormat, name []byte, r io.Reader, lim Limits,
	visit func(m Member, data io.Reader) error) error
type Zip struct{ /* unexported */ }
// OpenZip checks the entry count before parsing the central directory, then
// the D4 and D5 rules for every member; it returns a *Stop for those outcomes.
func OpenZip(r io.ReaderAt, size int64, lim Limits) (*Zip, error)
func (z *Zip) Members() []Member
// Open returns the member's unpacked bytes. Reading reports a *Stop: corrupt
// on a CRC or size mismatch, partial at a budget (cumulative per Zip).
func (z *Zip) Open(index int) (io.ReadCloser, error)

// internal/compare
type SearchSource struct { Source domain.SourceID; Epoch int64 }
type Search struct {
	ID      domain.CopySearchID
	Sources []SearchSource // by source ID; Sources[0] is the job's source
	Attempt int64
	State   domain.CopySearchState
	Roots   []Root // by source ID, then path
	Job     domain.JobID
}
func (s Search) Epoch(source domain.SourceID) (int64, bool)
type Root struct { Node domain.NodeID; Source domain.SourceID; Path []byte }
// Sources gives a phase one source's scan after moving the job to its device.
type Sources interface {
	Use(ctx context.Context, source domain.SourceID) (sources.Scan, error)
}
const PhaseArchives = 6
const ProgressArchivesTotal, ProgressArchivesDone, ProgressArchiveBytesRead = "archives_total", "archives_done", "archive_bytes_read"
func CheckLive(ctx context.Context, tx *sql.Tx, search Search) error // every source's epoch, attempt, state

// slice X (start.go, list.go)
type Scope struct { Sources []domain.SourceID; Folders []domain.NodeID }
type Started struct {
	Search    domain.CopySearchID
	Job       jobs.Record
	Coalesced bool
	Sources   []domain.SourceID
	Roots     []Root
}
func (s *Service) Start(ctx context.Context, tx *jobs.Tx, scope Scope) (Started, error)
func (s *Service) List(ctx context.Context, rt jobs.Runtime, src Sources, search *Search) (Listed, error)

// slice O (archive.go)
type Opened struct { Archives, Cached, Read, BytesRead, Members int64; Stop ListStop }
func (s *Service) OpenArchives(ctx context.Context, rt jobs.Runtime, src Sources, search Search) (Opened, error)
func (s *Service) PruneArchives(ctx context.Context, rt jobs.Runtime, search Search) error

// slice H (hash.go, members.go): signatures as M4 except
func (s *Service) Hash(ctx context.Context, rt jobs.Runtime, src Sources, search Search, sel Selection) (Hashed, error)
// Hashed gains MembersRead, MemberBytesRead int64.

// slice E (relate.go, results.go)
type Inner struct { Kind domain.InnerKind; Path []byte }
// Side gains Source domain.SourceID and Inner *Inner (nil outside archives).

// slice V (reader.go, archive_reader.go)
// SideView gains Source domain.SourceID and Inner *InnerView{Kind domain.InnerKind; Path []byte}.
// SearchView gains Sources []domain.SourceID, Archives map[domain.ArchiveState]int64, ArchiveExamples []ArchiveExample.
type ArchiveExample struct { Outcome domain.ArchiveState; Source domain.SourceID; Path, Member []byte; Detail string }
type EntryCursor struct { AfterName []byte }
type ArchiveItem struct { Entry int64; Name []byte; Kind domain.MemberKind; Size *int64; Mtime *time.Time }
type ArchiveListing struct {
	State         domain.ArchiveState // "never_opened" when no row exists
	Format        domain.ArchiveFormat
	OpenedAt      *time.Time
	ChangedSince  bool
	Detail        string
	Member        []byte
	Entries       int64
	UnpackedBytes int64
	Folder        int64  // 0: top level
	FolderPath    []byte
	Parent        *int64 // nil at top level
	Items         []ArchiveItem
	Next          *EntryCursor
}
const ArchiveNeverOpened domain.ArchiveState = "never_opened"
func (r *Reader) ArchiveContents(ctx context.Context, node domain.NodeID, folder int64, after *EntryCursor, limit int) (ArchiveListing, error)
// Searches(source) lists searches covering source through copy_search_sources.

// slice M (evidence.go)
// EvidenceDetail gains CopySourceID, OtherSourceID string and CopyInner, OtherInner *InnerDetail.
type InnerDetail struct { Kind domain.InnerKind `json:"kind"`; PathB64 []byte `json:"path_b64"` }
```

### Commands

- **`find-copies`** (X): see D14.
- **`set-disposition`** (M): evidence for a side inside an archive returns `400 invalid_request` for a single item. In a bulk request it fails the whole request with 400, as M4's wrong-side case does.

### Read endpoints (V)

- **`SearchJSON`** gains:
  - `"sources": ["disk", ...]`;
  - `"archives": {outcome: count}`;
  - `"archive_examples": [{"outcome", "source_id", "path", "path_b64", "member", "member_b64", "detail"}]` (detail endpoint only).
  `roots` items gain `"source_id"`. `GET /api/copy-searches?source=` lists searches covering the source.
- **`SideJSON`** gains `"source_id"` and `"inner": {"kind", "path", "path_b64"} | null`.
- **`GET /api/nodes/{id}/archive?entry=&cursor=`** returns `{"state", "format", "opened_at", "changed_since", "detail", "member", "member_b64", "entries", "unpacked_bytes", "folder": {"entry_id", "path", "path_b64", "parent_entry_id" | null}, "items": [{"entry_id", "name", "name_b64", "kind", "size" | null, "mtime" | null}], "next"}`.
  - `entry` defaults to `0` (top level).
  - Errors: `404 not_found` (node or entry), `409 invalid_node_state` (not an active file), `400 invalid_request` (malformed entry or cursor).
- **Pages:**
  - `/copies`: a "Find copies across sources" form with one checkbox per configured source, shown when there are two or more.
  - `/copies/{id}`: archive outcomes with examples; sides as `archive › inner path` with an archive badge and the source when the search covers several. An inner copy side gets the hint "a part of an archive cannot be removed on its own".
  - The inspector's Contents section (D15).
  - `app.js` sends the D14 body.

## Transaction boundaries

| Path (transaction) | Re-checked inside the writing transaction | Another command commits just before | …just after |
|---|---|---|---|
| `find-copies` (`jobs.Tx`) | each source configured; each folder active, a directory, not a mount boundary, of a configured source; no non-terminal search shares a source (else coalesce); search, sources with current epochs, and roots inserted; `Enqueue` | a collapse deactivates a folder: `invalid_node_state`. A search on a shared source starts: coalesced | a collapse: the listing's chain open fails, `listing_failed` gap |
| Listing batch (`store.Write`) | M4's checks, with each row's source epoch | as M4 | as M4 |
| Archive entry batch (`store.Write`, ≤ 1,000 rows) | the archive's source epoch; attempt current; the `archives` row is `opening` and belongs to this search and attempt | epoch change: the search fails; `opening` rows are deleted by the next archive phase of the source, or by Prune | the archive is rewritten: the closing `fstat` fails, so the rows are deleted and the file is `unstable` |
| Archive completion (`store.Write`) | epoch; attempt; the closing `fstat` matched (checked just before); sets `archives.state`, folder sizes, the `copy_files` outcome and `archive_id`, the `file_digests` row, and counts | as above | a rewrite after completion: the next search misses the cache by change time; a scan of a cataloged archive makes results read `changed` |
| Member digest commit (`store.Write`, ≤ 64) | epoch; attempt; the `archives` row is still `complete` with the same ID; `contents` upsert | epoch change: the search fails, nothing is committed for the batch | as above |
| Member failure (`store.Write`) | epoch; attempt; sets `archives.state` (or deletes the row when unstable) and this search's `copy_files` outcome | as above | the archive is a plain file for this search |
| `WriteResults` | every source epoch; attempt; state not terminal; in the final transaction, older searches sharing a source are superseded | epoch change: `failed` | a new `find-copies` on a shared source starts only after this job ends |
| Prune (≤ 5,000 rows) | snapshot rows of superseded searches; `archives` rows by D7 that no `copy_files` row references | none matter | a new search only references rows that are latest and valid, which Prune keeps |
| `UseSource` (runner) | the attempt still holds its lease; the arbiter grants under `Runner.mu`; `jobs.device_key` is updated with the attempt's next progress flush | a cancel: `ctx.Err()`, no slot held | n/a |
| `set-disposition` with evidence (`jobs.Tx`) | M4's checks, plus the side is not inside an archive | as M4 | as M4 |
| Reads (`store.Read`) | one snapshot per request | n/a | n/a |

## Risks / Trade-offs

- **[A first search of the surveyed tree reads about 320 GB of archives]** on top of M4's reads. → Largest first, per-archive progress, cancel publishes `partial`, and finished archives are never read again (D7).
- **[Zip central-directory memory]**: `archive/zip` keeps every header, about 300 bytes each. At the default of 1,000,000 entries that is about 300 MB. → The entry count is checked before parsing (D6). The limit and its memory are documented.
- **[A zip found corrupt only when a member is read]** turns into a plain file mid-search. Work spent on its members' size peers is wasted. → No wrong claim results, and the count shows `corrupt`.
- **[An archive whose bytes exist elsewhere under a name that is not an archive name]** is a folder in relations, so the identical file is missed. → A missed copy, never a false one. Streamed archives still record their whole-file digest.
- **[bzip2 is slow]** (tens of MB/s). → 0.4 GB in the survey, and the time budget bounds it.
- **[Archive listings in the database]**: about 2 million rows for the surveyed tree. → One opening is kept per file identity (D7), and the upgrade note states the size.
- **[Inner sides cannot be marked]**, so a backup only partly elsewhere cannot be marked part by part. → The owner marks whole archives or folders. Splitting an archive is M5 plan work.
- **[Time budgets]** depend on host speed. → The injected clock keeps tests deterministic. The default of 4 hours covers the 105 GB backup at about 10 MB/s.

## Migration Plan

1. Back up with `curator backup`.
2. On first start, migration `0007` runs. It adds tables and columns and fills the source of existing searches, roots, and results. The M4 binary refuses the newer schema.
3. Nothing changes until the owner starts a copy search. Existing results keep their freshness. Their sides have no inner path.
4. **Rollback:** stop the server, restore the backup, and start the M4 binary.

## Addendum: decisions made during apply

Added at archive (2026-10-05). These decisions were made or confirmed while M4b was implemented, and the code on `main` follows them. None departs from `directory-first-curator-spec-v0.2.md`: the M4b departures (no nested archives; 7z, rar, xz, and zstd not opened) are ADR 0007, recorded at proposal. D30's not-opened examples, D33, and D38 were found by the integration tests and the browser pass; the other decisions came from the slices and were approved during apply.

### Archive readers (slice U)

**D21. Member paths and repeats (refines D4).** Two directory members may share a path: a directory carries no content. Any other repeated path, or a non-directory at a folder's path, rejects the archive. A directory member with an empty path (`./`) is the top level: it is not reported but counts against the entry budget, and a non-directory with an empty path is rejected. Tar pax global headers (`g`) and GNU volume labels (`V`) are skipped, neither members nor counted. `Stop.Member` keeps a leading `/` and `..` components as written.

**D22. What the readers report (refines D5, D6).**
- A single-file gzip or bzip2 member has `Size` -1 until its end; the caller counts the bytes.
- The `Stop.Detail` strings are fixed: `entry budget`, `unpacked byte budget`, `ratio budget`, `time budget`, `member path leaves the archive`, `two members share a path`, `a member is both a file and a folder`, `member is encrypted`, `not a <format> archive`, `compression method <n> is not supported`, `multi-disk zip`, `checksum mismatch`, `size mismatch`, `unexpected end of data`, `hard link to an unknown member`, and `<format> format error: …`.
- `MaxEntries` counts every row an opening would write (members and the folders they newly imply). The zip's declared count is still checked first, as a lower bound.
- `archive/zip` reads its `ReaderAt` in 4 KiB pieces, so archive files are read through a read-ahead window (D29).

### Jobs and searches across sources (slices J and X, coordinator)

**D23. `Runtime.UseSource` (refines D13).** Each `FSCall` watches the source that was current when the call started, and the watchdog flags that source. A moved attempt counts as running in no source until it is granted, then counts as a claim does; a move to a device key also waits while that source is exclusive to a placeholder. The attempt's source and device key change only when `UseSource` returns nil. Jobs without a source may move; pool kinds may not. An unknown source is `unknown_source`. `jobs.device_key` is written by the next progress flush and by finish.

**D24. Checking across sources.** `Hash` runs per source in source ID order, after `Sources.Use`. A source with nothing selected is skipped without moving the job. Size peers span every source, and each run reads only its own source's `pre` range. A sample group with an unsampled file on another source calls no file distinct: those files are read in full, which costs reads but is never wrong. `LoadSearch` orders roots by `source_id`, path, and `node_id`, and the lister sorts the same way, so each source's snapshot is one contiguous `pre` range. Supersession and pruning follow shared sources.

**D25. `find-copies` coalescing (refines D14).** A request joins only a non-terminal search whose job is queued, running, or paused, the oldest (smallest ID) first. Resuming a paused job is the only write a coalesced request makes. On the path that starts a new search, a non-terminal search on a requested source whose job has ended is ended first: `cancelled`/`cancelled` when its job was cancelled, else `failed`/`error` "its job ended before the search finished". `discovery.page_size` counts the requested `node_ids` before drops, and a folder of a source listed whole is validated before it is dropped. `ScopeKey` is removed; the job is enqueued with `Enqueue`.

### The archive phase and cache (slice O)

**D26. Indexes (refines D18).** Migration `0007` also indexes `archive_entries.parent_id` and `link_id` (`archive_entries_parent_ref`, `archive_entries_link_ref`). Without them every deleted entry scans the table to check its self-references, which made pruning quadratic.

**D27. Opening and failing (refines D7, D8).**
- The `archives` row is inserted before the file is reached, and deleted again when the file or its folder turns out unstable or unreadable.
- An I/O failure that is not a change is classified as `Hash` classifies it: the file becomes `unreadable` with Hash's detail and `archive_state` `unstable`, the opening is deleted, and the file's digest-cache row is kept. A source root that no longer answers fails the phase with `source_unavailable`.
- An error other than a cancel or a file outcome (an epoch change, a database error) leaves the `opening` rows for the next archive phase or `PruneArchives`; a cancel deletes the opening itself.
- The cache lookup takes the newest row by ID with the full identity, a reusable state, and a non-zero `ctime_ns`.

**D28. The search's entry budget (refines D6).** The archive being read when the budget runs short opens with `MaxEntries` = min(`archive_max_entries`, budget left), and its stop is recorded as `partial`/`search entry budget`. Later candidates become `partial` with no row and no read, and a cache hit whose entries would pass the budget is a stop too. The detail lives in the archive examples and the stopped row, not in `copy_files.detail`, which belongs to the check state.

**D29. Reading and listing (refines D3, D8).**
- One read-ahead reader (`chunkFile`) serves both the archive phase and `Hash`'s zip members. A zip that grew past its listed size fails as `unstable` at the read that sees it.
- The whole-file digest is written whenever every byte went through SHA-256 in order, the end of the file was confirmed, and the closing `fstat` matched, also for stopped archives and for small zips read whole.
- Zip sizes come from the central directory (`archive/zip` verifies them when a member is read); the streamed formats use counted bytes.
- A tar hard link to a hard link points `link_id` at the member holding the content, and `link_text` keeps the target the tar names.
- Implied folders have no time and no locator; a directory member for an existing folder only fills in a missing time; zip directory members keep their locator.

**D30. Counts and examples (refines D8, Interfaces).** `counts.archives` holds absolute totals, `pending` included, recomputed by `GROUP BY` over the attempt in every writing transaction (the archive phase and `Hash` alike). `members` and `member_bytes` come from the `complete` and `cached` rows. `archive_examples` holds at most 20 items; an unstable or unreadable archive file adds a gap example too. When a search ends, `Finish` names the `not_opened` files in path order in the places the list has left, with the detail "7z, rar, xz, and zstd archives are not opened", so they never push out an archive that failed (spec inventory-explorer "Archive outcomes are visible").

**D31. Retention (refines D7).** Rule 3 of `PruneArchives` uses the latest complete, not superseded search whose roots include the source root, over its current attempt's file rows. An archive in a folder that search could not list may lose its cache row, which costs a read again, never a wrong result.

### Members in the checks (slice H)

**D32. Members (refines D9).**
- An unstable member read on an opening that other searches reference turns this search's file row `unstable` with `archive_id` NULL; the `archives` row is deleted only when nothing references it. For a failure that is not a change, a lost root is `source_unavailable`; otherwise the file row is `unreadable` or `unstable` by Hash's rules, `archive_state` is `unstable`, and the `archives` row is kept.
- Members are selected by the files' rule. A size with a member peer is never sampled. The large phase reads zip by zip, largest selected member first.
- `candidates_large` and `candidates_small` count members too. A cancel abandons the member digests not yet committed; they are read again by the next search.
- A member whose unpacked size differs from its entry row, or a zip file entry without a locator, fails the call: these break an invariant and are not archive outcomes.

### Relations and results (slice E, coordinator)

**D33. Keys of the candidate pass (refines D10 and M4 D11).** `LoadSnapshot(ctx, q, search)` loads the final snapshot and `LoadProvisional(ctx, q, search, largeFileBytes)` the candidate pass. In the provisional snapshot, pending regular files, file members without content, and file members smaller than `copies.large_file_bytes` are keyed by their size, even when hashed. A streamed archive's one pass hashes every member while the small regular files are still unread, so content keys never met size keys and a `.tar.gz` of small files was never paired with its unpacked folder.

**D34. Relating archives (refines D10).**
- Lifting never leaves an archive. A folder holding nothing but one archive, directly or through folders holding nothing else, is named by that archive. A member folder that lifts to the whole archive has its match recomputed, so the packed size is freed.
- A part of an archive is the copy against a folder in a `same` pair, like the whole archive, and parts free nothing.
- An archive frees its packed size only when every member key occurs under the partner (content as another inode, a link by its text); a size key, a unique member, or a gap frees nothing.
- A member's inode identity is (archive device, inode, member ordinal), so two names of one opening match each other and free nothing.
- Counting sizes for unread members: an opening counts once even under two names, hard-link members are not counted, and regular files count per name.
- Ranking ties also compare the copy's and the other side's inner paths.
- `LoadSnapshot` fails on directories out of depth-first order, and on an opened archive without its `archives` row.

**D35. Sides and their sources (refines D11).** A whole-archive side has `Inner{archive, ""}`, stored as `X''`. `WriteResults` refuses a side whose source the search does not cover. Side sources are required on every read path (the reader, `CheckEvidence`, `ResultFreshness`): a NULL is a data error, never filled from the search's first source, since migration `0007` fills the rows written before it. The reader applies `source_changed` per result, from its two sides' sources.

### Pages and marks (slices V and M, coordinator)

**D36. Pages (refines D15).**
- The inspector's folder control sends `{"source_ids": [], "node_ids": [id]}`: naming its source too would list the whole source.
- With no source ticked, the form across sources says "Choose at least one source." and sends nothing.
- A copy inside an archive gets the hint `archive_part`, whose unit is the archive's node (or the nearest linked ancestor): "a part of `<archive>` cannot be removed on its own". `InnerView.Archive` lets pages link the archive.
- The Contents section appears for a supported archive name, or when an opening exists. The node page takes `?entry=` and `?entry_cursor=` (`cursor` already pages the children); the API takes `?entry=&cursor=`. `ArchiveContents` answers `invalid_request` for a negative folder, and `not_found` for a folder of a missing or incomplete opening. `changed_since` is true when the node's size or modification time is unknown.
- The outcome block lists complete, cached, partial, rejected, encrypted, corrupt, unsupported, unstable, not_opened, and pending, in that order; the API's `archives` map carries all ten states.
- Single-source search pages keep M4's freshness wording; pages of a search across sources say "sharing a source" and "a source's".

**D37. Marks (refines D16).** The part-of-archive refusal applies to any unit holding the part, an atomic unit holding an uncataloged archive included, and names the archive by its display path. The marked tab shows another side's source as "on source X" when it is not the marked node's.

**D38. Live reloads (`app.js`).** On a live page, a job event's reload now waits while a command sent from the page is in flight, is dropped once a command's answer starts navigating, and runs afterwards otherwise. Before, the event of the job a command had just created could reload the page over the command's own navigation, so "Find copies across the chosen sources" sometimes stayed on `/copies`. M4's buttons had the same race.
