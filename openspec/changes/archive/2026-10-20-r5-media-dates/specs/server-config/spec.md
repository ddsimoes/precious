# Spec Delta

## ADDED Requirements

### Requirement: The dates time zone is validated
`[dates] time_zone` SHALL name an IANA time zone, such as `America/Sao_Paulo`, available in every build whatever the host has installed. Absent or empty, it SHALL be the server's local zone, and `check-config` and the server's start SHALL warn that it is unset. An unknown name SHALL stop startup and `check-config` with an error naming the key. `check-config` SHALL print the effective zone.

#### Scenario: An unknown zone
- **WHEN** the configuration sets `time_zone = "Mars/Olympus"` under `[dates]`
- **THEN** `precious check-config` fails naming `dates.time_zone`, and `precious serve` does not start

#### Scenario: An unset zone
- **WHEN** the configuration has no `[dates]` section
- **THEN** `precious check-config` succeeds, prints the server's local zone, and warns that `dates.time_zone` is unset

#### Scenario: A zone on a host without zone files
- **WHEN** the container image, which has no zone database, sets `time_zone = "America/Sao_Paulo"`
- **THEN** the server starts and reads floating capture times in that zone
