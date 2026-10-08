package cleanup

import (
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

// R4.4: a plan drafted from the duplicates list never takes the last copy
// of a content group (last_copy) nor both sides of a relation (both_sides),
// and running it quarantines only what it planned (design D5).

// dupsRow is a duplicates list row as GET /api/opportunities/duplicates
// renders it: a relation row (entry and relation) or a content group row
// (copies).
type dupsRow struct {
	ID       string    `json:"id"`
	Bytes    int64     `json:"bytes"`
	Entry    *pathView `json:"entry"`
	Relation *struct {
		Kind  string   `json:"kind"`
		Other pathView `json:"other"`
	} `json:"relation"`
	Copies []struct {
		Path     string `json:"path"`
		HardLink bool   `json:"hard_link"`
	} `json:"copies"`
}

// dupsRows reads every page of the open rows of src's duplicates list.
func (w *world) dupsRows(src domain.SourceID) []dupsRow {
	w.t.Helper()
	var out []dupsRow
	cursor := ""
	for {
		var p struct {
			Items      []dupsRow `json:"items"`
			NextCursor *string   `json:"next_cursor"`
		}
		path := "/api/opportunities/duplicates?source=" + string(src)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w.get(path, &p)
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out
		}
		cursor = *p.NextCursor
	}
}

// dupsGroup is the content group row of src listing a copy at path, and
// its copies' paths in path order.
func (w *world) dupsGroup(src domain.SourceID, path string) (dupsRow, []string) {
	w.t.Helper()
	for _, r := range w.dupsRows(src) {
		var paths []string
		for _, c := range r.Copies {
			paths = append(paths, c.Path)
		}
		if slices.Contains(paths, path) {
			slices.Sort(paths)
			return r, paths
		}
	}
	w.t.Fatalf("no duplicates group row lists %q", path)
	return dupsRow{}, nil
}

// dupsOutside lists, in path order, the present files of src outside the
// quarantine that hold the content of the file entry id.
func (w *world) dupsOutside(src domain.SourceID, id string) []string {
	w.t.Helper()
	rows, err := w.st.Reader().Query(`SELECT e.path FROM file_content fc JOIN entries e ON e.id = fc.entry_id
		WHERE e.source_id = ? AND e.state = 'present' AND e.kind = 'file'
			AND fc.content_id = (SELECT content_id FROM file_content WHERE entry_id = ?)
		ORDER BY e.path`, string(src), id)
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
		if !strings.HasPrefix(string(p), ".precious-quarantine/") {
			out = append(out, string(p))
		}
	}
	if err := rows.Err(); err != nil {
		w.t.Fatal(err)
	}
	return out
}

