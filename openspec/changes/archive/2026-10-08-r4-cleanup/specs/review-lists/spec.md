# Spec Delta

## ADDED Requirements

### Requirement: A review list's discards start a cleanup plan
Every review list SHALL offer to draft a cleanup plan of its rows whose entries are explicitly discarded, on one source. A plan from the duplicates list SHALL follow the duplicate rules of the cleanup capability. Quarantined entries SHALL be no row of any list or card.

#### Scenario: Drafting from the system junk list
- **WHEN** the owner discards three rows of the system junk list and drafts a cleanup plan from the list
- **THEN** the plan holds those three entries, and after it runs the card no longer counts them
