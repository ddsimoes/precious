package cleanup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

// The spec scenarios of r4 (cleanup, organizing, review-lists, and
// source-writes) through the API: commands, reads, the real executor, and
// the runner, on a synthfs source.

// scenAt is the modification time of every file the scenarios build.
var scenAt = time.Date(2011, 3, 4, 5, 6, 7, 0, time.UTC)

// scenEntry is the part of GET /api/entries/{id} the scenarios read.
type scenEntry struct {
	Entry struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"entry"`
	Intent struct {
		Decision    *string `json:"decision"`
		EffDecision string  `json:"eff_decision"`
		Tags        []struct {
			ID  int64 `json:"id"`
			Own bool  `json:"own"`
		} `json:"tags"`
	} `json:"intent"`
	InQuarantine *struct {
		PlanID        *string   `json:"plan_id"`
		QuarantinedAt *string   `json:"quarantined_at"`
		Original      *pathView `json:"original"`
	} `json:"in_quarantine"`
}

// scenDetail reads the detail of entry id.
func (w *world) scenDetail(id string) scenEntry {
	w.t.Helper()
	var e scenEntry
	w.get("/api/entries/"+id, &e)
	return e
}

// scenIndex is every row of src's index as "id path state", by ID: what a
// plan must leave alone.
func (w *world) scenIndex(src domain.SourceID) string {
	w.t.Helper()
	rows, err := w.st.Reader().Query(`SELECT id, path, state FROM entries WHERE source_id = ? ORDER BY id`, string(src))
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var (
			id    int64
			path  []byte
			state string
		)
		if err := rows.Scan(&id, &path, &state); err != nil {
			w.t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %s %s\n", id, path, state)
	}
	if err := rows.Err(); err != nil {
		w.t.Fatal(err)
	}
	return b.String()
}

// scenReadFile reads the regular file at rel below the synthfs root of src.
func (w *world) scenReadFile(src domain.SourceID, rel string) []byte {
	w.t.Helper()
	d, err := w.sfs.OpenRoot(w.roots[src])
	if err != nil {
		w.t.Fatal(err)
	}
	defer d.Close()
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		info, err := d.Lstat([]byte(p))
		if err != nil {
			w.t.Fatalf("%s: %v", rel, err)
		}
		next, err := d.OpenDir([]byte(p), info)
		if err != nil {
			w.t.Fatalf("%s: %v", rel, err)
		}
		defer next.Close()
		d = next
	}
	name := []byte(parts[len(parts)-1])
	info, err := d.Lstat(name)
	if err != nil {
		w.t.Fatalf("%s: %v", rel, err)
	}
	f, err := d.OpenFile(name, info)
	if err != nil {
		w.t.Fatalf("%s: %v", rel, err)
	}
	defer f.Close()
	buf := make([]byte, info.Size)
	if n, err := f.ReadAt(buf, 0); n != len(buf) || err != nil && err != io.EOF {
		w.t.Fatalf("read %s: %d bytes, %v", rel, n, err)
	}
	return buf
}

// scenCard reads a card of src from GET /api/opportunities.
func (w *world) scenCard(list string, src domain.SourceID) scenCardView {
	w.t.Helper()
	var body struct {
		Cards []scenCardView `json:"cards"`
	}
	w.get("/api/opportunities?source="+string(src), &body)
	for _, c := range body.Cards {
		if c.List == list {
			return c
		}
	}
	w.t.Fatalf("no %s card in %+v", list, body.Cards)
	return scenCardView{}
}

// scenCardView is a card of GET /api/opportunities.
type scenCardView struct {
	List         string `json:"list"`
	Bytes        int64  `json:"bytes"`
	Rows         int64  `json:"rows"`
	DecidedBytes int64  `json:"decided_bytes"`
	DecidedRows  int64  `json:"decided_rows"`
}

// scenCleanup drafts and runs a cleanup plan of src's discards, which must
// end done with every entry done, and returns the action's ID.
func (w *world) scenCleanup(src domain.SourceID) string {
	w.t.Helper()
	p := w.plan("plan-cleanup", fmt.Sprintf(`{"source_id":%q}`, src))
	a := w.run(p.Action.ID)
	if a.State != "done" || scenCounts(a.Entries) != fmt.Sprintf("done=%d", p.Action.Entries["planned"]) {
		w.t.Fatalf("cleanup %+v, planned %v", a, scenCounts(p.Action.Entries))
	}
	return p.Action.ID
}

// scenCounts is the nonzero counts of m, as "state=n" by state.
func scenCounts(m map[string]int64) string {
	var out []string
	for k, v := range m {
		if v != 0 {
			out = append(out, fmt.Sprintf("%s=%d", k, v))
		}
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

// Spec cleanup, "Drafting from the discards of a source": a discarded
// folder, a discarded file inside it, and a discarded file elsewhere make
// two items, the folder (the nested discard folded in) and the other file,
// with their bytes and files and the summary of their copies; drafting
// moves nothing, on disk or in the index, and the plan expires 24 hours
// after it was made.
func TestScenarioDraftingFromTheDiscardsOfASource(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		v := root.Dir("DCIM")
		v.File("a.txt", 100, scenAt).Seed(1)
		v.File("foto.jpg", 200, scenAt).Seed(2)
		v.File("quebrado.dat", 700, scenAt).Seed(3).Unreadable()
		root.File("solto.bin", 500, scenAt).Seed(4)
		root.Dir("Fotos").File("foto-copia.jpg", 200, scenAt).Seed(2)
		root.Dir("Outros").File("par.dat", 700, scenAt).Seed(9)
	})
	w.decide("casa", "DCIM", "discard")
	w.decide("casa", "DCIM/a.txt", "discard")
	w.decide("casa", "solto.bin", "discard")
	before := w.scenIndex("casa")

	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	plan := ".precious-quarantine/" + p.Action.ID
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+plan,
		"mkdir planned -> "+plan+"/1",
		"rename planned DCIM -> "+plan+"/1/DCIM",
		"record planned -> "+plan+"/1.json",
		"mkdir planned -> "+plan+"/2",
		"rename planned solto.bin -> "+plan+"/2/solto.bin",
		"record planned -> "+plan+"/2.json")
	for _, it := range p.Items {
		var wantBytes, wantFiles int64
		switch {
		case it.Op != "rename":
		case it.From.Path == "DCIM":
			wantBytes, wantFiles = 1000, 3
		case it.From.Path == "solto.bin":
			wantBytes, wantFiles = 500, 1
		}
		if it.Bytes != wantBytes || it.Files != wantFiles || it.KeptCount != nil {
			t.Errorf("%s: %d bytes, %d files, kept %v; want %d, %d, none", it.summary(), it.Bytes, it.Files,
				it.KeptCount, wantBytes, wantFiles)
		}
		if it.Op == "rename" && (it.Entry == nil || it.Entry.ID != w.id("casa", it.From.Path)) {
			t.Errorf("%s: entry %+v", it.summary(), it.Entry)
		}
	}
	a := p.Action
	if a.Kind != "cleanup" || a.State != "planned" || a.Bytes != 1500 || a.Files != 4 ||
		a.Ground == nil || *a.Ground != "discard" || a.List != nil || a.CheckID != nil ||
		scenCounts(a.Entries) != "planned=2" || scenCounts(a.Counts) != "planned=8" ||
		a.Undo.Possible || a.Undo.Reason == nil || *a.Undo.Reason != "not_undoable_kind" {
		t.Errorf("action %+v", a)
	}
	// DCIM/foto.jpg has a copy outside the plan, DCIM/quebrado.dat could not
	// be read for its size twin, and DCIM/a.txt and solto.bin have no file of
	// their size; the camera folder DCIM is personal, solto.bin is not.
	if *p.Summary != (summaryJSON{WithCopyBytes: 200, NoCopyBytes: 600, UncheckedBytes: 700, PersonalItems: 1}) {
		t.Errorf("summary %+v", *p.Summary)
	}

	var times struct {
		CreatedAt time.Time  `json:"created_at"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	w.get("/api/history/"+p.Action.ID, &times)
	if times.ExpiresAt == nil || times.ExpiresAt.Sub(times.CreatedAt) != 24*time.Hour {
		t.Errorf("created %v, expires %v; want 24 h later", times.CreatedAt, times.ExpiresAt)
	}

	if after := w.scenIndex("casa"); after != before {
		t.Errorf("drafting changed the index:\n%s\nwas:\n%s", after, before)
	}
	for _, rel := range []string{"DCIM/a.txt", "DCIM/foto.jpg", "DCIM/quebrado.dat", "solto.bin"} {
		if !w.exists("casa", rel) {
			t.Errorf("%s left its place", rel)
		}
	}
	if w.exists("casa", ".precious-quarantine") {
		t.Error("drafting made the quarantine")
	}
}

