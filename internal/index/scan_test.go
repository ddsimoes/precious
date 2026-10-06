package index

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/index/indextest"
)

var mtime = time.Date(2004, 6, 1, 10, 0, 0, 0, time.UTC)

// kindsTree builds a tree with every entry kind below root.
func kindsTree(e *env) *instrument.Recorder {
	root := e.disk("disk", "/src/disk", posix)
	fotos := root.Dir("Fotos")
	fotos.File("DSC_0001.JPG", 3000, mtime)
	fotos.File("IMG_0002.jpg", 2000, mtime.AddDate(1, 0, 0))
	fotos.Dir("vazia")
	docs := root.Dir("Documentos")
	docs.File("carta.doc", 500, mtime)
	docs.File("notas.txt", 40, mtime.AddDate(-2, 0, 0))
	docs.File("f\xe9.txt", 7, mtime)
	jogo := root.Dir("jogo")
	jogo.File("disco.bin", 9000, mtime)
	jogo.File("disco.cue", 100, mtime)
	jogo.File("solto.bin", 10, mtime)
	root.Symlink("fora", "/etc/passwd")
	root.Symlink("acima", "..")
	fotos.Symlink("ciclo", "../Fotos")
	root.Special("pipe", domain.EntryFIFO)
	root.Special("sock", domain.EntrySocket)
	root.Special("tty", domain.EntryCharDevice)
	mnt := root.Dir("montado")
	mnt.Dev(999)
	mnt.File("outro.txt", 123, mtime)
	bind := root.Dir("bind")
	bind.MountPoint()
	bind.File("dentro.txt", 5, mtime)
	trancada := root.Dir("trancada")
	trancada.File("segredo.txt", 77, mtime)
	trancada.Unreadable()
	rec := instrument.Wrap(e.sfs)
	e.fs = rec
	e.services()
	return rec
}

