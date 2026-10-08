package organize

import (
	"fmt"
	"maps"
	"net/http"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// subtreeIDs maps every path at and below prefix in src to its entry ID,
// with prefix cut.
func (w *world) subtreeIDs(src domain.SourceID, prefix string) map[string]int64 {
	w.t.Helper()
	rows, err := w.st.Reader().Query(`SELECT id, path FROM entries WHERE source_id = ? AND (path = ? OR
		(path >= ? AND path < ?))`, string(src), []byte(prefix), []byte(prefix+"/"), []byte(prefix+"0"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var (
			id   int64
			path []byte
		)
		if err := rows.Scan(&id, &path); err != nil {
			w.t.Fatal(err)
		}
		out[string(path[len(prefix):])] = id
	}
	return out
}

func undoBody(action string) string { return fmt.Sprintf(`{"action_id":%q}`, action) }

// R3.3: the owner moves Fotos/2004 into Documentos and undoes it; Fotos/2004
// is back at its path with the same entry IDs, the move's item reads the
// undo item in reversed_by, and the history shows the move undone. The
// undo can itself be undone.
func TestR3_3UndoOfAMove(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		f04 := root.Dir("Fotos").Dir("2004")
		f04.File("a.jpg", 100, mtime)
		f04.File("b.jpg", 200, mtime)
		f04.Dir("Natal").File("c.jpg", 300, mtime)
		root.Dir("Documentos")
	})
	before := w.subtreeIDs("disk", "Fotos/2004")
	move, _ := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, w.id("disk", "Fotos/2004"),
		w.id("disk", "Documentos")))
	if got := w.run(move.ID); got.State != "done" || !got.Undo.Possible {
		t.Fatalf("move %+v", got)
	}
	if moved := w.subtreeIDs("disk", "Documentos/2004"); !maps.Equal(moved, before) {
		t.Fatalf("after the move %v, want %v", moved, before)
	}

	undo, items := w.plan("plan-undo", undoBody(move.ID))
	wantItems(t, items, "rename planned Documentos/2004 -> Fotos/2004")
	if undo.Kind != "undo" || undo.UndoOf == nil || *undo.UndoOf != move.ID || undo.Bulk || undo.Destination != nil {
		t.Fatalf("undo action %+v", undo)
	}
	if got := w.run(undo.ID); got.State != "done" || got.Counts["done"] != 1 {
		t.Fatalf("undo %+v", got)
	}
	if back := w.subtreeIDs("disk", "Fotos/2004"); !maps.Equal(back, before) {
		t.Errorf("after the undo %v, want %v", back, before)
	}
	if !w.exists("/disk", "Fotos/2004/Natal/c.jpg") || w.exists("/disk", "Documentos/2004") {
		t.Error("the disk does not hold Fotos/2004 back in place")
	}
	var reversedBy, undoItem int64
	if err := w.st.Reader().QueryRow(`SELECT reversed_by FROM action_items WHERE action_id = ?`, move.ID).
		Scan(&reversedBy); err != nil {
		t.Fatal(err)
	}
	if err := w.st.Reader().QueryRow(`SELECT id FROM action_items WHERE action_id = ?`, undo.ID).Scan(&undoItem); err != nil {
		t.Fatal(err)
	}
	if reversedBy != undoItem {
		t.Errorf("the move's item reads reversed_by %d, want %d", reversedBy, undoItem)
	}
	got := w.action(move.ID)
	if got.Undo.Possible || got.Undo.Reason == nil || *got.Undo.Reason != undoAlreadyUndone || got.Reversed != 1 {
		t.Errorf("the undone move reads undo %+v, reversed %d", got.Undo, got.Reversed)
	}
	if its := w.items(move.ID, ""); len(its) != 1 || !its[0].Reversed {
		t.Errorf("the move's items %v", summaries(its))
	}
	w.refuse(http.StatusConflict, domain.CodeActionNotUndoable, "plan-undo", undoBody(move.ID))

	// The history lists both, newest first.
	var list page[actionJSON]
	w.get("/api/history?source=disk", &list)
	if len(list.Items) != 2 || list.Items[0].ID != undo.ID || list.Items[1].ID != move.ID {
		t.Errorf("history %+v", list.Items)
	}
	// Redo.
	redo, items := w.plan("plan-undo", undoBody(undo.ID))
	wantItems(t, items, "rename planned Fotos/2004 -> Documentos/2004")
	if got := w.run(redo.ID); got.State != "done" {
		t.Fatalf("redo %+v", got)
	}
}

