package executor

import (
	"context"
	"testing"
	"time"

	"precious/internal/fsaccess/instrument"
	"precious/internal/jobs"
)

// r5 task 1.6 (as r4 F6): until task 2.8 adds its step, a set_mtime item
// ends failed ("unknown step set_mtime") at intent, with nothing asked of
// the disk or the index, and the action goes on to its end. The item's
// new_mtime_ns and prev_mtime_ns are read with the other columns.
func TestSetMtimeWithoutAStep(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.scan()
	newTime := time.Date(2004, 6, 30, 8, 0, 0, 0, time.UTC).UnixNano()
	var action int64
	if err := e.st.Writer().QueryRow(`INSERT INTO actions (kind, source_id, state, bulk, created_at)
		VALUES ('set_mtime', ?, 'queued', 1, 0) RETURNING id`, string(srcID)).Scan(&action); err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{"Fotos/2004/a.jpg", "Fotos/2004/b.jpg"} {
		e.exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name, from_path, new_mtime_ns,
			state) SELECT ?, ?, ?, id, parent_id, name, path, ?, 'planned' FROM entries WHERE source_id = ? AND path = ?`,
			action, i+1, opSetMtime, newTime+int64(i), string(srcID), []byte(p))
	}
	it, err := scanItem(e.st.Reader().QueryRow(`SELECT `+itemColumns+` FROM action_items i
		WHERE i.action_id = ? AND i.seq = 2`, action))
	if err != nil {
		t.Fatal(err)
	}
	if it.op != opSetMtime || !it.newMtime.Valid || it.newMtime.Int64 != newTime+1 || it.prevMtime.Valid {
		t.Errorf("item read as op %q, new %v, prev %v", it.op, it.newMtime, it.prevMtime)
	}
	if err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		_, err := Enqueue(tx, srcID, action)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.run(action)
	e.wantStates(action, actionDone, stateFailed, stateFailed)
	for seq := 1; seq <= 2; seq++ {
		if r := e.item(action, seq); r.Detail != "unknown step set_mtime" {
			t.Errorf("item %d detail %q, want unknown step set_mtime", seq, r.Detail)
		}
	}
	if n := e.writes() + e.rec.Count(instrument.OpSetModTime); n != 0 {
		t.Errorf("%d writes, want none", n)
	}
	e.idx.mu.Lock()
	applied := len(e.idx.modTimes)
	e.idx.mu.Unlock()
	if applied != 0 {
		t.Errorf("the index applied %d times", applied)
	}
}
