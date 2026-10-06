# Spec Delta

## Purpose

Manages the operator-configured source roots curator may observe, tracking each source's filesystem identity, continuity epoch, and availability, so inventory is never silently attributed to a different disk mounted at the same path.

## ADDED Requirements

### Requirement: Sources come only from operator configuration
Each source SHALL be defined in the configuration with a stable ID, label, absolute root, and mode. No HTTP endpoint SHALL accept a filesystem path to create, alter, or select a source. Clients SHALL refer to sources only by configured ID.

#### Scenario: Browser cannot register a path
- **WHEN** an authenticated client sends a command whose payload contains an absolute path where a source ID is expected
- **THEN** the command is rejected as an unknown source, and no filesystem access occurs

### Requirement: Unconfigured sources keep their history
A source that disappears from the configuration SHALL be marked `unconfigured` rather than deleted. Its inventory SHALL stay browsable as history. Scans of it SHALL be refused.

#### Scenario: Source removed from configuration
- **WHEN** the server restarts after a source entry is removed from the configuration
- **THEN** the source is listed as `unconfigured`, its nodes remain browsable, and `start-scan` for it fails with `source_unconfigured`

### Requirement: Filesystem identity and source epoch
On a source's first successful observation, the system SHALL record the root's filesystem identity: device number, filesystem type and ID, mount identity where available, and root inode. It SHALL set the source epoch to 1. Every later scan SHALL compare the current identity with the recorded one before observing anything else.

#### Scenario: First observation records identity
- **WHEN** a newly configured source is scanned for the first time
- **THEN** its identity fields are recorded and its epoch is 1

### Requirement: Changed identity requires reconfirmation
When a source's current identity differs from the recorded one, the system SHALL set the source to `needs_reconfirmation`, refuse scans with `source_identity_changed`, and keep the existing inventory as history. Only `curator source confirm <id>` SHALL accept the new identity; doing so SHALL increment the source epoch and record an audit event.

#### Scenario: Different disk at the same path
- **WHEN** a different filesystem is mounted at a configured root and a scan is requested
- **THEN** no entries are listed, the source shows `needs_reconfirmation`, and the scan fails with `source_identity_changed`

#### Scenario: Operator reconfirms
- **WHEN** the operator runs `curator source confirm old-disk` for a source needing reconfirmation
- **THEN** the new identity is recorded, the epoch increments by one, and scans are allowed again

#### Scenario: Results bound to the starting epoch
- **WHEN** the source epoch changes while a discovery job is running
- **THEN** the job's remaining results are not applied to the inventory, and the job fails with `source_epoch_changed`

### Requirement: Source availability is explicit
A source SHALL be `available` only when its root can be opened with the recorded identity. A missing root, I/O error, stale or disconnected mount, or permission failure SHALL make the source `unavailable` and record the reason. Its inventory SHALL stay browsable as history, labeled unavailable, and SHALL never be shown as empty.

#### Scenario: A5 source disappears
- **WHEN** a source's backing disk is detached and the overview is loaded
- **THEN** the source shows `unavailable` with its reason, and its previously discovered nodes remain listed as historical

#### Scenario: Source returns
- **WHEN** the same filesystem is reattached and a scan is requested
- **THEN** the source becomes `available` again with an unchanged epoch

### Requirement: Read-only mount is reported
For each available source, the system SHALL report whether its mount is read-only as an observed capability, separately from the configured mode.

#### Scenario: A20 read-only mount reported
- **WHEN** a source root is on a read-only mount
- **THEN** `GET /api/sources` reports the mount as read-only, and discovery proceeds normally
