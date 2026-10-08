package organize

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
)

// decide sets an entry's own decision through set-decision.
func (w *world) decide(id, decision string) {
	w.t.Helper()
	w.ok(http.StatusOK, "set-decision", fmt.Sprintf(`{"entry_id":%q,"decision":%q}`, id, decision))
}

// tag creates a tag and puts it on the entries; it returns the tag's ID.
func (w *world) tag(name string, entries ...string) int64 {
	w.t.Helper()
	var res struct {
		Tag struct {
			ID int64 `json:"id"`
		} `json:"tag"`
	}
	decode(w.t, w.ok(http.StatusCreated, "create-tag", fmt.Sprintf(`{"name":%q}`, name)), &res)
	w.ok(http.StatusOK, "set-tags", fmt.Sprintf(`{"entry_ids":%s,"add":[%d]}`, ids(entries...), res.Tag.ID))
	return res.Tag.ID
}

// moveBody is a plan-move body with entry_ids.
func moveBody(dest string, entries ...string) string {
	return fmt.Sprintf(`{"entry_ids":%s,"destination_id":%q}`, ids(entries...), dest)
}

// outcomes maps each item's from path (to path when it has none) to
// "state reason".
func outcomes(items []itemJSON) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		key := ""
		switch {
		case it.From != nil:
			key = it.From.Path
		case it.To != nil:
			key = it.To.Path
		}
		v := it.State
		if it.Reason != nil {
			v += " " + *it.Reason
		}
		out[key] = v
	}
	return out
}

func wantOutcomes(t *testing.T, items []itemJSON, want map[string]string) {
	t.Helper()
	got := outcomes(items)
	if len(got) != len(want) || len(items) != len(want) {
		t.Errorf("%d items %v, want %v", len(items), got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("item %q is %q, want %q", k, got[k], v)
		}
	}
}

// planDisk is the plan tests' source "disk", scanned, with a second source
// "outro". Destino/c.jpg went missing with an own keep and a tag, and
// Documentos/curriculo.doc went missing without either. pacote.zip has one
// member listed. The runner is not started: plans read the index only.
func planDisk(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	var dest, docs *synthfs.Node
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		fotos := root.Dir("Fotos")
		f04 := fotos.Dir("2004")
		f04.File("a.jpg", 100, mtime)
		f04.File("b.jpg", 200, mtime)
		f05 := fotos.Dir("2005")
		f05.File("c.jpg", 300, mtime)
		f05.Dir("Natal").File("d.jpg", 400, mtime)
		docs = root.Dir("Documentos")
		docs.File("b.jpg", 210, mtime)
		docs.File("curriculo.doc", 20, mtime)
		dest = root.Dir("Destino")
		dest.File("a.jpg", 110, mtime)
		dest.File("c.jpg", 310, mtime)
		root.Dir("Montado").MountPoint().File("x.txt", 1, mtime)
		mixed := root.Dir("ComMontagem")
		mixed.Dir("sub").MountPoint().File("y.txt", 1, mtime)
		mixed.File("z.txt", 3, mtime)
		root.Dir("Lixo")
		root.Dir("Guardar").File("k.txt", 5, mtime)
		root.File("pacote.zip", 50, mtime)
	})
	w.disk("outro", "/outro", posix, func(root *synthfs.Node) { root.File("z.txt", 7, mtime) })

	missing := w.id("disk", "Destino/c.jpg")
	w.decide(missing, "keep")
	w.tag("familia", missing)
	dest.Remove("c.jpg")
	docs.Remove("curriculo.doc")
	w.scan("disk")
	if _, state := w.pathOf(missing); state != "missing" {
		t.Fatalf("Destino/c.jpg is %s after the rescan", state)
	}
	zip := w.id("disk", "pacote.zip")
	w.exec(`INSERT INTO archives (entry_id, format, state, size, mtime_ns, members) VALUES (?, 'zip', 'complete', 50, 0, 1)`, zip)
	w.exec(`INSERT INTO archive_members (id, archive_id, name, path, kind, size, state)
		VALUES (7001, ?, 'm.txt', 'm.txt', 'file', 4, 'unique_size')`, zip)
	return w
}

