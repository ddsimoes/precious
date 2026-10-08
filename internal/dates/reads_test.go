package dates

import (
	"bytes"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/dates/datestest"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/media"
)

// The read API's JSON, as the interface reads it.
type (
	entryViewB struct {
		ID       string     `json:"id"`
		SourceID string     `json:"source_id"`
		Name     string     `json:"name"`
		Path     string     `json:"path"`
		PathB64  []byte     `json:"path_b64"`
		Size     int64      `json:"size"`
		MTime    *time.Time `json:"mtime"`
	}
	dateViewB struct {
		Instant    *time.Time `json:"instant"`
		Local      *string    `json:"local"`
		OffsetMin  *int       `json:"offset_min"`
		Precision  *string    `json:"precision"`
		Source     string     `json:"source"`
		Confidence string     `json:"confidence"`
		Refined    bool       `json:"refined"`
		Corrected  *string    `json:"corrected"`
	}
	cameraViewB struct {
		Key, Make, Model, Serial string
	}
	correctionViewB struct {
		Kind      string    `json:"kind"`
		Local     string    `json:"local"`
		OffsetMin *int      `json:"offset_min"`
		ShiftS    *int64    `json:"shift_s"`
		CreatedAt time.Time `json:"created_at"`
	}
	mediaDateB struct {
		Entry      entryViewB       `json:"entry"`
		Date       dateViewB        `json:"date"`
		Metadata   string           `json:"metadata"`
		Flags      []string         `json:"flags"`
		Camera     *cameraViewB     `json:"camera"`
		Correction *correctionViewB `json:"correction"`
	}
	listViewB struct {
		Items      []mediaDateB `json:"items"`
		NextCursor *string      `json:"next_cursor"`
	}
	entryDatesB struct {
		Dates *struct {
			Date       dateViewB        `json:"date"`
			Metadata   string           `json:"metadata"`
			Flags      []string         `json:"flags"`
			Camera     *cameraViewB     `json:"camera"`
			Correction *correctionViewB `json:"correction"`
			Candidates []struct {
				Source    string    `json:"source"`
				Local     string    `json:"local"`
				OffsetMin *int      `json:"offset_min"`
				Instant   time.Time `json:"instant"`
				Precision string    `json:"precision"`
				Plausible bool      `json:"plausible"`
			} `json:"candidates"`
		} `json:"dates"`
	}
	camerasViewB struct {
		Items []struct {
			Key             string `json:"key"`
			Make            string `json:"make"`
			Model           string `json:"model"`
			Serial          string `json:"serial"`
			SourceID        string `json:"source_id"`
			Photos          int    `json:"photos"`
			State           string `json:"state"`
			SuggestedShiftS *int64 `json:"suggested_shift_s"`
			Events          []struct {
				Folder struct {
					ID      string `json:"id"`
					Path    string `json:"path"`
					PathB64 []byte `json:"path_b64"`
				} `json:"folder"`
				DeltaS    int64   `json:"delta_s"`
				Photos    int     `json:"photos"`
				Reference *string `json:"reference"`
			} `json:"events"`
			ComputedAt time.Time `json:"computed_at"`
		} `json:"items"`
	}
	summaryViewB struct {
		summary
		TimeZone    string     `json:"time_zone"`
		TimeZoneSet bool       `json:"time_zone_set"`
		SummaryAt   *time.Time `json:"summary_at"`
		DetectedAt  *time.Time `json:"detected_at"`
	}
)

// keysB returns the keys of the JSON object at path (dot-separated keys,
// numbers index arrays) of body, sorted.
func keysB(t *testing.T, body string, path ...any) []string {
	t.Helper()
	var v any
	decodeB(t, body, &v)
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = v.(map[string]any)[k]
		case int:
			v = v.([]any)[k]
		}
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v of %s is not an object", path, body)
	}
	return slices.Sorted(maps.Keys(m))
}

func wantKeysB(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s has keys %v, want %v", what, got, want)
	}
}

