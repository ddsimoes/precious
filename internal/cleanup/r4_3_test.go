package cleanup

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"precious/internal/fsaccess/synthfs"
)

// restoreRow is the index row of entry id as "path state size decision".
func restoreRow(w *world, id string) string {
	w.t.Helper()
	var (
		path            []byte
		state, dec, eff string
		size, inode     int64
	)
	if err := w.st.Reader().QueryRow(`SELECT path, state, size, coalesce(decision, ''), eff_decision,
		coalesce(ino, 0) FROM entries WHERE id = ?`, id).Scan(&path, &state, &size, &dec, &eff,
		&inode); err != nil {
		w.t.Fatalf("entry %s: %v", id, err)
	}
	return fmt.Sprintf("%s %s %d own=%s eff=%s ino=%d", path, state, size, dec, eff, inode)
}

// restoreTags is the number of own tags of entry id with tag tagID.
func restoreTags(w *world, id string, tagID int64) int64 {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM entry_tags WHERE entry_id = ? AND tag_id = ?`, id, tagID)
}

func TestR4_3RestoreToTheOriginalPath(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2010, 1, 2, 3, 4, 5, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		a := root.Dir("Antigas")
		a.File("praia.jpg", 100, at).Seed(1)
		s := a.Dir("Sub")
		s.File("serra.jpg", 200, at).Seed(2)
		d := root.Dir("Docs")
		d.File("solto.txt", 50, at).Seed(3)
	})
	antigas, praia := w.id("casa", "Antigas"), w.id("casa", "Antigas/praia.jpg")
	sub, serra := w.id("casa", "Antigas/Sub"), w.id("casa", "Antigas/Sub/serra.jpg")
	solto := w.id("casa", "Docs/solto.txt")
	w.decide("casa", "Antigas", "discard")
	w.decide("casa", "Antigas/Sub/serra.jpg", "discard")
	w.decide("casa", "Docs/solto.txt", "discard")
	var tag struct {
		Tag struct {
			ID int64 `json:"id"`
		} `json:"tag"`
	}
	decode(t, w.ok(http.StatusCreated, "create-tag", `{"name":"ferias"}`), &tag)
	w.ok(http.StatusOK, "set-tags", fmt.Sprintf(`{"entry_ids":%s,"add":[%d]}`, ids(praia, serra), tag.Tag.ID))
	before := map[string]string{}
	for _, id := range []string{antigas, praia, sub, serra, solto} {
		before[id] = restoreRow(w, id)
	}

	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	if a := w.run(p.Action.ID); a.State != "done" {
		t.Fatalf("cleanup %+v", a)
	}
	wantItems(t, w.items(p.Action.ID, ""),
		"mkdir done -> .precious-quarantine",
		"mkdir done -> .precious-quarantine/1",
		"mkdir done -> .precious-quarantine/1/1",
		"rename done Antigas -> .precious-quarantine/1/1/Antigas",
		"record done -> .precious-quarantine/1/1.json",
		"mkdir done -> .precious-quarantine/1/2",
		"rename done Docs/solto.txt -> .precious-quarantine/1/2/solto.txt",
		"record done -> .precious-quarantine/1/2.json")
	if p.Action.ID != "1" {
		t.Fatalf("cleanup action %s, want 1", p.Action.ID)
	}

	// Restore the folder by its entry: back to Antigas, then its record and
	// item folder go; solto.txt keeps the plan folder.
	r := w.plan("plan-restore", `{"entry_ids":`+ids(antigas)+`}`)
	wantItems(t, r.Items,
		"rename planned .precious-quarantine/1/1/Antigas -> Antigas",
		"unlink planned .precious-quarantine/1/1.json",
		"rmdir planned .precious-quarantine/1/1")
	if a := w.run(r.Action.ID); a.State != "done" || a.Kind != "restore" {
		t.Fatalf("restore %+v", a)
	}
	wantItems(t, w.items(r.Action.ID, ""),
		"rename done .precious-quarantine/1/1/Antigas -> Antigas",
		"unlink done .precious-quarantine/1/1.json",
		"rmdir done .precious-quarantine/1/1")
	for _, id := range []string{antigas, praia, sub, serra} {
		if got := restoreRow(w, id); got != before[id] {
			t.Errorf("entry %s after the restore: %s, want %s", id, got, before[id])
		}
	}
	for _, id := range []string{praia, serra} {
		if n := restoreTags(w, id, tag.Tag.ID); n != 1 {
			t.Errorf("entry %s has %d ferias tags, want 1", id, n)
		}
	}
	for rel, want := range map[string]bool{
		"Antigas/praia.jpg": true, "Antigas/Sub/serra.jpg": true,
		".precious-quarantine/1/1": false, ".precious-quarantine/1/1.json": false,
		".precious-quarantine/1/2/solto.txt": true, ".precious-quarantine/1/2.json": true,
	} {
		if got := w.exists("casa", rel); got != want {
			t.Errorf("%s on disk: %v, want %v", rel, got, want)
		}
	}
	if n := w.count(`SELECT count(*) FROM entries WHERE source_id = 'casa' AND (path = ? OR path = ?
		OR substr(path, 1, 25) = ?)`, []byte(".precious-quarantine/1/1"), []byte(".precious-quarantine/1/1.json"),
		[]byte(".precious-quarantine/1/1/")); n != 0 {
		t.Errorf("%d index rows left for the item folder and its record", n)
	}
	q := w.quarantined("casa")
	if len(q.Items) != 1 || q.Items[0].Entry.ID != solto || q.Total != (amount{Files: 1, Bytes: 50}) {
		t.Fatalf("quarantine after the first restore: %+v", q)
	}

	// Restore the rest of the plan by its ID: the plan folder, emptied, is
	// swept.
	r = w.plan("plan-restore", `{"plan_id":"1"}`)
	wantItems(t, r.Items,
		"rename planned .precious-quarantine/1/2/solto.txt -> Docs/solto.txt",
		"unlink planned .precious-quarantine/1/2.json",
		"rmdir planned .precious-quarantine/1/2",
		"rmdir planned .precious-quarantine/1")
	if a := w.run(r.Action.ID); a.State != "done" {
		t.Fatalf("restore %+v", a)
	}
	wantItems(t, w.items(r.Action.ID, ""),
		"rename done .precious-quarantine/1/2/solto.txt -> Docs/solto.txt",
		"unlink done .precious-quarantine/1/2.json",
		"rmdir done .precious-quarantine/1/2",
		"rmdir done .precious-quarantine/1")
	if got := restoreRow(w, solto); got != before[solto] {
		t.Errorf("solto.txt after the restore: %s, want %s", got, before[solto])
	}
	for rel, want := range map[string]bool{
		"Docs/solto.txt": true, ".precious-quarantine": true, ".precious-quarantine/1": false,
	} {
		if got := w.exists("casa", rel); got != want {
			t.Errorf("%s on disk: %v, want %v", rel, got, want)
		}
	}
	if n := w.count(`SELECT count(*) FROM entries WHERE source_id = 'casa' AND substr(path, 1, 21) = ?`,
		[]byte(".precious-quarantine/")); n != 0 {
		t.Errorf("%d index rows left in the quarantine", n)
	}
	q = w.quarantined("casa")
	if len(q.Items) != 0 || q.Total != (amount{}) {
		t.Fatalf("quarantine after the restores: %+v", q)
	}
	w.refuse(http.StatusNotFound, "not_found", "plan-restore", `{"plan_id":"1"}`)
}

func TestR4_3RestoreWhenThePathIsTaken(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2010, 1, 2, 3, 4, 5, 0, time.UTC)
	later := time.Date(2011, 6, 7, 8, 9, 10, 0, time.UTC)
	root := w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Docs").File("carta.txt", 50, at).Seed(3)
		root.Dir("Outra")
	})
	carta, outra := w.id("casa", "Docs/carta.txt"), w.id("casa", "Outra")
	w.decide("casa", "Docs/carta.txt", "discard")
	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	if a := w.run(p.Action.ID); a.State != "done" {
		t.Fatalf("cleanup %+v", a)
	}
	if path, _ := w.pathOf(carta); path != ".precious-quarantine/1/1/carta.txt" {
		t.Fatalf("carta.txt at %q", path)
	}

	// A new file now holds the original path.
	newcomer := root.Child("Docs").File("carta.txt", 80, later).Seed(9)
	w.scan("casa")
	fresh := w.id("casa", "Docs/carta.txt")
	if fresh == carta {
		t.Fatalf("the newcomer took the quarantined entry's ID %s", carta)
	}
	freshRow := restoreRow(w, fresh)
	cartaRow := restoreRow(w, carta)

	r := w.plan("plan-restore", `{"entry_ids":`+ids(carta)+`}`)
	wantItems(t, r.Items, "rename conflict name_taken .precious-quarantine/1/1/carta.txt -> Docs/carta.txt")

	// A destination in the quarantine is refused.
	w.refuse(http.StatusBadRequest, "invalid_request", "plan-restore",
		fmt.Sprintf(`{"entry_ids":%s,"destination_id":%q}`, ids(carta), w.id("casa", ".precious-quarantine/1")))

	r = w.plan("plan-restore", fmt.Sprintf(`{"entry_ids":%s,"destination_id":%q}`, ids(carta), outra))
	wantItems(t, r.Items,
		"rename planned .precious-quarantine/1/1/carta.txt -> Outra/carta.txt",
		"unlink planned .precious-quarantine/1/1.json",
		"rmdir planned .precious-quarantine/1/1",
		"rmdir planned .precious-quarantine/1")
	if a := w.run(r.Action.ID); a.State != "done" {
		t.Fatalf("restore %+v", a)
	}
	wantItems(t, w.items(r.Action.ID, ""),
		"rename done .precious-quarantine/1/1/carta.txt -> Outra/carta.txt",
		"unlink done .precious-quarantine/1/1.json",
		"rmdir done .precious-quarantine/1/1",
		"rmdir done .precious-quarantine/1")
	if got, want := restoreRow(w, carta), "Outra/carta.txt"+cartaRow[len(".precious-quarantine/1/1/carta.txt"):]; got != want {
		t.Errorf("carta.txt after the restore: %s, want %s", got, want)
	}
	if got := restoreRow(w, fresh); got != freshRow {
		t.Errorf("the newcomer after the restore: %s, want %s", got, freshRow)
	}
	if got := w.id("casa", "Docs/carta.txt"); got != fresh {
		t.Errorf("Docs/carta.txt is entry %s, want the newcomer %s", got, fresh)
	}
	if root.Child("Docs").Child("carta.txt") != newcomer || newcomer.Info().Size != 80 {
		t.Errorf("the newcomer changed on disk: %+v", root.Child("Docs").Child("carta.txt").Info())
	}
	if !w.exists("casa", "Outra/carta.txt") || root.Child("Outra").Child("carta.txt").Info().Size != 50 {
		t.Errorf("carta.txt is not in Outra on disk")
	}
	if w.exists("casa", ".precious-quarantine/1") {
		t.Errorf("the plan folder is still on disk")
	}
	if q := w.quarantined("casa"); len(q.Items) != 0 {
		t.Errorf("quarantine after the restore: %+v", q)
	}
}

func TestR4_3RestoreWhenTheOriginalFolderIsGone(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2010, 1, 2, 3, 4, 5, 0, time.UTC)
	root := w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Pasta").File("velho.txt", 70, at).Seed(4)
		root.Dir("Outra")
	})
	velho, outra := w.id("casa", "Pasta/velho.txt"), w.id("casa", "Outra")
	w.decide("casa", "Pasta/velho.txt", "discard")
	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	if a := w.run(p.Action.ID); a.State != "done" {
		t.Fatalf("cleanup %+v", a)
	}
	root.Remove("Pasta")
	w.scan("casa")
	if path, state := w.pathOf(w.id("casa", "Pasta")); path != "Pasta" || state != "missing" {
		t.Fatalf("Pasta %q %s, want missing", path, state)
	}

	r := w.plan("plan-restore", `{"entry_ids":`+ids(velho)+`}`)
	wantItems(t, r.Items, "rename conflict previous_folder_gone .precious-quarantine/1/1/velho.txt -> Pasta/velho.txt")
	r = w.plan("plan-restore", fmt.Sprintf(`{"entry_ids":%s,"destination_id":%q}`, ids(velho), outra))
	wantItems(t, r.Items,
		"rename planned .precious-quarantine/1/1/velho.txt -> Outra/velho.txt",
		"unlink planned .precious-quarantine/1/1.json",
		"rmdir planned .precious-quarantine/1/1",
		"rmdir planned .precious-quarantine/1")
	if a := w.run(r.Action.ID); a.State != "done" {
		t.Fatalf("restore %+v", a)
	}
	if path, state := w.pathOf(velho); path != "Outra/velho.txt" || state != "present" {
		t.Errorf("velho.txt %q %s", path, state)
	}
	if !w.exists("casa", "Outra/velho.txt") || w.exists("casa", ".precious-quarantine/1") {
		t.Errorf("disk after the restore: Outra/velho.txt %v, plan folder %v",
			w.exists("casa", "Outra/velho.txt"), w.exists("casa", ".precious-quarantine/1"))
	}
}
