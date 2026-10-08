package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
)

var errCrash = errors.New("the process died here")

// crashOnce is a hook that fails its first call only.
func crashOnce() func(int64) error {
	crashed := false
	return func(int64) error {
		if crashed {
			return nil
		}
		crashed = true
		return errCrash
	}
}

// photos is a source with Fotos/2004/{a,b}.jpg and an empty Arquivo.
func photos(e *env) (root *synthfs.Node) {
	mtime := time.Date(2004, 7, 1, 9, 0, 0, 0, time.UTC)
	return e.disk("/src", posix, func(r *synthfs.Node) {
		y := r.Dir("Fotos").Dir("2004")
		y.File("a.jpg", 100, mtime)
		y.File("b.jpg", 200, mtime)
		r.Dir("Arquivo")
	})
}

// R3.2 (spec source-writes, "Crash after the intent, before the rename"):
// the process stops after an intent commits and before the rename is
// called; a new executor over the same database, running the job the
// runner requeued, renames the entry exactly once and records it done.
func TestR3_2CrashAfterIntentRenamesOnce(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.hooks = Hooks{BeforeStep: crashOnce()}
	e.executor()
	action := e.action("move", false, step{Op: opRename, From: "Fotos/2004", To: "Arquivo", Name: "2004"})
	id := e.id("Fotos/2004")
	job := e.jobOf(action)
	if err := e.attempt(job); !errors.Is(err, errCrash) {
		t.Fatalf("first attempt: %v, want the simulated crash", err)
	}
	e.wantStates(action, actionRunning, stateIntent)
	if n := e.rec.Count(instrument.OpRename); n != 0 {
		t.Fatalf("%d renames before the crash, want 0", n)
	}
	active, err := OrganizeActive(context.Background(), e.st.Reader(), srcID)
	if err != nil || !active {
		t.Fatalf("OrganizeActive = %v, %v, want true while an intent is recorded", active, err)
	}

	// A new process: the runner requeues the job it finds running
	// (recoverOrphans), Startup sweeps and enqueues nothing more, and the job
	// runs again.
	e.hooks = Hooks{}
	e.executor()
	e.exec(`UPDATE jobs SET state = 'queued' WHERE id = ?`, int64(job))
	if err := e.ex.Startup(context.Background(), e.r); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'organize'`); n != 1 {
		t.Fatalf("%d organize jobs after Startup, want the requeued one only", n)
	}
	e.run(action)

	e.wantStates(action, actionDone, stateDone)
	if n := e.rec.Count(instrument.OpRename); n != 1 {
		t.Errorf("%d renames recorded, want exactly 1", n)
	}
	if _, ok := e.lstat("/src", "Arquivo/2004/a.jpg"); !ok {
		t.Error("Arquivo/2004/a.jpg is not there")
	}
	if got := e.id("Arquivo/2004"); got != id {
		t.Errorf("the moved folder has ID %d, want %d", got, id)
	}
	if renames, _, _, done := e.idx.counts(); renames != 1 || done != 1 {
		t.Errorf("index: %d renames applied, %d ActionDone, want 1 and 1", renames, done)
	}
}

// R3.2 (spec source-writes, "Crash after the rename, before recording it"):
// no second rename is attempted, the item is done, and the index update is
// applied once, by a reconcile job that Startup enqueues for the source.
func TestR3_2CrashAfterStepBeforeOutcome(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.hooks = Hooks{AfterStep: crashOnce()}
	e.executor()
	action := e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	job := e.jobOf(action)
	if err := e.attempt(job); !errors.Is(err, errCrash) {
		t.Fatalf("first attempt: %v, want the simulated crash", err)
	}
	e.wantStates(action, actionRunning, stateIntent, statePlanned)
	if n := e.rec.Count(instrument.OpRename); n != 1 {
		t.Fatalf("%d renames before the crash, want 1", n)
	}

	// A new process whose runner gave the job up (its attempts were used
	// up): Startup stops the action and enqueues a reconcile job.
	e.hooks = Hooks{}
	e.executor()
	e.exec(`UPDATE jobs SET state = 'failed', terminal_code = 'attempts_exhausted' WHERE id = ?`, int64(job))
	if err := e.ex.Startup(context.Background(), e.r); err != nil {
		t.Fatal(err)
	}
	e.wantStates(action, actionStopped, stateIntent, stateNotAttempted)
	var reconcile int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM jobs WHERE kind = 'organize' AND state = 'queued'
		AND scope_key = 'organize-reconcile:disk' AND payload = '{}'`).Scan(&reconcile); err != nil {
		t.Fatalf("Startup enqueued no reconcile job: %v", err)
	}
	if err := e.attempt(domain.JobID(reconcile)); err != nil {
		t.Fatal(err)
	}

	e.wantStates(action, actionStopped, stateDone, stateNotAttempted)
	if n := e.rec.Count(instrument.OpRename); n != 1 {
		t.Errorf("%d renames recorded, want 1: no second rename", n)
	}
	if renames, _, _, done := e.idx.counts(); renames != 1 || done != 1 {
		t.Errorf("index: %d renames applied, %d ActionDone, want 1 and 1", renames, done)
	}
	e.id("Arquivo/a.jpg")
	if active, err := OrganizeActive(context.Background(), e.st.Reader(), srcID); err != nil || active {
		t.Errorf("OrganizeActive = %v, %v after reconciling, want false", active, err)
	}
}

