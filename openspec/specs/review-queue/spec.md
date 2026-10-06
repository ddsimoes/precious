# review-queue Specification

## Purpose

Gives the owner queues of whole units awaiting a decision, and bulk decisions over exact, visible selections. Review stays fast without ever acting on a changing live query or on protected material.

## Requirements

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

### Requirement: Bulk selection is an exact list
A bulk command SHALL take an explicit list of node IDs, at most the page size, each with its expected intent revision. The server SHALL NOT accept a query, filter, or "select all matching" as a selection. A listed node whose revision changed SHALL be reported as a revision conflict and left unchanged.

#### Scenario: Live query changes after the page loads
- **WHEN** a new unit joins the `cleanup` queue after the owner loaded the page and submitted a bulk decision for the listed rows
- **THEN** only the listed rows are changed, and the new unit stays `unreviewed`

### Requirement: Selection preview before confirmation
Before a bulk decision is confirmed, the review page SHALL show:
- the exact selected objects, with their source and path;
- the normalized selection scope;
- size totals by size class;
- the protected items that would be excluded, and why.

The preview SHALL be served by `GET /api/selection-summary` and SHALL change nothing.

#### Scenario: Preview lists excluded protected items
- **WHEN** the owner selects five units for `cleanup_candidate` and one of them contains a pinned path
- **THEN** the preview shows the five units, marks that one as excluded with the pinned path as the reason, and totals the remaining four

### Requirement: Per-item bulk outcomes
A bulk disposition SHALL report one outcome per distinct listed node: `applied`, `excluded_protected`, `revision_conflict`, or `not_found`. It SHALL apply every eligible item even when others are excluded. Replaying it with the same idempotency key SHALL return the original outcomes without new effects.

#### Scenario: Mixed bulk outcome
- **WHEN** a bulk `cleanup_candidate` request lists three unprotected units, one protected unit, and one unit changed by another tab
- **THEN** three are `applied`, one is `excluded_protected`, one is `revision_conflict`, and only the three changed

### Requirement: Review works without models
The queues SHALL be populated from rules and owner decisions alone. The review workflow SHALL work with no classifier configured, no network egress, and every source mounted read-only.

#### Scenario: Review on a read-only source without models
- **WHEN** the only source is on a read-only mount and no classifier is configured
- **THEN** after a scan, the queues contain rule-classified units, and bulk dispositions succeed without any write to the source
