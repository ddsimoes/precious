//go:build linux

package fsaccess_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
)

// pattern returns n bytes that differ at every offset modulo 251.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func mustOpenFile(t *testing.T, d fsaccess.Dir, name string, expect fsaccess.EntryInfo) fsaccess.File {
	t.Helper()
	f, err := d.OpenFile([]byte(name), expect)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", name, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// wantNoFile checks that a failed OpenFile returned no handle.
func wantNoFile(t *testing.T, f fsaccess.File) {
	t.Helper()
	if f != nil {
		f.Close()
		t.Error("OpenFile returned a file")
	}
}

func ctimeOf(t *testing.T, path string) time.Time {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return time.Unix(st.Ctim.Unix())
}

// Lstat reports st_ctim for every kind of entry (design D6).
func TestLstatReportsCtime(t *testing.T) {
	src := t.TempDir()
	mkdir(t, filepath.Join(src, "dir"))
	writeFile(t, filepath.Join(src, "file"))
	if err := os.Symlink("file", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := openRoot(t, fsaccess.NewOS(), src)
	for _, name := range []string{"dir", "file", "link", "pipe"} {
		info := mustLstat(t, d, name)
		if want := ctimeOf(t, filepath.Join(src, name)); info.Ctime.IsZero() || !info.Ctime.Equal(want) {
			t.Errorf("Lstat(%s).Ctime = %v, want %v", name, info.Ctime, want)
		}
	}
	if self, want := d.Self(), ctimeOf(t, src); !self.Ctime.Equal(want) {
		t.Errorf("Self().Ctime = %v, want %v", self.Ctime, want)
	}
}

// Reads at offsets return the written bytes; Stat of the open file equals the
// Lstat the caller observed, change time included.
func TestOpenFileReadsAtOffsets(t *testing.T) {
	src := t.TempDir()
	data := pattern(100_000)
	if err := os.WriteFile(filepath.Join(src, "data.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	d := openRoot(t, fsaccess.NewOS(), src)
	info := mustLstat(t, d, "data.bin")
	f := mustOpenFile(t, d, "data.bin", info)

	for _, off := range []int64{0, 1, 4096, 50_000, 99_000} {
		p := make([]byte, 1000)
		n, err := f.ReadAt(p, off)
		if err != nil || n != len(p) || !bytes.Equal(p, data[off:off+1000]) {
			t.Errorf("ReadAt(1000 bytes, %d) = %d, %v; want the written bytes", off, n, err)
		}
	}
	p := make([]byte, 1000)
	if n, err := f.ReadAt(p, 99_500); n != 500 || err != io.EOF || !bytes.Equal(p[:n], data[99_500:]) {
		t.Errorf("ReadAt across the end = %d, %v; want the last 500 bytes and io.EOF", n, err)
	}
	for _, off := range []int64{100_000, 200_000} {
		if n, err := f.ReadAt(p, off); n != 0 || err != io.EOF {
			t.Errorf("ReadAt(%d) at or past the end = %d, %v; want 0, io.EOF", off, n, err)
		}
	}
	if _, err := f.ReadAt(p, -1); err == nil || outcomeOf(err) != "" {
		t.Errorf("ReadAt(-1) = %v, want a refusal without outcome", err)
	}

	got, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, info) || got.Ctime.IsZero() {
		t.Errorf("Stat() = %+v\nLstat  = %+v\nwant them equal, with a change time", got, info)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if _, err := f.ReadAt(p, 0); outcomeOf(err) != domain.OutcomeUnavailable {
		t.Errorf("ReadAt after Close = %v, want unavailable", err)
	}
	if _, err := f.Stat(); outcomeOf(err) != domain.OutcomeUnavailable {
		t.Errorf("Stat after Close = %v, want unavailable", err)
	}
}

func outcomeOf(err error) domain.AccessOutcome {
	o, _ := fsaccess.OutcomeOf(err)
	return o
}

// swapBeforeOpenFile runs replace (renaming the original away first) inside
// the OpenFile hook, after the caller observed the file, and checks that
// OpenFile fails with changed_during_observation without blocking and that
// nothing was read.
func swapBeforeOpenFile(t *testing.T, replace func(victim string) error) {
	t.Helper()
	parent := t.TempDir()
	src := filepath.Join(parent, "src")
	mkdir(t, src)
	victim := filepath.Join(src, "victim")
	if err := os.WriteFile(victim, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := instrument.Wrap(fsaccess.NewOS())
	d := openRoot(t, rec, src)
	info := mustLstat(t, d, "victim")

	rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op != instrument.OpOpenFile {
			return
		}
		if err := os.Rename(victim, victim+".moved"); err != nil {
			t.Error(err)
		}
		if err := replace(victim); err != nil {
			t.Error(err)
		}
	})
	within(t, 10*time.Second, func() {
		f, err := d.OpenFile([]byte("victim"), info)
		wantNoFile(t, f)
		wantErr(t, err, domain.OutcomeChangedDuringObservation, nil)
	})
	if n := rec.Count(instrument.OpReadAt) + rec.Count(instrument.OpFileStat); n != 0 || rec.BytesRead() != 0 {
		t.Errorf("%d file calls and %d bytes read after a refused open", n, rec.BytesRead())
	}
}

// A11 symlink swapped in before a read: a listed file replaced by a symlink to
// a file outside the source fails as changed_during_observation, and no byte
// of the target is read. The link to the moved original is the case that a
// following open would accept: same device, inode, size, and times.
func TestA11SymlinkSwappedInBeforeRead(t *testing.T) {
	t.Run("link outside the source", func(t *testing.T) {
		swapBeforeOpenFile(t, func(victim string) error {
			outside := filepath.Join(filepath.Dir(filepath.Dir(victim)), "secret")
			if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
				return err
			}
			return os.Symlink(outside, victim)
		})
	})
	t.Run("link to the moved original", func(t *testing.T) {
		swapBeforeOpenFile(t, func(victim string) error { return os.Symlink("victim.moved", victim) })
	})
}

// A11 FIFO swapped in before a read: the open returns at once with
// changed_during_observation and opens nothing.
func TestA11FIFOSwappedInBeforeRead(t *testing.T) {
	var pipe string
	swapBeforeOpenFile(t, func(victim string) error {
		pipe = victim
		return unix.Mkfifo(victim, 0o644)
	})
	// Nobody holds the FIFO open for reading: a non-blocking writer open fails
	// with ENXIO.
	fd, err := unix.Open(pipe, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		unix.Close(fd)
		t.Fatal("the FIFO has a reader")
	}
	if !errors.Is(err, unix.ENXIO) {
		t.Fatalf("open FIFO for writing: %v, want ENXIO", err)
	}
}

// Other objects swapped in at the name are refused the same way.
func TestOpenFileSwappedForOtherObjects(t *testing.T) {
	for name, replace := range map[string]func(victim string) error{
		"another file": func(victim string) error { return os.WriteFile(victim, []byte("inside"), 0o644) },
		"a directory":  func(victim string) error { return os.Mkdir(victim, 0o755) },
		"a socket": func(victim string) error {
			fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				return err
			}
			defer unix.Close(fd)
			return unix.Bind(fd, &unix.SockaddrUnix{Name: victim})
		},
	} {
		t.Run(name, func(t *testing.T) { swapBeforeOpenFile(t, replace) })
	}
}

