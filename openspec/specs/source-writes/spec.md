# source-writes Specification

## Purpose
Defines when Precious may change a source and how it does so safely (§5 I2, I3; §6.1). It covers the per-source write permission and its availability, and the one executor that journals each step before it runs, never replaces an existing entry, and reconciles after a crash or stops for manual recovery.

## Requirements

### Requirement: Write permission is per source and off by default
Every source SHALL have a write permission, off when the source is added (§6.1). `set-source-writes` SHALL turn it on or off and answer with the source. Each change SHALL write one audit event. `GET /api/sources` SHALL report, for each source, `writes.enabled` and `writes.unavailable`. The second is `null`, or the reason writes cannot be turned on:
- `forbidden_by_config`;
- `read_only` (the filesystem is read-only);
- `no_replace_rename` (the filesystem has no no-replace rename).

#### Scenario: A new source cannot be changed
- **WHEN** the owner adds a source
- **THEN** `GET /api/sources` reports `writes.enabled` as false for it

#### Scenario: Turning writes on and off
- **WHEN** the owner sends `set-source-writes` with `enabled` true for a source whose `writes.unavailable` is `null`, and later with `enabled` false
- **THEN** each answer reports the new value, and each change writes one audit event naming the source and the new value

### Requirement: Writes are unavailable where they cannot be safe
`set-source-writes` with `enabled` true SHALL fail with `409 writes_unavailable` and change nothing when the source's `writes.unavailable` is not `null`. Turning writes off SHALL always succeed. When a source's write permission is on and its filesystem later becomes read-only or loses its no-replace rename, the executor SHALL still refuse every step there (see "No write without permission").

#### Scenario: R3.7 Writes forbidden by the configuration
- **WHEN** the configuration sets `sources.allow_writes = false`
- **THEN** every source reports `writes.unavailable` as `forbidden_by_config`, `set-source-writes` with `enabled` true fails with `409 writes_unavailable`, and the Sources screen offers no control to turn writes on

#### Scenario: Read-only filesystem
- **WHEN** a source is on a read-only mount
- **THEN** it reports `writes.unavailable` as `read_only`, and turning writes on fails with `409 writes_unavailable`

### Requirement: Turning writes on needs a confirmation
The Sources screen SHALL show each source's write permission. It SHALL turn writes on only from a confirmation dialog that says Precious will then move and rename entries on that source when the owner asks. It SHALL turn writes off with one action. When writes are unavailable, the screen SHALL show the reason instead of the control.

#### Scenario: R3.7 Confirmation before writes
- **WHEN** the owner chooses to allow changes on a source and cancels the dialog
- **THEN** no command is sent, and the source still reports `writes.enabled` as false

### Requirement: No write without permission
The executor SHALL check, before each filesystem step and inside the transaction that records the step's intent, that:
- the source's write permission is on;
- the configuration allows writes;
- the source is online;
- its filesystem is neither read-only nor without a no-replace rename.

A step that fails the check SHALL NOT reach the filesystem. Its item SHALL end as `not_permitted` or `offline`, and the action's remaining items SHALL not be attempted. Commands that plan or run an action on a source without write permission SHALL fail with `409 writes_disabled` and change nothing.

#### Scenario: R3.7 No write happens without permission
- **WHEN** the owner runs a move on a source with writes on, and writes are turned off before its first item runs
- **THEN** the instrumented filesystem records no rename or folder creation, the item ends `not_permitted`, and the entries keep their paths

#### Scenario: Commands refuse a source without writes
- **WHEN** `plan-move`, `plan-rename`, or `run-action` names an entry or action on a source whose write permission is off
- **THEN** it fails with `409 writes_disabled`, and no action is created or run

### Requirement: Only the executor changes a source, and never by overwriting
The executor SHALL be the only code that changes a source (§5 I2):
- **Organizing.** It renames an entry and creates a folder.
- **Undo.** It removes a folder only when undo takes away an empty folder its own action created.
- **Cleanup.** Inside a source's quarantine folder only, it creates origin records and deletes the files and folders of a checked purge set.

It SHALL act only for an action the owner planned and ran.
- **Renaming.** Every rename SHALL use the platform's no-replace rename (`renameat2` with `RENAME_NOREPLACE` on Linux) between two folders opened through the rooted, identity-checked access, never by path.
- **Taken destinations.** A destination that exists when the rename runs SHALL stop that item as `conflict`, with nothing replaced, and the action SHALL go on with its next item (§5 I3).
- **Origin records.** An origin record SHALL be created exclusively, so an existing name is never replaced, and written in full and synced. It is written after the item it describes has moved, so it never describes a move that did not happen.
- **Purges.** A purge SHALL delete only what its check recorded, and only below the quarantine folder. Before deleting anything of an item, it SHALL verify the item's whole tree by identity, and compare each entry again just before deleting it.
- **Recovery.** A purge or unlink left with its intent recorded SHALL be resumed only after the write permission, the check, and the item's decisions are checked again. Otherwise it SHALL stop, with what was already deleted recorded.
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

### Requirement: Intent is journaled and a crash is reconciled
Before each filesystem step, the executor SHALL commit the step's intent: the operation, the entry, both folders and names, and the entry's identity. After the step, it SHALL fsync every folder the step changed, then confirm the step by looking at both paths. Only then SHALL it record the outcome and update the index, in one transaction. When the index update fails, the item SHALL be marked `manual_recovery`, never left with its intent recorded. Identity SHALL compare under the source's capabilities: device and inode only where identity is stable, and times within the filesystem's resolution.

An organize job SHALL reconcile every step of its source left with its intent recorded before it runs any item, and only while no scan of that source is running. When the server starts, Precious SHALL enqueue that reconciliation for every source that needs it, without touching the disk outside a job. Reconciling sorts each step:
- **Not done.** The entry is at its old path with its identity, and the new path is free. The step SHALL go back to pending, to run once.
- **Done.** The entry is at its new path with its identity, and the old path is free. The step SHALL be recorded as done, and the index SHALL be updated without renaming again.
- **Anything else** SHALL be marked `manual_recovery`, and the action's remaining items SHALL not be attempted.

While a source has an item in `manual_recovery`, planning and running actions there SHALL fail with `409 recovery_needed`, until the owner marks the item resolved with `resolve-recovery`. Resolving starts a scan of the source.

#### Scenario: R3.2 Crash after the intent, before the rename
- **WHEN** the process stops after committing a rename's intent and before calling the rename, and the server starts again
- **THEN** the entry is renamed exactly once, the instrumented filesystem records one rename for it, and the history shows the item done

#### Scenario: R3.2 Crash after the rename, before recording it
- **WHEN** the process stops after the rename succeeds and before its outcome is recorded, and the server starts again
- **THEN** no second rename is attempted, the item is done, and the index shows the entry at its new path with its ID

#### Scenario: R3.2 Ambiguous outcome
- **WHEN** at reconciliation both the old and the new path hold an object, or neither does
- **THEN** the item is marked `manual_recovery`, nothing is renamed, the history shows both paths with what was found, and planning on that source fails with `409 recovery_needed` until the item is resolved
