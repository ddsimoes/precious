package search

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	regress "precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// The files the content corpus marks not checked, with their states: none
// is in a duplicate group, so the groups stay as the ground truth has them.
var uncheckedCorpus = map[string]domain.ContentState{
	"Documentos/LEIAME.TXT":                                            domain.ContentPending,
	"Backup_PC_2004/C/Arquivos de programas/ICQ/icq.exe":               domain.ContentChanged,
	"Backup_PC_2004/C/Arquivos de programas/Mozilla Firefox/xpcom.dll": domain.ContentUnreadable,
}

// contentCorpus seeds the regression corpus as source "corpus" with the
// content states of a complete hashing run (SeedContent), then marks the
// files of uncheckedCorpus not checked.
func contentCorpus(t *testing.T) (*store.Store, *indextest.Seeded, regress.GroundTruth) {
	t.Helper()
	st := storetest.Open(t)
	gt := regress.Corpus().GroundTruth()
	s := indextest.Seed(t, st, indextest.Tree{Source: "corpus", CreateSource: true, MountPoint: "/corpus",
		Nodes: indextest.CorpusNodes(t, gt)})
	s.SeedContent(st, gt)
	for p, state := range uncheckedCorpus {
		s.SetContent(st, p, indextest.Content{State: state})
	}
	return st, s, gt
}

// matchPaths returns the raw paths of every match of q, sorted.
func matchPaths(t *testing.T, st *store.Store, q Query) []string {
	t.Helper()
	ids, err := Resolve(context.Background(), st.Reader(), q, MaxResolve)
	if err != nil {
		t.Fatalf("Resolve(%+v): %v", q, err)
	}
	var out []string
	for _, id := range ids {
		var p []byte
		if err := st.Reader().QueryRow(`SELECT path FROM entries WHERE id = ?`, int64(id)).Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, string(p))
	}
	slices.Sort(out)
	return out
}

