# Design

## Context

See proposal.md for why. What R4 builds on:
- **R3's executor.** It journals each step (intent → step → sync → confirm → outcome), runs one organize job per action, takes turns per source, excludes scans, reconciles after a crash, and turns writes off when it must.
- **R3's actions.** Actions and their items are the history and the journal (`actions`, `action_items`, migration 0006). Organizing plans are index-only commands.

What R4 has to add:
- **The index.** `entries.state` cannot be widened without rebuilding `entries`. Under `foreign_keys(1)` inside the migration's transaction, that rebuild would cascade-delete ten child tables. Scans and refolds also rewrite `state`.
- **Hashing.** Today it reads only `pending` rows. Nothing hashes a `unique_size`, `sampled`, or already `hashed` file on request, or re-reads a copy to verify it. Exported, identity-checked openers do exist: `content.OpenAt` and `(*content.Service).OpenMember`.
- **Writing.** `fsaccess.Writer` cannot create or delete a file.

## Goals / Non-Goals

**Goals:** close R4.1–R4.9 on Linux, on the R3 executor, without weakening I1–I9.

**Non-Goals:**
- Model probabilities in the check (R6).
- Moves across filesystems (§13).
- Quarantine on macOS and Windows (R8).
- Retention timers or automatic purges (§13).

## Decisions

### D1. One quarantine folder per source, at its top (§10.3)

Each source's quarantine is `.precious-quarantine` at the source's top folder. A quarantined item lives at `.precious-quarantine/<plan>/<seq>/<original name>`, and its origin record at `.precious-quarantine/<plan>/<seq>.json`.
- **Same filesystem.** Scans never cross a nested mount, so every indexed entry of a source is on the filesystem of its top folder. One folder per source therefore satisfies "the same filesystem as the item" (on ZFS, the same dataset; on a USB stick, the stick).
- **The item folder.** It keeps the original name exactly (I6), with no collisions between items of a plan.
- **The record beside it.** The record sits next to the item folder, never inside it, so no item's name can collide with it.

Rejected alternatives:
- The volume's root. It lies outside the source's rooted access, and maybe outside the allowed roots.
- A folder from the configuration. It may sit on another filesystem.
- Renaming items to their sequence number. A person browsing the disk would lose the names.

### D2. Quarantined entries stay in the index, hidden by their path (I4)

Quarantining moves an entry with R3's `index.MoveEntry` into the quarantine folder's index rows, created with `InsertFolder`. IDs, own decisions, tags, overrides, and digests therefore stay, and restore brings them back by moving again.

Visibility follows the path:
- every reader that shows or counts the disk adds `index.NotQuarantined(alias)`, a residual predicate on the row it already fetches: `NOT (p = Q OR (p >= Q||'/' AND p < Q||'0'))`, with `Q` the reserved name as a blob literal;
- scans skip the reserved name at the top, marking its stored row seen so nothing becomes missing;
- `Refold` skips it at the top;
- so source, Home, and folder totals leave quarantine out with no reader edits.

The relations snapshot drops the folder's row, and with it the whole subtree, as it already drops what lies below a missing folder.

Rejected alternatives:
- **A new `entries.state` value.** It needs the cascading rebuild, and scans and refolds would overwrite it.
- **A `quarantined` column.** It must be written for every row on every move in or out, and kept in sync by every future mover. It needs the same 45 predicate edits, plus a stale-flag failure mode.
- **Deleting the rows and letting a rescan of the restored files bring new ones.** Decisions, tags, and overrides would be lost (I4).

### D3. Cleanup plans are actions of the R3 executor (§6.8, §10.1, §10.2)

A cleanup plan is an action of kind `cleanup`, drafted by `plan-cleanup`. Its steps for each item are:
1. `mkdir` of `<seq>`;
2. `record` (the origin record);
3. `rename` of the entry into `<seq>`.

Before the first item, the plan adds a `mkdir` of `.precious-quarantine` (when it is absent) and of `<plan>`. Later steps name a folder planned earlier with `to_dir_seq`, as merges do.

