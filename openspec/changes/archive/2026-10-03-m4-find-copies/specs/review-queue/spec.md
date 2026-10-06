# Spec Delta

## MODIFIED Requirements

### Requirement: Review queues
The server SHALL offer four queues, paged by cursor through `GET /api/review?queue=&source=&cursor=` and a review page. The first three hold active, classified atomic directories:
- `cleanup`: suggested triage `cleanup_candidate`, disposition `unreviewed`;
- `preserve`: preservation risk, protection, or suggested triage `preserve`, disposition `unreviewed`;
- `ambiguous`: `unknown`, atomic `mixed` or `download_collection`, conflicting rules, or an override with changed evidence; disposition `unreviewed` or `review`;
- `marked`: active nodes of any kind with disposition `cleanup_candidate`, each with its copy evidence when it has one.

#### Scenario: Installation in the cleanup queue
- **WHEN** a scan classifies an unprotected directory as `application_installation` with no preservation indicator
- **THEN** it appears in the `cleanup` queue

#### Scenario: Risky installation goes to preserve, not cleanup
- **WHEN** an installation carries preservation risk from a `saves` entry
- **THEN** it appears in the `preserve` queue and not in the `cleanup` queue

#### Scenario: Decided units leave the queues
- **WHEN** the owner sets disposition `preserve` on a unit in the `preserve` queue
- **THEN** it no longer appears in any queue

#### Scenario: Marked copy listed with its evidence
- **WHEN** the owner marks the expanded directory `fotos-b` as `cleanup_candidate` with a copy result
- **THEN** it appears in the `marked` queue with "inside `fotos`", the result's freshness, and the search time, and in no other queue
