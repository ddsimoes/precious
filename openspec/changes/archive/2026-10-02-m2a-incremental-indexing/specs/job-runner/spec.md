# Spec Delta

## MODIFIED Requirements

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

### Requirement: Bounded filesystem concurrency
At most the configured number of filesystem workers (default 1) SHALL operate concurrently per source device. A job that starts before its source's device is known from a first observation SHALL be the only running job of that source until it finishes, and SHALL start only when no other job of that source is running. UI reads SHALL remain served while jobs run.

#### Scenario: Single worker per device
- **WHEN** two sources on the same device both have scans queued
- **THEN** their filesystem work does not overlap in time

#### Scenario: Device limit before the first observation
- **WHEN** a source's first scan is still running and the owner requests an aggregate walk of a directory that scan has already discovered, with one worker per device
- **THEN** the walk does not start until the scan has finished
