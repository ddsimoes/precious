# duplicates Specification

## Purpose
Finds what is a copy of what across every source: duplicate files, folders and archives that hold the same content, and the files unique to each side of two folders. Every claim it makes states how much of the content was checked.

## Requirements

### Requirement: Files with the same digest form a duplicate group
Present files and archive members with the same SHA-256 SHALL form a duplicate group, across every source, offline sources included. Hard links to one file SHALL count as one copy. A group's redundant bytes SHALL be its size times the number of copies minus one (§6.4).

#### Scenario: R2.1 Every duplicate group in the corpus is found
- **WHEN** the regression corpus is scanned and hashed to completion
- **THEN** the duplicate groups equal those listed in the corpus's ground truth, including `Downloads/Setup.exe` with `Downloads/Setup(1).exe`, every copy of `curriculo.doc`, and the photos inside `Downloads/fotos_2005_do_pendrive.zip` with their unpacked copies

#### Scenario: A copy on an offline source
- **WHEN** a photo's only other copy is on a source whose volume is not mounted
- **THEN** the photo is in a duplicate group with that copy, and the copy is marked offline

#### Scenario: Hard links are not redundant
- **WHEN** two entries are hard links to one file, and a third file has the same content
- **THEN** the group has two copies and its redundant bytes equal one file's size

### Requirement: Folder relations are computed from content
Precious SHALL relate folders and archives by their files' digests, ignoring names and layout: `same` when each side's content all exists in the other, `inside` when one side's content all exists in the other, and `overlap` when at least half of one side's bytes match the other. Each relation SHALL record its matched bytes and the files and bytes found only on each side (§6.4).

#### Scenario: R2.3 A zip and its unpacked folder are the same
- **WHEN** the corpus is hashed to completion
- **THEN** `Downloads/fotos_2005_do_pendrive.zip` and `Downloads/fotos_2005_do_pendrive` are related as `same`, and so are `Downloads/eMule0.47c-Installer.zip` and `Downloads/emule-0.47c`

#### Scenario: Renamed and rearranged copies
- **WHEN** a folder holds the same photos as another under different names and subfolders
- **THEN** the two folders are related as `same`

#### Scenario: A copied program folder is the same as the original
- **WHEN** `HD antigo/backup pc velho/Arquivos de programas/Winamp` holds a copy of every file of `Backup_PC_2004/C/Arquivos de programas/Winamp`, and nothing else
- **THEN** the two folders are related as `same`, and the relation lists no file only on one side

### Requirement: Unchecked content blocks containment claims
A folder that holds a file not yet checked, or one that could not be read, SHALL NOT be claimed `same` as or `inside` another folder. Its relations SHALL wait until that file is checked, and an unreadable file keeps blocking them (I7).

#### Scenario: Gap prevents an inside claim
- **WHEN** every file of folder A has a copy in folder B except one file of A that could not be read
- **THEN** A is not related to B as `inside` or `same`

### Requirement: One relation per copy
When a folder is related to another, its subfolders that are related to the other's matching subfolders SHALL NOT be listed again. Each copy SHALL appear once, at its highest related folder, and the files of a folder relation SHALL NOT be listed again as duplicate files of that relation.

#### Scenario: Nested copies are listed once
- **WHEN** `Fotos - Copia` overlaps `Fotos`, and their subfolders `2004` are the same
- **THEN** the relations list holds the `Fotos - Copia`/`Fotos` relation and the `2004` relation, and none for the folders inside `2004`

### Requirement: Relations follow hashing
Relations and folder duplication figures SHALL be recomputed from the index as hashing advances, at most every configured interval while a hashing job runs, when it ends, and after each scan. They SHALL never use a separate copy of the index (§7).

#### Scenario: Relations appear as hashing advances
- **WHEN** a long hashing job checks the files of two copied folders and keeps running
- **THEN** their relation appears within one interval, before the job ends

### Requirement: Folders show how much of them is duplicated
Every folder SHALL carry its duplicated bytes, the bytes of the files in its subtree that have a copy anywhere, and the share of its candidate bytes that was checked. Its percent duplicated is duplicated bytes over total bytes (§6.3, §11.2). A file that could not be read SHALL NOT count among a folder's candidate bytes: a folder whose only files not checked are unreadable counts as checked, while coverage still counts those files as unreadable (I7).

#### Scenario: Percent duplicated of a copied folder
- **WHEN** `Fotos - Copia` holds 597 KB, and all of its photos have a copy in `Fotos` except one photo of 20 KB
- **THEN** `Fotos - Copia` shows about 97% duplicated, and all of its candidate bytes as checked

