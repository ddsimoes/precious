package instrument_test

import (
	"io"
	"syscall"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// openFileAt opens /src, then the file name in it, through rec.
func openFileAt(t *testing.T, rec *instrument.Recorder, name string) (fsaccess.Dir, fsaccess.EntryInfo) {
	t.Helper()
	d, err := rec.OpenRoot("/src")
	must(t, err)
	t.Cleanup(func() { d.Close() })
	info, err := d.Lstat([]byte(name))
	must(t, err)
	return d, info
}

// File calls are logged under the file's path; ReadAt records the requested
// length and offset and the bytes returned, and BytesRead sums exactly the
// bytes returned.
func TestFileCallsAndBytesRead(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src").Dir("d").File("f", 2500, time.Time{})
	rec := instrument.Wrap(fsys)
	d, dInfo := openFileAt(t, rec, "d")
	sub, err := d.OpenDir([]byte("d"), dInfo)
	must(t, err)
	defer sub.Close()
	info, err := sub.Lstat([]byte("f"))
	must(t, err)
	rec.Reset()

	f, err := sub.OpenFile([]byte("f"), info)
	must(t, err)
	buf := make([]byte, 1000)
	type read struct {
		off   int64
		bytes int
		err   error
	}
	reads := []read{{0, 1000, nil}, {1000, 1000, nil}, {2000, 500, io.EOF}, {2500, 0, io.EOF}}
	for _, r := range reads {
		if n, err := f.ReadAt(buf, r.off); n != r.bytes || err != r.err {
			t.Fatalf("ReadAt(%d) = %d, %v; want %d, %v", r.off, n, err, r.bytes, r.err)
		}
	}
	if _, err := f.Stat(); err != nil {
		t.Fatal(err)
	}
	must(t, f.Close())

	if got := rec.BytesRead(); got != 2500 {
		t.Errorf("BytesRead() = %d, want 2500", got)
	}
	want := map[instrument.Op]int{instrument.OpOpenFile: 1, instrument.OpReadAt: 4, instrument.OpFileStat: 1, instrument.OpClose: 1}
	if counts := rec.Counts(); len(counts) != len(want) {
		t.Errorf("Counts() = %v, want %v", counts, want)
	}
	for op, n := range want {
		if rec.Count(op) != n {
			t.Errorf("count of %s = %d, want %d", op, rec.Count(op), n)
		}
	}
	calls := rec.Calls()
	if len(calls) != 7 {
		t.Fatalf("logged %d calls, want 7: %+v", len(calls), calls)
	}
	for i, c := range calls {
		if c.FullPath() != "/src/d/f" || c.Depth() != 2 {
			t.Errorf("call %d %s addresses %s at depth %d, want /src/d/f at depth 2", i, c.Op, c.FullPath(), c.Depth())
		}
	}
	for i, r := range reads {
		c := calls[1+i]
		if c.Op != instrument.OpReadAt || c.N != len(buf) || c.Off != r.off || c.Bytes != r.bytes || c.Err != r.err {
			t.Errorf("call %d = {%s N %d Off %d Bytes %d Err %v}, want ReadAt %+v of %d", 1+i, c.Op, c.N, c.Off, c.Bytes, c.Err, r, len(buf))
		}
	}
	if calls[0].Op != instrument.OpOpenFile || calls[5].Op != instrument.OpFileStat || calls[6].Op != instrument.OpClose {
		t.Errorf("call order = %s … %s, %s", calls[0].Op, calls[5].Op, calls[6].Op)
	}

	rec.Reset()
	if rec.BytesRead() != 0 || len(rec.Calls()) != 0 {
		t.Error("Reset left the byte count or the log behind")
	}
}

// A BeforeCall hook on ReadAt can patch the synthetic file mid-read: the next
// chunk sees the patch, and the closing Stat sees the new change time.
func TestBeforeCallHookPatchesMidRead(t *testing.T) {
	fsys := synthfs.New()
	file := fsys.Root("/src").File("f", 3000, time.Time{})
	rec := instrument.Wrap(fsys)
	d, info := openFileAt(t, rec, "f")
	rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpReadAt && c.Off == 1000 {
			file.Patch(1500, []byte("HOOK"))
		}
	})
	f, err := d.OpenFile([]byte("f"), info)
	must(t, err)
	defer f.Close()

	buf := make([]byte, 1000)
	_, err = f.ReadAt(buf, 0)
	must(t, err)
	_, err = f.ReadAt(buf, 1000)
	must(t, err)
	if string(buf[500:504]) != "HOOK" {
		t.Errorf("the chunk read after the hook holds %q, want HOOK", buf[500:504])
	}
	st, err := f.Stat()
	must(t, err)
	if !st.Ctime.After(info.Ctime) || !st.ModTime.Equal(info.ModTime) || st.Size != info.Size {
		t.Errorf("Stat after the patch = %+v; want a later change time and the observed size and mtime %+v", st, info)
	}
}

// Injected errors on OpenFile, ReadAt, and FileStat are returned without
// delegating, and an injected ReadAt adds no bytes.
func TestInjectedFileErrors(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src").File("f", 100, time.Time{})
	rec := instrument.Wrap(fsys)
	d, info := openFileAt(t, rec, "f")

	openErr := &fsaccess.Error{Op: "OpenFile", Name: []byte("f"), Outcome: domain.OutcomeUnreadable, Err: syscall.EACCES}
	rec.InjectError(instrument.OpOpenFile, "/src/f", openErr)
	if f, err := d.OpenFile([]byte("f"), info); f != nil || err != openErr {
		t.Fatalf("OpenFile = %v, %v; want the injected error", f, err)
	}
	rec.ClearErrors()
	f, err := d.OpenFile([]byte("f"), info)
	must(t, err)
	defer f.Close()

	readErr := &fsaccess.Error{Op: "ReadAt", Name: []byte("f"), Outcome: domain.OutcomeUnavailable, Err: syscall.EIO}
	statErr := &fsaccess.Error{Op: "FileStat", Name: []byte("f"), Outcome: domain.OutcomeUnavailable, Err: syscall.EIO}
	rec.InjectError(instrument.OpReadAt, "/src/f", readErr)
	rec.InjectError(instrument.OpFileStat, "/src/f", statErr)
	buf := make([]byte, 64)
	if n, err := f.ReadAt(buf, 0); n != 0 || err != readErr {
		t.Errorf("ReadAt = %d, %v; want 0 and the injected error", n, err)
	}
	if _, err := f.Stat(); err != statErr {
		t.Errorf("Stat = %v, want the injected error", err)
	}
	if rec.BytesRead() != 0 {
		t.Errorf("an injected ReadAt counted %d bytes", rec.BytesRead())
	}
	rec.ClearErrors()
	if n, err := f.ReadAt(buf, 0); n != 64 || err != nil {
		t.Errorf("ReadAt after ClearErrors = %d, %v", n, err)
	}
	if rec.BytesRead() != 64 {
		t.Errorf("BytesRead() = %d, want 64", rec.BytesRead())
	}
}
