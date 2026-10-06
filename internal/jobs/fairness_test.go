package jobs

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"precious/internal/domain"
)

const (
	kindInteractive Kind = "interactive"
	kindBulk        Kind = "bulk"
	kindHold        Kind = "hold"
	// kindReconcile is a reconciliation kind with no one-active-scan rule.
	kindReconcile Kind = "reconcile"
)

// grantLog records, in order, which job had the device.
type grantLog struct {
	mu  sync.Mutex
	seq []string
}

func (l *grantLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq = append(l.seq, s)
}

func (l *grantLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seq)
}

// sharedDevice adds source "a" with its device recorded, so its jobs share
// the key dev:1 and its single worker.
func sharedDevice(t *testing.T, e *env) {
	t.Helper()
	addSource(t, e.st, "a")
	recordDevice(e.reg, "a", 1)
}

// yielder is a handler whose attempts each wait for the test, call Yield
// once, report its result, and wait again before returning it.
type yielder struct {
	entered chan Job
	yield   chan struct{}
	yielded chan error
	finish  chan struct{}
}

func newYielder() *yielder {
	return &yielder{entered: make(chan Job, 4), yield: make(chan struct{}), yielded: make(chan error, 4),
		finish: make(chan struct{})}
}

func (y *yielder) Run(ctx context.Context, job Job, rt Runtime) error {
	y.entered <- job
	<-y.yield
	err := rt.Yield(ctx)
	y.yielded <- err
	<-y.finish
	return err
}

// Spec "Priority classes share each device" (design D13): with jobs of all
// three classes waiting for one device, the grants, counted rather than
// timed, follow the 4:2:1 rotation whatever the order of arrival.
func TestGrantsFollowTheClassWeights(t *testing.T) {
	e := newEnv(t)
	sharedDevice(t, e)
	var log grantLog
	ran := make(chan struct{}, 14)
	record := handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		log.add(string(job.Kind))
		ran <- struct{}{}
		return nil
	})
	g := newGate()
	r := e.runner(t, map[Kind]Handler{kindHold: g, kindReconcile: record})
	r.RegisterClass(kindInteractive, record, ClassInteractive)
	r.RegisterClass(kindBulk, record, ClassBulk)
	start(t, r)

	hold := enqueue(t, r, Spec{Kind: kindHold, SourceID: "a"})
	recv(t, g.entered, "holder")
	for _, c := range []struct {
		kind Kind
		n    int
	}{{kindBulk, 2}, {kindReconcile, 4}, {kindInteractive, 8}} {
		for range c.n {
			enqueue(t, r, Spec{Kind: c.kind, SourceID: "a"})
		}
	}
	g.release(hold.ID)
	for range 14 {
		recv(t, ran, "granted job")
	}
	I, R, B := string(kindInteractive), string(kindReconcile), string(kindBulk)
	want := []string{I, I, I, I, R, R, B, I, I, I, I, R, R, B}
	if got := log.get(); !slices.Equal(got, want) {
		t.Fatalf("grants = %v, want %v", got, want)
	}
}

// Design D13: within a class the oldest wait goes first, a queued job's wait
// counted from its available_at and a yielded job's from its yield.
func TestGrantsWithinAClassGoToTheOldestWait(t *testing.T) {
	e := newEnv(t)
	sharedDevice(t, e)
	long := newYielder()
	g := newGate()
	r := e.runner(t, nil)
	r.RegisterClass("long", long, ClassInteractive)
	r.RegisterClass(kindInteractive, g, ClassInteractive)
	start(t, r)

	a := enqueue(t, r, Spec{Kind: "long", SourceID: "a"})
	recv(t, long.entered, "long job")
	e.clock.Advance(time.Second)
	q1 := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"})
	e.clock.Advance(time.Second)
	long.yield <- struct{}{} // q1 has waited a second when the long job yields
	if job := recv(t, g.entered, "first queued job"); job.ID != q1.ID {
		t.Fatalf("first grant = %s, want %s", job.ID, q1.ID)
	}
	if got := mustGet(t, r, a.ID); got.State != domain.JobRunning {
		t.Fatalf("yielded job is %s, want running", got.State)
	}

	e.clock.Advance(time.Second)
	q2 := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"}) // waits from after the yield
	g.release(q1.ID)
	if err := recv(t, long.yielded, "the long job's grant"); err != nil {
		t.Fatalf("Yield = %v", err)
	}
	tick(t, r)
	if got := mustGet(t, r, q2.ID); got.State != domain.JobQueued {
		t.Fatalf("younger job is %s while the long job holds the device", got.State)
	}
	close(long.finish)
	if job := recv(t, g.entered, "second queued job"); job.ID != q2.ID {
		t.Fatalf("next grant = %s, want %s", job.ID, q2.ID)
	}
	g.release(q2.ID)
	waitJob(t, r, q2.ID, "succeeded", inState(domain.JobSucceeded))
}

