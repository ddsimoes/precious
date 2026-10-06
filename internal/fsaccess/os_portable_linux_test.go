package fsaccess_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
)

// portableTree builds, under a fresh temp directory, a source with a subtree,
// files, a FIFO, a symlink to a directory outside the source, and an internal
// symlink, and returns the source and the outside directory.
func portableTree(t *testing.T) (src, outside string) {
	t.Helper()
	parent := t.TempDir()
	src, outside = filepath.Join(parent, "src"), filepath.Join(parent, "outside")
	mkdir(t, src)
	mkdir(t, outside)
	writeFile(t, filepath.Join(outside, "secret"))
	mkdir(t, filepath.Join(src, "sub"))
	for i := range 5 {
		writeFile(t, filepath.Join(src, "sub", fmt.Sprintf("f%d", i)))
	}
	writeFile(t, filepath.Join(src, "file"))
	if err := unix.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("./sub", filepath.Join(src, "alias")); err != nil {
		t.Fatal(err)
	}
	return src, outside
}

func names(entries []fsaccess.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = string(e.Name)
	}
	slices.Sort(out)
	return out
}

// TestPortableListsInBatches: the portable backend lists a directory in
// batches of at most the requested size, with kinds from the listing, then
// io.EOF, and keeps returning io.EOF.
func TestPortableListsInBatches(t *testing.T) {
	src, _ := portableTree(t)
	d := openRoot(t, fsaccess.NewPortable(), src)
	var all []fsaccess.DirEntry
	for {
		batch, err := d.ReadBatch(2)
		if err == io.EOF {
			if len(batch) != 0 {
				t.Errorf("ReadBatch returned %d entries with io.EOF", len(batch))
			}
			break
		}
		if err != nil {
			t.Fatalf("ReadBatch: %v", err)
		}
		if len(batch) == 0 || len(batch) > 2 {
			t.Fatalf("ReadBatch(2) returned %d entries", len(batch))
		}
		all = append(all, batch...)
	}
	if _, err := d.ReadBatch(2); err != io.EOF {
		t.Errorf("ReadBatch after the end = %v, want io.EOF", err)
	}
	if got, want := names(all), []string{"alias", "escape", "file", "pipe", "sub"}; !slices.Equal(got, want) {
		t.Fatalf("listed %q, want %q", got, want)
	}
	wantKinds := map[string]domain.EntryKind{
		"alias": domain.EntrySymlink, "escape": domain.EntrySymlink, "file": domain.EntryFile,
		"pipe": domain.EntryFIFO, "sub": domain.EntryDirectory,
	}
	for _, e := range all {
		if e.Kind != wantKinds[string(e.Name)] {
			t.Errorf("listing kind of %s = %s, want %s", e.Name, e.Kind, wantKinds[string(e.Name)])
		}
	}
	_, err := d.ReadBatch(0)
	wantErr(t, err, "", nil)
}

// TestPortableLstatKinds: Lstat reports every kind without following links or
// opening the FIFO, with identity from the Linux stat result, and no entry of
// the source is a mount boundary.
func TestPortableLstatKinds(t *testing.T) {
	src, _ := portableTree(t)
	d := openRoot(t, fsaccess.NewPortable(), src)
	self := d.Self()
	if self.Kind != domain.EntryDirectory || self.Ino == 0 || string(self.Name) != "src" {
		t.Fatalf("Self() = %+v, want the src directory with its identity", self)
	}
	within(t, 10*time.Second, func() {
		for name, kind := range map[string]domain.EntryKind{
			"alias": domain.EntrySymlink, "escape": domain.EntrySymlink, "file": domain.EntryFile,
			"pipe": domain.EntryFIFO, "sub": domain.EntryDirectory,
		} {
			info := mustLstat(t, d, name)
			if info.Kind != kind || info.MountBoundary || info.Dev != self.Dev || info.Ino == 0 || info.Ctime.IsZero() {
				t.Errorf("Lstat(%s) = %+v, want a %s on the source's device with identity", name, info, kind)
			}
		}
	})
	_, err := d.Lstat([]byte("missing"))
	wantErr(t, err, domain.OutcomeAbsent, nil)
	for _, bad := range []string{"../outside", "sub/f0", ".", "..", "", "a\x00b"} {
		_, err := d.Lstat([]byte(bad))
		checkInvalid(t, err, "Lstat", bad)
	}
}

// TestPortableSymlinkOutsideNotFollowed: a symlink to a directory outside the
// source is recorded with its link text, and neither OpenDir nor OpenFile,
// even with stale observations, reaches anything through it.
func TestPortableSymlinkOutsideNotFollowed(t *testing.T) {
	src, outside := portableTree(t)
	rec := instrument.Wrap(fsaccess.NewPortable())
	d := openRoot(t, rec, src)

	info := mustLstat(t, d, "escape")
	if target, err := d.Readlink([]byte("escape")); err != nil || string(target) != outside {
		t.Fatalf("Readlink(escape) = %q, %v; want %s", target, err, outside)
	}
	_, err := d.OpenDir([]byte("escape"), info)
	wantErr(t, err, "", fsaccess.ErrNotDirectory)
	_, err = d.OpenFile([]byte("escape"), info)
	wantErr(t, err, "", fsaccess.ErrNotRegular)

	outsideInfo, err := os.Lstat(outside)
	if err != nil {
		t.Fatal(err)
	}
	stale := fsaccess.EntryInfo{Kind: domain.EntryDirectory, Dev: d.Self().Dev, Ino: info.Ino}
	_, err = d.OpenDir([]byte("escape"), stale)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, nil)
	staleFile := fsaccess.EntryInfo{Kind: domain.EntryFile, Dev: d.Self().Dev, Ino: info.Ino, ModTime: outsideInfo.ModTime()}
	_, err = d.OpenFile([]byte("escape"), staleFile)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)

	for _, c := range rec.Calls() {
		if c.Root != filepath.Clean(src) || c.Depth() > 1 {
			t.Errorf("call %s %q left the root or went below escape", c.Op, c.FullPath())
		}
		if (c.Op == instrument.OpOpenDir || c.Op == instrument.OpOpenFile) && c.Err == nil {
			t.Errorf("%s %q succeeded", c.Op, c.FullPath())
		}
	}
}

