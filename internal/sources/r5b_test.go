package sources

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index/indextest"
)

// ntfsCaps are NTFS's capabilities: stable identity, like ext4's.
var ntfsCaps = fsaccess.Capabilities{Known: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: 100 * time.Nanosecond, NoReplaceRename: true}

// unknownCaps are what an unclassified type gets, such as a bare fuseblk
// mount (ntfs-3g) where /dev/disk is not visible, or overlay.
var unknownCaps = fsaccess.UnknownCapabilities(false)

// pathFS gives dev's mount the filesystem type fsType at point, and caps.
func (e *env) pathFS(dev uint64, point, fsType string, caps fsaccess.Capabilities) {
	e.fs.SetFSInfo(dev, fsaccess.FSInfo{Mount: &fsaccess.MountInfo{MountPoint: point, Root: "/", FSType: fsType}})
	e.fs.SetCapabilities(dev, caps)
}

// pathSource puts a disk of type fsType and capabilities caps at base/name,
// as a weak path volume, registers source id on it, rooted at the folder rel
// below the mount point (the mount point itself when rel is empty), with a
// few seeded entries, records the root folder's inode as a scan does (unless
// noIno), and refreshes. It returns the disk's device and mount point.
func (e *env) pathSource(id, name, rel, fsType string, caps fsaccess.Capabilities, noIno bool) (uint64, string) {
	e.t.Helper()
	point := filepath.Join(e.base, name)
	root := e.fs.Root(point)
	dev := root.Info().Dev
	if rel != "" {
		root = e.folder(dev, filepath.Join(point, rel))
	}
	e.pathFS(dev, point, fsType, caps)
	e.insertSource(id, fsaccess.Volume{Kind: fsaccess.VolumePath, ID: point, FSType: fsType, DeviceKey: "mount:" + point}, rel)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`UPDATE sources SET capabilities = ? WHERE id = ?`, string(capsJSON), id); err != nil {
		e.t.Fatal(err)
	}
	indextest.Seed(e.t, e.st, indextest.Tree{Source: domain.SourceID(id), Nodes: []indextest.Node{
		{Path: "2004/a.jpg", Size: 1000, FileKind: domain.FileKindImage},
		{Path: "notes.txt", Size: 30},
	}})
	if !noIno {
		e.setRootIno(domain.SourceID(id), root.Info().Ino)
	}
	e.refresh()
	if got := e.get(domain.SourceID(id)); got.State != StateOnline || got.MountPoint != point {
		e.t.Fatalf("%s on its own disk = %+v, want online at %s", id, got, point)
	}
	return dev, point
}

// setRootIno records ino as the inode of source id's root folder, with the
// scanner's bit-cast.
func (e *env) setRootIno(id domain.SourceID, ino uint64) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(`UPDATE entries SET ino = ? WHERE source_id = ? AND path = X''`,
		int64(ino), string(id)); err != nil {
		e.t.Fatal(err)
	}
}

// standIn moves the source's disk dev away from point and puts a new folder
// of type fsType and capabilities caps there, as a container's bind of an
// empty folder does while the disk is not mounted, with a folder rel in it
// when rel is not empty. It returns the stand-in's device.
func (e *env) standIn(dev uint64, point, rel, fsType string, caps fsaccess.Capabilities) uint64 {
	e.t.Helper()
	e.fs.Mount(dev, filepath.Join(e.base, "away", filepath.Base(point)))
	other := e.fs.Root(point).Info().Dev
	if rel != "" {
		e.folder(other, filepath.Join(point, rel))
	}
	e.pathFS(other, point, fsType, caps)
	return other
}

// restore takes the stand-in other away from point and mounts the source's
// disk dev there again.
func (e *env) restore(dev, other uint64, point string) {
	e.t.Helper()
	e.fs.Mount(other, filepath.Join(e.base, "gone", filepath.Base(point)))
	e.fs.Mount(dev, point)
}

// wantOnline refreshes and checks that source id is online at point.
func (e *env) wantOnline(id domain.SourceID, point, what string) {
	e.t.Helper()
	e.refresh()
	if got := e.get(id); got.State != StateOnline || got.StateReason != "" || got.MountPoint != point {
		e.t.Fatalf("%s: %s = %+v, want online at %s", what, id, got, point)
	}
}

