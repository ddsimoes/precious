# Spec Delta

## MODIFIED Requirements

### Requirement: Removing a source deletes its index
`remove-source` SHALL delete the source with its entries, folder aggregates, decisions, tag assignments, and history of actions. It SHALL leave the files on disk and every other source untouched. It SHALL fail with `409 job_active`, and change nothing, while a scan of the source is queued, running, or paused, or while an action of the source is queued or running. It SHALL fail with `409 recovery_needed` while an item of the source has its intent recorded or awaits manual recovery, so the record of a step in flight is never deleted.

#### Scenario: Source removed
- **WHEN** the owner removes a scanned source with decisions and tags
- **THEN** the response is `200`, the source and its entries no longer appear in any list or search, no file on its volume is changed, and the tags themselves remain available

#### Scenario: Active scan blocks removal
- **WHEN** `remove-source` names a source whose scan is running
- **THEN** the request fails with `409 job_active`, and the source and its index are unchanged

#### Scenario: A move in progress blocks removal
- **WHEN** `remove-source` names a source with a queued move, or with an item awaiting manual recovery
- **THEN** the request fails with `409 job_active` or `409 recovery_needed`, and the source, its index, and its history are unchanged
