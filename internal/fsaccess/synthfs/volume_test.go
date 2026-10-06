package synthfs_test

import (
	"bytes"
	"errors"
	"io"
	"path"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

var (
	fatCaps   = fsaccess.Capabilities{Known: true, NormalizationSensitive: true, LocalTime: true, TimeResolution: 2 * time.Second}
	exfatCaps = fsaccess.Capabilities{Known: true, NormalizationSensitive: true, TimeResolution: 10 * time.Millisecond}
	ext4Caps  = fsaccess.Capabilities{Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true, TimeResolution: time.Nanosecond}
	usbVolume = fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "2658-C1FD", Label: "CARTAO", FSType: "vfat", DeviceKey: "dev:8:17", Strong: true}
)

func lstat(t *testing.T, d fsaccess.Dir, name string) fsaccess.EntryInfo {
	t.Helper()
	info, err := d.Lstat([]byte(name))
	must(t, err)
	return info
}

func mounted(t *testing.T, fsys fsaccess.FS) map[string]fsaccess.Mount {
	t.Helper()
	mounts, err := fsys.Mounts()
	must(t, err)
	byPoint := make(map[string]fsaccess.Mount, len(mounts))
	for _, m := range mounts {
		byPoint[m.Point] = m
	}
	return byPoint
}

// TestSetVolumeAndCapabilities: Mounts reports the volume SetVolume gave and
// Capabilities the set SetCapabilities gave, read-only also when the mount
// is; other devices keep the weak path volume and the unknown set.
func TestSetVolumeAndCapabilities(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/media/usb/fotos")
	fsys.Root("/srv/data")
	usb := open(t, fsys, "/media/usb/fotos").Self().Dev
	fsys.SetFSInfo(usb, fsaccess.FSInfo{Mount: &fsaccess.MountInfo{MountPoint: "/media/usb", Root: "/", FSType: "vfat"}})
	fsys.SetVolume(usb, usbVolume)
	fsys.SetCapabilities(usb, fatCaps)

	m := mounted(t, fsys)
	if got := m["/media/usb"]; got.Volume != usbVolume || got.ReadOnly || string(got.Root) != "/" {
		t.Errorf("usb mount = %+v, want volume %+v", got, usbVolume)
	}
	if got := m["/srv/data"].Volume; got.Kind != fsaccess.VolumePath || got.ID != "/srv/data" || got.Strong {
		t.Errorf("other volume = %+v, want the weak path volume", got)
	}
	caps, err := fsys.Capabilities("/media/usb/fotos/2008")
	must(t, err)
	if caps != fatCaps {
		t.Errorf("Capabilities on the card = %+v, want %+v", caps, fatCaps)
	}
	caps, err = fsys.Capabilities("/srv/data")
	must(t, err)
	if caps != fsaccess.UnknownCapabilities(false) {
		t.Errorf("Capabilities elsewhere = %+v, want the unknown set", caps)
	}

	fsys.SetFSInfo(usb, fsaccess.FSInfo{ReadOnly: true, Mount: &fsaccess.MountInfo{MountPoint: "/media/usb", Root: "/", FSType: "vfat"}})
	caps, err = fsys.Capabilities("/media/usb/fotos")
	must(t, err)
	if want := fatCaps; !caps.ReadOnly || caps.TimeResolution != want.TimeResolution || !caps.LocalTime {
		t.Errorf("Capabilities on a read-only mount = %+v, want %+v read-only", caps, want)
	}
}

// TestCaseInsensitiveLookup: on a case-insensitive device a name is found
// regardless of letter case, an exact match first, and the name asked for is
// reported; a case-sensitive device and one without capabilities match
// exactly.
func TestCaseInsensitiveLookup(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/card")
	photo := root.Dir("DCIM").File("Foto.JPG", 10, builtTime)
	root.Dir("a")
	root.Dir("A")
	root.Generated("gen", 3, 3)
	fsys.Root("/exact").Dir("DCIM")

	exact := open(t, fsys, "/exact")
	if _, err := exact.Lstat([]byte("dcim")); outcome(err) != domain.OutcomeAbsent {
		t.Errorf("Lstat(dcim) without capabilities = %v, want absent", err)
	}

	d := open(t, fsys, "/card")
	fsys.SetCapabilities(d.Self().Dev, fatCaps)
	info := lstat(t, d, "dcim")
	if string(info.Name) != "dcim" || info.Kind != domain.EntryDirectory {
		t.Errorf("Lstat(dcim) = %+v, want the DCIM directory under the name asked for", info)
	}
	dcim, err := d.OpenDir([]byte("dcim"), info)
	must(t, err)
	defer dcim.Close()
	if f := lstat(t, dcim, "foto.jpg"); f.Ino != photo.Info().Ino || f.Size != 10 {
		t.Errorf("Lstat(foto.jpg) = %+v, want Foto.JPG", f)
	}
	if a := lstat(t, d, "A"); a.Ino != root.Child("A").Info().Ino {
		t.Errorf("Lstat(A) = %+v, want the exact match", a)
	}
	gen, err := d.OpenDir([]byte("GEN"), lstat(t, d, "GEN"))
	must(t, err)
	defer gen.Close()
	if f := lstat(t, gen, "FILE-0000000.DAT"); f.Kind != domain.EntryFile {
		t.Errorf("Lstat(FILE-0000000.DAT) = %+v, want the generated file", f)
	}
	if _, err := d.Lstat([]byte("dcimx")); outcome(err) != domain.OutcomeAbsent {
		t.Errorf("Lstat(dcimx) = %v, want absent", err)
	}
	fsys.SetCapabilities(d.Self().Dev, ext4Caps)
	if _, err := d.Lstat([]byte("dcim")); outcome(err) != domain.OutcomeAbsent {
		t.Errorf("Lstat(dcim) on a case-sensitive device = %v, want absent", err)
	}
}

