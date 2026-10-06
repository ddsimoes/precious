package synthfs_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"runtime"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

func openFile(t *testing.T, d fsaccess.Dir, name string) fsaccess.File {
	t.Helper()
	info, err := d.Lstat([]byte(name))
	must(t, err)
	f, err := d.OpenFile([]byte(name), info)
	must(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

// readAll reads the whole file in 1000-byte chunks, as a copy search does.
func readAll(t *testing.T, f fsaccess.File) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 1000)
	for off := int64(0); ; {
		n, err := f.ReadAt(buf, off)
		out = append(out, buf[:n]...)
		off += int64(n)
		if err == io.EOF {
			return out
		}
		must(t, err)
	}
}

// Equal seeds and sizes give equal bytes; distinct inodes give distinct
// default content; explicit content is served as given.
func TestContentGeneration(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.File("a", 10_000, time.Time{}).Seed(42)
	root.Dir("sub").File("b", 10_000, time.Time{}).Seed(42)
	root.File("c", 10_000, time.Time{}).Seed(43)
	root.File("x", 10_000, time.Time{})
	root.File("y", 10_000, time.Time{})
	root.File("text", 0, time.Time{}).Content([]byte("hello, world"))
	d := open(t, fsys, "/src")
	info, err := d.Lstat([]byte("sub"))
	must(t, err)
	sub, err := d.OpenDir([]byte("sub"), info)
	must(t, err)
	defer sub.Close()

	a, b, c := readAll(t, openFile(t, d, "a")), readAll(t, openFile(t, sub, "b")), readAll(t, openFile(t, d, "c"))
	if len(a) != 10_000 || !bytes.Equal(a, b) {
		t.Errorf("equal seed and size: %d and %d bytes, equal = %v", len(a), len(b), bytes.Equal(a, b))
	}
	if bytes.Equal(a, c) {
		t.Error("seeds 42 and 43 gave equal bytes")
	}
	x, y := readAll(t, openFile(t, d, "x")), readAll(t, openFile(t, d, "y"))
	if len(x) != 10_000 || bytes.Equal(x, y) || bytes.Equal(x, a) {
		t.Error("default content of distinct inodes is not distinct")
	}
	if bytes.Count(x, x[:8]) > 1 {
		t.Error("default content repeats its first word")
	}
	if got := readAll(t, openFile(t, d, "text")); string(got) != "hello, world" {
		t.Errorf("explicit content = %q", got)
	}
	if info, err := d.Lstat([]byte("text")); err != nil || info.Size != 12 {
		t.Errorf("Content set size %d, %v; want 12", info.Size, err)
	}
}

// A 1 GiB generated file reads at its start, middle, and end without being
// materialised, and unaligned reads agree with aligned ones.
func TestHugeGeneratedFileIsNotAllocated(t *testing.T) {
	const size = 1 << 30
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	fsys := synthfs.New()
	fsys.Root("/src").File("big.iso", size, time.Time{})
	d := open(t, fsys, "/src")
	f := openFile(t, d, "big.iso")
	buf, ref := make([]byte, 4096), make([]byte, 4096+16)
	for _, off := range []int64{0, size/2 - 3, size - 4096} {
		n, err := f.ReadAt(buf, off)
		if n != len(buf) || err != nil {
			t.Fatalf("ReadAt(%d) = %d, %v", off, n, err)
		}
		aligned := off &^ 7
		if _, err := f.ReadAt(ref[:len(buf)+int(off-aligned)], aligned); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf, ref[off-aligned:int(off-aligned)+len(buf)]) {
			t.Errorf("ReadAt(%d) disagrees with the aligned read at %d", off, aligned)
		}
	}
	if n, err := f.ReadAt(buf, size-100); n != 100 || err != io.EOF {
		t.Errorf("ReadAt across the end = %d, %v; want 100, io.EOF", n, err)
	}

	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
		t.Errorf("building and reading a 1 GiB file allocated %d bytes", grown)
	}
}

