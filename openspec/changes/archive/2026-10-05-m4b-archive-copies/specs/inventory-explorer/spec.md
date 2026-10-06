# Spec Delta

## MODIFIED Requirements

### Requirement: Copies pages
`/copies` SHALL list each source's copy searches, newest first, with a control to search the whole source and a control to search several sources at once. `/copies/{id}` SHALL show a search's scope, state, progress while it runs, coverage, gaps, archive outcomes, and its results by rank. Each result SHALL show both paths with their sources and archive inner paths, the relation, matched and freeable bytes, file counts, gaps, freshness, and a mark control when its copy is a node. The header SHALL link to `/copies`.

#### Scenario: Results page
- **WHEN** the owner opens a completed search in which `fotos-b` is inside `fotos`
- **THEN** the first result shows `fotos-b`, "inside", `fotos`, the freeable bytes, `current`, and a control that marks `fotos-b` as `cleanup_candidate` with this result

#### Scenario: Copy inside an atomic unit
- **WHEN** a result's copy is `Elements/bkp-old-laptop` inside the atomic unit `Elements`
- **THEN** the result links to `Elements` and states that `Elements` must be refined before the copy can be marked, and it offers no mark control

#### Scenario: Gaps are visible
- **WHEN** a search recorded two unreadable directories and five unstable files
- **THEN** the search page shows those gaps by kind with bounded examples, and states that a file without a match has no other copy in this search

#### Scenario: Archive and its unpacked folder
- **WHEN** the owner opens a search in which the cataloged file `bkp.tar.gz` is the same as `bkp`
- **THEN** the result shows `bkp.tar.gz` as an archive, "same", `bkp`, the archive's packed size as freeable, and a control that marks `bkp.tar.gz`

#### Scenario: Copy inside an archive
- **WHEN** a result's copy is the folder `home/fotos` inside `bkp.tar.gz`
- **THEN** the result shows `bkp.tar.gz` followed by `home/fotos`, states that a part of an archive cannot be removed on its own, and offers no mark control

#### Scenario: Archive outcomes are visible
- **WHEN** a search opened three archives, rejected one, and did not open two 7z archives
- **THEN** the search page shows those counts by outcome, with bounded examples naming each archive and its reason

#### Scenario: Search several sources
- **WHEN** the owner selects `disk` and `old-disk` on `/copies` and starts a search
- **THEN** the page of the new search shows both source roots as its scope, and the search is listed under both sources

### Requirement: Copy-search read API
The server SHALL provide `GET /api/copy-searches?source=&cursor=`, `GET /api/copy-searches/{id}`, and `GET /api/copy-searches/{id}/results?cursor=`, paged by opaque cursors. Paths SHALL be returned as escaped display strings and as base64 raw bytes. Each result side SHALL carry its source and, when it lies in an archive, its path inside the archive. An unknown search SHALL be HTTP 404 `not_found`, and a malformed cursor HTTP 400 `invalid_request`.

#### Scenario: Results through the API
- **WHEN** a client fetches the results of a completed search
- **THEN** each result carries its rank, relation, both paths with their source and their node or holding unit, matched and freeable bytes and files, gaps, and freshness

#### Scenario: Side inside an archive through the API
- **WHEN** a client fetches a result whose copy is the folder `home/fotos` inside `bkp.tar.gz`
- **THEN** the copy side carries the path of `bkp.tar.gz`, its node, and the inner path `home/fotos` with its kind

#### Scenario: Unknown search
- **WHEN** a client requests a search ID that does not exist
- **THEN** the response is HTTP 404 with code `not_found`

### Requirement: Inspector shows copies
The inspector of an active directory or file SHALL show the results of the latest complete or partial copy search covering its source that name it on either side, with their freshness. A directory's inspector SHALL offer a control that starts a copy search of that directory.

#### Scenario: Inspector of a copy
- **WHEN** the owner opens `fotos-b` after a search found it inside `fotos`
- **THEN** the inspector's Copies section shows "inside `fotos`" with the matched size, freshness, and the search time, and links to the search

## ADDED Requirements

### Requirement: Inspector shows archive contents
The inspector of an active file that a copy search opened as an archive SHALL list the archive's folders and files with their sizes, one folder at a time and paged, with the time and outcome of its latest opening, through the page and `GET /api/nodes/{id}/archive`. When the file's size or modification time differs from that opening, it SHALL say the archive changed since. A file never opened SHALL say so.

#### Scenario: M4b-6 Contents of an opened archive
- **WHEN** the owner opens the inspector of `bkp.tar.gz` after a search opened it
- **THEN** it lists the archive's top-level folders and files with their sizes and the opening time, and selecting a folder lists that folder's entries

#### Scenario: M4b-6 Archive changed since it was opened
- **WHEN** a scan records a new modification time for `bkp.tar.gz` after the search
- **THEN** the inspector still lists the contents, stating that the archive changed since they were read

#### Scenario: Archive not looked inside
- **WHEN** the owner opens the inspector of `new.zip`, which no search has opened
- **THEN** it states that the archive has not been looked inside yet, and that a copy search of its folder will

#### Scenario: Rejected archive
- **WHEN** the owner opens the inspector of a zip that a search rejected for the member `../../etc/passwd`
- **THEN** it shows the outcome `rejected` and names that member
