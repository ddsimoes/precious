package media

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"path"
	"strings"
	"time"
)

// DeriveVersion is part of every inputs key: raising it re-derives every
// date (D9).
const DeriveVersion = 2

// Inputs are the facts a file's effective date is derived from (D9).
type Inputs struct {
	Path  []byte     // relative to the source's top
	Mtime *time.Time // nil when unknown
	Caps  struct {
		LocalTime  bool
		Resolution time.Duration
	}
	MetaState  MetaState // "pending" when there is no row
	Meta       *Meta     // non-nil only for MetaState "read"
	Correction *Correction
	Zone       *time.Location
	Now        time.Time // plausibility bound; not part of the key
}

// Candidate is one source's date, and whether it is plausible (D5). An
// owner's date is always plausible.
type Candidate struct {
	Source    Source
	Date      Date
	Plausible bool
}

// Effective is a file's effective date (D5): Date is nil for source
// "none". Corrected names the correction kind that applies, or "".
type Effective struct {
	Date       *Date
	Source     Source
	Confidence Confidence
	Refined    bool
	Corrected  string
	Flags      Flags
	Candidates []Candidate
}

// Plausibility bounds (D5, ADR 0012).
var (
	firstPlausibleYear = 1990
	cameraDefaults     = []int{1970, 1980, 2000, 2001}
)

const (
	futureSlack    = 24 * time.Hour
	disagreeAfter  = 24 * time.Hour
	localTimeSlack = time.Hour
)

// isPlausible is D5's test: not before 1990 (by the wall time), not after
// now plus one day, and not exactly midnight of a camera default's January
// 1st (by the wall time or in UTC).
func isPlausible(d Date, now time.Time) bool {
	if len(d.Local) < 4 {
		return false
	}
	if y, ok := digits(d.Local[:4]); !ok || y < firstPlausibleYear {
		return false
	}
	if d.Instant.After(now.Add(futureSlack)) {
		return false
	}
	if d.Precision == PrecisionSecond {
		u := d.Instant.UTC()
		for _, y := range cameraDefaults {
			if d.Local == fmt.Sprintf("%04d-01-01T00:00:00", y) || u.Equal(time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)) {
				return false
			}
		}
	}
	return true
}

// captureDate is the EXIF capture as a Date: an instant when its offset is
// known, else a wall time in zone (D5, D7).
func captureDate(m *Meta, zone *time.Location) (Date, bool) {
	if m == nil || len(m.CaptureLocal) < 19 {
		return Date{}, false
	}
	w, p, err := parseWall(m.CaptureLocal[:19])
	if err != nil || p != PrecisionSecond {
		return Date{}, false
	}
	if rest := m.CaptureLocal[19:]; rest != "" {
		ms, ok := 0, len(rest) == 4 && rest[0] == '.'
		if ok {
			ms, ok = digits(rest[1:])
		}
		if !ok {
			return Date{}, false
		}
		w = w.Add(time.Duration(ms) * time.Millisecond)
	}
	if m.CaptureOffsetMin != nil && (*m.CaptureOffsetMin < -840 || *m.CaptureOffsetMin > 840) {
		return Date{}, false
	}
	d := fromWall(w, PrecisionSecond, zoneOf(zone, m.CaptureOffsetMin), m.CaptureOffsetMin)
	return d, true
}

// nearestFolderDate is the date of the nearest dated ancestor folder below
// the source's top (D6).
func nearestFolderDate(p []byte, zone *time.Location, now time.Time) (Date, bool) {
	parts := strings.Split(string(p), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		if d, ok := FolderDate([]byte(parts[i]), zone, now); ok {
			return d, true
		}
	}
	return Date{}, false
}

