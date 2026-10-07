package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index/indextest"
	"precious/internal/search"
)

func TestSearch(t *testing.T) {
	e := homeEnv(t)
	var res struct {
		Items      []row   `json:"items"`
		NextCursor *string `json:"next_cursor"`
		Count      any     `json:"count"`
	}
	// Under a: an inherited discard, and an own keep that is not listed.
	e.get(t, "/api/search?source=disco&decision=discard&sort=name", 200, &res)
	var names []string
	for _, r := range res.Items {
		names = append(names, r.Path)
	}
	// The page carries no count: it is a request of its own (r2b D8).
	if fmt.Sprint(names) != "[a/b/IMG_0001.JPG a a/b a/b/c a/b/c/f.txt]" || res.Count != nil || res.NextCursor != nil {
		t.Fatalf("discard search: %v, count %v, cursor %v", names, res.Count, res.NextCursor)
	}
	e.get(t, "/api/search?source=disco&decision=discard&sort=name&limit=2", 200, &res)
	if len(res.Items) != 2 || res.NextCursor == nil {
		t.Fatalf("limit 2: %d items, cursor %v", len(res.Items), res.NextCursor)
	}
	e.get(t, "/api/search?source=disco&decision=discard&sort=name&limit=2&cursor="+*res.NextCursor, 200, &res)
	if len(res.Items) != 2 || res.Items[0].Path != "a/b" || res.Items[1].Path != "a/b/c" {
		t.Fatalf("second page: %+v", res.Items)
	}
	// count=only answers the count alone, whatever cursor and limit say.
	var count map[string]any
	e.get(t, "/api/search?source=disco&decision=discard&sort=name&count=only", 200, &count)
	if fmt.Sprint(count) != "map[count:5]" {
		t.Fatalf("count=only: %v", count)
	}
	e.get(t, "/api/search?source=disco&decision=discard&count=only&limit=2&cursor="+*res.NextCursor, 200, &count)
	if fmt.Sprint(count) != "map[count:5]" {
		t.Fatalf("count=only with a cursor: %v", count)
	}

	for _, target := range []string{
		"/api/search?sort=color", "/api/search?size=1", "/api/search?limit=0", "/api/search?limit=a",
		"/api/search?cursor=x", "/api/search?limit=1&limit=2", "/api/search?decision=maybe",
		// R2 D15: "copies elsewhere" needs within; an unknown dup value.
		"/api/search?dup=elsewhere", "/api/search?source=disco&dup=unique&dup=elsewhere", "/api/search?dup=keeper",
		// r2b D8, D9: count takes only "only", once; state only "unreadable".
		"/api/search?count=all", "/api/search?count=only&count=only", "/api/search?count=only&dup=elsewhere",
		"/api/search?state=present", "/api/search?state=unreadable&state=unreadable",
	} {
		e.fails(t, target, 400, "invalid_request")
	}
	e.fails(t, "/api/search?within=999999", 404, "not_found")
	e.fails(t, "/api/search?within=999999&count=only", 404, "not_found")
}

func TestMatchCount(t *testing.T) {
	for _, c := range []struct {
		count matchCount
		want  string
	}{
		{matchCount{n: 0}, `0`},
		{matchCount{n: 9500}, `9500`},
		{matchCount{n: search.CountCap, capped: true}, `"10000+"`},
	} {
		got, err := json.Marshal(c.count)
		if err != nil || string(got) != c.want {
			t.Errorf("%+v: %s, %v; want %s", c.count, got, err, c.want)
		}
	}
}

