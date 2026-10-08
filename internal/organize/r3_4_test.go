package organize

import (
	"fmt"
	"net/http"
	"testing"

	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// R3.4: the owner selects all results of a search and plans a move into a
// folder; a new matching file is indexed before the owner confirms. The
// plan lists every selected entry, with its conflict or refusal; the run
// moves exactly the planned runnable items, and the new file stays where
// it was.
func TestR3_4BulkMoveMovesOnlyTheEntriesShown(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	var f04 *synthfs.Node
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		fotos := root.Dir("Fotos")
		f04 = fotos.Dir("2004")
		f04.File("IMG_0001.jpg", 101, mtime)
		f04.File("IMG_0002.jpg", 102, mtime)
		f04.File("IMG_0003.jpg", 103, mtime)
		fotos.Dir("2005").File("IMG_0004.jpg", 104, mtime)
		root.Dir("Destino").File("IMG_0002.jpg", 202, mtime)
		root.Dir("Outros").File("notas.txt", 5, mtime)
	})
	id := func(p string) string { return w.id("disk", p) }
	w.decide(id("Destino"), "discard")
	w.decide(id("Fotos/2005"), "keep")

	var sel struct {
		SelectionID string `json:"selection_id"`
		Count       int    `json:"count"`
	}
	decode(t, w.ok(http.StatusCreated, "create-selection", `{"query":{"source":"disk","name":"IMG_"}}`), &sel)
	if sel.Count != 5 {
		t.Fatalf("the selection holds %d entries, want 5", sel.Count)
	}
	a, items := w.plan("plan-move", fmt.Sprintf(`{"selection_id":%q,"destination_id":%q}`, sel.SelectionID, id("Destino")))
	wantItems(t, items,
		"rename refused already_there Destino/IMG_0002.jpg -> Destino/IMG_0002.jpg",
		"rename planned Fotos/2004/IMG_0001.jpg -> Destino/IMG_0001.jpg",
		"rename conflict name_taken Fotos/2004/IMG_0002.jpg -> Destino/IMG_0002.jpg",
		"rename planned Fotos/2004/IMG_0003.jpg -> Destino/IMG_0003.jpg",
		"rename refused would_lose_keep Fotos/2005/IMG_0004.jpg -> Destino/IMG_0004.jpg")
	if !a.Bulk || len(items) != sel.Count || a.Counts["planned"] != 2 || a.Bytes != 101+103 {
		t.Fatalf("plan %+v", a)
	}
	for _, it := range items {
		if it.Entry == nil || it.Entry.Path != it.From.Path {
			t.Errorf("item %s has entry %+v", itemSummary(it), it.Entry)
		}
		if it.State == "planned" && (it.DecisionAfter == nil || *it.DecisionAfter != "discard") {
			t.Errorf("item %s decision_after %v", itemSummary(it), it.DecisionAfter)
		}
	}

	// A new matching file is indexed before the run.
	f04.File("IMG_0009.jpg", 109, mtime)
	w.scan("disk")
	newcomer := id("Fotos/2004/IMG_0009.jpg")

	done := w.run(a.ID)
	if done.State != "done" || done.Counts["done"] != 2 || done.Counts["conflict"] != 1 || done.Counts["refused"] != 2 {
		t.Fatalf("run ended %s with %v", done.State, done.Counts)
	}
	if n := w.rec.Count(instrument.OpRename); n != 2 {
		t.Errorf("%d renames on the disk, want 2", n)
	}
	for p, want := range map[string]bool{
		"Destino/IMG_0001.jpg": true, "Destino/IMG_0003.jpg": true, "Fotos/2004/IMG_0009.jpg": true,
		"Fotos/2004/IMG_0002.jpg": true, "Fotos/2005/IMG_0004.jpg": true,
		"Fotos/2004/IMG_0001.jpg": false, "Fotos/2004/IMG_0003.jpg": false, "Destino/IMG_0009.jpg": false,
	} {
		if w.exists("/disk", p) != want {
			t.Errorf("%s on the disk: %v, want %v", p, !want, want)
		}
	}
	if p, _ := w.pathOf(newcomer); p != "Fotos/2004/IMG_0009.jpg" {
		t.Errorf("the new file is at %q in the index", p)
	}
	for _, it := range w.items(a.ID, "state=done") {
		if it.Entry == nil || it.Entry.Path != it.To.Path || it.Entry.EffDecision != "discard" {
			t.Errorf("done item %s reads entry %+v", itemSummary(it), it.Entry)
		}
	}
}
