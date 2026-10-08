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
- `Mkdir`: `mkdirat(fd, name, 0700)`, then `fchmodat` to the parent folder's permission bits (`mode & 02777`). The new folder is then as reachable as its parent, whatever the service's umask (the shipped unit has `UMask=0077`). A setgid parent passes on its group by itself.
- `Rmdir`: `unlinkat(fd, name, AT_REMOVEDIR)`, which only removes an empty folder;
- `Sync`: `fsync` of the folder's descriptor.

Each call goes through the raw descriptors of `Dir` handles opened by the existing rooted, identity-checked descent from the source root, never by path. `Rmdir` serves only undo of a folder the same action created and recorded as `created`.

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
2. **Intent.** The executor re-checks the item and commits `intent`. The record holds the resolved old and new parent IDs, names, and paths, and the identity it expects (kind, size, mtime, and dev and ino when the source has `stable_identity`).
3. **Step.** It calls the primitive.
4. **Sync.** It fsyncs every folder the step changed: both parents of a rename, the parent of a `mkdir` or an `rmdir`. A step is not durable until its folders are, and the database commit that follows is fsynced at once on another filesystem. A sync error leaves the item `intent` and stops the action, so reconciliation decides it.
5. **Confirm.** It lstats both names through the same handles.
6. **Outcome.** In one transaction, it records the outcome and updates the index (D6). When the index update fails, that transaction rolls back, and a second one marks the item `manual_recovery`. An item never stays `intent` because of an index error.

`store` already opens the writer with `synchronous=FULL`, so an intent that committed survives a power cut.

**Reconciling** an `intent` item looks at both names:

| Old name | New name | Result |
|---|---|---|
| holds the expected identity | free | back to `planned`; it runs once |
| free | holds the expected identity | `done`, with the index update; no second step |
| anything else | anything else | `manual_recovery`, with the findings |

Identity compares as the scan's `unchanged` and `content.Matches` do: dev and ino only when the source has `stable_identity`, and times within the capability's resolution (vfat and exfat have no stable identity). For `mkdir`, an empty folder at the new name that is absent from the index counts as done. For `rmdir`, a free name counts as done.

Reconciliation runs only inside an organize job, after that job's no-running-scan check (D10), and before it runs any item. `executor.Startup` only enqueues: one reconcile job for each source with an `intent` item and no active organize job. It reads nothing on disk, so it never races an attempt that `recoverOrphans` requeued. A scan defers while its source has an `intent` item (D10), so no scan can index a half-recorded step.

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
1. **Missing rows at the destination.** A `missing` row at the new path is deleted, with its subtree and its `entry_names` rows, only when no row in that subtree carries owner intent (an own decision, a tag, or an override). Otherwise, the plan and the intent re-check make the item a `conflict` with reason `name_taken_by_missing`, so a kept, tagged file that went missing never loses its intent to a move (I4). A missing row cannot come back at a path something else now holds.
2. **The entry and its subtree.** The moved row's `parent_id`, `name`, and `path` change, and every descendant's path is rewritten by range: `path = new || substr(path, len(old)+1)` over `[old+'/', old+'0')`. IDs never change, so `entry_tags`, `entry_overrides`, `file_content`, `archives`, relations, and selections follow by themselves.
3. **Paths stored in JSON.** The `dir_stats.indicators` and `dir_stats.inside` lists of the moved folder and of every folder below it embed paths. They get the same prefix rewrite in their `path` and `path_b64` fields. Their ancestors' lists are re-derived by the refold (step 7).
4. **The name index.** The `entry_names` row changes when the name changes.
5. **Facts.** The post-step lstat facts of the moved entry and of both parent folders (mtime, ctime, dev, ino) are written. The rename's new ctime then does not make the next scan drop the digest (`writer.dropContent`). `file_content` and `archives` take the new ctime too.
6. **Decisions.** `decisions.Reinherit` recomputes the moved subtree's effective decisions under its new ancestors: the top from its new parent unless it has its own, then the existing `propagate` with its cut points. Effective tags need nothing, because they are walked through `parent_id` and filtered by path.
7. **Folds.** `index.Refolder.Refold` recomputes the moved or renamed entry's own classification. Its name feeds the rules. It also recomputes the sibling-dependent file rules of both parents' files (`.bin`/`.cue`). Then it recomputes the totals, `dir_stats`, and folder classification of both ancestor chains, bottom-up from stored children, with owner overrides applied. It uses the same fold code a scan uses, refactored to take stored rows, and writes only rows that differ.

The scan builds a folder's indicator list (capped at 20) in directory order, which a fold from stored rows cannot reproduce. So both the scan and the refold order indicators by path. The first scan after upgrading rewrites those lists once.

A done `mkdir` inserts the folder row, empty, with its `entry_names` row, inheriting its parent's effective decision, then refolds. A done `rmdir` deletes the row, its `entry_names` row, and any missing children without owner intent, then refolds. When a missing child carries owner intent, the `rmdir` is planned `not_empty` instead.

When an action ends, `relations.RequestRefresh` marks relations and review rows dirty, once per action, not per item.

The property this buys is that a full rescan after any sequence of moves adds no entry, marks none missing, and changes no folder's totals or `dir_stats`. A property test checks it (R3.5).

Rejected alternatives:
- **A rescan after each action.** Minutes per action on the owner's 1.5-million-entry source, and moves must wait while it runs (D10). Interactive organizing would stall.
- **Incremental arithmetic on totals only.** `dir_stats`, composition, and folder classification would be wrong until the next scan, which breaks I7 ("never fabricate sizes").
- **Matching moved entries by inode in the scanner.** Moves outside Precious are not R3's subject, and R1 D6 rejected identity matching.

### D7. Moves stay inside one source (§10.5 Destinations)

The destination folder must be on the moved entry's source, and on the same filesystem. The executor compares devices from the live handles: the device of the entry's lstat, of its parent's handle, and of the destination's handle. It never compares stored rows, since a USB disk's `st_dev` can change between mounts. A nested mount is never crossed:
- an entry that is itself a mount point is refused as `other_filesystem`;
- a folder whose subtree holds a mount point (`dir_stats.mount_boundaries > 0`) is refused as `contains_mount`.

Both are checked in the plan and again at intent. `renameat2` would carry a nested mount along in this namespace only, and the next mount would recreate the old path.

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

**Cancelling.** `cancel-action` stops an action in its own transaction:
- a `queued` one becomes `stopped`, its `planned` items become `not_attempted`, and its job is cancelled;
- a `running` one has its job cancelled, and the executor stops after the step in flight.

