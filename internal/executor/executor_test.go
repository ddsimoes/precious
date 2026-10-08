package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"syscall"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
)

// Register runs an action through a started runner: the job is claimed,
// the action ends done, and the job reports its progress.
func TestRegisterRunsAnAction(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	photos(e)
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: e.src, Logger: discard(),
		TickInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	e.r = r
	e.ex = New(Options{Store: e.st, Sources: e.src, Index: e.idx, AllowWrites: true, Logger: discard()})
	e.ex.Register(r)
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop(ctx) })
	if err := e.ex.Startup(ctx, r); err != nil {
		t.Fatal(err)
	}
	action := e.action("move", false, step{Op: opRename, From: "Fotos/2004", To: "Arquivo", Name: "2004"})
	job := e.jobOf(action)
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec, err := r.Get(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		if rec.State.Terminal() {
			if rec.State != domain.JobSucceeded {
				t.Fatalf("job ended %s: %s", rec.State, rec.TerminalDetail)
			}
			if rec.Progress["items"] != 1 || rec.Progress["done"] != 1 {
				t.Errorf("progress %v, want items 1, done 1", rec.Progress)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s", rec.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.wantStates(action, actionDone, stateDone)
}

// Enqueue binds one job to one action: scope organize:<id>, payload
// {"action_id":"<id>"}, recorded in actions.job_id; a second job for the
// same action is refused.
func TestEnqueue(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	photos(e)
	action := e.action("move", false, step{Op: opRename, From: "Fotos/2004", To: "Arquivo", Name: "2004"})
	rec, err := e.r.Get(ctx, e.jobOf(action))
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(action, 10)
	if rec.Kind != KindOrganize || rec.SourceID != srcID || rec.ScopeKey != "organize:"+id ||
		string(rec.Payload) != `{"action_id":"`+id+`"}` {
		t.Errorf("job %+v", rec)
	}
	if err := e.r.Write(ctx, func(tx *jobs.Tx) error {
		_, err := Enqueue(tx, srcID, action)
		return err
	}); err == nil {
		t.Error("a second job for the same action was enqueued")
	}
}

// OrganizeActive is true while an organize job of the source is queued or
// running, or one of its items is intent.
func TestOrganizeActive(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	photos(e)
	active := func() bool {
		t.Helper()
		ok, err := OrganizeActive(ctx, e.st.Reader(), srcID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if active() {
		t.Fatal("active with no action")
	}
	action := e.action("move", false, step{Op: opRename, From: "Fotos/2004", To: "Arquivo", Name: "2004"})
	if !active() {
		t.Error("not active with a queued job")
	}
	e.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, int64(e.jobOf(action)))
	if active() {
		t.Error("active with no queued or running job")
	}
	e.exec(`UPDATE action_items SET state = 'intent' WHERE action_id = ?`, action)
	if !active() {
		t.Error("not active with an intent item")
	}
}

// An undo removes the folder its original created and moves the entry back,
// setting each original's reversed_by; an undo item whose original was
// reversed meanwhile ends changed already_undone.
func TestUndoSetsReversedBy(t *testing.T) {
	e := newEnv(t)
	photos(e)
	orig := e.action("merge", false,
		step{Op: opMkdir, To: "Arquivo", Name: "2004"},
		step{Op: opRename, From: "Fotos/2004/a.jpg", ToSeq: 1, Name: "a.jpg"})
	e.run(orig)
	e.wantStates(orig, actionDone, stateDone, stateDone)
	mk, mv := e.item(orig, 1), e.item(orig, 2)
	id := e.id("Arquivo/2004/a.jpg")

	undo := e.action("undo", false,
		step{Op: opRename, From: "Arquivo/2004/a.jpg", To: "Fotos/2004", Name: "a.jpg", Reverses: mv.ID},
		step{Op: opRmdir, From: "Arquivo/2004", Reverses: mk.ID})
	again := e.action("undo", false,
		step{Op: opRename, From: "Arquivo/2004/a.jpg", To: "Fotos/2004", Name: "a.jpg", Reverses: mv.ID})
	e.run(undo)
	e.wantStates(undo, actionDone, stateDone, stateDone)
	if got := e.item(orig, 2).ReversedBy; got != e.item(undo, 1).ID {
		t.Errorf("move reversed_by %d, want %d", got, e.item(undo, 1).ID)
	}
	if got := e.item(orig, 1).ReversedBy; got != e.item(undo, 2).ID {
		t.Errorf("mkdir reversed_by %d, want %d", got, e.item(undo, 2).ID)
	}
	if got := e.id("Fotos/2004/a.jpg"); got != id {
		t.Errorf("a.jpg came back as %d, want %d", got, id)
	}
	if _, ok := e.lstat("/src", "Arquivo/2004"); ok {
		t.Error("Arquivo/2004 is still there")
	}
	e.run(again)
	e.wantStates(again, actionDone, stateChanged)
	if got := e.item(again, 1).Reason; got != reasonAlreadyUndone {
		t.Errorf("reason %q, want %q", got, reasonAlreadyUndone)
	}
	if _, mkdirs, rmdirs, _ := e.idx.counts(); mkdirs != 1 || rmdirs != 1 {
		t.Errorf("index: %d mkdirs and %d rmdirs, want 1 and 1", mkdirs, rmdirs)
	}
}

// The step errors of D5: each first item meets one, then the action goes on
// to its second item or stops. EINVAL on a rename is a missing no-replace
// flag only on a filesystem whose drivers may lack it (zfs); elsewhere the
// filesystem refused the name (design V3), and writes stay on.
func TestStepErrors(t *testing.T) {
	intoItself := &fsaccess.Error{Op: "RenameNoReplace", Name: []byte("a.jpg"),
		Err: fmt.Errorf("%w (%w)", fsaccess.ErrIntoItself, syscall.EINVAL)}
	einval := fsaccess.WriteError("RenameNoReplace", nil, syscall.EINVAL)
	cases := []struct {
		name      string
		fsType    string
		op        instrument.Op
		err       error
		state     string
		reason    string
		detail    string
		stops     bool
		writesOff bool
	}{
		{"exists", "", instrument.OpRename, fsaccess.WriteError("RenameNoReplace", nil, syscall.EEXIST),
			stateConflict, reasonNameTaken, "", false, false},
		{"no replace flag", "zfs", instrument.OpRename, einval, stateNoSafeRename, "", "", true, true},
		{"name refused on ext4", "ext4", instrument.OpRename, einval, stateFailed, "", "invalid argument", false, false},
		{"name refused on exfat", "exfat", instrument.OpRename, einval, stateFailed, "", "invalid argument", false, false},
		{"into itself", "zfs", instrument.OpRename, intoItself, stateRefused, reasonIntoItself, "", false, false},
		{"cross device", "", instrument.OpRename, fsaccess.WriteError("RenameNoReplace", nil, syscall.EXDEV),
			stateRefused, reasonOtherFilesystem, "", false, false},
		{"read-only", "", instrument.OpRename, fsaccess.WriteError("RenameNoReplace", nil, syscall.EROFS),
			stateFailed, "", "read-only file system", true, false},
		{"permission", "", instrument.OpRename, fsaccess.WriteError("RenameNoReplace", nil, syscall.EACCES),
			stateFailed, "", "permission denied", false, false},
		{"gone", "", instrument.OpRename, fsaccess.WriteError("RenameNoReplace", nil, syscall.ENOENT),
			stateChanged, "", "", false, false},
		{"i/o error, not done", "", instrument.OpRename, fsaccess.WriteError("RenameNoReplace", nil, syscall.EIO),
			stateFailed, "", "input/output error", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.fsType = c.fsType
			photos(e)
			e.rec.InjectError(c.op, "/src/Fotos/2004/a.jpg", c.err)
			action := e.action("move", true,
				step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
				step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
			e.run(action)
			want := []string{c.state, stateDone}
			wantAction := actionDone
			if c.stops {
				want[1], wantAction = stateNotAttempted, actionStopped
			}
			e.wantStates(action, wantAction, want...)
			got := e.item(action, 1)
			if got.Reason != c.reason || got.Detail != c.detail {
				t.Errorf("first item reason %q detail %q, want %q and %q", got.Reason, got.Detail, c.reason, c.detail)
			}
			var enabled bool
			if err := e.st.Reader().QueryRow(`SELECT write_enabled FROM sources WHERE id = ?`, string(srcID)).
				Scan(&enabled); err != nil {
				t.Fatal(err)
			}
			if enabled == c.writesOff {
				t.Errorf("write_enabled = %v", enabled)
			}
			var audit int
			var detail string
			err := e.st.Reader().QueryRow(`SELECT count(*), coalesce(max(detail), '') FROM audit_events
				WHERE kind = 'source_writes_set' AND actor = 'system'`).Scan(&audit, &detail)
			if err != nil {
				t.Fatal(err)
			}
			if c.writesOff {
				var d map[string]any
				if err := json.Unmarshal([]byte(detail), &d); err != nil || audit != 1 || d["source_id"] != string(srcID) ||
					d["enabled"] != false || d["previous_enabled"] != true || d["reason"] != "no_replace_rename" {
					t.Errorf("%d audit events, detail %s", audit, detail)
				}
			} else if audit != 0 {
				t.Errorf("%d audit events, want none", audit)
			}
			if _, ok := e.lstat("/src", "Fotos/2004/a.jpg"); !ok {
				t.Error("a.jpg moved")
			}
		})
	}
}

// Folder steps' errors: a taken name for a new folder is a conflict, and a
// folder not empty on disk is not removed.
func TestFolderStepErrors(t *testing.T) {
	e := newEnv(t)
	root := photos(e)
	vazio := root.Dir("Vazio")
	e.scan()
	vazio.File("late.txt", 1, testNow)
	root.Child("Arquivo").Dir("Novo")
	action := e.action("merge", false,
		step{Op: opMkdir, To: "Arquivo", Name: "Novo"},
		step{Op: opRmdir, From: "Vazio"},
		step{Op: opMkdir, To: "Arquivo", Name: "Outro"})
	e.run(action)
	e.wantStates(action, actionDone, stateConflict, stateNotEmpty, stateDone)
	if got := e.item(action, 1).Reason; got != reasonNameTaken {
		t.Errorf("mkdir reason %q", got)
	}
	if _, ok := e.lstat("/src", "Vazio/late.txt"); !ok {
		t.Error("Vazio lost its file")
	}
}

// The re-checks at intent (design Concurrency) and at preflight: a missing
// row with owner intent at the destination, the entry changed on disk since
// the scan, a mount below a folder, a destination that became a mount, a
// bulk move losing a keep, and a destination gone.
func TestIntentRechecks(t *testing.T) {
	e := newEnv(t)
	root := photos(e)
	other := root.Dir("Outro disco")
	e.scan()
	// Mounted after the scan: the index does not know.
	other.Dev(77)
	root.Child("Fotos").Child("2004").Child("b.jpg").Size(999)
	e.exec(`UPDATE dir_stats SET mount_boundaries = 1 WHERE entry_id = ?`, e.id("Fotos"))
	e.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen, decision)
		VALUES (?, ?, CAST('a.jpg' AS BLOB), CAST('Arquivo/a.jpg' AS BLOB), 'file', 'missing', 0, 0, 0, 'keep')`,
		string(srcID), e.id("Arquivo"))
	e.exec(`UPDATE entries SET eff_decision = 'keep' WHERE id = ?`, e.id("Fotos/2004/b.jpg"))
	e.exec(`UPDATE entries SET eff_decision = 'discard' WHERE id = ?`, e.id("Outro disco"))

	bulk := e.action("move", true,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},     // missing with intent
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"},     // size changed
		step{Op: opRename, From: "Fotos", To: "Arquivo", Name: "Fotos"},                // a mount inside
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Outro disco", Name: "x.jpg"}, // other filesystem
		step{Op: opMkdir, To: "Fotos/2004", Name: "Novo"})
	e.run(bulk)
	e.wantStates(bulk, actionDone, stateConflict, stateChanged, stateRefused, stateRefused, stateDone)
	for seq, want := range map[int]string{1: reasonNameTakenByMissing, 3: reasonContainsMount, 4: reasonOtherFilesystem} {
		if got := e.item(bulk, seq).Reason; got != want {
			t.Errorf("item %d reason %q, want %q", seq, got, want)
		}
	}

	// b.jpg is kept (effectively): a bulk move into a discarded folder
	// would lose the keep; an individual one records the decision after.
	root.Child("Fotos").Child("2004").Child("b.jpg").Size(200)
	e.exec(`UPDATE entries SET mtime_ns = ? WHERE id = ?`, mtimeOf(t, e, "Fotos/2004/b.jpg"), e.id("Fotos/2004/b.jpg"))
	other.Dev(0)
	keepBulk := e.action("move", true, step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Outro disco", Name: "b.jpg"})
	e.run(keepBulk)
	e.wantStates(keepBulk, actionDone, stateChanged)
	if got := e.item(keepBulk, 1).Reason; got != reasonWouldLoseKeep {
		t.Errorf("reason %q, want %q", got, reasonWouldLoseKeep)
	}
	single := e.action("move", false, step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Outro disco", Name: "b.jpg"})
	e.run(single)
	e.wantStates(single, actionDone, stateDone)
	var after string
	if err := e.st.Reader().QueryRow(`SELECT decision_after FROM action_items WHERE action_id = ?`, single).
		Scan(&after); err != nil || after != "discard" {
		t.Errorf("decision_after %q, %v, want discard", after, err)
	}

	// A destination gone from the index ends the item changed.
	gone := e.action("move", false, step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "z.jpg"})
	e.exec(`UPDATE entries SET state = 'missing' WHERE id = ?`, e.id("Arquivo"))
	e.run(gone)
	e.wantStates(gone, actionDone, stateChanged)
}

// mtimeOf is the modification time of rel on disk.
func mtimeOf(t *testing.T, e *env, rel string) int64 {
	t.Helper()
	info, ok := e.lstat("/src", rel)
	if !ok {
		t.Fatalf("%s is not there", rel)
	}
	return info.ModTime.UnixNano()
}

// A cancel request during a step: the step finishes and is recorded, the
// rest are not attempted, and the action stops.
func TestCancelStopsAfterTheStepInFlight(t *testing.T) {
	e := newEnv(t)
	photos(e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var action int64
	e.hooks = Hooks{AfterStep: func(int64) error {
		e.exec(`UPDATE jobs SET cancel_requested = 1 WHERE id = ?`, int64(e.jobOf(action)))
		cancel()
		return nil
	}}
	e.executor()
	action = e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	err := e.attemptWith(ctx, e.jobOf(action), &fakeRuntime{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("attempt: %v, want context.Canceled", err)
	}
	e.wantStates(action, actionStopped, stateDone, stateNotAttempted)
	if _, _, _, done := e.idx.counts(); done != 1 {
		t.Errorf("%d ActionDone, want 1", done)
	}
}

// Shutdown between items (no cancel request) leaves the action running for
// the next attempt.
func TestShutdownKeepsTheActionRunning(t *testing.T) {
	e := newEnv(t)
	photos(e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.hooks = Hooks{AfterStep: func(int64) error { cancel(); return nil }}
	e.executor()
	action := e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	if err := e.attemptWith(ctx, e.jobOf(action), &fakeRuntime{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("attempt: %v", err)
	}
	e.wantStates(action, actionRunning, stateDone, statePlanned)
	e.hooks = Hooks{}
	e.executor()
	e.run(action)
	e.wantStates(action, actionDone, stateDone, stateDone)
}

// Cancelling a running action whose job waits to run again (it deferred
// mid-run) ends the job at once; a reconcile job's sweep stops the action.
func TestCancelDeferredRunningAction(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	photos(e)
	action := e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	e.exec(`UPDATE actions SET state = 'running' WHERE id = ?`, action)
	e.exec(`UPDATE action_items SET state = 'done' WHERE action_id = ? AND seq = 1`, action)
	if err := e.r.Write(ctx, func(tx *jobs.Tx) error { return CancelAction(ctx, tx, action) }); err != nil {
		t.Fatal(err)
	}
	var reconcile int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM jobs WHERE scope_key = 'organize-reconcile:disk'
		AND state = 'queued'`).Scan(&reconcile); err != nil {
		t.Fatalf("no reconcile job: %v", err)
	}
	e.wantStates(action, actionRunning, stateDone, statePlanned)
	if err := e.attempt(domain.JobID(reconcile)); err != nil {
		t.Fatal(err)
	}
	e.wantStates(action, actionStopped, stateDone, stateNotAttempted)
	if _, _, _, done := e.idx.counts(); done != 1 {
		t.Errorf("%d ActionDone, want 1 from the sweep", done)
	}
	if n := e.writes(); n != 0 {
		t.Errorf("%d writes", n)
	}
	if err := e.r.Write(ctx, func(tx *jobs.Tx) error { return CancelAction(ctx, tx, 999) }); domain.CodeOf(err) != domain.CodeNotFound {
		t.Errorf("cancelling an unknown action: %v", err)
	}
}

// Actions of a source run oldest first: a newer one defers while an older
// one is queued.
func TestOlderActionGoesFirst(t *testing.T) {
	e := newEnv(t)
	photos(e)
	older := e.action("move", false, step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"})
	newer := e.action("move", false, step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	err := e.attempt(e.jobOf(newer))
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != deferTurn {
		t.Fatalf("newer action: %v, want a deferral for its turn", err)
	}
	e.wantStates(newer, actionQueued, statePlanned)
	e.run(older)
	e.run(newer)
	e.wantStates(older, actionDone, stateDone)
	e.wantStates(newer, actionDone, stateDone)
}

// A source that went offline: the first item ends offline and the action
// stops, with nothing written.
func TestOfflineSourceStops(t *testing.T) {
	e := newEnv(t)
	photos(e)
	action := e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	e.sfs.Vanish("/src")
	e.run(action)
	e.wantStates(action, actionStopped, stateOffline, stateNotAttempted)
	if n := e.writes(); n != 0 {
		t.Errorf("%d writes", n)
	}
}

// On a source without stable identity, folders are reached by name and
// checked to be folders; a remount that renumbers inodes changes nothing.
func TestUnstableIdentity(t *testing.T) {
	e := newEnv(t)
	var root *synthfs.Node
	e.disk("/src", fat, func(r *synthfs.Node) {
		root = r
		r.Dir("Fotos").Dir("2004").File("a.jpg", 100, time.Date(2004, 7, 1, 9, 0, 0, 0, time.UTC))
		r.Dir("Arquivo")
	})
	e.sfs.Remount(root.Info().Dev)
	action := e.action("move", false, step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"})
	e.run(action)
	e.wantStates(action, actionDone, stateDone)
}

// D4 step 4: a sync error after a step leaves the item intent, stops the
// action, and enqueues a reconcile job, which records the step done once
// the folders sync.
func TestSyncFailureLeavesIntent(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.rec.InjectError(instrument.OpSync, "/src/Arquivo", fsaccess.WriteError("Sync", []byte("Arquivo"), syscall.EIO))
	action := e.action("move", false,
		step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"},
		step{Op: opRename, From: "Fotos/2004/b.jpg", To: "Arquivo", Name: "b.jpg"})
	e.run(action)
	e.wantStates(action, actionStopped, stateIntent, stateNotAttempted)
	if renames, _, _, _ := e.idx.counts(); renames != 0 {
		t.Fatalf("%d index renames before the folders synced", renames)
	}
	var reconcile int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM jobs WHERE scope_key = 'organize-reconcile:disk'
		AND state = 'queued'`).Scan(&reconcile); err != nil {
		t.Fatalf("no reconcile job: %v", err)
	}
	e.rec.ClearErrors()
	if err := e.attempt(domain.JobID(reconcile)); err != nil {
		t.Fatal(err)
	}
	e.wantStates(action, actionStopped, stateDone, stateNotAttempted)
	if n := e.rec.Count(instrument.OpRename); n != 1 {
		t.Errorf("%d renames, want 1", n)
	}
	if renames, _, _, done := e.idx.counts(); renames != 1 || done != 1 {
		t.Errorf("index: %d renames, %d ActionDone, want 1 and 1", renames, done)
	}
}

// A reconcile job of an offline source waits for it, a minute at a time.
func TestReconcileWaitsForAnOfflineSource(t *testing.T) {
	e := newEnv(t)
	photos(e)
	e.hooks = Hooks{BeforeStep: crashOnce()}
	e.executor()
	action := e.action("move", false, step{Op: opRename, From: "Fotos/2004/a.jpg", To: "Arquivo", Name: "a.jpg"})
	if err := e.attempt(e.jobOf(action)); !errors.Is(err, errCrash) {
		t.Fatalf("first attempt: %v", err)
	}
	e.hooks = Hooks{}
	e.executor()
	e.exec(`UPDATE jobs SET state = 'failed' WHERE id = ?`, int64(e.jobOf(action)))
	if err := e.ex.Startup(context.Background(), e.r); err != nil {
		t.Fatal(err)
	}
	var reconcile int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM jobs WHERE scope_key = 'organize-reconcile:disk'`).
		Scan(&reconcile); err != nil {
		t.Fatal(err)
	}
	e.sfs.Vanish("/src")
	err := e.attempt(domain.JobID(reconcile))
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != deferOffline || !d.Until.Equal(testNow.Add(time.Minute)) {
		t.Fatalf("attempt on an offline source: %v, want a one-minute deferral", err)
	}
	e.sfs.Reattach("/src")
	if err := e.attempt(domain.JobID(reconcile)); err != nil {
		t.Fatal(err)
	}
	e.wantStates(action, actionStopped, stateNotAttempted)
	if n := e.writes(); n != 0 {
		t.Errorf("%d writes", n)
	}
}
