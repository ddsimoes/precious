# Spec Delta

## Purpose

Defines the only way curator may touch source filesystems: rooted, read-only, bounded access that never follows symlinks, never opens special files, never crosses mounts, never mutates, and preserves filename bytes exactly.

## ADDED Requirements

### Requirement: Rooted access cannot escape a source
Every source access SHALL resolve inside that source's configured root through a rooted handle. Names containing `..`, absolute paths, and symlinks pointing outside the root SHALL never reach an object outside the root. A plain path-prefix check SHALL NOT be the only safeguard.

#### Scenario: A11 symlink escape
- **WHEN** a source contains `escape -> /etc` and discovery reaches `escape`
- **THEN** `escape` is recorded as a symlink with its link text, and nothing under `/etc` is opened, listed, or stat'ed

#### Scenario: A11 traversal name
- **WHEN** a lookup is requested for a name containing `../`
- **THEN** it is rejected without accessing any path outside the source root

### Requirement: Symlinks are never followed
Discovery SHALL record a symbolic link as a `symlink` node with its link text as observed data. It SHALL never open, list, or stat the link's target, even when the target is inside the same source.

#### Scenario: Internal symlink not followed
- **WHEN** a source contains `alias -> ./Projects`
- **THEN** `alias` is a symlink node, no child nodes are created beneath it, and its link text reads `./Projects`

### Requirement: Swapped entries are detected, not followed
When an entry observed as a directory has a different identity once opened (for example, it was replaced by a symlink or another directory between observation and open), the access SHALL fail as `changed_during_observation`. Nothing read through the new object SHALL be recorded.

#### Scenario: A11 directory swapped for symlink
- **WHEN** a directory is replaced by a symlink to `/` after it was listed but before it is probed
- **THEN** the probe records a `changed_during_observation` error for that entry and reads nothing through the symlink

### Requirement: Special files are never opened
FIFOs, sockets, character devices, block devices, and other non-regular, non-directory, non-symlink entries SHALL never be opened or read. Each SHALL be recorded as a visible issue on its parent's listing, with its lossless name and observed type.

#### Scenario: A11 FIFO in a source
- **WHEN** a source directory contains a FIFO named `pipe`
- **THEN** discovery completes without blocking, `pipe` appears as a special-file issue on the parent, and it is never opened

### Requirement: Nested mounts are not crossed
A directory on a different device from its parent, or listed as a mount point in the process mount table (including same-device bind mounts), SHALL be recorded as a `mount_boundary` directory. It SHALL never be listed or probed.

#### Scenario: A11 nested mount
- **WHEN** a filesystem is mounted at `<root>/external` inside a source
- **THEN** `external` is recorded as a mount boundary, and no entries of the mounted filesystem are observed

#### Scenario: Bind mount detected
- **WHEN** `<root>/again` is a same-device bind mount of another directory
- **THEN** `again` is recorded as a mount boundary and is not probed

### Requirement: Observation never mutates sources
Source access SHALL issue no operation that creates, writes, renames, removes, or changes permissions, ownership, or timestamps of any source entry. It SHALL never execute any source file. In this release, file content SHALL never be read.

#### Scenario: A20 zero mutating calls
- **WHEN** a full M1 discovery runs against an instrumented filesystem
- **THEN** the recorded calls contain no mutating operation and zero bytes of file content read

#### Scenario: A20 read-only mount works
- **WHEN** a source root is on a read-only mount
- **THEN** discovery completes with no write-related errors

### Requirement: Bounded directory reads
Directory listings SHALL be read in batches no larger than the configured batch size, and each batch SHALL be processed before the next is requested. A listing SHALL never require loading the entire directory into memory, and SHALL stop as soon as its budget is reached.

#### Scenario: Huge directory read in batches
- **WHEN** discovery lists a root directory with 200,000 immediate entries
- **THEN** entries are requested and persisted in bounded batches, and peak memory does not grow with the directory's total size

### Requirement: Access failures are classified
Each failed lookup or listing SHALL record a distinct outcome: `absent` (no such entry), `unreadable` (permission denied), `unavailable` (I/O error, stale or disconnected mount, or missing source), or `changed_during_observation`. No failure SHALL be recorded as an empty or absent result unless its outcome is `absent`.

#### Scenario: A5 unreadable directory
- **WHEN** a child directory has mode `0000` and is probed
- **THEN** its probe records `unreadable`, and it is never reported as empty

### Requirement: Filename bytes are preserved exactly
Entry names SHALL be preserved as the exact bytes returned by the operating system, with no case folding, Unicode normalization, or re-encoding. Identity comparisons SHALL use those bytes.

#### Scenario: A17 non-UTF-8 name
- **WHEN** a source contains a file whose name is the bytes `0x66 0xE9 0x2E 0x74 0x78 0x74` (Latin-1 `fé.txt`)
- **THEN** the stored name is exactly those six bytes

#### Scenario: A17 normalization-distinct names
- **WHEN** a directory contains two entries named `é` in NFC and NFD forms
- **THEN** both are recorded as distinct entries
