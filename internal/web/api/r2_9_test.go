package api

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// R2 fixes of the owner's walkthrough (tasks 9.x).

func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// 9.2 (A2): a folder whose only unchecked file is unreadable reads as fully
// checked (candidate bytes equal checked bytes), entry folder and member
// folder alike, while the source's coverage still counts the unreadable
// file and member.
func TestR2_9_2UnreadableFilesAreNoCandidates(t *testing.T) {
	e := newEnv(t)
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "pen", CreateSource: true, MountPoint: "/media/pen",
		Nodes: []indextest.Node{
			{Path: "Pasta/lida.jpg", Size: 300},
			{Path: "Pasta/ilegivel.jpg", Size: 50},
			{Path: "Pasta/pacote.zip", Size: 900},
		}})
	s.SetContent(e.st, "Pasta/lida.jpg", indextest.Content{State: domain.ContentHashed, SHA256: digest("lida")})
	s.SetContent(e.st, "Pasta/ilegivel.jpg", indextest.Content{State: domain.ContentUnreadable})
	s.SetContent(e.st, "Pasta/pacote.zip", indextest.Content{State: domain.ContentHashed, SHA256: digest("pacote")})
	arc := s.SeedArchive(e.st, "Pasta/pacote.zip", indextest.Archive{Format: domain.ArchiveZip, Members: []indextest.Member{
		{Path: "f/a.txt", Size: 40, Content: indextest.Content{State: domain.ContentHashed, SHA256: digest("a")}},
		{Path: "f/b.txt", Size: 7, Content: indextest.Content{State: domain.ContentUnreadable}},
	}})
	indextest.RecomputeCoverage(t, e.st)
	(&contentWorld{env: e}).relate(t)

	pasta := e.contentDetail(t, s.ID("Pasta").String())
	if c := pasta.Entry; c.CandidateBytes == nil || c.CheckedBytes == nil || *c.CandidateBytes != 1200 || *c.CheckedBytes != 1200 {
		t.Errorf("Pasta: candidate %v, checked %v; want 1200 both", c.CandidateBytes, c.CheckedBytes)
	}
	f := e.contentDetail(t, domain.Ref{Member: arc.Member("f")}.String())
	if c := f.Entry; c.CandidateBytes == nil || c.CheckedBytes == nil || *c.CandidateBytes != 40 || *c.CheckedBytes != 40 {
		t.Errorf("member folder f: candidate %v, checked %v; want 40 both", c.CandidateBytes, c.CheckedBytes)
	}
	if u := pasta.Coverage.Unreadable; u.Files != 2 || u.Bytes != 57 {
		t.Errorf("coverage unreadable %+v; want 2 files, 57 bytes", u)
	}
}

