package store

import (
	"context"
	"reflect"
	"slices"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

// r2c task 2.1: migration 0005 upgrades a v4 database in place: it rebuilds
// review_rows with the list CHECK of r2c, keeps the gems_rescue rows as
// rescue rows with their group, drops the rows of the two removed Gems
// lists, keeps every other row and the source rows of the duplicates rows,
// and recreates the indexes.
func TestV4DatabaseMigratesToRescue(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 4)})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"a", "b"} {
		insertSource(t, s, src)
	}
	root := insertEntry(t, s, "a", nil, []byte{}, []byte{}, "directory")
	office := insertEntry(t, s, "a", root, []byte("Microsoft Office"), []byte("Microsoft Office"), "directory")
	xls := insertEntry(t, s, "a", office, []byte("orcamento.xls"), []byte("Microsoft Office/orcamento.xls"), "file")
	photo := insertEntry(t, s, "a", root, []byte("foto.jpg"), []byte("foto.jpg"), "file")
	rootB := insertEntry(t, s, "b", nil, []byte{}, []byte{}, "directory")
	copyB := insertEntry(t, s, "b", rootB, []byte("foto.jpg"), []byte("foto.jpg"), "file")
	content := lastID(t, mustWrite(t, s, `INSERT INTO contents (sha256, size) VALUES (zeroblob(32), 5)`))
	rel := lastID(t, mustWrite(t, s, `INSERT INTO relations (gen, kind, a_entry, b_entry, matched_bytes, redundant_bytes,
		a_bytes, a_files, b_bytes, b_files, a_only_files, a_only_bytes, b_only_files, b_only_bytes)
		VALUES (1, 'overlap', ?, ?, 5, 5, 5, 1, 5, 1, 0, 0, 0, 0)`, photo, copyB))
	mustWrite(t, s, `UPDATE review_state SET gen = 1, dirty = 0, computed_at = 7`)
	group := lastID(t, mustWrite(t, s, `INSERT INTO review_rows (gen, list, content_id, bytes, files, sort_key)
		VALUES (1, 'duplicates', ?, 5, 1, 5)`, content))
	for _, src := range []string{"a", "b"} {
		mustWrite(t, s, `INSERT INTO review_row_sources (row_id, source_id) VALUES (?, ?)`, group, src)
	}
	junk := lastID(t, mustWrite(t, s, `INSERT INTO review_rows (gen, list, source_id, entry_id, bytes, files, sort_key)
		VALUES (1, 'system_junk', 'a', ?, 5, 1, 5)`, photo))
	rescue := lastID(t, mustWrite(t, s, `INSERT INTO review_rows (gen, list, source_id, entry_id, group_id, bytes, files, sort_key)
		VALUES (1, 'gems_rescue', 'a', ?, ?, 300, 1, 0)`, xls, office))
	mustWrite(t, s, `INSERT INTO review_rows (gen, list, source_id, entry_id, bytes, files, sort_key)
		VALUES (1, 'gems_unique', 'a', ?, 5, 1, 1234)`, photo)
	mustWrite(t, s, `INSERT INTO review_rows (gen, list, source_id, entry_id, group_id, bytes, files, sort_key)
		VALUES (1, 'gems_only_in_copy', 'a', ?, ?, 5, 1, ?)`, photo, root, rel)
	before := tableRows(t, s.Writer())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 5)})
	if err != nil {
		t.Fatalf("open the v4 database with 0005: %v", err)
	}
	defer s.Close()
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != 5 {
		t.Fatalf("schema version after upgrade = %d, %v; want 5", v, err)
	}
	after := tableRows(t, s.Writer())
	for table, rows := range before {
		if table == "review_rows" {
			continue
		}
		if !reflect.DeepEqual(after[table], rows) {
			t.Errorf("%s rows changed by the upgrade:\n before %q\n after  %q", table, rows, after[table])
		}
	}
	if len(after["review_row_sources"]) != 2 {
		t.Errorf("review_row_sources = %q, want the duplicates row's two sources", after["review_row_sources"])
	}

	type row struct {
		id, entry, group, sortKey int64
		list                      string
	}
	rows, err := s.Reader().Query(`SELECT id, list, coalesce(entry_id, 0), coalesce(group_id, 0), sort_key
		FROM review_rows ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.list, &r.entry, &r.group, &r.sortKey); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	// A rescue row's sort key is its bytes (r2c design D4).
	want := []row{
		{id: group, list: "duplicates", sortKey: 5},
		{id: junk, list: "system_junk", entry: photo, sortKey: 5},
		{id: rescue, list: "rescue", entry: xls, group: office, sortKey: 300},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("review rows after the upgrade = %+v, want %+v", got, want)
	}

	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
	var fkTable string
	if err := s.Reader().QueryRow(`SELECT "table" FROM pragma_foreign_key_list('review_row_sources')
		WHERE "from" = 'row_id'`).Scan(&fkTable); err != nil || fkTable != "review_rows" {
		t.Errorf("review_row_sources.row_id references %q, %v; want review_rows", fkTable, err)
	}
	var indexes []string
	rows, err = s.Reader().Query(`SELECT name FROM sqlite_schema
		WHERE type = 'index' AND tbl_name IN ('review_rows', 'review_row_sources') AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, n)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	wantIndexes := []string{"review_row_sources_row", "review_rows_content", "review_rows_entry", "review_rows_group",
		"review_rows_list", "review_rows_relation", "review_rows_source"}
	if !slices.Equal(indexes, wantIndexes) {
		t.Errorf("indexes = %q, want %q", indexes, wantIndexes)
	}
	if n := countRows(t, s, `SELECT count(*) FROM sqlite_schema WHERE name LIKE '%_v5'`); n != 0 {
		t.Errorf("%d objects still named _v5", n)
	}

	// The new CHECK refuses the removed lists, and the cascades still hold.
	for _, l := range []string{"gems_unique", "gems_rescue", "gems_only_in_copy"} {
		_, err := s.Writer().Exec(`INSERT INTO review_rows (gen, list, source_id, entry_id, bytes, files, sort_key)
			VALUES (1, ?, 'a', ?, 5, 1, 5)`, l, photo)
		wantConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK)
	}
	mustWrite(t, s, `DELETE FROM review_rows WHERE id = ?`, group)
	if n := countRows(t, s, `SELECT count(*) FROM review_row_sources`); n != 0 {
		t.Errorf("deleting the duplicates row left %d source rows", n)
	}
	mustWrite(t, s, `DELETE FROM sources WHERE id = 'a'`)
	if n := countRows(t, s, `SELECT count(*) FROM review_rows`); n != 0 {
		t.Errorf("removing the source left %d review rows", n)
	}
}
