# Spec Delta

## ADDED Requirements

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
