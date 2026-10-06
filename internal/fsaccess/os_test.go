//go:build linux

package fsaccess_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
)

func openRoot(t *testing.T, fsys fsaccess.FS, path string) fsaccess.Dir {
	t.Helper()
	d, err := fsys.OpenRoot(path)
	if err != nil {
		t.Fatalf("OpenRoot(%s): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func listAll(t *testing.T, d fsaccess.Dir, batch int) []fsaccess.DirEntry {
	t.Helper()
	var all []fsaccess.DirEntry
	for {
		entries, err := d.ReadBatch(batch)
		if err == io.EOF {
			if len(entries) != 0 {
				t.Errorf("ReadBatch returned %d entries with io.EOF", len(entries))
			}
			return all
		}
		if err != nil {
			t.Errorf("ReadBatch: %v", err)
			return all
		}
		all = append(all, entries...)
	}
}

func find(entries []fsaccess.DirEntry, name string) (fsaccess.DirEntry, bool) {
	for _, e := range entries {
		if string(e.Name) == name {
			return e, true
		}
	}
	return fsaccess.DirEntry{}, false
}

func mustLstat(t *testing.T, d fsaccess.Dir, name string) fsaccess.EntryInfo {
	t.Helper()
	info, err := d.Lstat([]byte(name))
	if err != nil {
		t.Fatalf("Lstat(%q): %v", name, err)
	}
	return info
}

// wantErr checks that err is an *fsaccess.Error with the given outcome and,
// when target is non-nil, that it wraps target.
func wantErr(t *testing.T, err error, outcome domain.AccessOutcome, target error) {
	t.Helper()
	var e *fsaccess.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *fsaccess.Error", err)
	}
	if e.Outcome != outcome {
		t.Errorf("outcome = %q, want %q (err %v)", e.Outcome, outcome, err)
	}
	if target != nil && !errors.Is(err, target) {
		t.Errorf("err = %v, want it to wrap %v", err, target)
	}
}

// within runs fn and fails the test if it does not return in time: listing or
// stat'ing a FIFO must never block.
func within(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("filesystem call blocked for more than %v", limit)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestA11SymlinkEscape: `escape -> /etc` is recorded as a symlink with its link
// text, and nothing under /etc is opened, listed, or stat'ed.
func TestA11SymlinkEscape(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink("/etc", filepath.Join(src, "escape")); err != nil {
		t.Fatal(err)
	}
	rec := instrument.Wrap(fsaccess.NewOS())
	d := openRoot(t, rec, src)

	if e, ok := find(listAll(t, d, 16), "escape"); !ok || e.Kind != domain.EntrySymlink {
		t.Fatalf("listing entry for escape = %+v, %v; want a symlink", e, ok)
	}
	info := mustLstat(t, d, "escape")
	if info.Kind != domain.EntrySymlink || info.MountBoundary {
		t.Fatalf("Lstat(escape) = %+v, want a plain symlink", info)
	}
	target, err := d.Readlink([]byte("escape"))
	if err != nil || string(target) != "/etc" {
		t.Fatalf("Readlink(escape) = %q, %v; want /etc", target, err)
	}
	_, err = d.OpenDir([]byte("escape"), info)
	wantErr(t, err, "", fsaccess.ErrNotDirectory)

	// A caller with a stale observation that escape is a directory still
	// cannot reach /etc through it.
	stale := fsaccess.EntryInfo{Kind: domain.EntryDirectory, Dev: d.Self().Dev, Ino: info.Ino}
	_, err = d.OpenDir([]byte("escape"), stale)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, nil)

	for _, c := range rec.Calls() {
		if c.Root != filepath.Clean(src) || c.Depth() > 1 {
			t.Errorf("call %s %q left the root or went below escape", c.Op, c.FullPath())
		}
		if c.Op == instrument.OpOpenDir && c.Err == nil {
			t.Errorf("OpenDir %q succeeded", c.FullPath())
		}
	}
}

// Internal symlink not followed: `alias -> ./Projects` is a symlink with link
// text ./Projects, and nothing is reached through it.
func TestInternalSymlinkNotFollowed(t *testing.T) {
	src := t.TempDir()
	mkdir(t, filepath.Join(src, "Projects"))
	writeFile(t, filepath.Join(src, "Projects", "inner.txt"))
	if err := os.Symlink("./Projects", filepath.Join(src, "alias")); err != nil {
		t.Fatal(err)
	}
	rec := instrument.Wrap(fsaccess.NewOS())
	d := openRoot(t, rec, src)

	alias := mustLstat(t, d, "alias")
	if alias.Kind != domain.EntrySymlink {
		t.Fatalf("Lstat(alias).Kind = %s, want symlink", alias.Kind)
	}
	if target, err := d.Readlink([]byte("alias")); err != nil || string(target) != "./Projects" {
		t.Fatalf("Readlink(alias) = %q, %v; want ./Projects", target, err)
	}
	_, err := d.OpenDir([]byte("alias"), alias)
	wantErr(t, err, "", fsaccess.ErrNotDirectory)

	// os.Root itself would follow alias to Projects, and Projects' identity
	// would match. The backend still refuses: the name is a link.
	projects := mustLstat(t, d, "Projects")
	_, err = d.OpenDir([]byte("alias"), projects)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)

	// Opening the directory by its own name works and lists its entry.
	child, err := d.OpenDir([]byte("Projects"), projects)
	if err != nil {
		t.Fatalf("OpenDir(Projects): %v", err)
	}
	defer child.Close()
	if s := child.Self(); s.Dev != projects.Dev || s.Ino != projects.Ino {
		t.Errorf("Self() = %+v, want the identity of Projects", s)
	}
	if entries := listAll(t, child, 8); len(entries) != 1 || string(entries[0].Name) != "inner.txt" {
		t.Errorf("Projects lists %+v, want inner.txt", entries)
	}

	for _, c := range rec.Calls() {
		if len(c.Path) > 0 && string(c.Path[0]) == "alias" && (c.Depth() > 1 || (c.Op == instrument.OpOpenDir && c.Err == nil)) {
			t.Errorf("call %s %q reached through alias", c.Op, c.FullPath())
		}
	}
}

