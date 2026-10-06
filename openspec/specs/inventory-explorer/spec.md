# inventory-explorer Specification

## Purpose

Presents the discovered inventory to the owner through server-rendered screens and a JSON read API that show observed evidence, coverage, and unknowns honestly, render every filesystem-derived value safely, and work without any external network dependency.

## Requirements

### Requirement: Unambiguous display names
Display names SHALL be derived from raw name bytes by a reversible escape scheme. Invalid UTF-8 bytes, control characters, and the escape character itself SHALL appear as visible escapes, so two different raw names never share a display name.

#### Scenario: A17 distinct names display distinctly
- **WHEN** one name contains byte `0xE9` and another contains the literal text `\xE9`
- **THEN** their display strings differ

### Requirement: Filesystem-derived values rendered inert
Every filesystem-derived value (names, paths, link text, error text) SHALL always be rendered as text in every screen, never as markup. File content SHALL be shown only through the viewer's rules (file-viewer capability), and no source file content, HTML, SVG, or script SHALL ever run as part of the application.

#### Scenario: A15 malicious filename
- **WHEN** a source contains a folder named `<img src=x onerror=alert(1)>`
- **THEN** the Map, Search results, and detail panel display it as literal text, and no script executes

#### Scenario: Malicious link text
- **WHEN** a symlink's target text is `<script>alert(1)</script>`
- **THEN** the detail panel shows that text literally, and no script executes

### Requirement: Self-contained UI
All scripts and styles SHALL be embedded in the binary and served from the application's origin. The UI SHALL require no CDN, external font, or outbound network access, and Node.js SHALL be needed only to build it, never to run it. The UI SHALL work under a CSP that forbids inline scripts and inline styles.

#### Scenario: Offline operation
- **WHEN** the server host and the browser have no outbound network access
- **THEN** every screen loads fully, and the browser makes no requests to other origins

#### Scenario: Strict CSP holds
- **WHEN** the owner uses every screen under the application's Content-Security-Policy
- **THEN** the browser reports no CSP violation, because no inline script or style is used

#### Scenario: No Node.js at run time
- **WHEN** the binary runs on a host without Node.js installed
- **THEN** the complete interface is served and works

### Requirement: Read-only workflow without models
The complete R1 workflow SHALL work with every source mounted read-only and no network egress: logging in, adding a source, scanning, following progress, Home, Map, Search, the detail panel, the viewer, decisions, and tags.

#### Scenario: A20 read-only and cloud disabled
- **WHEN** the only source is on a read-only mount and the server has no network egress
- **THEN** the owner can log in, add the source, scan it, follow its progress, use Home, Map, and Search, open the detail panel and the viewer, and set decisions and tags, and no write to the source or outbound request is attempted

### Requirement: Home screen
The Home screen SHALL show, for one chosen source or for all sources together: total bytes, files, and folders; bytes by category family, by file kind, and by modification year; decision totals as bytes and files for undecided, keep, discard, and later; and each active scan with its live progress. When any part of a counted tree could not be read, Home SHALL say its figures are partial (§11.1).

#### Scenario: Totals and breakdowns
- **WHEN** the owner opens Home after scanning a source of 120 GB in 410,000 files and 31,000 folders
- **THEN** Home shows those three totals, and charts of the same bytes split by category family, by file kind, and by year

#### Scenario: Decision totals
- **WHEN** 40 GB of the source's files have the effective decision discard, 25 GB keep, 5 GB later, and the rest none
- **THEN** Home shows bytes and file counts for discard, keep, and later, and the remainder as undecided, with the four adding up to the total

#### Scenario: One source or all
- **WHEN** the owner switches Home from all sources to the source `fotos`
- **THEN** every total, breakdown, and decision figure is recomputed for `fotos` alone

#### Scenario: Partial figures are labeled
- **WHEN** a scan could not read one folder of the source
- **THEN** Home states that its figures are partial, rather than presenting them as complete

#### Scenario: Active scan on Home
- **WHEN** a scan of the source is running
- **THEN** Home shows that scan with its folders, files, and bytes so far, updating as the scan proceeds

### Requirement: Map table
The Map table SHALL list a folder's children, sorted by size, file count, newest modification, or name, ascending or descending, in pages of 200 by default and at most 1,000, using an opaque cursor. Each row SHALL show the display name, size, file count, kind or category, date range, triage, and decision; every folder row SHALL show its subtree size and file count and a bar of its composition by family. A folder with at least 1% of its bytes outside its dominant family SHALL also show that family's share next to its category. Selecting a row SHALL open the detail panel (§11.2).

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

### Requirement: Index read API
The server SHALL provide `GET /api/sources`, `GET /api/picker`, `GET /api/home`, `GET /api/entries/{id}`, `GET /api/entries/{id}/children`, `GET /api/entries/{id}/treemap`, `GET /api/search`, `GET /api/tags`, `GET /api/entries/{id}/content`, `GET /api/entries/{id}/text`, `GET /api/jobs/{id}`, and `GET /api/events`. Entry names and paths SHALL be returned both as an escaped display string and as the base64-encoded raw bytes.

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

