package media

import (
	"slices"
	"testing"
	"time"
)

const (
	canon = "Canon|Canon PowerShot SX230 HS|"
	sony  = "SONY|DSC-W55|"
	nikon = "NIKON|COOLPIX P5000|3012345"
	year  = 365 * 24 * time.Hour
	sonyS = int64(31546800) // 365 days 3 hours
)

type shot struct {
	at  time.Time
	gps *time.Time // nil: none
}

// shots of a camera in a folder, at hours after base; withGPS marks the
// first n as carrying GPS equal to the true time (true = capture + fix).
func shots(base time.Time, hours []float64, clock time.Duration, gpsN int) []shot {
	var out []shot
	for i, h := range hours {
		truth := base.Add(time.Duration(h * float64(time.Hour)))
		s := shot{at: truth.Add(clock)}
		if i < gpsN {
			g := truth
			s.gps = &g
		}
		out = append(out, s)
	}
	return out
}

type detector struct {
	photos []Photo
	next   int64
}

func (d *detector) add(folder int64, cam string, fd *Date, known bool, ss []shot) {
	for _, s := range ss {
		d.next++
		d.photos = append(d.photos, Photo{Entry: d.next, Folder: folder, Camera: cam, Capture: s.at, OffsetKnown: known, GPS: s.gps, FolderDate: fd})
	}
}

func folderDate(t *testing.T, name string) *Date {
	t.Helper()
	d, ok := FolderDate([]byte(name), time.UTC, now)
	if !ok {
		t.Fatalf("no folder date in %q", name)
	}
	return &d
}

func result(t *testing.T, rs []CameraResult, key string) CameraResult {
	t.Helper()
	for _, r := range rs {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no result for %s in %+v", key, rs)
	return CameraResult{}
}

func folders(r CameraResult) []int64 {
	var out []int64
	for _, e := range r.Events {
		out = append(out, e.Folder)
	}
	return out
}

func wantState(t *testing.T, name string, r CameraResult, state string, shift *int64) {
	t.Helper()
	if r.State != state || (shift == nil) != (r.ShiftS == nil) || (shift != nil && *shift != *r.ShiftS) {
		got := "nil"
		if r.ShiftS != nil {
			got = time.Duration(*r.ShiftS * int64(time.Second)).String()
		}
		t.Errorf("%s: %s is %s shift %s; want %s %v", name, r.Key, r.State, got, state, shift)
	}
}

func i64(v int64) *int64 { return &v }

// TestDetectCorpus is the corpus's shape (D8, D19): the Sony is offset by
// +1 year 3 hours in Bahia and Natal, backed by the Canon's GPS, and the
// Canon and the Nikon are ok.
func TestDetectCorpus(t *testing.T) {
	const bahia, natal, ouro, solo = 10, 20, 30, 40
	var d detector
	bahiaDay := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)
	natalDay := time.Date(2010, 12, 24, 19, 0, 0, 0, time.UTC)
	d.add(bahia, canon, folderDate(t, "2010-07 Bahia"), false, shots(bahiaDay, []float64{0, 1, 2, 3}, 0, 2))
	d.add(bahia, sony, folderDate(t, "2010-07 Bahia"), false, shots(bahiaDay, []float64{0, 0.5, 1, 2, 2.5, 3}, -time.Duration(sonyS)*time.Second, 0))
	d.add(natal, canon, folderDate(t, "2010-12 Natal"), false, shots(natalDay, []float64{0, 1, 2, 3}, 0, 1))
	d.add(natal, sony, folderDate(t, "2010-12 Natal"), false, shots(natalDay, []float64{0, 0.5, 1, 2, 2.5, 3}, -time.Duration(sonyS)*time.Second, 0))
	d.add(ouro, nikon, folderDate(t, "2008-03 Ouro Preto"), true, shots(time.Date(2008, 3, 22, 17, 0, 0, 0, time.UTC), []float64{0, 1. / 6, 2. / 6}, 0, 0))
	rs := Detect(d.photos)
	if len(rs) != 3 || rs[0].Key != canon || rs[1].Key != nikon || rs[2].Key != sony {
		t.Fatalf("results %+v", rs)
	}
	s := result(t, rs, sony)
	wantState(t, "corpus", s, CameraOffset, i64(sonyS))
	if s.Photos != 12 || !slices.Equal(folders(s), []int64{bahia, natal}) || !slices.Equal(s.Others, []string{canon}) {
		t.Errorf("Sony %+v", s)
	}
	for _, e := range s.Events {
		if e.Reference != ReferenceGPS || e.DeltaS != -sonyS || e.Photos != 6 || e.SpanS != 3*3600 {
			t.Errorf("Sony event %+v", e)
		}
	}
	for _, k := range []string{canon, nikon} {
		r := result(t, rs, k)
		wantState(t, "corpus", r, CameraOK, nil)
		if len(r.Events) != 0 {
			t.Errorf("%s has events %+v", k, r.Events)
		}
	}
	if c := result(t, rs, canon); c.Photos != 8 {
		t.Errorf("Canon photos %d", c.Photos)
	}

	// The Sony's photos in a folder of its own are not flagged: its events
	// stay exactly the two folders.
	d.add(solo, sony, nil, false, shots(time.Date(2010, 9, 1, 12, 0, 0, 0, time.UTC), []float64{0, 1, 2}, -time.Duration(sonyS)*time.Second, 0))
	s = result(t, Detect(d.photos), sony)
	wantState(t, "with a solo folder", s, CameraOffset, i64(sonyS))
	if s.Photos != 15 || !slices.Equal(folders(s), []int64{bahia, natal}) {
		t.Errorf("Sony with a solo folder %+v", s)
	}
}

