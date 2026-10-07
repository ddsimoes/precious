package domain

import (
	"fmt"
	"time"
)

// ScheduleEvery is how often a scheduled rescan runs.
type ScheduleEvery string

const (
	EveryDay  ScheduleEvery = "day"
	EveryWeek ScheduleEvery = "week"
)

// Schedule is a source's rescan schedule (§7; r2b design D6): every day, or
// every week on one weekday, at a time of day in a named time zone. A source
// without a schedule has none at all, so the zero value is not valid.
type Schedule struct {
	Every ScheduleEvery `json:"every"`
	// At is the time of day, "HH:MM" on a 24-hour clock.
	At string `json:"at"`
	// Weekday is 0 (Sunday) to 6 (Saturday), for a weekly schedule only.
	Weekday *time.Weekday `json:"weekday,omitempty"`
	// Zone is an IANA time zone name, such as "America/Sao_Paulo".
	Zone string `json:"zone"`
}

// clock is a validated schedule: its time of day and its zone.
type clock struct {
	hour, minute int
	loc          *time.Location
}

// check validates s; every problem is invalid_request.
func (s Schedule) check() (clock, error) {
	bad := func(format string, args ...any) (clock, error) {
		return clock{}, Errorf(CodeInvalidRequest, "schedule: "+format, args...)
	}
	switch s.Every {
	case EveryDay:
		if s.Weekday != nil {
			return bad("a daily schedule has no weekday")
		}
	case EveryWeek:
		if s.Weekday == nil || *s.Weekday < time.Sunday || *s.Weekday > time.Saturday {
			return bad("a weekly schedule needs a weekday from 0 (Sunday) to 6 (Saturday)")
		}
	default:
		return bad("every must be %q or %q, not %q", EveryDay, EveryWeek, s.Every)
	}
	digits := func(a, b byte) (int, bool) {
		if a < '0' || a > '9' || b < '0' || b > '9' {
			return 0, false
		}
		return int(a-'0')*10 + int(b-'0'), true
	}
	if len(s.At) != 5 || s.At[2] != ':' {
		return bad("at must be a time from 00:00 to 23:59 as HH:MM, not %q", s.At)
	}
	h, okH := digits(s.At[0], s.At[1])
	m, okM := digits(s.At[3], s.At[4])
	if !okH || !okM || h > 23 || m > 59 {
		return bad("at must be a time from 00:00 to 23:59 as HH:MM, not %q", s.At)
	}
	if s.Zone == "" || s.Zone == "Local" {
		return bad("a time zone name is required")
	}
	loc, err := time.LoadLocation(s.Zone)
	if err != nil {
		return bad("unknown time zone %q", s.Zone)
	}
	return clock{hour: h, minute: m, loc: loc}, nil
}

// Validate reports whether s is a valid schedule, as an invalid_request
// error naming the problem.
func (s Schedule) Validate() error {
	_, err := s.check()
	return err
}

// Next returns the first time of s strictly after t. A time of day that a
// daylight-saving change skips runs at the time Go normalizes it to, and one
// that occurs twice runs once.
func (s Schedule) Next(t time.Time) (time.Time, error) {
	c, err := s.check()
	if err != nil {
		return time.Time{}, err
	}
	local := t.In(c.loc)
	y, m, d := local.Date()
	for i := 0; i <= 8; i++ {
		at := time.Date(y, m, d+i, c.hour, c.minute, 0, 0, c.loc)
		if !at.After(t) {
			continue
		}
		if s.Every == EveryWeek && time.Date(y, m, d+i, 12, 0, 0, 0, c.loc).Weekday() != *s.Weekday {
			continue
		}
		return at, nil
	}
	return time.Time{}, fmt.Errorf("schedule: no time of %+v after %s", s, t) // unreachable for a valid schedule
}