// Every refusal reason and conflict of a move plan, in one bulk plan of
// entry_ids, plus into_itself; and the missing entry with the owner's
// intent keeps its decision and tag (spec "A missing kept file keeps its
// name").
func TestPlanMoveRefusalsAndConflicts(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	id := func(p string) string { return w.id("disk", p) }
	a, items := w.plan("plan-move", moveBody(id("Destino"),
		w.id("outro", "z.txt"), "m7001", id("Documentos/curriculo.doc"), id(""), id("Destino/a.jpg"), id("Montado"),
		id("ComMontagem"), id("Fotos/2004/a.jpg"), id("Fotos/2004/b.jpg"), id("Documentos/b.jpg"),
		id("Fotos/2005/c.jpg"), id("Fotos/2005/Natal/d.jpg")))
	wantOutcomes(t, items, map[string]string{
		"z.txt":                    "refused other_source",
		"pacote.zip/m.txt":         "refused inside_archive",
		"Documentos/curriculo.doc": "refused missing",
		"":                         "refused source_root",
		"Destino/a.jpg":            "refused already_there",
		"Montado":                  "refused other_filesystem",
		"ComMontagem":              "refused contains_mount",
		"Fotos/2004/a.jpg":         "conflict name_taken",
		"Documentos/b.jpg":         "planned",
		"Fotos/2004/b.jpg":         "conflict name_taken_in_plan",
		"Fotos/2005/c.jpg":         "conflict name_taken_by_missing",
		"Fotos/2005/Natal/d.jpg":   "planned",
	})
	// Items in path order, source by source.
	if items[0].From.Path != "" || items[len(items)-1].From.Path != "z.txt" {
		t.Errorf("items out of order: %v", summaries(items))
	}
	if !a.Bulk || a.Kind != "move" || a.State != "planned" || a.Counts["planned"] != 2 || a.Counts["refused"] != 7 ||
		a.Counts["conflict"] != 3 || a.Bytes != 210+400 || a.Files != 2 || a.Destination == nil ||
		a.Destination.Path != "Destino" || a.ExpiresAt == nil || !a.ExpiresAt.Equal(a.CreatedAt.Add(time.Hour)) {
		t.Errorf("action %+v", a)
	}
	for _, it := range items {
		if it.To == nil || it.Op != "rename" {
			t.Errorf("item %s has no destination path", itemSummary(it))
		}
	}

	// A folder into itself, or below itself.
	_, items = w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, id("Fotos"), id("Fotos/2005")))
	wantOutcomes(t, items, map[string]string{"Fotos": "refused into_itself"})
	_, items = w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, id("Fotos"), id("Fotos")))
	wantOutcomes(t, items, map[string]string{"Fotos": "refused into_itself"})

	// Planning changed nothing: the missing entry keeps its keep and tag.
	missing := id("Destino/c.jpg")
	if n := w.count(`SELECT count(*) FROM entries e JOIN entry_tags t ON t.entry_id = e.id
		WHERE e.id = ? AND e.decision = 'keep' AND e.state = 'missing'`, missing); n != 1 {
		t.Error("the missing kept file lost its decision or tag")
	}
	if p, _ := w.pathOf(id("Fotos/2005/Natal/d.jpg")); p != "Fotos/2005/Natal/d.jpg" {
		t.Errorf("a plan moved an entry in the index to %q", p)
	}
}

