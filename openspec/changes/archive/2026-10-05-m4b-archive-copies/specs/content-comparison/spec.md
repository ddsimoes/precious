# Spec Delta

## MODIFIED Requirements

### Requirement: A copy search is an explicit job
`find-copies` on one or more configured sources, either whole sources or listed active directories of them (expanded, atomic, or the root), SHALL enqueue a cancellable copy search and return HTTP 202 with its job and search. One copy search per source SHALL be active at a time; a request naming a source that an active search covers SHALL return that search marked as coalesced. No page, read, or other job SHALL start a search.

#### Scenario: Search a whole source
- **WHEN** the owner requests `find-copies` for source `disk` with no folders
- **THEN** the response is HTTP 202 with a job ID and a search ID whose scope is the source root

#### Scenario: Search two sources
- **WHEN** the owner requests `find-copies` for sources `disk` and `old-disk`
- **THEN** the response is HTTP 202 with one job and one search whose scope is both source roots

#### Scenario: Second request while one runs
- **WHEN** the owner requests `find-copies` for `disk` while a search of `disk` and `old-disk` is running
- **THEN** the response is HTTP 202 with the running search's job and search IDs, marked as coalesced, and no second job exists

#### Scenario: Folder that cannot be searched
- **WHEN** the request lists a file node, an inactive node, a mount boundary, or a node of an unconfigured source
- **THEN** the response is HTTP 409 with code `invalid_node_state` naming that node, and no job is created

### Requirement: Results bind to the source epoch
Every write of a copy search SHALL check that the epoch of each source it covers is unchanged since the search started. If an epoch changed, the search SHALL end as failed with `source_epoch_changed`, and its results SHALL be history only.

#### Scenario: Source replaced during a search
- **WHEN** the source's filesystem identity changes and is reconfirmed while a search runs
- **THEN** the search fails with `source_epoch_changed`, and the Copies page shows its results as history, not as current

#### Scenario: One of two sources replaced
- **WHEN** `old-disk` is replaced and reconfirmed while a search of `disk` and `old-disk` runs
- **THEN** the search fails with `source_epoch_changed`

### Requirement: Results show their freshness
Each result SHALL be `current`, `changed` (a cataloged entry under either folder changed or vanished, an archive on either side changed, or the atomic unit holding a folder changed at its top level), `superseded` (a newer complete search of one of its sources exists), or `source_changed` (the epoch of a source on either side changed).

#### Scenario: Change after the search
- **WHEN** a scan records a new file in a cataloged folder named by a result
- **THEN** the result reads `changed`

#### Scenario: Deep change inside an atomic unit
- **WHEN** a file deep inside atomic `Backup` changes and nothing at `Backup`'s top level does
- **THEN** results naming folders inside `Backup` stay `current`, and the page states that changes inside atomic folders are seen only by searching again

#### Scenario: Archive rewritten after the search
- **WHEN** a scan records a new size for the cataloged archive `bkp.tar.gz` that a result names
- **THEN** the result reads `changed`

#### Scenario: Newer search of one source
- **WHEN** a search of `disk` completes after a search of `disk` and `old-disk`
- **THEN** every result of the older search reads `superseded`

### Requirement: Interrupted searches resume through the cache
A search interrupted by a restart SHALL list its folders again from the start. Every digest committed before the interruption SHALL be reused without a read. An archive whose reading was interrupted SHALL be read again from its start, and every archive whose opening completed before the interruption SHALL be reused without a read.

#### Scenario: Restart during hashing
- **WHEN** the server restarts after a search has hashed 300 of 600 candidate files
- **THEN** the search lists again, reads only the 300 files not yet hashed, and completes with the same results

#### Scenario: M4b-8 Restart during an archive's read
- **WHEN** the server restarts while a search reads the third of four `.tar.gz` archives
- **THEN** the search reads the third and fourth archives from their start, does not read the first two, and completes with the same results as without the restart

