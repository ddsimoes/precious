# Spec Delta

## ADDED Requirements

### Requirement: Priority classes share each device
Each job that uses a filesystem device SHALL belong to a class: `interactive` for inbox intake, `reconciliation` for scans, and `bulk` for aggregate walks. When jobs of several classes wait for one device, the device SHALL be granted in a 4:2:1 weighted rotation, oldest job first within a class. A job that has waited longer than 5 minutes SHALL rank as `interactive`. Classifier pool jobs belong to no class.

#### Scenario: A36 inbox work during a bulk walk
- **WHEN** an aggregate walk of a 100,000-entry unit holds the only worker of a device, and a file arrives in an inbox on that device
- **THEN** the arrival is enrolled and triaged before the walk finishes
- **AND** the walk then finishes with the same measurement it would have reached without the arrival

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
