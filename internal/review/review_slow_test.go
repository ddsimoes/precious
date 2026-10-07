//go:build slow

package review

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"testing"
	"time"

	"precious/internal/domain"
)

// Shape of the generated index: one source of slowFolders folders of
// slowFiles files each, plus the root: 2,000,001 entries.
const (
	slowFolders = 2000
	slowFiles   = 999
)

// TestReviewPagesStayFastAt2MillionEntries generates an index of 2,000,001
// entries with the content state and relations a long hashing run leaves,
// refreshes its review rows, then times (D20):
//
//   - every review list's pages, for all sources and for one, open and
//     decided, the first ten pages of 50 rows of each: p95 < 300 ms;
//   - the eight cards, for all sources and for one: p95 < 1 s.
//
// The index, by folder number f (f mod 10):
//
//   - 0: system junk, 1: cache (folder and files classified);
//   - 2: application installations, groups of family programs, three
//     indicators each (the rescue rows); 3: an installer download;
//   - 4–9: personal photos.
//
// Each folder's files are 60% unique by size, 30% hashed, 10% pending. A
// hashed file has one copy at the same place of the paired folder (f xor
// 1): 300,000 duplicate groups. One pair in ten is a listed same relation,
// one in twenty an overlap. One file in twenty is discarded.
//
// Run with: go test -tags slow -run Review -v ./internal/review/
func TestReviewPagesStayFastAt2MillionEntries(t *testing.T) {
	if raceEnabled {
		t.Skip("time targets are measured without the race detector; make test-slow runs this test in its second pass")
	}
	w := newWorld(t)
	ctx := context.Background()
	start := time.Now()
	generate(t, w)
	t.Logf("generated %d entries in %s", slowFolders*(slowFiles+1)+1, time.Since(start).Round(time.Millisecond))

	start = time.Now()
	w.refresh()
	t.Logf("refresh: %s", time.Since(start).Round(time.Millisecond))
	var counts []string
	rows, err := w.st.Reader().Query(`SELECT list || ' ' || count(*) FROM review_rows GROUP BY list ORDER BY list`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		counts = append(counts, s)
	}
	rows.Close()
	t.Logf("rows: %v", counts)

	var cards []time.Duration
	for range 10 {
		for _, src := range []domain.SourceID{"", "big"} {
			s := time.Now()
			if _, err := Cards(ctx, w.st.Reader(), src); err != nil {
				t.Fatal(err)
			}
			cards = append(cards, time.Since(s))
		}
	}
	var pages []time.Duration
	worst := map[List]time.Duration{}
	for _, l := range CardLists {
		for _, src := range []domain.SourceID{"", "big"} {
			for _, decided := range []bool{false, true} {
				cursor := ""
				for range 10 {
					s := time.Now()
					p, err := Rows(ctx, w.st.Reader(), l, src, decided, cursor, 50)
					if err != nil {
						t.Fatal(err)
					}
					d := time.Since(s)
					pages = append(pages, d)
					worst[l] = max(worst[l], d)
					if cursor = p.NextCursor; cursor == "" {
						break
					}
				}
			}
		}
	}
	cp, pp := p95(cards), p95(pages)
	t.Logf("cards: %d runs, p95 %s, max %s", len(cards), cp, slices.Max(cards))
	t.Logf("pages: %d pages, p95 %s, max %s; worst by list %v", len(pages), pp, slices.Max(pages), worst)
	if cp >= time.Second {
		t.Errorf("cards p95 %s, want < 1 s", cp)
	}
	if pp >= 300*time.Millisecond {
		t.Errorf("pages p95 %s, want < 300 ms", pp)
	}
}

func p95(d []time.Duration) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	return s[(len(s)*95+99)/100-1]
}

