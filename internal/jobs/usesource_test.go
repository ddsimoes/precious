package jobs

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"runtime"
	"testing"
	"time"

	"precious/internal/domain"
)

// Task 1.4 (M4b slice J): Runtime.UseSource moves a running job to another
// source's device (design D13; spec job-runner "A job holds one device at a
// time"). Sources a and b are on devices 1 and 2, one worker each unless a
// test says otherwise.

const kindMove Kind = "move" // a reconciliation kind driven by a mover

// step is one piece of work a mover's attempt runs for the test.
type step func(ctx context.Context, rt Runtime) error

// mover is a handler whose attempt runs the steps the test sends, in order,
// and reports each result. It returns the first error, or nil once the test
// closes steps.
type mover struct {
	entered chan Job
	steps   chan step
	done    chan error
}

func newMover() *mover {
	return &mover{entered: make(chan Job, 2), steps: make(chan step), done: make(chan error, 1)}
}

func (m *mover) Run(ctx context.Context, job Job, rt Runtime) error {
	m.entered <- job
	for s := range m.steps {
		err := s(ctx, rt)
		m.done <- err
		if err != nil {
			return err
		}
	}
	return nil
}

// send hands s to the running attempt; its result arrives on m.done.
func (m *mover) send(t *testing.T, s step) {
	t.Helper()
	select {
	case m.steps <- s:
	case <-time.After(waitTimeout):
		t.Fatal("mover: attempt not taking steps")
	}
}

// run runs s in the attempt and returns its result.
func (m *mover) run(t *testing.T, s step) error {
	t.Helper()
	m.send(t, s)
	return recv(t, m.done, "step result")
}

// waiting fails the test when the attempt's current step has returned.
func (m *mover) waiting(t *testing.T, what string) {
	t.Helper()
	select {
	case err := <-m.done:
		t.Fatalf("%s: the move returned (%v), want it waiting", what, err)
	default:
	}
}

func use(source domain.SourceID) step {
	return func(ctx context.Context, rt Runtime) error { return rt.UseSource(ctx, source) }
}

// devices adds sources with their devices recorded.
func devices(t *testing.T, e *env, devs map[domain.SourceID]int64) {
	t.Helper()
	for src, dev := range devs {
		addSource(t, e.st, src)
		recordDevice(e.reg, src, dev)
	}
}

// deviceState is the runner's device-sharing state, copied under Runner.mu.
type deviceState struct {
	busy      map[string]int
	running   map[domain.SourceID]int
	exclusive map[domain.SourceID]bool
	yielded   map[string][]domain.JobID
	credits   map[string][len(classCredits)]int
}

func deviceStateOf(r *Runner) deviceState {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := deviceState{
		busy:      maps.Clone(r.slots.busy),
		running:   maps.Clone(r.slots.running),
		exclusive: maps.Clone(r.slots.exclusive),
		yielded:   map[string][]domain.JobID{},
		credits:   map[string][len(classCredits)]int{},
	}
	for key, list := range r.arb.yielded {
		for _, a := range list {
			s.yielded[key] = append(s.yielded[key], a.job.ID)
		}
	}
	for key, sh := range r.arb.shares {
		s.credits[key] = sh.credit
	}
	return s
}

// attemptState is what a running attempt holds, copied under Runner.mu.
type attemptState struct {
	slot                   slot
	yields, vacated, moved bool
	since                  int64
	granted                chan struct{}
}

func attemptStateOf(t *testing.T, r *Runner, id domain.JobID) attemptState {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.attempts[id]
	if a == nil {
		t.Fatalf("job %s has no running attempt", id)
	}
	return attemptState{slot: a.slot, yields: a.yields, vacated: a.vacated, moved: a.moved, since: a.since, granted: a.granted}
}

// stop stops r, waiting for every handler to return.
func stop(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// waitFinished waits until the runner has released job id's attempt, which
// finish does just after recording its outcome; no event marks it.
func waitFinished(t *testing.T, r *Runner, id domain.JobID) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		r.mu.Lock()
		_, running := r.attempts[id]
		r.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s's attempt not released", id)
		}
		runtime.Gosched()
	}
}

