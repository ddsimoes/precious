package media

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Date is a date at a precision (D5). Instant is the moment, UTC, or the
// start of the period when coarser than a second. Local is the wall time in
// the date's own offset when known, else in the zone: "YYYY", "YYYY-MM",
// "YYYY-MM-DD", or "YYYY-MM-DDTHH:MM:SS". OffsetMin is that offset, in
// minutes east of UTC, when the source records one.
type Date struct {
	Instant   time.Time
	Local     string
	OffsetMin *int
	Precision Precision
}

// Correction is an owner's correction (D11). SetLocal takes any of
// ParseLocal's forms; SetOffsetMin goes only with a time; ShiftS is a
// duration in seconds.
type Correction struct {
	Kind         string
	SetLocal     string
	SetOffsetMin *int
	ShiftS       int64
}

var errBadLocal = errors.New("media: invalid local date")

// layouts are the Local forms by precision.
var layouts = map[Precision]string{
	PrecisionYear:   "2006",
	PrecisionMonth:  "2006-01",
	PrecisionDay:    "2006-01-02",
	PrecisionSecond: localSecond,
}

// precisionOfLen maps a Local's length to its precision.
func precisionOfLen(n int) Precision {
	switch n {
	case 4:
		return PrecisionYear
	case 7:
		return PrecisionMonth
	case 10:
		return PrecisionDay
	case 19:
		return PrecisionSecond
	}
	return ""
}

// parseWall parses a Local form into its wall time (UTC as a carrier) and
// precision.
func parseWall(s string) (time.Time, Precision, error) {
	p := precisionOfLen(len(s))
	if p == "" {
		return time.Time{}, "", errBadLocal
	}
	f := [6]string{s[0:4], "01", "01", "00", "00", "00"}
	if len(s) >= 7 {
		if s[4] != '-' {
			return time.Time{}, "", errBadLocal
		}
		f[1] = s[5:7]
	}
	if len(s) >= 10 {
		if s[7] != '-' {
			return time.Time{}, "", errBadLocal
		}
		f[2] = s[8:10]
	}
	if len(s) == 19 {
		if s[10] != 'T' || s[13] != ':' || s[16] != ':' {
			return time.Time{}, "", errBadLocal
		}
		f[3], f[4], f[5] = s[11:13], s[14:16], s[17:19]
	}
	w, ok := wallIn(minStoredYear, maxStoredYear, f[0], f[1], f[2], f[3], f[4], f[5])
	if !ok {
		return time.Time{}, "", errBadLocal
	}
	return w, p, nil
}

// zoneOf is the location a wall time is read in: its own offset when
// known, else the zone.
func zoneOf(zone *time.Location, offsetMin *int) *time.Location {
	if offsetMin != nil {
		return time.FixedZone("", *offsetMin*60)
	}
	if zone == nil {
		return time.UTC
	}
	return zone
}

// fromWall builds the Date of a wall time read in loc, at a precision.
func fromWall(w time.Time, p Precision, loc *time.Location, offsetMin *int) Date {
	t := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), loc)
	return Date{Instant: t.UTC(), Local: w.Format(layouts[p]), OffsetMin: copyInt(offsetMin), Precision: p}
}

// fromInstant is the second-precision Date of an instant, with its wall
// time in the zone.
func fromInstant(t time.Time, zone *time.Location) Date {
	if zone == nil {
		zone = time.UTC
	}
	return Date{Instant: t.UTC(), Local: t.In(zone).Format(localSecond), Precision: PrecisionSecond}
}

func copyInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// ParseLocal parses a date as the owner sets it (D11): "YYYY", "YYYY-MM",
// "YYYY-MM-DD", or "YYYY-MM-DDTHH:MM:SS", read in offsetMin when given
// (only with a time, within ±14 h), else in zone.
func ParseLocal(s string, zone *time.Location, offsetMin *int) (Date, error) {
	w, p, err := parseWall(s)
	if err != nil || w.Year() < minYear || w.Year() > maxYear {
		return Date{}, fmt.Errorf("%w: %q", errBadLocal, s)
	}
	if offsetMin != nil && (p != PrecisionSecond || *offsetMin < -840 || *offsetMin > 840) {
		return Date{}, fmt.Errorf("%w: an offset needs a time, within ±14 hours", errBadLocal)
	}
	return fromWall(w, p, zoneOf(zone, offsetMin), offsetMin), nil
}

