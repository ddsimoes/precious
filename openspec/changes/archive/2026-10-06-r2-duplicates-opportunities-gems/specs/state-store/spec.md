# Spec Delta

## ADDED Requirements

### Requirement: R1 databases upgrade in place
Starting the R2 binary against a database created by R1 SHALL apply the R2 migration and keep every source, entry, decision, tag, job record, session, and audit event, so that no rescan is needed. Hashing then starts as for any source with files not yet checked.

#### Scenario: Upgrade keeps the index
- **WHEN** the R2 binary starts against an R1 `precious.db` holding a scanned source with decisions and tags
- **THEN** the R2 migration is recorded, the source's entries keep their IDs, decisions, and tags, and a hashing job starts for the source

#### Scenario: Removing a source removes its content data
- **WHEN** a hashed source with listed archives is removed
- **THEN** its digests, archive listings, relations, and review rows are deleted with its entries, and the other sources' are kept