// TestRawNames covers R1.8 through the API: a name that is not UTF-8 is
// sent as its exact bytes in base64 and displayed escaped, in the detail,
// in children, and in search results.
func TestRawNames(t *testing.T) {
	e := newEnv(t)
	s := detailTree(t, e)
	id := s.ID("f\xe9.txt")
	check := func(where string, r row) {
		t.Helper()
		if string(r.NameB64) != "f\xe9.txt" || string(r.PathB64) != "f\xe9.txt" ||
			r.Name != `f\xE9.txt` || r.Path != `f\xE9.txt` {
			t.Errorf("%s: name %q (raw %q), path %q (raw %q)", where, r.Name, r.NameB64, r.Path, r.PathB64)
		}
	}
	var detail struct {
		Entry row `json:"entry"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", id), 200, &detail)
	check("detail", detail.Entry)

	var p page
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=name", s.Root), 200, &p)
	found := false
	for _, r := range p.Items {
		if r.ID == id.String() {
			check("children", r)
			found = true
		}
	}
	if !found {
		t.Errorf("children of the root miss %s", id)
	}

	var res struct {
		Items []row `json:"items"`
		Count any   `json:"count"`
	}
	e.get(t, "/api/search?source=disco&ext=txt&min_size=6&max_size=6", 200, &res)
	if len(res.Items) != 1 || res.Items[0].ID != id.String() {
		t.Fatalf("search: %+v", res.Items)
	}
	check("search", res.Items[0])
}

// searchRow is the part of a search row these scenarios read.
type searchRow struct {
	SourceID string `json:"source_id"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Copies   *int64 `json:"copies"`
}

// search pages through target and asks its count, and returns both.
func (e *env) search(t *testing.T, target string) ([]searchRow, any) {
	t.Helper()
	var p struct {
		Items      []searchRow `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	e.get(t, target+"&limit=1000", 200, &p)
	if p.NextCursor != nil {
		t.Fatalf("%s: more than one page", target)
	}
	var c struct {
		Count any `json:"count"`
	}
	e.get(t, target+"&count=only", 200, &c)
	return p.Items, c.Count
}

// The inventory-explorer scenarios "Where each result is" and "One
// source": three copies of MOV_0195.mp4, two in fotos and one in pen.
// Each row carries its own folder in its path and the three copies; with
// the source fotos chosen, only its two are listed and counted.
func TestSearchWhereEachResultIs(t *testing.T) {
	e := newEnv(t)
	video := domain.FileKindVideo
	fotos := indextest.Seed(t, e.st, indextest.Tree{Source: "fotos", CreateSource: true, Nodes: []indextest.Node{
		{Path: "Videos/2006/MOV_0195.mp4", Size: 4096, MTime: year(2006), FileKind: video},
		{Path: "Backup/Celular/MOV_0195.mp4", Size: 4096, MTime: year(2006), FileKind: video},
	}})
	pen := indextest.Seed(t, e.st, indextest.Tree{Source: "pen", CreateSource: true, Nodes: []indextest.Node{
		{Path: "DCIM/MOV_0195.mp4", Size: 4096, MTime: year(2006), FileKind: video},
	}})
	sum := sha256.Sum256([]byte("MOV_0195"))
	hashed := indextest.Content{State: domain.ContentHashed, SHA256: sum[:]}
	fotos.SetContent(e.st, "Videos/2006/MOV_0195.mp4", hashed)
	fotos.SetContent(e.st, "Backup/Celular/MOV_0195.mp4", hashed)
	pen.SetContent(e.st, "DCIM/MOV_0195.mp4", hashed)

	rows, count := e.search(t, "/api/search?name=MOV_0195&sort=name")
	var got []string
	for _, r := range rows {
		got = append(got, r.SourceID+":"+r.Path)
		if r.Copies == nil || *r.Copies != 3 {
			t.Errorf("%s:%s: copies %v, want 3", r.SourceID, r.Path, r.Copies)
		}
	}
	slices.Sort(got)
	want := []string{"fotos:Backup/Celular/MOV_0195.mp4", "fotos:Videos/2006/MOV_0195.mp4", "pen:DCIM/MOV_0195.mp4"}
	if !slices.Equal(got, want) || fmt.Sprint(count) != "3" {
		t.Errorf("rows %q, count %v; want %q, 3", got, count, want)
	}

	rows, count = e.search(t, "/api/search?name=MOV_0195&source=fotos")
	if len(rows) != 2 || rows[0].SourceID != "fotos" || rows[1].SourceID != "fotos" || fmt.Sprint(count) != "2" {
		t.Errorf("source fotos: %+v, count %v", rows, count)
	}
	rows, count = e.search(t, "/api/search?source=pen&dup=copies")
	if len(rows) != 1 || rows[0].Path != "DCIM/MOV_0195.mp4" || fmt.Sprint(count) != "1" {
		t.Errorf("source pen, copies: %+v, count %v", rows, count)
	}
}

// The inventory-explorer scenario "From Home to the unreadable entries":
// two sources each hold a folder that could not be opened and one whose
// listing failed part way. Home says fotos's figures are partial, and its
// link, state=unreadable with the source, lists and counts exactly the
// folders of fotos that could not be read.
func TestUnreadableFromHome(t *testing.T) {
	e := newEnv(t)
	sfs := synthfs.New()
	for _, src := range []string{"fotos", "pen"} {
		root := sfs.Root("/" + src)
		root.Dir("ok").File("a.jpg", 10, year(2006))
		root.Dir("locked").Unreadable()
		half := root.Dir("deep").Dir("half")
		half.File("1.jpg", 10, year(2006))
		half.File("2.jpg", 10, year(2006))
		half.FailListingAfter(1, domain.OutcomeUnreadable)
		e.scanSynth(t, sfs, domain.SourceID(src), "/"+src, root)
	}
	var home struct {
		Partial bool `json:"partial"`
	}
	e.get(t, "/api/home?source=fotos", 200, &home)
	if !home.Partial {
		t.Fatal("Home does not say fotos is partial")
	}

	rows, count := e.search(t, "/api/search?state=unreadable&source=fotos&sort=name")
	var got []string
	for _, r := range rows {
		got = append(got, r.SourceID+":"+r.Path)
		if r.State != "unreadable" {
			t.Errorf("%s: state %s", r.Path, r.State)
		}
	}
	if want := []string{"fotos:deep/half", "fotos:locked"}; !slices.Equal(got, want) || fmt.Sprint(count) != "2" {
		t.Errorf("fotos: %q, count %v; want %q, 2", got, count, want)
	}
	if _, count := e.search(t, "/api/search?state=unreadable"); fmt.Sprint(count) != "4" {
		t.Errorf("every source: count %v, want 4", count)
	}
	// The state filter combines with the others, through the same index.
	if rows, _ := e.search(t, "/api/search?state=unreadable&name=half"); len(rows) != 2 {
		t.Errorf("unreadable named half: %+v", rows)
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(search.Query{State: search.StateUnreadable}); err != nil ||
		buf.String() != `{"state":"unreadable"}`+"\n" {
		t.Errorf("selection query JSON %s, %v", buf.String(), err)
	}
}
