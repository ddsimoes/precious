package cleanup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
)

// staleAt is the time of every file of the R4.8 disks.
var staleAt = time.Date(2010, 1, 2, 3, 4, 5, 0, time.UTC)

// staleCheckState fails unless check id is in state with stale_reason
// reason ("" for none) and allowed as given.
func staleCheckState(w *world, id, state, reason string, allowed bool) checkJSON {
	w.t.Helper()
	c := w.check(id)
	got := ""
	if c.StaleReason != nil {
		got = *c.StaleReason
	}
	if c.State != state || got != reason || c.Allowed != allowed {
		w.t.Fatalf("check %s: state %s, stale_reason %q, allowed %v; want %s, %q, %v", id, c.State, got, c.Allowed,
			state, reason, allowed)
	}
	return c
}

// staleQuarantineVelho quarantines the discarded folder Velho of casa, the
// first item of cleanup action 1, and returns its entry ID.
func staleQuarantineVelho(w *world) string {
	w.t.Helper()
	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	if a := w.run(p.Action.ID); a.State != "done" || a.ID != "1" {
		w.t.Fatalf("cleanup %+v", a)
	}
	velho := w.id("casa", ".precious-quarantine/1/1/Velho")
	return velho
}

// staleSafeTwin fails unless the files of check id are exactly Velho (no
// content) and Velho/praia.jpg, safe thanks to Fotos/praia.jpg.
func staleSafeTwin(w *world, id string) {
	w.t.Helper()
	files := w.checkFiles(id, "verdict=safe")
	if len(files) != 1 || files[0].Path != ".precious-quarantine/1/1/Velho/praia.jpg" || files[0].Size != 100 ||
		files[0].Copy == nil || files[0].Copy.SourceID != "casa" || files[0].Copy.Path != "Fotos/praia.jpg" ||
		files[0].Copy.HardLink {
		w.t.Fatalf("safe files of check %s: %+v", id, files)
	}
}

// R4.8 "A copy decided again after the check": the owner discards the
// twin's folder, and both a new purge plan and one prepared before are
// refused check_stale, with nothing deleted.
func TestR4_8ACopyDecidedAgainAfterTheCheck(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Fotos").File("praia.jpg", 100, staleAt).Seed(1)
		root.Dir("Velho").File("praia.jpg", 100, staleAt).Seed(1)
	})
	w.decide("casa", "Velho", "discard")
	velho := staleQuarantineVelho(w)
	praia := w.id("casa", ".precious-quarantine/1/1/Velho/praia.jpg")
	check, c := w.checkPurge(velho)
	if c.State != "ready" || !c.Allowed || c.Counts.Verdict["safe"] != (amount{Files: 1, Bytes: 100}) {
		t.Fatalf("check %+v", c)
	}
	staleSafeTwin(w, check)
	purgeBody := fmt.Sprintf(`{"check_id":%q}`, check)
	pp := w.plan("plan-purge", purgeBody)
	wantItems(t, pp.Items,
		"verify planned",
		"purge planned .precious-quarantine/1/1/Velho",
		"rmdir planned .precious-quarantine/1")

	w.decide("casa", "Fotos", "discard")
	staleCheckState(w, check, "stale", "index_changed", false)
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", purgeBody)
	w.refuse(http.StatusConflict, "check_stale", "run-action", fmt.Sprintf(`{"action_id":%q}`, pp.Action.ID))
	w.idle()

	a := w.action(pp.Action.ID)
	if a.State != "planned" || a.DeletedFiles != 0 || a.DeletedBytes != 0 || a.FreedBytes != 0 {
		t.Fatalf("purge action after the refusals: %+v", a)
	}
	wantItems(t, w.items(pp.Action.ID, ""),
		"verify planned",
		"purge planned .precious-quarantine/1/1/Velho",
		"rmdir planned .precious-quarantine/1")
	if !w.exists("casa", ".precious-quarantine/1/1/Velho/praia.jpg") || !w.exists("casa", "Fotos/praia.jpg") {
		t.Fatalf("a file is gone from the disk")
	}
	for id, want := range map[string]string{velho: ".precious-quarantine/1/1/Velho", praia: ".precious-quarantine/1/1/Velho/praia.jpg"} {
		if path, state := w.pathOf(id); path != want || state != "present" {
			t.Errorf("entry %s: %q %s, want %q present", id, path, state, want)
		}
	}
	if q := w.quarantined("casa"); len(q.Items) != 1 || q.Items[0].Entry.ID != velho {
		t.Errorf("quarantine: %+v", q)
	}
}

