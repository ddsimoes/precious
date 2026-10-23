//go:build slow

package api

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"precious/internal/fsaccess/synthfs"
)

// placesSlowEntries is the size of the index: two scanned sources.
const placesSlowEntries = 2_000_000

// TestPlacesPagesStayFastAt2MillionEntries is r5c design D10's gate: in a
// 2,000,000-entry index of two sources, the first page (200 rows) of a
// 30,000-file folder answers in under 1 s at p95 with sort=only_here, and
// with hide=other_source and sort=bytes. The fixture:
//
//   - five folders of 30,000 files and one of 100,000 (recorded, not gated);
//   - a second source holding copies of half of their files;
//   - a quarter of their files copied on their own source, an eighth also
//     inside an archive, and one in 200 with 50 copies;
//   - the rest unique by size, or pending;
//   - a complete archive of 50,000 members at its top (recorded, not gated),
//     half of them with the contents above.
//
// Each sample is a request no earlier request prepared: the server keeps no
// cache of places, so every first page places every child of its folder.
//
// Run with: go test -tags slow -run PlacesPagesStayFast -v ./internal/web/api/
func TestPlacesPagesStayFastAt2MillionEntries(t *testing.T) {
	e := newEnv(t)
	sfs := synthfs.New()
	const (
		flat     = 30_000
		huge     = 100_000
		copies   = 130_000
		members  = 50_000
		perFlats = 5
	)
	root := sfs.Root("/big")
	for i := range perFlats {
		root.Generated(fmt.Sprintf("flat%d", i), flat, flat)
	}
	root.Generated("huge", huge, huge)
	// The root, its folders, and the rest in deep; the copies source holds
	// its root, one folder, and its copies.
	bigEntries := placesSlowEntries - (copies + 2)
	root.Generated("deep", bigEntries-1-(perFlats+2)-perFlats*flat-huge, 12)
	other := sfs.Root("/copies")
	other.Generated("copies", copies, 1000)

	start := time.Now()
	rootID := e.scanSynth(t, sfs, "big", "/big", root)
	e.scanSynth(t, sfs, "b", "/copies", other)
	var n int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM entries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != placesSlowEntries {
		t.Fatalf("indexed %d entries, want %d", n, placesSlowEntries)
	}
	t.Logf("scan of %d entries: %s", n, time.Since(start).Round(time.Millisecond))

	start = time.Now()
	// The listed files: every non-empty file of the six folders, numbered
	// in its folder (i) and among the files of its role (r).
	e.exec(t, `CREATE TABLE listed AS SELECT e.id, e.size,
		row_number() OVER (PARTITION BY e.parent_id ORDER BY e.id) - 1 AS i
		FROM entries e JOIN entries p ON p.id = e.parent_id
		WHERE p.parent_id = ? AND (p.name LIKE 'flat%' OR p.name = CAST('huge' AS BLOB))
			AND e.kind = 'file' AND e.size > 0`, int64(rootID))
	e.exec(t, `CREATE TABLE deepf AS SELECT e.id, e.size, row_number() OVER (ORDER BY e.id) - 1 AS r
		FROM entries e WHERE e.source_id = 'big' AND e.path >= CAST('deep/' AS BLOB) AND e.path < CAST('deep0' AS BLOB)
			AND e.kind = 'file' AND e.size > 0`)
	e.exec(t, `CREATE TABLE bf AS SELECT id, row_number() OVER (ORDER BY id) - 1 AS r
		FROM entries WHERE source_id = 'b' AND kind = 'file' AND size > 0`)
	// Hashed: every listed file but i % 4 = 3, its content its own ID.
	e.exec(t, `INSERT INTO contents (id, sha256, size) SELECT id, randomblob(32), size FROM listed WHERE i % 4 <> 3`)
	e.exec(t, `INSERT INTO file_content (entry_id, source_id, state, size, content_id, checked_at)
		SELECT id, 'big', 'hashed', size, id, 0 FROM listed WHERE i % 4 <> 3`)
	e.exec(t, `INSERT INTO file_content (entry_id, source_id, state, size)
		SELECT id, 'big', CASE WHEN i % 8 = 7 THEN 'pending' ELSE 'unique_size' END, size FROM listed WHERE i % 4 = 3`)
	// Even files: a copy on the second source.
	e.exec(t, `INSERT INTO file_content (entry_id, source_id, state, size, content_id, checked_at)
		SELECT b.id, 'b', 'hashed', l.size, l.id, 0
		FROM (SELECT id, size, row_number() OVER (ORDER BY id) - 1 AS r FROM listed WHERE i % 2 = 0) l
		JOIN bf b ON b.r = l.r`)
	// i % 4 = 1: a copy in deep, on the same source.
	e.exec(t, `CREATE TABLE same AS SELECT id, size, row_number() OVER (ORDER BY id) - 1 AS r FROM listed WHERE i % 4 = 1`)
	e.exec(t, `INSERT INTO file_content (entry_id, source_id, state, size, content_id, checked_at)
		SELECT d.id, 'big', 'hashed', d.size, s.id, 0 FROM same s JOIN deepf d ON d.r = s.r`)
	// One in 200: 49 more copies in deep, after those.
	e.exec(t, `INSERT INTO file_content (entry_id, source_id, state, size, content_id, checked_at)
		SELECT d.id, 'big', 'hashed', d.size, m.id, 0
		FROM (SELECT id, row_number() OVER (ORDER BY id) - 1 AS r FROM listed WHERE i % 200 = 6) m
		JOIN deepf d ON d.r >= (SELECT count(*) FROM same) AND (d.r - (SELECT count(*) FROM same)) / 49 = m.r`)
	// The archive: a deep file no content row names, listed whole, with
	// 50,000 members at its top; the first ones carry the contents of the
	// files with i % 8 = 0.
	var arc int64
	if err := e.st.Reader().QueryRow(`SELECT d.id FROM deepf d LEFT JOIN file_content f ON f.entry_id = d.id
		WHERE f.entry_id IS NULL ORDER BY d.id DESC LIMIT 1`).Scan(&arc); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO file_content (entry_id, source_id, state, size) SELECT id, 'big', 'unique_size', size
		FROM entries WHERE id = ?`, arc)
	e.exec(t, `INSERT INTO archives (entry_id, format, state, size, mtime_ns, members, unpacked_bytes, listed_at)
		SELECT id, 'zip', 'complete', size, ifnull(mtime_ns, 0), ?, 0, 0 FROM entries WHERE id = ?`, members, arc)
	e.exec(t, `WITH RECURSIVE k(k) AS (SELECT 0 UNION ALL SELECT k + 1 FROM k WHERE k + 1 < ?),
		src AS (SELECT id, size, row_number() OVER (ORDER BY id) - 1 AS r FROM listed WHERE i % 8 = 0)
		INSERT INTO archive_members (archive_id, name, path, kind, size, total_bytes, total_files, state, content_id)
		SELECT ?, CAST('m' || k AS BLOB), CAST('m' || k AS BLOB), 'file', ifnull(s.size, 1000 + k), ifnull(s.size, 1000 + k), 1,
			CASE WHEN s.id IS NULL THEN 'unique_size' ELSE 'hashed' END, s.id
		FROM k LEFT JOIN src s ON s.r = k.k AND k.k < ?`, members, arc, members/2)
	e.exec(t, `UPDATE entries SET nlink = 1 WHERE nlink IS NULL AND kind = 'file'`)
	e.exec(t, `ANALYZE`)
	t.Logf("contents and archive seeded in %s", time.Since(start).Round(time.Millisecond))

	var top page
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=name&limit=1000", rootID), 200, &top)
	folder := map[string]string{}
	for _, r := range top.Items {
		folder[r.Name] = r.ID
	}

	type sample struct {
		d    time.Duration
		rows int
	}
	timeFirst := func(id, query string) sample {
		t.Helper()
		var p ppage
		d := e.timed(t, "/api/entries/"+id+"/children?"+query, &p)
		return sample{d, len(p.Items)}
	}
	gate := func(name, query string) {
		t.Helper()
		var ds []time.Duration
		for round := range 2 {
			for i := range perFlats {
				for _, order := range []string{"desc", "asc"} {
					s := timeFirst(folder[fmt.Sprintf("flat%d", i)], query+"&order="+order)
					if s.rows != 200 {
						t.Fatalf("%s: first page of %d rows", name, s.rows)
					}
					if round == 0 && i == 0 && order == "desc" {
						t.Logf("%s: the first request %s", name, s.d.Round(time.Millisecond))
					}
					ds = append(ds, s.d)
				}
			}
		}
		p50, p95 := percentiles(ds)
		t.Logf("%s, first page of a %d-file folder: %d samples, p50 %s, p95 %s, max %s", name, flat, len(ds),
			p50.Round(time.Millisecond), p95.Round(time.Millisecond), slices.Max(ds).Round(time.Millisecond))
		if p95 >= time.Second {
			t.Errorf("%s: p95 %s, want under 1 s (design D10)", name, p95)
		}
	}
	base := timeFirst(folder["flat0"], "sort=bytes")
	t.Logf("baseline, sort=bytes without places: %s", base.d.Round(time.Millisecond))
	gate("sort=only_here", "places=1&sort=only_here")
	gate("hide and sort=bytes", "places=1&hide=other_source&sort=bytes")

	for _, q := range []string{"places=1&sort=only_here", "places=1&hide=other_source&sort=bytes", "places=1&sort=bytes"} {
		s := timeFirst(folder["huge"], q)
		t.Logf("recorded: %d-file folder, %s: %s (%d rows)", huge, q, s.d.Round(time.Millisecond), s.rows)
	}
	arcRef := fmt.Sprint(arc)
	for _, q := range []string{"sort=bytes", "places=1&sort=bytes", "places=1&sort=only_here",
		"places=1&hide=other_source&sort=bytes"} {
		s := timeFirst(arcRef, q)
		t.Logf("recorded: %d-member archive level, %s: %s (%d rows)", members, q, s.d.Round(time.Millisecond), s.rows)
	}
	var tm ptreemap
	d := e.timed(t, "/api/entries/"+arcRef+"/treemap?places=1", &tm)
	t.Logf("recorded: %d-member archive treemap with places: %s", members, d.Round(time.Millisecond))
	var det pdetail
	d = e.timed(t, "/api/entries/"+arcRef+"?places=1", &det)
	t.Logf("recorded: %d-member archive entry with member_places: %s", members, d.Round(time.Millisecond))
}
