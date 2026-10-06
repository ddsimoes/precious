# Spec Delta

## ADDED Requirements

### Requirement: Copy-search settings are validated
The `[copies]` settings SHALL be validated at startup and by `check-config`: the large-file threshold, sample threshold, read chunk size, bytes between yields, entry budget, and reporting minimum each lie within their documented range, and the sample threshold is at least the large-file threshold. A violation SHALL name its key and stop startup.

#### Scenario: Sample threshold below the large-file threshold
- **WHEN** `copies.sample_from_bytes` is smaller than `copies.large_file_bytes`
- **THEN** `check-config` and startup fail with an error naming `copies.sample_from_bytes`

#### Scenario: Defaults load
- **WHEN** the configuration has no `[copies]` section
- **THEN** startup uses the documented defaults, and `check-config` prints them
