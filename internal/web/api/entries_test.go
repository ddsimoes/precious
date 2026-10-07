package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"testing"

	"precious/internal/clock"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/rules"
	"precious/internal/web/clientip"
)

// detailTree seeds source "disco": a decided and tagged folder a with a
// file three levels down, an application group, a name that is not UTF-8,
// and an unreadable folder.
func detailTree(t *testing.T, e *env) *indextest.Seeded {
	t.Helper()
	img, doc := domain.FileKindImage, domain.FileKindDocument
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "disco", CreateSource: true, MountPoint: "/mnt/disco", Nodes: []indextest.Node{
		{Path: "a/b/c/f.txt", Size: 10, MTime: year(2004), FileKind: doc},
		{Path: "a/b/IMG_0001.JPG", Size: 200, MTime: year(2006), FileKind: img},
		{Path: "a/kept.doc", Size: 30, MTime: year(2006), FileKind: doc},
		{Path: "Arquivos de programas/Office", Kind: domain.EntryDirectory, Category: domain.CategoryApplicationInstallation, Triage: domain.TriageReview,
			Group: true},
		{Path: "Arquivos de programas/Office/orcamento.xls", Size: 60, MTime: year(2009), FileKind: doc},
		{Path: "Arquivos de programas/Office/WINWORD.EXE", Size: 5000, MTime: year(2003), FileKind: domain.FileKindExecutable},
		{Path: "f\xe9.txt", Size: 6, MTime: year(2012), FileKind: doc},
		{Path: "trancada", Kind: domain.EntryDirectory, Unreadable: true},
	}})
	svc := decisions.New(clock.Real{}, rules.Default(), nil)
	ctx := clientip.With(context.Background(), clientip.Info{Addr: netip.MustParseAddr("192.0.2.7"), Scheme: "https"})
	for _, d := range []decisions.SetDecision{
		{EntryID: s.ID("a"), Decision: domain.DecisionDiscard},
		{EntryID: s.ID("a/kept.doc"), Decision: domain.DecisionKeep},
	} {
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := svc.SetDecision(ctx, tx, d)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.Tag(e.st, "familia", "a/b")
	s.Tag(e.st, "Zeta", "a/b/c/f.txt")
	s.Tag(e.st, "texto", "a/b/c/f.txt", "f\xe9.txt")
	return s
}

func TestEntryDetail(t *testing.T) {
	e := newEnv(t)
	s := detailTree(t, e)
	familia, zeta, texto := s.Tag(e.st, "familia"), s.Tag(e.st, "Zeta"), s.Tag(e.st, "texto")
	f := s.ID("a/b/c/f.txt")

	var got struct {
		Entry     row `json:"entry"`
		Ancestors []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			NameB64 []byte `json:"name_b64"`
		} `json:"ancestors"`
		Classification json.RawMessage `json:"classification"`
		Intent         json.RawMessage `json:"intent"`
		Stats          json.RawMessage `json:"stats"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", f), 200, &got)

	wantAnc := []string{s.Root.String() + ":", s.ID("a").String() + ":a", s.ID("a/b").String() + ":b", s.ID("a/b/c").String() + ":c"}
	var gotAnc []string
	for _, a := range got.Ancestors {
		gotAnc = append(gotAnc, a.ID+":"+string(a.NameB64))
		if a.Name != string(a.NameB64) {
			t.Errorf("ancestor %s display %q, raw %q", a.ID, a.Name, a.NameB64)
		}
	}
	if !reflect.DeepEqual(gotAnc, wantAnc) {
		t.Errorf("ancestors %v, want %v", gotAnc, wantAnc)
	}

	r := got.Entry
	if r.ID != f.String() || r.SourceID != "disco" || r.Name != "f.txt" || string(r.NameB64) != "f.txt" ||
		r.Path != "a/b/c/f.txt" || string(r.PathB64) != "a/b/c/f.txt" || r.Kind != "file" ||
		str(r.FileKind) != "document" || r.MainKind != nil || r.Size != 10 || r.TotalBytes != 10 || r.TotalFiles != 1 ||
		str(r.MTime) != "2004-06-01T12:00:00Z" || str(r.Newest) != str(r.MTime) || r.State != "present" ||
		r.Decision != nil || r.EffDecision != "discard" {
		t.Errorf("entry %+v", r)
	}
	if want := []int64{min(zeta, texto), max(zeta, texto)}; !reflect.DeepEqual(r.TagIDs, want) {
		t.Errorf("tag_ids %v, want %v", r.TagIDs, want)
	}

	wantIntent := fmt.Sprintf(`{"decision":null,"eff_decision":"discard","from":{"id":"%s","path":"a","path_b64":"YQ=="},"tags":[`+
		`{"id":%d,"name":"familia","own":false,"from":{"id":"%s","path":"a/b","path_b64":"YS9i"}},`+
		`{"id":%d,"name":"texto","own":true,"from":null},`+
		`{"id":%d,"name":"Zeta","own":true,"from":null}]}`,
		s.ID("a"), familia, s.ID("a/b"), texto, zeta)
	assertJSON(t, "intent", got.Intent, wantIntent)
	assertJSON(t, "stats", got.Stats, `null`)
	assertJSON(t, "classification", got.Classification,
		`{"category":null,"family":null,"traits":[],"triage":null,"group":false,"veto":false,"rules":[],"indicators":[]}`)

	// An own decision has no origin but itself.
	var kept struct {
		Intent json.RawMessage `json:"intent"`
	}
	k := s.ID("a/kept.doc")
	e.get(t, fmt.Sprintf("/api/entries/%s", k), 200, &kept)
	assertJSON(t, "own intent", kept.Intent, fmt.Sprintf(
		`{"decision":"keep","eff_decision":"keep","from":{"id":"%s","path":"a/kept.doc","path_b64":"YS9rZXB0LmRvYw=="},"tags":[]}`, k))

	// The root: no ancestors, the default decision, folder stats.
	var root struct {
		Ancestors []json.RawMessage `json:"ancestors"`
		Intent    json.RawMessage   `json:"intent"`
		Stats     json.RawMessage   `json:"stats"`
		Entry     row               `json:"entry"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", s.Root), 200, &root)
	if root.Ancestors == nil || len(root.Ancestors) != 0 || root.Entry.Name != "" || root.Entry.NameB64 == nil || !root.Entry.Partial {
		t.Errorf("root: ancestors %s, entry %+v", root.Ancestors, root.Entry)
	}
	assertJSON(t, "root intent", root.Intent, `{"decision":null,"eff_decision":"undecided","from":null,"tags":[]}`)
	assertJSON(t, "root stats", root.Stats, `{"dirs":6,"files":6,"unreadable":1,"mount_boundaries":0,`+
		`"by_kind":[{"kind":"image","bytes":200,"files":1},{"kind":"document","bytes":106,"files":4},{"kind":"executable","bytes":5000,"files":1}],`+
		`"by_year":[{"year":2003,"bytes":5000,"files":1},{"year":2004,"bytes":10,"files":1},{"year":2006,"bytes":230,"files":2},{"year":2009,"bytes":60,"files":1},{"year":2012,"bytes":6,"files":1}],`+
		`"inside":[`+insideJSON(s, "Arquivos de programas/Office", "application_installation", "programs", true, 5060, 2)+`,`+
		insideJSON(s, "a", "", "", false, 240, 3)+`,`+insideJSON(s, "f\xe9.txt", "", "personal", false, 6, 1)+`]}`)
}

