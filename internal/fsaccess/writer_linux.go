package fsaccess

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
)

var _ Writer = (*osDir)(nil)

// writeConn returns the raw connection of the folder's own descriptor for a
// Writer call. The descriptor stays valid inside RawConn.Control even if the
// Dir is closed meanwhile; Control then fails instead.
func (d *osDir) writeConn(op string) (syscall.RawConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, d.failure(op, domain.OutcomeUnavailable, os.ErrClosed)
	}
	rc, err := d.dirConn()
	if err != nil {
		return nil, d.failure(op, outcomeFor(err), err)
	}
	return rc, nil
}

// RenameNoReplace is renameat2(RENAME_NOREPLACE) between the descriptors of
// two folders of this backend. The kernel answers EINVAL both for a
// filesystem without the flag and for a folder moved into itself; the second
// is told apart by the folders' current paths.
func (d *osDir) RenameNoReplace(name []byte, to Dir, newName []byte) error {
	const op = opRename
	if err := ValidateName(op, name); err != nil {
		return err
	}
	if err := ValidateName(op, newName); err != nil {
		return err
	}
	t, ok := to.(*osDir)
	if !ok {
		return &Error{Op: op, Name: bytes.Clone(name), Err: errForeignDir}
	}
	from, err := d.writeConn(op)
	if err != nil {
		return err
	}
	dst := from
	if t != d {
		if dst, err = t.writeConn(op); err != nil {
			return err
		}
	}
	var rerr error
	intoItself := false
	cerr := controlBoth(from, dst, func(fromFD, toFD int) {
		rerr = retryEINTR(func() error {
			return unix.Renameat2(fromFD, string(name), toFD, string(newName), unix.RENAME_NOREPLACE)
		})
		if rerr == unix.EINVAL {
			intoItself = movesIntoItself(fromFD, name, toFD)
		}
	})
	switch {
	case cerr != nil:
		return &Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: cerr}
	case rerr == nil:
		return nil
	case intoItself:
		return intoItselfError(name, rerr)
	}
	return WriteError(op, name, rerr)
}

// movesIntoItself reports whether the folder toFD is the child name of the
// folder fromFD or lies below it, by their current paths in this mount
// namespace.
func movesIntoItself(fromFD int, name []byte, toFD int) bool {
	fromPath, err1 := os.Readlink("/proc/self/fd/" + strconv.Itoa(fromFD))
	toPath, err2 := os.Readlink("/proc/self/fd/" + strconv.Itoa(toFD))
	if err1 != nil || err2 != nil {
		return false
	}
	moved := childPath(fromPath, name)
	return toPath == moved || strings.HasPrefix(toPath, moved+"/")
}

// Mkdir is mkdirat(0700), then fchmod, through an O_NOFOLLOW handle on the
// new folder, to the parent's mode & 02777 read by fstat of the parent's
// descriptor (r3 design D3).
func (d *osDir) Mkdir(name []byte) error {
	const op = opMkdir
	if err := ValidateName(op, name); err != nil {
		return err
	}
	rc, err := d.writeConn(op)
	if err != nil {
		return err
	}
	var merr error
	if err := rc.Control(func(fd uintptr) { merr = mkdirAt(int(fd), string(name)) }); err != nil {
		return &Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: err}
	}
	if merr != nil {
		return WriteError(op, name, merr)
	}
	return nil
}

func mkdirAt(parent int, name string) error {
	var st unix.Stat_t
	if err := fstat(parent, &st); err != nil {
		return err
	}
	if err := retryEINTR(func() error { return unix.Mkdirat(parent, name, 0o700) }); err != nil {
		return err
	}
	var fd int
	err := retryEINTR(func() (err error) {
		fd, err = unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		return err
	})
	if err == nil {
		err = retryEINTR(func() error { return unix.Fchmod(fd, st.Mode&0o2777) })
		unix.Close(fd)
	}
	if err != nil {
		// The folder is new and empty: take it away, so a failed Mkdir leaves
		// nothing behind (best effort; an ambiguous result is confirmed by the
		// caller anyway).
		_ = retryEINTR(func() error { return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR) })
		return err
	}
	return nil
}

// Rmdir is unlinkat(AT_REMOVEDIR), which removes only an empty folder.
func (d *osDir) Rmdir(name []byte) error {
	const op = opRmdir
	if err := ValidateName(op, name); err != nil {
		return err
	}
	rc, err := d.writeConn(op)
	if err != nil {
		return err
	}
	var rerr error
	if err := rc.Control(func(fd uintptr) {
		rerr = retryEINTR(func() error { return unix.Unlinkat(int(fd), string(name), unix.AT_REMOVEDIR) })
	}); err != nil {
		return &Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: err}
	}
	if rerr != nil {
		return WriteError(op, name, rerr)
	}
	return nil
}

