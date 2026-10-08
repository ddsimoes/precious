//go:build e2e && linux

package fsaccess_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// r3 task 1.5: the Linux Writer on real directories.

func linuxWriter(t *testing.T, d fsaccess.Dir) fsaccess.Writer {
	t.Helper()
	w, ok := fsaccess.AsWriter(d)
	if !ok {
		t.Fatalf("%T has no Writer", d)
	}
	return w
}

func openChild(t *testing.T, parent fsaccess.Dir, name string) fsaccess.Dir {
	t.Helper()
	d, err := parent.OpenDir([]byte(name), mustLstat(t, parent, name))
	if err != nil {
		t.Fatalf("OpenDir(%s): %v", name, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Renaming onto a taken name fails with ErrExist and leaves both files; a
// free name renames, keeping the inode, within a folder and across folders;
// a folder moved into itself fails with ErrIntoItself, not as a missing
// no-replace flag.
func TestWriterRenameNeverReplaces(t *testing.T) {
	src := t.TempDir()
	writeText(t, filepath.Join(src, "a.txt"), "A")
	writeText(t, filepath.Join(src, "b.txt"), "B")
	mkdir(t, filepath.Join(src, "Fotos"))
	mkdir(t, filepath.Join(src, "Fotos", "2007"))
	d := openRoot(t, fsaccess.NewOS(), src)
	w := linuxWriter(t, d)

	err := w.RenameNoReplace([]byte("a.txt"), d, []byte("b.txt"))
	wantErr(t, err, "", fsaccess.ErrExist)
	if !errors.Is(err, syscall.EEXIST) {
		t.Errorf("err = %v, want it to carry EEXIST", err)
	}
	if a, b := readText(t, filepath.Join(src, "a.txt")), readText(t, filepath.Join(src, "b.txt")); a != "A" || b != "B" {
		t.Fatalf("after the refused rename a.txt = %q, b.txt = %q", a, b)
	}

	before := mustLstat(t, d, "a.txt")
	if err := w.RenameNoReplace([]byte("a.txt"), d, []byte("c.txt")); err != nil {
		t.Fatal(err)
	}
	fotos := openChild(t, d, "Fotos")
	if err := w.RenameNoReplace([]byte("c.txt"), fotos, []byte("a.txt")); err != nil {
		t.Fatal(err)
	}
	if after := mustLstat(t, fotos, "a.txt"); after.Ino != before.Ino || after.Dev != before.Dev {
		t.Errorf("moved file = %+v, want the inode of %+v", after, before)
	}
	if _, err := d.Lstat([]byte("c.txt")); err == nil {
		t.Error("c.txt still exists after the move")
	}

	inner := openChild(t, fotos, "2007")
	for _, to := range []fsaccess.Dir{fotos, inner} {
		wantErr(t, w.RenameNoReplace([]byte("Fotos"), to, []byte("x")), "", fsaccess.ErrIntoItself)
	}
	wantErr(t, w.RenameNoReplace([]byte("nada"), d, []byte("x")), domain.OutcomeAbsent, syscall.ENOENT)
	for _, dir := range []fsaccess.Dir{d, fotos} {
		if err := linuxWriter(t, dir).Sync(); err != nil {
			t.Errorf("Sync: %v", err)
		}
	}
}

// Rmdir removes an empty folder and refuses a non-empty one with
// ErrNotEmpty, leaving it.
func TestWriterRmdirNotEmpty(t *testing.T) {
	src := t.TempDir()
	mkdir(t, filepath.Join(src, "vazio"))
	mkdir(t, filepath.Join(src, "cheio"))
	writeText(t, filepath.Join(src, "cheio", "a.txt"), "A")
	writeText(t, filepath.Join(src, "b.txt"), "B")
	d := openRoot(t, fsaccess.NewOS(), src)
	w := linuxWriter(t, d)

	wantErr(t, w.Rmdir([]byte("cheio")), "", fsaccess.ErrNotEmpty)
	if readText(t, filepath.Join(src, "cheio", "a.txt")) != "A" {
		t.Fatal("the refused Rmdir changed the folder")
	}
	if err := w.Rmdir([]byte("vazio")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(src, "vazio")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("vazio after Rmdir: %v", err)
	}
	wantErr(t, w.Rmdir([]byte("vazio")), domain.OutcomeAbsent, syscall.ENOENT)
	wantErr(t, w.Rmdir([]byte("b.txt")), domain.OutcomeUnavailable, syscall.ENOTDIR)
}

// A folder made under umask 0077 inside a 0755 parent ends 0755, and one
// inside a setgid 02775 parent ends 02775; a taken name is ErrExist.
func TestWriterMkdirUnderUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	src := t.TempDir()
	for name, mode := range map[string]os.FileMode{"publico": 0o755, "grupo": 0o775 | os.ModeSetgid} {
		p := filepath.Join(src, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	d := openRoot(t, fsaccess.NewOS(), src)
	for name, want := range map[string]uint32{"publico": 0o755, "grupo": 0o2775} {
		parent := openChild(t, d, name)
		if err := linuxWriter(t, parent).Mkdir([]byte("Novo")); err != nil {
			t.Fatal(err)
		}
		var st unix.Stat_t
		if err := unix.Lstat(filepath.Join(src, name, "Novo"), &st); err != nil {
			t.Fatal(err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0o7777 != want {
			t.Errorf("folder made in %s has mode %o, want a folder with %o", name, st.Mode&0o7777, want)
		}
		wantErr(t, linuxWriter(t, parent).Mkdir([]byte("Novo")), "", fsaccess.ErrExist)
	}
}

// Renaming across a mount fails with ErrCrossDevice and leaves the file;
// writes on a read-only mount fail with ErrReadOnly.
func TestWriterAcrossMountsAndReadOnly(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := t.TempDir()
		writeText(t, filepath.Join(src, "a.txt"), "A")
		other, ro := filepath.Join(src, "outro"), filepath.Join(src, "somente-leitura")
		mkdir(t, other)
		mkdir(t, ro)
		mount(t, "tmpfs", other, "tmpfs", 0, "")
		mount(t, "tmpfs", ro, "tmpfs", 0, "")
		mkdir(t, filepath.Join(ro, "vazio"))
		if err := unix.Mount("", ro, "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
			t.Fatalf("remount %s read-only: %v", ro, err)
		}

		fsys := fsaccess.NewOS()
		d := openRoot(t, fsys, src)
		otherDir := openRoot(t, fsys, other)
		wantErr(t, linuxWriter(t, d).RenameNoReplace([]byte("a.txt"), otherDir, []byte("a.txt")), "", fsaccess.ErrCrossDevice)
		if readText(t, filepath.Join(src, "a.txt")) != "A" {
			t.Fatal("the refused rename changed the file")
		}
		if _, err := os.Lstat(filepath.Join(other, "a.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a.txt on the other mount: %v", err)
		}

		roDir := linuxWriter(t, openRoot(t, fsys, ro))
		wantErr(t, roDir.Mkdir([]byte("novo")), "", fsaccess.ErrReadOnly)
		wantErr(t, roDir.Rmdir([]byte("vazio")), "", fsaccess.ErrReadOnly)
		wantErr(t, roDir.RenameNoReplace([]byte("vazio"), openRoot(t, fsys, ro), []byte("outro")), "", fsaccess.ErrReadOnly)
	})
}
