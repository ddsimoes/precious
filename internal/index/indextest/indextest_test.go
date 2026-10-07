package indextest_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

func year(y int) time.Time { return time.Date(y, 6, 1, 12, 0, 0, 0, time.UTC) }

func threeLevels() []indextest.Node {
	img := domain.FileKindImage
	return []indextest.Node{
		{Path: "Fotos", Kind: domain.EntryDirectory, Category: domain.CategoryPersonalMedia},
		{Path: "Fotos/2004/a.JPG", Size: 100, MTime: year(2004), FileKind: img},
		{Path: "Fotos/2004/b.jpg", Size: 200, MTime: year(2004), FileKind: img},
		{Path: "Fotos/2006/c.jpg", Size: 300, MTime: year(2006), FileKind: img},
		{Path: "Documentos/OFFICE11/Meu orcamento casamento.xls", Size: 50, MTime: year(2010), FileKind: domain.FileKindDocument},
		{Path: "Programas", Kind: domain.EntryDirectory, Category: domain.CategoryApplicationInstallation, Group: true},
		{Path: "Programas/setup.exe", Size: 1000, MTime: year(2001), FileKind: domain.FileKindInstaller},
		{Path: "Programas/icon.png", Size: 10, MTime: year(2001), FileKind: img},
		{Path: "link", Kind: domain.EntrySymlink, LinkText: "/etc"},
		{Path: "pipe", Kind: domain.EntryFIFO},
		{Path: "vazio", Kind: domain.EntryDirectory},
		{Path: "trancado", Kind: domain.EntryDirectory, Unreadable: true},
		{Path: "readme", Size: 5, MTime: year(2003)},
	}
}

type row struct {
	id, parent             sql.NullInt64
	name, path             []byte
	kind, state, eff       string
	size, bytes, files     int64
	newest, oldest         sql.NullInt64
	partial, gen           int64
	dirs, nfiles, symlinks sql.NullInt64
	specials, unreadable   sql.NullInt64
	byKind, byYear, byFam  sql.NullString
}

func loadRows(t *testing.T, st *store.Store, src domain.SourceID) map[int64]*row {
	t.Helper()
	rows, err := st.Reader().Query(`SELECT e.id, e.parent_id, e.name, e.path, e.kind, e.state, e.eff_decision,
		e.size, e.total_bytes, e.total_files, e.newest_ns, e.oldest_ns, e.partial, e.scan_gen,
		d.dirs, d.files, d.symlinks, d.specials, d.unreadable, d.by_kind, d.by_year, d.by_family
		FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id WHERE e.source_id = ?`, string(src))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]*row{}
	for rows.Next() {
		r := &row{}
		if err := rows.Scan(&r.id, &r.parent, &r.name, &r.path, &r.kind, &r.state, &r.eff,
			&r.size, &r.bytes, &r.files, &r.newest, &r.oldest, &r.partial, &r.gen,
			&r.dirs, &r.nfiles, &r.symlinks, &r.specials, &r.unreadable, &r.byKind, &r.byYear, &r.byFam); err != nil {
			t.Fatal(err)
		}
		out[r.id.Int64] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// subtree recomputes a folder's aggregates from its descendants' rows,
// independently of Seed. newest and oldest span the rows with a known time
// (dated); a file of an unknown time has none of its own.
type subtree struct {
	bytes, files, dirs, symlinks, specials, unreadable int64
	newest, oldest                                     int64
	partial, dated                                     bool
}

func (s *subtree) addRange(oldest, newest int64) {
	if !s.dated {
		s.oldest, s.newest, s.dated = oldest, newest, true
		return
	}
	s.oldest = min(s.oldest, oldest)
	s.newest = max(s.newest, newest)
}

func recompute(children map[int64][]*row, id int64) subtree {
	var s subtree
	for _, c := range children[id] {
		switch c.kind {
		case "directory":
			cs := recompute(children, c.id.Int64)
			s.bytes += cs.bytes
			s.files += cs.files
			s.dirs += 1 + cs.dirs
			s.symlinks += cs.symlinks
			s.specials += cs.specials
			s.unreadable += cs.unreadable
			if c.state == "unreadable" {
				s.unreadable++
			}
			s.partial = s.partial || cs.partial || c.state == "unreadable"
			if cs.dated {
				s.addRange(cs.oldest, cs.newest)
			}
		case "file":
			s.bytes += c.size
			s.files++
			if c.newest.Valid {
				s.addRange(c.oldest.Int64, c.newest.Int64)
			}
		case "symlink":
			s.symlinks++
		default:
			s.specials++
		}
	}
	return s
}

func sumCounts(t *testing.T, js string) indextest.Counts {
	t.Helper()
	var m map[string]indextest.Counts
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("%s: %v", js, err)
	}
	var total indextest.Counts
	for _, c := range m {
		total.Files += c.Files
		total.Bytes += c.Bytes
	}
	return total
}

