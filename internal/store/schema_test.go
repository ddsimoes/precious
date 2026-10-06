package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"reflect"
	"regexp"
	"testing"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"precious/migrations"
)

// Task 1.4: the R1 baseline schema enforces entry identity, its CHECK
// constraints, and source-removal cascades, and its name index is a
// contentless FTS5 trigram table.

const testMillis = 1_700_000_000_000

func mustWrite(t *testing.T, s *Store, query string, args ...any) sql.Result {
	t.Helper()
	var res sql.Result
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		res, err = tx.Exec(query, args...)
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return res
}

func lastID(t *testing.T, res sql.Result) int64 {
	t.Helper()
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const insertSourceSQL = `INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong,
	rel_root, capabilities, state, created_at)
	VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', '{"known":true}', 'online', ?)`

func insertSource(t *testing.T, s *Store, id string) {
	t.Helper()
	mustWrite(t, s, insertSourceSQL, id, id, "vol-"+id, testMillis)
}

const insertEntrySQL = `INSERT INTO entries (source_id, parent_id, name, path, kind, state,
	first_seen, last_seen, scan_gen)
	VALUES (?, ?, ?, ?, ?, 'present', 1, 1, 1)`

func insertEntry(t *testing.T, s *Store, source string, parent any, name, path []byte, kind string) int64 {
	t.Helper()
	return lastID(t, mustWrite(t, s, insertEntrySQL, source, parent, name, path, kind))
}

// wantConstraint fails unless err is the SQLite extended constraint code.
func wantConstraint(t *testing.T, err error, code int) {
	t.Helper()
	var se *sqlite.Error
	if !errors.As(err, &se) || se.Code() != code {
		t.Fatalf("err = %v, want SQLite constraint code %d", err, code)
	}
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An entry is unique on (source_id, path), comparing the path bytewise; a
// violation aborts the whole transaction, including its earlier statements.
func TestEntryPathUniquePerSource(t *testing.T) {
	s, _ := openTemp(t, Options{})
	insertSource(t, s, "a")
	insertSource(t, s, "b")
	root := insertEntry(t, s, "a", nil, []byte{}, []byte{}, "directory")
	insertEntry(t, s, "a", root, []byte("Readme"), []byte("Readme"), "file")

	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(insertEntrySQL, "a", root, []byte("other"), []byte("other"), "file"); err != nil {
			return err
		}
		_, err := tx.Exec(insertEntrySQL, "a", root, []byte("Readme"), []byte("Readme"), "symlink")
		return err
	})
	wantConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_UNIQUE)
	if n := countRows(t, s, `SELECT count(*) FROM entries WHERE source_id = 'a'`); n != 2 {
		t.Fatalf("source a has %d entries after the failed transaction, want 2", n)
	}

	// The same path in another source, and case- or normalization-distinct
	// paths in the same source, are different entries.
	insertEntry(t, s, "b", nil, []byte("Readme"), []byte("Readme"), "file")
	insertEntry(t, s, "a", root, []byte("README"), []byte("README"), "file")
	insertEntry(t, s, "a", root, []byte("caf\u00e9"), []byte("caf\u00e9"), "file")
	insertEntry(t, s, "a", root, []byte("cafe\u0301"), []byte("cafe\u0301"), "file")
}

var checkClause = regexp.MustCompile(`\bCHECK\s*\(`)

