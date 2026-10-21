# Proposal

## Why

On 2026-10-20 the owner attached a 466 GB NTFS disk to the reference server. Precious runs there in a container that sees no `/dev/disk`, so the disk is a `path` source, known only by its mount point (a bind of the host's mount). Before a planned power-off, a reading of the code found a hazard:

- When the host boots without the disk mounted, the container platform still binds the host's empty folder at that mount point.
- The registry would see a mount at the source's mount point and call the source `online`, holding nothing.
- The startup hashing job would then find every file still to read gone, and mark it `changed`. A `changed` file whose facts a rescan finds unchanged is never read again.
- A scan would mark every entry missing.

The same holds for any `path` source whose disk is replaced at its mount point by something else, such as a container bind of an empty folder, or an empty tmpfs. A strong volume (UUID, dataset, btrfs ID) has no such hazard: another filesystem has another identity and leaves the source `offline` (source-registry, "Different disk at the old mount point").

The owner chose on 2026-10-21 to fix Precious now ("A"), rather than rely on mounting the disk by hand before the container sees the folder.

## What Changes

- A mounted filesystem counts as a `path` source's volume only when the source's root folder there is the one indexed:
  - the same inode, where both the recorded and the mounted filesystem keep stable inode numbers;
  - otherwise the same filesystem type, when both types are known.
- Another filesystem at the source's mount point, such as a bind of a folder of the host's own filesystem, leaves the source `offline`, with the new reason `other_filesystem`. Nothing that needs the disk runs: scans, hashing, header reading, content, and changes are refused, as for any offline source.
- Another whole filesystem of the source's own type is still taken for it when the source is that filesystem's root: root inodes are fixed per type (design, Risks).
- The same disk mounted through another driver (NTFS through `ntfs3` or `ntfs-3g`) keeps its root inode, so it is still the source.

## Acceptance

R1.16 and the existing source-registry scenarios keep passing. The new scenarios in the delta spec are checked:

- by unit tests over the synthetic filesystem;
- by an end-to-end test with real mounts (tmpfs and bind mounts);
- on the reference server, with the owner's sign-off.

Not in scope:

- **Re-reading `changed` files.** A file left `changed` by a rescan that finds its facts unchanged is still never read again; this change only stops the stand-in from producing such files.
- **A strong identity without `/dev/disk`.** For example, the NTFS serial that `ntfs3` reports as `f_fsid`.

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `source-registry`: when a `path` source's volume counts as mounted.

## Impact

- **Go:** `internal/sources`:
  - the `Source` row gains the root folder's indexed inode, read with the row;
  - `observe` checks it, or the type, for `path` sources;
  - a new reason constant;
  - unit tests.
  - An end-to-end test in `internal/index` with real mounts.
- **No migration:** the root entry's inode and the source's filesystem type are already stored.
- **Docs:** `docs/operator.md`, in the state reasons table and Volume identity.
- **No UI change.** The Sources screen already shows the reason under "The disk is not connected".
- **No change** to I1–I9, the API's fields, or strong volumes.