// Content, size, modification-time, permission, link, and identity changes
// advance the change time on one strictly increasing clock; Patch and Size
// keep the modification time; Ctime sets it explicitly.
func TestChangeTimes(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	mtime := time.Date(2022, 5, 1, 0, 0, 0, 0, time.UTC)
	file := root.File("f", 100, mtime)
	other := root.File("g", 100, mtime)
	link := root.Symlink("l", "f")
	dir := root.Dir("d")
	d := open(t, fsys, "/src")
	lstat := func(name string) fsaccess.EntryInfo {
		t.Helper()
		info, err := d.Lstat([]byte(name))
		must(t, err)
		return info
	}
	for _, name := range []string{"f", "g", "l", "d"} {
		if lstat(name).Ctime.IsZero() {
			t.Errorf("%s has no change time", name)
		}
	}
	if !d.Self().Ctime.Before(lstat("f").Ctime) || !lstat("f").Ctime.Before(lstat("g").Ctime) {
		t.Error("creation change times do not increase")
	}

	last := lstat("g").Ctime
	for _, step := range []struct {
		name   string
		entry  string
		change func()
	}{
		{"Patch", "f", func() { file.Patch(10, []byte("xyz")) }},
		{"Size", "f", func() { file.Size(200) }},
		{"Seed", "f", func() { file.Seed(7) }},
		{"Content", "f", func() { file.Content(bytes.Repeat([]byte{1}, 100)) }},
		{"ModTime", "f", func() { file.ModTime(mtime) }},
		{"Perm", "f", func() { file.Perm(0o600) }},
		{"HardLink on its target", "g", func() { dir.HardLink("g2", other) }},
		{"Ident", "g", func() { other.Ident(0, 999) }},
		{"Perm of a symlink", "l", func() { link.Perm(0o700) }},
		{"Perm of a directory", "d", func() { dir.Perm(0o700) }},
	} {
		before := lstat(step.entry)
		step.change()
		after := lstat(step.entry)
		if !after.Ctime.After(before.Ctime) || !after.Ctime.After(last) {
			t.Errorf("%s: change time %v, before %v, clock %v; want it advanced", step.name, after.Ctime, before.Ctime, last)
		}
		if (step.name == "Patch" || step.name == "Size") && !after.ModTime.Equal(before.ModTime) {
			t.Errorf("%s changed the modification time", step.name)
		}
		last = after.Ctime
	}
	if info := lstat("f"); info.Size != 100 || !info.ModTime.Equal(mtime) {
		t.Errorf("f after the changes = %+v", info)
	}

	explicit := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	file.Ctime(explicit)
	if got := lstat("f").Ctime; !got.Equal(explicit) {
		t.Errorf("Ctime(%v) reported %v", explicit, got)
	}
	other.Perm(0o644)
	if got := lstat("g").Ctime; !got.After(explicit) {
		t.Errorf("a change after an explicit later Ctime gave %v; the clock went back", got)
	}
}