// Derive computes a file's effective date (D5, D6, D8, D11). It never sets
// FlagCameraOffset, and the owner's date bypasses plausibility.
func Derive(in Inputs) Effective {
	zone := in.Zone
	if zone == nil {
		zone = time.UTC
	}
	var cands []Candidate
	add := func(src Source, d Date, ok bool) *Candidate {
		if !ok {
			return nil
		}
		cands = append(cands, Candidate{Source: src, Date: d, Plausible: isPlausible(d, in.Now)})
		return &cands[len(cands)-1]
	}
	var meta *Meta
	if in.MetaState == MetaRead {
		meta = in.Meta
	}
	exif, gps, container := -1, -1, -1
	if d, ok := captureDate(meta, zone); ok {
		add(SourceEXIF, d, true)
		exif = len(cands) - 1
	}
	if meta != nil && meta.GPS != nil {
		add(SourceGPS, fromInstant(*meta.GPS, zone), true)
		gps = len(cands) - 1
	}
	if meta != nil && meta.Container != nil {
		add(SourceContainer, fromInstant(*meta.Container, zone), true)
		container = len(cands) - 1
	}
	name, folder := -1, -1
	if d, ok := NameDate([]byte(path.Base(string(in.Path))), zone); ok {
		add(SourceFileName, d, true)
		name = len(cands) - 1
	}
	if d, ok := nearestFolderDate(in.Path, zone, in.Now); ok {
		add(SourceFolderName, d, true)
		folder = len(cands) - 1
	}
	mtime := -1
	if in.Mtime != nil {
		add(SourceMtime, fromInstant(*in.Mtime, zone), true)
		mtime = len(cands) - 1
	}

	tol := time.Duration(0)
	if in.Caps.LocalTime {
		tol = localTimeSlack
	}
	// take turns candidate i into the effective date, refined by the
	// modification time when coarser than a second and containing it.
	take := func(i int) Effective {
		c := cands[i]
		d := c.Date
		e := Effective{Source: c.Source}
		switch c.Source {
		case SourceEXIF:
			e.Confidence = ConfidenceMedium
			if d.OffsetMin != nil {
				e.Confidence = ConfidenceHigh
			}
		case SourceGPS:
			e.Confidence = ConfidenceHigh
		case SourceContainer:
			e.Confidence = ConfidenceMedium
		case SourceFileName, SourceFolderName:
			e.Confidence = ConfidenceLow
			if d.Precision != PrecisionSecond && in.Mtime != nil && d.contains(*in.Mtime, zone, tol) {
				d = fromInstant(*in.Mtime, zone)
				e.Refined, e.Confidence = true, ConfidenceMedium
			}
		case SourceMtime:
			e.Confidence = ConfidenceLowest
		}
		e.Date = &d
		return e
	}

	// The first plausible candidate wins; a known modification time is the
	// last resort even when implausible.
	e := Effective{Source: SourceNone, Confidence: ConfidenceNone}
	for i, c := range cands {
		if c.Plausible {
			e = take(i)
			break
		}
	}
	if e.Date == nil && mtime >= 0 {
		e = take(mtime)
	}

	if c := in.Correction; c != nil {
		switch c.Kind {
		case CorrectionSet:
			if d, err := ParseLocal(c.SetLocal, zone, c.SetOffsetMin); err == nil {
				e = owner(d, ConfidenceHigh, CorrectionSet)
			}
		case CorrectionShift:
			base := e.Date
			if exif >= 0 {
				base = &cands[exif].Date
			}
			if base != nil {
				if d, ok := shift(*base, c.ShiftS, zone); ok {
					e = owner(d, ConfidenceMedium, CorrectionShift)
				}
			}
		case CorrectionUseName:
			if name >= 0 {
				e = take(name)
				e.Corrected = CorrectionUseName
			}
		case CorrectionUseFolder:
			if folder >= 0 {
				e = take(folder)
				e.Corrected = CorrectionUseFolder
			}
		}
	}
	e.Candidates = cands
	if e.Source == SourceOwner {
		e.Candidates = append([]Candidate{{Source: SourceOwner, Date: *e.Date, Plausible: true}}, cands...)
	}

	metaFound := false
	for _, i := range []int{exif, gps, container} {
		if i < 0 {
			continue
		}
		metaFound = true
		if !cands[i].Plausible {
			e.Flags |= FlagImplausible
		}
	}
	if !metaFound && (in.MetaState == MetaRead || in.MetaState == MetaNone) {
		e.Flags |= FlagNoDateMetadata
	}
	switch e.Source {
	case SourceEXIF, SourceGPS, SourceContainer, SourceOwner:
		if in.Mtime != nil && e.Date != nil && e.Date.distance(*in.Mtime, zone) > disagreeAfter+tol {
			e.Flags |= FlagMtimeDisagrees
		}
	}
	return e
}

// distance is how far t lies from the date: from its instant at second
// precision, else from its period.
func (d Date) distance(t time.Time, zone *time.Location) time.Duration {
	start, end := d.Instant, d.Instant
	if d.Precision != PrecisionSecond {
		end = d.end(zone)
	}
	switch {
	case t.Before(start):
		return start.Sub(t)
	case t.After(end):
		return t.Sub(end)
	}
	return 0
}

