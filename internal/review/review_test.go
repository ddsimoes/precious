package review

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// 5.1: Backup_PC_2004/C/WINDOWS is an operating-system copy holding
// system32, which the rules also match: programs has one row for WINDOWS
// and none below it.
func TestRefreshProgramsHoldsWindowsOnce(t *testing.T) {
	w := newCorpusWorld(t)
	const windows = "Backup_PC_2004/C/WINDOWS"
	var n int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM entries WHERE source_id = 'corpus'
		AND path > CAST(? AS BLOB) AND path < CAST(? AS BLOB) AND category IN ('os_installation', 'application_installation')`,
		windows+"/", windows+"0").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("the rules match nothing below %s; the test proves nothing", windows)
	}
	var rows int
	for _, r := range w.all(ListPrograms, "", false, 100) {
		p := w.path(r.Entry)
		if p == windows {
			rows++
			if r.Bytes != w.corpusTotals(windows) {
				t.Errorf("WINDOWS row has %d bytes, want its total %d", r.Bytes, w.corpusTotals(windows))
			}
		}
		if strings.HasPrefix(p, windows+"/") {
			t.Errorf("programs lists %s below WINDOWS", p)
		}
	}
	if rows != 1 {
		t.Fatalf("programs has %d rows for WINDOWS, want 1", rows)
	}
}

// corpusTotals is the bytes of the corpus files at or below p.
func (w *corpusWorld) corpusTotals(p string) int64 {
	var n int64
	for q, s := range w.sizes {
		if (q == p || strings.HasPrefix(q, p+"/")) && !strings.Contains(q, "!") {
			n += s
		}
	}
	return n
}

// Spec "Opportunity cards", scenario "A downloads folder is not an
// installer" (r2d D1): every installer and disk image the ground truth
// asserts, Downloads/Setup.exe and the ISOs among them, is a row of the
// installers card with its own bytes; every row is an installer_download
// entry; and no row is a downloads folder, which keeps its category.
func TestDownloadsFolderIsNotAnInstaller(t *testing.T) {
	w := newCorpusWorld(t)
	rows := map[string]int64{}
	for _, r := range w.all(ListInstallers, "", false, 3) {
		rows[w.path(r.Entry)] = r.Bytes
		var category string
		if err := w.st.Reader().QueryRow(`SELECT coalesce(category, '') FROM entries WHERE id = ?`, r.Entry).Scan(&category); err != nil {
			t.Fatal(err)
		}
		if category != string(domain.CategoryInstallerDownload) {
			t.Errorf("installers lists %s, of category %q", w.path(r.Entry), category)
		}
	}
	var want []string
	for _, e := range w.gt.Entries {
		if e.Category == string(domain.CategoryInstallerDownload) {
			want = append(want, raw(t, e.PathB64))
		}
	}
	for _, p := range []string{"Downloads/Setup.exe", "ISOs/Windows XP Professional SP2.iso", "ISOs/Office 2003.iso",
		"ISOs/copia/Windows XP Professional SP2.iso"} {
		if !slices.Contains(want, p) {
			t.Fatalf("the ground truth does not assert %s an installer; the test proves less than it says", p)
		}
	}
	for _, p := range want {
		if bytes, ok := rows[p]; !ok || bytes != w.sizes[p] {
			t.Errorf("installers row %s: listed %v with %d bytes, want its %d bytes", p, ok, bytes, w.sizes[p])
		}
	}
	folders, err := w.st.Reader().Query(`SELECT path FROM entries WHERE category = ?`, domain.CategoryDownloadCollection)
	if err != nil {
		t.Fatal(err)
	}
	defer folders.Close()
	var downloads []string
	for folders.Next() {
		var p []byte
		if err := folders.Scan(&p); err != nil {
			t.Fatal(err)
		}
		downloads = append(downloads, string(p))
	}
	if err := folders.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(downloads, "Downloads") {
		t.Fatalf("downloads folders %q, want Downloads among them", downloads)
	}
	for _, p := range downloads {
		if _, ok := rows[p]; ok {
			t.Errorf("installers lists the downloads folder %s", p)
		}
	}
}

// 5.1: no entry counts twice in a card: an entry card names each entry
// once and never an entry below another of its rows; duplicates names each
// relation and each content once.
func TestRefreshCountsNoEntryTwiceInACard(t *testing.T) {
	w := newCorpusWorld(t)
	for _, l := range CardLists {
		rows := w.all(l, "", false, 3)
		if len(rows) == 0 {
			t.Errorf("%s has no rows on the corpus", l)
		}
		seen := map[string]bool{}
		for _, r := range rows {
			var k string
			switch {
			case r.Entry != 0:
				k = string(r.Source) + "\x00" + w.path(r.Entry)
			case r.Relation != 0:
				k = fmt.Sprint("relation ", r.Relation)
			default:
				k = fmt.Sprint("content ", r.Content)
			}
			if seen[k] {
				t.Errorf("%s lists %q twice", l, k)
			}
			seen[k] = true
		}
		for k := range seen {
			for o := range seen {
				if o != k && strings.HasPrefix(k, o+"/") {
					t.Errorf("%s lists %q below its row %q", l, k, o)
				}
			}
		}
	}
}

// 5.1: every empty folder (no files, not unreadable, no mount boundary)
// and every zero-byte file is a leftovers row or below one, and every
// leftovers row is one of those or a partial download.
func TestRefreshPutsEmptyFoldersAndZeroByteFilesInLeftovers(t *testing.T) {
	w := newCorpusWorld(t)
	files := map[string]int{}
	unreadable := map[string]bool{}
	var dirs, zero []string
	for _, e := range w.gt.Entries {
		p := raw(t, e.PathB64)
		switch {
		case e.Kind == domain.EntryFile:
			for d := p; strings.Contains(d, "/"); {
				d = d[:strings.LastIndexByte(d, '/')]
				files[d]++
			}
			if *e.Size == 0 {
				zero = append(zero, p)
			}
		case e.Kind == domain.EntryDirectory && e.Unreadable:
			for d := p; strings.Contains(d, "/"); {
				d = d[:strings.LastIndexByte(d, '/')]
				unreadable[d] = true
			}
			unreadable[p] = true
		case e.Kind == domain.EntryDirectory:
			dirs = append(dirs, p)
		}
	}
	var empty []string
	for _, d := range dirs {
		if files[d] == 0 && !unreadable[d] {
			empty = append(empty, d)
		}
	}
	if len(empty) == 0 || len(zero) == 0 {
		t.Fatalf("the corpus has %d empty folders and %d zero-byte files; the test proves nothing", len(empty), len(zero))
	}
	rows := map[string]bool{}
	for _, r := range w.all(ListLeftovers, "corpus", false, 100) {
		rows[w.path(r.Entry)] = true
	}
	covered := func(p string) bool {
		for q := p; ; q = q[:strings.LastIndexByte(q, '/')] {
			if rows[q] {
				return true
			}
			if !strings.Contains(q, "/") {
				return false
			}
		}
	}
	for _, p := range append(slices.Clone(empty), zero...) {
		if !covered(p) {
			t.Errorf("%q is not in leftovers", p)
		}
	}
	for p := range rows {
		if !slices.Contains(empty, p) && !slices.Contains(zero, p) && !strings.HasSuffix(p, ".part") {
			t.Errorf("leftovers lists %q, no empty folder, zero-byte file, or partial download", p)
		}
	}
	if !rows["Downloads/filme.avi.part"] {
		t.Error("the partial download is not in leftovers")
	}
	for _, r := range w.all(ListCaches, "", false, 100) {
		if strings.HasSuffix(w.path(r.Entry), ".part") {
			t.Errorf("caches lists the partial download %s", w.path(r.Entry))
		}
	}
}

// 5.1: the duplicates card lists each same or inside relation, and the
// duplicate groups with a copy outside every one of them, with the
// redundant bytes no relation counts.
func TestRefreshDuplicatesExcludeTheFilesOfListedRelations(t *testing.T) {
	w := newCorpusWorld(t)
	var sides []string
	wantRels := map[int64]bool{}
	for i, r := range w.gt.Relations {
		if r.Kind == "same" || r.Kind == "inside" {
			sides = append(sides, raw(t, r.A.PathB64), raw(t, r.B.PathB64))
			wantRels[w.rels[i]] = true
		}
	}
	inside := func(copy string) bool {
		file, _, _ := strings.Cut(copy, "!")
		for _, s := range sides {
			if file == s || strings.HasPrefix(file, s+"/") {
				return true
			}
		}
		return false
	}
	want := map[string]int64{} // sha256 → bytes
	var excluded int
	for _, d := range w.gt.Duplicates {
		var in, out int64
		for _, c := range d.Copies {
			if inside(raw(t, c.PathB64)) {
				in++
			} else {
				out++
			}
		}
		if d.SHA256 == w.penCopy.SHA256 {
			out++ // pen's copy
		}
		if out == 0 {
			excluded++
			continue
		}
		n := out
		if in == 0 {
			n--
		}
		want[d.SHA256] = d.Size * n
	}
	if excluded == 0 || len(wantRels) == 0 {
		t.Fatalf("%d groups lie inside relations and %d relations are listed; the test proves nothing", excluded, len(wantRels))
	}
	got := map[string]int64{}
	gotRels := map[int64]bool{}
	for _, r := range w.all(ListDuplicates, "", false, 7) {
		if r.Relation != 0 {
			gotRels[r.Relation] = true
			continue
		}
		var sum []byte
		if err := w.st.Reader().QueryRow(`SELECT sha256 FROM contents WHERE id = ?`, r.Content).Scan(&sum); err != nil {
			t.Fatal(err)
		}
		got[hex.EncodeToString(sum)] = r.Bytes
		if r.Copy == (domain.Ref{}) {
			t.Errorf("content row %d names no copy", r.ID)
		}
	}
	if !maps(gotRels, wantRels) {
		t.Errorf("relation rows %v, want %v", gotRels, wantRels)
	}
	for sum, b := range want {
		if got[sum] != b {
			t.Errorf("group %s: row bytes %d, want %d", sum[:12], got[sum], b)
		}
	}
	for sum := range got {
		if _, ok := want[sum]; !ok {
			t.Errorf("group %s is listed, but every copy lies inside a listed relation", sum[:12])
		}
	}
}

func maps[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// copiesWorld is source "d": folders A and B with the same two files, x
// and y, and a third copy of x in C, with A same B listed.
type copiesWorld struct {
	*world
	d   *indextest.Seeded
	rel int64
}

func newCopiesWorld(t *testing.T) *copiesWorld {
	w := &copiesWorld{world: newWorld(t)}
	w.d = indextest.Seed(t, w.st, indextest.Tree{Source: "d", CreateSource: true, MountPoint: "/mnt/d", Nodes: []indextest.Node{
		{Path: "A/x.jpg", Size: 1000}, {Path: "A/y.jpg", Size: 2000},
		{Path: "B/x.jpg", Size: 1000}, {Path: "B/y.jpg", Size: 2000},
		{Path: "C/x.jpg", Size: 1000},
	}})
	for _, p := range []string{"A/x.jpg", "B/x.jpg", "C/x.jpg"} {
		w.d.SetContent(w.st, p, indextest.Content{State: domain.ContentHashed, SHA256: digest("x")})
	}
	for _, p := range []string{"A/y.jpg", "B/y.jpg"} {
		w.d.SetContent(w.st, p, indextest.Content{State: domain.ContentHashed, SHA256: digest("y")})
	}
	w.rel = w.relation("same", w.d.ID("A"), w.d.ID("B"))
	w.refresh()
	return w
}

// A three-copy group with two copies inside a listed relation adds one
// copy's size to the relation's redundant bytes: the card is exactly the
// bytes that are copies of something else, no byte counted twice.
func TestDuplicatesGroupBesideARelationCountsEachByteOnce(t *testing.T) {
	w := newCopiesWorld(t)
	rows := w.all(ListDuplicates, "", false, 10)
	if len(rows) != 2 {
		t.Fatalf("duplicates has %d rows, want the relation and x's group", len(rows))
	}
	var rel, group Row
	for _, r := range rows {
		if r.Relation != 0 {
			rel = r
		} else {
			group = r
		}
	}
	if rel.Relation != w.rel || rel.Bytes != 3000 {
		t.Errorf("relation row %+v, want relation %d with A's 3000 bytes", rel, w.rel)
	}
	if group.Bytes != 1000 || group.Files != 1 {
		t.Errorf("x's group row has %d bytes in %d files, want one copy: 1000 in 1", group.Bytes, group.Files)
	}
	// 7000 bytes hold 3000 bytes of distinct content.
	if c := w.card(ListDuplicates, ""); c.Bytes != 4000 || c.Rows != 2 || c.Basis != BasisContent {
		t.Errorf("duplicates card %+v, want 4000 bytes in 2 rows, basis content", c)
	}
}

// 5.2: a duplicates row stays open while two of its copies are undecided;
// a relation row while both sides are.
func TestDuplicatesRowStaysOpenWhileTwoCopiesAreUndecided(t *testing.T) {
	w := newCopiesWorld(t)
	w.decide(w.d.ID("C/x.jpg"), domain.DecisionDiscard)
	if c := w.card(ListDuplicates, ""); c.Bytes != 4000 || c.Rows != 2 {
		t.Fatalf("after discarding C/x.jpg the card is %+v, want both rows open (A/x.jpg and B/x.jpg undecided)", c)
	}
	w.decide(w.d.ID("B"), domain.DecisionKeep)
	if c := w.card(ListDuplicates, ""); c.Bytes != 0 || c.Rows != 0 {
		t.Fatalf("after keeping B the card is %+v, want no open row", c)
	}
	decided := w.all(ListDuplicates, "", true, 10)
	if len(decided) != 2 {
		t.Fatalf("decided lists %d rows, want 2", len(decided))
	}
}

// A hard-link set is one copy: discarding the only other copy closes the
// row, and the group's bytes count the set once.
func TestHardLinksAreOneCopy(t *testing.T) {
	w := newWorld(t)
	d := indextest.Seed(t, w.st, indextest.Tree{Source: "d", CreateSource: true, MountPoint: "/mnt/d", Nodes: []indextest.Node{
		{Path: "H/a.jpg", Size: 500}, {Path: "H/b.jpg", Size: 500}, {Path: "G/c.jpg", Size: 500},
	}})
	w.exec(`UPDATE entries SET dev = 7, ino = 42, nlink = 2 WHERE id IN (?, ?)`, d.ID("H/a.jpg"), d.ID("H/b.jpg"))
	for _, p := range []string{"H/a.jpg", "H/b.jpg", "G/c.jpg"} {
		d.SetContent(w.st, p, indextest.Content{State: domain.ContentHashed, SHA256: digest("h")})
	}
	w.refresh()
	if c := w.card(ListDuplicates, ""); c.Bytes != 500 || c.Rows != 1 {
		t.Fatalf("card %+v, want one row of 500 bytes (two copies)", c)
	}
	w.decide(d.ID("G/c.jpg"), domain.DecisionDiscard)
	if c := w.card(ListDuplicates, ""); c.Rows != 0 {
		t.Fatalf("card %+v after discarding the only other copy, want no open row", c)
	}
}

// 5.2: deciding a row removes it from its list and shrinks its card by its
// bytes; decided lists it.
func TestDecidingARowShrinksItsCard(t *testing.T) {
	w := newCorpusWorld(t)
	before := w.card(ListSystemJunk, "")
	rows := w.all(ListSystemJunk, "", false, 100)
	victim := rows[0]
	w.decide(victim.Entry, domain.DecisionDiscard)
	after := w.card(ListSystemJunk, "")
	if after.Bytes != before.Bytes-victim.Bytes || after.Rows != before.Rows-1 {
		t.Fatalf("card %+v after discarding a %d-byte row, was %+v", after, victim.Bytes, before)
	}
	for _, r := range w.all(ListSystemJunk, "", false, 100) {
		if r.ID == victim.ID {
			t.Fatal("the discarded row is still open")
		}
	}
	decided := w.all(ListSystemJunk, "", true, 100)
	if len(decided) != 1 || decided[0].ID != victim.ID {
		t.Fatalf("decided lists %+v, want the discarded row", decided)
	}
	// Inheriting a decision closes a row too.
	sub := w.all(ListSystemJunk, "", false, 100)[0]
	parent := w.path(sub.Entry)
	if i := strings.LastIndexByte(parent, '/'); i > 0 {
		w.decide(w.corpus.ID(parent[:i]), domain.DecisionKeep)
		for _, r := range w.all(ListSystemJunk, "", false, 100) {
			if r.ID == sub.ID {
				t.Fatal("a row inside a kept folder is still open")
			}
		}
	}
}

// 5.2: a source filter counts only rows that touch that source: its
// entries' rows, and duplicates rows with a copy there.
func TestSourceFilterCountsTheSourcesRows(t *testing.T) {
	w := newCorpusWorld(t)
	if c := w.card(ListSystemJunk, "pen"); c.Bytes != 4093 || c.Rows != 1 {
		t.Errorf("pen's system junk card %+v, want Thumbs.db: 4093 bytes in 1 row", c)
	}
	if c := w.card(ListCaches, "pen"); c.Bytes != 3004 || c.Rows != 1 {
		t.Errorf("pen's caches card %+v, want its cache folder: 3004 bytes in 1 row", c)
	}
	all, corp := w.card(ListSystemJunk, ""), w.card(ListSystemJunk, "corpus")
	if corp.Bytes != all.Bytes-4093 || corp.Rows != all.Rows-1 {
		t.Errorf("corpus's system junk card %+v, all sources %+v: want all but pen's row", corp, all)
	}
	dups := w.all(ListDuplicates, "pen", false, 10)
	if len(dups) != 1 || dups[0].Content == 0 {
		t.Fatalf("pen's duplicates %+v, want the group of its copy", dups)
	}
	var sum []byte
	if err := w.st.Reader().QueryRow(`SELECT sha256 FROM contents WHERE id = ?`, dups[0].Content).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(sum) != w.penCopy.SHA256 {
		t.Errorf("pen's duplicates row is content %x, want its copy's", sum)
	}
	for _, l := range CardLists {
		for _, r := range w.all(l, "pen", false, 50) {
			if r.Entry != 0 && r.Source != "pen" {
				t.Errorf("%s for pen lists %s's entry %d", l, r.Source, r.Entry)
			}
		}
	}
}

// Rows refuses an unknown list and a malformed cursor; Resolve refuses
// duplicates.
func TestRowsRefuseBadRequests(t *testing.T) {
	w := newCopiesWorld(t)
	ctx := context.Background()
	if _, err := Rows(ctx, w.st.Reader(), "junk", "", false, "", 10); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Errorf("unknown list: %v", err)
	}
	if _, err := Rows(ctx, w.st.Reader(), ListDuplicates, "", false, "!!", 10); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Errorf("malformed cursor: %v", err)
	}
	if _, err := Resolve(ctx, w.st.Reader(), ListDuplicates, "", 10); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Errorf("resolve duplicates: %v", err)
	}
}

// A refresh skips the rows of entries deleted since the relations were
// computed, and replaces a generation an interrupted run left.
func TestRefreshSkipsDeletedEntriesAndRedoesItsGeneration(t *testing.T) {
	w := newCopiesWorld(t)
	w.relation("same", w.d.ID("A"), w.d.ID("B"))
	if err := Refresh(context.Background(), w.st, w.gen+1); err != nil {
		t.Fatal(err)
	}
	w.exec(`DELETE FROM entries WHERE id = ?`, w.d.ID("C/x.jpg"))
	w.refresh() // the same generation again
	if c := w.card(ListDuplicates, ""); c.Bytes != 3000 || c.Rows != 1 {
		t.Fatalf("card %+v, want only the relation once x has no copy outside it", c)
	}
	var n int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM review_rows WHERE gen = ?`, w.gen).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("generation %d has %d rows, want 1", w.gen, n)
	}
	if err := Refresh(context.Background(), w.st, w.gen); err == nil {
		t.Fatal("refreshing the visible generation succeeded")
	}
}

