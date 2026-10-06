# Spec Delta

## Purpose

Defines how the operator configures the `curator` server and which unsafe configurations the process refuses to run with, so deployment mistakes fail loudly at startup instead of weakening source or browser safety.

## ADDED Requirements

### Requirement: Strict configuration file
The server SHALL load its configuration from a single operator-supplied file and SHALL refuse to start when the file is missing, malformed, contains unknown keys, or contains invalid values. The error SHALL name each offending key.

#### Scenario: Unknown key rejected
- **WHEN** the configuration contains a misspelled key such as `sorces`
- **THEN** startup fails with a non-zero exit status and an error naming `sorces`

#### Scenario: Valid configuration accepted
- **WHEN** `curator check-config` runs against a valid configuration
- **THEN** it exits zero and prints the effective settings, with secret values redacted

### Requirement: Unsafe source layouts rejected
The server SHALL refuse to start when any two configured source roots are equal or nested (compared after resolving the operator-supplied paths), when two sources share an ID, when a root is not an absolute directory path, or when the state directory is inside a source root or contains one.

#### Scenario: Nested roots rejected
- **WHEN** one source root is `/data` and another is `/data/photos`
- **THEN** startup fails with an error naming both sources

#### Scenario: State directory inside a source rejected
- **WHEN** the state directory is `/data/.curator` and `/data` is a source root
- **THEN** startup fails, and no database file is created

### Requirement: Write mode unavailable in this release
The server SHALL accept only read-only source mode. Any configuration that requests write, quarantine, or purge capability SHALL be rejected at startup until a later milestone delivers those capabilities.

#### Scenario: Write mode requested
- **WHEN** a source is configured with `mode = "read_write"`
- **THEN** startup fails with an error stating that write mode is not supported by this release

### Requirement: Loopback-by-default listener
The listener SHALL default to `127.0.0.1:8080`. A non-loopback listen address SHALL require an explicit opt-in setting. An external origin SHALL always be configured. A plain-HTTP origin SHALL be accepted only when its host is loopback and an explicit insecure-local-mode setting is enabled.

#### Scenario: Non-loopback bind without opt-in
- **WHEN** the listen address is `0.0.0.0:8080` and the non-loopback opt-in is not set
- **THEN** startup fails with an error explaining the opt-in

#### Scenario: Remote plain HTTP rejected
- **WHEN** the external origin is `http://curator.example.net`
- **THEN** startup fails because plain HTTP is only allowed for a loopback origin in explicit local mode

### Requirement: Forwarded headers trusted only from allowlisted proxies
The server SHALL honor `Forwarded`, `X-Forwarded-For`, and `X-Forwarded-Proto` only when the immediate peer address is in the configured trusted-proxy list. Otherwise it SHALL ignore those headers when deriving the client address and scheme. The configured external origin, not request headers, SHALL determine the expected origin and cookie security.

#### Scenario: A15 forged forwarded headers ignored
- **WHEN** a client outside the trusted-proxy list sends `X-Forwarded-For: 127.0.0.1` and `X-Forwarded-Proto: https`
- **THEN** throttling and audit records use the client's real peer address, and the expected origin is unchanged

#### Scenario: Trusted proxy honored
- **WHEN** a request arrives from an address in the trusted-proxy list with `X-Forwarded-For: 203.0.113.7`
- **THEN** throttling and audit records attribute the request to `203.0.113.7`

### Requirement: Private state directory
The server SHALL create a missing state directory with owner-only permissions and SHALL create the database and other state files readable and writable only by the service account. It SHALL refuse to start when an existing state directory is accessible to group or other users.

#### Scenario: Fresh state directory
- **WHEN** the server starts with a state directory that does not exist
- **THEN** the directory is created with mode `0700`, and the database file has mode `0600`

#### Scenario: Over-permissive state directory
- **WHEN** an existing state directory has mode `0755`
- **THEN** startup fails with an error naming the directory and the required mode