// Design D13: a job that has waited longer than 5 minutes ranks as
// interactive, so it goes before younger interactive work; one that has
// waited exactly 5 minutes still ranks in its own class.
func TestLongWaitRanksInteractive(t *testing.T) {
	e := newEnv(t)
	sharedDevice(t, e)
	var log grantLog
	ran := make(chan struct{}, 4)
	record := handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		log.add(string(job.Kind))
		ran <- struct{}{}
		return nil
	})
	g := newGate()
	r := e.runner(t, map[Kind]Handler{kindHold: g})
	r.RegisterClass(kindInteractive, record, ClassInteractive)
	r.RegisterClass(kindBulk, record, ClassBulk)
	start(t, r)

	for _, wait := range []time.Duration{agingAfter + time.Millisecond, agingAfter} {
		hold := enqueue(t, r, Spec{Kind: kindHold, SourceID: "a"})
		recv(t, g.entered, "holder")
		enqueue(t, r, Spec{Kind: kindBulk, SourceID: "a"})
		e.clock.Advance(wait)
		enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"})
		g.release(hold.ID)
		recv(t, ran, "first grant")
		recv(t, ran, "second grant")
	}
	I, B := string(kindInteractive), string(kindBulk)
	if got, want := log.get(), []string{B, I, I, B}; !slices.Equal(got, want) {
		t.Fatalf("grants = %v, want %v (aged bulk first, then interactive first)", got, want)
	}
}

// Spec "Long jobs yield between work units": a job that started before its
// source's device was known (placeholder key) never yields, even to
// interactive work waiting for its key.
func TestPlaceholderJobNeverYields(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a") // device not recorded yet
	scan := newYielder()
	g := newGate()
	r := e.runner(t, map[Kind]Handler{KindScan: scan})
	r.RegisterClass(kindInteractive, g, ClassInteractive)
	start(t, r)

	job := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, scan.entered, "placeholder scan")
	intake := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"})
	tick(t, r)
	scan.yield <- struct{}{}
	if err := recv(t, scan.yielded, "placeholder Yield"); err != nil {
		t.Fatalf("Yield = %v", err)
	}
	if got := mustGet(t, r, intake.ID); got.State != domain.JobQueued {
		t.Fatalf("interactive job is %s after the placeholder's Yield, want queued", got.State)
	}
	if key := mustGet(t, r, job.ID).DeviceKey; key != "source:a" {
		t.Fatalf("scan claimed under %q, want the placeholder source:a", key)
	}
	close(scan.finish)
	if got := recv(t, g.entered, "interactive job"); got.ID != intake.ID {
		t.Fatalf("next job = %s, want %s", got.ID, intake.ID)
	}
	g.release(intake.ID)
}

// Design D13: pool jobs have no class; their Yield returns at once, even
// while another job waits for the pool's only slot.
func TestPoolJobYieldReturnsAtOnce(t *testing.T) {
	e := newEnv(t)
	first := newYielder()
	var calls atomic.Int32
	entered := make(chan Job, 2)
	h := handlerFunc(func(ctx context.Context, job Job, rt Runtime) error {
		if calls.Add(1) == 1 {
			return first.Run(ctx, job, rt)
		}
		entered <- job
		return nil
	})
	r := e.runner(t, nil)
	r.RegisterPool(kindClassify, h, "classifier", 1)
	start(t, r)

	enqueue(t, r, Spec{Kind: kindClassify})
	recv(t, first.entered, "first pool job")
	second := enqueue(t, r, Spec{Kind: kindClassify})
	tick(t, r)
	first.yield <- struct{}{}
	if err := recv(t, first.yielded, "pool Yield"); err != nil {
		t.Fatalf("Yield = %v", err)
	}
	if got := mustGet(t, r, second.ID); got.State != domain.JobQueued {
		t.Fatalf("second pool job is %s after the first one's Yield, want queued", got.State)
	}
	close(first.finish)
	if got := recv(t, entered, "second pool job"); got.ID != second.ID {
		t.Fatalf("next job = %s, want %s", got.ID, second.ID)
	}
}

