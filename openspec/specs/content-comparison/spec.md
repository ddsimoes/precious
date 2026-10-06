# content-comparison Specification

## Purpose

Finds copies by content: an owner-started copy search lists chosen folders, reads and hashes only files that could be copies, and reports which folders and large files already exist elsewhere in the search, with every coverage gap explicit.

## Requirements

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

### Requirement: The search snapshot is not inventory
A copy search SHALL list every entry under its folders, inside atomic units too, into a snapshot of its own. It SHALL create no inventory node, change no inventory mode, and add nothing to explorer listings, totals, or review queues.

#### Scenario: Search inside an atomic unit
- **WHEN** a copy search covers the atomic unit `Backup` holding 10,000 nested entries
- **THEN** the search lists 10,000 entries, `Backup` stays atomic with zero active descendants, and the total node count is unchanged

### Requirement: Listing respects the filesystem boundary
A copy search SHALL never follow a symbolic link, open a special file, or cross a mount boundary. Each symbolic link SHALL be recorded with its link text. Each special file, mount boundary, unreadable directory, and failed listing SHALL be recorded as a gap of its folder.

#### Scenario: Gaps recorded, nothing crossed
- **WHEN** a searched folder holds a symlink to `/etc`, a FIFO, a nested mount, and an unreadable directory
- **THEN** no call opens or lists through any of them, and the folder shows one link and three gaps by kind

### Requirement: Files with a unique size are never read
A non-empty regular file whose size no other regular file in the search shares SHALL be recorded as distinct without being opened. Empty files SHALL never be opened.

#### Scenario: Unique sizes cost no reads
- **WHEN** a search lists 1,000 files of which 990 have sizes no other file shares
- **THEN** the recorded calls open none of those 990 files, and each is reported as having no other copy in this search

### Requirement: Samples only rule matches out
Files of at least the sample threshold that share a size SHALL first be compared by three samples, at the start, middle, and end of the file. Files whose samples differ SHALL be distinct without a full read. Equal samples SHALL never establish a match.

#### Scenario: A10 sampled-hash collision is not a copy
- **WHEN** two 32 MiB files have identical samples but differ in one byte between them
- **THEN** both files are read in full, their digests differ, neither is reported as a copy of the other, and no folder holding one is reported inside the other's folder

#### Scenario: Different samples skip the full read
- **WHEN** two 1 GiB files of equal size differ in their first sample
- **THEN** neither is read beyond its samples, and both are reported as distinct

### Requirement: Content identity needs a complete hash
Two files SHALL be reported as having the same content only when both were read completely in this search or by an unchanged earlier read, and their sizes and SHA-256 digests are equal. Metadata or name equality SHALL never establish a match.

#### Scenario: Equal names and sizes are not enough
- **WHEN** `a/IMG_1.JPG` and `b/IMG_1.JPG` are 3 MB each, with equal sizes and modification times but different bytes
- **THEN** both are read in full, and they are reported as different

### Requirement: Unstable files are blocked
Before reading, a file SHALL be confirmed to be the regular file the listing saw (device, inode, size, modification and change time), and after reading its metadata SHALL be unchanged and every byte read. Otherwise it SHALL be `unstable`: it gets no digest, nothing is cached for it, and it counts as a gap.

#### Scenario: A10 file changing during a check is blocked
- **WHEN** a file is rewritten while it is being read
- **THEN** it is reported `unstable` with no digest, no folder holding it is reported fully inside another, and the next search reads it again

#### Scenario: A10 file changed since listing is blocked
- **WHEN** a file's size and change time differ between the listing and the open
- **THEN** it is reported `unstable` and is not read

### Requirement: Large files first, small files only where copies are likely
Same-size files at or above the large-file threshold SHALL be checked first, largest first. A smaller same-size file SHALL be read only when its folder already overlaps another searched folder by at least half its bytes, counting unread small files by size alone. Every other small same-size file SHALL be `not_checked`, which counts as a gap.

