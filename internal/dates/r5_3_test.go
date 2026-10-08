package dates

import (
	"testing"
	"time"

	"precious/internal/media"
)

// R5.3 WhatsApp-named images without EXIF take their date from the file
// name (task 2.4): precision day for the 2012 copies, and refined to the
// modification time for IMG-20090612-WA0001.jpg.
func TestR5_3WhatsAppImagesTakeTheirNameDateA(t *testing.T) {
	e, _, truth := newCorpusEnv(t)
	e.mediaA(corpusSource)
	byPath := map[string]int{}
	for i, te := range truth.Entries {
		byPath[te.Path] = i
	}
	const (
		wa11 = "celular_2011/WhatsApp/Media/WhatsApp Images/"
		wa09 = "celular_backup_2009/WhatsApp/Media/WhatsApp Images/"
	)
	for _, c := range []struct{ path, local string }{
		{wa11 + "IMG-20110416-WA0003.jpg", "2011-04-16"},
		{wa11 + "IMG-20110417-WA0004.jpg", "2011-04-17"},
		{wa11 + "Sent/IMG-20110416-WA0003.jpg", "2011-04-16"},
	} {
		id := e.id(corpusSource, c.path)
		got, state, ok := e.dateTruthA(id)
		day, _ := time.Parse("2006-01-02", c.local)
		if !ok || state != string(media.MetaRead) || got.Source != string(media.SourceFileName) ||
			got.Precision != string(media.PrecisionDay) || got.Local != c.local || got.Refined ||
			!got.Effective.Equal(day) {
			t.Errorf("%s: %+v (%s)", c.path, got, state)
		}
		if i, ok := byPath[c.path]; !ok || !sameTruthA(got, *truth.Entries[i].Date) {
			t.Errorf("%s: not its truth", c.path)
		}
	}
	const refined = wa09 + "IMG-20090612-WA0001.jpg"
	id := e.id(corpusSource, refined)
	var mtime int64
	if err := e.st.Reader().QueryRow(`SELECT mtime_ns FROM entries WHERE id = ?`, int64(id)).Scan(&mtime); err != nil {
		t.Fatal(err)
	}
	got, _, ok := e.dateTruthA(id)
	if !ok || got.Source != string(media.SourceFileName) || !got.Refined ||
		got.Precision != string(media.PrecisionSecond) || got.Effective.UnixNano() != mtime ||
		got.Local[:10] != "2009-06-12" {
		t.Errorf("%s: %+v", refined, got)
	}
	if i, ok := byPath[refined]; !ok || !sameTruthA(got, *truth.Entries[i].Date) {
		t.Errorf("%s: not its truth", refined)
	}
}
