package fsaccess

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"syscall"

	"precious/internal/domain"
)

// Refusals. OpenDir refuses to open an entry whose expected kind is not a
// directory, OpenFile one whose expected kind is not a regular file, and both
// refuse an entry observed as a mount boundary. A refusal made before touching
// the filesystem carries an empty Outcome, like ErrInvalidName: it reports a
// caller bug, not an observation, and must not be recorded as an access
// outcome.
var (
	ErrNotDirectory  = errors.New("fsaccess: entry is not a directory")
	ErrNotRegular    = errors.New("fsaccess: entry is not a regular file")
	ErrMountBoundary = errors.New("fsaccess: entry is a mount boundary")
	// ErrIdentityChanged is the Err of an OpenDir or OpenFile failure with
	// outcome changed_during_observation: the object found at the name is not
	// the one that was observed (device, inode, or type differs, the name now
	// is a symlink, or a file's size, modification time, or change time
	// differs).
	ErrIdentityChanged = errors.New("fsaccess: entry changed between observation and open")
)

var (
	errRelativePath   = errors.New("fsaccess: path is not absolute")
	errBatchSize      = errors.New("fsaccess: batch size must be positive")
	errNegativeOffset = errors.New("fsaccess: negative read offset")
)

// ValidateName returns nil when name is a single path component: non-empty,
// not "." or "..", and free of '/' and NUL. Otherwise it returns an *Error with
// the given Op, a copy of name, ErrInvalidName, and an empty Outcome. Every Dir
// implementation calls it before touching anything.
func ValidateName(op string, name []byte) error {
	if len(name) == 0 ||
		bytes.Equal(name, []byte(".")) ||
		bytes.Equal(name, []byte("..")) ||
		bytes.IndexByte(name, '/') >= 0 ||
		bytes.IndexByte(name, 0) >= 0 {
		return &Error{Op: op, Name: bytes.Clone(name), Err: ErrInvalidName}
	}
	return nil
}

// outcomeFor maps an operating-system error to an access outcome (design D3).
// An error that cannot be classified is unavailable: a failure is never
// reported as absent unless the OS said the entry does not exist.
func outcomeFor(err error) domain.AccessOutcome {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return domain.OutcomeAbsent
	case errors.Is(err, fs.ErrPermission):
		return domain.OutcomeUnreadable
	}
	return domain.OutcomeUnavailable
}

// rootOutcome classifies an OpenRoot failure: a missing root (or a path whose
// components are no longer directories) makes the source unavailable, never
// absent.
func rootOutcome(err error) domain.AccessOutcome {
	if o := outcomeFor(err); o != domain.OutcomeAbsent {
		return o
	}
	return domain.OutcomeUnavailable
}

// openDirOutcome classifies a failure to open a child that was observed as a
// directory. ENOTDIR and ELOOP mean the name no longer denotes a plain
// directory. os.Root reports a symlink that now resolves outside the root
// ("path escapes from parent") without an errno.
func openDirOutcome(err error) domain.AccessOutcome {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.ENOTDIR || errno == syscall.ELOOP {
			return domain.OutcomeChangedDuringObservation
		}
		return outcomeFor(err)
	}
	if errors.Is(err, os.ErrClosed) {
		return domain.OutcomeUnavailable
	}
	return domain.OutcomeChangedDuringObservation
}

// readlinkOutcome classifies a Readlink failure. EINVAL means the entry is no
// longer a symlink, which callers only learn after observing one.
func readlinkOutcome(err error) domain.AccessOutcome {
	if errors.Is(err, syscall.EINVAL) {
		return domain.OutcomeChangedDuringObservation
	}
	return outcomeFor(err)
}
