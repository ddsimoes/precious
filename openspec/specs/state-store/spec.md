# state-store Specification

## Purpose

Guarantees that curator's own state (inventory, evidence, jobs, sessions, audit) is stored durably and consistently on local storage, evolves only through versioned migrations, and can be backed up safely while the service runs.

## Requirements

### Requirement: Local database storage
The database SHALL live in the configured state directory. The server SHALL refuse to open it when that directory resides on a network filesystem (NFS, SMB/CIFS) or inside a configured source root.

#### Scenario: Network filesystem refused
- **WHEN** the state directory is on an NFS mount
- **THEN** startup fails with an error explaining that the database requires local storage

### Requirement: Versioned forward migrations
Schema changes SHALL be applied only through numbered migrations. Each migration SHALL run in its own transaction at startup and be recorded with its version. The server SHALL refuse to start against a database whose recorded schema version is newer than the binary supports.

#### Scenario: Fresh database migrated
- **WHEN** the server starts with an empty state directory
- **THEN** all migrations apply in order, and the recorded schema version equals the latest migration

#### Scenario: Newer schema refused
- **WHEN** an older binary starts against a database migrated by a newer binary
- **THEN** startup fails without modifying the database

#### Scenario: Failed migration leaves prior version
- **WHEN** a migration fails partway
- **THEN** its changes are rolled back and the recorded schema version is unchanged

### Requirement: Enforced integrity constraints
The database SHALL enforce foreign keys and use write-ahead logging. At most one current inventory node SHALL exist per parent and exact raw name. Command idempotency keys SHALL be unique. A constraint violation SHALL abort the enclosing write transaction and never partially apply it.

#### Scenario: Duplicate current child rejected
- **WHEN** a write would create a second current node with the same parent and byte-identical name
- **THEN** the transaction fails, and the existing node is unchanged

#### Scenario: Case-distinct names coexist
- **WHEN** a parent contains `Readme` and `README`
- **THEN** both are stored as separate current nodes

### Requirement: Consistent online backup
`curator backup <destination>` SHALL write a transactionally consistent copy of the database while the server may be running. It SHALL refuse to overwrite an existing destination and SHALL create the copy readable only by the service account. The copy SHALL pass an integrity check before the command reports success.

#### Scenario: Backup while serving
- **WHEN** a backup is taken while a discovery job is writing
- **THEN** the backup opens as a valid database at a single consistent point in time, and its integrity check passes

#### Scenario: Existing destination refused
- **WHEN** the backup destination already exists
- **THEN** the command exits non-zero and leaves the existing file untouched