// TestA11TraversalName: names that are not a single component are rejected
// before the filesystem is touched. Every rejected name would resolve to an
// existing object if it reached os.Root.
func TestA11TraversalName(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "src")
	mkdir(t, src)
	writeFile(t, filepath.Join(parent, "x"))
	mkdir(t, filepath.Join(src, "a"))
	mkdir(t, filepath.Join(src, "a", "b"))

	d := openRoot(t, fsaccess.NewOS(), src)
	dirExpect := fsaccess.EntryInfo{Kind: domain.EntryDirectory, Dev: d.Self().Dev}
	for _, bad := range []string{"../x", "a/b", ".", "..", "", "a\x00b", "/etc", "a/"} {
		_, err := d.Lstat([]byte(bad))
		checkInvalid(t, err, "Lstat", bad)
		_, err = d.OpenDir([]byte(bad), dirExpect)
		checkInvalid(t, err, "OpenDir", bad)
		_, err = d.Readlink([]byte(bad))
		checkInvalid(t, err, "Readlink", bad)
	}
}

func checkInvalid(t *testing.T, err error, op, name string) {
	t.Helper()
	var e *fsaccess.Error
	if !errors.As(err, &e) || !errors.Is(err, fsaccess.ErrInvalidName) || e.Op != op || string(e.Name) != name || e.Outcome != "" {
		t.Errorf("%s(%q) = %v, want *Error{Op: %s, Err: ErrInvalidName} without outcome", op, name, err, op)
	}
}

