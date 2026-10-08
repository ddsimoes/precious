package organize

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
)

// r4 task 1.6: the index adapter's ApplyPurge and ApplyUnlink. Each deletes
// only rows strictly below the quarantine folder, of its source, and refolds
// the quarantine; the top folder's totals never change, and a scan of the
// disk the steps left agrees with the index.
func TestApplyPurgeAndUnlink(t *testing.T) {
	w := newWorld(t)
	q := index.QuarantineName
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var seven *synthfs.Node
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.Dir("Fotos").File("a.jpg", 1000, now)
		seven = root.Dir(q).Dir("7")
		velho := seven.Dir("1").Dir("Velho")
		velho.File("c.txt", 40, now)
		velho.Dir("sub").File("d.txt", 4, now)
		seven.Dir("2").File("a.jpg", 100, now)
		seven.Child("2").File("b.jpg", 10, now)
		seven.File("1.json", 2, now)
		seven.File("2.json", 3, now)
	})
	w.disk("other", "/other", posix, func(root *synthfs.Node) {
		root.Dir(q).Dir("7").Dir("1").File("x.jpg", 5, now)
	})
	idx := w.org.Index()
	id := func(src domain.SourceID, p string) domain.EntryID {
		n, err := strconv.ParseInt(w.id(src, p), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return domain.EntryID(n)
	}
	totals := func(p string) (bytes, files int64) {
		t.Helper()
		if err := w.st.Reader().QueryRow(`SELECT total_bytes, total_files FROM entries WHERE source_id = 'disk' AND path = ?`,
			[]byte(p)).Scan(&bytes, &files); err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		return bytes, files
	}
	apply := func(fn func(tx *sql.Tx) error) error { return w.st.Write(context.Background(), fn) }
	rows := func() int { return w.count(`SELECT count(*) FROM entries`) }
	topBytes, topFiles := totals("")
	if b, f := totals(q); b != 159 || f != 6 {
		t.Fatalf("the quarantine folds %d bytes, %d files", b, f)
	}

	n := rows()
	for name, refused := range map[string]func(tx *sql.Tx) error{
		"an entry outside the quarantine": func(tx *sql.Tx) error {
			return idx.ApplyPurge(context.Background(), tx, "disk", nil, id("disk", "Fotos/a.jpg"))
		},
		"the quarantine folder itself": func(tx *sql.Tx) error {
			return idx.ApplyPurge(context.Background(), tx, "disk", nil, id("disk", q))
		},
		"an entry of another source": func(tx *sql.Tx) error {
			return idx.ApplyPurge(context.Background(), tx, "disk",
				[]domain.EntryID{id("disk", q+"/7/2/a.jpg"), id("other", q+"/7/1/x.jpg")}, 0)
		},
		"an unlink outside the quarantine": func(tx *sql.Tx) error {
			return idx.ApplyUnlink(context.Background(), tx, "disk", []byte("Fotos/a.jpg"))
		},
		"an unlink of a folder's row": func(tx *sql.Tx) error {
			return idx.ApplyUnlink(context.Background(), tx, "disk", []byte(q+"/7/1"))
		},
	} {
		if err := apply(refused); err == nil {
			t.Errorf("%s: not refused", name)
		}
		if got := rows(); got != n {
			t.Fatalf("%s: %d rows, want %d", name, got, n)
		}
	}

	// The whole item 7/1, gone from the disk.
	seven.Remove("1")
	if err := apply(func(tx *sql.Tx) error {
		return idx.ApplyPurge(context.Background(), tx, "disk", nil, id("disk", q+"/7/1"))
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.count(`SELECT count(*) FROM entries WHERE source_id = 'disk' AND path >= ? AND path < ?`,
		[]byte(q+"/7/1/"), []byte(q+"/7/10")); got != 0 || rows() != n-5 {
		t.Errorf("after the whole purge: %d rows below 7/1, %d rows; want 0, %d", got, rows(), n-5)
	}
	if b, f := totals(q); b != 115 || f != 4 {
		t.Errorf("the quarantine folds %d bytes, %d files; want 115, 4", b, f)
	}

	// Part of item 7/2: one file, with a removed ID listed twice.
	seven.Child("2").Remove("a.jpg")
	part := id("disk", q+"/7/2/a.jpg")
	if err := apply(func(tx *sql.Tx) error {
		return idx.ApplyPurge(context.Background(), tx, "disk", []domain.EntryID{part, part}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if b, f := totals(q + "/7/2"); b != 10 || f != 1 {
		t.Errorf("7/2 folds %d bytes, %d files; want 10, 1", b, f)
	}

	// A record a scan indexed, and one it never did.
	seven.Remove("1.json")
	if err := apply(func(tx *sql.Tx) error {
		if err := idx.ApplyUnlink(context.Background(), tx, "disk", []byte(q+"/7/1.json")); err != nil {
			return err
		}
		return idx.ApplyUnlink(context.Background(), tx, "disk", []byte(q+"/7/9.json"))
	}); err != nil {
		t.Fatal(err)
	}
	if b, f := totals(q); b != 13 || f != 2 {
		t.Errorf("the quarantine folds %d bytes, %d files; want 13, 2", b, f)
	}
	if b, f := totals(""); b != topBytes || f != topFiles {
		t.Errorf("the top folds %d bytes, %d files; want %d, %d as before", b, f, topBytes, topFiles)
	}
	if w.count(`SELECT count(*) FROM entry_names WHERE rowid NOT IN (SELECT id FROM entries)`) != 0 {
		t.Errorf("name index rows outlived their entries")
	}

	// A scan of what the steps left finds the index as it is.
	before := rows()
	w.scan("disk")
	if got := rows(); got != before {
		t.Errorf("the scan after the steps changed the rows from %d to %d", before, got)
	}
	if b, f := totals(q); b != 13 || f != 2 {
		t.Errorf("after the scan, the quarantine folds %d bytes, %d files; want 13, 2", b, f)
	}
	if w.count(`SELECT count(*) FROM entries WHERE source_id = 'disk' AND state = 'missing'`) != 0 {
		t.Errorf("the scan found missing entries")
	}
}
