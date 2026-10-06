package decisions

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/search"
)

func (e *env) tryTag(fn func(tx *sql.Tx) (Tag, error)) (Tag, error) {
	var tag Tag
	err := e.write(func(tx *sql.Tx) error {
		var err error
		tag, err = fn(tx)
		return err
	})
	return tag, err
}

func (e *env) tagCount(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTagNames(t *testing.T) {
	e := newEnv(t)
	familia := e.createTag(t, "familia")
	livro := e.createTag(t, "  livro ")
	if livro.Name != "livro" {
		t.Errorf("created %q, want the name trimmed", livro.Name)
	}
	e.createTag(t, "Ação")
	create := func(name string) error {
		_, err := e.tryTag(func(tx *sql.Tx) (Tag, error) { return e.svc.CreateTag(reqCtx(), tx, name) })
		return err
	}
	rename := func(id int64, name string) error {
		_, err := e.tryTag(func(tx *sql.Tx) (Tag, error) { return e.svc.RenameTag(reqCtx(), tx, id, name) })
		return err
	}
	for _, name := range []string{"Familia", "FAMILIA", " familia", "LIVRO", "AÇÃO", "aÇão"} {
		if err := create(name); domain.CodeOf(err) != domain.CodeTagExists {
			t.Errorf("creating %q: %v, want tag_exists", name, err)
		}
	}
	for _, name := range []string{"", "   ", strings.Repeat("a", MaxTagName+1), strings.Repeat("é", MaxTagName+1)} {
		if err := create(name); domain.CodeOf(err) != domain.CodeInvalidRequest {
			t.Errorf("creating %q: %v, want invalid_request", name, err)
		}
	}
	if n := e.tagCount(t, `SELECT count(*) FROM tags`); n != 3 {
		t.Errorf("%d tags after refused creations, want 3", n)
	}
	if err := create(strings.Repeat("é", MaxTagName)); err != nil {
		t.Errorf("a %d-character name: %v", MaxTagName, err)
	}

	if err := rename(familia.ID, "família"); err != nil {
		t.Errorf("renaming familia to família: %v", err)
	}
	if err := rename(familia.ID, "Família"); err != nil {
		t.Errorf("renaming a tag to its own name in another case: %v", err)
	}
	if err := rename(livro.ID, "FAMÍLIA"); domain.CodeOf(err) != domain.CodeTagExists {
		t.Errorf("renaming livro onto Família: %v, want tag_exists", err)
	}
	if err := rename(livro.ID, ""); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Errorf("renaming livro to an empty name: %v, want invalid_request", err)
	}
	if err := rename(999, "outro"); domain.CodeOf(err) != domain.CodeNotFound {
		t.Errorf("renaming an unknown tag: %v, want not_found", err)
	}
}

func TestRenameKeepsAssignments(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	familia := e.createTag(t, "familia")
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos"), s.ID("Fotos0/z.jpg")}, Add: []int64{familia.ID}})
	renamed, err := e.tryTag(func(tx *sql.Tx) (Tag, error) { return e.svc.RenameTag(reqCtx(), tx, familia.ID, "família") })
	if err != nil || renamed != (Tag{ID: familia.ID, Name: "família"}) {
		t.Fatalf("RenameTag = %+v, %v", renamed, err)
	}
	for _, p := range []string{"Fotos", "Fotos/2006/borrada.jpg", "Fotos0/z.jpg"} {
		if tags := e.intent(t, s.ID(p)).Tags; len(tags) != 1 || tags[0].Name != "família" || tags[0].ID != familia.ID {
			t.Errorf("%q tags %s, want família", p, fmtTags(tags))
		}
	}
}