// Spec cleanup, "A keep set after drafting": a keep set below a planned
// folder after drafting ends that item blocked holds_kept, and a keep set on
// a planned folder itself ends it changed decision_changed, each with its
// mkdir and record and nothing moved; the plan's other item is quarantined.
func TestScenarioAKeepSetAfterDrafting(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		lixo := root.Dir("Lixo")
		lixo.File("l.txt", 110, scenAt).Seed(1)
		v := root.Dir("Velho")
		v.File("x.txt", 120, scenAt).Seed(2)
		v.Dir("sub").File("y.txt", 130, scenAt).Seed(3)
		root.File("solto.txt", 140, scenAt).Seed(4)
	})
	for _, p := range []string{"Lixo", "Velho", "solto.txt"} {
		w.decide("casa", p, "discard")
	}
	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	plan := ".precious-quarantine/" + p.Action.ID
	if p.Action.Entries["planned"] != 3 {
		t.Fatalf("plan %+v", p.Action)
	}
	w.decide("casa", "Velho/sub/y.txt", "keep")
	w.decide("casa", "Lixo", "keep")

	a := w.run(p.Action.ID)
	wantItems(t, w.items(p.Action.ID, ""),
		"mkdir done -> .precious-quarantine",
		"mkdir done -> "+plan,
		"mkdir changed decision_changed -> "+plan+"/1",
		"rename changed decision_changed Lixo -> "+plan+"/1/Lixo",
		"record changed decision_changed -> "+plan+"/1.json",
		"mkdir blocked holds_kept -> "+plan+"/2",
		"rename blocked holds_kept Velho -> "+plan+"/2/Velho",
		"record blocked holds_kept -> "+plan+"/2.json",
		"mkdir done -> "+plan+"/3",
		"rename done solto.txt -> "+plan+"/3/solto.txt",
		"record done -> "+plan+"/3.json")
	if a.State != "done" || scenCounts(a.Entries) != "blocked=1 changed=1 done=1" ||
		scenCounts(a.Counts) != "blocked=3 changed=3 done=5" || a.Bytes != 140 || a.Files != 1 {
		t.Errorf("action %+v", a)
	}
	for _, rel := range []string{"Lixo/l.txt", "Velho/x.txt", "Velho/sub/y.txt", plan + "/3/solto.txt", plan + "/3.json"} {
		if !w.exists("casa", rel) {
			t.Errorf("%s is not on disk", rel)
		}
	}
	for _, rel := range []string{plan + "/1", plan + "/2", plan + "/1.json", plan + "/2.json", "solto.txt"} {
		if w.exists("casa", rel) {
			t.Errorf("%s is on disk", rel)
		}
	}
	for path, want := range map[string]string{"Lixo": "Lixo", "Velho": "Velho", "Velho/sub/y.txt": "Velho/sub/y.txt"} {
		if got, state := w.pathOf(w.id("casa", path)); got != want || state != "present" {
			t.Errorf("%s indexed at %q %s", path, got, state)
		}
	}
	if got, _ := w.pathOf(w.id("casa", plan+"/3/solto.txt")); got != plan+"/3/solto.txt" {
		t.Errorf("solto.txt at %q", got)
	}
}

