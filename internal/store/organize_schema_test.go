package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

// r3 task 1.1: migration 0006 upgrades a v5 database in place, adding the
// write permission (off for every source) and the organize tables; their
// CHECK constraints reject bad values, removing a source removes its actions
// and items, and removing an entry or an action leaves the history with a
// NULL reference.

// tableColumns returns the column names of table in order.
func tableColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.Reader().Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	return cols
}

func TestV5DatabaseMigratesToOrganize(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 5)})
	if err != nil {
		t.Fatal(err)
	}
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	insertEntry(t, s, "fotos", root, []byte("a.jpg"), []byte("a.jpg"), "file")
	before := tableRows(t, s.Writer())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 6)})
	if err != nil {
		t.Fatalf("open the v5 database with 0006: %v", err)
	}
	defer s.Close()
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != 6 {
		t.Fatalf("schema version after upgrade = %d, %v; want 6", v, err)
	}
	after := tableRows(t, s.Writer())
	for table, rows := range before {
		if table == "sources" {
			continue
		}
		if !reflect.DeepEqual(after[table], rows) {
			t.Errorf("%s rows changed by the upgrade:\n before %q\n after  %q", table, rows, after[table])
		}
	}
	// The source keeps every value and gains write_enabled = 0, the last column.
	if want := []string{before["sources"][0] + "int64:0|"}; !reflect.DeepEqual(after["sources"], want) {
		t.Errorf("sources after the upgrade = %q, want %q", after["sources"], want)
	}
	for _, table := range []string{"actions", "action_items"} {
		if len(after[table]) != 0 {
			t.Errorf("%s has %d rows after the upgrade, want none", table, len(after[table]))
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
}

// The 0006 schema, as R3 shipped it; 0007 rebuilds these tables (see
// cleanup_schema_test.go).
func TestOrganizeColumnsAndIndexes(t *testing.T) {
	s, _ := openTemp(t, Options{Migrations: migrationsUpTo(t, 6)})
	if v, err := SchemaVersion(context.Background(), s.Writer()); err != nil || v != 6 {
		t.Fatalf("schema version = %d, %v; want 6", v, err)
	}
	if cols := tableColumns(t, s, "sources"); cols[len(cols)-1] != "write_enabled" {
		t.Errorf("sources columns end %q, want write_enabled", cols[len(cols)-1])
	}
	want := map[string][]string{
		"actions": {"id", "kind", "source_id", "state", "bulk", "destination_id", "undo_of", "kept_lost", "job_id",
			"created_at", "expires_at", "started_at", "finished_at"},
		"action_items": {"id", "action_id", "seq", "op", "entry_id",
			"from_parent", "from_name", "from_path", "to_parent", "to_dir_seq", "to_name", "to_path",
			"bytes", "files", "kind", "dev", "ino", "size", "mtime_ns", "decision_after", "created",
			"reverses", "reversed_by", "state", "reason", "detail", "finished_at"},
	}
	for table, cols := range want {
		if got := tableColumns(t, s, table); !slices.Equal(got, cols) {
			t.Errorf("%s columns = %q, want %q", table, got, cols)
		}
	}

	var indexes []string
	rows, err := s.Reader().Query(`SELECT name FROM sqlite_schema
		WHERE type = 'index' AND tbl_name IN ('actions', 'action_items') AND sql IS NOT NULL ORDER BY name`)
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
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	wantIndexes := []string{"action_items_entry", "action_items_from_parent", "action_items_open",
		"action_items_reversed_by", "action_items_reverses", "action_items_to_parent",
		"actions_by_source", "actions_destination", "actions_undo_of"}
	if !slices.Equal(indexes, wantIndexes) {
		t.Errorf("indexes = %q, want %q", indexes, wantIndexes)
	}
	// Removing a source's actions checks the undo references by index.
	assertForeignKeysIndexed(t, s, "actions", 2)
	assertForeignKeysIndexed(t, s, "action_items", 2)
}

// Every CHECK of 0006 rejects a bad value; the good values are accepted.
func TestOrganizeChecksRejectBadValues(t *testing.T) {
	s, _ := openTemp(t, Options{})
	insertSource(t, s, "fotos")
	const actionSQL = `INSERT INTO actions (kind, source_id, state, bulk, created_at) VALUES (?, 'fotos', ?, ?, 1)`
	const itemSQL = `INSERT INTO action_items (action_id, seq, op, decision_after, created, state)
		VALUES (?, ?, ?, ?, ?, ?)`
	action := lastID(t, mustWrite(t, s, actionSQL, "move", "planned", 0))
	write := func(q string, args ...any) error {
		return s.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(q, args...)
			return err
		})
	}

	seq := 0
	item := func(op string, decision any, created int, state string) error {
		seq++
		return write(itemSQL, action, seq, op, decision, created, state)
	}
	for _, kind := range []string{"move", "rename", "create_folder", "rescue", "merge", "undo"} {
		for _, state := range []string{"planned", "queued", "running", "done", "stopped", "expired"} {
			for _, bulk := range []int{0, 1} {
				if err := write(actionSQL, kind, state, bulk); err != nil {
					t.Fatalf("action %s %s bulk %d: %v", kind, state, bulk, err)
				}
			}
		}
	}
	for _, op := range []string{"rename", "mkdir", "rmdir"} {
		if err := item(op, nil, 1, "planned"); err != nil {
			t.Fatalf("item op %s: %v", op, err)
		}
	}
	for _, d := range []string{"undecided", "keep", "discard", "later"} {
		if err := item("rename", d, 0, "planned"); err != nil {
			t.Fatalf("item decision_after %s: %v", d, err)
		}
	}
	for _, state := range []string{"planned", "refused", "conflict", "intent", "done", "not_permitted", "offline",
		"changed", "failed", "no_safe_rename", "not_empty", "manual_recovery", "not_attempted", "resolved"} {
		if err := item("rename", nil, 0, state); err != nil {
			t.Fatalf("item state %s: %v", state, err)
		}
	}
	if err := write(`UPDATE sources SET write_enabled = 1 WHERE id = 'fotos'`); err != nil {
		t.Fatalf("write_enabled 1: %v", err)
	}

	for _, bad := range []struct {
		name string
		err  func() error
	}{
		{"write_enabled 2", func() error { return write(`UPDATE sources SET write_enabled = 2 WHERE id = 'fotos'`) }},
		{"action kind", func() error { return write(actionSQL, "delete", "planned", 0) }},
		{"action state", func() error { return write(actionSQL, "move", "failed", 0) }},
		{"action bulk 2", func() error { return write(actionSQL, "move", "planned", 2) }},
		{"item op", func() error { return item("delete", nil, 0, "planned") }},
		{"item decision_after", func() error { return item("rename", "maybe", 0, "planned") }},
		{"item created 2", func() error { return item("mkdir", nil, 2, "planned") }},
		{"item state", func() error { return item("rename", nil, 0, "running") }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			wantConstraint(t, bad.err(), sqlite3.SQLITE_CONSTRAINT_CHECK)
		})
	}
	// seq is unique within an action.
	wantConstraint(t, write(itemSQL, action, 1, "rename", nil, 0, "planned"), sqlite3.SQLITE_CONSTRAINT_UNIQUE)
}

