# Spec Delta

## MODIFIED Requirements

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

## ADDED Requirements

### Requirement: Classifier settings page
The classifier settings page SHALL show:
- the configured profiles with their declared capabilities, locality, health, and latest operator error;
- the routing policies;
- each source's permission, with grant and revoke controls and the payload preview;
- spend and usage per profile and task against the caps;
- pending and deferred requests;
- the latest evaluation summaries.

Comparisons SHALL NOT imply that score scales of different profiles are interchangeable.

#### Scenario: Revocation explained on the page
- **WHEN** the owner revokes a source's permission
- **THEN** the page shows the source as not permitted, the count of cancelled requests, and that data already sent cannot be recalled