// dupsWant fails unless got equals want.
func dupsWant[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// dupsEntries is the rename counts by state that the history shows for a
// cleanup action: planned and refused, or done and refused.
func dupsEntries(t *testing.T, a actionView, want map[string]int64) {
	t.Helper()
	for state, n := range a.Entries {
		if n != want[state] {
			t.Fatalf("action %s entries %v, want %v", a.ID, a.Entries, want)
		}
	}
}

// dupsGround checks a plan drafted from the duplicates list.
func dupsGround(t *testing.T, a actionView) {
	t.Helper()
	if a.Kind != "cleanup" || a.Ground == nil || *a.Ground != "duplicate" || a.List == nil || *a.List != "duplicates" {
		t.Fatalf("action %+v: want a cleanup with ground duplicate from the list duplicates", a)
	}
}

// TestR4_4LastCopyOfAGroupStays: on the regression corpus, the duplicates
// card lists curriculo.doc as one content group. The corpus holds four
// copies of it (two in the old backups' profiles, two under Documentos);
// the owner discards every copy the card lists and drafts from the list.
// In path order the first three copies are planned, each leaving a copy
// outside the plan; the fourth would leave none and is refused last_copy.
// Running the plan quarantines the three and leaves exactly one copy
// outside the quarantine, on disk and in the index.
func TestR4_4LastCopyOfAGroupStays(t *testing.T) {
	w := newWorld(t)
	root, _ := corpus.BuildSynth(w.sfs, "/casa", corpus.Corpus())
	w.add("casa", "/casa", root, "ext4")

	row, copies := w.dupsGroup("casa", "Documentos/curriculo.doc")
	if row.Entry != nil || row.Relation != nil || len(copies) != 4 || row.Bytes != 73_728 {
		t.Fatalf("curriculo.doc row %+v, copies %q: want a content group of 4 copies, 73728 redundant bytes", row, copies)
	}
	// The backups' copies sit in a profile's "Meus documentos" folder.
	const doc = "/Meus documentos/curriculo.doc"
	backupA, backupB := copies[0], copies[3]
	if !strings.HasPrefix(backupA, "Backup_PC_2004/C/Documents and Settings/") || !strings.HasSuffix(backupA, doc) ||
		copies[1] != "Documentos/curriculo (1).doc" || copies[2] != "Documentos/curriculo.doc" ||
		!strings.HasPrefix(backupB, "HD antigo/backup pc velho/Documents and Settings/") || !strings.HasSuffix(backupB, doc) {
		t.Fatalf("curriculo.doc copies %q", copies)
	}
	entry := map[string]string{}
	for _, p := range copies {
		entry[p] = w.id("casa", p)
		w.decide("casa", p, "discard")
	}

	p := w.plan("plan-cleanup", `{"source_id":"casa","list":"duplicates"}`)
	a, q := p.Action.ID, ".precious-quarantine/"+p.Action.ID
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+q,
		"mkdir planned -> "+q+"/1",
		"rename planned "+backupA+" -> "+q+"/1/curriculo.doc",
		"record planned -> "+q+"/1.json",
		"mkdir planned -> "+q+"/2",
		"rename planned Documentos/curriculo (1).doc -> "+q+"/2/curriculo (1).doc",
		"record planned -> "+q+"/2.json",
		"mkdir planned -> "+q+"/3",
		"rename planned Documentos/curriculo.doc -> "+q+"/3/curriculo.doc",
		"record planned -> "+q+"/3.json",
		"rename refused last_copy "+backupB,
	)
	dupsGround(t, p.Action)
	dupsEntries(t, p.Action, map[string]int64{"planned": 3, "refused": 1})
	// Every planned byte keeps a copy outside the plan: the refused one.
	dupsWant(t, "summary", *p.Summary, summaryJSON{WithCopyBytes: 3 * 24_576})

	done := w.run(a)
	dupsWant(t, "state", done.State, "done")
	dupsGround(t, done)
	dupsEntries(t, done, map[string]int64{"done": 3, "refused": 1})
	wantItems(t, w.items(a, "op=rename"),
		"rename done "+backupA+" -> "+q+"/1/curriculo.doc",
		"rename done Documentos/curriculo (1).doc -> "+q+"/2/curriculo (1).doc",
		"rename done Documentos/curriculo.doc -> "+q+"/3/curriculo.doc",
		"rename refused last_copy "+backupB,
	)

	// The disk: the three are in the plan folder, the fourth where it was.
	moved := map[string]string{
		backupA:                        q + "/1/curriculo.doc",
		"Documentos/curriculo (1).doc": q + "/2/curriculo (1).doc",
		"Documentos/curriculo.doc":     q + "/3/curriculo.doc",
	}
	for from, to := range moved {
		if w.exists("casa", from) || !w.exists("casa", to) {
			t.Fatalf("disk: %q exists %v, %q exists %v", from, w.exists("casa", from), to, w.exists("casa", to))
		}
		if path, state := w.pathOf(entry[from]); path != to || state != "present" {
			t.Fatalf("index: entry of %q at %q %s, want %q present", from, path, state, to)
		}
	}
	if !w.exists("casa", backupB) {
		t.Fatalf("disk: the last copy %q is gone", backupB)
	}
	if path, state := w.pathOf(entry[backupB]); path != backupB || state != "present" {
		t.Fatalf("index: the last copy at %q %s", path, state)
	}
	// Exactly one copy is left outside the quarantine.
	if got := w.dupsOutside("casa", entry[backupB]); !slices.Equal(got, []string{backupB}) {
		t.Fatalf("copies outside the quarantine %q, want only %q", got, backupB)
	}
	var quarantined []string
	for _, it := range w.quarantined("casa").Items {
		if it.Original == nil || it.PlanID == nil || *it.PlanID != a {
			t.Fatalf("quarantined %+v: want an origin in plan %s", it, a)
		}
		quarantined = append(quarantined, it.Original.Path)
	}
	slices.Sort(quarantined)
	if want := []string{backupA, "Documentos/curriculo (1).doc", "Documentos/curriculo.doc"}; !slices.Equal(quarantined, want) {
		t.Fatalf("quarantine holds %q, want %q", quarantined, want)
	}
}