// A seeded three-level tree has paths and parent links that agree, folder
// totals and dir_stats consistent with the rows below each folder, the D7
// family breakdown, undecided entries, and names found by an FTS trigram
// substring query.
func TestSeedThreeLevelTree(t *testing.T) {
	st := storetest.Open(t)
	seed := indextest.Seed(t, st, indextest.Tree{Source: "disk", CreateSource: true, Nodes: threeLevels()})
	rows := loadRows(t, st, "disk")
	if len(rows) != 18 {
		t.Fatalf("seeded %d entries, want 18", len(rows))
	}

	children := map[int64][]*row{}
	for _, r := range rows {
		if r.eff != "undecided" || r.gen != 1 {
			t.Errorf("%q: eff_decision %q, scan_gen %d; want undecided, 1", r.path, r.eff, r.gen)
		}
		if !r.parent.Valid {
			if r.id.Int64 != int64(seed.Root) || len(r.path) != 0 || len(r.name) != 0 {
				t.Errorf("parentless entry %d %q is not the root %d", r.id.Int64, r.path, seed.Root)
			}
			continue
		}
		children[r.parent.Int64] = append(children[r.parent.Int64], r)
		parent := rows[r.parent.Int64]
		want := string(r.name)
		if len(parent.path) > 0 {
			want = string(parent.path) + "/" + want
		}
		if string(r.path) != want {
			t.Errorf("entry %d path %q, want %q from its parent", r.id.Int64, r.path, want)
		}
	}

	for _, r := range rows {
		if r.kind != "directory" {
			continue
		}
		s := recompute(children, r.id.Int64)
		got := subtree{r.bytes, r.files, r.dirs.Int64, r.symlinks.Int64, r.specials.Int64, r.unreadable.Int64,
			r.newest.Int64, r.oldest.Int64, r.partial == 1, r.newest.Valid}
		if got != s || r.nfiles.Int64 != s.files {
			t.Errorf("folder %q: stored %+v (dir_stats files %d), recomputed %+v", r.path, got, r.nfiles.Int64, s)
		}
		want := indextest.Counts{Files: r.files, Bytes: r.bytes}
		for name, js := range map[string]string{"by_kind": r.byKind.String, "by_year": r.byYear.String, "by_family": r.byFam.String} {
			if c := sumCounts(t, js); c != want {
				t.Errorf("folder %q: %s %s sums to %+v, want %+v", r.path, name, js, c, want)
			}
		}
	}

	root := rows[int64(seed.Root)]
	var fam map[domain.Family]indextest.Counts
	if err := json.Unmarshal([]byte(root.byFam.String), &fam); err != nil {
		t.Fatal(err)
	}
	wantFam := map[domain.Family]indextest.Counts{
		domain.FamilyPersonal:   {Files: 4, Bytes: 650},
		domain.FamilyPrograms:   {Files: 2, Bytes: 1010},
		domain.FamilyDisposable: {},
		domain.FamilyContainers: {Files: 1, Bytes: 5},
	}
	if !reflect.DeepEqual(fam, wantFam) {
		t.Errorf("root by_family = %v, want %v", fam, wantFam)
	}
	var mainKind string
	if err := st.Reader().QueryRow(`SELECT main_kind FROM entries WHERE id = ?`, int64(seed.Root)).Scan(&mainKind); err != nil || mainKind != "installer" {
		t.Errorf("root main_kind = %q, %v; want installer", mainKind, err)
	}
	// The root is mostly programs: the group, the two personal folders, and
	// the loose file of another family are notable; the empty folders are
	// not (design D21).
	var inside string
	if err := st.Reader().QueryRow(`SELECT inside FROM dir_stats WHERE entry_id = ?`, int64(seed.Root)).Scan(&inside); err != nil {
		t.Fatal(err)
	}
	item := func(path, category, family string, group bool, bytes, files int64) string {
		cat, fam := "null", "null"
		if category != "" {
			cat = `"` + category + `"`
		}
		if family != "" {
			fam = `"` + family + `"`
		}
		b64, _ := json.Marshal([]byte(path))
		return fmt.Sprintf(`{"entry_id":"%d","path_b64":%s,"path":%q,"category":%s,"family":%s,"group":%t,"bytes":%d,"files":%d}`,
			seed.ID(path), b64, path, cat, fam, group, bytes, files)
	}
	if want := "[" + strings.Join([]string{
		item("Programas", "application_installation", "programs", true, 1010, 2),
		item("Fotos", "personal_media", "personal", false, 600, 3),
		item("Documentos", "", "", false, 50, 1),
		item("readme", "", "containers", false, 5, 1),
	}, ",") + "]"; inside != want {
		t.Errorf("root inside\n got  %s\n want %s", inside, want)
	}
	var ext, fileKind string
	if err := st.Reader().QueryRow(`SELECT ext, file_kind FROM entries WHERE id = ?`,
		int64(seed.ID("Fotos/2004/a.JPG"))).Scan(&ext, &fileKind); err != nil || ext != "jpg" || fileKind != "image" {
		t.Errorf("a.JPG ext, file_kind = %q, %q, %v; want jpg, image", ext, fileKind, err)
	}

	for q, want := range map[string][]domain.EntryID{
		`"CASAMENTO"`: {seed.ID("Documentos/OFFICE11/Meu orcamento casamento.xls")},
		`"otos"`:      {seed.ID("Fotos")},
		`"jpg"`:       {seed.ID("Fotos/2004/a.JPG"), seed.ID("Fotos/2004/b.jpg"), seed.ID("Fotos/2006/c.jpg")},
	} {
		var got []domain.EntryID
		res, err := st.Reader().Query(`SELECT rowid FROM entry_names WHERE entry_names MATCH ? ORDER BY rowid`, q)
		if err != nil {
			t.Fatal(err)
		}
		for res.Next() {
			var id domain.EntryID
			if err := res.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got = append(got, id)
		}
		if err := res.Err(); err != nil {
			t.Fatal(err)
		}
		res.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MATCH %s = %v, want %v", q, got, want)
		}
	}
}

