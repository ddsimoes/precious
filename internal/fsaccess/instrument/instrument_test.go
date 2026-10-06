package instrument_test

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

func TestRecorderCountsScriptedSequence(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src/a")
	projects := root.Dir("Projects")
	projects.File("main.go", 10, time.Time{})
	projects.File("go.mod", 5, time.Time{})
	root.Symlink("alias", "./Projects")
	root.File("readme", 1, time.Time{})

	rec := instrument.Wrap(fsys)
	var hooked []string
	rec.SetBeforeCall(func(c instrument.Call) {
		if c.Err != nil || c.Entries != 0 {
			t.Errorf("hook saw a completed call: %+v", c)
		}
		hooked = append(hooked, fmt.Sprintf("%s %s", c.Op, c.FullPath()))
	})
	var openDirHooks []string
	rec.SetBeforeOpenDir(func(c instrument.Call) { openDirHooks = append(openDirHooks, c.FullPath()) })
	injected := &fsaccess.Error{Op: "Lstat", Name: []byte("readme"), Outcome: domain.OutcomeUnreadable, Err: syscall.EACCES}
	rec.InjectError(instrument.OpLstat, "/src/a/readme", injected)

	// The script.
	d, err := rec.OpenRoot("/src/a/")
	must(t, err)
	for _, want := range []int{2, 1} {
		got, err := d.ReadBatch(2)
		must(t, err)
		if len(got) != want {
			t.Fatalf("ReadBatch(2) returned %d entries, want %d", len(got), want)
		}
	}
	if _, err := d.ReadBatch(2); err != io.EOF {
		t.Fatalf("third ReadBatch = %v, want io.EOF", err)
	}
	pInfo, err := d.Lstat([]byte("Projects"))
	must(t, err)
	_, err = d.Lstat([]byte("alias"))
	must(t, err)
	if _, err := d.Lstat([]byte("readme")); err != injected {
		t.Fatalf("Lstat(readme) = %v, want the injected error", err)
	}
	_, err = d.Readlink([]byte("alias"))
	must(t, err)
	child, err := d.OpenDir([]byte("Projects"), pInfo)
	must(t, err)
	got, err := child.ReadBatch(10)
	must(t, err)
	if len(got) != 2 {
		t.Fatalf("child ReadBatch returned %d entries, want 2", len(got))
	}
	if _, err := child.ReadBatch(10); err != io.EOF {
		t.Fatalf("child ReadBatch = %v, want io.EOF", err)
	}
	_, err = child.Lstat([]byte("main.go"))
	must(t, err)
	_, err = child.FSInfo()
	must(t, err)
	must(t, child.Close())
	must(t, d.Close())

	wantCounts := map[instrument.Op]int{
		instrument.OpOpenRoot: 1, instrument.OpReadBatch: 5, instrument.OpLstat: 4,
		instrument.OpReadlink: 1, instrument.OpOpenDir: 1, instrument.OpFSInfo: 1, instrument.OpClose: 2,
	}
	counts := rec.Counts()
	if len(counts) != len(wantCounts) {
		t.Errorf("Counts() = %v, want %v", counts, wantCounts)
	}
	for op, n := range wantCounts {
		if counts[op] != n || rec.Count(op) != n {
			t.Errorf("count of %s = %d, want %d", op, counts[op], n)
		}
	}
	if rec.Entries() != 5 {
		t.Errorf("Entries() = %d, want 5", rec.Entries())
	}

	type row struct {
		op      instrument.Op
		path    string
		depth   int
		entries int
		err     error
	}
	want := []row{
		{instrument.OpOpenRoot, "/src/a", 0, 0, nil},
		{instrument.OpReadBatch, "/src/a", 0, 2, nil},
		{instrument.OpReadBatch, "/src/a", 0, 1, nil},
		{instrument.OpReadBatch, "/src/a", 0, 0, io.EOF},
		{instrument.OpLstat, "/src/a/Projects", 1, 0, nil},
		{instrument.OpLstat, "/src/a/alias", 1, 0, nil},
		{instrument.OpLstat, "/src/a/readme", 1, 0, injected},
		{instrument.OpReadlink, "/src/a/alias", 1, 0, nil},
		{instrument.OpOpenDir, "/src/a/Projects", 1, 0, nil},
		{instrument.OpReadBatch, "/src/a/Projects", 1, 2, nil},
		{instrument.OpReadBatch, "/src/a/Projects", 1, 0, io.EOF},
		{instrument.OpLstat, "/src/a/Projects/main.go", 2, 0, nil},
		{instrument.OpFSInfo, "/src/a/Projects", 1, 0, nil},
		{instrument.OpClose, "/src/a/Projects", 1, 0, nil},
		{instrument.OpClose, "/src/a", 0, 0, nil},
	}
	calls := rec.Calls()
	if len(calls) != len(want) || len(hooked) != len(want) {
		t.Fatalf("logged %d calls and hooked %d, want %d: %+v", len(calls), len(hooked), len(want), calls)
	}
	for i, w := range want {
		c := calls[i]
		if c.Op != w.op || c.FullPath() != w.path || c.Depth() != w.depth || c.Entries != w.entries || c.Err != w.err {
			t.Errorf("call %d = {%s %s depth %d entries %d err %v}, want %+v", i, c.Op, c.FullPath(), c.Depth(), c.Entries, c.Err, w)
		}
		if h := fmt.Sprintf("%s %s", w.op, w.path); hooked[i] != h {
			t.Errorf("hook %d saw %q, want %q", i, hooked[i], h)
		}
	}
	if calls[1].N != 2 || calls[9].N != 10 {
		t.Errorf("requested batch sizes = %d, %d; want 2, 10", calls[1].N, calls[9].N)
	}
	if len(openDirHooks) != 1 || openDirHooks[0] != "/src/a/Projects" {
		t.Errorf("OpenDir hook saw %v", openDirHooks)
	}
	if rec.MaxDepth() != 2 {
		t.Errorf("MaxDepth() = %d, want 2", rec.MaxDepth())
	}

	rec.Reset()
	if len(rec.Calls()) != 0 || len(rec.Counts()) != 0 || rec.Entries() != 0 {
		t.Error("Reset left counters or log behind")
	}
	rec.ClearErrors()
	d, err = rec.OpenRoot("/src/a")
	must(t, err)
	defer d.Close()
	if _, err := d.Lstat([]byte("readme")); err != nil {
		t.Errorf("Lstat(readme) after ClearErrors: %v", err)
	}
}