// TestR4_4LastCopyOfAPairStays is R4.4 word for word: exactly two copies of
// curriculo.doc, both discarded (the corpus holds four, so this small disk
// gives the two-copy case). The first by path is planned, the other refused
// last_copy, and running the plan leaves it as the one copy outside the
// quarantine.
func TestR4_4LastCopyOfAPairStays(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2004, 3, 2, 23, 41, 0, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		// Each folder also holds a file of its own, so the two folders are
		// no relation and the copies stay a content group row.
		docs := root.Dir("Documentos")
		docs.File("curriculo.doc", 24_576, at).Seed(41)
		docs.File("carta.doc", 9_216, at).Seed(43)
		mine := root.Dir("Meus documentos")
		mine.File("curriculo.doc", 24_576, at).Seed(41)
		mine.File("notas.doc", 11_264, at).Seed(44)
		root.File("outro.doc", 24_576, at).Seed(42)
	})

	const first, second = "Documentos/curriculo.doc", "Meus documentos/curriculo.doc"
	rows := w.dupsRows("casa")
	row, copies := w.dupsGroup("casa", first)
	if len(rows) != 1 || row.Bytes != 24_576 || !slices.Equal(copies, []string{first, second}) {
		t.Fatalf("duplicates rows %+v: want one group of %q and %q", rows, first, second)
	}
	firstID, secondID := w.id("casa", first), w.id("casa", second)
	w.decide("casa", first, "discard")
	w.decide("casa", second, "discard")
	w.decide("casa", "outro.doc", "discard") // on no duplicates row: out of the plan

	p := w.plan("plan-cleanup", `{"source_id":"casa","list":"duplicates"}`)
	a, q := p.Action.ID, ".precious-quarantine/"+p.Action.ID
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+q,
		"mkdir planned -> "+q+"/1",
		"rename planned "+first+" -> "+q+"/1/curriculo.doc",
		"record planned -> "+q+"/1.json",
		"rename refused last_copy "+second,
	)
	dupsGround(t, p.Action)
	dupsEntries(t, p.Action, map[string]int64{"planned": 1, "refused": 1})
	dupsWant(t, "summary", *p.Summary, summaryJSON{WithCopyBytes: 24_576})

	done := w.run(a)
	dupsWant(t, "state", done.State, "done")
	dupsEntries(t, done, map[string]int64{"done": 1, "refused": 1})
	if w.exists("casa", first) || !w.exists("casa", q+"/1/curriculo.doc") || !w.exists("casa", second) ||
		!w.exists("casa", "outro.doc") {
		t.Fatal("disk: want only Documentos/curriculo.doc quarantined")
	}
	if path, state := w.pathOf(firstID); path != q+"/1/curriculo.doc" || state != "present" {
		t.Fatalf("index: the planned copy at %q %s", path, state)
	}
	if got := w.dupsOutside("casa", secondID); !slices.Equal(got, []string{second}) {
		t.Fatalf("copies outside the quarantine %q, want only %q", got, second)
	}
	if qs := w.quarantined("casa").Items; len(qs) != 1 || qs[0].Entry.ID != firstID || qs[0].Original == nil ||
		qs[0].Original.Path != first {
		t.Fatalf("quarantine %+v: want %s from %q", qs, firstID, first)
	}
}

