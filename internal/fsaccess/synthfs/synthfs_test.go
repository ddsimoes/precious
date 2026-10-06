package synthfs_test

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, fsys fsaccess.FS, path string) fsaccess.Dir {
	t.Helper()
	d, err := fsys.OpenRoot(path)
	must(t, err)
	t.Cleanup(func() { d.Close() })
	return d
}

func outcome(err error) domain.AccessOutcome {
	o, _ := fsaccess.OutcomeOf(err)
	return o
}

// A 200,000-entry directory read with batch 256: every ReadBatch returns at
// most 256 entries and the listing is never materialised (huge directory
// read in batches).
func TestBatchedListingHugeDirectory(t *testing.T) {
	const total, batch = 200_000, 256
	fsys := synthfs.New()
	fsys.Root("/huge").Generate(total, total)
	if fsys.EntriesListed() != 0 {
		t.Fatal("building the tree generated entries")
	}
	rec := instrument.Wrap(fsys)
	d := open(t, rec, "/huge")

	n := 0
	var prev []byte
	for {
		entries, err := d.ReadBatch(batch)
		if err == io.EOF {
			break
		}
		must(t, err)
		for _, e := range entries {
			// Generated names are zero-padded, so strictly increasing names
			// prove there are no duplicates.
			if bytes.Compare(prev, e.Name) >= 0 {
				t.Fatalf("name %q after %q", e.Name, prev)
			}
			prev = e.Name
		}
		n += len(entries)
	}
	if n != total {
		t.Fatalf("listed %d entries, want %d", n, total)
	}
	for _, c := range rec.Calls() {
		if c.Op == instrument.OpReadBatch && (c.N != batch || c.Entries > batch) {
			t.Fatalf("ReadBatch(%d) returned %d entries", c.N, c.Entries)
		}
	}
	if got, want := rec.Count(instrument.OpReadBatch), (total+batch-1)/batch+1; got != want {
		t.Errorf("ReadBatch calls = %d, want %d", got, want)
	}
	if fsys.MaxBatch() > batch {
		t.Errorf("synthfs materialised %d entries at once", fsys.MaxBatch())
	}

	last, err := d.Lstat([]byte("file-0199999.dat"))
	if err != nil || last.Kind != domain.EntryFile {
		t.Errorf("Lstat(last entry) = %+v, %v", last, err)
	}
	if _, err := d.Lstat([]byte("file-0200000.dat")); outcome(err) != domain.OutcomeAbsent {
		t.Errorf("Lstat past the end = %v, want absent", err)
	}
}

// A generated subtree has exactly the requested descendants, unique inodes,
// and listing kinds that agree with Lstat.
func TestGeneratedTreeIsComplete(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.File("loose", 3, time.Time{})
	root.Generated("tree", 1000, 7)
	d := open(t, fsys, "/src")

	inos := map[uint64]bool{d.Self().Ino: true}
	maxDepth := 0
	var walk func(d fsaccess.Dir, depth int) int
	walk = func(d fsaccess.Dir, depth int) int {
		maxDepth = max(maxDepth, depth)
		count := 0
		for {
			entries, err := d.ReadBatch(3)
			if err == io.EOF {
				return count
			}
			must(t, err)
			for _, e := range entries {
				count++
				info, err := d.Lstat(e.Name)
				must(t, err)
				if info.Kind != e.Kind || info.MountBoundary || !bytes.Equal(info.Name, e.Name) {
					t.Fatalf("Lstat(%q) = %+v, listed as %s", e.Name, info, e.Kind)
				}
				if inos[info.Ino] {
					t.Fatalf("inode %d reused by %q", info.Ino, e.Name)
				}
				inos[info.Ino] = true
				if info.Kind == domain.EntryDirectory {
					child, err := d.OpenDir(e.Name, info)
					must(t, err)
					count += walk(child, depth+1)
					must(t, child.Close())
				}
			}
		}
	}
	if got := walk(d, 0); got != 1002 {
		t.Errorf("walked %d entries, want 1002 (loose, tree, 1000 generated)", got)
	}
	if maxDepth < 3 {
		t.Errorf("tree depth %d; fanout 7 should nest at least 3 levels", maxDepth)
	}
}

