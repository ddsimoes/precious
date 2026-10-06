# Spec Delta

## Purpose

Finds copies by content: an owner-started copy search lists chosen folders, reads and hashes only files that could be copies, and reports which folders and large files already exist elsewhere in the search, with every coverage gap explicit.

## ADDED Requirements

### Requirement: A copy search is an explicit job
`find-copies` on a configured source, either the whole source or listed active directories of it (expanded, atomic, or the root), SHALL enqueue a cancellable copy search and return HTTP 202 with its job and search. One copy search per source SHALL be active at a time; a request while one is active SHALL return the active one marked as coalesced. No page, read, or other job SHALL start a search.

#### Scenario: Search a whole source
- **WHEN** the owner requests `find-copies` for source `disk` with no folders
- **THEN** the response is HTTP 202 with a job ID and a search ID whose scope is the source root

#### Scenario: Second request while one runs
- **WHEN** the owner requests `find-copies` for `disk` while a search of `disk` is running
- **THEN** the response is HTTP 202 with the running search's job and search IDs, marked as coalesced, and no second job exists

#### Scenario: Folder that cannot be searched
- **WHEN** the request lists a file node, an inactive node, a mount boundary, or a node of another source
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
Every write of a copy search SHALL check that the source epoch is unchanged since the search started. If the epoch changed, the search SHALL end as failed with `source_epoch_changed`, and its results SHALL be history only.

#### Scenario: Source replaced during a search
- **WHEN** the source's filesystem identity changes and is reconfirmed while a search runs
- **THEN** the search fails with `source_epoch_changed`, and the Copies page shows its results as history, not as current

### Requirement: Results show their freshness
Each result SHALL be `current`, `changed` (a cataloged entry under either folder changed or vanished, or the atomic unit holding a folder changed at its top level), `superseded` (a newer complete search of the source exists), or `source_changed` (the source epoch changed).

#### Scenario: Change after the search
- **WHEN** a scan records a new file in a cataloged folder named by a result
- **THEN** the result reads `changed`

#### Scenario: Deep change inside an atomic unit
- **WHEN** a file deep inside atomic `Backup` changes and nothing at `Backup`'s top level does
- **THEN** results naming folders inside `Backup` stay `current`, and the page states that changes inside atomic folders are seen only by searching again

### Requirement: Interrupted searches resume through the cache
A search interrupted by a restart SHALL list its folders again from the start. Every digest committed before the interruption SHALL be reused without a read.

#### Scenario: Restart during hashing
- **WHEN** the server restarts after a search has hashed 300 of 600 candidate files
- **THEN** the search lists again, reads only the 300 files not yet hashed, and completes with the same results
