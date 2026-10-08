package instrument_test

import (
	"errors"
	"slices"
	"syscall"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// r3 task 1.5: the Recorder's directories implement fsaccess.Writer. Every
// write is counted and logged (a rename with both paths), hooks run before
// it, and an injected error stops it before it reaches the filesystem.
func TestWriterCallsRecorded(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.Dir("Fotos").File("a.jpg", 1, time.Time{})
	root.Dir("Arquivo")
	rec := instrument.Wrap(fsys)
	d, err := rec.OpenRoot("/src")
	must(t, err)
	open := func(name string) fsaccess.Dir {
		info, err := d.Lstat([]byte(name))
		must(t, err)
		sub, err := d.OpenDir([]byte(name), info)
		must(t, err)
		return sub
	}
	fotos, arquivo := open("Fotos"), open("Arquivo")
	writer := func(d fsaccess.Dir) fsaccess.Writer {
		w, ok := fsaccess.AsWriter(d)
		if !ok {
			t.Fatalf("%T has no Writer", d)
		}
		return w
	}
	var hooked []string
	rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpRename || c.Op == instrument.OpMkdir || c.Op == instrument.OpRmdir || c.Op == instrument.OpSync {
			hooked = append(hooked, string(c.Op)+" "+c.FullPath()+" "+c.To)
		}
	})
	rec.Reset()

	must(t, writer(fotos).RenameNoReplace([]byte("a.jpg"), arquivo, []byte("b.jpg")))
	must(t, writer(fotos).Sync())
	must(t, writer(arquivo).Sync())
	must(t, writer(arquivo).Mkdir([]byte("2007")))
	must(t, writer(arquivo).Rmdir([]byte("2007")))
	if got := root.Child("Arquivo").Child("b.jpg"); got == nil {
		t.Fatal("the rename did not reach the filesystem")
	}

	want := []string{
		"RenameNoReplace /src/Fotos/a.jpg /src/Arquivo/b.jpg",
		"Sync /src/Fotos ",
		"Sync /src/Arquivo ",
		"Mkdir /src/Arquivo/2007 ",
		"Rmdir /src/Arquivo/2007 ",
	}
	if !slices.Equal(hooked, want) {
		t.Errorf("hooks saw %q, want %q", hooked, want)
	}
	var logged []string
	for _, c := range rec.Calls() {
		logged = append(logged, string(c.Op)+" "+c.FullPath()+" "+c.To)
	}
	if !slices.Equal(logged, want) {
		t.Errorf("logged %q, want %q", logged, want)
	}
	for op, n := range map[instrument.Op]int{instrument.OpRename: 1, instrument.OpSync: 2, instrument.OpMkdir: 1, instrument.OpRmdir: 1} {
		if got := rec.Count(op); got != n {
			t.Errorf("Count(%s) = %d, want %d", op, got, n)
		}
	}

	// Injected errors stop each write before the filesystem.
	injected := &fsaccess.Error{Op: "RenameNoReplace", Name: []byte("b.jpg"), Err: errors.Join(fsaccess.ErrExist, syscall.EEXIST)}
	rec.InjectError(instrument.OpRename, "/src/Arquivo/b.jpg", injected)
	rec.InjectError(instrument.OpMkdir, "/src/Arquivo/novo", injected)
	rec.InjectError(instrument.OpRmdir, "/src/Arquivo/novo", injected)
	rec.InjectError(instrument.OpSync, "/src/Arquivo", injected)
	for _, err := range []error{
		writer(arquivo).RenameNoReplace([]byte("b.jpg"), fotos, []byte("a.jpg")),
		writer(arquivo).Mkdir([]byte("novo")),
		writer(arquivo).Rmdir([]byte("novo")),
		writer(arquivo).Sync(),
	} {
		if err != injected {
			t.Errorf("err = %v, want the injected error", err)
		}
	}
	if root.Child("Fotos").Child("a.jpg") != nil || root.Child("Arquivo").Child("novo") != nil {
		t.Error("an injected write reached the filesystem")
	}
	if c := rec.Calls()[len(rec.Calls())-1]; c.Op != instrument.OpSync || c.Err != injected {
		t.Errorf("last call = %+v, want the failed Sync", c)
	}

	// A write the wrapped directory refuses is logged with its error.
	rec.ClearErrors()
	err = writer(arquivo).Rmdir([]byte("b.jpg"))
	if o, _ := fsaccess.OutcomeOf(err); o != domain.OutcomeUnavailable || !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("Rmdir of a file = %v, want unavailable ENOTDIR", err)
	}
	if c := rec.Calls()[len(rec.Calls())-1]; c.Op != instrument.OpRmdir || c.Err != err {
		t.Errorf("last call = %+v, want the failed Rmdir", c)
	}
}

