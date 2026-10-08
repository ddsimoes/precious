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
- **`GET /api/checks/{id}`** → `Check {id, source_id, state, created_at, finished_at, stale_reason, items, counts, confirmed, junk_confirmed, allowed, unconfirmed}`. `counts` has a bucket per verdict, and per class for `unique`, each `{files, bytes}`.
- **`GET /api/checks/{id}/files?verdict=&class=&confirmed=&cursor=&limit=`** → `{items: CheckFile[], next_cursor}`, 200 by default and at most 1,000.

  `CheckFile {id, item: EntryRow, entry_id|null, path, path_b64, member, kind, size, verdict, class, copy: {source_id, path, path_b64, hard_link}|null, confirmed}`.
- **`GET /api/history/{id}/items/{item}/kept?cursor=`** → `{count, items: EntryRow[], next_cursor}`, 100 to a page.
- **`GET /api/history/{id}/export.csv`**, as in D16.
- **The Action JSON** gains `ground`, `list`, `check_id`, `deleted_files`, `deleted_bytes`, and `freed_bytes`. The Item JSON gains `kept_count` for `blocked` items, and the undo reason `not_undoable_kind`.
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