## ADDED Requirements

### Requirement: Supported archives are opened
A copy search SHALL open each file whose name ends in `.zip`, `.tar`, `.tar.gz`, `.tgz`, `.tar.bz2`, `.tbz`, `.tbz2`, `.gz`, or `.bz2`, optionally followed by `.old`, `.bak`, or `.orig`, when its first bytes match that format. A file named as a 7z, rar, xz, or zstd archive SHALL be counted as not opened, without a read. Any other file, and a file inside an archive, SHALL never be opened as an archive.

#### Scenario: Formats opened and not opened
- **WHEN** a search covers `a.zip`, `b.tar.gz`, `c.tgz.old`, `d.tar.bz2`, `e.sql.gz`, `f.7z`, `g.rar`, `h.jar`, and `i.docx`
- **THEN** the first five are opened, `f.7z` and `g.rar` are counted as not opened and are not read for that, and `h.jar` and `i.docx` are compared as plain files

#### Scenario: Name says zip, bytes do not
- **WHEN** `photos.zip` holds bytes that are not a zip archive
- **THEN** it is counted `unsupported` and is compared as a plain file

#### Scenario: Archive inside an archive
- **WHEN** `bkp.tar.gz` holds the member `old/site.zip`
- **THEN** `old/site.zip` is compared as one member by its unpacked bytes and is never opened

### Requirement: Archive members are compared by unpacked content
Two members, or a member and a file, SHALL be reported as having the same content only when both were read completely and their sizes and SHA-256 digests of the unpacked bytes are equal. Member names, sizes, timestamps, and stored checksums SHALL never establish a match. A member whose unpacked bytes disagree with its stored size or checksum SHALL make its archive `corrupt`.

#### Scenario: M4b-3 Name, size, and CRC are not enough
- **WHEN** the zip member `docs/plan.txt` has the name, size, and CRC-32 of the file `docs/plan.txt` in a folder, but different bytes
- **THEN** the two are reported as different, and the zip is not reported inside the folder

#### Scenario: Checksum mismatch
- **WHEN** a zip member's unpacked bytes do not match its stored CRC-32
- **THEN** the zip is reported `corrupt`, and no claim rests on its members

### Requirement: Archives are opened under budgets
Opening one archive SHALL stop when it reaches the configured budget of entries, of unpacked bytes, of unpacked bytes per packed byte read, or of time. The archive SHALL then be `partial`, and it SHALL be compared as a plain file.

#### Scenario: M4b-4 Zip bomb stops at the ratio budget
- **WHEN** a 1 MiB zip holds one member that unpacks to 1 GiB of zeros, with the ratio budget at 100
- **THEN** unpacking stops before 128 MiB has been produced, the zip is reported `partial` with the ratio budget named, and it is compared as a plain file

#### Scenario: Entry budget
- **WHEN** a zip's directory declares more entries than the entry budget
- **THEN** no member is listed, the zip is reported `partial` with the entry budget named, and it is compared as a plain file

### Requirement: Unsafe and damaged archives are not compared by contents
An archive SHALL be `rejected` when a member path is absolute, has a `..` component, or equals another member's path after removing `.` and empty components. An archive that is encrypted in any member SHALL be `encrypted`; one that cannot be read to its end SHALL be `corrupt`; one using an unsupported variant SHALL be `unsupported`. Each SHALL be compared as a plain file and SHALL report its first offending member.

#### Scenario: M4b-4 Unsafe and damaged archives
- **WHEN** a search covers a zip with the member `../../etc/passwd`, a zip with two members named `a/b.txt` and `a//b.txt`, an encrypted zip, a `.tar.gz` cut off in the middle, and a zip using LZMA compression
- **THEN** they are reported `rejected`, `rejected`, `encrypted`, `corrupt`, and `unsupported`, each naming the first offending member, and no folder result rests on their members

