package media

import (
	"slices"
	"sort"
	"time"
)

// Photo is one photo with a plausible EXIF capture and a camera key, as
// detection sees it (D8): its capture instant (after a shift correction),
// its folder, its GPS time, and its folder's date (D6), if any.
type Photo struct {
	Entry, Folder int64
	Camera        string
	Capture       time.Time
	OffsetKnown   bool
	GPS           *time.Time
	FolderDate    *Date
}

// Event is a camera's candidate in one folder (D8). DeltaS is its median
// capture minus the other cameras' (or minus its own GPS, for "own_gps"),
// and Reference what sides with the other cameras there: "gps",
// "folder_name", "own_gps", or "".
type Event struct {
	Folder    int64
	DeltaS    int64
	Photos    int
	SpanS     int64
	Reference string
}

// CameraResult is one camera's detection (D8). State is "ok", "offset", or
// "disagrees"; ShiftS is the suggestion, only for "offset", whose Events
// are exactly the folders whose photos it flags. Others are the cameras its
// events compared it with.
type CameraResult struct {
	Key    string
	Photos int
	State  string
	ShiftS *int64
	Events []Event
	Others []string
}

// Detection states and references.
const (
	CameraOK        = "ok"
	CameraOffset    = "offset"
	CameraDisagrees = "disagrees"

	ReferenceGPS        = "gps"
	ReferenceFolderName = "folder_name"
	ReferenceOwnGPS     = "own_gps"
)

const (
	eventSpan      = 24 * time.Hour   // a candidate's photos span less
	eventMargin    = 6 * time.Hour    // the other cameras' range is widened by this
	agreeWithin    = 10 * time.Minute // GPS and candidates agree within this
	ownGPSMin      = 3
	ownGPSKnown    = time.Hour      // own GPS difference beyond this, with a known offset
	ownGPSFloating = 14 * time.Hour // and without one
)

// candidate is an Event with the cameras it was compared with.
type candidate struct {
	Event
	others []string
}

// Detect finds the cameras whose clock is off (D8): reference-backed
// offsets first, then the counterparts again without them; cameras left
// with candidates disagree, and the rest are ok. Results are by key.
func Detect(photos []Photo) []CameraResult {
	count := map[string]int{}
	for _, p := range photos {
		if p.Camera != "" {
			count[p.Camera]++
		}
	}
	first := candidates(photos, nil)
	offset := map[string]bool{}
	results := map[string]*CameraResult{}
	for cam, cs := range first {
		if shift, ok := offsetOf(cs); ok {
			offset[cam] = true
			r := &CameraResult{Key: cam, Photos: count[cam], State: CameraOffset, ShiftS: &shift}
			r.Events, r.Others = eventsOf(cs)
			results[cam] = r
		}
	}
	second := candidates(photos, offset)
	for cam, cs := range second {
		if offset[cam] || len(cs) == 0 {
			continue
		}
		r := &CameraResult{Key: cam, Photos: count[cam], State: CameraDisagrees}
		r.Events, r.Others = eventsOf(cs)
		results[cam] = r
	}
	out := make([]CameraResult, 0, len(count))
	for cam, n := range count {
		if r, ok := results[cam]; ok {
			out = append(out, *r)
		} else {
			out = append(out, CameraResult{Key: cam, Photos: n, State: CameraOK})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// eventsOf lists candidates by folder, and the cameras they compared with.
func eventsOf(cs []candidate) ([]Event, []string) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Folder < cs[j].Folder })
	var events []Event
	var others []string
	for _, c := range cs {
		events = append(events, c.Event)
		others = append(others, c.others...)
	}
	slices.Sort(others)
	return events, slices.Compact(others)
}

// offsetOf decides whether a camera is offset (D8): its candidates agree
// within 10 minutes, and a reference sides with the other cameras (GPS or
// own GPS at one event; also the folder date at two or more). The
// suggestion is minus the median delta, rounded to the minute.
func offsetOf(cs []candidate) (int64, bool) {
	if len(cs) == 0 {
		return 0, false
	}
	deltas := make([]int64, len(cs))
	backed := false
	for i, c := range cs {
		deltas[i] = c.DeltaS
		switch c.Reference {
		case ReferenceGPS, ReferenceOwnGPS:
			backed = true
		case ReferenceFolderName:
			backed = backed || len(cs) >= 2
		}
	}
	slices.Sort(deltas)
	if time.Duration(deltas[len(deltas)-1]-deltas[0])*time.Second > agreeWithin || !backed {
		return 0, false
	}
	return roundMinute(-medianInt(deltas)), true
}