// R2.5: on the seeded corpus, every card's bytes and row count equal the
// sums over its review list read through all its pages, for all sources and
// for each one, with some rows decided; and its decided bytes and rows
// equal the sums over its decided list (r2b D13). The rescue card holds a
// row kept (decided) and a row whose group is discarded (open).
func TestR2_5CardBytesEqualTheirLists(t *testing.T) {
	w := newCorpusWorld(t)
	w.decide(w.all(ListSystemJunk, "", false, 1)[0].Entry, domain.DecisionDiscard)
	w.decide(w.corpus.ID("Downloads/Setup(1).exe"), domain.DecisionDiscard)
	w.decide(w.corpus.ID("Projetos/app_react"), domain.DecisionKeep)
	w.decide(w.corpus.ID(office), domain.DecisionDiscard)
	w.decide(w.corpus.ID(saveFolder), domain.DecisionKeep)
	sum := func(rows []Row) (bytes, n int64) {
		for _, r := range rows {
			bytes += r.Bytes
			n++
		}
		return bytes, n
	}
	for _, src := range []domain.SourceID{"", "corpus", "pen"} {
		cards, err := Cards(context.Background(), w.st.Reader(), src)
		if err != nil {
			t.Fatal(err)
		}
		if len(cards) != len(CardLists) {
			t.Fatalf("%d cards, want %d", len(cards), len(CardLists))
		}
		decided := map[List]int64{}
		for i, c := range cards {
			if i > 0 && c.Bytes > cards[i-1].Bytes && (i > 1 || cards[0].List != ListRescue || cards[0].Rows == 0) {
				t.Errorf("source %q: card %s is larger than the one before", src, c.List)
			}
			if bytes, n := sum(w.all(c.List, src, false, 2)); bytes != c.Bytes || n != c.Rows {
				t.Errorf("source %q: card %s has %d bytes in %d rows, its list %d in %d", src, c.List, c.Bytes, c.Rows, bytes, n)
			}
			if bytes, n := sum(w.all(c.List, src, true, 2)); bytes != c.DecidedBytes || n != c.DecidedRows {
				t.Errorf("source %q: card %s has %d decided bytes in %d rows, its decided list %d in %d", src, c.List,
					c.DecidedBytes, c.DecidedRows, bytes, n)
			}
			if src == "" && c.Rows == 0 {
				t.Errorf("card %s is empty on the corpus", c.List)
			}
			decided[c.List] = c.DecidedRows
		}
		if src != "pen" && (decided[ListSystemJunk] == 0 || decided[ListRescue] != 1) {
			t.Errorf("source %q: decided rows %v, want the discarded system junk row and the kept rescue row among them",
				src, decided)
		}
	}
}