// Mount boundaries by device change and by mount-table membership. A regular
// file on another device is a boundary too: a bind-mounted file (M4 design D5).
func TestMountBoundaries(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	external := root.Dir("external").Dev(999)
	external.File("inside", 1, time.Time{})
	root.Dir("again").MountPoint()
	root.Dir("plain")
	root.File("file-elsewhere", 1, time.Time{}).Dev(998)
	rec := instrument.Wrap(fsys)
	d := open(t, rec, "/src")

	for name, want := range map[string]bool{"external": true, "again": true, "plain": false, "file-elsewhere": true} {
		info, err := d.Lstat([]byte(name))
		must(t, err)
		if info.MountBoundary != want {
			t.Errorf("Lstat(%s).MountBoundary = %v, want %v", name, info.MountBoundary, want)
		}
	}
	info, err := d.Lstat([]byte("external"))
	must(t, err)
	if _, err := d.OpenDir([]byte("external"), info); !errors.Is(err, fsaccess.ErrMountBoundary) || outcome(err) != "" {
		t.Errorf("OpenDir(external) = %v, want a refusal", err)
	}
	info.MountBoundary = false
	if _, err := d.OpenDir([]byte("external"), info); !errors.Is(err, fsaccess.ErrMountBoundary) || outcome(err) != domain.OutcomeChangedDuringObservation {
		t.Errorf("OpenDir(external, flag dropped) = %v, want changed_during_observation", err)
	}
	if rec.MaxDepth() > 1 {
		t.Errorf("a call went below a boundary")
	}
}

func TestFSInfo(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	d := open(t, fsys, "/src")
	dev := d.Self().Dev
	info, err := d.FSInfo()
	must(t, err)
	if info.Type != synthfs.DefaultFSType || info.FSID != dev || info.Dev != dev || info.ReadOnly || info.Mount != nil {
		t.Errorf("default FSInfo = %+v", info)
	}
	mount := &fsaccess.MountInfo{MountID: 40, MajorMinor: "8:1", Root: "/", MountPoint: "/src", FSType: "ext4", Source: "/dev/sda1"}
	fsys.SetFSInfo(dev, fsaccess.FSInfo{Type: 0x9123683E, FSID: 77, ReadOnly: true, Mount: mount})
	mount.MountID = 0 // the FS keeps its own copy
	info, err = d.FSInfo()
	must(t, err)
	if info.Type != 0x9123683E || info.FSID != 77 || info.Dev != dev || !info.ReadOnly || info.Mount == nil || info.Mount.MountID != 40 {
		t.Errorf("configured FSInfo = %+v", info)
	}
	if got := root.Info(); got.Dev != dev || got.Ino != d.Self().Ino {
		t.Errorf("root Info() = %+v, Self() = %+v", got, d.Self())
	}
}

