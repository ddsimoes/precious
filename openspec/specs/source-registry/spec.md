# source-registry Specification

## Purpose

Manages the operator-configured source roots curator may observe, tracking each source's filesystem identity, continuity epoch, and availability, so inventory is never silently attributed to a different disk mounted at the same path.

## Requirements

### Requirement: Source availability is explicit
A source SHALL be in exactly one state, reported with its reason: `online` when its volume is mounted and its root opens; `offline` when its volume is not mounted; `unavailable` when the volume is mounted but the root is missing, or fails with an I/O, stale or disconnected mount, or permission error. A source that is not `online` SHALL keep its index browsable, never shown as empty, and `start-scan` for it SHALL fail with `409 source_offline`.

#### Scenario: A5 source disappears
- **WHEN** a source's backing disk is detached and the sources list is loaded
- **THEN** the source shows `offline` with its reason, and its indexed entries remain listed

#### Scenario: Source returns
- **WHEN** the same volume is attached again and the sources list is loaded
- **THEN** the source shows `online` again, with the same ID and entries

#### Scenario: Root folder gone
- **WHEN** a source's volume is mounted but the source's folder has been deleted or cannot be opened
- **THEN** the source shows `unavailable` with its reason, its entries remain listed, and `start-scan` fails with `409 source_offline`

### Requirement: Read-only mount is reported
For each source, `GET /api/sources` SHALL report the filesystem capabilities detected for its volume as observed facts: read-only, case sensitivity, name normalization sensitivity, stable file identity, timestamp resolution, whether times are stored in local time, hard links, and whether the capabilities are known. When they cannot be detected, the source SHALL report `known` as false with conservative values.

#### Scenario: A20 read-only mount reported
- **WHEN** a source root is on a read-only mount
- **THEN** `GET /api/sources` reports `capabilities.read_only` as true, and scanning proceeds normally

#### Scenario: FAT capabilities
- **WHEN** a source is on a `vfat` filesystem on Linux
- **THEN** its capabilities report case-insensitive names, no stable identity, a 2-second time resolution, local time, and `known` as true

#### Scenario: Unknown filesystem
- **WHEN** a source is on a filesystem type Precious does not recognize, or on macOS or Windows in this release
- **THEN** its capabilities report `known` as false, case-insensitive names, no stable identity, and a 2-second time resolution

### Requirement: Sources are added only through the picker
A source SHALL be added only by `add-source` naming a picker handle. Handles SHALL be opaque, SHALL designate only folders inside the allowed roots, and SHALL stop being accepted when the server restarts. No endpoint SHALL accept a typed filesystem path to add, select, or browse a source. A new source SHALL get an ID derived from its label, made unique with a numeric suffix, and adding it SHALL NOT start a scan.

#### Scenario: Source added from a handle
- **WHEN** the owner sends `add-source` with a valid handle for the folder `Fotos` and no label
- **THEN** the response is `201` with `{"source":…}` whose label is `Fotos` and whose ID is `fotos`, and no scan job is queued

#### Scenario: Same label gets a distinct ID
- **WHEN** a second source is added with the label `Fotos` on another volume
- **THEN** its ID is `fotos-2`, and the first source is unchanged

#### Scenario: R1.18 Raw path refused
- **WHEN** a client sends `add-source` or `GET /api/picker?handle=…` with a raw path such as `/etc` or `C:\Windows` in place of a handle
- **THEN** the request fails with `400 invalid_request`, no source is created, and the path is not opened

#### Scenario: R1.18 Location outside the allowed roots refused
- **WHEN** a request names a handle whose folder resolves outside every allowed root, for example a symlink inside an allowed root that points to `/etc`
- **THEN** `add-source` and `GET /api/picker?handle=…` fail with `403 outside_allowed_roots`, and no source is created

#### Scenario: Handle expires on restart
- **WHEN** a handle obtained before a server restart is sent after it
- **THEN** the request fails with `400 invalid_request`

### Requirement: Picker browses folders within the allowed roots
`GET /api/picker` SHALL list the allowed roots as items with handles. `GET /api/picker?handle=H` SHALL list the folder `H` designates and its child folders, never files, at most 1,000 of them, with `truncated` set when more exist. Each item SHALL say whether it is already a source.

#### Scenario: Browsing a root
- **WHEN** the owner opens the picker and selects the allowed root `/mnt`
- **THEN** `GET /api/picker` returns `/mnt` among `roots`, and `GET /api/picker?handle=…` for it returns its child folders, each with its own handle