#### Scenario: Copied folder of small files is checked
- **WHEN** `code` and `code-copy` hold the same 2,000 files of 4 KiB each, and `notes` holds 2,000 unrelated 4 KiB files
- **THEN** the files of `code` and `code-copy` are read, `code-copy` is reported inside `code`, and the files of `notes` are not read and are counted as not checked

### Requirement: Digests are reused while files are unchanged
A file whose source epoch, device, inode, size, modification time, and change time equal those of an earlier complete read SHALL take that read's digest without being opened.

#### Scenario: Repeating a search reads only changes
- **WHEN** a search runs again after one of 500 checked files was rewritten
- **THEN** only the rewritten file is read, and the results match a search from scratch

#### Scenario: Restored modification time does not hide a change
- **WHEN** a file is rewritten with its size and modification time restored afterwards
- **THEN** its change time differs, so it is read again

### Requirement: Hard links are one file
Paths sharing a device and inode SHALL be read at most once per search. A match between two such paths SHALL count toward `inside`, but SHALL free no bytes.

#### Scenario: Hard-linked copy frees nothing
- **WHEN** every file in `b` is a hard link to a file in `a`
- **THEN** `b` is reported inside `a` with zero bytes that a removal would free

### Requirement: Folder relations
A folder SHALL be `inside` another folder that is neither its ancestor nor its descendant when every non-empty regular file under it has a file of the same content under the other, symbolic links match by link text, and it has no gap. Two folders each inside the other SHALL be the `same`. Otherwise a folder whose matched bytes reach 10% of its bytes SHALL `overlap` the other by that share.

#### Scenario: Renamed and rearranged photos are found inside
- **WHEN** `fotos-b/2014/11/dsc02434.jpg` and the rest of `fotos-b` exist with identical bytes as `fotos/2014/21-11-2014/DSC02434.JPG` and siblings, plus 30% more files in `fotos`
- **THEN** `fotos-b` is reported inside `fotos`, and `fotos` overlaps `fotos-b` by its matched share

#### Scenario: A gap prevents an inside claim
- **WHEN** every readable file of `old` has a copy in `new`, but one file of `old` is unreadable
- **THEN** `old` is not reported inside `new`; it overlaps `new` with its matched share and one gap

#### Scenario: Two identical folders
- **WHEN** `x` and `y` hold the same files under different names
- **THEN** one result reports `x` and `y` as the same

### Requirement: Results are ranked and not repeated per subfolder
Folder results SHALL be ordered by the bytes a removal of the reported folder would free, largest first. A folder SHALL NOT be reported when its parent's result already names the same other folder or one containing it. Folder results whose matched bytes are below the configured minimum SHALL be omitted.

#### Scenario: One line per copy
- **WHEN** `fotos-b` and each of its 120 subfolders are inside `fotos`
- **THEN** exactly one folder result names `fotos-b`

#### Scenario: Two copies of one original
- **WHEN** `fotos-b` and `fotos-reorg2` hold the same photos in different layouts, and `fotos` holds them and more
- **THEN** `fotos-b` and `fotos-reorg2` are each reported inside `fotos`, they are not paired with each other, and no pair of their subfolders is reported

### Requirement: Large files with copies are listed
A file at or above the configured minimum that has a copy elsewhere in the search, and that no reported folder result explains, SHALL be reported as a file result naming one other copy.

#### Scenario: A loose duplicate archive
- **WHEN** `Downloads/setup.iso` and `Backup/old/setup.iso` are identical 4 GB files in folders that otherwise differ
- **THEN** one file result names both, with 4 GB that a removal would free

### Requirement: Coverage is explicit
Each search SHALL report entries listed, files read, bytes read, and gaps by kind: mount boundary, unreadable, special file, failed listing, unstable, not checked, and budget stop. No result or page SHALL claim that a file or folder is the only copy on a disk; a file without a match SHALL be described as having no other copy in this search.

#### Scenario: Budget stop
- **WHEN** a search reaches its entry budget
- **THEN** it ends `partial`, the unlisted folders are gaps, and no folder with an unlisted part is reported inside another

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
