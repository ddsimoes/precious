# Spec Delta

## MODIFIED Requirements

### Requirement: Overview screen
The overview SHALL show, per source: availability, identity status, read-only mount state, last scan time, active jobs, counts of inventory units by coverage state, units per effective category, and the number of units in each review queue. Totals SHALL be shown by size class (measured, lower bound, stale, and units of unknown size), never as one figure unless the other classes are empty.

#### Scenario: Unknown sizes stay visible
- **WHEN** a source has 12 loose files of known size and 30 unmeasured atomic directories
- **THEN** the overview shows the known file bytes labeled as partial, alongside "30 units of unknown size", and no total presented as the source's size

#### Scenario: Queue and category counts
- **WHEN** a scan has classified 8 installations, 3 caches, and 2 `unknown` directories, with no owner decisions yet
- **THEN** the overview shows those category counts, 11 units in the `cleanup` queue, and 2 in the `ambiguous` queue, each linking to its queue

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

### Requirement: JSON read API
The server SHALL provide `GET /api/sources`, `GET /api/nodes/{id}`, `GET /api/nodes/{id}/children?cursor=`, `GET /api/nodes/{id}/evidence`, `GET /api/nodes/{id}/peek`, `GET /api/review`, `GET /api/selection-summary`, `GET /api/jobs/{id}`, and `GET /api/events`. Node names SHALL be returned both as an escaped display string and as the base64-encoded raw bytes.

#### Scenario: A17 lossless name in API
- **WHEN** a client fetches a node whose name contains invalid UTF-8 bytes
- **THEN** the response carries the exact raw bytes in base64 and an unambiguous escaped display form

#### Scenario: Unknown node
- **WHEN** a client requests a node ID that does not exist
- **THEN** the response is HTTP 404 with code `not_found`

#### Scenario: Node carries policy and intent fields
- **WHEN** a client fetches a node
- **THEN** the response carries its effective category and source, traits, preservation risk, suggested triage, disposition, protection state, current or stale measurement, and current intent revision, for use as the expected revision of owner commands

### Requirement: Read-only workflow without models
The complete workflow SHALL work with every source mounted read-only and no classifier or network egress configured. The workflow covers logging in, scanning, following progress, browsing, inspecting evidence, rule classification, overrides, protection, refine, collapse, peek, aggregate walks, and review.

#### Scenario: A20 read-only and cloud disabled
- **WHEN** the only source is on a read-only mount and no classifier is configured
- **THEN** the owner can log in, scan, browse, inspect, peek, measure, refine, collapse, override, protect, and review, and no write to the source or outbound request is attempted

## ADDED Requirements

### Requirement: Inspector shows classification and owner controls
Beyond the descriptor, the inspector SHALL show the effective classification with its explanation, traits, preservation risk, and suggested triage. It SHALL also show alternative and historical suggestions with their provenance, the owner override, disposition, and protection state, and the current or stale measurement. It SHALL offer only the owner controls valid for the node's state.

#### Scenario: Classification explained with provenance
- **WHEN** the owner opens a directory that a rule classified and that also has a stale model-kind suggestion
- **THEN** the inspector shows the rule category with its explanation and cited evidence, and lists the model-kind suggestion as stale, with its observed revision

#### Scenario: Controls follow node state
- **WHEN** the inspector shows an atomic directory
- **THEN** it offers override, disposition, protection, refine, aggregate, and peek, but not collapse; for an expanded non-root directory it offers collapse, but not peek or aggregate
