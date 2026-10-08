# Spec Delta

## ADDED Requirements

### Requirement: A quarantined copy is not a copy elsewhere
Copies inside a source's quarantine folder SHALL count for nothing. They are not listed among a file's copies, not counted in duplicate groups or relations, and not used for the duplicated share (§10.6). A file whose only other copy was quarantined SHALL read as having no other copy once relations are computed again.

#### Scenario: Quarantining one of two copies
- **WHEN** one of two identical files is quarantined and relations are computed again
- **THEN** the other file reads "No other copy", and the duplicates card no longer lists the pair