// TestA11FIFOInSource: a FIFO named pipe is listed and stat'ed without
// blocking, is reported as a FIFO, and is never opened.
func TestA11FIFOInSource(t *testing.T) {
	src := t.TempDir()
	pipe := filepath.Join(src, "pipe")
	if err := unix.Mkfifo(pipe, 0o644); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(src, "dir"))
	writeFile(t, filepath.Join(src, "file"))
	rec := instrument.Wrap(fsaccess.NewOS())
	d := openRoot(t, rec, src)

	var info fsaccess.EntryInfo
	within(t, 10*time.Second, func() {
		entries := listAll(t, d, 2)
		if e, ok := find(entries, "pipe"); !ok || e.Kind != domain.EntryFIFO {
			t.Errorf("listing entry for pipe = %+v, %v; want a FIFO", e, ok)
		}
		var err error
		if info, err = d.Lstat([]byte("pipe")); err != nil || info.Kind != domain.EntryFIFO {
			t.Errorf("Lstat(pipe) = %+v, %v; want a FIFO", info, err)
		}
	})
	for _, c := range rec.Calls() {
		if c.Op == instrument.OpOpenDir || c.Depth() > 1 {
			t.Errorf("observing the listing issued %s %q", c.Op, c.FullPath())
		}
	}

	// OpenDir refuses it up front, and even a caller that wrongly believes it
	// is a directory gets an error at once instead of opening it.
	within(t, 10*time.Second, func() {
		_, err := d.OpenDir([]byte("pipe"), info)
		if !errors.Is(err, fsaccess.ErrNotDirectory) {
			t.Errorf("OpenDir(pipe) = %v, want ErrNotDirectory", err)
		}
		stale := fsaccess.EntryInfo{Kind: domain.EntryDirectory, Dev: info.Dev, Ino: info.Ino}
		_, err = d.OpenDir([]byte("pipe"), stale)
		if outcome, _ := fsaccess.OutcomeOf(err); outcome != domain.OutcomeChangedDuringObservation {
			t.Errorf("OpenDir(pipe, stale) = %v, want changed_during_observation", err)
		}
	})

	// Nobody holds the FIFO open for reading: a non-blocking writer open fails
	// with ENXIO.
	fd, err := unix.Open(pipe, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		unix.Close(fd)
		t.Fatal("pipe has a reader; it was opened")
	}
	if !errors.Is(err, unix.ENXIO) {
		t.Fatalf("open pipe for writing: %v, want ENXIO", err)
	}
}

// TestA11DirectorySwappedForSymlink: a directory replaced by `-> /` after it
// was observed but before it is opened yields changed_during_observation, and
// nothing is read through the link. The other cases replace it with objects
// that os.Root would open without complaint.
func TestA11DirectorySwappedForSymlink(t *testing.T) {
	// Each replacement runs inside the OpenDir hook, on a non-test goroutine.
	cases := map[string]func(victim string) error{
		"symlink to /":                   func(victim string) error { return os.Symlink("/", victim) },
		"symlink to the moved directory": func(victim string) error { return os.Symlink("./victim.moved", victim) },
		"another directory":              func(victim string) error { return os.Mkdir(victim, 0o755) },
		"FIFO":                           func(victim string) error { return unix.Mkfifo(victim, 0o644) },
	}
	for name, replace := range cases {
		t.Run(name, func(t *testing.T) {
			src := t.TempDir()
			victim := filepath.Join(src, "victim")
			mkdir(t, victim)
			writeFile(t, filepath.Join(victim, "secret.txt"))
			rec := instrument.Wrap(fsaccess.NewOS())
			d := openRoot(t, rec, src)
			info := mustLstat(t, d, "victim")

			rec.SetBeforeOpenDir(func(c instrument.Call) {
				if err := os.Rename(victim, victim+".moved"); err != nil {
					t.Error(err)
				}
				if err := replace(victim); err != nil {
					t.Error(err)
				}
			})
			within(t, 10*time.Second, func() {
				child, err := d.OpenDir([]byte("victim"), info)
				if child != nil {
					child.Close()
					t.Error("OpenDir returned a directory")
				}
				if outcome, _ := fsaccess.OutcomeOf(err); outcome != domain.OutcomeChangedDuringObservation {
					t.Errorf("OpenDir(victim) = %v, want changed_during_observation", err)
				}
			})
			for _, c := range rec.Calls() {
				if c.Depth() > 1 || (c.Op != instrument.OpLstat && c.Op != instrument.OpOpenRoot && c.Op != instrument.OpOpenDir) {
					t.Errorf("unexpected call %s %q", c.Op, c.FullPath())
				}
			}
		})
	}
}

