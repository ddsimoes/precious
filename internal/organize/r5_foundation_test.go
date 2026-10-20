package organize

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
)

// r5 task 1.6: the index adapter's ApplyModTime writes the file's new
// times, and its folders' by-year figures and newest and oldest times
// follow the written time; a scan afterwards changes nothing.
func TestApplyModTimeRefolds(t *testing.T) {
	w := newWorld(t)
	t2004 := time.Date(2004, 7, 1, 9, 0, 0, 0, time.UTC)
	t2006 := time.Date(2006, 3, 4, 10, 0, 0, 0, time.UTC)
	t2010 := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)
	root := w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		fotos := root.Dir("Fotos")
		fotos.Dir("2004").File("a.jpg", 1000, t2004)
		fotos.Dir("2006").File("b.jpg", 2000, t2006)
	})
	byYear := func(p string) string {
		t.Helper()
		var s string
		if err := w.st.Reader().QueryRow(`SELECT d.by_year FROM dir_stats d JOIN entries e ON e.id = d.entry_id
			WHERE e.source_id = 'disk' AND e.path = ?`, []byte(p)).Scan(&s); err != nil {
			t.Fatalf("by_year of %q: %v", p, err)
		}
		return s
	}
	times := func(p string) (newest, oldest int64) {
		t.Helper()
		if err := w.st.Reader().QueryRow(`SELECT newest_ns, oldest_ns FROM entries WHERE source_id = 'disk' AND path = ?`,
			[]byte(p)).Scan(&newest, &oldest); err != nil {
			t.Fatalf("times of %q: %v", p, err)
		}
		return newest, oldest
	}
	if got, want := byYear("Fotos"), `{"2004":{"files":1,"bytes":1000},"2006":{"files":1,"bytes":2000}}`; got != want {
		t.Fatalf("Fotos by year %s, want %s", got, want)
	}

	// The executor's step: the time set on disk, then the outcome.
	dir := root.Child("Fotos").Child("2004")
	opened, err := w.sfs.OpenRoot("/disk")
	if err != nil {
		t.Fatal(err)
	}
	fotosDir := openPath(t, opened, "Fotos", "2004")
	wr, ok := fsaccess.AsWriter(fotosDir)
	if !ok {
		t.Fatal("synthfs folders write")
	}
	if err := wr.SetModTime([]byte("a.jpg"), t2010); err != nil {
		t.Fatal(err)
	}
	fotosDir.Close()
	info := dir.Child("a.jpg").Info()
	id, err := strconv.ParseInt(w.id("disk", "Fotos/2004/a.jpg"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	m := index.ModTime{Source: "disk", Entry: domain.EntryID(id), Facts: index.PostFacts{Dev: info.Dev, Ino: info.Ino,
		MtimeNs: info.ModTime.UnixNano(), CtimeNs: info.Ctime.UnixNano()}}
	if err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
		return w.org.Index().ApplyModTime(context.Background(), tx, m)
	}); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]string{
		"Fotos/2004": `{"2010":{"files":1,"bytes":1000}}`,
		"Fotos":      `{"2006":{"files":1,"bytes":2000},"2010":{"files":1,"bytes":1000}}`,
		"":           `{"2006":{"files":1,"bytes":2000},"2010":{"files":1,"bytes":1000}}`,
	} {
		if got := byYear(p); got != want {
			t.Errorf("%q by year %s, want %s", p, got, want)
		}
	}
	for p, want := range map[string][2]time.Time{
		"Fotos/2004/a.jpg": {t2010, t2010},
		"Fotos/2004":       {t2010, t2010},
		"Fotos":            {t2010, t2006},
	} {
		if n, o := times(p); n != want[0].UnixNano() || o != want[1].UnixNano() {
			t.Errorf("%q newest, oldest %v, %v; want %v, %v", p, time.Unix(0, n).UTC(), time.Unix(0, o).UTC(), want[0], want[1])
		}
	}

	// A scan of the disk finds the index as the outcome left it.
	before := map[string]string{"": byYear(""), "Fotos": byYear("Fotos"), "Fotos/2004": byYear("Fotos/2004")}
	w.scan("disk")
	for p, want := range before {
		if got := byYear(p); got != want {
			t.Errorf("after the scan, %q by year %s, want %s", p, got, want)
		}
	}
	if n := w.count(`SELECT count(*) FROM entries WHERE source_id = 'disk' AND state <> 'present'`); n != 0 {
		t.Errorf("the scan found %d entries changed state", n)
	}
}

// openPath opens the folders names below dir, closing dir.
func openPath(t *testing.T, dir fsaccess.Dir, names ...string) fsaccess.Dir {
	t.Helper()
	for _, name := range names {
		info, err := dir.Lstat([]byte(name))
		if err != nil {
			t.Fatal(err)
		}
		next, err := dir.OpenDir([]byte(name), info)
		dir.Close()
		if err != nil {
			t.Fatal(err)
		}
		dir = next
	}
	return dir
}
