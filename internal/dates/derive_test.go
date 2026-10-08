package dates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/corpus"
	"precious/internal/dates/datestest"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/media"
)

// storedDate is a media_dates row as the tests compare it.
type storedDate struct {
	effective  sql.NullInt64
	local      sql.NullString
	precision  sql.NullString
	source     string
	refined    bool
	corrected  sql.NullString
	flags      media.Flags
	computedAt int64
}

func (e *env) date(id domain.EntryID) (storedDate, bool) {
	e.t.Helper()
	var d storedDate
	err := e.st.Reader().QueryRow(`SELECT effective_ns, local, precision, source, refined, corrected, flags, computed_at
		FROM media_dates WHERE entry_id = ?`, int64(id)).Scan(&d.effective, &d.local, &d.precision, &d.source, &d.refined,
		&d.corrected, &d.flags, &d.computedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedDate{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return d, true
}

// flagNames names flags as the truth does, in bit order.
func flagNames(f media.Flags) []string {
	out := []string{}
	for i, n := range []string{"mtime_disagrees", "implausible", "camera_offset", "no_date_metadata"} {
		if f&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// checkSummary fails when src's stored summary differs from a recount.
func (e *env) checkSummary(src domain.SourceID, when string) {
	e.t.Helper()
	if got, want := e.storedSummary(src), e.recounted(src); got != want {
		e.t.Errorf("%s: summary %+v, a recount %+v", when, got, want)
	}
}

// r5 task 1.6, D9, D10: Rederive writes each media file's date from its
// inputs, as the truth records it (without the cameras pass); skips a row
// whose inputs key is unchanged; keeps the camera_offset bit unless a set
// or shift correction applies; deletes the rows of missing, quarantined,
// and non-media entries; and keeps the summary equal to a full recount.
func TestRederive(t *testing.T) {
	e, root, truth := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)

	// Every media file's date is its truth's, but for camera_offset.
	all := e.mediaIDs(corpusSource)
	var nMedia int
	for _, en := range truth.Entries {
		if en.Date == nil {
			continue
		}
		nMedia++
		raw, err := en.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		d, ok := e.date(e.id(corpusSource, string(raw)))
		if !ok {
			t.Errorf("%s: no date", raw)
			continue
		}
		want := en.Date
		got := corpus.DateTruth{Source: d.source, Refined: d.refined, Local: d.local.String, Precision: d.precision.String,
			Flags: flagNames(d.flags)}
		var wantFlags []string
		for _, f := range want.Flags {
			if f != "camera_offset" {
				wantFlags = append(wantFlags, f)
			}
		}
		if got.Source != want.Source || got.Refined != want.Refined || got.Local != want.Local ||
			got.Precision != want.Precision || strings.Join(got.Flags, ",") != strings.Join(wantFlags, ",") {
			t.Errorf("%s: %+v, truth %+v", raw, got, *want)
		}
		if (want.Effective == nil) != !d.effective.Valid ||
			want.Effective != nil && want.Effective.UnixNano() != d.effective.Int64 {
			t.Errorf("%s: effective %v, truth %v", raw, d.effective, want.Effective)
		}
	}
	if len(all) != nMedia || e.count(`SELECT count(*) FROM media_dates`) != nMedia {
		t.Errorf("%d media, %d dates; truth %d", len(all), e.count(`SELECT count(*) FROM media_dates`), nMedia)
	}
	if s := e.storedSummary(corpusSource); s.Media != int64(nMedia) || s.BySource.EXIF == 0 || s.Metadata.None == 0 {
		t.Errorf("summary %+v", s)
	}
	e.checkSummary(corpusSource, "after the seed")

	// Unchanged inputs: nothing is written again.
	e.clk.set(testNow.Add(time.Hour))
	later := clock.Millis(testNow.Add(time.Hour))
	e.rederive(all...)
	if n := e.count(`SELECT count(*) FROM media_dates WHERE computed_at = ?`, later); n != 0 {
		t.Errorf("%d rows written again with unchanged inputs", n)
	}

	// The cameras pass flags the Sony's photos (as task 2.1 does); a
	// changed input keeps the bit, a set or shift correction clears it.
	sony := func(dir string, n int) domain.EntryID {
		return e.id(corpusSource, fmt.Sprintf("%s/DSC%05d.JPG", dir, n))
	}
	e.exec(`UPDATE media_dates SET flags = flags | 4 WHERE camera_key = ?`, sonyKey)
	if n := e.count(`SELECT count(*) FROM media_dates WHERE flags & 4 <> 0`); n != 12 {
		t.Fatalf("%d photos flagged camera_offset, want the Sony's 12", n)
	}
	e.write(func(tx *jobs.Tx) error {
		s, err := recount(context.Background(), tx.SQL(), corpusSource)
		if err != nil {
			return err
		}
		return writeSummary(context.Background(), tx.SQL(), corpusSource, s)
	})
	kept, set, shifted := sony(bahia, 301), sony(bahia, 302), sony(natal, 401)
	before, _ := e.date(shifted)
	for id, c := range map[domain.EntryID]string{
		kept:    `'use_name', NULL, NULL`, // no name date: it does not apply, but the inputs change
		set:     `'set', '2010-07-17', NULL`,
		shifted: `'shift', NULL, 31546800`,
	} {
		e.exec(`INSERT INTO date_corrections (entry_id, kind, set_local, shift_s, batch_id, created_at)
			VALUES (?, `+c+`, 'b1', 0)`, int64(id))
	}
	e.rederive(kept, set, shifted)
	for id, want := range map[domain.EntryID]bool{kept: true, set: false, shifted: false} {
		d, _ := e.date(id)
		if d.computedAt != later {
			t.Errorf("entry %d not written again", id)
		}
		if got := d.flags&media.FlagCameraOffset != 0; got != want {
			t.Errorf("entry %d camera_offset %v, want %v", id, got, want)
		}
	}
	if d, _ := e.date(set); d.source != "owner" || d.local.String != "2010-07-17" || d.precision.String != "day" {
		t.Errorf("the set photo: %+v", d)
	}
	if d, _ := e.date(shifted); d.effective.Int64-before.effective.Int64 != 31546800*int64(time.Second) ||
		d.corrected.String != "shift" {
		t.Errorf("the shifted photo: %+v, before %+v", d, before)
	}
	if n := e.count(`SELECT count(*) FROM media_dates WHERE flags & 4 <> 0`); n != 10 {
		t.Errorf("%d photos flagged camera_offset, want the Sony's 12 but the set and the shifted ones", n)
	}
	e.checkSummary(corpusSource, "after the corrections")

	// Rows of entries that are no longer media go: one gone from the disk,
	// one moved into the quarantine, one no longer of a media kind.
	gone := e.id(corpusSource, ouroPreto+"/DSCN0002.JPG")
	quarantined := e.id(corpusSource, ouroPreto+"/DSCN0001.JPG")
	reclassified := e.id(corpusSource, ouroPreto+"/DSCN0003.JPG")
	root.Child("Viagens").Child("2008-03 Ouro Preto").Remove("DSCN0002.JPG")
	root.Dir(index.QuarantineName).Dir("7")
	e.scan(corpusSource)
	e.move(corpusSource, corpusRoot, ouroPreto+"/DSCN0001.JPG", index.QuarantineName+"/7")
	e.exec(`UPDATE entries SET file_kind = 'document' WHERE id = ?`, int64(reclassified))
	for _, id := range []domain.EntryID{gone, quarantined, reclassified} {
		if _, ok := e.date(id); !ok {
			t.Fatalf("entry %d lost its date before Rederive", id)
		}
	}
	e.rederive(gone, quarantined, reclassified, 99999999)
	for _, id := range []domain.EntryID{gone, quarantined, reclassified} {
		if _, ok := e.date(id); ok {
			t.Errorf("entry %d keeps its date", id)
		}
	}
	if s := e.storedSummary(corpusSource); s.Media != int64(nMedia-3) {
		t.Errorf("summary counts %d media, want %d", s.Media, nMedia-3)
	}
	e.checkSummary(corpusSource, "after the deletions")
}

// r5 review, Addendum G12: a file dated, unrefined, by its folder's month
// keeps that date when a date organize moves it into `{year}/{month}`:
// re-derived at Fotos/2010/07, it is still 2010-07 from the folder name,
// not the year 2010 refined to its December modification time.
func TestRederiveAfterADateOrganizeKeepsTheFolderMonth(t *testing.T) {
	e, root, _ := newCorpusEnv(t)
	mtime := time.Date(2010, 12, 26, 15, 0, 0, 0, time.UTC)
	root.Child("Viagens").Child("2010-07 Bahia").File("scan.jpg", 2048, mtime).Seed(7)
	root.Child("Fotos").Dir("2010").Dir("07")
	e.scan(corpusSource)
	from := bahia + "/scan.jpg"
	id := e.id(corpusSource, from)
	check := func(when string) {
		t.Helper()
		e.rederive(id)
		if d, _ := e.date(id); d.source != "folder_name" || d.local.String != "2010-07" || d.precision.String != "month" ||
			d.refined {
			t.Errorf("%s: %+v, want 2010-07 from the folder name, unrefined", when, d)
		}
	}
	check("in " + bahia)
	e.move(corpusSource, corpusRoot, from, "Fotos/2010/07")
	if got := e.id(corpusSource, "Fotos/2010/07/scan.jpg"); got != id {
		t.Fatalf("the move made entry %d, want %d", got, id)
	}
	check("in Fotos/2010/07")
}
