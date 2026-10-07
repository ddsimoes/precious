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
