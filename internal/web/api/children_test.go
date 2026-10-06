package api

import (
	"bytes"
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/search"
)

// mapTree seeds "big", a folder of 250 children: files with tied sizes and
// times, empty folders (no newest time), folders holding one file, and a
// name that is not UTF-8; and "wide", a folder of 1,001 files.
func mapTree(t *testing.T, e *env) *indextest.Seeded {
	t.Helper()
	var nodes []indextest.Node
	for i := range 250 {
		switch {
		case i%10 == 0:
			nodes = append(nodes, indextest.Node{Path: fmt.Sprintf("big/d%03d", i), Kind: domain.EntryDirectory})
		case i%10 == 1:
			nodes = append(nodes, indextest.Node{Path: fmt.Sprintf("big/D%03d/x.bin", i), Size: int64(i % 17), MTime: year(2000 + i%5)})
		default:
			nodes = append(nodes, indextest.Node{Path: fmt.Sprintf("big/f%03d", i), Size: int64(i * 37 % 50), MTime: year(2000 + i%7)})
		}
	}
	nodes = append(nodes, indextest.Node{Path: "big/\xffz", Size: 3})
	for i := range 1001 {
		nodes = append(nodes, indextest.Node{Path: fmt.Sprintf("wide/w%04d", i), Size: int64(i*7919%1000 + 1)})
	}
	return indextest.Seed(t, e.st, indextest.Tree{Source: "map", CreateSource: true, Nodes: nodes})
}

// sortKey is the sort key of a decoded row; null is nil.
func sortKey(t *testing.T, sort string, r row) any {
	t.Helper()
	switch sort {
	case search.SortBytes:
		return r.TotalBytes
	case search.SortFiles:
		return r.TotalFiles
	case search.SortNewest:
		if r.Newest == nil {
			return nil
		}
		tm, err := time.Parse(time.RFC3339Nano, *r.Newest)
		if err != nil {
			t.Fatalf("newest %q: %v", *r.Newest, err)
		}
		return tm.UnixNano()
	default:
		return r.NameB64
	}
}

// compareRows orders two rows by sort key, NULL first, then by ID.
func compareRows(t *testing.T, sort string, a, b row) int {
	t.Helper()
	ka, kb := sortKey(t, sort, a), sortKey(t, sort, b)
	var c int
	switch {
	case ka == nil && kb == nil:
	case ka == nil:
		c = -1
	case kb == nil:
		c = 1
	case sort == search.SortName:
		c = bytes.Compare(ka.([]byte), kb.([]byte))
	default:
		c = cmp.Compare(ka.(int64), kb.(int64))
	}
	ia, _ := strconv.ParseInt(a.ID, 10, 64)
	ib, _ := strconv.ParseInt(b.ID, 10, 64)
	return cmp.Or(c, cmp.Compare(ia, ib))
}

// pages follows next_cursor from the first page and returns every row.
func (e *env) pages(t *testing.T, base string, limit int) (rows []row, sizes []int) {
	t.Helper()
	cursor := ""
	for {
		q := base
		if limit > 0 {
			q += "&limit=" + strconv.Itoa(limit)
		}
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		var p page
		e.get(t, q, 200, &p)
		rows = append(rows, p.Items...)
		sizes = append(sizes, len(p.Items))
		if p.NextCursor == nil {
			return rows, sizes
		}
		cursor = *p.NextCursor
		if len(sizes) > 300 {
			t.Fatalf("%s: no last page", base)
		}
	}
}

func TestChildrenSortsAndPages(t *testing.T) {
	e := newEnv(t)
	s := mapTree(t, e)
	big := s.ID("big")
	var want []string
	for i := range 250 {
		switch i % 10 {
		case 0:
			want = append(want, s.ID(fmt.Sprintf("big/d%03d", i)).String())
		case 1:
			want = append(want, s.ID(fmt.Sprintf("big/D%03d", i)).String())
		default:
			want = append(want, s.ID(fmt.Sprintf("big/f%03d", i)).String())
		}
	}
	want = append(want, s.ID("big/\xffz").String())
	slices.Sort(want)

	for _, sort := range []string{search.SortBytes, search.SortFiles, search.SortNewest, search.SortName} {
		for _, order := range []string{"", search.OrderDesc, search.OrderAsc} {
			t.Run(sort+" "+order, func(t *testing.T) {
				base := fmt.Sprintf("/api/entries/%s/children?sort=%s&order=%s", big, sort, order)
				all, sizes := e.pages(t, base, 1000)
				if len(sizes) != 1 {
					t.Fatalf("limit 1000 took %d pages", len(sizes))
				}
				desc := order == search.OrderDesc || (order == "" && sort != search.SortName)
				for i := 1; i < len(all); i++ {
					c := compareRows(t, sort, all[i-1], all[i])
					if (desc && c <= 0) || (!desc && c >= 0) {
						t.Fatalf("rows %d and %d out of order: %+v then %+v", i-1, i, all[i-1], all[i])
					}
				}
				got := slices.Sorted(slices.Values(ids(all)))
				if !slices.Equal(got, want) {
					t.Fatalf("children %v, want %v", got, want)
				}
				small, _ := e.pages(t, base, 7)
				if !slices.Equal(ids(small), ids(all)) {
					t.Fatalf("pages of 7: %v, want %v", ids(small), ids(all))
				}
				def, sizes := e.pages(t, base, 0)
				if !slices.Equal(sizes, []int{200, 51}) || !slices.Equal(ids(def), ids(all)) {
					t.Fatalf("default pages %v, want 200 and 51 in the same order", sizes)
				}
			})
		}
	}

	// The default order: bytes, largest first.
	var first, bytesDesc page
	e.get(t, fmt.Sprintf("/api/entries/%s/children", big), 200, &first)
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=bytes&order=desc", big), 200, &bytesDesc)
	if !slices.Equal(ids(first.Items), ids(bytesDesc.Items)) {
		t.Errorf("default order differs from bytes desc")
	}

	// Empty folders have no newest time and come last in descending order.
	var newest page
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=newest&limit=1000", big), 200, &newest)
	last := newest.Items[len(newest.Items)-1]
	if last.Newest != nil || last.Kind != "directory" {
		t.Errorf("last row by newest desc: %+v, want an empty folder", last)
	}
}

