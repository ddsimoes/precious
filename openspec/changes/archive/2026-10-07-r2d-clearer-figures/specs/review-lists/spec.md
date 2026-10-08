# Spec Delta

## MODIFIED Requirements

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

### Requirement: Similar folders are listed
Opportunities SHALL offer a list of the folders and archives related as `overlap`.
- **Row content.** Each row SHALL show both sides and the bytes they have in common, with a link that opens Compare on the two. It SHALL NOT count the files found only on each side: Compare counts them (ADR 0010).
- **Order and scope.** The list SHALL be ordered by bytes in common, largest first, and SHALL follow the chosen source.
- **Read-only.** It SHALL carry no decision controls and no card bytes, because a similar folder is not a copy (§6.4).

#### Scenario: Similar folders on the corpus
- **WHEN** the corpus is hashed to completion and the owner opens the similar folders list
- **THEN** the `Fotos - Copia`/`Fotos` relation is listed with its bytes in common and no count of files only on either side, and its Compare link opens Compare on the two
