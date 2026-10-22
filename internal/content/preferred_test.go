package content

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/store"
)

// preferredWorld is three sources: fotos and disco online, usb offline
// (scanned and hashed while online).
func preferredWorld(t *testing.T, build func(fotos, disco, usb *synthfs.Node)) *env {
	t.Helper()
	e := newEnv(t)
	fotos := e.disk("fotos", "/mnt/fotos", posix)
	disco := e.disk("disco", "/mnt/disco", posix)
	usb := e.disk("usb", "/mnt/usb", posix)
	build(fotos, disco, usb)
	for _, s := range []domain.SourceID{"usb", "disco", "fotos"} {
		e.scan(s)
	}
	// A second round hashes the files whose size a member listed in the
	// first round shares.
	for range 2 {
		for _, s := range []domain.SourceID{"usb", "disco", "fotos"} {
			e.hash(s)
		}
	}
	e.setOffline("usb")
	return e
}

// preferred runs PreferredCopies for one ref.
func (e *env) preferred(ref domain.Ref, place domain.Place) (PreferredCopy, bool) {
	e.t.Helper()
	got, err := PreferredCopies(context.Background(), e.st.Reader(), map[domain.Ref]domain.Place{ref: place})
	if err != nil {
		e.t.Fatalf("PreferredCopies(%s): %v", ref, err)
	}
	p, ok := got[ref]
	return p, ok
}

func copyName(c Copy) string { return string(c.SourceID) + ":" + c.Path }

// For other_source, a copy on an online source wins over a lower one on an
// offline source, and an offline copy is named, marked, when it is the
// only one elsewhere. The file's own hard-link names, on its own source,
// are never named, and a copy in the quarantine is none.
func TestPreferredOtherSource(t *testing.T) {
	data := []byte("relatório anual de 2004")
	alone := []byte("só no disco externo, 2004")
	gone := []byte("quarentena, apenas ali..")
	e := preferredWorld(t, func(fotos, disco, usb *synthfs.Node) {
		a := fotos.File("relatorio.doc", 0, fileTime).Content(data)
		fotos.HardLink("relatorio-link.doc", a)
		usb.File("relatorio.doc", 0, fileTime).Content(data)
		disco.File("relatorio.doc", 0, fileTime).Content(data)
		fotos.File("unico.doc", 0, fileTime).Content(alone)
		usb.File("unico.doc", 0, fileTime).Content(alone)
		fotos.File("q.doc", 0, fileTime).Content(gone)
		disco.File("q.doc", 0, fileTime).Content(gone)
	})
	e.quarantine("disco", "q.doc")
	if e.id("usb", "relatorio.doc") > e.id("disco", "relatorio.doc") {
		t.Fatal("the offline copy must have the lower ID")
	}
	p, ok := e.preferred(domain.Ref{Entry: e.id("fotos", "relatorio.doc")}, domain.PlaceOtherSource)
	if !ok || copyName(p.Copy) != "disco:relatorio.doc" || p.Offline || p.ArchiveID != nil || p.HardLink {
		t.Errorf("other_source copy %+v (%v), want disco:relatorio.doc online", p, ok)
	}
	// Physical copies: fotos' hard-link set, usb, disco.
	if p.More != 1 {
		t.Errorf("more = %d, want 1", p.More)
	}
	p, ok = e.preferred(domain.Ref{Entry: e.id("fotos", "unico.doc")}, domain.PlaceOtherSource)
	if !ok || copyName(p.Copy) != "usb:unico.doc" || !p.Offline || p.More != 0 {
		t.Errorf("only offline copy %+v (%v), want usb:unico.doc offline, more 0", p, ok)
	}
	// this_source asks for a copy on fotos: only the file's own other name is.
	if p, ok := e.preferred(domain.Ref{Entry: e.id("fotos", "relatorio.doc")}, domain.PlaceThisSource); ok {
		t.Errorf("this_source named %s, the file's own hard link", copyName(p.Copy))
	}
	if p, ok := e.preferred(domain.Ref{Entry: e.id("fotos", "q.doc")}, domain.PlaceOtherSource); ok {
		t.Errorf("named %s, in the quarantine", copyName(p.Copy))
	}
	for _, place := range []domain.Place{domain.PlaceOnlyCopy, domain.PlaceUnchecked, domain.PlaceNone} {
		if p, ok := e.preferred(domain.Ref{Entry: e.id("fotos", "relatorio.doc")}, place); ok {
			t.Errorf("%s named %s", place, copyName(p.Copy))
		}
	}
}

