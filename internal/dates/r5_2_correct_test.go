package dates

import (
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
