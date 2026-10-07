package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func weekday(d time.Weekday) *time.Weekday { return &d }

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// r2b task 1.3: Next is the first scheduled time strictly after a time, in
// the schedule's zone, across days, weeks, and daylight-saving changes.
func TestScheduleNext(t *testing.T) {
	sp := mustZone(t, "America/Sao_Paulo") // no daylight saving since 2019
	ny := mustZone(t, "America/New_York")
	cases := []struct {
		name  string
		s     Schedule
		after time.Time
		want  time.Time
	}{
		{"daily, later today", Schedule{Every: EveryDay, At: "03:00", Zone: "America/Sao_Paulo"},
			time.Date(2026, 10, 7, 1, 0, 0, 0, sp), time.Date(2026, 10, 7, 3, 0, 0, 0, sp)},
		{"daily, already past today", Schedule{Every: EveryDay, At: "03:00", Zone: "America/Sao_Paulo"},
			time.Date(2026, 10, 7, 13, 0, 0, 0, sp), time.Date(2026, 10, 8, 3, 0, 0, 0, sp)},
		{"daily, exactly at the time is not after it", Schedule{Every: EveryDay, At: "03:00", Zone: "America/Sao_Paulo"},
			time.Date(2026, 10, 7, 3, 0, 0, 0, sp), time.Date(2026, 10, 8, 3, 0, 0, 0, sp)},
		{"daily, the instant is compared in the schedule's zone", Schedule{Every: EveryDay, At: "03:00", Zone: "America/Sao_Paulo"},
			time.Date(2026, 10, 7, 5, 30, 0, 0, time.UTC), time.Date(2026, 10, 7, 3, 0, 0, 0, sp)},
		{"weekly, Sunday from a Wednesday", Schedule{Every: EveryWeek, At: "03:00", Weekday: weekday(time.Sunday), Zone: "America/Sao_Paulo"},
			time.Date(2026, 10, 7, 13, 0, 0, 0, sp), time.Date(2026, 10, 11, 3, 0, 0, 0, sp)},
		{"weekly, same weekday but past", Schedule{Every: EveryWeek, At: "03:00", Weekday: weekday(time.Wednesday), Zone: "America/Sao_Paulo"},
			time.Date(2026, 10, 7, 13, 0, 0, 0, sp), time.Date(2026, 10, 14, 3, 0, 0, 0, sp)},
		{"the day before a gap", Schedule{Every: EveryDay, At: "02:30", Zone: "America/New_York"},
			time.Date(2026, 3, 7, 0, 0, 0, 0, ny), time.Date(2026, 3, 7, 2, 30, 0, 0, ny)},
		{"UTC", Schedule{Every: EveryDay, At: "23:59", Zone: "UTC"},
			time.Date(2026, 12, 31, 23, 59, 30, 0, time.UTC), time.Date(2027, 1, 1, 23, 59, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.s.Next(c.after)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(c.want) {
				t.Errorf("Next(%s) = %s, want %s", c.after, got, c.want)
			}
		})
	}
}

// A time that occurs twice (the hour repeated when daylight saving ends) runs
// once that day: the next time after it is the following day.
func TestScheduleRepeatedHourRunsOnce(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	s := Schedule{Every: EveryDay, At: "01:30", Zone: "America/New_York"}
	first, err := s.Next(time.Date(2026, 11, 1, 0, 0, 0, 0, ny))
	if err != nil {
		t.Fatal(err)
	}
	if y, m, d := first.In(ny).Date(); y != 2026 || m != 11 || d != 1 || first.In(ny).Hour() != 1 || first.In(ny).Minute() != 30 {
		t.Fatalf("first = %s, want 2026-11-01 01:30 local", first.In(ny))
	}
	second, err := s.Next(first)
	if err != nil {
		t.Fatal(err)
	}
	if y, m, d := second.In(ny).Date(); y != 2026 || m != 11 || d != 2 {
		t.Errorf("after %s the next is %s, want 2026-11-02", first.In(ny), second.In(ny))
	}
}

// A time that a daylight-saving change skips runs once that day, at whatever
// time Go normalizes it to, and the day after is back at its time.
func TestScheduleGapRunsOnce(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	s := Schedule{Every: EveryDay, At: "02:30", Zone: "America/New_York"}
	first, err := s.Next(time.Date(2026, 3, 8, 0, 0, 0, 0, ny))
	if err != nil {
		t.Fatal(err)
	}
	if y, m, d := first.In(ny).Date(); y != 2026 || m != 3 || d != 8 {
		t.Fatalf("first = %s, want a time on 2026-03-08", first.In(ny))
	}
	second, err := s.Next(first)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 3, 9, 2, 30, 0, 0, ny); !second.Equal(want) {
		t.Errorf("after %s the next is %s, want %s", first.In(ny), second.In(ny), want)
	}
}

func TestScheduleRejectsMalformed(t *testing.T) {
	for _, s := range []Schedule{
		{Every: EveryDay, At: "25:00", Zone: "UTC"},
		{Every: EveryDay, At: "12:60", Zone: "UTC"},
		{Every: EveryDay, At: "3:00", Zone: "UTC"},
		{Every: EveryDay, At: "+1:00", Zone: "UTC"},
		{Every: EveryDay, At: "", Zone: "UTC"},
		{Every: "month", At: "03:00", Zone: "UTC"},
		{Every: EveryDay, At: "03:00", Weekday: weekday(time.Monday), Zone: "UTC"},
		{Every: EveryWeek, At: "03:00", Zone: "UTC"},
		{Every: EveryWeek, At: "03:00", Weekday: weekday(7), Zone: "UTC"},
		{Every: EveryDay, At: "03:00", Zone: "Mars/Olympus"},
		{Every: EveryDay, At: "03:00", Zone: ""},
		{Every: EveryDay, At: "03:00", Zone: "Local"},
	} {
		err := s.Validate()
		if CodeOf(err) != CodeInvalidRequest {
			t.Errorf("%+v: err = %v, want invalid_request", s, err)
		}
		if _, err := s.Next(time.Now()); err == nil {
			t.Errorf("%+v: Next accepted it", s)
		}
	}
}

// The JSON form is the one the sources table stores and the API carries.
func TestScheduleJSON(t *testing.T) {
	s := Schedule{Every: EveryWeek, At: "03:00", Weekday: weekday(time.Sunday), Zone: "America/Sao_Paulo"}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"every":"week","at":"03:00","weekday":0,"zone":"America/Sao_Paulo"}`; got != want {
		t.Errorf("weekly = %s, want %s", got, want)
	}
	b, _ = json.Marshal(Schedule{Every: EveryDay, At: "03:00", Zone: "UTC"})
	if got, want := string(b), `{"every":"day","at":"03:00","zone":"UTC"}`; got != want {
		t.Errorf("daily = %s, want %s", got, want)
	}
}