// R3.2: the folders a step changed are synced before its outcome is
// recorded: both folders of a move, the parent of a new folder.
func TestR3_2SyncsBeforeOutcome(t *testing.T) {
	e := newEnv(t)
	photos(e)
	var synced [][]string
	e.idx.onApply = func() {
		var paths []string
		for _, c := range e.rec.Calls() {
			if c.Op == instrument.OpSync {
				paths = append(paths, c.FullPath())
			}
		}
		synced = append(synced, paths)
	}
	action := e.action("merge", false,
		step{Op: opMkdir, To: "Arquivo", Name: "2004"},
		step{Op: opRename, From: "Fotos/2004/a.jpg", ToSeq: 1, Name: "a.jpg"})
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone)
	if len(synced) != 2 {
		t.Fatalf("%d index updates, want 2", len(synced))
	}
	if got := synced[0]; len(got) != 1 || got[0] != "/src/Arquivo" {
		t.Errorf("synced before the mkdir's outcome: %q, want [/src/Arquivo]", got)
	}
	if got := synced[1][1:]; len(got) != 2 || got[0] != "/src/Fotos/2004" || got[1] != "/src/Arquivo/2004" {
		t.Errorf("synced before the rename's outcome: %q, want the old and the new folder", got)
	}
	if mk := e.item(action, 1); !mk.Created || mk.Entry == 0 || mk.Entry != e.id("Arquivo/2004") {
		t.Errorf("mkdir item %+v: want created, with the new folder's entry", mk)
	}
	if _, ok := e.lstat("/src", "Arquivo/2004/a.jpg"); !ok {
		t.Error("a.jpg was not moved into the new folder")
	}
}

// R3.2 (spec source-writes, "Ambiguous outcome"): at reconciliation, both
// names holding something, or neither, ends the item manual_recovery with
// the findings; nothing is renamed, and the action's other items are not
// attempted.
func TestR3_2AmbiguousNeedsCheck(t *testing.T) {
	cases := []struct {
		name  string
		after func(e *env, root *synthfs.Node)
		want  string
	}{
		{"both names", func(e *env, root *synthfs.Node) {
			root.Child("Arquivo").File("a.jpg", 7, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
		}, `{"from":"same","to":"other"}`},
		{"neither name", func(e *env, root *synthfs.Node) {
			root.Child("Fotos").Child("2004").Remove("a.jpg")
		}, `{"from":"absent","to":"absent"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			root := photos(e)
			e.hooks = Hooks{BeforeStep: crashOnce()}
			e.executor()
			action := e.action("move", true,
				step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
				step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
			job := e.jobOf(action)
			if err := e.attempt(job); !errors.Is(err, errCrash) {
				t.Fatalf("first attempt: %v, want the simulated crash", err)
			}
			c.after(e, root)
			e.hooks = Hooks{}
			e.executor()
			e.run(action)

			e.wantStates(action, actionStopped, stateManualRecovery, stateNotAttempted)
			if got := e.item(action, 1).Detail; got != c.want {
				t.Errorf("findings %s, want %s", got, c.want)
			}
			if n := e.writes(); n != 0 {
				t.Errorf("%d writes, want none", n)
			}
			if renames, _, _, _ := e.idx.counts(); renames != 0 {
				t.Errorf("%d index renames, want none", renames)
			}
			// Nothing else runs on the source until the item is resolved.
			next := e.action("move", false, step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
			e.run(next)
			e.wantStates(next, actionStopped, stateNotAttempted)
			if n := e.writes(); n != 0 {
				t.Errorf("%d writes after the recovery item, want none", n)
			}
		})
	}
}

// R3.2: a failing index update ends the item manual_recovery (never
// intent), and the action stops.
func TestR3_2IndexFailureNeedsCheck(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.idx.fail = errors.New("index broke")
	action := e.action("move", true,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	e.run(action)
	e.wantStates(action, actionStopped, stateManualRecovery, stateNotAttempted)
	if got := e.item(action, 1).Detail; got != `{"from":"absent","to":"same"}` {
		t.Errorf("findings %s", got)
	}
	if _, ok := e.lstat("/src", "Arquivo/a.jpg"); !ok {
		t.Error("a.jpg was not moved")
	}
	// The index still has a.jpg where it was: the outcome rolled back.
	e.id("Fotos/2004/a.jpg")
	if n := e.rec.Count(instrument.OpRename); n != 1 {
		t.Errorf("%d renames, want 1", n)
	}
}

// R3.2 and D10: a running scan of the source delays reconciliation; the
// job defers without looking at the disk, and reconciles once the scan
// ended.
func TestR3_2RunningScanDelaysReconciliation(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.hooks = Hooks{AfterStep: crashOnce()}
	e.executor()
	action := e.action("move", false, step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"})
	job := e.jobOf(action)
	if err := e.attempt(job); !errors.Is(err, errCrash) {
		t.Fatalf("first attempt: %v, want the simulated crash", err)
	}
	e.hooks = Hooks{}
	e.executor()
	e.exec(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts, max_attempts, available_at,
		created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)`, string(srcID))
	e.rec.Reset()
	err := e.attempt(job)
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != deferScan || !d.Until.Equal(testNow.Add(time.Second)) {
		t.Fatalf("attempt during a scan: %v, want a 1 s deferral for the scan", err)
	}
	if n := len(e.rec.Calls()); n != 0 {
		t.Errorf("%d filesystem calls while the scan runs, want none", n)
	}
	e.wantStates(action, actionRunning, stateIntent)

	e.exec(`UPDATE jobs SET state = 'succeeded' WHERE kind = 'scan' AND state = 'running'`)
	e.run(action)
	e.wantStates(action, actionDone, stateDone)
	if n := e.rec.Count(instrument.OpRename); n != 0 {
		t.Errorf("%d renames when reconciling, want 0", n)
	}
	if info, ok := e.lstat("/src", "Arquivo/a.jpg"); !ok || info.Size != 100 {
		t.Errorf("Arquivo/a.jpg: %+v, %v", info, ok)
	}
}
