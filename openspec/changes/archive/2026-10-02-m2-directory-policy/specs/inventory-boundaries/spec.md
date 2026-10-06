# Spec Delta

## Purpose

Lets the owner move the point where cataloging stops: expanding an atomic directory by one level, or collapsing an expanded subtree back into one atomic unit. Every move keeps the atomic-boundary contract, node identities, and owner decisions intact.

## ADDED Requirements

### Requirement: Owner refine expands one level
`refine-node` on an active atomic directory SHALL mark it `expanded` by the owner, catalog its immediate entries, and probe and classify each child directory before any further descent. It SHALL return HTTP 202 with the discovery job ID. Refining a file, a symlink, a mount boundary, or a directory that is not atomic SHALL fail with HTTP 409 `invalid_node_state`.

#### Scenario: A3 refine a mixed backup
- **WHEN** the owner refines atomic `Backup2003`, which holds `GetRight` (with `setup.exe` and `uninstall.exe`), `Camera` (with `DCIM`), and `proj` (with `.git` and `go.mod`)
- **THEN** exactly `Backup2003`'s immediate entries become active nodes, `GetRight` is classified `application_installation`, `Camera` `personal_media`, and `proj` `source_project`, each stays atomic, and none of their descendants is cataloged

#### Scenario: Children do not inherit the parent's verdict
- **WHEN** the owner refines a directory whose effective category is `cache` and whose disposition is `cleanup_candidate`
- **THEN** its child directories receive their own classifications, its loose files receive only a name-derived kind hint, and every new child starts `unreviewed` with no inherited category

#### Scenario: Refining a mount boundary
- **WHEN** the owner refines a directory recorded as a mount boundary
- **THEN** the response is HTTP 409 `invalid_node_state`, and nothing is listed

### Requirement: Collapse returns a subtree to one unit
`collapse-node` on an expanded directory other than a source root SHALL mark it `atomic` by the owner. In the same transaction it SHALL deactivate every active descendant, so none appears in current listings, totals, or review queues. Deactivated rows SHALL be kept as history. Collapsing a source root or a non-expanded node SHALL fail with HTTP 409 `invalid_node_state`.

#### Scenario: Collapse leaves no active descendants
- **WHEN** the owner collapses an expanded directory with 40 active descendants
- **THEN** it is atomic, no active node lies beneath it, and the 40 rows remain as inactive history

#### Scenario: Collapse during an in-flight expansion
- **WHEN** a collapse commits while a discovery job is still listing a directory inside that subtree
- **THEN** the job's later batches create or reactivate no node beneath the collapsed directory

### Requirement: Re-expansion reactivates history
When a listing observes an entry whose parent holds an inactive node with the same raw name, kind, device, and inode, it SHALL reactivate that node instead of creating a new one. The reactivated node keeps its ID, category override, disposition, and owner-set inventory mode, and changed metadata increments its observation revision. An entry whose identity differs SHALL become a new node that inherits no owner assertion except path-scoped protection.

#### Scenario: Same identity after collapse and refine
- **WHEN** the owner collapses `Backup`, then refines it again, and `Backup/Photos` had an owner override and disposition `preserve`
- **THEN** `Backup/Photos` is active again with its previous node ID, its override, and disposition `preserve`

#### Scenario: Replacement at the same name
- **WHEN** `Backup/Photos` was replaced on disk by a different directory before the re-expansion
- **THEN** a new node is created for it with no category override and disposition `unreviewed`, the old node stays inactive history, and a pin on `Backup/Photos` still protects the new node

### Requirement: Owner inventory modes are sticky
Policy SHALL never change an inventory mode set by the owner. A rescan SHALL re-list every owner-expanded directory and SHALL keep every owner-collapsed directory atomic. Policy SHALL never collapse any directory. It may only expand a policy-decided atomic directory.

#### Scenario: Owner-collapsed mixed directory stays atomic
- **WHEN** the owner collapses a directory that policy had expanded as `mixed`, and a later scan again classifies it `mixed`
- **THEN** it stays atomic, and no descendant is cataloged

#### Scenario: Policy does not collapse
- **WHEN** a policy-expanded directory is classified as a coherent unit on a later scan
- **THEN** it stays expanded, and its descendants stay active

### Requirement: No hidden catalog beneath atomic units
At every committed state, no active node SHALL have an atomic or inactive ancestor. This SHALL hold through scans, refines, collapses, aggregate walks, peeks, and crashes during any of them.

#### Scenario: Invariant after a mixed sequence
- **WHEN** any sequence of scans, refines, collapses, aggregate walks, peeks, and process kills completes and the jobs settle
- **THEN** the inventory holds no active node beneath an atomic or inactive directory