// A missing entry without the owner's intent gives way.
func TestPlanMoveIgnoresAMissingEntryWithoutIntent(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	_, items := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`,
		w.id("disk", "Fotos/2004/b.jpg"), w.id("disk", "Documentos")))
	wantOutcomes(t, items, map[string]string{"Fotos/2004/b.jpg": "conflict name_taken"})
	w.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES ('disk', ?, 'c.jpg', 'Documentos/c.jpg', 'file', 'missing', 0, 0, 0)`, w.id("disk", "Documentos"))
	_, items = w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`,
		w.id("disk", "Fotos/2005/c.jpg"), w.id("disk", "Documentos")))
	wantOutcomes(t, items, map[string]string{"Fotos/2005/c.jpg": "planned"})
}

// Names compare as the source's filesystem compares them: Foto.jpg and
// foto.jpg collide on a case-insensitive source, not on a case-sensitive
// one (spec "Collisions follow the filesystem"); a rename that changes only
// the letter case is refused there with 400 (D17), and a name taken but for
// the case is 409 name_taken.
func TestCaseInsensitiveCollisions(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	build := func(root *synthfs.Node) {
		root.File("Foto.jpg", 10, mtime)
		root.File("outra.jpg", 11, mtime)
		root.Dir("Album").File("foto.jpg", 12, mtime)
	}
	w.disk("fat", "/fat", fat, build)
	w.disk("ext", "/ext", posix, build)
	for src, want := range map[domain.SourceID]string{"fat": "conflict name_taken", "ext": "planned"} {
		_, items := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`,
			w.id(src, "Foto.jpg"), w.id(src, "Album")))
		wantOutcomes(t, items, map[string]string{"Foto.jpg": want})
		// Two items of one plan whose names differ only by case.
		_, items = w.plan("plan-move", moveBody(w.id(src, "Album"), w.id(src, "outra.jpg"), w.id(src, "Foto.jpg")))
		if src == "fat" {
			wantOutcomes(t, items, map[string]string{"Foto.jpg": "conflict name_taken", "outra.jpg": "planned"})
		}
	}
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rename",
		fmt.Sprintf(`{"entry_id":%q,"name":"FOTO.JPG"}`, w.id("fat", "Foto.jpg")))
	_, body := w.post("plan-rename", fmt.Sprintf(`{"entry_id":%q,"name":"FOTO.JPG"}`, w.id("fat", "Foto.jpg")))
	if !strings.Contains(body, "only the letter case differs") {
		t.Errorf("case-only rename message %s", body)
	}
	w.plan("plan-rename", fmt.Sprintf(`{"entry_id":%q,"name":"FOTO.JPG"}`, w.id("ext", "Foto.jpg")))
	w.refuse(http.StatusConflict, domain.CodeNameTaken, "plan-rename",
		fmt.Sprintf(`{"entry_id":%q,"name":"foto.JPG"}`, w.id("fat", "outra.jpg")))
}

// D14: a single move of a file kept through its folder into a discarded
// folder previews decision_after discard and counts one kept item lost; a
// bulk move refuses it (would_lose_keep) and moves the undecided file. An
// entry with its own keep keeps it anywhere.
func TestMoveKeepRules(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	id := func(p string) string { return w.id("disk", p) }
	w.decide(id("Guardar"), "keep")
	w.decide(id("Lixo"), "discard")
	w.decide(id("Fotos/2004/a.jpg"), "keep")

	a, items := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, id("Guardar/k.txt"), id("Lixo")))
	wantOutcomes(t, items, map[string]string{"Guardar/k.txt": "planned"})
	if a.Bulk || a.KeptLost != 1 || items[0].DecisionAfter == nil || *items[0].DecisionAfter != "discard" {
		t.Errorf("single move: bulk %v kept_lost %d decision_after %v", a.Bulk, a.KeptLost, items[0].DecisionAfter)
	}

	a, items = w.plan("plan-move", moveBody(id("Lixo"), id("Guardar/k.txt"), id("Fotos/2004/b.jpg"), id("Fotos/2004/a.jpg")))
	wantOutcomes(t, items, map[string]string{"Guardar/k.txt": "refused would_lose_keep", "Fotos/2004/b.jpg": "planned",
		"Fotos/2004/a.jpg": "planned"})
	if !a.Bulk || a.KeptLost != 0 {
		t.Errorf("bulk move: bulk %v kept_lost %d", a.Bulk, a.KeptLost)
	}
	for _, it := range items {
		switch it.From.Path {
		case "Fotos/2004/b.jpg":
			if it.DecisionAfter == nil || *it.DecisionAfter != "discard" {
				t.Errorf("the undecided file's decision_after %v", it.DecisionAfter)
			}
		case "Fotos/2004/a.jpg":
			if it.DecisionAfter != nil {
				t.Errorf("the file with its own keep reads decision_after %q", *it.DecisionAfter)
			}
		}
	}
}

// A target inside another planned folder folds into it; one inside a folder
// the plan refuses moves on its own.
func TestPlanMoveFoldsNestedTargets(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	id := func(p string) string { return w.id("disk", p) }
	_, items := w.plan("plan-move", moveBody(id("Documentos"), id("Fotos/2005/Natal/d.jpg"), id("Fotos/2005"),
		id("Fotos/2005/c.jpg"), id("Fotos/2005/Natal"), id("Fotos/2004/a.jpg"), id("ComMontagem"), id("ComMontagem/z.txt")))
	wantOutcomes(t, items, map[string]string{"Fotos/2005": "planned", "Fotos/2004/a.jpg": "planned",
		"ComMontagem": "refused contains_mount", "ComMontagem/z.txt": "planned"})
	for _, it := range items {
		if it.From.Path == "Fotos/2005" && (it.Bytes != 700 || it.Files != 2) {
			t.Errorf("Fotos/2005 counts %d bytes, %d files", it.Bytes, it.Files)
		}
	}
}

