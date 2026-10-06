# Spec Delta

## MODIFIED Requirements

### Requirement: Observation never mutates sources
Source access SHALL issue no operation that creates, writes, renames, removes, or changes permissions, ownership, or timestamps of any source entry. It SHALL never execute any source file. File content SHALL be read only by a copy search, and only from regular files. Content SHALL never be stored, logged, sent anywhere, or written to any disk; only sizes and SHA-256 digests are kept, plus, for archives a search opened, each member's path, kind, size, modification time, and link text.

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

#### Scenario: Archives are unpacked in memory only
- **WHEN** a copy search opens a `.zip` and a `.tar.gz` on a read-only mount
- **THEN** it completes, the recorded calls contain no mutating operation, and the process creates no file outside its database

### Requirement: Content is read in bounded chunks
A file, including an archive being unpacked, SHALL be read in chunks no larger than the configured chunk size. Each chunk SHALL be one watched filesystem call, and the search SHALL stop between chunks when cancelled.

#### Scenario: Large file read in chunks
- **WHEN** a copy search reads a 64 MiB file with a 1 MiB chunk size
- **THEN** the recorded calls show 64 reads of at most 1 MiB, and cancelling during the read stops it before the next chunk

#### Scenario: Archive read in chunks
- **WHEN** a copy search opens a 64 MiB `.tar.gz` with a 1 MiB chunk size
- **THEN** every recorded read is at most 1 MiB, and cancelling during the read stops it before the next chunk, with nothing cached for that archive