// Seed accepts an existing source and advances its scan generation.
func TestSeedExistingSource(t *testing.T) {
	st := storetest.Open(t)
	if _, err := st.Writer().Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong,
		rel_root, capabilities, state, scan_gen, created_at)
		VALUES ('old', 'Old disk', 'uuid', 'u-1', 'ext4', 1, X'', ?, 'offline', 4, 1)`, indextest.Capabilities); err != nil {
		t.Fatal(err)
	}
	seed := indextest.Seed(t, st, indextest.Tree{Source: "old", Nodes: []indextest.Node{{Path: "a/b.txt", Size: 7}}})

	var gen, lastScan, entryGen, total int64
	if err := st.Reader().QueryRow(`SELECT s.scan_gen, s.last_scan_at, e.scan_gen, e.total_bytes
		FROM sources s JOIN entries e ON e.source_id = s.id WHERE e.id = ?`, int64(seed.ID("a"))).
		Scan(&gen, &lastScan, &entryGen, &total); err != nil {
		t.Fatal(err)
	}
	if gen != 5 || entryGen != 5 || lastScan != indextest.DefaultNow.UnixMilli() || total != 7 {
		t.Fatalf("source gen %d, last_scan_at %d, entry gen %d, total %d; want 5, %d, 5, 7",
			gen, lastScan, entryGen, total, indextest.DefaultNow.UnixMilli())
	}
}

// Attach addresses an already indexed source by path with the same IDs, and
// its content can then be seeded.
func TestAttach(t *testing.T) {
	st := storetest.Open(t)
	seed := indextest.Seed(t, st, indextest.Tree{Source: "disk", CreateSource: true, Nodes: threeLevels()})
	got := indextest.Attach(t, st, "disk")
	if got.Source != seed.Source || got.Root != seed.Root {
		t.Fatalf("attached source %q root %d, want %q root %d", got.Source, got.Root, seed.Source, seed.Root)
	}
	for _, n := range threeLevels() {
		if got.ID(n.Path) != seed.ID(n.Path) {
			t.Errorf("%s: attached ID %d, seeded %d", n.Path, got.ID(n.Path), seed.ID(n.Path))
		}
	}
	file := "Fotos/2006/c.jpg"
	got.SetContent(st, file, indextest.Content{State: domain.ContentUniqueSize})
	var state string
	if err := st.Reader().QueryRow(`SELECT state FROM file_content WHERE entry_id = ?`, int64(seed.ID(file))).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.ContentUniqueSize) {
		t.Fatalf("state %q after SetContent through Attach", state)
	}
}

// A node's Lstat gives its row the platform facts with the scanner's
// encodings; other rows keep them NULL.
func TestSeedLstatFacts(t *testing.T) {
	st := storetest.Open(t)
	mtime := time.Date(2010, 3, 3, 10, 0, 0, 123456789, time.UTC)
	info := fsaccess.EntryInfo{
		Name: []byte("b.txt"), Kind: domain.EntryFile, Size: 5000, ModTime: mtime, Mode: 0o644,
		Dev: 1<<63 + 2, Ino: 77, Nlink: 2, Blocks: 16, Ctime: mtime.Add(time.Second),
	}
	seed := indextest.Seed(t, st, indextest.Tree{Source: "s", CreateSource: true, Nodes: []indextest.Node{
		{Path: "a/b.txt", Lstat: &info},
		{Path: "a/c.txt", Size: 3},
	}})

	type facts struct {
		size, mtime, alloc, ctime, dev, ino, nlink, mode sql.NullInt64
	}
	read := func(path string) facts {
		var f facts
		if err := st.Reader().QueryRow(`SELECT size, mtime_ns, alloc, ctime_ns, dev, ino, nlink, mode
			FROM entries WHERE id = ?`, int64(seed.ID(path))).
			Scan(&f.size, &f.mtime, &f.alloc, &f.ctime, &f.dev, &f.ino, &f.nlink, &f.mode); err != nil {
			t.Fatal(err)
		}
		return f
	}
	v := func(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
	want := facts{size: v(5000), mtime: v(mtime.UnixNano()), alloc: v(16 * 512), ctime: v(mtime.Add(time.Second).UnixNano()),
		dev: v(int64(info.Dev)), ino: v(77), nlink: v(2), mode: v(0o644)}
	if got := read("a/b.txt"); got != want {
		t.Errorf("a/b.txt facts %+v, want %+v", got, want)
	}
	if got := read("a/c.txt"); got != (facts{size: v(3), mtime: v(indextest.DefaultNow.UnixNano())}) {
		t.Errorf("a/c.txt facts %+v, want NULL platform facts", got)
	}
}

func TestTag(t *testing.T) {
	st := storetest.Open(t)
	s := indextest.Seed(t, st, indextest.Tree{Source: "s", CreateSource: true, Nodes: threeLevels()})
	familia := s.Tag(st, "familia", "Fotos/2006", "readme")
	if again := s.Tag(st, "FAMILIA", "Fotos/2006", "Fotos"); again != familia {
		t.Fatalf("a case variant made tag %d, want %d", again, familia)
	}
	rows, err := st.Reader().Query(`SELECT e.path, et.added_at FROM entry_tags et JOIN entries e ON e.id = et.entry_id
		WHERE et.tag_id = ? ORDER BY e.path`, familia)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var path []byte
		var added int64
		if err := rows.Scan(&path, &added); err != nil {
			t.Fatal(err)
		}
		if added != indextest.DefaultNow.UnixMilli() {
			t.Errorf("%s added at %d", path, added)
		}
		got = append(got, string(path))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Own tags only: the descendants of Fotos/2006 inherit it.
	if want := []string{"Fotos", "Fotos/2006", "readme"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tagged %q, want %q", got, want)
	}
}
