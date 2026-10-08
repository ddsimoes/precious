package cleanup

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

// r41Kept is the kept read of a blocked item.
type r41Kept struct {
	Count int64 `json:"count"`
	Items []struct {
		ID       string  `json:"id"`
		Path     string  `json:"path"`
		Decision *string `json:"decision"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// r41KeptRead reads one page of the kept entries of item of action.
func (w *world) r41KeptRead(action, item, cursor string) r41Kept {
	w.t.Helper()
	path := "/api/history/" + action + "/items/" + item + "/kept"
	if cursor != "" {
		path += "?cursor=" + cursor
	}
	var k r41Kept
	w.get(path, &k)
	return k
}

// r41Tree lists every path below the synthfs root of src, sorted.
func (w *world) r41Tree(src domain.SourceID) []string {
	w.t.Helper()
	d, err := w.sfs.OpenRoot(w.roots[src])
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	var walk func(d fsaccess.Dir, prefix string)
	walk = func(d fsaccess.Dir, prefix string) {
		defer d.Close()
		for {
			batch, err := d.ReadBatch(64)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				w.t.Fatalf("read %q: %v", prefix, err)
			}
			for _, e := range batch {
				p := prefix + string(e.Name)
				out = append(out, p)
				if e.Kind != domain.EntryDirectory {
					continue
				}
				info, err := d.Lstat(e.Name)
				if err != nil {
					w.t.Fatalf("lstat %q: %v", p, err)
				}
				sub, err := d.OpenDir(e.Name, info)
				if err != nil {
					w.t.Fatalf("open %q: %v", p, err)
				}
				walk(sub, p+"/")
			}
		}
	}
	walk(d, "")
	slices.Sort(out)
	return out
}

// r41IndexPaths lists the paths src indexes that are not missing, sorted.
func (w *world) r41IndexPaths(src domain.SourceID) []string {
	w.t.Helper()
	rows, err := w.st.Reader().Query(`SELECT path FROM entries WHERE source_id = ? AND state <> 'missing'`, string(src))
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, string(p))
	}
	if err := rows.Err(); err != nil {
		w.t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func r41Same(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// r41Entries fails unless the action's entries count exactly want by state
// (every other state zero).
func r41Entries(t *testing.T, a actionView, want map[string]int64) {
	t.Helper()
	for state, n := range a.Entries {
		if n != want[state] {
			t.Fatalf("entries %v, want %v", a.Entries, want)
		}
	}
	for state := range want {
		if _, ok := a.Entries[state]; !ok {
			t.Fatalf("entries %v, want %v", a.Entries, want)
		}
	}
}

// R4.1: a discarded folder holding a kept file is listed blocked with the
// kept file named; running the plan quarantines nothing of the folder and
// quarantines the other discarded file.
func TestR4_1KeptFileInsideDiscardedFolder(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2011, 3, 4, 5, 6, 7, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		f := root.Dir("Fotos")
		f.File("praia.jpg", 300, at).Seed(1)
		f.File("copia.jpg", 200, at).Seed(2)
		r := f.Dir("Rascunhos")
		r.File("a.txt", 50, at).Seed(3)
		r.File("b.txt", 60, at).Seed(4)
		root.File("antigo.zip", 500, at).Seed(5)
		root.File("notas.txt", 70, at).Seed(6)
	})
	w.decide("casa", "Fotos", "discard")
	w.decide("casa", "Fotos/praia.jpg", "keep")
	w.decide("casa", "Fotos/Rascunhos", "discard")
	w.decide("casa", "antigo.zip", "discard")

	folder := []string{"Fotos", "Fotos/Rascunhos", "Fotos/Rascunhos/a.txt", "Fotos/Rascunhos/b.txt",
		"Fotos/copia.jpg", "Fotos/praia.jpg"}
	folderIDs := make([]string, len(folder))
	for i, p := range folder {
		folderIDs[i] = w.id("casa", p)
	}
	other := w.id("casa", "antigo.zip")
	before := []string{"Fotos", "Fotos/Rascunhos", "Fotos/Rascunhos/a.txt", "Fotos/Rascunhos/b.txt",
		"Fotos/copia.jpg", "Fotos/praia.jpg", "antigo.zip", "notas.txt"}
	r41Same(t, "disk before", w.r41Tree("casa"), before)

	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	id := p.Action.ID
	q := ".precious-quarantine/" + id
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+q,
		"rename blocked holds_kept Fotos",
		"mkdir planned -> "+q+"/1",
		"rename planned antigo.zip -> "+q+"/1/antigo.zip",
		"record planned -> "+q+"/1.json",
	)
	blocked := p.Items[2]
	if blocked.Entry == nil || blocked.Entry.ID != folderIDs[0] || blocked.Bytes != 610 || blocked.Files != 4 ||
		blocked.KeptCount == nil || *blocked.KeptCount != 1 {
		t.Fatalf("blocked item %+v (entry %+v, kept %v), want entry %s, 610 bytes, 4 files, kept_count 1",
			blocked, blocked.Entry, blocked.KeptCount, folderIDs[0])
	}
	for i, it := range p.Items {
		if i != 2 && it.KeptCount != nil {
			t.Fatalf("item %d (%s) has kept_count %d", i, it.summary(), *it.KeptCount)
		}
	}
	if p.Items[4].Entry == nil || p.Items[4].Entry.ID != other || p.Items[4].Bytes != 500 || p.Items[4].Files != 1 {
		t.Fatalf("planned item %+v, want entry %s, 500 bytes, 1 file", p.Items[4], other)
	}
	if *p.Summary != (summaryJSON{NoCopyBytes: 500}) {
		t.Fatalf("summary %+v", *p.Summary)
	}
	if p.Action.Kind != "cleanup" || p.Action.State != "planned" || p.Action.Bytes != 500 || p.Action.Files != 1 {
		t.Fatalf("action %+v", p.Action)
	}
	r41Entries(t, p.Action, map[string]int64{"planned": 1, "blocked": 1})

	k := w.r41KeptRead(id, blocked.ID, "")
	if k.Count != 1 || k.NextCursor != nil || len(k.Items) != 1 || k.Items[0].ID != w.id("casa", "Fotos/praia.jpg") ||
		k.Items[0].Path != "Fotos/praia.jpg" || k.Items[0].Decision == nil || *k.Items[0].Decision != "keep" {
		t.Fatalf("kept %+v", k)
	}
	// The planned item blocks on nothing.
	if k := w.r41KeptRead(id, p.Items[4].ID, ""); k.Count != 0 || len(k.Items) != 0 || k.NextCursor != nil {
		t.Fatalf("kept of the planned item %+v", k)
	}

	a := w.run(id)
	if a.State != "done" {
		t.Fatalf("action %+v", a)
	}
	r41Entries(t, a, map[string]int64{"blocked": 1, "done": 1})
	wantItems(t, w.items(id, ""),
		"mkdir done -> .precious-quarantine",
		"mkdir done -> "+q,
		"rename blocked holds_kept Fotos",
		"mkdir done -> "+q+"/1",
		"rename done antigo.zip -> "+q+"/1/antigo.zip",
		"record done -> "+q+"/1.json",
	)

	// Nothing of the folder moved: same IDs at the same paths, on disk and
	// in the index; the other file is in quarantine with its record.
	for i, p := range folder {
		if got, state := w.pathOf(folderIDs[i]); got != p || state == "missing" {
			t.Fatalf("entry %s of %s is at %q (%s)", folderIDs[i], p, got, state)
		}
		if !w.exists("casa", p) {
			t.Fatalf("%s gone from disk", p)
		}
	}
	if got, state := w.pathOf(other); got != q+"/1/antigo.zip" || state == "missing" {
		t.Fatalf("antigo.zip is at %q (%s)", got, state)
	}
	r41Same(t, "disk after", w.r41Tree("casa"), []string{
		".precious-quarantine", q, q + "/1", q + "/1.json", q + "/1/antigo.zip",
		"Fotos", "Fotos/Rascunhos", "Fotos/Rascunhos/a.txt", "Fotos/Rascunhos/b.txt",
		"Fotos/copia.jpg", "Fotos/praia.jpg", "notas.txt",
	})
	var inQuarantine []string
	for _, p := range w.r41IndexPaths("casa") {
		if strings.HasPrefix(p, ".precious-quarantine") {
			inQuarantine = append(inQuarantine, p)
		}
	}
	r41Same(t, "index in quarantine", inQuarantine, []string{".precious-quarantine", q, q + "/1", q + "/1/antigo.zip"})

	qv := w.quarantined("casa")
	if len(qv.Items) != 1 || qv.Items[0].Entry.ID != other || qv.Items[0].Entry.Name != "antigo.zip" ||
		qv.Items[0].Original == nil || qv.Items[0].Original.Path != "antigo.zip" ||
		qv.Items[0].PlanID == nil || *qv.Items[0].PlanID != id || qv.Items[0].Bytes != 500 || qv.Items[0].Files != 1 {
		t.Fatalf("quarantine %+v", qv)
	}
	if qv.Total != (amount{Files: 1, Bytes: 500}) {
		t.Fatalf("quarantine total %+v", qv.Total)
	}
}

// The kept read pages 100 entries at a time, by path, and counts them all.
func TestR4_1KeptReadPages(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2011, 3, 4, 5, 6, 7, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		a := root.Dir("Arquivo")
		for i := range 150 {
			a.File(fmt.Sprintf("k%03d.jpg", i), int64(10+i), at).Seed(uint64(i + 1))
		}
		a.Dir("Sub").File("z.jpg", 5, at).Seed(500)
		a.File("solto.tmp", 7, at).Seed(501)
	})
	w.decide("casa", "Arquivo", "discard")
	var want []string // kept paths, by path
	want = append(want, "Arquivo/Sub/z.jpg")
	for i := range 150 {
		want = append(want, fmt.Sprintf("Arquivo/k%03d.jpg", i))
	}
	keep := make([]string, len(want))
	for i, p := range want {
		keep[i] = w.id("casa", p)
	}
	w.ok(http.StatusOK, "set-decision", `{"entry_ids":`+ids(keep...)+`,"decision":"keep"}`)

	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	// Nothing planned: no quarantine folders, only the blocked item (E1).
	wantItems(t, p.Items, "rename blocked holds_kept Arquivo")
	item := p.Items[0]
	if item.KeptCount == nil || *item.KeptCount != 151 {
		t.Fatalf("kept_count %v, want 151", item.KeptCount)
	}

	first := w.r41KeptRead(p.Action.ID, item.ID, "")
	if first.Count != 151 || len(first.Items) != 100 || first.NextCursor == nil {
		t.Fatalf("first page: count %d, %d items, cursor %v", first.Count, len(first.Items), first.NextCursor)
	}
	second := w.r41KeptRead(p.Action.ID, item.ID, *first.NextCursor)
	if second.Count != 151 || len(second.Items) != 51 || second.NextCursor != nil {
		t.Fatalf("second page: count %d, %d items, cursor %v", second.Count, len(second.Items), second.NextCursor)
	}
	var got, gotIDs []string
	for _, e := range append(first.Items, second.Items...) {
		got = append(got, e.Path)
		gotIDs = append(gotIDs, e.ID)
	}
	r41Same(t, "kept paths", got, want)
	r41Same(t, "kept IDs", gotIDs, keep)
}
