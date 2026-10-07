package store

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"

	"precious/internal/domain"
)

// r2b task 1.2: migration 0003 upgrades an R2 database in place: it keeps
// every row, adds the override table and the schedule columns empty, and
// rebuilds the name index so that it folds accents and holds every name
// exactly as the scanner writes it.
func TestR2DatabaseMigratesToOwnerKeepingRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 2)})
	if err != nil {
		t.Fatal(err)
	}
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	party := insertEntry(t, s, "fotos", root, []byte("Confraternização 2018"), []byte("Confraternização 2018"), "directory")
	latin1 := []byte("f\xe9.txt") // not valid UTF-8: the index holds its escaped display name
	file := insertEntry(t, s, "fotos", party, latin1, append([]byte("Confraternização 2018/"), latin1...), "file")
	mustWrite(t, s, `UPDATE entries SET decision = 'keep', decision_at = 5, eff_decision = 'keep' WHERE id = ?`, party)
	for _, id := range []int64{party, file} {
		var name []byte
		if err := s.Reader().QueryRow(`SELECT name FROM entries WHERE id = ?`, id).Scan(&name); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, s, `INSERT INTO entry_names (rowid, name) VALUES (?, ?)`, id, domain.DisplayName(name))
	}
	before := ownerComparable(tableRows(t, s.Writer()))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open the R2 database with 0003: %v", err)
	}
	defer s.Close()
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != latestVersion(t) || v < 3 {
		t.Fatalf("schema version after upgrade = %d, %v; want %d", v, err, latestVersion(t))
	}
	after := ownerComparable(tableRows(t, s.Writer()))
	for table, rows := range before {
		if !reflect.DeepEqual(after[table], rows) {
			t.Errorf("%s rows changed by the upgrade:\n before %q\n after  %q", table, rows, after[table])
		}
	}
	var schedule, next, skippedAt, reason any
	var requested int
	if err := s.Reader().QueryRow(`SELECT scan_schedule, next_scan_at, schedule_skipped_at, schedule_skip_reason,
		rescan_requested FROM sources WHERE id = 'fotos'`).Scan(&schedule, &next, &skippedAt, &reason, &requested); err != nil {
		t.Fatal(err)
	}
	if schedule != nil || next != nil || skippedAt != nil || reason != nil || requested != 0 {
		t.Errorf("new source columns = %v %v %v %v %d; want NULL NULL NULL NULL 0", schedule, next, skippedAt, reason, requested)
	}
	if n := countRows(t, s, `SELECT count(*) FROM entry_overrides`); n != 0 {
		t.Errorf("entry_overrides has %d rows after the upgrade", n)
	}
	for q, want := range map[string]int64{`"confraternizacao"`: party, `"\xE9.t"`: file} {
		var got []int64
		rows, err := s.Reader().Query(`SELECT rowid FROM entry_names WHERE entry_names MATCH ?`, q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got = append(got, id)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []int64{want}) {
			t.Errorf("match %s = %v, want [%d]", q, got, want)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM entry_names WHERE entry_names MATCH '"fotos"'`); n != 0 {
		t.Errorf("the source's root was indexed: %d rows", n)
	}
}

// ownerComparable drops what 0003 rebuilds or widens on purpose: the name
// index's own tables, and sources, whose new columns the test checks itself.
func ownerComparable(rows map[string][]string) map[string][]string {
	out := map[string][]string{}
	for table, r := range rows {
		if strings.HasPrefix(table, "entry_names") || table == "sources" {
			continue
		}
		out[table] = r
	}
	return out
}

// entry_overrides refuses values outside its domain and goes with its entry.
func TestOwnerChecksRejectBadValues(t *testing.T) {
	s, _ := openTemp(t, Options{})
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	dir := insertEntry(t, s, "fotos", root, []byte("Praia"), []byte("Praia"), "directory")
	write := func(q string, args ...any) error {
		return s.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(q, args...)
			return err
		})
	}
	for _, bad := range []struct {
		name string
		q    string
		args []any
	}{
		{"unknown category", `INSERT INTO entry_overrides (entry_id, category, updated_at) VALUES (?, 'fotos', 1)`, []any{dir}},
		{"group mark 2", `INSERT INTO entry_overrides (entry_id, group_mark, updated_at) VALUES (?, 2, 1)`, []any{dir}},
		{"nothing set", `INSERT INTO entry_overrides (entry_id, updated_at) VALUES (?, 1)`, []any{dir}},
		{"rescan_requested 2", `UPDATE sources SET rescan_requested = 2 WHERE id = 'fotos'`, nil},
	} {
		t.Run(bad.name, func(t *testing.T) {
			wantConstraint(t, write(bad.q, bad.args...), sqlite3.SQLITE_CONSTRAINT_CHECK)
		})
	}
	mustWrite(t, s, `INSERT INTO entry_overrides (entry_id, category, group_mark, updated_at) VALUES (?, 'personal_media', 1, 1)`, dir)
	mustWrite(t, s, `DELETE FROM entries WHERE id = ?`, dir)
	if n := countRows(t, s, `SELECT count(*) FROM entry_overrides`); n != 0 {
		t.Errorf("an override outlived its entry: %d rows", n)
	}
}