// Every CHECK in the baseline accepts a good value and rejects a bad one.
// Each case runs in a transaction that is rolled back, so cases are
// independent; the case count must equal the number of CHECK clauses.
func TestBaselineChecksRejectBadValues(t *testing.T) {
	const sourceSQL = `INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong,
		rel_root, capabilities, state, created_at) VALUES ('s', 's', ?, 'v', 'ext4', ?, X'', '{}', ?, 1)`
	const jobSQL = `INSERT INTO jobs (kind, payload_version, payload, state, cancel_requested,
		max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, ?, 3, 1, 1, 1)`
	const userSQL = `INSERT INTO users (id, username, password_hash, password_changed_at, created_at)
		VALUES (?, 'admin', 'h', 1, 1)`
	const sessionSQL = `INSERT INTO sessions (token_hash, kind, user_id, csrf_token, created_at,
		last_seen_at, idle_expires_at, absolute_expires_at) VALUES (?, ?, ?, 'c', 1, 1, 2, 2)`
	const entrySQL = `INSERT INTO entries (source_id, name, path, kind, state, first_seen, last_seen,
		scan_gen, decision, eff_decision) VALUES ('src', X'', X'', ?, ?, 1, 1, 1, ?, ?)`
	hash := make([]byte, 32)
	withUser := []string{`INSERT INTO users (id, username, password_hash, password_changed_at, created_at)
		VALUES (1, 'admin', 'h', 1, 1)`}

	cases := []struct {
		name      string
		setup     []string
		query     string
		good, bad []any
	}{
		{"sources.volume_kind", nil, sourceSQL, []any{"uuid", 1, "online"}, []any{"label", 1, "online"}},
		{"sources.strong", nil, sourceSQL, []any{"zfs", 0, "online"}, []any{"zfs", 2, "online"}},
		{"sources.state", nil, sourceSQL, []any{"path", 1, "unavailable"}, []any{"path", 1, "gone"}},
		{"jobs.state", nil, jobSQL, []any{"queued", 0}, []any{"done", 0}},
		{"jobs.cancel_requested", nil, jobSQL, []any{"queued", 1}, []any{"queued", 2}},
		{"users.id", nil, userSQL, []any{1}, []any{2}},
		{"sessions.token_hash", withUser, sessionSQL, []any{hash, "authenticated", 1}, []any{hash[:31], "authenticated", 1}},
		{"sessions.kind", withUser, sessionSQL, []any{hash, "pre_login", nil}, []any{hash, "anonymous", nil}},
		{"sessions.kind_user", withUser, sessionSQL, []any{hash, "authenticated", 1}, []any{hash, "pre_login", 1}},
		{"entries.kind", nil, entrySQL, []any{"special", "present", nil, "undecided"}, []any{"device", "present", nil, "undecided"}},
		{"entries.state", nil, entrySQL, []any{"file", "unreadable", nil, "undecided"}, []any{"file", "deleted", nil, "undecided"}},
		{"entries.decision", nil, entrySQL, []any{"file", "present", "later", "later"}, []any{"file", "present", "inherit", "undecided"}},
		{"entries.eff_decision", nil, entrySQL, []any{"file", "present", "keep", "keep"}, []any{"file", "present", nil, "maybe"}},
	}

	baseline, err := fs.ReadFile(migrations.FS, "0001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(checkClause.FindAll(baseline, -1)); n != len(cases) {
		t.Fatalf("baseline has %d CHECK clauses, the table covers %d", n, len(cases))
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

// Deleting a source removes its entries, their dir_stats and tag links, and
// its jobs and their events; tags themselves, audit events, and other
// sources' rows stay.
func TestSourceDeleteCascades(t *testing.T) {
	s, _ := openTemp(t, Options{})
	tagID := lastID(t, mustWrite(t, s, `INSERT INTO tags (name, created_at) VALUES ('familia', 1)`))
	mustWrite(t, s, `INSERT INTO audit_events (occurred_at, kind, actor) VALUES (1, 'add-source', 'admin')`)
	for _, src := range []string{"gone", "kept"} {
		insertSource(t, s, src)
		root := insertEntry(t, s, src, nil, []byte{}, []byte{}, "directory")
		dir := insertEntry(t, s, src, root, []byte("Fotos"), []byte("Fotos"), "directory")
		file := insertEntry(t, s, src, dir, []byte("a.jpg"), []byte("Fotos/a.jpg"), "file")
		mustWrite(t, s, `UPDATE entries SET eff_from = ? WHERE id = ?`, dir, file)
		for _, id := range []int64{root, dir} {
			mustWrite(t, s, `INSERT INTO dir_stats (entry_id, dirs, files, symlinks, specials, unreadable,
				mount_boundaries, by_kind, by_year, by_family, signals, indicators, inside)
				VALUES (?, 0, 1, 0, 0, 0, 0, '{}', '{}', '{}', '{}', '[]', '[]')`, id)
		}
		mustWrite(t, s, `INSERT INTO entry_tags (entry_id, tag_id, added_at) VALUES (?, ?, 1)`, dir, tagID)
		job := lastID(t, mustWrite(t, s, `INSERT INTO jobs (kind, payload_version, payload, source_id, state,
			max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'succeeded', 3, 1, 1, 1)`, src))
		mustWrite(t, s, `INSERT INTO job_events (job_id, type, payload, created_at) VALUES (?, 'job', '{}', 1)`, job)
	}

	mustWrite(t, s, `DELETE FROM sources WHERE id = 'gone'`)

	counts := map[string]string{
		"entries":    `SELECT count(*) FROM entries WHERE source_id = ?`,
		"dir_stats":  `SELECT count(*) FROM dir_stats JOIN entries ON entries.id = entry_id WHERE source_id = ?`,
		"entry_tags": `SELECT count(*) FROM entry_tags JOIN entries ON entries.id = entry_id WHERE source_id = ?`,
		"jobs":       `SELECT count(*) FROM jobs WHERE source_id = ?`,
		"job_events": `SELECT count(*) FROM job_events JOIN jobs ON jobs.id = job_id WHERE source_id = ?`,
	}
	want := map[string]int{"entries": 3, "dir_stats": 2, "entry_tags": 1, "jobs": 1, "job_events": 1}
	for table, q := range counts {
		if n := countRows(t, s, q, "kept"); n != want[table] {
			t.Errorf("kept source: %d %s, want %d", n, table, want[table])
		}
	}
	got := map[string]int{
		"entries":    countRows(t, s, `SELECT count(*) FROM entries`),
		"dir_stats":  countRows(t, s, `SELECT count(*) FROM dir_stats`),
		"entry_tags": countRows(t, s, `SELECT count(*) FROM entry_tags`),
		"jobs":       countRows(t, s, `SELECT count(*) FROM jobs`),
		"job_events": countRows(t, s, `SELECT count(*) FROM job_events`),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows left after deleting a source = %v, want only the kept source's %v", got, want)
	}
	if n := countRows(t, s, `SELECT count(*) FROM tags`); n != 1 {
		t.Errorf("tags = %d, want 1", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM audit_events`); n != 1 {
		t.Errorf("audit_events = %d, want 1", n)
	}
}

// Every foreign key into entries is searched through an index, so deleting
// entries (removing a source, a rescan dropping missing entries) checks the
// referencing rows by index rather than by scanning a table of millions of
// rows once per deleted entry.
func TestForeignKeysIntoEntriesAreIndexed(t *testing.T) {
	s, _ := openTemp(t, Options{})
	assertForeignKeysIndexed(t, s, "entries", 4)
}

// Pruning an unreferenced contents row checks every table that references
// contents, so those references are indexed too (R2 Interfaces).
func TestForeignKeysIntoContentsAreIndexed(t *testing.T) {
	s, _ := openTemp(t, Options{})
	assertForeignKeysIndexed(t, s, "contents", 3)
}

// assertForeignKeysIndexed checks that every foreign key into parent (at
// least min of them) is searched through an index.
func assertForeignKeysIndexed(t *testing.T, s *Store, parent string, min int) {
	t.Helper()
	rows, err := s.Reader().Query(`SELECT m.name, f."from" FROM sqlite_schema m, pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND f."table" = ? ORDER BY m.name, f."from"`, parent)
	if err != nil {
		t.Fatal(err)
	}
	type fk struct{ table, column string }
	var fks []fk
	for rows.Next() {
		var k fk
		if err := rows.Scan(&k.table, &k.column); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, k)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if len(fks) < min {
		t.Fatalf("foreign keys into %s: %v", parent, fks)
	}
	indexed := regexp.MustCompile(`SEARCH .* USING (COVERING INDEX|INDEX|INTEGER PRIMARY KEY|PRIMARY KEY)`)
	for _, k := range fks {
		var id, parentID, notUsed int
		var detail string
		q := `EXPLAIN QUERY PLAN SELECT 1 FROM "` + k.table + `" WHERE "` + k.column + `" = ?`
		if err := s.Reader().QueryRow(q, 1).Scan(&id, &parentID, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		if !indexed.MatchString(detail) {
			t.Errorf("%s.%s references %s without an index: %s", k.table, k.column, parent, detail)
		}
	}
}

// entry_names is a contentless FTS5 trigram index keyed by entry ID: a
// case-insensitive substring query finds an inserted name, and a delete by
// rowid removes it.
func TestEntryNamesInsertAndContentlessDelete(t *testing.T) {
	s, _ := openTemp(t, Options{})
	names := map[int64]string{1: "Meu orcamento casamento.xls", 2: "Orçamento 2006.doc", 3: "fotos.zip"}
	for id, name := range names {
		mustWrite(t, s, `INSERT INTO entry_names (rowid, name) VALUES (?, ?)`, id, name)
	}
	match := func(q string) []int64 {
		t.Helper()
		rows, err := s.Reader().Query(`SELECT rowid FROM entry_names WHERE entry_names MATCH ? ORDER BY rowid`, q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}

	if got := match(`"AMENTO"`); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf(`match "AMENTO" = %v, want [1 2]`, got)
	}
	mustWrite(t, s, `DELETE FROM entry_names WHERE rowid = ?`, 1)
	if got := match(`"amento"`); !reflect.DeepEqual(got, []int64{2}) {
		t.Fatalf(`match "amento" after delete = %v, want [2]`, got)
	}
	if got := match(`"fotos"`); !reflect.DeepEqual(got, []int64{3}) {
		t.Fatalf(`match "fotos" = %v, want [3]`, got)
	}
}