// Spec cleanup, "A quarantined folder keeps its intent": a discarded folder
// holding a tagged file is quarantined under .precious-quarantine/<plan>/1
// with its origin record 1.json beside it; its entries keep their IDs, own
// decisions, and tags; Home counts its bytes in quarantine, and its detail
// tells where it came from.
func TestScenarioAQuarantinedFolderKeepsItsIntent(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		d := root.Dir("Docs")
		d.File("cv.doc", 300, scenAt).Seed(1)
		d.Dir("sub").File("b.txt", 400, scenAt).Seed(2)
		root.File("fica.txt", 50, scenAt).Seed(3)
	})
	w.decide("casa", "Docs", "discard")
	w.decide("casa", "Docs/sub/b.txt", "discard")
	var tag struct {
		Tag struct {
			ID int64 `json:"id"`
		} `json:"tag"`
	}
	decode(t, w.ok(http.StatusCreated, "create-tag", `{"name":"curriculo"}`), &tag)
	w.ok(http.StatusOK, "set-tags", fmt.Sprintf(`{"entry_ids":%s,"add":[%d]}`, ids(w.id("casa", "Docs/cv.doc")), tag.Tag.ID))
	idOf := map[string]string{}
	for _, p := range []string{"Docs", "Docs/cv.doc", "Docs/sub", "Docs/sub/b.txt"} {
		idOf[p] = w.id("casa", p)
	}
	var home struct {
		Decisions map[string]amount `json:"decisions"`
	}
	w.get("/api/home?source=casa", &home)
	if home.Decisions["quarantine"] != (amount{}) || home.Decisions["discard"] != (amount{Files: 2, Bytes: 700}) {
		t.Fatalf("home before %+v", home.Decisions)
	}

	plan := w.scenCleanup("casa")
	dir := ".precious-quarantine/" + plan
	for p, id := range idOf {
		if got, state := w.pathOf(id); got != dir+"/1/"+p || state != "present" {
			t.Errorf("%s (%s) indexed at %q %s", p, id, got, state)
		}
		if !w.exists("casa", dir+"/1/"+p) {
			t.Errorf("%s is not on disk in quarantine", p)
		}
	}
	if w.exists("casa", "Docs") {
		t.Error("Docs is still at its place")
	}

	var rec struct {
		Version  int    `json:"version"`
		SourceID string `json:"source_id"`
		EntryID  string `json:"entry_id"`
		PlanID   string `json:"plan_id"`
		Original struct {
			Path    string `json:"path"`
			PathB64 []byte `json:"path_b64"`
		} `json:"original"`
		QuarantinedAt string `json:"quarantined_at"`
	}
	raw := w.scenReadFile("casa", dir+"/1.json")
	if !bytes.HasSuffix(raw, []byte("}\n")) || bytes.Count(raw, []byte("\n")) != 1 {
		t.Errorf("record %q is not one line of JSON", raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		t.Fatalf("record %s: %v", raw, err)
	}
	if rec.Version != 1 || rec.SourceID != "casa" || rec.EntryID != idOf["Docs"] || rec.PlanID != plan ||
		rec.Original.Path != "Docs" || string(rec.Original.PathB64) != "Docs" {
		t.Errorf("record %s", raw)
	}
	at, err := time.Parse(time.RFC3339Nano, rec.QuarantinedAt)
	if err != nil || at.Location() != time.UTC {
		t.Errorf("record quarantined_at %q: %v", rec.QuarantinedAt, err)
	}

	cases := []struct {
		path     string
		decision *string
		tags     []int64
	}{
		{"Docs", scenPtr("discard"), nil},
		{"Docs/cv.doc", nil, []int64{tag.Tag.ID}},
		{"Docs/sub", nil, nil},
		{"Docs/sub/b.txt", scenPtr("discard"), nil},
	}
	for _, c := range cases {
		e := w.scenDetail(idOf[c.path])
		if (e.Intent.Decision == nil) != (c.decision == nil) || c.decision != nil && *e.Intent.Decision != *c.decision ||
			e.Intent.EffDecision != "discard" {
			t.Errorf("%s: decision %v, effective %s", c.path, e.Intent.Decision, e.Intent.EffDecision)
		}
		var own []int64
		for _, tg := range e.Intent.Tags {
			if tg.Own {
				own = append(own, tg.ID)
			}
		}
		if fmt.Sprint(own) != fmt.Sprint(c.tags) {
			t.Errorf("%s: own tags %v, want %v", c.path, own, c.tags)
		}
		q := e.InQuarantine
		if q == nil || q.PlanID == nil || *q.PlanID != plan || q.Original == nil || q.Original.Path != "Docs" ||
			q.QuarantinedAt == nil {
			t.Errorf("%s: in_quarantine %+v", c.path, q)
		}
	}
	if e := w.scenDetail(w.id("casa", "fica.txt")); e.InQuarantine != nil {
		t.Errorf("fica.txt in_quarantine %+v", e.InQuarantine)
	}

	w.get("/api/home?source=casa", &home)
	if home.Decisions["quarantine"] != (amount{Files: 2, Bytes: 700}) || home.Decisions["discard"] != (amount{}) {
		t.Errorf("home after %+v", home.Decisions)
	}
}

