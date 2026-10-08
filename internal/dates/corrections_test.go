package dates

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/dates/datestest"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/media"
)

// Corpus media the correction tests use.
const (
	canonPhoto  = bahia + "/IMG_0101.JPG"
	sonyPhoto   = bahia + "/DSC00301.JPG"
	clip        = "celular_2011/DCIM/Camera/VID_20110423_101500.mp4"
	screenshot  = "celular_2011/Pictures/Screenshots/Screenshot_2011-05-02-21-14-07.png"
	whatsapp3   = "celular_2011/WhatsApp/Media/WhatsApp Images/IMG-20110416-WA0003.jpg"
	whatsapp4   = "celular_2011/WhatsApp/Media/WhatsApp Images/IMG-20110417-WA0004.jpg"
	aDocument   = "Documentos/curriculo.doc"
	sixteenYrsS = 16 * 365 * 24 * 3600
)

// seededB is the corpus env, seeded as a complete media job leaves it.
func seededB(t *testing.T) (*env, *apiB) {
	t.Helper()
	e, _, _ := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	return e, e.apiB()
}

// r5 task 2.7, "A scanned print from 1978": the owner's year wins whatever
// its age, at precision year, source owner, and a {year}/{month} template
// finds it too coarse. A set after tomorrow is 400 and changes nothing;
// tomorrow itself is accepted.
func TestScannedPrintFrom1978(t *testing.T) {
	e, api := seededB(t)
	id := e.id(corpusSource, canonPhoto)
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_id": id.String(),
		"correction": map[string]any{"kind": "set", "local": "1978"}}), &got)
	if got.Applied != 1 || got.SkippedCount != 0 {
		t.Fatalf("set 1978 answered %+v", got)
	}
	d, _ := e.date(id)
	if d.source != "owner" || d.precision.String != "year" || d.local.String != "1978" || d.corrected.String != "set" ||
		d.effective.Int64 != time.Date(1978, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano() {
		t.Errorf("the print's date: %+v", d)
	}
	date, err := media.DateFromRow(d.effective.Int64, d.local.String, nil, d.precision.String)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := media.ParseTemplate("{year}/{month}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Folders(date, nil); !errors.Is(err, media.ErrTooCoarse) {
		t.Errorf("{year}/{month} of 1978: %v, want too coarse", err)
	}
	e.checkSummary(corpusSource, "after 1978")

	// After tomorrow: 400, nothing written. Tomorrow (now is 2026-10-01
	// 12:00 UTC): accepted.
	other := e.id(corpusSource, bahia+"/IMG_0102.JPG")
	for _, local := range []string{"2026-10-03", "2026-10-02T12:00:01", "2027"} {
		api.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, CommandSetDateCorrection, jsonB(map[string]any{
			"entry_id": other.String(), "correction": map[string]any{"kind": "set", "local": local}}))
	}
	if _, ok := e.correctionOfB(other); ok {
		t.Error("a refused set wrote a correction")
	}
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_id": other.String(),
		"correction": map[string]any{"kind": "set", "local": "2026-10-02"}}), &got)
	if got.Applied != 1 {
		t.Errorf("set tomorrow answered %+v", got)
	}
}

// r5 task 2.7, D11: a shift that would move a target past tomorrow skips
// it in_future in bulk, and fails a single request 409.
func TestShiftIntoTheFuture(t *testing.T) {
	e, api := seededB(t)
	photo, video := e.id(corpusSource, canonPhoto), e.id(corpusSource, clip)
	before, _ := e.date(photo)
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_ids": []string{video.String(), photo.String()},
		"correction": map[string]any{"kind": "shift", "shift_s": sixteenYrsS}}), &got)
	if got.Applied != 1 || got.SkippedCount != 1 || len(got.Skipped) != 1 || got.Skipped[0].EntryID != video.String() ||
		got.Skipped[0].Reason != "in_future" || got.Skipped[0].Path != clip || string(got.Skipped[0].PathB64) != clip {
		t.Fatalf("the shift answered %+v", got)
	}
	if d, _ := e.date(photo); d.effective.Int64 != before.effective.Int64+sixteenYrsS*int64(time.Second) {
		t.Errorf("the photo was not shifted: %+v", d)
	}
	if _, ok := e.correctionOfB(video); ok {
		t.Error("the skipped video has a correction")
	}
	api.refuse(http.StatusConflict, domain.CodeInvalidEntryState, CommandSetDateCorrection, jsonB(map[string]any{
		"entry_id": video.String(), "correction": map[string]any{"kind": "shift", "shift_s": sixteenYrsS}}))
}

