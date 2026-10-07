package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	sqlite3 "modernc.org/sqlite/lib"

	"precious/migrations"
)

// Task 1.2: migration 0002 upgrades an R1 database in place, its CHECK
// constraints reject bad values, and removing a source cascades through the
// new tables.

// baselineOnly is the embedded migrations cut to the R1 baseline.
func baselineOnly(t *testing.T) fs.FS { return migrationsUpTo(t, 1) }

// migrationsUpTo is the embedded migrations cut to version last, so an
// upgrade test starts from the schema a release shipped.
func migrationsUpTo(t *testing.T, last int) fs.FS {
	t.Helper()
	ms, err := LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, m := range ms {
		if m.Version <= last {
			out[fmt.Sprintf("%04d_%s.sql", m.Version, m.Name)] = &fstest.MapFile{Data: []byte(m.SQL)}
		}
	}
	if len(out) != last {
		t.Fatalf("migrations up to %d: found %d", last, len(out))
	}
	return out
}

// tableRows returns every row of every non-internal table as a string per
// row, so two snapshots compare row by row.
func tableRows(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	ctx := context.Background()
	names, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations' AND sql NOT LIKE 'CREATE VIRTUAL%'
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := errors.Join(names.Err(), names.Close()); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.QueryContext(ctx, `SELECT * FROM "`+table+`" ORDER BY 1, 2`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], fmtRow(vals))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// fmtRow renders a row with each value's dynamic type, so a changed type
// shows as a change.
func fmtRow(vals []any) string {
	var b strings.Builder
	for _, v := range vals {
		fmt.Fprintf(&b, "%T:%v|", v, v)
	}
	return b.String()
}

