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

### Requirement: One active scan per source
While a scan job for a source is queued, running, or paused, a new `start-scan` for that source with a different idempotency key SHALL NOT create a second job. It SHALL resume the paused job, or else return the existing active job ID. `refine-node` and `refresh-scope` SHALL add their work to the active job, resuming it if paused, and return its ID. Scheduled reconciliation SHALL never resume a job. With no active job, each SHALL start a job that processes only its own work.

#### Scenario: Concurrent scan request
- **WHEN** `start-scan` is issued for a source whose scan is running
- **THEN** the response carries the running job's ID, and no second scan job is created

#### Scenario: Refine joins a running scan
- **WHEN** the owner refines a directory while that source's scan is running
- **THEN** the response carries the running scan's job ID, and that job lists and decides the refined directory before finishing

#### Scenario: Refine without an active scan
- **WHEN** the owner refines a directory in a source with no active scan
- **THEN** a job is created that lists the refined directory and decides its children, without re-listing the rest of the source

#### Scenario: Refresh joins a running scan
- **WHEN** the owner sends `refresh-scope` for a directory while that source's scan is running
- **THEN** the response carries the running scan's job ID, and that job reconciles the directory before finishing

### Requirement: Machine-readable command errors
Command failures SHALL return a JSON body with a stable error code. The codes SHALL distinguish at least: `unknown_source`, `source_unconfigured`, `source_unavailable`, `source_identity_changed`, `permission_denied`, `not_found`, `budget_exhausted`, `idempotency_key_reused`, `revision_conflict` (HTTP 409), `protected` (HTTP 409), `invalid_node_state` (HTTP 409), and `stale_evidence` (HTTP 409).

#### Scenario: Scan of unavailable source
- **WHEN** `start-scan` targets a source whose root is missing
- **THEN** the job ends with code `source_unavailable`, or the command is rejected with it, and nothing is recorded as absent

#### Scenario: Collapse of a source root
- **WHEN** `collapse-node` targets a source root
- **THEN** the response is HTTP 409 with code `invalid_node_state`, and nothing changes

### Requirement: Prompt cancellation
`cancel-job` SHALL stop a job from issuing new filesystem operations as soon as the in-progress operation returns. Observations already committed SHALL be kept, with their recorded coverage. If an in-progress operation has not returned within the configured threshold, the job SHALL be shown as `cancel_requested`, and the source SHALL be flagged as unresponsive.

#### Scenario: Cancel mid-scan
- **WHEN** a running scan is cancelled
- **THEN** no further directories are probed, the job ends `cancelled`, and already-probed descriptors remain

#### Scenario: Blocked filesystem call
- **WHEN** a cancelled job's current filesystem call blocks beyond the threshold
- **THEN** the job shows `cancel_requested`, and the source is flagged unresponsive rather than reported cancelled

### Requirement: Bounded filesystem concurrency
At most the configured number of filesystem workers (default 1) SHALL operate concurrently per source device. A job that starts before its source's device is known from a first observation SHALL be the only running job of that source until it finishes, and SHALL start only when no other job of that source is running. UI reads SHALL remain served while jobs run.

#### Scenario: Single worker per device
- **WHEN** two sources on the same device both have scans queued
- **THEN** their filesystem work does not overlap in time

#### Scenario: Device limit before the first observation
- **WHEN** a source's first scan is still running and the owner requests an aggregate walk of a directory that scan has already discovered, with one worker per device
- **THEN** the walk does not start until the scan has finished

### Requirement: Batched progress and resumable events
Job progress SHALL be persisted in batches, not per entry. Job changes SHALL be published as events with monotonically increasing IDs on an authenticated server-sent event stream. A client reconnecting with its last event ID SHALL receive every later event. When that ID has expired, the client SHALL receive a reset signal and reload a fresh snapshot.

#### Scenario: A16 browser disconnects
- **WHEN** the browser tab closes during a scan and reconnects later with its last event ID
- **THEN** the scan has kept running, and the client receives all events after that ID

#### Scenario: Expired event history
- **WHEN** a client reconnects with an event ID older than the retained history
- **THEN** it receives a reset signal instead of a partial event sequence

