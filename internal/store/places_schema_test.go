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
// folder and complete archive, archive_places, each complete archive's
// unpacked member sums, and member_places, each member folder's, and marks
// the relations dirty so the first start runs one relate pass. Their CHECKs
// reject negative figures, and a row goes with its entry or member.

// placesTable is one table of place figures and its key column.
type placesTable struct{ name, key string }

var (
	entryPlacesT   = placesTable{"entry_places", "entry_id"}
	archivePlacesT = placesTable{"archive_places", "entry_id"}
	memberPlacesT  = placesTable{"member_places", "member_id"}
	placesTables   = []placesTable{entryPlacesT, archivePlacesT, memberPlacesT}
)

// placesFigures are the figure columns of every places table, in order
// (r5c Interfaces).
var placesFigures = []string{
	"only_copy_files", "only_copy_bytes", "this_source_files", "this_source_bytes",
	"other_source_files", "other_source_bytes", "unchecked_files", "unchecked_bytes",
	"unreadable_files", "unreadable_bytes", "disposable_files", "disposable_bytes"}

// columns are the table's columns in order.
func (p placesTable) columns() []string {
	return append([]string{p.key}, placesFigures...)
}

// insert inserts a row of columns.
func (p placesTable) insert() string {
	return `INSERT INTO ` + p.name + ` (` + strings.Join(p.columns(), ", ") + `) VALUES (?` +
		strings.Repeat(", ?", len(placesFigures)) + `)`
}

// count counts the rows of the given keys.
func (p placesTable) count(t *testing.T, s *Store, keys ...int64) int {
	t.Helper()
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	return countRows(t, s, `SELECT count(*) FROM `+p.name+` WHERE `+p.key+` IN (?`+strings.Repeat(", ?", len(keys)-1)+`)`, args...)
}

// placesRow is a places row for key with every figure set to n.
func placesRow(key, n int64) []any {
	row := []any{key}
	for range placesFigures {
		row = append(row, n)
	}
	return row
}

// insertArchive adds a complete zip named name under parent on src, with
// one member folder "site", and returns the zip's entry ID and the folder's
// member ID.
func insertArchive(t *testing.T, s *Store, src string, parent int64, name string) (zip, folder int64) {
	t.Helper()
	zip = insertEntry(t, s, src, parent, []byte(name), []byte(name), "file")
	mustWrite(t, s, `INSERT INTO archives (entry_id, format, state, size, mtime_ns, members, unpacked_bytes)
		VALUES (?, 'zip', 'complete', 9, 1, 1, 0)`, zip)
	folder = lastID(t, mustWrite(t, s, `INSERT INTO archive_members (archive_id, name, path, kind)
		VALUES (?, X'73697465', X'73697465', 'directory')`, zip))
	return zip, folder
}

// TestV8DatabaseMigratesToPlaces upgrades a v8 database whose relations are
// up to date: every row stays, the places tables start empty, and
// review_state is dirty again with its generation kept, so the visible
// figures stay until the relate pass that startup enqueues replaces them.
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
	insertArchive(t, s, "fotos", album, "site.zip")
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
	for _, p := range placesTables {
		if rows, ok := after[p.name]; ok {
			t.Errorf("%s has rows after the upgrade: %q", p.name, rows)
		}
		if got := tableColumns(t, s, p.name); !slices.Equal(got, p.columns()) {
			t.Errorf("%s columns = %q, want %q", p.name, got, p.columns())
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
	// Deleting an entry or a member checks the places tables through their
	// keys.
	assertForeignKeysIndexed(t, s, "entries", 2, entryPlacesT.name, archivePlacesT.name)
	assertForeignKeysIndexed(t, s, "archive_members", 1, memberPlacesT.name)
}

// Every figure's CHECK rejects a negative value and accepts zero, and a row
// names an existing entry or member.
func TestPlacesChecksRejectNegativeFigures(t *testing.T) {
	s, _ := openTemp(t, Options{})
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	zip, folder := insertArchive(t, s, "fotos", root, "site.zip")
	keys := map[placesTable]int64{entryPlacesT: root, archivePlacesT: zip, memberPlacesT: folder}
	for _, p := range placesTables {
		write := func(args ...any) error {
			return s.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(p.insert(), args...)
				return err
			})
		}
		for i, col := range placesFigures {
			row := placesRow(keys[p], 0)
			row[i+1] = int64(-1)
			if err := write(row...); err == nil {
				t.Errorf("%s accepted %s = -1", p.name, col)
			} else {
				wantConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK)
			}
		}
		if err := write(placesRow(keys[p], 0)...); err != nil {
			t.Fatalf("%s: a row of zero figures: %v", p.name, err)
		}
		wantConstraint(t, write(placesRow(99999, 0)...), sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
	}
}