// The scenario "Rescue before discarding", with the rescue's refusals.
func TestRescueBeforeDiscarding(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		old := root.Dir("Velho")
		old.File("k1.txt", 10, mtime)
		old.File("lixo.txt", 11, mtime)
		old.Dir("Sub").File("k2.txt", 12, mtime)
		root.Dir("Documentos")
		root.Dir("Guardado").File("g.txt", 1, mtime)
	})
	id := func(p string) string { return w.id("disk", p) }
	w.decide(id("Velho"), "discard")
	w.decide(id("Velho/k1.txt"), "keep")
	w.decide(id("Velho/Sub"), "keep")
	w.decide(id("Velho/Sub/k2.txt"), "keep")
	w.decide(id("Guardado"), "keep")

	rescue := func(folder, dest string) string {
		return fmt.Sprintf(`{"folder_id":%q,"destination_id":%q}`, id(folder), id(dest))
	}
	w.refuse(http.StatusConflict, domain.CodeInvalidEntryState, "plan-rescue", rescue("Guardado", "Documentos"))
	w.refuse(http.StatusConflict, domain.CodeInvalidEntryState, "plan-rescue", rescue("Documentos", "Velho"))
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rescue", rescue("Velho", "Velho/Sub"))
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rescue", rescue("Velho/lixo.txt", "Documentos"))

	a, items := w.plan("plan-rescue", rescue("Velho", "Documentos"))
	wantItems(t, items, "rename planned Velho/Sub -> Documentos/Sub", "rename planned Velho/k1.txt -> Documentos/k1.txt")
	if a.Kind != "rescue" || !a.Bulk {
		t.Errorf("action %+v", a)
	}
	done := w.run(a.ID)
	if done.State != "done" || done.Counts["done"] != 2 {
		t.Fatalf("rescue ended %s with %v", done.State, done.Counts)
	}
	for p, want := range map[string]string{"Documentos/Sub": "keep", "Documentos/k1.txt": "keep",
		"Documentos/Sub/k2.txt": "keep", "Velho/lixo.txt": "discard"} {
		var own, eff sql.NullString
		if err := w.st.Reader().QueryRow(`SELECT decision, eff_decision FROM entries WHERE source_id = 'disk' AND path = ?`,
			[]byte(p)).Scan(&own, &eff); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if eff.String != want || (want == "keep" && own.String != "keep") {
			t.Errorf("%s reads %v/%v, want %s", p, own, eff, want)
		}
		if !w.exists("/disk", p) {
			t.Errorf("%s is not on the disk", p)
		}
	}
}

