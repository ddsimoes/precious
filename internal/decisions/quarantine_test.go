package decisions

import (
	"testing"

	"precious/internal/index"
)

// TestQuarantineHelpersMatchIndex checks that the private copies of the
// quarantine helpers agree with index's (r4 design D2): the residual
// renders the same SQL, and the path test answers the same on paths at,
// below, beside, and above the quarantine.
func TestQuarantineHelpersMatchIndex(t *testing.T) {
	if quarantineName != index.QuarantineName {
		t.Fatalf("quarantineName = %q, index has %q", quarantineName, index.QuarantineName)
	}
	for _, alias := range []string{"e", "t", "entries"} {
		if got, want := notQuarantined(alias), index.NotQuarantined(alias); got != want {
			t.Errorf("notQuarantined(%q) = %s, index has %s", alias, got, want)
		}
	}
	for _, p := range []string{"", ".precious-quarantine", ".precious-quarantine/", ".precious-quarantine/7/1/Fotos",
		".precious-quarantine/7/1.json", ".precious-quarantine0", ".precious-quarantine-old/a", ".precious-quarantin",
		"Fotos/.precious-quarantine", "Fotos/.precious-quarantine/a"} {
		if got, want := quarantinePath([]byte(p)), index.IsQuarantinePath([]byte(p)); got != want {
			t.Errorf("quarantinePath(%q) = %v, index says %v", p, got, want)
		}
	}
}
