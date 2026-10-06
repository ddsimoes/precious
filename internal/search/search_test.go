package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

func year(y int) time.Time { return time.Date(y, 6, 1, 12, 0, 0, 0, time.UTC) }

// corpus seeds two sources. "disco" holds the traps: "Fotos - Copia" and
// "Fotos0" next to "Fotos", a group, a non-UTF-8 folder and file, and
// non-ASCII names; "pen" holds one more natal photo.
func corpus(t *testing.T) (*store.Store, *indextest.Seeded, *indextest.Seeded) {
	t.Helper()
	st := storetest.Open(t)
	img, doc := domain.FileKindImage, domain.FileKindDocument
	disco := indextest.Seed(t, st, indextest.Tree{Source: "disco", CreateSource: true, MountPoint: "/mnt/disco", Nodes: []indextest.Node{
		{Path: "Fotos", Kind: domain.EntryDirectory, Category: domain.CategoryPersonalMedia},
		{Path: "Fotos/2004/Fotos Natal 2004", Kind: domain.EntryDirectory, MTime: year(2015)},
		{Path: "Fotos/2004/Fotos Natal 2004/NATAL.JPG", Size: 100, MTime: year(2004), FileKind: img},
		{Path: "Fotos/2004/Fotos Natal 2004/IMG_0002.jpg", Size: 200, MTime: year(2004), FileKind: img},
		{Path: "Fotos/2006/c.jpg", Size: 300, MTime: year(2006), FileKind: img},
		{Path: "Fotos/2006/natal/IMG_0001.JPG", Size: 400, MTime: year(2006), FileKind: img},
		{Path: "Fotos - Copia/c.jpg", Size: 300, MTime: year(2006), FileKind: img},
		{Path: "Fotos0/z.jpg", Size: 1, MTime: year(2006), FileKind: img},
		{Path: "Documentos/carta.doc", Size: 50, MTime: year(2010), FileKind: doc, Triage: domain.TriageKeep},
		{Path: "Documentos/velho.DOC", Size: 55, MTime: year(1999), FileKind: doc},
		{Path: "Documentos/AÇÃO.TXT", Size: 7, MTime: year(2011), FileKind: doc},
		{Path: "Backup/relatorio.doc", Size: 70, MTime: year(2010), FileKind: doc},
		{Path: "Arquivos de programas/Microsoft Office", Kind: domain.EntryDirectory, Category: domain.CategoryApplicationInstallation, Triage: domain.TriageReview,
			Group: true},
		{Path: "Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls", Size: 60, MTime: year(2009), FileKind: doc},
		{Path: "Arquivos de programas/Microsoft Office/OFFICE11/WINWORD.EXE", Size: 5000, MTime: year(2003), FileKind: domain.FileKindExecutable, Triage: domain.TriageDiscard},
		{Path: "dir\xff/in.bin", Size: 8, MTime: year(2012)},
		{Path: "dir\xff0.bin", Size: 9, MTime: year(2012)},
		{Path: "f\xe9.txt", Size: 6, MTime: year(2012)},
		{Path: "link", Kind: domain.EntrySymlink, LinkText: "/etc", MTime: year(2012)},
		{Path: "pipe", Kind: domain.EntryFIFO, MTime: year(2012)},
		{Path: "vazio", Kind: domain.EntryDirectory, MTime: year(2012)},
	}})
	pen := indextest.Seed(t, st, indextest.Tree{Source: "pen", CreateSource: true, Nodes: []indextest.Node{
		{Path: "Fotos/natal.jpg", Size: 10, MTime: year(2006), FileKind: img},
	}})
	return st, disco, pen
}

// resolve returns the "source:path" of every match, sorted.
func resolve(t *testing.T, st *store.Store, q Query) []string {
	t.Helper()
	ids, err := Resolve(context.Background(), st.Reader(), q, MaxResolve)
	if err != nil {
		t.Fatalf("Resolve(%+v): %v", q, err)
	}
	return pathsOf(t, st, ids)
}

