# inventory-explorer Specification

## Purpose

Presents the discovered inventory to the owner through server-rendered screens and a JSON read API that show observed evidence, coverage, and unknowns honestly, render every filesystem-derived value safely, and work without any external network dependency.

## Requirements

### Requirement: Overview screen
The overview SHALL show, per source: availability, identity status, read-only mount state, last scan time, active jobs, counts of inventory units by coverage state, units per effective category, the number of units in each review queue, and the source's classifier permission. Totals SHALL be shown by size class (measured, lower bound, stale, and units of unknown size), never as one figure unless the other classes are empty. The overview SHALL also show today's classifier spend against the daily cap, with reserved and unknown amounts kept separate.

#### Scenario: Unknown sizes stay visible
- **WHEN** a source has 12 loose files of known size and 30 unmeasured atomic directories
- **THEN** the overview shows the known file bytes labeled as partial, alongside "30 units of unknown size", and no total presented as the source's size

#### Scenario: Queue and category counts
- **WHEN** a scan has classified 8 installations, 3 caches, and 2 `unknown` directories, with no owner decisions yet
- **THEN** the overview shows those category counts, 11 units in the `cleanup` queue, and 2 in the `ambiguous` queue, each linking to its queue

#### Scenario: API spend on the overview
- **WHEN** today's attempts have an estimated charge of 0.004, 0.001 is still reserved, and one attempt's usage is unknown, against a daily cap of 0.10
- **THEN** the overview shows the estimated spend, the reserved amount, the count of unknown-usage attempts, and the cap, each labeled

### Requirement: Paginated explorer
The explorer SHALL list a node's current children in pages of the configured size (default 100), ordered by raw name bytes, using an opaque cursor. Each row SHALL show the display name, kind, inventory mode (marked if owner-set), category and its source, disposition, protection, preservation risk, coverage, size or size class, and last observation time, plus symlink, mount-boundary, and issue markers. The page SHALL show the escaped source-relative path.

#### Scenario: Cursor pagination
- **WHEN** a directory has 250 current children and the client follows `next` cursors
- **THEN** it receives pages of 100, 100, and 50 children, with no duplicates or omissions

#### Scenario: Atomic row
- **WHEN** an atomic directory is listed
- **THEN** its row shows `atomic`, its size as `unknown` or as its current measurement with that measurement's size class, and opening it shows its inspector rather than a listing of descendants

#### Scenario: Expanded row
- **WHEN** an expanded directory is listed
- **THEN** its row shows `expanded`, links to its children, and shows no size of its own

### Requirement: Node inspector shows evidence and its limits
The inspector SHALL show the node's latest descriptor: examined entries, marker results, signals with their cited evidence, errors, and coverage, including the applied budget and stop reason. A truncated listing SHALL be described as "the first N entries in directory order, not a sample". An atomic node SHALL state that its descendants are not cataloged and that deeper content is unknown.

#### Scenario: Truncated listing explained
- **WHEN** the inspector shows a descriptor that stopped at the 256-entry budget
- **THEN** it states that the first 256 entries in directory order were examined, that this is not a representative sample, and that the listing is incomplete

### Requirement: JSON read API
The server SHALL provide `GET /api/sources`, `GET /api/sources/{id}/index-health`, `GET /api/nodes/{id}`, `GET /api/nodes/{id}/children?cursor=` (with `state=history` for inactive children), `GET /api/nodes/{id}/evidence`, `GET /api/nodes/{id}/peek`, `GET /api/nodes/{id}/classifier-preview`, `GET /api/review`, `GET /api/selection-summary`, `GET /api/classifier-profiles`, `GET /api/classifier-usage`, `GET /api/jobs/{id}`, and `GET /api/events`. Node names SHALL be returned both as an escaped display string and as the base64-encoded raw bytes.

#### Scenario: A17 lossless name in API
- **WHEN** a client fetches a node whose name contains invalid UTF-8 bytes
- **THEN** the response carries the exact raw bytes in base64 and an unambiguous escaped display form

#### Scenario: Unknown node
- **WHEN** a client requests a node ID that does not exist
- **THEN** the response is HTTP 404 with code `not_found`

#### Scenario: Node carries policy and intent fields
- **WHEN** a client fetches a node
- **THEN** the response carries its effective category and source, traits, preservation risk, suggested triage, disposition, protection state, current or stale measurement, and current intent revision, for use as the expected revision of owner commands

