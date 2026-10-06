package api

import (
	"crypto/sha256"
	"fmt"
	"testing"

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

// contentDetail reads GET /api/entries/{ref} with the R2 fields.
func (e *env) contentDetail(t *testing.T, ref string) detailRes {
	t.Helper()
	var d detailRes
	e.get(t, fmt.Sprintf("/api/entries/%s", ref), 200, &d)
	return d
}