func scenPtr(s string) *string { return &s }

// Spec organizing, "A quarantined file cannot be moved in bulk or into
// quarantine" and "Moving a file out of quarantine": a bulk move refuses the
// quarantined photo in_quarantine and moves the other file; a move into the
// quarantine fails 400 invalid_request; decisions, tags, and check-now on
// the photo fail 409 in_quarantine; an individual move takes it out to a
// folder outside, with its ID, and the ready check that held it goes stale.
func TestScenarioQuarantinedFileMoves(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Fotos").File("praia.jpg", 2048, scenAt).Seed(1)
		root.Dir("Outros").File("x.txt", 30, scenAt).Seed(2)
		root.Dir("Destino")
	})
	w.decide("casa", "Fotos/praia.jpg", "discard")
	photo := w.id("casa", "Fotos/praia.jpg")
	plan := w.scenCleanup("casa")
	dir := ".precious-quarantine/" + plan
	check, c := w.checkPurge(photo)
	if c.State != "ready" {
		t.Fatalf("check %+v", c)
	}
	dest, other := w.id("casa", "Destino"), w.id("casa", "Outros/x.txt")

	bulk := w.plan("plan-move", fmt.Sprintf(`{"entry_ids":%s,"destination_id":%q}`, ids(photo, other), dest))
	wantItems(t, bulk.Items,
		"rename refused in_quarantine "+dir+"/1/praia.jpg -> Destino/praia.jpg",
		"rename planned Outros/x.txt -> Destino/x.txt")
	if a := w.run(bulk.Action.ID); a.State != "done" || scenCounts(a.Counts) != "done=1 refused=1" {
		t.Fatalf("bulk move %+v", a)
	}
	if got, _ := w.pathOf(photo); got != dir+"/1/praia.jpg" || !w.exists("casa", dir+"/1/praia.jpg") {
		t.Fatalf("the bulk move took the photo to %q", got)
	}
	if got, _ := w.pathOf(other); got != "Destino/x.txt" {
		t.Fatalf("x.txt at %q", got)
	}
	if c := w.check(check); c.State != "ready" {
		t.Fatalf("check %+v after a move that did not touch it", c)
	}

	for _, into := range []string{".precious-quarantine", dir, dir + "/1"} {
		w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-move",
			fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, other, w.id("casa", into)))
		w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-move",
			fmt.Sprintf(`{"entry_ids":%s,"destination_id":%q}`, ids(other), w.id("casa", into)))
	}
	var tag struct {
		Tag struct {
			ID int64 `json:"id"`
		} `json:"tag"`
	}
	decode(t, w.ok(http.StatusCreated, "create-tag", `{"name":"praia"}`), &tag)
	w.refuse(http.StatusConflict, domain.CodeInQuarantine, "set-decision",
		fmt.Sprintf(`{"entry_id":%q,"decision":"keep"}`, photo))
	w.refuse(http.StatusConflict, domain.CodeInQuarantine, "set-tags",
		fmt.Sprintf(`{"entry_ids":%s,"add":[%d]}`, ids(photo), tag.Tag.ID))
	w.refuse(http.StatusConflict, domain.CodeInQuarantine, "check-now", fmt.Sprintf(`{"entry_ids":%s}`, ids(photo)))
	if e := w.scenDetail(photo); e.Intent.Decision == nil || *e.Intent.Decision != "discard" || len(e.Intent.Tags) != 0 {
		t.Fatalf("the refusals changed the photo: %+v", e.Intent)
	}
	if n := w.count(`SELECT count(*) FROM actions`); n != 2 {
		t.Fatalf("%d actions, want the cleanup and the bulk move: the refusals planned nothing", n)
	}

	out := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, photo, dest))
	wantItems(t, out.Items, "rename planned "+dir+"/1/praia.jpg -> Destino/praia.jpg")
	if a := w.run(out.Action.ID); a.State != "done" || scenCounts(a.Counts) != "done=1" {
		t.Fatalf("move out %+v", a)
	}
	if got, state := w.pathOf(photo); got != "Destino/praia.jpg" || state != "present" {
		t.Fatalf("photo at %q %s", got, state)
	}
	if !w.exists("casa", "Destino/praia.jpg") || w.exists("casa", dir+"/1/praia.jpg") {
		t.Error("the photo did not move on disk")
	}
	if e := w.scenDetail(photo); e.InQuarantine != nil || e.Entry.ID != photo {
		t.Errorf("photo detail %+v", e)
	}
	if q := w.quarantined("casa"); len(q.Items) != 0 {
		t.Errorf("quarantine %+v", q.Items)
	}
	c = w.check(check)
	if c.State != "stale" || c.StaleReason == nil || *c.StaleReason != "index_changed" {
		t.Errorf("check %+v, want stale index_changed", c)
	}
}

