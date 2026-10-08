# Spec Delta

## MODIFIED Requirements

### Requirement: Only the executor changes a source, and never by overwriting
The executor SHALL be the only code that changes a source (§5 I2):
- **Organizing.** It renames an entry and creates a folder.
- **Undo.** It removes a folder only when undo takes away an empty folder its own action created.
- **Cleanup.** Inside a source's quarantine folder only, it creates origin records and deletes the files and folders of a checked purge set.

It SHALL act only for an action the owner planned and ran.
- **Renaming.** Every rename SHALL use the platform's no-replace rename (`renameat2` with `RENAME_NOREPLACE` on Linux) between two folders opened through the rooted, identity-checked access, never by path.
- **Taken destinations.** A destination that exists when the rename runs SHALL stop that item as `conflict`, with nothing replaced, and the action SHALL go on with its next item (§5 I3).
- **Origin records.** An origin record SHALL be created exclusively, so an existing name is never replaced, and SHALL be written in full and synced before the item it describes moves.
- **Purges.** A purge SHALL delete only what its check recorded, compared by identity just before each deletion, and only below the quarantine folder.
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
- **THEN** the item ends `conflict`, the existing file is unchanged, and the entry is not moved
