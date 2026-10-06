# Spec Delta

## Purpose

Reads zip and tar-family archives in memory, within safety budgets, so that their members can be browsed, viewed, and compared as read-only entries without anything being extracted to disk.

## ADDED Requirements

### Requirement: Supported archives are read in memory
Precious SHALL open zip (stored and deflate), tar, tar.gz, tar.bz2, and single-file gzip and bzip2 archives, recognized by their signature, and SHALL read them only in memory: no member is extracted to disk, not even to a temporary file (§6.4, ADR 0007). Other formats, and archives inside archives, SHALL stay unopened and be treated as plain files.

#### Scenario: A zip is listed without extraction
- **WHEN** the corpus's `Downloads/fotos_2005_do_pendrive.zip` is hashed and browsed
- **THEN** its members are listed with their names, sizes, and dates, and no file appears in the state directory, the temporary directory, or the source

#### Scenario: A tar.gz is listed in one pass
- **WHEN** a 64 MiB `.tar.gz` with 1,000 members is listed
- **THEN** it is read once from start to end, and every member is listed and hashed in that pass

#### Scenario: An archive inside an archive stays closed
- **WHEN** a zip holds another zip
- **THEN** the inner zip is a member hashed as a file, and its own members are not listed

#### Scenario: Other formats are plain files
- **WHEN** a source holds a `.7z`, a `.rar`, a `.docx`, and a `.jar`
- **THEN** none is opened as an archive, and each is hashed as a plain file when its size is shared

### Requirement: Archive reading is bounded
Reading one archive SHALL stop at the first of these budgets: a number of members, a number of unpacked bytes, a ratio of unpacked to packed bytes, and a time limit. An archive stopped by a budget SHALL be shown as partly read, with which budget it reached, and SHALL get no members.

#### Scenario: A zip bomb stops early
- **WHEN** a 1 MiB zip unpacks to 1 GiB of zeros
- **THEN** reading stops before 128 MiB are unpacked, and the archive is shown as partly read because it unpacks to too much

#### Scenario: Too many members
- **WHEN** an archive holds more members than the configured maximum
- **THEN** the archive is shown as partly read, and none of its members is listed

### Requirement: Unsafe or damaged archives are reported, not trusted
An archive SHALL get members only when it was read completely and correctly. An archive with an absolute member path, a `..` component, two members at one path, or a member that is both a file and a folder SHALL be rejected, naming that member. An encrypted archive, an unsupported compression method, and a failed checksum SHALL each be reported as such. Member names SHALL be kept as raw bytes (I6).

#### Scenario: Path traversal rejected
- **WHEN** a tar holds a member named `../../etc/passwd`
- **THEN** the archive is shown as rejected, naming that member, and it has no members

#### Scenario: Encrypted zip
- **WHEN** a zip's members are encrypted
- **THEN** the archive is shown as encrypted, and it is hashed as a plain file when its size is shared

#### Scenario: Damaged archive
- **WHEN** a `.tar.gz` is cut off in the middle
- **THEN** the archive is shown as damaged, and it has no members

### Requirement: Members are read-only entries
Each member of a completely read archive SHALL be an entry that the owner can open in the Map, the detail panel, and the viewer, with its name, path inside the archive, kind, size, modification time, and, for a member folder, its total bytes and files. A member SHALL carry no decision or tags of its own and is never written to. Symlink members SHALL keep their link text without being followed.

#### Scenario: Browsing inside a zip
- **WHEN** the owner opens `Downloads/eMule0.47c-Installer.zip` in the Map
- **THEN** the table and the treemap show its top-level members, and choosing a member folder shows its contents

#### Scenario: A symlink member
- **WHEN** a tar holds a symlink member pointing to `/etc`
- **THEN** the member is shown as a link with the text `/etc`, and nothing outside the archive is read

### Requirement: Archive listings are kept until the archive changes
An archive's listing and its members' digests SHALL be kept until a rescan updates the archive's entry. An unchanged archive SHALL never be read again to list it.

#### Scenario: An unchanged archive is not re-read
- **WHEN** the corpus is hashed twice with no change on disk
- **THEN** the second hashing job opens no archive

#### Scenario: A changed archive is read again
- **WHEN** a member is added to a listed zip and the source is rescanned and hashed
- **THEN** the zip is listed again, and the new member appears