// 9.4 (A4): the raw name of a zip member that is not UTF-8 (as Windows zip
// tools write it, without the UTF-8 flag) displays decoded from code page
// 850 in rows and ancestors, while name_b64 and path_b64 keep the raw
// bytes; a tar member's same raw name keeps DisplayName's escapes.
func TestR2_9_4ZipMemberNamesDecodeFromCodePage850(t *testing.T) {
	e := newEnv(t)
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "pen", CreateSource: true, MountPoint: "/media/pen",
		Nodes: []indextest.Node{{Path: "Docs/pacote.zip", Size: 900}, {Path: "Docs/pacote.tar", Size: 900}}})
	const folder, file = "Relat\xA2rios", "Relat\xA2rios/Anota\x87\xE4es.txt"
	members := []indextest.Member{{Path: file, Size: 40, Content: indextest.Content{State: domain.ContentPending}}}
	zip := s.SeedArchive(e.st, "Docs/pacote.zip", indextest.Archive{Format: domain.ArchiveZip, Members: members})
	tar := s.SeedArchive(e.st, "Docs/pacote.tar", indextest.Archive{Format: domain.ArchiveTar, Members: members})

	var page struct {
		Items []row `json:"items"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/children", domain.Ref{Member: zip.Member(folder)}), 200, &page)
	if len(page.Items) != 1 {
		t.Fatalf("children of the zip folder: %+v", page.Items)
	}
	r := page.Items[0]
	if r.Name != "Anotações.txt" || string(r.NameB64) != "Anota\x87\xE4es.txt" ||
		r.Path != "Docs/pacote.zip!Relatórios/Anotações.txt" || string(r.PathB64) != "Docs/pacote.zip!"+file {
		t.Errorf("zip member: name %q (%q), path %q (%q)", r.Name, r.NameB64, r.Path, r.PathB64)
	}
	d := e.contentDetail(t, domain.Ref{Member: zip.Member(file)}.String())
	if a := d.Ancestors; len(a) == 0 || a[len(a)-1].Name != "Relatórios" {
		t.Errorf("ancestors of the zip member: %+v", a)
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/children", domain.Ref{Member: tar.Member(folder)}), 200, &page)
	if len(page.Items) != 1 || page.Items[0].Name != `Anota\x87\xE4es.txt` ||
		page.Items[0].Path != `Docs/pacote.tar!Relat\xA2rios/Anota\x87\xE4es.txt` {
		t.Errorf("tar member: %+v", page.Items)
	}
}

// 9.5 (A5): a modification time at or before the epoch is unknown. A
// folder holding an epoch file and 2004 files has 2004 as its oldest and
// newest year, and its by_year lists 2004, then a null year for the epoch
// file, summing to the totals; the epoch file's own times are null. The
// same holds for a member folder (a member without a time included) and for
// the home breakdown.
func TestR2_9_5UnknownDatesAreNull(t *testing.T) {
	e := newEnv(t)
	epoch, y2004 := time.Unix(0, 0), time.Date(2004, 6, 1, 12, 0, 0, 0, time.UTC)
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "pen", CreateSource: true, MountPoint: "/media/pen",
		Nodes: []indextest.Node{
			{Path: "Pasta/velho.jpg", Size: 70, MTime: epoch},
			{Path: "Pasta/novo.jpg", Size: 30, MTime: y2004},
			{Path: "Pasta/pacote.zip", Size: 100, MTime: y2004},
		}})
	arc := s.SeedArchive(e.st, "Pasta/pacote.zip", indextest.Archive{Format: domain.ArchiveZip, Members: []indextest.Member{
		{Path: "f/x.txt", Size: 9, MTime: epoch, Content: indextest.Content{State: domain.ContentPending}},
		{Path: "f/y.txt", Size: 1, MTime: y2004, Content: indextest.Content{State: domain.ContentPending}},
		{Path: "f/z.txt", Size: 2, Content: indextest.Content{State: domain.ContentPending}},
	}})
	type yearRes struct {
		Year  *int  `json:"year"`
		Files int64 `json:"files"`
		Bytes int64 `json:"bytes"`
	}
	type detail struct {
		Entry row `json:"entry"`
		Stats *struct {
			ByYear []yearRes `json:"by_year"`
		} `json:"stats"`
	}
	y := 2004
	when := y2004.Format(time.RFC3339)
	for _, c := range []struct {
		ref  string
		want []yearRes
	}{
		{s.ID("Pasta").String(), []yearRes{{&y, 2, 130}, {nil, 1, 70}}},
		{domain.Ref{Member: arc.Member("f")}.String(), []yearRes{{&y, 1, 1}, {nil, 2, 11}}},
	} {
		var d detail
		e.get(t, "/api/entries/"+c.ref, 200, &d)
		if d.Entry.Newest == nil || *d.Entry.Newest != when || d.Entry.Oldest == nil || *d.Entry.Oldest != when ||
			d.Stats == nil || !reflect.DeepEqual(d.Stats.ByYear, c.want) {
			t.Errorf("%s: newest %v oldest %v stats %+v", c.ref, d.Entry.Newest, d.Entry.Oldest, d.Stats)
		}
	}
	for _, ref := range []string{s.ID("Pasta/velho.jpg").String(), domain.Ref{Member: arc.Member("f/x.txt")}.String()} {
		var d detail
		e.get(t, "/api/entries/"+ref, 200, &d)
		if d.Entry.MTime != nil || d.Entry.Newest != nil || d.Entry.Oldest != nil {
			t.Errorf("%s: mtime %v newest %v oldest %v; want null", ref, d.Entry.MTime, d.Entry.Newest, d.Entry.Oldest)
		}
	}
	var home struct {
		ByYear []yearRes `json:"by_year"`
	}
	e.get(t, "/api/home", 200, &home)
	if want := []yearRes{{&y, 2, 130}, {nil, 1, 70}}; !reflect.DeepEqual(home.ByYear, want) {
		t.Errorf("home by_year %+v", home.ByYear)
	}
}

// 9.9 (A9): an unpacked_archives row names the folder its archive was
// unpacked into: its relation, seen from the archive (self a), has that
// folder as the other side, and the pair opens in Compare.
func TestR2_9_9UnpackedArchiveRowsNameTheirFolder(t *testing.T) {
	w := newContentWorld(t)
	var list struct {
		Items []struct {
			Entry    *contentRow  `json:"entry"`
			Relation *relationRes `json:"relation"`
			Copies   []copyRes    `json:"copies"`
		} `json:"items"`
	}
	w.get(t, "/api/opportunities/unpacked_archives?limit=500", 200, &list)
	if len(list.Items) == 0 {
		t.Fatal("no unpacked_archives rows")
	}
	for _, it := range list.Items {
		if it.Entry == nil || it.Relation == nil || it.Copies != nil || it.Relation.Self != "a" ||
			it.Relation.Other.Kind != "directory" || it.Relation.Other.ID == it.Entry.ID ||
			(it.Relation.Kind != "same" && it.Relation.Kind != "inside") {
			t.Errorf("unpacked row %+v", it)
			continue
		}
		var cmp struct {
			Left  contentRow `json:"left"`
			Right contentRow `json:"right"`
		}
		w.get(t, fmt.Sprintf("/api/compare?left=%s&right=%s", it.Entry.ID, it.Relation.Other.ID), 200, &cmp)
		if cmp.Left.ID != it.Entry.ID || cmp.Right.ID != it.Relation.Other.ID {
			t.Errorf("compare %s with %s: %s, %s", it.Entry.ID, it.Relation.Other.ID, cmp.Left.ID, cmp.Right.ID)
		}
	}
}

// contentDetail reads GET /api/entries/{ref} with the R2 fields.
func (e *env) contentDetail(t *testing.T, ref string) detailRes {
	t.Helper()
	var d detailRes
	e.get(t, fmt.Sprintf("/api/entries/%s", ref), 200, &d)
	return d
}
