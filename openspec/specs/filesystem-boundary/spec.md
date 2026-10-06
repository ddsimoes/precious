# filesystem-boundary Specification

## Purpose

Defines the only way curator may touch source filesystems: rooted, read-only, bounded access that never follows symlinks, never opens special files, never crosses mounts, never mutates, and preserves filename bytes exactly.

## Requirements

### Requirement: Rooted access cannot escape a source
Every source access SHALL resolve inside that source's root, the folder located by its volume identity and its path inside that volume (§6.1), through a rooted handle. Names containing `..`, absolute paths, and symlinks pointing outside the root SHALL never reach an object outside the root. A plain path-prefix check SHALL NOT be the only safeguard.

#### Scenario: A11 symlink escape
- **WHEN** a source contains `escape -> /etc` and a scan reaches `escape`
- **THEN** `escape` is recorded as a symlink with its link text, and nothing under `/etc` is opened, listed, or stat'ed

#### Scenario: A11 traversal name
- **WHEN** a lookup is requested for a name containing `../`
- **THEN** it is rejected without accessing any path outside the source root

#### Scenario: Root follows the volume to its current mount
- **WHEN** a source's volume is mounted at a different mount point than when the source was added, and a scan runs
- **THEN** every access resolves inside the source's folder on that volume at its current mount point, and nothing outside it is opened, listed, or stat'ed

### Requirement: Symlinks are never followed
A scan SHALL record a symbolic link as an entry of kind `symlink`, with its link text as observed data. It SHALL never open, list, or stat the link's target, even when the target is inside the same source.

#### Scenario: Internal symlink not followed
- **WHEN** a source contains `alias -> ./Projects` and is scanned
- **THEN** `alias` is an entry of kind `symlink` whose link text reads `./Projects`, and no entry is recorded beneath it

### Requirement: Swapped entries are detected, not followed
When an entry observed as a directory has a different identity once opened (for example, it was replaced by a symlink or another directory between observation and open), the access SHALL fail as `changed_during_observation`. Nothing read through the new object SHALL be recorded.

#### Scenario: A11 directory swapped for symlink
- **WHEN** a directory is replaced by a symlink to `/` after it was listed but before it is probed
- **THEN** the probe records a `changed_during_observation` error for that entry and reads nothing through the symlink

### Requirement: Special files are never opened
FIFOs, sockets, character devices, block devices, and other non-regular, non-directory, non-symlink entries SHALL never be opened or read. A scan SHALL record each as an entry of kind `special`, with its lossless name and its special kind (`fifo`, `socket`, `char`, or `block`).

#### Scenario: A11 FIFO in a source
- **WHEN** a source directory contains a FIFO named `pipe` and is scanned
- **THEN** the scan completes without blocking, `pipe` is an entry of kind `special` with special kind `fifo`, and it is never opened

### Requirement: Nested mounts are not crossed
A directory on a different device from its parent, or listed as a mount point in the platform's mount table (including same-device bind mounts), SHALL be recorded as a directory entry with `mount_boundary` set and no children. It SHALL never be listed.

#### Scenario: A11 nested mount
- **WHEN** a filesystem is mounted at `<root>/external` inside a source and the source is scanned
- **THEN** `external` is a directory entry with `mount_boundary` set and no children, and no entries of the mounted filesystem are observed

#### Scenario: Bind mount detected
- **WHEN** `<root>/again` is a same-device bind mount of another directory and the source is scanned
- **THEN** `again` is recorded as a mount boundary and is never listed

### Requirement: Bounded directory reads
Directory listings SHALL be read in batches no larger than `[scan] list_batch`, and each batch SHALL be processed before the next is requested. A listing SHALL never require loading the entire directory into memory.

#### Scenario: Huge directory read in batches
- **WHEN** a scan lists a directory with 200,000 immediate entries
- **THEN** entries are requested in batches of at most `[scan] list_batch` and persisted in bounded batches, and peak memory does not grow with the directory's total size

### Requirement: Access failures are classified
Each failed lookup or listing SHALL record a distinct outcome: `absent` (no such entry), `unreadable` (permission denied), `unavailable` (I/O error, stale or disconnected mount, or missing source), or `changed_during_observation`. No failure SHALL be recorded as an empty or absent result unless its outcome is `absent`.

#### Scenario: A5 unreadable directory
- **WHEN** a child directory has mode `0000` and is probed
- **THEN** its probe records `unreadable`, and it is never reported as empty

### Requirement: Filename bytes are preserved exactly
Entry names SHALL be preserved exactly as the platform gives them: bytes on Linux and macOS, and UTF-16 on Windows, including sequences that are not valid text such as unpaired surrogates. They SHALL undergo no case folding, Unicode normalization, or re-encoding, and identity comparisons SHALL use the preserved name. Names SHALL be displayed with invisible or invalid parts shown through a reversible escape (§5 I6).

#### Scenario: R1.8 Non-UTF-8 names round-trip losslessly
- **WHEN** a source contains a file whose name is the bytes `0x66 0xE9 0x2E 0x74 0x78 0x74` (Latin-1 `fé.txt`) and is scanned
- **THEN** the API returns that entry with `name_b64` decoding to exactly those six bytes and `name` displayed as `f\xE9.txt`
- **AND** a search finds it, and `GET /api/entries/{id}` with the returned ID answers with the same `name_b64`

#### Scenario: A17 non-UTF-8 name
- **WHEN** a source contains a file whose name is the bytes `0x66 0xE9 0x2E 0x74 0x78 0x74` (Latin-1 `fé.txt`)
- **THEN** the stored name is exactly those six bytes

#### Scenario: A17 normalization-distinct names
- **WHEN** a directory contains two entries named `é` in NFC and NFD forms
- **THEN** both are recorded as distinct entries