// G9: a copy decided again while the check runs, before the record that
// names it is written (so MarkStale finds nothing to mark), still makes the
// check stale: the check reads Velho/praia.jpg and finds its twin, then,
// while it reads Velho/z.txt, the owner discards the twin's folder.
func TestR4_8ACopyDecidedAgainWhileTheCheckRuns(t *testing.T) {
	for _, decide := range []bool{false, true} {
		t.Run(fmt.Sprintf("decide=%v", decide), func(t *testing.T) {
			w := newWorld(t)
			w.disk("casa", "/casa", func(root *synthfs.Node) {
				root.Dir("Fotos").File("praia.jpg", 100, staleAt).Seed(1)
				v := root.Dir("Velho")
				v.File("praia.jpg", 100, staleAt).Seed(1)
				v.File("z.txt", 50, staleAt).Seed(2)
			})
			w.decide("casa", "Velho", "discard")
			velho := staleQuarantineVelho(w)
			ctx := context.Background()
			var check int64
			err := w.r.Write(ctx, func(tx *jobs.Tx) error {
				if err := tx.SQL().QueryRow(`INSERT INTO purge_checks (source_id, state, created_at)
					VALUES ('casa', 'running', 0) RETURNING id`).Scan(&check); err != nil {
					return err
				}
				_, err := tx.SQL().Exec(`INSERT INTO purge_check_items (check_id, entry_id, path, readable)
					VALUES (?, ?, ?, 1)`, check, velho, []byte(".precious-quarantine/1/1/Velho"))
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			rt := &checkRuntime{onYield: func(n int) {
				if n == 2 && decide { // the twin is found; z.txt is next
					w.decide("casa", "Fotos", "discard")
				}
			}}
			payload, _ := json.Marshal(checkPayload{CheckID: strconv.FormatInt(check, 10)})
			if err := (&checkHandler{s: w.svc}).Run(ctx, jobs.Job{Kind: KindPurgeCheck, PayloadVersion: 1,
				Payload: payload, SourceID: "casa", Attempt: 1}, rt); err != nil {
				t.Fatal(err)
			}
			id := strconv.FormatInt(check, 10)
			staleSafeTwin(w, id)
			if !decide {
				staleCheckState(w, id, "ready", "", false) // z.txt has no copy
				return
			}
			staleCheckState(w, id, "stale", "index_changed", false)
			w.refuse(http.StatusConflict, "check_stale", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, id))
		})
	}
}

// R4.8: restoring an item of a checked set makes its check stale, and so
// does moving one of its files out of the quarantine.
func TestR4_8RestoreOrMoveOutMakesTheCheckStale(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Fotos").File("praia.jpg", 100, staleAt).Seed(1)
		v := root.Dir("Velho")
		v.File("praia.jpg", 100, staleAt).Seed(1)
		v.File("serra.jpg", 200, staleAt).Seed(2)
		root.File("solto.bin", 300, staleAt).Seed(5)
	})
	fotos := w.id("casa", "Fotos")
	w.decide("casa", "Velho", "discard")
	w.decide("casa", "solto.bin", "discard")
	velho := staleQuarantineVelho(w)
	solto := w.id("casa", ".precious-quarantine/1/2/solto.bin")
	serra := w.id("casa", ".precious-quarantine/1/1/Velho/serra.jpg")

	// A restore of one item of the set.
	first, c := w.checkPurge(velho, solto)
	if c.State != "ready" || c.Items != 2 || c.Counts.Verdict["unique"] != (amount{Files: 2, Bytes: 500}) {
		t.Fatalf("first check %+v", c)
	}
	r := w.plan("plan-restore", `{"entry_ids":`+ids(solto)+`}`)
	wantItems(t, r.Items,
		"rename planned .precious-quarantine/1/2/solto.bin -> solto.bin",
		"unlink planned .precious-quarantine/1/2.json",
		"rmdir planned .precious-quarantine/1/2")
	if a := w.run(r.Action.ID); a.State != "done" {
		t.Fatalf("restore %+v", a)
	}
	if path, state := w.pathOf(solto); path != "solto.bin" || state != "present" {
		t.Fatalf("solto.bin %q %s", path, state)
	}
	staleCheckState(w, first, "stale", "index_changed", false)
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, first))

	// A move of one of its files out of the quarantine.
	second, c := w.checkPurge(velho)
	if c.State != "ready" || c.Items != 1 || c.Counts.Verdict["unique"] != (amount{Files: 1, Bytes: 200}) ||
		c.Counts.Verdict["safe"] != (amount{Files: 1, Bytes: 100}) {
		t.Fatalf("second check %+v", c)
	}
	m := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, serra, fotos))
	wantItems(t, m.Items, "rename planned .precious-quarantine/1/1/Velho/serra.jpg -> Fotos/serra.jpg")
	if a := w.run(m.Action.ID); a.State != "done" {
		t.Fatalf("move %+v", a)
	}
	if path, state := w.pathOf(serra); path != "Fotos/serra.jpg" || state != "present" {
		t.Fatalf("serra.jpg %q %s", path, state)
	}
	if !w.exists("casa", "Fotos/serra.jpg") || w.exists("casa", ".precious-quarantine/1/1/Velho/serra.jpg") {
		t.Fatalf("serra.jpg did not move on disk")
	}
	staleCheckState(w, second, "stale", "index_changed", false)
	staleCheckState(w, first, "stale", "index_changed", false)
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, second))
}