// Spec cleanup (D13): a done cleanup is not undone; its restore is the
// reverse. plan-undo fails 409 action_not_undoable, and History offers no
// Undo, with reason not_undoable_kind.
func TestScenarioCleanupIsNotUndoable(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.File("solto.bin", 500, scenAt).Seed(1)
	})
	w.decide("casa", "solto.bin", "discard")
	plan := w.scenCleanup("casa")
	w.refuse(http.StatusConflict, domain.CodeActionNotUndoable, "plan-undo", fmt.Sprintf(`{"action_id":%q}`, plan))
	a := w.action(plan)
	if a.State != "done" || a.Undo.Possible || a.Undo.Reason == nil || *a.Undo.Reason != "not_undoable_kind" {
		t.Errorf("action %+v", a)
	}
	if n := w.count(`SELECT count(*) FROM actions`); n != 1 {
		t.Errorf("%d actions, want the cleanup only", n)
	}
}

// Spec cleanup (D1, D13): a .precious-quarantine of the owner's at a
// source's top makes plan-cleanup fail 409 quarantine_name_taken, with no
// plan made; and no entry is renamed, nor folder made, under that name at a
// source's top (400 invalid_request).
func TestScenarioQuarantineNameReserved(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir(".precious-quarantine").File("meu.txt", 10, scenAt).Seed(1)
		root.File("solto.bin", 500, scenAt).Seed(2)
	})
	w.disk("pen", "/pen", func(root *synthfs.Node) {
		root.Dir("Velho").File("a.txt", 20, scenAt).Seed(3)
	})
	w.decide("casa", "solto.bin", "discard")
	w.refuse(http.StatusConflict, domain.CodeQuarantineNameTaken, "plan-cleanup", `{"source_id":"casa"}`)
	if n := w.count(`SELECT count(*) FROM actions`); n != 0 {
		t.Errorf("%d actions after the refusal", n)
	}
	if !w.exists("casa", ".precious-quarantine/meu.txt") || !w.exists("casa", "solto.bin") {
		t.Error("the refusal changed the disk")
	}

	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rename",
		fmt.Sprintf(`{"entry_id":%q,"name":".precious-quarantine"}`, w.id("pen", "Velho")))
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-create-folder",
		fmt.Sprintf(`{"parent_id":%q,"name":".precious-quarantine"}`, w.id("pen", "")))
	if n := w.count(`SELECT count(*) FROM actions`); n != 0 {
		t.Errorf("%d actions after the refusals", n)
	}
	// Below the top, the name is the owner's to use.
	p := w.plan("plan-rename", fmt.Sprintf(`{"entry_id":%q,"name":".precious-quarantine"}`, w.id("pen", "Velho/a.txt")))
	wantItems(t, p.Items, "rename planned Velho/a.txt -> Velho/.precious-quarantine")
}

