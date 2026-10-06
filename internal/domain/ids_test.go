package domain

import "testing"

func TestIDsRoundTrip(t *testing.T) {
	for _, n := range []int64{1, 42, 1 << 40, 1<<63 - 1} {
		if got, err := ParseEntryID(EntryID(n).String()); err != nil || got != EntryID(n) {
			t.Errorf("ParseEntryID(%q) = %d, %v", EntryID(n).String(), got, err)
		}
		if got, err := ParseJobID(JobID(n).String()); err != nil || got != JobID(n) {
			t.Errorf("ParseJobID(%q) = %d, %v", JobID(n).String(), got, err)
		}
	}
	// Read endpoints answer 404 not_found for a malformed ID.
	for _, bad := range []string{"", "0", "-1", "abc", "1.5", " 1", "+1x", "9223372036854775808"} {
		if _, err := ParseEntryID(bad); CodeOf(err) != CodeNotFound {
			t.Errorf("ParseEntryID(%q) err = %v, want not_found", bad, err)
		}
		if _, err := ParseJobID(bad); CodeOf(err) != CodeNotFound {
			t.Errorf("ParseJobID(%q) err = %v, want not_found", bad, err)
		}
	}
	if got := EntryID(7).GoString(); got != "EntryID(7)" {
		t.Errorf("GoString = %q", got)
	}
}
