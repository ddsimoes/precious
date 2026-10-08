# Spec Delta

## ADDED Requirements

### Requirement: The quarantine is walked but never counted
A scan SHALL walk `.precious-quarantine` at the top of a source like any folder, so its index rows stay true. It SHALL leave the folder out of everything it counts:
- the totals and breakdowns of the source's top folder and above;
- hashing, which SHALL neither enrol nor read its files;
- size groups and coverage.

The quarantine folder's own row SHALL still fold its bytes (§10.3, ADR 0011).

#### Scenario: A rescan after quarantining
- **WHEN** a folder is quarantined and the source is rescanned
- **THEN** the quarantined entries keep their IDs and state, the scan adds no entry and marks none missing, and the source's totals exclude the quarantined bytes