// TestR4_4BothSidesOfARelation: the corpus's two Winamp installations are
// a "same" relation row of the duplicates list. With both folders
// discarded, the plan takes the earlier by path and refuses the later
// both_sides; running it quarantines only the earlier, all of it.
func TestR4_4BothSidesOfARelation(t *testing.T) {
	w := newWorld(t)
	root, _ := corpus.BuildSynth(w.sfs, "/casa", corpus.Corpus())
	w.add("casa", "/casa", root, "ext4")

	const (
		earlier = "Backup_PC_2004/C/Arquivos de programas/Winamp"
		later   = "HD antigo/backup pc velho/Arquivos de programas/Winamp"
	)
	var found []string
	for _, r := range w.dupsRows("casa") {
		if r.Relation == nil || r.Entry == nil {
			continue
		}
		sides := []string{r.Entry.Path, r.Relation.Other.Path}
		slices.Sort(sides)
		if sides[0] == earlier && sides[1] == later {
			found = append(found, r.Relation.Kind)
		}
	}
	if !slices.Equal(found, []string{"same"}) {
		t.Fatalf("relation rows of the two Winamp folders: %q, want one same row", found)
	}
	earlierID, laterID := w.id("casa", earlier), w.id("casa", later)
	w.decide("casa", later, "discard")
	w.decide("casa", earlier, "discard")

	p := w.plan("plan-cleanup", `{"source_id":"casa","list":"duplicates"}`)
	a, q := p.Action.ID, ".precious-quarantine/"+p.Action.ID
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+q,
		"mkdir planned -> "+q+"/1",
		"rename planned "+earlier+" -> "+q+"/1/Winamp",
		"record planned -> "+q+"/1.json",
		"rename refused both_sides "+later,
	)
	for _, it := range p.Items {
		if it.Op == "rename" && it.State == "planned" && (it.Bytes != 344_238 || it.Files != 8) {
			t.Fatalf("planned rename %s: %d bytes %d files, want 344238 and 8", it.summary(), it.Bytes, it.Files)
		}
	}
	dupsGround(t, p.Action)
	dupsEntries(t, p.Action, map[string]int64{"planned": 1, "refused": 1})
	// Every file of the planned folder has its copy in the refused one.
	dupsWant(t, "summary", *p.Summary, summaryJSON{WithCopyBytes: 344_238})

	done := w.run(a)
	dupsWant(t, "state", done.State, "done")
	dupsEntries(t, done, map[string]int64{"done": 1, "refused": 1})
	if w.exists("casa", earlier) || !w.exists("casa", q+"/1/Winamp") || !w.exists("casa", later) {
		t.Fatal("disk: want only the earlier Winamp quarantined")
	}
	if path, state := w.pathOf(earlierID); path != q+"/1/Winamp" || state != "present" {
		t.Fatalf("index: the earlier side at %q %s", path, state)
	}
	if path, state := w.pathOf(laterID); path != later || state != "present" {
		t.Fatalf("index: the later side at %q %s", path, state)
	}
	// The folder moved whole: its 8 files are below the quarantine path.
	if n := w.count(`SELECT count(*) FROM entries WHERE source_id = 'casa' AND kind = 'file' AND state = 'present'
		AND substr(path, 1, ?) = ?`, len(q+"/1/Winamp/"), []byte(q+"/1/Winamp/")); n != 8 {
		t.Fatalf("index: %d files below the quarantined Winamp, want 8", n)
	}
	if qs := w.quarantined("casa").Items; len(qs) != 1 || qs[0].Entry.ID != earlierID || qs[0].Files != 8 ||
		qs[0].Bytes != 344_238 {
		t.Fatalf("quarantine %+v: want the earlier Winamp, 8 files, 344238 bytes", qs)
	}
}