#### Scenario: Node carries freshness fields
- **WHEN** a client fetches a directory node
- **THEN** the response carries whether it is active, its inactive reason and time if inactive, and its scope's kind, last pass, next due time, dirty state with reasons, and last outcome

#### Scenario: Profiles listed without secrets
- **WHEN** a client fetches `GET /api/classifier-profiles`
- **THEN** each profile shows its adapter, endpoint, model, deployment revision, locality, declared capabilities, and the name of its secret reference, and no response contains a secret value

### Requirement: Unambiguous display names
Display names SHALL be derived from raw name bytes by a reversible escape scheme. Invalid UTF-8 bytes, control characters, and the escape character itself SHALL appear as visible escapes, so two different raw names never share a display name.

#### Scenario: A17 distinct names display distinctly
- **WHEN** one name contains byte `0xE9` and another contains the literal text `\xE9`
- **THEN** their display strings differ

### Requirement: Filesystem-derived values rendered inert
Every filesystem-derived value (names, link text, error text) SHALL be escaped for its output context in HTML and in JSON. No source file content, HTML, SVG, or script SHALL be rendered in the application's origin.

#### Scenario: A15 malicious filename
- **WHEN** a source contains a directory named `<img src=x onerror=alert(1)>`
- **THEN** the explorer and inspector display it as literal text, and no script executes under the page's CSP

### Requirement: Self-contained UI
All scripts, styles, and templates SHALL be embedded in the binary and served from the application's origin. The UI SHALL require no CDN, external font, Node.js runtime, or outbound network access. Interactive behavior SHALL work under a CSP that forbids inline script.

#### Scenario: Offline operation
- **WHEN** the server host has no outbound network access
- **THEN** every screen loads fully, and the browser makes no requests to other origins

### Requirement: Read-only workflow without models
The complete workflow SHALL work with every source mounted read-only and no classifier or network egress configured. The workflow covers logging in, scanning, following progress, browsing, inspecting evidence, rule classification, overrides, protection, refine, collapse, peek, aggregate walks, copy searches, and review.

#### Scenario: A20 read-only and cloud disabled
- **WHEN** the only source is on a read-only mount and no classifier is configured
- **THEN** the owner can log in, scan, browse, inspect, peek, measure, find copies, refine, collapse, override, protect, and review, and no write to the source or outbound request is attempted

### Requirement: Inspector shows classification and owner controls
Beyond the descriptor, the inspector SHALL show the effective classification with its explanation, traits, preservation risk, and suggested triage. It SHALL also show alternative and historical suggestions with their provenance, the owner override, disposition, and protection state, and the current or stale measurement. A model suggestion SHALL show its profile, returned model, result status, typed measures labeled by kind, assessments, and whether it was applied or awaits review. The inspector SHALL offer only the owner controls valid for the node's state, including reclassification where a permission exists.

#### Scenario: Classification explained with provenance
- **WHEN** the owner opens a directory that a rule classified and that also has a stale model-kind suggestion
- **THEN** the inspector shows the rule category with its explanation and cited evidence, and lists the model-kind suggestion as stale, with its observed revision

#### Scenario: Controls follow node state
- **WHEN** the inspector shows an atomic directory
- **THEN** it offers override, disposition, protection, refine, aggregate, and peek, but not collapse; for an expanded non-root directory it offers collapse, but not peek or aggregate

#### Scenario: Model suggestion awaiting review
- **WHEN** the owner opens an `unknown` directory whose current model suggestion is `documents` and was not applied
- **THEN** the inspector shows the suggestion as awaiting review, with its profile, returned model, and measures labeled by kind or "no measures", and offers to accept it as an override and to reclassify

### Requirement: Index health is visible
For each source, the overview, an index-health page, and `GET /api/sources/{id}/index-health` SHALL show the following:
- the notification status: `watching`, `degraded` with its reason and the count of unwatched directories, or `polling_only` with its reason;
- the schedule intervals;
- counts of scopes that are dirty, overdue, failing, or pending in a job;
- the reconciliation lag and the age of the oldest dirty scope;
- the last pass;
- any open coverage gap;
- why dispatch is held, such as unavailable or paused.

#### Scenario: A27 polling-only status is visible
- **WHEN** the notification backend is off and the owner opens the overview
- **THEN** each source states that notifications are off and polling alone keeps it current, together with its lag and its count of overdue scopes

#### Scenario: Failing scope is listed
- **WHEN** an expanded directory's listing fails on every pass with permission denied
- **THEN** the index-health page lists it as failing, with its last outcome, its next retry time, and a link to its node

