package index

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/rules"
)

// node returns the synthfs node at the '/'-joined path below root.
func node(t *testing.T, root *synthfs.Node, path string) *synthfs.Node {
	t.Helper()
	n := root
	for _, name := range strings.Split(path, "/") {
		if n = n.Child(name); n == nil {
			t.Fatalf("no synthfs node %q", path)
		}
	}
	return n
}

// diffTree is a small tree for rescan tests.
func diffTree(e *env, caps fsaccess.Capabilities) *synthfs.Node {
	root := e.disk("disk", "/src/disk", caps)
	docs := root.Dir("docs")
	docs.File("a.txt", 100, mtime)
	docs.File("b.txt", 200, mtime)
	docs.File("c.txt", 300, mtime)
	inner := root.Dir("sub").Dir("inner")
	inner.File("x.txt", 400, mtime)
	inner.File("y.txt", 500, mtime)
	root.File("keep.txt", 600, mtime)
	return root
}

// A rescan of an unchanged tree writes no row.
func TestRescanUnchangedWritesNothing(t *testing.T) {
	e := newEnv(t)
	diffTree(e, posix)
	e.scan("disk")
	before := e.entries("disk")
	writes := e.writes()
	e.scan("disk")
	if n := writes(); n != 0 {
		t.Errorf("unchanged rescan wrote %d rows", n)
	}
	if e.sourceGen("disk") != 2 {
		t.Errorf("scan_gen %d, want 2", e.sourceGen("disk"))
	}
	for p, r := range e.entries("disk") {
		if r != before[p] {
			t.Errorf("%q changed:\n %+v\n %+v", p, before[p], r)
		}
	}
}

