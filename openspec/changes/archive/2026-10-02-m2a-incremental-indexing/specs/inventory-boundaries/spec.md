# Spec Delta

## MODIFIED Requirements

### Requirement: Re-expansion reactivates history
When a listing observes an entry whose parent holds an inactive node with the same raw name, kind, and inode, and, for a mount boundary, the same device, it SHALL reactivate that node instead of creating a new one. The reactivated node keeps its ID, category override, disposition, and owner-set inventory mode, and changed metadata increments its observation revision. An entry whose identity differs SHALL become a new node that inherits no owner assertion except path-scoped protection.

#### Scenario: Same identity after collapse and refine
- **WHEN** the owner collapses `Backup`, then refines it again, and `Backup/Photos` had an owner override and disposition `preserve`
- **THEN** `Backup/Photos` is active again with its previous node ID, its override, and disposition `preserve`

#### Scenario: Replacement at the same name
- **WHEN** `Backup/Photos` was replaced on disk by a different directory before the re-expansion
- **THEN** a new node is created for it with no category override and disposition `unreviewed`, the old node stays inactive history, and a pin on `Backup/Photos` still protects the new node

#### Scenario: Entry returns after it was missing
- **WHEN** a file tombstoned as `missing` is moved back to its old name with the same inode, and its parent is listed again
- **THEN** its old node is active again with its ID and disposition

### Requirement: No hidden catalog beneath atomic units
At every committed state, no active node SHALL have an atomic or inactive ancestor. This SHALL hold through scans, scheduled reconciliation, refreshes, refines, collapses, tombstones, replacements, aggregate walks, peeks, and crashes during any of them.

#### Scenario: Invariant after a mixed sequence
- **WHEN** any sequence of scans, scheduled passes, refreshes, refines, collapses, deletions and replacements on disk, aggregate walks, peeks, and process kills completes and the jobs settle
- **THEN** the inventory holds no active node beneath an atomic or inactive directory