// r5 task 2.7, "Using the name date where there is none": use_name on five
// photos, two of them without a date in their names, corrects three and
// skips two no_name_date, in path order. A single use_folder without a
// folder date is 409 invalid_entry_state.
func TestUseNameWhereThereIsNone(t *testing.T) {
	e, api := seededB(t)
	named := []string{screenshot, whatsapp3, whatsapp4}
	unnamed := []string{sonyPhoto, canonPhoto} // path order: DSC… before IMG_…
	var refs []string
	for _, p := range append(slices.Clone(unnamed), named...) {
		refs = append(refs, e.ref(corpusSource, p))
	}
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_ids": refs,
		"correction": map[string]any{"kind": "use_name"}}), &got)
	if got.Applied != 3 || got.SkippedCount != 2 || len(got.Skipped) != 2 {
		t.Fatalf("use_name answered %+v", got)
	}
	for i, p := range unnamed {
		if s := got.Skipped[i]; s.Path != p || s.Reason != "no_name_date" || s.EntryID != e.ref(corpusSource, p) {
			t.Errorf("skipped %d: %+v, want %s no_name_date", i, s, p)
		}
		if _, ok := e.correctionOfB(e.id(corpusSource, p)); ok {
			t.Errorf("%s has a correction", p)
		}
	}
	for _, p := range named {
		d, _ := e.date(e.id(corpusSource, p))
		if d.source != "file_name" || d.corrected.String != "use_name" {
			t.Errorf("%s: %+v", p, d)
		}
	}
	e.checkSummary(corpusSource, "after use_name")
	api.refuse(http.StatusConflict, domain.CodeInvalidEntryState, CommandSetDateCorrection, jsonB(map[string]any{
		"entry_id": e.ref(corpusSource, whatsapp3), "correction": map[string]any{"kind": "use_folder"}}))
	api.refuse(http.StatusConflict, domain.CodeInvalidEntryState, CommandSetDateCorrection, jsonB(map[string]any{
		"entry_id": e.ref(corpusSource, canonPhoto), "correction": map[string]any{"kind": "use_name"}}))
}

// r5 task 2.7, "A quarantined target": a correction naming a quarantined
// photo fails 409 in_quarantine, and nothing changes: no correction, date,
// audit event, or job.
func TestCorrectionOfAQuarantinedTarget(t *testing.T) {
	e, root, _ := newCorpusEnv(t)
	root.Dir(index.QuarantineName).Dir("7")
	e.scan(corpusSource)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	e.move(corpusSource, corpusRoot, bahia+"/IMG_0104.JPG", index.QuarantineName+"/7")
	quarantined := e.ref(corpusSource, index.QuarantineName+"/7/IMG_0104.JPG")
	api := e.apiB()
	ok := e.id(corpusSource, canonPhoto)
	before, _ := e.date(ok)
	for _, body := range []map[string]any{
		{"entry_ids": []string{ok.String(), quarantined}, "correction": map[string]any{"kind": "set", "local": "2010"}},
		{"entry_id": quarantined, "correction": map[string]any{"kind": "set", "local": "2010"}},
		{"folder_ids": []string{e.ref(corpusSource, index.QuarantineName)}, "correction": map[string]any{"kind": "use_name"}},
	} {
		api.refuse(http.StatusConflict, domain.CodeInQuarantine, CommandSetDateCorrection, jsonB(body))
	}
	api.refuse(http.StatusConflict, domain.CodeInQuarantine, CommandClearDateCorrection, jsonB(map[string]any{
		"entry_ids": []string{ok.String(), quarantined}}))
	if n := e.count(`SELECT count(*) FROM date_corrections`); n != 0 {
		t.Errorf("%d corrections written", n)
	}
	if after, _ := e.date(ok); after != before {
		t.Errorf("the other photo's date changed: %+v, was %+v", after, before)
	}
	if n := e.count(`SELECT count(*) FROM audit_events`); n != 0 {
		t.Errorf("%d audit events", n)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media'`); n != 0 {
		t.Errorf("%d media jobs", n)
	}
}

