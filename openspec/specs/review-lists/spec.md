# review-lists Specification

## Purpose
Turns the index, the rules, and the duplicates into focused lists that answer "what should I do first?", so that the owner reviews and decides in groups whenever they choose.

## Requirements

### Requirement: Opportunity cards
Precious SHALL offer these opportunity cards, for one source or all sources (§11.4):
- the owner's own files inside program or disposable groups (rescue);
- exact duplicate folders and files;
- archives already unpacked elsewhere;
- system junk;
- old installers and disk images, wherever they are: a downloads folder is not a row of its own, the installers inside it are (ADR 0010);
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

#### Scenario: A downloads folder is not an installer
- **WHEN** the corpus is scanned and the owner opens the installers card
- **THEN** it lists `Downloads/Setup.exe` and the corpus's disk images as rows, and no row is the folder `Downloads` itself

### Requirement: A card's bytes equal its review list
Each card SHALL open a review list whose rows are the card's open rows, and the card's bytes SHALL equal the sum of the bytes of those rows. No byte SHALL be counted twice in one card (§11.4).

#### Scenario: R2.5 Card bytes equal the sum of the list
- **WHEN** the corpus is scanned and hashed to completion
- **THEN** for every card, its bytes equal the sum of the bytes of every row of its review list, read through all of its pages

#### Scenario: Nested matches are one row
- **WHEN** `Backup_PC_2004/C/WINDOWS` is an operating-system copy and holds `system32`, which the rules also match
- **THEN** the programs card has one row for `Backup_PC_2004/C/WINDOWS`, and `system32` adds no bytes of its own

### Requirement: Review list rows
A row SHALL be the outermost entry that matches its card: a group, a folder, an archive, or a file. In the duplicates card, a row SHALL be a folder relation, or a duplicate group of files outside every listed relation, and its bytes are its redundant bytes. Each row SHALL show its size, dates, suggestion with its source, and a one-line summary of what it holds (§11.5). A row's file count SHALL count files as the Map does, without the members of archives. A row of the archives-already-unpacked card SHALL name the folder that holds the archive's content and open Compare on the two.

#### Scenario: Evidence summary
- **WHEN** the owner opens the programs card
- **THEN** the row for `Backup_PC_2004/C/Arquivos de programas/Microsoft Office` names its category, its years, its file count, its size, and that it holds a spreadsheet

#### Scenario: A duplicate group row
- **WHEN** the owner opens the duplicates card
- **THEN** one row lists `Downloads/Setup.exe` and `Downloads/Setup(1).exe` with each copy's path and decision, and its bytes are one copy's size

#### Scenario: Where an archive was unpacked
- **WHEN** the owner opens the archives-already-unpacked card
- **THEN** the row for `Downloads/eMule0.47c-Installer.zip` names `Downloads/emule-0.47c`, and its Compare link opens Compare on the zip and that folder

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

### Requirement: Review lists work from the keyboard
A review list SHALL let the owner keep, discard, or set later the selected row, and move to the next or previous row, from the keyboard alone, and SHALL ignore those keys while typing in a field or inside a dialog (§11.5). When a decision makes the selected row leave the list, the row that takes its place SHALL be selected. Moving to the next row past the last loaded row SHALL load the next page and select its first row.

#### Scenario: Deciding with the keyboard
- **WHEN** the owner selects the first row of the system junk list and presses the discard key twice
- **THEN** the first two rows are discarded, and the row that followed them is selected

#### Scenario: Moving past the loaded rows
- **WHEN** the owner presses the next-row key on the last loaded row of a list that has more rows
- **THEN** the next page is loaded and its first row is selected

### Requirement: Bulk decisions from a review list
Every review list except duplicates SHALL let the owner select all of its open rows. The confirmation SHALL show the count, the bytes, and the kept entries, and the decision SHALL skip every kept entry and report it, like any bulk decision (§11.5, I5).

#### Scenario: Discarding all system junk
- **WHEN** the owner selects all rows of the system junk list, one of which is inside a kept folder, and confirms discard
- **THEN** every other row is discarded, and the result lists the kept one as skipped

#### Scenario: No select-all in duplicates
- **WHEN** the owner opens the duplicates list
- **THEN** there is no control to select all rows, and copies are decided one by one or by choosing them

### Requirement: Similar folders are listed
Opportunities SHALL offer a list of the folders and archives related as `overlap`.
- **Row content.** Each row SHALL show both sides and the bytes they have in common, with a link that opens Compare on the two. It SHALL NOT count the files found only on each side: Compare counts them (ADR 0010).
- **Order and scope.** The list SHALL be ordered by bytes in common, largest first, and SHALL follow the chosen source.
- **Read-only.** It SHALL carry no decision controls and no card bytes, because a similar folder is not a copy (§6.4).

#### Scenario: Similar folders on the corpus
- **WHEN** the corpus is hashed to completion and the owner opens the similar folders list
- **THEN** the `Fotos - Copia`/`Fotos` relation is listed with its bytes in common and no count of files only on either side, and its Compare link opens Compare on the two

### Requirement: Cards show what was decided
Each opportunity card and review list header SHALL show, besides its open rows, the count and bytes of its rows that are no longer open. A card with no open row left SHALL say so instead of showing zero.

#### Scenario: Progress after deciding
- **WHEN** the owner discards 20 rows of the partial downloads, empty folders, and zero-byte files list, holding 4.4 GB
- **THEN** the card shows the open rows left, and that 20 rows holding 4.4 GB were decided

### Requirement: Your files inside programs
The rescue card SHALL list the owner's own material (user-material indicators: a file, or a folder such as saved games) found inside program or disposable groups, one row per item, for one source or all. Each row SHALL name the outermost such group that holds the item and link to it. Its basis SHALL be rules (§11.4; ADR 0009 moves this list here from §11.7).

#### Scenario: R2.6 The spreadsheet inside Microsoft Office
- **WHEN** the corpus is scanned and hashed to completion and the owner opens the rescue card
- **THEN** it lists `Backup_PC_2004/C/Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls`, inside `Backup_PC_2004/C/Arquivos de programas/Microsoft Office`

#### Scenario: A file outside every group is not listed
- **WHEN** a spreadsheet sits in `Documentos`, outside any program or disposable group
- **THEN** the rescue card does not list it
