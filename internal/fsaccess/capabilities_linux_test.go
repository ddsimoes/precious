package fsaccess

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
)

var (
	posixCaps = Capabilities{Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true, TimeResolution: time.Nanosecond}
	fatCaps   = Capabilities{Known: true, NormalizationSensitive: true, LocalTime: true, TimeResolution: 2 * time.Second}
	exfatCaps = Capabilities{Known: true, NormalizationSensitive: true, TimeResolution: 10 * time.Millisecond}
	ntfsCaps  = Capabilities{Known: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true, TimeResolution: 100 * time.Nanosecond}
	opticCaps = Capabilities{Known: true, ReadOnly: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true, TimeResolution: time.Second}
)

func readOnlyCaps(c Capabilities) Capabilities {
	c.ReadOnly = true
	return c
}

// TestLinuxCapabilityTable covers every row of the design D3 table, mounted
// read-write and read-only, the unknown fallback, and NTFS through fuseblk,
// recognised by subtype or by its 16-hex-digit serial in /dev/disk/by-uuid.
func TestLinuxCapabilityTable(t *testing.T) {
	type row struct {
		fsType, majorMinor string
		rw, ro             Capabilities
	}
	rows := []row{
		{"ext2", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"ext3", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"ext4", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"xfs", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"btrfs", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"zfs", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"f2fs", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"tmpfs", "0:1", posixCaps, readOnlyCaps(posixCaps)},
		{"vfat", "0:1", fatCaps, readOnlyCaps(fatCaps)},
		{"exfat", "0:1", exfatCaps, readOnlyCaps(exfatCaps)},
		{"ntfs", "0:1", ntfsCaps, readOnlyCaps(ntfsCaps)},
		{"ntfs3", "0:1", ntfsCaps, readOnlyCaps(ntfsCaps)},
		{"fuseblk.ntfs-3g", "0:1", ntfsCaps, readOnlyCaps(ntfsCaps)},
		{"fuseblk.lowntfs-3g", "0:1", ntfsCaps, readOnlyCaps(ntfsCaps)},
		{"fuseblk", "8:49", ntfsCaps, readOnlyCaps(ntfsCaps)},
		{"fuseblk", "8:65", UnknownCapabilities(false), UnknownCapabilities(true)},
		{"fuseblk", "0:1", UnknownCapabilities(false), UnknownCapabilities(true)},
		{"fuseblk.exfat", "0:1", UnknownCapabilities(false), UnknownCapabilities(true)},
		{"iso9660", "0:1", opticCaps, opticCaps},
		{"udf", "0:1", opticCaps, opticCaps},
		{"nfs4", "0:1", UnknownCapabilities(false), UnknownCapabilities(true)},
		{"fuse.sshfs", "0:1", UnknownCapabilities(false), UnknownCapabilities(true)},
		{"overlay", "0:1", UnknownCapabilities(false), UnknownCapabilities(true)},
	}
	var table strings.Builder
	table.WriteString("1 0 0:1 / / rw - ext4 /dev/root rw\n")
	for i, r := range rows {
		fmt.Fprintf(&table, "%d 1 %s / /rw/%d rw - %s src rw\n", 10+2*i, r.majorMinor, i, r.fsType)
		fmt.Fprintf(&table, "%d 1 %s / /ro/%d ro - %s src rw\n", 11+2*i, r.majorMinor, i, r.fsType)
	}
	o := fixtureFS(t, table.String(), map[string]string{
		"disk/by-uuid/4224167724166E63": "../../sdd1",
		"disk/by-uuid/2658-C1FD":        "../../sde1",
	}, map[string]string{"sdd1": "8:49", "sde1": "8:65"})
	for i, r := range rows {
		for _, c := range []struct {
			path string
			want Capabilities
		}{{fmt.Sprintf("/rw/%d/a/b", i), r.rw}, {fmt.Sprintf("/ro/%d", i), r.ro}} {
			got, err := o.Capabilities(c.path)
			if err != nil {
				t.Fatalf("Capabilities(%s) on %s: %v", c.path, r.fsType, err)
			}
			if got != c.want {
				t.Errorf("Capabilities(%s) on %s (%s) = %+v, want %+v", c.path, r.fsType, r.majorMinor, got, c.want)
			}
		}
	}
	if u := UnknownCapabilities(false); u.Known || u.CaseSensitive || u.NormalizationSensitive || u.StableIdentity ||
		u.LocalTime || u.HardLinks || u.TimeResolution != 2*time.Second {
		t.Errorf("unknown set %+v is not the conservative one", u)
	}
}

// TestLinuxCapabilitiesFollowDeepestMount: the mount holding a path is the
// deepest mount point containing it, the last of stacked mounts winning, and
// its read-only flag (per mount or per superblock) is reported.
func TestLinuxCapabilitiesFollowDeepestMount(t *testing.T) {
	o := fixtureFS(t, mountinfoFixture, nil, nil)
	for path, want := range map[string]Capabilities{
		"/":                         posixCaps,
		"/home/user":                posixCaps,
		"/srv/src/with space":       readOnlyCaps(posixCaps),
		"/srv/src/with space/a/b":   readOnlyCaps(posixCaps),
		"/srv/src/with spaceX":      posixCaps,
		"/mnt/tab\tand\\slash/x":    UnknownCapabilities(false),
		"/mnt/stack/x":              readOnlyCaps(posixCaps),
		"/mnt/empty/../stack/inner": readOnlyCaps(posixCaps),
	} {
		caps, err := o.Capabilities(path)
		if err != nil {
			t.Fatalf("Capabilities(%q): %v", path, err)
		}
		if caps != want {
			t.Errorf("Capabilities(%q) = %+v, want %+v", path, caps, want)
		}
	}

	_, err := o.Capabilities("relative")
	var e *Error
	if !errors.As(err, &e) || e.Outcome != "" || !errors.Is(err, errRelativePath) {
		t.Errorf("Capabilities(relative) = %v, want a refusal without outcome", err)
	}
	_, err = fixtureFS(t, "30 22 8:1 / /srv rw - ext4 /dev/sda1 rw\n", nil, nil).Capabilities("/home")
	if !errors.As(err, &e) || e.Outcome != domain.OutcomeUnavailable || !errors.Is(err, errNoMount) {
		t.Errorf("Capabilities outside every mount = %v, want unavailable errNoMount", err)
	}
	_, err = newOS(t.TempDir(), t.TempDir(), t.TempDir()).Capabilities("/")
	if !errors.As(err, &e) || e.Outcome != domain.OutcomeUnavailable {
		t.Errorf("Capabilities without a mount table = %v, want unavailable", err)
	}
}
