package relations

import (
	"context"
	"database/sql"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/rules"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// R5c task 2.1: the places of folders and complete archives (design D1,
// D3, D4, D5, D14), each case checked on the stored entry_places row. The
// worlds use three sources: disk and usb online, old offline.

// policyClassifier classifies members with the default rules, as serve
// wires it.
func policyClassifier() classifier {
	pol := rules.Default()
	return func(name []byte, size int64) (domain.Category, domain.FileKind, domain.Triage) {
		kind := pol.FileKind(name)
		res := pol.ClassifyFile(rules.FileFacts{Name: name, Kind: kind, Size: size})
		return res.Category, kind, res.Triage
	}
}

// storedPlaces reads every entry_places row.
func storedPlaces(t testing.TB, st *store.Store) map[int64]domain.PlaceSums {
	t.Helper()
	out := map[int64]domain.PlaceSums{}
	var id int64
	var p domain.PlaceSums
	dest := []any{&id}
	for _, f := range entryPlacesTable.fields(&p) {
		dest = append(dest, f)
	}
	err := scanAll(context.Background(), st.Reader(), `SELECT entry_id, only_copy_files, only_copy_bytes,
		this_source_files, this_source_bytes, other_source_files, other_source_bytes, unchecked_files, unchecked_bytes,
		unreadable_files, unreadable_bytes, disposable_files, disposable_bytes FROM entry_places`, nil,
		func(r *sql.Rows) error {
			if err := r.Scan(dest...); err != nil {
				return err
			}
			out[id] = p
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// places seeds w, takes source "old" offline when it has one, runs a
// relate pass with the policy's member classifier, and returns the stored
// rows.
func (w *world) places(t *testing.T) placeRows {
	t.Helper()
	w.seed()
	if _, ok := w.seeded["old"]; ok {
		if _, err := w.st.Writer().Exec(`UPDATE sources SET state = 'offline' WHERE id = 'old'`); err != nil {
			t.Fatal(err)
		}
	}
	h := w.handler(nil)
	h.SetClassifier(policyClassifier())
	runJob(t, h)
	return placeRows{w: w, rows: storedPlaces(t, w.st)}
}

type placeRows struct {
	w    *world
	rows map[int64]domain.PlaceSums
}

// want fails unless the row of "source:path" is p.
func (r placeRows) want(t *testing.T, path string, p domain.PlaceSums) {
	t.Helper()
	got, ok := r.rows[int64(r.w.id(path))]
	if !ok {
		t.Errorf("%s: no entry_places row", path)
		return
	}
	if got != p {
		t.Errorf("%s: places %+v, want %+v", path, got, p)
	}
}

func pa(files, bytes int64) domain.PlaceAmount { return domain.PlaceAmount{Files: files, Bytes: bytes} }

func only(files, bytes int64) domain.PlaceSums {
	return domain.PlaceSums{OnlyCopy: pa(files, bytes)}
}

func this(files, bytes int64) domain.PlaceSums {
	return domain.PlaceSums{ThisSource: pa(files, bytes)}
}

func other(files, bytes int64) domain.PlaceSums {
	return domain.PlaceSums{OtherSource: pa(files, bytes)}
}

const gib = int64(1) << 30

// The relate pass's SQL for D14 agrees with domain.IsDisposable on every
// category, file kind, and triage, absent (NULL) and unknown values too.
func TestDisposableSQLAgreesWithTheDomain(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t)
	q, err := st.Reader().Prepare(`SELECT ` + disposableE + ` FROM (SELECT ? AS category, ? AS file_kind, ? AS triage) e`)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	cats := append([]domain.Category{"", "bogus"}, domain.Categories...)
	kinds := append([]domain.FileKind{"", "bogus"}, domain.FileKinds...)
	triages := append([]domain.Triage{"", "bogus"}, domain.Triages...)
	n := 0
	for _, c := range cats {
		for _, k := range kinds {
			for _, tr := range triages {
				var got bool
				if err := q.QueryRow(null(string(c)), null(string(k)), null(string(tr))).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if want := domain.IsDisposable(c, k, tr); got != want {
					t.Errorf("(%q, %q, %q): SQL %v, IsDisposable %v", c, k, tr, got, want)
				}
				if got {
					n++
				}
			}
		}
	}
	if n == 0 {
		t.Error("no combination is disposable")
	}
}

func TestPlacesOfSingleFiles(t *testing.T) {
	t.Parallel()
	t.Run("only copy by size", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("a/x.txt", 100, "")
		w.add(wf{path: "b/s.bin", size: 300, state: domain.ContentSampled})
		r := w.places(t)
		r.want(t, "a", only(1, 100))
		r.want(t, "b", only(1, 300))
	})
	t.Run("copies on the same source", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("a/x.txt", 100, "x")
		w.file("b/x.txt", 100, "x")
		r := w.places(t)
		r.want(t, "a", this(1, 100))
		r.want(t, "", this(2, 200))
	})
	t.Run("a copy inside an archive on the same source", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("a/x.txt", 100, "x")
		w.archive(wf{path: "z/p.zip", size: 80}, mem("d/x.txt", 100, "x"))
		r := w.places(t)
		r.want(t, "a", this(1, 100))
		r.want(t, "z/p.zip", this(1, 80))
		r.want(t, "z", this(1, 80))
		// The top, a, z, and the archive: the member folder d has none.
		if len(r.rows) != 4 {
			t.Errorf("%d rows, want 4: %+v", len(r.rows), r.rows)
		}
	})
	t.Run("a hard link that is not a copy", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.add(wf{path: "a/x.txt", size: 100, content: "x", ino: 7})
		w.add(wf{path: "b/x.txt", size: 100, content: "x", ino: 7})
		r := w.places(t)
		r.want(t, "a", only(1, 100))
		r.want(t, "", only(2, 200))
	})
	t.Run("hard-linked archive names", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		members := []indextest.Member{mem("a.txt", 100, "a"), mem("b.txt", 200, "b")}
		w.archive(wf{path: "daily.0/bkp.zip", size: 250, content: "bkp", ino: 9}, members...)
		w.archive(wf{path: "daily.1/bkp.zip", size: 250, content: "bkp", ino: 9}, members...)
		r := w.places(t)
		r.want(t, "daily.0/bkp.zip", only(1, 250))
		r.want(t, "daily.1", only(1, 250))
	})
	t.Run("two sources with the same dev and ino", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.add(wf{path: "a/x.txt", size: 100, content: "x", ino: 5})
		w.add(wf{path: "usb:a/x.txt", size: 100, content: "x", ino: 5})
		r := w.places(t)
		r.want(t, "a", other(1, 100))
		r.want(t, "usb:a", other(1, 100))
	})
	t.Run("a tar link member and its target are one copy", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archives = append(w.archives, warc{file: wf{path: "t.tar", size: 450}, format: domain.ArchiveTar,
			members: []indextest.Member{mem("orig/f.bin", 300, "f"), mem("copy/g.bin", 300, "f")}})
		w.seed()
		if _, err := w.st.Writer().Exec(`UPDATE archive_members SET link_member = ? WHERE id = ?`,
			int64(w.ref("t.tar!orig/f.bin").Member), int64(w.ref("t.tar!copy/g.bin").Member)); err != nil {
			t.Fatal(err)
		}
		r := w.places(t)
		r.want(t, "t.tar", only(1, 450))
	})
	t.Run("a copy on an offline source", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("a/x.txt", 100, "x")
		w.file("old:b/x.txt", 100, "x")
		r := w.places(t)
		r.want(t, "a", other(1, 100))
		r.want(t, "old:b", other(1, 100))
	})
	t.Run("a quarantined copy is no copy", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("a/x.txt", 100, "x")
		w.file(quarantineName+"/1/1/x.txt", 100, "x")
		w.archive(wf{path: quarantineName + "/1/2/x.zip", size: 90}, mem("x.txt", 100, "x"))
		r := w.places(t)
		r.want(t, "a", only(1, 100))
		r.want(t, "", only(1, 100))
	})
	t.Run("not checked or unreadable", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.add(wf{path: "a/new.txt", size: 100, state: "none"})
		w.add(wf{path: "a/pending.txt", size: 200, state: domain.ContentPending})
		w.add(wf{path: "a/changed.txt", size: 300, state: domain.ContentChanged})
		w.add(wf{path: "b/bad.txt", size: 400, state: domain.ContentUnreadable})
		w.add(wf{path: "b/empty.txt"})
		w.add(wf{path: "b/link", kind: domain.EntrySymlink, link: "x"})
		r := w.places(t)
		r.want(t, "a", domain.PlaceSums{Unchecked: pa(3, 600)})
		r.want(t, "b", domain.PlaceSums{Unreadable: pa(1, 400)})
	})
	t.Run("an archive member with a copy on another source", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "z/p.zip", size: 90}, mem("m.txt", 100, "m"))
		w.file("usb:c/m.txt", 100, "m")
		r := w.places(t)
		r.want(t, "z/p.zip", other(1, 90))
		r.want(t, "usb:c", other(1, 100))
	})
}