// A file rewritten between Lstat and OpenFile fails the identity check: its
// size, or only its change time, differs from the observation (design D6).
func TestOpenFileRewrittenAfterLstat(t *testing.T) {
	cases := map[string]func(t *testing.T, path string, before os.FileInfo){
		"appended": func(t *testing.T, path string, _ os.FileInfo) {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString("more"); err != nil {
				t.Fatal(err)
			}
		},
		"same size, modification time restored": func(t *testing.T, path string, before os.FileInfo) {
			if err := os.WriteFile(path, []byte("OTHER!"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, time.Time{}, before.ModTime()); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, rewrite := range cases {
		t.Run(name, func(t *testing.T) {
			src := t.TempDir()
			path := filepath.Join(src, "f")
			if err := os.WriteFile(path, []byte("inside"), 0o644); err != nil {
				t.Fatal(err)
			}
			d := openRoot(t, fsaccess.NewOS(), src)
			info := mustLstat(t, d, "f")
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			rewrite(t, path, before)
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if after.Size() == info.Size && after.ModTime().Equal(info.ModTime) && ctimeOf(t, path).Equal(info.Ctime) {
				t.Skip("the kernel's coarse timestamp clock gave the rewrite the observed change time")
			}
			f, err := d.OpenFile([]byte("f"), info)
			wantNoFile(t, f)
			wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
		})
	}
}

// Each identity field of the expectation is compared.
func TestOpenFileComparesIdentity(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "f"))
	d := openRoot(t, fsaccess.NewOS(), src)
	info := mustLstat(t, d, "f")
	for name, perturb := range map[string]func(e *fsaccess.EntryInfo){
		"device":            func(e *fsaccess.EntryInfo) { e.Dev++ },
		"inode":             func(e *fsaccess.EntryInfo) { e.Ino++ },
		"size":              func(e *fsaccess.EntryInfo) { e.Size++ },
		"modification time": func(e *fsaccess.EntryInfo) { e.ModTime = e.ModTime.Add(time.Nanosecond) },
		"change time":       func(e *fsaccess.EntryInfo) { e.Ctime = e.Ctime.Add(-time.Nanosecond) },
		"unknown ctime":     func(e *fsaccess.EntryInfo) { e.Ctime = time.Time{} },
	} {
		expect := info
		perturb(&expect)
		f, err := d.OpenFile([]byte("f"), expect)
		wantNoFile(t, f)
		if outcomeOf(err) != domain.OutcomeChangedDuringObservation || !errors.Is(err, fsaccess.ErrIdentityChanged) {
			t.Errorf("OpenFile with another %s = %v, want changed_during_observation", name, err)
		}
	}
	f := mustOpenFile(t, d, "f", info)
	if n, err := f.ReadAt(make([]byte, 1), 0); n != 1 || err != nil {
		t.Errorf("ReadAt with the observed identity = %d, %v", n, err)
	}
}

// Invalid names, kinds other than a regular file, and observed mount
// boundaries are refused before any system call: the refusals carry no
// outcome, and a closed directory still answers with them instead of failing.
func TestOpenFileRefusedBeforeAnySystemCall(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "src")
	mkdir(t, src)
	writeFile(t, filepath.Join(parent, "x"))
	mkdir(t, filepath.Join(src, "a"))
	writeFile(t, filepath.Join(src, "a", "b"))
	writeFile(t, filepath.Join(src, "file"))
	if err := os.Symlink("file", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	rec := instrument.Wrap(fsaccess.NewOS())
	d := openRoot(t, rec, src)
	dirInfo, linkInfo, fileInfo := mustLstat(t, d, "a"), mustLstat(t, d, "link"), mustLstat(t, d, "file")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"../x", "a/b", ".", "..", "", "a\x00b", "/etc", "a/"} {
		_, err := d.OpenFile([]byte(bad), fileInfo)
		checkInvalid(t, err, "OpenFile", bad)
	}
	for name, expect := range map[string]fsaccess.EntryInfo{"a": dirInfo, "link": linkInfo} {
		f, err := d.OpenFile([]byte(name), expect)
		wantNoFile(t, f)
		wantErr(t, err, "", fsaccess.ErrNotRegular)
	}
	for _, kind := range []domain.EntryKind{domain.EntryFIFO, domain.EntrySocket, domain.EntryCharDevice, domain.EntryBlockDevice, domain.EntryUnknown} {
		expect := fileInfo
		expect.Kind = kind
		_, err := d.OpenFile([]byte("file"), expect)
		wantErr(t, err, "", fsaccess.ErrNotRegular)
	}
	boundary := fileInfo
	boundary.MountBoundary = true
	_, err := d.OpenFile([]byte("file"), boundary)
	wantErr(t, err, "", fsaccess.ErrMountBoundary)

	// The closed directory fails a well-formed request as unavailable.
	_, err = d.OpenFile([]byte("file"), fileInfo)
	wantErr(t, err, domain.OutcomeUnavailable, os.ErrClosed)
}