// The corpus paths the rescue tests decide.
const (
	office     = "Backup_PC_2004/C/Arquivos de programas/Microsoft Office"
	orcamento  = office + "/OFFICE11/Meu orcamento casamento.xls"
	saveFolder = "Jogos/Need for Speed Underground 2/save"
)

// R2.6: the rescue card lists the ground truth's rows, each inside its
// outermost group, in the card's order (largest first, then by path), the
// spreadsheet inside Microsoft Office among them, and nothing of pen.
func TestR2_6RescueListsTheOwnersFilesInsidePrograms(t *testing.T) {
	w := newCorpusWorld(t)
	var got, want []string
	for _, r := range w.all(ListRescue, "", false, 2) {
		got = append(got, w.path(r.Entry)+" inside "+w.path(r.Group))
		if r.SortKey != r.Bytes {
			t.Errorf("%s: sort key %d, bytes %d", w.path(r.Entry), r.SortKey, r.Bytes)
		}
	}
	// The rescue paths are valid UTF-8, so their display and raw forms agree.
	for _, r := range w.gt.Rescue {
		want = append(want, r.Path+" inside "+r.Group)
	}
	if !slices.Equal(got, want) {
		t.Errorf("rescue:\n got %q\nwant %q", got, want)
	}
	if !slices.Contains(got, orcamento+" inside "+office) {
		t.Errorf("rescue does not list %s inside %s", orcamento, office)
	}
	if rows := w.all(ListRescue, "pen", false, 10); len(rows) != 0 {
		t.Errorf("pen's rescue rows %+v, want none", rows)
	}
}