func TestPlacesOfArchives(t *testing.T) {
	t.Parallel()
	t.Run("a backup beside its folder", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("fotos/a.jpg", 100, "a")
		w.file("fotos/b.jpg", 200, "b")
		w.archive(wf{path: "bkp.zip", size: 250}, mem("fotos/a.jpg", 100, "a"), mem("fotos/b.jpg", 200, "b"))
		r := w.places(t)
		r.want(t, "bkp.zip", this(1, 250))
		r.want(t, "fotos", this(2, 300))
		r.want(t, "", this(3, 550))
	})
	t.Run("the same archive on two sources", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		members := []indextest.Member{mem("a.txt", 100, "a"), mem("b.txt", 200, "b")}
		w.archive(wf{path: "z/p.zip", size: 250, content: "p"}, members...)
		w.archive(wf{path: "usb:y/p.zip", size: 250, content: "p"}, members...)
		r := w.places(t)
		r.want(t, "z/p.zip", other(1, 250))
		r.want(t, "usb:y", other(1, 250))
	})
	t.Run("a copy of the archive file covers its unique members", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "z/p.zip", size: 250, content: "p"}, mem("a.txt", 100, ""), mem("b.txt", 200, ""))
		w.file("usb:y/p.zip", 250, "p")
		w.archive(wf{path: "q/p.zip", size: 400, content: "q"}, mem("a.txt", 100, ""), mem("c.txt", 300, "c"))
		w.file("q2/q.zip", 400, "q")
		w.file("usb:c.txt", 300, "c")
		r := w.places(t)
		r.want(t, "z/p.zip", other(1, 250))
		// this_source coverage turns only_copy members into this_source,
		// and leaves a member with a copy on another source as it is.
		r.want(t, "q/p.zip", domain.PlaceSums{ThisSource: pa(1, 100), OtherSource: pa(0, 300)})
	})
	t.Run("one unique member", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "z/p.zip", size: 400}, mem("u.txt", 100, ""), mem("a.txt", 300, "a"))
		w.file("usb:a.txt", 300, "a")
		r := w.places(t)
		r.want(t, "z/p.zip", domain.PlaceSums{OnlyCopy: pa(1, 100), OtherSource: pa(0, 300)})
		r.want(t, "z", domain.PlaceSums{OnlyCopy: pa(1, 100), OtherSource: pa(0, 300)})
	})
	t.Run("a 5 GiB archive", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "z/big.zip", size: 5 * gib}, mem("u.bin", 3*gib, ""), mem("a.bin", 3*gib, "a"))
		w.file("usb:a.bin", 3*gib, "a")
		w.file("z/x.bin", 10, "")
		r := w.places(t)
		r.want(t, "z/big.zip", domain.PlaceSums{OnlyCopy: pa(1, 5*gib/2), OtherSource: pa(0, 5*gib/2)})
		r.want(t, "z", domain.PlaceSums{OnlyCopy: pa(2, 5*gib/2+10), OtherSource: pa(0, 5*gib/2)})
	})
	t.Run("a highly compressed archive", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "z/zeros.zip", size: 1000}, mem("zeros.bin", gib, ""), mem("a.txt", 1, "a"))
		w.file("usb:a.txt", 1, "a")
		r := w.places(t)
		r.want(t, "z/zeros.zip", domain.PlaceSums{OnlyCopy: pa(1, 999), OtherSource: pa(0, 1)})
	})
	t.Run("an archive not completely listed is a file", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archives = append(w.archives, warc{file: wf{path: "p/y.zip", size: 260, content: "y"},
			format: domain.ArchiveZip, state: domain.ArchivePartial,
			members: []indextest.Member{mem("u.txt", 100, "")}})
		w.file("usb:q/y.zip", 260, "y")
		r := w.places(t)
		r.want(t, "p", other(1, 260))
		if _, ok := r.rows[int64(w.id("p/y.zip"))]; ok {
			t.Error("an archive not completely listed has a row")
		}
	})
	t.Run("an archive whose members have no bytes", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "e/empty.zip", size: 22, content: "e"}, mem("a.txt", 0, ""), mem("b.txt", 0, ""))
		w.file("usb:e.zip", 22, "e")
		w.archive(wf{path: "f/empty.zip", size: 30}, indextest.Member{Path: "d", Kind: domain.MemberDirectory})
		r := w.places(t)
		r.want(t, "e/empty.zip", other(1, 22))
		r.want(t, "f/empty.zip", only(1, 30))
	})
	t.Run("a nested archive member is a file", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "z/outer.zip", size: 500}, mem("inner.zip", 600, "inner"))
		w.file("x/inner.zip", 600, "inner")
		r := w.places(t)
		r.want(t, "z/outer.zip", this(1, 500))
		r.want(t, "x", this(1, 600))
	})
	t.Run("a Thumbs.db inside a zip of copied photos", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "z/fotos.zip", size: 290}, mem("a.jpg", 100, "a"), mem("b.jpg", 200, "b"),
			mem("Thumbs.db", 10, ""))
		w.file("usb:fotos/a.jpg", 100, "a")
		w.file("usb:fotos/b.jpg", 200, "b")
		r := w.places(t)
		r.want(t, "z/fotos.zip", other(1, 290))
	})
	t.Run("a disposable archive", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.archive(wf{path: "tmp/cache.zip", size: 700, category: domain.CategoryTemporaryData,
			fileKind: domain.FileKindArchive, triage: domain.TriageDiscard}, mem("u.txt", 1000, ""))
		w.file("tmp/u.txt", 5, "")
		r := w.places(t)
		r.want(t, "tmp/cache.zip", domain.PlaceSums{Disposable: pa(1, 700)})
		r.want(t, "tmp", domain.PlaceSums{OnlyCopy: pa(1, 5), Disposable: pa(1, 700)})
	})
}