// DateFromRow rebuilds a stored date (`media_dates` or a plan's row): the
// instant in nanoseconds, its Local form, offset, and precision.
func DateFromRow(effectiveNs int64, local string, offsetMin *int, precision string) (Date, error) {
	p := Precision(precision)
	if p.rank() == 0 || precisionOfLen(len(local)) != p {
		return Date{}, fmt.Errorf("%w: %q at precision %q", errBadLocal, local, precision)
	}
	if _, _, err := parseWall(local); err != nil {
		return Date{}, fmt.Errorf("%w: %q", errBadLocal, local)
	}
	return Date{Instant: time.Unix(0, effectiveNs).UTC(), Local: local, OffsetMin: copyInt(offsetMin), Precision: p}, nil
}

// end is the end of the date's period (exclusive), read in the zone or its
// own offset. Without a zone (detection), the offset its instant implies at
// the start of the period is used.
func (d Date) end(zone *time.Location) time.Time {
	w, p, err := parseWall(d.Local)
	if err != nil {
		return d.Instant.Add(time.Second)
	}
	loc := zoneOf(zone, d.OffsetMin)
	if zone == nil && d.OffsetMin == nil {
		loc = time.FixedZone("", int(w.Sub(d.Instant)/time.Second))
	}
	start := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, loc)
	switch p {
	case PrecisionYear:
		return start.AddDate(1, 0, 0)
	case PrecisionMonth:
		return start.AddDate(0, 1, 0)
	case PrecisionDay:
		return start.AddDate(0, 0, 1)
	}
	return d.Instant.Add(time.Second)
}

// contains reports whether t lies in the date's period, widened by tol on
// both sides.
func (d Date) contains(t time.Time, zone *time.Location, tol time.Duration) bool {
	return !t.Before(d.Instant.Add(-tol)) && t.Before(d.end(zone).Add(tol))
}

