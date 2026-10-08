package fsaccess_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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