// More than 10,000 items is 400, and changes nothing; a plan's first page
// holds 200 items, and the rest follow by cursor.
func TestPlanCap(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.Dir("Grande")
		root.Dir("Destino")
	})
	big := w.id("disk", "Grande")
	w.exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO entries (source_id, parent_id, name, path, kind, state, size, total_bytes, total_files, first_seen,
			last_seen, scan_gen)
		SELECT 'disk', ?, CAST(printf('f%05d', i) AS BLOB), CAST('Grande/' || printf('f%05d', i) AS BLOB), 'file',
			'present', 1, 1, 1, 0, 0, 0 FROM n`, maxItems, big)
	selection := func(n int) string {
		id := fmt.Sprintf("sel-%d", n)
		exp := clock.Millis(w.clk.Now().Add(time.Hour))
		w.exec(`INSERT INTO selections (id, query, count, bytes, kept, created_at, expires_at) VALUES (?, '{}', ?, ?, 0, 0, ?)`,
			id, n, n, exp)
		w.exec(`INSERT INTO selection_entries (selection_id, entry_id) SELECT ?, id FROM entries
			WHERE parent_id = ? ORDER BY path LIMIT ?`, id, big, n)
		return id
	}
	dest := w.id("disk", "Destino")
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-move",
		fmt.Sprintf(`{"selection_id":%q,"destination_id":%q}`, selection(maxItems+1), dest))
	if n := w.count(`SELECT count(*) FROM actions`); n != 0 {
		t.Fatalf("a refused plan left %d actions", n)
	}
	const n = itemsDefaultLimit + 50
	var res planResponse
	decode(t, w.ok(http.StatusCreated, "plan-move", fmt.Sprintf(`{"selection_id":%q,"destination_id":%q}`,
		selection(n), dest)), &res)
	if res.Action.Counts["planned"] != n || len(res.Items) != itemsDefaultLimit || res.NextCursor == nil {
		t.Fatalf("plan of %d: counts %v, first page %d, next %v", n, res.Action.Counts, len(res.Items), res.NextCursor)
	}
	more := w.items(res.Action.ID, "cursor="+*res.NextCursor+"&limit=20")
	if len(more) != n-itemsDefaultLimit || more[0].Seq != itemsDefaultLimit+1 || more[len(more)-1].To.Path != "Destino/f00249" {
		t.Errorf("the rest of the items: %d from seq %d", len(more), more[0].Seq)
	}
}

// A plan runs within its hour; after it, run-action answers
// action_expired, the action reads expired, and a day later a new plan
// deletes it. An expired selection is selection_expired.
func TestPlanExpiry(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	id := func(p string) string { return w.id("disk", p) }
	a, _ := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, id("Fotos/2004/b.jpg"), id("Lixo")))
	w.clk.advance(actionTTL + time.Minute)
	w.refuse(http.StatusConflict, domain.CodeActionExpired, "run-action", fmt.Sprintf(`{"action_id":%q}`, a.ID))
	if got := w.action(a.ID); got.State != "expired" || got.Undo.Possible {
		t.Errorf("expired action reads %s, undo %+v", got.State, got.Undo)
	}
	if p, _ := w.pathOf(id("Fotos/2004/b.jpg")); p != "Fotos/2004/b.jpg" {
		t.Errorf("an expired plan moved the file to %q", p)
	}
	var list page[actionJSON]
	w.get("/api/history", &list)
	if len(list.Items) != 0 {
		t.Errorf("the history lists a plan that never ran: %+v", list.Items)
	}
	w.clk.advance(plannedRetention)
	w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, id("Fotos/2004/b.jpg"), id("Lixo")))
	if n := w.count(`SELECT count(*) FROM actions WHERE state <> 'planned' OR created_at < ?`,
		clock.Millis(w.clk.Now().Add(-time.Hour))); n != 0 {
		t.Errorf("%d plans expired for over a day are kept", n)
	}

	var sel struct {
		SelectionID string `json:"selection_id"`
	}
	decode(t, w.ok(http.StatusCreated, "create-selection", `{"query":{"source":"disk","name":"jpg"}}`), &sel)
	w.clk.advance(2 * time.Hour)
	w.refuse(http.StatusConflict, domain.CodeSelectionExpired, "plan-move",
		fmt.Sprintf(`{"selection_id":%q,"destination_id":%q}`, sel.SelectionID, id("Lixo")))
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "plan-move",
		fmt.Sprintf(`{"selection_id":"nope","destination_id":%q}`, id("Lixo")))
}

// plan-rename and plan-create-folder: a taken name is 409 name_taken with
// no action; names no entry can take, an unchanged name, a source's top
// folder, and a member are 400.
func TestRenameAndNewFolderRules(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	id := func(p string) string { return w.id("disk", p) }
	rename := func(entry, name string) string { return fmt.Sprintf(`{"entry_id":%q,"name":%q}`, entry, name) }
	w.refuse(http.StatusConflict, domain.CodeNameTaken, "plan-rename", rename(id("Fotos/2004/a.jpg"), "b.jpg"))
	// Destino/c.jpg is missing with the owner's intent.
	w.refuse(http.StatusConflict, domain.CodeNameTaken, "plan-rename", rename(id("Destino/a.jpg"), "c.jpg"))
	w.refuse(http.StatusConflict, domain.CodeNameTaken, "plan-create-folder",
		fmt.Sprintf(`{"parent_id":%q,"name":"Fotos"}`, id("")))
	if n := w.count(`SELECT count(*) FROM actions`); n != 0 {
		t.Fatalf("refused plans left %d actions", n)
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\x00b", strings.Repeat("x", 256)} {
		w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rename", rename(id("Fotos/2004/a.jpg"), name))
		w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-create-folder",
			fmt.Sprintf(`{"parent_id":%q,"name":%q}`, id("Fotos"), name))
	}
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rename", rename(id("Fotos/2004/a.jpg"), "a.jpg"))
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rename", rename(id(""), "raiz"))
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-rename", rename("m7001", "x.txt"))
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "plan-rename", rename("999999", "x.txt"))
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-create-folder",
		fmt.Sprintf(`{"parent_id":%q,"name":"Nova"}`, id("pacote.zip")))

	a, items := w.plan("plan-rename", rename(id("Fotos/2004/a.jpg"), strings.Repeat("é", 127)))
	wantItems(t, items, "rename planned Fotos/2004/a.jpg -> Fotos/2004/"+strings.Repeat("é", 127))
	if a.Kind != "rename" || a.Bulk || a.Destination != nil {
		t.Errorf("rename action %+v", a)
	}
	a, items = w.plan("plan-create-folder", fmt.Sprintf(`{"parent_id":%q,"name":"Nova"}`, id("Fotos")))
	wantItems(t, items, "mkdir planned -> Fotos/Nova")
	if a.Kind != "create_folder" || a.Destination == nil || a.Destination.Path != "Fotos" || items[0].Entry != nil {
		t.Errorf("new folder action %+v, item %+v", a, items[0])
	}
}

// The request shapes plan-move refuses, and its destinations.
func TestPlanMoveRequests(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	id := func(p string) string { return w.id("disk", p) }
	file, dest := id("Fotos/2004/a.jpg"), id("Lixo")
	many := make([]string, maxEntryIDs+1)
	for i := range many {
		many[i] = file
	}
	for _, body := range []string{
		fmt.Sprintf(`{"destination_id":%q}`, dest),
		fmt.Sprintf(`{"entry_id":%q,"entry_ids":[%q],"destination_id":%q}`, file, file, dest),
		fmt.Sprintf(`{"entry_ids":[],"destination_id":%q}`, dest),
		moveBody(dest, many...),
		fmt.Sprintf(`{"entry_id":%q}`, file),
		fmt.Sprintf(`{"entry_id":%q,"destination_id":%q,"path":"/etc"}`, file, dest),
		fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, file, id("pacote.zip")),
		fmt.Sprintf(`{"entry_id":%q,"destination_id":"m7001"}`, file),
		fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, file, id("Destino/c.jpg")),
	} {
		w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-move", body)
	}
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":"999999"}`, file))
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "plan-move", fmt.Sprintf(`{"entry_id":"999999","destination_id":%q}`, dest))
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "plan-move", fmt.Sprintf(`{"entry_id":"x1","destination_id":%q}`, dest))
}

