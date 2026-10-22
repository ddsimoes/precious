# Spec Delta

## MODIFIED Requirements

### Requirement: Map table
The Map table SHALL list a folder's children, sorted by size, file count, newest modification, or name, ascending or descending, in pages of 200 by default and at most 1,000, using an opaque cursor. In the Only here mode it SHALL also sort by bytes only on this source. Each row SHALL show the display name, size, file count, kind or category, date range, percent duplicated (in the Only here mode, its place instead), triage, and decision; every folder row SHALL show its subtree size and file count and a bar of its composition by family. A folder with at least 1% of its bytes outside its dominant family SHALL also show that family's share next to its category. Selecting a row SHALL open the detail panel (§11.2).

#### Scenario: A mixed folder is visible in the table
- **WHEN** a folder classified `personal_media` holds 98% personal bytes and 2% programs
- **THEN** its row shows a composition bar with both families and states that it is 98% personal

#### Scenario: Cursor pagination
- **WHEN** a folder has 250 children and the client follows `next_cursor` with the default page size
- **THEN** it receives pages of 200 and 50 children, with no duplicates or omissions, and the last page has no cursor

#### Scenario: Page size is bounded
- **WHEN** a client asks for a page of 5,000 children
- **THEN** the page holds at most 1,000 rows and carries a cursor for the rest

#### Scenario: Sorting
- **WHEN** the owner sorts a folder's table by files, descending
- **THEN** the folder holding the most files in its subtree comes first, and paging continues in that order

#### Scenario: Sorting by what is only on this source
- **WHEN** the owner, in the Only here mode, sorts a folder's table by bytes only on this source
- **THEN** the child holding the most bytes that are an only copy or a copy only on this source comes first, children whose figures are not computed yet come before all, children with none come last, and paging continues in that order

#### Scenario: R1.2 Every folder shows its size
- **WHEN** the owner browses the regression corpus in the Map table, including folders the rules mark as groups, such as `Arquivos de programas/Winamp`
- **THEN** every folder row shows its total size and file count, each equal to the sum over the files in its subtree

#### Scenario: R1.10 Map pages stay fast at 2 million entries
- **WHEN** a synthetic tree of 2,000,000 entries has been scanned
- **THEN** a 200-row page of a folder's children sorted by size answers in under 300 ms at the 95th percentile, and one treemap level answers in under 500 ms

#### Scenario: Percent duplicated in the table
- **WHEN** the corpus is hashed and the owner opens its root in the Map
- **THEN** the `Fotos - Copia` row shows its percent duplicated, and a file with no other copy shows 0%

### Requirement: Treemap
The Map SHALL show a treemap of the same folder as its table, one level per request: the 300 largest children by bytes plus one "other" area holding the count and bytes of the rest. The owner SHALL be able to color it by category family, file kind, age, decision, tag, duplication, or where copies are; coloring by family SHALL use each entry's dominant composition family. Clicking a folder or a completely read archive SHALL drill into it, and the table SHALL follow.

#### Scenario: Unclassified photos take the personal color
- **WHEN** the owner colors by family a folder of JPEGs that no rule matches
- **THEN** every photo takes the Personal and valuable color

#### Scenario: Large folder with an other area
- **WHEN** the owner opens a folder with 1,000 children in the Map
- **THEN** the treemap shows its 300 largest children and one "other" area labeled with the 700 remaining children and their bytes

#### Scenario: Drill into a group
- **WHEN** the owner clicks the group `Arquivos de programas/Winamp` in the treemap
- **THEN** the treemap and the table both show the contents of `Winamp`

#### Scenario: Color by decision
- **WHEN** the owner colors the treemap by decision and one child folder has the effective decision discard
- **THEN** that folder's area takes the discard color, and the legend names each decision

#### Scenario: Color by duplication
- **WHEN** the owner colors the corpus root by duplication after hashing
- **THEN** `Fotos - Copia` takes the color of its percent duplicated, a folder not yet checked takes the not-checked color, and the legend names each band

#### Scenario: Color by where copies are
- **WHEN** the owner turns on the Only here mode on a folder holding a child with an only copy and a child wholly on another source, and hides what has a copy elsewhere
- **THEN** the treemap is colored by where copies are and still shows both children, the first in the only-copy color and the second in the other-source color, and the legend names each place

#### Scenario: Drill into an archive
- **WHEN** the owner clicks `Downloads/eMule0.47c-Installer.zip` in the treemap
- **THEN** the treemap and the table both show the archive's members

