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

// r5 task 1.1: migration 0008 rebuilds actions and action_items in place
// again, keeping every row, ID, and reference between them; it adds the
// media metadata cache, the effective dates, the owner's corrections, the
// cameras, and each source's media state. Their CHECK constraints reject bad
// values, the cascades hold, and every foreign key is searched through an
// index.

// TestV7DatabaseMigratesToMedia fills a v7 database with actions and items
// that reference each other (an undo of a move), entries, and a purge check
// a purge action names, then upgrades it: every old value and ID stays, the
// new columns take their defaults, and the references still act as foreign
// keys into the rebuilt tables.
func TestV7DatabaseMigratesToMedia(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 7)})
	if err != nil {
		t.Fatal(err)
	}
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	album := insertEntry(t, s, "fotos", root, []byte("Album"), []byte("Album"), "directory")
	file := insertEntry(t, s, "fotos", root, []byte("a.jpg"), []byte("a.jpg"), "file")
	quarantine := insertEntry(t, s, "fotos", root, []byte(".precious-quarantine"), []byte(".precious-quarantine"), "directory")
	held := insertEntry(t, s, "fotos", quarantine, []byte("b.jpg"), []byte(".precious-quarantine/b.jpg"), "file")
	mustWrite(t, s, `UPDATE sources SET write_enabled = 1, quarantine_entry_id = ? WHERE id = 'fotos'`, quarantine)

	move := lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, destination_id, kept_lost,
		job_id, created_at, expires_at, started_at, finished_at) VALUES ('move', 'fotos', 'done', 1, ?, 2, 7, 10, 20, 11, 12)`, album))
	mkdir := lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, to_parent, to_name, to_path,
		created, state, finished_at) VALUES (?, 1, 'mkdir', ?, ?, X'416c62756d', X'416c62756d', 1, 'done', 11)`, move, album, root))
	moved := lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name,
		from_path, to_parent, to_dir_seq, to_name, to_path, bytes, files, kind, dev, ino, size, mtime_ns, decision_after,
		state, finished_at, ctime_ns) VALUES (?, 2, 'rename', ?, ?, X'612e6a7067', X'612e6a7067', ?, 1,
		X'612e6a7067', X'416c62756d2f612e6a7067', 100, 1, 'file', 5, 6, 100, 99, 'keep', 'done', 12, 98)`,
		move, file, root, album))
	undo := lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, undo_of, created_at)
		VALUES ('undo', 'fotos', 'stopped', 0, ?, 30)`, move))
	reverse := lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, to_parent,
		reverses, state, reason, detail) VALUES (?, 1, 'rename', ?, ?, ?, ?, 'manual_recovery', 'x', '{"from":"absent","to":"same"}')`,
		undo, file, album, root, moved))
	mustWrite(t, s, `UPDATE action_items SET reversed_by = ? WHERE id = ?`, reverse, moved)
	mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, reverses, state)
		VALUES (?, 2, 'rmdir', ?, ?, 'not_attempted')`, undo, album, mkdir)
	cleanup := lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, ground, list, created_at)
		VALUES ('cleanup', 'fotos', 'done', 1, 'discard', 'review:junk', 40)`))
	mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, from_path, kind, dev, ino, size, mtime_ns,
		ctime_ns, draft_bytes, draft_files, state, reason) VALUES (?, 1, 'rename', ?, X'622e6a7067', 'file', 5, 7, 50, 97, 96,
		50, 1, 'blocked', 'kept')`, cleanup, held)
	check := lastID(t, mustWrite(t, s, `INSERT INTO purge_checks (source_id, state, job_id, created_at, finished_at)
		VALUES ('fotos', 'ready', 9, 50, 51)`))
	mustWrite(t, s, `INSERT INTO purge_check_items (check_id, entry_id, path, readable)
		VALUES (?, ?, X'622e6a7067', 1)`, check, held)
	purge := lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, check_id, created_at,
		deleted_files, deleted_bytes, freed_bytes) VALUES ('purge', 'fotos', 'done', 1, ?, 52, 1, 50, 4096)`, check))
	mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, state) VALUES (?, 1, 'purge', ?, 'done')`, purge, held)
	before := tableRows(t, s.Writer())
	if len(before["actions"]) != 4 || len(before["action_items"]) != 6 || len(before["purge_checks"]) != 1 {
		t.Fatalf("v7 fixture: %d actions, %d items, %d checks",
			len(before["actions"]), len(before["action_items"]), len(before["purge_checks"]))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, dir, Options{Migrations: migrationsUpTo(t, 8)})
	if err != nil {
		t.Fatalf("open the v7 database with 0008: %v", err)
	}
	defer s.Close()
	if v, err := SchemaVersion(ctx, s.Writer()); err != nil || v != 8 {
		t.Fatalf("schema version after upgrade = %d, %v; want 8", v, err)
	}
	after := tableRows(t, s.Writer())
	null := "<nil>:<nil>|"
	grown := map[string]string{
		"actions":      null + "int64:0|",  // template, rename
		"action_items": null + null + null, // new_mtime_ns, prev_mtime_ns, copy_of
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
	for _, table := range []string{"media_meta", "media_dates", "date_corrections", "media_cameras", "media_sources"} {
		if _, ok := after[table]; ok {
			t.Errorf("%s has rows after the upgrade", table)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
	// The references still point at the same rows, and act as foreign keys
	// into the rebuilt tables: removing the check clears the purge's link,
	// and removing the move clears the undo's links.
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE id = ? AND reversed_by = ?`, moved, reverse); n != 1 {
		t.Errorf("reversed_by lost by the upgrade")
	}
	mustWrite(t, s, `DELETE FROM purge_checks WHERE id = ?`, check)
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ? AND check_id IS NULL`, purge); n != 1 {
		t.Errorf("check_id outlived the check after the upgrade")
	}
	mustWrite(t, s, `DELETE FROM actions WHERE id = ?`, move)
	if n := countRows(t, s, `SELECT count(*) FROM actions WHERE id = ? AND undo_of IS NULL`, undo); n != 1 {
		t.Errorf("undo_of outlived the undone action after the upgrade")
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE action_id = ? AND reverses IS NULL`, undo); n != 2 {
		t.Errorf("reverses outlived the reversed items after the upgrade")
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE action_id = ?`, move); n != 0 {
		t.Errorf("removing an action left %d of its items after the upgrade", n)
	}
}

// TestMediaColumnsAndIndexes pins the 0008 columns, appended after 0007's,
// and every index of the rebuilt and new tables.
func TestMediaColumnsAndIndexes(t *testing.T) {
	s, _ := openTemp(t, Options{})
	if v, err := SchemaVersion(context.Background(), s.Writer()); err != nil || v != latestVersion(t) || v < 8 {
		t.Fatalf("schema version = %d, %v; want the latest, at least 8", v, err)
	}
	want := map[string][]string{
		"actions": {"id", "kind", "source_id", "state", "bulk", "destination_id", "undo_of", "kept_lost", "job_id",
			"created_at", "expires_at", "started_at", "finished_at",
			"ground", "check_id", "list", "deleted_files", "deleted_bytes", "freed_bytes",
			"template", "rename"},
		"action_items": {"id", "action_id", "seq", "op", "entry_id",
			"from_parent", "from_name", "from_path", "to_parent", "to_dir_seq", "to_name", "to_path",
			"bytes", "files", "kind", "dev", "ino", "size", "mtime_ns", "decision_after", "created",
			"reverses", "reversed_by", "state", "reason", "detail", "finished_at",
			"ctime_ns", "draft_bytes", "draft_files",
			"new_mtime_ns", "prev_mtime_ns", "copy_of"},
		"media_meta": {"entry_id", "source_id", "state", "size", "mtime_ns", "ctime_ns", "ino",
			"capture_local", "capture_offset_min", "gps_ns", "container_ns", "make", "model", "serial", "read_at"},
		"media_dates": {"entry_id", "source_id", "effective_ns", "local", "offset_min", "precision", "source",
			"confidence", "refined", "corrected", "flags", "meta_state", "camera_key", "inputs_key", "computed_at"},
		"date_corrections": {"entry_id", "kind", "set_local", "set_offset_min", "shift_s", "batch_id", "created_at"},
		"media_cameras": {"source_id", "camera_key", "make", "model", "serial", "photos", "state",
			"suggested_shift_s", "basis", "computed_at"},
		"media_sources": {"source_id", "dirty", "passes_job", "summary", "summary_at", "detected_at"},
	}
	for table, cols := range want {
		if got := tableColumns(t, s, table); !slices.Equal(got, cols) {
			t.Errorf("%s columns = %q, want %q", table, got, cols)
		}
	}

	rows, err := s.Reader().Query(`SELECT name FROM sqlite_schema WHERE type = 'index' AND sql IS NOT NULL
		AND tbl_name IN ('actions', 'action_items', 'media_meta', 'media_dates', 'date_corrections',
			'media_cameras', 'media_sources')
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
	wantIndexes := []string{"action_items_copy_of", "action_items_entry", "action_items_from_parent", "action_items_open",
		"action_items_reversed_by", "action_items_reverses", "action_items_to_parent",
		"actions_by_source", "actions_check", "actions_destination", "actions_undo_of",
		"media_dates_by_camera", "media_dates_by_source", "media_dates_by_time",
		"media_dates_camera_offset", "media_dates_implausible", "media_dates_mtime_disagrees",
		"media_dates_no_date_metadata", "media_meta_by_source"}
	if !slices.Equal(indexes, wantIndexes) {
		t.Errorf("indexes = %q, want %q", indexes, wantIndexes)
	}
	// Rebuilt references still name the renamed tables.
	var schema string
	if err := s.Reader().QueryRow(`SELECT group_concat(sql, ' ') FROM sqlite_schema WHERE type = 'table'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(schema, "_v8") || strings.Contains(schema, "_v7") {
		t.Errorf("the schema still names a rebuilt table's temporary name")
	}
	// Every foreign key into entries, actions, items, and checks is
	// searched through an index, and so is every new one into sources, so
	// deleting a row checks its references by index. (jobs.source_id and
	// review_rows.source_id predate R5 and are left as they are.)
	assertForeignKeysIndexed(t, s, "entries", 12)
	assertForeignKeysIndexed(t, s, "sources", 3, "actions", "media_cameras", "media_sources")
	assertForeignKeysIndexed(t, s, "actions", 2)
	assertForeignKeysIndexed(t, s, "action_items", 2)
	assertForeignKeysIndexed(t, s, "purge_checks", 3)
}

// Every CHECK 0008 adds or widens rejects a bad value; the good values are
// accepted.
func TestMediaChecksRejectBadValues(t *testing.T) {
	s, _ := openTemp(t, Options{})
	insertSource(t, s, "fotos")
	root := insertEntry(t, s, "fotos", nil, []byte{}, []byte{}, "directory")
	n := 0
	entry := func() int64 {
		n++
		name := []byte{'a' + byte(n/26), 'a' + byte(n%26)}
		return insertEntry(t, s, "fotos", root, name, name, "file")
	}
	write := func(q string, args ...any) error {
		return s.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(q, args...)
			return err
		})
	}
	const actionSQL = `INSERT INTO actions (kind, source_id, state, bulk, template, rename, created_at)
		VALUES (?, 'fotos', 'planned', 1, ?, ?, 1)`
	const itemSQL = `INSERT INTO action_items (action_id, seq, op, state, new_mtime_ns, prev_mtime_ns, copy_of)
		VALUES (?, ?, ?, 'planned', ?, ?, ?)`
	const metaSQL = `INSERT INTO media_meta (entry_id, source_id, state, size, capture_local, capture_offset_min,
		gps_ns, make) VALUES (?, 'fotos', ?, 1, ?, ?, ?, ?)`
	const datesSQL = `INSERT INTO media_dates (entry_id, source_id, effective_ns, local, precision, source, confidence,
		refined, corrected, flags, meta_state, inputs_key, computed_at) VALUES (?, 'fotos', ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1)`
	const correctionSQL = `INSERT INTO date_corrections (entry_id, kind, set_local, set_offset_min, shift_s, batch_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'b', 1)`
	const cameraSQL = `INSERT INTO media_cameras (source_id, camera_key, photos, state, suggested_shift_s, basis, computed_at)
		VALUES ('fotos', ?, 1, ?, ?, ?, 1)`
	const sourceSQL = `INSERT INTO media_sources (source_id, dirty, summary) VALUES (?, ?, ?)`

	for _, kind := range []string{"move", "rename", "create_folder", "rescue", "merge", "undo", "cleanup", "restore",
		"purge", "set_mtime", "date_organize"} {
		if err := write(actionSQL, kind, nil, 0); err != nil {
			t.Fatalf("action kind %s: %v", kind, err)
		}
	}
	if err := write(actionSQL, "date_organize", "{year}/{month}", 1); err != nil {
		t.Fatalf("date organize with template and rename: %v", err)
	}
	action := lastID(t, mustWrite(t, s, actionSQL, "set_mtime", nil, 0))
	seq := 0
	item := func(op string, newNs, prevNs, copyOf any) error {
		seq++
		return write(itemSQL, action, seq, op, newNs, prevNs, copyOf)
	}
	for _, op := range []string{"rename", "mkdir", "rmdir", "record", "unlink", "purge", "verify"} {
		if err := item(op, nil, nil, nil); err != nil {
			t.Fatalf("item op %s: %v", op, err)
		}
	}
	if err := item("set_mtime", 1_000, nil, nil); err != nil {
		t.Fatalf("set_mtime item: %v", err)
	}
	if err := item("set_mtime", 1_000, 999, nil); err != nil {
		t.Fatalf("journaled set_mtime item: %v", err)
	}
	if err := item("rename", nil, nil, entry()); err != nil {
		t.Fatalf("item copy_of: %v", err)
	}

	for _, state := range []string{"pending", "none", "unreadable"} {
		if err := write(metaSQL, entry(), state, nil, nil, nil, nil); err != nil {
			t.Fatalf("meta state %s: %v", state, err)
		}
	}
	for _, off := range []int{-840, 0, 840} {
		if err := write(metaSQL, entry(), "read", "2010-07-17T10:00:00", off, 1, "Canon"); err != nil {
			t.Fatalf("meta read offset %d: %v", off, err)
		}
	}

	type dates struct {
		eff                                  any
		local, prec, source, conf, corrected any
		refined, flags                       int
		meta                                 string
	}
	goodDates := []dates{
		{1, "2010", "year", "folder_name", "low", nil, 0, 0, "pending"},
		{1, "2010-07", "month", "file_name", "medium", "use_name", 1, 0, "none"},
		{1, "2010-07-17", "day", "owner", "high", "set", 0, 15, "read"},
		{1, "2010-07-17T10:00:00", "second", "exif", "high", nil, 0, 7, "unreadable"},
		{1, "2010-07-17T10:00:00", "second", "gps", "high", "shift", 0, 8, "none"},
		{1, "2010-07-17T10:00:00", "second", "container", "medium", "use_folder", 0, 0, "read"},
		{1, "2010-07-17T10:00:00", "second", "mtime", "lowest", nil, 0, 0, "read"},
		{nil, nil, nil, "none", "none", nil, 0, 8, "read"},
	}
	for _, d := range goodDates {
		if err := write(datesSQL, entry(), d.eff, d.local, d.prec, d.source, d.conf, d.refined, d.corrected, d.flags, d.meta); err != nil {
			t.Fatalf("dates %+v: %v", d, err)
		}
	}

	for _, c := range []struct {
		kind              string
		local, off, shift any
	}{
		{"set", "1978", nil, nil},
		{"set", "1978-05", nil, nil},
		{"set", "1978-05-01", nil, nil},
		{"set", "1978-05-01T10:00:00", nil, nil},
		{"set", "1978-05-01T10:00:00", -180, nil},
		{"shift", nil, nil, 31546800},
		{"shift", nil, nil, -1577880000},
		{"use_name", nil, nil, nil},
		{"use_folder", nil, nil, nil},
	} {
		if err := write(correctionSQL, entry(), c.kind, c.local, c.off, c.shift); err != nil {
			t.Fatalf("correction %+v: %v", c, err)
		}
	}

	for i, state := range []string{"ok", "offset", "disagrees"} {
		var shift any
		if state == "offset" {
			shift = 31546800
		}
		if err := write(cameraSQL, "cam"+string(rune('0'+i)), state, shift, `{"events":[]}`); err != nil {
			t.Fatalf("camera state %s: %v", state, err)
		}
	}
	for _, dirty := range []int{0, 1} {
		src := "media" + string(rune('0'+dirty))
		insertSource(t, s, src)
		if err := write(sourceSQL, src, dirty, `{}`); err != nil {
			t.Fatalf("media source dirty %d: %v", dirty, err)
		}
	}
	insertSource(t, s, "spare")

	for _, bad := range []struct {
		name string
		err  func() error
	}{
		{"action kind", func() error { return write(actionSQL, "set_date", nil, 0) }},
		{"action rename 2", func() error { return write(actionSQL, "date_organize", nil, 2) }},
		{"item op", func() error { return item("touch", nil, nil, nil) }},
		{"set_mtime without new_mtime_ns", func() error { return item("set_mtime", nil, nil, nil) }},
		{"new_mtime_ns on a rename", func() error { return item("rename", 1_000, nil, nil) }},
		{"prev_mtime_ns on a rename", func() error { return item("rename", nil, 999, nil) }},
		{"meta state", func() error { return write(metaSQL, entry(), "changed", nil, nil, nil, nil) }},
		{"meta capture on pending", func() error {
			return write(metaSQL, entry(), "pending", "2010-07-17T10:00:00", nil, nil, nil)
		}},
		{"meta gps on none", func() error { return write(metaSQL, entry(), "none", nil, nil, 1, nil) }},
		{"meta make on unreadable", func() error { return write(metaSQL, entry(), "unreadable", nil, nil, nil, "Canon") }},
		{"meta offset 841", func() error { return write(metaSQL, entry(), "read", "2010-07-17T10:00:00", 841, nil, nil) }},
		{"dates precision", func() error {
			return write(datesSQL, entry(), 1, "2010", "week", "exif", "high", 0, nil, 0, "read")
		}},
		{"dates source", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "xmp", "high", 0, nil, 0, "read")
		}},
		{"dates confidence", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "exif", "certain", 0, nil, 0, "read")
		}},
		{"dates refined 2", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "exif", "high", 2, nil, 0, "read")
		}},
		{"dates corrected", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "owner", "high", 0, "guess", 0, "read")
		}},
		{"dates flags 16", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "exif", "high", 0, nil, 16, "read")
		}},
		{"dates flags -1", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "exif", "high", 0, nil, -1, "read")
		}},
		{"dates flag 8 with pending", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "folder_name", "low", 0, nil, 8, "pending")
		}},
		{"dates flag 8 with unreadable", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "folder_name", "low", 0, nil, 9, "unreadable")
		}},
		{"dates meta state", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "exif", "high", 0, nil, 0, "changed")
		}},
		{"dates none with a date", func() error {
			return write(datesSQL, entry(), 1, "2010", "year", "none", "none", 0, nil, 0, "read")
		}},
		{"dates exif without a date", func() error {
			return write(datesSQL, entry(), nil, nil, nil, "exif", "high", 0, nil, 0, "read")
		}},
		{"dates without precision", func() error {
			return write(datesSQL, entry(), 1, "2010", nil, "exif", "high", 0, nil, 0, "read")
		}},
		{"dates without local", func() error {
			return write(datesSQL, entry(), 1, nil, "year", "exif", "high", 0, nil, 0, "read")
		}},
		{"correction kind", func() error { return write(correctionSQL, entry(), "guess", nil, nil, nil) }},
		{"set without set_local", func() error { return write(correctionSQL, entry(), "set", nil, nil, nil) }},
		{"set_local on use_name", func() error { return write(correctionSQL, entry(), "use_name", "2010", nil, nil) }},
		{"set_local of length 8", func() error { return write(correctionSQL, entry(), "set", "2010-07-", nil, nil) }},
		{"offset on a date-only set", func() error { return write(correctionSQL, entry(), "set", "2010-07-17", -180, nil) }},
		{"set offset 841", func() error {
			return write(correctionSQL, entry(), "set", "2010-07-17T10:00:00", 841, nil)
		}},
		{"shift without shift_s", func() error { return write(correctionSQL, entry(), "shift", nil, nil, nil) }},
		{"shift_s on a set", func() error { return write(correctionSQL, entry(), "set", "2010", nil, 60) }},
		{"shift beyond 50 years", func() error { return write(correctionSQL, entry(), "shift", nil, nil, 1577880001) }},
		{"camera state", func() error { return write(cameraSQL, "x", "wrong", nil, `{}`) }},
		{"offset without a suggestion", func() error { return write(cameraSQL, "y", "offset", nil, `{}`) }},
		{"a suggestion on ok", func() error { return write(cameraSQL, "z", "ok", 60, `{}`) }},
		{"camera basis not JSON", func() error { return write(cameraSQL, "w", "ok", nil, `{`) }},
		{"dirty 2", func() error { return write(sourceSQL, "spare", 2, `{}`) }},
		{"summary not JSON", func() error { return write(sourceSQL, "spare", 0, `x`) }},
	} {
		t.Run(bad.name, func(t *testing.T) {
			wantConstraint(t, bad.err(), sqlite3.SQLITE_CONSTRAINT_CHECK)
		})
	}
	// A copy_of must name an entry, a camera is listed once per source, and
	// media state belongs to an existing source.
	wantConstraint(t, item("rename", nil, nil, 99999), sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
	wantConstraint(t, write(cameraSQL, "cam0", "ok", nil, `{}`), sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
	wantConstraint(t, write(sourceSQL, "nowhere", 0, `{}`), sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
	wantConstraint(t, write(correctionSQL, 99999, "use_name", nil, nil, nil), sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
}

// Removing an entry removes its metadata, dates, and correction and clears
// an item's copy_of; removing a source removes its cameras and media state
// (and, through its entries, their media rows), and leaves another source's
// alone.
func TestMediaCascades(t *testing.T) {
	s, _ := openTemp(t, Options{})
	type fixture struct {
		photo, copy, item int64
	}
	setup := func(src string) fixture {
		insertSource(t, s, src)
		root := insertEntry(t, s, src, nil, []byte{}, []byte{}, "directory")
		var f fixture
		f.photo = insertEntry(t, s, src, root, []byte("a.jpg"), []byte("a.jpg"), "file")
		f.copy = insertEntry(t, s, src, root, []byte("b.jpg"), []byte("b.jpg"), "file")
		for _, e := range []int64{f.photo, f.copy} {
			mustWrite(t, s, `INSERT INTO media_meta (entry_id, source_id, state, size) VALUES (?, ?, 'pending', 1)`, e, src)
			mustWrite(t, s, `INSERT INTO media_dates (entry_id, source_id, source, confidence, meta_state, inputs_key,
				computed_at) VALUES (?, ?, 'none', 'none', 'pending', 1, 1)`, e, src)
			mustWrite(t, s, `INSERT INTO date_corrections (entry_id, kind, batch_id, created_at)
				VALUES (?, 'use_name', 'b', 1)`, e)
		}
		mustWrite(t, s, `INSERT INTO media_cameras (source_id, camera_key, photos, state, computed_at)
			VALUES (?, 'SONY|DSC-W55|', 2, 'ok', 1)`, src)
		mustWrite(t, s, `INSERT INTO media_sources (source_id, dirty) VALUES (?, 1)`, src)
		action := lastID(t, mustWrite(t, s, `INSERT INTO actions (kind, source_id, state, bulk, created_at)
			VALUES ('date_organize', ?, 'planned', 1, 1)`, src))
		f.item = lastID(t, mustWrite(t, s, `INSERT INTO action_items (action_id, seq, op, entry_id, copy_of, state, reason)
			VALUES (?, 1, 'rename', ?, ?, 'refused', 'identical_copy')`, action, f.photo, f.copy))
		return f
	}
	gone, kept := setup("gone"), setup("kept")

	mustWrite(t, s, `DELETE FROM entries WHERE id = ?`, kept.copy)
	for _, table := range []string{"media_meta", "media_dates", "date_corrections"} {
		if n := countRows(t, s, `SELECT count(*) FROM "`+table+`" WHERE entry_id = ?`, kept.copy); n != 0 {
			t.Errorf("%s kept a removed entry's row", table)
		}
		if n := countRows(t, s, `SELECT count(*) FROM "`+table+`" WHERE entry_id = ?`, kept.photo); n != 1 {
			t.Errorf("removing an entry removed another's %s row", table)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM action_items WHERE id = ? AND copy_of IS NULL AND entry_id = ?`,
		kept.item, kept.photo); n != 1 {
		t.Errorf("copy_of outlived its entry")
	}

	mustWrite(t, s, `DELETE FROM sources WHERE id = 'gone'`)
	for q, want := range map[string]int{
		`SELECT count(*) FROM media_meta`:       1,
		`SELECT count(*) FROM media_dates`:      1,
		`SELECT count(*) FROM date_corrections`: 1,
		`SELECT count(*) FROM media_cameras`:    1,
		`SELECT count(*) FROM media_sources`:    1,
	} {
		if n := countRows(t, s, q); n != want {
			t.Errorf("%s = %d after removing a source, want %d", q, n, want)
		}
	}
	for _, q := range []string{
		`SELECT count(*) FROM media_meta WHERE entry_id IN (?, ?)`,
		`SELECT count(*) FROM date_corrections WHERE entry_id IN (?, ?)`,
	} {
		if n := countRows(t, s, q, gone.photo, gone.copy); n != 0 {
			t.Errorf("%s: %d rows of the removed source's entries", q, n)
		}
	}
	if n := countRows(t, s, `SELECT count(*) FROM media_cameras WHERE source_id = 'gone'`); n != 0 {
		t.Errorf("removing the source left its cameras")
	}
	if n := countRows(t, s, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d violations", n)
	}
}