// TestA5UnreadableDirectory: a mode 0000 child is reported unreadable when
// opened, never as empty.
func TestA5UnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not apply to root")
	}
	src := t.TempDir()
	locked := filepath.Join(src, "locked")
	mkdir(t, locked)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	d := openRoot(t, fsaccess.NewOS(), src)
	info := mustLstat(t, d, "locked")
	child, err := d.OpenDir([]byte("locked"), info)
	if child != nil {
		child.Close()
	}
	wantErr(t, err, domain.OutcomeUnreadable, syscall.EACCES)
}

func TestOpenRootFailures(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file)
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	fsys := fsaccess.NewOS()
	within(t, 10*time.Second, func() {
		for _, p := range []string{filepath.Join(dir, "missing"), file, fifo} {
			_, err := fsys.OpenRoot(p)
			if outcome, _ := fsaccess.OutcomeOf(err); outcome != domain.OutcomeUnavailable {
				t.Errorf("OpenRoot(%s) = %v, want unavailable", p, err)
			}
		}
	})
	if _, err := fsys.OpenRoot("relative/path"); err == nil {
		t.Error("OpenRoot accepted a relative path")
	}
}

// TestA17NonUTF8Name: the name bytes 66 E9 2E 74 78 74 round-trip exactly.
func TestA17NonUTF8Name(t *testing.T) {
	src := t.TempDir()
	name := []byte{0x66, 0xE9, 0x2E, 0x74, 0x78, 0x74}
	writeFile(t, filepath.Join(src, string(name)))
	d := openRoot(t, fsaccess.NewOS(), src)

	entries := listAll(t, d, 8)
	if len(entries) != 1 || !bytes.Equal(entries[0].Name, name) || entries[0].Kind != domain.EntryFile {
		t.Fatalf("listing = %+v, want one file named % X", entries, name)
	}
	info, err := d.Lstat(entries[0].Name)
	if err != nil || !bytes.Equal(info.Name, name) || info.Kind != domain.EntryFile {
		t.Fatalf("Lstat(% X) = %+v, %v", name, info, err)
	}
}