// A rescan updates a changed file, inserts a new one, marks a deleted file
// and a deleted folder's subtree missing, replaces an entry whose kind
// changed, and brings a returning file back under its old ID.
func TestRescanDiff(t *testing.T) {
	e := newEnv(t)
	root := diffTree(e, posix)
	e.scan("disk")
	first := e.entries("disk")

	docs := node(t, root, "docs")
	node(t, root, "docs/a.txt").Size(150)
	docs.File("d.txt", 50, mtime)
	docs.Remove("b.txt")
	docs.Remove("c.txt")
	docs.Dir("c.txt").File("z.txt", 7, mtime)
	root.Remove("sub")
	e.scan("disk")
	second := e.entries("disk")

	a := get(t, second, "docs/a.txt")
	if a.ID != first["docs/a.txt"].ID || a.Size != 150 || a.ScanGen != 2 {
		t.Errorf("changed file: id %d size %d gen %d", a.ID, a.Size, a.ScanGen)
	}
	if d := get(t, second, "docs/d.txt"); d.State != "present" || d.ScanGen != 2 || d.FTS != 1 {
		t.Errorf("new file %+v", d)
	}
	for _, p := range []string{"docs/b.txt", "sub", "sub/inner", "sub/inner/x.txt", "sub/inner/y.txt"} {
		r := get(t, second, p)
		if r.State != "missing" || r.MissingSince.Int64 != testNow.UnixMilli() || r.ID != first[p].ID || r.FTS != 1 {
			t.Errorf("%s: state %s since %v id %d, want missing with id %d", p, r.State, r.MissingSince, r.ID, first[p].ID)
		}
	}
	c := get(t, second, "docs/c.txt")
	if c.Kind != "directory" || c.ID == first["docs/c.txt"].ID || c.TotalBytes != 7 {
		t.Errorf("kind change: %+v, old id %d", c, first["docs/c.txt"].ID)
	}
	var oldNames int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM entry_names WHERE rowid = ?`, first["docs/c.txt"].ID).Scan(&oldNames); err != nil {
		t.Fatal(err)
	}
	if oldNames != 0 {
		t.Error("the replaced entry kept its FTS row")
	}
	if k := get(t, second, "keep.txt"); k.ScanGen != 1 {
		t.Errorf("unchanged file rewritten (gen %d)", k.ScanGen)
	}
	if docsRow := get(t, second, "docs"); docsRow.TotalBytes != 150+50+7 || docsRow.TotalFiles != 3 {
		t.Errorf("docs totals %d/%d", docsRow.TotalBytes, docsRow.TotalFiles)
	}
	if r := get(t, second, ""); r.TotalBytes != 150+50+7+600 || r.Dirs.Int64 != 2 || r.Partial {
		t.Errorf("root totals %d bytes %d dirs partial %v", r.TotalBytes, r.Dirs.Int64, r.Partial)
	}

	docs.File("b.txt", 210, mtime)
	root.Dir("sub").File("w.txt", 1, mtime)
	e.scan("disk")
	third := e.entries("disk")
	for _, p := range []string{"docs/b.txt", "sub"} {
		r := get(t, third, p)
		if r.State != "present" || r.MissingSince.Valid || r.ID != first[p].ID {
			t.Errorf("returning %s: %+v, want present with id %d", p, r, first[p].ID)
		}
	}
	if r := get(t, third, "sub/inner"); r.State != "missing" {
		t.Errorf("sub/inner came back: %s", r.State)
	}
	if r := get(t, third, "docs/b.txt"); r.Size != 210 {
		t.Errorf("returning b.txt size %d", r.Size)
	}
}

// A listing that fails keeps the folder's stored children as they were,
// marks it unreadable and its ancestors partial; a folder that cannot be
// opened does the same.
func TestRescanFailedListingKeepsChildren(t *testing.T) {
	e := newEnv(t)
	root := diffTree(e, posix)
	e.scan("disk")
	first := e.entries("disk")
	node(t, root, "docs").FailListingAfter(1, domain.OutcomeUnreadable)
	node(t, root, "sub/inner").Unreadable()
	e.scan("disk")
	rows := e.entries("disk")
	for _, p := range []string{"docs", "sub/inner"} {
		if r := get(t, rows, p); r.State != "unreadable" || r.ID != first[p].ID || r.Partial {
			t.Errorf("%s: state %s partial %v", p, r.State, r.Partial)
		}
	}
	for _, p := range []string{"docs/a.txt", "docs/b.txt", "docs/c.txt", "sub/inner/x.txt", "sub/inner/y.txt"} {
		if r := get(t, rows, p); r.State != "present" || r.ID != first[p].ID {
			t.Errorf("%s: state %s", p, r.State)
		}
	}
	for _, p := range []string{"", "sub"} {
		if r := get(t, rows, p); !r.Partial {
			t.Errorf("%q is not partial", p)
		}
	}
	if r := get(t, rows, ""); r.Unreadable.Int64 != 2 {
		t.Errorf("root unreadable count %d", r.Unreadable.Int64)
	}
}

// A scan cancelled mid-tree keeps what it wrote, marks the folders it had
// not finished partial, and does not record a scan; the next scan walks from
// the root again and completes the index.
func TestCancelMidTreeThenFullRescan(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/src/disk", posix)
	root.Dir("a").Generate(300, 6)
	root.Dir("b").Generate(300, 6)
	rec := instrument.Wrap(e.sfs)
	e.fs = rec
	e.services()
	e.cfg.BatchSize = 25

	ctx, cancel := context.WithCancel(context.Background())
	rt := &fakeRuntime{onYield: func(n int) {
		if n == 8 {
			cancel()
		}
	}}
	if err := e.scanWith(ctx, "disk", rt); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan returned %v", err)
	}
	cut := e.entries("disk")
	if len(cut) <= 1 || len(cut) >= 603 {
		t.Fatalf("cancelled scan left %d rows", len(cut))
	}
	if !get(t, cut, "").Partial || !get(t, cut, "a").Partial {
		t.Errorf("unfinished folders are not partial")
	}
	if e.sourceGen("disk") != 0 {
		t.Errorf("a cancelled scan recorded scan_gen %d", e.sourceGen("disk"))
	}

	rec.Reset()
	e.scan("disk")
	calls := rec.Calls()
	if len(calls) == 0 || calls[0].Op != instrument.OpMounts && calls[0].Op != instrument.OpOpenRoot {
		t.Errorf("rescan began with %v", calls[0])
	}
	rows := e.entries("disk")
	if len(rows) != 603 {
		t.Errorf("full rescan indexed %d rows, want 603", len(rows))
	}
	for p, r := range rows {
		if r.Partial || r.State != "present" {
			t.Errorf("%q partial %v state %s after a complete scan", p, r.Partial, r.State)
		}
	}
	compareWithSeed(t, e, "disk")

	// A cancelled rescan leaves the previous totals of the folders it did
	// not finish, marked partial.
	root.Dir("c").Generate(100, 5)
	before := get(t, e.entries("disk"), "")
	ctx, cancel = context.WithCancel(context.Background())
	rt = &fakeRuntime{onYield: func(n int) {
		if n == 3 {
			cancel()
		}
	}}
	if err := e.scanWith(ctx, "disk", rt); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled rescan returned %v", err)
	}
	after := get(t, e.entries("disk"), "")
	if after.TotalBytes != before.TotalBytes || after.TotalFiles != before.TotalFiles || !after.Partial {
		t.Errorf("root after a cancelled rescan: %d/%d partial %v, before %d/%d", after.TotalBytes,
			after.TotalFiles, after.Partial, before.TotalBytes, before.TotalFiles)
	}
	e.scan("disk")
	if r := get(t, e.entries("disk"), ""); r.Partial || r.TotalFiles <= before.TotalFiles {
		t.Errorf("root after a complete rescan: partial %v, %d files", r.Partial, r.TotalFiles)
	}
}

// Time comparison by capabilities (design D8).
func TestSameTime(t *testing.T) {
	stored := some(mtime.UnixNano())
	for _, tc := range []struct {
		name  string
		caps  fsaccess.Capabilities
		shift time.Duration
		want  bool
	}{
		{"exact", posix, 0, true},
		{"one ns on ext4", posix, time.Nanosecond, true},
		{"two ns on ext4", posix, 2 * time.Nanosecond, false},
		{"two s on FAT", fat, 2 * time.Second, true},
		{"minus two s on FAT", fat, -2 * time.Second, true},
		{"three s on FAT", fat, 3 * time.Second, false},
		{"DST forward on FAT", fat, time.Hour, true},
		{"DST back plus resolution on FAT", fat, -time.Hour - 2*time.Second, true},
		{"DST plus three s on FAT", fat, time.Hour + 3*time.Second, false},
		{"two hours on FAT", fat, 2 * time.Hour, false},
		{"an hour without local time", fsaccess.Capabilities{TimeResolution: 2 * time.Second}, time.Hour, false},
		{"an hour on ext4", posix, time.Hour, false},
	} {
		if got := sameTime(&tc.caps, stored, mtime.Add(tc.shift)); got != tc.want {
			t.Errorf("%s: sameTime = %v, want %v", tc.name, got, tc.want)
		}
	}
	if sameTime(&posix, opt{}, mtime) {
		t.Error("a NULL stored time compared equal")
	}
}

// On a filesystem with stable identity a file replaced by another object
// with the same size and time is rewritten under its ID; without stable
// identity it is not.
func TestRescanIdentityByCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caps    fsaccess.Capabilities
		rewrite bool
	}{
		{"stable", posix, true},
		{"unstable", fsaccess.Capabilities{Known: true, TimeResolution: time.Nanosecond}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			root := diffTree(e, tc.caps)
			e.scan("disk")
			old := get(t, e.entries("disk"), "keep.txt")
			root.Remove("keep.txt")
			root.File("keep.txt", 600, mtime)
			e.scan("disk")
			r := get(t, e.entries("disk"), "keep.txt")
			if r.ID != old.ID || (r.Ino != old.Ino) != tc.rewrite || (r.ScanGen == 2) != tc.rewrite {
				t.Errorf("id %d→%d ino %d→%d gen %d", old.ID, r.ID, old.Ino.Int64, r.Ino.Int64, r.ScanGen)
			}
		})
	}
}

// R1.17: on the FAT fixture, a rescan after the card is mounted again with
// times shifted by up to 2 s and by one hour (DST) and new inode numbers
// writes no row.
func TestR1_17FATRescanWithNoChangesCreatesNoEntries(t *testing.T) {
	e := newEnv(t)
	root, gt := corpus.BuildSynth(e.sfs, "/card", corpus.FATFixture())
	e.addSource("card", "/card", root, fat)
	dev := root.Info().Dev
	e.scan("card")
	first := e.entries("card")
	if len(first) != len(gt.Entries)+1 {
		t.Fatalf("indexed %d rows, ground truth %d", len(first), len(gt.Entries))
	}

	for i, g := range gt.Entries {
		if g.Size == nil {
			continue
		}
		n := node(t, root, rawPath(t, g))
		// Up to 2 s either way, as FAT's 2-second stamps round.
		n.ModTime(time.Unix(0, first[rawPath(t, g)].MTime.Int64).Add(time.Duration(i%3-1) * 2 * time.Second))
	}
	e.sfs.SetTimeZone(dev, time.FixedZone("summer", 3600))
	e.sfs.Remount(dev)
	// The card now reports every time an hour off, give or take 2 s.
	d, err := e.fs.OpenRoot("/card")
	if err != nil {
		t.Fatal(err)
	}
	info, err := d.Lstat([]byte("AUTORUN.INF"))
	d.Close()
	if err != nil {
		t.Fatal(err)
	}
	if shift := info.ModTime.UnixNano() - first["AUTORUN.INF"].MTime.Int64; abs(abs(shift)-int64(time.Hour)) > int64(2*time.Second) {
		t.Fatalf("AUTORUN.INF moved by %v, want an hour", time.Duration(shift))
	}
	writes := e.writes()
	e.scan("card")
	if n := writes(); n != 0 {
		t.Errorf("unchanged FAT rescan wrote %d rows", n)
	}
	second := e.entries("card")
	for p, r := range second {
		if r != first[p] {
			t.Errorf("%q changed:\n %+v\n %+v", p, first[p], r)
		}
	}

	// A real edit three seconds past the hour is a change.
	g := gt.Entries[len(gt.Entries)-1]
	n := node(t, root, rawPath(t, g))
	n.ModTime(time.Unix(0, first[rawPath(t, g)].MTime.Int64).Add(4 * time.Second))
	e.scan("card")
	if r := get(t, e.entries("card"), rawPath(t, g)); r.ScanGen != 3 {
		t.Errorf("a 4 s edit on FAT was not written (gen %d)", r.ScanGen)
	}
}

// R1.6: a rescan updates sizes and keeps decisions and tags: deleted files
// go missing with theirs, new files inherit their folder's decision, and a
// returning file has its old ID.
func TestR1_6RescanKeepsDecisionsAndTags(t *testing.T) {
	e := newEnv(t)
	root, _ := corpus.BuildSynth(e.sfs, "/corpus", corpus.Corpus())
	e.addSource("corpus", "/corpus", root, posix)
	e.scan("corpus")
	first := e.entries("corpus")
	id := func(p string) domain.EntryID { return domain.EntryID(get(t, first, p).ID) }

	const (
		copia     = "Fotos - Copia"
		casamento = "Fotos/2006/Casamento"
		resized   = "Fotos/2006/Casamento/DSC02101.JPG"
		thumbs    = "Fotos/2006/Praia/Thumbs.db"
		gone      = "Fotos/2007"
	)
	dec := decisions.New(fixedClock{testNow}, rules.Default(), nil)
	ctx := context.Background()
	var familia, lixo int64
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		for p, d := range map[string]domain.Decision{copia: domain.DecisionDiscard, casamento: domain.DecisionKeep,
			thumbs: domain.DecisionLater, gone + "/Formatura": domain.DecisionKeep} {
			if _, err := dec.SetDecision(ctx, tx, decisions.SetDecision{Decision: d, EntryID: id(p)}); err != nil {
				return err
			}
		}
		for name, into := range map[string]*int64{"familia": &familia, "lixo": &lixo} {
			tag, err := dec.CreateTag(ctx, tx, name)
			if err != nil {
				return err
			}
			*into = tag.ID
		}
		if _, err := dec.SetTags(ctx, tx, decisions.SetTags{EntryIDs: []domain.EntryID{id("Fotos/2006"), id(gone)}, Add: []int64{familia}}); err != nil {
			return err
		}
		_, err := dec.SetTags(ctx, tx, decisions.SetTags{EntryIDs: []domain.EntryID{id(thumbs), id(resized)}, Add: []int64{lixo}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	decided := e.entries("corpus")
	tags := e.tagRows()

	node(t, root, resized).Size(99_999)
	node(t, root, "Fotos/2006/Praia").Remove("Thumbs.db")
	node(t, root, "Fotos").Remove("2007")
	node(t, root, copia+"/2006").File("novo.jpg", 1234, mtime)
	node(t, root, casamento).File("novo.jpg", 4321, mtime)
	e.scan("corpus")
	rows := e.entries("corpus")

	if r := get(t, rows, resized); r.Size != 99_999 || r.ID != decided[resized].ID || r.EffDecision != "keep" {
		t.Errorf("resized: %+v", r)
	}
	for _, p := range []string{thumbs, gone, gone + "/Formatura", gone + "/Formatura/DSC03001.JPG"} {
		r, d := get(t, rows, p), decided[p]
		if r.State != "missing" || r.ID != d.ID || r.Decision != d.Decision || r.EffDecision != d.EffDecision || r.EffFrom != d.EffFrom {
			t.Errorf("%s: %+v, decided %+v", p, r, d)
		}
	}
	if r := get(t, rows, copia+"/2006/novo.jpg"); r.EffDecision != "discard" || r.EffFrom.Int64 != int64(id(copia)) || r.Decision.Valid {
		t.Errorf("new file under a discarded folder: eff %s from %d own %v", r.EffDecision, r.EffFrom.Int64, r.Decision)
	}
	if r := get(t, rows, casamento+"/novo.jpg"); r.EffDecision != "keep" || r.EffFrom.Int64 != int64(id(casamento)) {
		t.Errorf("new file under a kept folder: eff %s from %d", r.EffDecision, r.EffFrom.Int64)
	}
	for p, d := range decided {
		r := get(t, rows, p)
		if r.ID != d.ID || r.Decision != d.Decision || r.EffDecision != d.EffDecision || r.EffFrom != d.EffFrom {
			t.Errorf("%q decision changed: %+v → %+v", p, d, r)
		}
	}
	if got := e.tagRows(); got != tags {
		t.Errorf("tags %q, before the rescan %q", got, tags)
	}

	node(t, root, "Fotos/2006/Praia").File("Thumbs.db", 8_192, mtime)
	e.scan("corpus")
	if r := get(t, e.entries("corpus"), thumbs); r.State != "present" || r.ID != decided[thumbs].ID ||
		r.Decision.String != "later" {
		t.Errorf("returning Thumbs.db: %+v", r)
	}
	if got := e.tagRows(); got != tags {
		t.Errorf("tags %q after the return, before %q", got, tags)
	}
}

// tagRows lists every entry_tags row.
func (e *env) tagRows() string {
	e.t.Helper()
	var s sql.NullString
	if err := e.st.Reader().QueryRow(`SELECT group_concat(entry_id || ':' || tag_id, ',') FROM
		(SELECT entry_id, tag_id FROM entry_tags ORDER BY entry_id, tag_id)`).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s.String
}