// TestPortableOpenDirRefusesSymlink: os.Root would follow the internal link
// alias to sub, whose identity matches; OpenDir refuses because the name is a
// link, and opens sub by its own name.
func TestPortableOpenDirRefusesSymlink(t *testing.T) {
	src, _ := portableTree(t)
	d := openRoot(t, fsaccess.NewPortable(), src)

	alias := mustLstat(t, d, "alias")
	_, err := d.OpenDir([]byte("alias"), alias)
	wantErr(t, err, "", fsaccess.ErrNotDirectory)
	sub := mustLstat(t, d, "sub")
	_, err = d.OpenDir([]byte("alias"), sub)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)

	pipe := mustLstat(t, d, "pipe")
	within(t, 10*time.Second, func() {
		stale := fsaccess.EntryInfo{Kind: domain.EntryDirectory, Dev: pipe.Dev, Ino: pipe.Ino}
		_, err := d.OpenDir([]byte("pipe"), stale)
		wantErr(t, err, domain.OutcomeChangedDuringObservation, nil)
		staleFile := pipe
		staleFile.Kind = domain.EntryFile
		_, err = d.OpenFile([]byte("pipe"), staleFile)
		wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
	})

	child, err := d.OpenDir([]byte("sub"), sub)
	if err != nil {
		t.Fatalf("OpenDir(sub): %v", err)
	}
	defer child.Close()
	if s := child.Self(); s.Dev != sub.Dev || s.Ino != sub.Ino {
		t.Errorf("Self() = %+v, want the identity of sub", s)
	}
	if got := names(listAll(t, child, 3)); !slices.Equal(got, []string{"f0", "f1", "f2", "f3", "f4"}) {
		t.Errorf("sub lists %q", got)
	}
}

// TestPortableOpenFile: a regular file opens when it is the observed one and
// reads its content; a changed file is refused.
func TestPortableOpenFile(t *testing.T) {
	src, _ := portableTree(t)
	if err := os.WriteFile(filepath.Join(src, "data"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := openRoot(t, fsaccess.NewPortable(), src)
	info := mustLstat(t, d, "data")
	f, err := d.OpenFile([]byte("data"), info)
	if err != nil {
		t.Fatalf("OpenFile(data): %v", err)
	}
	buf := make([]byte, 16)
	if n, err := f.ReadAt(buf, 6); err != io.EOF || !bytes.Equal(buf[:n], []byte("world")) {
		t.Errorf("ReadAt(16, 6) = %q, %v; want world, io.EOF", buf[:n], err)
	}
	if st, err := f.Stat(); err != nil || st.Ino != info.Ino || st.Size != info.Size {
		t.Errorf("Stat() = %+v, %v; want the observed file", st, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt(buf, 0); !errors.Is(err, os.ErrClosed) {
		t.Errorf("ReadAt after Close = %v, want os.ErrClosed", err)
	}

	grown := info
	grown.Size++
	_, err = d.OpenFile([]byte("data"), grown)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
}

// TestPortableVolumesAndCapabilities: Mounts lists one weak path volume per
// root opened, and Capabilities is always the unknown set.
func TestPortableVolumesAndCapabilities(t *testing.T) {
	src, _ := portableTree(t)
	fsys := fsaccess.NewPortable()
	if mounts, err := fsys.Mounts(); err != nil || len(mounts) != 0 {
		t.Fatalf("Mounts() before any root = %+v, %v; want none", mounts, err)
	}
	openRoot(t, fsys, src+"/")
	openRoot(t, fsys, filepath.Join(src, "sub"))
	mounts, err := fsys.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	var points []string
	for _, m := range mounts {
		points = append(points, m.Point)
		want := fsaccess.Volume{Kind: fsaccess.VolumePath, ID: m.Point, DeviceKey: "mount:" + m.Point}
		if m.Volume != want || string(m.Root) != "/" || m.ReadOnly {
			t.Errorf("mount %+v, want a writable path volume %+v", m, want)
		}
	}
	if want := []string{src, filepath.Join(src, "sub")}; !slices.Equal(points, want) {
		t.Errorf("mount points %q, want %q", points, want)
	}

	caps, err := fsys.Capabilities(src)
	if err != nil {
		t.Fatal(err)
	}
	if caps != fsaccess.UnknownCapabilities(false) || caps.Known || caps.CaseSensitive || caps.StableIdentity || caps.TimeResolution != 2*time.Second {
		t.Errorf("Capabilities(%s) = %+v, want the unknown set", src, caps)
	}
	_, err = fsys.Capabilities("relative")
	wantErr(t, err, "", nil)
}
