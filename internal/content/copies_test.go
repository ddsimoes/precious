package content

import (
	"context"
	"slices"
	"testing"

	"precious/internal/domain"
)

// Copies lists the other copies of a file's or member's content, files and
// members, pages through them, flags another name of the same file, and
// marks a copy on an offline source. A copy's own decision is listed beside
// its effective one; a copy that follows its folder, and a member, has none.
// A zip member's raw name that is not UTF-8 (a Windows zip tool's, without
// the UTF-8 flag) displays decoded from code page 850; path_b64 keeps the
// raw bytes.
func TestCopies(t *testing.T) {
	e := newEnv(t)
	data := []byte("curriculum vitae, 2004")
	root := e.disk("fotos", "/mnt/fotos", posix)
	docs := root.Dir("Documentos")
	a := docs.File("curriculo.doc", 0, fileTime).Content(data)
	docs.HardLink("curriculo-link.doc", a)
	root.Dir("Downloads").File("curriculo (1).doc", 0, fileTime).Content(data)
	root.File("docs.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "cv/Anota\x87\xE4es.doc", data: data}))
	e.disk("usb", "/mnt/usb", posix).File("cv.doc", 0, fileTime).Content(data)
	e.scan("fotos")
	e.scan("usb")
	e.hash("fotos")
	e.hash("usb")
	e.setOffline("usb")
	for _, p := range []struct{ path, decision string }{{"Downloads/curriculo (1).doc", "discard"}, {"docs.zip", "keep"}} {
		if _, err := e.st.Writer().Exec(`UPDATE entries SET decision = ?1, eff_decision = ?1 WHERE source_id = 'fotos' AND path = ?2`,
			p.decision, []byte(p.path)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	self := domain.Ref{Entry: e.id("fotos", "Documentos/curriculo.doc")}
	var all []Copy
	cursor := ""
	for {
		page, total, next, err := Copies(ctx, e.st.Reader(), self, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if total != 4 {
			t.Fatalf("count %d, want 4", total)
		}
		all = append(all, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	var paths []string
	for _, c := range all {
		paths = append(paths, string(c.SourceID)+":"+c.Path)
		switch c.Path {
		case "Documentos/curriculo-link.doc":
			if !c.HardLink {
				t.Error("the hard link is not flagged")
			}
		case "docs.zip!cv/Anotações.doc":
			if c.ArchiveID == nil || *c.ArchiveID != e.id("fotos", "docs.zip") || !c.Ref.IsMember() ||
				string(c.PathB64) != "docs.zip!cv/Anota\x87\xE4es.doc" {
				t.Errorf("member copy %+v", c)
			}
		case "cv.doc":
			if !c.Offline {
				t.Error("the copy on the offline source is not marked offline")
			}
		}
		want := map[string][2]domain.Decision{
			"Downloads/curriculo (1).doc": {domain.DecisionDiscard, domain.DecisionDiscard},
			"docs.zip!cv/Anotações.doc":   {"", domain.DecisionKeep},
		}[c.Path]
		if want == ([2]domain.Decision{}) {
			want = [2]domain.Decision{"", domain.DecisionUndecided}
		}
		if got := [2]domain.Decision{c.Decision, c.EffDecision}; got != want {
			t.Errorf("%s: decision %q, effective %q; want %q", c.Path, got[0], got[1], want)
		}
	}
	want := []string{"fotos:Documentos/curriculo-link.doc", "fotos:Downloads/curriculo (1).doc", "usb:cv.doc",
		"fotos:docs.zip!cv/Anotações.doc"}
	slices.Sort(paths)
	slices.Sort(want)
	if !slices.Equal(paths, want) {
		t.Errorf("copies %q, want %q", paths, want)
	}
	member := all[len(all)-1].Ref
	if got, n, _, err := Copies(ctx, e.st.Reader(), member, "", 10); err != nil || n != 4 || len(got) != 4 {
		t.Errorf("copies of the member: %d of %d, %v", len(got), n, err)
	}
	if _, _, _, err := Copies(ctx, e.st.Reader(), domain.Ref{Entry: 999999}, "", 10); domain.CodeOf(err) != domain.CodeNotFound {
		t.Errorf("unknown ref: %v", err)
	}
	if _, _, _, err := Copies(ctx, e.st.Reader(), self, "x", 10); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Errorf("bad cursor: %v", err)
	}
}
