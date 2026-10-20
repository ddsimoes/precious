package dates

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"precious/internal/dates/datestest"
)

// r5 review, Addendum G1: the store never runs ANALYZE, so without
// statistics pass 1's and pass 3's window queries must still read entries
// by its rowid range, in rowid order, and never walk the source through an
// index of entries and sort. Addendum K1: the source's ID range they walk
// is read through an index that leads with source_id, not a scan.
func TestJobWindowPlansWithoutStatistics(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	if n := e.count(`SELECT count(*) FROM sqlite_master WHERE name = 'sqlite_stat1'`); n != 0 {
		t.Fatal("the corpus database holds statistics; this guard needs none")
	}
	for _, c := range []struct {
		name, query string
		args        []any
	}{
		{"pass 1", planWindowSQL, []any{0, idSpan, string(corpusSource)}},
		{"pass 3", deriveWindowSQL, []any{0, idSpan, string(corpusSource), deriveWindow}},
	} {
		p := planB(t, e.st.Reader(), c.query, c.args)
		t.Logf("%s:\n%s", c.name, p)
		if !strings.Contains(p, "SEARCH e USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)") || scanOrSortB.MatchString(p) {
			t.Errorf("%s does not read entries by its bounded rowid range without a sort:\n%s", c.name, p)
		}
	}
	p := planB(t, e.st.Reader(), sourceIDsSQL, []any{string(corpusSource)})
	t.Logf("source IDs:\n%s", p)
	if !strings.Contains(p, "(source_id=?)") || scanOrSortB.MatchString(p) {
		t.Errorf("the source's ID range is not read through an index by source_id:\n%s", p)
	}
}

// visitsK records the highest entry ID the instrumented window queries
// (instrumentWindowsK) looked at.
var visitsK struct {
	sync.Mutex
	max int64
}

func init() {
	sqlite.MustRegisterScalarFunction("visit_k", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value,
		error) {
		id, _ := args[0].(int64)
		visitsK.Lock()
		visitsK.max = max(visitsK.max, id)
		visitsK.Unlock()
		return args[1], nil
	})
}

// instrumentWindowsK makes pass 1's and pass 3's window queries record
// every entry their rowid range visits, until the test ends: the source
// test of each row goes through visit_k, which notes its ID.
func instrumentWindowsK(t *testing.T) {
	t.Helper()
	plan, derive := planWindowSQL, deriveWindowSQL
	for _, q := range []*string{&planWindowSQL, &deriveWindowSQL} {
		if strings.Count(*q, "e.source_id = ?") != 1 {
			t.Fatalf("no single source test to instrument in %s", *q)
		}
		*q = strings.Replace(*q, "e.source_id = ?", "visit_k(e.id, e.source_id) = ?", 1)
	}
	t.Cleanup(func() { planWindowSQL, deriveWindowSQL = plan, derive })
}

// maxVisitedK returns the highest entry ID visited since the last call.
func maxVisitedK() int64 {
	visitsK.Lock()
	defer visitsK.Unlock()
	m := visitsK.max
	visitsK.max = 0
	return m
}

// r5 review, Addendum K1: a small source added before a large one has its
// media job read no entry past its own last ID: pass 1's and pass 3's
// windows stop at it, though the last window of each finds fewer rows than
// it may take. Files added to the small source after the large one land
// past an empty span of IDs and the large one's entries: the windows go on
// across them, every photo is enrolled and dated, and still nothing past
// the source's last ID is read.
func TestMediaJobReadsNoEntryPastItsSource(t *testing.T) {
	e := newEnv(t)
	instrumentWindowsK(t)
	card := e.photoDiskA("card", 300)
	nas := e.sfs.Root("/mnt/nas")
	e.addSource("nas", "/mnt/nas", nas, posix)
	// The large source's photos take IDs from 100,000, past two empty spans.
	e.exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 1999)
		INSERT INTO entries (id, source_id, parent_id, name, path, kind, size, ext, file_kind, state, first_seen,
			last_seen, scan_gen)
		SELECT 100000 + i, 'nas', ?, CAST(printf('IMG_%04d.JPG', i) AS BLOB), CAST(printf('IMG_%04d.JPG', i) AS BLOB),
			'file', 4096, 'jpg', 'image', 'present', 0, 0, 0 FROM n`, int64(e.id("nas", "")))
	lastOf := func() int64 {
		return int64(e.count(`SELECT max(id) FROM entries WHERE source_id = 'card'`))
	}
	check := func(when string, media int) {
		t.Helper()
		maxVisitedK()
		e.mediaA("card")
		last := lastOf()
		if got := maxVisitedK(); got == 0 || got > last {
			t.Errorf("%s: the windows read entry %d, past the source's last entry %d", when, got, last)
		}
		if n := e.count(`SELECT count(*) FROM media_meta WHERE source_id = 'card'`); n != media {
			t.Errorf("%s: %d photos enrolled, want %d", when, n, media)
		}
		if n := e.count(`SELECT count(*) FROM media_dates d JOIN entries e ON e.id = d.entry_id
			WHERE e.source_id = 'card'`); n != media {
			t.Errorf("%s: %d photos dated, want %d", when, n, media)
		}
		e.checkSummary("card", when)
	}
	check("before the large source's IDs", 300)

	fotos := card.Child("Fotos")
	for i := range 20 {
		fotos.File(fmt.Sprintf("IMG_%04d.JPG", 301+i), 4096, time.Date(2011, 1, 1, 10, 0, i, 0, time.UTC)).
			Seed(uint64(301 + i))
	}
	e.scan("card")
	if first := e.count(`SELECT min(id) FROM entries WHERE source_id = 'card' AND id > 100000`); first < 102000 {
		t.Fatalf("the new photos start at ID %d, want past the large source's", first)
	}
	check("across the large source's IDs", 320)
	if n := e.count(`SELECT count(*) FROM media_meta WHERE source_id = 'nas'`); n != 0 {
		t.Errorf("the large source got %d media_meta rows from the small one's job", n)
	}
}