// builtTime has a fractional second, so truncation shows.
var builtTime = time.Date(2008, 10, 18, 15, 30, 13, 987_654_321, time.UTC)

// TestTimesFollowResolutionAndLocalTime: reported modification times are
// truncated to the device's resolution; on a local-time device they are read
// in the zone it is mounted with, so moving that zone by an hour shifts them
// by an hour; OpenFile accepts what Lstat reported.
func TestTimesFollowResolutionAndLocalTime(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/card")
	root.File("a.jpg", 5, builtTime).Content([]byte("hello"))
	root.Generated("gen", 1, 1)
	d := open(t, fsys, "/card")
	dev := d.Self().Dev
	if got := lstat(t, d, "a.jpg").ModTime; !got.Equal(builtTime) {
		t.Errorf("ModTime without capabilities = %v, want %v as built", got, builtTime)
	}

	fsys.SetCapabilities(dev, exfatCaps)
	if got, want := lstat(t, d, "a.jpg").ModTime, builtTime.Truncate(10*time.Millisecond); !got.Equal(want) {
		t.Errorf("exFAT ModTime = %v, want %v", got, want)
	}
	fsys.SetTimeZone(dev, time.FixedZone("summer", 3600))
	if got, want := lstat(t, d, "a.jpg").ModTime, builtTime.Truncate(10*time.Millisecond); !got.Equal(want) {
		t.Errorf("exFAT ModTime with a zone = %v, want %v: exFAT does not store local time", got, want)
	}

	fsys.SetCapabilities(dev, fatCaps)
	fsys.SetTimeZone(dev, nil)
	fat := builtTime.Truncate(2 * time.Second)
	info := lstat(t, d, "a.jpg")
	if !info.ModTime.Equal(fat) || fat.Second()%2 != 0 || fat.Nanosecond() != 0 {
		t.Errorf("FAT ModTime = %v, want %v", info.ModTime, fat)
	}
	gen := lstat(t, d, "gen")
	if !gen.ModTime.Equal(gen.ModTime.Truncate(2 * time.Second)) {
		t.Errorf("generated ModTime %v is not on an even second", gen.ModTime)
	}

	fsys.SetTimeZone(dev, time.FixedZone("summer", 3600))
	shifted := lstat(t, d, "a.jpg")
	if want := fat.Add(-time.Hour); !shifted.ModTime.Equal(want) {
		t.Errorf("FAT ModTime read an hour east = %v, want %v", shifted.ModTime, want)
	}
	f, err := d.OpenFile([]byte("a.jpg"), shifted)
	must(t, err)
	defer f.Close()
	st, err := f.Stat()
	must(t, err)
	if !st.ModTime.Equal(shifted.ModTime) {
		t.Errorf("open file ModTime = %v, want %v", st.ModTime, shifted.ModTime)
	}
	if _, err := d.OpenFile([]byte("a.jpg"), info); outcome(err) != domain.OutcomeChangedDuringObservation {
		t.Errorf("OpenFile with the time read before the zone moved = %v, want changed_during_observation", err)
	}

	genDir, err := d.OpenDir([]byte("gen"), gen)
	must(t, err)
	defer genDir.Close()
	gf := lstat(t, genDir, "file-0000000.dat")
	gfile, err := genDir.OpenFile([]byte("file-0000000.dat"), gf)
	must(t, err)
	defer gfile.Close()
	if st, err := gfile.Stat(); err != nil || !st.ModTime.Equal(gf.ModTime) || st.Ino != gf.Ino {
		t.Errorf("generated file Stat = %+v, %v; want %+v", st, err, gf)
	}
}