// listAllB walks every page of the list at query, limit per page.
func (a *apiB) listAllB(query string, limit int) []mediaDateB {
	a.e.t.Helper()
	var out []mediaDateB
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 1000 {
			a.e.t.Fatal("the list does not end")
		}
		q := query + "&limit=" + strconv.Itoa(limit)
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		var page listViewB
		a.get("/api/dates?"+q, &page)
		if len(page.Items) > limit || (page.NextCursor != nil && len(page.Items) != limit) {
			a.e.t.Fatalf("%s: a page of %d items, cursor %v", q, len(page.Items), page.NextCursor)
		}
		out = append(out, page.Items...)
		if page.NextCursor == nil {
			return out
		}
		cursor = *page.NextCursor
	}
}

// countB answers count=only for query.
func (a *apiB) countB(query string) int {
	a.e.t.Helper()
	var c struct {
		Count *int `json:"count"`
	}
	a.get("/api/dates?"+query+"&count=only", &c)
	if c.Count == nil {
		a.e.t.Fatalf("%s: no count", query)
	}
	return *c.Count
}

func idsOfB(items []mediaDateB) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Entry.ID
	}
	return out
}

// truthB maps the truth's media paths to their entries.
func truthB(t *testing.T, truth corpus.GroundTruth) map[string]corpus.Entry {
	t.Helper()
	out := map[string]corpus.Entry{}
	for _, en := range truth.Entries {
		raw, err := en.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		if en.Date != nil {
			out[string(raw)] = en
		}
	}
	return out
}

// truthFlagsB is the truth's flags without camera_offset, which only the
// cameras pass sets (datestest.Seed does not run it).
func truthFlagsB(en corpus.Entry) []string {
	out := []string{}
	for _, f := range en.Date.Flags {
		if f != "camera_offset" {
			out = append(out, f)
		}
	}
	return out
}

// insertCamerasB writes the corpus's media_cameras rows as the cameras
// pass would, plus extra.
func (e *env) insertCamerasB(extra ...string) {
	e.t.Helper()
	basis := jsonB(map[string]any{"events": []map[string]any{
		{"folder_id": e.ref(corpusSource, bahia), "path": bahia, "path_b64": []byte(bahia), "delta_s": -31546800,
			"photos": 6, "span_s": 10800, "reference": "gps"},
		{"folder_id": e.ref(corpusSource, natal), "path": natal, "path_b64": []byte(natal), "delta_s": -31546800,
			"photos": 6, "span_s": 10800, "reference": "gps"},
		{"folder_id": "99999999", "path": "gone", "path_b64": []byte("gone"), "delta_s": -31546800, "photos": 1,
			"span_s": 0, "reference": nil},
	}, "others": []string{canonKey}})
	at := testNow.UnixMilli()
	e.exec(`INSERT INTO media_cameras (source_id, camera_key, make, model, serial, photos, state, suggested_shift_s,
		basis, computed_at) VALUES (?, ?, 'SONY', 'DSC-W55', NULL, 12, 'offset', 31546800, ?, ?)`,
		string(corpusSource), sonyKey, basis, at)
	e.exec(`INSERT INTO media_cameras (source_id, camera_key, make, model, serial, photos, state, computed_at)
		VALUES (?, ?, 'Canon', 'Canon PowerShot SX230 HS', NULL, 8, 'ok', ?)`, string(corpusSource), canonKey, at)
	e.exec(`INSERT INTO media_cameras (source_id, camera_key, make, model, serial, photos, state, computed_at)
		VALUES (?, ?, 'NIKON', 'COOLPIX P5000', '3012345', 3, 'ok', ?)`, string(corpusSource), nikonKeyB, at)
	for _, key := range extra {
		e.exec(`INSERT INTO media_cameras (source_id, camera_key, make, model, photos, state, computed_at)
			VALUES (?, ?, 'X', 'Y', 1, 'disagrees', ?)`, string(corpusSource), key, at)
	}
	e.storeRecountB(corpusSource)
}

const nikonKeyB = "NIKON|COOLPIX P5000|3012345"

