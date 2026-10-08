package config

import (
	"testing"
	"time"

	// The binary links the zone database (cmd/precious); so do these tests,
	// so they pass on a host without zone files.
	_ "time/tzdata"
)

const datesHead = "state_dir = \"/var/lib/precious\"\n[server]\nexternal_origin = \"https://precious.example.net\"\n"

// server-config "An unknown zone": Load refuses a zone name it cannot
// resolve, naming dates.time_zone, so neither check-config nor serve goes on.
func TestLoadUnknownTimeZone(t *testing.T) {
	_, err := Load(writeConfig(t, datesHead+"[dates]\ntime_zone = \"Mars/Olympus\"\n"))
	checkProblems(t, err, []want{{"dates.time_zone", `"Mars/Olympus" is not a known IANA time zone`}})
}

// server-config "An unset zone" (loading half; the check-config output is
// tested in cmd/precious): with no [dates] section, or an empty name, the
// zone is the server's local zone.
func TestLoadUnsetTimeZone(t *testing.T) {
	for _, body := range []string{datesHead, datesHead + "[dates]\n", datesHead + "[dates]\ntime_zone = \"\"\n"} {
		cfg, err := Load(writeConfig(t, body))
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if cfg.Dates.TimeZone != "" {
			t.Errorf("%q: time_zone = %q, want empty", body, cfg.Dates.TimeZone)
		}
		loc, err := cfg.Dates.Location()
		if err != nil || loc != time.Local {
			t.Errorf("%q: Location() = %v, %v, want time.Local", body, loc, err)
		}
	}
}

// A set zone resolves to that IANA zone; "Local", Go's alias for the
// server's zone, and malformed names are refused like unknown ones.
func TestDatesLocation(t *testing.T) {
	cfg, err := Load(writeConfig(t, datesHead+"[dates]\ntime_zone = \"America/Sao_Paulo\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	loc, err := cfg.Dates.Location()
	if err != nil {
		t.Fatal(err)
	}
	if loc.String() != "America/Sao_Paulo" {
		t.Errorf("Location() = %v, want America/Sao_Paulo", loc)
	}
	if _, offset := time.Date(2026, 10, 4, 12, 0, 0, 0, loc).Zone(); offset != -3*60*60 {
		t.Errorf("America/Sao_Paulo offset %ds, want -3h", offset)
	}
	if loc, err := (Dates{TimeZone: "UTC"}).Location(); err != nil || loc != time.UTC {
		t.Errorf("UTC: Location() = %v, %v, want time.UTC", loc, err)
	}

	for _, tc := range []struct{ zone, text string }{
		{"Local", "not an IANA time zone name"},
		{" UTC", "not a known IANA time zone"},
		{"../../etc/passwd", "not a known IANA time zone"},
	} {
		c, _ := validConfig(t)
		c.Dates.TimeZone = tc.zone
		checkProblems(t, Validate(&c), []want{{"dates.time_zone", tc.text}})
	}
}