#### Scenario: Degraded notifications are visible
- **WHEN** the watch budget leaves 40 of a source's expanded directories unwatched
- **THEN** the source shows notifications as `degraded`, with reason `watch_budget` and 40 unwatched directories, on the overview, the index-health page, and the API

### Requirement: Freshness is shown per scope
The inspector SHALL show a directory's scope: when it was last reconciled, when it is next due, whether it is dirty and why, and its last outcome. For an atomic directory it SHALL state "boundary-only monitoring; nested changes may be unobserved". Each row SHALL show as its last observation the later of its own last observation and its parent's latest complete listing.

#### Scenario: A29 boundary-only statement
- **WHEN** the owner opens an atomic directory's inspector
- **THEN** it states "boundary-only monitoring; nested changes may be unobserved", next to the time of the last boundary refresh

#### Scenario: Unchanged entry shows its latest confirmation
- **WHEN** a file is unchanged and its parent was last listed completely ten minutes ago
- **THEN** its row's last observation is ten minutes ago, not the time it was first recorded

### Requirement: Removed entries stay browsable as history
A directory's inactive children SHALL be listed on a history page and through `state=history`, paged like current children, each with its reason (`missing`, `replaced`, `ancestor_gone`, `collapsed`, or `unrecorded`) and time. They SHALL never appear in current listings, totals, or review queues.

#### Scenario: A28 history remains available
- **WHEN** a file has been tombstoned as `missing`
- **THEN** it no longer appears in its parent's children or totals, and it is listed in the parent's history with reason `missing`, its tombstone time, and its last metadata

### Requirement: Classifier settings page
The classifier settings page SHALL show:
- the configured profiles with their declared capabilities, locality, health, and latest operator error;
- the routing policies;
- each source's permission and each inbox's permission per task, with grant and revoke controls and the payload preview;
- spend and usage per profile and task against the caps;
- pending and deferred requests;
- the latest evaluation summaries.

Comparisons SHALL NOT imply that score scales of different profiles are interchangeable.

#### Scenario: Revocation explained on the page
- **WHEN** the owner revokes a source's permission
- **THEN** the page shows the source as not permitted, the count of cancelled requests, and that data already sent cannot be recalled

#### Scenario: Inbox grant previewed on a sample arrival
- **WHEN** the owner opens the `file_kind` grant form of an inbox that holds a loose-file arrival
- **THEN** it shows the exact request body each permitted profile would receive for that file, with no credential in it

### Requirement: Inbox page
Each inbox SHALL have a page that groups its items: settling, queued or classifying, needs review, proposed, and ended. Each item SHALL show its readiness basis and blockers, last change, generation, evidence limits, classifier profile, and proposal with any blocker or staleness. Organization SHALL be shown as unavailable. The page SHALL offer pause, mark ready, retry, ignore, and refine controls, and the median and maximum settling and first-suggestion times of recent items. The overview SHALL show each inbox's group counts.

#### Scenario: A39 arrivals reviewable without models
- **WHEN** an inbox with no classifier has one settling file, one `needs_review` directory, and one `proposed` directory
- **THEN** its page lists each item in its group, with readiness basis, generation, rule result, and proposal or blockers
- **AND** the overview shows the inbox with one item in each of those groups, and every item states that organization is unavailable

### Requirement: Inbox read API
`GET /api/inboxes` SHALL list every inbox with its source, node, state (`active`, `paused`, or `unavailable`), revision, grants, and counts per state. `GET /api/inboxes/{id}/items?state=&cursor=` SHALL page through items in arrival order, optionally limited to one state. Each item has the fields that the inbox page shows. An unknown inbox SHALL return HTTP 404 `not_found`, and an unknown state HTTP 400 `invalid_request`.

#### Scenario: Items filtered by state
- **WHEN** an inbox has 120 settling items and the client requests `state=settling`
- **THEN** it receives the first 100 in arrival order with a cursor, and the next page holds the remaining 20 with no cursor

### Requirement: Inspector shows intake state
The inspector of a node that has an open intake item SHALL show the item's state, readiness basis and blockers, generation, whether its triage is provisional, its proposal, and a link to its inbox. The inspector of an inbox SHALL show the role and its counts.

#### Scenario: Provisional directory arrival
- **WHEN** the owner opens the inspector of a ready directory arrival
- **THEN** it shows the item as provisional, `heuristic_stable`, with its generation and a link to its inbox, and it states that nested content was not inspected

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
