package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

// r4 task 1.1: migration 0007 rebuilds actions and action_items in place,
// keeping every row and every reference between them; it adds the
// quarantine folder of each source and the pre-delete check tables. Their
// CHECK constraints reject bad values, the cascades hold, and every foreign
// key is searched through an index.

// TestV6DatabaseMigratesToCleanup fills a v6 database with actions and
// items that reference each other (an undo of a move) and entries, then
// upgrades it: every old value stays, the new columns take their defaults.
func TestV6DatabaseMigratesToCleanup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 6)})
	if err != nil {
		t.Fatal(err)
	}
	insertSource(t, s, "fotos")
	insertSource(t, s, "docs")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	album := insertEntry(t, s, "fotos", root, []byte("Album"), []byte("Album"), "directory")
	file := insertEntry(t, s, "fotos", root, []byte("a.jpg"), []byte("a.jpg"), "file")
	docsRoot := insertEntry(t, s, "docs", nil, []byte{}, []byte{}, "directory")
	mustWrite(t, s, `UPDATE sources SET write_enabled = 1 WHERE id = 'fotos'`)

	move := lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, destination_id, kept_lost,
		job_id, created_at, expires_at, started_at, finished_at) VALUES ('move', 'fotos', 'done', 1, ?, 2, 7, 10, 20, 11, 12)`, album))
	mkdir := lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, to_parent, to_name, to_path,
		created, state, finished_at) VALUES (?, 1, 'mkdir', ?, ?, X'416c62756d', X'416c62756d', 1, 'done', 11)`, move, album, root))
	moved := lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name,
		from_path, to_parent, to_dir_seq, to_name, to_path, bytes, files, kind, dev, ino, size, mtime_ns, decision_after,
		state, reason, detail, finished_at) VALUES (?, 2, 'rename', ?, ?, X'612e6a7067', X'612e6a7067', ?, 1,
		X'612e6a7067', X'416c62756d2f612e6a7067', 100, 1, 'file', 5, 6, 100, 99, 'keep', 'done', NULL, NULL, 12)`,
		move, file, root, album))
	undo := lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, undo_of, created_at)
		VALUES ('undo', 'fotos', 'stopped', 0, ?, 30)`, move))
	reverse := lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, to_parent,
		reverses, state, reason, detail) VALUES (?, 1, 'rename', ?, ?, ?, ?, 'manual_recovery', 'x', '{"from":"absent","to":"same"}')`,
		undo, file, album, root, moved))
	mustWrite(t, s, `UPDATE action_items SET reversed_by = ? WHERE id = ?`, reverse, moved)
	mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, reverses, state)
		VALUES (?, 2, 'rmdir', ?, ?, 'not_attempted')`, undo, album, mkdir)
	mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, destination_id, created_at)
		VALUES ('create_folder', 'docs', 'expired', 0, ?, 40)`, docsRoot)
	before := tableRows(t, s.Writer())
	if len(before["actions"]) != 3 || len(before["action_items"]) != 4 {
		t.Fatalf("v6 fixture: %d actions, %d items", len(before["actions"]), len(before["action_items"]))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 7)})
	if err != nil {
		t.Fatalf("open the v6 database with 0007: %v", err)
	}
	defer s.Close()
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != 7 {
		t.Fatalf("schema version after upgrade = %d, %v; want 7", v, err)
	}
	after := tableRows(t, s.Writer())
	null := "<nil>:<nil>|"
	grown := map[string]string{
		"sources":      null,                                            // quarantine_entry_id
		"actions":      null + null + null + "int64:0|int64:0|int64:0|", // ground, check_id, list, deleted_*, freed_bytes
		"action_items": null + null + null,                              // ctime_ns, draft_bytes, draft_files
	}
	for table, rows := range before {
		want := rows
		if suffix, ok := grown[table]; ok {
			want = nil
			for _, r := range rows {
				want = append(want, r+suffix)
			}
		}
		if !reflect.DeepEqual(after[table], want) {
			t.Errorf("%s rows changed by the upgrade:\n want %q\n got  %q", table, want, after[table])
		}
	}
	for _, table := range []string{"purge_checks", "purge_check_items", "purge_check_files"} {
		if _, ok := after[table]; ok {
			t.Errorf("%s has rows after the upgrade", table)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
	// The references still point at the same rows, and act as foreign keys
	// into the rebuilt tables: removing the move clears the undo's links.
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE id = ? AND reversed_by = ?`, moved, reverse); n != 1 {
		t.Errorf("reversed_by lost by the upgrade")
	}
	mustWrite(t, s, `DELETE FROM actions WHERE id = ?`, move)
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ? AND undo_of IS NULL`, undo); n != 1 {
		t.Errorf("undo_of outlived the undone action after the upgrade")
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE action_id = ? AND reverses IS NULL`, undo); n != 2 {
		t.Errorf("reverses outlived the reversed items after the upgrade")
	}
}