func TestDetectOneEvent(t *testing.T) {
	day := time.Date(2012, 5, 5, 10, 0, 0, 0, time.UTC)
	hours := []float64{0, 1, 2, 3}

	// The other camera's GPS backs one event.
	var d detector
	d.add(1, canon, nil, false, shots(day, hours, 0, 4))
	d.add(1, sony, nil, false, shots(day, hours, -year, 0))
	rs := Detect(d.photos)
	wantState(t, "other GPS", result(t, rs, sony), CameraOffset, i64(int64(year/time.Second)))
	wantState(t, "other GPS", result(t, rs, canon), CameraOK, nil)

	// Own GPS backs one event: the Sony's GPS is 2 days after its clock.
	d = detector{}
	d.add(1, canon, nil, false, shots(day, hours, 0, 0))
	d.add(1, sony, nil, false, shots(day, hours, -48*time.Hour, 3))
	rs = Detect(d.photos)
	s := result(t, rs, sony)
	wantState(t, "own GPS", s, CameraOffset, i64(48*3600))
	if len(s.Events) != 1 || s.Events[0].Reference != ReferenceOwnGPS || s.Events[0].DeltaS != -48*3600 {
		t.Errorf("own GPS events %+v", s.Events)
	}
	wantState(t, "own GPS", result(t, rs, canon), CameraOK, nil)

	// Own GPS scattered, as a stale fix gives: no own-GPS candidate, and no
	// reference for the plain one.
	d = detector{}
	d.add(1, canon, nil, false, shots(day, hours, 0, 0))
	scattered := shots(day, hours, -48*time.Hour, 3)
	*scattered[1].gps = scattered[1].gps.Add(3 * time.Hour)
	*scattered[2].gps = scattered[2].gps.Add(-5 * time.Hour)
	d.add(1, sony, nil, false, scattered)
	rs = Detect(d.photos)
	wantState(t, "own GPS scattered", result(t, rs, sony), CameraDisagrees, nil)
	wantState(t, "own GPS scattered", result(t, rs, canon), CameraDisagrees, nil)

	// Own GPS 2 hours off: enough with a known offset, not without one.
	for _, known := range []bool{true, false} {
		d = detector{}
		d.add(1, canon, nil, true, shots(day, hours, 0, 0))
		d.add(1, sony, nil, known, shots(day, hours, -2*time.Hour, 4))
		s := result(t, Detect(d.photos), sony)
		if known {
			wantState(t, "own GPS 2 h, offset known", s, CameraOffset, i64(2*3600))
		} else {
			wantState(t, "own GPS 2 h, floating", s, CameraOK, nil)
		}
	}

	// Only a folder date, at one event: no suggestion, both disagree.
	fd := folderDate(t, "2012-05 Festa")
	d = detector{}
	d.add(1, canon, fd, false, shots(day, hours, 0, 0))
	d.add(1, sony, fd, false, shots(day, hours, -year, 0))
	rs = Detect(d.photos)
	wantState(t, "one folder date", result(t, rs, sony), CameraDisagrees, nil)
	wantState(t, "one folder date", result(t, rs, canon), CameraDisagrees, nil)

	// "Two cameras and no reference": a day apart, one event.
	d = detector{}
	d.add(1, canon, nil, false, shots(day, hours, 0, 0))
	d.add(1, sony, nil, false, shots(day, hours, -24*time.Hour, 0))
	rs = Detect(d.photos)
	for _, k := range []string{canon, sony} {
		r := result(t, rs, k)
		wantState(t, "tie", r, CameraDisagrees, nil)
		if len(r.Events) != 1 || r.Events[0].Reference != "" || len(r.Others) != 1 {
			t.Errorf("tie %s %+v", k, r)
		}
	}

	// An offset of 5 hours is not detected, even against GPS.
	d = detector{}
	d.add(1, canon, nil, false, shots(day, hours, 0, 4))
	d.add(1, sony, nil, false, shots(day, hours, -5*time.Hour, 0))
	rs = Detect(d.photos)
	wantState(t, "5 hours", result(t, rs, sony), CameraOK, nil)
	wantState(t, "5 hours", result(t, rs, canon), CameraOK, nil)

	// A camera used on one day of a week-long trip: neither is flagged.
	d = detector{}
	var week []float64
	for h := 0.0; h < 7*24; h += 6 {
		week = append(week, h)
	}
	d.add(1, canon, nil, false, shots(day, week, 0, 4))
	d.add(1, sony, nil, false, shots(day.Add(72*time.Hour), hours, 0, 0))
	rs = Detect(d.photos)
	wantState(t, "one day of a week", result(t, rs, sony), CameraOK, nil)
	wantState(t, "one day of a week", result(t, rs, canon), CameraOK, nil)

	// Photos on the 1st of the next month in a "2010-07 X" folder: the
	// folder date sides with the July camera, but one event is not enough.
	fd = folderDate(t, "2010-07 Praia")
	d = detector{}
	d.add(1, canon, fd, false, shots(time.Date(2010, 7, 31, 14, 0, 0, 0, time.UTC), []float64{0, 2, 4, 6}, 0, 0))
	d.add(1, sony, fd, false, shots(time.Date(2010, 8, 1, 9, 0, 0, 0, time.UTC), []float64{0, 1, 2}, 0, 0))
	for _, r := range Detect(d.photos) {
		if r.State == CameraOffset || r.ShiftS != nil {
			t.Errorf("next month: %s is %s", r.Key, r.State)
		}
	}

	// A correct camera's photos on both sides of a phone's, more than 6
	// hours away each (G6): the phone, with GPS, shoots 12:00–12:30; the
	// camera at 04:00, 04:10, 20:00, 20:10. Its medians are 10 minutes
	// apart, which is no clock offset: no candidate, no suggestion.
	noon := time.Date(2012, 5, 5, 12, 0, 0, 0, time.UTC)
	d = detector{}
	d.add(1, canon, nil, false, shots(noon, []float64{0, 0.25, 0.5}, 0, 3))
	d.add(1, sony, nil, false, shots(noon, []float64{-8, -8 + 10.0/60, 8, 8 + 10.0/60}, 0, 0))
	rs = Detect(d.photos)
	for _, k := range []string{canon, sony} {
		if r := result(t, rs, k); r.State != CameraOK || r.ShiftS != nil || len(r.Events) != 0 {
			t.Errorf("both sides: %s is %s, shift %v, events %+v; want ok with none", k, r.State, r.ShiftS, r.Events)
		}
	}
}