func TestEntryClassification(t *testing.T) {
	e := newEnv(t)
	s := detailTree(t, e)
	office := s.ID("Arquivos de programas/Office")
	xls := s.ID("Arquivos de programas/Office/orcamento.xls")
	e.exec(t, `UPDATE entries SET traits = '["contains_user_material"]', rule_ids = '["system_file","retired_rule"]',
		is_group = 1, veto = 1 WHERE id = ?`, int64(office))
	e.exec(t, `UPDATE dir_stats SET indicators = ? WHERE entry_id = ?`, fmt.Sprintf(
		`[{"entry_id":"%s","path_b64":"QXJxdWl2b3MgZGUgcHJvZ3JhbWFzL09mZmljZS9vcmNhbWVudG8ueGxz","path":"Arquivos de programas/Office/orcamento.xls","signal":"office_document"}]`, xls),
		int64(office))

	var got struct {
		Entry          row             `json:"entry"`
		Classification json.RawMessage `json:"classification"`
		Stats          json.RawMessage `json:"stats"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", office), 200, &got)
	explain := rules.Default().Explain([]string{"system_file"})[0]
	if explain == "" {
		t.Fatal("rule system_file has no explanation")
	}
	sentence, _ := json.Marshal(explain)
	assertJSON(t, "classification", got.Classification, fmt.Sprintf(`{"category":"application_installation","family":"programs",`+
		`"traits":["contains_user_material"],"triage":"review","group":true,"veto":true,`+
		`"rules":[{"id":"system_file","explain":%s},{"id":"retired_rule","explain":""}],`+
		`"indicators":[{"entry_id":"%s","path":"Arquivos de programas/Office/orcamento.xls","path_b64":"QXJxdWl2b3MgZGUgcHJvZ3JhbWFzL09mZmljZS9vcmNhbWVudG8ueGxz","signal":"office_document"}]}`,
		sentence, xls))
	if !got.Entry.Group || !got.Entry.Veto || str(got.Entry.Category) != "application_installation" ||
		str(got.Entry.Family) != "programs" || str(got.Entry.Triage) != "review" || str(got.Entry.MainKind) != "executable" {
		t.Errorf("entry %+v", got.Entry)
	}
	assertJSON(t, "stats", got.Stats, `{"dirs":0,"files":2,"unreadable":0,"mount_boundaries":0,`+
		`"by_kind":[{"kind":"document","bytes":60,"files":1},{"kind":"executable","bytes":5000,"files":1}],`+
		`"by_year":[{"year":2003,"bytes":5000,"files":1},{"year":2009,"bytes":60,"files":1}],`+
		`"inside":[`+insideJSON(s, "Arquivos de programas/Office/orcamento.xls", "", "personal", false, 60, 1)+`]}`)
}

func assertJSON(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %s: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: want %s: %v", what, want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}