// Planning and running check the source: writes turned off is 409
// writes_disabled, an item that needs the owner's check is 409
// recovery_needed, an offline source 409 source_offline, and a
// configuration that forbids writes 409 writes_unavailable.
func TestPlanningChecksTheSource(t *testing.T) {
	t.Parallel()
	w := planDisk(t)
	id := func(p string) string { return w.id("disk", p) }
	move := fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, id("Fotos/2004/b.jpg"), id("Lixo"))
	a, _ := w.plan("plan-move", move)
	run := fmt.Sprintf(`{"action_id":%q}`, a.ID)

	w.exec(`UPDATE sources SET write_enabled = 0 WHERE id = 'disk'`)
	w.refuse(http.StatusConflict, domain.CodeWritesDisabled, "plan-move", move)
	w.refuse(http.StatusConflict, domain.CodeWritesDisabled, "plan-rename", fmt.Sprintf(`{"entry_id":%q,"name":"n.jpg"}`, id("Fotos/2004/b.jpg")))
	w.refuse(http.StatusConflict, domain.CodeWritesDisabled, "run-action", run)
	w.exec(`UPDATE sources SET write_enabled = 1 WHERE id = 'disk'`)

	w.exec(`INSERT INTO actions (id, kind, source_id, state, bulk, created_at) VALUES (900, 'move', 'disk', 'stopped', 0, 0)`)
	w.exec(`INSERT INTO action_items (action_id, seq, op, state, detail) VALUES (900, 1, 'rename', 'manual_recovery',
		'{"from":"absent","to":"other"}')`)
	w.refuse(http.StatusConflict, domain.CodeRecoveryNeeded, "plan-move", move)
	w.refuse(http.StatusConflict, domain.CodeRecoveryNeeded, "plan-create-folder", fmt.Sprintf(`{"parent_id":%q,"name":"N"}`, id("Lixo")))
	w.refuse(http.StatusConflict, domain.CodeRecoveryNeeded, "run-action", run)
	// Another source is not held up.
	w.plan("plan-create-folder", fmt.Sprintf(`{"parent_id":%q,"name":"N"}`, w.id("outro", "")))
	w.exec(`UPDATE action_items SET state = 'resolved' WHERE action_id = 900`)

	w.exec(`UPDATE sources SET state = 'offline' WHERE id = 'disk'`)
	w.refuse(http.StatusConflict, domain.CodeSourceOffline, "plan-move", move)
	w.exec(`UPDATE sources SET state = 'online' WHERE id = 'disk'`)

	forbidden := New(Options{Store: w.st, AllowWrites: false, Clock: w.clk, Logger: discard()})
	h := commands.New(commands.Options{Store: w.st, Jobs: w.r, Logger: discard()})
	forbidden.RegisterCommands(h)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", h)
	saved := w.mux
	w.mux = mux
	w.refuse(http.StatusConflict, domain.CodeWritesUnavailable, "plan-move", move)
	w.refuse(http.StatusConflict, domain.CodeWritesUnavailable, "run-action", run)
	w.mux = saved
}

