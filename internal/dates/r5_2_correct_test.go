package dates

import (
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/dates/datestest"
	"precious/internal/domain"
	"precious/internal/media"
)

// r5 task 2.7, "R5.2 One bulk correction fixes all its photos": on the
// seeded corpus, with the Sony's camera bits set as detection sets them,
// one set-date-correction of the suggested shift for the Sony's photos in
// its two event folders applies 12; each photo's effective date is its
// truth plus the shift, on the Canon's timeline; camera_offset is clear;
// the source is dirty and its media job is requested.
func TestR5_2CorrectionFixesTheSony(t *testing.T) {
	e, _, truth := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	const shift = 31546800

	type photo struct {
		id    domain.EntryID
		truth time.Time
	}
	var sony []photo
	canon := map[string][2]time.Time{} // per event folder, the Canon's first and last capture
	for _, en := range truth.Entries {
		raw, err := en.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		p := string(raw)
		for _, dir := range []string{bahia, natal} {
			switch {
			case strings.HasPrefix(p, dir+"/DSC"):
				sony = append(sony, photo{id: e.id(corpusSource, p), truth: *en.Date.Effective})
			case strings.HasPrefix(p, dir+"/IMG_"):
				r := canon[dir]
				if r[0].IsZero() || en.Date.Effective.Before(r[0]) {
					r[0] = *en.Date.Effective
				}
				if en.Date.Effective.After(r[1]) {
					r[1] = *en.Date.Effective
				}
				canon[dir] = r
			}
		}
	}
	if len(sony) != 12 {
		t.Fatalf("the truth has %d Sony photos in the events", len(sony))
	}
	ids := make([]domain.EntryID, len(sony))
	for i, p := range sony {
		ids[i] = p.id
	}
	e.setCameraBitsB(corpusSource, ids)
	e.exec(`UPDATE media_sources SET dirty = 0 WHERE source_id = ?`, string(corpusSource))

	api := e.apiB()
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{
		"folder_ids": []string{e.ref(corpusSource, bahia), e.ref(corpusSource, natal)},
		"camera_key": sonyKey,
		"correction": map[string]any{"kind": "shift", "shift_s": shift},
	}), &got)
	if got.Applied != 12 || got.SkippedCount != 0 || len(got.Skipped) != 0 || got.BatchID == "" {
		t.Fatalf("the shift answered %+v, want 12 applied", got)
	}

	for _, p := range sony {
		d, ok := e.date(p.id)
		if !ok {
			t.Fatalf("entry %d has no date", p.id)
		}
		want := p.truth.Add(shift * time.Second)
		if !d.effective.Valid || d.effective.Int64 != want.UnixNano() || d.corrected.String != media.CorrectionShift {
			t.Errorf("entry %d: effective %v corrected %q, want %v shifted", p.id,
				time.Unix(0, d.effective.Int64).UTC(), d.corrected.String, want)
		}
		if d.flags&media.FlagCameraOffset != 0 {
			t.Errorf("entry %d keeps camera_offset", p.id)
		}
		if c, ok := e.correctionOfB(p.id); !ok || c.kind != "shift" || c.shiftS.Int64 != shift || c.batch != got.BatchID {
			t.Errorf("entry %d correction %+v", p.id, c)
		}
	}
	// The Canon's timeline: each event's Sony photos now lie within the
	// Canon's captures of that event.
	for _, dir := range []string{bahia, natal} {
		r := canon[dir]
		for _, p := range sony {
			d, _ := e.date(p.id)
			at := time.Unix(0, d.effective.Int64)
			if e.parentPathB(p.id) == dir && (at.Before(r[0]) || at.After(r[1])) {
				t.Errorf("entry %d in %s at %v, outside the Canon's %v–%v", p.id, dir, at.UTC(), r[0], r[1])
			}
		}
	}
	if n := e.count(`SELECT count(*) FROM media_dates WHERE flags & 4 <> 0`); n != 0 {
		t.Errorf("%d photos keep camera_offset", n)
	}
	if s := e.storedSummary(corpusSource); s.Flags.CameraOffset != 0 || s.BySource.EXIF != e.recounted(corpusSource).BySource.EXIF {
		t.Errorf("summary %+v", s)
	}
	e.checkSummary(corpusSource, "after the shift")
	if n := e.count(`SELECT dirty FROM media_sources WHERE source_id = ?`, string(corpusSource)); n != 1 {
		t.Errorf("dirty %d after the correction", n)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media' AND scope_key = 'media:corpus' AND state = 'queued'`); n != 1 {
		t.Errorf("%d media jobs queued", n)
	}
}