### Requirement: Zip members are read only when they could be copies
A zip's member list SHALL be read from its central directory. A zip member SHALL be unpacked only under the rules for files: its size is shared by another file or member in the search, large members first, and small members only inside likely copies. Unread members SHALL count as files with no other copy in this search or as not checked, by the same rules as files.

#### Scenario: Unique-size zip members cost no reads
- **WHEN** a 2 GB zip holds 500 members whose sizes no other file or member in the search shares
- **THEN** the recorded reads cover the zip's directory and none of those members' data

### Requirement: Streamed archives are read once
A tar, gzip, or bzip2 archive SHALL be read from start to end once per opening, and every member SHALL be hashed in that pass. When the pass reads the whole file without a change, the digest of the archive file itself SHALL be kept from the same pass.

#### Scenario: One pass over a large backup
- **WHEN** a search opens a 64 MiB `.tar.gz` holding 1,000 members
- **THEN** the recorded reads cover the archive's bytes exactly once, and every member and the archive file have digests

### Requirement: Opened archives are reused while unchanged
An archive whose source epoch, device, inode, size, modification time, and change time equal those of an earlier complete opening SHALL take that opening's member list and digests without being read. A changed archive SHALL be opened again.

#### Scenario: M4b-5 Repeated search skips an unchanged archive
- **WHEN** a search runs again after one of three opened archives was rewritten
- **THEN** only the rewritten archive is read, and the results match a search from scratch

### Requirement: Archives relate as folders
An opened archive SHALL count as a folder holding its members for `inside`, `same`, and `overlap`, against folders and other archives. In a `same` pair of an archive and a folder, the archive SHALL be the copy. A member at or above the reporting minimum SHALL be listed as a file result by the rule for large files.

#### Scenario: M4b-1 Backup archive next to its unpacked copy
- **WHEN** `bkp.tar.gz` holds exactly the files of the folder `bkp`, under the same or different names
- **THEN** one result reports `bkp.tar.gz` and `bkp` as the same, with `bkp.tar.gz` as the copy

#### Scenario: M4b-2 Older snapshot inside a newer one
- **WHEN** every member of `snap-2019.tar.gz` also occurs in `snap-2021.tar.gz`, which holds more
- **THEN** `snap-2019.tar.gz` is reported inside `snap-2021.tar.gz`

#### Scenario: M4b-2 Zip of some photos inside the photo folder
- **WHEN** every member of `fotos-2013.zip` has a copy under `fotos`, which holds more
- **THEN** `fotos-2013.zip` is reported inside `fotos`

#### Scenario: Folder inside an archive
- **WHEN** every file of the folder `old-site` occurs in `site-backup.zip`, which holds more
- **THEN** `old-site` is reported inside `site-backup.zip`

### Requirement: Freeable bytes of archive sides
Removing an archive SHALL count as freeing its size on disk when every member is matched, and nothing otherwise. A folder or file inside an archive SHALL free nothing. Matched and total bytes SHALL count members by their unpacked size.

#### Scenario: Archive frees its packed size
- **WHEN** a 40 GB `bkp.tar.gz` unpacks to 60 GB and is the same as `bkp`
- **THEN** the result shows 60 GB matched and 40 GB freeable

#### Scenario: Part of an archive frees nothing
- **WHEN** the folder `home/fotos` inside `bkp.tar.gz` is inside `fotos`, and the rest of the archive is not
- **THEN** a result for `home/fotos` inside `bkp.tar.gz` shows 0 bytes freeable

### Requirement: Searches across sources
A search covering several sources SHALL compare every file and member it lists against all of them, as within one source. A side's source SHALL be part of its result. Hard links SHALL be recognized across sources on one device.

#### Scenario: M4b-7 Folder inside a folder on another source
- **WHEN** a search covers sources `disk` and `old-disk`, and every file of `old-disk:/bkp` occurs under `disk:/fotos`
- **THEN** `old-disk:/bkp` is reported inside `disk:/fotos`, naming both sources