// R4.8 "A verified copy changes before the purge": the twin is modified on
// disk with no rescan, so the plan is drafted on an unchanged index, and the
// purge stops at its verify step with nothing deleted.
func TestR4_8AVerifiedCopyChangesBeforeThePurge(t *testing.T) {
	w := newWorld(t)
	var twin *synthfs.Node
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		twin = root.Dir("Fotos").File("praia.jpg", 100, staleAt).Seed(1)
		root.Dir("Velho").File("praia.jpg", 100, staleAt).Seed(1)
	})
	twinID := w.id("casa", "Fotos/praia.jpg")
	w.decide("casa", "Velho", "discard")
	velho := staleQuarantineVelho(w)
	praia := w.id("casa", ".precious-quarantine/1/1/Velho/praia.jpg")
	check, c := w.checkPurge(velho)
	if c.State != "ready" || !c.Allowed {
		t.Fatalf("check %+v", c)
	}
	staleSafeTwin(w, check)
	twinRow := restoreRow(w, twinID)

	// The twin's content changes in place, same size; nothing is rescanned.
	twin.Seed(99)
	pp := w.plan("plan-purge", fmt.Sprintf(`{"check_id":%q}`, check))
	wantItems(t, pp.Items,
		"verify planned",
		"purge planned .precious-quarantine/1/1/Velho",
		"rmdir planned .precious-quarantine/1")
	a := w.run(pp.Action.ID)
	if a.State != "stopped" || a.DeletedFiles != 0 || a.DeletedBytes != 0 || a.FreedBytes != 0 ||
		a.CheckID == nil || *a.CheckID != check || a.Counts["changed"] != 1 || a.Counts["not_attempted"] != 2 ||
		a.Counts["done"] != 0 {
		t.Fatalf("purge %+v", a)
	}
	wantItems(t, w.items(pp.Action.ID, ""),
		"verify changed copy_changed",
		"purge not_attempted .precious-quarantine/1/1/Velho",
		"rmdir not_attempted .precious-quarantine/1")
	staleCheckState(w, check, "stale", "disk_changed", false)

	if !w.exists("casa", ".precious-quarantine/1/1/Velho/praia.jpg") || !w.exists("casa", ".precious-quarantine/1/1.json") ||
		!w.exists("casa", "Fotos/praia.jpg") {
		t.Fatalf("a file is gone from the disk")
	}
	for id, want := range map[string]string{velho: ".precious-quarantine/1/1/Velho", praia: ".precious-quarantine/1/1/Velho/praia.jpg"} {
		if path, state := w.pathOf(id); path != want || state != "present" {
			t.Errorf("entry %s: %q %s, want %q present", id, path, state, want)
		}
	}
	if got := restoreRow(w, twinID); got != twinRow {
		t.Errorf("the twin's index row changed: %s, want %s", got, twinRow)
	}
	if q := w.quarantined("casa"); len(q.Items) != 1 || q.Items[0].Entry.ID != velho {
		t.Errorf("quarantine: %+v", q)
	}
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, check))
}