// Every kind is recorded; symlinks are never followed, special files never
// opened, and mount boundaries never entered; nothing's content is read.
func TestScanKinds(t *testing.T) {
	e := newEnv(t)
	rec := kindsTree(e)
	rt := e.scan("disk")
	rows := e.entries("disk")

	for path, want := range map[string]struct {
		kind, special, link string
	}{
		"fora": {"symlink", "", "/etc/passwd"}, "acima": {"symlink", "", ".."}, "Fotos/ciclo": {"symlink", "", "../Fotos"},
		"pipe": {"special", "fifo", ""}, "sock": {"special", "socket", ""}, "tty": {"special", "char_device", ""},
	} {
		r := get(t, rows, path)
		if r.Kind != want.kind || r.Special.String != want.special || r.Link.String != want.link {
			t.Errorf("%s: kind %s/%s link %q, want %s/%s %q", path, r.Kind, r.Special.String, r.Link.String,
				want.kind, want.special, want.link)
		}
		if r.TotalBytes != 0 || r.TotalFiles != 0 || r.Category.Valid {
			t.Errorf("%s counts %d bytes, %d files, category %v", path, r.TotalBytes, r.TotalFiles, r.Category)
		}
	}
	for _, p := range []string{"montado", "bind"} {
		r := get(t, rows, p)
		if !r.Boundary || r.Kind != "directory" || r.TotalFiles != 0 || !r.HasStats {
			t.Errorf("%s: %+v, want an empty mount-boundary folder", p, r)
		}
		for q := range rows {
			if strings.HasPrefix(q, p+"/") {
				t.Errorf("%s was crossed: %s indexed", p, q)
			}
		}
	}
	if r := get(t, rows, "trancada"); r.State != "unreadable" || r.TotalFiles != 0 {
		t.Errorf("trancada: state %s, %d files", r.State, r.TotalFiles)
	}
	root := get(t, rows, "")
	if !root.Partial || root.Unreadable.Int64 != 1 || root.Mounts.Int64 != 2 || root.Symlinks.Int64 != 3 ||
		root.Specials.Int64 != 3 || root.Dirs.Int64 != 7 {
		t.Errorf("root stats: partial %v unreadable %d mounts %d symlinks %d specials %d dirs %d", root.Partial,
			root.Unreadable.Int64, root.Mounts.Int64, root.Symlinks.Int64, root.Specials.Int64, root.Dirs.Int64)
	}
	if got := get(t, rows, "jogo/disco.bin").FileKind.String; got != "archive" {
		t.Errorf("disco.bin next to disco.cue is %s, want archive", got)
	}
	if got := get(t, rows, "jogo/solto.bin").FileKind.String; got != "other" {
		t.Errorf("solto.bin alone is %s, want other", got)
	}
	fotos := get(t, rows, "Fotos")
	if fotos.MainKind.String != "image" || fotos.Newest.Int64 != mtime.AddDate(1, 0, 0).UnixNano() ||
		fotos.Oldest.Int64 != mtime.UnixNano() {
		t.Errorf("Fotos main kind %s newest %d oldest %d", fotos.MainKind.String, fotos.Newest.Int64, fotos.Oldest.Int64)
	}
	if !strings.Contains(fotos.Indicators.String, `"path":"Fotos/DSC_0001.JPG"`) ||
		!strings.Contains(fotos.Indicators.String, `"entry_id":"`+domain.EntryID(get(t, rows, "Fotos/DSC_0001.JPG").ID).String()+`"`) {
		t.Errorf("Fotos indicators %s", fotos.Indicators.String)
	}
	if r := get(t, rows, "Fotos/vazia"); r.Kind != "directory" || !r.HasStats || r.TotalFiles != 0 || r.Newest.Valid || r.MainKind.Valid {
		t.Errorf("empty folder %+v", r)
	}

	for _, c := range rec.Calls() {
		p := c.FullPath()
		switch {
		case c.Op == instrument.OpOpenFile || c.Op == instrument.OpReadAt || c.Op == instrument.OpFileStat:
			t.Errorf("%s %s: content was opened", c.Op, p)
		case c.Op == instrument.OpReadlink && !slices.Contains([]string{"/src/disk/fora", "/src/disk/acima", "/src/disk/Fotos/ciclo"}, p):
			t.Errorf("Readlink %s", p)
		case c.Op == instrument.OpOpenDir && slices.Contains([]string{"/src/disk/fora", "/src/disk/acima",
			"/src/disk/Fotos/ciclo", "/src/disk/pipe", "/src/disk/sock", "/src/disk/tty", "/src/disk/montado", "/src/disk/bind"}, p):
			t.Errorf("OpenDir %s", p)
		case strings.HasPrefix(p, "/src/disk/montado/") || strings.HasPrefix(p, "/src/disk/bind/") ||
			strings.HasPrefix(p, "/src/disk/fora/") || strings.HasPrefix(p, "/src/disk/acima/") ||
			strings.HasPrefix(p, "/src/disk/Fotos/ciclo/") || strings.HasPrefix(p, "/etc"):
			t.Errorf("%s %s reached through a symlink or a mount boundary", c.Op, p)
		}
	}
	for _, k := range []string{ProgressPhase, ProgressDirs, ProgressFiles, ProgressBytes, ProgressWritten,
		ProgressUnreadable, ProgressMissing} {
		if _, ok := rt.progress[k]; !ok {
			t.Errorf("progress key %s missing: %v", k, rt.progress)
		}
	}
	if rt.progress[ProgressPhase] != PhaseFinishing || rt.progress[ProgressFiles] != 8 ||
		rt.progress[ProgressUnreadable] != 1 || rt.progress[ProgressDirs] != 8 {
		t.Errorf("progress %v", rt.progress)
	}
	if e.sourceGen("disk") != 1 {
		t.Errorf("source scan_gen %d, want 1", e.sourceGen("disk"))
	}
}

// The scan writes rows exactly as indextest.Seed does for the same tree:
// paths, totals, breakdowns, partial and unreadable folders, FTS names, and
// effective decisions.
func TestScanMatchesSeedConventions(t *testing.T) {
	e := newEnv(t)
	kindsTree(e)
	e.scan("disk")
	compareWithSeed(t, e, "disk")
}

