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
