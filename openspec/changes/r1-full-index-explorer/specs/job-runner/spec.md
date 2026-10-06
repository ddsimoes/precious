## MODIFIED Requirements

### Requirement: Long jobs yield between work units
A scan SHALL offer its device after each directory listing batch. When another job is waiting for the same device, the scan SHALL release it. When the device is granted back, the scan SHALL continue where it stopped, losing no work and using no attempt.

#### Scenario: A yielded scan continues
- **WHEN** a scan yields to another source's scan 10 times while listing a 2,000-entry directory
- **THEN** every entry is indexed once, the listing completes, and the scan's attempt count is unchanged

## ADDED Requirements

### Requirement: One scan at a time per source
While a scan job for a source is queued, running, or paused, a new `start-scan` for that source with a different idempotency key SHALL NOT create a second job. It SHALL resume the paused job, or else return the existing active job's ID, with `coalesced` set to true. With no active scan, `start-scan` SHALL queue a new scan job. A `start-scan` for a source that is not online SHALL be rejected with HTTP 409 `source_offline`.

#### Scenario: Concurrent scan request
- **WHEN** `start-scan` is issued for a source whose scan is running
- **THEN** the response carries the running job's ID with `coalesced` true, and no second scan job is created

#### Scenario: Paused scan resumed
- **WHEN** `start-scan` is issued for a source whose scan is paused
- **THEN** that paused job resumes, and the response carries its ID with `coalesced` true

#### Scenario: New scan after the last one ended
- **WHEN** `start-scan` is issued for a source whose previous scan has succeeded
- **THEN** a new scan job is queued, and the response carries its ID with `coalesced` false

#### Scenario: Offline source
- **WHEN** `start-scan` is issued for a source whose volume is not mounted
- **THEN** the response is HTTP 409 with code `source_offline`, and no job is created

### Requirement: Command error codes
Command failures SHALL return a JSON body with a stable error code. The codes SHALL distinguish at least: `unknown_source`, `not_found`, `invalid_request` (HTTP 400), `permission_denied`, `outside_allowed_roots` (HTTP 403), `source_exists`, `source_offline`, `job_active`, `tag_exists`, `selection_expired`, `invalid_entry_state`, `idempotency_key_reused` (each HTTP 409), and `internal`.

#### Scenario: Removing a source while it is scanned
- **WHEN** `remove-source` is sent while that source's scan is running
- **THEN** the response is HTTP 409 with code `job_active`, and the source and its entries remain

#### Scenario: Duplicate tag name
- **WHEN** `create-tag` is sent with a name that an existing tag already has, in any letter case
- **THEN** the response is HTTP 409 with code `tag_exists`, and no tag is created

#### Scenario: Unknown source
- **WHEN** `rename-source` names a source ID that does not exist
- **THEN** the response is HTTP 404 with code `unknown_source`

### Requirement: Per-device filesystem workers
At most the configured number of filesystem workers (default 1) SHALL operate concurrently per device. A source's device SHALL be its volume's device: the pool for ZFS, the block device for UUID-identified volumes. Jobs of sources on one device SHALL share that device's workers. While a source's device is unknown, the source SHALL count as a device of its own. UI reads SHALL remain served while jobs run.

#### Scenario: Single worker per device
- **WHEN** two sources on the same ZFS pool both have scans queued, with one worker per device
- **THEN** their filesystem work does not overlap in time

#### Scenario: Different devices run in parallel
- **WHEN** two sources on different block devices both have scans queued
- **THEN** both scans can run at the same time

#### Scenario: Reads during a scan
- **WHEN** a scan holds the only worker of a device
- **THEN** Map, Search, and Home requests are still answered

## REMOVED Requirements

### Requirement: One active scan per source
**Reason**: `refine-node`, `refresh-scope`, and scheduled reconciliation are removed (ADR 0008); a scan now always walks the whole source.
**Migration**: See Requirement: One scan at a time per source.

### Requirement: Machine-readable command errors
**Reason**: The v0.2 codes (`source_unconfigured`, `source_identity_changed`, `budget_exhausted`, `revision_conflict`, `protected`, `invalid_node_state`, `stale_evidence`) belong to removed commands and concepts.
**Migration**: See Requirement: Command error codes.

### Requirement: Bounded filesystem concurrency
**Reason**: The device is now the source's volume device rather than a device learned from a first observation; aggregate walks are removed.
**Migration**: See Requirement: Per-device filesystem workers.

### Requirement: Priority classes share each device
**Reason**: In R1 only scans use a device, and all scans share one class, so weighted sharing between classes is not observable (ADR 0008 removed inbox intake, aggregate walks, and copy searches). Device classes return with the milestones that reintroduce such jobs: R2 hashing and copy search, R6 classifier.
**Migration**: None.

### Requirement: One active aggregate walk per node
**Reason**: Aggregate walks are removed (ADR 0008); every scan computes folder aggregates in the same pass.
**Migration**: Use `start-scan`; folder totals come from the index.

### Requirement: Classifier work never holds filesystem workers
**Reason**: R1 has no classifier jobs; the classifier stack is rebuilt in R6.
**Migration**: None in R1; R6 restates the classifier pool requirement.

### Requirement: Deferred jobs resume at their time
**Reason**: No R1 job defers to a retry time or a budget reset, so the behavior is not observable. It returns with the milestones that reintroduce deferring jobs: R2 hashing and copy search, R6 classifier.
**Migration**: None.

### Requirement: No class starves another
**Reason**: In R1 only scans use a device, all in one class, so starvation between classes is not observable. It returns with the milestones that reintroduce other job classes: R2 hashing and copy search, R6 classifier.
**Migration**: None.

### Requirement: A job holds one device at a time
**Reason**: No R1 job reads several sources, so the behavior is not observable. It returns with the milestones that reintroduce cross-source jobs: R2 hashing and copy search, R6 classifier.
**Migration**: None.