// generate writes the index of TestReviewPagesStayFastAt2MillionEntries.
func generate(t *testing.T, w *world) {
	t.Helper()
	err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key,
				capabilities, state, mount_point, created_at)
				VALUES ('big', 'big', 'uuid', 'uuid-big', 'ext4', 1, X'', 'dev:big',
				'{"known":true,"stable_identity":true,"hard_links":true}', 'online', '/big', 0)`,
			`INSERT INTO entries (id, source_id, parent_id, name, path, kind, total_bytes, total_files, state,
				first_seen, last_seen, scan_gen) VALUES (1, 'big', NULL, X'', X'', 'directory', 0, 0, 'present', 0, 0, 1)`,
			// Folders: id f+2.
			`WITH RECURSIVE f(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM f WHERE i < ` + itoa(slowFolders-1) + `)
			INSERT INTO entries (id, source_id, parent_id, name, path, kind, total_bytes, total_files, category, family,
				is_group, state, first_seen, last_seen, scan_gen)
			SELECT i + 2, 'big', 1, CAST(printf('d%04d', i) AS BLOB), CAST(printf('d%04d', i) AS BLOB), 'directory',
				0, ` + itoa(slowFiles) + `,
				CASE i % 10 WHEN 0 THEN 'system_junk' WHEN 1 THEN 'cache' WHEN 2 THEN 'application_installation'
					WHEN 3 THEN 'installer_download' ELSE 'personal_media' END,
				CASE i % 10 WHEN 0 THEN 'disposable' WHEN 1 THEN 'disposable' WHEN 2 THEN 'programs'
					WHEN 3 THEN 'programs' ELSE 'personal' END,
				i % 10 = 2, 'present', 0, 0, 1 FROM f`,
			// Files: id 2002 + f*slowFiles + j.
			`WITH RECURSIVE f(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM f WHERE i < ` + itoa(slowFolders-1) + `),
				j(k) AS (SELECT 0 UNION ALL SELECT k + 1 FROM j WHERE k < ` + itoa(slowFiles-1) + `)
			INSERT INTO entries (id, source_id, parent_id, name, path, kind, size, total_bytes, total_files, mtime_ns,
				file_kind, category, family, state, first_seen, last_seen, scan_gen, decision, eff_decision)
			SELECT 2002 + i * ` + itoa(slowFiles) + ` + k, 'big', i + 2, CAST(printf('f%03d.jpg', k) AS BLOB),
				CAST(printf('d%04d/f%03d.jpg', i, k) AS BLOB), 'file', 1000 + k * 7 + i, 1000 + k * 7 + i, 1,
				((i * 7919 + k * 104729) % 1000000) * 1000000000,
				CASE WHEN i % 10 >= 4 THEN 'image' ELSE 'other' END,
				CASE i % 10 WHEN 0 THEN 'system_junk' WHEN 1 THEN 'cache' ELSE NULL END,
				CASE i % 10 WHEN 0 THEN 'disposable' WHEN 1 THEN 'disposable' ELSE NULL END,
				'present', 0, 0, 1,
				CASE WHEN k % 20 = 0 THEN 'discard' END, CASE WHEN k % 20 = 0 THEN 'discard' ELSE 'undecided' END
			FROM f, j`,
			`UPDATE entries SET total_bytes = (SELECT sum(c.size) FROM entries c WHERE c.parent_id = entries.id)
				WHERE kind = 'directory' AND parent_id = 1`,
			// Contents: one per hashed (pair, file) place.
			`WITH RECURSIVE p(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM p WHERE i < ` + itoa(slowFolders/2-1) + `),
				j(k) AS (SELECT 0 UNION ALL SELECT k + 1 FROM j WHERE k < ` + itoa(slowFiles-1) + `)
			INSERT INTO contents (id, sha256, size)
			SELECT 1 + i * ` + itoa(slowFiles) + ` + k, CAST(printf('%032d', 1 + i * ` + itoa(slowFiles) + ` + k) AS BLOB), 1000
			FROM p, j WHERE k % 10 BETWEEN 6 AND 8`,
			`INSERT INTO file_content (entry_id, source_id, state, size, content_id)
			SELECT e.id, 'big', CASE (e.id - 2002) % ` + itoa(slowFiles) + ` % 10 WHEN 6 THEN 'hashed' WHEN 7 THEN 'hashed'
					WHEN 8 THEN 'hashed' WHEN 9 THEN 'pending' ELSE 'unique_size' END, e.size,
				CASE WHEN (e.id - 2002) % ` + itoa(slowFiles) + ` % 10 BETWEEN 6 AND 8
					THEN 1 + ((e.id - 2002) / ` + itoa(slowFiles) + ` / 2) * ` + itoa(slowFiles) + ` + (e.id - 2002) % ` + itoa(slowFiles) + ` END
			FROM entries e WHERE e.kind = 'file'`,
			// Relations: pair p (folders 2p, 2p+1) is same when p % 10 = 4,
			// overlap when p % 20 = 5.
			`WITH RECURSIVE p(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM p WHERE i < ` + itoa(slowFolders/2-1) + `)
			INSERT INTO relations (gen, kind, a_entry, b_entry, matched_bytes, redundant_bytes, a_bytes, a_files,
				b_bytes, b_files, a_only_files, a_only_bytes, b_only_files, b_only_bytes)
			SELECT 1, CASE WHEN i % 10 = 4 THEN 'same' ELSE 'overlap' END, 2 * i + 2, 2 * i + 3,
				1000000, 1000000, 1000000, ` + itoa(slowFiles) + `, 1000000, ` + itoa(slowFiles) + `, 0, 0, 0, 0
			FROM p WHERE i % 10 = 4 OR i % 20 = 5`,
			// The groups' indicators.
			`INSERT INTO dir_stats (entry_id, dirs, files, symlinks, specials, unreadable, mount_boundaries,
				by_kind, by_year, by_family, signals, indicators, inside)
			SELECT e.id, 0, ` + itoa(slowFiles) + `, 0, 0, 0, 0, '{}', '{}', '{}', '{}',
				json_array(json_object('entry_id', CAST(2002 + (e.id - 2) * ` + itoa(slowFiles) + ` AS TEXT)),
					json_object('entry_id', CAST(2003 + (e.id - 2) * ` + itoa(slowFiles) + ` AS TEXT)),
					json_object('entry_id', CAST(2004 + (e.id - 2) * ` + itoa(slowFiles) + ` AS TEXT))), '[]'
			FROM entries e WHERE e.is_group = 1`,
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

func itoa(n int) string { return strconv.Itoa(n) }
