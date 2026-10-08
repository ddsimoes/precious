# Design

## Context

See proposal.md for why. Today nothing writes to a source:
- `internal/fsaccess` has no write method, by design.
- The `sources` table has no write permission.
- The scanner matches entries only by parent and name, so a folder moved outside Precious comes back as new entries, and the old ones are marked missing.
- Effective decisions are materialized in `entries.eff_decision`/`eff_from`, and kept true by path-range updates.
- Folder totals, `dir_stats`, and folder classification are computed only by a scan's bottom-up fold.
- Relations, `dir_dups`, and review rows are computed only by the relate job.
- Scans yield their device between folders, so another job of the same device can run in the middle of a scan.

## Goals / Non-Goals

**Goals:** close R3.1–R3.7 on Linux, with an executor that R4 reuses unchanged.

**Non-Goals:**
- Quarantine and plans (R4).
- Writing modification times (R5).
- The no-replace rename on macOS and Windows (R8).
- Moves between sources (D7).
- Multi-select on the Map.

## Decisions

### D1. Write permission: a column, a config switch, and availability (§6.1; R3.7)

Migration 0006 adds `sources.write_enabled` (0 or 1, default 0). A source's writes are **unavailable**, with the first reason that applies:
1. `forbidden_by_config`: `[sources] allow_writes = false`;
2. `read_only`: `capabilities.read_only`;
3. `no_replace_rename`: `!capabilities.no_replace_rename`.

`set-source-writes` turns the column on (refused when unavailable) or off (always allowed). The confirmation is a UI dialog. The command is the confirmed act.

`allow_writes` defaults to `true`. Every source still starts with writes off, and the example deployments keep sources read-only at the OS level (systemd `ReadOnlyPaths`, read-only compose binds), so upgrading changes nothing until the owner acts. The docs say how to allow writes.

Rejected alternatives:
- `allow_writes = false` by default. The owner would then edit the configuration before the interface's toggle meant anything, which is the opposite of §6.1.
- A server-side `confirm: true` field. It adds nothing a client cannot send blindly.

### D2. `no_replace_rename` is a detected capability, corrected at run time (§6.1, §5 I3)

`fsaccess.Capabilities` gains `no_replace_rename`:
- **True** on Linux for the local types whose drivers honor `RENAME_NOREPLACE`: ext2/3/4, xfs, btrfs, zfs, f2fs, tmpfs, vfat, exfat, ntfs3.
- **False** for ntfs-3g (fuseblk), iso9660/udf, unknown types, and the portable backend on macOS and Windows.

Old OpenZFS releases answer the flag with `EINVAL`. The kernel rejects an unknown flag before touching anything, so `EINVAL` is a clean refusal. The executor then ends the item `no_safe_rename`, stops the action, and turns the source's write permission off with an audit event.

Rejected alternatives:
- Probing with a test rename when writes are turned on. A probe needs two real files, which is a write the owner did not ask for.
- Trusting the type table alone. That gives a false `true` on old ZFS.

### D3. One executor, three primitives, rooted handles only (§5 I2, I3, I6)

`internal/executor` is the only importer of the new `fsaccess.Writer`:
- `RenameNoReplace`: `renameat2(fromDirFD, name, toDirFD, newName, RENAME_NOREPLACE)`;
- `Mkdir`: `mkdirat(fd, name, 0777)`, under the process umask;
- `Rmdir`: `unlinkat(fd, name, AT_REMOVEDIR)`, which only removes an empty folder.

Each call goes through the raw descriptors of two `Dir` handles opened by the existing rooted, identity-checked descent from the source root, never by path. `Rmdir` serves only undo of a folder the same action created and recorded as `created`.

