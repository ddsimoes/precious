package executor

import (
	"context"
	"testing"

	"precious/internal/domain"
	"precious/internal/jobs"
)

// mixed is an action with every kind of step: a move, a new folder, and the
// removal of an empty folder.
func mixed(e *env) int64 {
	return e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opMkdir, To: "Arquivo", Name: "Novo"},
		step{Op: opRmdir, From: "Vazio"})
}

// wantUntouched fails unless nothing was written and the entries keep their
// paths.
func wantUntouched(t *testing.T, e *env) {
	t.Helper()
	if n := e.writes(); n != 0 {
		t.Errorf("%d renames, mkdirs, or rmdirs recorded, want none", n)
	}
	e.id("Fotos/2004/a.jpg")
	e.id("Vazio")
	if _, ok := e.lstat("/src", "Fotos/2004/a.jpg"); !ok {
		t.Error("a.jpg moved")
	}
	if renames, mkdirs, rmdirs, _ := e.idx.counts(); renames+mkdirs+rmdirs != 0 {
		t.Error("the index was updated")
	}
}

// R3.7 (spec source-writes, "No write happens without permission"): writes
// turned off after the action was queued and before its first item: no
// write reaches the filesystem, the item ends not_permitted, and the rest
// are not attempted.
func TestR3_7WritesTurnedOffAfterQueueing(t *testing.T) {
	e := newEnv(t)
	photos(e).Dir("Vazio")
	e.scan()
	action := mixed(e)
	e.exec(`UPDATE sources SET write_enabled = 0 WHERE id = ?`, string(srcID))
	e.run(action)
	e.wantStates(action, actionStopped, stateNotPermitted, stateNotAttempted, stateNotAttempted)
	wantUntouched(t, e)
}

// R3.7 (spec source-writes, "Writes forbidden by the configuration"): the
// same with [sources] allow_writes = false.
func TestR3_7AllowWritesFalse(t *testing.T) {
	e := newEnv(t)
	photos(e).Dir("Vazio")
	e.scan()
	e.allowWrites = false
	e.executor()
	action := mixed(e)
	e.run(action)
	e.wantStates(action, actionStopped, stateNotPermitted, stateNotAttempted, stateNotAttempted)
	wantUntouched(t, e)
}

// R3.7 and D9 (spec organizing, "Cancelling a queued move"): a queued
// action cancelled while it waits behind a scan is stopped and never runs.
// One whose job alone was cancelled (cancel-job) is swept to stopped by the
// next organize attempt of its source, and never runs either.
func TestR3_7CancelledQueuedActionNeverRuns(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	photos(e).Dir("Vazio")
	e.scan()

	// Waiting behind a scan: the first attempt defers.
	e.exec(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts, max_attempts, available_at,
		created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)`, string(srcID))
	action := mixed(e)
	job := e.jobOf(action)
	if err := e.attempt(job); err == nil {
		t.Fatal("the action ran during a scan")
	}
	e.wantStates(action, actionQueued, statePlanned, statePlanned, statePlanned)
	if err := e.r.Write(ctx, func(tx *jobs.Tx) error { return CancelAction(ctx, tx, action) }); err != nil {
		t.Fatal(err)
	}
	e.wantStates(action, actionStopped, stateNotAttempted, stateNotAttempted, stateNotAttempted)
	if rec, err := e.r.Get(ctx, job); err != nil || rec.State != domain.JobCancelled {
		t.Fatalf("job %+v, %v: want cancelled", rec, err)
	}
	e.exec(`UPDATE jobs SET state = 'succeeded' WHERE kind = 'scan' AND state = 'running'`)
	if err := e.r.Write(ctx, func(tx *jobs.Tx) error { return CancelAction(ctx, tx, action) }); domain.CodeOf(err) != domain.CodeActionNotRunnable {
		t.Errorf("cancelling a stopped action: %v, want action_not_runnable", err)
	}

	// cancel-job on a queued organize job: the action is still queued
	// until a sweep.
	second := mixed(e)
	if _, err := e.r.Cancel(ctx, e.jobOf(second)); err != nil {
		t.Fatal(err)
	}
	e.wantStates(second, actionQueued, statePlanned, statePlanned, statePlanned)
	third := e.action("rename", false, step{Op: opRename, From: "Arquivo", To: "", Name: "Arquivo antigo"})
	e.run(third)
	e.wantStates(second, actionStopped, stateNotAttempted, stateNotAttempted, stateNotAttempted)
	e.wantStates(third, actionDone, stateDone)
	if n := e.writes(); n != 1 {
		t.Errorf("%d writes, want the third action's rename only", n)
	}
	if _, ok := e.lstat("/src", "Fotos/2004/a.jpg"); !ok {
		t.Error("a.jpg moved")
	}
}
