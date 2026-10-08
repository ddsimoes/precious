package content

import (
	"bytes"
	"context"
	"database/sql"
	"sync"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
)

// writable is posix with the no-replace rename a move needs.
var writable = func() fsaccess.Capabilities {
	c := posix
	c.NoReplaceRename = true
	return c
}()

// moveFolder renames the folder from, a child of the source root at
// rootPath, to to, on disk and in the index, as a done move of the
// executor leaves them (r3 design D6): the folder's row and its subtree's
// paths change, IDs and identities stay.
func (e *env) moveFolder(src domain.SourceID, rootPath, from, to string) {
	e.t.Helper()
	d, err := e.sfs.OpenRoot(rootPath)
	if err != nil {
		e.t.Fatal(err)
	}
	defer d.Close()
	w, ok := fsaccess.AsWriter(d)
	if !ok {
		e.t.Fatal("the synthetic root cannot be written")
	}
	if err := w.RenameNoReplace([]byte(from), d, []byte(to)); err != nil {
		e.t.Fatalf("rename %s to %s: %v", from, to, err)
	}
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, path FROM entries WHERE source_id = ? AND (path = ? OR (path > ? AND path < ?))`,
			string(src), []byte(from), []byte(from+"/"), []byte(from+"0"))
		if err != nil {
			return err
		}
		moved := map[int64][]byte{}
		for rows.Next() {
			var id int64
			var p []byte
			if err := rows.Scan(&id, &p); err != nil {
				rows.Close()
				return err
			}
			moved[id] = append([]byte(to), bytes.TrimPrefix(p, []byte(from))...)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for id, p := range moved {
			if _, err := tx.Exec(`UPDATE entries SET path = ? WHERE id = ?`, p, id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE entries SET name = ? WHERE source_id = ? AND path = ?`, []byte(to), string(src), []byte(to))
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// moveDuring moves the folder from to to once, before the first filesystem
// call of the hashing run under from: the walk to a file (walk), or the
// first read of an open file (read). It reports whether the move happened.
func (e *env) moveDuring(src domain.SourceID, rootPath, from, to string, read bool) func() bool {
	var (
		once  sync.Once
		moved bool
	)
	e.rec.SetBeforeCall(func(c instrument.Call) {
		if len(c.Path) == 0 || string(c.Path[0]) != from || (read && c.Op != instrument.OpReadAt) {
			return
		}
		once.Do(func() {
			e.moveFolder(src, rootPath, from, to)
			moved = true
		})
	})
	e.t.Cleanup(func() { e.rec.SetBeforeCall(nil) })
	return func() bool { return moved }
}

// Scenario "Hashing a moved folder's files" (r3 design D18): a hashing
// batch loaded before its folder moves reads the files through the old
// path, either failing to find them or reading them through a folder
// already open. Nothing it read is recorded: no file becomes changed, both
// stay pending at their new paths, and the next pass hashes them there.
func TestHashingMovedFolderFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		read bool
	}{{"moved before the walk", false}, {"moved during the read", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			root := e.disk("fotos", "/mnt/fotos", writable)
			pairOf(root.Dir("2004"), 5000, "a.jpg", "b.jpg")
			e.scan("fotos")
			e.plan()
			for _, p := range []string{"2004/a.jpg", "2004/b.jpg"} {
				if s := e.state("fotos", p); s != domain.ContentPending {
					t.Fatalf("%s is %s before hashing, want pending", p, s)
				}
			}
			ids := []domain.EntryID{e.id("fotos", "2004/a.jpg"), e.id("fotos", "2004/b.jpg")}

			moved := e.moveDuring("fotos", "/mnt/fotos", "2004", "2005", tc.read)
			if err := e.hashWith(context.Background(), "fotos", &fakeRuntime{}); err != nil {
				t.Fatal(err)
			}
			if !moved() {
				t.Fatal("the hashing run never reached the folder")
			}
			if n := e.count(`SELECT count(*) FROM file_content WHERE state IN ('changed', 'unreadable', 'hashed')`); n != 0 {
				t.Errorf("%d files were recorded from reads through the old path", n)
			}
			for i, p := range []string{"2005/a.jpg", "2005/b.jpg"} {
				if id := e.id("fotos", p); id != ids[i] {
					t.Fatalf("%s has ID %s, want %s", p, id, ids[i])
				}
				if s, d := e.state("fotos", p), e.digest("fotos", p); s != domain.ContentPending || d != "" {
					t.Errorf("%s: %s %q after the moved read; want pending without digest", p, s, d)
				}
			}

			e.rec.SetBeforeCall(nil)
			e.hash("fotos")
			for _, p := range []string{"2005/a.jpg", "2005/b.jpg"} {
				if s, d := e.state("fotos", p), e.digest("fotos", p); s != domain.ContentHashed || d == "" {
					t.Errorf("the next pass left %s %s %q; want hashed", p, s, d)
				}
			}
			e.checkCoverage()
		})
	}
}

// The archive listing's guard (r3 design D18): a zip of a folder that moves
// before its listing opens it, or while its listing reads it, is not
// recorded changed or listed (the rows of an unfinished listing stay hidden
// in state listing); the next pass lists it at its new path.
func TestListingMovedFolderArchive(t *testing.T) {
	for _, tc := range []struct {
		name string
		read bool
	}{{"moved before the walk", false}, {"moved during the read", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			root := e.disk("fotos", "/mnt/fotos", writable)
			root.Dir("2004").File("fotos.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "a.jpg", data: []byte("aaaa")}))
			e.scan("fotos")
			e.plan()
			before := e.state("fotos", "2004/fotos.zip")

			moved := e.moveDuring("fotos", "/mnt/fotos", "2004", "2005", tc.read)
			if err := e.hashWith(context.Background(), "fotos", &fakeRuntime{}); err != nil {
				t.Fatal(err)
			}
			if !moved() {
				t.Fatal("the hashing run never reached the folder")
			}
			if n := e.count(`SELECT count(*) FROM archives WHERE state <> 'listing'`); n != 0 {
				t.Errorf("%d archives recorded from a read through the old path", n)
			}
			if n := e.count(`SELECT count(*) FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
				WHERE a.state <> 'listing'`); n != 0 {
				t.Errorf("%d members recorded from a read through the old path", n)
			}
			if s := e.state("fotos", "2005/fotos.zip"); s != before {
				t.Errorf("the zip's content state went from %q to %q", before, s)
			}

			e.rec.SetBeforeCall(nil)
			e.hash("fotos")
			if a := e.archive("fotos", "2005/fotos.zip"); a.state != domain.ArchiveComplete || a.members != 1 {
				t.Errorf("the next pass left the zip %+v; want complete with its member", a)
			}
			e.checkCoverage()
		})
	}
}