### Requirement: Treemap
The Map SHALL show a treemap of the same folder as its table, one level per request: the 300 largest children by bytes plus one "other" area holding the count and bytes of the rest. The owner SHALL be able to color it by category family, file kind, age, decision, or tag; coloring by family SHALL use each entry's dominant composition family. Clicking a folder SHALL drill into it, and the table SHALL follow.

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

### Requirement: Search
Search SHALL find entries anywhere in the index, inside groups too, by any combination of: name substring (case-insensitive), extension, file kind, size range, year range, category, tag (own or inherited), effective decision, and a folder to search within. Results SHALL be paged by cursor and carry the match count, exact up to 10,000 and shown as `10000+` beyond.

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

### Requirement: Detail panel
Selecting an entry SHALL open a detail panel with:
- its path, with links to its ancestors;
- size, file count, and dates;
- breakdowns by kind and by year;
- for a folder, its composition by family and its notable entries inside, each a link;
- for a file, a preview, without opening the viewer: an image, video and audio players, a PDF, or the first lines of text, source code, or Markdown, under the viewer's rules (file-viewer capability);
- its classification, with each matched rule's explanation and any veto indicators;
- its own and effective decision, with where it comes from;
- its own and inherited tags, with their origin;
- decision and tag controls;
- technical details (raw name bytes, inode) in a collapsed section.

(§11.8)

#### Scenario: A photo previews in the panel
- **WHEN** the owner selects a JPEG in the Map
- **THEN** the panel shows the photo without the owner choosing Open, and Open still shows it full size

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
- **THEN** inode and raw name bytes are hidden until the owner expands the technical details section

### Requirement: Screens fit the window
At a window of 1366×768 or larger, every screen SHALL keep its content inside its own area. No table column, header, or control SHALL extend past its card or the window, and no two controls SHALL overlap. A table too wide for its card SHALL drop its lowest-priority columns or scroll inside the card, with header and rows aligned. Opening the detail panel SHALL NOT squeeze the Map table below a usable width.

#### Scenario: Map with the detail panel at 1366×768
- **WHEN** the owner opens a folder's details on the Map in a 1366×768 window
- **THEN** the table's name, size, and category columns stay visible inside the table's card, and nothing spills past the window

#### Scenario: Search filters
- **WHEN** the owner opens Search in a 1366×768 window
- **THEN** no filter control overlaps another or its label

### Requirement: Sources screen
The Sources screen SHALL list each source with its state, its volume and whether that volume is recognized if moved (strong or weak), its filesystem capabilities, its totals, its last scan, and its active scan. It SHALL let the owner add a source through the folder picker, limited to folders inside the allowed roots, rename a source, remove one after a confirmation, and start a scan.

#### Scenario: Offline source listed
- **WHEN** a source's volume is not mounted
- **THEN** the source is listed as offline with its last totals and last scan time, and it can still be browsed in the Map

#### Scenario: Weak volume explained
- **WHEN** a source sits on a volume that has no stable identity
- **THEN** the screen states that the source will not be recognized if the volume is mounted at another path

#### Scenario: Picker shows only folders within allowed roots
- **WHEN** the owner opens the picker to add a source
- **THEN** it starts at the allowed roots and lists only folders, and the owner can add a folder without typing a path

#### Scenario: Remove asks first
- **WHEN** the owner chooses to remove a source
- **THEN** nothing is removed until the owner confirms

#### Scenario: Scan now
- **WHEN** the owner chooses "Scan now" on an online source
- **THEN** a scan starts and the source shows it as its active scan

### Requirement: Live progress
The progress of each active scan (folders, files, and bytes so far) SHALL update on screen without a reload, through the event stream. When the stream drops, the interface SHALL reconnect and resume from its last event, or reload a fresh snapshot when told to reset.

#### Scenario: Progress updates without reload
- **WHEN** a scan is running while the owner watches the Sources screen
- **THEN** its folder, file, and byte counts increase on screen without the owner reloading

#### Scenario: Reconnect resumes
- **WHEN** the network drops for a minute during a scan and then returns
- **THEN** the interface reconnects and shows the scan's current progress, or its final state if it ended meanwhile

### Requirement: Plain vocabulary
The interface SHALL NOT show the internal terms atomic, expanded, descriptor, epoch, intent revision, frontier, or coverage scope in any user-facing string (§11.11).

#### Scenario: No internal terms in the catalog
- **WHEN** every string of the English translation catalog is checked
- **THEN** none contains any of those terms

### Requirement: English interface through translation keys
Every user-facing string SHALL come from the translation catalog, which ships English only in R1. Numbers, sizes, and dates SHALL be formatted for the active locale.

#### Scenario: Strings come from the catalog
- **WHEN** a screen is rendered
- **THEN** each label, message, and button text it shows is looked up by a translation key in the English catalog

#### Scenario: Locale formatting
- **WHEN** a folder holds 1234567 files and the locale is English
- **THEN** the count is shown as `1,234,567`, its size in human units, and its dates in the locale's date format