// scenJunk is a source with four system junk files, a photo, and a
// discarded file that no list holds; three of the junk files are discarded.
func scenJunk(t *testing.T) *world {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		f := root.Dir("Fotos")
		f.File("Thumbs.db", 4093, scenAt).Seed(1)
		f.File("desktop.ini", 282, scenAt).Seed(2)
		f.File("a.jpg", 5000, scenAt).Seed(3)
		root.Dir("Musica").File(".DS_Store", 6148, scenAt).Seed(4)
		root.Dir("Docs").File("Thumbs.db", 1024, scenAt).Seed(5)
		root.File("velho.txt", 77, scenAt).Seed(6)
	})
	var list struct {
		Items []struct {
			Bytes int64 `json:"bytes"`
			Entry struct {
				Path string `json:"path"`
			} `json:"entry"`
		} `json:"items"`
	}
	w.idle()
	w.get("/api/opportunities/system_junk?source=casa&limit=100", &list)
	var rows []string
	for _, it := range list.Items {
		rows = append(rows, fmt.Sprintf("%s %d", it.Entry.Path, it.Bytes))
	}
	if got := strings.Join(rows, ", "); got != "Musica/.DS_Store 6148, Fotos/Thumbs.db 4093, Docs/Thumbs.db 1024, Fotos/desktop.ini 282" {
		t.Fatalf("system junk rows: %s", got)
	}
	if c := w.scenCard("system_junk", "casa"); c != (scenCardView{List: "system_junk", Bytes: 11547, Rows: 4}) {
		t.Fatalf("card before %+v", c)
	}
	for _, p := range []string{"Fotos/Thumbs.db", "Fotos/desktop.ini", "Musica/.DS_Store", "velho.txt"} {
		w.decide("casa", p, "discard")
	}
	if c := w.scenCard("system_junk", "casa"); c != (scenCardView{List: "system_junk", Bytes: 1024, Rows: 1,
		DecidedBytes: 10523, DecidedRows: 3}) {
		t.Fatalf("card after the discards %+v", c)
	}
	return w
}

