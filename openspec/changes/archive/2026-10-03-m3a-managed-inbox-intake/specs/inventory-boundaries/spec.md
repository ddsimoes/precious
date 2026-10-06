# Spec Delta

## MODIFIED Requirements

### Requirement: Collapse returns a subtree to one unit
`collapse-node` on an expanded directory other than a source root SHALL mark it `atomic` by the owner. In the same transaction it SHALL deactivate every active descendant, so none appears in current listings, totals, or review queues. Deactivated rows SHALL be kept as history. Collapsing a source root, a non-expanded node, an inbox, or a directory above an inbox SHALL fail with HTTP 409 `invalid_node_state`.

#### Scenario: Collapse leaves no active descendants
- **WHEN** the owner collapses an expanded directory with 40 active descendants
- **THEN** it is atomic, no active node lies beneath it, and the 40 rows remain as inactive history

#### Scenario: Collapse during an in-flight expansion
- **WHEN** a collapse commits while a discovery job is still listing a directory inside that subtree
- **THEN** the job's later batches create or reactivate no node beneath the collapsed directory

#### Scenario: A40 collapse above an inbox
- **WHEN** the owner collapses `data` while `data/Incoming` is an inbox
- **THEN** the response is HTTP 409 `invalid_node_state` naming `data/Incoming`, and nothing beneath `data` is deactivated