## ADDED Requirements

### Requirement: The Map shows where copies are
The Map SHALL offer an Only here mode, kept in the address and across drilling. Every row SHALL state its place in words and by emphasis: only copy strongest, this source less, other source dimmed, and none and disposable files muted. Not checked, unreadable, and not computed yet SHALL be marked as not known. Folder and archive rows SHALL show a bar of their bytes per place, and the open folder its totals per place.

#### Scenario: Emphasis follows the place
- **WHEN** the owner turns on the mode at the root of a source holding a folder whose files all have a copy on another source, and a folder holding one only copy
- **THEN** the first folder's row is dimmed and says that everything has a copy on another source, and the second is highlighted and shows the bytes of its only copy

#### Scenario: The mode stays on
- **WHEN** the owner turns on the mode, drills into a folder, and reloads the page
- **THEN** the mode is still on, and the folder's rows show their places

#### Scenario: Inside an archive
- **WHEN** the owner drills into a complete zip in the mode
- **THEN** each member's row shows its place, member folders show their bytes per place, and the totals give the members' unpacked sizes

#### Scenario: Figures not computed yet
- **WHEN** the owner opens a folder in the mode before its figures have been computed
- **THEN** its folder rows read "Not computed yet", its file rows show their places, and the totals say the figures are being updated

#### Scenario: R2.7 Only-copy figures name the checked share
- **WHEN** hashing has checked 80% of the content that could have a copy across all sources, and the owner opens a folder in the mode
- **THEN** its totals say that 80% of that content has been checked and that the figures may still change, and its files not checked yet read "Not checked yet", never "Only copy"

### Requirement: The Only here mode can hide what has a copy elsewhere
In the Only here mode, the owner SHALL be able to hide every row whose place is other source or none, every disposable file, and every missing entry. Rows not checked, unreadable, or not computed yet SHALL stay. Paging SHALL follow the filter.

#### Scenario: Hiding what is safe elsewhere
- **WHEN** the owner hides what has a copy on another source, at a root holding one folder wholly on another source and one folder holding an only copy
- **THEN** only the second folder is listed, and drilling into it keeps the filter

### Requirement: File rows name another copy
In the Only here mode, a file whose place is this source or other source SHALL name one other copy, with a link that opens it, and SHALL say how many more there are. For other source, the named copy SHALL be on another source, a connected one first. For this source, it SHALL be on its own source, loose files before archive members. A copy inside an archive SHALL be named by archive and member. A copy on a source that is not connected SHALL say so.

#### Scenario: A copy on the same source
- **WHEN** a document's other copies are two files in other folders of its source
- **THEN** its row names one of them and says there is one more

#### Scenario: A copy inside an archive
- **WHEN** a file's only other copy is a member of `backup.tar.gz` on its source
- **THEN** its row says it is also inside `backup.tar.gz`, naming the member

#### Scenario: A copy on an unconnected source
- **WHEN** a file's only other-source copy is on a source that is offline
- **THEN** its row names that copy and says that its source is not connected

#### Scenario: A member covered by its archive
- **WHEN** a member of a zip has no copy of its own, but the zip file has a copy on another source
- **THEN** the member's row says it is inside a copy of the zip and names that copy

### Requirement: Folder rows name the folder holding all their content
In the Only here mode, a folder or archive whose content all exists in another folder or archive SHALL name it, with a link to Compare the two. This holds when they hold the same content, or when this one is inside the other.

#### Scenario: A folder copied whole
- **WHEN** `Fotos 2004` holds the same content as `Backup/Fotos 2004` on another source
- **THEN** its row says that it holds the same content as `Backup/Fotos 2004`, and the link opens Compare on the two

### Requirement: The detail panel shows places
The detail panel of a folder or complete archive SHALL show its files and bytes per place, its disposable files, and the checked share across all sources. An archive's figures SHALL be its members' unpacked sizes, and the panel SHALL say so. A file's panel SHALL state its place above its list of copies.

#### Scenario: A folder's places in the panel
- **WHEN** the owner opens the panel of a folder holding only copies, copies only on its source, copies on another source, and system files
- **THEN** the panel lists the files and bytes of each place and of the disposable files, and the checked share

#### Scenario: A file's place in the panel
- **WHEN** the owner opens the panel of a file whose only other copy is on another source
- **THEN** the panel says that it has a copy on another source, above the copy's path