// r2c D2: a rescue row stays open until the owner decides its file or the
// file is kept. An inherited discard or later leaves it open; an inherited
// keep closes it; the file's own keep, discard, or later closes it; its own
// explicit undecided leaves it open.
func TestRescueRowStaysOpenUntilTheFileIsDecided(t *testing.T) {
	w := newCorpusWorld(t)
	file, group := w.corpus.ID(orcamento), w.corpus.ID(office)
	check := func(step string, wantOpen bool, wantEff domain.Decision) {
		t.Helper()
		open := slices.ContainsFunc(w.all(ListRescue, "", false, 50), func(r Row) bool { return r.Entry == file })
		decided := slices.ContainsFunc(w.all(ListRescue, "", true, 50), func(r Row) bool { return r.Entry == file })
		if open != wantOpen || decided == wantOpen {
			t.Errorf("%s: open %v, decided %v; want open %v", step, open, decided, wantOpen)
		}
		var eff string
		if err := w.st.Reader().QueryRow(`SELECT eff_decision FROM entries WHERE id = ?`, file).Scan(&eff); err != nil {
			t.Fatal(err)
		}
		if domain.Decision(eff) != wantEff {
			t.Errorf("%s: effective decision %s, want %s", step, eff, wantEff)
		}
	}
	check("no decision", true, domain.DecisionUndecided)
	w.decide(group, domain.DecisionDiscard)
	check("inherited discard", true, domain.DecisionDiscard)
	w.decide(group, domain.DecisionLater)
	check("inherited later", true, domain.DecisionLater)
	w.decide(group, domain.DecisionKeep)
	check("inherited keep", false, domain.DecisionKeep)
	w.decide(group, domain.DecisionDiscard)
	for _, d := range []domain.Decision{domain.DecisionKeep, domain.DecisionDiscard, domain.DecisionLater} {
		w.decide(file, d)
		check("own "+string(d), false, d)
	}
	w.decide(file, domain.DecisionUndecided)
	check("own undecided", true, domain.DecisionUndecided)
	w.inherit(file)
	check("inherit again", true, domain.DecisionDiscard)
}