// TestMountsOnePathVolumePerDevice: every root's device is a weak path volume
// at the root's path, or where SetFSInfo's mount row puts it; a vanished root
// is not mounted; Capabilities is the unknown set with the mount's read-only.
func TestMountsOnePathVolumePerDevice(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src")
	fsys.Root("/media/usb/photos")
	fsys.Root("/gone")
	fsys.Vanish("/gone")
	usb := open(t, fsys, "/media/usb/photos").Self().Dev
	fsys.SetFSInfo(usb, fsaccess.FSInfo{ReadOnly: true, Mount: &fsaccess.MountInfo{
		MountID: 41, MajorMinor: "8:17", Root: "/", MountPoint: "/media/usb", FSType: "vfat", Source: "/dev/sdb1"}})

	mounts, err := fsys.Mounts()
	must(t, err)
	want := []fsaccess.Mount{
		{Point: "/src", Root: []byte("/"), Volume: fsaccess.Volume{Kind: fsaccess.VolumePath, ID: "/src", DeviceKey: "mount:/src"}},
		{Point: "/media/usb", Root: []byte("/"), ReadOnly: true, Volume: fsaccess.Volume{
			Kind: fsaccess.VolumePath, ID: "/media/usb", FSType: "vfat", DeviceKey: "mount:/media/usb"}},
	}
	if len(mounts) != len(want) {
		t.Fatalf("Mounts() = %+v, want %+v", mounts, want)
	}
	for i := range want {
		if m := mounts[i]; m.Point != want[i].Point || !bytes.Equal(m.Root, want[i].Root) || m.Volume != want[i].Volume || m.ReadOnly != want[i].ReadOnly {
			t.Errorf("mount %d = %+v, want %+v", i, m, want[i])
		}
	}

	for path, readOnly := range map[string]bool{"/src": false, "/src/a": false, "/media/usb/photos": true, "/elsewhere": false} {
		caps, err := fsys.Capabilities(path)
		must(t, err)
		if caps != fsaccess.UnknownCapabilities(readOnly) {
			t.Errorf("Capabilities(%s) = %+v, want the unknown set with ReadOnly %v", path, caps, readOnly)
		}
	}
	if _, err := fsys.Capabilities("relative"); err == nil {
		t.Error("Capabilities(relative) succeeded")
	}
}

func TestFailures(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.Dir("flaky").Generate(1000, 1000).FailListingAfter(300, domain.OutcomeUnavailable)
	root.Dir("locked").Unreadable()
	root.Dir("marker").FailLstat(domain.OutcomeUnreadable)
	root.File("typeless", 1, time.Time{}).HideKind()
	d := open(t, fsys, "/src")

	t.Run("listing fails after N entries", func(t *testing.T) {
		info, err := d.Lstat([]byte("flaky"))
		must(t, err)
		flaky, err := d.OpenDir([]byte("flaky"), info)
		must(t, err)
		defer flaky.Close()
		var sizes []int
		for {
			entries, err := flaky.ReadBatch(256)
			if err != nil {
				if err == io.EOF || outcome(err) != domain.OutcomeUnavailable {
					t.Fatalf("listing ended with %v, want unavailable", err)
				}
				break
			}
			sizes = append(sizes, len(entries))
		}
		if len(sizes) != 2 || sizes[0] != 256 || sizes[1] != 44 {
			t.Errorf("batches before the failure = %v, want [256 44]", sizes)
		}
	})
	t.Run("unreadable directory", func(t *testing.T) {
		info, err := d.Lstat([]byte("locked"))
		must(t, err)
		if info.Mode.Perm() != 0 {
			t.Errorf("unreadable mode = %v", info.Mode)
		}
		if _, err := d.OpenDir([]byte("locked"), info); outcome(err) != domain.OutcomeUnreadable {
			t.Errorf("OpenDir(locked) = %v, want unreadable", err)
		}
	})
	t.Run("lstat failure", func(t *testing.T) {
		if _, err := d.Lstat([]byte("marker")); outcome(err) != domain.OutcomeUnreadable {
			t.Errorf("Lstat(marker) = %v, want unreadable", err)
		}
		if _, err := d.Lstat([]byte("missing")); outcome(err) != domain.OutcomeAbsent {
			t.Errorf("Lstat(missing) = %v, want absent", err)
		}
	})
	t.Run("hidden kind", func(t *testing.T) {
		d := open(t, fsys, "/src")
		entries, err := d.ReadBatch(10)
		must(t, err)
		for _, e := range entries {
			if string(e.Name) == "typeless" && e.Kind != domain.EntryUnknown {
				t.Errorf("listed kind %s, want unknown", e.Kind)
			}
		}
		if info, err := d.Lstat([]byte("typeless")); err != nil || info.Kind != domain.EntryFile {
			t.Errorf("Lstat(typeless) = %+v, %v", info, err)
		}
	})
}