// Spec scenario "Two sources on one device": a move between two sources
// with the same device key returns at once, keeping the slot, recording
// no wait, and changing nothing in the arbiter; the watchdog then watches
// the source moved to.
func TestUseSourceTwoSourcesOnOneDevice(t *testing.T) {
	e := newEnv(t)
	devices(t, e, map[domain.SourceID]int64{"disk": 1, "photos": 1})
	m, g := newMover(), newGate()
	r := e.runner(t, map[Kind]Handler{kindMove: m})
	r.RegisterClass(kindInteractive, g, ClassInteractive)
	start(t, r)

	job := enqueue(t, r, Spec{Kind: kindMove, SourceID: "disk"})
	recv(t, m.entered, "copy search")
	intake := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "photos"})
	tick(t, r) // the arbiter has seen the interactive job wait for dev:1
	devBefore, attBefore := deviceStateOf(r), attemptStateOf(t, r, job.ID)
	if err := m.run(t, use("photos")); err != nil {
		t.Fatalf("UseSource(photos) = %v", err)
	}
	if got := deviceStateOf(r); !reflect.DeepEqual(got, devBefore) {
		t.Fatalf("device state after the move = %+v, want unchanged %+v", got, devBefore)
	}
	if got := attemptStateOf(t, r, job.ID); !reflect.DeepEqual(got, attBefore) {
		t.Fatalf("attempt after the move = %+v, want unchanged %+v", got, attBefore)
	}
	tick(t, r)
	if got := mustGet(t, r, intake.ID); got.State != domain.JobQueued {
		t.Fatalf("interactive job on dev:1 is %s after the move, want queued", got.State)
	}

	var done func()
	if err := m.run(t, func(_ context.Context, rt Runtime) error { done = rt.FSCall("readdir"); return nil }); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(e.cfg.CallWatchdog.Duration)
	tick(t, r)
	done()
	if calls := e.reg.unresponsiveCalls(); len(calls) != 2 || calls[0].Source != "photos" || calls[0].Since.IsZero() ||
		calls[1].Source != "photos" || !calls[1].Since.IsZero() {
		t.Fatalf("unresponsive calls = %+v, want photos flagged, then cleared", calls)
	}
	close(m.steps)
	if got := recv(t, g.entered, "interactive job"); got.ID != intake.ID {
		t.Fatalf("next job = %s, want %s", got.ID, intake.ID)
	}
	g.release(intake.ID)
	waitJob(t, r, job.ID, "succeeded", inState(domain.JobSucceeded))
}