A vet-style test fails the build if any package but `internal/executor` (and fsaccess's own tests) calls a `Writer` method.

Rejected alternatives:
- `os.Rename`. It replaces existing destinations.
- `link` plus `unlink`. It does not work for folders, and leaves two names during a crash.
- `openat2` with `RESOLVE_BENEATH` as the boundary. It was already rejected in R1, and stays a later defense in depth.

### D4. The journal is the action's items; each step has an intent and an outcome (§10.2 Mechanics; R3.2)

Tables `actions` and `action_items` (Interfaces) hold both the history and the journal. One item is one filesystem step:
- `rename`, for a move or a rename;
- `mkdir`;
- `rmdir`.

An item's life:
1. **Planned.** `organize` writes it as `planned`, or as `refused`/`conflict`.
2. **Intent.** The executor re-checks the item and commits `intent`, with the resolved old and new parent IDs, names, and paths, and the identity it expects (dev, ino, kind, size, mtime).
3. **Step.** It calls the primitive.
4. **Confirm.** It lstats both names through the same handles.
5. **Outcome.** In one transaction, it records the outcome and updates the index (D6).

`store` already opens the writer with `synchronous=FULL`, so an intent that committed survives a power cut.

**Reconciling** an `intent` item looks at both names:

| Old name | New name | Result |
|---|---|---|
| holds the expected identity | free | back to `planned`; it runs once |
| free | holds the expected identity | `done`, with the index update; no second step |
| anything else | anything else | `manual_recovery`, with the findings |

For `mkdir`, an empty folder at the new name that is absent from the index counts as done. For `rmdir`, a free name counts as done.

Reconciliation runs at the start of every organize attempt for its source, before anything else, and from `executor.Startup` after the runner starts. Startup enqueues the organize job of every source with an `intent` item, so a job that ran out of attempts cannot strand one.

Rejected alternatives:
- A separate journal table beside the history. It means two writes per step and two truths to reconcile.
- Sidecar files in the source, as in v0.2. They write files the owner never asked for, and need their own crash handling.

### D5. Errors per step, and where an action stops (R3.1)

| Primitive's answer | Item ends | Action |
|---|---|---|
| `EEXIST` (rename, mkdir) | `conflict` | goes on |
| `EINVAL` on rename | `no_safe_rename` (D2) | stops |
| `EXDEV` | refused `other_filesystem` | goes on |
| `ENOENT` before the step, or identity differs at preflight | `changed` | goes on |
| `EROFS` | `failed` | stops; the filesystem became read-only |
| `EACCES`, `EPERM` | `failed` with the OS text | goes on |
| `ENOTEMPTY` on rmdir | `not_empty` | goes on |
| any other error (`EIO`, …) | confirmed as after a crash: `failed` when clearly not done, `done` when clearly done, otherwise `manual_recovery` | stops on `manual_recovery` |

`not_permitted` and `offline` at intent stop the action. Stopped actions mark their remaining items `not_attempted`.

Rejected: retrying an ambiguous step. I3 forbids blind retries.

### D6. The index moves with the disk, in the outcome's transaction (R3.5; §6.9 Durability; I4)

For a done `rename`, in one transaction:
1. **Missing rows at the destination.** Any `missing` row at the new path, and its subtree, is deleted, as a scan deletes a row whose kind changed at the same path. A missing row cannot come back at a path something else now holds.
2. **The entry and its subtree.** The moved row's `parent_id`, `name`, and `path` change, and every descendant's path is rewritten by range: `path = new || substr(path, len(old)+1)` over `[old+'/', old+'0')`. IDs never change, so `entry_tags`, `entry_overrides`, `file_content`, `archives`, relations, and selections follow by themselves.
3. **The name index.** The `entry_names` row changes when the name changes.
4. **Facts.** The post-step lstat facts of the moved entry and of both parent folders (mtime, ctime, dev, ino) are written. The rename's new ctime then does not make the next scan drop the digest (`writer.dropContent`). `file_content` and `archives` take the new ctime too.
5. **Decisions.** `decisions.Reinherit` recomputes the moved subtree's effective decisions under its new ancestors: the top from its new parent unless it has its own, then the existing `propagate` with its cut points. Effective tags need nothing, because they are walked through `parent_id` and filtered by path.
6. **Folds.** `index.Refolder.Refold` recomputes the moved or renamed entry's own classification. Its name feeds the rules. It also recomputes the sibling-dependent file rules of both parents' files (`.bin`/`.cue`). Then it recomputes the totals, `dir_stats`, and folder classification of both ancestor chains, bottom-up from stored children, with owner overrides applied. It uses the same fold code a scan uses, refactored to take stored rows, and writes only rows that differ.

A done `mkdir` inserts the folder row, empty, inheriting its parent's effective decision, then refolds. A done `rmdir` deletes the row (with any missing children), then refolds.

When an action ends, `relations.RequestRefresh` marks relations and review rows dirty, once per action, not per item.

The property this buys is that a full rescan after any sequence of moves adds no entry, marks none missing, and changes no folder's totals or `dir_stats`. A property test checks it (R3.5).

Rejected alternatives:
- **A rescan after each action.** Minutes per action on the owner's 1.5-million-entry source, and moves must wait while it runs (D10). Interactive organizing would stall.
- **Incremental arithmetic on totals only.** `dir_stats`, composition, and folder classification would be wrong until the next scan, which breaks I7 ("never fabricate sizes").
- **Matching moved entries by inode in the scanner.** Moves outside Precious are not R3's subject, and R1 D6 rejected identity matching.

### D7. Moves stay inside one source (§10.5 Destinations)

The destination folder must be on the moved entry's source, and on the same filesystem: equal `dev` for the entry, its parent, and the destination. A nested mount is never crossed.

Rejected: moves between two sources on one filesystem. They need `source_id` rewrites across `file_content.source_id`, coverage, review row sources, and two write permissions, for a case the spec does not ask for.

### D8. Plans read the index, never the disk (§10.5 Execution; R3.4)

`plan-*` commands compute items and conflicts from the index only:
- the present rows in the destination;
- names taken by other items of the same plan;
- names compared under the source's capabilities. When `case_sensitive` is false, names compare after Unicode simple case folding.

A plan does no disk I/O inside a command, and shows what the owner sees.

A name taken on disk but not yet indexed shows up at run time as `conflict` (D5). The run still moves only planned items, so R3.4 holds.

Rejected: listing destination folders on disk while planning. That puts slow, unbounded I/O inside a command, and the run must re-check anyway.

### D9. Single actions run at once; bulk ones after a preview (§10.5 Execution)

**Single actions.**
- `plan-rename` and `plan-create-folder` answer `409 name_taken` when the name is taken, and create no action.
- For a one-item move without a conflict (detail panel), the UI calls `run-action` right after `plan-move`, with no dialog.

**Bulk and conflicted actions.** Every bulk move, and every plan with a conflict, refused item, or `kept_lost > 0`, opens the preview dialog. It lists every item, paged with "Load more" over `GET /api/history/{id}/items`. A planned action expires after 1 hour, and holds at most 10,000 items.

Rejected alternatives:
- A synchronous HTTP move. A command's `Apply` runs inside the write transaction, and the filesystem step must not.
- No cap. 1 million single-entry steps in one action is a cleanup, not organizing.

### D10. Organize jobs and scans of a source exclude each other (R3.5)

Kind `organize` is registered with `ClassInteractive`. It is bound to its source, with `ScopeKey = "organize:<source>"`, so `EnqueueOnce` coalesces. One job serves the source's queued actions in order, oldest first. Being interactive, it gets the device at the next yield of a hashing job.

The exclusion:
- **Organize.** The job defers (`jobs.Defer`, 1 s) while a `scan` job of its source is `running`. It checks inside its first intent transaction, and again before each item.
- **Scan.** The scan handler defers (3 s) at start while an `organize` job of its source is `running`.

The unequal delays break the tie when both start together with `workers_per_device > 1`. A paused scan does not block moves, because a resumed scan walks again from the root, reading stored children folder by folder.

Hashing needs nothing. A moved file's commit fails the existing identity re-check, because the ctime changed (D6), so the result is dropped (I9).

Rejected: refusing `run-action` while a scan is active (`409 job_active`). Scheduled rescans would then turn organizing into a guessing game. Waiting is honest.

### D11. Undo is a planned action of the reverse steps (R3.3)

`plan-undo` reverses an action's `done` items, in reverse `seq` order:
- **Renames.** Each goes from the entry's current parent and name to the original `from_parent` and `from_name`.
- **Folders.** Each `mkdir` the action recorded as `created` becomes an `rmdir`.

An item is a `conflict` when:
- its old name is taken (`name_taken`);
- its old parent is gone or no longer a present folder (`previous_folder_gone`).

With `destination_id`, those items target that folder under their old names. `undone_by` is set when the undo is run, not when it is planned, so an expired undo leaves the action undoable. An undo is an action, so it can be undone too (redo).

Undo works per action. A bulk move is one action and is undone as a whole.

Rejected:
- Per-item undo inside a bulk action. More UI for a case the history makes rare.
- Undo of an action whose entries were moved again since: it is allowed, from wherever they are now. Refusing it would strand entries.

### D12. Rescue moves the outermost own keeps, flat (§6.8)

`plan-rescue` moves, into the destination folder, the entries inside a folder that have their own `keep`, leaving out any whose ancestor inside that folder has one. They keep their names, flat, and conflicts show in the preview. The destination must lie outside the folder, otherwise `400 invalid_request`. A folder whose effective decision is `keep` answers `409 invalid_entry_state`.

Rejected: rebuilding each entry's relative path under the destination. It creates deep, empty-looking chains such as `C/Meus documentos/` around one rescued spreadsheet.

### D13. Merge moves Compare's only-on-one-side files to their same relative paths (§10.5 Folder copies; §11.6; R3.6)

`plan-merge {left_id, right_id, from}` reads every item of Compare's `only_left` (or `only_right`) group, the counts to act on (ADR 0010):
- **Files.** Each file goes to the same path inside the other side.
- **Missing folders.** One `mkdir` item is planned for each folder missing on the way, parents first. A file whose parent is planned uses `to_dir_seq` instead of `to_parent`.
- **Refusals.** Archive members are refused (`inside_archive`). Either side being an archive refuses the plan with `400 invalid_request`. A non-folder entry where a folder is needed makes the items below it a `conflict`.

Files in `different` and `unchecked` are never moved.

Rejected: moving by content-based relation counts. Compare is what the owner sees, and archive members cannot move.

### D14. Decisions after the move are previewed, never changed by the move (§6.7; I4, I5)

A move never writes `decision`. For each item whose top entry has no own decision, the plan records `decision_after` = the destination's effective decision when it differs from the entry's current one. The action carries `kept_lost`, the number of items going from `keep` to another value. The preview shows "N kept items would no longer be kept". The run does not recheck this. The real inheritance at done time is D6.5's.

Rejected: giving a moved entry an own `keep` automatically. A job would then write an owner decision, which I4 forbids.

### D15. `remove-source` waits for organizing

`remove-source` fails with:
- `409 job_active` while an action of the source is `queued` or `running`;
- `409 recovery_needed` while one of its items is `intent` or `manual_recovery`.

Its cascade would otherwise delete the journal of a step in flight.

Rejected: cascading anyway. That loses the only record of a half-done rename.

### D16. Operator surfaces and vocabulary

| Surface | What it gets |
|---|---|
| Main navigation | History |
| Detail panel | an Organize section: Rename, Move to…, New folder, Rescue kept items |
| Search bulk | Move to… |
| Compare | a move action on the two only-on-one-side groups |
| Sources | a Changes by Precious row |

The catalog avoids "journal", "executor", and the banned terms. Items read "Moved", "Name taken", "Changed on disk since the last scan", "Needs your check", and so on.

The destination chooser browses the index with `GET /api/entries/{id}/children?kind=directory`, from the source's top folder. It offers New folder.

## Interfaces

### Import direction

```
organize ──▶ executor ──▶ index, decisions, sources, fsaccess, store, jobs
   │             ▲
   └─ commands, relations (Compare), search (selections)
cmd/precious wires: organize.New(..., executor.New(...)); index scan gets executor.OrganizeActive
```
`index` and `decisions` never import `executor` or `organize`.

### Go signatures (slices only add; foundation adds the shared ones)

```go
// internal/fsaccess (foundation)
type Capabilities struct { /* existing fields */ NoReplaceRename bool `json:"no_replace_rename"` }
// Writer is the write surface of a Dir. Only internal/executor calls it (I2).
type Writer interface {
	RenameNoReplace(name []byte, to Dir, newName []byte) error
	Mkdir(name []byte) error
	Rmdir(name []byte) error
}
func AsWriter(d Dir) (Writer, bool)
var (ErrExist, ErrNoReplaceUnsupported, ErrCrossDevice, ErrNotEmpty, ErrReadOnly, ErrPermission error)
// absent/unreadable/unavailable keep coming back as *Error with an AccessOutcome.

// internal/fsaccess/instrument (foundation): OpRename, OpMkdir, OpRmdir; InjectError and SetBeforeCall cover them.
// internal/fsaccess/synthfs (foundation): its Dir implements Writer with the same errors.

// internal/config (foundation)
type Sources struct { AllowedRoots []string `toml:"allowed_roots"`; AllowWrites bool `toml:"allow_writes"` } // default true

// internal/domain (foundation)
const (CodeWritesUnavailable, CodeWritesDisabled, CodeNameTaken, CodeActionExpired,
	CodeActionNotRunnable, CodeActionNotUndoable, CodeRecoveryNeeded Code = "writes_unavailable", "writes_disabled",
	"name_taken", "action_expired", "action_not_runnable", "action_not_undoable", "recovery_needed") // all 409

// internal/sources (foundation)
// Source gains WriteEnabled bool.
func WritesUnavailable(src Source, allowWrites bool) string // "", "forbidden_by_config", "read_only", "no_replace_rename"
// CheckWrites reads the source inside q and returns nil, or a *domain.Error:
// unknown_source, source_offline, writes_unavailable, writes_disabled.
func CheckWrites(ctx context.Context, q store.Queryer, id domain.SourceID, allowWrites bool) error

// internal/index (slice 2.1)
type PostFacts struct { Dev, Ino uint64; MtimeNs, CtimeNs int64 }
type Move struct {
	Source domain.SourceID; Entry, NewParent domain.EntryID; NewName []byte
	Facts PostFacts; OldParentFacts, NewParentFacts PostFacts
}
func MoveEntry(ctx context.Context, tx *sql.Tx, m Move) (old, new []byte, err error) // D6 steps 1–4
type NewFolder struct { Source domain.SourceID; Parent domain.EntryID; Name []byte; Facts PostFacts; ParentFacts PostFacts }
func InsertFolder(ctx context.Context, tx *sql.Tx, f NewFolder) (domain.EntryID, error)
func RemoveFolder(ctx context.Context, tx *sql.Tx, id domain.EntryID, parentFacts PostFacts) error
func NewRefolder(pol *rules.Policy) *Refolder
// Refold re-derives touched entries and both of their ancestor chains (D6 step 6).
func (r *Refolder) Refold(ctx context.Context, tx *sql.Tx, src domain.SourceID, touched []domain.EntryID) error
const DeferOrganizing = "organizing"
// HandlerOptions or a setter gives the scan handler: func(ctx, q store.Queryer, src domain.SourceID) (bool, error)
func (h *Handler) DeferWhile(active func(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error))

// internal/decisions (slice 2.1)
func Reinherit(ctx context.Context, tx *sql.Tx, id domain.EntryID) error

// internal/executor (slice 2.2)
const KindOrganize jobs.Kind = "organize"
type Index interface { // implemented by organize from index + decisions + Refolder
	ApplyRename(ctx context.Context, tx *sql.Tx, m index.Move) error
	ApplyMkdir(ctx context.Context, tx *sql.Tx, f index.NewFolder) (domain.EntryID, error)
	ApplyRmdir(ctx context.Context, tx *sql.Tx, src domain.SourceID, id domain.EntryID, parentFacts index.PostFacts) error
	ActionDone(ctx context.Context, tx *jobs.Tx, src domain.SourceID) error // RequestRefresh
}
type Options struct { Store *store.Store; Sources *sources.Service; Index Index; AllowWrites bool; Clock clock.Clock; Logger *slog.Logger; Hooks Hooks }
type Hooks struct { BeforeStep, AfterStep func(itemID int64) error } // tests only: an error simulates a crash at that point
func New(o Options) *Executor
func (e *Executor) Register(r *jobs.Runner)
func (e *Executor) Startup(ctx context.Context, r *jobs.Runner) error
// OrganizeActive reports whether an organize job of src is running (for index.Handler.DeferWhile).
func OrganizeActive(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error)
func Enqueue(tx *jobs.Tx, src domain.SourceID) (jobs.Record, bool, error) // EnqueueOnce with the organize scope

// internal/organize (slice 3.1)
func New(o Options) *Service // Store, Runner, Sources, Policy, Executor, AllowWrites, Clock, Logger
func (s *Service) RegisterCommands(h *commands.Handler)
func (s *Service) Routes(mux *http.ServeMux)
func (s *Service) Index() executor.Index
```

### Tables (migration 0006; foundation)

```sql
ALTER TABLE sources ADD COLUMN write_enabled INTEGER NOT NULL DEFAULT 0 CHECK (write_enabled IN (0,1));
CREATE TABLE actions (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('move','rename','create_folder','rescue','merge','undo')),
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK (state IN ('planned','queued','running','done','stopped','expired')),
  destination_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  undo_of INTEGER REFERENCES actions(id) ON DELETE SET NULL,
  undone_by INTEGER REFERENCES actions(id) ON DELETE SET NULL,
  kept_lost INTEGER NOT NULL DEFAULT 0,
  job_id INTEGER,
  created_at INTEGER NOT NULL, expires_at INTEGER, started_at INTEGER, finished_at INTEGER);
CREATE INDEX actions_by_source ON actions(source_id, state, id);
CREATE TABLE action_items (
  id INTEGER PRIMARY KEY,
  action_id INTEGER NOT NULL REFERENCES actions(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  op TEXT NOT NULL CHECK (op IN ('rename','mkdir','rmdir')),
  entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,  -- rename: the entry; mkdir: the folder once done; rmdir: the folder
  from_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, from_name BLOB, from_path BLOB,
  to_parent INTEGER REFERENCES entries(id) ON DELETE SET NULL, to_dir_seq INTEGER, to_name BLOB, to_path BLOB,
  bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0,
  kind TEXT, dev INTEGER, ino INTEGER, size INTEGER, mtime_ns INTEGER,   -- identity expected, set at intent
  decision_after TEXT CHECK (decision_after IN ('undecided','keep','discard','later')),
  created INTEGER NOT NULL DEFAULT 0 CHECK (created IN (0,1)),          -- mkdir made the folder
  state TEXT NOT NULL CHECK (state IN ('planned','refused','conflict','intent','done','not_permitted','offline',
    'changed','failed','no_safe_rename','not_empty','manual_recovery','not_attempted','resolved')),
  reason TEXT,   -- refused/conflict reason, see the item JSON
  detail TEXT,   -- failed: the OS error text; manual_recovery: {"from":"absent|same|other","to":"absent|same|other"}
  finished_at INTEGER,
  UNIQUE (action_id, seq));
CREATE INDEX action_items_open ON action_items(state, action_id) WHERE state IN ('intent','manual_recovery');
```
Writers:
- `organize` inserts `planned`/`refused`/`conflict` items.
- `run-action` and `resolve-recovery` (organize) move an action to `queued` and an item from `manual_recovery` to `resolved`.
- `executor` owns every other transition.

### Commands (POST `/api/commands/{name}`; strict JSON; IDs are strings)

| Command | Body | Success | Errors |
|---|---|---|---|
| `set-source-writes` | `{source_id, enabled}` | 200 `{source}` | 404 `unknown_source`; 409 `writes_unavailable` (enable only) |
| `plan-move` | exactly one of `entry_id` (individual), `entry_ids` (1–1,000), `selection_id` (bulk); and `destination_id` | 201 `{action, items, next_cursor}` | 400 `invalid_request` (shape, >10,000 items, destination not a present folder, member destination); 404 `not_found`; 409 `selection_expired`, `source_offline`, `writes_disabled`, `recovery_needed` |
| `plan-rename` | `{entry_id, name}` | 201 same | 400 `invalid_request` (empty, `/`, NUL, `.`, `..`, >255 bytes, not UTF-8, unchanged, member, source root); 404; 409 `name_taken`, `source_offline`, `writes_disabled`, `recovery_needed` |
| `plan-create-folder` | `{parent_id, name}` | 201 same | as `plan-rename` |
| `plan-rescue` | `{folder_id, destination_id}` | 201 same | 400 (destination inside the folder); 404; 409 `invalid_entry_state` (folder kept, or holds no own keep), the 409s above |
| `plan-merge` | `{left_id, right_id, from: "left"\|"right"}` | 201 same | 400 (a side is an archive or member, sides on two sources); 404; the 409s above |
| `plan-undo` | `{action_id, destination_id?}` | 201 same | 404; 409 `action_not_undoable`, the 409s above |
| `run-action` | `{action_id}` | 202 `{action, job_id, state, coalesced}` | 404; 409 `action_expired`, `action_not_runnable` (not planned, or nothing runnable), `action_not_undoable` (undo of an action undone since), `source_offline`, `writes_disabled`, `recovery_needed` |
| `resolve-recovery` | `{item_id}` | 200 `{action, scan: {job_id, coalesced}}` | 404; 409 `invalid_entry_state` (item not in `manual_recovery`) |

`plan-move` treats an individual and a bulk request alike, because keeps do not restrict moves (§10.5). Only the target form differs.

### Read API

- `GET /api/history?source=&cursor=&limit=` → `{items: Action[], next_cursor}`. Newest first, actions that were run only, 50 by default and at most 200.
- `GET /api/history/{id}` → `Action`, including planned and expired ones.
- `GET /api/history/{id}/items?state=&cursor=&limit=` → `{items: Item[], next_cursor}`. In `seq` order, 200 by default and at most 1,000. `state` repeats.
- `GET /api/entries/{id}/children?kind=directory` → folders only (archives left out).

```json
Action {"id":"12","kind":"move","source_id":"corpus","state":"done",
  "created_at":"…","expires_at":"…"|null,"started_at":"…"|null,"finished_at":"…"|null,
  "destination":EntryRow|null,"job_id":"7"|null,"undo_of":"…"|null,"undone_by":"…"|null,
  "counts":{"planned":0,"refused":0,"conflict":0,"done":3,"failed":0,"changed":0,"not_permitted":0,"offline":0,
            "no_safe_rename":0,"not_empty":0,"not_attempted":0,"manual_recovery":0,"resolved":0,"intent":0},
  "bytes":123,"files":3,"kept_lost":0,
  "undo":{"possible":true,"reason":null|"not_done"|"already_undone"|"nothing_done"}}
Item {"id":"77","seq":1,"op":"rename","entry":EntryRow|null,
  "from":{"path":"…","path_b64":"…"}|null,"to":{"path":"…","path_b64":"…"}|null,
  "state":"done","reason":null|"other_source"|"inside_archive"|"missing"|"source_root"|"into_itself"|"already_there"
    |"other_filesystem"|"name_taken"|"name_taken_in_plan"|"previous_folder_gone",
  "decision_after":null|"discard","detail":null|"…","found":null|{"from":"absent|same|other","to":"absent|same|other"},
  "bytes":123,"files":1}
Source += "writes":{"enabled":false,"unavailable":null|"forbidden_by_config"|"read_only"|"no_replace_rename"}
Capabilities += "no_replace_rename":true
Job event kind "organize": progress {"items":n,"done":n}
```

## Concurrency

- **`plan-*` (`Apply`, in the write transaction).**
  - Re-checks: `sources.CheckWrites`, no `manual_recovery` item on the source, targets and destination present, and the selection not expired.
  - A commit just before (a decision, a scan batch, another action's outcome) is read as it is. One just after leaves the plan stale; the run's checks catch it per item (`changed`, `conflict`), and `decision_after` stays a preview.
- **`run-action`.**
  - Re-checks: expiry, `state='planned'`, `CheckWrites`, recovery, for an undo that its target's `undone_by` is null; then sets `queued` (and the target's `undone_by`) and runs `executor.Enqueue`.
  - Writes turned off just after this commit are caught by the next intent transaction.
- **Organize job, per item.** The intent transaction re-checks:
  - `CheckWrites` (permission, config, online, capabilities) and no running scan of the source (else `Defer`);
  - the action not cancelled, the item still `planned`, the entry present at the planned `from_parent`/`from_name`;
  - the destination folder present, or its `to_dir_seq` item done;
  - no `manual_recovery` on the source.

  A command committing just before the intent is seen; one just after cannot stop the step, which the owner already confirmed. The outcome transaction re-checks that the item is still `intent`, since reconcile is the only other writer and runs only at attempt start. It then applies D6.
  - **Decisions.** A decision committed between intent and outcome is read by `Reinherit`.
  - **Hashing.** A hash commit after the outcome fails the identity re-check, because the ctime changed.
- **Reconcile.** It runs at attempt start and from `Startup`, while no item of the source can be mid-step (one organize job per source, by scope). It checks the disk, then in a transaction re-checks `state='intent'` before changing it.
- **Scan start.** It checks `executor.OrganizeActive` in a read. A scan claimed just after the organize job's check defers at its own start. The two delays settle a tie (D10).
- **`set-source-writes`.** It writes the column. The executor reads it in each intent transaction, which is the guarantee's boundary (R3.7).
- **`resolve-recovery`.** It moves the item from `manual_recovery` to `resolved` and starts a scan of the source in the same transaction. The scan reconciles the index with the disk; anything unknown becomes new entries.
- **`cancel-job` on an organize job.** The executor checks its context between items. The step in flight finishes and is confirmed, then the remaining items end `not_attempted` and the action `stopped`.
- **`remove-source`.** It re-checks for active actions and open items in its transaction (D15).

## Risks / Trade-offs

- [Refold must equal a scan's fold exactly] → One fold function serves both. The property test compares Refold with a fresh rescan after random moves on the corpus.
- [A huge folder move holds the single writer] → One transaction rewrites the subtree's paths. A slow-tagged test bounds 100,000 descendants at 10 s on the development machine, and the docs say large moves pause other writes briefly.
- [The owner's server keeps sources read-only at the OS level] → Writes report `read_only` until the service allows them. The docs show the change.
- [Old ZFS lacks the no-replace flag] → `EINVAL` is a clean refusal (D2). The owner sees why writes went off.
- [Files moved by hand while an action runs] → Preflight identity checks give `changed`, and only `manual_recovery` stops the action.

## Migration Plan

- **Upgrade.** Migration 0006 adds a column with a default and two new tables. No backfill is needed, and every source starts with writes off.
- **Rollback.** The r2d binary refuses a newer schema (`ErrSchemaTooNew`), so roll back by restoring the backup taken before deploying.