func TestDetectTwoEvents(t *testing.T) {
	a := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)
	b := time.Date(2010, 12, 24, 19, 0, 0, 0, time.UTC)
	hours := []float64{0, 1, 2, 3}

	// Folder dates at two events back the Canon's timeline.
	var d detector
	d.add(1, canon, folderDate(t, "2010-07 Bahia"), false, shots(a, hours, 0, 0))
	d.add(1, sony, folderDate(t, "2010-07 Bahia"), false, shots(a, hours, -year, 0))
	d.add(2, canon, folderDate(t, "2010-12 Natal"), false, shots(b, hours, 0, 0))
	d.add(2, sony, folderDate(t, "2010-12 Natal"), false, shots(b, hours, -year, 0))
	rs := Detect(d.photos)
	s := result(t, rs, sony)
	wantState(t, "two folder dates", s, CameraOffset, i64(int64(year/time.Second)))
	if !slices.Equal(folders(s), []int64{1, 2}) || s.Events[0].Reference != ReferenceFolderName {
		t.Errorf("two folder dates %+v", s)
	}
	wantState(t, "two folder dates", result(t, rs, canon), CameraOK, nil)

	// "Two events and no reference": both disagree, neither is offset.
	d = detector{}
	d.add(1, canon, nil, false, shots(a, hours, 0, 0))
	d.add(1, sony, nil, false, shots(a, hours, -year, 0))
	d.add(2, canon, nil, false, shots(b, hours, 0, 0))
	d.add(2, sony, nil, false, shots(b, hours, -year, 0))
	rs = Detect(d.photos)
	for _, k := range []string{canon, sony} {
		r := result(t, rs, k)
		wantState(t, "no reference", r, CameraDisagrees, nil)
		if !slices.Equal(folders(r), []int64{1, 2}) {
			t.Errorf("no reference %s events %+v", k, r.Events)
		}
	}

	// Inconstant candidates: a year in one event, two in the other.
	d = detector{}
	d.add(1, canon, nil, false, shots(a, hours, 0, 4))
	d.add(1, sony, nil, false, shots(a, hours, -year, 0))
	d.add(2, canon, nil, false, shots(b, hours, 0, 4))
	d.add(2, sony, nil, false, shots(b, hours, -2*year, 0))
	rs = Detect(d.photos)
	wantState(t, "inconstant", result(t, rs, sony), CameraDisagrees, nil)
	wantState(t, "inconstant", result(t, rs, canon), CameraDisagrees, nil)

	// Candidates 5 minutes apart still agree; the suggestion is their
	// median, rounded to the minute.
	d = detector{}
	d.add(1, canon, nil, false, shots(a, hours, 0, 4))
	d.add(1, sony, nil, false, shots(a, hours, -year, 0))
	d.add(2, canon, nil, false, shots(b, hours, 0, 4))
	d.add(2, sony, nil, false, shots(b, hours, -year-5*time.Minute-20*time.Second, 0))
	s = result(t, Detect(d.photos), sony)
	wantState(t, "close candidates", s, CameraOffset, i64(int64(year/time.Second)+3*60))
}

func TestDetectIgnoresPhotosWithoutCamera(t *testing.T) {
	day := time.Date(2012, 5, 5, 10, 0, 0, 0, time.UTC)
	var d detector
	d.add(1, "", nil, false, shots(day, []float64{0, 1}, -year, 0))
	d.add(1, canon, nil, false, shots(day, []float64{0, 1}, 0, 2))
	rs := Detect(d.photos)
	if len(rs) != 1 || rs[0].Key != canon || rs[0].State != CameraOK {
		t.Errorf("results %+v", rs)
	}
	if len(Detect(nil)) != 0 {
		t.Error("results without photos")
	}
}