### Requirement: One active aggregate walk per node
While an aggregate walk for a node is queued, running, or paused, another `inspect-node` aggregate request for that node SHALL NOT create a second job; it SHALL return the active job's ID. Aggregate walks SHALL run under the per-device filesystem worker limit and SHALL be cancellable with `cancel-job`.

#### Scenario: Repeated aggregate request
- **WHEN** the owner requests an aggregate walk twice for the same node with different idempotency keys while the first is running
- **THEN** both responses carry the same job ID, and only one walk runs

### Requirement: Classifier work never holds filesystem workers
Classifier jobs SHALL run in their own bounded worker pool, sized by configuration, and SHALL NOT occupy a per-device filesystem worker or read any source. Scans, refreshes, and aggregate walks SHALL proceed while a provider is slow or unreachable.

#### Scenario: Slow provider, running scan
- **WHEN** every classifier request takes 60 seconds and a scan of the same source is queued, with one filesystem worker per device
- **THEN** the scan runs to completion while classifier jobs wait on the provider, and no more classifier jobs run at once than the pool allows

### Requirement: Deferred jobs resume at their time
A job that waits for a retry time or a budget reset SHALL be stored as queued with that time. It SHALL NOT be claimed earlier, SHALL survive a restart, and SHALL NOT consume an attempt by waiting.

#### Scenario: Restart during backoff
- **WHEN** a classifier job is deferred for 30 seconds and the server restarts 10 seconds later
- **THEN** after the restart the job is still queued, it is claimed no earlier than its deferred time, and its attempt count is unchanged

### Requirement: Priority classes share each device
Each job that uses a filesystem device SHALL belong to a class: `interactive` for inbox intake, `reconciliation` for scans, and `bulk` for aggregate walks and copy searches. When jobs of several classes wait for one device, the device SHALL be granted in a 4:2:1 weighted rotation, oldest job first within a class. A job that has waited longer than 5 minutes SHALL rank as `interactive`. Classifier pool jobs belong to no class.

#### Scenario: A36 inbox work during a bulk walk
- **WHEN** an aggregate walk of a 100,000-entry unit holds the only worker of a device, and a file arrives in an inbox on that device
- **THEN** the arrival is enrolled and triaged before the walk finishes
- **AND** the walk then finishes with the same measurement it would have reached without the arrival

#### Scenario: Inbox work during a copy search
- **WHEN** a copy search is reading a large file on the only worker of a device, and a file arrives in an inbox on that device
- **THEN** the arrival is enrolled and triaged before the search finishes, and the search's results are unchanged by the wait

### Requirement: Long jobs yield between work units
Scans, intake jobs, and aggregate walks SHALL offer their device after each bounded work unit: a probe, a listing batch, or a walk batch. If a waiting job ranks ahead, the offering job SHALL release the device. When the device is granted back, it SHALL continue where it stopped, losing no work and using no attempt. A job that started before its source's device was known SHALL NOT yield.

#### Scenario: A yielded scan continues
- **WHEN** a scan yields to an intake job 10 times while listing a 2,000-entry directory
- **THEN** every entry is cataloged once, the listing completes, and the scan's attempt count is unchanged

### Requirement: No class starves another
While a job of any class waits for a device, it SHALL receive that device at least once in every 7 grants of it.

#### Scenario: A36 continuous arrivals do not starve reconciliation
- **WHEN** files keep arriving in an inbox at every poll while a scan on the same device has 500 pending directories
- **THEN** the scan receives at least one of every 7 grants of the device, and completes

### Requirement: A job holds one device at a time
A job that reads several sources SHALL hold a slot of at most one device at a time. It SHALL move to another source's device only between work units, by releasing its slot and then waiting for a slot of the other device as a waiting job of its class does. Moving between two sources on one device SHALL keep the slot.

#### Scenario: M4b-7 Inbox work on both devices during a cross-source search
- **WHEN** a copy search of `disk` and `old-disk`, which are on different devices with one worker each, reads `old-disk`, and a file arrives in an inbox on each device
- **THEN** the arrival on `disk` is enrolled and triaged without waiting for the search, and the arrival on `old-disk` is triaged before the search finishes

#### Scenario: Two sources on one device
- **WHEN** a copy search moves from `disk` to `photos`, which is on the same device
- **THEN** it keeps its slot and does not wait
