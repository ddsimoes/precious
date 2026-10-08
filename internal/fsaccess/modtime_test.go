package fsaccess

import (
	"testing"
	"time"
)

// r5 H1: the times each filesystem type stores as given. FAT, exFAT, and an
// unknown type keep 1980 to 2107 with a day's margin for the mount's zone;
// ext4, XFS, and ZFS the 32-bit seconds; btrfs and NTFS any nanosecond time.
func TestStoresModTime(t *testing.T) {
	at := func(y int, mo time.Month, d, h, mi, s int) int64 {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC).UnixNano()
	}
	for _, c := range []struct {
		fsType string
		ns     int64
		want   bool
	}{
		{"vfat", at(1975, 6, 1, 12, 0, 0), false},
		{"vfat", at(1980, 1, 1, 12, 0, 0), false},
		{"vfat", at(1980, 1, 2, 0, 0, 0), true},
		{"vfat", at(2010, 7, 17, 10, 0, 0), true},
		{"vfat", at(2107, 12, 31, 0, 0, 0), true},
		{"vfat", at(2107, 12, 31, 0, 0, 1), false},
		{"exfat", at(1979, 12, 31, 0, 0, 0), false},
		{"fuseblk", at(1975, 6, 1, 12, 0, 0), false},
		{"", at(2150, 1, 1, 0, 0, 0), false},
		{"ext4", at(1901, 12, 13, 20, 45, 51), false},
		{"ext4", at(1901, 12, 13, 20, 45, 52), true},
		{"ext4", at(1975, 6, 1, 12, 0, 0), true},
		{"xfs", at(2038, 1, 19, 3, 14, 7), true},
		{"xfs", at(2038, 1, 19, 3, 14, 8), false},
		{"zfs", at(2100, 1, 1, 0, 0, 0), false},
		{"btrfs", at(1700, 1, 1, 0, 0, 0), true},
		{"ntfs3", at(2200, 1, 1, 0, 0, 0), true},
	} {
		if got := StoresModTime(c.fsType, c.ns); got != c.want {
			t.Errorf("StoresModTime(%q, %v) = %v, want %v", c.fsType, time.Unix(0, c.ns).UTC(), got, c.want)
		}
	}
}
