# Spec Delta

## Purpose

Presents the discovered inventory to the owner through server-rendered screens and a JSON read API that show observed evidence, coverage, and unknowns honestly, render every filesystem-derived value safely, and work without any external network dependency.

## ADDED Requirements

### Requirement: Overview screen
The overview SHALL show, per source: availability, identity status, read-only mount state, last scan time, active jobs, and counts of inventory units by coverage state. Known byte totals SHALL be labeled as covering only measured files, with the number of units of unknown size shown beside them.

#### Scenario: Unknown sizes stay visible
- **WHEN** a source has 12 loose files of known size and 30 atomic directories
- **THEN** the overview shows the known file bytes labeled as partial, alongside "30 units of unknown size", and no total presented as the source's size

### Requirement: Paginated explorer
The explorer SHALL list a node's current children in pages of the configured size (default 100), ordered by raw name bytes, using an opaque cursor. Each row SHALL show the display name, physical kind, inventory mode, coverage state, known size or `unknown`, and last observation time, plus markers for symlink, mount boundary, and issues. The page SHALL also show the escaped source-relative path.

#### Scenario: Cursor pagination
- **WHEN** a directory has 250 current children and the client follows `next` cursors
- **THEN** it receives pages of 100, 100, and 50 children, with no duplicates or omissions

#### Scenario: Atomic row
- **WHEN** an atomic directory is listed
- **THEN** its row shows `atomic`, size `unknown`, and no expansion control that would catalog its descendants

### Requirement: Node inspector shows evidence and its limits
The inspector SHALL show the node's latest descriptor: examined entries, marker results, signals with their cited evidence, errors, and coverage, including the applied budget and stop reason. A truncated listing SHALL be described as "the first N entries in directory order, not a sample". An atomic node SHALL state that its descendants are not cataloged and that deeper content is unknown.

#### Scenario: Truncated listing explained
- **WHEN** the inspector shows a descriptor that stopped at the 256-entry budget
- **THEN** it states that the first 256 entries in directory order were examined, that this is not a representative sample, and that the listing is incomplete

### Requirement: JSON read API
The server SHALL provide `GET /api/sources`, `GET /api/nodes/{id}`, `GET /api/nodes/{id}/children?cursor=`, `GET /api/nodes/{id}/evidence`, `GET /api/jobs/{id}`, and `GET /api/events`. Node names SHALL be returned both as an escaped display string and as the base64-encoded raw bytes.

#### Scenario: A17 lossless name in API
- **WHEN** a client fetches a node whose name contains invalid UTF-8 bytes
- **THEN** the response carries the exact raw bytes in base64 and an unambiguous escaped display form

#### Scenario: Unknown node
- **WHEN** a client requests a node ID that does not exist
- **THEN** the response is HTTP 404 with code `not_found`

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
The complete M1 workflow (log in, start a scan, follow progress, browse the explorer, and inspect evidence) SHALL work with every source mounted read-only and no classifier or network egress configured.

#### Scenario: A20 read-only and cloud disabled
- **WHEN** the only source is on a read-only mount and no classifier is configured
- **THEN** the owner can log in, scan, browse the explorer, and inspect descriptors, and no write or outbound request is attempted
