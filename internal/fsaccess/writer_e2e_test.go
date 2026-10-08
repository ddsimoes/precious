//go:build e2e && linux

package fsaccess_test

import (
	"bytes"
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

// r4 task 1.3, design D12: CreateExclusive and Unlink on a fresh tmpfs.

// tmpfsDir mounts a tmpfs with the mount data opts on a new folder and
// returns its path. Call it inside inUserNamespace.
func tmpfsDir(t *testing.T, opts string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tmpfs")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	mount(t, "tmpfs", p, "tmpfs", 0, opts)
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Creating onto a taken name (a file, a folder, a dangling symlink) fails
// with ErrExist and leaves the entry as it was; nothing follows the link.
func TestWriterCreateExclusiveNeverReplaces(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := tmpfsDir(t, "")
		writeText(t, filepath.Join(src, "a.txt"), "A")
		mkdir(t, filepath.Join(src, "pasta"))
		if err := os.Symlink("nada", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		w := linuxWriter(t, openRoot(t, fsaccess.NewOS(), src))
		for _, name := range []string{"a.txt", "pasta", "link"} {
			err := w.CreateExclusive([]byte(name), []byte("NOVO"))
			wantErr(t, err, "", fsaccess.ErrExist)
			if !errors.Is(err, syscall.EEXIST) {
				t.Errorf("CreateExclusive(%s) = %v, want it to carry EEXIST", name, err)
			}
		}
		if got := readText(t, filepath.Join(src, "a.txt")); got != "A" {
			t.Errorf("a.txt after the refused create = %q, want A", got)
		}
		if entries, err := os.ReadDir(filepath.Join(src, "pasta")); err != nil || len(entries) != 0 {
			t.Errorf("pasta after the refused create = %v, %v; want an empty folder", entries, err)
		}
		if target, err := os.Readlink(filepath.Join(src, "link")); err != nil || target != "nada" {
			t.Errorf("link after the refused create = %q, %v; want nada", target, err)
		}
		if _, err := os.Lstat(filepath.Join(src, "nada")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the dangling link's target after the refused create: %v", err)
		}
		for _, bad := range []string{"", ".", "..", "a/b", "a\x00b"} {
			checkInvalid(t, w.CreateExclusive([]byte(bad), nil), "CreateExclusive", bad)
		}
	})
}

// A created file is complete and synced, and has its parent's mode & 0666
// under umask 0077; an empty file is created too.
func TestWriterCreateExclusiveComplete(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		old := syscall.Umask(0o077)
		defer syscall.Umask(old)
		src := tmpfsDir(t, "")
		parents := []struct {
			name       string
			mode, want uint32
		}{
			{"privado", 0o750, 0o640},
			{"leitura", 0o644, 0o644},
			{"aberto", 0o777, 0o666},
			{"grupo", 0o2775, 0o664},
		}
		for _, p := range parents {
			mkdir(t, filepath.Join(src, p.name))
			if err := unix.Chmod(filepath.Join(src, p.name), p.mode); err != nil {
				t.Fatal(err)
			}
		}
		d := openRoot(t, fsaccess.NewOS(), src)
		data := pattern(3<<20 + 7)
		for _, p := range parents {
			parent := openChild(t, d, p.name)
			w := linuxWriter(t, parent)
			if err := w.CreateExclusive([]byte("arquivo.bin"), data); err != nil {
				t.Fatalf("CreateExclusive in %s: %v", p.name, err)
			}
			if err := w.Sync(); err != nil {
				t.Errorf("Sync of %s: %v", p.name, err)
			}
			path := filepath.Join(src, p.name, "arquivo.bin")
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
				t.Errorf("%s holds %d bytes (%v), want the %d written", path, len(got), err, len(data))
			}
			var st unix.Stat_t
			if err := unix.Lstat(path, &st); err != nil {
				t.Fatal(err)
			}
			if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o7777 != p.want || st.Nlink != 1 {
				t.Errorf("file made in %s (mode %o) has mode %o and %d links, want a regular file with %o and 1",
					p.name, p.mode, st.Mode&0o7777, st.Nlink, p.want)
			}
		}
		w := linuxWriter(t, d)
		if err := w.CreateExclusive([]byte("vazio"), nil); err != nil {
			t.Fatal(err)
		}
		if got := mustLstat(t, d, "vazio"); got.Kind != domain.EntryFile || got.Size != 0 || got.Mode.Perm() != 0o644 {
			t.Errorf("empty file = %+v, want an empty 0644 file", got)
		}
	})
}

