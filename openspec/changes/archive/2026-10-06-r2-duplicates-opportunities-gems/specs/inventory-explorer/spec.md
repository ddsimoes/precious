# Spec Delta

## ADDED Requirements

### Requirement: Opportunities, Gems, and Compare screens
The interface SHALL offer Opportunities and Gems in its main navigation. Each opportunity card SHALL open its review list. Compare SHALL open from a relation in the detail panel or in a review list, and from a folder's detail panel by choosing a second folder or archive, with the two sides named in the address so it can be bookmarked (§11.4, §11.6, §11.7).

#### Scenario: From a card to its list
- **WHEN** the owner chooses the system junk card on Home
- **THEN** the system junk review list opens

#### Scenario: Compare two chosen folders
- **WHEN** the owner chooses "Compare with…" on `Fotos`, then opens `Fotos - Copia` and chooses to compare it with `Fotos`
- **THEN** Compare opens with `Fotos` on the left and `Fotos - Copia` on the right, and reloading the page shows the same comparison

#### Scenario: Deciding from Compare
- **WHEN** the owner sets discard on a file listed only on the right in Compare
- **THEN** the file's decision is discard, and the list shows it

### Requirement: Browsing inside archives
The Map SHALL let the owner drill into a completely read archive as into a folder, with the table and the treemap showing its members, and open a member in the detail panel and the viewer. A member's detail panel SHALL show the archive's effective decision as the one that applies, with a link to the archive, and no decision or tag controls (§11.2, §6.4).

#### Scenario: A member's detail panel
- **WHEN** the owner selects a photo inside `Downloads/fotos_2005_do_pendrive.zip`
- **THEN** the panel shows its preview, its path inside the archive, and its copies, and states that it is decided with the archive

## MODIFIED Requirements

### Requirement: Read-only workflow without models
The complete R2 workflow SHALL work with every source mounted read-only and no network egress: logging in, adding a source, scanning, hashing, following progress, Home, Map, Search, the detail panel, the viewer, decisions, tags, Opportunities and review lists, Compare, and Gems.

#### Scenario: A20 read-only and cloud disabled
- **WHEN** the only source is on a read-only mount and the server has no network egress
- **THEN** the owner can log in, add the source, scan and hash it, follow its progress, use Home, Map, Search, Opportunities, Compare, and Gems, open the detail panel and the viewer, and set decisions and tags, and no write to the source or outbound request is attempted

### Requirement: Home screen
The Home screen SHALL show, for one chosen source or for all sources together: total bytes, files, and folders; bytes by category family, by file kind, and by modification year; decision totals as bytes and files for undecided, keep, discard, and later; hashing coverage; the opportunity cards with their bytes; and each active scan and hashing job with its live progress. When any part of a counted tree could not be read, Home SHALL say its figures are partial (§11.1).

#### Scenario: Totals and breakdowns
- **WHEN** the owner opens Home after scanning a source of 120 GB in 410,000 files and 31,000 folders
- **THEN** Home shows those three totals, and charts of the same bytes split by category family, by file kind, and by year

#### Scenario: Decision totals
- **WHEN** 40 GB of the source's files have the effective decision discard, 25 GB keep, 5 GB later, and the rest none
- **THEN** Home shows bytes and file counts for discard, keep, and later, and the remainder as undecided, with the four adding up to the total

#### Scenario: One source or all
- **WHEN** the owner switches Home from all sources to the source `fotos`
- **THEN** every total, breakdown, decision figure, coverage figure, and card is recomputed for `fotos` alone

#### Scenario: Partial figures are labeled
- **WHEN** a scan could not read one folder of the source
- **THEN** Home states that its figures are partial, rather than presenting them as complete

#### Scenario: Active scan on Home
- **WHEN** a scan of the source is running
- **THEN** Home shows that scan with its folders, files, and bytes so far, updating as the scan proceeds

#### Scenario: Hashing on Home
- **WHEN** a hashing job of the source is running
- **THEN** Home shows its checked bytes out of the bytes that could have a copy, updating as it proceeds, and the opportunity cards with their bytes

### Requirement: Map table
The Map table SHALL list a folder's children, sorted by size, file count, newest modification, or name, ascending or descending, in pages of 200 by default and at most 1,000, using an opaque cursor. Each row SHALL show the display name, size, file count, kind or category, date range, percent duplicated, triage, and decision; every folder row SHALL show its subtree size and file count and a bar of its composition by family. A folder with at least 1% of its bytes outside its dominant family SHALL also show that family's share next to its category. Selecting a row SHALL open the detail panel (§11.2).

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

