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

### Requirement: Write mode unavailable in this release
No configuration key, command, or endpoint SHALL enable writes to a source in this release; writes arrive in a later milestone (R3). A v0.2 write setting such as `mode` SHALL be refused as an unknown key.

#### Scenario: Write mode requested
- **WHEN** the configuration's `[sources]` section contains `mode = "read_write"`
- **THEN** startup fails with an error naming `sources.mode` as an unknown key

#### Scenario: No write toggle offered
- **WHEN** the owner reads `GET /api/sources`
- **THEN** no source reports a write permission, and no command exists to turn one on

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
