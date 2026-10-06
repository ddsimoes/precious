package search

import (
	"context"
	"database/sql"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/store/storetest"
)

// BenchmarkPage times a first page (200 rows and the capped count) of the
// main query shapes over 200,000 entries: 200 folders of 1,000 files, one
// in ten a .jpg, the folder d0042 tagged and kept.
func BenchmarkPage(b *testing.B) {
	st := storetest.Open(b)
	seeded := indextest.Seed(b, st, indextest.Tree{Source: "bench", CreateSource: true})
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 199)
			INSERT INTO entries (source_id, parent_id, name, path, kind, total_bytes, total_files,
				state, first_seen, last_seen, scan_gen)
			SELECT 'bench', ?, CAST(printf('d%04d', i) AS BLOB), CAST(printf('d%04d', i) AS BLOB), 'directory',
				1000, 1000, 'present', 0, 0, 1 FROM n`, []any{int64(seeded.Root)}},
			{`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 199999)
			INSERT INTO entries (source_id, parent_id, name, path, kind, size, total_bytes, total_files,
				mtime_ns, newest_ns, oldest_ns, ext, file_kind, state, first_seen, last_seen, scan_gen)
			SELECT 'bench', d.id, CAST(printf('f%06d.%s', i, iif(i % 10 = 0, 'jpg', 'dat')) AS BLOB),
				CAST(printf('d%04d/f%06d.%s', i / 1000, i, iif(i % 10 = 0, 'jpg', 'dat')) AS BLOB), 'file',
				i, i, 1, i, i, i, iif(i % 10 = 0, 'jpg', 'dat'), iif(i % 10 = 0, 'image', 'other'),
				'present', 0, 0, 1
			FROM n JOIN entries d ON d.source_id = 'bench' AND d.path = CAST(printf('d%04d', i / 1000) AS BLOB)`, nil},
			{`INSERT INTO entry_names (rowid, name) SELECT id, CAST(name AS TEXT) FROM entries WHERE path <> X''`, nil},
			{`INSERT INTO tags (name, created_at) VALUES ('t', 0)`, nil},
			{`INSERT INTO entry_tags (entry_id, tag_id, added_at)
				SELECT id, 1, 0 FROM entries WHERE source_id = 'bench' AND path = CAST('d0042' AS BLOB)`, nil},
			{`UPDATE entries SET eff_decision = 'keep' WHERE source_id = 'bench' AND path >= CAST('d0042' AS BLOB)
				AND path < CAST('d00420' AS BLOB)`, nil},
		} {
			if _, err := tx.Exec(stmt.sql, stmt.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	var d42 domain.EntryID
	if err := st.Reader().QueryRow(`SELECT id FROM entries WHERE path = CAST('d0042' AS BLOB)`).Scan(&d42); err != nil {
		b.Fatal(err)
	}
	keep := []domain.Decision{domain.DecisionKeep}
	for _, s := range []struct {
		name  string
		query Query
	}{
		{"name rare", Query{Name: "f012345"}},
		{"name common", Query{Name: "jpg"}},
		{"name, source, ext", Query{Name: "f0123", Source: "bench", Ext: []string{"jpg"}}},
		{"within", Query{Within: &d42}},
		{"within by name", Query{Within: &d42, Sort: SortName}},
		{"tag", Query{Tags: []int64{1}}},
		{"source, decision", Query{Source: "bench", Decisions: keep}},
		{"decision", Query{Decisions: keep}},
		{"ext (unindexed)", Query{Ext: []string{"jpg"}}},
		{"short name (unindexed)", Query{Name: "f0"}},
	} {
		b.Run(s.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := Page(context.Background(), st.Reader(), s.query, "", DefaultLimit); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