func owner(d Date, c Confidence, kind string) Effective {
	return Effective{Date: &d, Source: SourceOwner, Confidence: c, Corrected: kind}
}

// shift moves a date by s seconds, keeping its precision and offset; a date
// coarser than a second starts its new period. A result whose year (as it
// reads where it was taken) lies outside minYear–maxYear is refused, as P4
// bounds every date read or set, so its instant fits an int64 of
// nanoseconds (Addendum G9).
func shift(d Date, s int64, zone *time.Location) (Date, bool) {
	loc := zoneOf(zone, d.OffsetMin)
	t := d.Instant.Add(time.Duration(s) * time.Second).In(loc)
	if y := t.Year(); y < minYear || y > maxYear {
		return Date{}, false
	}
	if d.Precision == PrecisionSecond {
		return Date{Instant: t.UTC(), Local: t.Format(localSecond), OffsetMin: copyInt(d.OffsetMin), Precision: PrecisionSecond}, true
	}
	w, _, _ := parseWall(t.Format(layouts[d.Precision]))
	return fromWall(w, d.Precision, loc, d.OffsetMin), true
}

// InputsKey is the FNV-64a of every input but Now (D9).
func InputsKey(in Inputs) uint64 {
	h := fnv.New64a()
	k := keyWriter{h}
	k.int(DeriveVersion)
	k.bytes(in.Path)
	if in.Mtime != nil {
		k.int(1)
		k.int(in.Mtime.UnixNano())
	} else {
		k.int(0)
	}
	k.bool(in.Caps.LocalTime)
	k.int(int64(in.Caps.Resolution))
	k.str(string(in.MetaState))
	if m := in.Meta; m != nil && in.MetaState == MetaRead {
		k.int(1)
		k.str(m.CaptureLocal)
		k.intp(m.CaptureOffsetMin)
		k.timep(m.GPS)
		k.timep(m.Container)
		k.str(m.Make)
		k.str(m.Model)
		k.str(m.Serial)
	} else {
		k.int(0)
	}
	if c := in.Correction; c != nil {
		k.int(1)
		k.str(c.Kind)
		k.str(c.SetLocal)
		k.intp(c.SetOffsetMin)
		k.int(c.ShiftS)
	} else {
		k.int(0)
	}
	k.int(int64(ZoneKey(in.Zone)))
	return h.Sum64()
}

// ZoneKey identifies a resolved zone (D7): its name, and its offsets on
// January 1 and July 1 of every year from 1990 to 2040.
func ZoneKey(loc *time.Location) uint64 {
	if loc == nil {
		loc = time.UTC
	}
	h := fnv.New64a()
	k := keyWriter{h}
	k.str(loc.String())
	for y := 1990; y <= 2040; y++ {
		for _, m := range []time.Month{time.January, time.July} {
			_, off := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).In(loc).Zone()
			k.int(int64(off))
		}
	}
	return h.Sum64()
}

// keyWriter writes length-prefixed fields, so no two inputs share a key
// by concatenation.
type keyWriter struct{ h hash.Hash64 }

func (k keyWriter) int(v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	k.h.Write(b[:])
}

func (k keyWriter) bytes(b []byte) {
	k.int(int64(len(b)))
	k.h.Write(b)
}

func (k keyWriter) str(s string) { k.bytes([]byte(s)) }

func (k keyWriter) bool(v bool) {
	if v {
		k.int(1)
	} else {
		k.int(0)
	}
}

func (k keyWriter) intp(p *int) {
	if p == nil {
		k.int(0)
		return
	}
	k.int(1)
	k.int(int64(*p))
}

func (k keyWriter) timep(t *time.Time) {
	if t == nil {
		k.int(0)
		return
	}
	k.int(1)
	k.int(t.UnixNano())
}

// CameraKey is a camera's key (D8): "make|model|serial", each part trimmed
// of spaces and NULs, with '|' in a part replaced by '/'. A photo with
// neither make, model, nor serial has no key ("").
func CameraKey(make, model, serial string) string {
	clean := func(s string) string {
		return strings.ReplaceAll(strings.Trim(s, " \x00"), "|", "/")
	}
	mk, md, sn := clean(make), clean(model), clean(serial)
	if mk == "" && md == "" && sn == "" {
		return ""
	}
	return mk + "|" + md + "|" + sn
}