// Sync is fsync of the folder's own descriptor.
func (d *osDir) Sync() error {
	const op = opSync
	rc, err := d.writeConn(op)
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = retryEINTR(func() error { return unix.Fsync(int(fd)) })
	}); err != nil {
		return d.failure(op, domain.OutcomeUnavailable, err)
	}
	if serr != nil {
		return WriteError(op, d.self.Name, serr)
	}
	return nil
}

// CreateExclusive is openat(O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC)
// with the parent's mode & 0666 read by fstat of the parent's descriptor,
// then fchmod to that mode (defeating the umask), every byte written, fsync,
// and close (r4 design D12).
func (d *osDir) CreateExclusive(name, data []byte) error {
	const op = opCreate
	if err := ValidateName(op, name); err != nil {
		return err
	}
	rc, err := d.writeConn(op)
	if err != nil {
		return err
	}
	var cerr error
	if err := rc.Control(func(fd uintptr) { cerr = createAt(int(fd), string(name), data) }); err != nil {
		return &Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: err}
	}
	if cerr != nil {
		return WriteError(op, name, cerr)
	}
	return nil
}

func createAt(parent int, name string, data []byte) error {
	var st unix.Stat_t
	if err := fstat(parent, &st); err != nil {
		return err
	}
	mode := st.Mode & 0o666
	var fd int
	err := retryEINTR(func() (err error) {
		fd, err = unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		return err
	})
	if err != nil {
		return err
	}
	var made unix.Stat_t
	err = fstat(fd, &made)
	known := err == nil
	if err == nil {
		err = retryEINTR(func() error { return unix.Fchmod(fd, mode) })
	}
	if err == nil {
		err = writeAll(fd, data)
	}
	if err == nil {
		err = retryEINTR(func() error { return unix.Fsync(fd) })
	}
	// close is not retried: the descriptor is released even when it fails.
	if cerr := unix.Close(fd); err == nil {
		err = cerr
	}
	if err != nil {
		// Take the partial file away, so a failed CreateExclusive leaves
		// nothing behind (best effort), but never another entry that took
		// the name meanwhile.
		var now unix.Stat_t
		if !known || (unix.Fstatat(parent, name, &now, unix.AT_SYMLINK_NOFOLLOW) == nil &&
			now.Dev == made.Dev && now.Ino == made.Ino) {
			_ = retryEINTR(func() error { return unix.Unlinkat(parent, name, 0) })
		}
		return err
	}
	return nil
}

// writeAll writes all of data to fd, looping over short writes.
func writeAll(fd int, data []byte) error {
	for len(data) > 0 {
		var n int
		err := retryEINTR(func() (err error) {
			n, err = unix.Write(fd, data)
			return err
		})
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// Unlink is unlinkat(fd, name, 0). Linux answers EISDIR for a folder; a
// filesystem answering EPERM instead is told apart by fstatat of the name.
func (d *osDir) Unlink(name []byte) error {
	const op = opUnlink
	if err := ValidateName(op, name); err != nil {
		return err
	}
	rc, err := d.writeConn(op)
	if err != nil {
		return err
	}
	var uerr error
	isDir := false
	if err := rc.Control(func(fd uintptr) {
		uerr = retryEINTR(func() error { return unix.Unlinkat(int(fd), string(name), 0) })
		if uerr == unix.EPERM {
			var st unix.Stat_t
			isDir = unix.Fstatat(int(fd), string(name), &st, unix.AT_SYMLINK_NOFOLLOW) == nil &&
				st.Mode&unix.S_IFMT == unix.S_IFDIR
		}
	}); err != nil {
		return &Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: err}
	}
	switch {
	case uerr == nil:
		return nil
	case isDir:
		return isDirError(name, uerr)
	}
	return WriteError(op, name, uerr)
}

// controlBoth runs fn with the descriptors of a and b, both held valid; a and
// b may be the same connection.
func controlBoth(a, b syscall.RawConn, fn func(afd, bfd int)) error {
	if a == b {
		return a.Control(func(fd uintptr) { fn(int(fd), int(fd)) })
	}
	var inner error
	outer := a.Control(func(afd uintptr) {
		inner = b.Control(func(bfd uintptr) { fn(int(afd), int(bfd)) })
	})
	return errors.Join(outer, inner)
}

// retryEINTR calls fn until it fails with anything but EINTR.
func retryEINTR(fn func() error) error {
	for {
		if err := fn(); err != unix.EINTR {
			return err
		}
	}
}