// A create that runs out of space mid-write fails and leaves no partial
// file behind.
func TestWriterCreateExclusiveRemovesPartial(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := tmpfsDir(t, "size=64k")
		d := openRoot(t, fsaccess.NewOS(), src)
		wantErr(t, linuxWriter(t, d).CreateExclusive([]byte("grande"), pattern(1<<20)), domain.OutcomeUnavailable, syscall.ENOSPC)
		if _, err := os.Lstat(filepath.Join(src, "grande")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("grande after the failed create: %v", err)
		}
	})
}

// Unlink removes a file, and a symlink without its target; a folder, empty
// or not, fails with ErrIsDir and stays; a missing name is absent.
func TestWriterUnlink(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := tmpfsDir(t, "")
		writeText(t, filepath.Join(src, "a.txt"), "A")
		writeText(t, filepath.Join(src, "b.txt"), "B")
		if err := os.Symlink("b.txt", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		mkdir(t, filepath.Join(src, "vazia"))
		mkdir(t, filepath.Join(src, "cheia"))
		writeText(t, filepath.Join(src, "cheia", "c.txt"), "C")
		w := linuxWriter(t, openRoot(t, fsaccess.NewOS(), src))

		for _, name := range []string{"a.txt", "link"} {
			if err := w.Unlink([]byte(name)); err != nil {
				t.Fatalf("Unlink(%s): %v", name, err)
			}
			if _, err := os.Lstat(filepath.Join(src, name)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s after Unlink: %v", name, err)
			}
		}
		if readText(t, filepath.Join(src, "b.txt")) != "B" {
			t.Error("unlinking the symlink changed its target")
		}
		for _, name := range []string{"vazia", "cheia"} {
			err := w.Unlink([]byte(name))
			wantErr(t, err, "", fsaccess.ErrIsDir)
			if !errors.Is(err, syscall.EISDIR) {
				t.Errorf("Unlink(%s) = %v, want it to carry EISDIR", name, err)
			}
			if fi, err := os.Lstat(filepath.Join(src, name)); err != nil || !fi.IsDir() {
				t.Errorf("%s after the refused Unlink: %v, %v", name, fi, err)
			}
		}
		if readText(t, filepath.Join(src, "cheia", "c.txt")) != "C" {
			t.Error("the refused Unlink changed the folder")
		}
		wantErr(t, w.Unlink([]byte("a.txt")), domain.OutcomeAbsent, syscall.ENOENT)
		for _, bad := range []string{"", ".", "..", "cheia/c.txt", "a\x00b"} {
			checkInvalid(t, w.Unlink([]byte(bad)), "Unlink", bad)
		}
	})
}

// Creating and unlinking on a read-only remount fail with ErrReadOnly and
// change nothing.
func TestWriterCreateUnlinkReadOnly(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		ro := tmpfsDir(t, "")
		writeText(t, filepath.Join(ro, "a.txt"), "A")
		if err := unix.Mount("", ro, "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
			t.Fatalf("remount %s read-only: %v", ro, err)
		}
		w := linuxWriter(t, openRoot(t, fsaccess.NewOS(), ro))
		for _, err := range []error{w.CreateExclusive([]byte("novo"), []byte("x")), w.Unlink([]byte("a.txt"))} {
			wantErr(t, err, "", fsaccess.ErrReadOnly)
			if !errors.Is(err, syscall.EROFS) {
				t.Errorf("err = %v, want it to carry EROFS", err)
			}
		}
		if readText(t, filepath.Join(ro, "a.txt")) != "A" {
			t.Error("the refused Unlink changed a.txt")
		}
		if _, err := os.Lstat(filepath.Join(ro, "novo")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("novo after the refused create: %v", err)
		}
	})
}