// A call blocked in the BeforeCall hook holds no Recorder lock: other calls and
// queries proceed, and the blocked call is logged only once it returns.
func TestBeforeCallHookCanBlock(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src").Dir("d")
	rec := instrument.Wrap(fsys)
	d, err := rec.OpenRoot("/src")
	must(t, err)
	defer d.Close()

	entered, release := make(chan struct{}), make(chan struct{})
	rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpReadBatch {
			close(entered)
			<-release
		}
	})
	done := make(chan error)
	go func() {
		_, err := d.ReadBatch(8)
		done <- err
	}()
	<-entered
	if _, err := d.Lstat([]byte("d")); err != nil {
		t.Fatalf("Lstat while ReadBatch is blocked: %v", err)
	}
	if n := rec.Count(instrument.OpReadBatch); n != 0 {
		t.Fatalf("blocked ReadBatch already counted (%d)", n)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := rec.Count(instrument.OpReadBatch); n != 1 {
		t.Fatalf("ReadBatch count = %d, want 1", n)
	}
}

func TestInjectedOpenRootError(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src")
	rec := instrument.Wrap(fsys)
	gone := &fsaccess.Error{Op: "OpenRoot", Name: []byte("/src"), Outcome: domain.OutcomeUnavailable, Err: syscall.ENOTCONN}
	rec.InjectError(instrument.OpOpenRoot, "/src", gone)
	if _, err := rec.OpenRoot("/src"); !errors.Is(err, syscall.ENOTCONN) {
		t.Fatalf("OpenRoot = %v, want the injected error", err)
	}
	if c := rec.Calls(); len(c) != 1 || c[0].Err != gone {
		t.Fatalf("log = %+v", c)
	}
}

// TestRecorderForwardsVolumeQueries: Mounts and Capabilities are delegated,
// logged with the cleaned path as Root, and can fail by injection.
func TestRecorderForwardsVolumeQueries(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src")
	rec := instrument.Wrap(fsys)
	mounts, err := rec.Mounts()
	must(t, err)
	if len(mounts) != 1 || mounts[0].Point != "/src" {
		t.Fatalf("Mounts() = %+v, want the one synthetic volume", mounts)
	}
	caps, err := rec.Capabilities("/src/a/../b/")
	must(t, err)
	if caps != fsaccess.UnknownCapabilities(false) {
		t.Fatalf("Capabilities = %+v, want the unknown set", caps)
	}
	gone := &fsaccess.Error{Op: "Capabilities", Name: []byte("/src/b"), Outcome: domain.OutcomeUnavailable, Err: syscall.EIO}
	rec.InjectError(instrument.OpCapabilities, "/src/b", gone)
	if _, err := rec.Capabilities("/src/b"); err != gone {
		t.Fatalf("Capabilities = %v, want the injected error", err)
	}
	calls := rec.Calls()
	if len(calls) != 3 || calls[0].Op != instrument.OpMounts || calls[0].Root != "" ||
		calls[1].Op != instrument.OpCapabilities || calls[1].Root != "/src/b" || calls[1].Err != nil ||
		calls[2].Op != instrument.OpCapabilities || calls[2].Err != gone {
		t.Fatalf("log = %+v", calls)
	}
	if rec.Count(instrument.OpCapabilities) != 2 || rec.Count(instrument.OpMounts) != 1 {
		t.Errorf("counts = %v", rec.Counts())
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
