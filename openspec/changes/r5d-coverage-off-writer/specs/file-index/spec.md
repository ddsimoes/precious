# Spec Delta

## ADDED Requirements

### Requirement: Coverage is recomputed without holding the writer
Recomputing a source's coverage from its rows SHALL NOT hold the database writer while the aggregate is computed. Other writes SHALL be able to commit meanwhile. A coverage change committed during the recomputation SHALL be kept, so the result equals a full recomputation at the moment it is written (§7, I7).

#### Scenario: Signing in while coverage is recomputed
- **WHEN** a hashing job is computing the coverage aggregate of every source
- **THEN** a sign-in, a session refresh, or a decision commits without waiting for the aggregate to finish

#### Scenario: A hashing commit during the recomputation is kept
- **WHEN** a hashing job commits a newly checked file after the aggregate was read and before its result is written
- **THEN** coverage afterwards counts that file as checked, and equals a full recomputation of the rows