// A wrapped directory without a Writer fails every write with
// ErrNoReplaceUnsupported, and the call is still logged.
func TestWriterWithoutInnerWriter(t *testing.T) {
	fsys := synthfs.New()
	fsys.Root("/src").File("a.jpg", 1, time.Time{})
	rec := instrument.Wrap(readOnlyFS{fsys})
	d, err := rec.OpenRoot("/src")
	must(t, err)
	w, ok := fsaccess.AsWriter(d)
	if !ok {
		t.Fatal("instrumented Dir has no Writer")
	}
	for _, err := range []error{
		w.RenameNoReplace([]byte("a.jpg"), d, []byte("b.jpg")),
		w.Mkdir([]byte("novo")),
		w.Rmdir([]byte("novo")),
		w.Sync(),
		w.CreateExclusive([]byte("novo"), []byte("x")),
		w.Unlink([]byte("a.jpg")),
	} {
		if !errors.Is(err, fsaccess.ErrNoReplaceUnsupported) {
			t.Errorf("err = %v, want ErrNoReplaceUnsupported", err)
		}
	}
	n := 0
	for _, op := range []instrument.Op{instrument.OpRename, instrument.OpMkdir, instrument.OpRmdir, instrument.OpSync,
		instrument.OpCreate, instrument.OpUnlink} {
		n += rec.Count(op)
	}
	if n != 6 {
		t.Errorf("%d writes logged, want 6", n)
	}
}

// r4 task 1.3: CreateExclusive and Unlink are counted and logged like the
// other writes (a create with N = len(data)), hooks run before them, and an
// injected error stops them before the filesystem.
func TestQuarantineWritesRecorded(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	q := root.Dir(".precious-quarantine")
	q.File("velho.jpg", 1, time.Time{})
	rec := instrument.Wrap(fsys)
	d, err := rec.OpenRoot("/src")
	must(t, err)
	info, err := d.Lstat([]byte(".precious-quarantine"))
	must(t, err)
	qd, err := d.OpenDir([]byte(".precious-quarantine"), info)
	must(t, err)
	w, ok := fsaccess.AsWriter(qd)
	if !ok {
		t.Fatalf("%T has no Writer", qd)
	}
	var hooked []string
	rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpCreate || c.Op == instrument.OpUnlink {
			hooked = append(hooked, string(c.Op)+" "+c.FullPath())
		}
	})
	rec.Reset()

	must(t, w.CreateExclusive([]byte("record.json"), []byte("{}\n")))
	must(t, w.Unlink([]byte("velho.jpg")))
	if q.Child("record.json") == nil || q.Child("velho.jpg") != nil {
		t.Fatal("the writes did not reach the filesystem")
	}
	want := []string{
		"CreateExclusive /src/.precious-quarantine/record.json",
		"Unlink /src/.precious-quarantine/velho.jpg",
	}
	if !slices.Equal(hooked, want) {
		t.Errorf("hooks saw %q, want %q", hooked, want)
	}
	calls := rec.Calls()
	var logged []string
	for _, c := range calls {
		logged = append(logged, string(c.Op)+" "+c.FullPath())
	}
	if !slices.Equal(logged, want) {
		t.Fatalf("logged %q, want %q", logged, want)
	}
	if calls[0].N != 3 || calls[0].Err != nil || calls[1].Err != nil {
		t.Errorf("logged calls = %+v, want a 3-byte create and both without error", calls)
	}
	if rec.Count(instrument.OpCreate) != 1 || rec.Count(instrument.OpUnlink) != 1 {
		t.Errorf("counts = %v, want one create and one unlink", rec.Counts())
	}

	injected := &fsaccess.Error{Op: "Unlink", Name: []byte("record.json"), Err: errors.Join(fsaccess.ErrReadOnly, syscall.EROFS)}
	rec.InjectError(instrument.OpCreate, "/src/.precious-quarantine/outro.json", injected)
	rec.InjectError(instrument.OpUnlink, "/src/.precious-quarantine/record.json", injected)
	for _, err := range []error{
		w.CreateExclusive([]byte("outro.json"), []byte("{}")),
		w.Unlink([]byte("record.json")),
	} {
		if err != injected {
			t.Errorf("err = %v, want the injected error", err)
		}
	}
	if q.Child("outro.json") != nil || q.Child("record.json") == nil {
		t.Error("an injected write reached the filesystem")
	}
	if c := rec.Calls()[len(rec.Calls())-1]; c.Op != instrument.OpUnlink || c.Err != injected {
		t.Errorf("last call = %+v, want the failed Unlink", c)
	}
	if rec.Count(instrument.OpCreate) != 2 || rec.Count(instrument.OpUnlink) != 2 {
		t.Errorf("counts = %v, want two creates and two unlinks", rec.Counts())
	}

	// Refusals of the wrapped directory are logged with their error.
	rec.ClearErrors()
	err = w.CreateExclusive([]byte("record.json"), nil)
	if !errors.Is(err, fsaccess.ErrExist) {
		t.Errorf("CreateExclusive over a taken name = %v, want ErrExist", err)
	}
	if c := rec.Calls()[len(rec.Calls())-1]; c.Op != instrument.OpCreate || c.Err != err {
		t.Errorf("last call = %+v, want the failed CreateExclusive", c)
	}
}

// readOnlyFS hands out directories that expose only fsaccess.Dir.
type readOnlyFS struct{ fsaccess.FS }

func (f readOnlyFS) OpenRoot(path string) (fsaccess.Dir, error) {
	d, err := f.FS.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return struct{ fsaccess.Dir }{d}, nil
}
