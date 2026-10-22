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
- **Otherwise, the same filesystem type.** The mounted type must name the same filesystem as `sources.fs_type`, when both are known: the same name, or one family of names (addendum A1). FAT and exFAT number inodes afresh at each mount, and network and FUSE filesystems give no stable identity, so for them only the type is compared. A stand-in folder on the host's or container's own filesystem (ext4, xfs, overlay, zfs) has another type than a FAT, exFAT, NTFS, or network source.

The mounted type is the opened folder's own mount row (`FSInfo().Mount`), and its capabilities are read after the open, so a filesystem mounted at the point after the mount table was read is the one judged (A3).

The device number is not compared: a disk plugged in again may come back as another device (`sdd` for `sdc`).

The filesystem type alone is not the test when inodes are stable on both sides: it would miss an ext4 stand-in for an ext4 disk, which the inode catches.

On macOS and Windows the portable backend reports no type and no stable identity, so every mount at the point is taken for the source, as before (A5).

Rejected:

- **`f_fsid`.** It is content-derived on ext4, btrfs, and `ntfs3` (the NTFS serial), but derived from the device number on FAT, exFAT, xfs, and f2fs. It also needs a new column.
- **"An empty root is not the source".** A disk the owner emptied must still read as empty; that is a special case of one input, not an identity.
- **The type in every case.** It misses an ext4 stand-in for an ext4 disk, which the inode catches.

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

