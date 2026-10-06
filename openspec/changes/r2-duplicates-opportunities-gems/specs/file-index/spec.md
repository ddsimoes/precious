# Spec Delta

## ADDED Requirements

### Requirement: Hashing runs in the background
After a scan completes, and when the server starts, Precious SHALL hash every online source that has files whose content is not yet known, without the owner asking. At most one hashing job per source SHALL be active at a time, and hashing SHALL give way to scans and to interactive work on the same device (§7).

#### Scenario: Hashing follows a scan
- **WHEN** a scan of the regression corpus completes
- **THEN** a hashing job for that source starts without any request, and Home shows it with its progress

#### Scenario: A second request joins the running job
- **WHEN** `start-hash` is requested for a source whose hashing job is running
- **THEN** the response is 202 with that job's `job_id` and `coalesced` true, and no second job exists

#### Scenario: Offline source is not hashed
- **WHEN** `start-hash` is requested for a source whose volume is not mounted
- **THEN** the response is 409 `source_offline`, and its stored digests stay as they were

### Requirement: Only files that could have a copy are read
A file SHALL be read for hashing only when another present file or archive member in the index, on any source, has the same size and is not the same physical file. Zero-byte files SHALL never be read and are never duplicates. A file whose size no other file shares SHALL count as having no other copy without being read (§7).

#### Scenario: Unique sizes cost no reads
- **WHEN** a source holds 990 files of 990 distinct sizes and is hashed
- **THEN** no file is opened, and each of them reads as having no other copy

#### Scenario: Hard links are one file
- **WHEN** two entries are hard links to the same file and no other file has their size
- **THEN** neither is read, and neither is reported as a duplicate of the other

#### Scenario: A size shared across sources
- **WHEN** a file on source `fotos` has the same size as a file on source `usb`
- **THEN** both files are hashed

### Requirement: Large files are compared by samples first
A file of at least 16 MiB SHALL first be compared through the SHA-256 of three 64 KiB samples, at its start, middle, and end. It SHALL be read in full only when another file of its size has the same sample digest. Equal samples SHALL never by themselves make two files duplicates (§7).

#### Scenario: Different samples skip the full read
- **WHEN** two 1 GiB files of the same size differ in their first 64 KiB
- **THEN** each is read for at most 192 KiB, and both read as having no other copy

#### Scenario: Equal samples, different content
- **WHEN** two 32 MiB files have identical samples but differ elsewhere
- **THEN** both are read in full and are not in the same duplicate group

### Requirement: Largest files are hashed first
Hashing SHALL read the files that could have a copy from the largest to the smallest, except that files smaller than 1 MiB inside two folders that may be copies of each other SHALL be read before other files of that size class (§7).

#### Scenario: Size order
- **WHEN** a source holds candidates of 3 GB, 40 MB, and 200 KB, and none was hashed before
- **THEN** the 3 GB file is read before the 40 MB file, and the 40 MB file before the 200 KB file

#### Scenario: Small files of likely copies first
- **WHEN** after the large files, `Fotos` and `Fotos - Copia` share most of their sizes, and other small candidates exist elsewhere
- **THEN** the small files of `Fotos` and `Fotos - Copia` are hashed before the others

### Requirement: A digest needs a complete read of an unchanged file
A digest SHALL be set only after every byte of the file was read and its size, modification time, change time, and file identity, at open and at close, equal its indexed row. Otherwise the file SHALL get no digest and SHALL be counted as not checked, or as unreadable when it cannot be opened or read. Hashing SHALL never write to a source (I1).

#### Scenario: A file changing during hashing
- **WHEN** a file grows while it is being read for hashing
- **THEN** it gets no digest and is counted as not checked, and hashing continues with the next file

#### Scenario: Unreadable file
- **WHEN** a candidate file cannot be opened for lack of permission
- **THEN** it is counted as unreadable, and every duplicate or uniqueness claim that depends on it says it could not be checked

#### Scenario: Hashing on a read-only mount
- **WHEN** the regression corpus is on a read-only mount, and it is scanned and hashed with its archives listed
- **THEN** every entry's modification and access times, and the mount's contents, are unchanged afterward

### Requirement: Digests are kept until the file changes
A digest SHALL stay valid until a rescan finds that the file's size, times, or identity changed, and such a rescan SHALL discard its digest. A rescan that only rewrites the file's classification, or brings a missing file back with the same size, times, and identity, SHALL keep it. A file whose size, times, and identity are unchanged SHALL never be read again, across hashing runs and server restarts (§7).

