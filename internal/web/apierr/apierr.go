// Package apierr writes the JSON error envelope shared by every API handler:
//
//	{"error": {"code": "<stable code>", "message": "<human detail>"}}
package apierr

import (
	"encoding/json"
	"errors"
	"net/http"

	"precious/internal/domain"
)

// Body is the envelope written by Write.
type Body struct {
	Error Detail `json:"error"`
}

// Detail is the envelope content.
type Detail struct {
	Code    domain.ErrorCode `json:"code"`
	Message string           `json:"message"`
	// JobID is set when an error refers to an existing job (for example a
	// coalesced scan request).
	JobID string `json:"job_id,omitempty"`
}

// Write sends status with the envelope.
func Write(w http.ResponseWriter, status int, code domain.ErrorCode, message string) {
	WriteDetail(w, status, Detail{Code: code, Message: message})
}

// WriteDetail sends status with a prepared envelope.
func WriteDetail(w http.ResponseWriter, status int, d Detail) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Body{Error: d})
}

// Status maps a stable code to its HTTP status.
func Status(code domain.ErrorCode) int {
	switch code {
	case domain.CodeUnknownSource, domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeUnauthenticated, domain.CodeLoginFailed:
		return http.StatusUnauthorized
	case domain.CodeForbidden, domain.CodeOutsideAllowedRoots:
		return http.StatusForbidden
	case domain.CodeRequestTooLarge:
		return http.StatusRequestEntityTooLarge
	case domain.CodeInvalidRequest:
		return http.StatusBadRequest
	case domain.CodeIdempotencyKeyReused, domain.CodeSourceExists, domain.CodeSourceOffline,
		domain.CodeJobActive, domain.CodeTagExists, domain.CodeSelectionExpired, domain.CodeInvalidEntryState,
		domain.CodeWritesUnavailable, domain.CodeWritesDisabled, domain.CodeNameTaken, domain.CodeActionExpired,
		domain.CodeActionNotRunnable, domain.CodeActionNotUndoable, domain.CodeRecoveryNeeded,
		domain.CodeInQuarantine, domain.CodePurgeNotAllowed, domain.CodeCheckStale, domain.CodeCheckRunning,
		domain.CodeQuarantineNotEmpty, domain.CodeQuarantineNameTaken:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// FromError writes err using its domain code; non-domain errors become a
// generic internal error without leaking detail.
func FromError(w http.ResponseWriter, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		Write(w, Status(de.Code), de.Code, de.Message)
		return
	}
	Write(w, http.StatusInternalServerError, domain.CodeInternal, "internal error")
}
