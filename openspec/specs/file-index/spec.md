# file-index Specification

## Purpose
Keeps a full metadata index of every file and folder of a source, with folder aggregates, kept current by rescans. R2 extends it with content hashing.

## Requirements

### Requirement: A scan indexes every entry
A scan SHALL record every entry below the source root (folders, files, symlinks, and special files) with its raw name, kind, size, allocated size where the filesystem reports it, modification time, change time where the platform has one, the file identity the platform gives, extension, and file kind (§6.2). A scan SHALL read no file content, SHALL never follow a symlink, open a special file, or cross a mount boundary.

#### Scenario: R1.1 Every entry is indexed and folder totals add up
- **WHEN** the regression corpus is added as a source and scanned to completion
- **THEN** every path listed in the corpus's ground truth is an entry of the source with the listed kind
- **AND** every folder's `total_bytes` equals the sum of the sizes of the files in its subtree, and its `total_files` equals their count

#### Scenario: Symlinks and special files are recorded, never followed or opened
- **WHEN** a source contains a symlink to a folder outside the source and a FIFO
- **THEN** both are entries, with kinds `symlink` and `special`
- **AND** nothing below the symlink's target is indexed, and the FIFO is never opened

#### Scenario: A mount boundary is recorded without children
- **WHEN** another filesystem is mounted on a folder inside the source
- **THEN** that folder is an entry with `mount_boundary` true and no children

#### Scenario: No file content is read
- **WHEN** a source is scanned
- **THEN** no file below the source root is opened for reading

### Requirement: Folder aggregates are computed in the same scan
Every folder SHALL carry, over its whole subtree, total bytes, file and folder counts, bytes and counts by file kind and by modification year (UTC), its newest and oldest modification times, and its main file kind. They SHALL be computed in the same scan that lists the folder, with no second pass, and be updated by every rescan (§6.3). A file's total bytes SHALL be its size and its file count 1.

#### Scenario: Breakdowns by kind and year
- **WHEN** a folder holds three JPEG files modified in 2004 and a subfolder holding one PDF modified in 2010, and the source is scanned
- **THEN** `GET /api/entries/{id}` of the folder returns `stats.by_kind` with image 3 files and document 1 file, and `stats.by_year` with 2004 and 2010, each with its bytes
- **AND** the folder's `newest` is the PDF's modification time and its `oldest` is the oldest JPEG's

#### Scenario: Totals of an empty folder
- **WHEN** a source contains an empty folder
- **THEN** its `total_bytes` and `total_files` are 0 and it is not `partial`

### Requirement: Unreadable folders are explicit
A folder whose listing fails SHALL be recorded with state `unreadable`, and each of its ancestors SHALL be marked `partial` and count it in its unreadable total. An unreadable folder SHALL never be presented as an empty folder.

#### Scenario: The corpus's unreadable folder
- **WHEN** the regression corpus, whose unreadable folder cannot be listed by the server's user, is scanned
- **THEN** that folder's `state` is `unreadable`
- **AND** every ancestor up to the source root has `partial` true and `stats.unreadable` of at least 1
- **AND** the scan still completes and indexes every other entry

### Requirement: Rescans write only what changed
A rescan SHALL match stored entries to the walk by path within the source. An entry whose kind and size are equal and whose modification time is unchanged within the filesystem's resolution SHALL not be written. A changed entry SHALL be updated in place, keeping its ID, decision, and tags, and a new entry SHALL be inserted. A different kind at the same path SHALL be a new entry, and the old one becomes missing.

#### Scenario: R1.6 A rescan updates sizes and keeps decisions and tags
- **WHEN** the regression corpus is scanned, the owner sets decisions and tags on files and folders, then files are added, one file's size changes, and some decided or tagged files are deleted, and the source is rescanned
- **THEN** the changed file and its ancestor folders show the new sizes and totals, and the added files are present
- **AND** the deleted entries have state `missing`
- **AND** every decision and tag set before the rescan is still on its entry, missing entries included

#### Scenario: Unchanged rescan keeps every ID
- **WHEN** a source is rescanned with no change on disk
- **THEN** no entry is inserted and every entry keeps its ID

#### Scenario: Kind change at the same path
- **WHEN** a file `notes` is replaced by a folder `notes` and the source is rescanned
- **THEN** the old file entry is `missing` and a new folder entry with a new ID exists at that path

