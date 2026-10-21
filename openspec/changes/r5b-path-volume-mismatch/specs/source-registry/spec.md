## MODIFIED Requirements

### Requirement: Sources are located by volume identity
A source SHALL be located by its volume identity plus its folder's path inside that volume, never by an absolute path alone. The volume kind SHALL be `uuid` (filesystem UUID), `zfs` (dataset), `fsid` (btrfs filesystem ID), or `path` (mount point, when no other identity exists). `GET /api/sources` SHALL report `volume.strong` as `true` for the first three and `false` for `path`. A `path` source SHALL be recognized only at the same mount point, and only when the filesystem mounted there holds the source's indexed root folder: the same inode when the source's recorded filesystem and the mounted one both keep stable inode numbers and the index recorded it, otherwise the same filesystem type when both are known. Another filesystem at a `path` source's mount point SHALL leave the source `offline` with reason `other_filesystem`.

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

#### Scenario: An empty folder in place of a path source's disk
- **WHEN** a scanned `path` source on an NTFS disk mounted at `/mnt/hd` has its disk unmounted, and a container binds an empty folder of the host's ext4 filesystem at `/mnt/hd`
- **THEN** the source shows `offline` with reason `other_filesystem`, `start-scan` fails with `409 source_offline`, and no entry or digest of the source changes
- **AND WHEN** the disk is mounted at `/mnt/hd` again
- **THEN** the source shows `online`, and a rescan creates, changes, and marks missing no entry

#### Scenario: A folder of a filesystem of the same type at a path source's mount point
- **WHEN** a scanned `path` source on a tmpfs has, bound at its mount point instead, an empty folder of another tmpfs
- **THEN** the source shows `offline` with reason `other_filesystem`

#### Scenario: The same disk through another driver
- **WHEN** a scanned `path` source on an NTFS disk mounted through `ntfs3` is mounted at the same point through `ntfs-3g`
- **THEN** the source shows `online`: its root folder has the inode the index recorded

#### Scenario: Filesystems without stable inodes compare by type
- **WHEN** a scanned `path` source on a FAT card is mounted at the same point again, its folders numbered afresh, and later a folder of an ext4 filesystem is bound there instead
- **THEN** the source shows `online` the first time, and `offline` with reason `other_filesystem` the second