// TestCleanupColumnsAndIndexes pins the 0007 columns, appended after
// 0006's, and every index of the rebuilt and new tables, as R4 shipped them;
// 0008 rebuilds actions and action_items again (see media_schema_test.go).
func TestCleanupColumnsAndIndexes(t *testing.T) {
	s, _ := openTemp(t, Options{Migrations: migrationsUpTo(t, 7)})
	if v, err := SchemaVersion(context.Background(), s.Writer()); err != nil || v != 7 {
		t.Fatalf("schema version = %d, %v; want 7", v, err)
	}
	want := map[string][]string{
		"actions": {"id", "kind", "source_id", "state", "bulk", "destination_id", "undo_of", "kept_lost", "job_id",
			"created_at", "expires_at", "started_at", "finished_at",
			"ground", "check_id", "list", "deleted_files", "deleted_bytes", "freed_bytes"},
		"action_items": {"id", "action_id", "seq", "op", "entry_id",
			"from_parent", "from_name", "from_path", "to_parent", "to_dir_seq", "to_name", "to_path",
			"bytes", "files", "kind", "dev", "ino", "size", "mtime_ns", "decision_after", "created",
			"reverses", "reversed_by", "state", "reason", "detail", "finished_at",
			"ctime_ns", "draft_bytes", "draft_files"},
		"purge_checks": {"id", "source_id", "state", "job_id", "created_at", "finished_at",
			"junk_confirmed_at", "stale_reason"},
		"purge_check_items": {"check_id", "entry_id", "path", "readable"},
		"purge_check_files": {"id", "check_id", "item_id", "entry_id", "member_id",
			"kind", "path", "size", "mtime_ns", "ctime_ns", "ino", "dev", "nlink", "alloc", "sha256",
			"verdict", "class", "copy_source", "copy_path", "copy_entry", "copy_member",
			"copy_size", "copy_mtime_ns", "copy_ctime_ns", "copy_ino", "copy_dev", "copy_hard_link", "confirmed_at"},
	}
	for table, cols := range want {
		if got := tableColumns(t, s, table); !slices.Equal(got, cols) {
			t.Errorf("%s columns = %q, want %q", table, got, cols)
		}
	}
	if cols := tableColumns(t, s, "sources"); cols[len(cols)-1] != "quarantine_entry_id" {
		t.Errorf("sources columns end %q, want quarantine_entry_id", cols[len(cols)-1])
	}

	rows, err := s.Reader().Query(`SELECT name FROM sqlite_schema WHERE type = 'index' AND sql IS NOT NULL
		AND tbl_name IN ('actions', 'action_items', 'purge_checks', 'purge_check_items', 'purge_check_files', 'sources')
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var indexes []string
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
		"actions_by_source", "actions_check", "actions_destination", "actions_undo_of",
		"purge_check_files_by_check", "purge_check_files_copy", "purge_check_files_path",
		"purge_check_items_entry", "purge_checks_by_source", "sources_quarantine"}
	if !slices.Equal(indexes, wantIndexes) {
		t.Errorf("indexes = %q, want %q", indexes, wantIndexes)
	}
	// Rebuilt references still name the renamed tables.
	var schema string
	if err := s.Reader().QueryRow(`SELECT group_concat(sql, ' ') FROM sqlite_schema WHERE type = 'table'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(schema, "_v7") {
		t.Errorf("the schema still names a _v7 table")
	}
	assertForeignKeysIndexed(t, s, "actions", 2)
	assertForeignKeysIndexed(t, s, "action_items", 2)
	assertForeignKeysIndexed(t, s, "purge_checks", 3)
}

// Every CHECK 0007 adds or widens rejects a bad value; the good values are
// accepted.
func TestCleanupChecksRejectBadValues(t *testing.T) {
	s, _ := openTemp(t, Options{})
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	write := func(q string, args ...any) error {
		return s.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(q, args...)
			return err
		})
	}
	const actionSQL = `INSERT INTO actions (kind, source_id, state, bulk, ground, created_at) VALUES (?, 'fotos', 'planned', 0, ?, 1)`
	const itemSQL = `INSERT INTO action_items (action_id, seq, op, state) VALUES (?, ?, ?, ?)`
	const checkSQL = `INSERT INTO purge_checks (source_id, state, created_at) VALUES ('fotos', ?, 1)`
	const setSQL = `INSERT INTO purge_check_items (check_id, entry_id, path, readable) VALUES (?, ?, X'', ?)`
	const fileSQL = `INSERT INTO purge_check_files (check_id, item_id, entry_id, kind, path, size, verdict, class)
		VALUES (?, 1, 1, 'file', X'', 0, ?, ?)`

	action := lastID(t, mustWrite(t, s, actionSQL, "cleanup", "discard"))
	for _, kind := range []string{"move", "rename", "create_folder", "rescue", "merge", "undo", "cleanup", "restore", "purge"} {
		if err := write(actionSQL, kind, nil); err != nil {
			t.Fatalf("action kind %s: %v", kind, err)
		}
	}
	for _, ground := range []string{"discard", "duplicate"} {
		if err := write(actionSQL, "cleanup", ground); err != nil {
			t.Fatalf("action ground %s: %v", ground, err)
		}
	}
	seq := 0
	item := func(op, state string) error {
		seq++
		return write(itemSQL, action, seq, op, state)
	}
	for _, op := range []string{"rename", "mkdir", "rmdir", "record", "unlink", "purge", "verify"} {
		if err := item(op, "planned"); err != nil {
			t.Fatalf("item op %s: %v", op, err)
		}
	}
	for _, state := range []string{"planned", "refused", "conflict", "intent", "done", "not_permitted", "offline",
		"changed", "failed", "no_safe_rename", "not_empty", "manual_recovery", "not_attempted", "resolved", "blocked"} {
		if err := item("rename", state); err != nil {
			t.Fatalf("item state %s: %v", state, err)
		}
	}
	check := lastID(t, mustWrite(t, s, checkSQL, "running"))
	for _, state := range []string{"running", "ready", "failed", "stale"} {
		if err := write(checkSQL, state); err != nil {
			t.Fatalf("check state %s: %v", state, err)
		}
	}
	for i, readable := range []int{0, 1} {
		entry := insertEntry(t, s, "fotos", root, []byte{'a' + byte(i)}, []byte{'a' + byte(i)}, "file")
		if err := write(setSQL, check, entry, readable); err != nil {
			t.Fatalf("set readable %d: %v", readable, err)
		}
	}
	for _, verdict := range []string{"safe", "copy_offline", "unique", "unreadable", "opaque_archive", "no_content"} {
		if err := write(fileSQL, check, verdict, nil); err != nil {
			t.Fatalf("file verdict %s: %v", verdict, err)
		}
	}
	for _, class := range []string{"possibly_valuable", "likely_junk", "uncertain"} {
		if err := write(fileSQL, check, "unique", class); err != nil {
			t.Fatalf("file class %s: %v", class, err)
		}
	}

	for _, bad := range []struct {
		name string
		err  func() error
	}{
		{"action kind", func() error { return write(actionSQL, "delete", nil) }},
		{"action ground", func() error { return write(actionSQL, "cleanup", "junk") }},
		{"item op", func() error { return item("delete", "planned") }},
		{"item state", func() error { return item("rename", "running") }},
		{"check state", func() error { return write(checkSQL, "done") }},
		{"set readable 2", func() error {
			return write(setSQL, check, insertEntry(t, s, "fotos", root, []byte("z"), []byte("z"), "file"), 2)
		}},
		{"file verdict", func() error { return write(fileSQL, check, "kept", nil) }},
		{"file class", func() error { return write(fileSQL, check, "unique", "precious") }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			wantConstraint(t, bad.err(), sqlite3.SQLITE_CONSTRAINT_CHECK)
		})
	}
	// An entry is in a check's set once.
	entry := insertEntry(t, s, "fotos", root, []byte("y"), []byte("y"), "file")
	mustWrite(t, s, setSQL, check, entry, 1)
	wantConstraint(t, write(setSQL, check, entry, 1), sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
	// A check_id must name a check.
	wantConstraint(t, write(`UPDATE actions SET check_id = 999 WHERE id = ?`, action), sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
}

// Removing the quarantine folder's entry clears the source's reference;
// removing a set entry drops it from the set; removing a check clears the
// purge action's reference and removes its set and files; removing a source
// removes its checks, and leaves another source's alone.
func TestCleanupCascades(t *testing.T) {
	s, _ := openTemp(t, Options{})
	type fixture struct {
		quarantine, item, check, action int64
	}
	setup := func(src string) fixture {
		insertSource(t, s, src)
		root := insertEntry(t, s, src, nil, []byte{}, []byte{}, "directory")
		var f fixture
		f.quarantine = insertEntry(t, s, src, root, []byte(".precious-quarantine"), []byte(".precious-quarantine"), "directory")
		f.item = insertEntry(t, s, src, f.quarantine, []byte("a.jpg"), []byte(".precious-quarantine/a.jpg"), "file")
		mustWrite(t, s, `UPDATE sources SET quarantine_entry_id = ? WHERE id = ?`, f.quarantine, src)
		f.check = lastID(t, mustWrite(t, s, `INSERT INTO purge_checks (source_id, state, created_at) VALUES (?, 'ready', 1)`, src))
		mustWrite(t, s, `INSERT INTO purge_check_items (check_id, entry_id, path, readable) VALUES (?, ?, X'00', 1)`, f.check, f.item)
		mustWrite(t, s, `INSERT INTO purge_check_files (check_id, item_id, entry_id, kind, path, size, verdict)
			VALUES (?, ?, ?, 'file', X'00', 1, 'unique')`, f.check, f.item, f.item)
		f.action = lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, check_id, created_at)
			VALUES ('purge', ?, 'planned', 0, ?, 1)`, src, f.check))
		return f
	}
	gone, kept := setup("gone"), setup("kept")

	mustWrite(t, s, `DELETE FROM entries WHERE id = ?`, kept.item)
	if n := countRows(t, s, `SELECT count(*) FROM purge_check_items WHERE check_id = ?`, kept.check); n != 0 {
		t.Errorf("the set kept a removed entry")
	}
	if n := countRows(t, s, `SELECT count(*) FROM purge_check_files WHERE check_id = ?`, kept.check); n != 1 {
		t.Errorf("removing an entry removed the check's recorded file")
	}
	mustWrite(t, s, `DELETE FROM entries WHERE id = ?`, kept.quarantine)
	if n := countRows(t, s, `SELECT count(*) FROM sources WHERE id = 'kept' AND quarantine_entry_id IS NULL`); n != 1 {
		t.Errorf("quarantine_entry_id outlived its entry")
	}
	mustWrite(t, s, `DELETE FROM purge_checks WHERE id = ?`, kept.check)
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ? AND check_id IS NULL`, kept.action); n != 1 {
		t.Errorf("the purge action's check_id outlived its check")
	}
	if n := countRows(t, s, `SELECT count(*) FROM purge_check_files WHERE check_id = ?`, kept.check); n != 0 {
		t.Errorf("removing a check left %d of its files", n)
	}

	mustWrite(t, s, `DELETE FROM sources WHERE id = 'gone'`)
	for q, want := range map[string]int{
		`SELECT count(*) FROM purge_checks`:      0,
		`SELECT count(*) FROM purge_check_items`: 0,
		`SELECT count(*) FROM purge_check_files`: 0,
		`SELECT count(*) FROM actions`:           1,
	} {
		if n := countRows(t, s, q); n != want {
			t.Errorf("%s = %d after removing a source, want %d", q, n, want)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ?`, gone.action); n != 0 {
		t.Errorf("removing the source left its purge action")
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
}