// r5 task 2.6, D10, Interfaces: each read's JSON on the seeded corpus and
// a second source, metadata included: the summary of one source and of all,
// with the zone; the list by date (the dateless last), paged, with every
// item equal to its truth, and by each filter, with count=only; the
// cameras, offset first, without an event whose folder is gone; and one
// entry's dates with its candidates.
func TestDatesReadsJSON(t *testing.T) {
	e, _, truth := newCorpusEnv(t)
	mtime := time.Date(2010, 5, 5, 10, 0, 0, 0, time.UTC)
	e.disk("outro", "/mnt/outro", posix, func(root *synthfs.Node) {
		f := root.Dir("Fotos")
		f.File("a.png", 100, time.Unix(0, 0).UTC()) // an unknown modification time: no date
		f.File("b.png", 100, mtime)
	})
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	datestest.Seed(t, e.st, e.src, e.svc, "outro")
	e.insertCamerasB()
	api := e.apiB()
	truthMedia := truthB(t, truth)
	corrected := e.ref("outro", "Fotos/b.png")
	var set setAnswerB
	api.ok(CommandSetDateCorrection, jsonB(map[string]any{"entry_id": corrected,
		"correction": map[string]any{"kind": "set", "local": "2010-05-05T07:00:00", "offset_min": -180}}), &set)

	// The summary.
	_, raw := api.getRaw("/api/dates/summary?source=corpus")
	wantKeysB(t, "the summary", keysB(t, raw), "media", "metadata", "by_source", "by_confidence", "flags", "cameras",
		"time_zone", "time_zone_set", "summary_at", "detected_at")
	wantKeysB(t, "the summary's metadata", keysB(t, raw, "metadata"), "pending", "read", "none", "unreadable")
	var sum summaryViewB
	api.get("/api/dates/summary?source=corpus", &sum)
	if sum.summary != e.storedSummary(corpusSource) || sum.Media != int64(len(truthMedia)) || sum.TimeZone != "UTC" ||
		!sum.TimeZoneSet || sum.SummaryAt != nil || sum.DetectedAt != nil {
		t.Errorf("the corpus's summary %+v, stored %+v", sum, e.storedSummary(corpusSource))
	}
	if sum.Metadata.Read == 0 || sum.Metadata.None == 0 || sum.Metadata.Read+sum.Metadata.None != sum.Media {
		t.Errorf("the corpus's metadata states %+v", sum.Metadata)
	}
	e.exec(`UPDATE media_sources SET summary_at = ?, detected_at = ? WHERE source_id = 'corpus'`,
		testNow.UnixMilli(), testNow.Add(time.Minute).UnixMilli())
	api.get("/api/dates/summary?source=corpus", &sum)
	if sum.SummaryAt == nil || !sum.SummaryAt.Equal(testNow) || sum.DetectedAt == nil ||
		!sum.DetectedAt.Equal(testNow.Add(time.Minute)) {
		t.Errorf("summary_at %v, detected_at %v", sum.SummaryAt, sum.DetectedAt)
	}
	var all summaryViewB
	api.get("/api/dates/summary", &all)
	want := e.storedSummary(corpusSource)
	outro := e.storedSummary("outro")
	want.add(&outro)
	if all.summary != want || all.Media != int64(len(truthMedia))+2 || all.BySource.None != 1 || all.BySource.Owner != 1 ||
		all.SummaryAt != nil {
		t.Errorf("the summary of all sources %+v, want %+v and no summary_at", all, want)
	}
	// An unset zone is the server's local one.
	local := New(Options{Store: e.st, Runner: e.r, Sources: e.src, Clock: e.clk, Logger: discard()})
	mux := http.NewServeMux()
	local.Routes(mux)
	localAPI := &apiB{e: e, mux: mux}
	localAPI.get("/api/dates/summary?source=corpus", &sum)
	if sum.TimeZoneSet || sum.TimeZone == "" {
		t.Errorf("an unset zone: %q set %v", sum.TimeZone, sum.TimeZoneSet)
	}

	// The list by date: every media file, by instant then ID, each as its
	// truth says.
	byDate := api.listAllB("source=corpus", 1000)
	if len(byDate) != len(truthMedia) || api.countB("source=corpus") != len(truthMedia) {
		t.Fatalf("the list holds %d items, the truth %d media", len(byDate), len(truthMedia))
	}
	for i, it := range byDate {
		en, ok := truthMedia[it.Entry.Path]
		if !ok {
			t.Errorf("%s is listed", it.Entry.Path)
			continue
		}
		d, td := it.Date, en.Date
		if d.Instant == nil || !d.Instant.Equal(*td.Effective) || *d.Local != td.Local || *d.Precision != td.Precision ||
			d.Source != td.Source || d.Refined != td.Refined || !slices.Equal(it.Flags, truthFlagsB(en)) ||
			it.Correction != nil || d.Corrected != nil {
			t.Errorf("%s: %+v flags %v, truth %+v", it.Entry.Path, d, it.Flags, *td)
		}
		wantMeta := "read"
		if media.FormatOf(strings.ToLower(strings.TrimPrefix(pathExtB(it.Entry.Path), "."))) == media.FormatNone {
			wantMeta = "none"
		}
		if it.Metadata != wantMeta || it.Entry.SourceID != "corpus" || string(it.Entry.PathB64) != it.Entry.Path ||
			en.Size == nil || it.Entry.Size != *en.Size {
			t.Errorf("%s: metadata %q, entry %+v", it.Entry.Path, it.Metadata, it.Entry)
		}
		if i > 0 {
			prev := byDate[i-1]
			if prev.Date.Instant.After(*d.Instant) || prev.Date.Instant.Equal(*d.Instant) &&
				e.idOfB(prev.Entry.ID) > e.idOfB(it.Entry.ID) {
				t.Errorf("%s listed after %s", it.Entry.Path, prev.Entry.Path)
			}
		}
	}
	if paged := api.listAllB("source=corpus", 7); !slices.Equal(idsOfB(paged), idsOfB(byDate)) {
		t.Errorf("pages of 7 list %v, one page %v", idsOfB(paged), idsOfB(byDate))
	}

	// One item's JSON, every pinned key.
	nikon := ouroPreto + "/DSCN0001.JPG"
	_, raw = api.getRaw("/api/dates?source=corpus&camera=" + url.QueryEscape(nikonKeyB) + "&within=" +
		e.ref(corpusSource, ouroPreto) + "&limit=1")
	wantKeysB(t, "a list page", keysB(t, raw), "items", "next_cursor")
	wantKeysB(t, "a MediaDate", keysB(t, raw, "items", 0), "entry", "date", "metadata", "flags", "camera", "correction")
	wantKeysB(t, "its entry", keysB(t, raw, "items", 0, "entry"), "id", "source_id", "name", "path", "path_b64", "size", "mtime")
	wantKeysB(t, "its date", keysB(t, raw, "items", 0, "date"), "instant", "local", "offset_min", "precision", "source",
		"confidence", "refined", "corrected")
	wantKeysB(t, "its camera", keysB(t, raw, "items", 0, "camera"), "key", "make", "model", "serial")
	var page listViewB
	decodeB(t, raw, &page)
	it := page.Items[0]
	if it.Entry.Path != nikon || it.Entry.Name != "DSCN0001.JPG" || it.Entry.ID != e.ref(corpusSource, nikon) ||
		it.Entry.MTime == nil || !it.Entry.MTime.Equal(time.Date(2011, 1, 15, 10, 0, 0, 0, time.UTC)) ||
		*it.Date.Local != "2008-03-22T14:00:00" || it.Date.OffsetMin == nil || *it.Date.OffsetMin != -180 ||
		it.Date.Source != "exif" || it.Date.Confidence != "high" || it.Metadata != "read" ||
		!slices.Equal(it.Flags, []string{"mtime_disagrees"}) ||
		*it.Camera != (cameraViewB{Key: nikonKeyB, Make: "NIKON", Model: "COOLPIX P5000", Serial: "3012345"}) ||
		page.NextCursor == nil {
		t.Errorf("the first Nikon photo: %+v, camera %+v", it, it.Camera)
	}

	// The second source: the dateless last, across pages; a correction.
	_, raw = api.getRaw("/api/dates?source=outro&limit=1")
	wantKeysB(t, "a corrected MediaDate's correction", keysB(t, raw, "items", 0, "correction"), "kind", "local",
		"offset_min", "created_at")
	outroItems := api.listAllB("source=outro", 1)
	if len(outroItems) != 2 || outroItems[0].Entry.Path != "Fotos/b.png" || outroItems[1].Entry.Path != "Fotos/a.png" {
		t.Fatalf("the second source lists %v", outroItems)
	}
	b, a := outroItems[0], outroItems[1]
	if b.Date.Source != "owner" || *b.Date.Corrected != "set" || !b.Date.Instant.Equal(mtime) ||
		b.Correction == nil || b.Correction.Kind != "set" || b.Correction.Local != "2010-05-05T07:00:00" ||
		*b.Correction.OffsetMin != -180 || b.Correction.ShiftS != nil || !b.Correction.CreatedAt.Equal(testNow) ||
		b.Metadata != "none" {
		t.Errorf("the corrected file: %+v, correction %+v", b, b.Correction)
	}
	if a.Date.Source != "none" || a.Date.Instant != nil || a.Date.Local != nil || a.Date.Precision != nil ||
		a.Date.Confidence != "none" || a.Entry.MTime != nil || !slices.Equal(a.Flags, []string{"no_date_metadata"}) ||
		a.Camera != nil {
		t.Errorf("the dateless file: %+v", a)
	}

	// The filters, with count=only.
	filtered := func(name, query string, keep func(path string, it mediaDateB) bool, n int) {
		t.Helper()
		got := api.listAllB("source=corpus&"+query, 5)
		var want []string
		for _, it := range byDate {
			if keep(it.Entry.Path, it) {
				want = append(want, it.Entry.ID)
			}
		}
		if !slices.Equal(idsOfB(got), want) || len(want) != n || api.countB("source=corpus&"+query) != n {
			t.Errorf("%s: %d items %v, want %d: %v", name, len(got), idsOfB(got), n, want)
		}
	}
	hasFlag := func(f string) func(string, mediaDateB) bool {
		return func(p string, _ mediaDateB) bool { return slices.Contains(truthMedia[p].Date.Flags, f) }
	}
	nFlag := func(f string) int {
		n := 0
		for _, en := range truthMedia {
			if f != "camera_offset" && slices.Contains(en.Date.Flags, f) {
				n++
			}
		}
		return n
	}
	for _, f := range []string{"mtime_disagrees", "implausible", "no_date_metadata"} {
		filtered("flag "+f, "flag="+f, hasFlag(f), nFlag(f))
	}
	filtered("flag camera_offset", "flag=camera_offset", func(string, mediaDateB) bool { return false }, 0)
	filtered("date_source exif", "date_source=exif", func(p string, _ mediaDateB) bool { return truthMedia[p].Date.Source == "exif" },
		e.count(`SELECT count(*) FROM media_dates WHERE source_id = 'corpus' AND source = 'exif'`))
	filtered("the Sony", "camera="+url.QueryEscape(sonyKey), func(p string, _ mediaDateB) bool {
		return strings.HasPrefix(p, bahia+"/DSC") || strings.HasPrefix(p, natal+"/DSC")
	}, 12)
	filtered("the Nikon's disagreeing exif dates", "camera="+url.QueryEscape(nikonKeyB)+"&flag=mtime_disagrees&date_source=exif",
		func(p string, it mediaDateB) bool {
			return strings.HasPrefix(p, ouroPreto+"/") && slices.Contains(truthMedia[p].Date.Flags, "mtime_disagrees") &&
				truthMedia[p].Date.Source == "exif"
		}, 3)

	// within: path order; with camera, the photos of one event directly
	// in it come out of its subtree.
	var below []string
	for p := range truthMedia {
		if strings.HasPrefix(p, bahia+"/") {
			below = append(below, p)
		}
	}
	slices.SortFunc(below, func(x, y string) int { return bytes.Compare([]byte(x), []byte(y)) })
	inBahia := api.listAllB("source=corpus&within="+e.ref(corpusSource, bahia), 4)
	var paths []string
	for _, it := range inBahia {
		paths = append(paths, it.Entry.Path)
	}
	if !slices.Equal(paths, below) || api.countB("source=corpus&within="+e.ref(corpusSource, bahia)) != len(below) ||
		!slices.Contains(paths, bahia+"/do celular da Ana/IMG_0102.JPG") {
		t.Errorf("within Bahia: %q, want %q", paths, below)
	}
	sonyBahia := api.listAllB("source=corpus&camera="+url.QueryEscape(sonyKey)+"&within="+e.ref(corpusSource, bahia), 4)
	paths = paths[:0]
	for _, it := range sonyBahia {
		paths = append(paths, it.Entry.Path)
	}
	if want := []string{bahia + "/DSC00301.JPG", bahia + "/DSC00302.JPG", bahia + "/DSC00303.JPG",
		bahia + "/DSC00304.JPG", bahia + "/DSC00305.JPG", bahia + "/DSC00306.JPG"}; !slices.Equal(paths, want) {
		t.Errorf("the Sony within Bahia: %q", paths)
	}
	if n := api.countB("source=corpus&within=" + e.ref(corpusSource, "")); n != len(truthMedia) {
		t.Errorf("within the top: %d, want %d", n, len(truthMedia))
	}

	// The cameras: offset first, then by photos; the gone event left out.
	_, raw = api.getRaw("/api/dates/cameras?source=corpus")
	wantKeysB(t, "a camera", keysB(t, raw, "items", 0), "key", "make", "model", "serial", "source_id", "photos", "state",
		"suggested_shift_s", "events", "computed_at")
	wantKeysB(t, "an event", keysB(t, raw, "items", 0, "events", 0), "folder", "delta_s", "photos", "reference")
	wantKeysB(t, "an event's folder", keysB(t, raw, "items", 0, "events", 0, "folder"), "id", "path", "path_b64")
	var cams camerasViewB
	decodeB(t, raw, &cams)
	if len(cams.Items) != 3 || cams.Items[0].Key != sonyKey || cams.Items[1].Key != canonKey || cams.Items[2].Key != nikonKeyB {
		t.Fatalf("the cameras: %+v", cams)
	}
	sony := cams.Items[0]
	if sony.State != "offset" || *sony.SuggestedShiftS != 31546800 || sony.Photos != 12 || sony.Make != "SONY" ||
		sony.Serial != "" || sony.SourceID != "corpus" || !sony.ComputedAt.Equal(testNow) || len(sony.Events) != 2 {
		t.Fatalf("the Sony: %+v", sony)
	}
	for i, dir := range []string{bahia, natal} {
		ev := sony.Events[i]
		if ev.Folder.ID != e.ref(corpusSource, dir) || ev.Folder.Path != dir || string(ev.Folder.PathB64) != dir ||
			ev.DeltaS != -31546800 || ev.Photos != 6 || ev.Reference == nil || *ev.Reference != "gps" {
			t.Errorf("the Sony's event %d: %+v", i, ev)
		}
	}
	if c := cams.Items[1]; c.State != "ok" || c.SuggestedShiftS != nil || c.Events == nil || len(c.Events) != 0 || c.Photos != 8 {
		t.Errorf("the Canon: %+v", c)
	}

	// One entry's dates: the stored date with its candidates.
	var one entryDatesB
	_, raw = api.getRaw("/api/entries/" + e.ref(corpusSource, nikon) + "/dates")
	wantKeysB(t, "an entry's dates", keysB(t, raw, "dates"), "date", "metadata", "flags", "camera", "correction", "candidates")
	wantKeysB(t, "a candidate", keysB(t, raw, "dates", "candidates", 0), "source", "local", "offset_min", "instant",
		"precision", "plausible")
	decodeB(t, raw, &one)
	d := one.Dates
	if d == nil || jsonB(d.Date) != jsonB(it.Date) || d.Metadata != "read" || !slices.Equal(d.Flags, it.Flags) || *d.Camera != *it.Camera ||
		d.Correction != nil {
		t.Fatalf("the Nikon photo's dates %+v, listed %+v", d, it)
	}
	sources := map[string]bool{}
	for _, c := range d.Candidates {
		sources[c.Source] = c.Plausible
	}
	if c := d.Candidates[0]; c.Source != "exif" || c.Local != "2008-03-22T14:00:00" || *c.OffsetMin != -180 ||
		c.Precision != "second" || !c.Plausible || !c.Instant.Equal(*it.Date.Instant) {
		t.Errorf("the first candidate %+v", c)
	}
	if !sources["mtime"] || !sources["folder_name"] {
		t.Errorf("the candidates %+v miss the modification time or the folder", d.Candidates)
	}
	// Not derived yet: derived on read.
	e.exec(`DELETE FROM media_dates WHERE entry_id = ?`, int64(e.id(corpusSource, nikon)))
	var again entryDatesB
	api.get("/api/entries/"+e.ref(corpusSource, nikon)+"/dates", &again)
	if again.Dates == nil || jsonB(again.Dates.Date) != jsonB(d.Date) || !slices.Equal(again.Dates.Flags, d.Flags) ||
		*again.Dates.Camera != *d.Camera || len(again.Dates.Candidates) != len(d.Candidates) {
		t.Errorf("derived on read %+v, stored %+v", again.Dates, d)
	}
	api.get("/api/entries/"+corrected+"/dates", &again)
	if again.Dates == nil || again.Dates.Correction == nil || again.Dates.Candidates[0].Source != "owner" {
		t.Errorf("the corrected file's dates %+v", again.Dates)
	}
	for _, p := range []string{aDocument, "Viagens", ""} {
		var none entryDatesB
		_, raw := api.getRaw("/api/entries/" + e.ref(corpusSource, p) + "/dates")
		decodeB(t, raw, &none)
		if none.Dates != nil || raw != "{\"dates\":null}\n" {
			t.Errorf("%q: %s, want null dates", p, raw)
		}
	}
}

