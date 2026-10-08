package fsaccess

import (
	"strings"
	"time"
)

// fsTypeCapabilities is the Linux capability table (design D3), by mountinfo
// filesystem type. readOnly comes from the mount; optical filesystems are
// always read-only. A type outside the table gets the unknown set.
//
// Every recognised type stores names byte for byte, without Unicode
// normalization. NTFS is treated as case-insensitive because Windows treats
// its names that way. NoReplaceRename is true for the local types whose
// drivers honour RENAME_NOREPLACE (r3 design D2); "ntfs" is the old kernel
// driver and ntfs-3g through fuseblk, which do not.
func fsTypeCapabilities(fsType string, readOnly bool) Capabilities {
	var c Capabilities
	switch fsType {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "tmpfs":
		c = Capabilities{CaseSensitive: true, StableIdentity: true, HardLinks: true, TimeResolution: time.Nanosecond,
			NoReplaceRename: true}
	case "vfat":
		c = Capabilities{LocalTime: true, TimeResolution: 2 * time.Second, NoReplaceRename: true}
	case "exfat":
		c = Capabilities{TimeResolution: 10 * time.Millisecond, NoReplaceRename: true}
	case "ntfs3":
		c = Capabilities{StableIdentity: true, HardLinks: true, TimeResolution: 100 * time.Nanosecond,
			NoReplaceRename: true}
	case "ntfs":
		c = Capabilities{StableIdentity: true, HardLinks: true, TimeResolution: 100 * time.Nanosecond}
	case "iso9660", "udf":
		c = Capabilities{CaseSensitive: true, StableIdentity: true, HardLinks: true, TimeResolution: time.Second}
		readOnly = true
	default:
		return UnknownCapabilities(readOnly)
	}
	c.Known, c.NormalizationSensitive, c.ReadOnly = true, true, readOnly
	return c
}

// capabilities returns the capabilities of the filesystem a mountinfo row
// mounts. An NTFS filesystem mounted through fuseblk (ntfs-3g) gets NTFS's.
func (o *osFS) capabilities(r *MountInfo) Capabilities {
	fsType := r.FSType
	if o.isNTFSFuseblk(r) {
		fsType = "ntfs"
	}
	return fsTypeCapabilities(fsType, r.ReadOnly)
}

// isNTFSFuseblk recognises NTFS among fuseblk mounts without running blkid:
// by an NTFS subtype in the type ("fuseblk.ntfs-3g"), or, for a bare
// "fuseblk", by the device's /dev/disk/by-uuid name, since only NTFS volume
// serials are 16 hex digits (FAT and exFAT ones read XXXX-XXXX).
func (o *osFS) isNTFSFuseblk(r *MountInfo) bool {
	sub, ok := strings.CutPrefix(r.FSType, "fuseblk")
	if !ok {
		return false
	}
	if sub != "" {
		return sub == ".ntfs" || sub == ".ntfs-3g" || sub == ".lowntfs-3g"
	}
	dev, ok := parseMajorMinor(r.MajorMinor)
	if !ok {
		return false
	}
	return isNTFSSerial(o.readDiskLinks("by-uuid")[dev])
}

func isNTFSSerial(uuid string) bool {
	if len(uuid) != 16 {
		return false
	}
	for i := range len(uuid) {
		switch c := uuid[i]; {
		case '0' <= c && c <= '9', 'a' <= c && c <= 'f', 'A' <= c && c <= 'F':
		default:
			return false
		}
	}
	return true
}
