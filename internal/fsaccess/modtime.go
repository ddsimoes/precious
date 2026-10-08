package fsaccess

import (
	"math"
	"time"
)

// Linux passes every time a utimensat sets through timestamp_truncate, which
// clamps it to the range the filesystem's superblock declares and reports no
// error: a time outside the range is silently stored as its nearer end. The
// ranges below are the times Precious may write on a filesystem type, so that
// what it writes is what the disk keeps (r5 H1):
//   - FAT (vfat) stores local wall times from 1980-01-01 to 2107-12-31, in
//     whatever zone it is mounted with, and exFAT the same years in UTC; a
//     day's margin at each end holds for every zone (UTC-12 to UTC+14);
//   - ext2/3/4 and XFS store from 1901-12-13T20:45:52Z, and up to
//     2038-01-19T03:14:07Z with small inodes or without XFS's bigtime, which
//     the mount does not tell; ZFS refuses a time outside those 32-bit
//     seconds (EOVERFLOW);
//   - btrfs, f2fs, tmpfs, and NTFS store every time a nanosecond count does;
//   - any other type, fuseblk included, gets FAT's range, the narrowest.
//
// These are the disks' ranges only: set_mtime also refuses, on every type, a
// time before 1970-01-02, which the index reads back as unknown
// (domain.KnownModTime; r5 K3), so the lower ends below 1970 never apply.
var (
	fatFrom   = time.Date(1980, 1, 2, 0, 0, 0, 0, time.UTC)
	fatTo     = time.Date(2107, 12, 31, 0, 0, 0, 0, time.UTC)
	int32From = time.Unix(math.MinInt32, 0)
	int32To   = time.Unix(math.MaxInt32, 0)
	nsFrom    = time.Unix(0, math.MinInt64)
	nsTo      = time.Unix(0, math.MaxInt64)
)

// WritableModTimes returns the first and last modification times (both
// included) a filesystem of type fsType, as mountinfo names it, stores as
// given rather than clamping them.
func WritableModTimes(fsType string) (from, to time.Time) {
	switch fsType {
	case "ext2", "ext3", "ext4", "xfs", "zfs":
		return int32From, int32To
	case "btrfs", "f2fs", "tmpfs", "ntfs", "ntfs3":
		return nsFrom, nsTo
	default:
		return fatFrom, fatTo
	}
}

// StoresModTime reports whether a filesystem of type fsType stores the
// modification time ns (nanoseconds since the epoch) as given
// (WritableModTimes).
func StoresModTime(fsType string, ns int64) bool {
	from, to := WritableModTimes(fsType)
	t := time.Unix(0, ns)
	return !t.Before(from) && !t.After(to)
}