// pathExtB is the extension of a path's name, with its dot.
func pathExtB(p string) string {
	if i := strings.LastIndexByte(p, '.'); i > strings.LastIndexByte(p, '/') {
		return p[i:]
	}
	return ""
}

// idOfB parses an API entry ID.
func (e *env) idOfB(ref string) domain.EntryID {
	e.t.Helper()
	r, err := domain.ParseRef(ref)
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Entry
}

// r5 task 2.6, Interfaces: the reads refuse what they do not take: the
// list without source, an unknown parameter on each read, a repeated one,
// bad values, and a bad cursor are 400; an unknown source or entry is 404.
func TestDatesReadsRefuse(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	api := e.apiB()
	photo := e.ref(corpusSource, canonPhoto)
	bad := []string{
		"/api/dates",
		"/api/dates?flag=mtime_disagrees",
		"/api/dates?source=corpus&x=1",
		"/api/dates?source=corpus&source=corpus",
		"/api/dates?source=corpus&flag=late",
		"/api/dates?source=corpus&date_source=guess",
		"/api/dates?source=corpus&camera=",
		"/api/dates?source=corpus&within=x",
		"/api/dates?source=corpus&within=m4",
		"/api/dates?source=corpus&within=" + photo,
		"/api/dates?source=corpus&cursor=nope",
		"/api/dates?source=corpus&cursor=" + *listCursor{Path: []byte("a")}.encode(),
		"/api/dates?source=corpus&within=" + e.ref(corpusSource, bahia) + "&cursor=" + *listCursor{None: true, ID: 3}.encode(),
		"/api/dates?source=corpus&limit=0",
		"/api/dates?source=corpus&limit=x",
		"/api/dates?source=corpus&count=all",
		"/api/dates?source=corpus&count=only&limit=5",
		"/api/dates/summary?x=1",
		"/api/dates/summary?source=",
		"/api/dates/cameras",
		"/api/dates/cameras?source=corpus&x=1",
		"/api/entries/" + photo + "/dates?x=1",
		"/api/entries/x/dates",
	}
	for _, p := range bad {
		api.getFails(http.StatusBadRequest, domain.CodeInvalidRequest, p)
	}
	for _, p := range []string{"/api/dates?source=nada", "/api/dates/summary?source=nada", "/api/dates/cameras?source=nada"} {
		api.getFails(http.StatusNotFound, domain.CodeUnknownSource, p)
	}
	for _, p := range []string{"/api/entries/99999999/dates", "/api/entries/m99/dates",
		"/api/dates?source=corpus&within=99999999"} {
		api.getFails(http.StatusNotFound, domain.CodeNotFound, p)
	}
	var page listViewB
	api.get("/api/dates?source=corpus&limit=5000", &page)
	if len(page.Items) != len(e.mediaIDs(corpusSource)) || page.NextCursor != nil {
		t.Errorf("limit 5000 gave %d items", len(page.Items))
	}
	api.get("/api/dates?source=corpus", &page)
	if len(page.Items) != min(listDefaultLimit, len(e.mediaIDs(corpusSource))) {
		t.Errorf("the default page holds %d items", len(page.Items))
	}
}

