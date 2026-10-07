# Spec Delta

## ADDED Requirements

### Requirement: Search answers quickly at scale
On an index of 2 million entries on the development machine, Search SHALL show the first page of results within 1 s at the 95th percentile, and its count within 2 s. This SHALL hold with no filter, with each duplicate state, and with a source chosen.

#### Scenario: Duplicate filters at 2 million entries
- **WHEN** the slow test searches a 2-million-entry index with no filter, then with each duplicate state, with and without a source
- **THEN** every first page arrives within 1 s and every count within 2 s at the 95th percentile

### Requirement: Unreadable entries are marked
A folder or file that could not be read SHALL be shown as "could not be read" in the Map, in Search results, and in the detail panel, never as an empty folder or an unchecked file (I7). Home's notice that its figures are partial SHALL link to a search that lists the entries that could not be read.

#### Scenario: An unreadable folder in the Map
- **WHEN** the Map lists a folder that the scan could not read
- **THEN** its row says it could not be read instead of showing 0 bytes, 0 files, and 0% duplicated

#### Scenario: From Home to the unreadable entries
- **WHEN** Home says its figures are partial and the owner follows its link
- **THEN** Search lists exactly the folders and files that could not be read, in the source Home shows

### Requirement: The Map works from the keyboard
In the Map table:
- the up and down arrow keys SHALL move the selected row, opening its details;
- Enter SHALL open the selected folder, or show the selected file in the viewer;
- Escape SHALL close the detail panel.

The keys SHALL be ignored while typing in a field or inside a dialog, as in review lists.

#### Scenario: Walking a folder with the arrows
- **WHEN** the owner selects the first row of a folder and presses the down arrow twice, then Enter on a folder row
- **THEN** the third row is selected with its details shown, and Enter opens that folder in the Map

### Requirement: Folder paths collapse single-child chains
The Map's folder path SHALL show a chain of folders that each hold nothing but the next as one step, which opens the deepest of them. A folder that holds only one folder SHALL open on that folder's contents when it is the start of a source.

#### Scenario: A source whose top holds one folder
- **WHEN** a source's top folder holds only `old-disk`, which holds only `home`
- **THEN** opening the source in the Map shows the contents of `home`, and the path shows the source, then `old-disk/home` as one step

### Requirement: The chosen source is remembered
The source the owner last chose on Home, Opportunities, Gems, Search, or the Map SHALL be used by those screens, and by the Map's starting folder, until the owner chooses another source or all sources. It SHALL be remembered per browser.

#### Scenario: Home's choice carries to the Map
- **WHEN** the owner chooses the source `archive` on Home and then opens the Map from the main menu
- **THEN** the Map starts on `archive`, and Opportunities and Gems show `archive` too

### Requirement: Dialogs return focus
Closing the viewer or any confirmation dialog SHALL return the keyboard focus to the control that opened it.

#### Scenario: Escape from the viewer
- **WHEN** the owner opens a photo in the viewer from the detail panel and presses Escape
- **THEN** the viewer closes, focus is on the panel's Open button, and a second Escape closes the panel

## MODIFIED Requirements

### Requirement: Search
Search SHALL find entries anywhere in the index, inside groups too, by any combination of: name substring (case-insensitive, and accent-insensitive when the searched name has three or more characters), extension, file kind, size range, year range, category, tag (own or inherited), effective decision, duplicate state, entries that could not be read, source, and a folder to search within. The duplicate states SHALL be: has another copy, has a copy outside the folder searched within, no other copy, and not checked. Each result SHALL show the folder that holds it and, for a checked file, its number of copies. Results SHALL be paged by cursor and carry the match count, exact up to 10,000 and shown as `10000+` beyond.

#### Scenario: R1.3 Search finds files anywhere
- **WHEN** the owner searches the regression corpus by name, by extension, by size range, by year range, and by tag
- **THEN** each search lists every matching file, including files inside groups such as `Microsoft Office/OFFICE11`

#### Scenario: Case-insensitive name substring
- **WHEN** the owner searches for the name `natal`
- **THEN** the results include `Fotos Natal 2004` and `NATAL.JPG`

#### Scenario: Accents are ignored
- **WHEN** a source holds the folder `Confraternização 2018` and the owner searches for `confraternizacao`
- **THEN** the results include `Confraternização 2018`

#### Scenario: Where each result is
- **WHEN** a search lists three copies of `MOV_0195.mp4` in three folders
- **THEN** each result shows its own folder, and each shows that it has 3 copies

#### Scenario: One source
- **WHEN** the owner chooses the source `fotos` in Search
- **THEN** only entries of `fotos` are listed and counted

#### Scenario: Inherited tag and effective decision
- **WHEN** the folder `Fotos` carries the tag `familia` and the decision keep, and the owner searches for tag `familia` with decision keep
- **THEN** the results include the files inside `Fotos`, whose tag and decision are inherited

#### Scenario: Search within a folder
- **WHEN** the owner limits a search for extension `doc` to the folder `Documentos`
- **THEN** only matching entries inside `Documentos` are listed

#### Scenario: Large result counts
- **WHEN** a search matches 25,000 entries
- **THEN** the count is shown as `10000+`, and a search matching 9,500 entries shows exactly 9,500

#### Scenario: Copies outside a folder
- **WHEN** the owner searches within `Fotos - Copia` for files with a copy outside it
- **THEN** every photo of `Fotos - Copia` except `2006/Praia/DSC_editada.JPG` is listed, and selecting all of them gives an explicit list that a bulk decision can use

#### Scenario: Copy outside requires a folder
- **WHEN** a search asks for files with a copy outside the folder but names no folder to search within
- **THEN** the response is HTTP 400 `invalid_request`
