//go:build linux

package fsaccess

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"precious/internal/domain"
)

func TestOutcomeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want domain.AccessOutcome
	}{
		{syscall.ENOENT, domain.OutcomeAbsent},
		{syscall.EACCES, domain.OutcomeUnreadable},
		{syscall.EPERM, domain.OutcomeUnreadable},
		{syscall.EIO, domain.OutcomeUnavailable},
		{syscall.ESTALE, domain.OutcomeUnavailable},
		{syscall.ENOTCONN, domain.OutcomeUnavailable},
		{syscall.ENODEV, domain.OutcomeUnavailable},
		{syscall.ENXIO, domain.OutcomeUnavailable},
		// Anything unclassified is unavailable, never absent.
		{syscall.ELOOP, domain.OutcomeUnavailable},
		{syscall.EMFILE, domain.OutcomeUnavailable},
		{errors.New("opaque"), domain.OutcomeUnavailable},
	}
	for _, tc := range cases {
		wrapped := &fs.PathError{Op: "statat", Path: "x", Err: tc.err}
		for _, err := range []error{tc.err, wrapped, fmt.Errorf("ctx: %w", wrapped)} {
			if got := outcomeFor(err); got != tc.want {
				t.Errorf("outcomeFor(%v) = %q, want %q", err, got, tc.want)
			}
		}
	}
}

func TestContextualOutcomes(t *testing.T) {
	escape := &fs.PathError{Op: "openat", Path: "x/.", Err: errors.New("path escapes from parent")}
	cases := []struct {
		name string
		fn   func(error) domain.AccessOutcome
		err  error
		want domain.AccessOutcome
	}{
		{"missing root", rootOutcome, syscall.ENOENT, domain.OutcomeUnavailable},
		{"root not a directory", rootOutcome, syscall.ENOTDIR, domain.OutcomeUnavailable},
		{"root permission", rootOutcome, syscall.EACCES, domain.OutcomeUnreadable},
		{"open: now a file or FIFO", openDirOutcome, syscall.ENOTDIR, domain.OutcomeChangedDuringObservation},
		{"open: symlink loop", openDirOutcome, syscall.ELOOP, domain.OutcomeChangedDuringObservation},
		{"open: symlink escapes", openDirOutcome, escape, domain.OutcomeChangedDuringObservation},
		{"open: gone", openDirOutcome, syscall.ENOENT, domain.OutcomeAbsent},
		{"open: mode 0000", openDirOutcome, syscall.EACCES, domain.OutcomeUnreadable},
		{"open: closed parent", openDirOutcome, os.ErrClosed, domain.OutcomeUnavailable},
		{"readlink: no longer a link", readlinkOutcome, syscall.EINVAL, domain.OutcomeChangedDuringObservation},
		{"readlink: gone", readlinkOutcome, syscall.ENOENT, domain.OutcomeAbsent},
		{"open file: now a symlink", openFileOutcome, syscall.ELOOP, domain.OutcomeChangedDuringObservation},
		{"open file: gone", openFileOutcome, syscall.ENOENT, domain.OutcomeAbsent},
		{"open file: parent unreadable", openFileOutcome, syscall.EACCES, domain.OutcomeUnreadable},
		{"reopen: /proc missing", reopenOutcome, syscall.ENOENT, domain.OutcomeUnavailable},
		{"reopen: mode 0000", reopenOutcome, syscall.EACCES, domain.OutcomeUnreadable},
	}
	for _, tc := range cases {
		if got := tc.fn(&fs.PathError{Op: "op", Path: "x", Err: tc.err}); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x", "a/b", "/abs", "x/", "a\x00b", "\x00"} {
		err := ValidateName("Lstat", []byte(name))
		var e *Error
		if !errors.As(err, &e) || !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want *Error wrapping ErrInvalidName", name, err)
			continue
		}
		if e.Op != "Lstat" || string(e.Name) != name || e.Outcome != "" {
			t.Errorf("ValidateName(%q) = %+v, want Op Lstat, the name, and no outcome", name, e)
		}
	}
	for _, name := range []string{"a", "...", ".git", "f\xe9.txt", "e\u0301", " ", "a\\b", "-"} {
		if err := ValidateName("Lstat", []byte(name)); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}