#### Scenario: A second run reads nothing
- **WHEN** the corpus is hashed to completion and a second hashing job runs with no change on disk
- **THEN** the second job opens no file

#### Scenario: A restored modification time does not hide a change
- **WHEN** a hashed file's content is replaced with different bytes of the same size, its modification time is set back to the old value, and the source is rescanned and hashed
- **THEN** the file is read again and gets its new digest

### Requirement: Late hashing results never win
A digest SHALL be stored only if, inside the writing transaction, the entry still has the size, times, and identity that the read observed. Otherwise the result SHALL be dropped and the file stays not checked (I9).

#### Scenario: A rescan commits during a read
- **WHEN** a rescan updates a file's entry while hashing is reading that file
- **THEN** the digest from that read is not stored, and the file is hashed again by the next hashing run

### Requirement: Hashing coverage is published
Precious SHALL publish, for each source and for all sources together: the bytes and files that could have a copy, how many of them are checked, how many are not checked yet, and how many could not be read. A file counts as checked when it has a digest, or when its sample digest differs from every other file of its size (§7, I7).

#### Scenario: R2.1 Coverage is shown while hashing runs
- **WHEN** the corpus is being hashed
- **THEN** Home shows the bytes checked out of the bytes that could have a copy, growing as hashing proceeds, and the figure reaches 100% when the job ends with nothing unreadable

#### Scenario: Unreadable files are not counted as checked
- **WHEN** hashing ends and one candidate file could not be read
- **THEN** coverage is below 100%, and that file is counted as unreadable

### Requirement: Hashing can be cancelled and resumed without rereading
Cancelling a hashing job SHALL keep every digest already committed, and the next hashing job SHALL continue with the files not yet checked.

#### Scenario: Cancel and start again
- **WHEN** a hashing job is cancelled after hashing half of the candidates, and `start-hash` is requested again
- **THEN** the new job reads only the files that were not yet checked

### Requirement: Check now for chosen folders
`check-now` with one or two folder or archive IDs SHALL hash the files not yet checked inside them before any other hashing work on their devices, and return 202 with the started jobs. An ID on an offline source SHALL be refused with 409 `source_offline`, and an unknown ID with 404.

#### Scenario: Comparing during the first hashing run
- **WHEN** hashing of a large source has not reached the small files, and the owner asks to check `Fotos` and `Fotos - Copia` now
- **THEN** the files of those two folders are hashed next, and the rest of the source continues afterward

### Requirement: Hashing progress is published
While a hashing job runs, its progress SHALL report the files and bytes it has to check, the files and bytes checked so far, the bytes read, the archives listed, and the files that could not be read, through `GET /api/jobs/{id}` and `GET /api/events`.

#### Scenario: Progress grows during hashing
- **WHEN** a hashing job is running
- **THEN** successive reads of `GET /api/jobs/{id}` show the checked files and bytes growing

### Requirement: A modification time at the epoch is unknown
A modification time at or before 1970-01-01T00:00:00Z SHALL be treated as unknown. It SHALL NOT set a folder's newest or oldest time. The by-year breakdowns SHALL count its file under an unknown year, listed after the years, so their sums still equal the totals. The read API SHALL return no time for it, and the stored value SHALL be kept as the platform gave it. A rescan SHALL bring folders indexed before this rule up to date.

#### Scenario: A file stamped at the epoch
- **WHEN** a folder holds a photo from 2004 and a file whose modification time is 0
- **THEN** the folder's oldest time is in 2004, its by-year breakdown has 2004 and an unknown year, and the file's own time is shown as unknown

## MODIFIED Requirements

### Requirement: Rescans write only what changed
A rescan SHALL match stored entries to the walk by path within the source. An entry whose kind and size are equal, whose modification time is unchanged within the filesystem's tolerance, where the platform reports one, whose change time is unchanged within the same tolerance, and, where identity is stable, whose identity is equal, SHALL not be written. A changed entry SHALL be updated in place, keeping its ID, decision, and tags. An entry whose size, times, or identity changed SHALL also lose its digest and its archive listing. A new entry SHALL be inserted. A different kind at the same path SHALL replace the old entry and its subtree with a new entry.

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
- **THEN** a new folder entry with a new ID exists at that path, and the old file entry, with its decision and tags, is gone

#### Scenario: A changed change time updates the entry
- **WHEN** a hashed file keeps its size and modification time but its change time moves, and the source is rescanned
- **THEN** its entry is updated, keeping its ID, and it has no digest until hashing reads it again