func rawPath(t *testing.T, p regress.Path) string {
	t.Helper()
	e := regress.Entry{PathB64: p.PathB64}
	raw, err := e.RawPath()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The duplicate filter matches the content states SeedContent wrote:
// copies are the ground truth's duplicated files, unique every other
// non-empty file, unchecked the files marked so; elsewhere within
// "Fotos - Copia" is every file there with a copy outside it, which is every
// photo but DSC_editada.JPG. Each value works alone, values combine with
// OR, and a selection's stored query resolves to the same entries.
func TestDupFilter(t *testing.T) {
	st, s, gt := contentCorpus(t)

	var copies, unique, unchecked, elsewhere []string
	dup := map[string]bool{}
	const copia = "Fotos - Copia/"
	for _, d := range gt.Duplicates {
		paths := make([]string, len(d.Copies))
		outside := false
		for i, c := range d.Copies {
			paths[i] = rawPath(t, c)
			if !strings.HasPrefix(paths[i], copia) {
				outside = true
			}
		}
		for _, p := range paths {
			if strings.Contains(p, "!") || uncheckedCorpus[p] != "" {
				continue
			}
			dup[p] = true
			copies = append(copies, p)
			if outside && strings.HasPrefix(p, copia) {
				elsewhere = append(elsewhere, p)
			}
		}
	}
	for _, e := range gt.Entries {
		p := rawPath(t, regress.Path{PathB64: e.PathB64})
		switch {
		case e.Kind != domain.EntryFile || e.Size == nil || *e.Size == 0:
		case uncheckedCorpus[p] != "":
			unchecked = append(unchecked, p)
		case !dup[p]:
			unique = append(unique, p)
		}
	}
	for _, l := range [][]string{copies, unique, unchecked, elsewhere} {
		slices.Sort(l)
	}

	check := func(what string, q Query, want []string) {
		t.Helper()
		if got := matchPaths(t, st, q); !slices.Equal(got, want) {
			t.Errorf("%s: %d matches, want %d\n got %q\nwant %q", what, len(got), len(want), got, want)
		}
	}
	dups := func(d ...domain.DupFilter) []domain.DupFilter { return d }
	check("copies", Query{Dup: dups(domain.DupCopies)}, copies)
	check("unique", Query{Dup: dups(domain.DupUnique)}, unique)
	check("unchecked", Query{Dup: dups(domain.DupUnchecked)}, unchecked)
	both := slices.Sorted(slices.Values(append(slices.Clone(copies), unchecked...)))
	check("copies or unchecked", Query{Dup: dups(domain.DupCopies, domain.DupUnchecked)}, both)

	fotosCopia := s.ID("Fotos - Copia")
	check("elsewhere", Query{Within: &fotosCopia, Dup: dups(domain.DupElsewhere)}, elsewhere)
	var photos []string
	for _, p := range elsewhere {
		if strings.HasSuffix(p, ".JPG") {
			photos = append(photos, p)
		}
	}
	var wantPhotos []string
	for _, e := range gt.Entries {
		p := rawPath(t, regress.Path{PathB64: e.PathB64})
		if strings.HasPrefix(p, copia) && strings.HasSuffix(p, ".JPG") && !strings.HasSuffix(p, "/DSC_editada.JPG") {
			wantPhotos = append(wantPhotos, p)
		}
	}
	slices.Sort(wantPhotos)
	if !slices.Equal(photos, wantPhotos) || len(photos) < 30 {
		t.Errorf("elsewhere within Fotos - Copia: photos %q, want every photo but DSC_editada.JPG %q", photos, wantPhotos)
	}
	// Within the root, elsewhere means on another source: the corpus has
	// none.
	check("elsewhere within the root", Query{Within: &s.Root, Dup: dups(domain.DupElsewhere)}, nil)

	// A copy in an archive counts: the zip's photos are copies of the
	// unpacked folder's.
	pendrive := s.ID("Downloads/fotos_2005_do_pendrive")
	got := matchPaths(t, st, Query{Within: &pendrive, Dup: dups(domain.DupElsewhere)})
	if len(got) != 9 {
		t.Errorf("elsewhere within the unpacked pendrive folder: %q, want its 9 photos", got)
	}

	// The stored form of a selection resolves to the same entries.
	q := Query{Within: &fotosCopia, Dup: dups(domain.DupElsewhere, domain.DupUnique)}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var back Query
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if a, z := matchPaths(t, st, q), matchPaths(t, st, back); !slices.Equal(a, z) || len(a) != len(elsewhere)+1 {
		t.Errorf("selection round trip: %q, then %q", a, z)
	}

	// Rows carry the content state and the physical copies, the row itself
	// included: curriculo.doc's group has four copies.
	size := ptr[int64](24576)
	res, err := Page(context.Background(), st.Reader(), Query{Name: "curriculo", MinSize: size, MaxSize: size}, "", 0)
	if err != nil || len(res.Items) != 4 {
		t.Fatalf("curriculo.doc's group: %d rows, %v", len(res.Items), err)
	}
	for _, r := range res.Items {
		if r.ContentState != domain.ContentHashed || r.Copies.Int64 != 4 {
			t.Errorf("%s: %s with %v copies, want hashed with 4", r.Path, r.ContentState, r.Copies)
		}
	}
}

// A hard-link set is one copy: two names of one file are no duplicate,
// and a third, separate file with the same content makes all three copies.
func TestDupFilterHardLinks(t *testing.T) {
	st := storetest.Open(t)
	mtime := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	file := func(p string, ino, nlink uint64) indextest.Node {
		return indextest.Node{Path: p, Kind: domain.EntryFile, Lstat: &fsaccess.EntryInfo{Kind: domain.EntryFile,
			Size: 1000, ModTime: mtime, Dev: 7, Ino: ino, Nlink: nlink, Mode: 0o644}}
	}
	s := indextest.Seed(t, st, indextest.Tree{Source: "disco", CreateSource: true, MountPoint: "/mnt/disco",
		Nodes: []indextest.Node{file("a/um.bin", 10, 2), file("b/um.bin", 10, 2), file("c/outro.bin", 11, 1)}})
	sum := make([]byte, 32)
	sum[0] = 1
	s.SetContent(st, "a/um.bin", indextest.Content{State: domain.ContentHashed, SHA256: sum})
	s.SetContent(st, "b/um.bin", indextest.Content{State: domain.ContentHashed, SHA256: sum})
	copiesQ := Query{Dup: []domain.DupFilter{domain.DupCopies}}
	if got := matchPaths(t, st, copiesQ); len(got) != 0 {
		t.Errorf("hard links alone: copies %q", got)
	}
	if got := matchPaths(t, st, Query{Dup: []domain.DupFilter{domain.DupUnique}}); len(got) != 2 {
		t.Errorf("hard links alone: unique %q, want both names", got)
	}
	s.SetContent(st, "c/outro.bin", indextest.Content{State: domain.ContentHashed, SHA256: sum})
	if got := matchPaths(t, st, copiesQ); len(got) != 3 {
		t.Errorf("with a separate copy: copies %q", got)
	}
	res, err := Page(context.Background(), st.Reader(), Query{Ext: []string{"bin"}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Items {
		if r.Copies.Int64 != 2 {
			t.Errorf("%s: %v copies, want 2 (the link set and the other file)", r.Path, r.Copies)
		}
	}
}

// The duplicate filter's pages (r2b design D8): by bytes they merge each
// source's size-ordered ranges, in other orders they sort the driven
// matches. Either way the pages list exactly the matches Resolve finds, in
// order, without repeats across page boundaries, and Count counts them.
func TestDupPages(t *testing.T) {
	st, s, _ := contentCorpus(t)
	ctx := context.Background()
	dups := func(d ...domain.DupFilter) []domain.DupFilter { return d }
	fotosCopia := s.ID("Fotos - Copia")
	for _, q := range []Query{
		{Dup: dups(domain.DupCopies)},
		{Dup: dups(domain.DupCopies), Order: OrderAsc},
		{Dup: dups(domain.DupUnique), Source: "corpus"},
		{Dup: dups(domain.DupUnchecked, domain.DupCopies)},
		{Dup: dups(domain.DupUnique), Ext: []string{"jpg"}},
		{Dup: dups(domain.DupCopies), Sort: SortName},
		{Dup: dups(domain.DupUnique), Sort: SortNewest},
		{Dup: dups(domain.DupElsewhere), Within: &fotosCopia},
		{Dup: dups(domain.DupCopies), Source: "nenhuma"},
	} {
		want := matchPaths(t, st, q)
		full, err := Page(ctx, st.Reader(), q, "", MaxLimit)
		if err != nil {
			t.Fatal(err)
		}
		paged := allPages(t, st.Reader(), q, 7)
		if !reflect.DeepEqual(rowPaths(paged), rowPaths(full.Items)) {
			t.Errorf("%+v: pages of 7 %q\nwant %q", q, rowPaths(paged), rowPaths(full.Items))
		}
		var got []string
		for _, r := range full.Items {
			got = append(got, string(r.Path))
		}
		if !slices.Equal(slices.Sorted(slices.Values(got)), want) {
			t.Errorf("%+v: page %q\nwant %q", q, got, want)
		}
		s := q.sortSpec()
		if s.key == SortBytes && !slices.IsSortedFunc(full.Items, func(a, b Row) int {
			c := cmpThen(cmpInt(a.TotalBytes, b.TotalBytes), cmpInt(int64(a.ID), int64(b.ID)))
			if s.desc {
				return -c
			}
			return c
		}) {
			t.Errorf("%+v: not in size order: %q", q, rowPaths(full.Items))
		}
		if n, capped, err := Count(ctx, st.Reader(), q); err != nil || capped || n != len(want) {
			t.Errorf("%+v: count %d (capped %v, %v), want %d", q, n, capped, err, len(want))
		}
	}
}