// For this_source, a loose copy wins over a member with a lower ID; a
// member's own copy is named the same way, and a member copy carries its
// archive. A file whose only other copy on its source is a member names
// that member, never its own hard-link name.
func TestPreferredThisSource(t *testing.T) {
	data := []byte("carta de apresentação, 2004")
	linked := []byte("nota com outro nome, 2004")
	e := preferredWorld(t, func(fotos, disco, usb *synthfs.Node) {
		fotos.File("a.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "cartas/carta.doc", data: data},
			zipEntry{name: "nota.txt", data: linked}))
		fotos.File("carta.doc", 0, fileTime).Content(data)
		fotos.Dir("z").File("carta (1).doc", 0, fileTime).Content(data)
		n := fotos.File("nota.txt", 0, fileTime).Content(linked)
		fotos.HardLink("nota-link.txt", n)
	})
	member := e.memberRef("fotos", "a.zip", "cartas/carta.doc")
	p, ok := e.preferred(domain.Ref{Entry: e.id("fotos", "z/carta (1).doc")}, domain.PlaceThisSource)
	if !ok || copyName(p.Copy) != "fotos:carta.doc" || p.ArchiveID != nil || p.More != 1 {
		t.Errorf("this_source copy %+v (%v), want fotos:carta.doc, more 1", p, ok)
	}
	p, ok = e.preferred(member, domain.PlaceThisSource)
	if !ok || copyName(p.Copy) != "fotos:carta.doc" || p.More != 1 {
		t.Errorf("member's copy %+v (%v), want fotos:carta.doc, more 1", p, ok)
	}
	p, ok = e.preferred(domain.Ref{Entry: e.id("fotos", "nota.txt")}, domain.PlaceThisSource)
	arc := e.id("fotos", "a.zip")
	if !ok || copyName(p.Copy) != "fotos:a.zip!nota.txt" || p.ArchiveID == nil || *p.ArchiveID != arc ||
		p.Ref.Entry != arc || !p.Ref.IsMember() || p.More != 0 {
		t.Errorf("hard-linked file's copy %+v (%v), want the member of a.zip, more 0", p, ok)
	}
}

// A tar hard link and its target are one copy: neither is named for the
// other, and they count once in more.
func TestPreferredTarLinkGroup(t *testing.T) {
	note := []byte("uma nota dentro do tar, 2004")
	e := preferredWorld(t, func(fotos, disco, usb *synthfs.Node) {
		fotos.File("site.tar.gz", 0, fileTime).Content(makeTarGz(t, tarEntry{name: "a/nota.txt", data: note},
			tarEntry{name: "a/ligacao.txt", link: "a/nota.txt"}))
		disco.File("nota.txt", 0, fileTime).Content(note)
	})
	for _, mpath := range []string{"a/nota.txt", "a/ligacao.txt"} {
		ref := e.memberRef("fotos", "site.tar.gz", mpath)
		if p, ok := e.preferred(ref, domain.PlaceThisSource); ok {
			t.Errorf("%s: this_source named %s, its own tar link group", mpath, copyName(p.Copy))
		}
		p, ok := e.preferred(ref, domain.PlaceOtherSource)
		if !ok || copyName(p.Copy) != "disco:nota.txt" || p.More != 0 {
			t.Errorf("%s: other_source copy %+v (%v), want disco:nota.txt, more 0", mpath, p, ok)
		}
	}
	p, ok := e.preferred(domain.Ref{Entry: e.id("disco", "nota.txt")}, domain.PlaceOtherSource)
	if !ok || copyName(p.Copy) != "fotos:site.tar.gz!a/nota.txt" || p.More != 0 {
		t.Errorf("file's copy %+v (%v), want the tar's target member, more 0", p, ok)
	}
}

