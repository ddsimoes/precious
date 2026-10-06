package fsaccess

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// devIndex maps device numbers to the names udev lists for them under
// /dev/disk/by-uuid and /dev/disk/by-label.
type devIndex struct {
	uuid  map[uint64]string
	label map[uint64]string
}

// readDevIndex reads both udev directories below devRoot. A missing directory
// (no udev, or a container) leaves its map empty: every volume then falls back
// to a weaker kind.
func (o *osFS) readDevIndex() devIndex {
	return devIndex{
		uuid:  o.readDiskLinks("by-uuid"),
		label: o.readDiskLinks("by-label"),
	}
}

// readDiskLinks maps the device number of each link's target to the link's
// decoded name. Links that do not name a block device are skipped. When two
// names denote one device, the first in name order wins.
func (o *osFS) readDiskLinks(kind string) map[uint64]string {
	dir := filepath.Join(o.devRoot, "disk", kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	links := make(map[uint64]string, len(entries))
	for _, e := range entries {
		dev, ok := o.blockDevice(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		if _, dup := links[dev]; !dup {
			links[dev] = decodeUdevName(e.Name())
		}
	}
	return links
}

// blockDevice returns the device number of the block device a /dev path
// names: the name its symlink points to (udev's links point to ../../sdb1 or
// ../dm-0), or its own name, looked up in /sys/class/block. No device node
// has to be visible, so identities hold under a private /dev or in a
// container that sees only /dev/disk.
func (o *osFS) blockDevice(path string) (uint64, bool) {
	name := filepath.Base(path)
	if target, err := os.Readlink(path); err == nil {
		name = filepath.Base(target)
	}
	if name == "." || name == ".." || name == "/" {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(o.sysRoot, "class", "block", name, "dev"))
	if err != nil {
		return 0, false
	}
	return parseMajorMinor(strings.TrimSpace(string(data)))
}

// decodeUdevName undoes udev's `\xHH` escaping of unsafe characters in link
// names, such as a space or '/' in a label.
func decodeUdevName(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseMajorMinor parses a mountinfo "major:minor" field into a device number.
func parseMajorMinor(mm string) (uint64, bool) {
	maj, minor, ok := strings.Cut(mm, ":")
	if !ok {
		return 0, false
	}
	ma, err1 := strconv.ParseUint(maj, 10, 32)
	mi, err2 := strconv.ParseUint(minor, 10, 32)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return unix.Mkdev(uint32(ma), uint32(mi)), true
}

// volume derives the identity of the filesystem a mountinfo row mounts
// (design D4), strongest first:
//
//   - a ZFS dataset is known by its name; datasets of one pool share the
//     device key pool:<pool>;
//   - a filesystem whose device (the row's major:minor) is listed in
//     /dev/disk/by-uuid is known by that UUID, keyed by the device;
//   - a btrfs filesystem without such a match (its mounts report an anonymous
//     device) is known by the statfs f_fsid of its mount point, which btrfs
//     derives from the filesystem UUID and the subvolume;
//   - anything else is a weak path volume, known only by its mount point.
//
// The label comes from /dev/disk/by-label, matched by the row's device or,
// failing that, by the device node named as the mount source.
func (o *osFS) volume(r *MountInfo, idx devIndex) Volume {
	dev, devOK := parseMajorMinor(r.MajorMinor)
	label := ""
	if devOK {
		label = idx.label[dev]
	}
	if label == "" {
		if src, ok := o.sourceDevice(r.Source); ok {
			label = idx.label[src]
		}
	}
	switch {
	case r.FSType == "zfs" && r.Source != "":
		pool, _, _ := strings.Cut(r.Source, "/")
		return Volume{Kind: VolumeZFS, ID: r.Source, FSType: r.FSType, DeviceKey: "pool:" + pool, Strong: true}
	case devOK && idx.uuid[dev] != "":
		return Volume{Kind: VolumeUUID, ID: idx.uuid[dev], Label: label, FSType: r.FSType, DeviceKey: "dev:" + r.MajorMinor, Strong: true}
	case r.FSType == "btrfs":
		if fsid, ok := o.btrfsFSID(r.MountPoint); ok {
			id := fmt.Sprintf("%016x", fsid)
			return Volume{Kind: VolumeFSID, ID: id, Label: label, FSType: r.FSType, DeviceKey: "fsid:" + id, Strong: true}
		}
	}
	v := pathVolume(r.MountPoint, r.FSType)
	v.Label = label
	return v
}

// sourceDevice returns the device number of a mount source naming a block
// device under /dev.
func (o *osFS) sourceDevice(source string) (uint64, bool) {
	rel, ok := strings.CutPrefix(source, "/dev/")
	if !ok || rel == "" {
		return 0, false
	}
	return o.blockDevice(filepath.Join(o.devRoot, filepath.Clean("/"+rel)))
}

// btrfsFSID returns the f_fsid of the btrfs filesystem mounted at point. It
// fails when statfs fails or finds another filesystem there (one stacked over
// the btrfs mount), so a wrong identity is never reported.
func (o *osFS) btrfsFSID(point string) (uint64, bool) {
	var st unix.Statfs_t
	for {
		err := o.statfs(point, &st)
		if err == unix.EINTR {
			continue
		}
		if err != nil || uint32(st.Type) != unix.BTRFS_SUPER_MAGIC {
			return 0, false
		}
		return uint64(uint32(st.Fsid.Val[0])) | uint64(uint32(st.Fsid.Val[1]))<<32, true
	}
}
