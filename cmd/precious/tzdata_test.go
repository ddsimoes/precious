package main

import (
	"testing"
	"time"
)

// Rescan schedules name IANA zones (r2b design D6). With ZONEINFO empty the
// zone still resolves: from the system database where there is one, and
// from the time/tzdata copy linked into the binary everywhere else.
func TestScheduleZonesResolve(t *testing.T) {
	t.Setenv("ZONEINFO", "")
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := time.Date(2026, 10, 4, 3, 0, 0, 0, loc).Zone(); offset != -3*60*60 {
		t.Fatalf("America/Sao_Paulo offset %ds, want -3h", offset)
	}
}