func TestDeleteTagRemovesItEverywhere(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	livro := e.createTag(t, "livro")
	keepTag := e.createTag(t, "outro")
	var ids []domain.EntryID
	for _, r := range rows(t, e.st) {
		if len(ids) < 12 {
			ids = append(ids, domain.EntryID(r.id))
		}
	}
	e.setTags(t, SetTags{EntryIDs: ids, Add: []int64{livro.ID, keepTag.ID}})
	deleted, err := e.tryTag(func(tx *sql.Tx) (Tag, error) { return e.svc.DeleteTag(reqCtx(), tx, livro.ID) })
	if err != nil || deleted != livro {
		t.Fatalf("DeleteTag = %+v, %v; want %+v", deleted, err, livro)
	}
	if n := e.tagCount(t, `SELECT count(*) FROM entry_tags WHERE tag_id = ?`, livro.ID); n != 0 {
		t.Errorf("%d entries still carry the deleted tag", n)
	}
	if n := e.tagCount(t, `SELECT count(*) FROM entry_tags WHERE tag_id = ?`, keepTag.ID); n != 12 {
		t.Errorf("%d entries carry the other tag, want 12", n)
	}
	if n := e.tagCount(t, `SELECT count(*) FROM tags WHERE id = ?`, livro.ID); n != 0 {
		t.Error("the deleted tag is still listed")
	}
	_, err = e.trySetTags(SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos")}, Add: []int64{livro.ID}})
	wantCode(t, err, domain.CodeNotFound)
	_, err = e.tryTag(func(tx *sql.Tx) (Tag, error) { return e.svc.DeleteTag(reqCtx(), tx, livro.ID) })
	wantCode(t, err, domain.CodeNotFound)

	ev := audits(t, e.st)
	last := ev[len(ev)-1]
	for i := len(ev) - 1; i >= 0; i-- {
		if ev[i].kind == AuditTagDeleted {
			last = ev[i]
			break
		}
	}
	if last.kind != AuditTagDeleted || last.detail["name"] != "livro" || last.detail["entries"] != float64(12) {
		t.Errorf("deletion audited as %+v", last)
	}
}

func TestRemovingAnInheritedTagChangesNothing(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	familia := e.createTag(t, "familia")
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos/2006")}, Add: []int64{familia.ID}})
	before := e.tagCount(t, `SELECT count(*) FROM entry_tags`)
	if n := e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos/2006/natal")}, Remove: []int64{familia.ID}}); n != 1 {
		t.Errorf("applied %d, want 1", n)
	}
	if after := e.tagCount(t, `SELECT count(*) FROM entry_tags`); after != before {
		t.Errorf("entry_tags went from %d to %d rows", before, after)
	}
	from := &Ref{ID: s.ID("Fotos/2006"), Path: "Fotos/2006", PathB64: []byte("Fotos/2006")}
	if got := e.intent(t, s.ID("Fotos/2006/natal")).Tags; !tagsEqual(got, []TagRef{{ID: familia.ID, Name: "familia", From: from}}) {
		t.Errorf("Fotos/2006/natal tags %s, want familia from Fotos/2006", fmtTags(got))
	}

	// Removing it at the folder that carries it removes it below.
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos/2006")}, Remove: []int64{familia.ID}})
	if got := e.intent(t, s.ID("Fotos/2006/natal/IMG_0001.JPG")).Tags; len(got) != 0 {
		t.Errorf("tags %s after removal at the folder, want none", fmtTags(got))
	}
}

func TestNestedTagsUnion(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	familia, natal := e.createTag(t, "familia"), e.createTag(t, "natal")
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos")}, Add: []int64{familia.ID}})
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos/2006")}, Add: []int64{familia.ID, natal.ID}})
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos/2006/natal/IMG_0001.JPG")}, Add: []int64{natal.ID}})
	ref := func(p string) *Ref { return &Ref{ID: s.ID(p), Path: p, PathB64: []byte(p)} }
	for path, want := range map[string][]TagRef{
		"Fotos/2006/borrada.jpg":        {{ID: familia.ID, Name: "familia", From: ref("Fotos/2006")}, {ID: natal.ID, Name: "natal", From: ref("Fotos/2006")}},
		"Fotos/2006/natal/IMG_0001.JPG": {{ID: familia.ID, Name: "familia", From: ref("Fotos/2006")}, {ID: natal.ID, Name: "natal", Own: true}},
		"Fotos/IMG_0042.JPG":            {{ID: familia.ID, Name: "familia", From: ref("Fotos")}},
		"Fotos - Copia/c.jpg":           {},
	} {
		if got := e.intent(t, s.ID(path)).Tags; !tagsEqual(got, want) {
			t.Errorf("%q tags %s, want %s", path, fmtTags(got), fmtTags(want))
		}
	}
}

