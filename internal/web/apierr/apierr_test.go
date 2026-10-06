package apierr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"precious/internal/domain"
)

// Every stable code answers with its status of the design's Interfaces
// section; a code without a mapping is an internal error.
func TestStatus(t *testing.T) {
	for code, want := range map[domain.ErrorCode]int{
		domain.CodeUnknownSource:        http.StatusNotFound,
		domain.CodeNotFound:             http.StatusNotFound,
		domain.CodeUnauthenticated:      http.StatusUnauthorized,
		domain.CodeLoginFailed:          http.StatusUnauthorized,
		domain.CodeForbidden:            http.StatusForbidden,
		domain.CodeOutsideAllowedRoots:  http.StatusForbidden,
		domain.CodeRequestTooLarge:      http.StatusRequestEntityTooLarge,
		domain.CodeInvalidRequest:       http.StatusBadRequest,
		domain.CodeIdempotencyKeyReused: http.StatusConflict,
		domain.CodeSourceExists:         http.StatusConflict,
		domain.CodeSourceOffline:        http.StatusConflict,
		domain.CodeJobActive:            http.StatusConflict,
		domain.CodeTagExists:            http.StatusConflict,
		domain.CodeSelectionExpired:     http.StatusConflict,
		domain.CodeInvalidEntryState:    http.StatusConflict,
		domain.CodeAttemptsExhausted:    http.StatusInternalServerError,
		domain.CodeInternal:             http.StatusInternalServerError,
		"no_such_code":                  http.StatusInternalServerError,
	} {
		if got := Status(code); got != want {
			t.Errorf("Status(%s) = %d, want %d", code, got, want)
		}
	}
}

// FromError writes a domain error's code, status, and message, and hides
// every other error behind a generic internal error.
func TestFromError(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   domain.ErrorCode
		msg    string
	}{
		{fmtWrap(domain.Errorf(domain.CodeSourceOffline, "source %q is offline", "fotos")),
			http.StatusConflict, domain.CodeSourceOffline, `source "fotos" is offline`},
		{errors.New("disk on fire: /secret/path"), http.StatusInternalServerError, domain.CodeInternal, "internal error"},
	} {
		rec := httptest.NewRecorder()
		FromError(rec, tc.err)
		var body Body
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.status || body.Error.Code != tc.code || body.Error.Message != tc.msg {
			t.Errorf("FromError(%v) = %d %+v, want %d %s %q", tc.err, rec.Code, body.Error, tc.status, tc.code, tc.msg)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
	}
}

func fmtWrap(err error) error { return errors.Join(errors.New("context"), err) }
