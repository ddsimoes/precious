# Spec Delta

## ADDED Requirements

### Requirement: Organizing leaves the quarantine alone
`.precious-quarantine` at the top of a source SHALL be a reserved name:
- `plan-rename` and `plan-create-folder` SHALL refuse it there with `400 invalid_request`;
- every plan SHALL refuse the quarantine folder and anything below it as a target (`in_quarantine`) or as a destination (`400 invalid_request`);
- quarantined entries SHALL leave quarantine only through restore and purge.

#### Scenario: A quarantined file cannot be moved
- **WHEN** the owner plans a move of a quarantined file, or into the quarantine folder
- **THEN** the file's item is refused as `in_quarantine`, or the plan fails with `400 invalid_request`, and nothing moves
