package synthfs_test

import (
	"errors"
	"io"
	"syscall"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

// r3 task 1.5: synthfs directories implement fsaccess.Writer with the Linux
// backend's semantics and errors.

func writer(t *testing.T, d fsaccess.Dir) fsaccess.Writer {
	t.Helper()
	w, ok := fsaccess.AsWriter(d)
	if !ok {
		t.Fatalf("%T has no Writer", d)
	}
	return w
}

func openDir(t *testing.T, parent fsaccess.Dir, name string) fsaccess.Dir {
	t.Helper()
	d, err := parent.OpenDir([]byte(name), lstat(t, parent, name))
	must(t, err)
	t.Cleanup(func() { d.Close() })
	return d
}

// wantWriteErr fails unless err is an *fsaccess.Error of op with outcome o
// that matches target (when not nil).
func wantWriteErr(t *testing.T, err error, op string, target error, o domain.AccessOutcome) {
	t.Helper()
	var e *fsaccess.Error
	if !errors.As(err, &e) || e.Op != op {
		t.Fatalf("err = %v, want an *fsaccess.Error of %s", err, op)
	}
	if target != nil && !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
	if e.Outcome != o {
		t.Fatalf("err = %v with outcome %q, want %q", err, e.Outcome, o)
	}
}

func absent(t *testing.T, d fsaccess.Dir, name string) {
	t.Helper()
	if _, err := d.Lstat([]byte(name)); outcome(err) != domain.OutcomeAbsent {
		t.Fatalf("Lstat(%s) = %v, want absent", name, err)
	}
}

func listAll(t *testing.T, d fsaccess.Dir) []fsaccess.DirEntry {
	t.Helper()
	var out []fsaccess.DirEntry
	for {
		batch, err := d.ReadBatch(16)
		if err == io.EOF {
			return out
		}
		must(t, err)
		out = append(out, batch...)
	}
}

// A rename moves the node: the entry keeps its device, inode, size, and
// content under its new name, its subtree comes along, its change time and
// both folders' times advance, and handles opened before keep working.
func TestRenameKeepsIdentity(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	fotos := root.Dir("Fotos")
	fotos.File("praia.jpg", 5, synthfs.DefaultModTime).Content([]byte("PRAIA"))
	fotos.Dir("2007").File("a.jpg", 3, synthfs.DefaultModTime)
	root.Dir("Arquivo")

	d := open(t, fsys, "/src")
	src, dst := openDir(t, d, "Fotos"), openDir(t, d, "Arquivo")
	before, album := lstat(t, src, "praia.jpg"), lstat(t, src, "2007")
	srcBefore, dstBefore := lstat(t, d, "Fotos"), lstat(t, d, "Arquivo")
	held := openDir(t, src, "2007")

	must(t, writer(t, src).RenameNoReplace([]byte("praia.jpg"), dst, []byte("praia 2007.jpg")))
	absent(t, src, "praia.jpg")
	after := lstat(t, dst, "praia 2007.jpg")
	if after.Dev != before.Dev || after.Ino != before.Ino || after.Size != before.Size || !after.ModTime.Equal(before.ModTime) {
		t.Errorf("renamed file = %+v, want the identity of %+v", after, before)
	}
	if !after.Ctime.After(before.Ctime) {
		t.Errorf("change time %v did not advance past %v", after.Ctime, before.Ctime)
	}
	if got := readAll(t, openFile(t, dst, "praia 2007.jpg")); string(got) != "PRAIA" {
		t.Errorf("content after the rename = %q", got)
	}
	for name, was := range map[string]fsaccess.EntryInfo{"Fotos": srcBefore, "Arquivo": dstBefore} {
		now := lstat(t, d, name)
		if !now.ModTime.After(was.ModTime) || !now.Ctime.After(was.Ctime) || now.Ino != was.Ino {
			t.Errorf("%s after the rename = %+v, want later times than %+v", name, now, was)
		}
	}

	// A folder moves with its subtree, then is renamed within one folder.
	must(t, writer(t, src).RenameNoReplace([]byte("2007"), dst, []byte("2007")))
	if lstat(t, openDir(t, dst, "2007"), "a.jpg").Kind != domain.EntryFile {
		t.Error("the moved folder lost its file")
	}
	if lstat(t, held, "a.jpg").Kind != domain.EntryFile {
		t.Error("a handle opened before the move stopped working")
	}
	must(t, writer(t, dst).RenameNoReplace([]byte("2007"), dst, []byte("Praia 2007")))
	absent(t, dst, "2007")
	if got := lstat(t, dst, "Praia 2007"); got.Ino != album.Ino || got.Kind != domain.EntryDirectory {
		t.Errorf("renamed folder = %+v, want the folder %+v", got, album)
	}
	if names := listAll(t, dst); len(names) != 2 {
		t.Errorf("destination lists %v, want two entries", names)
	}
}

// A taken name is never replaced: ErrExist, both entries unchanged. On a
// case-insensitive device a name differing only in letter case is taken,
// the entry's own included.
func TestRenameNeverReplaces(t *testing.T) {
	const op = "RenameNoReplace"
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.File("a.jpg", 1, synthfs.DefaultModTime)
	root.File("b.jpg", 2, synthfs.DefaultModTime)
	root.File("FOTO.JPG", 3, synthfs.DefaultModTime)
	d := open(t, fsys, "/src")
	w := writer(t, d)
	a, b := lstat(t, d, "a.jpg"), lstat(t, d, "b.jpg")

	wantWriteErr(t, w.RenameNoReplace([]byte("a.jpg"), d, []byte("b.jpg")), op, fsaccess.ErrExist, "")
	wantWriteErr(t, w.RenameNoReplace([]byte("a.jpg"), d, []byte("a.jpg")), op, fsaccess.ErrExist, "")
	if got := lstat(t, d, "a.jpg"); got.Ino != a.Ino || got.Size != 1 {
		t.Errorf("a.jpg after the refusal = %+v", got)
	}
	if got := lstat(t, d, "b.jpg"); got.Ino != b.Ino || got.Size != 2 {
		t.Errorf("b.jpg after the refusal = %+v", got)
	}
	// A case-sensitive device renames a case change.
	must(t, w.RenameNoReplace([]byte("FOTO.JPG"), d, []byte("foto.jpg")))

	fat := fsys.Root("/card")
	fat.File("FOTO.JPG", 3, synthfs.DefaultModTime)
	fat.File("outra.jpg", 3, synthfs.DefaultModTime)
	card := open(t, fsys, "/card")
	fsys.SetCapabilities(card.Self().Dev, fatCaps)
	for _, name := range []string{"foto.jpg", "OUTRA.JPG"} {
		wantWriteErr(t, writer(t, card).RenameNoReplace([]byte("FOTO.JPG"), card, []byte(name)), op, fsaccess.ErrExist, "")
	}
	if fat.Child("FOTO.JPG") == nil {
		t.Error("a refused case change renamed the file")
	}
}

// Refusals in the kernel's order, each changing nothing.
func TestRenameRefusals(t *testing.T) {
	const op = "RenameNoReplace"
	fsys := synthfs.New()
	root := fsys.Root("/src")
	fotos := root.Dir("Fotos")
	inner := fotos.Dir("2007")
	root.File("a.jpg", 1, synthfs.DefaultModTime)
	root.Dir("mnt").MountPoint()
	fsys.Root("/other")
	d := open(t, fsys, "/src")
	w := writer(t, d)
	fotosDir := openDir(t, d, "Fotos")
	innerDir := openDir(t, fotosDir, "2007")
	other := open(t, fsys, "/other")
	foreignFS := synthfs.New()
	foreignFS.Root("/src")
	foreign := open(t, foreignFS, "/src")

	wantWriteErr(t, w.RenameNoReplace([]byte("Fotos"), fotosDir, []byte("x")), op, fsaccess.ErrIntoItself, "")
	wantWriteErr(t, w.RenameNoReplace([]byte("Fotos"), innerDir, []byte("x")), op, fsaccess.ErrIntoItself, "")
	wantWriteErr(t, w.RenameNoReplace([]byte("a.jpg"), other, []byte("a.jpg")), op, fsaccess.ErrCrossDevice, "")
	wantWriteErr(t, w.RenameNoReplace([]byte("nada"), d, []byte("x")), op, syscall.ENOENT, domain.OutcomeAbsent)
	wantWriteErr(t, w.RenameNoReplace([]byte("mnt"), d, []byte("x")), op, syscall.EBUSY, domain.OutcomeUnavailable)
	// Invalid names and a Dir of another FS are refused before anything.
	wantWriteErr(t, w.RenameNoReplace([]byte("a/b"), d, []byte("x")), op, fsaccess.ErrInvalidName, "")
	wantWriteErr(t, w.RenameNoReplace([]byte("a.jpg"), d, []byte("..")), op, fsaccess.ErrInvalidName, "")
	wantWriteErr(t, w.RenameNoReplace([]byte("a.jpg"), foreign, []byte("b")), op, nil, "")
	if inner.Child("x") != nil || fotos.Child("x") != nil || root.Child("a.jpg") == nil || root.Child("x") != nil {
		t.Fatal("a refused rename changed the tree")
	}

	// A device whose capabilities lack the no-replace rename refuses after
	// the existence check, as an old driver does.
	noFlag := ext4Caps
	noFlag.NoReplaceRename = false
	fsys.SetCapabilities(d.Self().Dev, noFlag)
	wantWriteErr(t, w.RenameNoReplace([]byte("a.jpg"), d, []byte("Fotos")), op, fsaccess.ErrExist, "")
	wantWriteErr(t, w.RenameNoReplace([]byte("a.jpg"), d, []byte("b.jpg")), op, fsaccess.ErrNoReplaceUnsupported, "")
	if root.Child("b.jpg") != nil {
		t.Fatal("a rename without the no-replace flag moved the file")
	}
	fsys.SetCapabilities(d.Self().Dev, ext4Caps)
	must(t, w.RenameNoReplace([]byte("a.jpg"), d, []byte("b.jpg")))

	// A read-only device, by its capabilities or its FSInfo, refuses every
	// write.
	ro := ext4Caps
	ro.ReadOnly = true
	fsys.SetCapabilities(d.Self().Dev, ro)
	wantWriteErr(t, w.RenameNoReplace([]byte("b.jpg"), d, []byte("c.jpg")), op, fsaccess.ErrReadOnly, "")
	wantWriteErr(t, w.Mkdir([]byte("novo")), "Mkdir", fsaccess.ErrReadOnly, "")
	wantWriteErr(t, w.Rmdir([]byte("mnt")), "Rmdir", fsaccess.ErrReadOnly, "")
	fsys.SetCapabilities(d.Self().Dev, ext4Caps)
	fsys.SetFSInfo(d.Self().Dev, fsaccess.FSInfo{ReadOnly: true})
	wantWriteErr(t, w.Mkdir([]byte("novo")), "Mkdir", fsaccess.ErrReadOnly, "")
}

// Mkdir creates an empty folder with its parent's permission bits and a new
// inode, refuses a taken name (generated ones included), and advances the
// parent's times.
func TestMkdir(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.Dir("privado").Perm(0o750)
	root.Generated("gerado", 30, 5)
	root.File("a.jpg", 1, synthfs.DefaultModTime)
	d := open(t, fsys, "/src")
	priv := openDir(t, d, "privado")
	privBefore := lstat(t, d, "privado")

	must(t, writer(t, priv).Mkdir([]byte("Novo")))
	got := lstat(t, priv, "Novo")
	if got.Kind != domain.EntryDirectory || got.Mode.Perm() != 0o750 || got.Ino == privBefore.Ino {
		t.Errorf("new folder = %+v, want a folder with mode 0750 and its own inode", got)
	}
	if entries := listAll(t, openDir(t, priv, "Novo")); len(entries) != 0 {
		t.Errorf("new folder lists %v", entries)
	}
	if now := lstat(t, d, "privado"); !now.ModTime.After(privBefore.ModTime) || !now.Ctime.After(privBefore.Ctime) {
		t.Errorf("parent after Mkdir = %+v, want later times than %+v", now, privBefore)
	}
	must(t, writer(t, d).Mkdir([]byte("raiz")))
	if got := lstat(t, d, "raiz").Mode.Perm(); got != 0o755 {
		t.Errorf("folder made in a default folder has mode %v, want 0755", got)
	}

	gen := openDir(t, d, "gerado")
	for _, tc := range []struct {
		d    fsaccess.Dir
		name string
	}{{priv, "Novo"}, {d, "a.jpg"}, {d, "privado"}, {gen, "dir-0000000"}} {
		wantWriteErr(t, writer(t, tc.d).Mkdir([]byte(tc.name)), "Mkdir", fsaccess.ErrExist, "")
	}
	// Generated folders cannot be changed; explicit ones holding generated
	// entries can.
	must(t, writer(t, gen).Mkdir([]byte("x")))
	wantWriteErr(t, writer(t, openDir(t, gen, "dir-0000000")).Mkdir([]byte("x")), "Mkdir", nil, domain.OutcomeUnavailable)
	wantWriteErr(t, writer(t, d).Mkdir([]byte("")), "Mkdir", fsaccess.ErrInvalidName, "")
}

// Rmdir removes only an empty folder.
func TestRmdir(t *testing.T) {
	const op = "Rmdir"
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.Dir("vazio")
	root.Dir("cheio").File("a.jpg", 1, synthfs.DefaultModTime)
	root.Generated("gerado", 2, 10)
	root.File("b.jpg", 1, synthfs.DefaultModTime)
	d := open(t, fsys, "/src")
	w := writer(t, d)
	before := d.Self()

	must(t, w.Rmdir([]byte("vazio")))
	absent(t, d, "vazio")
	if now := root.Info(); !now.ModTime.After(before.ModTime) {
		t.Errorf("parent after Rmdir = %+v, want a later modification time than %+v", now, before)
	}
	wantWriteErr(t, w.Rmdir([]byte("vazio")), op, syscall.ENOENT, domain.OutcomeAbsent)
	wantWriteErr(t, w.Rmdir([]byte("cheio")), op, fsaccess.ErrNotEmpty, "")
	wantWriteErr(t, w.Rmdir([]byte("gerado")), op, fsaccess.ErrNotEmpty, "")
	wantWriteErr(t, w.Rmdir([]byte("b.jpg")), op, syscall.ENOTDIR, domain.OutcomeUnavailable)
	if root.Child("cheio") == nil || root.Child("gerado") == nil || root.Child("b.jpg") == nil {
		t.Error("a refused Rmdir removed an entry")
	}
}

// Writes into a folder that was removed or replaced find nothing, a closed
// handle or a vanished root is unavailable, and Sync checks the handle only.
func TestWritesOnStaleHandles(t *testing.T) {
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.Dir("velho")
	root.File("a.jpg", 1, synthfs.DefaultModTime)
	d := open(t, fsys, "/src")
	old := openDir(t, d, "velho")
	must(t, writer(t, old).Sync())

	root.Remove("velho")
	root.Dir("velho")
	wantWriteErr(t, writer(t, old).Mkdir([]byte("x")), "Mkdir", syscall.ENOENT, domain.OutcomeAbsent)
	wantWriteErr(t, writer(t, old).CreateExclusive([]byte("x"), nil), "CreateExclusive", syscall.ENOENT, domain.OutcomeAbsent)
	wantWriteErr(t, writer(t, old).Unlink([]byte("x")), "Unlink", syscall.ENOENT, domain.OutcomeAbsent)
	wantWriteErr(t, writer(t, d).RenameNoReplace([]byte("a.jpg"), old, []byte("a.jpg")), "RenameNoReplace",
		syscall.ENOENT, domain.OutcomeAbsent)
	if root.Child("a.jpg") == nil {
		t.Fatal("a rename into a removed folder moved the file")
	}

	must(t, old.Close())
	wantWriteErr(t, writer(t, old).Sync(), "Sync", nil, domain.OutcomeUnavailable)
	fsys.Vanish("/src")
	wantWriteErr(t, writer(t, d).Mkdir([]byte("x")), "Mkdir", nil, domain.OutcomeUnavailable)
	wantWriteErr(t, writer(t, d).Unlink([]byte("a.jpg")), "Unlink", nil, domain.OutcomeUnavailable)
	wantWriteErr(t, writer(t, d).Sync(), "Sync", nil, domain.OutcomeUnavailable)
}

// r4 task 1.3, design D12: CreateExclusive writes a new file holding exactly
// the data, with its parent's permission bits & 0666, a new inode, and later
// parent times; a taken name, whatever holds it, fails with ErrExist and
// leaves the entry as it was.
func TestCreateExclusive(t *testing.T) {
	const op = "CreateExclusive"
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.Dir("privado").Perm(0o750)
	root.File("a.jpg", 8, synthfs.DefaultModTime).Content([]byte("ORIGINAL"))
	root.Symlink("link", "nowhere")
	root.Generated("gerado", 30, 5)
	d := open(t, fsys, "/src")
	priv := openDir(t, d, "privado")
	privBefore := lstat(t, d, "privado")

	data := []byte("DADOS")
	must(t, writer(t, priv).CreateExclusive([]byte("novo.bin"), data))
	data[0] = 'X' // the file holds a copy
	got := lstat(t, priv, "novo.bin")
	if got.Kind != domain.EntryFile || got.Size != 5 || got.Mode.Perm() != 0o640 || got.Ino == privBefore.Ino {
		t.Errorf("new file = %+v, want a 5-byte file with mode 0640 and its own inode", got)
	}
	if b := readAll(t, openFile(t, priv, "novo.bin")); string(b) != "DADOS" {
		t.Errorf("new file holds %q, want %q", b, "DADOS")
	}
	if now := lstat(t, d, "privado"); !now.ModTime.After(privBefore.ModTime) || !now.Ctime.After(privBefore.Ctime) {
		t.Errorf("parent after CreateExclusive = %+v, want later times than %+v", now, privBefore)
	}
	must(t, writer(t, d).CreateExclusive([]byte("vazio"), nil))
	if got := lstat(t, d, "vazio"); got.Size != 0 || got.Mode.Perm() != 0o644 {
		t.Errorf("empty file made in a default folder = %+v, want 0 bytes with mode 0644", got)
	}

	before := lstat(t, d, "a.jpg")
	gen := openDir(t, d, "gerado")
	for _, tc := range []struct {
		d    fsaccess.Dir
		name string
	}{{d, "a.jpg"}, {d, "privado"}, {d, "link"}, {priv, "novo.bin"}, {gen, "dir-0000000"}} {
		wantWriteErr(t, writer(t, tc.d).CreateExclusive([]byte(tc.name), []byte("NOVO")), op, fsaccess.ErrExist, "")
	}
	if after := lstat(t, d, "a.jpg"); after.Ino != before.Ino || after.Size != before.Size || !after.Ctime.Equal(before.Ctime) {
		t.Errorf("a.jpg after a refused create = %+v, want %+v", after, before)
	}
	if b := readAll(t, openFile(t, d, "a.jpg")); string(b) != "ORIGINAL" {
		t.Errorf("a.jpg holds %q after a refused create, want ORIGINAL", b)
	}
	if b := readAll(t, openFile(t, priv, "novo.bin")); string(b) != "DADOS" {
		t.Errorf("novo.bin holds %q after a refused create, want DADOS", b)
	}
	wantWriteErr(t, writer(t, openDir(t, gen, "dir-0000000")).CreateExclusive([]byte("x"), nil), op, nil,
		domain.OutcomeUnavailable)
	for _, bad := range []string{"", ".", "..", "a/b", "a\x00b"} {
		wantWriteErr(t, writer(t, d).CreateExclusive([]byte(bad), nil), op, fsaccess.ErrInvalidName, "")
	}

	ro := ext4Caps
	ro.ReadOnly = true
	fsys.SetCapabilities(d.Self().Dev, ro)
	wantWriteErr(t, writer(t, d).CreateExclusive([]byte("novo"), []byte("x")), op, fsaccess.ErrReadOnly, "")
	absent(t, d, "novo")
}

// r4 task 1.3, design D12: Unlink removes a file or a symlink and advances
// the parent's times; a folder, empty or not, fails with ErrIsDir and stays;
// a missing name is absent; a hard link's other name reports one link fewer.
func TestUnlink(t *testing.T) {
	const op = "Unlink"
	fsys := synthfs.New()
	root := fsys.Root("/src")
	root.File("a.jpg", 1, synthfs.DefaultModTime)
	root.Dir("cheia").File("b.jpg", 1, synthfs.DefaultModTime)
	root.Dir("vazia")
	root.Symlink("link", "a.jpg")
	orig := root.File("orig.jpg", 4, synthfs.DefaultModTime).Content([]byte("ORIG"))
	root.HardLink("copia.jpg", orig)
	root.Generated("gerado", 30, 5)
	root.Generated("plano", 4, 10)
	d := open(t, fsys, "/src")
	w := writer(t, d)
	before := root.Info()

	must(t, w.Unlink([]byte("a.jpg")))
	absent(t, d, "a.jpg")
	if now := root.Info(); !now.ModTime.After(before.ModTime) || !now.Ctime.After(before.Ctime) {
		t.Errorf("parent after Unlink = %+v, want later times than %+v", now, before)
	}
	must(t, w.Unlink([]byte("link")))
	absent(t, d, "link")

	for _, name := range []string{"cheia", "vazia"} {
		wantWriteErr(t, w.Unlink([]byte(name)), op, fsaccess.ErrIsDir, "")
		if got := lstat(t, d, name); got.Kind != domain.EntryDirectory {
			t.Errorf("%s after a refused Unlink = %+v", name, got)
		}
	}
	if root.Child("cheia").Child("b.jpg") == nil {
		t.Error("a refused Unlink changed the folder")
	}
	wantWriteErr(t, w.Unlink([]byte("a.jpg")), op, syscall.ENOENT, domain.OutcomeAbsent)

	if n := lstat(t, d, "orig.jpg").Nlink; n != 2 {
		t.Fatalf("orig.jpg has %d links before, want 2", n)
	}
	must(t, w.Unlink([]byte("orig.jpg")))
	absent(t, d, "orig.jpg")
	if got := lstat(t, d, "copia.jpg"); got.Nlink != 1 || got.Size != 4 {
		t.Errorf("copia.jpg after unlinking its other name = %+v, want 1 link and 4 bytes", got)
	}
	if b := readAll(t, openFile(t, d, "copia.jpg")); string(b) != "ORIG" {
		t.Errorf("copia.jpg holds %q, want ORIG", b)
	}

	gen := openDir(t, d, "gerado")
	plano := openDir(t, d, "plano")
	var genFile string
	for _, e := range listAll(t, plano) {
		if e.Kind == domain.EntryFile {
			genFile = string(e.Name)
			break
		}
	}
	if genFile == "" {
		t.Fatal("plano lists no generated file")
	}
	wantWriteErr(t, writer(t, plano).Unlink([]byte(genFile)), op, nil, domain.OutcomeUnavailable)
	wantWriteErr(t, writer(t, gen).Unlink([]byte("dir-0000000")), op, fsaccess.ErrIsDir, "")
	for _, bad := range []string{"", ".", "..", "a/b", "a\x00b"} {
		wantWriteErr(t, w.Unlink([]byte(bad)), op, fsaccess.ErrInvalidName, "")
	}

	ro := ext4Caps
	ro.ReadOnly = true
	fsys.SetCapabilities(d.Self().Dev, ro)
	wantWriteErr(t, w.Unlink([]byte("copia.jpg")), op, fsaccess.ErrReadOnly, "")
	lstat(t, d, "copia.jpg")
}
