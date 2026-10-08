package fsaccess_test

import (
	"errors"
	"syscall"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// WriteError maps each operating-system answer of a Writer call (r3 design
// D5) to its refusal or outcome, keeping the errno in the chain.
func TestWriteErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		op      string
		errno   syscall.Errno
		refusal error
		outcome domain.AccessOutcome
	}{
		{"RenameNoReplace", syscall.EEXIST, fsaccess.ErrExist, ""},
		{"RenameNoReplace", syscall.ENOTEMPTY, fsaccess.ErrExist, ""},
		{"Mkdir", syscall.EEXIST, fsaccess.ErrExist, ""},
		{"RenameNoReplace", syscall.EINVAL, fsaccess.ErrNoReplaceUnsupported, ""},
		{"Mkdir", syscall.EINVAL, nil, domain.OutcomeUnavailable},
		{"Sync", syscall.EINVAL, nil, domain.OutcomeUnavailable},
		{"RenameNoReplace", syscall.EXDEV, fsaccess.ErrCrossDevice, ""},
		{"Rmdir", syscall.ENOTEMPTY, fsaccess.ErrNotEmpty, ""},
		{"Rmdir", syscall.EEXIST, fsaccess.ErrNotEmpty, ""},
		{"Mkdir", syscall.EROFS, fsaccess.ErrReadOnly, ""},
		{"Sync", syscall.EROFS, fsaccess.ErrReadOnly, ""},
		{"RenameNoReplace", syscall.EACCES, fsaccess.ErrPermission, ""},
		{"Rmdir", syscall.EPERM, fsaccess.ErrPermission, ""},
		{"RenameNoReplace", syscall.ENOENT, nil, domain.OutcomeAbsent},
		{"Mkdir", syscall.ENOENT, nil, domain.OutcomeAbsent},
		{"Rmdir", syscall.ENOTDIR, nil, domain.OutcomeUnavailable},
		{"Sync", syscall.EIO, nil, domain.OutcomeUnavailable},
		{"CreateExclusive", syscall.EEXIST, fsaccess.ErrExist, ""},
		{"CreateExclusive", syscall.EROFS, fsaccess.ErrReadOnly, ""},
		{"CreateExclusive", syscall.ENOENT, nil, domain.OutcomeAbsent},
		{"CreateExclusive", syscall.EISDIR, nil, domain.OutcomeUnavailable},
		{"Unlink", syscall.EISDIR, fsaccess.ErrIsDir, ""},
		{"Unlink", syscall.EPERM, fsaccess.ErrPermission, ""},
		{"Unlink", syscall.EROFS, fsaccess.ErrReadOnly, ""},
		{"Unlink", syscall.ENOENT, nil, domain.OutcomeAbsent},
		{"Rmdir", syscall.EISDIR, nil, domain.OutcomeUnavailable},
	} {
		err := fsaccess.WriteError(tc.op, []byte("a.jpg"), tc.errno)
		var e *fsaccess.Error
		if !errors.As(err, &e) || e.Op != tc.op || string(e.Name) != "a.jpg" || e.Outcome != tc.outcome {
			t.Errorf("%s %v = %v, want an *Error of %s with outcome %q", tc.op, tc.errno, err, tc.op, tc.outcome)
			continue
		}
		if !errors.Is(err, tc.errno) {
			t.Errorf("%s %v = %v, which lost the errno", tc.op, tc.errno, err)
		}
		for _, r := range []error{fsaccess.ErrExist, fsaccess.ErrNoReplaceUnsupported, fsaccess.ErrCrossDevice,
			fsaccess.ErrNotEmpty, fsaccess.ErrIsDir, fsaccess.ErrReadOnly, fsaccess.ErrPermission, fsaccess.ErrIntoItself} {
			if got := errors.Is(err, r); got != (r == tc.refusal) {
				t.Errorf("%s %v = %v: errors.Is(%v) = %v", tc.op, tc.errno, err, r, got)
			}
		}
	}
}
