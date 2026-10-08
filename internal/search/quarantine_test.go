package search

import (
	"testing"

	"precious/internal/index"
)

// TestQuarantineConditionsMatchIndex checks that the private copies of the
// quarantine conditions render exactly index's (r4 design D2).
func TestQuarantineConditionsMatchIndex(t *testing.T) {
	if quarantineName != index.QuarantineName {
		t.Fatalf("quarantineName = %q, index has %q", quarantineName, index.QuarantineName)
	}
	for _, alias := range []string{"e", "oe", "oae", "ce", "cle", "cae"} {
		if got, want := inQuarantine(alias), index.InQuarantine(alias); got != want {
			t.Errorf("inQuarantine(%q) = %s, index has %s", alias, got, want)
		}
		if got, want := notQuarantined(alias), index.NotQuarantined(alias); got != want {
			t.Errorf("notQuarantined(%q) = %s, index has %s", alias, got, want)
		}
	}
}