// Design D13, a move to a busy device: the job on device 1 moves to device
// 2 while another job holds it. Its first device's waiting interactive job
// is granted the freed slot at once; the moved job waits, still running,
// until device 2's job finishes, then continues; and its next progress
// write records the new key in jobs.device_key.
func TestUseSourceMoveToBusyDevice(t *testing.T) {
	e := newEnv(t)
	devices(t, e, map[domain.SourceID]int64{"a": 1, "b": 2})
	m, g := newMover(), newGate()
	r := e.runner(t, map[Kind]Handler{kindMove: m, kindHold: g}, func(o *Options) { o.ProgressBatch = 1 })
	r.RegisterClass(kindInteractive, g, ClassInteractive)
	start(t, r)

	hold := enqueue(t, r, Spec{Kind: kindHold, SourceID: "b"})
	recv(t, g.entered, "device 2's job")
	job := enqueue(t, r, Spec{Kind: kindMove, SourceID: "a"})
	recv(t, m.entered, "moving job")
	intake := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"})
	tick(t, r)
	if got := mustGet(t, r, intake.ID); got.State != domain.JobQueued {
		t.Fatalf("interactive job is %s while the moving job holds device 1", got.State)
	}

	m.send(t, use("b"))
	if got := recv(t, g.entered, "interactive job"); got.ID != intake.ID {
		t.Fatalf("job granted device 1 = %s, want the interactive job %s", got.ID, intake.ID)
	}
	if n := r.Yielded(); n != 1 {
		t.Fatalf("Yielded() = %d while the job waits for device 2, want 1", n)
	}
	m.waiting(t, "device 2 held")
	if got := mustGet(t, r, job.ID); got.State != domain.JobRunning || got.DeviceKey != "dev:1" {
		t.Fatalf("waiting job = %s under %q, want running under dev:1", got.State, got.DeviceKey)
	}

	g.release(hold.ID)
	if err := recv(t, m.done, "the move's grant"); err != nil {
		t.Fatalf("UseSource(b) = %v", err)
	}
	waitJob(t, r, hold.ID, "succeeded", inState(domain.JobSucceeded))
	if n := r.Yielded(); n != 0 {
		t.Fatalf("Yielded() = %d after the grant, want 0", n)
	}
	if err := m.run(t, func(_ context.Context, rt Runtime) error {
		rt.Progress(map[string]int64{"hashed": 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, r, job.ID); got.DeviceKey != "dev:2" || got.Progress["hashed"] != 1 {
		t.Fatalf("after a progress flush: device_key %q, progress %v; want dev:2 and hashed 1", got.DeviceKey, got.Progress)
	}
	close(m.steps)
	if got := waitJob(t, r, job.ID, "succeeded", inState(domain.JobSucceeded)); got.DeviceKey != "dev:2" {
		t.Fatalf("finished job under %q, want dev:2", got.DeviceKey)
	}
	g.release(intake.ID)
	waitJob(t, r, intake.ID, "succeeded", inState(domain.JobSucceeded))
}

// Design D13: a moved job waits for its new device as a job of its class
// that began waiting at the move. Device 2's interactive job goes first;
// among reconciliation jobs, one queued before the move goes before the
// moved job, and one queued after it goes after.
func TestUseSourceRanksTheMovedJob(t *testing.T) {
	e := newEnv(t)
	devices(t, e, map[domain.SourceID]int64{"a": 1, "b": 2})
	m, g := newMover(), newGate()
	r := e.runner(t, map[Kind]Handler{kindMove: m, kindHold: g, kindReconcile: g})
	r.RegisterClass(kindInteractive, g, ClassInteractive)
	start(t, r)

	hold := enqueue(t, r, Spec{Kind: kindHold, SourceID: "b"})
	recv(t, g.entered, "device 2's job")
	job := enqueue(t, r, Spec{Kind: kindMove, SourceID: "a"})
	recv(t, m.entered, "moving job")
	e.clock.Advance(time.Second)
	older := enqueue(t, r, Spec{Kind: kindReconcile, SourceID: "b"})
	filler := enqueue(t, r, Spec{Kind: kindReconcile, SourceID: "a"})
	e.clock.Advance(time.Second)
	m.send(t, use("b"))
	if got := recv(t, g.entered, "device 1's next job"); got.ID != filler.ID {
		t.Fatalf("job granted device 1 = %s, want %s", got.ID, filler.ID)
	}
	e.clock.Advance(time.Second)
	intake := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "b"})
	e.clock.Advance(time.Second)
	younger := enqueue(t, r, Spec{Kind: kindReconcile, SourceID: "b"})

	g.release(hold.ID)
	if got := recv(t, g.entered, "first grant of device 2"); got.ID != intake.ID {
		t.Fatalf("first grant = %s, want the interactive job %s", got.ID, intake.ID)
	}
	m.waiting(t, "interactive job holds device 2")
	g.release(intake.ID)
	if got := recv(t, g.entered, "second grant of device 2"); got.ID != older.ID {
		t.Fatalf("second grant = %s, want the reconciliation job queued before the move %s", got.ID, older.ID)
	}
	m.waiting(t, "older job holds device 2")
	g.release(older.ID)
	if err := recv(t, m.done, "the move's grant"); err != nil {
		t.Fatalf("UseSource(b) = %v", err)
	}
	tick(t, r)
	if got := mustGet(t, r, younger.ID); got.State != domain.JobQueued {
		t.Fatalf("job queued after the move is %s while the moved job holds device 2", got.State)
	}
	close(m.steps)
	if got := recv(t, g.entered, "third grant of device 2"); got.ID != younger.ID {
		t.Fatalf("next grant = %s, want %s", got.ID, younger.ID)
	}
	waitJob(t, r, job.ID, "succeeded", inState(domain.JobSucceeded))
	g.release(younger.ID)
	g.release(filler.ID)
}

// Design D13, cancel while waiting: whether the job is cancelled or the
// context passed to UseSource ends, UseSource returns context.Canceled
// holding no slot, the job finishes cleanly releasing nothing more, and once
// every job has finished the device slot counts are back to zero.
func TestUseSourceCancelWhileWaiting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final domain.JobState
	}{
		{"job cancelled", domain.JobCancelled},
		{"context ended", domain.JobSucceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			devices(t, e, map[domain.SourceID]int64{"a": 1, "b": 2})
			m, g := newMover(), newGate()
			r := e.runner(t, map[Kind]Handler{kindMove: m, kindHold: g, kindReconcile: g})
			start(t, r)

			hold := enqueue(t, r, Spec{Kind: kindHold, SourceID: "b"})
			recv(t, g.entered, "device 2's job")
			job := enqueue(t, r, Spec{Kind: kindMove, SourceID: "a"})
			recv(t, m.entered, "moving job")
			filler := enqueue(t, r, Spec{Kind: kindReconcile, SourceID: "a"})

			wctx, endWait := context.WithCancel(context.Background())
			defer endWait()
			type result struct {
				err   error
				state deviceState
			}
			moved := make(chan result, 1)
			m.send(t, func(ctx context.Context, rt Runtime) error {
				if tc.final == domain.JobSucceeded {
					ctx = wctx
				}
				err := rt.UseSource(ctx, "b")
				moved <- result{err, deviceStateOf(r)}
				if tc.final == domain.JobSucceeded {
					return nil // the handler winds down and succeeds
				}
				return err
			})
			if got := recv(t, g.entered, "device 1's next job"); got.ID != filler.ID {
				t.Fatalf("job granted device 1 = %s, want %s", got.ID, filler.ID)
			}
			if tc.final == domain.JobCancelled {
				if _, err := r.Cancel(context.Background(), job.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				endWait()
			}
			res := recv(t, moved, "the interrupted move")
			if !errors.Is(res.err, context.Canceled) {
				t.Fatalf("UseSource = %v, want context.Canceled", res.err)
			}
			wantBusy := map[string]int{"dev:1": 1, "dev:2": 1}
			wantRunning := map[domain.SourceID]int{"a": 1, "b": 1}
			if !maps.Equal(res.state.busy, wantBusy) || !maps.Equal(res.state.running, wantRunning) || len(res.state.yielded) != 0 {
				t.Fatalf("after the interrupted move: %+v; want busy %v, running %v (the other jobs only), nothing yielded",
					res.state, wantBusy, wantRunning)
			}
			recv(t, m.done, "step result")
			if tc.final == domain.JobSucceeded {
				close(m.steps)
			}
			waitJob(t, r, job.ID, string(tc.final), inState(tc.final))
			waitFinished(t, r, job.ID)
			if s := deviceStateOf(r); !maps.Equal(s.busy, wantBusy) || !maps.Equal(s.running, wantRunning) || len(s.yielded) != 0 {
				t.Fatalf("after the job finished: %+v; want busy %v, running %v, nothing yielded", s, wantBusy, wantRunning)
			}

			g.release(hold.ID)
			g.release(filler.ID)
			for _, id := range []domain.JobID{hold.ID, filler.ID} {
				waitJob(t, r, id, "succeeded", inState(domain.JobSucceeded))
			}
			stop(t, r)
			if s := deviceStateOf(r); len(s.busy)+len(s.running)+len(s.exclusive)+len(s.yielded) != 0 {
				t.Fatalf("device state after every job finished = %+v, want empty", s)
			}
		})
	}
}

