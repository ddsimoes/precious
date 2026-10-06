# Spec Delta

## MODIFIED Requirements

### Requirement: Observation never mutates sources
Source access SHALL issue no operation that creates, writes, renames, removes, or changes permissions, ownership, or timestamps of any source entry. It SHALL never execute any source file. File content SHALL be read only by a copy search, and only from regular files. Content SHALL never be stored, logged, or sent anywhere; only its size and SHA-256 digest are kept.

#### Scenario: A20 zero mutating calls
- **WHEN** a full M1 discovery runs against an instrumented filesystem
- **THEN** the recorded calls contain no mutating operation and zero bytes of file content read

#### Scenario: A20 read-only mount works
- **WHEN** a source root is on a read-only mount
- **THEN** discovery completes with no write-related errors

#### Scenario: A20 copy search on a read-only mount
- **WHEN** a copy search runs on a source on a read-only mount
- **THEN** it completes, and the recorded calls contain no mutating operation

#### Scenario: Only copy searches read content
- **WHEN** a scan, an inbox poll, a probe, an aggregate walk, and a peek run against an instrumented filesystem
- **THEN** none of them opens a regular file or reads content

## ADDED Requirements

### Requirement: File reads are identity-checked
A file SHALL be opened for reading through its directory's rooted handle by a single-component name, without following a symbolic link and without blocking. The opened object SHALL be a regular file on its directory's device, not a mount point, with the device, inode, size, and modification and change time the listing recorded. Otherwise the read SHALL fail as `changed_during_observation`, and nothing read SHALL be used.

#### Scenario: A11 symlink swapped in before a read
- **WHEN** a listed file is replaced by a symlink to a file outside the source before it is opened
- **THEN** the open fails as `changed_during_observation`, and no byte outside the source is read

#### Scenario: A11 FIFO swapped in before a read
- **WHEN** a listed file is replaced by a FIFO before it is opened
- **THEN** the open returns without blocking, fails as `changed_during_observation`, and nothing is read

#### Scenario: A11 bind-mounted file
- **WHEN** a listed regular file is a bind mount of a file from another filesystem
- **THEN** it is not read and is recorded as a mount-boundary gap

### Requirement: Content is read in bounded chunks
A file SHALL be read in chunks no larger than the configured chunk size. Each chunk SHALL be one watched filesystem call, and the search SHALL stop between chunks when cancelled.

#### Scenario: Large file read in chunks
- **WHEN** a copy search reads a 64 MiB file with a 1 MiB chunk size
- **THEN** the recorded calls show 64 reads of at most 1 MiB, and cancelling during the read stops it before the next chunk