// Removing an entry keeps the history and clears the reference; removing an
// action clears the undo references to it; removing a source removes its
// actions and their items, and leaves other sources' alone.
func TestOrganizeCascades(t *testing.T) {
	s, _ := openTemp(t, Options{})
	type fixture struct {
		action, undo, item, undoItem, dir, file int64
	}
	setup := func(src string) fixture {
		insertSource(t, s, src)
		root := insertEntry(t, s, src, nil, []byte{}, []byte{}, "directory")
		dir := insertEntry(t, s, src, root, []byte("Fotos"), []byte("Fotos"), "directory")
		file := insertEntry(t, s, src, root, []byte("a.jpg"), []byte("a.jpg"), "file")
		var f fixture
		f.dir, f.file = dir, file
		f.action = lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, destination_id, created_at)
			VALUES ('move', ?, 'done', 0, ?, 1)`, src, dir))
		f.item = lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, to_parent, state)
			VALUES (?, 1, 'rename', ?, ?, ?, 'done')`, f.action, file, root, dir))
		f.undo = lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, undo_of, created_at)
			VALUES ('undo', ?, 'done', 0, ?, 2)`, src, f.action))
		f.undoItem = lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, to_parent,
			reverses, state) VALUES (?, 1, 'rename', ?, ?, ?, ?, 'done')`, f.undo, file, dir, root, f.item))
		mustWrite(t, s, `UPDATE action_items SET reversed_by = ? WHERE id = ?`, f.undoItem, f.item)
		return f
	}
	gone, kept := setup("gone"), setup("kept")

	// Removing entries keeps every item and clears the references.
	mustWrite(t, s, `DELETE FROM entries WHERE id IN (?, ?)`, kept.file, kept.dir)
	if n := countRows(t, s, `SELECT count(*) FROM action_items i JOIN actions a ON a.id = i.action_id
		WHERE a.source_id = 'kept' AND i.entry_id IS NULL AND i.to_parent IS NULL`); n != 1 {
		t.Errorf("items of kept with entry_id and to_parent NULL = %d, want 1", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items i JOIN actions a ON a.id = i.action_id
		WHERE a.source_id = 'kept' AND i.entry_id IS NULL AND i.from_parent IS NULL`); n != 1 {
		t.Errorf("items of kept with entry_id and from_parent NULL = %d, want 1", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ? AND destination_id IS NULL`, kept.action); n != 1 {
		t.Errorf("the action's destination outlived its entry")
	}

	// Removing the undo item clears reversed_by; removing the original
	// action clears undo_of and reverses.
	mustWrite(t, s, `DELETE FROM action_items WHERE id = ?`, kept.undoItem)
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE id = ? AND reversed_by IS NULL`, kept.item); n != 1 {
		t.Errorf("reversed_by outlived the undo item")
	}
	mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, reverses, state) VALUES (?, 2, 'rename', ?, 'planned')`,
		kept.undo, kept.item)
	mustWrite(t, s, `DELETE FROM actions WHERE id = ?`, kept.action)
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ? AND undo_of IS NULL`, kept.undo); n != 1 {
		t.Errorf("undo_of outlived the undone action")
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE action_id = ? AND reverses IS NULL`, kept.undo); n != 1 {
		t.Errorf("reverses outlived the reversed item")
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE action_id = ?`, kept.action); n != 0 {
		t.Errorf("removing an action left %d of its items", n)
	}

	mustWrite(t, s, `DELETE FROM sources WHERE id = 'gone'`)
	for _, id := range []int64{gone.action, gone.undo} {
		if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ?`, id); n != 0 {
			t.Errorf("removing the source left action %d", id)
		}
		if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE action_id = ?`, id); n != 0 {
			t.Errorf("removing the source left %d items of action %d", n, id)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE source_id = 'kept'`); n != 1 {
		t.Errorf("the other source has %d actions, want its undo action", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items`); n != 1 {
		t.Errorf("action_items = %d, want the other source's undo item", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
}
