# Spec Delta

## MODIFIED Requirements

### Requirement: JSON read API
The server SHALL provide `GET /api/sources`, `GET /api/sources/{id}/index-health`, `GET /api/nodes/{id}`, `GET /api/nodes/{id}/children?cursor=` (with `state=history` for inactive children), `GET /api/nodes/{id}/evidence`, `GET /api/nodes/{id}/peek`, `GET /api/review`, `GET /api/selection-summary`, `GET /api/jobs/{id}`, and `GET /api/events`. Node names SHALL be returned both as an escaped display string and as the base64-encoded raw bytes.

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

## ADDED Requirements

### Requirement: Index health is visible
For each source, the overview, an index-health page, and `GET /api/sources/{id}/index-health` SHALL show the following: notification status, which is polling only in this release; the schedule intervals; counts of scopes that are dirty, overdue, failing, or pending in a job; the reconciliation lag; the age of the oldest dirty scope; the last pass; any open coverage gap; and why dispatch is held, such as unavailable or paused.

#### Scenario: A27 polling-only status is visible
- **WHEN** the owner opens the overview
- **THEN** each source states that notifications are off and polling alone keeps it current, together with its lag and its count of overdue scopes

#### Scenario: Failing scope is listed
- **WHEN** an expanded directory's listing fails on every pass with permission denied
- **THEN** the index-health page lists it as failing, with its last outcome, its next retry time, and a link to its node

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