// G10: once every item of a check was deleted for good, the check keeps
// its records but is not allowed: plan-purge says the set is gone (409
// check_stale) rather than offering it again, and check-purge of the check
// finds nothing left (404 not_found).
func TestR4_8AFullyPurgedCheckIsNotAllowed(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Fotos").File("praia.jpg", 100, staleAt).Seed(1)
		root.Dir("Velho").File("praia.jpg", 100, staleAt).Seed(1)
	})
	w.decide("casa", "Velho", "discard")
	velho := staleQuarantineVelho(w)
	check, c := w.checkPurge(velho)
	if c.State != "ready" || !c.Allowed || c.Items != 1 {
		t.Fatalf("check %+v", c)
	}
	body := fmt.Sprintf(`{"check_id":%q}`, check)
	if a := w.run(w.plan("plan-purge", body).Action.ID); a.State != "done" || a.DeletedFiles != 1 {
		t.Fatalf("purge %+v", a)
	}
	if c := staleCheckState(w, check, "ready", "", false); c.Items != 0 {
		t.Fatalf("check after the purge %+v; want no item", c)
	}
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", body)
	w.refuse(http.StatusNotFound, "not_found", "check-purge", body)
}

// G11: check-purge {"check_id"} checks again what is left of that check's
// own set, whichever newer check also holds its items: an item restored
// since is left out.
func TestR4_8CheckAgainAnOlderCheck(t *testing.T) {
	w := newWorld(t)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Fotos").File("praia.jpg", 100, staleAt).Seed(1)
		root.Dir("Velho").File("praia.jpg", 100, staleAt).Seed(1)
		root.File("solto.bin", 300, staleAt).Seed(5)
		root.File("outro.bin", 200, staleAt).Seed(6)
	})
	w.decide("casa", "Velho", "discard")
	w.decide("casa", "solto.bin", "discard")
	w.decide("casa", "outro.bin", "discard")
	velho := staleQuarantineVelho(w)
	q := w.quarantined("casa")
	if len(q.Items) != 3 {
		t.Fatalf("quarantine %+v", q)
	}
	var solto, outro string
	for _, it := range q.Items {
		switch it.Entry.ID {
		case w.id("casa", ".precious-quarantine/1/2/outro.bin"):
			outro = it.Entry.ID
		case w.id("casa", ".precious-quarantine/1/3/solto.bin"):
			solto = it.Entry.ID
		}
	}
	if solto == "" || outro == "" {
		t.Fatalf("quarantine %+v", q)
	}
	older, _ := w.checkPurge(velho, solto)
	newer, _ := w.checkPurge(velho, solto, outro) // now the newest check of every item
	r := w.plan("plan-restore", `{"entry_ids":`+ids(solto)+`}`)
	if a := w.run(r.Action.ID); a.State != "done" {
		t.Fatalf("restore %+v", a)
	}
	staleCheckState(w, older, "stale", "index_changed", false)

	var started checkStarted
	decode(t, w.ok(http.StatusAccepted, "check-purge", fmt.Sprintf(`{"check_id":%q}`, older)), &started)
	w.idle()
	if started.CheckID == older || started.CheckID == newer {
		t.Fatalf("check again started %+v", started)
	}
	if c := w.check(started.CheckID); c.State != "ready" || c.Items != 1 {
		t.Fatalf("check again %+v; want ready with Velho only", c)
	}
	var item string
	w.readTx(func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT entry_id FROM purge_check_items WHERE check_id = ?`, started.CheckID).Scan(&item)
	})
	if item != velho {
		t.Fatalf("check again holds entry %s, want Velho (%s)", item, velho)
	}
	w.refuse(http.StatusBadRequest, "invalid_request", "check-purge",
		fmt.Sprintf(`{"check_id":%q,"entry_ids":%s}`, older, ids(velho)))
	w.refuse(http.StatusNotFound, "not_found", "check-purge", `{"check_id":"99"}`)
}