func TestChildrenLimits(t *testing.T) {
	e := newEnv(t)
	s := mapTree(t, e)
	wide := s.ID("wide")
	var p page
	e.get(t, fmt.Sprintf("/api/entries/%s/children?limit=5000", wide), 200, &p)
	if len(p.Items) != 1000 || p.NextCursor == nil {
		t.Fatalf("limit 5000: %d rows, cursor %v; want 1000 and a cursor", len(p.Items), p.NextCursor)
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/children?limit=5000&cursor=%s", wide, url.QueryEscape(*p.NextCursor)), 200, &p)
	if len(p.Items) != 1 || p.NextCursor != nil {
		t.Fatalf("second page: %d rows, cursor %v; want 1 and none", len(p.Items), p.NextCursor)
	}
	// A file has no children.
	e.get(t, fmt.Sprintf("/api/entries/%s/children", s.ID("wide/w0001")), 200, &p)
	if len(p.Items) != 0 || p.NextCursor != nil {
		t.Fatalf("children of a file: %+v", p)
	}
}

func TestChildrenErrors(t *testing.T) {
	e := newEnv(t)
	s := mapTree(t, e)
	big := s.ID("big")
	var p page
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=bytes&limit=3", big), 200, &p)
	bytesCursor := url.QueryEscape(*p.NextCursor)

	for _, target := range []string{
		"/api/entries/%s/children?sort=color",
		"/api/entries/%s/children?order=up",
		"/api/entries/%s/children?limit=0",
		"/api/entries/%s/children?limit=x",
		"/api/entries/%s/children?cursor=not-a-cursor",
		"/api/entries/%s/children?cursor=e30",
		"/api/entries/%s/children?sort=name&cursor=" + bytesCursor,
		"/api/entries/%s/children?sort=bytes&order=asc&cursor=" + bytesCursor,
		"/api/entries/%s/children?sort=bytes&sort=name",
		"/api/entries/%s/children?color=1",
		"/api/entries/%s/treemap?limit=1",
		"/api/entries/%s?x=1",
	} {
		e.fails(t, fmt.Sprintf(target, big), 400, "invalid_request")
	}
	for _, target := range []string{
		"/api/entries/999999/children", "/api/entries/abc/children", "/api/entries/0/children",
		"/api/entries/999999/treemap", "/api/entries/-1/treemap",
		"/api/entries/999999", "/api/entries/12x",
	} {
		e.fails(t, target, 404, "not_found")
	}
}

func TestTreemap(t *testing.T) {
	e := newEnv(t)
	s := mapTree(t, e)
	wide := s.ID("wide")
	// One child goes missing: it takes no space, so it gets no area.
	gone := s.ID("wide/w0999")
	e.exec(t, `UPDATE entries SET state = 'missing', missing_since = 1 WHERE id = ?`, int64(gone))

	type child struct {
		id   domain.EntryID
		size int64
	}
	var kids []child
	var total int64
	for i := range 1001 {
		id := s.ID(fmt.Sprintf("wide/w%04d", i))
		if id == gone {
			continue
		}
		size := int64(i*7919%1000 + 1)
		kids = append(kids, child{id, size})
		total += size
	}
	slices.SortFunc(kids, func(a, b child) int { return cmp.Or(cmp.Compare(b.size, a.size), cmp.Compare(b.id, a.id)) })

	var tm struct {
		Entry row   `json:"entry"`
		Items []row `json:"items"`
		Other struct {
			Count int64 `json:"count"`
			Bytes int64 `json:"bytes"`
		} `json:"other"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/treemap", wide), 200, &tm)
	if tm.Entry.ID != wide.String() || tm.Entry.Path != "wide" {
		t.Errorf("entry %+v, want wide", tm.Entry)
	}
	if len(tm.Items) != 300 {
		t.Fatalf("%d items, want 300", len(tm.Items))
	}
	var top int64
	for i, it := range tm.Items {
		if it.ID != kids[i].id.String() || it.TotalBytes != kids[i].size {
			t.Fatalf("item %d: %s (%d bytes), want %s (%d bytes)", i, it.ID, it.TotalBytes, kids[i].id, kids[i].size)
		}
		top += it.TotalBytes
	}
	if tm.Other.Count != 700 || tm.Other.Bytes != total-top {
		t.Errorf("other %+v, want 700 children and %d bytes", tm.Other, total-top)
	}

	// A folder with fewer children than the limit has an empty other area.
	e.get(t, fmt.Sprintf("/api/entries/%s/treemap", s.Root), 200, &tm)
	if len(tm.Items) != 2 || tm.Other.Count != 0 || tm.Other.Bytes != 0 {
		t.Errorf("root treemap: %d items, other %+v", len(tm.Items), tm.Other)
	}
	// A file has no area inside it.
	e.get(t, fmt.Sprintf("/api/entries/%s/treemap", s.ID("wide/w0001")), 200, &tm)
	if len(tm.Items) != 0 || tm.Other.Count != 0 {
		t.Errorf("file treemap: %+v", tm)
	}
}