// Spec review-lists, "Drafting from the system junk list": a plan drafted
// from the list holds the three discarded rows only (not the discarded file
// no list holds, nor the open row), and once it ran and the relations were
// computed again, the card counts none of them.
func TestScenarioDraftingFromTheSystemJunkList(t *testing.T) {
	w := scenJunk(t)
	p := w.plan("plan-cleanup", `{"source_id":"casa","list":"system_junk"}`)
	dir := ".precious-quarantine/" + p.Action.ID
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+dir,
		"mkdir planned -> "+dir+"/1",
		"rename planned Fotos/Thumbs.db -> "+dir+"/1/Thumbs.db",
		"record planned -> "+dir+"/1.json",
		"mkdir planned -> "+dir+"/2",
		"rename planned Fotos/desktop.ini -> "+dir+"/2/desktop.ini",
		"record planned -> "+dir+"/2.json",
		"mkdir planned -> "+dir+"/3",
		"rename planned Musica/.DS_Store -> "+dir+"/3/.DS_Store",
		"record planned -> "+dir+"/3.json")
	if a := p.Action; a.List == nil || *a.List != "system_junk" || a.Ground == nil || *a.Ground != "discard" ||
		a.Bytes != 10523 || a.Files != 3 {
		t.Errorf("action %+v", a)
	}
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-cleanup", `{"source_id":"casa","list":"nada"}`)

	if a := w.run(p.Action.ID); a.State != "done" || scenCounts(a.Entries) != "done=3" || scenCounts(a.Counts) != "done=11" {
		t.Fatalf("run %+v", a)
	}
	w.idle()
	if c := w.scenCard("system_junk", "casa"); c != (scenCardView{List: "system_junk", Bytes: 1024, Rows: 1}) {
		t.Errorf("card after the run %+v", c)
	}
	if !w.has("casa", "velho.txt") || !w.has("casa", "Docs/Thumbs.db") {
		t.Error("the plan took what the list did not hold")
	}
}

// Spec cleanup, "From a review list to quarantine": the approved plan of the
// system junk list moves its items to quarantine, which the Cleanup screen
// lists with their original paths and plan, restorable.
func TestScenarioFromAReviewListToQuarantine(t *testing.T) {
	w := scenJunk(t)
	p := w.plan("plan-cleanup", `{"source_id":"casa","list":"system_junk"}`)
	if a := w.run(p.Action.ID); a.State != "done" || scenCounts(a.Entries) != "done=3" {
		t.Fatalf("run %+v", a)
	}
	dir := ".precious-quarantine/" + p.Action.ID
	q := w.quarantined("casa")
	var got []string
	for _, it := range q.Items {
		line := fmt.Sprintf("%s %s %d/%d", it.Entry.Name, w.scenPath(it.Entry.ID), it.Bytes, it.Files)
		if it.Original != nil {
			line += " from " + it.Original.Path
		}
		if it.PlanID == nil || *it.PlanID != p.Action.ID || it.QuarantinedAt == nil || it.Check != nil {
			t.Errorf("%s: plan %v, at %v, check %+v", it.Entry.Name, it.PlanID, it.QuarantinedAt, it.Check)
		}
		got = append(got, line)
	}
	want := []string{
		".DS_Store " + dir + "/3/.DS_Store 6148/1 from Musica/.DS_Store",
		"desktop.ini " + dir + "/2/desktop.ini 282/1 from Fotos/desktop.ini",
		"Thumbs.db " + dir + "/1/Thumbs.db 4093/1 from Fotos/Thumbs.db",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("quarantine:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, rel := range []string{"Fotos/Thumbs.db", "Fotos/desktop.ini", "Musica/.DS_Store"} {
		if w.exists("casa", rel) {
			t.Errorf("%s is still in place", rel)
		}
	}

	r := w.plan("plan-restore", fmt.Sprintf(`{"plan_id":%q}`, p.Action.ID))
	var renames []string
	for _, it := range r.Items {
		if it.Op == "rename" {
			renames = append(renames, it.summary())
		}
	}
	if got := strings.Join(renames, "\n"); got != strings.Join([]string{
		"rename planned " + dir + "/1/Thumbs.db -> Fotos/Thumbs.db",
		"rename planned " + dir + "/2/desktop.ini -> Fotos/desktop.ini",
		"rename planned " + dir + "/3/.DS_Store -> Musica/.DS_Store",
	}, "\n") {
		t.Errorf("restore renames:\n%s", got)
	}
}

// scenPath is the index path of entry id.
func (w *world) scenPath(id string) string {
	w.t.Helper()
	p, _ := w.pathOf(id)
	return p
}

// Spec cleanup (D14): remove-source fails 409 quarantine_not_empty while
// the source's quarantine holds an entry, and leaves the source as it was;
// a source without one is removed.
func TestScenarioRemoveSourceWithAQuarantine(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.File("solto.bin", 500, scenAt).Seed(1)
	})
	w.disk("pen", "/pen", func(root *synthfs.Node) {
		root.File("a.txt", 20, scenAt).Seed(2)
	})
	w.decide("casa", "solto.bin", "discard")
	w.scenCleanup("casa")
	before := w.scenIndex("casa")
	w.refuse(http.StatusConflict, domain.CodeQuarantineNotEmpty, "remove-source", `{"source_id":"casa"}`)
	if after := w.scenIndex("casa"); after != before {
		t.Errorf("the refusal changed the index:\n%s\nwas:\n%s", after, before)
	}
	if n := w.count(`SELECT count(*) FROM sources WHERE id = 'casa'`); n != 1 {
		t.Errorf("source casa: %d rows", n)
	}
	w.ok(http.StatusOK, "remove-source", `{"source_id":"pen"}`)
	if n := w.count(`SELECT count(*) FROM sources WHERE id = 'pen'`); n != 0 {
		t.Errorf("source pen: %d rows after its removal", n)
	}
}

