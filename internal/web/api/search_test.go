package api

import (
	"encoding/json"
	"fmt"
	"testing"

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
	if fmt.Sprint(names) != "[a/b/IMG_0001.JPG a a/b a/b/c a/b/c/f.txt]" || fmt.Sprint(res.Count) != "5" || res.NextCursor != nil {
		t.Fatalf("discard search: %v, count %v, cursor %v", names, res.Count, res.NextCursor)
	}
	e.get(t, "/api/search?source=disco&decision=discard&sort=name&limit=2", 200, &res)
	if len(res.Items) != 2 || res.NextCursor == nil || fmt.Sprint(res.Count) != "5" {
		t.Fatalf("limit 2: %d items, cursor %v, count %v", len(res.Items), res.NextCursor, res.Count)
	}
	e.get(t, "/api/search?source=disco&decision=discard&sort=name&limit=2&cursor="+*res.NextCursor, 200, &res)
	if len(res.Items) != 2 || res.Items[0].Path != "a/b" || res.Items[1].Path != "a/b/c" {
		t.Fatalf("second page: %+v", res.Items)
	}

	for _, target := range []string{
		"/api/search?sort=color", "/api/search?size=1", "/api/search?limit=0", "/api/search?limit=a",
		"/api/search?cursor=x", "/api/search?limit=1&limit=2", "/api/search?decision=maybe",
	} {
		e.fails(t, target, 400, "invalid_request")
	}
	e.fails(t, "/api/search?within=999999", 404, "not_found")
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