// Design D13: cancellation while blocked in Yield returns the context's
// error and ends the job cancelled; the job it yielded to keeps the device
// to itself.
func TestCancelWhileYielded(t *testing.T) {
	e := newEnv(t)
	sharedDevice(t, e)
	scan := newYielder()
	close(scan.finish)
	g := newGate()
	r := e.runner(t, map[Kind]Handler{KindScan: scan})
	r.RegisterClass(kindInteractive, g, ClassInteractive)
	start(t, r)

	job := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, scan.entered, "scan")
	intake := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"})
	scan.yield <- struct{}{}
	recv(t, g.entered, "interactive job")
	if n := r.Yielded(); n != 1 {
		t.Fatalf("Yielded() = %d while the scan waits for the device, want 1", n)
	}
	if _, err := r.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := recv(t, scan.yielded, "cancelled Yield"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Yield = %v, want context.Canceled", err)
	}
	waitJob(t, r, job.ID, "cancelled", inState(domain.JobCancelled))
	if n := r.Yielded(); n != 0 {
		t.Fatalf("Yielded() = %d after the cancelled scan ended, want 0", n)
	}

	third := enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"})
	tick(t, r)
	if got := mustGet(t, r, third.ID); got.State != domain.JobQueued {
		t.Fatalf("a second job runs on the device: %s", got.State)
	}
	g.release(intake.ID)
	if got := recv(t, g.entered, "third job"); got.ID != third.ID {
		t.Fatalf("next job = %s, want %s", got.ID, third.ID)
	}
	g.release(third.ID)
}

// Design D13: a yielded job stays running and its lease keeps being renewed;
// a restart while it is yielded recovers it as a lost worker's job, with one
// more attempt.
func TestYieldedJobKeepsItsLease(t *testing.T) {
	e := newEnv(t)
	sharedDevice(t, e)
	scan := newYielder()
	g := newGate()
	dead := e.runner(t, map[Kind]Handler{KindScan: scan})
	dead.RegisterClass(kindInteractive, g, ClassInteractive)
	start(t, dead)

	job := enqueue(t, dead, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, scan.entered, "scan")
	enqueue(t, dead, Spec{Kind: kindInteractive, SourceID: "a"})
	scan.yield <- struct{}{}
	recv(t, g.entered, "interactive job")
	for range 10 {
		e.clock.Advance(e.cfg.LeaseRenew.Duration)
		tick(t, dead)
	}
	got := mustGet(t, dead, job.ID)
	if got.State != domain.JobRunning || got.Attempts != 0 {
		t.Fatalf("yielded job after 10 renew periods: %s attempts %d", got.State, got.Attempts)
	}
	if want := e.clock.Now().Add(e.cfg.Lease.Duration).UnixMilli(); got.leaseExpires != want {
		t.Fatalf("lease expires %d, want %d", got.leaseExpires, want)
	}

	abandon(dead) // the process dies with the scan yielded
	ran := make(chan Job, 1)
	restarted := e.runner(t, map[Kind]Handler{KindScan: handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		ran <- job
		return nil
	})})
	restarted.RegisterClass(kindInteractive, handlerFunc(func(context.Context, Job, Runtime) error { return nil }), ClassInteractive)
	start(t, restarted)
	if again := recv(t, ran, "scan after restart"); again.ID != job.ID || again.Attempt != 2 {
		t.Fatalf("after restart: job %s attempt %d, want job %s attempt 2", again.ID, again.Attempt, job.ID)
	}
	if final := waitJob(t, restarted, job.ID, "succeeded", inState(domain.JobSucceeded)); final.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", final.Attempts)
	}
	close(scan.finish)
}

// Spec "No class starves another", at runner level: a reconciliation job
// that yields after each work unit, while an interactive job is enqueued
// again after every grant, still gets 2 of every 6 grants.
func TestYieldingJobIsNotStarvedByArrivals(t *testing.T) {
	e := newEnv(t)
	sharedDevice(t, e)
	const units = 6
	var (
		log      grantLog
		scanDone atomic.Bool
		r        *Runner
	)
	begin, quiet := make(chan struct{}), make(chan struct{})
	scan := handlerFunc(func(ctx context.Context, _ Job, rt Runtime) error {
		<-begin
		for i := range units {
			log.add("R")
			if i == units-1 {
				break
			}
			if err := rt.Yield(ctx); err != nil {
				return err
			}
		}
		scanDone.Store(true)
		return nil
	})
	arrival := handlerFunc(func(ctx context.Context, _ Job, _ Runtime) error {
		log.add("I")
		if scanDone.Load() {
			close(quiet)
			return nil
		}
		_, err := r.Enqueue(ctx, Spec{Kind: kindInteractive, SourceID: "a"})
		return err
	})
	r = e.runner(t, map[Kind]Handler{KindScan: scan})
	r.RegisterClass(kindInteractive, arrival, ClassInteractive)
	start(t, r)

	job := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	waitJob(t, r, job.ID, "running", inState(domain.JobRunning))
	enqueue(t, r, Spec{Kind: kindInteractive, SourceID: "a"})
	close(begin)
	waitJob(t, r, job.ID, "succeeded", inState(domain.JobSucceeded))
	recv(t, quiet, "last arrival")
	want := []string{"R", "I", "I", "I", "I", "R", "R", "I", "I", "I", "I", "R", "R", "I", "I", "I", "I", "R", "I"}
	if got := log.get(); !slices.Equal(got, want) {
		t.Fatalf("grants = %v, want %v", got, want)
	}
}