// G8: remove-source counts only quarantined top items (S1). A plan folder,
// or a <seq> folder and its record, that normal runs leave without an item
// does not hold the source forever.
func TestScenarioRemoveSourceWithAnItemlessQuarantine(t *testing.T) {
	t.Run("the only item changed before the run", func(t *testing.T) {
		w := newWorld(t)
		var solto *synthfs.Node
		w.disk("casa", "/casa", func(root *synthfs.Node) {
			solto = root.File("solto.bin", 500, scenAt).Seed(1)
		})
		w.decide("casa", "solto.bin", "discard")
		p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
		solto.ModTime(scenAt.Add(time.Hour))
		a := w.run(p.Action.ID)
		if a.Entries["changed"] != 1 || a.Entries["done"] != 0 {
			t.Fatalf("cleanup %+v", a)
		}
		if !w.has("casa", ".precious-quarantine/1") || w.has("casa", ".precious-quarantine/1/1") ||
			!w.has("casa", "solto.bin") {
			t.Fatal("want an empty plan folder, and solto.bin where it was")
		}
		w.ok(http.StatusOK, "remove-source", `{"source_id":"casa"}`)
		if n := w.count(`SELECT count(*) FROM sources WHERE id = 'casa'`); n != 0 {
			t.Errorf("source casa: %d rows after its removal", n)
		}
	})

	t.Run("the only item moved out", func(t *testing.T) {
		w := newWorld(t)
		w.disk("casa", "/casa", func(root *synthfs.Node) {
			root.Dir("Fotos").File("a.txt", 20, scenAt).Seed(2)
			root.File("solto.bin", 500, scenAt).Seed(1)
		})
		w.decide("casa", "solto.bin", "discard")
		w.scenCleanup("casa")
		solto := w.id("casa", ".precious-quarantine/1/1/solto.bin")
		m := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, solto, w.id("casa", "Fotos")))
		if a := w.run(m.Action.ID); a.State != "done" {
			t.Fatalf("move %+v", a)
		}
		if !w.has("casa", "Fotos/solto.bin") {
			t.Fatal("solto.bin is not in Fotos")
		}
		if n := w.count(`SELECT count(*) FROM entries WHERE source_id = 'casa' AND path > ? AND path < ?
			AND state <> 'missing'`, []byte(".precious-quarantine/1/"), []byte(".precious-quarantine/10")); n == 0 {
			t.Fatal("want what the item leaves in its plan folder")
		}
		w.ok(http.StatusOK, "remove-source", `{"source_id":"casa"}`)
		if n := w.count(`SELECT count(*) FROM sources WHERE id = 'casa'`); n != 0 {
			t.Errorf("source casa: %d rows after its removal", n)
		}
	})
}
