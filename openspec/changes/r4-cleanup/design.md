# Design

## Context

See proposal.md for why. What R4 builds on:
- **R3's executor.** It journals each step (intent → step → sync → confirm → outcome), runs one organize job per action, takes turns per source, excludes scans, reconciles after a crash, and turns writes off when it must.
- **R3's actions.** Actions and their items are the history and the journal (`actions`, `action_items`, migration 0006). Organizing plans are index-only commands.
- **R3's undo.** Restore follows its "reverse steps" model.

What R4 has to add:
- **The index.** `entries.state` cannot be widened without rebuilding `entries`. Under `foreign_keys(1)` inside the migration's transaction, that rebuild would cascade-delete ten child tables. Scans and refolds also rewrite `state`.
- **Hashing.** Today it reads only `pending` rows. Nothing hashes a `unique_size`, `sampled`, or already `hashed` file on request, or re-reads a copy to verify it. Exported, identity-checked openers do exist: `content.OpenAt` and `(*content.Service).OpenMember`.
- **Writing.** `fsaccess.Writer` cannot create or delete a file.
- **Identity.** R3 sets the identity it expects at intent, from the index.

## Goals / Non-Goals

**Goals:** close R4.1–R4.9 on Linux, on the R3 executor, without weakening I1–I9. No file is deleted unless the owner confirmed it, or a copy outside the purge set and the quarantine was read and verified, and nothing about it changed since.

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

`sources.quarantine_entry_id` records the folder once Precious creates it.

A top-level `.precious-quarantine` that Precious did not create is the owner's own folder. In that case:
- `plan-cleanup` refuses with `409 quarantine_name_taken`;
- the Sources screen asks the owner to rename it;
- views still hide it, as they hide the name everywhere. Hiding it silently would break I7, so the warning names it.

Rejected alternatives:
- The volume's root. It lies outside the source's rooted access, and maybe outside the allowed roots.
- A folder from the configuration. It may sit on another filesystem.
- Renaming items to their sequence number. A person browsing the disk would lose the names.

### D2. Quarantined entries stay in the index, hidden by their path; scans walk them (I4, I7; ADR 0011)

Quarantining moves an entry with R3's `index.MoveEntry` into the quarantine folder's index rows. IDs, own decisions, tags, overrides, and digests therefore stay, and restore brings them back by moving again.

**Visibility follows the path.** `index.NotQuarantined(alias)` renders a residual predicate with precomputed blob literals: `NOT (alias.path = X'<Q>' OR (alias.path >= X'<Q>2f' AND alias.path < X'<Q>30'))`. A `Q || '/'` would be TEXT and would compare above every blob path.
- Every reader that shows or counts the disk adds it on the row it already fetches.
- The relations snapshot drops the folder's row, and with it the whole subtree, as it already drops what lies below a missing folder.

**Scans walk the quarantine** like any folder, so its rows stay true. A file deleted by hand there goes missing, and a step a crash left unrecorded gets indexed. They also leave it out of everything they report:
- the fold at the source's top does not absorb the quarantine child, in the scan and in `Refold`;
- so source, Home, and folder totals exclude it, while the quarantine folder's own row still folds its bytes for Home (D15).

Hashing, size groups, coverage, review, and relations exclude it through the predicate. This reads §10.3's "excluded from scans" as excluded from what a scan counts and reports, not from its walk (ADR 0011).

Rejected alternatives:
- **A new `entries.state` value.** It needs the cascading rebuild, and scans and refolds would overwrite it.
- **A `quarantined` column.** It must be kept in sync on every move in or out, and it needs the same edits plus a stale-flag failure mode.
- **Scans that skip the quarantine entirely.** Then a crash between a rename and its outcome leaves a file in quarantine that no scan or resolve can ever index (review finding).
- **Deleting rows on quarantine.** Restore would lose decisions and tags (I4).

### D3. Cleanup plans are actions of the R3 executor, with draft-time identity (§6.8, §10.1, §10.2)

A cleanup plan is an action of kind `cleanup`, drafted by `plan-cleanup`. Its steps for each item are, in order:
1. `mkdir` of `<seq>`;
2. `rename` of the entry into `<seq>`;
3. `record` of `<seq>.json`.

Before the first item, the plan adds a `mkdir` of `.precious-quarantine` (when absent) and of `<plan>`. Later steps name a folder planned earlier with `to_dir_seq`, as merges do. The record follows the move, so it never describes a move that did not happen. If a crash leaves a record missing, reconciliation writes it (D4).

**Draft-time identity.** Unlike R3, a cleanup item records the identity it was drafted against (§6.8): kind, size, mtime, ctime, dev, ino, and, for a folder, its total bytes and files. These are new `action_items` columns. The first step's intent, the `mkdir` of `<seq>`, re-checks the whole item:
- the entry's current index row and its lstat match the draft;
- its own decision is still `discard`;
- nothing in its inclusive subtree is effectively `keep`;
- for a duplicate ground, D5's copies are verified.

On any failure, the item's three steps end together: `changed` (`identity_changed`, `decision_changed`, `no_verified_copy`) or `blocked`/`holds_kept`, with nothing made on disk. The rename's own intent repeats the identity and decision tests.

**If the rename does not happen** (a race between the two intents, a cancel), the empty `<seq>` stays behind. D6's and D11's folder sweep removes it later. A scan between draft and run updates the index, and the draft comparison then catches it (R4.2).

Running the plan is the approval (`run-action`). It goes through R3's turn order, scan exclusion, intent and outcome transactions, reconciliation, cancel, and writes-off. A cleanup action holds at most 30,000 steps, so 9,999 entries. A larger plan answers `400 invalid_request` and asks for a narrower scope. A cleanup plan is runnable for 24 hours.

Rejected alternatives:
- **Identity taken at intent, as R3 does.** A rescan during the 24 hours would hide a changed file (review finding).
- **Writing the record before the move.** A refusal at the rename would leave a false record.

### D4. Recovery for every new step (§10.2)

| Step | Reconciled from `intent` |
|---|---|
| `record` | name absent → `planned` (it runs once); present with the expected bytes → `done`; else `manual_recovery` |
| `unlink` | name absent → `done`; present with the recorded identity → `planned`; else `manual_recovery` |
| `purge` | D11 |
| `verify` | → `planned` (it is read-only) |

`resolve-recovery` keeps R3's semantics: the item is `resolved`, and a scan starts. Since scans walk the quarantine (D2), the scan brings its rows in line with the disk. A file the owner left there by hand is indexed at its quarantine path, under a new ID if the move's outcome was never recorded. The Cleanup screen lists it among quarantined items with "origin unknown", restorable to a chosen folder.

### D5. Duplicates plans keep a copy, and verify it before moving (§10.1.4, R4.4)

The duplicates list holds two kinds of rows:
- **Relation rows** have two sides. A side with its own `discard` is an item. When both sides are discarded, the side that sorts later by path is refused `both_sides`.
- **Content-group rows** have copies. Each discarded copy is an item, in path order, until one would leave no physical copy that is outside the plan, outside quarantine, and present. Hard links count as one copy. That item and the later ones are refused `last_copy`.

The action's `ground` is `duplicate`.

**Verification.** In the first step's preflight, outside any write transaction, the executor re-reads in full one staying copy of each file below the item, and compares the digest (D9). Inside the intent transaction it then re-checks that each verified copy:
- has an unchanged identity;
- is not quarantined;
- is not at or below the `from_path` of any cleanup `rename` item in state `intent`, on any source.

The last test closes the race between two duplicate-ground plans on different sources, which could otherwise verify each other's copy and both quarantine. If any file lacks a verified copy, the item ends `changed`/`no_verified_copy`, and nothing moves.

Rejected: reading the copies inside the intent transaction. That holds the store's only writer for the whole read.

### D6. Restore is a planned action of reverse steps (§10.3, R4.3)

`plan-restore {entry_ids | plan_id, destination_id?}` plans, for each quarantined top item:
1. `rename` back to its original parent and name;
2. `unlink` of its record;
3. `rmdir` of its `<seq>`.

When every item of a plan folder leaves in this action, a sweep follows: `rmdir` of every empty `<seq>` left behind, `unlink` of records whose item is gone, and `rmdir` of `<plan>`.