// r5 task 2.7, D11: a single target that is not media is 409
// invalid_entry_state (clear-date-correction clears nothing instead); a
// member ref is 400 as entry_id, and skipped not_media in entry_ids; every
// malformed body is 400 invalid_request.
func TestCorrectionRefusals(t *testing.T) {
	e, api := seededB(t)
	doc := e.ref(corpusSource, aDocument)
	api.refuse(http.StatusConflict, domain.CodeInvalidEntryState, CommandSetDateCorrection, jsonB(map[string]any{
		"entry_id": doc, "correction": map[string]any{"kind": "set", "local": "2004"}}))
	var cleared clearAnswerB
	api.ok(CommandClearDateCorrection, jsonB(map[string]any{"entry_id": doc}), &cleared)
	if cleared.Cleared != 0 {
		t.Errorf("clearing a document answered %+v", cleared)
	}
	api.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, CommandSetDateCorrection, jsonB(map[string]any{
		"entry_id": "m7", "correction": map[string]any{"kind": "set", "local": "2004"}}))
	api.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, CommandClearDateCorrection, `{"entry_id":"m7"}`)
	var got setAnswerB
	photo := e.ref(corpusSource, canonPhoto)
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_ids": []string{"m7", doc, photo},
		"correction": map[string]any{"kind": "set", "local": "2010-07-17"}}), &got)
	if got.Applied != 1 || got.SkippedCount != 2 || len(got.Skipped) != 2 ||
		got.Skipped[0].EntryID != doc || got.Skipped[0].Reason != "not_media" || got.Skipped[0].Path != aDocument ||
		got.Skipped[1].EntryID != "m7" || got.Skipped[1].Reason != "not_media" {
		t.Errorf("a member and a document in bulk: %+v", got)
	}
	api.refuse(http.StatusNotFound, domain.CodeNotFound, CommandSetDateCorrection, jsonB(map[string]any{
		"entry_ids": []string{photo, "99999999"}, "correction": map[string]any{"kind": "use_name"}}))

	set := func(c map[string]any) string { return jsonB(map[string]any{"entry_id": photo, "correction": c}) }
	for name, body := range map[string]string{
		"no targets":            `{"correction":{"kind":"use_name"}}`,
		"two forms":             jsonB(map[string]any{"entry_id": photo, "entry_ids": []string{photo}, "correction": map[string]any{"kind": "use_name"}}),
		"no correction":         jsonB(map[string]any{"entry_id": photo}),
		"an unknown field":      jsonB(map[string]any{"entry_id": photo, "correction": map[string]any{"kind": "use_name"}, "x": 1}),
		"an unknown kind":       set(map[string]any{"kind": "guess"}),
		"a set without a date":  set(map[string]any{"kind": "set"}),
		"a bad date":            set(map[string]any{"kind": "set", "local": "2010-13"}),
		"a date with a slash":   set(map[string]any{"kind": "set", "local": "2010/07"}),
		"an offset on a day":    set(map[string]any{"kind": "set", "local": "2010-07-17", "offset_min": -180}),
		"an offset past 14 h":   set(map[string]any{"kind": "set", "local": "2010-07-17T10:00:00", "offset_min": 900}),
		"a set with a shift":    set(map[string]any{"kind": "set", "local": "2010", "shift_s": 60}),
		"a shift of 0":          set(map[string]any{"kind": "shift", "shift_s": 0}),
		"a shift of 51 years":   set(map[string]any{"kind": "shift", "shift_s": 51 * 366 * 86400}),
		"a shift with a date":   set(map[string]any{"kind": "shift", "shift_s": 60, "local": "2010"}),
		"use_name with a shift": set(map[string]any{"kind": "use_name", "shift_s": 60}),
		"a camera without folders": jsonB(map[string]any{"entry_ids": []string{photo}, "camera_key": sonyKey,
			"correction": map[string]any{"kind": "use_name"}}),
		"1,001 entries": jsonB(map[string]any{"entry_ids": slices.Repeat([]string{photo}, 1001),
			"correction": map[string]any{"kind": "use_name"}}),
	} {
		if code, out := api.post(CommandSetDateCorrection, body); code != http.StatusBadRequest ||
			!strings.Contains(out, `"code":"invalid_request"`) {
			t.Errorf("%s: %d %s, want 400 invalid_request", name, code, out)
		}
	}
	// A time with its offset is accepted, and stored with it.
	api.ok(CommandSetDateCorrection, set(map[string]any{"kind": "set", "local": "2010-07-17T10:00:00", "offset_min": -180}), &got)
	if d, _ := e.date(e.id(corpusSource, canonPhoto)); d.effective.Int64 != time.Date(2010, 7, 17, 13, 0, 0, 0, time.UTC).UnixNano() ||
		d.local.String != "2010-07-17T10:00:00" {
		t.Errorf("a set with an offset: %+v", d)
	}
}

