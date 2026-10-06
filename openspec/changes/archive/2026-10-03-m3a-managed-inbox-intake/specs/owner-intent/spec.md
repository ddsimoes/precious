# Spec Delta

## MODIFIED Requirements

### Requirement: Protection blocks cleanup designation
A node that is explicitly or inheritedly protected, that contains a protected path, or that is an inbox or lies above one, SHALL NOT receive disposition `cleanup_candidate`. A single-node request SHALL fail with HTTP 409 code `protected`. In a bulk request the node SHALL be excluded and reported, without blocking the other items.

#### Scenario: Ancestor of a protected path
- **WHEN** the owner marks `Backup` as `cleanup_candidate` while `Backup/Photos` is pinned
- **THEN** the response is HTTP 409 with code `protected` naming the pinned path, and `Backup`'s disposition is unchanged

#### Scenario: A40 inbox cannot become a cleanup candidate
- **WHEN** the owner marks the inbox `data/Incoming`, or `data`, as `cleanup_candidate`
- **THEN** the response is HTTP 409 with code `protected` naming the inbox, and no disposition changes
