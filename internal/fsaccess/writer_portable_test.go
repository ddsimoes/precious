package fsaccess_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"precious/internal/fsaccess"
)

// r3 task 1.5: the portable backend has no no-replace rename, so every
// Writer method fails with ErrNoReplaceUnsupported and changes nothing;
// invalid names are still refused as such.
func TestPortableWriterRefusesEverything(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2005, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(src, "a.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "vazio"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := fsaccess.NewPortable().OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	w, ok := fsaccess.AsWriter(d)
	if !ok {
		t.Fatal("portable Dir has no Writer")
	}
	for name, err := range map[string]error{
		"RenameNoReplace": w.RenameNoReplace([]byte("a.txt"), d, []byte("b.txt")),
		"Mkdir":           w.Mkdir([]byte("novo")),
		"Rmdir":           w.Rmdir([]byte("vazio")),
		"Sync":            w.Sync(),
		"CreateExclusive": w.CreateExclusive([]byte("c.txt"), []byte("C")),
		"Unlink":          w.Unlink([]byte("a.txt")),
		"SetModTime":      w.SetModTime([]byte("a.txt"), time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)),
	} {
		var e *fsaccess.Error
		if !errors.As(err, &e) || e.Op != name || e.Outcome != "" || !errors.Is(err, fsaccess.ErrNoReplaceUnsupported) {
			t.Errorf("%s = %v, want ErrNoReplaceUnsupported", name, err)
		}
	}
	if err := w.Mkdir([]byte("..")); !errors.Is(err, fsaccess.ErrInvalidName) {
		t.Errorf("Mkdir(..) = %v, want ErrInvalidName", err)
	}
	if err := w.CreateExclusive([]byte("a/b"), nil); !errors.Is(err, fsaccess.ErrInvalidName) {
		t.Errorf("CreateExclusive(a/b) = %v, want ErrInvalidName", err)
	}
	if err := w.Unlink([]byte(".")); !errors.Is(err, fsaccess.ErrInvalidName) {
		t.Errorf("Unlink(.) = %v, want ErrInvalidName", err)
	}
	if err := w.SetModTime([]byte("a\x00b"), old); !errors.Is(err, fsaccess.ErrInvalidName) {
		t.Errorf("SetModTime(a NUL b) = %v, want ErrInvalidName", err)
	}
	if fi, err := os.Lstat(filepath.Join(src, "a.txt")); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("a.txt after the refused SetModTime: %v, %v; want modified at %v", fi, err, old)
	}
	for _, name := range []string{"a.txt", "vazio"} {
		if _, err := os.Lstat(filepath.Join(src, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"b.txt", "novo", "c.txt"} {
		if _, err := os.Lstat(filepath.Join(src, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a refused write: %v", name, err)
		}
	}
}