// Removing an entry removes its entry_places and archive_places rows and
// leaves another's; removing a member folder, or its archive, removes its
// member_places row; removing a source removes its entries' and members'
// rows.
func TestPlacesCascade(t *testing.T) {
	s, _ := openTemp(t, Options{})
	type world struct{ root, album, zip, folder, zip2, folder2 int64 }
	worlds := map[string]world{}
	for _, src := range []string{"gone", "kept"} {
		insertSource(t, s, src)
		var w world
		w.root = insertEntry(t, s, src, nil, []byte{}, []byte{}, "directory")
		w.album = insertEntry(t, s, src, w.root, []byte("Album"), []byte("Album"), "directory")
		w.zip, w.folder = insertArchive(t, s, src, w.root, "site.zip")
		w.zip2, w.folder2 = insertArchive(t, s, src, w.root, "more.zip")
		for _, e := range []int64{w.root, w.album, w.zip, w.zip2} {
			mustWrite(t, s, entryPlacesT.insert(), placesRow(e, 1)...)
		}
		for _, e := range []int64{w.zip, w.zip2} {
			mustWrite(t, s, archivePlacesT.insert(), placesRow(e, 1)...)
		}
		for _, m := range []int64{w.folder, w.folder2} {
			mustWrite(t, s, memberPlacesT.insert(), placesRow(m, 1)...)
		}
		worlds[src] = w
	}

	kept := worlds["kept"]
	mustWrite(t, s, `DELETE FROM entries WHERE id = ?`, kept.album)
	if n := entryPlacesT.count(t, s, kept.album); n != 0 {
		t.Errorf("entry_places kept a removed entry's row")
	}
	if n := entryPlacesT.count(t, s, kept.root); n != 1 {
		t.Errorf("removing an entry removed another's entry_places row")
	}
	mustWrite(t, s, `DELETE FROM archive_members WHERE id = ?`, kept.folder)
	if n := memberPlacesT.count(t, s, kept.folder); n != 0 {
		t.Errorf("member_places kept a removed member's row")
	}
	if n := archivePlacesT.count(t, s, kept.zip); n != 1 {
		t.Errorf("removing a member removed its archive's archive_places row")
	}
	mustWrite(t, s, `DELETE FROM entries WHERE id = ?`, kept.zip2)
	if n := archivePlacesT.count(t, s, kept.zip2) + memberPlacesT.count(t, s, kept.folder2); n != 0 {
		t.Errorf("removing an archive left %d of its archive_places and member_places rows", n)
	}

	mustWrite(t, s, `DELETE FROM sources WHERE id = 'gone'`)
	gone := worlds["gone"]
	if n := entryPlacesT.count(t, s, gone.root, gone.album, gone.zip, gone.zip2) + archivePlacesT.count(t, s, gone.zip, gone.zip2) +
		memberPlacesT.count(t, s, gone.folder, gone.folder2); n != 0 {
		t.Errorf("removing a source left %d places rows of its entries and members", n)
	}
	for p, want := range map[placesTable]int{entryPlacesT: 2, archivePlacesT: 1, memberPlacesT: 0} {
		if n := countRows(t, s, `SELECT count(*) FROM `+p.name); n != want {
			t.Errorf("%s = %d rows after removing a source, want the kept source's %d", p.name, n, want)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
}
