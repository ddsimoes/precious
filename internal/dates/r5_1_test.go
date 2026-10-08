package dates

import (
	"context"
	"database/sql"
	"reflect"
	"sync"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
	"precious/internal/media"
)

// R5.1 (task 2.3): the corpus on synthfs, zone UTC, scanned and its media
// job run, gives every image and video the date its ground truth records.

// flagNamesA are the flag names in bit order (design D8).
var flagNamesA = []string{"mtime_disagrees", "implausible", "camera_offset", "no_date_metadata"}

// dateTruthA reads id's media_dates row as the corpus writes its truth;
// false without a row.
func (e *env) dateTruthA(id domain.EntryID) (corpus.DateTruth, string, bool) {
	e.t.Helper()
	var (
		effective         sql.NullInt64
		local, precision  sql.NullString
		source, metaState string
		refined           bool
		flags             int64
	)
	err := e.st.Reader().QueryRow(`SELECT effective_ns, local, precision, source, refined, flags, meta_state
		FROM media_dates WHERE entry_id = ?`, int64(id)).Scan(&effective, &local, &precision, &source, &refined,
		&flags, &metaState)
	if err == sql.ErrNoRows {
		return corpus.DateTruth{}, "", false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	d := corpus.DateTruth{Local: local.String, Precision: precision.String, Source: source, Refined: refined,
		Flags: []string{}}
	if effective.Valid {
		t := time.Unix(0, effective.Int64).UTC()
		d.Effective = &t
	}
	for i, n := range flagNamesA {
		if flags&(1<<i) != 0 {
			d.Flags = append(d.Flags, n)
		}
	}
	return d, metaState, true
}

// sameTruthA compares two dates, instants by Equal.
func sameTruthA(a, b corpus.DateTruth) bool {
	if (a.Effective == nil) != (b.Effective == nil) || (a.Effective != nil && !a.Effective.Equal(*b.Effective)) {
		return false
	}
	a.Effective, b.Effective = nil, nil
	return reflect.DeepEqual(a, b)
}

func hasFlagA(d corpus.DateTruth, name string) bool {
	for _, f := range d.Flags {
		if f == name {
			return true
		}
	}
	return false
}

// R5.1 Every photo and video gets an effective date with its source; and
// R5.1 photos whose modification time disagrees with EXIF are flagged. No
// pending photo carries no_date_metadata while the job runs.
func TestR5_1EveryPhotoAndVideoGetsItsDateA(t *testing.T) {
	e, _, truth := newCorpusEnv(t)
	var once sync.Once
	e.hookA(func(ctx context.Context, job jobs.Job, stage string) error {
		if stage != stagePlanned {
			return nil
		}
		once.Do(func() {
			// Pass 1 enrolled every media file and nothing is read yet:
			// derived now, the readable ones are pending.
			e.rederive(e.mediaIDs(corpusSource)...)
			if n := e.count(`SELECT count(*) FROM media_dates WHERE meta_state = 'pending'`); n == 0 {
				t.Error("no pending photo while the job ran")
			}
			if n := e.count(`SELECT count(*) FROM media_dates WHERE meta_state = 'pending' AND flags & 8 <> 0`); n != 0 {
				t.Errorf("%d pending photos flagged no_date_metadata", n)
			}
		})
		return nil
	})
	e.mediaA(corpusSource)

	dated, disagree, wantDisagree := 0, map[string]bool{}, map[string]bool{}
	for _, te := range truth.Entries {
		raw, err := te.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		if te.Kind != domain.EntryFile {
			continue
		}
		var id int64
		err = e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`,
			string(corpusSource), raw).Scan(&id)
		if err == sql.ErrNoRows && te.Date == nil {
			continue // in an unreadable folder
		}
		if err != nil {
			t.Fatalf("%s: %v", te.Path, err)
		}
		got, _, ok := e.dateTruthA(domain.EntryID(id))
		if te.Date == nil {
			if ok {
				t.Errorf("%s: not media, dated %+v", te.Path, got)
			}
			continue
		}
		dated++
		if !ok {
			t.Errorf("%s: no date", te.Path)
			continue
		}
		if !sameTruthA(got, *te.Date) {
			t.Errorf("%s: date %+v, truth %+v", te.Path, got, *te.Date)
		}
		if hasFlagA(got, "mtime_disagrees") {
			disagree[te.Path] = true
		}
		if hasFlagA(*te.Date, "mtime_disagrees") {
			wantDisagree[te.Path] = true
		}
	}
	if n := len(e.mediaIDs(corpusSource)); n != dated {
		t.Errorf("%d media entries, the truth dates %d", n, dated)
	}
	if n := e.count(`SELECT count(*) FROM media_dates WHERE source_id = ?`, string(corpusSource)); n != dated {
		t.Errorf("%d dates, want %d", n, dated)
	}
	if len(wantDisagree) == 0 || !reflect.DeepEqual(disagree, wantDisagree) {
		t.Errorf("mtime_disagrees on %v, want %v", disagree, wantDisagree)
	}

	// An implausible capture date is skipped: the camera default
	// 2000-01-01 00:00:00 gives way to the folder's month, flagged.
	got, _, _ := e.dateTruthA(e.id(corpusSource, "Viagens/2008-03 Ouro Preto/DSCN0004.JPG"))
	if got.Source != string(media.SourceFolderName) || got.Precision != string(media.PrecisionMonth) ||
		got.Local != "2008-03" || !hasFlagA(got, "implausible") {
		t.Errorf("DSCN0004.JPG: %+v", got)
	}
	if got, want := e.storedSummary(corpusSource), e.recounted(corpusSource); got != want {
		t.Errorf("summary %+v, recount %+v", got, want)
	}
	if s := e.storedSummary(corpusSource); s.Media != int64(dated) || s.Metadata.Pending != 0 {
		t.Errorf("summary %+v", s)
	}
}

// A folder year that disagrees with the modification time: a photo without
// metadata in …/2006/Praia modified in 2008 is dated the year 2006.
func TestR5_1FolderYearThatDisagreesWithTheModificationTimeA(t *testing.T) {
	e := newEnv(t)
	e.disk("s", "/mnt/s", posix, func(root *synthfs.Node) {
		root.Dir("Fotos").Dir("2006").Dir("Praia").File("foto.jpg", 4096, time.Date(2008, 3, 1, 9, 0, 0, 0, time.UTC)).Seed(3)
	})
	e.mediaA("s")
	got, state, ok := e.dateTruthA(e.id("s", "Fotos/2006/Praia/foto.jpg"))
	start := time.Date(2006, 1, 1, 0, 0, 0, 0, time.UTC)
	want := corpus.DateTruth{Effective: &start, Local: "2006", Precision: "year", Source: "folder_name",
		Flags: []string{"no_date_metadata"}}
	if !ok || state != "read" || !sameTruthA(got, want) {
		t.Errorf("date %+v (%s), want %+v", got, state, want)
	}
}

// An unreadable photo: its metadata state is unreadable, it is not flagged
// no_date_metadata, and its date comes from its name.
func TestR5_1AnUnreadablePhotoA(t *testing.T) {
	e := newEnv(t)
	const p = "Fotos/IMG-20110416-WA0009.jpg"
	e.disk("s", "/mnt/s", posix, func(root *synthfs.Node) {
		root.Dir("Fotos").File("IMG-20110416-WA0009.jpg", 4096, time.Date(2012, 2, 1, 9, 0, 0, 0, time.UTC)).
			Seed(4).Unreadable()
	})
	rt := e.mediaA("s")
	if s := e.metaStateA("s", p); s != string(media.MetaUnreadable) {
		t.Errorf("metadata %q", s)
	}
	got, state, ok := e.dateTruthA(e.id("s", p))
	if !ok || state != string(media.MetaUnreadable) || hasFlagA(got, "no_date_metadata") ||
		got.Source != string(media.SourceFileName) || got.Local != "2011-04-16" {
		t.Errorf("date %+v (%s)", got, state)
	}
	if rt.progress[progUnreadable] != 1 {
		t.Errorf("progress %v", rt.progress)
	}
}