### Requirement: Missing entries are kept
An entry not found in a complete listing of its parent SHALL become `missing` with the time it went missing, together with its subtree, keeping its ID, decision, and tags, and SHALL no longer count in its ancestors' totals. A folder whose listing failed SHALL keep its stored children as they were. An entry found again at its path SHALL become `present` with the same ID.

#### Scenario: Deleted folder goes missing with its subtree
- **WHEN** an indexed folder with files is deleted and the source is rescanned
- **THEN** the folder and every entry below it are `missing` with a missing-since time
- **AND** the parent's totals no longer include their bytes

#### Scenario: A failed listing never makes children missing
- **WHEN** an indexed folder becomes unreadable and the source is rescanned
- **THEN** the folder is `unreadable` and its stored children keep their previous state

#### Scenario: A returning entry keeps its ID
- **WHEN** a file that was `missing` appears again at the same path and the source is rescanned
- **THEN** the entry with the same ID is `present` again, with its decision and tags

### Requirement: Times are compared within the filesystem's resolution
A modification time SHALL be unchanged when it differs from the stored one by no more than the filesystem's time resolution. On a filesystem with local-time stamps (FAT), a difference of one hour, plus or minus the resolution, SHALL also count as unchanged (a daylight-saving shift). Any other difference SHALL count as a change.

#### Scenario: R1.17 FAT rescan with no changes creates no entries
- **WHEN** a source on the FAT-capability fixture (no stable identity, 2-second local-time stamps) is scanned and then rescanned with no change
- **THEN** every entry is matched by path, size, and modification time within 2 seconds
- **AND** the rescan creates no new entries and marks none missing

#### Scenario: Daylight-saving shift on FAT
- **WHEN** every modification time on a FAT source reads one hour later than at the last scan, with no other change, and the source is rescanned
- **THEN** no entry is reported changed

#### Scenario: Real edit within the hour
- **WHEN** a file on a FAT source keeps its size but its modification time moves by 20 minutes
- **THEN** the rescan updates the entry as changed

### Requirement: Interrupted scans restart from the root
A scan interrupted by a cancel, a crash, or a restart SHALL keep the entries it already wrote. Its next attempt SHALL walk again from the root, never resuming mid-tree. Folders whose subtree an interrupted scan did not finish SHALL keep their previous totals and be marked `partial` until a scan completes them.

#### Scenario: Cancelled scan, then a complete one
- **WHEN** a scan is cancelled midway and a new scan is started
- **THEN** after the cancel, the entries written so far remain and the unfinished folders' ancestors are `partial`
- **AND** after the new scan completes, every entry is indexed and no folder is `partial` unless it holds an unreadable folder

### Requirement: Scans need an online source and never overlap
`POST /api/commands/start-scan` with `{"source_id"}` SHALL return 202 `{"job_id","state","coalesced"}`. While a scan of the source is not terminal, a new request SHALL return that scan with `coalesced` true instead of starting another. A source that is not `online` SHALL be refused with 409 `source_offline`, and an unknown source with 404.

#### Scenario: Second request coalesces
- **WHEN** `start-scan` is requested twice for the same source while the first scan is running
- **THEN** both responses carry the same `job_id`, and the second has `coalesced` true

#### Scenario: Offline source is not scanned
- **WHEN** `start-scan` is requested for a source whose volume is not mounted
- **THEN** the response is 409 `source_offline` and no job is created

### Requirement: Indexing cost is bounded
A scan, including its database writes, SHALL take at most 1.5 times as long as a bare metadata walk that only stats each entry of the same tree on the same machine (§7). R1 SHALL check the bound warm first, with both runs after one priming walk; if the warm bound fails, a cold measurement decides and both numbers are recorded.

#### Scenario: R1.14 Scan within 1.5x of a bare walk
- **WHEN** on the reference server, the owner's archive is walked once to prime the cache, then timed with a bare metadata walk and with a scan into a scratch database
- **THEN** the scan's time is at most 1.5 times the bare walk's time
- **AND** if it is not, a cold run of both decides, and both measurements are recorded

### Requirement: Scan progress is published
While a scan runs, its job progress SHALL report `phase` (1 walking, 2 finishing), `dirs`, `files`, `bytes`, `written`, `unreadable`, and `missing`, readable through `GET /api/jobs/{id}` and streamed through `GET /api/events`.

#### Scenario: Progress grows during a scan
- **WHEN** a scan of a large source is running
- **THEN** successive reads of `GET /api/jobs/{id}` show `dirs`, `files`, and `bytes` growing, and `phase` 1 until the walk ends