// lower folds ASCII letters to lower case, leaving every other byte.
func lower(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// boundary reports whether nothing, or a non-digit, follows position i.
func boundary(s string, i int) bool { return i >= len(s) || !isDigit(s[i]) }

// NameDate finds a date at the start of a file name (D6), with the
// precision of its pattern. Wall times are read in zone; PXL names are UTC.
func NameDate(name []byte, zone *time.Location) (Date, bool) {
	s := lower(name)
	if zone == nil {
		zone = time.UTC
	}
	compact := func(at int) (time.Time, bool) { // YYYYMMDD_HHMMSS
		if len(s) < at+15 || s[at+8] != '_' || !boundary(s, at+15) {
			return time.Time{}, false
		}
		return wallFromDigits(s[at:at+4], s[at+4:at+6], s[at+6:at+8], s[at+9:at+11], s[at+11:at+13], s[at+13:at+15])
	}
	second := func(w time.Time, ok bool) (Date, bool) {
		if !ok {
			return Date{}, false
		}
		return fromWall(w, PrecisionSecond, zone, nil), true
	}
	switch {
	case strings.HasPrefix(s, "img_") || strings.HasPrefix(s, "vid_"):
		return second(compact(4))
	case strings.HasPrefix(s, "pxl_"):
		const at = 4 // YYYYMMDD_HHMMSSmmm
		if len(s) < at+18 || s[at+8] != '_' || !boundary(s, at+18) {
			return Date{}, false
		}
		w, ok := wallFromDigits(s[at:at+4], s[at+4:at+6], s[at+6:at+8], s[at+9:at+11], s[at+11:at+13], s[at+13:at+15])
		ms, ok2 := digits(s[at+15 : at+18])
		if !ok || !ok2 {
			return Date{}, false
		}
		return fromInstant(w.Add(time.Duration(ms)*time.Millisecond), zone), true
	case strings.HasPrefix(s, "img-") || strings.HasPrefix(s, "vid-"):
		const at = 4 // YYYYMMDD-WA
		if len(s) < at+11 || s[at+8:at+11] != "-wa" {
			return Date{}, false
		}
		w, ok := wallFromDigits(s[at:at+4], s[at+4:at+6], s[at+6:at+8], "00", "00", "00")
		if !ok {
			return Date{}, false
		}
		return fromWall(w, PrecisionDay, zone, nil), true
	case strings.HasPrefix(s, "screenshot_"):
		const at = 11
		r := s[at:]
		// YYYY-MM-DD-HH-MM-SS
		if len(r) >= 19 && r[4] == '-' && r[7] == '-' && r[10] == '-' && r[13] == '-' && r[16] == '-' && boundary(r, 19) {
			return second(wallFromDigits(r[0:4], r[5:7], r[8:10], r[11:13], r[14:16], r[17:19]))
		}
		// YYYYMMDD-HHMMSS
		if len(r) >= 15 && r[8] == '-' && boundary(r, 15) {
			return second(wallFromDigits(r[0:4], r[4:6], r[6:8], r[9:11], r[11:13], r[13:15]))
		}
		// YYYY-MM-DD
		if len(r) >= 10 && r[4] == '-' && r[7] == '-' && boundary(r, 10) {
			w, ok := wallFromDigits(r[0:4], r[5:7], r[8:10], "00", "00", "00")
			if !ok {
				return Date{}, false
			}
			return fromWall(w, PrecisionDay, zone, nil), true
		}
		return Date{}, false
	}
	// YYYYMMDD_HHMMSS
	if len(s) >= 15 && isDigit(s[0]) {
		if d, ok := second(compact(0)); ok {
			return d, true
		}
	}
	// YYYY-MM-DD HH.MM.SS
	if len(s) >= 19 && s[4] == '-' && s[7] == '-' && s[10] == ' ' && s[13] == '.' && s[16] == '.' && boundary(s, 19) {
		return second(wallFromDigits(s[0:4], s[5:7], s[8:10], s[11:13], s[14:16], s[17:19]))
	}
	return Date{}, false
}

// leadingDate finds a folder date's syntax at the start of a name (D6):
// "YYYY", "YYYY-MM", or "YYYY-MM-DD" (a valid date), followed by the end, a
// space, '-', '_', or '.'. It returns the wall time, precision, and length.
func leadingDate(s string) (time.Time, Precision, int, bool) {
	sep := func(i int) bool {
		if i >= len(s) {
			return true
		}
		switch s[i] {
		case ' ', '-', '_', '.':
			return true
		}
		return false
	}
	if len(s) < 4 {
		return time.Time{}, "", 0, false
	}
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' && sep(10) {
		if w, ok := wallFromDigits(s[0:4], s[5:7], s[8:10], "00", "00", "00"); ok {
			return w, PrecisionDay, 10, true
		}
	}
	if len(s) >= 7 && s[4] == '-' && sep(7) {
		if w, ok := wallFromDigits(s[0:4], s[5:7], "01", "00", "00", "00"); ok {
			return w, PrecisionMonth, 7, true
		}
	}
	if sep(4) {
		if w, ok := wallFromDigits(s[0:4], "01", "01", "00", "00", "00"); ok {
			return w, PrecisionYear, 4, true
		}
	}
	return time.Time{}, "", 0, false
}

// FolderDate is the date a folder's name carries (D6), with a year from
// 1990 to now's year in zone.
func FolderDate(name []byte, zone *time.Location, now time.Time) (Date, bool) {
	if zone == nil {
		zone = time.UTC
	}
	w, p, _, ok := leadingDate(string(name))
	if !ok || w.Year() < 1990 || w.Year() > now.In(zone).Year() {
		return Date{}, false
	}
	return fromWall(w, p, zone, nil), true
}