Running it is the approval (`run-action`). It goes through R3's turn order, scan exclusion, intent and outcome transactions, reconciliation, cancel, and writes-off unchanged.

A cleanup action holds at most 30,000 steps, so 9,999 entries. A plan over that answers `400 invalid_request` and asks for a narrower scope: one review list, or fewer discards. A cleanup plan is runnable for 24 hours, not R3's one hour, since the owner reads a large plan before approving it.

Rejected alternatives:
- **A second executor or job kind.** That means a second journal, and a second crash-recovery path.
- **One composite "quarantine" step.** Three crash windows hidden in one intent.

### D4. Items, blocks, and the light summary are read from the index (§10.1)

The items are the topmost entries with their own `discard` in scope; nested own discards fold into them. The scope is the source, or the rows of one review list. Each item is then checked against the R3 refusals and the new ones:
- `in_quarantine`;
- `holds_kept`, when an entry below it has `eff_decision = 'keep'`. This uses the `(source_id, path)` range query rescue already uses, with no file reads;
- `contains_mount` and `other_filesystem`, as in R3.

A blocked item has state `blocked` and reason `holds_kept`. It is never partly quarantined (R4.1). `GET /api/history/{id}/items/{item}/kept` lists its kept entries, with their count, 100 to a page.

The plan answer carries the light summary, counted over the planned items' present files:
- `with_copy_bytes`: a `hashed` file with another present copy that is outside quarantine and outside the plan;
- `no_copy_bytes`: `unique_size`, `sampled`, or `hashed` with no such copy;
- `unchecked_bytes`: `pending`, `changed`, or `unreadable`;
- `personal_items`: items whose own family is personal, or that hold indicators (`dir_stats.indicators`) or a veto.

### D5. Duplicates plans keep a copy, and verify it before moving (§10.1.4, R4.4)

The duplicates list holds two kinds of rows:
- **Relation rows** have two sides. A side with its own `discard` is an item. When both sides are discarded, the side that sorts later by path is refused `both_sides`.
- **Content-group rows** have copies. Each discarded copy is an item, in path order, until one would leave no physical copy that is outside the plan, outside quarantine, and present. Hard links count as one copy. That item and the later ones are refused `last_copy`.

