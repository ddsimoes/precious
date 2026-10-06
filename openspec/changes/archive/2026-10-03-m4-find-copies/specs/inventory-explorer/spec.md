# Spec Delta

## MODIFIED Requirements

### Requirement: Read-only workflow without models
The complete workflow SHALL work with every source mounted read-only and no classifier or network egress configured. The workflow covers logging in, scanning, following progress, browsing, inspecting evidence, rule classification, overrides, protection, refine, collapse, peek, aggregate walks, copy searches, and review.

#### Scenario: A20 read-only and cloud disabled
- **WHEN** the only source is on a read-only mount and no classifier is configured
- **THEN** the owner can log in, scan, browse, inspect, peek, measure, find copies, refine, collapse, override, protect, and review, and no write to the source or outbound request is attempted

## ADDED Requirements

### Requirement: Copies pages
`/copies` SHALL list each source's copy searches, newest first, with a control to search the whole source. `/copies/{id}` SHALL show a search's scope, state, progress while it runs, coverage and gaps, and its results by rank. Each result SHALL show both paths, the relation, matched and freeable bytes, file counts, gaps, freshness, and a mark control when its copy is a node. The header SHALL link to `/copies`.

#### Scenario: Results page
- **WHEN** the owner opens a completed search in which `fotos-b` is inside `fotos`
- **THEN** the first result shows `fotos-b`, "inside", `fotos`, the freeable bytes, `current`, and a control that marks `fotos-b` as `cleanup_candidate` with this result

#### Scenario: Copy inside an atomic unit
- **WHEN** a result's copy is `Elements/bkp-old-laptop` inside the atomic unit `Elements`
- **THEN** the result links to `Elements` and states that `Elements` must be refined before the copy can be marked, and it offers no mark control

#### Scenario: Gaps are visible
- **WHEN** a search recorded two unreadable directories and five unstable files
- **THEN** the search page shows those gaps by kind with bounded examples, and states that a file without a match has no other copy in this search

### Requirement: Copy-search read API
The server SHALL provide `GET /api/copy-searches?source=&cursor=`, `GET /api/copy-searches/{id}`, and `GET /api/copy-searches/{id}/results?cursor=`, paged by opaque cursors. Paths SHALL be returned as escaped display strings and as base64 raw bytes. An unknown search SHALL be HTTP 404 `not_found`, and a malformed cursor HTTP 400 `invalid_request`.

#### Scenario: Results through the API
- **WHEN** a client fetches the results of a completed search
- **THEN** each result carries its rank, relation, both paths with their node or holding unit, matched and freeable bytes and files, gaps, and freshness

#### Scenario: Unknown search
- **WHEN** a client requests a search ID that does not exist
- **THEN** the response is HTTP 404 with code `not_found`

### Requirement: Inspector shows copies
The inspector of an active directory or file SHALL show the results of its source's latest complete copy search that name it on either side, with their freshness. A directory's inspector SHALL offer a control that starts a copy search of that directory.

#### Scenario: Inspector of a copy
- **WHEN** the owner opens `fotos-b` after a search found it inside `fotos`
- **THEN** the inspector's Copies section shows "inside `fotos`" with the matched size, freshness, and the search time, and links to the search
