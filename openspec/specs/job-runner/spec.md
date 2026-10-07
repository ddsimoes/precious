# job-runner Specification

## Purpose

Runs long work as durable, recoverable jobs, and exposes state-changing commands with idempotency, explicit error codes, and resumable progress events. Work never depends on an open browser, and repeated requests never duplicate effects.

## Requirements

### Requirement: Durable job lifecycle
Jobs SHALL be persisted with a kind, a payload version, and a state of `queued`, `running`, `paused`, `succeeded`, `failed`, or `cancelled`, plus attempts, progress, and a terminal reason. Job state SHALL survive process restarts.

#### Scenario: Job visible after restart
- **WHEN** a scan job is queued and the server restarts before it runs
- **THEN** the job is still queued after the restart and then runs

### Requirement: Leases and crash recovery
A running job SHALL hold a lease with an expiry that its worker renews. A job whose lease expired, or whose worker was lost at restart, SHALL be requeued with its attempt count incremented. After the configured maximum attempts it SHALL be marked `failed` with reason `attempts_exhausted`.

#### Scenario: Crashed worker recovered
- **WHEN** the process is killed while a scan is running and is then restarted
- **THEN** the job returns to `queued` with one more attempt and later resumes

#### Scenario: Attempts exhausted
- **WHEN** a job's worker dies on every attempt up to the maximum
- **THEN** the job ends `failed` with `attempts_exhausted` and is not retried again

### Requirement: Long-running commands return a job
A command that starts long-running work SHALL return HTTP 202 with the durable job ID. `GET /api/jobs/{id}` SHALL return the job's state, progress counters, and terminal reason.

#### Scenario: Start scan accepted
- **WHEN** `POST /api/commands/start-scan` is accepted for an available source
- **THEN** the response is HTTP 202 with a job ID that `GET /api/jobs/{id}` resolves

### Requirement: Idempotent commands
Every command SHALL carry an idempotency key. Repeating a key with an identical payload SHALL return the original outcome without new effects. Reusing a key with a different payload SHALL return HTTP 409 with code `idempotency_key_reused`.

#### Scenario: Retried command after lost response
- **WHEN** a client resends `start-scan` with the same idempotency key after losing the first response
- **THEN** it receives the original job ID, and only one job exists

#### Scenario: Key reused with different payload
- **WHEN** a key previously used for source `a` is sent with source `b`
- **THEN** the response is HTTP 409 with code `idempotency_key_reused`

### Requirement: Prompt cancellation
`cancel-job` SHALL stop a job from issuing new filesystem operations as soon as the in-progress operation returns. Observations already committed SHALL be kept, with their recorded coverage. If an in-progress operation has not returned within the configured threshold, the job SHALL be shown as `cancel_requested`, and the source SHALL be flagged as unresponsive.

#### Scenario: Cancel mid-scan
- **WHEN** a running scan is cancelled
- **THEN** no further directories are probed, the job ends `cancelled`, and already-probed descriptors remain

#### Scenario: Blocked filesystem call
- **WHEN** a cancelled job's current filesystem call blocks beyond the threshold
- **THEN** the job shows `cancel_requested`, and the source is flagged unresponsive rather than reported cancelled

### Requirement: Batched progress and resumable events
Job progress SHALL be persisted in batches, not per entry. Job changes SHALL be published as events with monotonically increasing IDs on an authenticated server-sent event stream. A client reconnecting with its last event ID SHALL receive every later event. When that ID has expired, the client SHALL receive a reset signal and reload a fresh snapshot.

#### Scenario: A16 browser disconnects
- **WHEN** the browser tab closes during a scan and reconnects later with its last event ID
- **THEN** the scan has kept running, and the client receives all events after that ID

#### Scenario: Expired event history
- **WHEN** a client reconnects with an event ID older than the retained history
- **THEN** it receives a reset signal instead of a partial event sequence

### Requirement: Long jobs yield between work units
A scan SHALL offer its device after each directory listing batch. When another job is waiting for the same device, the scan SHALL release it. When the device is granted back, the scan SHALL continue where it stopped, losing no work and using no attempt.

#### Scenario: A yielded scan continues
- **WHEN** a scan yields to another source's scan 10 times while listing a 2,000-entry directory
- **THEN** every entry is indexed once, the listing completes, and the scan's attempt count is unchanged

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