The original place comes from the cleanup item's `from_parent` and `from_name`. A parent that is gone, not a present folder, or itself at or below the quarantine (`IsQuarantinePath`) is `previous_folder_gone`. A taken name is `name_taken`. With `destination_id`, those items go to that folder under their original names. A destination in the quarantine is `400 invalid_request`. The rename is no-replace, so restore never merges or overwrites. An item quarantined with unknown origin (D4) can be restored only to a destination.

### D7. The pre-delete check is a read-only job over a chosen set (§10.6, R4.6)

`check-purge {entry_ids}` takes the quarantined top items of one source and starts a `purge_check` job (ClassInteractive, bound to the source).

**1. Record every entry below the items, of every kind.** Each entry is recorded with its identity.
- Files are read in full through `content.HashEntry`.
- A complete archive is opened once with `content.HashArchive`, which yields every file member's digest in one pass.
- Folders, symlinks, and specials are recorded with verdict `no_content`.
- A 7z, rar, or incomplete archive is one `opaque_archive` file: its container digest counts, and the report says its insides were not opened.
- Empty files are `no_content`.
- A folder the scan could not read, or an item marked partial, makes its item `unreadable`. Such an item cannot be purged (D11), since Precious cannot see what it would delete (I7).

**2. Look for a copy of each digest** outside the set and outside quarantine, in this order:
- entries and complete-archive members with that digest's content;
- present files of the same size with no digest, which it hashes.

It re-reads each candidate in full, identity-checked, on an online source, and stops at the first match. A hard link outside the set counts: the data stays reachable through it. The copy's source, path, and identity are recorded.

**3. Give each file a verdict:**
- `safe`, with the copy recorded;
- `copy_offline`, when the only candidates are on offline sources;
- `unique` otherwise.

**4. End in one transaction.** The check sets `ready` only if it is still `running`, and only if every set item still sits at its quarantine path. Otherwise it ends `stale` (I9).

The check writes no `file_content` row. It reads only, needs no write permission, and is not an executor step (I2). It defers while an organize job of its source is queued or running.

Rejected alternatives:
- **Trusting stored digests.** §10.6 asks for a full read at this destructive step.
- **Hashing members one `HashMember` at a time.** A streamed tar would be read again for each member (review finding).

### D8. Rules rank unique files; the gate follows the rank (§10.6.2–3, R4.7)

Classes:
- **`possibly_valuable`.** The file's family is personal (`personal_media`, `documents`, `source_project`, `application_user_data`), or its traits hold `contains_user_material`, `contains_credentials`, or `contains_database`, or its file kind is image, video, audio, document, or source, or its extension is mail (`eml`, `mbox`, `msg`, `pst`, `dbx`).
- **`likely_junk`.** Otherwise, its family is disposable, or its category is `installer_download`, `application_installation`, or `os_installation`.
- **`uncertain`.** Everything else, including `opaque_archive` containers with no verified copy.

Archive members have no stored classification. They are classified by name through `rules.Policy`.

The gate:
- `likely_junk` unique files need one group confirmation (`confirm-purge {check_id, group:"likely_junk"}`);
- `possibly_valuable`, `uncertain`, and `copy_offline` files each need one confirmation (`confirm-purge {check_id, file_ids}`), or they are taken out of the set.

There are two ways to take a file out:
- **Restore its item.** It goes back to its original place.
- **Move it out.** An individual organize move of that entry to a folder outside the quarantine (D13).

Either makes the check stale, and the owner checks the smaller set again. This keeps §10.6's "moved out through organizing".

### D9. Hashing for the check and the verifications is exported by `content` (foundation)

```go
func HashEntry(ctx context.Context, root fsaccess.Dir, r Row, caps fsaccess.Capabilities) (sum [32]byte, info fsaccess.EntryInfo, err error)
// HashArchive opens or streams a complete archive once and calls fn for every file member.
func (s *Service) HashArchive(ctx context.Context, q store.Queryer, archive domain.EntryID, fn func(member int64, sum [32]byte, size int64) error) error
func (s *Service) HashMember(ctx context.Context, q store.Queryer, ref domain.Ref) (sum [32]byte, size int64, err error) // one copy, for D5 and the copy search
```

These read in full through the existing identity-checked openers, in bounded chunks. Identity differences surface as `invalid_entry_state`. They write nothing.

### D10. Freshness: a check is for one set and its copies (R4.8)

A check is `running`, `ready`, `failed`, or `stale`. `stale.MarkStale(ctx, tx, src, path)` marks stale every `running` or `ready` check of `src` that has a set item, a recorded file, or a recorded copy at or below `path`. Copies are matched by `copy_source` and `copy_path`, so moving or deciding a folder that holds a copy counts. It is called:
- in every executor outcome transaction, with the step's old and new paths (renames, restores, moves out, purges, unlinks, rmdirs), on whatever source;
- in `set-decision`, `set-category`, and `set-group` transactions, for each target's path;
- when a check's own end finds an item out of place (D7).

`plan-purge` and `run-action` refuse a stale check with `409 check_stale`.

**Disk changes the index has not seen** are caught by the purge action's first step, `verify`. Before any deletion, it lstats every recorded entry of the set and every relied-on copy:
- the identity must match (size, mtime, ctime, and inode where stable);
- each copy must still be present and outside quarantine in the index;
- each copy's source must be online.

Any difference marks the check stale and ends the action with nothing deleted ("before the purge runs"). Each `purge` step compares again, file by file, just before each unlink.

**Hard links.** After the purge unlinks one name of an inode, the other names of that inode get a new ctime and a lower link count. For an inode the running purge has already unlinked a name of, the comparison accepts that change, and still requires the size, mtime, and inode to match.

Rejected: re-reading every copy at purge time. The check read them in full, and identity, as everywhere else, detects a change.

### D11. Purge deletes one checked item per step, verified whole before its first unlink (§10.3, R4.5)

`plan-purge {check_id}` plans an action of kind `purge`:
1. one `verify` step (D10);
2. one `purge` step per checked item that is not `unreadable` (`unreadable` items are refused);
3. the D6 folder sweep of the plan folders it empties.

**A `purge` step:**
1. Its intent re-checks the check (`ready`, gate satisfied), writes (`CheckWrites`), and the item's own decision and inclusive subtree (still discarded, nothing kept).
2. It opens `<seq>` through rooted handles and walks the whole tree, comparing every entry with the check's records. Any entry that differs, or that the check did not record, ends the item `changed`, with nothing deleted.
3. It deletes depth first: `Unlink` each file and symlink, then `Rmdir` each folder after its contents. Then it unlinks the record and removes `<seq>`.
4. It syncs the folders it changed.

**The outcome.** It deletes the index rows of everything removed (`index.DeleteSubtree`, or the rows of the removed entries when the step stopped partway), and refolds the quarantine chain.

**Recovery.** A `purge` left in `intent` is replayed only after its intent's re-checks pass again. Otherwise the item ends `changed`, with reason `writes_off`, `check_stale`, or `decision_changed`, its removed part recorded and its rows deleted, and nothing more unlinked. Deleting checked files is the approved intent, so partial progress is never ambiguous.

**The report.** The action reports `deleted_files`, `deleted_bytes`, and `freed_bytes`, the allocated bytes of the files whose link count was 1 when removed. On ZFS, the interface adds that snapshots may keep the space.

A purge step cannot be cancelled midway; cancelling takes effect between items.

Rejected alternatives:
- **One step per file.** 100,000 journal rows for a folder, against R3's cap.
- **A single walk that deletes as it compares.** An unrecorded entry found late would leave the item half deleted (review finding).

### D12. Two primitives, used only inside the quarantine (I2, I3)

`fsaccess.Writer` gains:
- **`CreateExclusive(name, data)`.** It opens with `openat(O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC)`, mode `parent & 0666`, writes everything, fsyncs the file, and closes it. If the write fails, it unlinks the partial file.
- **`Unlink(name)`.** It runs `unlinkat(fd, name, 0)`. A folder answers `ErrIsDir`.

The executor calls them only through a folder whose index path the quarantine contains (`IsQuarantinePath`), checked at intent and again in the step. The guard test keeps every other package away. A name taken at `record` time ends the item `conflict`, with nothing replaced (I3).