#### Scenario: Large folder truncated
- **WHEN** a folder in the picker has 1,500 child folders
- **THEN** the listing returns 1,000 of them with `"truncated":true`

#### Scenario: Files not listed
- **WHEN** a folder in the picker contains files and folders
- **THEN** only the folders are listed

### Requirement: Allowed roots default per platform
When `[sources] allowed_roots` is absent or empty, the allowed roots SHALL be the platform defaults, keeping only those that exist: on Linux the home folder of the account running Precious, `/media`, `/mnt`, `/run/media`, and `/srv`; on macOS that home folder and `/Volumes`; on Windows the user profile folder and every drive letter. A configured list SHALL replace the defaults entirely. Roots SHALL be resolved through symlinks once at startup.

#### Scenario: Linux defaults
- **WHEN** Precious starts on Linux without `allowed_roots`, and `/mnt` and `/media` exist but `/srv` does not
- **THEN** the picker's roots include the home folder, `/mnt`, and `/media`, and not `/srv`

#### Scenario: Configured roots replace the defaults
- **WHEN** the configuration sets `allowed_roots = ["/tank"]`
- **THEN** the picker lists only `/tank`, and a handle for a folder under `/mnt` is refused with `403 outside_allowed_roots`

### Requirement: Sources are located by volume identity
A source SHALL be located by its volume identity plus its folder's path inside that volume, never by an absolute path alone. The volume kind SHALL be `uuid` (filesystem UUID), `zfs` (dataset), `fsid` (btrfs filesystem ID), or `path` (mount point, when no other identity exists). `GET /api/sources` SHALL report `volume.strong` as `true` for the first three and `false` for `path`. A `path` source SHALL be recognized only at the same mount point.

#### Scenario: ZFS dataset identity
- **WHEN** a folder on the ZFS dataset `tank/fotos` is added as a source
- **THEN** the source reports volume kind `zfs`, ID `tank/fotos`, and `"strong":true`

#### Scenario: Filesystem UUID identity
- **WHEN** a folder on an ext4 USB disk with a filesystem UUID is added as a source
- **THEN** the source reports volume kind `uuid` with that UUID and `"strong":true`

#### Scenario: Weak identity
- **WHEN** a folder on a filesystem with no UUID or dataset identity is added as a source
- **THEN** the source reports volume kind `path` and `"strong":false`, and the same filesystem mounted at another mount point is not recognized as this source

#### Scenario: Portable platforms
- **WHEN** a source is added on macOS or Windows in this release
- **THEN** it reports volume kind `path` and `"strong":false`

### Requirement: Overlapping sources are refused
`add-source` SHALL fail with `409 source_exists` when the chosen folder, on the same volume as an existing source, equals that source's root, lies inside it, or contains it. A folder that is the state directory, lies inside it, or contains it SHALL be refused with `403 outside_allowed_roots`. These checks SHALL hold for concurrent requests.

#### Scenario: Same folder twice
- **WHEN** `/mnt/disk` is a source and `add-source` names `/mnt/disk` again
- **THEN** the request fails with `409 source_exists`

#### Scenario: Nested folder refused
- **WHEN** `/mnt/disk` is a source and `add-source` names `/mnt/disk/Fotos`
- **THEN** the request fails with `409 source_exists`

#### Scenario: Enclosing folder refused
- **WHEN** `/mnt/disk/Fotos` is a source and `add-source` names `/mnt/disk`
- **THEN** the request fails with `409 source_exists`

#### Scenario: Concurrent additions
- **WHEN** two `add-source` requests for the same folder arrive at the same time
- **THEN** exactly one succeeds with `201`, and the other fails with `409 source_exists`

#### Scenario: State directory refused
- **WHEN** `add-source` names the folder that contains the state directory
- **THEN** the request fails with `403 outside_allowed_roots`, and no source is created

### Requirement: Offline sources stay browsable
While a source is `offline` or `unavailable`, its entries, folder aggregates, decisions, and tags SHALL stay readable through the children, treemap, search, entry detail, and Home endpoints. Operations that need the disk SHALL fail with `409 source_offline`: `start-scan`, `GET /api/entries/{id}/content`, and `GET /api/entries/{id}/text`.

#### Scenario: R1.16 Unmounted volume stays browsable and searchable
- **WHEN** on Linux, the volume of a scanned source is unmounted and availability is refreshed
- **THEN** the source shows `offline`, its children and search results return the same entries with their decisions and tags, and content, text, and `start-scan` requests fail with `409 source_offline`