#### Scenario: R1.2 Every folder shows its size
- **WHEN** the owner browses the regression corpus in the Map table, including folders the rules mark as groups, such as `Arquivos de programas/Winamp`
- **THEN** every folder row shows its total size and file count, each equal to the sum over the files in its subtree

#### Scenario: R1.10 Map pages stay fast at 2 million entries
- **WHEN** a synthetic tree of 2,000,000 entries has been scanned
- **THEN** a 200-row page of a folder's children sorted by size answers in under 300 ms at the 95th percentile, and one treemap level answers in under 500 ms

#### Scenario: Percent duplicated in the table
- **WHEN** the corpus is hashed and the owner opens its root in the Map
- **THEN** the `Fotos - Copia` row shows its percent duplicated, and a file with no other copy shows 0%

### Requirement: Index read API
The server SHALL provide `GET /api/sources`, `GET /api/picker`, `GET /api/home`, `GET /api/entries/{id}`, `GET /api/entries/{id}/children`, `GET /api/entries/{id}/treemap`, `GET /api/search`, `GET /api/tags`, `GET /api/entries/{id}/content`, `GET /api/entries/{id}/text`, `GET /api/opportunities`, `GET /api/opportunities/{card}`, `GET /api/gems`, `GET /api/compare`, `GET /api/jobs/{id}`, and `GET /api/events`. Entry names and paths SHALL be returned both as an escaped display string and as the base64-encoded raw bytes.

#### Scenario: A17 lossless name in API
- **WHEN** a client fetches an entry whose name contains invalid UTF-8 bytes
- **THEN** the response carries the exact raw bytes of its name and path in base64, and an unambiguous escaped display form of each

#### Scenario: Unknown entry
- **WHEN** a client requests an entry ID that does not exist, or one that is not a valid ID
- **THEN** the response is HTTP 404 with code `not_found`

#### Scenario: Bad query value
- **WHEN** a client requests children with `sort=color` or with a cursor it did not receive from the server
- **THEN** the response is HTTP 400 with code `invalid_request`

#### Scenario: Entry carries classification and intent fields
- **WHEN** a client fetches `GET /api/entries/{id}` for a folder
- **THEN** the response carries its ancestors, its category, family, traits, triage, group and veto flags, the matched rules with their explanations and any veto indicators, its own and effective decision with the entry it comes from, its own and inherited tags with their origins, and its breakdowns by kind and by year

#### Scenario: Entry carries content fields
- **WHEN** a client fetches `GET /api/entries/{id}` for a hashed file with two other copies
- **THEN** the response carries its content state, its SHA-256, the two copies with their paths and decisions, and the checked share of the content that could have a copy

#### Scenario: Unknown card
- **WHEN** a client requests `GET /api/opportunities/colors`
- **THEN** the response is HTTP 404 with code `not_found`

### Requirement: Treemap
The Map SHALL show a treemap of the same folder as its table, one level per request: the 300 largest children by bytes plus one "other" area holding the count and bytes of the rest. The owner SHALL be able to color it by category family, file kind, age, decision, tag, or duplication; coloring by family SHALL use each entry's dominant composition family. Clicking a folder or a completely read archive SHALL drill into it, and the table SHALL follow.

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

#### Scenario: Drill into an archive
- **WHEN** the owner clicks `Downloads/eMule0.47c-Installer.zip` in the treemap
- **THEN** the treemap and the table both show the archive's members

### Requirement: Search
Search SHALL find entries anywhere in the index, inside groups too, by any combination of: name substring (case-insensitive), extension, file kind, size range, year range, category, tag (own or inherited), effective decision, duplicate state, and a folder to search within. The duplicate states SHALL be: has another copy, has a copy outside the folder searched within, no other copy, and not checked. Results SHALL be paged by cursor and carry the match count, exact up to 10,000 and shown as `10000+` beyond.

#### Scenario: R1.3 Search finds files anywhere
- **WHEN** the owner searches the regression corpus by name, by extension, by size range, by year range, and by tag
- **THEN** each search lists every matching file, including files inside groups such as `Microsoft Office/OFFICE11`

#### Scenario: Case-insensitive name substring
- **WHEN** the owner searches for the name `natal`
- **THEN** the results include `Fotos Natal 2004` and `NATAL.JPG`

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

