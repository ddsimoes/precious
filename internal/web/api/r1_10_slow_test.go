//go:build slow

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"precious/internal/fsaccess/synthfs"
)

// r1_10Entries is the size of the generated tree, its root included.
const r1_10Entries = 2_000_000

// TestR1_10MapPagesStayFastAt2MillionEntries scans a generated synthfs tree
// of 2,000,000 entries, then times through the handlers 200 pages of
// children sorted by bytes (p95 < 300 ms) and 50 treemap levels (p95 <
// 500 ms) (R1.10, design D11). The tree mixes the shapes a disk has:
//
//   - flat: one folder of 20,000 files, paged to its end;
//   - wide: 6,000 folders of 49 files each, paged to its end;
//   - mid: 150 folders of 150 folders of about 20 files;
//   - deep: folders of 12 entries, about six levels down.
//
// Run with: go test -tags slow -run R1_10 -v ./internal/web/api/
func TestR1_10MapPagesStayFastAt2MillionEntries(t *testing.T) {
	e := newEnv(t)
	sfs := synthfs.New()
	root := sfs.Root("/big")
	root.Generated("flat", 20_000, 20_000)
	root.Generated("wide", 300_000, 6_000)
	root.Generated("mid", 480_000, 150)
	// The root, its four folders, and the rest in deep.
	root.Generated("deep", r1_10Entries-5-20_000-300_000-480_000, 12)

	start := time.Now()
	rootID := e.scanSynth(t, sfs, "big", "/big", root)
	scan := time.Since(start)
	var n int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM entries WHERE source_id = 'big'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != r1_10Entries {
		t.Fatalf("indexed %d entries, want %d", n, r1_10Entries)
	}
	t.Logf("scan of %d entries: %s (%.0f entries/s)", n, scan.Round(time.Millisecond), float64(n)/scan.Seconds())

	var top page
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=name", rootID), 200, &top)
	folder := map[string]string{}
	for _, r := range top.Items {
		folder[r.Name] = r.ID
	}

	// 200 pages of children by bytes.
	var pages []time.Duration
	pageThrough := func(id string, max int) {
		cursor := ""
		for range max {
			target := fmt.Sprintf("/api/entries/%s/children?sort=bytes", id)
			if cursor != "" {
				target += "&cursor=" + url.QueryEscape(cursor)
			}
			var p page
			pages = append(pages, e.timed(t, target, &p))
			if len(p.Items) == 0 || p.NextCursor == nil {
				return
			}
			cursor = *p.NextCursor
		}
	}
	pageThrough(folder["flat"], 100)
	pageThrough(folder["wide"], 30)
	var midKids page
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=name", folder["mid"]), 200, &midKids)
	for _, r := range midKids.Items {
		if len(pages) == 200 {
			break
		}
		pageThrough(r.ID, 1)
	}
	if len(pages) != 200 {
		t.Fatalf("timed %d pages, want 200", len(pages))
	}

	// 50 treemap levels: the root and its folders, then drilling into the
	// largest folder from each folder of deep and mid, as a user does.
	type treemap struct {
		Items []row `json:"items"`
		Other struct {
			Count int64 `json:"count"`
		} `json:"other"`
	}
	var levels []time.Duration
	level := func(id string) treemap {
		var tm treemap
		levels = append(levels, e.timed(t, fmt.Sprintf("/api/entries/%s/treemap", id), &tm))
		return tm
	}
	var starts [2][]row
	level(rootID.String())
	if tm := level(folder["flat"]); len(tm.Items) != 300 || tm.Other.Count != 19_700 {
		t.Fatalf("flat treemap: %d items, %d others", len(tm.Items), tm.Other.Count)
	}
	level(folder["wide"])
	starts[0] = level(folder["deep"]).Items
	starts[1] = level(folder["mid"]).Items
drill:
	for j := 0; ; j++ {
		for _, s := range starts {
			if j >= len(s) {
				t.Fatal("ran out of folders to drill into")
			}
			for r := s[j]; r.Kind == "directory"; {
				if len(levels) == 50 {
					break drill
				}
				tm := level(r.ID)
				r = row{}
				for _, it := range tm.Items {
					if it.Kind == "directory" {
						r = it
						break
					}
				}
			}
		}
	}

	p50, p95 := percentiles(pages)
	t.Logf("children pages by bytes: %d, p50 %s, p95 %s, max %s", len(pages), p50, p95, slices.Max(pages))
	if p95 >= 300*time.Millisecond {
		t.Errorf("children page p95 %s, want under 300 ms", p95)
	}
	p50, p95 = percentiles(levels)
	t.Logf("treemap levels: %d, p50 %s, p95 %s, max %s", len(levels), p50, p95, slices.Max(levels))
	if p95 >= 500*time.Millisecond {
		t.Errorf("treemap p95 %s, want under 500 ms", p95)
	}
}

// timed serves target, checks it answers 200, decodes the body into out, and
// returns how long the handler took, encoding included.
func (e *env) timed(t *testing.T, target string, out any) time.Duration {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	start := time.Now()
	e.mux.ServeHTTP(rec, req)
	d := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", target, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	return d
}

// percentiles returns the nearest-rank 50th and 95th percentiles of ds.
func percentiles(ds []time.Duration) (p50, p95 time.Duration) {
	s := slices.Sorted(slices.Values(ds))
	rank := func(p int) time.Duration { return s[(len(s)*p+99)/100-1] }
	return rank(50), rank(95)
}