// r5 task 2.6, "Listing the flagged photos of one folder": flag=
// mtime_disagrees within Viagens holds exactly the flagged photos below
// it, with a cursor while more remain, and no quarantined photo. The
// quarantined photo, whose date row is still there until the dates pass,
// is also left out of the cameras (a camera with no other photo goes),
// the summary (once the dates pass ran), and the entry's dates (null).
func TestListingFlaggedPhotosOfOneFolder(t *testing.T) {
	e, root, truth := newCorpusEnv(t)
	root.Dir(index.QuarantineName).Dir("7")
	e.scan(corpusSource)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	api := e.apiB()
	truthMedia := truthB(t, truth)
	quarantined := ouroPreto + "/DSCN0002.JPG"
	if !slices.Contains(truthMedia[quarantined].Date.Flags, "mtime_disagrees") {
		t.Fatalf("%s is not flagged in the truth", quarantined)
	}
	qid := e.id(corpusSource, quarantined)
	// Its own camera, as detection would have listed it before the move.
	const lone = "Kodak|EasyShare|"
	e.exec(`UPDATE media_dates SET camera_key = ? WHERE entry_id = ?`, lone, int64(qid))
	e.insertCamerasB(lone)
	before := e.storedSummary(corpusSource)

	var flagged []string
	for p, en := range truthMedia {
		if strings.HasPrefix(p, "Viagens/") && p != quarantined && slices.Contains(en.Date.Flags, "mtime_disagrees") {
			flagged = append(flagged, e.ref(corpusSource, p))
		}
	}
	if len(flagged) < 2 {
		t.Fatalf("only %d flagged photos below Viagens", len(flagged))
	}
	e.move(corpusSource, corpusRoot, quarantined, index.QuarantineName+"/7")
	if _, ok := e.date(qid); !ok {
		t.Fatal("the move dropped the quarantined photo's date row")
	}

	query := "source=corpus&flag=mtime_disagrees&within=" + e.ref(corpusSource, "Viagens")
	var first listViewB
	api.get("/api/dates?"+query+"&limit=1", &first)
	if len(first.Items) != 1 || first.NextCursor == nil {
		t.Errorf("the first page of 1: %d items, cursor %v", len(first.Items), first.NextCursor)
	}
	got := api.listAllB(query, 1)
	gotIDs := idsOfB(got)
	slices.Sort(gotIDs)
	slices.Sort(flagged)
	if !slices.Equal(gotIDs, flagged) || api.countB(query) != len(flagged) {
		t.Errorf("flagged within Viagens: %v, want %v", gotIDs, flagged)
	}
	for _, it := range got {
		if !slices.Contains(it.Flags, "mtime_disagrees") || !strings.HasPrefix(it.Entry.Path, "Viagens/") ||
			strings.Contains(it.Entry.Path, index.QuarantineName) {
			t.Errorf("listed %s, flags %v", it.Entry.Path, it.Flags)
		}
	}
	for _, q := range []string{"source=corpus", "source=corpus&flag=mtime_disagrees", "source=corpus&camera=" + url.QueryEscape(lone),
		"source=corpus&within=" + e.ref(corpusSource, "")} {
		for _, it := range api.listAllB(q, 1000) {
			if it.Entry.ID == qid.String() {
				t.Errorf("%s lists the quarantined photo", q)
			}
		}
	}
	var one entryDatesB
	api.get("/api/entries/"+qid.String()+"/dates", &one)
	if one.Dates != nil {
		t.Errorf("the quarantined photo's dates: %+v", one.Dates)
	}
	var cams camerasViewB
	api.get("/api/dates/cameras?source=corpus", &cams)
	var keys []string
	for _, c := range cams.Items {
		keys = append(keys, c.Key)
	}
	if !slices.Equal(keys, []string{sonyKey, canonKey, nikonKeyB}) {
		t.Errorf("the cameras: %v", keys)
	}

	// The dates pass re-derives the source, the moved photo included: the
	// summary leaves it out.
	e.rederive(append(e.mediaIDs(corpusSource), qid)...)
	var sum summaryViewB
	api.get("/api/dates/summary?source=corpus", &sum)
	if sum.Media != before.Media-1 || sum.Flags.MtimeDisagrees != before.Flags.MtimeDisagrees-1 {
		t.Errorf("the summary counts %d media, %d disagreeing; before %d, %d", sum.Media, sum.Flags.MtimeDisagrees,
			before.Media, before.Flags.MtimeDisagrees)
	}
	e.checkSummary(corpusSource, "after the quarantine")
}
