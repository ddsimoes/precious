package store

import (
	"context"
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

// r5c task 1.1: migration 0009 adds entry_places, the stored figures of each
// folder and complete archive, and marks the relations dirty so the first
// start runs one relate pass. Its CHECKs reject negative figures, and a row
// goes with its entry.

// placesColumns are entry_places' columns in order (r5c Interfaces).
var placesColumns = []string{"entry_id",
	"only_copy_files", "only_copy_bytes", "this_source_files", "this_source_bytes",
	"other_source_files", "other_source_bytes", "unchecked_files", "unchecked_bytes",
	"unreadable_files", "unreadable_bytes", "disposable_files", "disposable_bytes"}

// insertPlacesSQL inserts an entry_places row of placesColumns.
var insertPlacesSQL = `INSERT INTO entry_places (` + strings.Join(placesColumns, ", ") + `) VALUES (?` +
	strings.Repeat(", ?", len(placesColumns)-1) + `)`

// placesRow is an entry_places row for entry with every figure set to n.
func placesRow(entry, n int64) []any {
	row := []any{entry}
	for range placesColumns[1:] {
		row = append(row, n)
	}
	return row
}

// TestV8DatabaseMigratesToPlaces upgrades a v8 database whose relations are
// up to date: every row stays, entry_places starts empty, and review_state
// is dirty again with its generation kept, so the visible figures stay until
// the relate pass that startup enqueues replaces them.
func TestV8DatabaseMigratesToPlaces(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 8)})
	if err != nil {
		t.Fatal(err)
	}
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	album := insertEntry(t, s, "fotos", root, []byte("Album"), []byte("Album"), "directory")
	insertEntry(t, s, "fotos", album, []byte("a.jpg"), []byte("Album/a.jpg"), "file")
	mustWrite(t, s, `UPDATE review_state SET gen = 3, dirty = 0, computed_at = 7`)
	before := tableRows(t, s.Writer())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 9)})
	if err != nil {
		t.Fatalf("open the v8 database with 0009: %v", err)
	}
	defer s.Close()
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != 9 {
		t.Fatalf("schema version after upgrade = %d, %v; want 9", v, err)
	}
	after := tableRows(t, s.Writer())
	for table, rows := range before {
		if table == "review_state" {
			continue
		}
		if !reflect.DeepEqual(after[table], rows) {
			t.Errorf("%s rows changed by the upgrade:\n before %q\n after  %q", table, rows, after[table])
		}
	}
	if got := after["review_state"]; len(got) != 1 || got[0] != `int64:1|int64:3|int64:1|int64:7|` {
		t.Errorf("review_state after the upgrade = %q, want gen 3 dirty 1 computed_at 7", got)
	}
	if rows, ok := after["entry_places"]; ok {
		t.Errorf("entry_places has rows after the upgrade: %q", rows)
	}
	if got := tableColumns(t, s, "entry_places"); !slices.Equal(got, placesColumns) {
		t.Errorf("entry_places columns = %q, want %q", got, placesColumns)
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
	// Deleting an entry checks entry_places through its key.
	assertForeignKeysIndexed(t, s, "entries", 1, "entry_places")
}

// Every figure's CHECK rejects a negative value and accepts zero, and a row
// names an existing entry.
func TestPlacesChecksRejectNegativeFigures(t *testing.T) {
	s, _ := openTemp(t, Options{})
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	write := func(args ...any) error {
		return s.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(insertPlacesSQL, args...)
			return err
		})
	}
	for i, col := range placesColumns[1:] {
		row := placesRow(root, 0)
		row[i+1] = int64(-1)
		if err := write(row...); err == nil {
			t.Errorf("entry_places accepted %s = -1", col)
		} else {
			wantConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK)
		}
	}
	if err := write(placesRow(root, 0)...); err != nil {
		t.Fatalf("a row of zero figures: %v", err)
	}
	wantConstraint(t, write(placesRow(99999, 0)...), sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
}

// Removing an entry removes its entry_places row and leaves another's;
// removing a source removes its entries' rows.
func TestPlacesCascade(t *testing.T) {
	s, _ := openTemp(t, Options{})
	folders := map[string][2]int64{}
	for _, src := range []string{"gone", "kept"} {
		insertSource(t, s, src)
		root := insertEntry(t, s, src, nil, []byte{}, []byte{}, "directory")
		album := insertEntry(t, s, src, root, []byte("Album"), []byte("Album"), "directory")
		folders[src] = [2]int64{root, album}
		for _, e := range folders[src] {
			mustWrite(t, s, insertPlacesSQL, placesRow(e, 1)...)
		}
	}

	kept := folders["kept"]
	mustWrite(t, s, `DELETE FROM entries WHERE id = ?`, kept[1])
	if n := countRows(t, s, `SELECT count(*) FROM entry_places WHERE entry_id = ?`, kept[1]); n != 0 {
		t.Errorf("entry_places kept a removed entry's row")
	}
	if n := countRows(t, s, `SELECT count(*) FROM entry_places WHERE entry_id = ?`, kept[0]); n != 1 {
		t.Errorf("removing an entry removed another's entry_places row")
	}

	mustWrite(t, s, `DELETE FROM sources WHERE id = 'gone'`)
	gone := folders["gone"]
	if n := countRows(t, s, `SELECT count(*) FROM entry_places WHERE entry_id IN (?, ?)`, gone[0], gone[1]); n != 0 {
		t.Errorf("removing a source left %d entry_places rows of its entries", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM entry_places`); n != 1 {
		t.Errorf("entry_places = %d rows after removing a source, want the kept source's 1", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
}