// OpenFile follows the os.Root backend's contract.
func TestOpenFileContract(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.File("f", 10, time.Time{})
	root.Dir("dir")
	root.Symlink("link", "f")
	root.Special("pipe", domain.EntryFIFO)
	root.File("elsewhere", 10, time.Time{}).Dev(998)
	root.File("bound", 10, time.Time{}).MountPoint()
	root.File("locked", 10, time.Time{}).Unreadable()
	root.File("swapped", 10, time.Time{})
	root.Generated("gen", 3, 10)
	d := open(t, fsys, "/src")
	lstat := func(dir fsaccess.Dir, name string) fsaccess.EntryInfo {
		t.Helper()
		info, err := dir.Lstat([]byte(name))
		must(t, err)
		return info
	}
	refused := func(err error, outcome domain.AccessOutcome, target error) {
		t.Helper()
		var e *fsaccess.Error
		if !errors.As(err, &e) || e.Outcome != outcome || (target != nil && !errors.Is(err, target)) {
			t.Errorf("OpenFile = %v, want outcome %q and %v", err, outcome, target)
		}
	}
	fileInfo := lstat(d, "f")

	t.Run("refused before touching anything", func(t *testing.T) {
		for _, bad := range []string{"../x", "a/b", ".", "..", "", "a\x00b"} {
			_, err := d.OpenFile([]byte(bad), fileInfo)
			refused(err, "", fsaccess.ErrInvalidName)
		}
		for _, name := range []string{"dir", "link", "pipe"} {
			_, err := d.OpenFile([]byte(name), lstat(d, name))
			refused(err, "", fsaccess.ErrNotRegular)
		}
		for _, name := range []string{"elsewhere", "bound"} {
			info := lstat(d, name)
			if !info.MountBoundary {
				t.Errorf("Lstat(%s).MountBoundary = false", name)
			}
			_, err := d.OpenFile([]byte(name), info)
			refused(err, "", fsaccess.ErrMountBoundary)
			info.MountBoundary = false
			_, err = d.OpenFile([]byte(name), info)
			refused(err, domain.OutcomeChangedDuringObservation, fsaccess.ErrMountBoundary)
		}
	})
	t.Run("changed between observation and open", func(t *testing.T) {
		info := lstat(d, "swapped")
		root.Remove("swapped")
		root.Symlink("swapped", "f")
		_, err := d.OpenFile([]byte("swapped"), info)
		refused(err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
		root.Remove("swapped")
		root.Special("swapped", domain.EntryFIFO)
		_, err = d.OpenFile([]byte("swapped"), info)
		refused(err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
		root.Remove("swapped")
		_, err = d.OpenFile([]byte("swapped"), info)
		refused(err, domain.OutcomeAbsent, fs.ErrNotExist)

		stale := lstat(d, "f")
		root.Child("f").Patch(0, []byte("z"))
		_, err = d.OpenFile([]byte("f"), stale)
		refused(err, domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
	})
	t.Run("unreadable", func(t *testing.T) {
		info := lstat(d, "locked")
		if info.Mode.Perm() != 0 {
			t.Errorf("unreadable file mode = %v", info.Mode)
		}
		_, err := d.OpenFile([]byte("locked"), info)
		refused(err, domain.OutcomeUnreadable, fs.ErrPermission)
	})
	t.Run("generated file", func(t *testing.T) {
		gen, err := d.OpenDir([]byte("gen"), lstat(d, "gen"))
		must(t, err)
		defer gen.Close()
		info := lstat(gen, "file-0000000.dat")
		f := openFile(t, gen, "file-0000000.dat")
		if got := readAll(t, f); int64(len(got)) != info.Size {
			t.Errorf("read %d bytes of a %d-byte generated file", len(got), info.Size)
		}
		if st, err := f.Stat(); err != nil || !reflect.DeepEqual(st, info) {
			t.Errorf("Stat() = %+v, %v; want %+v", st, err, info)
		}
	})
	t.Run("vanished root and closed file", func(t *testing.T) {
		info := lstat(d, "f")
		f := openFile(t, d, "f")
		fsys.Vanish("/src")
		_, err := f.ReadAt(make([]byte, 1), 0)
		refused(err, domain.OutcomeUnavailable, nil)
		_, err = d.OpenFile([]byte("f"), info)
		refused(err, domain.OutcomeUnavailable, nil)
		fsys.Reattach("/src")
		must(t, f.Close())
		_, err = f.ReadAt(make([]byte, 1), 0)
		refused(err, domain.OutcomeUnavailable, fs.ErrClosed)
		_, err = f.Stat()
		refused(err, domain.OutcomeUnavailable, fs.ErrClosed)
	})
}

// An open file is read as it is at each call: a patch or a size change between
// chunks is visible to the next chunk, and Stat reports the new metadata.
func TestReadsSeeCurrentContent(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	file := root.File("f", 3000, time.Time{})
	root.HardLink("again", file)
	d := open(t, fsys, "/src")
	f := openFile(t, d, "f")
	link := openFile(t, d, "again")

	first := make([]byte, 1000)
	_, err := f.ReadAt(first, 0)
	must(t, err)
	file.Patch(1500, []byte("PATCHED"))
	second := make([]byte, 1000)
	_, err = f.ReadAt(second, 1000)
	must(t, err)
	if string(second[500:507]) != "PATCHED" {
		t.Errorf("the chunk after a Patch reads %q", second[500:507])
	}
	again := make([]byte, 1000)
	_, err = link.ReadAt(again, 1000)
	must(t, err)
	if !bytes.Equal(again, second) {
		t.Error("a hard link reads other content than its target")
	}
	untouched := make([]byte, 1000)
	_, err = f.ReadAt(untouched, 0)
	must(t, err)
	if !bytes.Equal(untouched, first) {
		t.Error("a Patch changed bytes outside its range")
	}

	file.Size(1503)
	n, err := f.ReadAt(second, 1000)
	if n != 503 || err != io.EOF || string(second[500:503]) != "PAT" {
		t.Errorf("ReadAt after shrinking = %d, %v, %q; want 503 bytes ending in PAT and io.EOF", n, err, second[500:n])
	}
	if st, err := f.Stat(); err != nil || st.Size != 1503 || string(st.Name) != "f" {
		t.Errorf("Stat after shrinking = %+v, %v", st, err)
	}
	file.Size(2000)
	n, err = f.ReadAt(second, 1000)
	if n != 1000 || err != nil || string(second[500:503]) != "PAT" || bytes.Contains(second[503:], []byte("CHED")) {
		t.Errorf("ReadAt after growing = %d, %v; the cut patch reappeared or was lost", n, err)
	}

	text := root.File("text", 0, time.Time{}).Content([]byte("abcdef"))
	tf := openFile(t, d, "text")
	text.Size(4).Size(8)
	buf := make([]byte, 8)
	if n, err := tf.ReadAt(buf, 0); n != 8 || err != nil || !bytes.Equal(buf, []byte("abcd\x00\x00\x00\x00")) {
		t.Errorf("explicit content shrunk and grown = %q, %v; want abcd and zeros", buf[:n], err)
	}
}

// FailReadAfter returns the bytes before the failing offset with the outcome.
func TestFailReadAfter(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src").File("bad", 5000, time.Time{}).FailReadAfter(2500, domain.OutcomeUnavailable)
	d := open(t, fsys, "/src")
	f := openFile(t, d, "bad")
	buf := make([]byte, 1000)
	for off, wantN := range map[int64]int{0: 1000, 2000: 500, 3000: 0} {
		n, err := f.ReadAt(buf, off)
		if off+1000 <= 2500 {
			if n != wantN || err != nil {
				t.Errorf("ReadAt(%d) = %d, %v; want %d bytes", off, n, err, wantN)
			}
			continue
		}
		if n != wantN || outcome(err) != domain.OutcomeUnavailable {
			t.Errorf("ReadAt(%d) = %d, %v; want %d bytes and unavailable", off, n, err, wantN)
		}
	}
}

func TestContentBuildersPanicOnMisuse(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	file := root.File("f", 10, time.Time{})
	dir := root.Dir("d")
	for name, fn := range map[string]func(){
		"Content of a directory": func() { dir.Content([]byte("x")) },
		"Seed of a directory":    func() { dir.Seed(1) },
		"Patch past the end":     func() { file.Patch(8, []byte("xyz")) },
		"Patch before the start": func() { file.Patch(-1, []byte("x")) },
		"negative Size":          func() { file.Size(-1) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			fn()
		}()
	}
}
