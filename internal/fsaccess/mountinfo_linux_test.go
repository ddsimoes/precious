package fsaccess

import (
	"testing"

	"golang.org/x/sys/unix"
)

// Fixture rows, as /proc/self/mountinfo prints them:
//   - 30 is a same-device bind mount (same major:minor as /, non-"/" root);
//   - 31 has no optional fields, an escaped space in its mount point, and a
//     read-only mount;
//   - 32 has several optional fields and tab/backslash escapes;
//   - 33 and 34 stack two mounts on one mount point, 34 on a read-only
//     superblock;
//   - 35 has an empty mount source;
//   - 22's "errors=remount-ro" is not a read-only option.
const mountinfoFixture = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro
23 22 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw
30 22 8:1 /srv/target /srv/src/again rw,relatime shared:1 - ext4 /dev/sda1 rw
31 22 8:17 / /srv/src/with\040space ro,relatime - ext4 /dev/sdb1 rw
32 22 0:45 / /mnt/tab\011and\134slash rw shared:5 master:2 propagate_from:1 - fuse.sshfs user@host:/a\040b rw,user_id=0
33 22 0:46 / /mnt/stack rw - tmpfs first rw
34 33 0:47 / /mnt/stack rw - tmpfs second ro
35 22 0:48 / /mnt/empty rw - tmpfs  rw
`

func TestParseMountinfoFixture(t *testing.T) {
	rows, err := parseMountinfo([]byte(mountinfoFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := []MountInfo{
		{22, "8:1", "/", "/", "ext4", "/dev/sda1", false},
		{23, "0:21", "/", "/proc", "proc", "proc", false},
		{30, "8:1", "/srv/target", "/srv/src/again", "ext4", "/dev/sda1", false},
		{31, "8:17", "/", "/srv/src/with space", "ext4", "/dev/sdb1", true},
		{32, "0:45", "/", "/mnt/tab\tand\\slash", "fuse.sshfs", "user@host:/a b", false},
		{33, "0:46", "/", "/mnt/stack", "tmpfs", "first", false},
		{34, "0:47", "/", "/mnt/stack", "tmpfs", "second", true},
		{35, "0:48", "/", "/mnt/empty", "tmpfs", "", false},
	}
	if len(rows) != len(want) {
		t.Fatalf("parsed %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestMountTableBoundariesAndLookup(t *testing.T) {
	rows, err := parseMountinfo([]byte(mountinfoFixture))
	if err != nil {
		t.Fatal(err)
	}
	mt := newMountTable(rows)

	// The bind mount shares the device of its parent, so only the mount table
	// reveals it.
	for p, want := range map[string]bool{
		"/srv/src/again":      true,
		"/srv/src/with space": true,
		"/srv/src":            false,
		"/srv/src/again/x":    false,
		"/srv/src/with":       false,
	} {
		if got := mt.isMountPoint(p); got != want {
			t.Errorf("isMountPoint(%q) = %v, want %v", p, got, want)
		}
	}

	dev := func(major, minor uint32) uint64 { return unix.Mkdev(major, minor) }
	for _, tc := range []struct {
		path   string
		dev    uint64
		wantID int
	}{
		{"/srv/src/plain", dev(8, 1), 22},
		{"/srv/src/again/x", dev(8, 1), 30},
		{"/srv/src/againx", dev(8, 1), 22},
		{"/srv/src/with space/deep", dev(8, 17), 31},
		{"/mnt/stack/x", dev(0, 47), 34},
		// The device picks the covered mount over the deeper or later one.
		{"/mnt/stack/x", dev(0, 46), 33},
		{"/srv/src/again/x", dev(9, 9), 30},
		// No device match: deepest mount point, last row on ties.
		{"/mnt/stack", dev(9, 9), 34},
	} {
		got := mt.lookup(tc.path, tc.dev)
		if got == nil || got.MountID != tc.wantID {
			t.Errorf("lookup(%q, %d:%d) = %+v, want mount %d", tc.path, unix.Major(tc.dev), unix.Minor(tc.dev), got, tc.wantID)
		}
	}
	if got := mt.lookup("relative", dev(8, 1)); got != nil {
		t.Errorf("lookup of a relative path = %+v, want nil", got)
	}
	// lookup returns a copy; the snapshot cannot be changed through it.
	mt.lookup("/srv/src/again", dev(8, 1)).MountPoint = "/elsewhere"
	if !mt.isMountPoint("/srv/src/again") || mt.rows[2].MountPoint != "/srv/src/again" {
		t.Error("lookup result aliases the mount table")
	}
}

func TestUnescapeOctal(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:             "plain",
		`a\040b`:            "a b",
		`\011\012\134`:      "\t\n\\",
		`end\04`:            `end\04`,
		`bad\9xx`:           `bad\9xx`,
		`big\400`:           `big\400`,
		`trailing\`:         `trailing\`,
		`\303\251t\303\251`: "été",
	} {
		if got := unescapeOctal(in); got != want {
			t.Errorf("unescapeOctal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMountinfoRejectsMalformed(t *testing.T) {
	for name, data := range map[string]string{
		"empty":              "",
		"no separator":       "22 1 8:1 / / rw shared:1 ext4 /dev/sda1 rw\n",
		"bad mount ID":       "x 1 8:1 / / rw - ext4 /dev/sda1 rw\n",
		"bad major:minor":    "22 1 81 / / rw - ext4 /dev/sda1 rw\n",
		"too few fields":     "22 1 8:1 / / rw - ext4\n",
		"one bad among good": "22 1 8:1 / / rw - ext4 /dev/sda1 rw\n23 22 0:21\n",
	} {
		if rows, err := parseMountinfo([]byte(data)); err == nil {
			t.Errorf("%s: parsed %+v, want error", name, rows)
		}
	}
}
