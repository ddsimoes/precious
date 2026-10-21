package sources

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
)

// ntfsCaps are NTFS's capabilities: stable identity, like ext4's.
var ntfsCaps = fsaccess.Capabilities{Known: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: 100 * time.Nanosecond, NoReplaceRename: true}

// pathFS gives dev's mount the filesystem type fsType at point, and caps.
func (e *env) pathFS(dev uint64, point, fsType string, caps fsaccess.Capabilities) {
	e.fs.SetFSInfo(dev, fsaccess.FSInfo{Mount: &fsaccess.MountInfo{MountPoint: point, Root: "/", FSType: fsType}})
	e.fs.SetCapabilities(dev, caps)
}

// pathSource puts a disk of type fsType and capabilities caps at base/name,
// as a weak path volume, registers source id on it with a few seeded
// entries, records the root folder's inode as a scan does (unless noIno),
// and refreshes. It returns the disk's device and mount point.
func (e *env) pathSource(id, name, fsType string, caps fsaccess.Capabilities, noIno bool) (uint64, string) {
	e.t.Helper()
	point := filepath.Join(e.base, name)
	root := e.fs.Root(point)
	dev := root.Info().Dev
	e.pathFS(dev, point, fsType, caps)
	e.insertSource(id, fsaccess.Volume{Kind: fsaccess.VolumePath, ID: point, FSType: fsType, DeviceKey: "mount:" + point}, "")
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`UPDATE sources SET capabilities = ? WHERE id = ?`, string(capsJSON), id); err != nil {
		e.t.Fatal(err)
	}
	indextest.Seed(e.t, e.st, indextest.Tree{Source: domain.SourceID(id), Nodes: []indextest.Node{
		{Path: "Fotos/a.jpg", Size: 1000, FileKind: domain.FileKindImage},
		{Path: "notes.txt", Size: 30},
	}})
	if !noIno {
		if _, err := e.st.Writer().Exec(`UPDATE entries SET ino = ? WHERE source_id = ? AND path = X''`,
			int64(root.Info().Ino), id); err != nil {
			e.t.Fatal(err)
		}
	}
	e.refresh()
	if got := e.get(domain.SourceID(id)); got.State != StateOnline || got.MountPoint != point {
		e.t.Fatalf("%s on its own disk = %+v, want online at %s", id, got, point)
	}
	return dev, point
}

// standIn moves the source's disk dev away from point and puts a new folder
// of type fsType and capabilities caps there, as a container's bind of an
// empty folder does while the disk is not mounted. It returns the stand-in's
// device.
func (e *env) standIn(dev uint64, point, fsType string, caps fsaccess.Capabilities) uint64 {
	e.t.Helper()
	e.fs.Mount(dev, filepath.Join(e.base, "away", filepath.Base(point)))
	other := e.fs.Root(point).Info().Dev
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
// its disk back, also when that disk comes back through another driver.
func TestPathSourceStandInFolder(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("hd", "hd-elements", "ntfs3", ntfsCaps, false)
	before := e.entriesSnapshot("hd")

	other := e.standIn(dev, point, "ext4", ext4Caps)
	e.wantOtherFilesystem("hd", ntfsCaps, before)

	e.restore(dev, other, point)
	e.refresh()
	if got := e.get("hd"); got.State != StateOnline || got.StateReason != "" || got.MountPoint != point {
		t.Fatalf("with its disk back = %+v, want online at %s", got, point)
	}
	op, err := e.svc.Open(context.Background(), "hd")
	if err != nil {
		t.Fatal(err)
	}
	op.Root.Close()

	e.pathFS(dev, point, "fuseblk", ntfsCaps)
	e.refresh()
	if got := e.get("hd"); got.State != StateOnline {
		t.Fatalf("the same disk through ntfs-3g = %+v, want online", got)
	}
	if after := e.entriesSnapshot("hd"); after != before {
		t.Fatalf("entries changed:\n%s\nwant:\n%s", after, before)
	}
}

// r5b D1 (spec source-registry, "A folder of a filesystem of the same type
// at a path source's mount point"): a folder of another filesystem of the
// source's own type, bound there, has another inode and is not the source.
func TestPathSourceSameTypeOtherRoot(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("tmp", "tmp", "tmpfs", ext4Caps, false)
	before := e.entriesSnapshot("tmp")
	e.standIn(dev, point, "tmpfs", ext4Caps)
	e.wantOtherFilesystem("tmp", ext4Caps, before)
}

// r5b D1 (spec source-registry, "Filesystems without stable inodes compare
// by type"): a FAT source mounted again, its inodes renumbered, is still the
// source; a folder of another type at its mount point is not.
func TestPathSourceWithoutStableInodes(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("card", "card", "vfat", fatCaps, false)
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
	e.standIn(dev, point, "ext4", ext4Caps)
	e.wantOtherFilesystem("card", fatCaps, before)
}

// r5b D1, D3: before the first scan records the root folder's inode, the
// type decides, also for a filesystem with stable inodes.
func TestPathSourceWithoutRecordedRootInode(t *testing.T) {
	e := newEnv(t)
	dev, point := e.pathSource("hd", "hd", "ntfs3", ntfsCaps, true)
	if got := e.get("hd").RootIno; got != 0 {
		t.Fatalf("RootIno = %d, want 0 without a recorded inode", got)
	}
	before := e.entriesSnapshot("hd")
	e.standIn(dev, point, "ext4", ext4Caps)
	e.wantOtherFilesystem("hd", ntfsCaps, before)
}
