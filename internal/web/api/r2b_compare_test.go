package api

import (
	"fmt"
	"net/url"
	"slices"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// r2b tasks 6.1–6.3: Compare's paths and extra copies, the similar folders
// list, and the cards' decided figures.

type compareItemRes struct {
	Path      string      `json:"path"`
	LeftPath  *string     `json:"left_path"`
	RightPath *string     `json:"right_path"`
	Left      *contentRow `json:"left"`
	Right     *contentRow `json:"right"`
	Twin      *struct {
		Path  string     `json:"path"`
		Entry contentRow `json:"entry"`
	} `json:"twin"`
}

type compareRes struct {
	Bucket  string               `json:"bucket"`
	Summary map[string]amountRes `json:"summary"`
	Items   []compareItemRes     `json:"items"`
}

// Spec "Compare shows where each copy is": copies filed under different
// folders show each side's path, and an extra copy on one side names the
// other side's file; without a bucket the first holding files is listed
// and named.
func TestR2bCompareShowsWhereEachCopyIs(t *testing.T) {
	e := newEnv(t)
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "disco", CreateSource: true, MountPoint: "/mnt/disco",
		Nodes: []indextest.Node{
			{Path: "fotos-b/2002/12/img_0001.jpg", Size: 100},
			{Path: "fotos/2014/celular/IMG_0001.jpg", Size: 100},
			{Path: "fotos/2015/IMG_0001 (1).jpg", Size: 100},
			{Path: "fotos/2016/novo.jpg", Size: 300},
		}})
	for _, p := range []string{"fotos-b/2002/12/img_0001.jpg", "fotos/2014/celular/IMG_0001.jpg", "fotos/2015/IMG_0001 (1).jpg"} {
		s.SetContent(e.st, p, indextest.Content{State: domain.ContentHashed, SHA256: digest("img_0001")})
	}
	s.SetContent(e.st, "fotos/2016/novo.jpg", indextest.Content{State: domain.ContentUniqueSize})
	left, right := s.ID("fotos-b").String(), s.ID("fotos").String()

	var res compareRes
	e.get(t, fmt.Sprintf("/api/compare?left=%s&right=%s&bucket=identical", left, right), 200, &res)
	if res.Bucket != "identical" || len(res.Items) != 2 {
		t.Fatalf("identical: %+v", res)
	}
	pair, extra := res.Items[0], res.Items[1]
	if str(pair.LeftPath) != "2002/12/img_0001.jpg" || str(pair.RightPath) != "2014/celular/IMG_0001.jpg" ||
		pair.Left == nil || pair.Right == nil || pair.Twin != nil {
		t.Errorf("pair %+v", pair)
	}
	if extra.Left != nil || extra.LeftPath != nil || str(extra.RightPath) != "2015/IMG_0001 (1).jpg" || extra.Twin == nil ||
		extra.Twin.Path != "2002/12/img_0001.jpg" || extra.Twin.Entry.ID != s.ID("fotos-b/2002/12/img_0001.jpg").String() {
		t.Errorf("extra copy %+v", extra)
	}
	// Counts and bytes are unchanged: two items of 100 bytes each.
	if got := res.Summary["identical"]; got != (amountRes{Files: 2, Bytes: 200}) {
		t.Errorf("identical summary %+v", got)
	}

	// Without a bucket: novo.jpg is only on the right, which comes first.
	e.get(t, fmt.Sprintf("/api/compare?left=%s&right=%s", left, right), 200, &res)
	if res.Bucket != "only_right" || len(res.Items) != 1 || res.Items[0].Path != "2016/novo.jpg" ||
		res.Items[0].LeftPath != nil || str(res.Items[0].RightPath) != "2016/novo.jpg" {
		t.Errorf("opening: %+v", res)
	}
}

type overlapRes struct {
	relationRes
	A contentRow `json:"a"`
}

