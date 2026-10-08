package fsaccess

import (
	"bytes"
	"errors"
	"fmt"
	"syscall"

	"precious/internal/domain"
)

// Writer is the write surface of a Dir (r3 design D3), and the only way
// precious changes a source. Only internal/executor calls it (§5 I2); a guard
// test fails when any other non-test package calls a Writer method or
// AsWriter.
//
// Every call goes through the descriptors of Dir handles opened by the rooted,
// identity-checked descent, never through a path. Names are single
// components, checked with ValidateName before anything is touched. A failed
// call returns an *Error whose Op is the method name and whose Name is the
// name it was given (the folder's own name for Sync):
//
//   - when the filesystem refuses the step, an empty Outcome and an Err
//     matching (errors.Is) one of ErrExist, ErrNoReplaceUnsupported,
//     ErrCrossDevice, ErrNotEmpty, ErrReadOnly, ErrPermission, or
//     ErrIntoItself, and the operating system's error too;
//   - the outcome absent when the name or a folder no longer exists;
//   - the outcome unavailable for anything else (an I/O error, a closed
//     handle), wrapping the operating system's error.
//
// Refusals made before any system call (an invalid name, a destination Dir
// of another FS) carry an empty Outcome and report a caller bug.
type Writer interface {
	// RenameNoReplace renames this folder's child name to newName inside
	// the folder to, which must be a Dir of the same FS (this folder
	// included). It never replaces an existing entry: when newName is
	// taken it fails with ErrExist and changes nothing. A filesystem without
	// the no-replace flag fails with ErrNoReplaceUnsupported, another
	// filesystem with ErrCrossDevice, and moving a folder into itself or
	// below itself with ErrIntoItself.
	RenameNoReplace(name []byte, to Dir, newName []byte) error
	// Mkdir creates the empty folder name with this folder's permission
	// bits (mode & 02777), whatever the process umask: it is created 0700
	// and then given those bits through a handle on the new folder. When
	// that fails, the new folder is removed again before the error returns.
	// A taken name fails with ErrExist.
	Mkdir(name []byte) error
	// Rmdir removes the folder name only when it is empty (ErrNotEmpty
	// otherwise).
	Rmdir(name []byte) error
	// Sync flushes this folder's entries to stable storage (fsync), so a
	// step that changed it survives a power loss.
	Sync() error
}

// AsWriter returns the write surface of d. ok is false for a Dir that has
// none. Every backend's Dir has one; on a backend without a no-replace
// rename (the portable one) each method fails with ErrNoReplaceUnsupported.
// Only internal/executor calls it.
func AsWriter(d Dir) (Writer, bool) {
	w, ok := d.(Writer)
	return w, ok
}

// Write refusals, matched with errors.Is on a Writer error (see Writer).
var (
	// ErrExist: the destination name of a rename or a new folder is taken.
	ErrExist = errors.New("fsaccess: name already exists")
	// ErrNoReplaceUnsupported: the filesystem or platform has no rename
	// that refuses to replace (RENAME_NOREPLACE), so nothing was renamed.
	ErrNoReplaceUnsupported = errors.New("fsaccess: no rename without replacing on this filesystem")
	// ErrCrossDevice: the two folders of a rename are on different
	// filesystems.
	ErrCrossDevice = errors.New("fsaccess: rename across filesystems")
	// ErrNotEmpty: the folder to remove is not empty.
	ErrNotEmpty = errors.New("fsaccess: folder is not empty")
	// ErrReadOnly: the filesystem is read-only.
	ErrReadOnly = errors.New("fsaccess: filesystem is read-only")
	// ErrPermission: the operating system denied the step.
	ErrPermission = errors.New("fsaccess: permission denied")
	// ErrIntoItself: a folder would move into itself or below itself.
	ErrIntoItself = errors.New("fsaccess: folder would move into itself")
)

// Writer method names, the Op of their errors.
const (
	opRename = "RenameNoReplace"
	opMkdir  = "Mkdir"
	opRmdir  = "Rmdir"
	opSync   = "Sync"
)

// errForeignDir refuses a rename whose destination Dir comes from another FS.
var errForeignDir = errors.New("fsaccess: destination folder is not from the same filesystem access")

// WriteError returns the error a Writer method named op reports for the
// operating-system error err on name (see Writer): EEXIST, and ENOTEMPTY on
// a rename, is ErrExist; EINVAL on a rename is ErrNoReplaceUnsupported;
// EXDEV is ErrCrossDevice; ENOTEMPTY and EEXIST on Rmdir are ErrNotEmpty;
// EROFS is ErrReadOnly; EACCES and EPERM are ErrPermission; ENOENT is the
// outcome absent; anything else is unavailable. Backends and the test
// filesystems share it, so they fail alike.
func WriteError(op string, name []byte, err error) error {
	e := &Error{Op: op, Name: bytes.Clone(name), Err: err}
	var refusal error
	switch {
	case (errors.Is(err, syscall.EEXIST) || errors.Is(err, syscall.ENOTEMPTY)) && op == opRmdir:
		refusal = ErrNotEmpty
	case errors.Is(err, syscall.EEXIST), errors.Is(err, syscall.ENOTEMPTY) && op == opRename:
		refusal = ErrExist
	case errors.Is(err, syscall.EINVAL) && op == opRename:
		refusal = ErrNoReplaceUnsupported
	case errors.Is(err, syscall.EXDEV):
		refusal = ErrCrossDevice
	case errors.Is(err, syscall.EROFS):
		refusal = ErrReadOnly
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		refusal = ErrPermission
	case errors.Is(err, syscall.ENOENT):
		e.Outcome = domain.OutcomeAbsent
		return e
	default:
		e.Outcome = domain.OutcomeUnavailable
		return e
	}
	e.Err = fmt.Errorf("%w (%w)", refusal, err)
	return e
}

// intoItselfError is the error of a rename that would move a folder into
// itself; err is the operating system's (EINVAL).
func intoItselfError(name []byte, err error) error {
	return &Error{Op: opRename, Name: bytes.Clone(name), Err: fmt.Errorf("%w (%w)", ErrIntoItself, err)}
}