// Entries replaced between Lstat and OpenDir are detected, not followed.
func TestSwappedEntries(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.Dir("victim").File("secret", 1, time.Time{})
	root.Symlink("link", "victim")
	d := open(t, fsys, "/src")

	info, err := d.Lstat([]byte("victim"))
	must(t, err)
	root.Remove("victim")
	root.Symlink("victim", "/")
	if _, err := d.OpenDir([]byte("victim"), info); outcome(err) != domain.OutcomeChangedDuringObservation {
		t.Errorf("OpenDir(victim -> /) = %v, want changed_during_observation", err)
	}
	root.Remove("victim")
	root.Dir("victim")
	if _, err := d.OpenDir([]byte("victim"), info); outcome(err) != domain.OutcomeChangedDuringObservation {
		t.Errorf("OpenDir(new victim) = %v, want changed_during_observation", err)
	}
	if _, err := d.Readlink([]byte("victim")); outcome(err) != domain.OutcomeChangedDuringObservation {
		t.Errorf("Readlink(directory) = %v, want changed_during_observation", err)
	}
	if target, err := d.Readlink([]byte("link")); err != nil || string(target) != "victim" {
		t.Errorf("Readlink(link) = %q, %v", target, err)
	}
	linkInfo, err := d.Lstat([]byte("link"))
	must(t, err)
	if _, err := d.OpenDir([]byte("link"), linkInfo); !errors.Is(err, fsaccess.ErrNotDirectory) {
		t.Errorf("OpenDir(symlink) = %v, want ErrNotDirectory", err)
	}
}

// A vanished root makes every later call unavailable; reattaching restores
// the same identity. Root on an existing path models a different disk.
func TestVanishReattachReplace(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src").Dir("sub")
	d := open(t, fsys, "/src")
	info, err := d.Lstat([]byte("sub"))
	must(t, err)
	sub, err := d.OpenDir([]byte("sub"), info)
	must(t, err)
	defer sub.Close()
	self := d.Self()

	fsys.Vanish("/src")
	for name, err := range map[string]error{
		"OpenRoot":        second(fsys.OpenRoot("/src")),
		"ReadBatch":       second(d.ReadBatch(1)),
		"Lstat":           second(d.Lstat([]byte("sub"))),
		"OpenDir":         second(d.OpenDir([]byte("sub"), info)),
		"Readlink":        second(d.Readlink([]byte("sub"))),
		"FSInfo":          second(d.FSInfo()),
		"child ReadBatch": second(sub.ReadBatch(1)),
	} {
		if outcome(err) != domain.OutcomeUnavailable {
			t.Errorf("%s after Vanish = %v, want unavailable", name, err)
		}
	}

	fsys.Reattach("/src")
	again := open(t, fsys, "/src")
	if s := again.Self(); s.Dev != self.Dev || s.Ino != self.Ino {
		t.Errorf("reattached root %+v, want identity of %+v", s, self)
	}
	fsys.Root("/src")
	other := open(t, fsys, "/src")
	if s := other.Self(); s.Dev == self.Dev && s.Ino == self.Ino {
		t.Error("a replaced root kept the old identity")
	}
}

func TestNameValidation(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src").Dir("a")
	d := open(t, fsys, "/src")
	for _, bad := range []string{"../x", "a/b", ".", "..", "", "a\x00b"} {
		_, err := d.Lstat([]byte(bad))
		if !errors.Is(err, fsaccess.ErrInvalidName) {
			t.Errorf("Lstat(%q) = %v, want ErrInvalidName", bad, err)
		}
		_, err = d.OpenDir([]byte(bad), fsaccess.EntryInfo{Kind: domain.EntryDirectory})
		if !errors.Is(err, fsaccess.ErrInvalidName) {
			t.Errorf("OpenDir(%q) = %v, want ErrInvalidName", bad, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
