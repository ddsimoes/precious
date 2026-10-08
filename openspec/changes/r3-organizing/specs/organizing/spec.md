# Spec Delta

## ADDED Requirements

### Requirement: An action is planned, then run
Every change to a source SHALL be an action that a `plan-*` command creates and `run-action` runs. Each command answers with the action and its items:
- `plan-move` (entries into a folder);
- `plan-rename` (one entry);
- `plan-create-folder`;
- `plan-rescue` (the kept entries out of a folder);
- `plan-merge` (the files only on one side of Compare into the other side);
- `plan-undo` (a done action).

Each item names its entry, its path before and after, and its state. A planned action SHALL list every item it will run and every item it refuses, with the reason. It SHALL change nothing on disk or in the index, and SHALL expire one hour after it was planned unless run.

`run-action` SHALL run exactly the planned items that are not refused, in order, in an organize job, and SHALL answer `202` with the job.
- **Errors.** An expired action SHALL fail with `409 action_expired`, and an action already run with `409 action_not_runnable`.
- **Size.** An action SHALL hold at most 10,000 items. A larger plan SHALL fail with `400 invalid_request`.

#### Scenario: A plan changes nothing
- **WHEN** the owner plans a move of a folder and does not run it
- **THEN** the folder stays at its path on disk and in the index, and after an hour `run-action` fails with `409 action_expired`

### Requirement: Single moves and renames run at once
In the detail panel, the owner SHALL be able to rename an entry, move it to a chosen folder, and create a folder inside a folder. When the plan has one item and no conflict, the interface SHALL run it without a further confirmation. It SHALL then show the result, with an Undo control (§10.5 Execution). A conflict SHALL be shown at once, and nothing SHALL run.

#### Scenario: Renaming a file from the detail panel
- **WHEN** the owner renames `Documentos/curriculo.doc` to `curriculo 2005.doc` on a source with writes on
- **THEN** the file has the new name on disk and in the index, with the same entry ID, and the panel offers Undo

#### Scenario: A taken name is refused
- **WHEN** the owner renames a file to the name of a sibling
- **THEN** the panel shows that the name is taken, and nothing changes

### Requirement: A bulk move shows every entry and conflict before it runs
For a bulk move, from Search results or a selection, the interface SHALL show the planned action's items before anything runs. It SHALL show every item, with its path before and after, every conflict, and every refused item with its reason, and it SHALL run only after the owner confirms. Entries indexed after the plan SHALL NOT join it (§10.5 Execution).

#### Scenario: R3.4 A bulk move moves only the entries shown
- **WHEN** the owner selects all results of a search, plans a move into a folder, and a new matching file is indexed before the owner confirms
- **THEN** the preview lists every selected entry and every conflict, the run moves exactly the listed non-conflicting entries, and the new file stays where it was

### Requirement: What a move plan refuses
A move plan SHALL fold an entry that lies inside another planned entry into that entry. It SHALL refuse, with a reason per item:
- an entry on another source than the destination (`other_source`);
- an archive member (`inside_archive`);
- an entry that is missing (`missing`);
- a source's top folder (`source_root`);
- a move of a folder into itself or below it (`into_itself`);
- an entry already in the destination (`already_there`);
- an entry on another filesystem than the destination, such as a mount point (`other_filesystem`).

An item whose name is taken in the destination, on disk or in the index, SHALL be a `conflict`. So SHALL an item whose name another item of the same plan takes. Names SHALL compare under the destination filesystem's capabilities, so `Foto.jpg` and `foto.jpg` collide on a case-insensitive filesystem (§10.5 Safety).

#### Scenario: Collisions follow the filesystem
- **WHEN** a plan moves `Foto.jpg` into a folder that holds `foto.jpg`, on a source whose capabilities report `case_sensitive: false`
- **THEN** the item is a `conflict`, and on a case-sensitive source it is not

### Requirement: A plan shows how decisions change
A moved entry SHALL keep its own decision, and an entry with none SHALL take its effective decision from its new place. Each planned item whose effective decision would change SHALL carry the decision after the move. The preview SHALL warn when an entry that is kept would no longer be kept (§6.7, I5).

#### Scenario: Moving a kept file into a discarded folder
- **WHEN** a file kept through its folder's keep is planned into a discarded folder
- **THEN** its item reports the effective decision `discard` after the move, and the preview warns that one kept entry would no longer be kept