// TestA17NormalizationDistinctNames: NFC and NFD forms of é are two distinct
// entries with their exact bytes.
func TestA17NormalizationDistinctNames(t *testing.T) {
	src := t.TempDir()
	nfc, nfd := []byte("\u00e9"), []byte("e\u0301")
	for i, name := range [][]byte{nfc, nfd} {
		f, err := os.OpenFile(filepath.Join(src, string(name)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if i == 1 && errors.Is(err, os.ErrExist) {
			t.Skip("the temporary directory's filesystem normalizes names")
		}
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	d := openRoot(t, fsaccess.NewOS(), src)
	entries := listAll(t, d, 8)
	if len(entries) != 2 {
		t.Fatalf("listing = %+v, want two entries", entries)
	}
	inos := map[uint64]bool{}
	for _, want := range [][]byte{nfc, nfd} {
		if _, ok := find(entries, string(want)); !ok {
			t.Errorf("no entry named % X in %+v", want, entries)
		}
		info, err := d.Lstat(want)
		if err != nil || !bytes.Equal(info.Name, want) {
			t.Fatalf("Lstat(% X) = %+v, %v", want, info, err)
		}
		inos[info.Ino] = true
	}
	if len(inos) != 2 {
		t.Errorf("both names resolve to the same inode")
	}
}

func TestReadBatchBoundedOnRealDirectory(t *testing.T) {
	src := t.TempDir()
	const total, batch = 1000, 64
	for i := range total {
		writeFile(t, filepath.Join(src, fmt.Sprintf("n%04d", i)))
	}
	rec := instrument.Wrap(fsaccess.NewOS())
	d := openRoot(t, rec, src)
	if _, err := d.ReadBatch(0); err == nil {
		t.Error("ReadBatch(0) succeeded")
	}
	entries := listAll(t, d, batch)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[string(e.Name)] = true
	}
	if len(entries) != total || len(seen) != total {
		t.Fatalf("listed %d entries (%d distinct), want %d", len(entries), len(seen), total)
	}
	for _, c := range rec.Calls() {
		if c.Op == instrument.OpReadBatch && c.Entries > batch {
			t.Errorf("ReadBatch(%d) returned %d entries", c.N, c.Entries)
		}
	}
	if _, err := d.ReadBatch(batch); err != io.EOF {
		t.Errorf("ReadBatch after the end = %v, want io.EOF", err)
	}
}

func TestFSInfoOnRealMount(t *testing.T) {
	src := t.TempDir()
	canon, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	d := openRoot(t, fsaccess.NewOS(), src)
	info, err := d.FSInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Dev != d.Self().Dev || info.Type == 0 {
		t.Errorf("FSInfo = %+v, want Dev %d and a statfs type", info, d.Self().Dev)
	}
	if info.Mount == nil {
		t.Fatal("FSInfo.Mount is nil: the temp directory's mount is not in the snapshot")
	}
	if mp := info.Mount.MountPoint; mp != "/" && canon != mp && !strings.HasPrefix(canon, mp+"/") {
		t.Errorf("mount point %q does not contain %q", mp, canon)
	}
}

// Same-device bind mounts are found through the mount-table snapshot, which
// is substituted here so the test runs without privileges.
func TestMountTableBoundaryOnRealDirectory(t *testing.T) {
	src := t.TempDir()
	canon, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"again", "with space", "plain"} {
		mkdir(t, filepath.Join(src, n))
	}
	var st unix.Stat_t
	if err := unix.Stat(src, &st); err != nil {
		t.Fatal(err)
	}
	mm := fmt.Sprintf("%d:%d", unix.Major(st.Dev), unix.Minor(st.Dev))
	escaped := strings.ReplaceAll(canon, " ", `\040`)
	table := fmt.Sprintf("22 1 %[1]s / / rw - ext4 /dev/root rw\n"+
		"30 22 %[1]s /target %[2]s/again rw shared:1 - ext4 /dev/root rw\n"+
		"31 22 %[1]s /other %[2]s/with\\040space rw - ext4 /dev/root rw\n", mm, escaped)
	fsys := fsaccess.NewOSWithMountinfo(t, table)
	d := openRoot(t, fsys, src)

	for name, want := range map[string]bool{"again": true, "with space": true, "plain": false} {
		if info := mustLstat(t, d, name); info.MountBoundary != want {
			t.Errorf("Lstat(%q).MountBoundary = %v, want %v", name, info.MountBoundary, want)
		}
	}
	again := mustLstat(t, d, "again")
	_, err = d.OpenDir([]byte("again"), again)
	wantErr(t, err, "", fsaccess.ErrMountBoundary)
	// A caller that dropped the boundary flag is still refused after the open.
	again.MountBoundary = false
	_, err = d.OpenDir([]byte("again"), again)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrMountBoundary)

	if child, err := d.OpenDir([]byte("plain"), mustLstat(t, d, "plain")); err != nil {
		t.Errorf("OpenDir(plain): %v", err)
	} else {
		child.Close()
	}
	info, err := d.FSInfo()
	if err != nil || info.Mount == nil || info.Mount.MountID != 22 {
		t.Errorf("FSInfo = %+v, %v; want mount 22", info, err)
	}
}

func TestOpenRootNeedsMountTable(t *testing.T) {
	src := t.TempDir()
	for name, fsys := range map[string]fsaccess.FS{
		"missing":   fsaccess.NewOSAt(t.TempDir(), t.TempDir(), t.TempDir()),
		"malformed": fsaccess.NewOSWithMountinfo(t, "garbage\n"),
	} {
		_, err := fsys.OpenRoot(src)
		if outcome, _ := fsaccess.OutcomeOf(err); outcome != domain.OutcomeUnavailable {
			t.Errorf("%s mount table: OpenRoot = %v, want unavailable", name, err)
		}
	}
}
