# Spec Delta

## ADDED Requirements

### Requirement: Each file has a place relative to its source
A present file outside the quarantine SHALL have one place relative to its own source:
- `only_copy`: unique by size or sample, or hashed with no other physical copy;
- `this_source`: hashed, with other physical copies all on its source;
- `other_source`: hashed, with a physical copy on another source;
- `unchecked`: not checked yet, including a file not yet planned for checking;
- `unreadable`: could not be read;
- `none`: empty, a link, or a special file.

Copies SHALL count as in duplicate groups.

#### Scenario: Only copy by size
- **WHEN** a photo's size occurs in no other file on any source
- **THEN** its place is only copy, and it was not read

#### Scenario: Copies on the same source
- **WHEN** a hashed document has a second copy in another folder of its source and no copy on another source
- **THEN** the place of both copies is this source

#### Scenario: A copy inside an archive on the same source
- **WHEN** a file's only other copy is a member of a complete zip on its source
- **THEN** its place is this source

#### Scenario: A hard link is not a copy
- **WHEN** a file's only other path is a hard link to it, on a source with stable file identity
- **THEN** its place is only copy

#### Scenario: Hard-linked archive names are one archive
- **WHEN** a complete zip has two hard-linked names, and one of its members exists nowhere else
- **THEN** that member's place is only copy under either name

#### Scenario: A copy on an unconnected source
- **WHEN** a file's only other copy is on a source that is offline or unavailable
- **THEN** its place is other source, and it is marked as having no copy on a connected source

#### Scenario: A quarantined copy is no copy
- **WHEN** a file's only other copy is in its source's quarantine folder
- **THEN** its place is only copy

#### Scenario: R2.7 Not checked is not an only copy
- **WHEN** a photo's size is shared and it has not been hashed yet, or it was just scanned and not yet planned for checking
- **THEN** its place is not checked, never only copy

#### Scenario: Archive members have places
- **WHEN** a member of a complete zip has its only other copy loose on another source
- **THEN** the member's place is other source, its source being the zip's

### Requirement: A complete archive is placed by its members
A completely listed archive SHALL split its bytes among places in proportion to its members' bytes per place. A copy of the archive file on another source SHALL make every member count as other source. A copy only on its own source SHALL make a member that would be an only copy count as this source. Any other archive SHALL be placed as a file.

#### Scenario: A backup beside its folder
- **WHEN** a complete tar.gz holds exactly the files of a folder beside it on the same source, and neither has a copy elsewhere
- **THEN** the archive's place is this source, and so is every file of the folder

#### Scenario: The same archive on two sources
- **WHEN** the same zip file is on two sources and its members exist nowhere else
- **THEN** each zip's place is other source, its members included

#### Scenario: One unique member
- **WHEN** a complete 10 MB zip unpacks to 9 MB of members with copies on another source and a 1 MB member with no other copy
- **THEN** the zip's place is only copy, with about 1 MB of its bytes as only copy and 9 MB as other source

#### Scenario: A large archive
- **WHEN** a complete 5 GiB tar holds 6 GiB of members, all only copies
- **THEN** all its 5 GiB count as only copy, and relations keep being computed

#### Scenario: An archive not completely listed
- **WHEN** an encrypted zip's bytes have no other copy
- **THEN** its place is only copy, as for any file

### Requirement: Folders carry their places
Every folder SHALL carry its subtree's files and bytes per place, recomputed whenever its duplicated figures are. A folder's place SHALL be the first of only copy, this source, unchecked, unreadable, and other source that holds a file not counted apart as disposable. It SHALL be `none` when no such file has content. A folder not read completely, or with an unreadable entry or a mount boundary below it, SHALL NOT be placed other source or none (I7).

#### Scenario: Figures equal their files
- **WHEN** the corpus and a second source holding copies of some of its files are hashed and their relations computed
- **THEN** every folder's files and bytes per place equal the sum over the files in its subtree, each complete archive counting as one file whose bytes are split by its members' places

#### Scenario: A folder with one only copy
- **WHEN** every file of a folder has a copy on another source except one 3 KB photo with no other copy
- **THEN** the folder's place is only copy, with 3 KB as only copy

#### Scenario: A folder wholly elsewhere
- **WHEN** every file of a folder is hashed and has a copy on another source
- **THEN** the folder's place is other source

#### Scenario: A folder not read completely
- **WHEN** a folder could not be read completely, and every file that was read has a copy on another source
- **THEN** its place is unreadable, not other source

#### Scenario: An unchanged index writes nothing
- **WHEN** relations are computed twice with no change to the index in between
- **THEN** the second computation writes no folder figures

### Requirement: Disposable files are counted apart
A file or archive member whose family is disposable, and whose triage is not review, SHALL count in a folder's or archive's disposable figure instead of its places. It SHALL NOT decide that folder's or archive's place. Its own place SHALL still be computed.

#### Scenario: System files beside copied photos
- **WHEN** every photo of a folder has a copy on another source, and the folder also holds a `Thumbs.db` that exists nowhere else
- **THEN** the folder's place is other source, its disposable figure holds the `Thumbs.db`, and the `Thumbs.db` itself is an only copy

#### Scenario: A recovered chunk still counts
- **WHEN** a folder holds a recovered file chunk that the rules ask to review, and it exists nowhere else
- **THEN** the folder's place is only copy

#### Scenario: System files inside an archive
- **WHEN** a complete zip holds photos that all have a copy on another source, and a `Thumbs.db` that exists nowhere else
- **THEN** the zip's place is other source, and its `Thumbs.db` counts as disposable