// wantOtherFilesystem checks that source id reads offline with reason
// other_filesystem, keeping its recorded capabilities, that Open refuses it,
// and that its entries are as before.
func (e *env) wantOtherFilesystem(id domain.SourceID, caps fsaccess.Capabilities, before string) {
	e.t.Helper()
	e.refresh()
	got := e.get(id)
	if got.State != StateOffline || got.StateReason != ReasonOtherFilesystem || got.MountPoint != "" || got.Caps != caps {
		e.t.Fatalf("with another filesystem at its mount point, %s = %+v, want offline/%s with its own capabilities",
			id, got, ReasonOtherFilesystem)
	}
	_, err := e.svc.Open(context.Background(), id)
	wantCode(e.t, err, domain.CodeSourceOffline)
	if after := e.entriesSnapshot(id); after != before {
		e.t.Fatalf("entries of %s changed:\n%s\nwant:\n%s", id, after, before)
	}
}

// r5b D1, D2 (spec source-registry, "An empty folder in place of a path
// source's disk", "The same disk through another driver"): an NTFS path
// source whose mount point holds a folder of the host's ext4 instead reads
// offline/other_filesystem and cannot be opened, and is online again with
// its disk back, also when that disk comes back through ntfs-3g: a bare
// fuseblk mount without /dev/disk, which gets the unknown capabilities.
func TestPathSourceStandInFolder(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("hd", "hd-elements", "", "ntfs3", ntfsCaps, false)
	before := e.entriesSnapshot("hd")

	other := e.standIn(dev, point, "", "ext4", ext4Caps)
	e.wantOtherFilesystem("hd", ntfsCaps, before)

	e.restore(dev, other, point)
	e.wantOnline("hd", point, "with its disk back")
	op, err := e.svc.Open(context.Background(), "hd")
	if err != nil {
		t.Fatal(err)
	}
	op.Root.Close()

	e.pathFS(dev, point, "fuseblk", unknownCaps)
	e.wantOnline("hd", point, "the same disk through ntfs-3g")
	if after := e.entriesSnapshot("hd"); after != before {
		t.Fatalf("entries changed:\n%s\nwant:\n%s", after, before)
	}
}

// r5b A1 (spec source-registry, "The same disk through another driver"): a
// source added through ntfs-3g, with fuseblk's unknown capabilities, is
// still the source through ntfs3; an overlay folder there is not.
func TestPathSourceAddedThroughNTFS3G(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("hd", "hd", "", "fuseblk", unknownCaps, false)
	before := e.entriesSnapshot("hd")
	e.pathFS(dev, point, "ntfs3", ntfsCaps)
	e.wantOnline("hd", point, "the same disk through ntfs3")
	e.standIn(dev, point, "", "overlay", unknownCaps)
	e.wantOtherFilesystem("hd", ntfsCaps, before)
}

// r5b D1 (spec source-registry, "A folder of a filesystem of the same type
// at a path source's mount point"): a folder of another filesystem of the
// source's own type, bound there, has another inode and is not the source.
func TestPathSourceSameTypeOtherRoot(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("tmp", "tmp", "", "tmpfs", ext4Caps, false)
	before := e.entriesSnapshot("tmp")
	e.standIn(dev, point, "", "tmpfs", ext4Caps)
	e.wantOtherFilesystem("tmp", ext4Caps, before)
}

// r5b D1: a source with stable inodes, and a stand-in whose type has none
// known (overlay), are told apart by type.
func TestPathSourceStandInWithoutStableInodes(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("disk", "disk", "", "ext4", ext4Caps, false)
	before := e.entriesSnapshot("disk")
	e.standIn(dev, point, "", "overlay", unknownCaps)
	e.wantOtherFilesystem("disk", ext4Caps, before)
}

// r5b D1: a source rooted at a folder below its mount point is judged by that
// folder's inode, not the mount point's: a stand-in that holds a folder of
// the same name is not the source.
func TestPathSourceRootedBelowItsMountPoint(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("fotos", "hd", "Fotos", "ntfs3", ntfsCaps, false)
	before := e.entriesSnapshot("fotos")
	e.standIn(dev, point, "Fotos", "ntfs3", ntfsCaps)
	e.wantOtherFilesystem("fotos", ntfsCaps, before)
}

// r5b D1 (spec source-registry, "Filesystems without stable inodes compare
// by type"): a FAT source mounted again, its inodes renumbered, is still the
// source; a folder of another type at its mount point is not.
func TestPathSourceWithoutStableInodes(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("card", "card", "", "vfat", fatCaps, false)
	before := e.entriesSnapshot("card")
	rootIno := e.get("card").RootIno
	e.fs.Remount(dev)
	op, err := e.svc.Open(context.Background(), "card")
	if err != nil {
		t.Fatalf("the remounted card: %v", err)
	}
	if ino := op.Root.Self().Ino; ino == rootIno {
		t.Fatalf("the remount kept the root inode %d; the test needs a renumbered one", ino)
	}
	op.Root.Close()
	if got := e.get("card"); got.State != StateOnline {
		t.Fatalf("the remounted card = %+v, want online", got)
	}
	e.standIn(dev, point, "", "ext4", ext4Caps)
	e.wantOtherFilesystem("card", fatCaps, before)
}

