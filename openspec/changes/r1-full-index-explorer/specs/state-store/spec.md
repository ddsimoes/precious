## ADDED Requirements

### Requirement: Fresh baseline schema
This release SHALL create a new database with its baseline schema and SHALL NOT migrate, import, open, or modify a database created by v0.2 (`curator.db`). When the state directory contains `curator.db`, the server SHALL log a line naming it and leave it untouched.

#### Scenario: v0.2 database left untouched
- **WHEN** the server starts with a state directory that contains `curator.db` and no `precious.db`
- **THEN** `precious.db` is created with the baseline schema, `curator.db` is byte-for-byte unchanged, and a log line names `curator.db`

#### Scenario: Nothing imported
- **WHEN** the server first starts beside a v0.2 `curator.db` that has sources and inventory
- **THEN** `GET /api/sources` returns no sources

## MODIFIED Requirements

### Requirement: Local database storage
The database SHALL be the file `precious.db` in the configured state directory. On Linux, the server SHALL refuse to open it when that directory resides on a network filesystem (NFS, SMB/CIFS).

#### Scenario: Network filesystem refused
- **WHEN** on Linux, the state directory is on an NFS mount
- **THEN** startup fails with an error explaining that the database requires local storage

#### Scenario: Database file name
- **WHEN** the server starts with an empty state directory
- **THEN** it creates `precious.db` there

### Requirement: Versioned forward migrations
Schema changes SHALL be applied only through numbered migrations, starting from this release's baseline `0001`. Each migration SHALL run in its own transaction at startup and be recorded with its version. The server SHALL refuse to start against a database whose recorded schema version is newer than the binary supports, or whose recorded migrations do not match the binary's migration names.

#### Scenario: Fresh database migrated
- **WHEN** the server starts with an empty state directory
- **THEN** all migrations apply in order from `0001`, and the recorded schema version equals the latest migration

#### Scenario: Newer schema refused
- **WHEN** an older binary starts against a database migrated by a newer binary
- **THEN** startup fails without modifying the database

#### Scenario: Failed migration leaves prior version
- **WHEN** a migration fails partway
- **THEN** its changes are rolled back and the recorded schema version is unchanged

#### Scenario: Foreign migration history refused
- **WHEN** the server starts against a `precious.db` whose recorded migrations have names that differ from the binary's, such as a v0.2 database renamed to `precious.db`
- **THEN** startup fails without modifying the database

### Requirement: Enforced integrity constraints
The database SHALL enforce foreign keys and use write-ahead logging. At most one entry SHALL exist per source and exact raw path, whether it is present or missing. Command idempotency keys SHALL be unique. A constraint violation SHALL abort the enclosing write transaction and never partially apply it.

#### Scenario: Duplicate current child rejected
- **WHEN** a write would create a second entry with the same source and byte-identical path
- **THEN** the transaction fails, and the existing entry is unchanged

#### Scenario: Case-distinct names coexist
- **WHEN** a folder contains `Readme` and `README`
- **THEN** both are stored as separate entries

### Requirement: Consistent online backup
`precious backup <destination>` SHALL write a transactionally consistent copy of the database while the server may be running. It SHALL refuse to overwrite an existing destination and SHALL create the copy readable only by the service account. The copy SHALL pass an integrity check before the command reports success.

#### Scenario: Backup while serving
- **WHEN** a backup is taken while a scan job is writing
- **THEN** the backup opens as a valid database at a single consistent point in time, and its integrity check passes

#### Scenario: Existing destination refused
- **WHEN** the backup destination already exists
- **THEN** the command exits non-zero and leaves the existing file untouched
