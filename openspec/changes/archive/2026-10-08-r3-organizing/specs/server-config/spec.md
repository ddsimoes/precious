# Spec Delta

## ADDED Requirements

### Requirement: Writes can be forbidden by the configuration
`[sources] allow_writes` SHALL be a boolean, `true` by default. When it is `false`, no source SHALL allow writes, whatever its stored write permission, and the write permission SHALL be unavailable with the reason `forbidden_by_config` (§6.1). `check-config` SHALL print it among the effective settings. A v0.2 write setting such as `mode` SHALL still be refused as an unknown key.

#### Scenario: Read-only installation
- **WHEN** the configuration sets `sources.allow_writes = false` and a source had writes on
- **THEN** the source reports `writes.unavailable` as `forbidden_by_config`, and no organize job changes anything on it

#### Scenario: Write mode requested with the old key
- **WHEN** the configuration's `[sources]` section contains `mode = "read_write"`
- **THEN** startup fails with an error naming `sources.mode` as an unknown key

## REMOVED Requirements

### Requirement: Write mode unavailable in this release
**Reason**: R3 delivers writes (§14 R3), which this requirement deferred to.
**Migration**: Writes are now per source and off by default (source-writes capability), and `sources.allow_writes = false` forbids them for the whole installation.