// r5b D3: add-source records the root folder's inode, so a stand-in that
// takes the disk's place before the first scan completes is not the source.
func TestPathSourceAddedRecordsTheRootInode(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("disk", fsaccess.Volume{})
	point := filepath.Join(e.base, "disk")
	e.pathFS(dev, point, "ext4", ext4Caps)
	src := e.add(point, "")
	op, err := e.svc.Open(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	ino := op.Root.Self().Ino
	op.Root.Close()
	if src.RootIno == 0 || src.RootIno != ino {
		t.Fatalf("the added source records root inode %d, its folder has %d", src.RootIno, ino)
	}
	before := e.entriesSnapshot(src.ID)
	e.standIn(dev, point, "", "ext4", ext4Caps)
	e.wantOtherFilesystem(src.ID, ext4Caps, before)
}

// r5b D3: a source whose root entry has no inode recorded (added before r5b
// and never scanned since) is judged by type, also on a filesystem with
// stable inodes.
func TestPathSourceWithoutRecordedRootInode(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("hd", "hd", "", "ntfs3", ntfsCaps, true)
	if got := e.get("hd").RootIno; got != 0 {
		t.Fatalf("RootIno = %d, want 0 without a recorded inode", got)
	}
	before := e.entriesSnapshot("hd")
	e.standIn(dev, point, "", "ext4", ext4Caps)
	e.wantOtherFilesystem("hd", ntfsCaps, before)
}

// r5b D4: a strong volume is found by its identity alone. Its root folder,
// with another inode than the one recorded, is still the source's.
func TestStrongSourceIgnoresTheRootInode(t *testing.T) {
	e := newEnv(t)
	vol := uuidVolume("u-1", "")
	e.disk("usb", vol)
	e.insertSource("usb", vol, "")
	indextest.Seed(t, e.st, indextest.Tree{Source: "usb", Nodes: []indextest.Node{{Path: "notes.txt", Size: 30}}})
	e.refresh()
	op, err := e.svc.Open(context.Background(), "usb")
	if err != nil {
		t.Fatal(err)
	}
	e.setRootIno("usb", op.Root.Self().Ino+1000)
	op.Root.Close()
	e.wantOnline("usb", filepath.Join(e.base, "usb"), "a strong volume with another root inode recorded")
}

// swapFS runs swap once, before an OpenRoot of at: a filesystem mounted at a
// path source's mount point after the mount table was read.
type swapFS struct {
	*synthfs.FS
	at   string
	swap func()
}

func (f *swapFS) OpenRoot(path string) (fsaccess.Dir, error) {
	if path == f.at && f.swap != nil {
		swap := f.swap
		f.swap = nil
		swap()
	}
	return f.FS.OpenRoot(path)
}

// r5b D1: the folder opened is judged by its own filesystem's type and
// capabilities, read after the open, not by the mount table read before.
func TestPathSourceSwappedBeforeTheOpen(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("card", "card", "", "vfat", fatCaps, false)
	before := e.entriesSnapshot("card")
	fsys := &swapFS{FS: e.fs, at: point, swap: func() { e.standIn(dev, point, "", "ext4", ext4Caps) }}
	svc, err := New(e.st, fsys, config.Sources{AllowedRoots: []string{e.base}, AllowWrites: true}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Open(context.Background(), "card")
	wantCode(t, err, domain.CodeSourceOffline)
	if got := e.get("card"); got.State != StateOffline || got.StateReason != ReasonOtherFilesystem {
		t.Fatalf("with a stand-in mounted before the open = %+v, want offline/%s", got, ReasonOtherFilesystem)
	}
	if after := e.entriesSnapshot("card"); after != before {
		t.Fatalf("entries changed:\n%s\nwant:\n%s", after, before)
	}
}

// r5b A1: the type names one filesystem is mounted under.
func TestSameFSType(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"ext4", "ext4", true},
		{"ntfs3", "fuseblk", true},
		{"fuseblk", "ntfs3", true},
		{"ntfs3", "ntfs", true},
		{"ntfs3", "fuseblk.ntfs-3g", true},
		{"exfat", "fuseblk", true},
		{"vfat", "msdos", true},
		{"nfs", "nfs4", true},
		{"cifs", "smb3", true},
		{"ntfs3", "ext4", false},
		{"fuseblk", "ext4", false},
		{"vfat", "exfat", false},
		{"exfat", "ntfs3", false},
		{"nfs4", "overlay", false},
		{"cifs", "nfs", false},
	} {
		if got := sameFSType(c.a, c.b); got != c.want {
			t.Errorf("sameFSType(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
