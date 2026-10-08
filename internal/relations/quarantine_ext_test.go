package relations_test

import (
	"testing"

	"precious/internal/index"
	"precious/internal/relations"
)

// relations renders the quarantine residual itself, since index imports it
// through sources (r4 design Addendum B1): its name, its predicate on every
// alias it uses, and its path test must stay index's.
func TestQuarantineRenderingIsIndexs(t *testing.T) {
	if relations.QuarantineName != index.QuarantineName {
		t.Errorf("relations names the quarantine %q, index %q", relations.QuarantineName, index.QuarantineName)
	}
	for _, alias := range []string{"e"} {
		if got, want := relations.NotQuarantined(alias), index.NotQuarantined(alias); got != want {
			t.Errorf("relations renders %q\nindex renders     %q", got, want)
		}
	}
	for _, p := range []string{"", ".precious-quarantine", ".precious-quarantine/1/2/x", ".precious-quarantine0",
		".precious-quarantinex/y", ".precious-quarantin", "a/.precious-quarantine", "a/.precious-quarantine/x"} {
		if got, want := relations.IsQuarantinePath([]byte(p)), index.IsQuarantinePath([]byte(p)); got != want {
			t.Errorf("IsQuarantinePath(%q): relations %v, index %v", p, got, want)
		}
	}
}
