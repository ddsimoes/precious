package domain

import (
	"fmt"
	"testing"
)

// The organize codes are stable wire values (r3 design Interfaces), and
// CodeOf finds them through wrapping.
func TestOrganizeErrorCodes(t *testing.T) {
	for code, want := range map[ErrorCode]string{
		CodeWritesUnavailable: "writes_unavailable",
		CodeWritesDisabled:    "writes_disabled",
		CodeNameTaken:         "name_taken",
		CodeActionExpired:     "action_expired",
		CodeActionNotRunnable: "action_not_runnable",
		CodeActionNotUndoable: "action_not_undoable",
		CodeRecoveryNeeded:    "recovery_needed",
	} {
		if string(code) != want {
			t.Errorf("code %q, want %q", code, want)
		}
		if got := CodeOf(fmt.Errorf("context: %w", Errorf(code, "refused"))); got != code {
			t.Errorf("CodeOf(wrapped %s) = %s", code, got)
		}
	}
}

// The cleanup codes are stable wire values (r4 design Interfaces), and
// CodeOf finds them through wrapping.
func TestCleanupErrorCodes(t *testing.T) {
	for code, want := range map[ErrorCode]string{
		CodeInQuarantine:        "in_quarantine",
		CodePurgeNotAllowed:     "purge_not_allowed",
		CodeCheckStale:          "check_stale",
		CodeCheckRunning:        "check_running",
		CodeQuarantineNotEmpty:  "quarantine_not_empty",
		CodeQuarantineNameTaken: "quarantine_name_taken",
	} {
		if string(code) != want {
			t.Errorf("code %q, want %q", code, want)
		}
		if got := CodeOf(fmt.Errorf("context: %w", Errorf(code, "refused"))); got != code {
			t.Errorf("CodeOf(wrapped %s) = %s", code, got)
		}
	}
}
