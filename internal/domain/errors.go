package domain

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable, machine-readable error identifier exposed through the
// command API, job terminal reasons, and logs.
type ErrorCode string

const (
	CodeUnknownSource        ErrorCode = "unknown_source"
	CodeNotFound             ErrorCode = "not_found"
	CodeIdempotencyKeyReused ErrorCode = "idempotency_key_reused"
	CodeAttemptsExhausted    ErrorCode = "attempts_exhausted"
	CodeUnauthenticated      ErrorCode = "unauthenticated"
	CodeForbidden            ErrorCode = "forbidden"
	CodeRequestTooLarge      ErrorCode = "request_too_large"
	CodeInvalidRequest       ErrorCode = "invalid_request"
	// CodeOutsideAllowedRoots refuses a picker path outside every allowed
	// root (design D5).
	CodeOutsideAllowedRoots ErrorCode = "outside_allowed_roots"
	// CodeSourceExists refuses a source equal to, inside, or around an
	// existing source of the same volume.
	CodeSourceExists ErrorCode = "source_exists"
	// CodeSourceOffline refuses work that needs a source's volume while it is
	// not mounted.
	CodeSourceOffline ErrorCode = "source_offline"
	// CodeJobActive refuses a change blocked by a job that is not terminal,
	// such as removing a source during its scan.
	CodeJobActive ErrorCode = "job_active"
	// CodeTagExists refuses a tag name already in use (case-insensitive).
	CodeTagExists ErrorCode = "tag_exists"
	// CodeSelectionExpired refuses a request naming an expired selection.
	CodeSelectionExpired ErrorCode = "selection_expired"
	// CodeInvalidEntryState refuses a request the entry's kind or state does
	// not allow, such as the content of a folder or of a missing file.
	CodeInvalidEntryState ErrorCode = "invalid_entry_state"
	// CodeWritesUnavailable refuses turning writes on for a source whose
	// writes cannot be safe: forbidden by the configuration, a read-only
	// filesystem, or one without a no-replace rename (r3 design D1).
	CodeWritesUnavailable ErrorCode = "writes_unavailable"
	// CodeWritesDisabled refuses planning or running an action on a source
	// whose write permission is off.
	CodeWritesDisabled ErrorCode = "writes_disabled"
	// CodeNameTaken refuses a rename or a new folder whose name is already
	// used in its folder.
	CodeNameTaken ErrorCode = "name_taken"
	// CodeActionExpired refuses running a planned action after it expired.
	CodeActionExpired ErrorCode = "action_expired"
	// CodeActionNotRunnable refuses running or cancelling an action whose
	// state does not allow it, or that has nothing left to run.
	CodeActionNotRunnable ErrorCode = "action_not_runnable"
	// CodeActionNotUndoable refuses undoing an action that did not run or
	// has nothing left to reverse.
	CodeActionNotUndoable ErrorCode = "action_not_undoable"
	// CodeRecoveryNeeded refuses changes on a source with a step the owner
	// must check, or one still being recorded.
	CodeRecoveryNeeded ErrorCode = "recovery_needed"
	// CodeInQuarantine refuses a change to a quarantined entry other than
	// restore, purge, or an individual move out (r4 D13).
	CodeInQuarantine ErrorCode = "in_quarantine"
	// CodePurgeNotAllowed refuses planning or running a purge whose check
	// still has files that need the owner's confirmation (r4 D8, D11).
	CodePurgeNotAllowed ErrorCode = "purge_not_allowed"
	// CodeCheckStale refuses acting on a pre-delete check that something
	// it relied on has changed since (r4 D10).
	CodeCheckStale ErrorCode = "check_stale"
	// CodeCheckRunning refuses a request while a pre-delete check is still
	// running (r4 D7).
	CodeCheckRunning ErrorCode = "check_running"
	// CodeQuarantineNotEmpty refuses removing a source whose quarantine
	// still holds items (r4 D14).
	CodeQuarantineNotEmpty ErrorCode = "quarantine_not_empty"
	// CodeQuarantineNameTaken refuses a cleanup plan while the source's top
	// folder holds a .precious-quarantine that Precious did not make (r4 D1).
	CodeQuarantineNameTaken ErrorCode = "quarantine_name_taken"
	// CodeLoginFailed refuses a login without saying which check failed
	// (wrong password, throttled, or no administrator).
	CodeLoginFailed ErrorCode = "login_failed"
	CodeInternal    ErrorCode = "internal"
)

// Error carries a stable code plus human detail. Messages may include escaped
// display names but never secrets.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds an *Error with a formatted message.
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a code to an underlying error.
func Wrap(code ErrorCode, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the code of the first *Error in err's chain, or CodeInternal.
func CodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}