// A5: a modification time at or before the epoch is unknown. Such a file
// has no newest or oldest time of its own, its folders' ranges leave it out,
// and by_year counts it under the year 0, so the buckets still sum to the
// totals; indextest.Seed agrees. A rescan of rows written before the rule
// refreshes them.
func TestScanUnknownTimes(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/src/disk", posix)
	velhas := root.Dir("Velhas")
	velhas.File("a.txt", 30, mtime)
	velhas.File("sem-data.txt", 70, time.Unix(0, 0))
	velhas.Dir("antes").File("b.txt", 5, time.Unix(-3600, 0))
	e.scan("disk")

	check := func(when string) {
		t.Helper()
		rows := e.entries("disk")
		for _, p := range []string{"Velhas/sem-data.txt", "Velhas/antes/b.txt", "Velhas/antes"} {
			if r := get(t, rows, p); r.Newest.Valid || r.Oldest.Valid {
				t.Errorf("%s: %s newest %v oldest %v; want NULL", when, p, r.Newest, r.Oldest)
			}
		}
		if r := get(t, rows, "Velhas/antes"); !r.MainKind.Valid || r.ByYear.String != `{"0":{"files":1,"bytes":5}}` {
			t.Errorf("%s: antes main kind %v, by_year %s", when, r.MainKind, r.ByYear.String)
		}
		for _, p := range []string{"Velhas", ""} {
			r := get(t, rows, p)
			if r.Newest.Int64 != mtime.UnixNano() || r.Oldest.Int64 != mtime.UnixNano() ||
				r.ByYear.String != `{"0":{"files":2,"bytes":75},"2004":{"files":1,"bytes":30}}` {
				t.Errorf("%s: %q newest %v oldest %v by_year %s", when, p, r.Newest, r.Oldest, r.ByYear.String)
			}
		}
	}
	check("scan")
	compareWithSeed(t, e, "disk")

	// Rows as a scan before the rule wrote them: a file's own time as its
	// range, the folders' ranges and years over it.
	for _, q := range []string{
		`UPDATE entries SET newest_ns = mtime_ns, oldest_ns = mtime_ns WHERE source_id = 'disk' AND kind = 'file'`,
		`UPDATE entries SET oldest_ns = -3600000000000 WHERE source_id = 'disk' AND kind = 'directory'`,
		`UPDATE dir_stats SET by_year = '{"1969":{"files":1,"bytes":5}}'`,
	} {
		if _, err := e.st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.scan("disk")
	check("rescan")
}

// compareWithSeed seeds the scanned tree of src with indextest into a new
// source and compares every column both write.
func compareWithSeed(t *testing.T, e *env, src domain.SourceID) {
	t.Helper()
	scanned := e.entries(src)
	var nodes []indextest.Node
	for path, r := range scanned {
		n := indextest.Node{Path: path, Kind: domain.EntryKind(r.Kind), Size: r.Size,
			MTime: time.Unix(0, r.MTime.Int64).UTC(), LinkText: r.Link.String,
			Unreadable: r.State == "unreadable", MountBoundary: r.Boundary, Group: r.Group}
		if r.Kind == "special" {
			n.Kind = domain.EntryKind(r.Special.String)
		}
		if r.Kind == "file" {
			n.FileKind = domain.FileKind(r.FileKind.String)
		}
		if r.Category.Valid {
			n.Category, n.Triage = domain.Category(r.Category.String), domain.Triage(r.Triage.String)
		}
		if r.State == "missing" {
			t.Fatalf("compareWithSeed needs a tree without missing entries: %q", path)
		}
		nodes = append(nodes, n)
	}
	indextest.Seed(t, e.st, indextest.Tree{Source: "seed", CreateSource: true, Nodes: nodes})
	seeded := e.entries("seed")
	if len(seeded) != len(scanned) {
		t.Fatalf("seeded %d rows, scanned %d", len(seeded), len(scanned))
	}
	scannedIDs, seededIDs := pathsByID(scanned), pathsByID(seeded)
	for path, s := range scanned {
		d := get(t, seeded, path)
		if d.Category.Valid {
			if s.Category != d.Category || s.Family != d.Family || s.Triage != d.Triage {
				t.Errorf("%q: classification %v/%v/%v, seed %v/%v/%v", path, s.Category, s.Family, s.Triage,
					d.Category, d.Family, d.Triage)
			}
		}
		// The inside lists name the same entries, by their own IDs.
		if a, b := insideByPath(t, scannedIDs, s.Inside), insideByPath(t, seededIDs, d.Inside); a != b {
			t.Errorf("%q: inside\n scanned %s\n seeded  %s", path, a, b)
		}
		// Columns the seed does not fill: identity, classification extras,
		// signals, and indicators.
		for _, x := range []*entry{&s, &d} {
			x.ID, x.Parent, x.Dev, x.Ino, x.Alloc, x.EffFrom = 0, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
			x.Category, x.Family, x.Triage, x.Traits, x.RuleIDs = sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{}
			x.Veto = false
			x.Signals, x.Indicators, x.Inside, x.ScanGen = sql.NullString{}, sql.NullString{}, sql.NullString{}, 0
		}
		if s != d {
			t.Errorf("%q:\n scanned %+v\n seeded  %+v", path, s, d)
		}
	}
	// Parents match by path.
	byID := map[int64]string{}
	for p, r := range e.entries(src) {
		byID[r.ID] = p
	}
	for p, r := range e.entries(src) {
		if p == "" {
			continue
		}
		parent := byID[r.Parent.Int64]
		if want := p[:max(0, strings.LastIndexByte(p, '/'))]; parent != want {
			t.Errorf("%q has parent %q", p, parent)
		}
	}
}

// pathsByID maps the IDs of rows to their raw paths.
func pathsByID(rows map[string]entry) map[string]string {
	out := make(map[string]string, len(rows))
	for p, r := range rows {
		out[strconv.FormatInt(r.ID, 10)] = p
	}
	return out
}

// insideByPath re-encodes a dir_stats.inside list with each entry_id replaced
// by the path of the row it names, which must be the item's path.
func insideByPath(t *testing.T, paths map[string]string, inside sql.NullString) string {
	t.Helper()
	if !inside.Valid {
		return ""
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(inside.String), &items); err != nil {
		t.Fatalf("inside %s: %v", inside.String, err)
	}
	for _, it := range items {
		raw, _ := base64.StdEncoding.DecodeString(it["path_b64"].(string))
		id := it["entry_id"].(string)
		if paths[id] != string(raw) {
			t.Errorf("inside item %s names entry %s at %q", raw, id, paths[id])
		}
		it["entry_id"] = paths[id]
	}
	j, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return string(j)
}

// R1.8: a name that is not valid UTF-8 is stored byte-exact, its path holds
// the raw bytes, and its FTS row holds its display name.
func TestR1_8NonUTF8NamesRoundTrip(t *testing.T) {
	e := newEnv(t)
	kindsTree(e)
	e.scan("disk")
	var (
		id         int64
		name, path []byte
	)
	err := e.st.Reader().QueryRow(`SELECT id, name, path FROM entries WHERE source_id = 'disk' AND path = ?`,
		[]byte("Documentos/f\xe9.txt")).Scan(&id, &name, &path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(name, []byte("f\xe9.txt")) || !bytes.Equal(path, []byte("Documentos/f\xe9.txt")) {
		t.Errorf("name %q path %q", name, path)
	}
	// The FTS row holds the display name f\xE9.txt: its rowid is found by
	// the display form, and by nothing else of the raw name.
	for query, want := range map[string][]int64{`"f\xE9.txt"`: {id}, `"xE9.t"`: {id}} {
		var got []int64
		rows, err := e.st.Reader().Query(`SELECT rowid FROM entry_names WHERE entry_names MATCH ?`, query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var r int64
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			got = append(got, r)
		}
		rows.Close()
		if !slices.Equal(got, want) {
			t.Errorf("MATCH %s = %v, want %v", query, got, want)
		}
	}
}

// R1.9: a full scan and a rescan make only read-class calls.
func TestR1_9ScanNeverWritesToTheSource(t *testing.T) {
	e := newEnv(t)
	rec := kindsTree(e)
	e.scan("disk")
	e.scan("disk")
	read := []instrument.Op{instrument.OpOpenRoot, instrument.OpReadBatch, instrument.OpLstat, instrument.OpOpenDir,
		instrument.OpReadlink, instrument.OpFSInfo, instrument.OpClose, instrument.OpMounts, instrument.OpCapabilities}
	if len(rec.Calls()) == 0 {
		t.Fatal("nothing recorded")
	}
	for op, n := range rec.Counts() {
		if !slices.Contains(read, op) {
			t.Errorf("%d %s calls: not a metadata read", n, op)
		}
	}
	if rec.Count(instrument.OpOpenRoot) != 2 {
		t.Errorf("OpenRoot %d times, want once per scan", rec.Count(instrument.OpOpenRoot))
	}
}

// A slow writer blocks the walk: no more than the queued batches, the one
// being written, and the one being filled are ever listed ahead of it.
func TestScanPipelineBackpressure(t *testing.T) {
	e := newEnv(t)
	root := e.disk("big", "/src/big", posix)
	const total = 1000
	root.Generate(total, total) // flat: every listed entry is one insert
	e.cfg.BatchSize, e.cfg.ListBatch = 20, 16
	rec := instrument.Wrap(e.sfs)
	e.fs = rec
	e.services()

	ctx := context.Background()
	held := make(chan func(), 1)
	var once bool
	rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op != instrument.OpReadBatch || once {
			return
		}
		once = true
		conn, err := e.st.Writer().Conn(ctx) // the writer's only connection
		if err != nil {
			t.Error(err)
			return
		}
		held <- func() { conn.Close() }
	})
	done := make(chan error, 1)
	go func() { done <- e.scanWith(ctx, "big", &fakeRuntime{}) }()
	release := <-held
	stable, last := 0, int64(-1)
	for stable < 10 {
		time.Sleep(20 * time.Millisecond)
		select {
		case err := <-done:
			t.Fatalf("scan finished while the writer was blocked: %v", err)
		default:
		}
		if n := rec.Entries(); n == last {
			stable++
		} else {
			stable, last = 0, n
		}
	}
	if limit := int64((queueBatches+2)*e.cfg.BatchSize + e.cfg.ListBatch); last > limit {
		t.Errorf("listed %d entries ahead of a blocked writer, want at most %d", last, limit)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := len(e.entries("big")); got != total+1 {
		t.Errorf("indexed %d rows, want %d", got, total+1)
	}
}