#### Scenario: Unpaired surrogate on Windows
- **WHEN** a source on Windows contains a file whose UTF-16 name holds an unpaired surrogate
- **THEN** the name is preserved with that surrogate intact, and it is displayed with the surrogate escaped

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
The viewer SHALL read a file in chunks no larger than a fixed bound. Each chunk SHALL be one watched filesystem call, and reading SHALL stop between chunks when the request is cancelled.

#### Scenario: Large file read in chunks
- **WHEN** the viewer serves a 64 MiB file
- **THEN** every recorded read is at most the chunk bound, and each read is one watched filesystem call

#### Scenario: Cancelled view stops reading
- **WHEN** the browser abandons the request while the viewer is serving a large file
- **THEN** reading stops before the next chunk

#### Scenario: Archive read in chunks
- **WHEN** the viewer serves a 64 MiB `.tar.gz`
- **THEN** it is offered as a download without being unpacked, every recorded read is at most the chunk bound, and abandoning the request stops reading before the next chunk

### Requirement: Observation never writes to sources
Scanning, rescanning, the folder picker, availability refresh, and the viewer SHALL issue no operation that creates, writes, renames, removes, or changes permissions, ownership, or timestamps of any source entry, and SHALL never execute any source file (§5 I1). File content SHALL be read only by the viewer, only from regular files, and only through identity-checked reads. Content SHALL never be stored, logged, or written to any disk.

#### Scenario: R1.9 A scan never writes to the source
- **WHEN** a source root is on a read-only mount, and a scan and then a rescan of it run against the instrumented filesystem
- **THEN** both complete with no write-related errors, and the recorded calls contain no create, write, rename, remove, permission, ownership, or timestamp change

#### Scenario: Only the viewer reads content
- **WHEN** a scan, a rescan, the picker, and an availability refresh run against an instrumented filesystem
- **THEN** none of them opens a regular file for reading or reads any content

#### Scenario: Viewer on a read-only mount
- **WHEN** the viewer serves a file from a source on a read-only mount
- **THEN** it completes, the recorded calls contain no mutating operation, and the process writes no copy of the content to any disk

### Requirement: Filesystem capabilities are detected
Precious SHALL detect and record each source's filesystem capabilities (§6.1) when the source is added and on each availability refresh: `known`, `read_only`, `case_sensitive`, `normalization_sensitive`, `stable_identity`, `local_time`, `hard_links`, and `time_resolution_ns`. On Linux they SHALL follow the filesystem type; an unrecognized type SHALL get the conservative unknown set with `known: false`.

#### Scenario: Case-sensitive filesystems with stable identity
- **WHEN** a source is on ext2, ext3, ext4, xfs, btrfs, zfs, f2fs, or tmpfs
- **THEN** its capabilities report `known: true`, `case_sensitive: true`, `stable_identity: true`, `local_time: false`, and `time_resolution_ns: 1`

#### Scenario: FAT capabilities
- **WHEN** a source is on vfat
- **THEN** its capabilities report `known: true`, `case_sensitive: false`, `stable_identity: false`, `local_time: true`, and `time_resolution_ns: 2000000000`

#### Scenario: exFAT capabilities
- **WHEN** a source is on exfat
- **THEN** its capabilities report `known: true`, `case_sensitive: false`, `stable_identity: false`, `local_time: false`, and `time_resolution_ns: 10000000`

#### Scenario: NTFS treated as case-insensitive
- **WHEN** a source is on ntfs, ntfs3, or an NTFS filesystem mounted through fuseblk
- **THEN** its capabilities report `known: true`, `case_sensitive: false`, `stable_identity: true`, `local_time: false`, and `time_resolution_ns: 100`

#### Scenario: Optical filesystems are always read-only
- **WHEN** a source is on iso9660 or udf
- **THEN** its capabilities report `known: true`, `read_only: true`, `case_sensitive: true`, `stable_identity: true`, and `time_resolution_ns: 1000000000`

#### Scenario: Read-only follows the mount
- **WHEN** a source is on an ext4 filesystem mounted read-only
- **THEN** its capabilities report `read_only: true`, and the same filesystem mounted read-write reports `read_only: false`

#### Scenario: Unknown filesystem type
- **WHEN** a source is on a filesystem type outside the recognized list
- **THEN** its capabilities report `known: false`, `case_sensitive: false`, `stable_identity: false`, and `time_resolution_ns: 2000000000`

### Requirement: Comparisons follow capabilities
Wherever Precious compares two names or two times of entries on one source, it SHALL apply that source's recorded capabilities. Times SHALL be equal when they differ by no more than the time resolution, or, on a local-time filesystem, by one hour plus or minus that resolution. On a case-insensitive filesystem, names that differ only in letter case SHALL compare as equal; stored names stay exactly as given.

#### Scenario: Time within resolution is unchanged
- **WHEN** a rescan of a vfat source finds a file with the stored size whose modification time differs from the stored one by 1 second
- **THEN** the entry is treated as unchanged and is not rewritten

#### Scenario: Daylight-saving shift on a local-time filesystem
- **WHEN** a rescan of a vfat source finds a file with the stored size whose modification time differs from the stored one by exactly one hour
- **THEN** the entry is treated as unchanged

#### Scenario: Fine resolution detects small changes
- **WHEN** a rescan of an ext4 source finds a file with the stored size whose modification time differs from the stored one by 1 microsecond
- **THEN** the entry is treated as changed and its row is updated

#### Scenario: Case-insensitive name equality
- **WHEN** Precious compares the names `Fotos` and `FOTOS` on a source whose capabilities report `case_sensitive: false`
- **THEN** they compare as equal, and each entry keeps its name exactly as listed
