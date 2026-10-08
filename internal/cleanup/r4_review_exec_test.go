package cleanup

import (
	"fmt"
	"net/http"
	"testing"

	"precious/internal/fsaccess/synthfs"
)

// G5 (r4 D13, S12): undoing the creation of a folder since quarantined
// plans no rmdir of it: refused in_quarantine. The folder stays in the
// quarantine, where a restore brings it back.
func TestR4UndoLeavesAQuarantinedFolder(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Docs").File("carta.txt", 100, staleAt).Seed(1)
	})
	made := w.plan("plan-create-folder", fmt.Sprintf(`{"parent_id":%q,"name":"Novo"}`, w.id("casa", "")))
	if a := w.run(made.Action.ID); a.State != "done" {
		t.Fatalf("create-folder %+v", a)
	}
	w.decide("casa", "Novo", "discard")
	cleanup := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	if a := w.run(cleanup.Action.ID); a.State != "done" {
		t.Fatalf("cleanup %+v", a)
	}
	at := ".precious-quarantine/" + cleanup.Action.ID + "/1/Novo"
	novo := w.id("casa", at)

	undo := w.plan("plan-undo", fmt.Sprintf(`{"action_id":%q}`, made.Action.ID))
	wantItems(t, undo.Items, "rmdir refused in_quarantine "+at)
	w.refuse(http.StatusConflict, "action_not_runnable", "run-action", fmt.Sprintf(`{"action_id":%q}`, undo.Action.ID))
	if path, state := w.pathOf(novo); path != at || state != "present" || !w.exists("casa", at) {
		t.Fatalf("Novo after the undo: %q %s", path, state)
	}

	r := w.plan("plan-restore", `{"entry_ids":`+ids(novo)+`}`)
	if len(r.Items) == 0 || r.Items[0].summary() != "rename planned "+at+" -> Novo" {
		t.Fatalf("restore items %+v", r.Items)
	}
	if a := w.run(r.Action.ID); a.State != "done" {
		t.Fatalf("restore %+v", a)
	}
	if path, state := w.pathOf(novo); path != "Novo" || state != "present" || !w.exists("casa", "Novo") {
		t.Errorf("Novo after the restore: %q %s", path, state)
	}
}

// unreadableVelho quarantines casa's folder Velho, which holds a folder
// made unreadable since, and solto.jpg, whose twin Fotos/praia.jpg is
// verified, and returns their entry IDs.
func unreadableVelho(w *world) (velho, solto string) {
	w.t.Helper()
	root := w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Fotos").File("praia.jpg", 100, staleAt).Seed(1)
		v := root.Dir("Velho")
		v.File("a.txt", 50, staleAt).Seed(2)
		v.Dir("privado").File("diario.txt", 60, staleAt).Seed(3)
		root.File("solto.jpg", 100, staleAt).Seed(1)
	})
	w.decide("casa", "Velho", "discard")
	w.decide("casa", "solto.jpg", "discard")
	velho = staleQuarantineVelho(w)
	solto = w.id("casa", ".precious-quarantine/1/2/solto.jpg")
	root.Child(".precious-quarantine").Child("1").Child("1").Child("Velho").Child("privado").Unreadable()
	w.scan("casa")
	return velho, solto
}

// G6 (r4 D11, C3): the records of an item that is not readable gate no
// purge. With one item holding an unreadable folder and one file whose twin
// is verified, the check is allowed with nothing to confirm, plan-purge
// answers 201 refusing the unreadable item, and the purge deletes the other.
func TestR4UnreadableItemGatesNoPurge(t *testing.T) {
	w := newWorld(t)
	velho, solto := unreadableVelho(w)

	check, c := w.checkPurge(velho, solto)
	if c.State != "ready" || !c.Allowed || c.Unconfirmed != (amount{}) {
		t.Fatalf("check %+v; want ready and allowed, nothing unconfirmed", c)
	}
	if n := w.count(`SELECT count(*) FROM purge_check_files f JOIN purge_check_items i
		ON i.check_id = f.check_id AND i.entry_id = f.item_id
		WHERE f.check_id = ? AND i.readable = 0 AND f.verdict = 'unreadable'`, check); n == 0 {
		t.Fatal("the unreadable item has no unreadable records")
	}
	pp := w.plan("plan-purge", fmt.Sprintf(`{"check_id":%q}`, check))
	got := map[string]bool{}
	for _, it := range pp.Items {
		got[it.summary()] = true
	}
	for _, want := range []string{
		"purge refused unreadable .precious-quarantine/1/1/Velho",
		"purge planned .precious-quarantine/1/2/solto.jpg",
	} {
		if !got[want] {
			t.Errorf("plan-purge items %v; want %q", got, want)
		}
	}
	if a := w.run(pp.Action.ID); a.State != "done" {
		t.Fatalf("purge %+v", a)
	}
	if w.exists("casa", ".precious-quarantine/1/2/solto.jpg") {
		t.Error("solto.jpg was not deleted")
	}
	if !w.exists("casa", ".precious-quarantine/1/1/Velho/a.txt") || !w.exists("casa", "Fotos/praia.jpg") {
		t.Error("the unreadable item or the copy was deleted")
	}
}

// G17: a check whose items left could not be read has nothing to delete:
// after its readable item was deleted for good, or from the start, it is
// ready but not allowed, and plan-purge answers 409 check_stale.
func TestR4OnlyUnreadableItemsAreNotAllowed(t *testing.T) {
	w := newWorld(t)
	velho, solto := unreadableVelho(w)
	check, _ := w.checkPurge(velho, solto)
	body := fmt.Sprintf(`{"check_id":%q}`, check)
	if a := w.run(w.plan("plan-purge", body).Action.ID); a.State != "done" || a.DeletedFiles != 1 {
		t.Fatalf("purge %+v", a)
	}
	if c := staleCheckState(w, check, "ready", "", false); c.Items != 1 || c.Unconfirmed != (amount{}) {
		t.Fatalf("check after the purge %+v; want the unreadable item left, nothing unconfirmed", c)
	}
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", body)

	only, c := w.checkPurge(velho)
	if c.State != "ready" || c.Allowed || c.Items != 1 || c.Unconfirmed != (amount{}) {
		t.Fatalf("check of the unreadable item %+v; want ready, not allowed, nothing unconfirmed", c)
	}
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, only))
	if !w.exists("casa", ".precious-quarantine/1/1/Velho/a.txt") {
		t.Error("the unreadable item was deleted")
	}
}
