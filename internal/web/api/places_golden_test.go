//go:build slow

package api

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

// checkedAt matches a content's checked_at time.
var checkedAt = regexp.MustCompile(`"checked_at":"[^"]*"`)

// goldenFile holds, for every request goldenResponses makes on the corpus
// world, the first 64 bits of the SHA-256 of its body, as the build before
// r5c answered it. Set PRECIOUS_UPDATE_GOLDEN=1 to rewrite it, only ever
// from a build whose answers without places=1 are meant to change.
const goldenFile = "testdata/places_golden.txt"

// r5c task 3.1, design D10 and Interfaces: without places=1 no response
// changes. Every children page (each sort and order, small pages followed
// through their cursors), treemap, and entry of the corpus entries and
// archive members, and a set of searches (about 3,500 requests, 20 ms each
// under the race detector), answer byte for byte what the previous build
// answered. With PRECIOUS_UPDATE_GOLDEN set, it rewrites the golden file
// instead.
func TestWithoutPlacesNoCorpusResponseChanges(t *testing.T) {
	w := newContentWorld(t)
	got := goldenResponses(t, w)
	if os.Getenv("PRECIOUS_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skipf("wrote %d responses to %s", len(got), goldenFile)
	}
	f, err := os.Open(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := map[string]string{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, sum, _ := cutLast(sc.Text(), " ")
		want[name] = sum
		order = append(order, name)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	diffs := 0
	for i, line := range got {
		name, sum, _ := cutLast(line, " ")
		if w, ok := want[name]; !ok || w != sum {
			if diffs++; diffs <= 10 {
				t.Errorf("response %d, %s: digest %s, recorded %q", i, name, sum, w)
			}
		}
	}
	if len(got) != len(order) {
		t.Errorf("%d responses, %d recorded", len(got), len(order))
	}
	if diffs > 0 {
		t.Fatalf("%d of %d responses differ", diffs, len(got))
	}
}

// cutLast cuts s around the last sep.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// goldenResponses requests every corpus listing, treemap, and entry, and a
// set of searches, and returns one "request sha256" line per request, in
// request order. A page after the first is named by its listing and
// number: its cursor is in the body of the page before it.
func goldenResponses(t *testing.T, w *contentWorld) []string {
	t.Helper()
	// pages follows a listing through its cursors; an entry or a treemap
	// is one page.
	pages := func(base string) ([]string, error) {
		var out []string
		target := base
		for i := range 1000 {
			rec := httptest.NewRecorder()
			w.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			if rec.Code != http.StatusOK {
				return nil, fmt.Errorf("GET %s: status %d; body %s", target, rec.Code, rec.Body)
			}
			name := base
			if i > 0 {
				name = fmt.Sprintf("%s #%d", base, i+1)
			}
			// Seeding reads the files at the real time, so content.checked_at
			// is the one field that differs between runs; 64 bits of the
			// digest tell any change.
			sum := sha256.Sum256(checkedAt.ReplaceAll(rec.Body.Bytes(), []byte(`"checked_at":"seeded"`)))
			out = append(out, name+" "+hex.EncodeToString(sum[:8]))
			var p struct {
				NextCursor *string `json:"next_cursor"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				return nil, fmt.Errorf("GET %s: %v", target, err)
			}
			if p.NextCursor == nil {
				return out, nil
			}
			target = base + "&cursor=" + url.QueryEscape(*p.NextCursor)
		}
		return nil, fmt.Errorf("GET %s: too many pages", base)
	}
	type level struct {
		ref    string
		listed bool // a folder, a complete archive, or a member folder
	}
	var levels []level
	rows, err := w.st.Reader().Query(`SELECT e.id, e.kind = 'directory' OR ifnull(a.state = 'complete', 0)
		FROM entries e LEFT JOIN archives a ON a.entry_id = e.id ORDER BY e.id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var (
			id     int64
			listed bool
		)
		if err := rows.Scan(&id, &listed); err != nil {
			t.Fatal(err)
		}
		levels = append(levels, level{ref: fmt.Sprint(id), listed: listed})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	rows, err = w.st.Reader().Query(`SELECT m.id, m.kind = 'directory' FROM archive_members m
		JOIN archives a ON a.entry_id = m.archive_id AND a.state = 'complete' ORDER BY m.id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var (
			id     int64
			listed bool
		)
		if err := rows.Scan(&id, &listed); err != nil {
			t.Fatal(err)
		}
		levels = append(levels, level{ref: fmt.Sprintf("m%d", id), listed: listed})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(levels) < 100 {
		t.Fatalf("only %d entries and members", len(levels))
	}
	var listings []string
	for _, l := range levels {
		listings = append(listings, "/api/entries/"+l.ref)
		if !l.listed {
			continue
		}
		listings = append(listings, "/api/entries/"+l.ref+"/treemap")
		for _, sort := range []string{"bytes", "files", "newest", "name"} {
			for _, order := range []string{"", "&order=asc"} {
				listings = append(listings, "/api/entries/"+l.ref+"/children?sort="+sort+order+"&limit=4")
			}
		}
		listings = append(listings, "/api/entries/"+l.ref+"/children?kind=directory&limit=3",
			"/api/entries/"+l.ref+"/children")
	}
	for _, q := range []string{"", "dup=copies", "dup=unique", "dup=unchecked", "name=txt",
		"sort=name&order=asc", "sort=bytes&order=asc"} {
		listings = append(listings, "/api/search?"+q+"&limit=50")
	}
	var out []string
	for _, l := range listings {
		lines, err := pages(l)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, lines...)
	}
	return out
}
