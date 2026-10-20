//go:build e2e && linux

package fsaccess_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// r5 task 1.2, design D12: SetModTime on a fresh tmpfs, through the Linux
// backend's utimensat.

// lstatRaw is lstat(2) of path.
func lstatRaw(t *testing.T, path string) unix.Stat_t {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// setTimes sets path's own access and modification times (not a symlink's
// target's).
func setTimes(t *testing.T, path string, atime, mtime time.Time) {
	t.Helper()
	ts := []unix.Timespec{unix.NsecToTimespec(atime.UnixNano()), unix.NsecToTimespec(mtime.UnixNano())}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
}

// waitPastCoarseClock waits until the kernel's coarse clock, the floor of
// every change time it stamps, is past ts, so the next change gets a later
// change time than ts.
func waitPastCoarseClock(t *testing.T, ts unix.Timespec) {
	t.Helper()
	for {
		var now unix.Timespec
		if err := unix.ClockGettime(unix.CLOCK_REALTIME_COARSE, &now); err != nil {
			t.Fatal(err)
		}
		if now.Nano() > ts.Nano() {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func wantMtime(t *testing.T, path string, want time.Time) {
	t.Helper()
	if got := lstatRaw(t, path).Mtim; got.Nano() != want.UnixNano() {
		t.Errorf("%s modified at %v, want %v", filepath.Base(path), time.Unix(0, got.Nano()).UTC(), want)
	}
}

// The time is set to the nanosecond, the access time and the bytes stay, and
// the change time advances; a symlink gets its own time, never its target's,
// dangling or not; an absent name is absent, and invalid names are refused.
func TestWriterSetModTime(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		src := tmpfsDir(t, "")
		a, b := filepath.Join(src, "a.jpg"), filepath.Join(src, "b.jpg")
		data := pattern(64<<10 + 3)
		if err := os.WriteFile(a, data, 0o644); err != nil {
			t.Fatal(err)
		}
		writeText(t, b, "B")
		for link, target := range map[string]string{"link": "b.jpg", "solto": "nada"} {
			if err := os.Symlink(target, filepath.Join(src, link)); err != nil {
				t.Fatal(err)
			}
		}
		atime := time.Date(2001, 2, 3, 4, 5, 6, 7, time.UTC)
		old := time.Date(2002, 3, 4, 5, 6, 7, 8, time.UTC)
		for _, p := range []string{a, b, filepath.Join(src, "link"), filepath.Join(src, "solto")} {
			setTimes(t, p, atime, old)
		}
		d := openRoot(t, fsaccess.NewOS(), src)
		w := linuxWriter(t, d)

		before := lstatRaw(t, a)
		waitPastCoarseClock(t, before.Ctim)
		when := time.Date(2010, 7, 17, 10, 0, 0, 123_456_789, time.UTC)
		if err := w.SetModTime([]byte("a.jpg"), when); err != nil {
			t.Fatalf("SetModTime(a.jpg): %v", err)
		}
		after := lstatRaw(t, a)
		if after.Mtim.Nano() != when.UnixNano() {
			t.Errorf("a.jpg modified at %v, want %v", time.Unix(0, after.Mtim.Nano()).UTC(), when)
		}
		if after.Atim.Nano() != atime.UnixNano() {
			t.Errorf("a.jpg accessed at %v after SetModTime, want %v unchanged", time.Unix(0, after.Atim.Nano()).UTC(), atime)
		}
		if after.Ctim.Nano() <= before.Ctim.Nano() {
			t.Errorf("a.jpg change time %d did not advance past %d", after.Ctim.Nano(), before.Ctim.Nano())
		}
		if after.Ino != before.Ino || after.Size != before.Size || after.Mode != before.Mode || after.Nlink != 1 {
			t.Errorf("a.jpg after SetModTime = %+v, want the identity of %+v", after, before)
		}
		if got := mustLstat(t, d, "a.jpg").ModTime; !got.Equal(when) {
			t.Errorf("Lstat(a.jpg).ModTime = %v, want %v", got, when)
		}
		if got, err := os.ReadFile(a); err != nil || string(got) != string(data) {
			t.Errorf("a.jpg holds %d bytes (%v) after SetModTime, want the %d written", len(got), err, len(data))
		}

		// The symlinks get their own times; the target keeps its.
		linkTime := time.Date(2008, 3, 22, 14, 0, 0, 0, time.UTC)
		for _, link := range []string{"link", "solto"} {
			if err := w.SetModTime([]byte(link), linkTime); err != nil {
				t.Fatalf("SetModTime(%s): %v", link, err)
			}
			wantMtime(t, filepath.Join(src, link), linkTime)
			if got := mustLstat(t, d, link); got.Kind != domain.EntrySymlink {
				t.Errorf("%s after SetModTime = %+v, want a symlink", link, got)
			}
		}
		wantMtime(t, b, old)
		if _, err := os.Lstat(filepath.Join(src, "nada")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the dangling link's target after SetModTime: %v", err)
		}

		wantErr(t, w.SetModTime([]byte("nada"), when), domain.OutcomeAbsent, syscall.ENOENT)
		for _, bad := range []string{"", ".", "..", "a/b", "a\x00b"} {
			checkInvalid(t, w.SetModTime([]byte(bad), when), "SetModTime", bad)
		}
	})
}

// On a read-only remount SetModTime fails with ErrReadOnly and changes
// nothing; an absent name there is still absent.
func TestWriterSetModTimeReadOnly(t *testing.T) {
	inUserNamespace(t, func(t *testing.T) {
		ro := tmpfsDir(t, "")
		a := filepath.Join(ro, "a.jpg")
		writeText(t, a, "A")
		old := time.Date(2002, 3, 4, 5, 6, 7, 8, time.UTC)
		setTimes(t, a, old, old)
		if err := unix.Mount("", ro, "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
			t.Fatalf("remount %s read-only: %v", ro, err)
		}
		w := linuxWriter(t, openRoot(t, fsaccess.NewOS(), ro))
		err := w.SetModTime([]byte("a.jpg"), time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC))
		wantErr(t, err, "", fsaccess.ErrReadOnly)
		if !errors.Is(err, syscall.EROFS) {
			t.Errorf("err = %v, want it to carry EROFS", err)
		}
		wantMtime(t, a, old)
		wantErr(t, w.SetModTime([]byte("nada"), old), domain.OutcomeAbsent, syscall.ENOENT)
	})
}

// asUser runs fn on an operating-system thread of its own whose user and
// group IDs are all id, without supplementary groups or capabilities, as an
// unprivileged process runs. The raw system calls change that thread alone
// (Go's wrappers change every thread), and the thread is never handed back
// to the scheduler: it exits with the goroutine. The caller must be root.
func asUser(t *testing.T, id int, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked
		for _, call := range []struct {
			trap uintptr
			args [3]uintptr
		}{
			{unix.SYS_SETGROUPS, [3]uintptr{0, 0, 0}},
			{unix.SYS_SETRESGID, [3]uintptr{uintptr(id), uintptr(id), uintptr(id)}},
			{unix.SYS_SETRESUID, [3]uintptr{uintptr(id), uintptr(id), uintptr(id)}},
		} {
			if _, _, errno := unix.RawSyscall(call.trap, call.args[0], call.args[1], call.args[2]); errno != 0 {
				done <- errno
				return
			}
		}
		done <- fn()
	}()
	return <-done
}

