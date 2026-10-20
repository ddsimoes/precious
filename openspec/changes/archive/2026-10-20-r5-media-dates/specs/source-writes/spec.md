# Spec Delta

## MODIFIED Requirements

### Requirement: Only the executor changes a source, and never by overwriting
The executor SHALL be the only code that changes a source (§5 I2):
- **Organizing.** It renames an entry and creates a folder.
- **Undo.** It removes a folder only when undo takes away an empty folder its own action created.
- **Cleanup.** Inside a source's quarantine folder only, it creates origin records and deletes the files and folders of a checked purge set.
- **File dates.** It sets the modification time of a regular file outside the quarantine, and nothing else of it (§10.7.4).

It SHALL act only for an action the owner planned and ran.
- **Renaming.** Every rename SHALL use the platform's no-replace rename (`renameat2` with `RENAME_NOREPLACE` on Linux) between two folders opened through the rooted, identity-checked access, never by path.
- **Taken destinations.** A destination that exists when the rename runs SHALL stop that item as `conflict`, with nothing replaced, and the action SHALL go on with its next item (§5 I3).
- **Origin records.** An origin record SHALL be created exclusively, so an existing name is never replaced, and written in full and synced. It is written after the item it describes has moved, so it never describes a move that did not happen.
- **Purges.** A purge SHALL delete only what its check recorded, and only below the quarantine folder. Before deleting anything of an item, it SHALL verify the item's whole tree by identity, and compare each entry again just before deleting it.
- **Recovery.** A purge or unlink left with its intent recorded SHALL be resumed only after the write permission, the check, and the item's decisions are checked again. Otherwise it SHALL stop, with what was already deleted recorded.
- **Setting a time.** A modification time SHALL be set through the file's folder, opened through the rooted, identity-checked access, without following a symbolic link and without opening the file, only when the file still has the identity the item recorded, its change time included, and no other hard link. The change time is the system's. The time found just before SHALL be journaled first.
- **No safe rename.** When the filesystem refuses the no-replace flag, the item SHALL end `no_safe_rename` with nothing moved. The executor SHALL then turn the source's write permission off, with an audit event. Only a ZFS source's "invalid argument" on a rename is read as that refusal; on any other filesystem it refuses the name, and the item SHALL end `failed` with the system's message while writes stay on.

#### Scenario: R3.1 A destination that appears during execution stops that item
- **WHEN** a bulk move runs, and a file with the same name as its second item appears in the destination after the preview and before that item's rename
- **THEN** the second item ends `conflict`, the file that appeared is unchanged and the moved file stays where it was, and the action's other items are moved

#### Scenario: R3.1 A rename never overwrites
- **WHEN** a rename targets a name an existing file already has
- **THEN** the planning answers `409 name_taken`, and when the name is taken only after planning, the item ends `conflict` and both files keep their content and names

#### Scenario: Filesystem without the no-replace flag
- **WHEN** the filesystem of a ZFS source answers the no-replace rename with "invalid argument"
- **THEN** the item ends `no_safe_rename`, nothing is moved, the remaining items are not attempted, and the source's write permission is off with an audit event

#### Scenario: Deletion stays inside the quarantine
- **WHEN** a purge item names a path outside `.precious-quarantine`, or a file there that the check did not record
- **THEN** the executor deletes nothing, and the item ends `changed`

#### Scenario: An origin record never replaces a file
- **WHEN** a file already holds an origin record's name when the executor writes it
- **THEN** the record step ends `conflict` and the existing file is unchanged. The quarantined entry stays in quarantine, and its origin is still in the history.

#### Scenario: R5.4 Setting a time changes nothing else
- **WHEN** a `set_mtime` item runs on a photo
- **THEN** the instrumented filesystem records one time change and no open for writing, rename, or unlink, and the photo's size, bytes, inode, and access time are unchanged

#### Scenario: A file changed before its time is set
- **WHEN** a photo is modified on disk after the write-back was planned, even with its modification time put back afterwards
- **THEN** its item ends `changed` with nothing written, its digest and metadata are not carried to new times, and the action goes on

#### Scenario: A file the service does not own
- **WHEN** the operating system refuses to set the time of a photo that the service account does not own
- **THEN** that item ends `failed` with reason `not_owner`, the other items run, and the source's write permission stays on

## ADDED Requirements

### Requirement: A set modification time is reconciled after a crash
Reconciling a `set_mtime` step left with its intent recorded SHALL look at its file: with the recorded identity and the old time, the step SHALL go back to pending; with that identity and the new time, it SHALL be done and the index updated; anything else SHALL be `manual_recovery`. A time the filesystem cannot tell apart from the old one SHALL never be planned or run (`no_change`). Setting a time SHALL never turn writes off.

#### Scenario: Crash after setting the time, before recording it
- **WHEN** the process stops after a time was set and before its outcome is recorded, and the server starts again
- **THEN** the time is not set a second time, the item is done, and the index shows the new time

#### Scenario: A FAT card rounds the time
- **WHEN** the owner writes back a time with odd seconds to a photo on a FAT source
- **THEN** the planned time is rounded down to an even second, and reconciling after a crash recognizes it as done
