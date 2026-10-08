# Spec Delta

## MODIFIED Requirements

### Requirement: Filesystem capabilities are detected
Precious SHALL detect and record each source's filesystem capabilities (§6.1) when the source is added and on each availability refresh: `known`, `read_only`, `case_sensitive`, `normalization_sensitive`, `stable_identity`, `local_time`, `hard_links`, `time_resolution_ns`, and `no_replace_rename`. On Linux they SHALL follow the filesystem type; an unrecognized type SHALL get the conservative unknown set with `known: false`. `no_replace_rename` SHALL be true only for a recognized local filesystem whose Linux driver honors `RENAME_NOREPLACE`, and false on macOS and Windows until their primitives exist (R8).

#### Scenario: Case-sensitive filesystems with stable identity
- **WHEN** a source is on ext2, ext3, ext4, xfs, btrfs, zfs, f2fs, or tmpfs
- **THEN** its capabilities report `known: true`, `case_sensitive: true`, `stable_identity: true`, `local_time: false`, and `time_resolution_ns: 1`

#### Scenario: FAT capabilities
- **WHEN** a source is on vfat
- **THEN** its capabilities report `known: true`, `case_sensitive: false`, `stable_identity: false`, `local_time: true`, and `time_resolution_ns: 2000000000`

#### Scenario: exFAT capabilities
- **WHEN** a source is on exfat
- **THEN** its capabilities report `known: true`, `case_sensitive: false`, `stable_identity: false`, `local_time: false`, and `time_resolution_ns: 10000000`

#### Scenario: NTFS treated as case-insensitive
- **WHEN** a source is on ntfs, ntfs3, or an NTFS filesystem mounted through fuseblk
- **THEN** its capabilities report `known: true`, `case_sensitive: false`, `stable_identity: true`, `local_time: false`, and `time_resolution_ns: 100`

#### Scenario: Optical filesystems are always read-only
- **WHEN** a source is on iso9660 or udf
- **THEN** its capabilities report `known: true`, `read_only: true`, `case_sensitive: true`, `stable_identity: true`, and `time_resolution_ns: 1000000000`

#### Scenario: Read-only follows the mount
- **WHEN** a source is on an ext4 filesystem mounted read-only
- **THEN** its capabilities report `read_only: true`, and the same filesystem mounted read-write reports `read_only: false`

#### Scenario: Unknown filesystem type
- **WHEN** a source is on a filesystem type outside the recognized list
- **THEN** its capabilities report `known: false`, `case_sensitive: false`, `stable_identity: false`, `time_resolution_ns: 2000000000`, and `no_replace_rename: false`

#### Scenario: Which filesystems have a no-replace rename
- **WHEN** a source is on ext2, ext3, ext4, xfs, btrfs, zfs, f2fs, tmpfs, vfat, exfat, or ntfs3
- **THEN** its capabilities report `no_replace_rename: true`, and on ntfs or fuseblk, iso9660, udf, an unknown type, macOS, or Windows they report `no_replace_rename: false`
