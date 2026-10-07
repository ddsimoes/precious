//go:build slow

package api

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"precious/internal/store"
)

// Shape of the generated search index: two sources, a and b, each of
// searchFolders folders of searchFiles files plus its root: 2,000,002
// entries.
const (
	searchFolders = 1000
	searchFiles   = 999
)

// searchRuns is how many times each search is timed.
const searchRuns = 20

// TestSearchStaysFastAt2MillionEntries generates an index of 2,000,002
// entries (seedSearchScale) and times through the handler, searchRuns times
// each, the first page (the default 200 rows by bytes) and the count
// (count=only) of a search with no filter, then with each duplicate state,
// each over every source and over source a; and the unreadable entries.
// Every page's p95 must stay under 1 s and every count's under 2 s
// (inventory-explorer "Search answers quickly at scale", r2b design D8).
//
// Run with: go test -tags slow -run SearchStaysFast -v ./internal/web/api/
func TestSearchStaysFastAt2MillionEntries(t *testing.T) {
	if raceEnabled {
		t.Skip("time targets are measured without the race detector; make test-slow runs this test in its second pass")
	}
	e := newEnv(t)
	start := time.Now()
	seedSearchScale(t, e.st)
	t.Logf("generated %d entries in %s", 2*(1+searchFolders+searchFolders*searchFiles), time.Since(start).Round(time.Millisecond))
	var rootA int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = 'a' AND path = X''`).Scan(&rootA); err != nil {
		t.Fatal(err)
	}

	var queries []string
	for _, src := range []string{"", "&source=a"} {
		for _, f := range []string{"", "&dup=copies", "&dup=unique", "&dup=unchecked", "&state=unreadable"} {
			queries = append(queries, "/api/search?"+f+src)
		}
	}
	queries = append(queries, fmt.Sprintf("/api/search?within=%d&dup=elsewhere", rootA))

	var allPages, allCounts []time.Duration
	for _, q := range queries {
		var pages, counts []time.Duration
		var p page
		var c struct {
			Count any `json:"count"`
		}
		for range searchRuns {
			pages = append(pages, e.timed(t, q, &p))
			counts = append(counts, e.timed(t, q+"&count=only", &c))
		}
		if len(p.Items) == 0 {
			t.Errorf("%s: no match", q)
		}
		_, pageP95 := percentiles(pages)
		_, countP95 := percentiles(counts)
		t.Logf("%-50s page p95 %8s (max %8s, %d rows)   count p95 %8s (max %8s, %v)", q,
			pageP95.Round(time.Millisecond/10), slices.Max(pages).Round(time.Millisecond/10), len(p.Items),
			countP95.Round(time.Millisecond/10), slices.Max(counts).Round(time.Millisecond/10), c.Count)
		if pageP95 >= time.Second {
			t.Errorf("%s: first page p95 %s, want under 1 s", q, pageP95)
		}
		if countP95 >= 2*time.Second {
			t.Errorf("%s: count p95 %s, want under 2 s", q, countP95)
		}
		allPages, allCounts = append(allPages, pages...), append(allCounts, counts...)
	}
	p50, p95 := percentiles(allPages)
	t.Logf("every first page: %d, p50 %s, p95 %s, max %s", len(allPages), p50, p95, slices.Max(allPages))
	p50, p95 = percentiles(allCounts)
	t.Logf("every count: %d, p50 %s, p95 %s, max %s", len(allCounts), p50, p95, slices.Max(allCounts))
}

// seedSearchScale generates the search index. In each source, folder i
// holds files f000..f998; file k's content state, by k mod 20:
//
//   - 0–9: unique by its size;
//   - 10: sampled;
//   - 11–15: hashed, with one copy at the same place in the other source;
//   - 16: hashed, with no other copy;
//   - 17, 18: pending; 19: changed.
//
// A file's size spreads over 1 KB to 1 MB independently of its state, so
// every state is spread over the size order. Folder i is unreadable when
// i mod 100 is 37 (ten per source), and the roots are partial.
func seedSearchScale(t *testing.T, st *store.Store) {
	t.Helper()
	n := strconv.Itoa
	perSource := 1 + searchFolders + searchFolders*searchFiles
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for s, src := range []string{"a", "b"} {
			base := n(s * perSource)
			for _, q := range []string{
				`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key,
					capabilities, state, mount_point, created_at)
					VALUES ('` + src + `', 'disk ` + src + `', 'uuid', 'uuid-` + src + `', 'ext4', 1, X'', 'dev:` + src + `',
					'{"known":true,"stable_identity":true,"hard_links":true}', 'online', '/` + src + `', 0)`,
				`INSERT INTO entries (id, source_id, parent_id, name, path, kind, partial, state,
					first_seen, last_seen, scan_gen) VALUES (` + base + ` + 1, '` + src + `', NULL, X'', X'', 'directory', 1,
					'present', 0, 0, 1)`,
				`WITH RECURSIVE f(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM f WHERE i < ` + n(searchFolders-1) + `)
				INSERT INTO entries (id, source_id, parent_id, name, path, kind, total_files, state,
					first_seen, last_seen, scan_gen)
				SELECT ` + base + ` + 2 + i, '` + src + `', ` + base + ` + 1, CAST(printf('d%04d', i) AS BLOB),
					CAST(printf('d%04d', i) AS BLOB), 'directory', ` + n(searchFiles) + `,
					iif(i % 100 = 37, 'unreadable', 'present'), 0, 0, 1 FROM f`,
				`WITH RECURSIVE f(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM f WHERE i < ` + n(searchFolders-1) + `),
					j(k) AS (SELECT 0 UNION ALL SELECT k + 1 FROM j WHERE k < ` + n(searchFiles-1) + `)
				INSERT INTO entries (id, source_id, parent_id, name, path, kind, size, total_bytes, total_files,
					mtime_ns, newest_ns, oldest_ns, ext, file_kind, state, first_seen, last_seen, scan_gen)
				SELECT ` + base + ` + ` + n(2+searchFolders) + ` + i * ` + n(searchFiles) + ` + k, '` + src + `',
					` + base + ` + 2 + i, CAST(printf('f%03d.jpg', k) AS BLOB), CAST(printf('d%04d/f%03d.jpg', i, k) AS BLOB),
					'file', 1000 + (i * 7919 + k * 104729) % 1000000, 1000 + (i * 7919 + k * 104729) % 1000000, 1,
					(i * 1000 + k) * 1000000000, (i * 1000 + k) * 1000000000, (i * 1000 + k) * 1000000000,
					'jpg', 'image', 'present', 0, 0, 1
				FROM f, j`,
				`UPDATE entries SET total_bytes = (SELECT sum(c.size) FROM entries c WHERE c.parent_id = entries.id)
					WHERE source_id = '` + src + `' AND kind = 'directory' AND parent_id IS NOT NULL`,
				`UPDATE entries SET total_bytes = (SELECT sum(c.total_bytes) FROM entries c WHERE c.parent_id = entries.id),
					total_files = ` + n(searchFolders*searchFiles) + `
					WHERE source_id = '` + src + `' AND parent_id IS NULL`,
			} {
				if _, err := tx.Exec(q); err != nil {
					return err
				}
			}
		}
		// The place of a file (folder i, file k) is (id - first file) mod
		// perSource; the contents of a shared place are numbered by it, the
		// unique ones after every place.
		place := `((e.id - ` + n(2+searchFolders) + `) % ` + n(perSource) + `)`
		state := `CASE ` + place + ` % ` + n(searchFiles) + ` % 20
			WHEN 10 THEN 'sampled' WHEN 11 THEN 'hashed' WHEN 12 THEN 'hashed' WHEN 13 THEN 'hashed'
			WHEN 14 THEN 'hashed' WHEN 15 THEN 'hashed' WHEN 16 THEN 'hashed'
			WHEN 17 THEN 'pending' WHEN 18 THEN 'pending' WHEN 19 THEN 'changed' ELSE 'unique_size' END`
		content := `CASE WHEN ` + place + ` % ` + n(searchFiles) + ` % 20 BETWEEN 11 AND 15 THEN 1 + ` + place + `
			WHEN ` + place + ` % ` + n(searchFiles) + ` % 20 = 16 THEN ` + n(2*perSource) + ` + e.id END`
		for _, q := range []string{
			`INSERT INTO contents (id, sha256, size)
				SELECT DISTINCT ` + content + `, CAST(printf('%032d', ` + content + `) AS BLOB), e.size
				FROM entries e WHERE e.kind = 'file' AND ` + content + ` IS NOT NULL`,
			`INSERT INTO file_content (entry_id, source_id, state, size, sample, content_id, checked_at)
				SELECT e.id, e.source_id, ` + state + `, e.size,
					iif(` + place + ` % ` + n(searchFiles) + ` % 20 = 10, CAST(printf('%032d', e.id) AS BLOB), NULL),
					` + content + `, 0
				FROM entries e WHERE e.kind = 'file'`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
