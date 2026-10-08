# server-config Specification

## Purpose

Defines how the operator configures the `curator` server and which unsafe configurations the process refuses to run with, so deployment mistakes fail loudly at startup instead of weakening source or browser safety.

## Requirements

### Requirement: Strict configuration file
The server SHALL load its configuration from a single operator-supplied file and SHALL refuse to start when the file is missing, malformed, contains unknown keys, or contains invalid values. The error SHALL name each offending key.

#### Scenario: Unknown key rejected
- **WHEN** the configuration contains a misspelled key such as `sorces`
- **THEN** startup fails with a non-zero exit status and an error naming `sorces`

#### Scenario: Valid configuration accepted
- **WHEN** `curator check-config` runs against a valid configuration
- **THEN** it exits zero and prints the effective settings, with secret values redacted

### Requirement: Loopback-by-default listener
The listener SHALL default to `127.0.0.1:8080`. A non-loopback listen address SHALL require `allow_non_loopback_listen = true`. An external origin SHALL always be configured. An `https://` origin SHALL be accepted on any host. An `http://` origin, on any host, SHALL be accepted only when `allow_insecure_http = true`, so Precious can run over plain HTTP on a local network (§3).

#### Scenario: Non-loopback bind without opt-in
- **WHEN** the listen address is `0.0.0.0:8080` and the non-loopback opt-in is not set
- **THEN** startup fails with an error explaining the opt-in

#### Scenario: Remote plain HTTP rejected
- **WHEN** the external origin is `http://precious.lan:8080` and `allow_insecure_http` is not set
- **THEN** startup fails with an error explaining that plain HTTP needs `allow_insecure_http`

#### Scenario: Plain HTTP on a local network
- **WHEN** the listen address is `0.0.0.0:8080` with `allow_non_loopback_listen = true`, and the external origin is `http://192.168.1.10:8080` with `allow_insecure_http = true`
- **THEN** the server starts and serves the interface at that origin

### Requirement: Forwarded headers trusted only from allowlisted proxies
The server SHALL honor `Forwarded`, `X-Forwarded-For`, and `X-Forwarded-Proto` only when the immediate peer address is in the configured trusted-proxy list. Otherwise it SHALL ignore those headers when deriving the client address and scheme. The configured external origin, not request headers, SHALL determine the expected origin and cookie security.

#### Scenario: A15 forged forwarded headers ignored
- **WHEN** a client outside the trusted-proxy list sends `X-Forwarded-For: 127.0.0.1` and `X-Forwarded-Proto: https`
- **THEN** throttling and audit records use the client's real peer address, and the expected origin is unchanged

#### Scenario: Trusted proxy honored
- **WHEN** a request arrives from an address in the trusted-proxy list with `X-Forwarded-For: 203.0.113.7`
- **THEN** throttling and audit records attribute the request to `203.0.113.7`

### Requirement: Private state directory
On Linux and macOS, the server SHALL create a missing state directory with owner-only permissions and SHALL create the database and other state files readable and writable only by the service account. It SHALL refuse to start there when an existing state directory is accessible to group or other users. On Windows, this release SHALL NOT check or set these permissions (R8).

#### Scenario: Fresh state directory
- **WHEN** the server starts on Linux with a configured `state_dir` that does not exist
- **THEN** the directory is created with mode `0700`, and the database file has mode `0600`

#### Scenario: Over-permissive state directory
- **WHEN** an existing state directory has mode `0755` on Linux or macOS
- **THEN** startup fails with an error naming the directory and the required mode

#### Scenario: Windows skips the mode check
- **WHEN** the server starts on Windows with an existing state directory
- **THEN** startup does not fail because of the directory's permissions

### Requirement: Allowed roots are validated
Each entry of `[sources] allowed_roots` SHALL be an absolute path to an existing directory. Otherwise the server and `check-config` SHALL fail, naming `sources.allowed_roots` and the offending entry. An absent or empty list SHALL select the platform default roots.

#### Scenario: Relative root rejected
- **WHEN** `allowed_roots` contains `mnt/disks`
- **THEN** startup fails with an error naming `sources.allowed_roots` and `mnt/disks`

#### Scenario: Missing root rejected
- **WHEN** `allowed_roots` contains `/tank` and `/tank` does not exist
- **THEN** startup and `check-config` fail with an error naming `sources.allowed_roots` and `/tank`

#### Scenario: Root that is a file rejected
- **WHEN** `allowed_roots` contains the path of a regular file
- **THEN** startup fails with an error naming `sources.allowed_roots` and that path

#### Scenario: Defaults when absent
- **WHEN** the configuration has no `[sources]` section
- **THEN** startup succeeds, and the picker offers the platform default roots

### Requirement: Scan settings are validated
`[scan] batch_size` SHALL lie between 1 and 10,000, defaulting to 1,000, and `[scan] list_batch` SHALL lie between 1 and 4,096, defaulting to 256. A value out of range SHALL fail startup and `check-config`, naming its key.

#### Scenario: Batch size out of range
- **WHEN** `scan.batch_size` is `0`
- **THEN** startup and `check-config` fail with an error naming `scan.batch_size`

#### Scenario: List batch out of range
- **WHEN** `scan.list_batch` is `5000`
- **THEN** startup fails with an error naming `scan.list_batch`

#### Scenario: Scan defaults
- **WHEN** the configuration has no `[scan]` section
- **THEN** `check-config` prints `batch_size` 1000 and `list_batch` 256

### Requirement: Removed settings fail loudly
A configuration that still contains a section removed in this release (`[[sources]]`, `[inbox]`, `[watch]`, `[reconciliation]`, `[discovery]`, `[inspection]`, `[copies]`, `[classifier]`, `[[classifier_profiles]]`, or `[[classifier_policies]]`) SHALL be refused as containing unknown keys, naming each of them. Removed settings SHALL never be silently ignored.

#### Scenario: v0.2 configuration refused
- **WHEN** `precious check-config` runs against a v0.2 configuration with `[[sources]]`, `[inbox]`, and `[copies]`
- **THEN** it exits non-zero with an error naming `sources`, `inbox`, and `copies`

#### Scenario: Classifier settings refused
- **WHEN** the configuration contains `[classifier]` and `[[classifier_profiles]]`
- **THEN** startup fails with an error naming `classifier` and `classifier_profiles`

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

### Requirement: Writes can be forbidden by the configuration
`[sources] allow_writes` SHALL be a boolean, `true` by default. When it is `false`, no source SHALL allow writes, whatever its stored write permission, and the write permission SHALL be unavailable with the reason `forbidden_by_config` (§6.1). `check-config` SHALL print it among the effective settings. A v0.2 write setting such as `mode` SHALL still be refused as an unknown key.

#### Scenario: Read-only installation
- **WHEN** the configuration sets `sources.allow_writes = false` and a source had writes on
- **THEN** the source reports `writes.unavailable` as `forbidden_by_config`, and no organize job changes anything on it

#### Scenario: Write mode requested with the old key
- **WHEN** the configuration's `[sources]` section contains `mode = "read_write"`
- **THEN** startup fails with an error naming `sources.mode` as an unknown key