// TestRemountRenumbersWithoutStableIdentity: Remount gives every entry a new
// inode number on a device without stable identity (the unknown set
// included), keeping names, sizes, times, and content; a device with stable
// identity keeps its numbers.
func TestRemountRenumbersWithoutStableIdentity(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/card")
	root.Dir("sub").File("f", 4, builtTime).Content([]byte("data"))
	root.Generated("gen", 2, 2)
	d := open(t, fsys, "/card")
	dev := d.Self().Dev

	before := lstat(t, d, "sub")
	beforeGen := lstat(t, d, "gen")
	fsys.Remount(dev)
	after := lstat(t, d, "sub")
	afterGen := lstat(t, d, "gen")
	if after.Ino == before.Ino || afterGen.Ino == beforeGen.Ino {
		t.Fatalf("Remount without stable identity kept inode numbers: %d→%d, %d→%d", before.Ino, after.Ino, beforeGen.Ino, afterGen.Ino)
	}
	if after.Dev != before.Dev || !after.ModTime.Equal(before.ModTime) || afterGen.Size != beforeGen.Size || !afterGen.ModTime.Equal(beforeGen.ModTime) {
		t.Errorf("Remount changed more than inode numbers: %+v → %+v, %+v → %+v", before, after, beforeGen, afterGen)
	}
	if again := open(t, fsys, "/card").Self(); again.Ino == d.Self().Ino {
		t.Error("the root kept its inode number")
	}
	if _, err := d.OpenDir([]byte("sub"), before); outcome(err) != domain.OutcomeChangedDuringObservation {
		t.Errorf("OpenDir with the pre-remount identity = %v, want changed_during_observation", err)
	}
	sub, err := d.OpenDir([]byte("sub"), after)
	must(t, err)
	defer sub.Close()
	fi := lstat(t, sub, "f")
	f, err := sub.OpenFile([]byte("f"), fi)
	must(t, err)
	defer f.Close()
	buf := make([]byte, 4)
	if n, err := f.ReadAt(buf, 0); (err != nil && err != io.EOF) || n != 4 || string(buf) != "data" {
		t.Errorf("content after Remount = %q, %v", buf[:n], err)
	}

	fsys.SetCapabilities(dev, ext4Caps)
	fsys.Remount(dev)
	if stable := lstat(t, d, "sub"); stable.Ino != after.Ino {
		t.Errorf("Remount with stable identity changed the inode: %d → %d", after.Ino, stable.Ino)
	}
}

// TestUnmountAndMountElsewhere: Unmount takes the device out of Mounts and
// makes its roots and handles unavailable; Mount brings it back at another
// point, moving its roots along (relative to the mount point), keeping the
// volume SetVolume gave, renumbering its entries, and reporting the new point
// in FSInfo. A weak path volume is known by the new point.
func TestUnmountAndMountElsewhere(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/media/usb/fotos").File("a.jpg", 1, builtTime)
	fsys.Root("/srv/data")
	d := open(t, fsys, "/media/usb/fotos")
	usb := d.Self().Dev
	fsys.SetFSInfo(usb, fsaccess.FSInfo{Mount: &fsaccess.MountInfo{MountID: 40, MountPoint: "/media/usb", Root: "/", FSType: "vfat"}})
	fsys.SetVolume(usb, usbVolume)
	before := lstat(t, d, "a.jpg")

	fsys.Unmount(usb)
	if _, ok := mounted(t, fsys)["/media/usb"]; ok {
		t.Error("an unmounted device is still listed")
	}
	if _, ok := mounted(t, fsys)["/srv/data"]; !ok {
		t.Error("another device left the mount table")
	}
	if _, err := fsys.OpenRoot("/media/usb/fotos"); outcome(err) != domain.OutcomeUnavailable {
		t.Errorf("OpenRoot after Unmount = %v, want unavailable", err)
	}
	if _, err := d.Lstat([]byte("a.jpg")); outcome(err) != domain.OutcomeUnavailable {
		t.Errorf("Lstat on an old handle after Unmount = %v, want unavailable", err)
	}

	fsys.Mount(usb, "/run/media/owner/CARTAO")
	m, ok := mounted(t, fsys)["/run/media/owner/CARTAO"]
	if !ok || m.Volume != usbVolume {
		t.Fatalf("Mounts after Mount = %+v, want the card's volume at the new point", mounted(t, fsys))
	}
	if _, err := fsys.OpenRoot("/media/usb/fotos"); outcome(err) != domain.OutcomeUnavailable {
		t.Errorf("OpenRoot at the old path = %v, want unavailable", err)
	}
	moved := open(t, fsys, "/run/media/owner/CARTAO/fotos")
	if string(moved.Self().Name) != "fotos" {
		t.Errorf("moved root name %q, want fotos", moved.Self().Name)
	}
	after := lstat(t, moved, "a.jpg")
	if after.Ino == before.Ino || after.Size != before.Size || !after.ModTime.Equal(before.ModTime) {
		t.Errorf("a.jpg after Mount = %+v, want %+v with a new inode", after, before)
	}
	info, err := moved.FSInfo()
	must(t, err)
	if info.Mount == nil || info.Mount.MountPoint != "/run/media/owner/CARTAO" || info.Mount.MountID != 40 {
		t.Errorf("FSInfo mount row = %+v, want the new point", info.Mount)
	}
	if _, err := d.Lstat([]byte("a.jpg")); err != nil {
		t.Errorf("Lstat on an old handle after Mount = %v, want it working again", err)
	}

	// A root at the mount point moves to the new point and takes its name.
	disk := fsys.Root("/mnt/disk")
	disk.Dir("x")
	dev := disk.Info().Dev
	fsys.Mount(dev, "/mnt/other")
	if v := mounted(t, fsys)["/mnt/other"].Volume; v.Kind != fsaccess.VolumePath || v.ID != "/mnt/other" || v.DeviceKey != "mount:/mnt/other" {
		t.Errorf("weak volume after Mount = %+v, want one known by the new point", v)
	}
	other := open(t, fsys, "/mnt/other")
	if string(other.Self().Name) != "other" {
		t.Errorf("root name %q, want other", other.Self().Name)
	}
	lstat(t, other, "x")
	if _, err := fsys.OpenRoot("/mnt/disk"); outcome(err) != domain.OutcomeUnavailable {
		t.Errorf("OpenRoot(/mnt/disk) after the move = %v, want unavailable", err)
	}
}

