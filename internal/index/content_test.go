package index

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/rules"
	"precious/policies"
)

// Task 1.4: the scanner compares change times, drops the content rows of a
// file whose own facts changed, and runs its after-scan hook once per
// successful scan.

// seedContent gives the file at path a file_content row and, when archive,
// a complete zip listing with one member.
func seedContent(t *testing.T, e *env, src domain.SourceID, path string, archive bool) int64 {
	t.Helper()
	id := get(t, e.entries(src), path).ID
	db := e.st.Writer()
	if _, err := db.Exec(`INSERT INTO file_content (entry_id, source_id, state, size) VALUES (?, ?, 'pending', 1)`,
		id, string(src)); err != nil {
		t.Fatal(err)
	}
	if archive {
		if _, err := db.Exec(`INSERT INTO archives (entry_id, format, state, size, mtime_ns, members)
			VALUES (?, 'zip', 'complete', 1, 1, 1)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO archive_members (archive_id, name, path, kind, size, state)
			VALUES (?, X'61', X'61', 'file', 1, 'unique_size')`, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// contentRows counts the file_content, archives, and archive_members rows of
// entry id.
func contentRows(t *testing.T, e *env, id int64) (files, archives, members int) {
	t.Helper()
	err := e.st.Reader().QueryRow(`SELECT (SELECT count(*) FROM file_content WHERE entry_id = ?),
		(SELECT count(*) FROM archives WHERE entry_id = ?),
		(SELECT count(*) FROM archive_members WHERE archive_id = ?)`, id, id, id).Scan(&files, &archives, &members)
	if err != nil {
		t.Fatal(err)
	}
	return files, archives, members
}

func ctimeOf(t *testing.T, e *env, id int64) int64 {
	t.Helper()
	var ns int64
	if err := e.st.Reader().QueryRow(`SELECT ctime_ns FROM entries WHERE id = ?`, id).Scan(&ns); err != nil {
		t.Fatal(err)
	}
	return ns
}

// A change of the change time alone (a restored modification time) updates
// the row with the new change time and drops its digest and listing rows; a
// size change does too; untouched files keep theirs; and a rescan after
// that writes nothing.
func TestChangeTimeOnlyChangeDropsContent(t *testing.T) {
	e := newEnv(t)
	root := diffTree(e, posix)
	e.scan("disk")
	a := seedContent(t, e, "disk", "docs/a.txt", true)
	b := seedContent(t, e, "disk", "docs/b.txt", true)
	keep := seedContent(t, e, "disk", "keep.txt", true)
	before := ctimeOf(t, e, a)

	later := time.Unix(0, before).Add(time.Hour)
	node(t, root, "docs/a.txt").Ctime(later)
	node(t, root, "docs/b.txt").Size(201)
	e.scan("disk")

	rows := e.entries("disk")
	if r := get(t, rows, "docs/a.txt"); r.ScanGen != 2 || r.ID != a {
		t.Errorf("a.txt after a change-time change: gen %d id %d, want updated in place", r.ScanGen, r.ID)
	}
	if got := ctimeOf(t, e, a); got != later.UnixNano() {
		t.Errorf("a.txt ctime_ns = %d, want the observed %d", got, later.UnixNano())
	}
	for name, id := range map[string]int64{"a.txt": a, "b.txt": b} {
		if f, ar, m := contentRows(t, e, id); f+ar+m != 0 {
			t.Errorf("%s kept content rows after its facts changed: %d %d %d", name, f, ar, m)
		}
	}
	if f, ar, m := contentRows(t, e, keep); f != 1 || ar != 1 || m != 1 {
		t.Errorf("unchanged keep.txt lost content rows: %d %d %d", f, ar, m)
	}
	if r := get(t, rows, "keep.txt"); r.ScanGen != 1 {
		t.Errorf("unchanged keep.txt rewritten (gen %d)", r.ScanGen)
	}

	writes := e.writes()
	e.scan("disk")
	if n := writes(); n != 0 {
		t.Errorf("rescan after the change wrote %d rows", n)
	}
}

// On a local-time filesystem a daylight-saving shift moves the change time
// with the modification time; within the tolerance the file is unchanged.
func TestChangeTimeComparedWithinTolerance(t *testing.T) {
	e := newEnv(t)
	root := e.disk("card", "/src/card", fat)
	f := root.File("DSC0001.JPG", 1000, mtime)
	e.scan("card")
	id := seedContent(t, e, "card", "DSC0001.JPG", false)
	ct := time.Unix(0, ctimeOf(t, e, id))
	f.ModTime(mtime.Add(time.Hour))
	f.Ctime(ct.Add(time.Hour))
	writes := e.writes()
	e.scan("card")
	if n := writes(); n != 0 {
		t.Errorf("a daylight-saving shift wrote %d rows", n)
	}
	if files, _, _ := contentRows(t, e, id); files != 1 {
		t.Error("a daylight-saving shift dropped the digest row")
	}
}

// A rules change rewrites the classification of every affected row but
// keeps their content rows: only a file's own facts invalidate a digest.
// So does a file that comes back from missing with the same facts.
func TestRulesBumpAndReturnKeepContent(t *testing.T) {
	e := newEnv(t)
	diffTree(e, posix)
	e.scan("disk")
	a := seedContent(t, e, "disk", "docs/a.txt", true)
	keep := seedContent(t, e, "disk", "keep.txt", false)

	bumped := bytes.Replace(policies.Rules, []byte(`version = "rules-v2"`), []byte(`version = "rules-v2-test"`), 1)
	bumped = append(bumped, []byte(`
[[rules]]
id = "test_text_is_temporary"
target = "file"
name = ["*.txt"]
priority = 100
category = "temporary_data"
explain = "A test rule."
`)...)
	pol, err := rules.Load(policies.Markers, bumped)
	if err != nil {
		t.Fatal(err)
	}
	e.pol = pol
	before := get(t, e.entries("disk"), "keep.txt")
	e.scan("disk")
	after := get(t, e.entries("disk"), "keep.txt")
	if after.ScanGen != 2 || after.Category == before.Category {
		t.Fatalf("the rules change did not rewrite keep.txt: gen %d category %v -> %v",
			after.ScanGen, before.Category, after.Category)
	}
	if f, ar, m := contentRows(t, e, a); f != 1 || ar != 1 || m != 1 {
		t.Errorf("a.txt lost content rows to a rules change: %d %d %d", f, ar, m)
	}
	if f, _, _ := contentRows(t, e, keep); f != 1 {
		t.Error("keep.txt lost its digest row to a rules change")
	}

	if _, err := e.st.Writer().Exec(`UPDATE entries SET state = 'missing', missing_since = 1 WHERE id = ?`, keep); err != nil {
		t.Fatal(err)
	}
	e.scan("disk")
	if r := get(t, e.entries("disk"), "keep.txt"); r.State != "present" || r.ScanGen != 3 {
		t.Fatalf("keep.txt did not come back: %+v", r)
	}
	if f, _, _ := contentRows(t, e, keep); f != 1 {
		t.Error("keep.txt lost its digest row on returning with the same facts")
	}
}

// The after-scan hook runs once per successful scan with the source, and
// never for a failed or a cancelled one.
func TestOnScanDoneRunsOnlyAfterSuccess(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/src/disk", posix)
	root.Dir("a").Generate(300, 6)
	e.cfg.BatchSize = 25
	var calls []domain.SourceID
	e.onDone = func(ctx context.Context, src domain.SourceID) {
		if ctx == nil {
			t.Error("hook called without a context")
		}
		calls = append(calls, src)
	}

	e.scan("disk")
	if len(calls) != 1 || calls[0] != "disk" {
		t.Fatalf("after one scan the hook ran %v", calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rt := &fakeRuntime{onYield: func(n int) {
		if n == 3 {
			cancel()
		}
	}}
	if err := e.scanWith(ctx, "disk", rt); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan returned %v", err)
	}
	cancel()

	// The job stops being this attempt's mid-scan: the scan fails.
	rt = &fakeRuntime{onYield: func(n int) {
		if n == 3 {
			if _, err := e.st.Writer().Exec(`UPDATE jobs SET state = 'failed' WHERE state = 'running'`); err != nil {
				t.Error(err)
			}
		}
	}}
	if err := e.scanWith(context.Background(), "disk", rt); err == nil {
		t.Fatal("the scan that lost its job succeeded")
	}
	if len(calls) != 1 {
		t.Fatalf("the hook ran after a cancelled or failed scan: %v", calls)
	}

	e.scan("disk")
	if len(calls) != 2 {
		t.Fatalf("after a second successful scan the hook ran %d times", len(calls))
	}
}
