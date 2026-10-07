# Spec Delta

## ADDED Requirements

### Requirement: Your files inside programs
The rescue card SHALL list the owner's own material (user-material indicators: a file, or a folder such as saved games) found inside program or disposable groups, one row per item, for one source or all. Each row SHALL name the outermost such group that holds the item and link to it. Its basis SHALL be rules (§11.4; ADR 0009 moves this list here from §11.7).

#### Scenario: R2.6 The spreadsheet inside Microsoft Office
- **WHEN** the corpus is scanned and hashed to completion and the owner opens the rescue card
- **THEN** it lists `Backup_PC_2004/C/Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls`, inside `Backup_PC_2004/C/Arquivos de programas/Microsoft Office`

#### Scenario: A file outside every group is not listed
- **WHEN** a spreadsheet sits in `Documentos`, outside any program or disposable group
- **THEN** the rescue card does not list it

## MODIFIED Requirements

### Requirement: Opportunity cards
Precious SHALL offer these opportunity cards, for one source or all sources (§11.4):
- the owner's own files inside program or disposable groups (rescue);
- exact duplicate folders and files;
- archives already unpacked elsewhere;
- system junk;
- old installers, disk images, and downloads;
- application installations and operating-system copies;
- caches, temporary data, and generated artifacts;
- partial downloads, empty folders, and zero-byte files.

Each card SHALL show its bytes, its row count, and its basis (rules or same content), ranked by bytes, except that the rescue card SHALL come first while it has open rows. A card whose open rows hold no bytes, and the rescue card, SHALL lead with its row count instead of its bytes.

#### Scenario: Cards on the corpus
- **WHEN** the corpus is scanned and hashed to completion and the owner opens Opportunities
- **THEN** the eight cards are listed with the rescue card first and the others largest first, and the system junk card is based on rules while the duplicates card is based on same content

#### Scenario: One source
- **WHEN** the owner switches Opportunities to one source
- **THEN** every card counts only that source's rows

#### Scenario: A card of empty files and folders
- **WHEN** every open row of the partial downloads, empty folders, and zero-byte files card holds no bytes
- **THEN** the card leads with its count of items, not with "0 B"

#### Scenario: The rescue card leads with its files
- **WHEN** the rescue card has one open row of 20 KiB
- **THEN** it is the first card and leads with "1 item", not with its bytes

#### Scenario: A rescue card with nothing open
- **WHEN** every row of the rescue card is decided
- **THEN** it is ranked by its bytes like the other cards, and reads "Nothing left to review"

### Requirement: Decided rows leave the list
A row SHALL be open while its entry's effective decision is undecided. A rescue row SHALL instead be open until the owner sets a decision on the file itself, or its effective decision is keep, so that a decision inherited from the group around it does not hide it. A duplicates row SHALL be open while at least two of its copies are undecided. The list SHALL let the owner also show the rows that are no longer open.

#### Scenario: Deciding a row shrinks the card
- **WHEN** the owner discards one row of the system junk list
- **THEN** that row leaves the list, and the card's bytes shrink by the row's bytes

#### Scenario: Showing decided rows
- **WHEN** the owner chooses to show decided rows
- **THEN** the discarded row is listed again with its decision

#### Scenario: Discarding a program does not hide the owner's file
- **WHEN** the owner discards `Backup_PC_2004/C/Arquivos de programas/Microsoft Office`
- **THEN** `OFFICE11/Meu orcamento casamento.xls` stays open on the rescue card, and reads an inherited discard

#### Scenario: Keeping the owner's file closes its row
- **WHEN** the owner keeps `OFFICE11/Meu orcamento casamento.xls`
- **THEN** its row leaves the rescue card's open rows, whatever the decision on `Microsoft Office`

## REMOVED Requirements

### Requirement: Gems
**Reason**: The system cannot tell what is valuable from metadata and copies. On the owner's 780 GiB archive, "unique" listed 48,000 files, much of them noise, and "only in a copy" repeated most of it (ADR 0009).
**Migration**: The detail panel says whether a file has another copy (R2.7). Search's duplicate filter lists files with no other copy, by category or folder. Compare and Similar folders show what is only on one side. The rescue list moves to the rescue card.
