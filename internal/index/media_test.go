package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
)

// r5 task 1.4: the index side of media dates (r5 design D3, D11, D15). A
// rescan drops a changed file's media metadata with its digest, a move by
// Precious carries it, a date correction is owner intent that keeps a
// missing row, and ApplyModTime makes the index follow a written time.

// seedMedia gives entry id a media_meta row read from the file as indexed.
func seedMedia(t *testing.T, e *env, id int64) {
	t.Helper()
	if _, err := e.st.Writer().Exec(`INSERT INTO media_meta (entry_id, source_id, state, size, mtime_ns, ctime_ns,
		ino, capture_local, make, read_at) SELECT id, source_id, 'read', size, mtime_ns, ctime_ns, ino,
		'2010-07-17T10:00:00', 'Canon', 0 FROM entries WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

// correct gives entry id a date correction.
func correct(t *testing.T, e *env, id int64) {
	t.Helper()
	if _, err := e.st.Writer().Exec(`INSERT INTO date_corrections (entry_id, kind, shift_s, batch_id, created_at)
		VALUES (?, 'shift', 3600, 'b', 0)`, id); err != nil {
		t.Fatal(err)
	}
}

// cached is one row of a content table as a test compares it: the
// identity it was read from.
type cached struct {
	ok                         bool
	size                       int64
	mtime, ctime, ino          sql.NullInt64
	state, captureLocal, maker sql.NullString
}

// cachedRow reads entry id's row of table, one of contentTables.
func cachedRow(t *testing.T, e *env, table string, id int64) cached {
	t.Helper()
	extra := `NULL, NULL, NULL`
	switch table {
	case "media_meta":
		extra = `state, capture_local, make`
	case "file_content", "archives":
		extra = `state, NULL, NULL`
	}
	var c cached
	err := e.st.Reader().QueryRow(`SELECT size, mtime_ns, ctime_ns, ino, `+extra+` FROM `+table+` WHERE entry_id = ?`, id).
		Scan(&c.size, &c.mtime, &c.ctime, &c.ino, &c.state, &c.captureLocal, &c.maker)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return cached{}
	case err != nil:
		t.Fatal(err)
	}
	c.ok = true
	return c
}

func corrections(t *testing.T, e *env, ids ...int64) int {
	t.Helper()
	n := 0
	for _, id := range ids {
		var k int
		if err := e.st.Reader().QueryRow(`SELECT count(*) FROM date_corrections WHERE entry_id = ?`, id).Scan(&k); err != nil {
			t.Fatal(err)
		}
		n += k
	}
	return n
}

// A rescan that finds a file changed deletes its media_meta row in the
// transaction that updates its entries row: when that delete fails, the
// update is rolled back with it. The file's date correction stays, and an
// unchanged file keeps its metadata.
func TestRescanDropsMediaMetaWithTheUpdate(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", posix)
	fotos := root.Dir("Fotos")
	fotos.File("a.jpg", 100, testNow)
	fotos.File("b.jpg", 100, testNow)
	e.scan("disk")
	rows := e.entries("disk")
	a, b := get(t, rows, "Fotos/a.jpg").ID, get(t, rows, "Fotos/b.jpg").ID
	seedMedia(t, e, a)
	seedMedia(t, e, b)
	correct(t, e, a)
	exec := func(q string) {
		t.Helper()
		if _, err := e.st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	// A delete of a's row that fails aborts the scan's transaction: the
	// entries update of a.jpg is not committed without it.
	exec(fmt.Sprintf(`CREATE TEMP TRIGGER refuse_media_drop BEFORE DELETE ON main.media_meta
		WHEN old.entry_id = %d BEGIN SELECT RAISE(ABORT, 'media_meta drop refused'); END`, a))
	node(t, root, "Fotos/a.jpg").Size(200)
	if err := e.scanWith(context.Background(), "disk", &fakeRuntime{}); err == nil {
		t.Fatal("the scan succeeded although its media_meta delete failed")
	}
	if r := get(t, e.entries("disk"), "Fotos/a.jpg"); r.Size != 100 || r.ScanGen != 1 {
		t.Errorf("a.jpg's entries update committed without its media_meta delete: size %d gen %d", r.Size, r.ScanGen)
	}
	if !cachedRow(t, e, "media_meta", a).ok {
		t.Error("a.jpg's media_meta row went in a failed scan")
	}
	exec(`DROP TRIGGER temp.refuse_media_drop`)

	e.scan("disk")
	if r := get(t, e.entries("disk"), "Fotos/a.jpg"); r.Size != 200 || r.ID != a {
		t.Fatalf("a.jpg after its change: %+v", r)
	}
	if cachedRow(t, e, "media_meta", a).ok {
		t.Error("a.jpg kept its media_meta row after its facts changed")
	}
	if corrections(t, e, a) != 1 {
		t.Error("a.jpg lost its date correction to a rescan")
	}
	if !cachedRow(t, e, "media_meta", b).ok {
		t.Error("the unchanged b.jpg lost its media_meta row")
	}
	rescanWritesNothing(t, e, "disk")
}

// MoveEntry carries a media_meta row that describes the file as indexed to
// the change time the rename gave it, with what was read; a row read from
// another state of the file is left as it is. A rescan then writes nothing
// and keeps the carried row.
func TestMoveEntryKeepsMediaMeta(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", writable)
	fotos := root.Dir("Fotos")
	fotos.File("a.jpg", 10, testNow)
	fotos.File("b.jpg", 20, testNow)
	root.Dir("Novo")
	e.scan("disk")
	rows := e.entries("disk")
	a, b := get(t, rows, "Fotos/a.jpg").ID, get(t, rows, "Fotos/b.jpg").ID
	seedMedia(t, e, a)
	seedMedia(t, e, b)
	if _, err := e.st.Writer().Exec(`UPDATE media_meta SET mtime_ns = mtime_ns - 1 WHERE entry_id = ?`, b); err != nil {
		t.Fatal(err)
	}
	beforeA, beforeB := cachedRow(t, e, "media_meta", a), cachedRow(t, e, "media_meta", b)

	m := newMover(t, e, "disk", "/disk")
	if !m.move(rows, "Fotos/a.jpg", "Novo", "a.jpg") || !m.move(rows, "Fotos/b.jpg", "Novo", "b2.jpg") {
		t.Fatal("the disk refused")
	}
	ctime := ctimeOf(t, e, a)
	if ctime == beforeA.ctime.Int64 {
		t.Fatal("the rename left a.jpg's change time as it was; the test proves nothing")
	}
	afterA := cachedRow(t, e, "media_meta", a)
	want := beforeA
	want.ctime = sql.NullInt64{Int64: ctime, Valid: true}
	if afterA != want {
		t.Errorf("a.jpg's media_meta after the move = %+v, want %+v", afterA, want)
	}
	if got := cachedRow(t, e, "media_meta", b); got != beforeB {
		t.Errorf("b.jpg's stale media_meta was carried: %+v, want %+v", got, beforeB)
	}
	rescanWritesNothing(t, e, "disk")
	if got := cachedRow(t, e, "media_meta", a); got != want {
		t.Errorf("the rescan changed a.jpg's media_meta: %+v", got)
	}
}

// A missing entry whose only owner intent is a date correction is reported
// by MissingIntentAt and IntentBelow, and keeps its path: MoveEntry onto it
// fails with ErrMissingIntent and changes nothing. Rescans keep the
// correction, and so does the file's return.
func TestCorrectionKeepsAMissingEntry(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", writable)
	root.Dir("Fotos").File("a.jpg", 10, testNow)
	root.Dir("Old").File("b.jpg", 20, testNow)
	root.File("c.jpg", 30, testNow)
	root.File("d.jpg", 40, testNow)
	e.scan("disk")
	root.Remove("Old")
	root.Remove("c.jpg")
	e.scan("disk")
	rows := e.entries("disk")
	b, c := get(t, rows, "Old/b.jpg").ID, get(t, rows, "c.jpg").ID
	for _, p := range []string{"Old", "c.jpg"} {
		if missingIntent(t, e, p) {
			t.Fatalf("MissingIntentAt(%q) without a correction = true", p)
		}
	}
	correct(t, e, b)
	correct(t, e, c)

	for _, p := range []string{"Old", "Old/b.jpg", "c.jpg"} {
		if !missingIntent(t, e, p) {
			t.Errorf("MissingIntentAt(%q) with a correction = false", p)
		}
	}
	for p, want := range map[string]bool{"Old": true, "Fotos": false} {
		got, err := IntentBelow(context.Background(), e.st.Reader(), "disk", []byte(p))
		if err != nil || got != want {
			t.Errorf("IntentBelow(%q) = %v, %v; want %v", p, got, err, want)
		}
	}
	before := e.entries("disk")
	for _, mv := range []struct{ from, name string }{{"Fotos", "Old"}, {"d.jpg", "c.jpg"}} {
		err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
			_, _, err := MoveEntry(context.Background(), tx, Move{Source: "disk",
				Entry: domain.EntryID(get(t, rows, mv.from).ID), NewParent: domain.EntryID(get(t, rows, "").ID),
				NewName: []byte(mv.name)})
			return err
		})
		if !errors.Is(err, ErrMissingIntent) {
			t.Errorf("moving %q onto %q: %v, want ErrMissingIntent", mv.from, mv.name, err)
		}
	}
	if d := diffRows(before, e.entries("disk")); len(d) > 0 {
		t.Errorf("a refused move changed rows:\n%s", strings.Join(d, "\n"))
	}
	if corrections(t, e, b, c) != 2 {
		t.Error("a refused move lost a correction")
	}

	rescanWritesNothing(t, e, "disk")
	root.File("c.jpg", 30, testNow)
	e.scan("disk")
	if r := get(t, e.entries("disk"), "c.jpg"); r.ID != c || r.State != "present" {
		t.Errorf("c.jpg came back as %+v, want entry %d present", r, c)
	}
	if corrections(t, e, b, c) != 2 {
		t.Error("a rescan or the file's return lost a correction")
	}
}

// ApplyModTime writes a set_mtime's facts and the file's own newest and
// oldest times (none for an unknown time), carries the file_content,
// archives, and media_meta rows that describe the file as indexed, leaves
// the others, and refuses anything but a present file of the source. The
// folders' figures follow, a folder's own times do not change, and a rescan
// right after finds nothing changed.
func TestApplyModTime(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", writable)
	fotos := root.Dir("Fotos")
	fotos.File("a.jpg", 10, testNow)
	fotos.File("b.jpg", 20, testNow)
	fotos.File("c.jpg", 30, testNow)
	fotos.File("gone.jpg", 40, testNow)
	e.scan("disk")
	fotos.Remove("gone.jpg")
	e.scan("disk")
	rows := e.entries("disk")
	id := func(p string) int64 { return get(t, rows, p).ID }
	a, b, c := id("Fotos/a.jpg"), id("Fotos/b.jpg"), id("Fotos/c.jpg")
	for _, f := range []int64{a, b} {
		if _, err := e.st.Writer().Exec(`INSERT INTO file_content (entry_id, source_id, state, size, mtime_ns,
			ctime_ns, ino) SELECT id, source_id, 'pending', size, mtime_ns, ctime_ns, ino FROM entries WHERE id = ?`, f); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.Writer().Exec(`INSERT INTO archives (entry_id, format, state, size, mtime_ns, ctime_ns, ino)
			SELECT id, 'zip', 'complete', size, mtime_ns, ctime_ns, ino FROM entries WHERE id = ?`, f); err != nil {
			t.Fatal(err)
		}
		seedMedia(t, e, f)
	}
	// b's digest row was read from another inode and its metadata from
	// another time: neither describes the file as indexed.
	for _, q := range []string{`UPDATE file_content SET ino = ino + 1000 WHERE entry_id = ?`,
		`UPDATE media_meta SET mtime_ns = mtime_ns + 1 WHERE entry_id = ?`} {
		if _, err := e.st.Writer().Exec(q, b); err != nil {
			t.Fatal(err)
		}
	}
	staleB := map[string]cached{}
	for _, table := range contentTables {
		staleB[table] = cachedRow(t, e, table, b)
	}

	capture := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)
	times := map[int64]time.Time{a: capture, b: capture.Add(time.Hour), c: time.Unix(0, 0)}
	d := disk{t: t, e: e, root: "/disk"}
	apply := func(m ModTime) error {
		return e.st.Write(context.Background(), func(tx *sql.Tx) error {
			return ApplyModTime(context.Background(), tx, m)
		})
	}

	// Refusals change nothing.
	before := e.entries("disk")
	facts := d.facts("Fotos/a.jpg")
	for name, m := range map[string]ModTime{
		"a folder":       {Source: "disk", Entry: domain.EntryID(id("Fotos")), Facts: d.facts("Fotos")},
		"the root":       {Source: "disk", Entry: domain.EntryID(id("")), Facts: d.facts("")},
		"another source": {Source: "other", Entry: domain.EntryID(a), Facts: facts},
		"a missing file": {Source: "disk", Entry: domain.EntryID(id("Fotos/gone.jpg")), Facts: facts},
		"no entry":       {Source: "disk", Entry: 1 << 40, Facts: facts},
	} {
		if err := apply(m); err == nil {
			t.Errorf("ApplyModTime of %s succeeded", name)
		}
	}
	if diff := diffRows(before, e.entries("disk")); len(diff) > 0 {
		t.Errorf("a refused ApplyModTime changed rows:\n%s", strings.Join(diff, "\n"))
	}

	folder, folderCtime := get(t, rows, "Fotos"), ctimeOf(t, e, id("Fotos"))
	paths := map[int64]string{a: "Fotos/a.jpg", b: "Fotos/b.jpg", c: "Fotos/c.jpg"}
	written := map[int64]PostFacts{}
	for _, p := range []string{"Fotos/a.jpg", "Fotos/b.jpg", "Fotos/c.jpg"} {
		f := id(p)
		node(t, root, p).ModTime(times[f])
		written[f] = d.facts(p)
		if written[f].CtimeNs == ctimeOf(t, e, f) {
			t.Fatalf("%s: setting the time left the change time as it was", p)
		}
		if err := apply(ModTime{Source: "disk", Entry: domain.EntryID(f), Facts: written[f]}); err != nil {
			t.Fatalf("ApplyModTime %s: %v", p, err)
		}
	}

	after := e.entries("disk")
	for f, ft := range written {
		r := get(t, after, paths[f])
		if !r.MTime.Valid || r.MTime.Int64 != times[f].UnixNano() || ctimeOf(t, e, f) != ft.CtimeNs ||
			uint64(r.Ino.Int64) != ft.Ino || uint64(r.Dev.Int64) != ft.Dev {
			t.Errorf("%s: facts %v ctime %d, want %+v", r.Path, r.MTime, ctimeOf(t, e, f), ft)
		}
		known := domain.KnownModTime(times[f].UnixNano())
		if r.Newest.Valid != known || r.Oldest.Valid != known ||
			known && (r.Newest.Int64 != times[f].UnixNano() || r.Oldest.Int64 != times[f].UnixNano()) {
			t.Errorf("%s: own newest %v oldest %v, want the time when known", r.Path, r.Newest, r.Oldest)
		}
	}
	for _, table := range contentTables {
		got := cachedRow(t, e, table, a)
		if !got.ok || got.mtime.Int64 != times[a].UnixNano() || got.ctime.Int64 != written[a].CtimeNs ||
			uint64(got.ino.Int64) != written[a].Ino || got.size != 10 {
			t.Errorf("a.jpg's %s row was not carried: %+v", table, got)
		}
	}
	if got := cachedRow(t, e, "media_meta", a); got.state.String != "read" || got.captureLocal.String != "2010-07-17T10:00:00" {
		t.Errorf("a.jpg's media_meta lost what was read: %+v", got)
	}
	for table, want := range staleB {
		got := cachedRow(t, e, table, b)
		if table == "archives" {
			if got.mtime.Int64 != times[b].UnixNano() || got.ctime.Int64 != written[b].CtimeNs {
				t.Errorf("b.jpg's matching archives row was not carried: %+v", got)
			}
		} else if got != want {
			t.Errorf("b.jpg's %s row, read from another state, was carried: %+v, want %+v", table, got, want)
		}
	}
	// The folder's figures follow its files; its own times do not change.
	f := get(t, after, "Fotos")
	if f.MTime != folder.MTime || ctimeOf(t, e, folder.ID) != folderCtime {
		t.Errorf("Fotos's own time changed: %v -> %v", folder.MTime, f.MTime)
	}
	if f.Newest.Int64 != times[b].UnixNano() || f.Oldest.Int64 != times[a].UnixNano() {
		t.Errorf("Fotos newest %v oldest %v, want b's and a's times", f.Newest, f.Oldest)
	}

	rescanWritesNothing(t, e, "disk")
	for _, table := range contentTables {
		if !cachedRow(t, e, table, a).ok {
			t.Errorf("the rescan dropped a.jpg's %s row", table)
		}
	}
}