### D13. Quarantined entries are frozen, except restore, purge, and an individual move out (I4, I5)

The following refuse a quarantined target with `409 in_quarantine`:
- `set-decision`, `set-tags`, `set-category`, and `set-group`;
- `check-now`;
- bulk organize plans and every plan kind other than `plan-move`.

`plan-move` with one `entry_id` inside the quarantine and a destination outside it is allowed. That is §10.6's "move out", and it stales checks through D10. Every organize plan refuses a destination at or below the quarantine with `400 invalid_request`.

Organize planning and the executor's rename intent refuse to move or rename an entry to the reserved name at a source's top. The executor's own `mkdir` of the quarantine is exempt by op, and `index.validName` stays name-agnostic.

`plan-undo` answers `action_not_undoable` for kinds `cleanup`, `restore`, and `purge`, since restore is the reverse of cleanup, and History offers no Undo for them. An entry's detail answers `in_quarantine: {plan_id, quarantined_at, original}`.

### D14. `remove-source` refuses while its quarantine holds items

`remove-source` fails with `409 quarantine_not_empty` while the source's quarantine holds entries, along with R3's refusals. Its index would otherwise go, leaving quarantined files on disk with nothing in Precious to restore or purge them.

### D15. Home shows bytes in quarantine (§11.1)

Decision totals leave quarantine out (D2). Each source's bytes in quarantine are its quarantine folder row's `total_bytes`, which the refold and scans keep. Home's decision progress shows them as "In quarantine".

### D16. CSV export for every action (§10.4, R4.9)

`GET /api/history/{id}/export.csv` (`text/csv; charset=utf-8`, as an attachment named `precious-<kind>-<id>.csv`) has the header `path,size,operation,state,reason`, then one row per item in `seq` order:
- **path** is the item's `from_path`, or `to_path` for a `mkdir` or `record`, in display form (I6);
- **size** is its bytes;
- **operation** is `quarantine`, `restore`, `purge`, `move`, and so on;
- **state** and **reason** are the item's.

Every cell is quoted. A cell starting with `=`, `+`, `-`, `@`, a tab, or a carriage return gets a leading `'`.

## Interfaces

### Import direction

```
cleanup ──▶ executor, organize (plan helpers, history), content (HashEntry, HashArchive, HashMember), index, decisions, review, rules, sources, cleanup/stale
executor ──▶ index, content (HashEntry, HashArchive, HashMember for D5), sources, fsaccess, cleanup/stale
decisions, organize ──▶ cleanup/stale
cleanup/stale ──▶ store only (a leaf)
index ──▶ none of the above; search, content, review, relations, decisions, web/api use index.NotQuarantined
```

### Go signatures (foundation adds; slices only add)

```go
// internal/index (foundation)
const QuarantineName = ".precious-quarantine"
func NotQuarantined(alias string) string   // residual SQL with precomputed blob literals (D2)
func InQuarantine(alias string) string     // the positive form
func IsQuarantinePath(path []byte) bool
func DeleteSubtree(ctx context.Context, tx *sql.Tx, id domain.EntryID) error
func DeleteEntries(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) error // partial purges
// The scan and Refold folds skip the top-level quarantine child; scans still walk it.

// internal/fsaccess (foundation)
// Writer gains:
//	CreateExclusive(name, data []byte) error
//	Unlink(name []byte) error
var ErrIsDir error
// instrument: OpCreate, OpUnlink; synthfs and portable implement them.

// internal/content (foundation): HashEntry, HashArchive, HashMember (D9)

// internal/domain (foundation): 409 codes
CodeInQuarantine = "in_quarantine"; CodePurgeNotAllowed = "purge_not_allowed"; CodeCheckStale = "check_stale"
CodeCheckRunning = "check_running"; CodeQuarantineNotEmpty = "quarantine_not_empty"; CodeQuarantineNameTaken = "quarantine_name_taken"

// internal/cleanup/stale (foundation)
func MarkStale(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte) error

// internal/executor (foundation adds the contract; slice 2.3 implements the ops)
// Index gains:
//	ApplyPurge(ctx context.Context, tx *sql.Tx, src domain.SourceID, removed []domain.EntryID, whole domain.EntryID) error
//	ApplyUnlink(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte) error // drops a record's row if a scan indexed it
// organize's adapter implements both in the foundation. Options gains Content *content.Service.
// New ops: "record", "unlink", "purge", "verify". New item state: "blocked".

// internal/cleanup (slice 2.5 for the check job, group 3 for the rest)
const KindPurgeCheck jobs.Kind = "purge_check"
func New(o Options) *Service // Store, Runner, Sources, Content, Policy, Executor, AllowWrites, Clock, Logger
func (s *Service) Register(r *jobs.Runner)
func (s *Service) RegisterCommands(h *commands.Handler)
func (s *Service) Routes(mux *http.ServeMux)
```

### Tables (migration 0007; foundation)

The migration rebuilds `actions` and `action_items` together. The 0005 pattern is used: create the new tables, copy actions and then items, drop items and then actions, rename both, and recreate every index. Drops run with foreign keys on, so items go first. The changes:
- `actions.kind` adds `'cleanup','restore','purge'`;
- new columns `actions.ground TEXT CHECK (ground IN ('discard','duplicate'))`, `actions.check_id INTEGER REFERENCES purge_checks(id) ON DELETE SET NULL`, `actions.list TEXT`, `actions.deleted_files`, `deleted_bytes`, and `freed_bytes INTEGER NOT NULL DEFAULT 0`;
- `action_items.op` adds `'record','unlink','purge','verify'`;
- `action_items.state` adds `'blocked'`;
- new columns `action_items.ctime_ns INTEGER`, `draft_bytes INTEGER`, and `draft_files INTEGER`. For cleanup items, `kind`, `dev`, `ino`, `size`, `mtime_ns`, `ctime_ns`, `draft_bytes`, and `draft_files` hold the draft-time identity (D3).

`sources` gains `quarantine_entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL`, with an index.

```sql
CREATE TABLE purge_checks (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('running','ready','failed','stale')),
  job_id INTEGER, created_at INTEGER NOT NULL, finished_at INTEGER,
  junk_confirmed_at INTEGER, stale_reason TEXT);
CREATE TABLE purge_check_items (            -- the set: quarantined top entries
  check_id INTEGER NOT NULL REFERENCES purge_checks(id) ON DELETE CASCADE,
  entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
  path BLOB NOT NULL,                        -- its quarantine path at check time
  readable INTEGER NOT NULL CHECK (readable IN (0,1)),
  PRIMARY KEY (check_id, entry_id)) WITHOUT ROWID;
CREATE TABLE purge_check_files (            -- every recorded entry (files, members, folders, links)
  id INTEGER PRIMARY KEY,
  check_id INTEGER NOT NULL REFERENCES purge_checks(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL,
  entry_id INTEGER, member_id INTEGER,       -- exactly one; member_id belongs to entry_id's archive
  kind TEXT NOT NULL, path BLOB NOT NULL, size INTEGER NOT NULL,
  mtime_ns INTEGER, ctime_ns INTEGER, ino INTEGER, dev INTEGER, nlink INTEGER, alloc INTEGER,
  sha256 BLOB,
  verdict TEXT NOT NULL CHECK (verdict IN ('safe','copy_offline','unique','unreadable','opaque_archive','no_content')),
  class TEXT CHECK (class IN ('possibly_valuable','likely_junk','uncertain')),
  copy_source TEXT, copy_path BLOB, copy_entry INTEGER, copy_member INTEGER,
  copy_size INTEGER, copy_mtime_ns INTEGER, copy_ctime_ns INTEGER, copy_ino INTEGER, copy_dev INTEGER,
  copy_hard_link INTEGER NOT NULL DEFAULT 0,
  confirmed_at INTEGER);
CREATE INDEX purge_check_files_by_check ON purge_check_files(check_id, verdict, class, id);
CREATE INDEX purge_check_files_copy ON purge_check_files(copy_source, copy_path) WHERE copy_path IS NOT NULL;
CREATE INDEX purge_check_files_path ON purge_check_files(check_id, path);
CREATE INDEX purge_check_items_entry ON purge_check_items(entry_id);
```