// The EPERM retry: a root-owned, world-readable file cannot be opened with
// O_NOATIME by another user, so OpenFile retries without it and reads the
// same bytes os.ReadFile does. Root may always use O_NOATIME.
func TestOpenFileRetriesWithoutNoatime(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: O_NOATIME never fails with EPERM")
	}
	d := openRoot(t, fsaccess.NewOS(), "/etc")
	for _, name := range []string{"passwd", "group", "hostname"} {
		path := filepath.Join("/etc", name)
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0o004 == 0 {
			continue
		}
		info := mustLstat(t, d, name)
		if info.MountBoundary {
			continue // a bind-mounted file, as container runtimes provide
		}
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOATIME|unix.O_CLOEXEC, 0)
		if err == nil {
			unix.Close(fd)
			t.Skipf("O_NOATIME open of %s succeeded (CAP_FOWNER?): no EPERM to retry", path)
		}
		if !errors.Is(err, unix.EPERM) {
			t.Fatalf("O_NOATIME open of %s: %v, want EPERM", path, err)
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f := mustOpenFile(t, d, name, info)
		got := make([]byte, len(want)+1)
		n, err := f.ReadAt(got, 0)
		if err != io.EOF || !bytes.Equal(got[:n], want) {
			t.Errorf("ReadAt(%s) = %d bytes, %v; want the %d bytes os.ReadFile read, then io.EOF", path, n, err, len(want))
		}
		return
	}
	t.Skip("no root-owned, world-readable regular file in /etc")
}

