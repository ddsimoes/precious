package dates

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/media"
)

// r5 task 1.6, D2: MediaCond and media.IsMediaKind agree on every file
// kind; a quarantined, missing, or non-file row is never media, whatever its
// kind. Archive members are not entries, so no entries condition selects
// them (ExpandTargets skips a member ref as not_media).
func TestMediaCondAgreesWithIsMediaKind(t *testing.T) {
	e := newEnv(t)
	mtime := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)
	root := e.disk("d", "/d", posix, func(root *synthfs.Node) {
		fotos := root.Dir("Fotos")
		fotos.File("a.jpg", 10, mtime)
		fotos.File("v.mp4", 10, mtime)
		fotos.File("gone.jpg", 10, mtime)
		fotos.Dir("pasta.jpg")
		fotos.Symlink("link.jpg", "a.jpg")
		kinds := root.Dir("Tipos")
		for i := range domain.FileKinds {
			kinds.File(fmt.Sprintf("k%d.dat", i), 10, mtime)
		}
		root.Dir(index.QuarantineName).Dir("7").Dir("1").File("q.jpg", 10, mtime)
		root.Dir("Tipos2").File(index.QuarantineName+".jpg", 10, mtime) // a name like the quarantine's, below the top
	})
	root.Child("Fotos").Remove("gone.jpg")
	e.scan("d")
	// Every file kind, on otherwise identical rows.
	for i, k := range domain.FileKinds {
		e.exec(`UPDATE entries SET file_kind = ? WHERE source_id = 'd' AND path = ?`, string(k),
			[]byte(fmt.Sprintf("Tipos/k%d.dat", i)))
	}
	if n := e.count(`SELECT count(*) FROM entries WHERE source_id = 'd' AND path = ? AND state = 'missing'
		AND file_kind = 'image'`, []byte("Fotos/gone.jpg")); n != 1 {
		t.Fatalf("the gone image is not a missing image row")
	}
	if n := e.count(`SELECT count(*) FROM entries WHERE source_id = 'd' AND path = ? AND file_kind = 'image'`,
		[]byte(index.QuarantineName+"/7/1/q.jpg")); n != 1 {
		t.Fatalf("the quarantined photo is not an image row")
	}

	rows, err := e.st.Reader().Query(`SELECT e.path, e.kind, e.state, e.file_kind, ` + MediaCond("e") + `
		FROM entries e WHERE e.source_id = 'd'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var (
			path        []byte
			kind, state string
			fileKind    sql.NullString
			got         bool
		)
		if err := rows.Scan(&path, &kind, &state, &fileKind, &got); err != nil {
			t.Fatal(err)
		}
		want := kind == string(domain.EntryFile) && state == "present" && !index.IsQuarantinePath(path) &&
			media.IsMediaKind(fileKind.String)
		if got != want {
			t.Errorf("%s (%s, %s, %s): MediaCond %v, its Go twin %v", path, kind, state, fileKind.String, got, want)
		}
		if got {
			found = append(found, string(path))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Fotos/a.jpg": true, "Fotos/v.mp4": true, "Tipos/k0.dat": true, "Tipos/k1.dat": true,
		"Tipos2/" + index.QuarantineName + ".jpg": true}
	if len(found) != len(want) {
		t.Errorf("media %q, want %d of them", found, len(want))
	}
	for _, p := range found {
		if !want[p] {
			t.Errorf("%s is media", p)
		}
	}

	defer func() {
		if recover() == nil {
			t.Error("MediaCond accepted an alias that is not an identifier")
		}
	}()
	MediaCond("e; DROP TABLE entries")
}

// r5 task 1.6, D4: EnqueueMedia sets dirty, creates the media job while
// none is active and joins it while it is queued; while it runs, it also
// creates the follow-up, once; both carry {"if_dirty":true}. An offline
// source is requested too; an unknown one is unknown_source.
func TestEnqueueMedia(t *testing.T) {
	e := newEnv(t)
	e.disk("on", "/on", posix, nil)
	e.disk("off", "/off", posix, nil)
	e.exec(`UPDATE sources SET state = 'offline' WHERE id = 'off'`)
	request := func(src domain.SourceID) error {
		return e.r.Write(context.Background(), func(tx *jobs.Tx) error {
			return EnqueueMedia(context.Background(), tx, src)
		})
	}
	type job struct {
		id                    int64
		scope, state, payload string
		source                string
	}
	listJobs := func() []job {
		t.Helper()
		rows, err := e.st.Reader().Query(`SELECT id, scope_key, state, payload, source_id FROM jobs WHERE kind = 'media'
			ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []job
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.id, &j.scope, &j.state, &j.payload, &j.source); err != nil {
				t.Fatal(err)
			}
			out = append(out, j)
		}
		return out
	}
	dirty := func(src domain.SourceID) int {
		return e.count(`SELECT dirty FROM media_sources WHERE source_id = ?`, string(src))
	}
	ifDirty := func(j job) bool {
		return j.payload == `{"if_dirty":true}`
	}

	// No job: one is created, and the source is dirty.
	if err := request("on"); err != nil {
		t.Fatal(err)
	}
	js := listJobs()
	if len(js) != 1 || js[0].scope != "media:on" || js[0].state != "queued" || !ifDirty(js[0]) || js[0].source != "on" {
		t.Fatalf("after a first request: %+v", js)
	}
	if dirty("on") != 1 {
		t.Errorf("the source is not dirty")
	}
	first := js[0].id

	// Queued: the request joins it.
	e.exec(`UPDATE media_sources SET dirty = 0 WHERE source_id = 'on'`)
	if err := request("on"); err != nil {
		t.Fatal(err)
	}
	if js := listJobs(); len(js) != 1 || js[0].id != first {
		t.Errorf("a request while the job is queued: %+v", js)
	}
	if dirty("on") != 1 {
		t.Errorf("a request while the job is queued left the source clean")
	}

	// Running: the follow-up is created, once.
	e.exec(`UPDATE jobs SET state = 'running' WHERE id = ?`, first)
	e.exec(`UPDATE media_sources SET dirty = 0 WHERE source_id = 'on'`)
	for range 2 {
		if err := request("on"); err != nil {
			t.Fatal(err)
		}
	}
	js = listJobs()
	if len(js) != 2 || js[0].id != first || js[1].scope != "media-next:on" || js[1].state != "queued" ||
		!ifDirty(js[1]) || js[1].source != "on" {
		t.Errorf("requests while the job runs: %+v", js)
	}
	if dirty("on") != 1 {
		t.Errorf("a request while the job runs left the source clean")
	}

	// An offline source is requested too.
	if err := request("off"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'media' AND scope_key = 'media:off' AND state = 'queued'
		AND source_id = 'off'`); n != 1 || dirty("off") != 1 {
		t.Errorf("an offline source: %d jobs, dirty %d", n, dirty("off"))
	}

	if err := request("nada"); domain.CodeOf(err) != domain.CodeUnknownSource {
		t.Errorf("an unknown source: %v", err)
	}
}