// r2c D3: the rescue card comes first while it has open rows, however few
// its bytes; once every row is decided, it ranks by its bytes like the
// others.
func TestRescueCardFirstWhileOpen(t *testing.T) {
	w := newCorpusWorld(t)
	cards, err := Cards(context.Background(), w.st.Reader(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cards[0].List != ListRescue || cards[0].Rows != int64(len(w.gt.Rescue)) {
		t.Fatalf("first card %+v, want rescue with %d open rows", cards[0], len(w.gt.Rescue))
	}
	if cards[1].Bytes <= cards[0].Bytes {
		t.Fatalf("cards %+v: the rescue card is not smaller than the next, so the order proves nothing", cards)
	}
	for _, r := range w.all(ListRescue, "", false, 50) {
		w.decide(r.Entry, domain.DecisionKeep)
	}
	if cards, err = Cards(context.Background(), w.st.Reader(), ""); err != nil {
		t.Fatal(err)
	}
	var rescue int
	for i, c := range cards {
		if i > 0 && c.Bytes > cards[i-1].Bytes {
			t.Errorf("card %s is larger than the one before: %+v", c.List, cards)
		}
		if c.List == ListRescue {
			rescue = i
		}
	}
	if c := cards[rescue]; rescue == 0 || c.Rows != 0 || c.DecidedRows != int64(len(w.gt.Rescue)) {
		t.Errorf("rescue card at %d: %+v, want ranked by its 0 bytes with every row decided", rescue, c)
	}
}

// A member is a copy that reads its archive's decision: discarding the
// archive leaves one undecided copy and closes the row.
func TestMemberCopiesReadTheirArchivesDecision(t *testing.T) {
	w := newWorld(t)
	d := indextest.Seed(t, w.st, indextest.Tree{Source: "d", CreateSource: true, MountPoint: "/mnt/d", Nodes: []indextest.Node{
		{Path: "Z.zip", Size: 900}, {Path: "P/p.jpg", Size: 700},
	}})
	d.SetContent(w.st, "Z.zip", indextest.Content{State: domain.ContentUniqueSize})
	d.SetContent(w.st, "P/p.jpg", indextest.Content{State: domain.ContentHashed, SHA256: digest("p")})
	d.SeedArchive(w.st, "Z.zip", indextest.Archive{Format: domain.ArchiveZip, Members: []indextest.Member{
		{Path: "fotos/p.jpg", Size: 700, Content: indextest.Content{State: domain.ContentHashed, SHA256: digest("p")}},
	}})
	w.refresh()
	rows := w.all(ListDuplicates, "d", false, 10)
	if len(rows) != 1 || rows[0].Bytes != 700 || rows[0].Copy != (domain.Ref{Entry: d.ID("P/p.jpg")}) {
		t.Fatalf("duplicates %+v, want p's group of 700 bytes naming P/p.jpg", rows)
	}
	w.decide(d.ID("Z.zip"), domain.DecisionDiscard)
	if c := w.card(ListDuplicates, ""); c.Rows != 0 {
		t.Fatalf("card %+v after discarding the archive, want no open row", c)
	}
}