// Design D13, watchdog after a move: an overdue filesystem call made after
// the job moved from a to b marks b unresponsive, not a, and the call's
// return clears b's flag.
func TestUseSourceWatchdogWatchesTheNewSource(t *testing.T) {
	e := newEnv(t)
	devices(t, e, map[domain.SourceID]int64{"a": 1, "b": 2})
	m := newMover()
	r := e.runner(t, map[Kind]Handler{kindMove: m})
	start(t, r)

	job := enqueue(t, r, Spec{Kind: kindMove, SourceID: "a"})
	recv(t, m.entered, "moving job")
	if err := m.run(t, use("b")); err != nil {
		t.Fatalf("UseSource(b) = %v", err)
	}
	var done func()
	if err := m.run(t, func(_ context.Context, rt Runtime) error { done = rt.FSCall("read"); return nil }); err != nil {
		t.Fatal(err)
	}
	callStart := e.clock.Now()
	e.clock.Advance(e.cfg.CallWatchdog.Duration)
	tick(t, r)
	calls := e.reg.unresponsiveCalls()
	if len(calls) != 1 || calls[0].Source != "b" || !calls[0].Since.Equal(callStart) {
		t.Fatalf("unresponsive calls = %+v, want b flagged since %v", calls, callStart)
	}
	done()
	calls = e.reg.unresponsiveCalls()
	if len(calls) != 2 || calls[1].Source != "b" || !calls[1].Since.IsZero() {
		t.Fatalf("unresponsive calls = %+v, want b's flag cleared when the call returned", calls)
	}
	close(m.steps)
	waitJob(t, r, job.ID, "succeeded", inState(domain.JobSucceeded))
}