// r5 task 2.7, "Corrections survive a rescan": the Sony's photos are
// shifted; one is edited on disk (a new capture time), and the source is
// rescanned and read again. Every photo keeps its correction, and the
// edited one's effective date is its new capture plus the shift.
func TestCorrectionsSurviveARescan(t *testing.T) {
	e, root, _ := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	api := e.apiB()
	const shift = 31546800
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{
		"folder_ids": []string{e.ref(corpusSource, bahia), e.ref(corpusSource, natal)}, "camera_key": sonyKey,
		"correction": map[string]any{"kind": "shift", "shift_s": shift}}), &got)
	if got.Applied != 12 {
		t.Fatalf("the shift answered %+v", got)
	}
	sony := e.mediaOfCameraB(sonyKey)
	if len(sony) != 12 {
		t.Fatalf("%d Sony photos", len(sony))
	}

	// Edit DSC00301.JPG: its capture an hour later, written now.
	edited := e.id(corpusSource, sonyPhoto)
	var capture string
	if err := e.st.Reader().QueryRow(`SELECT capture_local FROM media_meta WHERE entry_id = ?`, int64(edited)).
		Scan(&capture); err != nil {
		t.Fatal(err)
	}
	old, err := time.Parse("2006-01-02T15:04:05", capture[:19])
	if err != nil {
		t.Fatal(err)
	}
	later := old.Add(time.Hour)
	exif := func(t time.Time) []byte { return []byte(t.Format("2006:01:02 15:04:05")) }
	content := e.readFileB(corpusRoot, sonyPhoto)
	if !bytes.Contains(content, exif(old)) {
		t.Fatalf("%s holds no %s", sonyPhoto, exif(old))
	}
	root.Child("Viagens").Child("2010-07 Bahia").Child("DSC00301.JPG").
		Content(bytes.ReplaceAll(content, exif(old), exif(later))).ModTime(testNow.Add(-time.Hour))
	e.scan(corpusSource)
	if n := e.count(`SELECT count(*) FROM media_meta WHERE entry_id = ?`, int64(edited)); n != 0 {
		t.Fatalf("the rescan kept the edited photo's metadata")
	}
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)

	for _, id := range sony {
		if c, ok := e.correctionOfB(id); !ok || c.kind != "shift" || c.shiftS.Int64 != shift {
			t.Errorf("entry %d lost its correction: %+v", id, c)
		}
	}
	d, _ := e.date(edited)
	if want := later.Add(shift * time.Second); d.effective.Int64 != want.UnixNano() || d.corrected.String != "shift" {
		t.Errorf("the edited photo: %v (%s), want %v", time.Unix(0, d.effective.Int64).UTC(), d.corrected.String, want)
	}
	e.checkSummary(corpusSource, "after the rescan")
}