// TestR4_4HardLinksAreOneCopy: a file a.doc, its hard link a-link.doc, and
// an identical b.doc form one content group of two physical copies (the
// hard-link set and b.doc). Hard links count as one copy (D5): a name of
// the set that stays outside the plan keeps the data reachable. The plans
// are D5's:
//   - all three discarded: both names of the set are planned, which leaves
//     b.doc, and b.doc is refused last_copy;
//   - a.doc and b.doc discarded: both are planned, since a-link.doc stays
//     and holds the set's data outside the plan.
//
// Running them shows the safe outcome the executor gives today (design
// addendum S14, open for the executor): a rename advances the shared
// inode's change time, as Linux does, so the later item tied to that inode
// ends changed and stays where it was: the other name's draft identity no
// longer matches (identity_changed), or the staying name's index row no
// longer matches its lstat, so no copy is verified (no_verified_copy).
// Nothing is lost either way.
func TestR4_4HardLinksAreOneCopy(t *testing.T) {
	at := time.Date(2006, 3, 10, 20, 0, 0, 0, time.UTC)
	build := func(t *testing.T) *world {
		w := newWorld(t)
		w.disk("casa", "/casa", func(root *synthfs.Node) {
			a := root.File("a.doc", 26_112, at).Seed(51)
			root.HardLink("a-link.doc", a)
			root.File("b.doc", 26_112, at).Seed(51)
		})
		rows := w.dupsRows("casa")
		_, copies := w.dupsGroup("casa", "b.doc")
		if len(rows) != 1 || !slices.Equal(copies, []string{"a-link.doc", "a.doc", "b.doc"}) {
			t.Fatalf("duplicates rows %+v: want one group of the link set and b.doc", rows)
		}
		links := 0
		for _, c := range rows[0].Copies {
			if c.HardLink {
				links++
				if c.Path == "b.doc" {
					t.Fatalf("b.doc shown as a hard link: %+v", rows[0])
				}
			}
		}
		if links != 1 {
			t.Fatalf("copies %+v: want one name of the set shown as a hard link", rows[0].Copies)
		}
		return w
	}

	t.Run("all three discarded", func(t *testing.T) {
		w := build(t)
		for _, p := range []string{"a.doc", "a-link.doc", "b.doc"} {
			w.decide("casa", p, "discard")
		}
		p := w.plan("plan-cleanup", `{"source_id":"casa","list":"duplicates"}`)
		a, q := p.Action.ID, ".precious-quarantine/"+p.Action.ID
		wantItems(t, p.Items,
			"mkdir planned -> .precious-quarantine",
			"mkdir planned -> "+q,
			"mkdir planned -> "+q+"/1",
			"rename planned a-link.doc -> "+q+"/1/a-link.doc",
			"record planned -> "+q+"/1.json",
			"mkdir planned -> "+q+"/2",
			"rename planned a.doc -> "+q+"/2/a.doc",
			"record planned -> "+q+"/2.json",
			"rename refused last_copy b.doc",
		)
		dupsGround(t, p.Action)
		dupsWant(t, "summary", *p.Summary, summaryJSON{WithCopyBytes: 2 * 26_112})

		done := w.run(a)
		dupsWant(t, "state", done.State, "done")
		dupsEntries(t, done, map[string]int64{"done": 1, "changed": 1, "refused": 1})
		wantItems(t, w.items(a, ""),
			"mkdir done -> .precious-quarantine",
			"mkdir done -> "+q,
			"mkdir done -> "+q+"/1",
			"rename done a-link.doc -> "+q+"/1/a-link.doc",
			"record done -> "+q+"/1.json",
			"mkdir changed identity_changed -> "+q+"/2",
			"rename changed identity_changed a.doc -> "+q+"/2/a.doc",
			"record changed identity_changed -> "+q+"/2.json",
			"rename refused last_copy b.doc",
		)
		if w.exists("casa", "a-link.doc") || !w.exists("casa", q+"/1/a-link.doc") || !w.exists("casa", "a.doc") ||
			w.exists("casa", q+"/2") || !w.exists("casa", "b.doc") {
			t.Fatal("disk: want only a-link.doc quarantined, a.doc and b.doc in place")
		}
		if got := w.dupsOutside("casa", w.id("casa", "b.doc")); !slices.Equal(got, []string{"a.doc", "b.doc"}) {
			t.Fatalf("copies outside the quarantine %q, want a.doc and b.doc", got)
		}
	})

	t.Run("a link stays", func(t *testing.T) {
		w := build(t)
		w.decide("casa", "a.doc", "discard")
		w.decide("casa", "b.doc", "discard")
		p := w.plan("plan-cleanup", `{"source_id":"casa","list":"duplicates"}`)
		a, q := p.Action.ID, ".precious-quarantine/"+p.Action.ID
		wantItems(t, p.Items,
			"mkdir planned -> .precious-quarantine",
			"mkdir planned -> "+q,
			"mkdir planned -> "+q+"/1",
			"rename planned a.doc -> "+q+"/1/a.doc",
			"record planned -> "+q+"/1.json",
			"mkdir planned -> "+q+"/2",
			"rename planned b.doc -> "+q+"/2/b.doc",
			"record planned -> "+q+"/2.json",
		)
		dupsGround(t, p.Action)
		dupsWant(t, "summary", *p.Summary, summaryJSON{WithCopyBytes: 2 * 26_112})

		done := w.run(a)
		dupsWant(t, "state", done.State, "done")
		dupsEntries(t, done, map[string]int64{"done": 1, "changed": 1})
		wantItems(t, w.items(a, ""),
			"mkdir done -> .precious-quarantine",
			"mkdir done -> "+q,
			"mkdir done -> "+q+"/1",
			"rename done a.doc -> "+q+"/1/a.doc",
			"record done -> "+q+"/1.json",
			"mkdir changed no_verified_copy -> "+q+"/2",
			"rename changed no_verified_copy b.doc -> "+q+"/2/b.doc",
			"record changed no_verified_copy -> "+q+"/2.json",
		)
		if w.exists("casa", "a.doc") || !w.exists("casa", q+"/1/a.doc") || !w.exists("casa", "a-link.doc") ||
			w.exists("casa", q+"/2") || !w.exists("casa", "b.doc") {
			t.Fatal("disk: want only a.doc quarantined, a-link.doc and b.doc in place")
		}
		if got := w.dupsOutside("casa", w.id("casa", "b.doc")); !slices.Equal(got, []string{"a-link.doc", "b.doc"}) {
			t.Fatalf("copies outside the quarantine %q, want a-link.doc and b.doc", got)
		}
	})
}
