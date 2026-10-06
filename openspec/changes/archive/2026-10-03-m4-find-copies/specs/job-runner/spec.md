# Spec Delta

## MODIFIED Requirements

### Requirement: Priority classes share each device
Each job that uses a filesystem device SHALL belong to a class: `interactive` for inbox intake, `reconciliation` for scans, and `bulk` for aggregate walks and copy searches. When jobs of several classes wait for one device, the device SHALL be granted in a 4:2:1 weighted rotation, oldest job first within a class. A job that has waited longer than 5 minutes SHALL rank as `interactive`. Classifier pool jobs belong to no class.

#### Scenario: A36 inbox work during a bulk walk
- **WHEN** an aggregate walk of a 100,000-entry unit holds the only worker of a device, and a file arrives in an inbox on that device
- **THEN** the arrival is enrolled and triaged before the walk finishes
- **AND** the walk then finishes with the same measurement it would have reached without the arrival

#### Scenario: Inbox work during a copy search
- **WHEN** a copy search is reading a large file on the only worker of a device, and a file arrives in an inbox on that device
- **THEN** the arrival is enrolled and triaged before the search finishes, and the search's results are unchanged by the wait