// Design D13 with D14, a move to a source whose device is not recorded: the
// placeholder key source:p has a free slot (two workers per device), but
// the move waits while another attempt runs in p, then holds p exclusively
// until it moves away.
func TestUseSourceToPlaceholderWaitsForTheSource(t *testing.T) {
	e := newEnv(t)
	e.cfg.WorkersPerDevice = 2
	devices(t, e, map[domain.SourceID]int64{"a": 1})
	addSource(t, e.st, "p") // device not recorded yet
	m, g := newMover(), newGate()
	r := e.runner(t, map[Kind]Handler{kindMove: m, KindScan: g, kindReconcile: g}, func(o *Options) { o.ProgressBatch = 1 })
	start(t, r)

	scan := enqueue(t, r, Spec{Kind: KindScan, SourceID: "p"})
	recv(t, g.entered, "p's placeholder scan")
	job := enqueue(t, r, Spec{Kind: kindMove, SourceID: "a"})
	recv(t, m.entered, "moving job")
	other := enqueue(t, r, Spec{Kind: kindReconcile, SourceID: "a"})
	recv(t, g.entered, "device 1's second job")
	filler := enqueue(t, r, Spec{Kind: kindReconcile, SourceID: "a"})

	m.send(t, use("p"))
	if got := recv(t, g.entered, "device 1's next job"); got.ID != filler.ID {
		t.Fatalf("job granted device 1 = %s, want %s", got.ID, filler.ID)
	}
	tick(t, r)
	if n := r.Yielded(); n != 1 {
		t.Fatalf("Yielded() = %d while p's scan runs, want 1 (the move waiting)", n)
	}
	m.waiting(t, "p's scan running")

	g.release(scan.ID)
	if err := recv(t, m.done, "the move's grant"); err != nil {
		t.Fatalf("UseSource(p) = %v", err)
	}
	if err := m.run(t, func(_ context.Context, rt Runtime) error {
		rt.Progress(map[string]int64{"listed": 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if key := mustGet(t, r, job.ID).DeviceKey; key != "source:p" {
		t.Fatalf("device_key after the move = %q, want source:p", key)
	}
	later := enqueue(t, r, Spec{Kind: kindReconcile, SourceID: "p"})
	tick(t, r)
	if got := mustGet(t, r, later.ID); got.State != domain.JobQueued {
		t.Fatalf("p's job is %s while the moved job holds p exclusively", got.State)
	}

	g.release(other.ID)
	waitJob(t, r, other.ID, "succeeded", inState(domain.JobSucceeded))
	if err := m.run(t, use("a")); err != nil {
		t.Fatalf("UseSource(a) = %v", err)
	}
	if got := recv(t, g.entered, "p's job"); got.ID != later.ID {
		t.Fatalf("next job = %s, want p's job %s once the moved job left p", got.ID, later.ID)
	}
	close(m.steps)
	waitJob(t, r, job.ID, "succeeded", inState(domain.JobSucceeded))
	g.release(later.ID)
	g.release(filler.ID)
}

// Design D13: a source with no row is an unknown_source error, and a pool
// job cannot move; either error fails its job.
func TestUseSourceRejects(t *testing.T) {
	e := newEnv(t)
	devices(t, e, map[domain.SourceID]int64{"a": 1})
	m, pooled := newMover(), newMover()
	r := e.runner(t, map[Kind]Handler{kindMove: m})
	r.RegisterPool(kindClassify, pooled, "classifier", 1)
	start(t, r)

	job := enqueue(t, r, Spec{Kind: kindMove, SourceID: "a"})
	recv(t, m.entered, "moving job")
	var de *domain.Error
	if err := m.run(t, use("gone")); !errors.As(err, &de) || de.Code != domain.CodeUnknownSource {
		t.Fatalf("UseSource(gone) = %v, want unknown_source", err)
	}
	if got := waitJob(t, r, job.ID, "failed", inState(domain.JobFailed)); got.TerminalCode != domain.CodeUnknownSource {
		t.Fatalf("job failed with %q, want unknown_source", got.TerminalCode)
	}

	pool := enqueue(t, r, Spec{Kind: kindClassify, SourceID: "a"})
	recv(t, pooled.entered, "pool job")
	if err := pooled.run(t, use("a")); err == nil || errors.As(err, &de) {
		t.Fatalf("pool job's UseSource(a) = %v, want a non-domain error", err)
	}
	waitJob(t, r, pool.ID, "failed", inState(domain.JobFailed))
}