// mediaOfCameraB returns the media entries whose date row has camera key.
func (e *env) mediaOfCameraB(key string) []domain.EntryID {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT d.entry_id FROM media_dates d JOIN entries e ON e.id = d.entry_id
		WHERE d.camera_key = ? AND `+MediaCond("e")+` ORDER BY e.path`, key)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []domain.EntryID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, domain.EntryID(id))
	}
	return out
}

// readFileB returns the bytes of the file at the raw path p below root.
func (e *env) readFileB(root, p string) []byte {
	e.t.Helper()
	parent, name := splitPath(p)
	dir := e.open(root, parent)
	defer dir.Close()
	info, err := dir.Lstat([]byte(name))
	if err != nil {
		e.t.Fatal(err)
	}
	f, err := dir.OpenFile([]byte(name), info)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, info.Size)
	if _, err := f.ReadAt(b, 0); err != nil && !errors.Is(err, io.EOF) {
		e.t.Fatal(err)
	}
	return b
}

// r5 task 2.7, "A correction is audited": a date set on three photos
// writes one date_correction_set event with the targets, the correction,
// three applied, none skipped, and the batch; a clear writes one
// date_correction_cleared.
func TestACorrectionIsAudited(t *testing.T) {
	e, api := seededB(t)
	refs := []string{e.ref(corpusSource, canonPhoto), e.ref(corpusSource, bahia+"/IMG_0102.JPG"),
		e.ref(corpusSource, bahia+"/IMG_0103.JPG")}
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_ids": refs,
		"correction": map[string]any{"kind": "set", "local": "2010-07"}}), &got)
	evs := e.auditsB(AuditDateCorrectionSet)
	if len(evs) != 1 {
		t.Fatalf("%d date_correction_set events", len(evs))
	}
	want := jsonB(map[string]any{"targets": map[string]any{"entry_ids": refs},
		"correction": map[string]any{"kind": "set", "local": "2010-07"}, "applied": 3, "skipped": 0,
		"batch_id": got.BatchID})
	if g := jsonB(evs[0]); g != want {
		t.Errorf("the event's detail %s, want %s", g, want)
	}
	var cleared clearAnswerB
	api.ok(CommandClearDateCorrection, jsonB(map[string]any{"entry_ids": refs}), &cleared)
	evs = e.auditsB(AuditDateCorrectionCleared)
	want = jsonB(map[string]any{"targets": map[string]any{"entry_ids": refs}, "cleared": 3, "batch_id": cleared.BatchID})
	if len(evs) != 1 || jsonB(evs[0]) != want {
		t.Errorf("cleared events %v, want one %s", evs, want)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE actor = 'admin'`); n != 2 {
		t.Errorf("%d admin events", n)
	}
}

// r5 task 2.7, D11: clear-date-correction restores the derived date, in
// the same transaction, and requests the media job; clearing again clears
// nothing.
func TestClearRestoresTheDerivedDate(t *testing.T) {
	e, api := seededB(t)
	id := e.id(corpusSource, canonPhoto)
	before, _ := e.date(id)
	var got setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_id": id.String(),
		"correction": map[string]any{"kind": "set", "local": "2001-05"}}), &got)
	if d, _ := e.date(id); d.source != "owner" {
		t.Fatalf("the set did not apply: %+v", d)
	}
	e.exec(`UPDATE media_sources SET dirty = 0`)
	e.exec(`DELETE FROM jobs`)
	var cleared clearAnswerB
	api.ok(CommandClearDateCorrection, jsonB(map[string]any{"folder_ids": []string{e.ref(corpusSource, bahia)}}), &cleared)
	if cleared.Cleared != 1 || cleared.BatchID == "" || cleared.BatchID == got.BatchID {
		t.Errorf("clear answered %+v", cleared)
	}
	after, _ := e.date(id)
	after.computedAt, before.computedAt = 0, 0
	if after != before {
		t.Errorf("after the clear %+v, want the derived %+v", after, before)
	}
	if _, ok := e.correctionOfB(id); ok {
		t.Error("the correction is still there")
	}
	if n := e.count(`SELECT dirty FROM media_sources WHERE source_id = 'corpus'`); n != 1 {
		t.Errorf("dirty %d after the clear", n)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media'`); n != 1 {
		t.Errorf("%d media jobs after the clear", n)
	}
	e.checkSummary(corpusSource, "after the clear")
	api.ok(CommandClearDateCorrection, jsonB(map[string]any{"entry_id": id.String()}), &cleared)
	if cleared.Cleared != 0 {
		t.Errorf("a second clear answered %+v", cleared)
	}
}
