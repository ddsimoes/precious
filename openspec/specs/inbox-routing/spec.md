# inbox-routing Specification

## Purpose

Suggests where an inbox arrival belongs. Suggestions come from destination collections the owner names and from deterministic routing rules. Each proposal cites its rule and the revisions it rests on, and anything that prevents it is shown as an explicit blocker, never resolved by guessing.

## Requirements

### Requirement: Destination collections
`set-routing-destination` SHALL add, relabel, or remove a destination, which is an active expanded directory with a label. It SHALL be refused with HTTP 409 `invalid_node_state` for anything else, for an inbox, and for a directory inside an inbox. Each change SHALL carry the expected routing revision, increment it, and be audited.

#### Scenario: Destination inside an inbox refused
- **WHEN** the owner names `Incoming/Photos` as a destination while `Incoming` is an inbox
- **THEN** the response is HTTP 409 `invalid_node_state`, and no destination is added

#### Scenario: Atomic destination refused
- **WHEN** the owner names the atomic directory `Projects` as a destination
- **THEN** the response is HTTP 409 `invalid_node_state` saying it must be refined first, and nothing is expanded

### Requirement: Routing rules
`set-routing-rule` SHALL add or remove a rule. A rule maps one effective category, or one file kind, to one destination with a priority, optionally only for one inbox. For an item, the matching rules of the highest priority SHALL decide. Equal-priority matches that name different destinations SHALL be a conflict. Each change SHALL carry the expected routing revision, increment it, and be audited.

#### Scenario: Higher priority wins
- **WHEN** rule `documents -> Documents` has priority 10, rule `documents -> Scans` limited to inbox `Scanner` has priority 20, and a `documents` arrival settles in `Scanner`
- **THEN** its proposal names `Scans` and cites the priority-20 rule

#### Scenario: Equal-priority conflict goes to review
- **WHEN** two priority-10 rules map `archive` to `Downloads` and to `Needs manual sorting`, and an archive arrives
- **THEN** the item is `needs_review`, with a conflict that names both rules, and has no valid proposal

### Requirement: Proposals come only from rules
A triaged item SHALL get a proposal only when the rules yield exactly one destination. The proposal SHALL name the destination parent, the arrival's own name as the basename, and the rule. It SHALL also record the item generation, destination revisions, and routing revision it used. If no rule matches, the item SHALL stay in `needs_review`. No destination SHALL ever come from a name, a date, or a model answer.

#### Scenario: Source project proposed
- **WHEN** the rule `source_project -> Projects` exists, and the arrival `OldProject/` is triaged as `source_project`
- **THEN** the item is `proposed`, with destination `Projects`, basename `OldProject`, and that rule cited

#### Scenario: No matching rule
- **WHEN** an arrival is triaged as `unknown`, and no rule maps `unknown`
- **THEN** the item is `needs_review` with no proposal

### Requirement: Blockers are explicit
A proposal SHALL carry a blocker and SHALL NOT be valid when its destination:
- already holds an active entry of the same name;
- is inactive or was replaced;
- is atomic;
- is an inbox or lies inside one;
- lies in a different source from the arrival.

The server SHALL never resolve a blocker by renaming, merging, expanding, or choosing another destination on its own.

#### Scenario: A37 destination conflicts and changes
- **WHEN** five arrivals are routed to five destinations: one holding an entry with the same name, one replaced on disk, one the owner collapsed, one that became an inbox, and one in another source
- **THEN** the items show the blockers `name_conflict`, `destination_unavailable`, `destination_atomic`, `destination_in_inbox`, and `cross_source`, and none of them has a valid proposal
- **AND** nothing is expanded, renamed, merged, or written

### Requirement: Proposals follow their inputs
A proposal SHALL become stale when the item's generation, the destination's state, or the routing revision changes. The next intake pass SHALL recompute it. Until then it SHALL be shown as stale, never as valid.

#### Scenario: Rule removed after a proposal
- **WHEN** the rule behind a proposal is removed
- **THEN** the proposal is shown as stale until the next intake pass, after which the item is `needs_review`

### Requirement: Proposals never move anything
No proposal, rule, readiness basis, or model answer SHALL write, rename, move, or delete anything in a source. Every item SHALL show organization as unavailable, with the reason.

#### Scenario: A39 read-only inbox with a proposal
- **WHEN** an inbox on a read-only mount has a `proposed` item whose handoff the owner confirmed
- **THEN** the item shows its proposal and states that organization is unavailable, because the source is read-only and organization is not part of this release
- **AND** no filesystem write is attempted