// The members of two hard-linked names of one archive are one copy: more
// counts them once, and a member never names its twin in the other name.
func TestPreferredHardLinkedArchive(t *testing.T) {
	data := []byte("planilha de gastos de 2004")
	twice := []byte("resumo guardado duas vezes")
	e := preferredWorld(t, func(fotos, disco, usb *synthfs.Node) {
		a := fotos.File("backup.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "gastos.xls", data: data},
			zipEntry{name: "resumo.txt", data: twice}, zipEntry{name: "velho/resumo.txt", data: twice}))
		fotos.HardLink("backup-link.zip", a)
		fotos.File("gastos.xls", 0, fileTime).Content(data)
		disco.File("gastos.xls", 0, fileTime).Content(data)
	})
	for _, p := range []string{"backup.zip", "backup-link.zip"} {
		if e.archive("fotos", p).state != "complete" {
			t.Fatalf("%s is not listed complete", p)
		}
	}
	// Physical copies: fotos:gastos.xls, the members (one), disco.
	p, ok := e.preferred(domain.Ref{Entry: e.id("fotos", "gastos.xls")}, domain.PlaceOtherSource)
	if !ok || copyName(p.Copy) != "disco:gastos.xls" || p.More != 1 {
		t.Errorf("other_source copy %+v (%v), want disco:gastos.xls, more 1", p, ok)
	}
	first := e.memberRef("fotos", "backup.zip", "gastos.xls")
	if twin := e.memberRef("fotos", "backup-link.zip", "gastos.xls"); twin.Member < first.Member {
		first = twin
	}
	p, ok = e.preferred(domain.Ref{Entry: e.id("fotos", "gastos.xls")}, domain.PlaceThisSource)
	if !ok || p.Ref.Member != first.Member || p.More != 1 {
		t.Errorf("this_source copy %+v (%v), want the lower member %s, more 1", p, ok, first)
	}
	for _, arc := range []string{"backup.zip", "backup-link.zip"} {
		p, ok := e.preferred(e.memberRef("fotos", arc, "gastos.xls"), domain.PlaceThisSource)
		if !ok || copyName(p.Copy) != "fotos:gastos.xls" || p.More != 1 {
			t.Errorf("%s member's copy %+v (%v), want fotos:gastos.xls, more 1", arc, p, ok)
		}
	}
	// Two paths of one archive are two copies; each names the other in
	// its own archive name, never in the twin name.
	for _, arc := range []string{"backup.zip", "backup-link.zip"} {
		p, ok := e.preferred(e.memberRef("fotos", arc, "resumo.txt"), domain.PlaceThisSource)
		if !ok || copyName(p.Copy) != "fotos:"+arc+"!velho/resumo.txt" || p.More != 0 {
			t.Errorf("%s!resumo.txt copy %+v (%v), want %s!velho/resumo.txt, more 0", arc, p, ok, arc)
		}
	}
	e.quarantine("fotos", "gastos.xls")
	for _, arc := range []string{"backup.zip", "backup-link.zip"} {
		if p, ok := e.preferred(e.memberRef("fotos", arc, "gastos.xls"), domain.PlaceThisSource); ok {
			t.Errorf("%s member named %s, its twin", arc, copyName(p.Copy))
		}
	}
}

// countingQueryer counts the statements run through it.
type countingQueryer struct {
	q store.Queryer
	n int
}

func (c *countingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.n++
	return c.q.QueryRowContext(ctx, query, args...)
}

func (c *countingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.n++
	return c.q.QueryContext(ctx, query, args...)
}

// One statement serves a page of 200 refs, files and members, each
// answered.
func TestPreferredCopiesPage(t *testing.T) {
	const n = 200
	e := preferredWorld(t, func(fotos, disco, usb *synthfs.Node) {
		var members []zipEntry
		for i := range n / 2 {
			data := fmt.Appendf(nil, "documento número %04d", i)
			fotos.File(fmt.Sprintf("f%03d.doc", i), 0, fileTime).Content(data)
			disco.File(fmt.Sprintf("f%03d.doc", i), 0, fileTime).Content(data)
			members = append(members, zipEntry{name: fmt.Sprintf("m%03d.doc", i), data: data})
		}
		disco.File("tudo.zip", 0, fileTime).Content(makeZip(t, members...))
	})
	want := map[domain.Ref]domain.Place{}
	for i := range n / 2 {
		want[domain.Ref{Entry: e.id("fotos", fmt.Sprintf("f%03d.doc", i))}] = domain.PlaceOtherSource
		want[e.memberRef("disco", "tudo.zip", fmt.Sprintf("m%03d.doc", i))] = domain.PlaceThisSource
	}
	cq := &countingQueryer{q: e.st.Reader()}
	got, err := PreferredCopies(context.Background(), cq, want)
	if err != nil {
		t.Fatal(err)
	}
	if cq.n != 1 {
		t.Errorf("%d statements for %d refs, want 1", cq.n, len(want))
	}
	if len(got) != n {
		t.Errorf("%d of %d refs answered", len(got), n)
	}
	for ref, p := range got {
		if want[ref] == domain.PlaceOtherSource && (p.SourceID != "disco" || p.ArchiveID != nil) ||
			want[ref] == domain.PlaceThisSource && (p.SourceID != "disco" || p.ArchiveID != nil) || p.More != 1 {
			t.Errorf("%s: %+v", ref, p)
		}
	}
}
