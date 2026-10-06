package domain

import "testing"

func TestParseRef(t *testing.T) {
	good := map[string]Ref{
		"123":                 {Entry: 123},
		"1":                   {Entry: 1},
		"m45":                 {Member: 45},
		"m1":                  {Member: 1},
		"9223372036854775807": {Entry: 1<<63 - 1},
	}
	for s, want := range good {
		got, err := ParseRef(s)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %#v, %v; want %#v", s, got, err, want)
			continue
		}
		if got.String() != s {
			t.Errorf("ParseRef(%q).String() = %q", s, got.String())
		}
		if got.IsMember() != (s[0] == 'm') {
			t.Errorf("ParseRef(%q).IsMember() = %v", s, got.IsMember())
		}
	}
	for _, bad := range []string{"", "m", "m0", "0", "-1", "12x", "M45", "+1", " 1", "012", "m012", "mm1", "m-1", "1.5",
		"9223372036854775808", "m9223372036854775808"} {
		if _, err := ParseRef(bad); CodeOf(err) != CodeInvalidRequest {
			t.Errorf("ParseRef(%q) err = %v, want invalid_request", bad, err)
		}
	}
	// An entry ref with a known archive renders as its member.
	if got := (Ref{Entry: 7, Member: 9}).String(); got != "m9" {
		t.Errorf("member ref String = %q", got)
	}
}

func TestContentVocabularyRoundTrips(t *testing.T) {
	states := []string{"unique_size", "pending", "sampled", "hashed", "changed", "unreadable"}
	if len(states) != len(ContentStates) {
		t.Fatalf("ContentStates = %v", ContentStates)
	}
	for i, s := range states {
		if v := ContentState(s); !v.Valid() || string(v) != s || ContentStates[i] != v {
			t.Errorf("ContentState %q does not round-trip", s)
		}
	}
	filters := []string{"copies", "elsewhere", "unique", "unchecked"}
	if len(filters) != len(DupFilters) {
		t.Fatalf("DupFilters = %v", DupFilters)
	}
	for i, s := range filters {
		if v := DupFilter(s); !v.Valid() || string(v) != s || DupFilters[i] != v {
			t.Errorf("DupFilter %q does not round-trip", s)
		}
	}
	for _, bad := range []string{"", "Hashed", "keeper", "copy"} {
		if ContentState(bad).Valid() || DupFilter(bad).Valid() {
			t.Errorf("%q accepted", bad)
		}
	}
}