A generic `cancel-job` on an organize job has the same effect, because a job serves exactly one action (D10).

Rejected alternatives:
- A synchronous HTTP move. A command's `Apply` runs inside the write transaction, and the filesystem step must not.
- No cap. 1 million single-entry steps in one action is a cleanup, not organizing.

### D10. Organize jobs and scans of a source exclude each other (R3.5)

Kind `organize` is registered with `ClassInteractive`. `run-action` enqueues one job per action, bound to the source, with `ScopeKey = "organize:<action_id>"` and payload `{action_id}`. A run never coalesces into another action's job, so a confirmed action cannot lose its wakeup. Being interactive, the job gets the device at the next yield of a hashing job.

Actions of one source run one at a time, oldest first:
- **Waiting its turn.** A job defers (1 s) while an older action of its source is `queued` or `running`.
- **Sweeping.** At the start of every organize attempt, and in `Startup`, an action that is `queued` or `running` with a terminal job becomes `stopped`. Its `planned` items become `not_attempted`, and its `intent` items stay for reconciliation. This covers a job cancelled while queued, which never runs its handler.

The exclusion with scans:
- **Organize.** The job defers (1 s) while a `scan` job of its source is `running`. It checks before reconciling, and again inside each intent transaction.
- **Scan.** The scan handler defers (3 s) at start while `executor.OrganizeActive` is true: an organize job of the source is queued or running, or the source has an `intent` item.

The unequal delays break the tie when both start together with `workers_per_device > 1`. The organize job only waits for a running scan, never a queued one, so the two cannot wait for each other. A paused scan does not block moves, because a resumed scan walks again from the root, reading stored children folder by folder.

Hashing and archive listing need the path guard of D18.

Rejected: refusing `run-action` while a scan is active (`409 job_active`). Scheduled rescans would then turn organizing into a guessing game. Waiting is honest.

### D11. Undo is a planned action of the reverse steps (R3.3)

`plan-undo` reverses an action's `done` items that no undo has reversed yet, in reverse `seq` order:
- **Renames.** Each goes from the entry's current parent and name to the original `from_parent` and `from_name`.
- **Folders.** Each `mkdir` the action recorded as `created` becomes an `rmdir`.

Each undo item records the item it `reverses`. When it ends `done`, the outcome transaction sets the original's `reversed_by`. An action is undone once all its reversible items are reversed. Until then, it stays undoable for the items left, for example after an undo that stopped on a conflict, a write turned off, or a cancel. An undo item whose original was reversed meanwhile, by a second undo plan, ends `changed` with reason `already_undone`.

An item is a `conflict` when:
- its old name is taken (`name_taken`);
- its old parent is gone or no longer a present folder (`previous_folder_gone`).

With `destination_id`, those items target that folder under their old names. An undo is an action, so it can be undone too (redo).

Undo works per action. A bulk move is one action and is undone as a whole.

Rejected:
- Per-item undo inside a bulk action. More UI for a case the history makes rare.
- Marking the original undone when its undo is queued. An undo that stops early would then strand the items it never reversed.
- Refusing to undo an action whose entries were moved again since. It is allowed, from wherever they are now; refusing would strand entries.

### D12. Rescue moves the outermost own keeps, flat (§6.8)

`plan-rescue` moves, into the destination folder, the entries inside a folder that have their own `keep`, leaving out any whose ancestor inside that folder has one. They keep their names, flat, and conflicts show in the preview. The destination must lie outside the folder, otherwise `400 invalid_request`. A folder whose effective decision is `keep` answers `409 invalid_entry_state`.

Rejected: rebuilding each entry's relative path under the destination. It creates deep, empty-looking chains such as `C/Meus documentos/` around one rescued spreadsheet.

### D13. Merge moves Compare's only-on-one-side files to their same relative paths (§10.5 Folder copies; §11.6; R3.6)

`plan-merge {left_id, right_id, from}` reads every item of Compare's `only_left` (or `only_right`) group, the counts to act on (ADR 0010):
- **Files.** Each file goes to the same relative path inside the other side. Compare drops a single wrapper folder from one side (`relations/compare.go`), so `relations` gains `Wrappers`, which returns each side's dropped prefix. The plan joins the destination root, its wrapper, and the item's relative path. That way `Fotos (copia)/Fotos/2007/x.jpg` merges into `Fotos/2007/` and not one level too high.
- **Missing folders.** One `mkdir` item is planned for each folder missing on the way, parents first. A file whose parent is planned uses `to_dir_seq` instead of `to_parent`.
- **Refusals.** Archive members are refused (`inside_archive`). Either side being an archive refuses the plan with `400 invalid_request`. A non-folder entry where a folder is needed makes the items below it a `conflict`.

Files in `different` and `unchecked` are never moved.

Rejected: moving by content-based relation counts. Compare is what the owner sees, and archive members cannot move.

### D14. Decisions after the move are previewed, never changed by the move (§6.7; I4, I5)

A move never writes `decision`. For each item whose top entry has no own decision, the plan records `decision_after` = the destination's effective decision when it differs from the entry's current one.