func TestTaggingASelection(t *testing.T) {
	e := newEnv(t)
	nodes := make([]string, 30)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("scans/p%02d.pdf", i)
	}
	s := e.seed(t, append(nodeFiles(nodes), file("outro.pdf", 1))...)
	scan := e.createTag(t, "scan")
	sel := e.createSelection(t, search.Query{Within: ptr(s.ID("scans"))})
	if sel.Count != 30 {
		t.Fatalf("selection of %d entries, want 30", sel.Count)
	}
	if n := e.setTags(t, SetTags{SelectionID: sel.ID, Add: []int64{scan.ID}}); n != 30 {
		t.Errorf("applied %d, want 30", n)
	}
	for _, p := range nodes {
		if got := e.intent(t, s.ID(p)).Tags; !tagsEqual(got, []TagRef{{ID: scan.ID, Name: "scan", Own: true}}) {
			t.Errorf("%q tags %s, want scan own", p, fmtTags(got))
		}
	}
	if got := e.intent(t, s.ID("outro.pdf")).Tags; len(got) != 0 {
		t.Errorf("an entry outside the selection got tags %s", fmtTags(got))
	}
	// Adding again is idempotent.
	if n := e.setTags(t, SetTags{SelectionID: sel.ID, Add: []int64{scan.ID}}); n != 30 {
		t.Errorf("applied %d, want 30", n)
	}
	if n := e.tagCount(t, `SELECT count(*) FROM entry_tags`); n != 30 {
		t.Errorf("%d entry_tags rows, want 30", n)
	}
}

func nodeFiles(paths []string) []indextest.Node {
	out := make([]indextest.Node, len(paths))
	for i, p := range paths {
		out[i] = file(p, 1)
	}
	return out
}

func TestSetTagsRefusals(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	tag := e.createTag(t, "familia")
	id := s.ID("Fotos")
	many := make([]domain.EntryID, MaxEntryIDs+1)
	for i := range many {
		many[i] = id
	}
	for name, c := range map[string]struct {
		req  SetTags
		code domain.ErrorCode
	}{
		"no targets":         {SetTags{Add: []int64{tag.ID}}, domain.CodeInvalidRequest},
		"both target forms":  {SetTags{EntryIDs: []domain.EntryID{id}, SelectionID: "x", Add: []int64{tag.ID}}, domain.CodeInvalidRequest},
		"too many":           {SetTags{EntryIDs: many, Add: []int64{tag.ID}}, domain.CodeInvalidRequest},
		"no tags":            {SetTags{EntryIDs: []domain.EntryID{id}}, domain.CodeInvalidRequest},
		"added and removed":  {SetTags{EntryIDs: []domain.EntryID{id}, Add: []int64{tag.ID}, Remove: []int64{tag.ID}}, domain.CodeInvalidRequest},
		"bad tag id":         {SetTags{EntryIDs: []domain.EntryID{id}, Add: []int64{0}}, domain.CodeInvalidRequest},
		"unknown tag":        {SetTags{EntryIDs: []domain.EntryID{id}, Add: []int64{tag.ID, 999}}, domain.CodeNotFound},
		"unknown entry":      {SetTags{EntryIDs: []domain.EntryID{id, 999999}, Add: []int64{tag.ID}}, domain.CodeNotFound},
		"unknown selection":  {SetTags{SelectionID: "nope", Add: []int64{tag.ID}}, domain.CodeNotFound},
		"unknown tag remove": {SetTags{EntryIDs: []domain.EntryID{id}, Remove: []int64{999}}, domain.CodeNotFound},
	} {
		if _, err := e.trySetTags(c.req); domain.CodeOf(err) != c.code {
			t.Errorf("%s: %v, want %s", name, err, c.code)
		}
	}
	if n := e.tagCount(t, `SELECT count(*) FROM entry_tags`); n != 0 {
		t.Errorf("refused requests left %d entry_tags rows", n)
	}
	if n := e.tagCount(t, `SELECT count(*) FROM audit_events WHERE kind = ?`, AuditTagsSet); n != 0 {
		t.Errorf("refused requests wrote %d audit events", n)
	}
}