type overlapsPage struct {
	Items      []overlapRes `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// Spec "Similar folders are listed", scenario "Similar folders on the
// corpus": the Fotos - Copia/Fotos overlap is listed with its bytes in
// common and the files only on each side, largest first, following the
// source; both sides open in Compare.
func TestR2bSimilarFoldersOnTheCorpus(t *testing.T) {
	w := newContentWorld(t)
	all := func(query string, limit int) []overlapRes {
		t.Helper()
		var out []overlapRes
		cursor := ""
		for {
			q := fmt.Sprintf("/api/relations?kind=overlap%s&limit=%d", query, limit)
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			var p overlapsPage
			w.get(t, q, 200, &p)
			out = append(out, p.Items...)
			if p.NextCursor == nil {
				return out
			}
			cursor = *p.NextCursor
		}
	}
	items := all("", 500)
	var stored int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM relations
		WHERE kind = 'overlap' AND gen = (SELECT gen FROM review_state WHERE id = 1)`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(items) != stored {
		t.Errorf("%d overlaps listed, %d stored", len(items), stored)
	}
	listed := map[[2]string]bool{}
	for _, it := range items {
		listed[[2]string{it.A.Path, it.Other.Path}] = true
	}
	for _, r := range w.gt.Relations {
		if r.Kind == "overlap" && !listed[[2]string{r.A.Path, r.B.Path}] {
			t.Errorf("the declared overlap %s/%s is not listed", r.A.Path, r.B.Path)
		}
	}
	var fotos *overlapRes
	for i, it := range items {
		if it.Kind != "overlap" || it.Self != "a" || it.A.ID == "" || it.Other.ID == "" || it.MatchedBytes <= 0 {
			t.Errorf("item %+v", it)
		}
		if i > 0 && (it.MatchedBytes > items[i-1].MatchedBytes ||
			it.MatchedBytes == items[i-1].MatchedBytes && idNum(it.ID) > idNum(items[i-1].ID)) {
			t.Errorf("item %s after %s is out of order", it.ID, items[i-1].ID)
		}
		if it.A.Path == "Fotos - Copia" && it.Other.Path == "Fotos" {
			fotos = &items[i]
		}
	}
	if fotos == nil {
		t.Fatalf("Fotos - Copia/Fotos is not listed: %+v", items)
	}
	if fotos.A.ID != w.id("Fotos - Copia") || fotos.Other.ID != w.id("Fotos") ||
		fotos.OnlyHere.Files != 1 || fotos.OnlyThere.Files != 3 || fotos.OnlyHere.Bytes <= 0 || fotos.OnlyThere.Bytes <= 0 {
		t.Errorf("Fotos - Copia/Fotos %+v", fotos)
	}
	// Paging one at a time lists the same items.
	paged := all("", 1)
	if !slices.EqualFunc(paged, items, func(a, b overlapRes) bool { return a.ID == b.ID }) {
		t.Errorf("paged %d items differ from %d", len(paged), len(items))
	}
	// The source filter: the corpus holds both sides of each.
	if got := all("&source=corpus", 500); len(got) != len(items) {
		t.Errorf("source corpus: %d items, want %d", len(got), len(items))
	}
	e := w.env
	indextest.Seed(t, e.st, indextest.Tree{Source: "pen", CreateSource: true, Nodes: []indextest.Node{{Path: "a.txt", Size: 1}}})
	if got := all("&source=pen", 500); len(got) != 0 {
		t.Errorf("source pen: %+v", got)
	}
	// Compare opens on the two.
	var cmp compareRes
	w.get(t, fmt.Sprintf("/api/compare?left=%s&right=%s", fotos.A.ID, fotos.Other.ID), 200, &cmp)
	if cmp.Bucket != "only_left" || cmp.Summary["only_left"].Files != 1 || cmp.Summary["only_right"].Files != 3 {
		t.Errorf("compare of the relation: %+v", cmp)
	}

	w.fails(t, "/api/relations", 400, "invalid_request")
	w.fails(t, "/api/relations?kind=same", 400, "invalid_request")
	w.fails(t, "/api/relations?kind=overlap&cursor=x", 400, "invalid_request")
	w.fails(t, "/api/relations?kind=overlap&limit=0", 400, "invalid_request")
	w.fails(t, "/api/relations?kind=overlap&source=nope", 404, "not_found")
}

func idNum(id string) int64 {
	var n int64
	_, _ = fmt.Sscan(id, &n)
	return n
}

type decidedCardRes struct {
	cardRes
	DecidedBytes int64 `json:"decided_bytes"`
	DecidedRows  int64 `json:"decided_rows"`
}

// Spec "Cards show what was decided", scenario "Progress after deciding":
// discarding rows of the leftovers list moves their count and bytes from
// the card's open figures to its decided ones, on the card and in the
// list's header.
func TestR2bCardsShowWhatWasDecided(t *testing.T) {
	w := newContentWorld(t)
	card := func() decidedCardRes {
		t.Helper()
		var opp struct {
			Cards []decidedCardRes `json:"cards"`
		}
		w.get(t, "/api/opportunities", 200, &opp)
		var list struct {
			Card decidedCardRes `json:"card"`
		}
		w.get(t, "/api/opportunities/leftovers?limit=1", 200, &list)
		for _, c := range opp.Cards {
			if c.List == "leftovers" {
				if c != list.Card {
					t.Errorf("card %+v, list header %+v", c, list.Card)
				}
				return c
			}
		}
		t.Fatal("no leftovers card")
		return decidedCardRes{}
	}
	before := card()
	if before.Rows < 2 || before.DecidedRows != 0 || before.DecidedBytes != 0 {
		t.Fatalf("leftovers before deciding: %+v", before)
	}
	var page struct {
		Items []struct {
			Bytes int64       `json:"bytes"`
			Entry *contentRow `json:"entry"`
		} `json:"items"`
	}
	w.get(t, "/api/opportunities/leftovers?limit=2", 200, &page)
	cmds := w.commands(t)
	var bytes int64
	for _, it := range page.Items {
		bytes += it.Bytes
		if code, body := w.post(t, cmds, "set-decision",
			fmt.Sprintf(`{"entry_id":%q,"decision":"discard"}`, it.Entry.ID)); code != 200 {
			t.Fatalf("discard %s: %d %s", it.Entry.ID, code, body)
		}
	}
	after := card()
	if after.Rows != before.Rows-2 || after.Bytes != before.Bytes-bytes || after.DecidedRows != 2 || after.DecidedBytes != bytes {
		t.Errorf("leftovers after discarding 2 rows of %d bytes: %+v, before %+v", bytes, after, before)
	}
}