`sourceColumns` gains a second subquery on the root entry, `(SELECT e.ino FROM entries e WHERE e.source_id = s.id AND e.path = X'')`. `Source.RootIno` holds it as stored (the scanner's bit-cast of `st_ino`), 0 when there is no root entry or its inode is unknown: a source added before r5b and never scanned since, or seeded test rows without `Lstat`. `observe` compares it with `uint64` of the opened root folder's inode, bit-cast back. No migration.

`add-source` records the folder's inode on the root entry it creates (`Candidate.RootIno`, from the folder `PrepareAdd` opens), so the check holds from the start, and not only once a first scan completes, which on a large disk takes hours (A2).

### D4. What does not change

- Strong volumes, whose identity already excludes another filesystem.
- `locate`, `mountFor`, and the root-covered check (`root_covered`), which stay as they are, and run before D1.
- A source whose root folder is a subfolder missing on the stand-in, which already reads `unavailable`/`root_missing`. D1 adds nothing there, and runs only after the folder opens.

## Risks / Trade-offs

- **The same type with unstable inodes is still trusted.** An empty FAT card mounted where another FAT source's card was is taken for it. This is the weak identity's documented limit; a different filesystem of another type, the case seen, is refused.
- **Another whole filesystem of the same type is still trusted.** Root inodes are fixed per type, so an empty ext4 disk, or a fresh tmpfs, mounted where an ext4 or tmpfs source's whole filesystem was, has the source's root inode and type, and is taken for it. A source rooted at a subfolder is not affected: the folder is missing there (`root_missing`), or has another inode. Telling two whole filesystems of one type apart needs a content identity, such as the `f_fsid` some types derive from their UUID, which `path` volumes do not record (proposal, not in scope).
- **A legitimate change of root inode reads as another filesystem.** For example, the same files copied onto a fresh disk mounted at the same point, with the source at a subfolder, or the source's folder deleted and made again. That is another disk or folder, as a reformatted disk is for a strong volume. The owner removes and adds the source again, as for a strong volume (A4).
- **A family of type names can admit another filesystem of that family.** A bare `fuseblk` is either NTFS (ntfs-3g) or exFAT (exfat-fuse), so an exFAT card mounted through exfat-fuse at an NTFS source's mount point passes the type test when the inode test does not apply. This is within the weak identity's limit; the stand-in seen, a folder of the host's own filesystem, is never of these families.
- **Inode 0.** A root entry with inode 0 is treated as unknown, so the type decides. No supported filesystem gives a directory inode 0.

## Migration Plan

Replace the binary. On the first availability check, a `path` source with a stand-in at its mount point reads `offline`/`other_filesystem`, and every other source reads as before. Rollback is the previous binary. A root inode that `add-source` records under r5b is what the previous binary's first scan would have written.

## Addendum: decisions made during implementation

An independent review of the first implementation (9cee1f4) found seven problems. Each fix below has a test in `internal/sources/r5b_test.go`.

- **A1. Type names are compared by family (review 1, 2).** Without `/dev/disk`, a bare `fuseblk` mount (ntfs-3g) is not recognized as NTFS, and gets the unknown capabilities, which have no stable identity. So the same NTFS disk mounted through ntfs-3g instead of `ntfs3` was compared by its exact type, and read `other_filesystem`; the reverse as well. That is the usual fallback when `ntfs3` refuses a volume left dirty by a power cut. The type test now accepts the names one filesystem is mounted under:
  - NTFS: `ntfs`, `ntfs3`, `fuseblk.ntfs-3g`, `fuseblk.ntfs`, `fuseblk.lowntfs-3g`, and a bare `fuseblk`;
  - exFAT: `exfat`, `fuseblk.exfat`, and a bare `fuseblk`;
  - FAT: `vfat`, `msdos`;
  - NFS: `nfs`, `nfs4`;
  - SMB: `cifs`, `smb3`.

  The first test's ntfs-3g step had given the fuseblk mount NTFS's stable capabilities, which the OS backend never reports for a `path` volume, so it passed through the inode test. It now uses the unknown set. Tests: `TestPathSourceStandInFolder`, `TestPathSourceAddedThroughNTFS3G`, `TestSameFSType`.
- **A2. add-source records the root inode (review 4).** The root entry `Add` creates had no inode until a scan completed: hours on a large disk, or never after an aborted one. A stand-in of the source's own type mounted in that window passed the type test. A scan of it then recorded the stand-in's inode, and locked the real disk out. `PrepareAdd` keeps the opened folder's inode, and `Add` writes it. Test: `TestPathSourceAddedRecordsTheRootInode`.
- **A3. The opened folder's own facts are judged (review 5).** The type came from the mount table read before the open, and the capabilities from a read before it as well. A filesystem mounted at the point in between was judged by the old one's. The type now comes from the opened folder's mount row, and the capabilities are read after the open. The capabilities recorded for an `unavailable` source are unchanged. Test: `TestPathSourceSwappedBeforeTheOpen`.
- **A4. No command re-accepts a filesystem (review 3).** The review asked for a way out of a wrong `other_filesystem`, such as a command that takes the mounted type and clears the recorded inode. A1 and A2 remove the false positives it found. What is left is the case of a strong volume whose disk was reformatted: another identity, which stays offline until the source is removed and added again. That includes being refused removal while it holds a quarantine or awaits a recovery check, which needs the disk. r5b keeps that model for `path` volumes, and adds no command.
- **A5. Docs scope (review 7).** The operator guide says that the check is Linux's. On macOS and Windows every mount at the point is still taken for the source.
- **A6. Further tests (review 6).**
  - A source rooted at a folder below its mount point is judged by that folder's inode, not the mount point's: `TestPathSourceRootedBelowItsMountPoint`.
  - A strong volume ignores the root inode: `TestStrongSourceIgnoresTheRootInode`.
  - A source with stable inodes and a stand-in with none known (overlay) is told apart by type: `TestPathSourceStandInWithoutStableInodes`.
- **A7. One capability read per check (re-review).** A second review of A1–A6 (1ade87a) undid each fix in turn, and the new test failed each time. It accepted A4. It found that a `path` source read the capabilities twice per check, each read parsing the mount table again: once before the open, and once after.
  - `observe` now reads them once:
    - after the open, for the check and the recorded capabilities alike;
    - when the observation fails, in `fail`.
  - Nothing observable changes, so no test is added.
  - The operator guide now also says that removal is refused while the quarantine holds items or a change waits for a check, as for a strong identity.
- **V1. Deploy on the reference server (2.3), 2026-10-21.**
  - **Deploy.** The database was backed up with the r5 binary (`r5-before-r5b.db`), and the server then ran `r5b-smoke-f219308`. All three sources were online. The NTFS source (`path`, ntfs3) was being hashed: 1,776,719 files checked.
  - **The hazard, reproduced.** The disk was unmounted on the host, and the container restarted. The container platform then bound the host's empty folder (ext4) at the source's mount point, as after a power cut.
  - **What Precious did.**
    - The source read `offline`/`other_filesystem`. The Sources screen showed "The disk is not connected" with the reason, and Scan now off ("Connect the disk to scan it").
    - The hashing job that was running before the restart was claimed again, planned, and ended `failed`/`source_offline` at its `Open`, having read nothing.
    - The checked count stayed at 1,776,719 files.
  - **The disk back.** With the disk mounted again and the container restarted, the source was `online`.
  - **Hashing did not restart on its own.** `Startup` enqueues hashing for the sources recorded online, and this one was recorded offline at shutdown, as a strong volume would be. It was started by hand (`start-hash`). That behavior is older than r5b, and unchanged.
- **V2. Owner sign-off (2.3), 2026-10-22.** The owner signed off ("A").
  - **Hashing finished.** The hashing started by hand ended `succeeded` on 2026-10-21. All 3,216,784 files of the NTFS source that could have a copy were checked.
  - **A real restart.** The owner then restarted the server, and the host came up without the disk mounted. The source read `offline`/`other_filesystem`, and nothing of its index changed.
  - **The disk back.** With the disk mounted on the host and the container restarted, the source was `online` again, its coverage still complete.