func TestTagAudit(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	familia := e.createTag(t, "familia")
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID("Fotos"), s.ID("Fotos0")}, Add: []int64{familia.ID}})
	if _, err := e.tryTag(func(tx *sql.Tx) (Tag, error) { return e.svc.RenameTag(reqCtx(), tx, familia.ID, "família") }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tryTag(func(tx *sql.Tx) (Tag, error) { return e.svc.DeleteTag(reqCtx(), tx, familia.ID) }); err != nil {
		t.Fatal(err)
	}
	ev := audits(t, e.st)
	kinds := make([]string, len(ev))
	for i, x := range ev {
		kinds[i] = x.kind
		if x.actor != "admin" || x.addr.String != clientAddr.String() {
			t.Errorf("audit event %+v, want by admin from %s", x, clientAddr)
		}
	}
	if want := []string{AuditTagCreated, AuditTagsSet, AuditTagRenamed, AuditTagDeleted}; fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("audit kinds %v, want %v", kinds, want)
	}
	if d := ev[0].detail; d["name"] != "familia" || d["tag_id"] != float64(familia.ID) {
		t.Errorf("creation audited as %v", d)
	}
	if d := ev[1].detail; fmt.Sprint(d["add"]) != fmt.Sprintf("[%d]", familia.ID) || d["applied"] != float64(2) ||
		d["added"] != float64(2) || d["removed"] != float64(0) || len(d["entry_ids"].([]any)) != 2 {
		t.Errorf("tagging audited as %v", d)
	}
	if d := ev[2].detail; d["old"] != "familia" || d["new"] != "família" || d["tag_id"] != float64(familia.ID) {
		t.Errorf("rename audited as %v", d)
	}
}

func (e *env) createTag(t *testing.T, name string) Tag {
	t.Helper()
	var tag Tag
	if err := e.write(func(tx *sql.Tx) error {
		var err error
		tag, err = e.svc.CreateTag(reqCtx(), tx, name)
		return err
	}); err != nil {
		t.Fatalf("CreateTag(%q): %v", name, err)
	}
	return tag
}

func (e *env) trySetTags(req SetTags) (int, error) {
	var n int
	err := e.write(func(tx *sql.Tx) error {
		var err error
		n, err = e.svc.SetTags(reqCtx(), tx, req)
		return err
	})
	return n, err
}

func (e *env) setTags(t *testing.T, req SetTags) int {
	t.Helper()
	n, err := e.trySetTags(req)
	if err != nil {
		t.Fatalf("SetTags(%+v): %v", req, err)
	}
	return n
}

func tagsEqual(a, b []TagRef) bool {
	return slices.EqualFunc(a, b, func(x, y TagRef) bool {
		if x.ID != y.ID || x.Name != y.Name || x.Own != y.Own || (x.From == nil) != (y.From == nil) {
			return false
		}
		return x.From == nil || (x.From.ID == y.From.ID && x.From.Path == y.From.Path && string(x.From.PathB64) == string(y.From.PathB64))
	})
}

func fmtTags(tags []TagRef) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, tg := range tags {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(tg.Name)
		switch {
		case tg.Own:
			b.WriteString(" (own)")
		case tg.From != nil:
			b.WriteString(" from " + tg.From.Path)
		}
	}
	b.WriteByte(']')
	return b.String()
}
