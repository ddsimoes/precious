# Spec Delta

## MODIFIED Requirements

### Requirement: Changed evidence flags an override
When a later descriptor of an overridden directory, probed with the same entry budget as the descriptor recorded with the override, has a different digest, the override SHALL stay effective and SHALL be flagged for owner review. A descriptor from a probe with a different budget, such as the escalated probe, SHALL NOT flag it. The override SHALL NOT be replaced or cleared automatically.

#### Scenario: Directory contents changed after override
- **WHEN** a directory overridden as `documents` is probed again and its examined entries now differ
- **THEN** it is still `documents` with source `owner`, it is flagged "evidence changed since your decision", and it appears in the ambiguous review queue

#### Scenario: Escalated probe does not flag
- **WHEN** an `unknown` directory is overridden after its escalated probe, and its next probe uses the normal budget and examines the same entries as the normal-budget probe before the override
- **THEN** the override is not flagged
