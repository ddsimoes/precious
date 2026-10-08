# Spec Delta

## ADDED Requirements

### Requirement: Scans skip the quarantine folder
A scan SHALL neither list nor descend into `.precious-quarantine` at the top of a source, and SHALL neither mark its index rows missing nor change them. Folder totals and breakdowns SHALL leave the quarantine folder out, from the source's top folder up. Hashing SHALL neither enrol nor read files below it (§10.3, "excluded from scans").

#### Scenario: A rescan after quarantining
- **WHEN** a folder is quarantined and the source is rescanned
- **THEN** the quarantined entries keep their IDs and state, the scan adds no entry and marks none missing, and the source's totals exclude the quarantined bytes