// TestFATFixtureWithFATCapabilities: the corpus FAT fixture given FAT
// capabilities through the knobs (R1.17's setup) reports them, keeps every
// modification time (all on even seconds), finds names regardless of case,
// and after a remount reports the same names, sizes, and times under new
// inode numbers.
func TestFATFixtureWithFATCapabilities(t *testing.T) {
	fsys := synthfs.New()
	root, truth := corpus.BuildSynth(fsys, "/media/card", corpus.FATFixture())
	before := walk(t, fsys, "/media/card")
	dev := root.Info().Dev
	fsys.SetVolume(dev, usbVolume)
	fsys.SetCapabilities(dev, fatCaps)

	caps, err := fsys.Capabilities("/media/card/DCIM")
	must(t, err)
	if caps != fatCaps {
		t.Fatalf("Capabilities = %+v, want %+v", caps, fatCaps)
	}
	fat := walk(t, fsys, "/media/card")
	if len(fat) != len(truth.Entries) {
		t.Fatalf("walked %d entries, ground truth has %d", len(fat), len(truth.Entries))
	}
	for p, e := range fat {
		b := before[p]
		if e.Size != b.Size || !e.ModTime.Equal(b.ModTime) || e.Ino != b.Ino {
			t.Errorf("%s with FAT capabilities = %+v, want %+v", p, e, b)
		}
	}
	d := open(t, fsys, "/media/card")
	if info := lstat(t, d, "dcim"); info.Kind != domain.EntryDirectory {
		t.Errorf("Lstat(dcim) = %+v, want DCIM", info)
	}

	fsys.Remount(dev)
	for p, e := range walk(t, fsys, "/media/card") {
		b := fat[p]
		if e.Ino == b.Ino || e.Size != b.Size || !e.ModTime.Equal(b.ModTime) || e.Kind != b.Kind {
			t.Errorf("%s after Remount = %+v, want %+v with a new inode", p, e, b)
		}
	}
}

// walk returns every entry below root by relative path.
func walk(t *testing.T, fsys fsaccess.FS, root string) map[string]fsaccess.EntryInfo {
	t.Helper()
	out := make(map[string]fsaccess.EntryInfo)
	var visit func(d fsaccess.Dir, prefix string)
	visit = func(d fsaccess.Dir, prefix string) {
		for {
			entries, err := d.ReadBatch(64)
			if errors.Is(err, io.EOF) {
				return
			}
			must(t, err)
			for _, e := range entries {
				info := lstat(t, d, string(e.Name))
				if !bytes.Equal(info.Name, e.Name) {
					t.Fatalf("Lstat(%q) reported %q", e.Name, info.Name)
				}
				p := path.Join(prefix, string(e.Name))
				out[p] = info
				if info.Kind == domain.EntryDirectory && !info.MountBoundary {
					sub, err := d.OpenDir(e.Name, info)
					must(t, err)
					visit(sub, p)
					sub.Close()
				}
			}
		}
	}
	visit(open(t, fsys, root), "")
	return out
}