### Requirement: Undo returns entries to their previous paths
Every done action that changed something SHALL be undoable from the history. `plan-undo` SHALL plan the reverse of the action's done items, in reverse order:
- each moved or renamed entry goes back to its previous folder and name;
- each folder the action created is removed, if it is empty.

An item whose previous path is taken, or whose previous folder no longer exists, SHALL be a `conflict` that asks for a destination. `plan-undo` with a `destination_id` SHALL plan those items into that folder under their previous names. An action SHALL be undone at most once, and its undo is itself an action in the history.

#### Scenario: R3.3 Undo of a move
- **WHEN** the owner moves `Fotos/2004` into `Documentos` and then undoes it
- **THEN** `Fotos/2004` is back at its path with the same entry IDs, and the history shows the move as undone

#### Scenario: R3.3 Undo when the previous path is taken
- **WHEN** the owner renames `a.txt` to `b.txt`, a new `a.txt` appears in the same folder, and the owner undoes the rename
- **THEN** the undo plan shows the item as a conflict and asks for a destination, and with one chosen, `b.txt` moves there as `a.txt` and the new `a.txt` is unchanged

### Requirement: Rescue the kept entries out of a folder
`plan-rescue` SHALL plan a move into a chosen folder of the outermost entries inside a folder whose own decision is `keep`, keeping their names (§6.8). A folder whose effective decision is `keep` SHALL be refused with `409 invalid_entry_state`, since nothing in it needs rescuing. The detail panel SHALL offer the rescue on a folder that holds kept entries.

#### Scenario: Rescue before discarding
- **WHEN** a folder holds two kept files, one inside a kept subfolder, and the owner rescues them into `Documentos`
- **THEN** the plan holds the kept subfolder and the other kept file, not the file inside the subfolder, and after the run both are in `Documentos` with their decisions

### Requirement: Move the files only on one side of Compare into the other side
In Compare, the owner SHALL be able to move the files only on the left into the right folder, or the reverse. `plan-merge` SHALL plan each file of the group to the same path inside the other folder. It SHALL plan the creation of each missing folder on the way first. It SHALL refuse archive members and a side that is an archive (§11.6, §10.5 Folder copies).

#### Scenario: R3.6 Merging Fotos - Copia into Fotos
- **WHEN** after hashing the owner moves the files only in `Fotos - Copia` into `Fotos`, and hashing and relations run again
- **THEN** `Fotos/2006/Praia/DSC_editada.JPG` exists, and Compare of the two shows no file only in `Fotos - Copia`, so every file left in `Fotos - Copia` has a copy in `Fotos`

### Requirement: The index follows a move
When a step is done, the index SHALL show the entry at its new path in the same transaction that records the outcome:
- **Identity.** Each moved entry and everything inside it SHALL keep its ID, own decision, own tags, category override, group mark, and digest.
- **Inherited intent.** Effective decisions and inherited tags SHALL come from the new ancestors.
- **Totals.** The totals and breakdowns of the folders on both sides SHALL equal what a full scan of the source would compute.

Relations and review lists SHALL be refreshed after the action, as after a scan.

#### Scenario: R3.5 Decisions and tags follow a move, and a rescan sees nothing new
- **WHEN** the owner moves a folder that carries its own keep, a tag, and an override, with hashed files inside, into another folder, and then rescans the source
- **THEN** the folder and its contents keep their IDs, decisions, tags, override, and digests, the rescan adds no entry and marks none missing, and it changes no folder's totals

### Requirement: Moves and scans of a source never overlap
An organize job SHALL wait while a scan of its source is running, and a scan SHALL wait while an organize job of its source is running. Each waits by deferring, never by failing. Hashing that reads a moved file SHALL drop its result, as for any changed file (I9).

#### Scenario: A move waits for the scan
- **WHEN** the owner runs a move while a scan of the same source is running
- **THEN** the action stays queued until the scan ends, then runs, and the scan's result does not mark the moved entry missing

### Requirement: The history lists every action
The interface SHALL offer History in its main navigation.
- **The list.** It SHALL list the actions newest first, each with its kind, time, state, and counts of items done, refused, in conflict, and failed. It SHALL offer Undo on actions that can be undone.
- **Recovery.** It SHALL show each item in `manual_recovery` with both paths and what was found, and offer to mark it resolved.

`GET /api/history` SHALL list actions with a cursor. `GET /api/history/{id}` SHALL return one action, and `GET /api/history/{id}/items` its items with a cursor.

#### Scenario: A move in the history
- **WHEN** the owner moves a file and opens History
- **THEN** the move is listed first, done, with one item and an Undo control