// The scenario "Cancelling a queued move": a move waits behind a scan of
// its source; cancelled, it is stopped, nothing moves, and it does not run
// when the scan ends.
func TestCancellingAQueuedMove(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.Dir("A").File("a.txt", 10, mtime)
		root.Dir("B")
	})
	w.idle()
	a, _ := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, w.id("disk", "A/a.txt"), w.id("disk", "B")))

	// A scan that hangs listing A until released.
	blocked, release := make(chan struct{}), make(chan struct{})
	var once bool
	w.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpReadBatch && c.FullPath() == "/disk/A" && !once {
			once = true
			close(blocked)
			<-release
		}
	})
	var scan jobs.Accepted
	decode(t, w.ok(http.StatusAccepted, "start-scan", `{"source_id":"disk"}`), &scan)
	<-blocked

	var ran runResponse
	decode(t, w.ok(http.StatusAccepted, "run-action", fmt.Sprintf(`{"action_id":%q}`, a.ID)), &ran)
	time.Sleep(300 * time.Millisecond) // the move's job has its chance to start
	if got := w.action(a.ID); got.State != "queued" {
		t.Fatalf("the move is %s while the scan runs, want queued", got.State)
	}
	var cancelled actionResponse
	decode(t, w.ok(http.StatusOK, "cancel-action", fmt.Sprintf(`{"action_id":%q}`, a.ID)), &cancelled)
	if cancelled.Action.State != "stopped" || cancelled.Action.Counts["not_attempted"] != 1 {
		t.Fatalf("cancelled action %+v", cancelled.Action)
	}
	w.refuse(http.StatusConflict, domain.CodeActionNotRunnable, "cancel-action", fmt.Sprintf(`{"action_id":%q}`, a.ID))
	w.refuse(http.StatusConflict, domain.CodeActionNotRunnable, "run-action", fmt.Sprintf(`{"action_id":%q}`, a.ID))
	close(release)
	w.rec.SetBeforeCall(nil)
	w.idle()

	if got := w.action(a.ID); got.State != "stopped" || got.Counts["done"] != 0 {
		t.Errorf("after the scan the move is %s with %v", got.State, got.Counts)
	}
	if w.rec.Count(instrument.OpRename) != 0 || !w.exists("/disk", "A/a.txt") || w.exists("/disk", "B/a.txt") {
		t.Error("the cancelled move moved the file")
	}
	if p, _ := w.pathOf(w.id("disk", "A/a.txt")); p != "A/a.txt" {
		t.Errorf("the index moved the file to %q", p)
	}
	rec, err := w.r.Get(context.Background(), domain.JobID(mustAtoi(t, ran.JobID)))
	if err != nil || rec.State != domain.JobCancelled {
		t.Errorf("the move's job is %v (%v), want cancelled", rec.State, err)
	}
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	id, err := domain.ParseJobID(s)
	if err != nil {
		t.Fatal(err)
	}
	return int64(id)
}