The gate needs a confirmation for every `unique`, `copy_offline`, `unreadable` (a member that could not be read), or `opaque_archive` file without a verified copy. A `safe` or `no_content` record needs none.

### Commands (strict JSON; IDs are strings)

| Command | Body | Success | Errors |
|---|---|---|---|
| `plan-cleanup` | `{source_id, list?}` | 201 `{action, items, next_cursor, summary}` | 400 `invalid_request` (unknown list, over 30,000 steps); 404; 409 `source_offline`, `writes_disabled`, `writes_unavailable`, `recovery_needed`, `quarantine_name_taken` |
| `plan-restore` | exactly one of `entry_ids` (1–1,000 quarantined top items, one source) or `plan_id`; `destination_id?` | 201 `{action, items, next_cursor}` | 400 (not quarantined top items, two sources, a destination in the quarantine); 404; the write 409s |
| `check-purge` | `{entry_ids}` (1–10,000 quarantined top items, one source) | 202 `{check_id, job_id}` | 400; 404; 409 `check_running`, `source_offline` |
| `confirm-purge` | `{check_id}` and exactly one of `file_ids` (1–1,000) or `group: "likely_junk"` | 200 `{check}` | 400 (a file not in the check, or not gated); 404; 409 `check_stale` |
| `plan-purge` | `{check_id}` | 201 `{action, items, next_cursor}` | 404; 409 `check_stale`, `check_running`, `purge_not_allowed` (with `unconfirmed`), the write 409s |
| `run-action` | R3's | 202 | adds 409 `check_stale` and `purge_not_allowed` for `purge` |
| `plan-undo` | R3's | | adds 409 `action_not_undoable` for `cleanup`, `restore`, and `purge` |

`summary` in `plan-cleanup` holds, from the index:
- `with_copy_bytes`: a `hashed` file with a present copy outside quarantine and the plan;
- `no_copy_bytes`: `unique_size`, `sampled`, or `hashed` with no such copy;
- `unchecked_bytes`: `pending`, `changed`, or `unreadable`;
- `personal_items`: items whose own family is personal, or that hold indicators or a veto.

### Read API

- **`GET /api/quarantine?source=&cursor=&limit=`** → `{items: Quarantined[], next_cursor, total: {bytes, files}}`. Newest first, 100 by default and at most 500.

  `Quarantined {entry: EntryRow, original: {path, path_b64}|null, plan_id|null, quarantined_at|null, bytes, files, check: {id, state}|null}`. A null `original` means unknown origin (D4).
- **`GET /api/checks/{id}`** → `Check {id, source_id, state, job_id|null, created_at, finished_at|null, stale_reason|null, items, counts, confirmed, unconfirmed, junk_confirmed, allowed}`:
  - `items` is the number of set items;
  - `counts` is `{verdict: {<verdict>: {files, bytes}}, class: {<class>: {files, bytes}}}`, where the class buckets cover `unique` files only;
  - `confirmed` and `unconfirmed` are `{files, bytes}` over the gated files. The likely-junk group counts as unconfirmed until it is confirmed;
  - `junk_confirmed` and `allowed` are booleans;
  - the `purge_check` job reports progress as `{files, bytes}` read so far, and `of_files`/`of_bytes`.

  `EntryDetail.in_quarantine` is `{plan_id|null, quarantined_at|null, original: {path, path_b64}|null}|null`.
- **`GET /api/checks/{id}/files?verdict=&class=&confirmed=&cursor=&limit=`** → `{items: CheckFile[], next_cursor}`, 200 by default and at most 1,000.

  `CheckFile {id, item: EntryRow, entry_id|null, path, path_b64, member, kind, size, verdict, class, copy: {source_id, path, path_b64, hard_link}|null, confirmed}`.
- **`GET /api/history/{id}/items/{item}/kept?cursor=`** → `{count, items: EntryRow[], next_cursor}`, 100 to a page.
- **`GET /api/history/{id}/export.csv`**, as in D16.
- **The Action JSON** gains `ground`, `list`, `check_id`, `deleted_files`, `deleted_bytes`, and `freed_bytes`. For kinds `cleanup`, `restore`, and `purge` it also gains `entries`: counts by item state of the steps that stand for an entry, `{"planned":n,"blocked":n,"refused":n,"done":n,…}`. Those steps are the `rename` steps for cleanup and restore, and the `purge` steps for purge.
- **The Item JSON** gains `kept_count` for `blocked` items, and the undo reason `not_undoable_kind`.
- **`GET /api/history/{id}/items`** accepts a repeated `op=` filter. The interface lists `op=rename` (cleanup, restore) or `op=purge`, one row per entry.
- **Refused and blocked entries.** A refused or blocked entry of a cleanup plan is a single `rename` row, in state `refused` or `blocked` with its reason, and no `mkdir` or `record` rows.
- **Other reads.**
  - `GET /api/home` decisions gain `quarantine {files, bytes}`.
  - `GET /api/entries/{id}` gains `in_quarantine`.
  - The sources JSON gains `quarantine {files, bytes, name_taken: bool}`.

### Reader exclusion (each adds `index.NotQuarantined` to the alias it already fetches)

| Slice | Readers |
|---|---|
| **2.1** | `search/filter.go` `buildFilter`, which covers page, count, and resolve, with a `Query.InQuarantine` opt-in for the Cleanup screen; `search/dup.go` `CopiesSQL`, `copiesColumn`, `otherCopy`, and `dupFilter`, on the other copy's alias and on `e`; `web/api` `childOrder.sql`, `treemapSQL`, `treemapRestSQL`, and `onlyFolder` at the top, plus `members.go` `memberSelect` and `memberCopySQL`; `decisions` `totalsSQL`, and Home's quarantine bucket. |
| **2.2** | `content` `Copies` (both arms), `plan.go` `coverageRows`, `groupQuery`, and `insertRows`, and the `job.go` batch selections; `review` `ruleRows`, `loadRelations`, `loadCopies`, `loadGroups`, `openSQL`'s copy arms, and the row-level copy query; `relations` `snapshot.loadDirs` (the folder row), plus `loadFiles`, `loadArchives`, and `compare.side.files`. |

The predicate is a residual written as `NOT (...)`, which SQLite does not use for index lookups. Every existing plan guard and the 2-million-entry tests must stay green.

## Concurrency

- **`plan-cleanup`, `plan-restore`, and `plan-purge`.** These are index-only commands in the write transaction. They re-check writes, recovery, quarantine, keeps, and the check. A commit just after is caught at the first intent of each item, against the draft (D3), the check (D10), and the disk (`verify`).
- **Intent and outcome.** As in R3. Every outcome calls `MarkStale` for its paths, in the same transaction, so a check can never stay `ready` past a move it relied on.
- **Duplicate-ground verification.** It reads outside transactions, and re-checks identity and the "not being quarantined" condition inside the intent (D5). Two plans on different sources cannot both remove the last copies.
- **The check job.**
  - It defers while an organize job of its source is queued or running.
  - Its end transaction sets `ready` only from `running`, and only with every item in place, so a late result never wins (I9).
  - It reads copies on other sources through `sources.Open`. A copy that changes during the check fails its identity check and is not used.
  - Once it is `ready`, any change goes through `MarkStale` (index-visible changes) or `verify` (disk-only changes).
- **Purge.** It holds its source's organize turn: `verify`, then items in order. A step's intent re-checks writes, the check, and the decisions, and replays after a crash pass the same checks (D11).
- **Scans.** They defer while an organize job of the source is active (R3). They walk the quarantine but leave it out of every total, and the check does not block them. A rescan that changes a relied-on copy is caught by `verify` or by the purge's per-file comparison.
- **`remove-source`.** It re-checks the quarantine in its transaction (D14).

## Risks / Trade-offs