#### Scenario: A folder whose only unchecked file is unreadable
- **WHEN** every file of a folder is checked except one that could not be read
- **THEN** the folder's percent duplicated is shown as final rather than "so far", and Home still counts that file as could not be read

### Requirement: Uniqueness claims state the checked share
Every "no other copy" or "duplicate" claim SHALL be shown together with the share of the content that could have a copy that was checked, and a file not yet checked SHALL be shown as not checked, never as unique (I7).

#### Scenario: R2.7 No other copy names the checked share
- **WHEN** hashing has checked 80% of the bytes that could have a copy, and the owner opens a photo whose size no other file shares
- **THEN** the panel says it has no other copy, and that 80% of the content that could have a copy was checked

#### Scenario: Not checked is not unique
- **WHEN** a photo's size is shared and the photo has not been hashed yet
- **THEN** it is shown as not checked yet, and a search for files with no other copy does not list it

### Requirement: Two folders can be compared
Compare SHALL take two folders or archives, where neither contains the other, and list their files in five groups: only on the left, only on the right, identical, same relative path with different content, and not checked yet. A file whose size does not occur on the other side SHALL count as only on its side without being read (§11.6). Compare opened without a chosen group SHALL open the first group that holds files, in this order: only on the left, only on the right, different content, not checked yet, identical.

#### Scenario: R2.2 Fotos - Copia against Fotos
- **WHEN** the owner compares `Fotos` with `Fotos - Copia` after hashing
- **THEN** only on the right lists `2006/Praia/DSC_editada.JPG`, only on the left lists the three photos missing from the copy, identical lists every other photo, and nothing is not checked

#### Scenario: A zip against its unpacked folder
- **WHEN** the owner compares `Downloads/fotos_2005_do_pendrive.zip` with `Fotos/2005`
- **THEN** every member is identical to a file of `Fotos/2005`, and no file is only on either side

#### Scenario: Same name, different content
- **WHEN** the owner compares `Projetos/site_antigo` with `Projetos/site_antigo_copia`
- **THEN** `contato.php` is listed as same path with different content, and every other file as identical

#### Scenario: Compare opens where the files are
- **WHEN** the owner opens Compare from a relation where everything on the left is also on the right
- **THEN** Compare opens on only on the right, or on identical when the right holds nothing more

#### Scenario: A folder against its own subfolder
- **WHEN** a Compare request names `Fotos` and `Fotos/2005`
- **THEN** the response is 400 `invalid_request`

### Requirement: Compare shows where each copy is
Each Compare item SHALL show the path of its file on each side, inside that side, whenever the two differ. An identical item with a file on one side only SHALL say that it is an extra copy of content found on both sides, and SHALL name the file on the other side that holds that content.

#### Scenario: Copies filed under different folders
- **WHEN** the owner compares `fotos-b` with `fotos`, and `fotos-b/2002/12/img_0001.jpg` is identical to `fotos/2014/celular/IMG_0001.jpg`
- **THEN** the item shows `2002/12/img_0001.jpg` on the left and `2014/celular/IMG_0001.jpg` on the right

#### Scenario: An extra copy on one side
- **WHEN** the right side holds two copies of a photo that the left side holds once
- **THEN** the identical group lists the pair, and lists the second right copy as an extra copy of the same content, naming the left file

### Requirement: Compare alone counts what is only on one side
Files found only on one side of a relation SHALL be counted on screen by Compare alone, in its buckets: only on the left, only on the right, and same name with different content. The detail panel's relations SHALL show each relation's kind and the bytes in common, with a link that opens Compare on the two, and SHALL NOT show a count of files only on either side. The relation SHALL still record those counts (§6.4), and the read API SHALL still serve them (ADR 0010).

#### Scenario: A folder with a similar copy
- **WHEN** the owner selects `Fotos - Copia` after hashing
- **THEN** the panel shows its relation to `Fotos`, with the bytes in common and a link that opens Compare on the two, and shows no count of files only on either side
- **AND** Compare on the two counts the files only on each side, `2006/Praia/DSC_editada.JPG` among those only on the right

### Requirement: A quarantined copy is not a copy elsewhere
Copies inside a source's quarantine folder SHALL count for nothing. They are not listed among a file's copies, not counted in duplicate groups or relations, and not used for the duplicated share (§10.6). A file whose only other copy was quarantined SHALL read as having no other copy once relations are computed again.

#### Scenario: Quarantining one of two copies
- **WHEN** one of two identical files is quarantined and relations are computed again
- **THEN** the other file reads "No other copy", and the duplicates card no longer lists the pair

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