// An explicit time needs the file's owner (or CAP_FOWNER): a process of
// another user gets ErrPermission even on a file anyone may write, and the
// time stays; the same process sets the time of a file it owns.
//
// Two users need real root: the user namespace inUserNamespace makes maps
// only one, so the test runs only as root (scripts/e2e-docker.sh), and
// skips otherwise.
func TestWriterSetModTimeNotOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root, to own files as two users (scripts/e2e-docker.sh runs it as root)")
	}
	const owner, other = 1000, 2000
	src := tmpfsDir(t, "")
	old := time.Date(2002, 3, 4, 5, 6, 7, 8, time.UTC)
	for name, uid := range map[string]int{"alheio.jpg": owner, "meu.jpg": other} {
		p := filepath.Join(src, name)
		writeText(t, p, name)
		if err := os.Chmod(p, 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(p, uid, uid); err != nil {
			t.Fatal(err)
		}
		setTimes(t, p, old, old)
	}
	w := linuxWriter(t, openRoot(t, fsaccess.NewOS(), src))
	when := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)

	var foreign, own error
	if err := asUser(t, other, func() error {
		foreign = w.SetModTime([]byte("alheio.jpg"), when)
		own = w.SetModTime([]byte("meu.jpg"), when)
		return nil
	}); err != nil {
		t.Fatalf("becoming user %d: %v", other, err)
	}
	wantErr(t, foreign, "", fsaccess.ErrPermission)
	if !errors.Is(foreign, syscall.EPERM) {
		t.Errorf("SetModTime of another user's file = %v, want it to carry EPERM", foreign)
	}
	wantMtime(t, filepath.Join(src, "alheio.jpg"), old)
	if own != nil {
		t.Errorf("SetModTime of the process's own file = %v, want it set", own)
	}
	wantMtime(t, filepath.Join(src, "meu.jpg"), when)
}
