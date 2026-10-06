# Spec Delta

## ADDED Requirements

### Requirement: Hashing settings are validated
`[hashing] read_chunk_bytes` SHALL lie between 64 KiB and 16 MiB, defaulting to 1 MiB, and `[hashing] yield_bytes` between 1 MiB and 1 GiB, defaulting to 64 MiB. A value out of range SHALL fail startup and `check-config`, naming its key.

#### Scenario: Hashing defaults printed
- **WHEN** the configuration has no `[hashing]` section
- **THEN** `check-config` prints `read_chunk_bytes` 1048576 and `yield_bytes` 67108864

#### Scenario: Chunk out of range
- **WHEN** `hashing.read_chunk_bytes` is `4096`
- **THEN** startup and `check-config` fail with an error naming `hashing.read_chunk_bytes`

### Requirement: Archive settings are validated
`[archives]` SHALL accept `max_members` from 1 to 5,000,000 (default 1,000,000), `max_unpacked_bytes` from 1 MiB to 16 TiB (default 1 TiB), `max_ratio` from 2 to 100,000 (default 100), `max_time` from 1 minute to 7 days (default 4 hours), and `view_max_bytes` from 1 MiB to 1 GiB (default 64 MiB). A value out of range SHALL fail startup and `check-config`, naming its key.

#### Scenario: Ratio out of range
- **WHEN** `archives.max_ratio` is `1`
- **THEN** startup and `check-config` fail with an error naming `archives.max_ratio`

#### Scenario: Archive defaults printed
- **WHEN** the configuration has no `[archives]` section
- **THEN** `check-config` prints every archive setting with its default

### Requirement: Duplicates settings are validated
`[duplicates] refresh_interval` SHALL lie between 1 minute and 24 hours, defaulting to 10 minutes. A value out of range SHALL fail startup and `check-config`, naming the key. The v0.2 section `[copies]` SHALL still be refused as removed.

#### Scenario: Interval out of range
- **WHEN** `duplicates.refresh_interval` is `"10s"`
- **THEN** startup fails with an error naming `duplicates.refresh_interval`

#### Scenario: v0.2 copies section still refused
- **WHEN** the configuration contains the v0.2 section `[copies]`
- **THEN** startup fails with an error naming `copies`