// A regular file listed as a mount point (a bind-mounted file) is a mount
// boundary: Lstat flags it, OpenFile refuses it before any call, and a caller
// that dropped the flag is refused after resolving it. The mount table is
// substituted so the test runs without privileges.
func TestMountTableBoundaryForFiles(t *testing.T) {
	src := t.TempDir()
	canon, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "bound file"))
	writeFile(t, filepath.Join(src, "plain"))
	var st unix.Stat_t
	if err := unix.Stat(src, &st); err != nil {
		t.Fatal(err)
	}
	mm := fmt.Sprintf("%d:%d", unix.Major(st.Dev), unix.Minor(st.Dev))
	table := fmt.Sprintf("22 1 %[1]s / / rw - ext4 /dev/root rw\n"+
		"30 22 %[1]s /hostname %[2]s/bound\\040file rw - ext4 /dev/root rw\n",
		mm, strings.ReplaceAll(canon, " ", `\040`))
	d := openRoot(t, fsaccess.NewOSWithMountinfo(t, table), src)

	bound := mustLstat(t, d, "bound file")
	if !bound.MountBoundary || bound.Kind != domain.EntryFile {
		t.Errorf("Lstat(bound file) = %+v, want a regular file marked as mount boundary", bound)
	}
	if plain := mustLstat(t, d, "plain"); plain.MountBoundary {
		t.Error("Lstat(plain).MountBoundary = true")
	}
	f, err := d.OpenFile([]byte("bound file"), bound)
	wantNoFile(t, f)
	wantErr(t, err, "", fsaccess.ErrMountBoundary)
	bound.MountBoundary = false
	f, err = d.OpenFile([]byte("bound file"), bound)
	wantNoFile(t, f)
	wantErr(t, err, domain.OutcomeChangedDuringObservation, fsaccess.ErrMountBoundary)
	mustOpenFile(t, d, "plain", mustLstat(t, d, "plain"))
}

// A file whose permissions deny reading is unreadable, never absent.
func TestOpenFileUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions do not deny reading")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "locked"), []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	d := openRoot(t, fsaccess.NewOS(), src)
	f, err := d.OpenFile([]byte("locked"), mustLstat(t, d, "locked"))
	wantNoFile(t, f)
	wantErr(t, err, domain.OutcomeUnreadable, nil)
}
