package organize

import (
	"fmt"
	"net/http"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

// The history lists the actions that ran, newest first, by pages; an
// action reads in any state; items filter by state; run-action and
// cancel-action refuse what they cannot do, and each write an audit event.
func TestHistory(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		a := root.Dir("A")
		a.File("1.txt", 1, mtime)
		a.File("2.txt", 2, mtime)
		root.Dir("B")
	})
	id := func(p string) string { return w.id("disk", p) }
	move, _ := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, id("A/1.txt"), id("B")))
	w.run(move.ID)
	rename, _ := w.plan("plan-rename", fmt.Sprintf(`{"entry_id":%q,"name":"dois.txt"}`, id("A/2.txt")))
	w.run(rename.ID)
	planned, _ := w.plan("plan-create-folder", fmt.Sprintf(`{"parent_id":%q,"name":"C"}`, id("")))

	var p page[actionJSON]
	w.get("/api/history?limit=1", &p)
	if len(p.Items) != 1 || p.Items[0].ID != rename.ID || p.NextCursor == nil {
		t.Fatalf("first page %+v", p)
	}
	w.get("/api/history?limit=1&cursor="+*p.NextCursor, &p)
	if len(p.Items) != 1 || p.Items[0].ID != move.ID || p.NextCursor != nil {
		t.Fatalf("second page %+v", p)
	}
	got := p.Items[0]
	if got.State != "done" || got.JobID == nil || got.StartedAt == nil || got.FinishedAt == nil ||
		got.Destination == nil || got.Destination.Path != "B" || len(got.Counts) != len(itemStates) {
		t.Errorf("done move %+v", got)
	}
	if a := w.action(planned.ID); a.State != "planned" || a.Undo.Possible || *a.Undo.Reason != undoNotDone {
		t.Errorf("planned action %+v", a)
	}
	for path, want := range map[string]int{
		"/api/history?source=nope": http.StatusNotFound, "/api/history?bogus=1": http.StatusBadRequest,
		"/api/history?limit=0": http.StatusBadRequest, "/api/history?cursor=x": http.StatusBadRequest,
		"/api/history/abc": http.StatusNotFound, "/api/history/999999": http.StatusNotFound,
		"/api/history/999999/items": http.StatusNotFound, "/api/history/" + move.ID + "/items?state=bogus": http.StatusBadRequest,
		"/api/history/" + move.ID + "/items?cursor=0": http.StatusBadRequest,
	} {
		if code, body := w.getRaw(path); code != want {
			t.Errorf("GET %s = %d %s, want %d", path, code, body, want)
		}
	}
	if items := w.items(move.ID, "state=done&state=conflict"); len(items) != 1 || items[0].Entry == nil ||
		items[0].Entry.Path != "B/1.txt" || items[0].From.Path != "A/1.txt" {
		t.Errorf("done items %v", summaries(items))
	}
	if items := w.items(move.ID, "state=refused"); len(items) != 0 {
		t.Errorf("refused items %v", summaries(items))
	}

	run := fmt.Sprintf(`{"action_id":%q}`, move.ID)
	w.refuse(http.StatusConflict, domain.CodeActionNotRunnable, "run-action", run)
	w.refuse(http.StatusConflict, domain.CodeActionNotRunnable, "cancel-action", run)
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "run-action", `{"action_id":"999999"}`)
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "cancel-action", `{"action_id":"x"}`)
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "run-action", `{}`)
	if n := w.count(`SELECT count(*) FROM audit_events WHERE kind = ?`, AuditActionRun); n != 2 {
		t.Errorf("%d action_run events, want 2", n)
	}
}

// An item that needs the owner's check reads what was found at each name;
// it blocks planning on its source until resolve-recovery, which marks it
// resolved and scans the source.
func TestResolveRecovery(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.Dir("A").File("1.txt", 1, mtime)
		root.Dir("B")
	})
	w.exec(`INSERT INTO actions (id, kind, source_id, state, bulk, created_at, finished_at)
		VALUES (50, 'move', 'disk', 'stopped', 0, 0, 0)`)
	w.exec(`INSERT INTO action_items (id, action_id, seq, op, from_path, to_path, state, detail)
		VALUES (500, 50, 1, 'rename', 'A/x.txt', 'B/x.txt', 'manual_recovery', '{"from":"absent","to":"other"}')`)
	items := w.items("50", "state=manual_recovery")
	if len(items) != 1 || items[0].Found == nil || items[0].Found.From != "absent" || items[0].Found.To != "other" ||
		items[0].Detail != nil {
		t.Fatalf("recovery items %+v", items)
	}
	move := fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, w.id("disk", "A/1.txt"), w.id("disk", "B"))
	w.refuse(http.StatusConflict, domain.CodeRecoveryNeeded, "plan-move", move)

	var res resolveResponse
	decode(t, w.ok(http.StatusOK, "resolve-recovery", `{"item_id":"500"}`), &res)
	if res.Action.ID != "50" || res.Action.Counts["resolved"] != 1 || res.Scan.JobID == "" {
		t.Fatalf("resolve-recovery answered %+v", res)
	}
	if n := w.count(`SELECT count(*) FROM jobs WHERE id = ? AND kind = 'scan' AND source_id = 'disk'`, res.Scan.JobID); n != 1 {
		t.Error("no scan of the source was started")
	}
	if n := w.count(`SELECT count(*) FROM audit_events WHERE kind = ?`, AuditRecoveryResolved); n != 1 {
		t.Errorf("%d recovery_resolved events", n)
	}
	w.idle()
	w.refuse(http.StatusConflict, domain.CodeInvalidEntryState, "resolve-recovery", `{"item_id":"500"}`)
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "resolve-recovery", `{"item_id":"999"}`)
	w.plan("plan-move", move)
}
