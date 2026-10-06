# Spec Delta

## Purpose

Turns the index, the rules, and the duplicates into focused lists that answer "what should I do first?" and "what is valuable?", so that the owner reviews and decides in groups whenever they choose.

## ADDED Requirements

### Requirement: Opportunity cards
Precious SHALL offer these opportunity cards, for one source or all sources (§11.4):
- exact duplicate folders and files;
- archives already unpacked elsewhere;
- system junk;
- old installers, disk images, and downloads;
- application installations and operating-system copies;
- caches, temporary data, and generated artifacts;
- partial downloads, empty folders, and zero-byte files.

Each card SHALL show its bytes, its row count, and its basis (rules or same content), ranked by bytes.

#### Scenario: Cards on the corpus
- **WHEN** the corpus is scanned and hashed to completion and the owner opens Opportunities
- **THEN** the seven cards are listed largest first, and the system junk card is based on rules while the duplicates card is based on same content

#### Scenario: One source
- **WHEN** the owner switches Opportunities to one source
- **THEN** every card counts only that source's rows

### Requirement: A card's bytes equal its review list
Each card SHALL open a review list whose rows are the card's open rows, and the card's bytes SHALL equal the sum of the bytes of those rows. No byte SHALL be counted twice in one card (§11.4).

#### Scenario: R2.5 Card bytes equal the sum of the list
- **WHEN** the corpus is scanned and hashed to completion
- **THEN** for every card, its bytes equal the sum of the bytes of every row of its review list, read through all of its pages

#### Scenario: Nested matches are one row
- **WHEN** `Backup_PC_2004/C/WINDOWS` is an operating-system copy and holds `system32`, which the rules also match
- **THEN** the programs card has one row for `Backup_PC_2004/C/WINDOWS`, and `system32` adds no bytes of its own

### Requirement: Review list rows
A row SHALL be the outermost entry that matches its card: a group, a folder, an archive, or a file. In the duplicates card, a row SHALL be a folder relation, or a duplicate group of files outside every listed relation, and its bytes are its redundant bytes. Each row SHALL show its size, dates, suggestion with its source, and a one-line summary of what it holds (§11.5).

#### Scenario: Evidence summary
- **WHEN** the owner opens the programs card
- **THEN** the row for `Backup_PC_2004/C/Arquivos de programas/Microsoft Office` names its category, its years, its file count, its size, and that it holds a spreadsheet

#### Scenario: A duplicate group row
- **WHEN** the owner opens the duplicates card
- **THEN** one row lists `Downloads/Setup.exe` and `Downloads/Setup(1).exe` with each copy's path and decision, and its bytes are one copy's size

### Requirement: Decided rows leave the list
A row SHALL be open while its entry's effective decision is undecided. A duplicates row SHALL be open while at least two of its copies are undecided. The list SHALL let the owner also show the rows that are no longer open.

#### Scenario: Deciding a row shrinks the card
- **WHEN** the owner discards one row of the system junk list
- **THEN** that row leaves the list, and the card's bytes shrink by the row's bytes

#### Scenario: Showing decided rows
- **WHEN** the owner chooses to show decided rows
- **THEN** the discarded row is listed again with its decision

### Requirement: Review lists work from the keyboard
A review list SHALL let the owner keep, discard, or set later the selected row, and move to the next or previous row, from the keyboard alone, and SHALL ignore those keys while typing in a field or inside a dialog (§11.5).

#### Scenario: Deciding with the keyboard
- **WHEN** the owner selects the first row of the system junk list and presses the discard key, then the next-row key
- **THEN** the first row is discarded and the following row is selected

### Requirement: Bulk decisions from a review list
Every review list except duplicates SHALL let the owner select all of its open rows. The confirmation SHALL show the count, the bytes, and the kept entries, and the decision SHALL skip every kept entry and report it, like any bulk decision (§11.5, I5).

#### Scenario: Discarding all system junk
- **WHEN** the owner selects all rows of the system junk list, one of which is inside a kept folder, and confirms discard
- **THEN** every other row is discarded, and the result lists the kept one as skipped

#### Scenario: No select-all in duplicates
- **WHEN** the owner opens the duplicates list
- **THEN** there is no control to select all rows, and copies are decided one by one or by choosing them

### Requirement: Gems
Gems SHALL list, for one source or all (§11.7):
- personal media and documents with no other copy, oldest first, outside any program or disposable group;
- user material found inside program or disposable groups;
- files with no other copy that sit only on one side of an `overlap` relation.

A file not yet checked SHALL NOT be listed as having no other copy, and each section SHALL state the checked share (I7).

#### Scenario: R2.6 Gems lists the corpus's unique personal files
- **WHEN** the corpus is scanned and hashed to completion and the owner opens Gems
- **THEN** the first section lists the unique personal photos and documents of the corpus's ground truth, including the phone backup's photos, oldest first
- **AND** the second section lists `Backup_PC_2004/C/Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls`
- **AND** the third section lists `Fotos - Copia/2006/Praia/DSC_editada.JPG`

#### Scenario: A copied photo is not a gem
- **WHEN** a photo has a copy in another folder
- **THEN** it is not listed in the first section