// r5 review, Addendum G2: the suggested shift takes exactly the photos
// detection flagged. Beside the Sony's 12, Bahia holds three more Sony
// photos detection is not given: one with the clock's reset date
// (implausible), one without a capture, and one the owner set. After the
// real media job, the camera target expands to the flagged photos; the
// set photo is skipped has_correction and keeps the owner's date, and the
// other two get no correction.
func TestCameraShiftTakesTheFlaggedPhotos(t *testing.T) {
	e, root, _ := newCorpusEnv(t)
	const shift = 31546800
	dir := root.Child("Viagens").Child("2010-07 Bahia")
	at := time.Date(2010, 7, 16, 9, 0, 0, 0, time.UTC)
	for name, original := range map[string]string{"DSC09001.JPG": "2000:01:01 00:00:00", "DSC09002.JPG": "",
		"DSC09003.JPG": "2009:07:16 06:00:00"} {
		dir.File(name, 0, at).Content(exifJPEGA(exifA{make: "SONY", model: "DSC-W55", original: original})).ModTime(at)
	}
	e.scan(corpusSource)
	reset, noCapture, set := e.id(corpusSource, bahia+"/DSC09001.JPG"), e.id(corpusSource, bahia+"/DSC09002.JPG"),
		e.id(corpusSource, bahia+"/DSC09003.JPG")
	api := e.apiB()
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_id": set.String(),
		"correction": map[string]any{"kind": "set", "local": "2010-07-17T12:00:00"}}), &got)
	e.mediaA(corpusSource)

	var flagged []domain.EntryID
	for _, p := range e.flaggedA(corpusSource) {
		flagged = append(flagged, e.id(corpusSource, p))
	}
	if len(flagged) != 12 {
		t.Fatalf("%d photos flagged camera_offset, want the Sony's 12", len(flagged))
	}
	targets := Targets{FolderIDs: []string{e.ref(corpusSource, bahia), e.ref(corpusSource, natal)}, CameraKey: sonyKey}
	exp, err := e.expandAs(targets, maxCorrectionMedia, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(exp.Media, flagged) {
		t.Errorf("the camera target takes %v, want the flagged %v", exp.Media, flagged)
	}

	before := map[domain.EntryID]storedDate{}
	for _, id := range []domain.EntryID{reset, noCapture, set} {
		before[id], _ = e.date(id)
	}
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"folder_ids": targets.FolderIDs, "camera_key": sonyKey,
		"correction": map[string]any{"kind": "shift", "shift_s": shift}}), &got)
	if got.Applied != 12 || got.SkippedCount != 1 || len(got.Skipped) != 1 || got.Skipped[0].EntryID != set.String() ||
		got.Skipped[0].Reason != "has_correction" {
		t.Fatalf("the shift answered %+v, want 12 applied and the set photo skipped has_correction", got)
	}
	if c, ok := e.correctionOfB(set); !ok || c.kind != "set" || c.setLocal.String != "2010-07-17T12:00:00" {
		t.Errorf("the set photo's correction is %+v", c)
	}
	for _, id := range []domain.EntryID{reset, noCapture} {
		if c, ok := e.correctionOfB(id); ok {
			t.Errorf("entry %d was given %+v", id, c)
		}
	}
	for id, d := range before {
		if now, _ := e.date(id); now.local != d.local || now.source != d.source {
			t.Errorf("entry %d's date moved from %+v to %+v", id, d, now)
		}
	}
}

// parentPathB returns the path of id's parent folder.
func (e *env) parentPathB(id domain.EntryID) string {
	e.t.Helper()
	var p []byte
	if err := e.st.Reader().QueryRow(`SELECT f.path FROM entries e JOIN entries f ON f.id = e.parent_id WHERE e.id = ?`,
		int64(id)).Scan(&p); err != nil {
		e.t.Fatal(err)
	}
	return string(p)
}