#### Scenario: R1.16 Remounted at a different path is recognized
- **WHEN** on Linux, that volume is mounted again at a different mount point and availability is refreshed
- **THEN** the source shows `online` with the new `mount_point`, the same source ID, and the same entry IDs, decisions, and tags, and a rescan of the unchanged tree creates no new entries

#### Scenario: Different disk at the old mount point
- **WHEN** a different filesystem is mounted where an offline source's volume used to be
- **THEN** the source stays `offline`, and none of the new filesystem's entries appear under it

### Requirement: Renaming a source changes only its label
`rename-source` SHALL change only the source's label. Its ID, location, entries, decisions, and tags SHALL stay unchanged. An unknown source ID SHALL fail with `404 unknown_source`.

#### Scenario: Source renamed
- **WHEN** the owner renames source `fotos` to `Blue USB stick`
- **THEN** the response is `200` with the new label, the ID is still `fotos`, and its entries, decisions, and tags are unchanged

#### Scenario: Unknown source
- **WHEN** `rename-source` names a source ID that does not exist
- **THEN** the request fails with `404 unknown_source`

### Requirement: Removing a source deletes its index
`remove-source` SHALL delete the source with its entries, folder aggregates, decisions, tag assignments, and history of actions. It SHALL leave the files on disk and every other source untouched. It SHALL fail with `409 job_active`, and change nothing, while a scan of the source is queued, running, or paused, or while an action of the source is queued or running. It SHALL fail with `409 recovery_needed` while an item of the source has its intent recorded or awaits manual recovery, so the record of a step in flight is never deleted.

#### Scenario: Source removed
- **WHEN** the owner removes a scanned source with decisions and tags
- **THEN** the response is `200`, the source and its entries no longer appear in any list or search, no file on its volume is changed, and the tags themselves remain available

#### Scenario: Active scan blocks removal
- **WHEN** `remove-source` names a source whose scan is running
- **THEN** the request fails with `409 job_active`, and the source and its index are unchanged

#### Scenario: A move in progress blocks removal
- **WHEN** `remove-source` names a source with a queued move, or with an item awaiting manual recovery
- **THEN** the request fails with `409 job_active` or `409 recovery_needed`, and the source, its index, and its history are unchanged

### Requirement: Availability is refreshed periodically and on listing
Source availability SHALL be re-evaluated every minute and whenever `GET /api/sources` is read. A refresh SHALL update only each source's state, reason, mount point, and capabilities, and SHALL never add, change, or mark missing any entry.

#### Scenario: Unmount noticed without a request
- **WHEN** a source's volume is unmounted and no request is made for one minute
- **THEN** the source's state is `offline` when the sources list is next read

#### Scenario: Listing reflects the current mounts
- **WHEN** a volume is mounted and `GET /api/sources` is read immediately afterwards
- **THEN** the response shows the source `online` with its current mount point

#### Scenario: Refresh leaves entries alone
- **WHEN** a refresh finds a source offline
- **THEN** none of its entries is marked missing, and their sizes, dates, decisions, and tags are unchanged

### Requirement: A source has an optional rescan schedule
- **Setting the schedule.** `set-source-schedule` SHALL set a source's rescan schedule. A schedule is off, daily at a time of day, or weekly on a day of the week at a time of day, in a named time zone. The UI sends the browser's zone.
- **What a source reports.** Each source in `GET /api/sources` SHALL carry:
  - its schedule;
  - the time of its next scheduled scan;
  - the outcome of the last time it came due, when that time was skipped.
- **Refused requests.**
  - An unknown source SHALL fail with 404 `unknown_source`.
  - A malformed schedule (a bad time, day, or zone) SHALL fail with 400 `invalid_request`.
- **Audit.** Each accepted request SHALL write one `source_schedule_set` audit event with the old and new schedules.
- **The Sources screen** SHALL show each source's schedule and next scan, and SHALL let the owner change the schedule.

#### Scenario: Scheduling a source
- **WHEN** the owner schedules the source `fotos` weekly on Sunday at 03:00 in `America/Sao_Paulo`
- **THEN** the response is 200 with the source, its schedule, and its next scan at the coming Sunday 03:00 in that zone, and a `source_schedule_set` audit event is written

#### Scenario: Turning a schedule off
- **WHEN** the owner sets the schedule of a scheduled source to off
- **THEN** the source has no next scheduled scan, and no scan starts by schedule

#### Scenario: A bad time is refused
- **WHEN** a `set-source-schedule` request names the time `25:00`
- **THEN** the response is 400 `invalid_request`, and the schedule is unchanged