A move that would take away an entry's effective `keep` follows §6.7, which says a bulk action never changes a keep:
- **Bulk actions** refuse each item that would go from `keep` to another value, with reason `would_lose_keep`: `plan-move` with `entry_ids` or a selection, `plan-merge`, and `plan-rescue`. Rescued entries have their own keep, so a rescue never hits it. The intent transaction re-derives the decision after the move, from the destination's current effective decision. A bulk item that would newly lose a keep ends `changed` with that reason.
- **Individual actions** (`entry_id`, `plan-rename`, and `plan-undo`, which restores the owner's own earlier state) are allowed. They carry `kept_lost`, the number of items going from `keep` to another value, and the preview warns: "N kept items would no longer be kept".

Rejected alternatives:
- Giving a moved entry an own `keep` automatically. A job would then write an owner decision, which I4 forbids.
- A warning alone for bulk moves. That contradicts §6.7.

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

### D17. A rename that changes only letter case is refused on a case-insensitive source

On vfat, exfat, and ntfs3, the new name `foto.jpg` resolves to the entry `FOTO.JPG` itself, so `renameat2` with `RENAME_NOREPLACE` answers `EEXIST`. `plan-rename` therefore refuses a new name that equals the old one after case folding on a source with `case_sensitive: false`. It answers `400 invalid_request`, with a message saying that only the letter case differs and that this disk cannot rename that in one step.

Rejected: two journaled steps through a free temporary name. Each step needs its own reconciliation, and a crash between them leaves a name the owner never chose, for a rare case.

### D18. Content results are dropped when the path they read moved (I9)

The hashing and archive-listing jobs keep batches of `entries.path` across their yields, and the organize job runs exactly at those yields. A file inside a moved folder keeps its size, times, dev, and ino, so the identity re-check in `content` `apply` and in the archive listing's `entryLive` passes. The stale path, meanwhile, gives `ENOENT`, which is mapped to "changed", and the job writes `changed` for a file that never changed.

Both guards therefore also compare the path the job read with the entry's current path. When they differ, the result is dropped, and the file stays as it was, to be read again.

Rejected: making organize wait for hashing to finish. Hashing a large source takes hours.

## Interfaces

### Import direction

```
organize ──▶ executor ──▶ index, decisions, sources, fsaccess, store, jobs
   │             ▲
   └─ commands, relations (Compare, Wrappers), search (selections)
cmd/precious wires: organize.New(..., executor.New(...)); index scan gets executor.OrganizeActive
content ──▶ (unchanged imports; D18 adds a path comparison to its guards)
```
`index` and `decisions` never import `executor` or `organize`.

### Go signatures (slices only add; foundation adds the shared ones)

```go
// internal/fsaccess (foundation)
type Capabilities struct { /* existing fields */ NoReplaceRename bool `json:"no_replace_rename"` }
// Writer is the write surface of a Dir. Only internal/executor calls it (I2).
type Writer interface {
	RenameNoReplace(name []byte, to Dir, newName []byte) error
	Mkdir(name []byte) error // mkdirat 0700, then the parent's mode & 02777 (D3)
	Rmdir(name []byte) error
	Sync() error             // fsync of this folder
}
func AsWriter(d Dir) (Writer, bool)
var (ErrExist, ErrNoReplaceUnsupported, ErrCrossDevice, ErrNotEmpty, ErrReadOnly, ErrPermission error)
// absent/unreadable/unavailable keep coming back as *Error with an AccessOutcome.

// internal/fsaccess/instrument (foundation): OpRename, OpMkdir, OpRmdir, OpSync; InjectError and SetBeforeCall cover them.
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

// internal/index (types PostFacts, Move, NewFolder land with group 1 in internal/index/move.go; functions are slice 2.1)
type PostFacts struct { Dev, Ino uint64; MtimeNs, CtimeNs int64 }
type Move struct {
	Source domain.SourceID; Entry, NewParent domain.EntryID; NewName []byte
	Facts PostFacts; OldParentFacts, NewParentFacts PostFacts
}
func MoveEntry(ctx context.Context, tx *sql.Tx, m Move) (old, new []byte, err error) // D6 steps 1–5
// MissingIntentAt reports whether a missing row at (src, path), or one below it, carries owner intent (D6 step 1).
func MissingIntentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error)
type NewFolder struct { Source domain.SourceID; Parent domain.EntryID; Name []byte; Facts PostFacts; ParentFacts PostFacts }
func InsertFolder(ctx context.Context, tx *sql.Tx, f NewFolder) (domain.EntryID, error)
func RemoveFolder(ctx context.Context, tx *sql.Tx, id domain.EntryID, parentFacts PostFacts) error
func NewRefolder(pol *rules.Policy) *Refolder
// Refold re-derives touched entries and both of their ancestor chains (D6 step 7).
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
	// MissingIntentAt is index.MissingIntentAt, so the executor builds before slice 2.1 lands.
	MissingIntentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error)
}
type Options struct { Store *store.Store; Sources *sources.Service; Index Index; AllowWrites bool; Clock clock.Clock; Logger *slog.Logger; Hooks Hooks }
type Hooks struct { BeforeStep, AfterStep func(itemID int64) error } // tests only: an error simulates a crash at that point
func New(o Options) *Executor
func (e *Executor) Register(r *jobs.Runner)
// Startup sweeps actions whose job is terminal (D10) and enqueues a reconcile job for each source with an
// intent item and no active organize job. It reads nothing on disk (D4).
func (e *Executor) Startup(ctx context.Context, r *jobs.Runner) error
// OrganizeActive reports whether an organize job of src is queued or running, or src has an intent item.
func OrganizeActive(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error)
// Enqueue starts the job of one action: scope "organize:<action_id>", payload {"action_id":"…"}.
func Enqueue(tx *jobs.Tx, src domain.SourceID, action int64) (jobs.Record, error)
// CancelAction stops a queued or running action in tx (D9 Cancelling).
func CancelAction(ctx context.Context, tx *jobs.Tx, action int64) error

// internal/relations (slice 3.1 adds)
// Wrappers returns the prefix Compare drops from each side, nil when none (D13).
func Wrappers(ctx context.Context, q store.Queryer, left, right domain.Ref) (leftWrap, rightWrap []byte, err error)

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
  bulk INTEGER NOT NULL CHECK (bulk IN (0,1)),                         -- D14
  destination_id INTEGER REFERENCES entries(id) ON DELETE SET NULL,
  undo_of INTEGER REFERENCES actions(id) ON DELETE SET NULL,
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
  reverses INTEGER REFERENCES action_items(id) ON DELETE SET NULL,      -- undo item: the item it reverses
  reversed_by INTEGER REFERENCES action_items(id) ON DELETE SET NULL,   -- set when that undo item is done
  state TEXT NOT NULL CHECK (state IN ('planned','refused','conflict','intent','done','not_permitted','offline',
    'changed','failed','no_safe_rename','not_empty','manual_recovery','not_attempted','resolved')),
  reason TEXT,   -- refused/conflict/changed reason, see the item JSON
  detail TEXT,   -- failed: the OS error text; manual_recovery: {"from":"absent|same|other","to":"absent|same|other"}
  finished_at INTEGER,
  UNIQUE (action_id, seq));
CREATE INDEX action_items_open ON action_items(state, action_id) WHERE state IN ('intent','manual_recovery');
CREATE INDEX action_items_entry ON action_items(entry_id) WHERE entry_id IS NOT NULL;
```
Writers:
- `organize` inserts `planned`/`refused`/`conflict` items.
- `run-action`, `cancel-action`, and `resolve-recovery` (organize) move an action to `queued` or `stopped`, and an item from `manual_recovery` to `resolved`.
- `executor` owns every other transition, including `reversed_by`.

### Commands (POST `/api/commands/{name}`; strict JSON; IDs are strings)

| Command | Body | Success | Errors |
|---|---|---|---|
| `set-source-writes` | `{source_id, enabled}` | 200 `{source}` | 404 `unknown_source`; 409 `writes_unavailable` (enable only) |
| `plan-move` | exactly one of `entry_id` (individual), `entry_ids` (1–1,000), `selection_id` (bulk); and `destination_id` | 201 `{action, items, next_cursor}` | 400 `invalid_request` (shape, >10,000 items, destination not a present folder, member destination); 404 `not_found`; 409 `selection_expired`, `source_offline`, `writes_disabled`, `recovery_needed` |
| `plan-rename` | `{entry_id, name}` | 201 same | 400 `invalid_request` (empty, `/`, NUL, `.`, `..`, >255 bytes, not UTF-8, unchanged, a case-only change on a case-insensitive source (D17), member, source root); 404; 409 `name_taken`, `source_offline`, `writes_disabled`, `recovery_needed` |
| `plan-create-folder` | `{parent_id, name}` | 201 same | as `plan-rename` |
| `plan-rescue` | `{folder_id, destination_id}` | 201 same | 400 (destination inside the folder); 404; 409 `invalid_entry_state` (folder kept, or holds no own keep), the 409s above |
| `plan-merge` | `{left_id, right_id, from: "left"\|"right"}` | 201 same | 400 (a side is an archive or member, sides on two sources); 404; the 409s above |
| `plan-undo` | `{action_id, destination_id?}` | 201 same | 404; 409 `action_not_undoable` (not done or stopped, or nothing left to reverse), the 409s above |
| `run-action` | `{action_id}` | 202 `{action, job_id, state}` | 404; 409 `action_expired`, `action_not_runnable` (not planned, or nothing runnable), `source_offline`, `writes_disabled`, `recovery_needed` |
| `cancel-action` | `{action_id}` | 200 `{action}` | 404; 409 `action_not_runnable` (not queued or running) |
| `resolve-recovery` | `{item_id}` | 200 `{action, scan: {job_id, coalesced}}` | 404; 409 `invalid_entry_state` (item not in `manual_recovery`) |

`plan-move` with `entry_id` is individual, and with `entry_ids` or `selection_id` it is bulk. Only bulk plans refuse items that would lose an effective keep (D14); otherwise both forms plan alike.

### Read API

- `GET /api/history?source=&cursor=&limit=` → `{items: Action[], next_cursor}`. Newest first, actions that were run only, 50 by default and at most 200.
- `GET /api/history/{id}` → `Action`, including planned and expired ones.
- `GET /api/history/{id}/items?state=&cursor=&limit=` → `{items: Item[], next_cursor}`. In `seq` order, 200 by default and at most 1,000. `state` repeats.
- `GET /api/entries/{id}/children?kind=directory` → folders only (archives left out).

```json
Action {"id":"12","kind":"move","source_id":"corpus","state":"done",
  "created_at":"…","expires_at":"…"|null,"started_at":"…"|null,"finished_at":"…"|null,
  "destination":EntryRow|null,"job_id":"7"|null,"undo_of":"…"|null,"bulk":false,
  "counts":{"planned":0,"refused":0,"conflict":0,"done":3,"failed":0,"changed":0,"not_permitted":0,"offline":0,
            "no_safe_rename":0,"not_empty":0,"not_attempted":0,"manual_recovery":0,"resolved":0,"intent":0},
  "bytes":123,"files":3,"kept_lost":0,"reversed":0,
  "undo":{"possible":true,"reason":null|"not_done"|"already_undone"|"nothing_done"}}
Item {"id":"77","seq":1,"op":"rename","entry":EntryRow|null,
  "from":{"path":"…","path_b64":"…"}|null,"to":{"path":"…","path_b64":"…"}|null,
  "state":"done","reason":null|"other_source"|"inside_archive"|"missing"|"source_root"|"into_itself"|"already_there"
    |"other_filesystem"|"contains_mount"|"name_taken"|"name_taken_in_plan"|"name_taken_by_missing"
    |"previous_folder_gone"|"would_lose_keep"|"already_undone",
  "decision_after":null|"discard","detail":null|"…","found":null|{"from":"absent|same|other","to":"absent|same|other"},
  "reversed":false,"bytes":123,"files":1}
Source += "writes":{"enabled":false,"unavailable":null|"forbidden_by_config"|"read_only"|"no_replace_rename"}
Capabilities += "no_replace_rename":true
Job event kind "organize": progress {"items":n,"done":n}
```

## Concurrency

- **`plan-*` (`Apply`, in the write transaction).**
  - Re-checks: `sources.CheckWrites`, no `manual_recovery` item on the source, targets and destination present, and the selection not expired.
  - A commit just before (a decision, a scan batch, another action's outcome) is read as it is. One just after leaves the plan stale; the run's checks catch it per item (`changed`, `conflict`), and `decision_after` stays a preview.
- **`run-action`.**
  - Re-checks: expiry, `state='planned'`, `CheckWrites`, and recovery. Then it sets `queued` and runs `executor.Enqueue`, a new job scoped to this action.
  - Nothing coalesces, so an organize job finishing at that moment cannot swallow the action (D10).
  - Writes turned off just after this commit are caught by the next intent transaction.
- **Organize job, at attempt start.** In order:
  1. Sweep actions with a terminal job (D10).
  2. Defer while a scan of the source is running, or while an older action of the source is queued or running.
  3. Reconcile the source's `intent` items.
  4. Run its own action's items.

  No other organize job of the source can be mid-step: older actions are done, newer ones wait their turn, and the scan is excluded. So reconcile, which checks the disk and then re-checks `state='intent'` in a transaction, cannot race a step.
- **Organize job, per item.** The intent transaction re-checks:
  - `CheckWrites` (permission, config, online, capabilities) and no running scan of the source (else `Defer`);
  - the action not cancelled, the item still `planned`, the entry present at the planned `from_parent`/`from_name`;
  - the destination folder present, or its `to_dir_seq` item done;
  - no missing row with owner intent at the destination (`name_taken_by_missing`);
  - for a bulk action, no lost keep (`would_lose_keep`);
  - for an undo item, that its original is not reversed yet (`already_undone`);
  - no `manual_recovery` on the source.

  A command committing just before the intent is seen. One committing just after cannot stop the step, which the owner already confirmed. The outcome transaction re-checks that the item is still `intent`, then applies D6, and sets `reversed_by` for an undo item. When D6 fails, the item goes to `manual_recovery` in a second transaction.
  - **Decisions.** A decision committed between intent and outcome is read by `Reinherit`.
  - **Hashing and archive listing.** A result read through a path that has since moved is dropped by the path guard (D18). A moved file's result also fails the ctime check.
- **Scan start.** It checks `executor.OrganizeActive` (an organize job queued or running, or an `intent` item) in a read. A scan claimed just after the organize job's check defers at its own start. The two delays settle a tie (D10).
- **`set-source-writes`.** It writes the column. The executor reads it in each intent transaction, which is the guarantee's boundary (R3.7).
- **`resolve-recovery`.** It moves the item from `manual_recovery` to `resolved` and starts a scan of the source in the same transaction. The scan reconciles the index with the disk; anything unknown becomes new entries.
- **`cancel-action` and `cancel-job`.** These are covered under D9's Cancelling. The executor checks its context between items, the step in flight finishes and is confirmed, and the sweep (D10) settles a job cancelled while it was queued.
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

## Addendum: decisions made during implementation

- **F1. More indexes in 0006.** Besides the three of Interfaces, 0006 indexes `actions.destination_id`, `actions.undo_of`, `action_items.from_parent`, `to_parent`, `reverses`, and `reversed_by` (partial, `IS NOT NULL`). The store's existing test requires every foreign key into `entries` to be searched by index, so a rescan dropping missing entries stays linear; the undo references keep removing a source's actions linear too. Tables and columns are exactly as in Interfaces.
- **F2. `ErrIntoItself`.** `renameat2` answers `EINVAL` both for a filesystem without `RENAME_NOREPLACE` and for a folder moved into itself or below itself. Read as `ErrNoReplaceUnsupported`, the second would end the item `no_safe_rename` and turn the source's writes off. So fsaccess adds `ErrIntoItself`: on `EINVAL`, the Linux backend compares the two folders' current paths (`/proc/self/fd`), and synthfs checks the tree. The executor maps it like the plan's `into_itself` refusal, never to `no_safe_rename`.
- **F3. Shape of Writer errors.** A refusal is an `*fsaccess.Error` with the method name as `Op`, an empty `Outcome` (it is a write result, not an access observation), and an `Err` matching both the sentinel and the errno (`errors.Is`), so the OS text stays available for `failed`. `ENOENT` is outcome `absent`; anything else `unavailable`. `fsaccess.WriteError(op, name, errno)` is exported so synthfs fails exactly like Linux. A destination `Dir` of another FS is refused before any call, with an empty outcome (a caller bug).
- **F4. Mkdir.** The mode is set with `fchmod` on an `O_DIRECTORY|O_NOFOLLOW` handle of the new folder, not `fchmodat` by name, which has no working `AT_SYMLINK_NOFOLLOW` on Linux before `fchmodat2`. When opening or `fchmod` fails, the new, empty folder is removed again before the error returns.
- **F5. EINTR.** All four calls retry `EINTR` like the rest of the backend. Go installs its signal handlers with `SA_RESTART`, and the executor confirms every step by looking at both names anyway.
- **F6. synthfs writes.** Checks follow the kernel's order: two devices (`EXDEV`), read-only (device capabilities, `FSInfo`, or its mount row), a missing name, a mount point (`EBUSY`, unavailable), into itself, a taken name (`EEXIST`, compared as `Lstat` compares, so case-insensitive devices refuse a case-only rename), and only then a device given capabilities without `NoReplaceRename` (`EINVAL`), as an old driver answers after the VFS check. A device without capabilities renames. Generated entries and folders cannot be changed (outcome unavailable).
- **F7. instrument.** `Call.To` is the full path of a rename's new name. `InjectError` keys a rename by its old name's full path and `Sync` by the folder's path. A wrapped `Dir` without a `Writer` fails every write with `ErrNoReplaceUnsupported`; the portable backend fails all four the same way, `Sync` included.
- **F8. The guard test** (`internal/fsaccess/writer_guard_test.go`) type-checks every non-test package of the module against `go list -export` data. Outside `internal/executor/...` and `internal/fsaccess/...` it fails on any use of `fsaccess.AsWriter` or the `fsaccess.Writer` type, any call or method value of a Writer method on a value implementing `Writer`, and any type assertion that gives a `Dir` such a method. A fixture proves each case is caught and that `os.Mkdir` or `(*os.File).Sync` are not.
- **F9. Upgrade.** Capabilities recorded before 0006 lack `no_replace_rename` and read `false` (`writes.unavailable: no_replace_rename`) until the next availability refresh rewrites them, at startup and every minute. `sources` exports the reasons as `WritesForbiddenByConfig`, `WritesReadOnly`, and `WritesNoReplaceRename`. `CheckWrites` reads the state and capabilities as last recorded.
- **F10. Names.** The domain type is `ErrorCode` (Interfaces wrote `Code`). The operator docs split the NTFS row of the capabilities table into `ntfs3` (no-replace rename) and `ntfs`/`fuseblk` (none).
- **U1. When a plan runs at once.** The interface previews a plan when it is `bulk`, or has a `conflict`, a `refused` item, `kept_lost > 0`, or nothing `planned`; otherwise it calls `run-action` right after the plan (`needsPreview` in `web/ui/src/api/organize.ts`). The rule is the same for every kind, so an undo of a multi-item action without a conflict runs at once. Search's Move to…, Rescue kept items…, and Compare's merge always preview.
- **U2. The result follows the action.** After `run-action`, the result line reads `GET /api/history/{id}`, which organize job events refetch: it says "Renaming…" (and so on) while queued or running, then "Renamed." with Undo once `undo.possible`. The result of an undo offers no Undo of its own; History does.
- **U3. The destination chooser.** It opens at the item's current folder, from the detail's `ancestors`, else at the source's top folder; with several sources that allow changes (Search), its first level lists those sources. Move here is off while the trail holds an entry being moved. Deeper cases, such as a selection's folders, are left to the plan's `into_itself`.
- **U4. New folder in the chooser** sends `plan-create-folder` and `run-action` at once, with no preview: `plan-create-folder` refuses a taken name with `409 name_taken`. The folder appears once its job ends, since folder listings are keyed under `['entries']`, which `refreshAfterMove` refetches.
- **U5. Which sources Search can move.** Move to… offers the sources that allow changes and are online, limited to the search's `source` and, for rows ticked one by one, to their sources. A selection is sent as `selection_id`, ticked rows as `entry_ids`.
- **U6. History counts.** The list shows Done, Not included (`refused`), Left as they were (`conflict`), Not done (`failed`, `changed`, `not_permitted`, `offline`, `no_safe_rename`, and `not_empty` together), Not attempted, and Needs your check. An action with `manual_recovery > 0` lists those items at once, read with `?state=manual_recovery`, each with I fixed it.
- **U7. Live history.** The history is one of the responses fetched again after missed events. Every organize event refetches it, and a terminal one runs `refreshAfterMove`.
- **U8. Vocabulary.** The vocabulary test also bans "journal" and "executor".
- **U9. Compare's merge** is offered on `only_left` or `only_right` when the group shown holds files, both sides are folders (not archives, not members) of one source, and that source allows changes and is online.
- **U10. Error wording by control.** `400 invalid_request` from Rename and New folder reads as a name that cannot be used, including a case-only change on a case-insensitive disk (D17). For Rescue kept items…, `409 invalid_entry_state` reads "Nothing to rescue", and `400 invalid_request` asks for a folder outside this one.
- **S1. `allow_writes` reaches the registry through `sources.New`.** Its existing `config.Sources` parameter already carries `AllowWrites`, so the `Service` keeps it, and `set-source-writes` and `GET /api/sources` read it there; no exported signature changes. A registry built from a zero `config.Sources` (as other packages' tests do) therefore reports `forbidden_by_config`; the sources tests build theirs with writes allowed, as the default configuration does.
- **S2. `set-source-writes` details.** `enabled` is required (`null` or missing is `invalid_request`), so a forgotten field never turns writes off. Setting the value a source already has answers 200 with the source, writes nothing, and records no event: the audit event is per change. Turning writes off is accepted also where they are unavailable or the source is offline; turning them on checks only `WritesUnavailable` on the capabilities as last recorded (F9), not the state, since `CheckWrites` refuses an offline source at plan, run, and intent time anyway. The command does not refresh availability itself: the Sources screen reads `GET /api/sources`, which does, just before.
- **S3. The audit event** is `source_writes_set` with detail `{"source_id", "enabled", "previous_enabled"}`. The executor writes the same kind when `no_safe_rename` turns writes off, adding its reason.
- **S4. `remove-source` order of refusals.** In its transaction: an active scan (`job_active`, as before), then a `queued` or `running` action (`job_active`), then an `intent` or `manual_recovery` item of any of its actions (`recovery_needed`). Planned, expired, done, and stopped actions with no open item never block, and go with the source by cascade.
- **S5. `kind=directory`.** The listing keeps children's every-state rule, so a missing folder is listed with its state, and the plan refuses it as a destination. Inside an archive read completely it lists the member folders. The cursor carries the filter (`"k"`), and one made without it, including every cursor issued before R3, stays valid only without it. The filter is an extra `e.kind = 'directory'` condition on the same per-sort index range, which the plan test checks for both forms.
- **S6. D18 beyond the two named guards.** The zip member-hash commit and `membersUnreadable` compare the path too; `membersUnreadable` had no entry re-check at all and now has the same one as `entryLive`. A listing dropped by the guard after it started stays hidden in state `listing`, and the next pass drops and repeats it, as for an identity change. Tests simulate a move in synthfs (its `Writer`, from a test file) and in the entries rows, both before the walk and during an open file's read.
- **I1. One fold, two feeders.** `internal/index/fold.go` holds the fold (`agg`): `nameSignals`, `addFile`, `fileRow`, `absorb` (a finished child folder into its parent), `notableFile`/`notableFolder`, and `finishFolder`. The scan's `entry`, `file`, and `finish` and the refold call the same functions; the refold rebuilds a stored child folder's part from its row and `dir_stats` (`storedFold`). `dir_stats` does not record a subtree's indicator count, so the refold counts the length of the stored indicator list instead: it is non-zero exactly when the count is, and the rules read only `Indicators > 0`.
- **I2. Indicators by path.** A folder's list is the 20 smallest paths among the indicator entries below it, merged up the tree; both the scan and the refold keep it so, and the first scan after upgrading rewrites the lists once. Because a list can now push out an entry the scan has listed but not yet inserted, a new folder's frame takes its token hold when the folder is listed, and a token released but not yet handed to the writer is taken back when a list holds it again.
- **I3. The root's name for the rules** is the last component of `sources.rel_root`, or the base of `mount_point` when `rel_root` is empty, in both the scan (formerly `filepath.Base(AbsRoot)`) and the refold, which cannot see the mount's root. They differ only for a bind mount of exactly the source's folder, where the root is now classified by its own name instead of the mount point's.
- **I4. What Refold is given.** `touched` is the moved entry and the folder it left (rename), the new folder (mkdir), or the folder that held it (rmdir); IDs no longer indexed are skipped. Every folder touched or above a touched entry is folded again, deepest first, and its direct files are classified again, which covers the sibling-dependent kinds of both parents. A folder that is unreadable or a mount boundary keeps its stored row and `dir_stats` (a scan cannot list it either). `partial` is what the children give, as in a scan, so a mark left by a cancelled scan on a folder a move refolds is cleared. The refold writes only the row columns a scan computes (never `last_seen`/`scan_gen`) and only rows that differ.
- **I5. MoveEntry refusals.** Besides into itself and another source, it refuses an invalid name, the unchanged path, a source's root, a missing entry, and a destination that is not a present folder. At the new path, a present row is an error; a missing row with a present row below it is an error; a missing subtree with intent is `ErrMissingIntent` (wrapped with the path). Every check runs before the first write, so a refusal changes nothing even if the caller commits.
- **I6. Post-step facts.** `CtimeNs == 0` means unknown and is stored as NULL, as a scan stores it. `file_content` and `archives` take only the new ctime, and only when they still describe the file as stored (size, mtime, ctime, inode) and the step left its mtime and inode as stored; otherwise they stay as they were, and hashing re-reads the file. A rename of a hard-linked file also changes its other names' ctime; those rows are not updated, so the next scan rewrites them and their digests are read again.
- **I7. InsertFolder and RemoveFolder.** `PostFacts` carries no size, so the inserted row has size 0 and NULL alloc, nlink, and mode; `first_seen`/`last_seen` are the database's clock, `scan_gen` the source's current one; an empty `dir_stats` row is written and Refold classifies it. The next scan only records the folder's own size facts (its row and `dir_stats` rewritten with the same totals). A missing row at the new folder's path is handled as for a move. RemoveFolder refuses a present entry below (error) or a missing one with intent (`ErrMissingIntent`); the folder's own decision or tags do not block it (it is undoing a folder Precious made).
- **I8. DeferWhile** runs before the scan opens its source; an error from it fails the attempt.
- **E1. The reconcile job.** A job that only sweeps and reconciles has scope `organize-reconcile:<source>` and payload `{}` (no `action_id`), enqueued with `EnqueueOnce` by `Startup`, by `CancelAction` (E9), and after a sync failure (E7). It ends at once when an action of its source is `queued` or `running`, since that action's job reconciles; an action job defers (`waiting_turn`) while it runs. These two checks read rows the other side commits before it runs (the action row before its job exists, the reconcile job's `running` state before its handler), so the two never reconcile side by side. On an offline source it defers by `sources.RefreshInterval` (`source_offline`), so an unchecked step waits for the disk instead of failing the job.
- **E2. `Enqueue` writes `actions.job_id`** in the same transaction, since the sweep reads it; `run-action` need not. The start of an attempt sets the action `running` with `started_at`. Deferral reasons are `scan_running` and `waiting_turn`. Progress `items` counts all of the action's items, `done` its done ones.
- **E3. Identity under capabilities.** The kind always; size and modification time (within the resolution, an hour off on a local-time filesystem) for anything but a folder; device and inode only with `stable_identity`, where a NULL stored value is a mismatch. The change time is never compared, since the rename itself changes it. A folder's size and time follow its contents, which the move carries along.
- **E4. Descent.** Every component below the root is lstat'd, checked (a folder, not a mount boundary, and the index's device and inode under `stable_identity`), then opened with `OpenDir` against that lstat, as the scan does. A mount on the way ends the item refused `other_filesystem`; any other mismatch `changed`. The root handle is compared with the root entry when that row has device and inode. A changed folder's post-step facts come from an lstat through its parent's handle; for the source root, from the root opened again through `sources.Open`, falling back to the handle's facts when that fails.
- **E5. Intent outcomes the design leaves open.** A destination that is no longer a present folder, or whose `to_dir_seq` item is not done, ends `changed` without a reason. A `manual_recovery` item on the source ends the current item and the rest `not_attempted` and stops the action. An `rmdir` whose folder has a non-missing child in the index, or a missing one with owner intent (`MissingIntentAt` of its path), ends `not_empty`. A folder moved into itself is refused `into_itself` at intent by path, besides `ErrIntoItself`. `decision_after` is rewritten at intent: the destination's effective decision when the entry has no own decision and it differs, else NULL.
- **E6. Cancel and shutdown.** Between items, an ended context stops the action only when the job's `cancel_requested` is set, and the handler then returns the context's error (job `cancelled`); on shutdown or a lost lease the action stays `running` for the next attempt. Everything after an intent commits (preflight, step, sync, confirm, outcome) runs on a context without cancellation.
- **E7. Sync and confirm.** A sync error leaves the item `intent`, stops the action, and enqueues the reconcile job. Reconciliation syncs the folders of a step it finds done before recording it; when that sync fails the reconcile attempt fails and the item stays `intent` until the next organize attempt or start. Confirming after a step and D5's last row use reconciliation's table, "not done" becoming `failed` (with the OS text, or a message when a step that reported success left both names as they were). Reconciliation that cannot look at a name (an I/O error) leaves the item `intent`; in an action job it then stops the action and fails the job. An item found not done whose action no longer runs ends `not_attempted` instead of `planned`. `ActionDone` runs once after reconciliation that recorded a done item.
- **E8. Findings.** For `mkdir`, `from` is always `absent`; for `rmdir`, `to` is always `absent`. A folder that cannot be opened while looking makes its name `other` (`absent` when the folder itself is gone), and a step counts as done only with both folders open.
- **E9. `CancelAction`** stops a `queued` action at once and cancels its job. For a `running` action whose job is running it only cancels the job. When that job has already ended (it was waiting after a deferral) or is gone, it enqueues the reconcile job, whose sweep stops the action and calls `ActionDone`; `CancelAction` has no `Index` to call it. An unknown action is `not_found`, any other state `action_not_runnable`.
- **E10. Writes off on `no_safe_rename`.** One transaction ends the item, stops the action, sets `write_enabled = 0`, and, only when it was on, writes the audit event set-source-writes writes, `source_writes_set`, with actor `system` and detail `{"source_id","enabled":false,"previous_enabled":true,"reason":"no_replace_rename"}`. The recorded capabilities are left alone: the availability refresh would set them back from the type table.
- **E11. An action job whose source cannot be opened** (offline, unknown) ends its first planned item `offline` and stops; nothing is reconciled.
- **E12. The e2e test** of R3.1 mounts a tmpfs (as root, or in a user and mount namespace), because a temporary directory on an overlay or unknown filesystem has no no-replace rename and its writes would be unavailable.
- **O1. organize.Options** holds Store, Policy, AllowWrites, Clock, and Logger, not Runner, Sources, or Executor: organize calls only the executor's package functions (`Enqueue`, `CancelAction`) and `sources.CheckWrites`, and `Index()` needs only the policy's Refolder. So serve builds organize first and the executor over `org.Index()`, with no construction cycle. Commands use their transaction's time (the runner's clock); the history reads `Options.Clock`; serve passes the same clock to both.
- **O2. EntryRow outside internal/web/api.** `api` exports `EntryRow` (an alias of its row type) and `EntryRows(ctx, tx, ids)`, so actions and items carry exactly the children row. The history handlers live in organize (`Routes`), which therefore imports `web/api`; `api` imports nothing of R3.
- **O3. Plan checks, in order:** the request's shape (400), its IDs and the destination (404, then 400 for a destination that is not a present folder: a file, an archive, a missing or unreadable folder, a member), the targets (404, `selection_expired`), then `CheckWrites` and `recovery_needed` on the destination's source, then pruning. A malformed ID is `not_found`, an archive member's ref where an entry is needed `invalid_request`, except as a `plan-move` target, where it is an item refused `inside_archive` with no entry and the archive's path joined with the member's. A refused plan rolls back and leaves no action.
- **O4. Item checks.** Refusals first (`other_source`, `missing`, `source_root`, `other_filesystem`, `contains_mount`, `into_itself`, `already_there`, then `would_lose_keep` in bulk), then conflicts (`name_taken` by an entry that is not missing, `name_taken_in_plan`, `name_taken_by_missing`). Only planned items take a name in the plan. Names are bucketed by a key of each rune's smallest simple-folding rune and confirmed with `bytes.EqualFold` on a case-insensitive source. `decision_after` is recorded on planned and conflict items; `kept_lost` counts planned items only. A target folds into an ancestor target only when the plan does not refuse that ancestor, so an entry inside a refused folder (for example one holding a mount) still moves on its own; a rescue keeps only the outermost own keeps before that.
- **O5. plan-rename** of a missing entry, a mount point, or a folder holding one answers 201 with that item refused, like any plan; only a taken name is refused outright (D9). A rename's action has no destination; a new folder's is its parent.
- **O6. Expiry.** The read API shows a planned action past `expires_at` as `expired`; the next plan of any source sets such actions `expired` and deletes those expired for over a day (an ID of a deleted plan may be given again, since `actions.id` has no AUTOINCREMENT). `run-action` answers `action_expired` for both.
- **O7. Action figures.** `bytes` and `files` sum the items planned, under way, or done. `undo.possible` needs a done or stopped action with a done rename, or a done mkdir with `created`, whose entry is still indexed and not reversed; `nothing_done` when it never had one, `already_undone` when all are reversed.
- **O8. plan-undo** skips an item whose entry is gone from the index, plans an rmdir of a created folder that is missing as refused `missing`, and does not reverse rmdir items (redoing an undo of a merge leaves its folders to be made again by hand, the renames then reading `previous_folder_gone`). With `destination_id`, every name conflict at the previous place (`name_taken`, `name_taken_in_plan`, `name_taken_by_missing`) and `previous_folder_gone` go to that folder; a destination on another source is `invalid_request`.
- **O9. plan-merge** reads Compare's group in pages of 1,000 and refuses a group over 10,000 files before planning. On the way, an existing folder is matched under the source's case rules; a non-folder, or an unreadable folder, makes the items below `name_taken`, and a missing entry with the owner's intent `name_taken_by_missing`, with no mkdir planned. A file is planned with `to_dir_seq` when its folder is a mkdir of the plan.
- **O10. resolve-recovery** on an offline or unknown source fails with `index.StartScan`'s `source_offline` or `unknown_source` and changes nothing: the item stays to be resolved once the scan can start.
- **O11. History API.** `source` names a known source (else `not_found`); the list's cursor is the last action's ID, the items' the last seq; `state` values are checked against the item states. The audit events are `action_run`, `action_cancelled`, and `recovery_resolved`, with the action, its kind, source, and job (plans write none).
- **C1. Playwright (4.1).** The six R3 tests run after every earlier one, because they change the corpus on disk. In order: allowing changes (cancel sends nothing), a rename and its undo (a sibling's name is refused first; the browser's expected 409 is matched and removed from the suite's problem list), Compare's merge of `Fotos - Copia` into `Fotos` (before the rescue takes `Thumbs.db` out of `Fotos`), a bulk move from Search, the rescue of the kept `Thumbs.db`, and History, which undoes the bulk move. They check the disk through the corpus path and the index through the read API. All 42 tests pass.
- **C2. Smoke (4.3).** An r2d binary scanned and hashed the corpus on ext4. On a copy of that schema-5 database, the r3 binary migrated it to schema 6. The source reported `writes {enabled: false, unavailable: null}` and `no_replace_rename: true`, and writes were then turned on. Every action ended `done`, with the disk matching:
  - `curriculo.doc` renamed;
  - `Fotos/2004` moved into `Documentos`, then undone (`undo.reason: already_undone`);
  - the merge moving `DSC_editada.JPG` into `Fotos`.

  A rescan then found the same 519 entries: no new ID, none gone, none missing, and the renamed file kept its ID at its new path. The script and its files were deleted.
- **V1. Facts are read before the outcome transaction.** `settle` used to read a changed folder's post-step facts inside the outcome's write transaction. For the source root, that reopens the source with `sources.Open`, which records a changed observation (capabilities, mount point, state) through the store's single writer, which the outcome transaction holds, so the job waited forever. `settle` now reads every fact (the moved entry, both parents, the parent of a new or removed folder) after the folders are synced and before the transaction opens, and the transaction only writes. No disk call and no other store write runs inside an executor write transaction.
- **V2. An undo's rmdir respects intent below the folder.** The rmdir intent asked `MissingIntentAt` at the folder's own path, which is false for a present folder. So a folder the merge made, holding only a missing file the owner had tagged, was removed on disk, then `RemoveFolder` refused it and the item ended `manual_recovery`. `index.IntentBelow` (the same intent test `RemoveFolder` runs, on every entry strictly below the folder) is added to `executor.Index`, and the rmdir ends `not_empty` at intent when it is true, before any disk call.
- **V3. Names a FAT-family or NTFS disk cannot hold.** vfat, exfat, and NTFS refuse `" * : < > ? \ |` and control characters with `EINVAL`, and drop a trailing dot or space, so the index would name an entry the disk does not hold. `plan-rename` and `plan-create-folder` answer `400 invalid_request` for such a name when the source's `fs_type` is `vfat`, `exfat`, `ntfs3`, `ntfs`, or `fuseblk` (also `fuseblk.*`); moves, merges, and undos keep names the disk already holds. The executor now reads `EINVAL` on a rename (other than `ErrIntoItself`) as a missing no-replace flag, so `no_safe_rename` with writes turned off, only on `zfs`, the one type that allows writes whose drivers may lack the flag. On any other type, the rename refused the name: the item ends `failed` with the system's message, writes stay on, and the action goes on. This narrows D5's `EINVAL` row. A Writer error without `EINVAL` (no Writer, the portable backend) is still `no_safe_rename`.
- **V4. A merge makes only folders that receive a file.** The merge plans the mkdirs on a file's way before it decides the file. When every file bound for a missing folder ended refused or in conflict (`would_lose_keep`, `name_taken_in_plan`, `inside_archive`), empty folders were made. After planning, `plan-merge` drops every mkdir with no planned rename at or below it, numbers the items again from 1, and remaps `to_dir_seq`. A refused or conflict item that named a dropped folder keeps only its path. A file in conflict with a dropped folder's name stays a conflict (`name_taken_in_plan`), which is conservative.
- **V5. A claim cut short by shutdown is not an error.** CI failed once on `TestJobRemovedWhileRunning` in `internal/jobs`, which R3 does not change. When `Stop` cancels the runner while a claim's query is running, `dispatch` logged the query's `context canceled` as an error. That code is older than R3, and the failure did not reproduce in 600 local runs. `dispatch` now logs a claim error only while its context is live, as `deviceKey` already does.
- **C3. Owner sign-off (4.4), 2026-10-08.**
  - **Deploy.** After a backup of the database (`r2d-before-r3.db`), the reference server ran `r3-smoke-8dc7ba3`, and the migration to schema 6 applied at start.
  - **Sources.** Both sources reported `writes {enabled: false, unavailable: null}` and `no_replace_rename: true`: ext4 for the corpus, ZFS 2.4.4 for the archive.
  - **Folder owners.** The corpus folders belonged to root, and the service runs as its own user, so every rename would have ended `failed` with a permission error. That user was made the owner of the corpus folders only. The archive stays unwritable to it.
  - **What the owner did.** He allowed changes on the corpus source only. There he renamed a file and undid it, moved a folder and undid it from History, and merged `Fotos - Copia` into `Fotos` from Compare.
  - **Result.** He signed off ("LGTM").
