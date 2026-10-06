package fsaccess

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
)

// fixtureFS returns the Linux backend over a fixture tree: table as
// proc/self/mountinfo; below dev, every link of links (relative name to
// target), with no device node, as in a private /dev; and below sys, a
// class/block/<name>/dev file for every block device of blocks (name to
// "major:minor"). statfs fails for every path unless the test replaces it.
func fixtureFS(t *testing.T, table string, links, blocks map[string]string) *osFS {
	t.Helper()
	dev, sys := t.TempDir(), t.TempDir()
	for name, target := range links {
		p := filepath.Join(dev, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	for name, mm := range blocks {
		writeFixture(t, filepath.Join(sys, "class", "block", name, "dev"), mm+"\n")
	}
	o := newOS(fakeProc(t, table), dev, sys)
	o.statfs = func(string, *unix.Statfs_t) error { return unix.ENOENT }
	return o
}

// fakeStatfs answers statfs for the listed mount points.
func fakeStatfs(fsys map[string]unix.Statfs_t) func(string, *unix.Statfs_t) error {
	return func(path string, st *unix.Statfs_t) error {
		s, ok := fsys[path]
		if !ok {
			return unix.ENOENT
		}
		*st = s
		return nil
	}
}

func btrfsStatfs(fsid0, fsid1 int32) unix.Statfs_t {
	return unix.Statfs_t{Type: unix.BTRFS_SUPER_MAGIC, Fsid: unix.Fsid{Val: [2]int32{fsid0, fsid1}}}
}

func equalMounts(a, b []Mount) bool {
	return slices.EqualFunc(a, b, func(x, y Mount) bool {
		return x.Point == y.Point && string(x.Root) == string(y.Root) && x.Volume == y.Volume && x.ReadOnly == y.ReadOnly
	})
}

// TestLinuxVolumeIdentity covers design D4 on one fixture tree: an ext4
// partition known by its UUID and labelled; a bind mount of a folder on it
// (same volume, relative root); an LVM volume reached through
// /dev/mapper; ZFS datasets known by name and keyed by pool; btrfs
// subvolumes known by f_fsid and labelled through their source device; a
// btrfs mount statfs cannot confirm and a tmpfs, both weak path volumes.
// Links that name no block device are ignored.
func TestLinuxVolumeIdentity(t *testing.T) {
	table := `22 1 8:17 / /media/usb rw,relatime - ext4 /dev/sdb1 rw
23 1 8:17 /fotos/2008 /srv/share ro,relatime - ext4 /dev/sdb1 rw
24 1 253:0 / /srv/lvm rw - ext4 /dev/mapper/vg-dados rw
30 1 0:50 / /tank/fotos rw - zfs tank/fotos rw,xattr
31 30 0:51 / /tank/fotos/raw\040files rw - zfs tank/fotos/raw\040files rw
40 1 0:60 /@home /home rw - btrfs /dev/sdc2 rw,subvolid=257,subvol=/@home
41 1 0:61 / /mnt/pool rw - btrfs /dev/sdc2 rw,subvolid=5,subvol=/
42 1 0:62 / /mnt/hidden rw - btrfs /dev/sdc3 rw
43 1 0:63 / /mnt/gone rw - btrfs /dev/sdc4 rw
50 1 0:70 / /tmp rw - tmpfs tmpfs rw
`
	links := map[string]string{
		"disk/by-uuid/6f1c2a4e-0d3b":           "../../sdb1",
		"disk/by-label/Fotos\\x20USB":          "../../sdb1",
		"disk/by-uuid/0b9e7d1c-lvm":            "../../dm-0",
		"disk/by-label/DADOS":                  "../../dm-0",
		"mapper/vg-dados":                      "../dm-0",
		"disk/by-label/backup":                 "../../sdc2",
		"disk/by-uuid/dangling":                "../../missing",
		"disk/by-uuid/root-of-everything":      "/",
		"disk/by-label/also\\x2fslash-dangles": "/nowhere",
	}
	blocks := map[string]string{"sdb1": "8:17", "sdc2": "8:34", "dm-0": "253:0"}
	o := fixtureFS(t, table, links, blocks)
	o.statfs = fakeStatfs(map[string]unix.Statfs_t{
		"/home":       btrfsStatfs(0x11223344, 0x55667788),
		"/mnt/pool":   btrfsStatfs(0x11223344, 0x55667789),
		"/mnt/hidden": {Type: unix.TMPFS_MAGIC},
	})

	usb := Volume{Kind: VolumeUUID, ID: "6f1c2a4e-0d3b", Label: "Fotos USB", FSType: "ext4", DeviceKey: "dev:8:17", Strong: true}
	want := []Mount{
		{Point: "/media/usb", Root: []byte("/"), Volume: usb},
		{Point: "/srv/share", Root: []byte("/fotos/2008"), Volume: usb, ReadOnly: true},
		{Point: "/srv/lvm", Root: []byte("/"), Volume: Volume{Kind: VolumeUUID, ID: "0b9e7d1c-lvm", Label: "DADOS", FSType: "ext4", DeviceKey: "dev:253:0", Strong: true}},
		{Point: "/tank/fotos", Root: []byte("/"), Volume: Volume{Kind: VolumeZFS, ID: "tank/fotos", FSType: "zfs", DeviceKey: "pool:tank", Strong: true}},
		{Point: "/tank/fotos/raw files", Root: []byte("/"), Volume: Volume{Kind: VolumeZFS, ID: "tank/fotos/raw files", FSType: "zfs", DeviceKey: "pool:tank", Strong: true}},
		{Point: "/home", Root: []byte("/@home"), Volume: Volume{Kind: VolumeFSID, ID: "5566778811223344", Label: "backup", FSType: "btrfs", DeviceKey: "fsid:5566778811223344", Strong: true}},
		{Point: "/mnt/pool", Root: []byte("/"), Volume: Volume{Kind: VolumeFSID, ID: "5566778911223344", Label: "backup", FSType: "btrfs", DeviceKey: "fsid:5566778911223344", Strong: true}},
		{Point: "/mnt/hidden", Root: []byte("/"), Volume: Volume{Kind: VolumePath, ID: "/mnt/hidden", FSType: "btrfs", DeviceKey: "mount:/mnt/hidden"}},
		{Point: "/mnt/gone", Root: []byte("/"), Volume: Volume{Kind: VolumePath, ID: "/mnt/gone", FSType: "btrfs", DeviceKey: "mount:/mnt/gone"}},
		{Point: "/tmp", Root: []byte("/"), Volume: Volume{Kind: VolumePath, ID: "/tmp", FSType: "tmpfs", DeviceKey: "mount:/tmp"}},
	}
	mounts, err := o.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	if !equalMounts(mounts, want) {
		t.Fatalf("Mounts() =\n%+v\nwant\n%+v", mounts, want)
	}
}

// TestLinuxVolumeIdentityWithoutUdev: without /dev/disk (a container, or no
// udev) a block-device filesystem is a weak path volume; ZFS and btrfs keep
// their identities, which need no udev link.
func TestLinuxVolumeIdentityWithoutUdev(t *testing.T) {
	o := fixtureFS(t, `22 1 8:1 / / rw - ext4 /dev/sda1 rw
30 22 0:50 / /tank rw - zfs tank rw
40 22 0:60 / /data rw - btrfs /dev/sdb1 rw
`, nil, map[string]string{"sda1": "8:1"})
	o.statfs = fakeStatfs(map[string]unix.Statfs_t{"/data": btrfsStatfs(1, 2)})
	mounts, err := o.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{
		pathMount("/", []byte("/"), "ext4", false),
		{Point: "/tank", Root: []byte("/"), Volume: Volume{Kind: VolumeZFS, ID: "tank", FSType: "zfs", DeviceKey: "pool:tank", Strong: true}},
		{Point: "/data", Root: []byte("/"), Volume: Volume{Kind: VolumeFSID, ID: "0000000200000001", FSType: "btrfs", DeviceKey: "fsid:0000000200000001", Strong: true}},
	}
	if !equalMounts(mounts, want) {
		t.Fatalf("Mounts() =\n%+v\nwant\n%+v", mounts, want)
	}
}

// TestLinuxMountsCarryRowFacts: every mountinfo row is listed, with its
// unescaped mount point, its root, and its read-only flag, the last of stacked
// mounts included.
func TestLinuxMountsCarryRowFacts(t *testing.T) {
	mounts, err := fixtureFS(t, mountinfoFixture, nil, nil).Mounts()
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{
		pathMount("/", []byte("/"), "ext4", false),
		pathMount("/proc", []byte("/"), "proc", false),
		pathMount("/srv/src/again", []byte("/srv/target"), "ext4", false),
		pathMount("/srv/src/with space", []byte("/"), "ext4", true),
		pathMount("/mnt/tab\tand\\slash", []byte("/"), "fuse.sshfs", false),
		pathMount("/mnt/stack", []byte("/"), "tmpfs", false),
		pathMount("/mnt/stack", []byte("/"), "tmpfs", true),
		pathMount("/mnt/empty", []byte("/"), "tmpfs", false),
	}
	if !equalMounts(mounts, want) {
		t.Fatalf("Mounts() =\n%+v\nwant\n%+v", mounts, want)
	}

	_, err = newOS(t.TempDir(), t.TempDir(), t.TempDir()).Mounts()
	var e *Error
	if !errors.As(err, &e) || e.Outcome != domain.OutcomeUnavailable || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Mounts() without a mount table = %v, want unavailable wrapping the cause", err)
	}
}

func TestDecodeUdevName(t *testing.T) {
	for in, want := range map[string]string{
		"plain":             "plain",
		`Fotos\x20USB`:      "Fotos USB",
		`a\x2fb`:            "a/b",
		`trailing\x2`:       `trailing\x2`,
		`not\xzzhex`:        `not\xzzhex`,
		`\x5c\x78`:          `\x`,
		"MEU\\x20CART\\xc3": "MEU CART\xc3",
	} {
		if got := decodeUdevName(in); got != want {
			t.Errorf("decodeUdevName(%q) = %q, want %q", in, got, want)
		}
	}
}