// An R1 database (baseline only, with entries, decisions, tags, selections,
// jobs, and audit events) migrates to 0002 at open and keeps every row.
func TestR1DatabaseMigratesToContentKeepingRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Migrations: baselineOnly(t)})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != 1 {
		t.Fatalf("R1 schema version = %d, %v; want 1", v, err)
	}
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	dir2006 := insertEntry(t, s, "fotos", root, []byte("2006"), []byte("2006"), "directory")
	file := insertEntry(t, s, "fotos", dir2006, []byte("praia.jpg"), []byte("2006/praia.jpg"), "file")
	mustWrite(t, s, `UPDATE entries SET decision = 'keep', decision_at = 5, eff_decision = 'keep' WHERE id = ?`, dir2006)
	mustWrite(t, s, `UPDATE entries SET eff_decision = 'keep', eff_from = ?, size = 1234, total_bytes = 1234,
		total_files = 1, mtime_ns = 7, ctime_ns = 8, dev = 1, ino = 99 WHERE id = ?`, dir2006, file)
	mustWrite(t, s, `INSERT INTO dir_stats (entry_id, dirs, files, symlinks, specials, unreadable,
		mount_boundaries, by_kind, by_year, by_family, signals, indicators, inside)
		VALUES (?, 0, 1, 0, 0, 0, 0, '{}', '{}', '{}', '{}', '[]', '[]')`, dir2006)
	mustWrite(t, s, `INSERT INTO entry_names (rowid, name) VALUES (?, 'praia.jpg')`, file)
	tag := lastID(t, mustWrite(t, s, `INSERT INTO tags (name, created_at) VALUES ('familia', 1)`))
	mustWrite(t, s, `INSERT INTO entry_tags (entry_id, tag_id, added_at) VALUES (?, ?, 1)`, file, tag)
	mustWrite(t, s, `INSERT INTO selections (id, query, count, bytes, kept, created_at, expires_at)
		VALUES ('sel', '{}', 1, 1234, 1, 1, 2)`)
	mustWrite(t, s, `INSERT INTO selection_entries (selection_id, entry_id) VALUES ('sel', ?)`, file)
	mustWrite(t, s, `INSERT INTO audit_events (occurred_at, kind, actor, detail) VALUES (1, 'set-decision', 'admin', '{}')`)
	job := lastID(t, mustWrite(t, s, `INSERT INTO jobs (kind, payload_version, payload, source_id, state,
		max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', 'fotos', 'succeeded', 3, 1, 1, 1)`))
	mustWrite(t, s, `INSERT INTO job_events (job_id, type, payload, created_at) VALUES (?, 'job', '{}', 1)`, job)
	before := tableRows(t, s.Writer())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 2)})
	if err != nil {
		t.Fatalf("open the R1 database with 0002: %v", err)
	}
	defer s.Close()
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != 2 {
		t.Fatalf("schema version after upgrade = %d, %v; want 2", v, err)
	}
	after := tableRows(t, s.Writer())
	for table, rows := range before {
		if !reflect.DeepEqual(after[table], rows) {
			t.Errorf("%s rows changed by the upgrade:\n before %q\n after  %q", table, rows, after[table])
		}
	}
	for _, table := range []string{"entries", "dir_stats", "tags", "entry_tags", "selections", "audit_events", "jobs"} {
		if len(before[table]) == 0 {
			t.Errorf("seed left %s empty", table)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM entry_names WHERE entry_names MATCH '"praia"'`); n != 1 {
		t.Errorf("entry_names match after upgrade = %d, want 1", n)
	}
	// The derived state starts dirty at generation 0, so the first relate
	// pass runs at startup.
	if got := after["review_state"]; !reflect.DeepEqual(got, []string{"int64:1|int64:0|int64:1|<nil>:<nil>|"}) {
		t.Errorf("review_state = %q", got)
	}
	for _, table := range []string{"contents", "file_content", "content_coverage", "archives", "archive_members",
		"relations", "dir_dups", "review_rows", "review_row_sources"} {
		if _, ok := before[table]; ok {
			t.Errorf("%s existed before 0002", table)
		}
		if n := countRows(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s has %d rows after the upgrade, want 0", table, n)
		}
	}
}

// Every CHECK of 0002 accepts a good value and rejects a bad one. As in the
// baseline test, the case count must equal the number of CHECK clauses.
func TestContentChecksRejectBadValues(t *testing.T) {
	const (
		entry1 = `INSERT INTO entries (id, source_id, name, path, kind, state, first_seen, last_seen, scan_gen)
			VALUES (1, 'src', X'', X'', 'directory', 'present', 1, 1, 1)`
		entry2 = `INSERT INTO entries (id, source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
			VALUES (2, 'src', 1, X'62', X'62', 'file', 'present', 1, 1, 1)`
		content1 = `INSERT INTO contents (id, sha256, size) VALUES (1, zeroblob(32), 10)`
		archive1 = `INSERT INTO archives (entry_id, format, state, size, mtime_ns) VALUES (1, 'zip', 'complete', 10, 1)`

		contentSQL = `INSERT INTO contents (sha256, size) VALUES (?, ?)`
		fileSQL    = `INSERT INTO file_content (entry_id, source_id, state, size, sample, content_id)
			VALUES (1, 'src', ?, 10, ?, ?)`
		archiveSQL  = `INSERT INTO archives (entry_id, format, state, size, mtime_ns) VALUES (1, ?, ?, 10, 1)`
		memberSQL   = `INSERT INTO archive_members (archive_id, name, path, kind, state) VALUES (1, X'61', X'61', ?, ?)`
		relationSQL = `INSERT INTO relations (gen, kind, a_entry, b_entry, matched_bytes, redundant_bytes,
			a_bytes, a_files, b_bytes, b_files, a_only_files, a_only_bytes, b_only_files, b_only_bytes)
			VALUES (1, ?, 1, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)`
		rowSQL = `INSERT INTO review_rows (gen, list, entry_id, content_id, bytes, files, sort_key)
			VALUES (1, ?, ?, ?, 0, 0, 0)`
		stateSQL = `UPDATE review_state SET id = ?`
	)
	hash := make([]byte, 32)
	cases := []struct {
		name      string
		setup     []string
		query     string
		good, bad []any
	}{
		{"contents.sha256", nil, contentSQL, []any{hash, 1}, []any{hash[:31], 1}},
		{"contents.size", nil, contentSQL, []any{hash, 1}, []any{hash, 0}},
		{"file_content.state", []string{entry1}, fileSQL, []any{"pending", nil, nil}, []any{"queued", nil, nil}},
		{"file_content.sample", []string{entry1}, fileSQL, []any{"pending", hash, nil}, []any{"pending", hash[:31], nil}},
		{"file_content.hashed_content", []string{entry1, content1}, fileSQL, []any{"hashed", nil, 1}, []any{"pending", nil, 1}},
		{"file_content.sampled_sample", []string{entry1}, fileSQL, []any{"sampled", hash, nil}, []any{"sampled", nil, nil}},
		{"archives.format", []string{entry1}, archiveSQL, []any{"tar_bzip2", "complete"}, []any{"7z", "complete"}},
		{"archives.state", []string{entry1}, archiveSQL, []any{"zip", "encrypted"}, []any{"zip", "done"}},
		{"archive_members.kind", []string{entry1, archive1}, memberSQL, []any{"special", nil}, []any{"fifo", nil}},
		{"archive_members.state", []string{entry1, archive1}, memberSQL, []any{"file", "unique_size"}, []any{"file", "sampled"}},
		{"archive_members.kind_state", []string{entry1, archive1}, memberSQL, []any{"directory", nil}, []any{"directory", "pending"}},
		{"relations.kind", []string{entry1, entry2}, relationSQL, []any{"overlap"}, []any{"contains"}},
		{"review_rows.list", []string{entry1}, rowSQL, []any{"gems_only_in_copy", 1, nil}, []any{"keeper", 1, nil}},
		{"review_rows.one_target", []string{entry1, content1}, rowSQL, []any{"duplicates", nil, 1}, []any{"duplicates", 1, 1}},
		{"review_state.id", nil, stateSQL, []any{1}, []any{2}},
	}

	schema, err := fs.ReadFile(migrations.FS, "0002_content.sql")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(checkClause.FindAll(schema, -1)); n != len(cases) {
		t.Fatalf("0002 has %d CHECK clauses, the table covers %d", n, len(cases))
	}

	s, _ := openTemp(t, Options{})
	insertSource(t, s, "src")
	errRollback := errors.New("rollback")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var badErr error
			err := s.Write(context.Background(), func(tx *sql.Tx) error {
				for _, q := range tc.setup {
					if _, err := tx.Exec(q); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(`SAVEPOINT good`); err != nil {
					return err
				}
				if _, err := tx.Exec(tc.query, tc.good...); err != nil {
					return err
				}
				if _, err := tx.Exec(`ROLLBACK TO good`); err != nil {
					return err
				}
				_, badErr = tx.Exec(tc.query, tc.bad...)
				return errRollback
			})
			if !errors.Is(err, errRollback) {
				t.Fatalf("good value rejected: %v", err)
			}
			wantConstraint(t, badErr, sqlite3.SQLITE_CONSTRAINT_CHECK)
		})
	}
}

// Removing a source deletes its file_content, coverage, archives, members,
// relations, dir_dups, and review rows, and keeps the other source's; a
// duplicates row shared by both sources stays, mapped to the kept source.
func TestSourceDeleteCascadesContent(t *testing.T) {
	s, _ := openTemp(t, Options{})
	content := lastID(t, mustWrite(t, s, `INSERT INTO contents (sha256, size) VALUES (zeroblob(32), 5)`))
	shared := lastID(t, mustWrite(t, s, `INSERT INTO review_rows (gen, list, content_id, bytes, files, sort_key)
		VALUES (1, 'duplicates', ?, 5, 2, 5)`, content))
	for _, src := range []string{"gone", "kept"} {
		insertSource(t, s, src)
		root := insertEntry(t, s, src, nil, []byte{}, []byte{}, "directory")
		dir := insertEntry(t, s, src, root, []byte("site"), []byte("site"), "directory")
		file := insertEntry(t, s, src, dir, []byte("a.txt"), []byte("site/a.txt"), "file")
		zip := insertEntry(t, s, src, root, []byte("site.zip"), []byte("site.zip"), "file")
		mustWrite(t, s, `INSERT INTO file_content (entry_id, source_id, state, size, content_id)
			VALUES (?, ?, 'hashed', 5, ?)`, file, src, content)
		mustWrite(t, s, `INSERT INTO file_content (entry_id, source_id, state, size) VALUES (?, ?, 'unique_size', 9)`, zip, src)
		mustWrite(t, s, `INSERT INTO content_coverage VALUES (?, 1, 5, 1, 5, 0, 0, 0, 0, 1)`, src)
		mustWrite(t, s, `INSERT INTO archives (entry_id, format, state, size, mtime_ns, members, unpacked_bytes)
			VALUES (?, 'zip', 'complete', 9, 1, 2, 5)`, zip)
		folder := lastID(t, mustWrite(t, s, `INSERT INTO archive_members (archive_id, name, path, kind)
			VALUES (?, X'73697465', X'73697465', 'directory')`, zip))
		member := lastID(t, mustWrite(t, s, `INSERT INTO archive_members (archive_id, parent_id, name, path, kind, size, state, content_id)
			VALUES (?, ?, X'612e747874', X'736974652f612e747874', 'file', 5, 'hashed', ?)`, zip, folder, content))
		mustWrite(t, s, `INSERT INTO archive_members (archive_id, parent_id, name, path, kind, link_member)
			VALUES (?, ?, X'62', X'736974652f62', 'special', ?)`, zip, folder, member)
		rel := lastID(t, mustWrite(t, s, `INSERT INTO relations (gen, kind, a_entry, a_member, b_entry,
			matched_bytes, redundant_bytes, a_bytes, a_files, b_bytes, b_files, a_only_files, a_only_bytes, b_only_files, b_only_bytes)
			VALUES (1, 'same', ?, ?, ?, 5, 5, 5, 1, 5, 1, 0, 0, 0, 0)`, zip, folder, dir))
		for _, id := range []int64{root, dir} {
			mustWrite(t, s, `INSERT INTO dir_dups VALUES (?, 5, 5, 5, 1)`, id)
		}
		mustWrite(t, s, `INSERT INTO review_rows (gen, list, source_id, entry_id, bytes, files, sort_key)
			VALUES (1, 'leftovers', ?, ?, 0, 0, 0)`, src, file)
		mustWrite(t, s, `INSERT INTO review_rows (gen, list, source_id, entry_id, group_id, bytes, files, sort_key)
			VALUES (1, 'gems_rescue', ?, ?, ?, 0, 0, 0)`, src, file, dir)
		relRow := lastID(t, mustWrite(t, s, `INSERT INTO review_rows (gen, list, relation_id, bytes, files, sort_key)
			VALUES (1, 'duplicates', ?, 5, 1, 5)`, rel))
		for _, row := range []int64{relRow, shared} {
			mustWrite(t, s, `INSERT INTO review_row_sources (row_id, source_id) VALUES (?, ?)`, row, src)
		}
	}

	mustWrite(t, s, `DELETE FROM sources WHERE id = 'gone'`)

	want := map[string]int{
		"file_content": 2, "content_coverage": 1, "archives": 1, "archive_members": 3, "relations": 1,
		"dir_dups": 2, "review_rows": 4, "review_row_sources": 2, "contents": 1,
	}
	got := map[string]int{}
	for table := range want {
		got[table] = countRows(t, s, `SELECT count(*) FROM `+table)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows left after deleting a source = %v, want only the kept source's %v", got, want)
	}
	kept := map[string]string{
		"file_content":       `SELECT count(*) FROM file_content JOIN entries ON entries.id = entry_id WHERE entries.source_id = 'kept'`,
		"content_coverage":   `SELECT count(*) FROM content_coverage WHERE source_id = 'kept'`,
		"archive_members":    `SELECT count(*) FROM archive_members JOIN entries ON entries.id = archive_id WHERE source_id = 'kept'`,
		"relations":          `SELECT count(*) FROM relations JOIN entries ON entries.id = a_entry WHERE source_id = 'kept'`,
		"dir_dups":           `SELECT count(*) FROM dir_dups JOIN entries ON entries.id = entry_id WHERE source_id = 'kept'`,
		"review_row_sources": `SELECT count(*) FROM review_row_sources WHERE source_id = 'kept'`,
	}
	for table, q := range kept {
		if n := countRows(t, s, q); n != want[table] {
			t.Errorf("kept source: %d %s, want %d", n, table, want[table])
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM review_rows WHERE id = ?`, shared); n != 1 {
		t.Errorf("the shared duplicates row was deleted")
	}
}
