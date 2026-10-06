# Spec Delta

## ADDED Requirements

### Requirement: Inbox and notification settings are validated
The server SHALL refuse to start, naming the offending key, when any of these is out of range:
- an inbox interval, or a quiet interval below 5 s;
- a temporary-name pattern that is not a valid name pattern;
- a notification backend other than `auto` or `off`;
- a coalescing window longer than the maximum delay;
- a negative watch budget.

#### Scenario: Quiet interval too short
- **WHEN** `inbox.quiet_interval` is `2s`
- **THEN** startup fails with an error naming `inbox.quiet_interval`

#### Scenario: Invalid temporary-name pattern
- **WHEN** `inbox.temporary_patterns` contains `[.part`
- **THEN** startup fails with an error naming `inbox.temporary_patterns`
