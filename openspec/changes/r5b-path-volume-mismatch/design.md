# Design

## Context

`(*Service).observe` resolves a source in three steps:

1. `locate` finds a mount whose volume has the source's kind and ID.
2. It checks that the root folder's path is not covered by another mount.
3. It opens the root folder.

For a `path` volume, the ID is the mount point (fsaccess `pathVolume`), so any filesystem mounted at that point passes `locate`. Every consumer that needs the disk goes through `Open`, which calls `observe`:

- the scan and hashing handlers;
- the media job;
- the viewer;
- the executor.

The facts a check can use are already stored:

- `sources.fs_type`, recorded when the source is added and never rewritten;
- `sources.capabilities`, the last observed set, whose `stable_identity` says whether inode numbers survive a remount;
- the root entry's `ino` (the entry at path `X''`), written by every scan.

The opened root folder's `Self()` gives its inode, and the mount gives its type.

## Decisions

### D1. When a mount at a `path` source's point is the source

After the root folder opens (step 3), a `path` source is online only when `samePathVolume(src, mount, root)` holds:

- **Same inode.** When the index recorded the root folder's inode, and both the source's recorded capabilities and the mounted filesystem's keep stable identity, the opened root folder's inode must equal the recorded one. A filesystem's root inode is fixed by its type: 2 on ext4, 5 on NTFS (its MFT record), 1 on a tmpfs, 256 on a btrfs subvolume. A folder of another filesystem bound there, which is what a container's or bind mount's stand-in is, has whatever inode that filesystem gave it. A source rooted at a subfolder keeps that folder's inode on these filesystems.
- **Otherwise, the same type.** The mount's filesystem type must equal `sources.fs_type`, when both are known. FAT and exFAT number inodes afresh at each mount, and network and FUSE filesystems give no stable identity, so for them only the type is compared. A stand-in folder on the host's or container's own filesystem (ext4, xfs, overlay, zfs) has another type than a FAT, exFAT, NTFS, or network source.

The device number is not compared: a disk plugged in again may come back as another device (`sdd` for `sdc`).

The filesystem type alone is not the test when inodes are stable. One NTFS disk mounted through `ntfs3` one day and `ntfs-3g` the next has two types and one root inode, and it is the same source.

Rejected:

- **`f_fsid`.** It is content-derived on ext4, btrfs, and `ntfs3` (the NTFS serial), but derived from the device number on FAT, exFAT, xfs, and f2fs. It also needs a new column.
- **"An empty root is not the source".** A disk the owner emptied must still read as empty; that is a special case of one input, not an identity.
- **The type in every case.** It refuses the `ntfs3`/`ntfs-3g` case above, and misses an ext4 stand-in for an ext4 disk, which the inode catches.

### D2. State and reason

A mismatch leaves the source `offline` with reason `other_filesystem`, mount point empty, and its recorded capabilities and device key unchanged: the source's volume is not mounted, and something else is. This matches what a strong volume reads when another disk is at its old mount point (`offline`), and the reason says why for a `path` source.

`Open` refuses it with `source_offline`, as it refuses any source that is not online. So the following do not run, and nothing in the index changes:

- scans;
- hashing;
- header reading;
- viewer content;
- the executor's changes.

The Sources screen shows "The disk is not connected" with the reason under it, as for `volume_not_mounted`. No UI change.

### D3. Where the inode is read

`sourceColumns` gains a second subquery on the root entry, `(SELECT e.ino FROM entries e WHERE e.source_id = s.id AND e.path = X'')`. `Source.RootIno` holds it as stored (the scanner's bit-cast of `st_ino`), 0 when there is no root entry or its inode is unknown. That covers a source never scanned, and seeded test rows without `Lstat`. `observe` compares it with `uint64` of the opened root folder's inode, bit-cast back. No migration.

### D4. What does not change

- Strong volumes, whose identity already excludes another filesystem.
- `locate`, `mountFor`, and the root-covered check (`root_covered`), which stay as they are, and run before D1.
- A source whose root folder is a subfolder missing on the stand-in, which already reads `unavailable`/`root_missing`. D1 adds nothing there, and runs only after the folder opens.
- Adding a source. A new source has no root entry yet, and its type is the mount's.

## Risks / Trade-offs

- **The same type with unstable inodes is still trusted.** An empty FAT card mounted where another FAT source's card was is taken for it. This is the weak identity's documented limit; a different filesystem of another type, the case seen, is refused.
- **Another whole filesystem of the same type is still trusted.** Root inodes are fixed per type, so an empty ext4 disk, or a fresh tmpfs, mounted where an ext4 or tmpfs source's whole filesystem was, has the source's root inode and type, and is taken for it. A source rooted at a subfolder is not affected: the folder is missing there (`root_missing`), or has another inode. Telling two whole filesystems of one type apart needs a content identity, such as the `f_fsid` some types derive from their UUID, which `path` volumes do not record (proposal, not in scope).
- **A legitimate change of root inode reads as another filesystem.** For example, the same files copied onto a fresh disk mounted at the same point, with the source at a subfolder. That is another disk, as it would be for a strong volume; the owner removes and adds the source again.
- **Inode 0.** A root entry with inode 0 is treated as unknown, so the type decides. No supported filesystem gives a directory inode 0.

## Migration Plan

Replace the binary. On the first availability check, a `path` source with a stand-in at its mount point reads `offline`/`other_filesystem`, and every other source reads as before. Rollback is the previous binary; nothing is stored differently.

## Addendum: decisions made during implementation