func TestPlacesOfFolders(t *testing.T) {
	t.Parallel()
	t.Run("a folder with one only copy", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("f/a.jpg", 100, "a")
		w.file("f/b.jpg", 200, "b")
		w.file("f/u.doc", 50, "")
		w.file("usb:a.jpg", 100, "a")
		w.file("old:b.jpg", 200, "b")
		r := w.places(t)
		want := domain.PlaceSums{OnlyCopy: pa(1, 50), OtherSource: pa(2, 300)}
		r.want(t, "f", want)
		if p := r.rows[int64(w.id("f"))].Place(true); p != domain.PlaceOnlyCopy {
			t.Errorf("f is %s, want only_copy", p)
		}
	})
	t.Run("a folder wholly elsewhere", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("f/a.jpg", 100, "a")
		w.file("f/sub/b.jpg", 200, "b")
		w.file("usb:g/a.jpg", 100, "a")
		w.file("usb:g/b.jpg", 200, "b")
		r := w.places(t)
		r.want(t, "f", other(2, 300))
		r.want(t, "f/sub", other(1, 200))
		if p := r.rows[int64(w.id("f"))].Place(true); p != domain.PlaceOtherSource {
			t.Errorf("f is %s, want other_source", p)
		}
	})
	t.Run("an unreadable folder or a mount boundary adds nothing", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("f/a.jpg", 100, "a")
		w.file("f/u.txt", 30, "")
		w.add(wf{path: "f/locked", kind: domain.EntryDirectory, unreadable: true})
		w.add(wf{path: "f/mnt", kind: domain.EntryDirectory, mount: true})
		w.file("usb:a.jpg", 100, "a")
		r := w.places(t)
		r.want(t, "f", domain.PlaceSums{OnlyCopy: pa(1, 30), OtherSource: pa(1, 100)})
		r.want(t, "f/locked", domain.PlaceSums{})
		r.want(t, "f/mnt", domain.PlaceSums{})
	})
	t.Run("Thumbs.db beside copied photos", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("f/a.jpg", 100, "a")
		w.add(wf{path: "f/Thumbs.db", size: 10, category: domain.CategorySystemJunk, fileKind: domain.FileKindSystem,
			triage: domain.TriageDiscard})
		w.add(wf{path: "f/desktop.ini", size: 20, content: "ini", fileKind: domain.FileKindSystem})
		w.file("usb:a.jpg", 100, "a")
		r := w.places(t)
		r.want(t, "f", domain.PlaceSums{OtherSource: pa(1, 100), Disposable: pa(2, 30)})
		if p := r.rows[int64(w.id("f"))].Place(true); p != domain.PlaceOtherSource {
			t.Errorf("f is %s, want other_source", p)
		}
	})
	t.Run("a recovered chunk with triage review", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		w.file("f/a.jpg", 100, "a")
		w.add(wf{path: "f/FILE0000.CHK", size: 4096, category: domain.CategorySystemJunk,
			fileKind: domain.FileKindSystem, triage: domain.TriageReview})
		w.file("usb:a.jpg", 100, "a")
		r := w.places(t)
		r.want(t, "f", domain.PlaceSums{OnlyCopy: pa(1, 4096), OtherSource: pa(1, 100)})
	})
}
