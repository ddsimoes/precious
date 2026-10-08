# Spec Delta

## ADDED Requirements

### Requirement: Organizing leaves the quarantine alone
`.precious-quarantine` at the top of a source SHALL be reserved:
- **The name.** No plan and no executor rename SHALL give an entry that name there.
- **Destinations.** Every plan SHALL refuse a destination in the quarantine with `400 invalid_request`.
- **Quarantined entries.** They SHALL be refused as targets (`in_quarantine`), except by `plan-move` with a single `entry_id` and a destination outside the quarantine. That is how the owner moves a file out before a purge (§10.6).

`plan-undo` SHALL answer `409 action_not_undoable` for cleanup, restore, and purge actions; restore is how a cleanup is reversed.

#### Scenario: A quarantined file cannot be moved in bulk or into quarantine
- **WHEN** the owner plans a bulk move that includes a quarantined file, or any move into the quarantine folder
- **THEN** the file's item is refused as `in_quarantine`, or the plan fails with `400 invalid_request`, and nothing moves

#### Scenario: Moving a file out of quarantine
- **WHEN** the owner moves one quarantined photo to a folder outside the quarantine
- **THEN** it moves there with its ID, it is no longer in quarantine, and a check that held it is stale