// candidates computes every camera's candidates in the events of the
// photos, leaving out the excluded cameras: a folder whose direct children
// hold photos of at least 2 cameras is an event.
func candidates(photos []Photo, exclude map[string]bool) map[string][]candidate {
	byFolder := map[int64]map[string][]Photo{}
	for _, p := range photos {
		if p.Camera == "" || exclude[p.Camera] {
			continue
		}
		m := byFolder[p.Folder]
		if m == nil {
			m = map[string][]Photo{}
			byFolder[p.Folder] = m
		}
		m[p.Camera] = append(m[p.Camera], p)
	}
	out := map[string][]candidate{}
	for folder, cams := range byFolder {
		if len(cams) < 2 {
			continue
		}
		var date *Date
		for _, ps := range cams {
			for _, p := range ps {
				if p.FolderDate != nil {
					date = p.FolderDate
				}
			}
		}
		for cam, own := range cams {
			var others []Photo
			var names []string
			for o, ps := range cams {
				if o != cam {
					others = append(others, ps...)
					names = append(names, o)
				}
			}
			slices.Sort(names)
			if c, ok := candidateIn(folder, own, others, date); ok {
				out[cam] = append(out[cam], candidate{Event: c, others: names})
			}
		}
	}
	return out
}

// candidateIn is a camera's candidate in one event: from its own GPS when
// that gives one, else when its photos span less than a day and all lie
// outside the other cameras' widened range.
func candidateIn(folder int64, own, others []Photo, date *Date) (Event, bool) {
	caps := captures(own)
	span := caps[len(caps)-1] - caps[0]
	ev := Event{Folder: folder, Photos: len(own), SpanS: span / int64(time.Second)}
	if d, ok := ownGPS(own); ok {
		ev.DeltaS, ev.Reference = d, ReferenceOwnGPS
		return ev, true
	}
	if time.Duration(span) >= eventSpan {
		return Event{}, false
	}
	oc := captures(others)
	lo, hi := oc[0]-int64(eventMargin), oc[len(oc)-1]+int64(eventMargin)
	for _, c := range caps {
		if c >= lo && c <= hi {
			return Event{}, false
		}
	}
	ev.DeltaS = roundSecond(medianInt(caps) - medianInt(oc))
	switch {
	case gpsAgrees(others):
		ev.Reference = ReferenceGPS
	case date != nil && containsAll(*date, others) && containsNone(*date, own):
		ev.Reference = ReferenceFolderName
	}
	return ev, true
}

// ownGPS is a camera's own-GPS delta in an event (D8): at least 3 photos
// with GPS whose capture − GPS all agree within 10 minutes, beyond 1 hour
// when their offsets are known, else beyond 14 hours.
func ownGPS(own []Photo) (int64, bool) {
	var diffs []int64
	known := true
	for _, p := range own {
		if p.GPS != nil {
			diffs = append(diffs, int64(p.Capture.Sub(*p.GPS)))
			known = known && p.OffsetKnown
		}
	}
	if len(diffs) < ownGPSMin {
		return 0, false
	}
	slices.Sort(diffs)
	if time.Duration(diffs[len(diffs)-1]-diffs[0]) > agreeWithin {
		return 0, false
	}
	m := medianInt(diffs)
	limit := ownGPSFloating
	if known {
		limit = ownGPSKnown
	}
	if abs(time.Duration(m)) <= limit {
		return 0, false
	}
	return roundSecond(m), true
}

// gpsAgrees reports whether the photos have GPS times, all within 10
// minutes of their captures.
func gpsAgrees(ps []Photo) bool {
	n := 0
	for _, p := range ps {
		if p.GPS == nil {
			continue
		}
		n++
		if abs(p.Capture.Sub(*p.GPS)) > agreeWithin {
			return false
		}
	}
	return n > 0
}

func containsAll(d Date, ps []Photo) bool {
	for _, p := range ps {
		if !d.contains(p.Capture, nil, 0) {
			return false
		}
	}
	return true
}

func containsNone(d Date, ps []Photo) bool {
	for _, p := range ps {
		if d.contains(p.Capture, nil, 0) {
			return false
		}
	}
	return true
}

// captures are the photos' capture instants in nanoseconds, sorted.
func captures(ps []Photo) []int64 {
	out := make([]int64, len(ps))
	for i, p := range ps {
		out[i] = p.Capture.UnixNano()
	}
	slices.Sort(out)
	return out
}

// medianInt is the median of sorted values; the mean of the middle two for
// an even count.
func medianInt(v []int64) int64 {
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	a, b := v[n/2-1], v[n/2]
	return a + (b-a)/2
}

// roundSecond turns nanoseconds into seconds, rounded half away from zero.
func roundSecond(ns int64) int64 { return roundDiv(ns, int64(time.Second)) }

// roundMinute rounds seconds to a whole minute, half away from zero.
func roundMinute(s int64) int64 { return roundDiv(s, 60) * 60 }

func roundDiv(v, d int64) int64 {
	if v < 0 {
		return -((-v + d/2) / d)
	}
	return (v + d/2) / d
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
