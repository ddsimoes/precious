# Spec Delta

## Purpose

Runs long work as durable, recoverable jobs, and exposes state-changing commands with idempotency, explicit error codes, and resumable progress events. Work never depends on an open browser, and repeated requests never duplicate effects.

## ADDED Requirements

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

### Requirement: One active scan per source
While a scan job for a source is queued, running, or paused, a new `start-scan` for that source with a different idempotency key SHALL NOT create a second job. It SHALL resume the paused job, or else return the existing active job ID.

#### Scenario: Concurrent scan request
- **WHEN** `start-scan` is issued for a source whose scan is running
- **THEN** the response carries the running job's ID, and no second scan job is created

### Requirement: Machine-readable command errors
Command failures SHALL return a JSON body with a stable error code. The codes SHALL distinguish at least: `unknown_source`, `source_unconfigured`, `source_unavailable`, `source_identity_changed`, `permission_denied`, `not_found`, `budget_exhausted`, `idempotency_key_reused`, and `revision_conflict` (HTTP 409).

#### Scenario: Scan of unavailable source
- **WHEN** `start-scan` targets a source whose root is missing
- **THEN** the job ends with code `source_unavailable`, or the command is rejected with it, and nothing is recorded as absent

### Requirement: Prompt cancellation
`cancel-job` SHALL stop a job from issuing new filesystem operations as soon as the in-progress operation returns. Observations already committed SHALL be kept, with their recorded coverage. If an in-progress operation has not returned within the configured threshold, the job SHALL be shown as `cancel_requested`, and the source SHALL be flagged as unresponsive.

#### Scenario: Cancel mid-scan
- **WHEN** a running scan is cancelled
- **THEN** no further directories are probed, the job ends `cancelled`, and already-probed descriptors remain

#### Scenario: Blocked filesystem call
- **WHEN** a cancelled job's current filesystem call blocks beyond the threshold
- **THEN** the job shows `cancel_requested`, and the source is flagged unresponsive rather than reported cancelled

### Requirement: Bounded filesystem concurrency
At most the configured number of filesystem workers (default 1) SHALL operate concurrently per source device. UI reads SHALL remain served while jobs run.

#### Scenario: Single worker per device
- **WHEN** two sources on the same device both have scans queued
- **THEN** their filesystem work does not overlap in time

### Requirement: Batched progress and resumable events
Job progress SHALL be persisted in batches, not per entry. Job changes SHALL be published as events with monotonically increasing IDs on an authenticated server-sent event stream. A client reconnecting with its last event ID SHALL receive every later event. When that ID has expired, the client SHALL receive a reset signal and reload a fresh snapshot.

#### Scenario: A16 browser disconnects
- **WHEN** the browser tab closes during a scan and reconnects later with its last event ID
- **THEN** the scan has kept running, and the client receives all events after that ID

#### Scenario: Expired event history
- **WHEN** a client reconnects with an event ID older than the retained history
- **THEN** it receives a reset signal instead of a partial event sequence