// R3.3: a rename whose old name was taken since plans a conflict on undo;
// with destination_id, the file moves there under its old name and the
// newcomer is left alone.
func TestR3_3UndoWhenThePreviousNameIsTaken(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	var docs *synthfs.Node
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		docs = root.Dir("Docs")
		docs.File("a.txt", 10, mtime)
		root.Dir("Outra")
	})
	file := w.id("disk", "Docs/a.txt")
	rename, _ := w.plan("plan-rename", fmt.Sprintf(`{"entry_id":%q,"name":"b.txt"}`, file))
	if got := w.run(rename.ID); got.State != "done" {
		t.Fatalf("rename %+v", got)
	}
	docs.File("a.txt", 99, mtime)
	w.scan("disk")
	newcomer := w.id("disk", "Docs/a.txt")

	undo, items := w.plan("plan-undo", undoBody(rename.ID))
	wantItems(t, items, "rename conflict name_taken Docs/b.txt -> Docs/a.txt")
	if undo.Counts["conflict"] != 1 || undo.Counts["planned"] != 0 {
		t.Errorf("undo counts %v", undo.Counts)
	}
	w.refuse(http.StatusConflict, domain.CodeActionNotRunnable, "run-action", fmt.Sprintf(`{"action_id":%q}`, undo.ID))

	undo, items = w.plan("plan-undo", fmt.Sprintf(`{"action_id":%q,"destination_id":%q}`, rename.ID, w.id("disk", "Outra")))
	wantItems(t, items, "rename planned Docs/b.txt -> Outra/a.txt")
	if undo.Destination == nil || undo.Destination.Path != "Outra" {
		t.Errorf("undo destination %+v", undo.Destination)
	}
	if got := w.run(undo.ID); got.State != "done" {
		t.Fatalf("undo %+v", got)
	}
	if p, _ := w.pathOf(file); p != "Outra/a.txt" {
		t.Errorf("the renamed file is at %q, want Outra/a.txt", p)
	}
	if p, state := w.pathOf(newcomer); p != "Docs/a.txt" || state != "present" {
		t.Errorf("the newcomer is at %q (%s)", p, state)
	}
	var size int64
	if err := w.st.Reader().QueryRow(`SELECT size FROM entries WHERE id = ?`, newcomer).Scan(&size); err != nil || size != 99 {
		t.Errorf("the newcomer's size %d (%v)", size, err)
	}
	if !w.exists("/disk", "Outra/a.txt") || !w.exists("/disk", "Docs/a.txt") || w.exists("/disk", "Docs/b.txt") {
		t.Error("the disk does not match")
	}
	if got := w.action(rename.ID); got.Undo.Possible {
		t.Errorf("the rename still reads undoable: %+v", got.Undo)
	}
}

// R3.3: an undo that stopped early (writes turned off after its first
// step) leaves the move's other item undoable; and an expired undo plan
// leaves the action undoable.
func TestR3_3UndoThatStoppedOrExpired(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		x := root.Dir("X")
		x.File("1.txt", 1, mtime)
		x.File("2.txt", 2, mtime)
		root.Dir("Y")
	})
	move, _ := w.plan("plan-move", fmt.Sprintf(`{"entry_ids":[%q,%q],"destination_id":%q}`, w.id("disk", "X/1.txt"),
		w.id("disk", "X/2.txt"), w.id("disk", "Y")))
	if got := w.run(move.ID); got.State != "done" || got.Counts["done"] != 2 {
		t.Fatalf("move %+v", got)
	}

	// An expired undo plan changes nothing.
	expired, _ := w.plan("plan-undo", undoBody(move.ID))
	w.clk.advance(actionTTL + time.Minute)
	w.refuse(http.StatusConflict, domain.CodeActionExpired, "run-action", fmt.Sprintf(`{"action_id":%q}`, expired.ID))
	if got := w.action(move.ID); !got.Undo.Possible {
		t.Fatalf("after an expired undo plan the move reads %+v", got.Undo)
	}

	undo, items := w.plan("plan-undo", undoBody(move.ID))
	wantItems(t, items, "rename planned Y/2.txt -> X/2.txt", "rename planned Y/1.txt -> X/1.txt")
	// Writes go off while the first step runs.
	w.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpRename {
			w.exec(`UPDATE sources SET write_enabled = 0 WHERE id = 'disk'`)
		}
	})
	stopped := w.run(undo.ID)
	w.rec.SetBeforeCall(nil)
	if stopped.State != "stopped" || stopped.Counts["done"] != 1 || stopped.Counts["not_permitted"] != 1 {
		t.Fatalf("undo %s with %v", stopped.State, stopped.Counts)
	}
	got := w.action(move.ID)
	if !got.Undo.Possible || got.Reversed != 1 {
		t.Fatalf("after a stopped undo the move reads %+v, reversed %d", got.Undo, got.Reversed)
	}
	w.exec(`UPDATE sources SET write_enabled = 1 WHERE id = 'disk'`)
	again, items := w.plan("plan-undo", undoBody(move.ID))
	wantItems(t, items, "rename planned Y/1.txt -> X/1.txt")
	if got := w.run(again.ID); got.State != "done" {
		t.Fatalf("second undo %+v", got)
	}
	if got := w.action(move.ID); got.Undo.Possible || got.Reversed != 2 {
		t.Errorf("the move reads %+v, reversed %d", got.Undo, got.Reversed)
	}
	if !w.exists("/disk", "X/1.txt") || !w.exists("/disk", "X/2.txt") {
		t.Error("the files are not back")
	}
}