- [45 reader edits could miss one] → A test in each slice quarantines a file in the corpus and asserts it from the API: the Map, Search, Home, copies, review cards, Compare, and relations all leave it out. A unit test runs the rendered predicate against a nested quarantined row. The plan guards and slow tests run in 4.2.
- [A check of a large set reads a lot] → It is interactive, yields between files, reports progress, and can be cancelled. Nothing is deleted without it, per §10.6.
- [Per-file confirmations of thousands of photos] → The spec asks for one by one. Restore and move out are the alternatives, and the interface says so.
- [The owner's service user cannot write the archive] → As in R3, writes are unavailable or fail until the deployment grants them.

## Migration Plan

- **Upgrade.** Migration 0007 rebuilds two small tables, adds three, and adds one column to `sources`. Existing actions keep their IDs and history.
- **Rollback.** Restore the backup taken before deploying, since an r3 binary refuses a newer schema.

## Addendum: decisions made during implementation

- **F1. Two indexes besides Interfaces in 0007.** `actions_check ON actions(check_id) WHERE check_id IS NOT NULL` keeps deleting a check linear, as the store's foreign-key guard requires of every reference into `actions`, `action_items`, and `purge_checks`; `purge_checks_by_source ON purge_checks(source_id, state, id)` serves removing a source and `MarkStale`. `sources_quarantine` is partial (`IS NOT NULL`). New columns come last in the rebuilt tables, so 0006's columns keep their order. Tables, columns, and CHECKs are exactly as in Interfaces.
- **F2. The top's fold leaves out any entry at the reserved name.** A file, link, or special file named `.precious-quarantine` at a source's top is still indexed and classified (into a fold of its own), but the top folder counts it no more than the folder, so totals agree with `NotQuarantined`, which hides the path whatever its kind. The quarantine raises no name signal or indicator of the top either. The scan's progress counters (`files`, `bytes`) still count what the walk read, quarantine included.
- **F3. `NotQuarantined(alias)` and `InQuarantine(alias)`** take a plain SQL identifier and panic on anything else, since the condition is spliced into SQL text.
- **F4. `DeleteSubtree` and `DeleteEntries`.** Both refuse a source's top folder, and skip IDs no longer indexed, so a replayed outcome deletes nothing twice. `DeleteEntries` deletes each entry with whatever is still indexed below it, shallowest first, so no row is left under a deleted folder.
- **F5. The adapter's guards.** `ApplyPurge` and `ApplyUnlink` refuse an entry or path of another source, or not strictly below the quarantine folder (the folder itself included), and change nothing then; `ApplyUnlink` also refuses a folder's row. `ApplyPurge` refolds the folders that held the removed entries; a folder removed too is skipped by `Refold`, and the folder above the outermost removed entry folds the chain up to the top.
- **F6. Unknown ops fail safe.** An op without a step in this executor ends `failed` (`unknown step <op>`) at intent, as before, and `run.item` no longer sends any op it does not know to `rmdir`: an item that reached intent with such an op ends `failed` too, with nothing on disk. Until slice 2.3, `record`, `unlink`, `purge`, and `verify` items end that way.
- **F7. `blocked` is counted.** The Action JSON's `counts` gains `blocked`, and the history's `state` filter accepts it.
- **F8. `MarkStale` matches copies on their own source.** A check of any source whose recorded copy has `copy_source = src` and `copy_path` at or below the path is marked, besides checks of `src` with a set item or recorded file there; an empty path is the source's top and matches everything of `src`. It records `stale_reason = 'index_changed'` (`stale.ReasonIndexChanged`) and leaves `finished_at` as it is.
- **F9. `CreateExclusive` and `Unlink` (fsaccess).** The new file is `fchmod`ed to the parent's mode `& 0666` after `openat`, so the umask has no effect, as `Mkdir` does. A partial file is removed only while the name still has the inode the call created. `Unlink` reads `EPERM` as `ErrIsDir` only when `fstatat` shows a folder. The portable backend refuses both with `ErrNoReplaceUnsupported`. synthfs checks a read-only device before the name, as its `Mkdir` does. `instrument` logs a create with `N = len(data)`.
- **F10. On-demand hashing (content).** `HashEntry` reads in `hashing.read_chunk_bytes` windows (the default, since it has no `Service`), and fstats the open file after the read: any difference is `invalid_entry_state`. `HashArchive` calls `fn` only after the whole archive was read and found unchanged, in read order, with results held in memory (bounded by `archives.max_members`); a file member without its `archive_members` row, a listed member never reached, a different size, or a duplicate path is `invalid_entry_state`, as `OpenMember` treats a listing mismatch. A tar hard link reports its own member ID with its target's digest. Incomplete listings answer as `OpenArchive` does (`invalid_entry_state`; `not_found` without an archives row). `HashMember` uses `OpenMember`'s lookup and `OpenArchive`, so it can fstat the archive after reading; a streamed archive is read up to the member only. `HashArchive`'s parameter is named `id`, not `archive`, which would shadow the package; its type is as pinned.
- **U1.** `GET /api/checks/{id}/files` takes `confirmed=1` or `confirmed=0`, as the review lists take `decided=`.
- **U2.** The interface lists a cleanup's or restore's entries with `op=rename`, and a purge's with `op=purge`: in the preview, and in History's Show items. History's list of items that need a check stays unfiltered. Counts come from `action.entries`; the other kinds keep `counts`. `kept_count` is read as optional, absent on items that are not blocked.
- **U3.** Delete for good plans first (`plan-purge`, index-only), then asks the final question with the purge plan's own `files` and `bytes`, and its `entries` counts, then runs it. Keep them leaves the plan to expire. The interface never adds up check buckets for this question, since archive members and their archive would count twice.
- **U4.** Check again rebuilds the set from `GET /api/quarantine?source=` (500 to a page, every page), keeping the items whose `check.id` is the stale check. Restored items are therefore left out; with none left, the report says so and starts nothing.
- **U5.** The Cleanup screen fetches each shown source's quarantine whatever `sources.quarantine` says, since a quarantined empty folder has no files and no bytes.
- **U6.** Home always shows In quarantine as the fifth decision bar, zero included, measured against the totals like the four others.
- **U7.** The detail panel hides the decision, tags, organize, and category controls for any entry with `in_quarantine`, nested ones included; the classification facts, the preview, Open, Show in Map, and Compare stay. A missing date reads "In quarantine, from …", and a missing origin "an unknown place".
- **U8.** History offers no Undo for `cleanup`, `restore`, and `purge` even if an answer said it could, and adds Export CSV to every action.
- **U9.** `purge_check` events store their progress by job ID. A running event refetches the checks themselves but not their file lists; a terminal one refetches every check response and the quarantine. A terminal organize event refetches the quarantine and the checks too.
- **U10.** A check file needs its own confirmation when it is `unique` and not `likely_junk`, `copy_offline`, `unreadable`, or `opaque_archive` with no copy. Only those files offer Confirm, Restore its item, and, when `entry_id` is set, Move out…. A member is shown as "<member> inside <path>".
- **U11.** The Sources card also names a `.precious-quarantine` folder of the owner's own (`quarantine.name_taken`), as D1 asks.
- **A1. Private copies of the quarantine conditions where `index` cannot be imported.** `index`'s own tests import `decisions`, which imports `search`, and `index` imports `sources`, so none of the three can import `index` (a test import cycle, or a plain one). `search` keeps `inQuarantine`/`notQuarantined`, and `decisions` `notQuarantined` and `quarantinePath` (for group 3's `in_quarantine` refusals), each rendering exactly `index`'s text from the same name; a test in each package asserts equality with `index.InQuarantine`, `NotQuarantined`, and `IsQuarantinePath`. `sources` keeps the name alone, and the read API's quarantine test finds through it a folder made at `index.QuarantineName`. `web/api` imports `index` as pinned.
- **A2. The Map's residual is on every level.** Children, treemaps, and `only_folder` add `NotQuarantined` whatever the parent, so a quarantined folder lists nothing even when named by ID; `ancestors[].only_child` leaves quarantined siblings out too, so a top holding one folder besides the quarantine reads as holding one. `memberSelect` carries the residual on the archive's entry: an archive in quarantine lists no members, and its members answer `404` by member ID, as nothing below the quarantine is shown outside the Cleanup screen. The entry detail itself (`GET /api/entries/{id}`) answers for any entry.
- **A3. Copies and the duplicate filter.** Every copy alias (`ce`, the lowest hard link `cle`, `cae`, `oe`, `oae`) carries `NotQuarantined`, so a hard-link set counts by its lowest name outside the quarantine. `copiesColumn` is `NULL` for a file in quarantine, which is no copy itself, so "this one included" cannot hold. `dupFilter` does not repeat the residual on `e`: `buildFilter` already holds it on the same row; under `Query.InQuarantine` a quarantined file's duplicate state reads its copies outside the quarantine.
- **A4. `Query.InQuarantine`** is `json:"-"` and has no URL parameter: a stored selection never carries it, and `create-selection` cannot select the quarantine.
- **A5. `in_quarantine`.** Any entry whose path `IsQuarantinePath` gets the object. Its fields come from the newest `done` cleanup `rename` item whose `entry_id` is the entry's top item (the entry at the path's first four components, `.precious-quarantine/<plan>/<seq>/<name>`), whose `to_path` is that path, and whose action is a `cleanup` of the same source: `plan_id` is the action, `quarantined_at` the item's `finished_at`, `original` its `from_path`. The quarantine folder, plan and `<seq>` folders, records, and an item with no such rename have every field `null`.
- **A6. Home and sources figures.** Home's `decisions` gains the key `quarantine`, the sum over the chosen sources of the row at `.precious-quarantine` when it is a folder that is not missing (`0` otherwise). The sources JSON's `quarantine.files`/`bytes` use the same row; `name_taken` is true when an entry of any kind at that path is not missing and `sources.quarantine_entry_id` is NULL or names another entry.
- **B1. `relations` renders the residual itself.** `index` imports `relations` through `sources` (`RequestRefresh`), so `relations` cannot import `index.NotQuarantined`. It keeps an unexported `notQuarantined(alias)` and `isQuarantinePath`, from its own copy of the name; an external test (`relations_test`) holds the name, the predicate on every alias it uses, and the path test equal to `index`'s.
- **B2. `snapshot.loadDirs` drops the whole quarantine subtree.** The residual on the folder query leaves out the folder's row and every folder below it in one pass; files and archives below fall away by their own residuals (and, as before, as below a missing folder). A side's `gap` is unchanged, so the top is not marked partial by the quarantine.
- **B3. Compare applies the residual only to a side outside the quarantine.** A source's top, or any folder outside, never lists quarantined files; a folder inside the quarantine still compares with its own files (U7 keeps Compare in a quarantined entry's detail).
- **B4. Hashing's commits need no residual.** Every result, listing, and member write already re-checks the path its read used (r3 D18), so a file or archive moved into the quarantine mid-read is dropped. `samplePass`'s list of sizes and its "settled" test read `file_content` and members without `entries` and are left as they are: a quarantined row there only costs an empty group or a full read instead of samples, never a read of a quarantined file.
- **B5. Published coverage follows the next hashing plan.** `coverageRows` and `groupQuery` exclude the quarantine, but `content_coverage` is rewritten only by a plan, so after a cleanup or restore it counts the moved files until the source's next hashing job; group 3 makes `ActionDone` enqueue it. A `hashed` copy left alone in its size keeps its digest (D3), so the staying copy of a quarantined pair stays `hashed`.
- **B6. Review rows read live drop quarantined copies at once.** `openSQL`'s copy arms and the row's `Copy` count and offer copies outside the quarantine only, so a duplicate group with one copy left outside is no longer open before the next refresh rewrites it; the `Copy` query's member arm joins its archive's `entries` row for the residual.
- **C1. The rows of `purge_check_files` (agreed with the executor slice).** Every recorded entry, the set item itself included, has `item_id` = its set item's `entry_id`, `entry_id` set, `member_id` NULL, its source path, and the identity of the lstat the check made (`ctime_ns` NULL only where the platform has none; `alloc` = blocks × 512). An archive member has `member_id`, `entry_id` = its archive's entry, `path` = the archive file's path, kind `file`, its size, and no disk identity: the executor's `verify` and `purge` read only `member_id IS NULL` rows. `copy_*` is the lstat of the copy's file (the archive file for a member copy) and `copy_path` its source path; `copy_hard_link = 1` only for a file copy with the recorded file's device and inode.
- **C2. A complete archive is its members.** Its own row is `no_content` with no digest, and each file member carries its verdict (an empty member is `no_content`). An archive that `HashArchive` refuses with `invalid_entry_state` or `not_found` (its listing no longer matches) is read with `HashEntry` as one opaque file instead. An opaque archive (an `archives` row not `complete`, or file kind `archive` with no row) with a verified copy is `opaque_archive` with the copy; with offline candidates only it is `copy_offline`; otherwise `opaque_archive`, class `uncertain`.
- **C3. Unreadable items and files.** An item is not readable (`readable = 0`) when it or an entry below it is a folder whose state is `unreadable` or that is `partial`, or is a mount point. Its entries are all recorded `unreadable` with the index's identity, and nothing in it is opened or counted in `of_files`. A file whose index state is `unreadable`, or whose read fails with the unreadable outcome, is `unreadable` and keeps its item readable. Missing rows are skipped: nothing is on the disk to record.
- **C4. A set entry that no longer matches the index fails the check.** A file `HashEntry` refuses as changed, or a folder, link, or special file whose lstat has another kind or inode, ends the job `invalid_entry_state` and the check `failed`; the owner scans and checks again. A copy candidate that fails its read is passed over.
- **C5. The copy search.** Candidates, outside quarantine by `index.NotQuarantined("e")`: present files with the digest's content, then file members of complete archives with it, then present files of the same size whose `file_content` state is `unique_size`, `pending`, `sampled`, `changed`, or `unreadable`, on each online source. Files without a `file_content` row are not searched. A source that cannot be opened counts as offline for the first two kinds; a same-size file there proves nothing. Each digest is searched, and each candidate read, once per attempt. Classes are set on `unique` files and on opaque archives with no verified copy only; a file's family is its `entries.family` or its category's family, and a member is ranked by `Policy.FileKind` and `ClassifyFile` of its name.
- **C6. The job's life.** Payload `{"check_id":"<id>"}`, scope `purge_check:<id>`; `enqueueCheck` (for check-purge) also sets `purge_checks.job_id`. It defers 3 s (`index.DeferOrganizing`) while `executor.OrganizeActive` holds. Each attempt starts over, deleting earlier records and setting each item's `readable`. Records are written 256 at a time and at the end of each item, each write after re-checking `running`, so a check that went stale stops there. A cancel request ends it `failed`; a stop or a lost lease leaves it `running` for the next attempt; a check found not running only gets its `finished_at`. `settleChecks(tx, now)` fails every running check whose job is no longer queued, running, or paused (cancelled before it ran): group 3 calls it in `check-purge` and the check reads. `rt.Yield` runs before each set file is read; copies on other devices are read without `UseSource`.
- **C7. The end transaction.** An item missing from the index, on another source, at another path, or not `present`, is passed to `MarkStale` at its recorded path; an item whose row is gone (its `purge_check_items` row went by cascade) leaves the check `stale` too. Every end sets `finished_at`.
- **E1. A cleanup item's rows.** The k-th item is a `mkdir` of `<n>` (`to_dir_seq` naming the plan folder's mkdir, or `to_parent` that folder), a `rename` (`entry_id`, `from_parent`, `from_name`, `from_path`, the draft identity, `to_dir_seq` naming that mkdir, `to_name` the entry's name), and a `record` (`entry_id` the rename's, the plan folder as for the mkdir, `to_name` `<n>.json`). A mkdir is an item's first step when a planned rename of its action names it in `to_dir_seq`; the item's record is the planned record with that rename's `entry_id`. The plan-level mkdirs are the quarantine's (`to_parent` the source's top) and `<plan>` (named by the action's ID). Of the draft identity, `kind` must equal the index's and, for anything but a folder, `size` must be set and equal; `dev`, `ino`, `mtime_ns`, `ctime_ns`, `draft_bytes`, and `draft_files` are compared when set: equal to the index row, and to the lstat under the source's capabilities (device and inode where stable, times within the resolution, the change time only when both are known, a folder's modification time included).
- **E2. A first step that fails its re-check** ends the mkdir, the rename, and the record in the same transaction, with the same state and reason, the rename row carrying what the interface shows. When the rename fails later (its own intent or step), the empty `<n>` stays, and the record ends `not_attempted` at its intent, since its rename is not done. A cleanup rename whose lstat differs from the draft ends `changed`/`identity_changed`.
- **E3. The quarantine's mkdir.** When the source's quarantine is already Precious's (`quarantine_entry_id` names a present folder at the reserved name), the mkdir ends `done` with that `entry_id` and `created = 0`, and nothing is asked of the disk, so two plans drafted before either ran both run. The outcome of a mkdir that made it sets `quarantine_entry_id`, for a cleanup action only. A folder there that Precious did not make fails the mkdir `conflict`/`name_taken`, and the plan's items end `changed`.
- **E4. Refusals at intent (D1, D6, D13).** Any kind: renaming the quarantine folder itself, or any entry to the reserved name at a top folder, is `refused`/`reserved_name`; a mkdir of that name at a top folder is too, unless the action is a cleanup. Organize kinds: a destination at or below the quarantine is `refused`/`in_quarantine`. A cleanup rename must move an entry not in quarantine (else `refused`/`in_quarantine`) to a folder strictly inside it (else `failed`). A restore rename must move a quarantined entry (else `changed`); a destination that is gone, not a present folder, or in the quarantine is `conflict`/`previous_folder_gone`. The rmdirs of restores and purges must be strictly inside the quarantine (else `changed`). The interface's reason catalog has no `reserved_name` yet; planning refuses those names first (D13), so the executor's refusal is a backstop.
- **E5. D5's verification.** It covers the files (kind `file`, not missing) at or below the item; an empty file needs no copy, and a file that is not `hashed` has none. For each distinct content, candidates are present files hashed with it, outside the quarantine, own source first, then file members of complete archives (through `Options.Content`, skipped when nil). A candidate is excluded when it or a folder above it is the entry of a planned or intent rename of the same action, or when it is at or below the `from_path` of an intent cleanup rename on its source, or its source cannot be opened. It is read with `content.HashEntry` against its index row (`HashMember` for a member) and compared with `contents.sha256`; the first match is the copy. The copies are kept per rename item for the attempt (read again only after a restart), and both intents re-check them: the index row unchanged (present, path, size, `mtime_ns`, `ctime_ns`, `ino`), still hashed with that content (a member: that content in a complete archive), and not excluded as above. The item's own digest is the index's; the pre-delete check reads it again (D7).
- **E6. The origin record** is one line of JSON and a newline: `{"version":1,"source_id","entry_id","plan_id","original":{"path","path_b64"},"quarantined_at"}`, IDs as strings, `path` in display form, `path_b64` the raw bytes. `original` is the rename's `from_path`, `quarantined_at` its `finished_at` in RFC 3339 (UTC), so the same bytes are built again when reconciling (D4). A file over 64 KiB is never read as a record.
- **E7. Unlink items** name `from_parent`, a folder strictly inside the quarantine, and `from_name` `<n>.json` (any other name fails). An unlink ends `not_attempted` while the index holds an entry, not missing, below `<folder>/<n>`, so a restore whose rename failed keeps the record, and a sweep removes only records whose item is gone. The step removes only a regular file that parses as a record of this source whose `plan_id` is its folder's name, with the index row's identity when a scan indexed it; anything else ends `changed`. The outcome calls `ApplyUnlink` for its path.
- **E8. Purge items.** `verify` has no columns. A `purge` item names the set item in `entry_id`; its intent resolves `from_parent`, `from_name`, and `from_path` from the index. The item must be in the action's check (`purge_check_items`), readable (else `refused`/`unreadable`), at the path recorded, and strictly inside the quarantine with its folder too; an item directly in the quarantine folder is never purged (`changed`). Its records are the `purge_check_files` rows with `item_id` its entry, `member_id` NULL, at or below its path; the check records the set item itself (agreed with slice C). The gate is recomputed in each intent: an unconfirmed `unique` file that is not `likely_junk` (or is, before `junk_confirmed_at`), `copy_offline`, `unreadable`, or `opaque_archive` without `copy_path` ends the item `changed`/`check_stale` and stops the action, as a check not `ready` for this source does.
- **E9. Verify** walks each readable set item whole from its `<seq>` folder (opened through the index's chain) and compares it fresh, folders with their times. The copies relied on are the distinct copies of rows with `copy_path`, not confirmed, and not `copy_offline`; each is lstat'd component by component from its source's top, following no link. A difference ends verify `changed`, `file_changed` or `copy_changed`, with the path as detail, marks the check `stale` with `stale_reason` `disk_changed` (`index_changed` stays MarkStale's), and stops the action.
- **E10. Purge comparisons.** The kind; device and inode where stable; for anything but a folder, the size, the modification time within the resolution, and the change time when both are known, except for an inode the purge removed a name of; for a folder, its times only before deleting (just before its `Rmdir`, the kind and inode). Those inodes are the names this attempt unlinked with a link count above 1, the recorded files of the check whose rows earlier recorded steps deleted, and, in a replay, the recorded files no longer on disk. A difference or a refusal (`ErrIsDir`, `ErrNotEmpty`, absent) during the deletion ends the item `changed`/`file_changed` with the removed entries recorded (`ApplyPurge` with their IDs) and the check stale; another error ends it `failed` with the system's text, stopping the action on a read-only filesystem.
- **E11. After a whole item** the record `<n>.json` is unlinked only when it parses as a record of the item's entry (another file stays for a sweep), then `<n>` is removed (`ErrNotEmpty` leaves it, the item still done). The outcome deletes the rows with `ApplyPurge` on `<n>` when it went, else on the item, and calls `ApplyUnlink` for the record. `deleted_files` and `deleted_bytes` count regular files; `freed_bytes` adds `st_blocks * 512` of each file whose link count was 1 when removed. A replay counts what it found deleted from the records (`alloc` when the recorded link count was 1).
- **E12. MarkStale in every outcome.** Done outcomes, purge outcomes, and the `manual_recovery` that replaces a failed outcome call `MarkStale` for `from_path` and `to_path`, and a purge also for `<n>` and its record. The purge's own check, `ready` when the outcome of an item done whole starts, is set back to `ready` after those calls in the same transaction, where only they can have changed it; a partial purge leaves it stale.
- **E13. Replay (D11).** At reconciliation the re-checks run in a write transaction: `CheckWrites` (`writes_off`), the check and its gate (`check_stale`, stopping the action), the item at its path, discarded, with nothing kept (`decision_changed`; any other difference of the item `file_changed`). When they pass, the step runs again, taking recorded entries absent from the disk as deleted before; with `<n>` gone, only the record is looked at. When they fail, the walk finds what is gone, those rows are deleted, and the item ends `changed` with the reason, nothing unlinked. A `<n>` that cannot be opened because it changed ends the item `manual_recovery`.
- **E14. Hooks** run around each Writer call, so a purge step calls them once per unlink and rmdir; the tests crash it midway that way.
- **S1. A quarantined top item** is an entry, not missing, at `.precious-quarantine/<plan>/<seq>/<name>` below the folder `sources.quarantine_entry_id` names. An owner's own `.precious-quarantine` (name taken) holds none: the quarantine read, `plan-restore`, `check-purge`, `plan-purge`, and `remove-source`'s refusal ignore it, so nothing of the owner's is restored or purged as Precious's.
- **S2. Scope.** A source's plan takes its own discards outside the quarantine (quarantined items keep theirs and are never planned again); `in_quarantine` arises only for a list row of an older review generation. Nested discards always fold into the topmost one, refused or blocked included, so no part of a blocked folder moves (R4.1). An empty scope answers `400 invalid_request`. A plan with no planned entry has no plan-level mkdirs, so it cannot run and makes no empty folders.
- **S3. The duplicates rules** run over the list's rows in its order, relation rows first, then content-group rows; a refusal wins over an earlier choice. A copy counts as staying when it is a present file, or a member of a complete archive, outside the quarantine and not at or below an entry the plan holds, on any source; a planned entry later blocked still counts as leaving (conservative). The executor's D5 verification remains the guarantee.
- **S4. The summary** counts the files below planned items that have a `file_content` row: `pending`, `changed`, `unreadable` as unchecked; `unique_size`, `sampled` as no copy; `hashed` by the copy test of S3. Empty files have no row and count nowhere. `personal_items`: own family `personal`, a veto, or a folder whose `dir_stats.indicators` is not empty.
- **S5. Kept entries** listed by `/kept` and counted by `kept_count` are the entries at or below the item with their own `keep` (the sources of its effective keeps), in any state, by path.
- **S6. Lifetimes.** A restore or purge plan runs for an hour, as R3's plans; a cleanup plan for 24 hours (D3). All three prune expired plans as organize does.
- **S7. Restore.** The origin is A5's rename. A conflict keeps a single `rename` row; a destination is tried only after the original place fails. The sweep (D6) runs for a plan folder only when every top item below it leaves in the action: `rmdir` of the other `<seq>` folders that hold nothing, `unlink` of the records whose item is gone (the cleanup's done records not unlinked since, and `.json` files a scan indexed), then `rmdir <plan>` unless something else stays there. A purge sweeps the same way, its own items' `<seq>` and records being its purge steps'.
- **S8. The purge gate** is one statement: `executor.PurgeGate`, used by the executor's intents, `organize.GatePurge` (plan-purge and run-action), and the Check JSON's `unconfirmed`. `purge_not_allowed` names up to ten unconfirmed files by path ("<member> inside <archive>"), in its message, which the interface shows as is. `plan-purge` answers `400` when no set item is readable, and `409 check_stale` when a set item is no longer a quarantined top item.
- **S9. confirm-purge** answers `409 check_running` for a running check and `409 check_stale` for a stale or failed one, and writes the audit event `purge_confirmed` (check, source, files or group, files and bytes).
- **S10. Reads.** The quarantine lists newest plan first by the plan folder's and the `<seq>` folder's names read as numbers; `total` is the quarantine folder row's totals, records included (as Home's bucket, A6). `check` is the newest check holding the item, in any state. The check reads settle (C6) through a write transaction only when a running check's job has ended. Check files page by ID; `confirmed` is a file's own confirmation, or its likely-junk group's.
- **S11. ActionDone** enqueues the hashing of the action's source (when online) for every kind, not only cleanup and restore: the plan pass is cheap and keeps coverage true after any move (B5).
- **S12. D13's shapes.** `plan-rename`, `plan-merge` (either side), and `plan-rescue` of a quarantined entry answer `409 in_quarantine`; a bulk move's or an undo's quarantined item is `refused in_quarantine`; an undo whose previous folder is in the quarantine finds it gone. Every plan's destination in the quarantine is `400` (organize's `folderArg`). The reserved name at a top is `400` for `plan-rename` and `plan-create-folder` and `refused reserved_name` for a move's item (the interface's catalog gains `reserved_name`).
- **S13. Export operations** are `quarantine` (a cleanup's rename), `restore`, `rename`, `move` (every other rename), `create_folder`, `remove_folder`, `write_record`, `remove_record`, `purge`, and `verify`; lines end with CRLF.
- **S14. Hard links in a plan (open, executor).** A rename advances the change time of its inode, so a later item of the same plan that is another name of that inode ends `changed`/`identity_changed`, and an item whose only staying copy is another name of a renamed inode ends `changed`/`no_verified_copy` (that name's index row keeps the old change time until a scan). Nothing is lost, and the owner drafts again after a scan; making the executor accept the change time a done rename of the same action caused is left open.
- **G1. A relations refresh is never lost (an R2 bug, found by R4's tests).** `RequestRefresh` sets `review_state.dirty` and calls `EnqueueOnce` with scope `relate`. A pass clears the flag at its start and reads it again when it flips the generation, so a refresh up to that read makes the running job pass again. A refresh after that read (while the pass prunes, or before the runner marks the job succeeded) coalesced into the running job, which then ended: the flag stayed set with no job queued, and review rows lagged until a later refresh (about one R4 test run in ten). Now, when the `relate` job `EnqueueOnce` returns is `running`, `RequestRefresh` also enqueues once a follow-up with scope `relate:next`. Two scopes suffice, since pool `relate` has capacity 1: one job runs, and at most one waits. Every job `RequestRefresh` and `Startup` enqueue carries the payload `{"if_dirty":true}`. On its first attempt such a job runs no pass when the flag is clear: a pass that began after the last refresh already covered it. A retry after a lost worker, and a job without the flag (tests call `Run` directly), always pass. A pass that fails sets the flag again, since it cleared it at its start. Otherwise a waiting follow-up, or the next `Startup`, would take the stale relations as fresh. A refresh can now add a second, passless `relate` job to the history.
- **G8. `remove-source` counts only quarantined top items (S1).** Its refusal joins the quarantine folder to a plan folder, a `<seq>` folder, and an entry not missing below it, by `parent_id`, so a plan folder whose items all stayed where they were (the plan-level mkdir runs first), or a `<seq>` folder and its record left after the item was moved out, no longer refuse the removal forever while the Cleanup screen lists nothing. No sweep removes those leftovers: they go with the source's index, and nothing on the disk changes.
- **G9. A copy decided again before its record is written makes the check stale.** MarkStale finds a relied-on copy only through its `purge_check_files` row, and the check writes rows up to 256 at a time, so a set-decision, set-category, or set-group on a copy (or a folder above it) between the copy's read and that write marked nothing, and the check ended ready. Now, when the copy search finds a copy, the check notes the state of the copy's file and of every folder above it (by `parent_id`, from the index row: path, parent, state, own and effective decision with its time and origin, category, family, triage, group and veto, and the `entry_overrides` row with its `updated_at`), each as first seen in the attempt; `finish` reads them again in its transaction and ends the check `stale` (`index_changed`) when any differs or is gone. A change after the last write is marked by MarkStale as before. A rescan that rewrites a folder's classification also counts: staleness errs toward checking again.
- **G10. A fully purged check is not ready to delete again.** Each done purge item puts its check back to `ready`, and the purged entries take their `purge_check_items` rows with them, so such a check read `ready` with `items: 0` and `allowed: true`, and `plan-purge` then answered a misleading `400`. `allowed` now also needs `items > 0`; `plan-purge` of a check with no set item left answers `409 check_stale` (the set is gone); the interface reads `items === 0` as nothing left: the set-gone notice, with no confirm or purge controls.
- **G11. Check again checks the check's own set (replaces U4's rebuild).** `check-purge` takes exactly one of `entry_ids` and `check_id`. With `check_id` it takes that check's `purge_check_items` entries that are still quarantined top items of its source (restored, moved-out, and purged ones drop out), by path; an unknown check, or one with none left, is `404 not_found` ("no item of check N is in the quarantine any more"). The interface's Check again sends `{"check_id"}` and shows the set-gone notice on `not_found`, instead of rebuilding the set from the quarantine pages by each item's newest check, which a newer check over the same items emptied, so an older check's Check again claimed the set was gone while every item was still in quarantine.
- **G12. A complete archive counts as its members.** Its own record stays `no_content` (C2, listed with the folders in the files), but the Check's verdict counts take no bytes from a `no_content` file record (an empty file, or a complete archive), so the "nothing to copy" bucket no longer adds the archive's bytes on top of its members', and the buckets add up to what is in the set. Folders and links keep their own size, as recorded.
- **G15. Files of an unreadable item are not offered for confirmation.** The executor's, organize's, and cleanup's gates leave out the records of items with `readable = 0`, which `plan-purge` refuses anyway. CheckFile gains `item_readable` (the record's item's `purge_check_items.readable`, true when the row is gone); the interface's `needsOwnConfirmation` is false for such a file, which offers no Confirm (Restore its item stays) and says it stays in quarantine because its item could not be read. `confirm-purge` still accepts such a file, harmlessly.
- **G13. A purge that stopped at its verify step says why (amends U2).** The interface lists a purge's items with `op=verify` and `op=purge`, verify first, in History's Show items, so a `verify` that ended `changed` shows its reason (`file_changed` or `copy_changed`) and its path; counts still come from `action.entries`. CleanupStatus, for a purge that ended `stopped`, reads the purge's `op=verify` items and lists each that is not `done` under "Why it stopped". Before, both showed only `not_attempted` purge rows with no reason (the CSV export alone carried it).
- **G14. The check report keeps nothing of another check, and names each file's actions.** CheckReport is keyed by the check's ID, so moving between two cached reports (Back after Check again) remounts it: an open purge confirmation, a purge's status, the set-gone notice, restore and move-out previews, and errors no longer carry over to another check, where Delete for good would have run the other check's purge. Each file row's Confirm, Move out…, and Restore its item sit in a `role="group"` named "Actions for <file>" (a member as "<member> inside <path>"), as DecisionButtons names its rows (U10).