The action's `ground` is `duplicate`. Before such an item's rename, the intent step re-reads in full one staying copy of each file below it and compares the digest (D9's `content.HashEntry` and `HashMember`). If any file has no verified copy, the item ends `changed` with reason `no_verified_copy`, and nothing moves.

Rejected: verifying only at draft time. A copy can change or be quarantined in between, and §10.1 asks the executor to verify.

### D6. Restore is a planned action of reverse steps (§10.3, R4.3)

`plan-restore {entry_ids | plan_id, destination_id?}` plans, for each quarantined top item:
1. `rename` back to its original parent and name;
2. `unlink` of its origin record;
3. `rmdir` of its `<seq>` folder.

Then come `unlink` and `rmdir` of `<plan>`, only when every item of that plan folder leaves it in this action. The original parent and name come from the cleanup item's `from_parent` and `from_name`.

A taken name or a missing parent is a conflict, `name_taken` or `previous_folder_gone`. With `destination_id`, those items target that folder under their original names, as R3's undo does. Restore never merges and never overwrites, because the rename is no-replace.

### D7. The pre-delete check is a read-only job over a chosen set (§10.6, R4.6)

`check-purge {entry_ids}` takes the quarantined top items of one source and starts a `purge_check` job (ClassInteractive, bound to the source).

The job:
1. Lists every present file below the items, and every file member of each complete archive among them.
2. Reads each in full through `content.HashEntry` or `HashMember`, and records its identity and digest in `purge_check_files`. A 7z, rar, or incomplete archive is one opaque file: its own digest is what counts, and the report says its insides were not opened. Empty files are safe, with nothing to lose.
3. For each digest, looks for a copy outside the set and outside quarantine, in this order:
   - entries and complete-archive members with that digest's content;
   - present files of the same size with no digest, which it then hashes.

   It re-reads each candidate in full and stops at the first one that matches. Each candidate is identity-checked, on an online source. A hard link outside the set counts: the data stays reachable through it.
4. Gives each file a verdict:
   - `safe`, with the copy recorded;
   - `copy_offline`, when the only candidates are on offline sources;
   - `unique` otherwise.

The check writes no `file_content` row, so coverage and relations are untouched. It reads only, so it needs no write permission, and it is not an executor step (I2 holds).

Rejected alternatives:
- **Trusting stored digests.** §10.6 asks for a full read at this destructive step.
- **Storing the results in `file_content`.** That would trigger size-group, coverage, and relate side effects for quarantined rows.

### D8. Rules rank unique files; the gate follows the rank (§10.6.2–3, R4.7)

Classes:
- **`possibly_valuable`.** The file's family is personal (`personal_media`, `documents`, `source_project`, `application_user_data`), or its traits hold `contains_user_material`, `contains_credentials`, or `contains_database`, or its file kind is image, video, audio, document, or source, or its extension is mail (`eml`, `mbox`, `msg`, `pst`, `dbx`).
- **`likely_junk`.** Otherwise, its family is disposable, or its category is `installer_download`, `application_installation`, or `os_installation`.
- **`uncertain`.** Everything else.

Archive members have no stored classification. They are classified by name through `rules.Policy` (`FileKind`, `ClassifyFile`).

The gate:
- `likely_junk` unique files need one group confirmation (`confirm-purge {check_id, group:"likely_junk"}`);
- `possibly_valuable`, `uncertain`, and `copy_offline` files need one confirmation each (`confirm-purge {check_id, file_ids}`, one ID per click in the interface), or their item restored.

Restoring an item takes it out of the set and makes the check stale. The owner then checks the smaller set again.

### D9. Hashing for the check and the verifications is exported by `content` (foundation)

```go
func HashEntry(ctx context.Context, root fsaccess.Dir, r Row, caps fsaccess.Capabilities) (sum [32]byte, info fsaccess.EntryInfo, err error)
func (s *Service) HashMember(ctx context.Context, q store.Queryer, ref domain.Ref) (sum [32]byte, size int64, err error)
```

Both read in full through the existing identity-checked openers, in bounded chunks. Identity differences surface as `invalid_entry_state`. Both write nothing.

### D10. Freshness: a check is for one set, compared again at purge (R4.8)

A check is `running`, `ready`, `failed`, or `stale`. It becomes stale:
- in the transaction of any `run-action` whose action renames, restores, or purges an entry of its set, or quarantines one of its recorded copies;
- in the transaction of a `set-decision` on a recorded copy;
- when a purge step finds a difference.

A purge step compares, just before each deletion:
- the file's lstat with the identity the check recorded (size, mtime, ctime, and inode where identity is stable);
- each relied-on copy's lstat with its recorded identity, where the copy must also be present and outside quarantine in the index.

Any difference ends the item `changed`, marks the check `stale`, and stops the action before anything else is deleted. A purge needs a `ready` check with its gate satisfied, re-checked in `run-action`'s transaction and at each item's intent.

Rejected: re-reading every copy again at purge time. The full read happened in the check, and identity, as everywhere else, detects a change.

### D11. Purge deletes one quarantined item per step, idempotently (§10.3, R4.5)

`plan-purge {check_id}` plans an action of kind `purge`, with one `purge` step per checked item, then `unlink` and `rmdir` of the plan folders it empties.

A `purge` step:
1. Opens the item's `<seq>` folder through rooted handles.
2. Walks it depth first.
3. For every file, compares it with the check (D10), then `Unlink`s it.
4. `Rmdir`s each folder after its contents.
5. Unlinks the origin record and `rmdir`s `<seq>`.
6. Syncs the folders it changed.

**Recovery.** The step is idempotent. A file already gone counts as done. An entry the check never recorded stops the item `changed`, with nothing of it deleted. So reconciling a `purge` item left in `intent` runs the step again, where a rename would compare names. Deleting checked files is the approved intent, and partial progress is never ambiguous.

**The outcome.** It deletes the item's index rows (`index.DeleteSubtree`) and refolds the quarantine chain.

**The report.** The action reports `deleted_files`, `deleted_bytes`, and `freed_bytes`, the allocated bytes of the files whose link count was 1 when removed. On ZFS, the interface adds that snapshots may keep the space.

A purge step cannot be cancelled midway; cancelling takes effect between items.

Rejected alternatives:
- **One step per file.** 100,000 journal rows for a folder, against R3's cap.
- **Deleting with `os.RemoveAll`.** It follows no identity check and is not rooted.

### D12. Two primitives, used only inside the quarantine (I2, I3)

`fsaccess.Writer` gains:
- **`CreateExclusive(name, data)`.** It opens with `openat(O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC)`, mode `parent & 0666`, writes everything, fsyncs the file, and closes it. If the write fails, it unlinks the partial file.
- **`Unlink(name)`.** It runs `unlinkat(fd, name, 0)`. A folder answers `EISDIR` or `EPERM`, mapped to `ErrNotEmpty`'s sibling `ErrIsDir`.

The executor calls them only through a folder whose index path is the quarantine folder or lies below it, and the guard test keeps every other package away. A name taken at `record` time ends the item `conflict`, and nothing moves (I3).

### D13. Quarantined entries are frozen for everything but restore and purge (I4, I5)

The following refuse a quarantined target with `409 in_quarantine`:
- the owner's commands: `set-decision`, `set-tags`, `set-category`, `set-group`, `check-now`, and every organize plan;
- an organize destination below the quarantine, with `400 invalid_request`.

Selections and review-list resolutions never contain quarantined entries (D2). An entry's detail answers `in_quarantine: {plan, quarantined_at, original_path}` so the panel can explain.

Rejected: letting the owner keep a quarantined file. Keep is the protection against plans, and restore is the way out of a plan.

### D14. `remove-source` refuses while its quarantine holds items

`remove-source` fails with `409 quarantine_not_empty` while the source's quarantine holds items, along with R3's refusals. Its index would otherwise go, leaving quarantined files on the disk with nothing in Precious to restore or purge them.

### D15. Home shows bytes in quarantine (§11.1)

Decision totals leave quarantine out (D2). Each source's bytes in quarantine are the quarantine folder row's `total_bytes`, kept by the refold on every move in or out. Home's decision progress shows them as "In quarantine".

### D16. CSV export for every action (§10.4, R4.9)

`GET /api/history/{id}/export.csv` (`text/csv; charset=utf-8`, as an attachment named `precious-<kind>-<id>.csv`) has the header `path,size,operation,state,reason`, then one row per item in `seq` order:
- **path** is the item's `from_path`, or `to_path` for a `mkdir`, in display form (I6);
- **size** is its bytes;
- **operation** is `quarantine`, `restore`, `purge`, `move`, and so on;
- **state** and **reason** are the item's.

Every cell is quoted. A cell starting with `=`, `+`, `-`, `@`, a tab, or a carriage return gets a leading `'`.

## Interfaces

### Import direction

```
cleanup ──▶ executor, organize (plan helpers, history), content (HashEntry, HashMember), index, decisions, review, rules, sources
executor ──▶ index, content (HashEntry, HashMember for D5), sources, fsaccess
index, content, search, review, relations, decisions, web/api ──▶ index.NotQuarantined (constant SQL; no cycle: index imports none of them)
```

### Go signatures (foundation adds; slices only add)

```go
// internal/index (foundation)
const QuarantineName = ".precious-quarantine"
// NotQuarantined renders a residual SQL predicate on alias.path that excludes the quarantine folder and its subtree.
func NotQuarantined(alias string) string
// InQuarantine renders the positive form, for the Cleanup screen's reads.
func InQuarantine(alias string) string
// IsQuarantinePath reports whether a source-relative path is the quarantine folder or below it.
func IsQuarantinePath(path []byte) bool
// DeleteSubtree deletes the entry, its subtree, and their entry_names rows (purge outcome).
func DeleteSubtree(ctx context.Context, tx *sql.Tx, id domain.EntryID) error
// The scan's skip and Refold's skip of QuarantineName at the top are foundation too.

// internal/fsaccess (foundation)
// Writer gains:
//	CreateExclusive(name, data []byte) error
//	Unlink(name []byte) error
var ErrIsDir error
// instrument: OpCreate, OpUnlink; synthfs and portable implement them.

// internal/content (foundation)
func HashEntry(ctx context.Context, root fsaccess.Dir, r Row, caps fsaccess.Capabilities) ([32]byte, fsaccess.EntryInfo, error)
func (s *Service) HashMember(ctx context.Context, q store.Queryer, ref domain.Ref) ([32]byte, int64, error)

// internal/domain (foundation): 409 codes
CodeInQuarantine = "in_quarantine"; CodePurgeNotAllowed = "purge_not_allowed"; CodeCheckStale = "check_stale"
CodeCheckRunning = "check_running"; CodeQuarantineNotEmpty = "quarantine_not_empty"

// internal/executor (slice 2.3)
// Index gains:
//	ApplyPurge(ctx context.Context, tx *sql.Tx, src domain.SourceID, id domain.EntryID) error // DeleteSubtree + Refold of the quarantine chain
// The foundation adds the method to the interface and to organize's adapter, so every slice builds.
// New ops: "record", "unlink", "purge". Options gains Content *content.Service (for HashMember).

// internal/cleanup (slice 2.4 for the check job, group 3 for the rest)
const KindPurgeCheck jobs.Kind = "purge_check"
func New(o Options) *Service // Store, Runner, Sources, Content, Policy, Executor, Organize, AllowWrites, Clock, Logger
func (s *Service) Register(r *jobs.Runner)         // the check job
func (s *Service) RegisterCommands(h *commands.Handler)
func (s *Service) Routes(mux *http.ServeMux)
// MarkStale marks every ready or running check whose set or copies include one of ids stale (D10).
func MarkStale(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) error
```

`organize` and `decisions` call `cleanup.MarkStale`, which writes only `purge_checks`, inside their command transactions. To avoid an import cycle, the foundation places `MarkStale` in `internal/cleanup/stale` (a leaf package).

### Tables (migration 0007; foundation)

The migration rebuilds `actions` and `action_items` together. The 0005 pattern is used: create the new tables, copy actions and then items, drop items and then actions, rename both, and recreate every index. Drops run with foreign keys on, so items go first. The changes:
- `actions.kind` adds `'cleanup','restore','purge'`;
- new columns `actions.ground TEXT CHECK (ground IN ('discard','duplicate'))`, `actions.check_id INTEGER REFERENCES purge_checks(id) ON DELETE SET NULL`, `actions.list TEXT` (the review list a plan came from), `actions.deleted_files`, `deleted_bytes`, and `freed_bytes INTEGER NOT NULL DEFAULT 0`;
- `action_items.op` adds `'record','unlink','purge'`;
- `action_items.state` adds `'blocked'`.

```sql
CREATE TABLE purge_checks (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('running','ready','failed','stale')),
  job_id INTEGER, created_at INTEGER NOT NULL, finished_at INTEGER,
  junk_confirmed_at INTEGER,
  stale_reason TEXT);
CREATE TABLE purge_check_items (            -- the set: quarantined top entries
  check_id INTEGER NOT NULL REFERENCES purge_checks(id) ON DELETE CASCADE,
  entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  PRIMARY KEY (check_id, entry_id)) WITHOUT ROWID;
CREATE TABLE purge_check_files (
  id INTEGER PRIMARY KEY,
  check_id INTEGER NOT NULL REFERENCES purge_checks(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL,                  -- the top entry it lies under
  entry_id INTEGER, member_id INTEGER,       -- exactly one of a file entry or an archive member (of entry_id's archive)
  path BLOB NOT NULL, size INTEGER NOT NULL,
  mtime_ns INTEGER, ctime_ns INTEGER, ino INTEGER, dev INTEGER,
  sha256 BLOB,                               -- NULL only when unreadable
  verdict TEXT NOT NULL CHECK (verdict IN ('safe','copy_offline','unique','unreadable','opaque_archive')),
  class TEXT CHECK (class IN ('possibly_valuable','likely_junk','uncertain')),
  copy_entry INTEGER, copy_member INTEGER, copy_source TEXT,
  copy_size INTEGER, copy_mtime_ns INTEGER, copy_ctime_ns INTEGER, copy_ino INTEGER, copy_dev INTEGER,
  copy_hard_link INTEGER NOT NULL DEFAULT 0,
  confirmed_at INTEGER);
CREATE INDEX purge_check_files_by_check ON purge_check_files(check_id, verdict, class, id);
CREATE INDEX purge_check_files_copy ON purge_check_files(copy_entry) WHERE copy_entry IS NOT NULL;
CREATE INDEX purge_check_items_entry ON purge_check_items(entry_id);
```

An `unreadable` file is gated like a unique `uncertain` file. An `opaque_archive` is a file whose container was read but whose members were not opened. It is `safe` only with a verified copy of the container, and otherwise gated as `uncertain`.

### Commands (strict JSON; IDs are strings)

| Command | Body | Success | Errors |
|---|---|---|---|
| `plan-cleanup` | `{source_id, list?}` (`list`: a review list name) | 201 `{action, items, next_cursor, summary}` | 400 `invalid_request` (unknown list, over 30,000 steps); 404; 409 `source_offline`, `writes_disabled`, `writes_unavailable`, `recovery_needed` |
| `plan-restore` | exactly one of `entry_ids` (1–1,000 quarantined top items, one source) or `plan_id`; `destination_id?` | 201 `{action, items, next_cursor}` | 400 (not quarantined top items, two sources); 404; the 409s above |
| `check-purge` | `{entry_ids}` (1–10,000 quarantined top items, one source) | 202 `{check_id, job_id}` | 400; 404; 409 `check_running` (a check of the source is running), `source_offline` |
| `confirm-purge` | `{check_id}` and exactly one of `file_ids` (1–1,000) or `group: "likely_junk"` | 200 `{check}` | 400 (a file not in the check, or not gated); 404; 409 `check_stale` |
| `plan-purge` | `{check_id}` | 201 `{action, items, next_cursor}` | 404; 409 `check_stale`, `check_running`, `purge_not_allowed` (with `unconfirmed`), the write 409s |
| `run-action` | R3's | 202 | adds 409 `check_stale` and `purge_not_allowed` for `purge` |
| `cancel-action`, `resolve-recovery` | R3's | | |

### Read API

- **`GET /api/quarantine?source=&cursor=&limit=`** → `{items: Quarantined[], next_cursor, total: {bytes, files}}`. Newest first, 100 by default and at most 500.

  `Quarantined {entry: EntryRow, original: {path, path_b64}, plan_id, quarantined_at, bytes, files, check: {id, state}|null}`.
- **`GET /api/checks/{id}`** → `Check {id, source_id, state, created_at, finished_at, stale_reason, items, counts}`. `counts` has these buckets: `safe`, `copy_offline`, `unique`, `unreadable`, `opaque_archive`, and `unique` by class, each `{files, bytes}`. Beside them:
  - `confirmed {files, bytes}`;
  - `junk_confirmed`;
  - `allowed: bool`;
  - `unconfirmed {files, bytes}`.
- **`GET /api/checks/{id}/files?verdict=&class=&confirmed=&cursor=&limit=`** → `{items: CheckFile[], next_cursor}`, 200 by default and at most 1,000.

  `CheckFile {id, item: EntryRow, path, path_b64, member: bool, size, verdict, class, copy: {path, path_b64, source_id, hard_link}|null, confirmed}`.
- **`GET /api/history/{id}/items/{item}/kept?cursor=`** → `{count, items: EntryRow[], next_cursor}`, 100 to a page.
- **`GET /api/history/{id}/export.csv`**, as in D16.
- **The Action JSON** gains `ground`, `list`, `check_id`, `deleted_files`, `deleted_bytes`, and `freed_bytes`. The Item JSON gains `kept_count` for `blocked` items.
- **Other reads.**
  - `GET /api/home` decisions gain `quarantine {files, bytes}`.
  - `GET /api/entries/{id}` gains `in_quarantine: {plan_id, quarantined_at, original: {path, path_b64}}|null`.
  - The sources JSON gains `quarantine {files, bytes}`.

### Reader exclusion (slices 2.1 and 2.2; each adds `index.NotQuarantined` to the alias it already fetches)

| Slice | Readers |
|---|---|
| **2.1** | `search/filter.go` `buildFilter`, which covers page, count, and resolve, with a `Query.InQuarantine` opt-in for the Cleanup screen; `search/dup.go` `CopiesSQL`, `copiesColumn`, `otherCopy`, and `dupFilter` on the other copy's alias and on `e`; `web/api` `childOrder.sql`, `treemapSQL`, `treemapRestSQL`, and `onlyFolder` at the top, plus `members.go` `memberSelect` and `memberCopySQL`; `decisions` `totalsSQL` and Home's quarantine bucket. |
| **2.2** | `content` `Copies` (both arms), `plan.go` `coverageRows`, `groupQuery`, and `insertRows`, and the `job.go` batch selections; `review` `ruleRows`, `loadRelations`, `loadCopies`, `loadGroups`, `openSQL`'s copy arms, and the row-level copy query; `relations` `snapshot.loadDirs` (the folder row), plus `loadFiles`, `loadArchives`, and `compare.side.files`. |

The predicate is a residual written as `NOT (...)`, which SQLite does not use for index lookups. Every existing plan guard and the 2-million-entry tests must stay green.

## Concurrency

- **`plan-cleanup` and `plan-restore`.** These are index-only commands in the write transaction. They re-check writes, recovery, quarantine, and keeps. A commit just after leaves the plan stale, and the intent re-checks catch it per item: `changed`, `blocked`/`holds_kept`, `no_verified_copy`, `conflict`.
- **`run-action`.**
  - For a `purge` action, it re-checks the check (`ready`, gate satisfied) in its transaction.
  - For any action whose items touch a checked set or a recorded copy, it calls `stale.MarkStale` in the same transaction.
  - The executor's intent transaction for a `purge` item re-checks the check's state. A `set-decision` or a scan cannot interleave there: the scan defers while organize runs, and decisions go through `MarkStale`.
- **The check job.**
  - It defers while an organize job of its source is queued or running, and organize jobs never wait for it.
  - At its end, in one transaction, it sets `ready` only if the check is still `running`. A `MarkStale` that ran meanwhile leaves it `stale`, so a late result cannot win (I9).
  - It reads copies on other sources through `sources.Open`. A copy changed during the check fails its identity check and is not used.
- **Purge steps.** They compare identities just before each unlink. A file changed after the check's read, but before the purge, is caught there (R4.8). A change after its unlink is moot. The purge holds the source's organize turn, so no restore or cleanup of the same source can interleave.
- **Scans.** They defer while an organize job of the source is active (R3). The check job does not block scans, because scans skip the quarantine. A rescan that changes a relied-on copy is caught by the purge's identity comparison.
- **`remove-source`.** It re-checks quarantine items in its transaction (D14).

## Risks / Trade-offs

- [45 reader edits could miss one] → A test in each slice quarantines a file in the corpus and asserts it from the API: the Map, Search, Home, copies, review cards, Compare, and relations all leave it out. The plan guards and slow tests run in 4.2.
- [A check of a large set reads a lot] → It is interactive, yields between files, reports progress (`files`, `bytes`, `of_bytes`), and can be cancelled. Nothing is deleted without it, per §10.6.
- [Per-file confirmations of thousands of photos] → The spec asks for one by one. The interface offers restoring the item as the way out, and says so.
- [The owner's service user cannot write the archive] → As in R3, writes stay unavailable or fail until the deployment grants them. The docs keep the steps.

## Migration Plan

- **Upgrade.** Migration 0007 rebuilds two small tables and adds three. Existing actions keep their IDs and history, and existing sources have no quarantine until a plan runs.
- **Rollback.** Restore the backup taken before deploying, since an r3 binary refuses a newer schema.