func pathsOf(t *testing.T, st *store.Store, ids []domain.EntryID) []string {
	t.Helper()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		var src string
		var path []byte
		if err := st.Reader().QueryRow(`SELECT source_id, path FROM entries WHERE id = ?`, int64(id)).Scan(&src, &path); err != nil {
			t.Fatal(err)
		}
		out = append(out, src+":"+string(path))
	}
	slices.Sort(out)
	return out
}

func rowPaths(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = string(r.Source) + ":" + string(r.Path)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func sorted(s ...string) []string {
	slices.Sort(s)
	return s
}

func wantCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if err == nil || domain.CodeOf(err) != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestEachFilter(t *testing.T) {
	st, disco, pen := corpus(t)
	fotos, docs := disco.ID("Fotos"), disco.ID("Documentos")
	tests := []struct {
		name  string
		query Query
		want  []string
	}{
		{"name is a case-insensitive substring", Query{Name: "natal"}, sorted(
			"disco:Fotos/2004/Fotos Natal 2004", "disco:Fotos/2004/Fotos Natal 2004/NATAL.JPG",
			"disco:Fotos/2006/natal", "pen:Fotos/natal.jpg")},
		{"name folds non-ASCII case", Query{Name: "ação"}, []string{"disco:Documentos/AÇÃO.TXT"}},
		{"name matches inside a group", Query{Name: "orcamento"}, []string{
			"disco:Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"}},
		{"name matches the display form", Query{Name: `f\xE9`}, []string{"disco:f\xe9.txt"}},
		{"two-character name", Query{Name: "çã"}, []string{"disco:Documentos/AÇÃO.TXT"}},
		{"one-character name", Query{Name: "Z", Source: "disco"}, sorted("disco:Fotos0/z.jpg", "disco:vazio")},
		{"quote in name", Query{Name: `a"b`}, []string{}},
		{"source", Query{Source: "pen"}, sorted("pen:", "pen:Fotos", "pen:Fotos/natal.jpg")},
		{"ext ignores case and dot", Query{Ext: []string{".DOC"}}, sorted(
			"disco:Documentos/carta.doc", "disco:Documentos/velho.DOC", "disco:Backup/relatorio.doc")},
		{"ext list", Query{Ext: []string{"xls", "exe"}}, sorted(
			"disco:Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls",
			"disco:Arquivos de programas/Microsoft Office/OFFICE11/WINWORD.EXE")},
		{"file kind", Query{FileKinds: []domain.FileKind{domain.FileKindExecutable, domain.FileKindOther}}, sorted(
			"disco:Arquivos de programas/Microsoft Office/OFFICE11/WINWORD.EXE",
			"disco:dir\xff/in.bin", "disco:dir\xff0.bin", "disco:f\xe9.txt")},
		{"size range is inclusive over total bytes", Query{MinSize: ptr[int64](300), MaxSize: ptr[int64](400)}, sorted(
			"disco:Fotos/2006/c.jpg", "disco:Fotos/2006/natal/IMG_0001.JPG", "disco:Fotos/2006/natal",
			"disco:Fotos - Copia/c.jpg", "disco:Fotos - Copia", "disco:Fotos/2004/Fotos Natal 2004",
			"disco:Fotos/2004")},
		{"year range on mtime", Query{YearFrom: ptr(2009), YearTo: ptr(2010)}, sorted(
			"disco:Documentos/carta.doc", "disco:Backup/relatorio.doc",
			"disco:Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls")},
		{"one year", Query{YearFrom: ptr(2015), YearTo: ptr(2015)}, []string{"disco:Fotos/2004/Fotos Natal 2004"}},
		{"year from only", Query{YearFrom: ptr(2012), Ext: []string{"bin", "txt"}}, sorted(
			"disco:dir\xff/in.bin", "disco:dir\xff0.bin", "disco:f\xe9.txt")},
		{"year to only", Query{YearTo: ptr(2003)}, sorted(
			"disco:Documentos/velho.DOC", "disco:Arquivos de programas/Microsoft Office/OFFICE11/WINWORD.EXE")},
		{"category", Query{Categories: []domain.Category{domain.CategoryApplicationInstallation}}, []string{
			"disco:Arquivos de programas/Microsoft Office"}},
		{"triage", Query{Triages: []domain.Triage{domain.TriageDiscard}}, []string{
			"disco:Arquivos de programas/Microsoft Office/OFFICE11/WINWORD.EXE"}},
		{"triage list", Query{Triages: []domain.Triage{domain.TriageKeep, domain.TriageReview}}, sorted(
			"disco:Documentos/carta.doc", "disco:Arquivos de programas/Microsoft Office")},
		{"triage and name", Query{Name: "car", Triages: []domain.Triage{domain.TriageKeep}}, []string{"disco:Documentos/carta.doc"}},
		{"triage and name, no match", Query{Name: "orcamento", Triages: []domain.Triage{domain.TriageDiscard}}, []string{}},
		{"within excludes the folder and look-alike siblings", Query{Within: &fotos, Ext: []string{"jpg"}}, sorted(
			"disco:Fotos/2004/Fotos Natal 2004/NATAL.JPG", "disco:Fotos/2004/Fotos Natal 2004/IMG_0002.jpg",
			"disco:Fotos/2006/c.jpg", "disco:Fotos/2006/natal/IMG_0001.JPG")},
		{"within a non-UTF-8 folder", Query{Within: ptr(disco.ID("dir\xff"))}, []string{"disco:dir\xff/in.bin"}},
		{"within the root", Query{Within: ptr(pen.Root)}, sorted("pen:Fotos", "pen:Fotos/natal.jpg")},
		{"within a file", Query{Within: ptr(disco.ID("f\xe9.txt"))}, []string{}},
		{"within and another source", Query{Within: &fotos, Source: "pen"}, []string{}},
		{"R1.3 search within a folder", Query{Within: &docs, Ext: []string{"doc"}}, sorted(
			"disco:Documentos/carta.doc", "disco:Documentos/velho.DOC")},
		{"combined", Query{Name: "IMG", Ext: []string{"jpg"}, YearFrom: ptr(2006), MinSize: ptr[int64](1), Within: &fotos},
			[]string{"disco:Fotos/2006/natal/IMG_0001.JPG"}},
		{"nothing", Query{Name: "não existe"}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(t, st, tc.query); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// decide sets an own decision on path and the effective decision of its
// subtree, as set-decision writes them (design D10), for a subtree without
// nested own decisions.
func decide(t *testing.T, st *store.Store, s *indextest.Seeded, path string, d domain.Decision) {
	t.Helper()
	id := s.ID(path)
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE entries SET decision = ?, eff_decision = ?, eff_from = ? WHERE id = ?`,
			string(d), string(d), int64(id), int64(id)); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE entries SET eff_decision = ?, eff_from = ? WHERE source_id = ? AND path >= ? AND path < ?`,
			string(d), int64(id), string(s.Source), []byte(path+"/"), []byte(path+"0"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTagsAndDecisions(t *testing.T) {
	st, disco, pen := corpus(t)
	familia := disco.Tag(st, "familia", "Fotos/2006")
	natal := disco.Tag(st, "natal", "Fotos/2006", "Fotos/2006/natal", "Fotos/2004/Fotos Natal 2004/NATAL.JPG")
	root := pen.Tag(st, "pen-tudo", "")
	weird := disco.Tag(st, "estranho", "dir\xff")
	fotos := disco.Tag(st, "fotos", "Fotos")
	decide(t, st, disco, "Fotos", domain.DecisionKeep)
	decide(t, st, disco, "Backup", domain.DecisionDiscard)

	tests := []struct {
		name  string
		query Query
		want  []string
	}{
		{"R1.12 an inherited tag is found below its folder", Query{Tags: []int64{familia}}, sorted(
			"disco:Fotos/2006", "disco:Fotos/2006/c.jpg", "disco:Fotos/2006/natal", "disco:Fotos/2006/natal/IMG_0001.JPG")},
		{"nested and file tags are not repeated", Query{Tags: []int64{natal, familia}}, sorted(
			"disco:Fotos/2006", "disco:Fotos/2006/c.jpg", "disco:Fotos/2006/natal", "disco:Fotos/2006/natal/IMG_0001.JPG",
			"disco:Fotos/2004/Fotos Natal 2004/NATAL.JPG")},
		{"a tag on the root covers its source", Query{Tags: []int64{root}}, sorted("pen:", "pen:Fotos", "pen:Fotos/natal.jpg")},
		{"a tag on a non-UTF-8 folder", Query{Tags: []int64{weird}}, sorted("disco:dir\xff", "disco:dir\xff/in.bin")},
		{"a folder tag skips look-alike siblings", Query{Tags: []int64{fotos}, Ext: []string{"jpg"}}, sorted(
			"disco:Fotos/2004/Fotos Natal 2004/NATAL.JPG", "disco:Fotos/2004/Fotos Natal 2004/IMG_0002.jpg",
			"disco:Fotos/2006/c.jpg", "disco:Fotos/2006/natal/IMG_0001.JPG")},
		{"unknown tag", Query{Tags: []int64{9999}}, []string{}},
		{"effective decision", Query{Decisions: []domain.Decision{domain.DecisionDiscard}}, sorted(
			"disco:Backup", "disco:Backup/relatorio.doc")},
		{"undecided", Query{Decisions: []domain.Decision{domain.DecisionUndecided}, Source: "pen"}, sorted(
			"pen:", "pen:Fotos", "pen:Fotos/natal.jpg")},
		{"inherited tag and decision", Query{Tags: []int64{fotos}, Decisions: []domain.Decision{domain.DecisionKeep}, FileKinds: []domain.FileKind{domain.FileKindImage}}, sorted(
			"disco:Fotos/2004/Fotos Natal 2004/NATAL.JPG", "disco:Fotos/2004/Fotos Natal 2004/IMG_0002.jpg",
			"disco:Fotos/2006/c.jpg", "disco:Fotos/2006/natal/IMG_0001.JPG")},
		{"tag and name", Query{Tags: []int64{familia}, Name: "img"}, []string{"disco:Fotos/2006/natal/IMG_0001.JPG"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(t, st, tc.query); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}

	t.Run("rows carry own tags and decisions", func(t *testing.T) {
		res, err := Page(context.Background(), st.Reader(), Query{Within: ptr(disco.ID("Fotos/2006")), Sort: SortName}, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		byPath := map[string]Row{}
		for _, r := range res.Items {
			byPath[string(r.Path)] = r
		}
		if got := byPath["Fotos/2006/natal"].TagIDs; !reflect.DeepEqual(got, []int64{natal}) {
			t.Errorf("natal folder tags = %v, want [%d]", got, natal)
		}
		if got := byPath["Fotos/2006/c.jpg"].TagIDs; got != nil {
			t.Errorf("c.jpg own tags = %v, want none", got)
		}
		r := byPath["Fotos/2006/c.jpg"]
		if r.Decision != "" || r.EffDecision != domain.DecisionKeep {
			t.Errorf("c.jpg decision %q eff %q, want inherited keep", r.Decision, r.EffDecision)
		}
	})
}

func TestRowFields(t *testing.T) {
	st, disco, _ := corpus(t)
	res, err := Page(context.Background(), st.Reader(), Query{Source: "disco", Sort: SortName}, "", MaxLimit)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Row{}
	for _, r := range res.Items {
		byPath[string(r.Path)] = r
	}
	xls := byPath["Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"]
	want := Row{
		ID: disco.ID("Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"), Source: "disco",
		Name: []byte("Meu orcamento casamento.xls"), Path: []byte("Arquivos de programas/Microsoft Office/OFFICE11/Meu orcamento casamento.xls"),
		Kind: domain.EntryFile, FileKind: domain.FileKindDocument, Size: 60, TotalBytes: 60, TotalFiles: 1,
		MTime: year(2009), Newest: year(2009), Oldest: year(2009), State: "present", EffDecision: domain.DecisionUndecided,
		// A document without a category counts as personal (design D21).
		Composition: []FamilyAmount{{Family: domain.FamilyPersonal, Bytes: 60, Files: 1}},
	}
	if !reflect.DeepEqual(xls, want) {
		t.Errorf("file row\n got %+v\nwant %+v", xls, want)
	}
	office := byPath["Arquivos de programas/Microsoft Office"]
	if office.Kind != domain.EntryDirectory || office.Category != domain.CategoryApplicationInstallation ||
		office.Family != domain.FamilyOf(domain.CategoryApplicationInstallation) || office.TotalBytes != 5060 ||
		office.TotalFiles != 2 || office.MainKind != domain.FileKindExecutable || !office.Newest.Equal(year(2009)) {
		t.Errorf("folder row %+v", office)
	}
	// A folder's composition is its by_family, in family order, without
	// empty families.
	if want := []FamilyAmount{{Family: domain.FamilyPersonal, Bytes: 60, Files: 1},
		{Family: domain.FamilyPrograms, Bytes: 5000, Files: 1}}; !reflect.DeepEqual(office.Composition, want) {
		t.Errorf("folder composition %+v, want %+v", office.Composition, want)
	}
	if p := byPath["pipe"]; p.Kind != domain.EntryFIFO || p.Composition == nil || len(p.Composition) != 0 {
		t.Errorf("pipe kind %q composition %+v, want fifo and none", p.Kind, p.Composition)
	}
	if v := byPath["vazio"]; !v.Newest.IsZero() || v.MainKind != "" || v.Composition == nil || len(v.Composition) != 0 {
		t.Errorf("empty folder newest %v main kind %q composition %+v, want unknown and none", v.Newest, v.MainKind, v.Composition)
	}
	if root := byPath[""]; root.ID != disco.Root || root.Name == nil || len(root.Name) != 0 {
		t.Errorf("root row %+v", root)
	}
}

// allPages pages through query with limit and returns the rows in order.
func allPages(t *testing.T, q store.Queryer, query Query, limit int) []Row {
	t.Helper()
	var out []Row
	cursor := ""
	for range 1000 {
		res, err := Page(context.Background(), q, query, cursor, limit)
		if err != nil {
			t.Fatalf("Page(%+v, %q): %v", query, cursor, err)
		}
		if len(res.Items) > limit {
			t.Fatalf("page of %d rows, limit %d", len(res.Items), limit)
		}
		out = append(out, res.Items...)
		if res.NextCursor == "" {
			return out
		}
		cursor = res.NextCursor
	}
	t.Fatal("paging did not end")
	return nil
}

func TestSortAndPaging(t *testing.T) {
	st, _, _ := corpus(t)
	type key struct {
		sort, order string
		less        func(a, b Row) int
	}
	newest := func(r Row) int64 {
		if r.Newest.IsZero() {
			return -1 << 63
		}
		return r.Newest.UnixNano()
	}
	keys := []key{
		{"", "", func(a, b Row) int {
			return cmpThen(-cmpInt(a.TotalBytes, b.TotalBytes), -cmpInt(int64(a.ID), int64(b.ID)))
		}},
		{SortBytes, OrderAsc, func(a, b Row) int {
			return cmpThen(cmpInt(a.TotalBytes, b.TotalBytes), cmpInt(int64(a.ID), int64(b.ID)))
		}},
		{SortFiles, "", func(a, b Row) int {
			return cmpThen(-cmpInt(a.TotalFiles, b.TotalFiles), -cmpInt(int64(a.ID), int64(b.ID)))
		}},
		{SortNewest, OrderDesc, func(a, b Row) int { return cmpThen(-cmpInt(newest(a), newest(b)), -cmpInt(int64(a.ID), int64(b.ID))) }},
		{SortNewest, OrderAsc, func(a, b Row) int { return cmpThen(cmpInt(newest(a), newest(b)), cmpInt(int64(a.ID), int64(b.ID))) }},
		{SortName, "", func(a, b Row) int {
			return cmpThen(strings.Compare(string(a.Name), string(b.Name)), cmpInt(int64(a.ID), int64(b.ID)))
		}},
		{SortName, OrderDesc, func(a, b Row) int {
			return cmpThen(-strings.Compare(string(a.Name), string(b.Name)), -cmpInt(int64(a.ID), int64(b.ID)))
		}},
	}
	for _, k := range keys {
		t.Run(k.sort+"_"+k.order, func(t *testing.T) {
			query := Query{Sort: k.sort, Order: k.order}
			full, err := Page(context.Background(), st.Reader(), query, "", MaxLimit)
			if err != nil {
				t.Fatal(err)
			}
			if full.NextCursor != "" || full.Count != len(full.Items) || full.CountCapped {
				t.Fatalf("one page: next %q count %d of %d", full.NextCursor, full.Count, len(full.Items))
			}
			if !slices.IsSortedFunc(full.Items, k.less) {
				t.Errorf("not in order: %q", rowPaths(full.Items))
			}
			for _, limit := range []int{1, 2, 7} {
				if paged := allPages(t, st.Reader(), query, limit); !reflect.DeepEqual(rowPaths(paged), rowPaths(full.Items)) {
					t.Errorf("limit %d pages %q\nwant %q", limit, rowPaths(paged), rowPaths(full.Items))
				}
			}
		})
	}
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpThen(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// insertEntry adds a file row and its name row as the scanner does.
func insertEntry(t *testing.T, st *store.Store, src domain.SourceID, parent domain.EntryID, path string, size int64) {
	t.Helper()
	name := path[strings.LastIndexByte(path, '/')+1:]
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, size, total_bytes, total_files,
			mtime_ns, newest_ns, oldest_ns, ext, file_kind, state, first_seen, last_seen, scan_gen)
			VALUES (?, ?, ?, ?, 'file', ?, ?, 1, 0, 0, 0, 'jpg', 'image', 'present', 0, 0, 1)`,
			string(src), int64(parent), []byte(name), []byte(path), size, size)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO entry_names (rowid, name) VALUES (?, ?)`, id, name)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCursorStableUnderInserts(t *testing.T) {
	for _, sort := range []string{SortName, SortBytes} {
		t.Run(sort, func(t *testing.T) {
			st, disco, _ := corpus(t)
			fotos := disco.ID("Fotos")
			query := Query{Within: &fotos, Ext: []string{"jpg"}, Sort: sort}
			before := allPages(t, st.Reader(), query, MaxLimit)
			if len(before) != 4 {
				t.Fatalf("seeded matches %q", rowPaths(before))
			}
			var got []Row
			cursor := ""
			for i := 0; ; i++ {
				res, err := Page(context.Background(), st.Reader(), query, cursor, 1)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, res.Items...)
				if res.NextCursor == "" {
					break
				}
				cursor = res.NextCursor
				// Rows that sort first, last, and between the seeded ones.
				for j, size := range []int64{0, 250, 1 << 40} {
					insertEntry(t, st, "disco", fotos, fmt.Sprintf("Fotos/%c-novo-%d-%d.jpg", "AMz"[j], i, j), size)
				}
			}
			seen := map[domain.EntryID]int{}
			var old []Row
			for _, r := range got {
				seen[r.ID]++
				if seen[r.ID] > 1 {
					t.Errorf("%q repeated", r.Path)
				}
				if !strings.Contains(string(r.Path), "-novo-") {
					old = append(old, r)
				}
			}
			if !reflect.DeepEqual(rowPaths(old), rowPaths(before)) {
				t.Errorf("seeded rows while inserting %q\nwant %q", rowPaths(old), rowPaths(before))
			}
		})
	}
}

func TestCountCap(t *testing.T) {
	st := storetest.Open(t)
	big := indextest.Seed(t, st, indextest.Tree{Source: "big", CreateSource: true, Nodes: []indextest.Node{
		{Path: "big", Kind: domain.EntryDirectory},
	}})
	// 10,001 files of sizes 0..10,000 in one statement: seeding them one
	// by one takes most of this package's test time under -race.
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
			INSERT INTO entries (source_id, parent_id, name, path, kind, size, total_bytes, total_files,
				mtime_ns, newest_ns, oldest_ns, ext, file_kind, state, first_seen, last_seen, scan_gen)
			SELECT 'big', ?, CAST(printf('%05d.dat', i) AS BLOB), CAST(printf('big/%05d.dat', i) AS BLOB), 'file',
				i, i, 1, 0, 0, 0, 'dat', 'other', 'present', 0, 0, 1 FROM n`, CountCap, int64(big.ID("big")))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	res, err := Page(ctx, st.Reader(), Query{Ext: []string{"dat"}}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != CountCap || !res.CountCapped || len(res.Items) != 10 || res.NextCursor == "" {
		t.Errorf("10,001 matches: count %d capped %v items %d next %q", res.Count, res.CountCapped, len(res.Items), res.NextCursor)
	}
	res, err = Page(ctx, st.Reader(), Query{Ext: []string{"dat"}, MinSize: ptr[int64](1)}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != CountCap || res.CountCapped {
		t.Errorf("10,000 matches: count %d capped %v", res.Count, res.CountCapped)
	}
	res, err = Page(ctx, st.Reader(), Query{Ext: []string{"dat"}, MaxSize: ptr[int64](9499)}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 9500 || res.CountCapped {
		t.Errorf("9,500 matches: count %d capped %v", res.Count, res.CountCapped)
	}

	ids, err := Resolve(ctx, st.Reader(), Query{Ext: []string{"dat"}}, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != CountCap+1 || !slices.IsSorted(ids) {
		t.Errorf("Resolve: %d ids, sorted %v", len(ids), slices.IsSorted(ids))
	}
	_, err = Resolve(ctx, st.Reader(), Query{Ext: []string{"dat"}}, CountCap)
	wantCode(t, err, domain.CodeInvalidRequest)
	ids, err = Resolve(ctx, st.Reader(), Query{Ext: []string{"dat"}, MinSize: ptr[int64](1)}, CountCap)
	if err != nil || len(ids) != CountCap {
		t.Errorf("Resolve at max: %d ids, %v", len(ids), err)
	}
	for _, max := range []int{0, MaxResolve + 1} {
		if _, err := Resolve(ctx, st.Reader(), Query{}, max); err == nil {
			t.Errorf("Resolve max %d: no error", max)
		}
	}
}

func TestParse(t *testing.T) {
	got, err := Parse(url.Values{
		"source": {"disco"}, "name": {"natal"}, "ext": {"jpg", "", "PNG"}, "file_kind": {"image"},
		"min_size": {"10"}, "max_size": {"20"}, "year_from": {"2004"}, "year_to": {"2006"},
		"category": {"personal_media", "documents"}, "tag": {"3", "4"}, "decision": {"keep"},
		"within": {"12"}, "sort": {"name"}, "order": {"asc"}, "cursor": {"x"}, "limit": {"5"}, "year_from_": nil,
	})
	wantCode(t, err, domain.CodeInvalidRequest) // year_from_ is unknown, even without a value

	v := url.Values{
		"source": {"disco"}, "name": {"natal"}, "ext": {"jpg", "", "PNG"}, "file_kind": {"image"},
		"min_size": {"10"}, "max_size": {"20"}, "year_from": {"2004"}, "year_to": {"2006"},
		"category": {"personal_media", "documents"}, "triage": {"review", "discard"}, "tag": {"3", "4"}, "decision": {"keep"},
		"dup":    {"copies", "", "elsewhere"},
		"within": {"12"}, "sort": {"name"}, "order": {"asc"}, "cursor": {"x"}, "limit": {"5"},
	}
	got, err = Parse(v)
	if err != nil {
		t.Fatal(err)
	}
	want := Query{
		Source: "disco", Name: "natal", Ext: []string{"jpg", "PNG"}, FileKinds: []domain.FileKind{domain.FileKindImage},
		MinSize: ptr[int64](10), MaxSize: ptr[int64](20), YearFrom: ptr(2004), YearTo: ptr(2006),
		Categories: []domain.Category{domain.CategoryPersonalMedia, domain.CategoryDocuments},
		Triages:    []domain.Triage{domain.TriageReview, domain.TriageDiscard},
		Tags:       []int64{3, 4}, Decisions: []domain.Decision{domain.DecisionKeep}, Within: ptr(domain.EntryID(12)),
		Dup:  []domain.DupFilter{domain.DupCopies, domain.DupElsewhere},
		Sort: SortName, Order: OrderAsc,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse\n got %+v\nwant %+v", got, want)
	}

	// The JSON form, which selections store, round-trips with the same
	// names, and the entry ID as a string like every API entry ID.
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"within":"12"`) || !strings.Contains(string(b), `"file_kind":["image"]`) ||
		!strings.Contains(string(b), `"dup":["copies","elsewhere"]`) {
		t.Errorf("JSON %s", b)
	}
	var back Query
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, want) {
		t.Errorf("JSON round trip %+v, %v", back, err)
	}

	empty, err := Parse(url.Values{"name": {""}, "within": {""}, "tag": {""}})
	if err != nil || !reflect.DeepEqual(empty, Query{}) {
		t.Errorf("empty values: %+v, %v", empty, err)
	}

	bad := []url.Values{
		{"color": {"red"}},
		{"name": {"a", "b"}},
		{"name": {"\xff"}},
		{"ext": {"."}},
		{"file_kind": {"picture"}},
		{"category": {"photos"}},
		{"triage": {"maybe"}},
		{"decision": {"inherit"}},
		{"tag": {"x"}},
		{"tag": {"0"}},
		{"min_size": {"-1"}},
		{"max_size": {"1k"}},
		{"min_size": {"5"}, "max_size": {"4"}},
		{"year_from": {"0"}},
		{"year_to": {"10000"}},
		{"year_from": {"2006"}, "year_to": {"2004"}},
		{"year_from": {"MMIV"}},
		{"within": {"abc"}},
		{"within": {"-3"}},
		{"sort": {"color"}},
		{"order": {"up"}},
		{"dup": {"keeper"}},
		{"dup": {"Copies"}},
		// "copies outside this folder" needs the folder.
		{"dup": {"elsewhere"}},
		{"dup": {"unique", "elsewhere"}, "source": {"disco"}},
	}
	for _, v := range bad {
		_, err := Parse(v)
		wantCode(t, err, domain.CodeInvalidRequest)
	}
}

func TestBadRequests(t *testing.T) {
	st, disco, _ := corpus(t)
	ctx := context.Background()
	page := func(q Query, cursor string) error {
		_, err := Page(ctx, st.Reader(), q, cursor, 2)
		return err
	}
	// Queries built without Parse (a selection's JSON) are validated too.
	wantCode(t, page(Query{Decisions: []domain.Decision{"maybe"}}, ""), domain.CodeInvalidRequest)
	wantCode(t, page(Query{Dup: []domain.DupFilter{domain.DupElsewhere}}, ""), domain.CodeInvalidRequest)
	wantCode(t, page(Query{Dup: []domain.DupFilter{"keeper"}}, ""), domain.CodeInvalidRequest)
	_, err := Resolve(ctx, st.Reader(), Query{MinSize: ptr[int64](-1)}, 10)
	wantCode(t, err, domain.CodeInvalidRequest)
	wantCode(t, page(Query{Within: ptr(domain.EntryID(987654))}, ""), domain.CodeNotFound)

	res, err := Page(ctx, st.Reader(), Query{Source: "disco", Sort: SortName}, "", 2)
	if err != nil || res.NextCursor == "" {
		t.Fatalf("first page: %v", err)
	}
	for _, c := range []string{"!!!", "e30", "bm90IGpzb24", encodeCursor(sortSpec{key: SortName}, position{ID: 0})} {
		wantCode(t, page(Query{Source: "disco", Sort: SortName}, c), domain.CodeInvalidRequest)
	}
	// A cursor of another order.
	wantCode(t, page(Query{Source: "disco", Sort: SortBytes}, res.NextCursor), domain.CodeInvalidRequest)
	wantCode(t, page(Query{Source: "disco", Sort: SortName, Order: OrderDesc}, res.NextCursor), domain.CodeInvalidRequest)
	// The same order continues, whatever the filters.
	if err := page(Query{Within: ptr(disco.ID("Fotos")), Sort: SortName}, res.NextCursor); err != nil {
		t.Errorf("same order: %v", err)
	}
}
