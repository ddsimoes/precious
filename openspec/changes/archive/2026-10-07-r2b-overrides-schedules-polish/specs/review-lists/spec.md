# Spec Delta

## ADDED Requirements

### Requirement: Similar folders are listed
Opportunities SHALL offer a list of the folders and archives related as `overlap`.
- **Row content.** Each row SHALL show both sides, the bytes they have in common, and the files and bytes found only on each side, with a link that opens Compare on the two.
- **Order and scope.** The list SHALL be ordered by bytes in common, largest first, and SHALL follow the chosen source.
- **Read-only.** It SHALL carry no decision controls and no card bytes, because a similar folder is not a copy (§6.4).

#### Scenario: Similar folders on the corpus
- **WHEN** the corpus is hashed to completion and the owner opens the similar folders list
- **THEN** the `Fotos - Copia`/`Fotos` relation is listed with its bytes in common and the files only on each side, and its Compare link opens Compare on the two

### Requirement: Cards show what was decided
Each opportunity card and review list header SHALL show, besides its open rows, the count and bytes of its rows that are no longer open. A card with no open row left SHALL say so instead of showing zero.

#### Scenario: Progress after deciding
- **WHEN** the owner discards 20 rows of the partial downloads, empty folders, and zero-byte files list, holding 4.4 GB
- **THEN** the card shows the open rows left, and that 20 rows holding 4.4 GB were decided
