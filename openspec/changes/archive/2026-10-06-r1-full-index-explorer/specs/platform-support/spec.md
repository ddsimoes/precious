## Purpose

Defines the operating systems and architectures Precious builds and runs on, and how platform differences are contained so the product behaves the same everywhere (§3, §12). R8 extends it with native macOS and Windows backends.

## ADDED Requirements

### Requirement: Builds for every supported platform
Every change SHALL build the `precious` binary for linux, darwin, and windows, each on amd64 and arm64. Continuous integration SHALL build all six targets on every change and SHALL fail the change when any target fails to build. In R1, only Linux runs the acceptance suite.

#### Scenario: R1.15 Every change builds for six targets
- **WHEN** continuous integration runs on a change
- **THEN** it builds `precious` for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, and windows/arm64
- **AND** it runs the acceptance suite on Linux

#### Scenario: One broken target fails the change
- **WHEN** a change compiles on Linux but not for windows/arm64
- **THEN** continuous integration fails the change and names the failing target

### Requirement: Linux is the complete platform in this release
On Linux, Precious SHALL discover mounts from the system's mount table, derive each volume's identity, and detect each filesystem's capabilities from its type, so that a source on a recognized filesystem reports a volume identity other than its mount point and capabilities with `known: true`.

#### Scenario: Source on a filesystem with a UUID
- **WHEN** a source is added on Linux on an ext4 filesystem whose block device has a filesystem UUID
- **THEN** `GET /api/sources` reports its `volume.kind` as `uuid`, `volume.strong` as true, and `capabilities.known` as true

#### Scenario: Source on a ZFS dataset
- **WHEN** a source is added on Linux on a ZFS dataset
- **THEN** `GET /api/sources` reports its `volume.kind` as `zfs`, `volume.id` as the dataset name, and `volume.strong` as true

### Requirement: Other platforms run with conservative capabilities
On macOS and Windows, `precious serve` SHALL start without any Linux-only facility, and sources SHALL be added, scanned, browsed, searched, and viewed through the portable backend. Every source there SHALL report volume kind `path` with `strong: false` and the conservative unknown capabilities with `known: false`. The interface SHALL show that such a source is recognized only at its current location.

#### Scenario: Conservative source on macOS
- **WHEN** a source is added on macOS
- **THEN** `GET /api/sources` reports its `volume.kind` as `path`, `volume.strong` as false, and capabilities with `known: false`, `case_sensitive: false`, `stable_identity: false`, and `time_resolution_ns: 2000000000`

#### Scenario: Scan on Windows
- **WHEN** a source is scanned on Windows
- **THEN** every file and folder is indexed with folder totals, no symlink is followed, no special file is opened, and no nested mount is crossed

#### Scenario: Weak identity is visible
- **WHEN** the Sources screen shows a source whose volume is not strong
- **THEN** it states that the source is recognized only at its current location

### Requirement: Platform differences stay behind one layer
The JSON API SHALL expose the same endpoints, response fields, field types, and error codes on every platform. Platform differences SHALL appear only as values of fields the API already carries, such as a source's volume and capabilities.

#### Scenario: Same response shapes on every platform
- **WHEN** `GET /api/sources`, `GET /api/entries/{id}`, and `GET /api/search` are called on a Linux server and on a Windows server holding the same tree
- **THEN** both servers answer with the same fields and field types, and differ only in values such as the volume and capabilities

#### Scenario: Same error codes on every platform
- **WHEN** content is requested for an entry of an offline source on macOS
- **THEN** the response is `409 source_offline`, as on Linux
