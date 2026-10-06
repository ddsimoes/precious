# Spec Delta

## MODIFIED Requirements

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

## ADDED Requirements

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