### Requirement: Detail panel
Selecting an entry SHALL open a detail panel with:
- its path, with links to its ancestors;
- size, file count, and dates;
- breakdowns by kind and by year;
- for a folder, its composition by family and its notable entries inside, each a link;
- for a file, a preview, without opening the viewer: an image, video and audio players, a PDF, or the first lines of text, source code, or Markdown, under the viewer's rules (file-viewer capability);
- its copies and relations, with the checked share (§11.8);
- its classification, with each matched rule's explanation and any veto indicators;
- its own and effective decision, with where it comes from;
- its own and inherited tags, with their origin;
- decision and tag controls;
- technical details (raw name bytes, inode, SHA-256) in a collapsed section.

(§11.8)

#### Scenario: A photo previews in the panel
- **WHEN** the owner selects a JPEG in the Map
- **THEN** the panel shows the photo without the owner choosing Open, and Open still shows it full size

#### Scenario: A file that could not be read
- **WHEN** the owner selects a photo that hashing could not read
- **THEN** the panel says the file could not be read, and does not ask for a rescan

#### Scenario: What is inside a folder
- **WHEN** the owner selects a folder that is mostly photos but holds a downloads folder and an installed program
- **THEN** the panel shows its composition and lists the downloads folder and the program with their sizes, and choosing one opens it

#### Scenario: Inherited decision shows its origin
- **WHEN** the owner opens a file inside `Downloads`, which has the decision discard, and the file has no decision of its own
- **THEN** the panel shows the effective decision discard as inherited from `Downloads`, with a link to it

#### Scenario: Veto indicators listed
- **WHEN** the owner opens a group whose discard was vetoed because it holds a spreadsheet
- **THEN** the panel shows the veto and names the spreadsheet among its indicators

#### Scenario: Rule explanation
- **WHEN** the owner opens a folder classified as an application installation
- **THEN** the panel shows that category, its family and triage, and the explanation of the rule that matched

#### Scenario: Technical details collapsed
- **WHEN** the owner opens any entry
- **THEN** inode, raw name bytes, and SHA-256 are hidden until the owner expands the technical details section

#### Scenario: Copies of a file
- **WHEN** the owner selects `Documentos/curriculo.doc` after hashing
- **THEN** the panel lists its other copies with their paths and decisions, each a link, and the checked share

#### Scenario: Relations of a folder
- **WHEN** the owner selects `Downloads/emule-0.47c` after hashing
- **THEN** the panel shows that it is the same as `Downloads/eMule0.47c-Installer.zip`, with a link that opens Compare on the two

### Requirement: Screens fit the window
At a window of 1366×768 or larger, every screen SHALL keep its content inside its own area. No table column, header, or control SHALL extend past its card or the window, and no two controls SHALL overlap. A table too wide for its card SHALL drop its lowest-priority columns or scroll inside the card, with header and rows aligned. Opening the detail panel SHALL NOT squeeze the Map table below a usable width.

#### Scenario: Map with the detail panel at 1366×768
- **WHEN** the owner opens a folder's details on the Map in a 1366×768 window
- **THEN** the table's name, size, and category columns stay visible inside the table's card, and nothing spills past the window

#### Scenario: Search filters
- **WHEN** the owner opens Search in a 1366×768 window
- **THEN** no filter control overlaps another or its label

#### Scenario: New screens at 1366×768
- **WHEN** the owner opens Opportunities, a review list, Compare, and Gems in a 1366×768 window
- **THEN** nothing spills past its card or the window, and no two controls overlap

### Requirement: Live progress
The progress of each active scan (folders, files, and bytes so far) and of each hashing job (bytes checked out of bytes to check) SHALL update on screen without a reload, through the event stream. When a hashing job or a recomputation of relations ends, the screens that show duplicates SHALL refresh. When the stream drops, the interface SHALL reconnect and resume from its last event, or reload a fresh snapshot when told to reset.

#### Scenario: Progress updates without reload
- **WHEN** a scan is running while the owner watches the Sources screen
- **THEN** its folder, file, and byte counts increase on screen without the owner reloading

#### Scenario: Reconnect resumes
- **WHEN** the network drops for a minute during a scan and then returns
- **THEN** the interface reconnects and shows the scan's current progress, or its final state if it ended meanwhile

#### Scenario: Duplicates refresh when hashing advances
- **WHEN** the owner watches Opportunities while relations are recomputed
- **THEN** the cards show the new figures without a reload
