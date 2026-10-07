package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/store"
)

func TestStateTransitions(t *testing.T) {
	const (
		q = domain.JobQueued
		r = domain.JobRunning
		p = domain.JobPaused
		s = domain.JobSucceeded
		f = domain.JobFailed
		c = domain.JobCancelled
	)
	allowed := map[[2]domain.JobState]bool{
		{q, r}: true, {q, c}: true,
		{r, s}: true, {r, f}: true, {r, c}: true, {r, p}: true, {r, q}: true,
		{p, q}: true, {p, c}: true,
	}
	all := []domain.JobState{q, r, p, s, f, c}
	for _, from := range all {
		for _, to := range all {
			if got, want := CanTransition(from, to), allowed[[2]domain.JobState{from, to}]; got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}

	for _, tc := range []struct {
		state  domain.JobState
		cancel bool
		want   domain.JobState
	}{
		{r, true, StateCancelRequested},
		{q, true, StateCancelRequested},
		{r, false, r},
		{c, true, c},
		{s, true, s},
	} {
		if got := DisplayState(tc.state, tc.cancel); got != tc.want {
			t.Errorf("DisplayState(%s, %v) = %s, want %s", tc.state, tc.cancel, got, tc.want)
		}
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	e := newEnv(t)
	r := e.runner(t, nil)
	ctx := context.Background()
	rec := enqueue(t, r, Spec{Kind: "test"})

	// queued -> succeeded skips running.
	err := r.Write(ctx, func(tx *Tx) error {
		next := rec
		next.State = domain.JobSucceeded
		_, err := tx.change(rec, next, "", eventState)
		return err
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("queued -> succeeded: err = %v, want ErrIllegalTransition", err)
	}
	if got := mustGet(t, r, rec.ID).State; got != domain.JobQueued {
		t.Fatalf("state after rejected transition = %s, want queued", got)
	}

	cancelled, err := r.Cancel(ctx, rec.ID)
	if err != nil || cancelled.State != domain.JobCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("Cancel queued = %+v, %v; want cancelled with finished_at", cancelled, err)
	}
	before := len(jobEvents(t, e.st, rec.ID))

	// cancelled is terminal: no resume, no claim, cancel is a no-op.
	if _, err := r.Resume(ctx, rec.ID); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Fatalf("Resume cancelled: err = %v, want invalid_request", err)
	}
	err = r.Write(ctx, func(tx *Tx) error {
		next := cancelled
		next.State = domain.JobRunning
		_, err := tx.change(cancelled, next, "", eventState)
		return err
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("cancelled -> running: err = %v, want ErrIllegalTransition", err)
	}
	again, err := r.Cancel(ctx, rec.ID)
	if err != nil || again.State != domain.JobCancelled {
		t.Fatalf("Cancel cancelled = %+v, %v", again, err)
	}
	if got := len(jobEvents(t, e.st, rec.ID)); got != before {
		t.Fatalf("no-op changes wrote %d events", got-before)
	}
	if _, err := r.Cancel(ctx, 9999); domain.CodeOf(err) != domain.CodeNotFound {
		t.Fatalf("Cancel missing job: err = %v, want not_found", err)
	}
}

// Spec scenario "Job visible after restart".
func TestJobVisibleAfterRestart(t *testing.T) {
	st, dir := openStore(t)
	e := &env{st: st, clock: newFakeClock(), reg: &fakeRegistry{}}
	addSource(t, st, "a")
	first := e.runner(t, nil)
	rec := enqueue(t, first, Spec{Kind: KindScan, SourceID: "a"})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	e.st = reopened
	ran := make(chan Job, 1)
	r := e.runner(t, map[Kind]Handler{KindScan: handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		ran <- job
		return nil
	})})
	if got := mustGet(t, r, rec.ID).State; got != domain.JobQueued {
		t.Fatalf("after restart state = %s, want queued", got)
	}
	start(t, r)
	job := recv(t, ran, "job run after restart")
	if job.ID != rec.ID || job.SourceID != "a" || job.Attempt != 1 || string(job.Payload) != "{}" || job.PayloadVersion != 1 {
		t.Fatalf("handler got %+v", job)
	}
	waitJob(t, r, rec.ID, "succeeded", inState(domain.JobSucceeded))
}

// blocker is a handler that reports each attempt and blocks until released,
// ignoring cancellation like a stuck filesystem call.
type blocker struct {
	entered chan Job
	release chan struct{}
}

func newBlocker() *blocker {
	return &blocker{entered: make(chan Job, 8), release: make(chan struct{})}
}

func (b *blocker) Run(ctx context.Context, job Job, _ Runtime) error {
	b.entered <- job
	<-b.release
	return ctx.Err()
}

// Spec scenario "Crashed worker recovered" (task 6.2 crash simulation).
func TestCrashedWorkerRecovered(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	stuck := newBlocker()
	dead := e.runner(t, map[Kind]Handler{KindScan: stuck})
	start(t, dead)
	rec := enqueue(t, dead, Spec{Kind: KindScan, SourceID: "a"})
	if job := recv(t, stuck.entered, "first attempt"); job.Attempt != 1 {
		t.Fatalf("first attempt number = %d", job.Attempt)
	}
	if got := mustGet(t, dead, rec.ID); got.State != domain.JobRunning || got.Attempts != 0 {
		t.Fatalf("before crash: %s attempts %d", got.State, got.Attempts)
	}
	abandon(dead) // the process dies with the job running

	ran := make(chan Job, 1)
	restarted := e.runner(t, map[Kind]Handler{KindScan: handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		ran <- job
		return nil
	})})
	start(t, restarted)
	if job := recv(t, ran, "attempt after restart"); job.Attempt != 2 {
		t.Fatalf("attempt after restart = %d, want 2", job.Attempt)
	}
	final := waitJob(t, restarted, rec.ID, "succeeded", inState(domain.JobSucceeded))
	if final.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", final.Attempts)
	}
	evs := jobEvents(t, e.st, rec.ID)
	wantStates := []domain.JobState{domain.JobQueued, domain.JobRunning, domain.JobQueued, domain.JobRunning, domain.JobSucceeded}
	if got := states(evs); !slices.Equal(got, wantStates) {
		t.Fatalf("event states = %v, want %v", got, wantStates)
	}
	if evs[2].Event.Attempts != 1 {
		t.Fatalf("requeue event attempts = %d, want 1", evs[2].Event.Attempts)
	}

	// The dead worker's handler finally returns: its outcome is discarded
	// because its lease is gone.
	close(stuck.release)
	stopNow(t, dead)
	if got := mustGet(t, restarted, rec.ID); got.State != domain.JobSucceeded || got.Attempts != 1 {
		t.Fatalf("after stale finish: %s attempts %d", got.State, got.Attempts)
	}
	if got := len(jobEvents(t, e.st, rec.ID)); got != len(evs) {
		t.Fatalf("stale attempt wrote %d events", got-len(evs))
	}
}

func stopNow(t *testing.T, r *Runner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// Spec scenario "Attempts exhausted".
func TestAttemptsExhausted(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	stuck := newBlocker()
	defer close(stuck.release)
	var id domain.JobID
	for attempt := 1; attempt <= e.cfg.MaxAttempts; attempt++ {
		r := e.runner(t, map[Kind]Handler{KindScan: stuck})
		start(t, r)
		if attempt == 1 {
			id = enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"}).ID
		}
		if job := recv(t, stuck.entered, "attempt"); job.Attempt != attempt {
			t.Fatalf("attempt number = %d, want %d", job.Attempt, attempt)
		}
		abandon(r)
	}

	last := e.runner(t, map[Kind]Handler{KindScan: handlerFunc(func(context.Context, Job, Runtime) error {
		t.Error("job retried after its attempts were exhausted")
		return nil
	})})
	start(t, last)
	tick(t, last) // every due job has been considered for dispatch
	got := mustGet(t, last, id)
	if got.State != domain.JobFailed || got.TerminalCode != domain.CodeAttemptsExhausted ||
		got.Attempts != e.cfg.MaxAttempts || got.FinishedAt == nil {
		t.Fatalf("after %d lost workers: %+v", e.cfg.MaxAttempts, got)
	}
}

// A worker that stops renewing its lease (hung process) loses the job once
// the lease expires.
func TestLeaseExpiryRequeues(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	ran := make(chan Job, 1)
	healthy := e.runner(t, map[Kind]Handler{KindScan: handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		ran <- job
		return nil
	})})
	start(t, healthy)
	tick(t, healthy) // its startup dispatch is over before the job exists

	stuck := newBlocker()
	hung := e.runner(t, map[Kind]Handler{KindScan: stuck})
	start(t, hung)
	rec := enqueue(t, hung, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, stuck.entered, "hung attempt")
	abandon(hung)

	e.clock.Advance(e.cfg.Lease.Duration - time.Second)
	tick(t, healthy)
	if got := mustGet(t, healthy, rec.ID); got.State != domain.JobRunning || got.Attempts != 0 {
		t.Fatalf("before lease expiry: %s attempts %d", got.State, got.Attempts)
	}
	e.clock.Advance(2 * time.Second)
	tick(t, healthy)
	if job := recv(t, ran, "attempt after lease expiry"); job.Attempt != 2 {
		t.Fatalf("attempt after expiry = %d, want 2", job.Attempt)
	}
	final := waitJob(t, healthy, rec.ID, "succeeded", inState(domain.JobSucceeded))
	if final.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", final.Attempts)
	}
	close(stuck.release)
	stopNow(t, hung)
	if got := mustGet(t, healthy, rec.ID).State; got != domain.JobSucceeded {
		t.Fatalf("hung worker overwrote the outcome: %s", got)
	}
}

// A running worker keeps its lease by renewing it, however long the job runs.
func TestLeaseRenewedWhileRunning(t *testing.T) {
	e := newEnv(t)
	stuck := newBlocker()
	r := e.runner(t, map[Kind]Handler{"test": stuck})
	start(t, r)
	rec := enqueue(t, r, Spec{Kind: "test"})
	recv(t, stuck.entered, "attempt")
	for range 10 {
		e.clock.Advance(e.cfg.LeaseRenew.Duration)
		tick(t, r)
	}
	got := mustGet(t, r, rec.ID)
	if got.State != domain.JobRunning || got.Attempts != 0 {
		t.Fatalf("after 10 renew periods: %s attempts %d", got.State, got.Attempts)
	}
	if want := e.clock.Now().Add(e.cfg.Lease.Duration).UnixMilli(); got.leaseExpires != want {
		t.Fatalf("lease expires %d, want %d", got.leaseExpires, want)
	}
	close(stuck.release)
	waitJob(t, r, rec.ID, "succeeded", inState(domain.JobSucceeded))
}

// Spec scenario "Single worker per device"; UI reads stay served meanwhile.
// Sources a and b share device 1, recorded by their first observation.
func TestSingleWorkerPerDevice(t *testing.T) {
	e := newEnv(t)
	devices := map[domain.SourceID]int64{"a": 1, "b": 1, "c": 2}
	release := map[domain.SourceID]chan struct{}{}
	for src, dev := range devices {
		addSource(t, e.st, src)
		recordDevice(e.reg, src, dev)
		release[src] = make(chan struct{})
	}
	var (
		mu        sync.Mutex
		active    = map[int64]int{}
		maxActive = map[int64]int{}
	)
	started := make(chan domain.SourceID, 3)
	h := handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		dev := devices[job.SourceID]
		mu.Lock()
		active[dev]++
		maxActive[dev] = max(maxActive[dev], active[dev])
		mu.Unlock()
		started <- job.SourceID
		<-release[job.SourceID]
		mu.Lock()
		active[dev]--
		mu.Unlock()
		return nil
	})
	r := e.runner(t, map[Kind]Handler{KindScan: h})
	start(t, r)
	ids := map[domain.SourceID]domain.JobID{}
	for _, src := range []domain.SourceID{"a", "b", "c"} {
		ids[src] = enqueue(t, r, Spec{Kind: KindScan, SourceID: src}).ID
	}
	first := []domain.SourceID{recv(t, started, "first scan"), recv(t, started, "second scan")}
	slices.Sort(first)
	if !slices.Equal(first, []domain.SourceID{"a", "c"}) {
		t.Fatalf("first scans = %v, want a (dev:1) and c (dev:2)", first)
	}
	tick(t, r) // every due job has been considered for dispatch
	if got := mustGet(t, r, ids["b"]).State; got != domain.JobQueued {
		t.Fatalf("second dev:1 scan is %s while the first runs", got)
	}

	// UI read queries are served while scans run.
	var n int
	if err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state = 'running'`).Scan(&n)
	}); err != nil || n != 2 {
		t.Fatalf("read during scan: %d running, %v", n, err)
	}

	close(release["a"])
	if got := recv(t, started, "queued dev:1 scan"); got != "b" {
		t.Fatalf("next scan = %s, want b", got)
	}
	close(release["b"])
	close(release["c"])
	for src, id := range ids {
		waitJob(t, r, id, "succeeded "+string(src), inState(domain.JobSucceeded))
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive[1] != 1 {
		t.Fatalf("device 1 ran %d scans at once", maxActive[1])
	}
	if key := mustGet(t, r, ids["b"]).DeviceKey; key != "dev:1" {
		t.Fatalf("b's scan claimed under %q, want dev:1", key)
	}
}

// Spec scenario "Cancel mid-scan".
func TestCancelMidScan(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	step := make(chan struct{}, 16)
	did := make(chan int, 16)
	var calls atomic.Int64
	h := handlerFunc(func(ctx context.Context, _ Job, rt Runtime) error {
		for i := 1; ; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-step:
			}
			// Cancellation is checked between filesystem calls.
			if err := ctx.Err(); err != nil {
				return err
			}
			done := rt.FSCall("probe")
			calls.Add(1)
			done()
			rt.Progress(map[string]int64{"probed": int64(i)})
			did <- i
		}
	})
	r := e.runner(t, map[Kind]Handler{KindScan: h}, func(o *Options) { o.ProgressBatch = 1 })
	start(t, r)
	rec := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	for range 2 {
		step <- struct{}{}
		recv(t, did, "probe")
	}
	got, err := r.Cancel(context.Background(), rec.ID)
	if err != nil || got.State != domain.JobRunning || got.DisplayState() != StateCancelRequested {
		t.Fatalf("Cancel running = %s/%s, %v", got.State, got.DisplayState(), err)
	}
	for range 5 { // work offered after the cancel is not done
		step <- struct{}{}
	}
	final := waitJob(t, r, rec.ID, "cancelled", inState(domain.JobCancelled))
	if n := calls.Load(); n != 2 {
		t.Fatalf("filesystem calls = %d, want 2", n)
	}
	if final.Progress["probed"] != 2 || !final.CancelRequested || final.TerminalCode != "" || final.FinishedAt == nil {
		t.Fatalf("cancelled job = %+v; want committed progress kept", final)
	}
}

// Spec scenario "Blocked filesystem call".
func TestBlockedFilesystemCall(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	entered := make(chan struct{})
	release := make(chan struct{})
	h := handlerFunc(func(ctx context.Context, _ Job, rt Runtime) error {
		done := rt.FSCall("readdir")
		close(entered)
		<-release // a filesystem call that does not observe cancellation
		done()
		return ctx.Err()
	})
	r := e.runner(t, map[Kind]Handler{KindScan: h})
	start(t, r)
	rec := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, entered, "blocked call")
	callStart := e.clock.Now()
	if _, err := r.Cancel(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}

	e.clock.Advance(e.cfg.CallWatchdog.Duration - time.Second)
	tick(t, r)
	if calls := e.reg.unresponsiveCalls(); len(calls) != 0 {
		t.Fatalf("flagged before the watchdog: %+v", calls)
	}
	e.clock.Advance(2 * time.Second)
	tick(t, r)
	tick(t, r) // flagged once, not per tick
	calls := e.reg.unresponsiveCalls()
	if len(calls) != 1 || calls[0].Source != "a" || !calls[0].Since.Equal(callStart) {
		t.Fatalf("unresponsive calls = %+v, want one for a since %v", calls, callStart)
	}
	if got := mustGet(t, r, rec.ID); got.State != domain.JobRunning || got.DisplayState() != StateCancelRequested {
		t.Fatalf("blocked job shows %s/%s, want running/cancel_requested", got.State, got.DisplayState())
	}

	close(release)
	waitJob(t, r, rec.ID, "cancelled", inState(domain.JobCancelled))
	calls = e.reg.unresponsiveCalls()
	if len(calls) != 2 || calls[1].Source != "a" || !calls[1].Since.IsZero() {
		t.Fatalf("unresponsive calls = %+v, want the flag cleared when the call returned", calls)
	}
}

func TestHandlerOutcomes(t *testing.T) {
	e := newEnv(t)
	h := handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		var p struct{ Outcome string }
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return err
		}
		switch p.Outcome {
		case "pause":
			return &Pause{Reason: "low_disk", Detail: "1 GiB left"}
		case "domain":
			return domain.Errorf(domain.CodeSourceOffline, "root missing")
		case "other":
			return errors.New("open /private/path: input/output error")
		case "panic":
			panic("kaboom")
		}
		return nil
	})
	r := e.runner(t, map[Kind]Handler{"test": h})
	start(t, r)
	for _, tc := range []struct {
		outcome string
		state   domain.JobState
		code    domain.ErrorCode
		pause   string
		detail  string
	}{
		{"ok", domain.JobSucceeded, "", "", ""},
		{"pause", domain.JobPaused, "", "low_disk", "1 GiB left"},
		{"domain", domain.JobFailed, domain.CodeSourceOffline, "", "root missing"},
		{"other", domain.JobFailed, domain.CodeInternal, "", "internal error"},
		{"panic", domain.JobFailed, domain.CodeInternal, "", "internal error"},
	} {
		payload, _ := json.Marshal(map[string]string{"outcome": tc.outcome})
		rec := enqueue(t, r, Spec{Kind: "test", Payload: payload})
		got := waitJob(t, r, rec.ID, string(tc.state), inState(tc.state))
		if got.TerminalCode != tc.code || got.PauseReason != tc.pause || got.TerminalDetail != tc.detail ||
			got.Attempts != 0 || (got.FinishedAt != nil) != tc.state.Terminal() {
			t.Errorf("%s: got %+v", tc.outcome, got)
		}
	}
}

func TestStartScanCoalescesAndResumes(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	var runs atomic.Int64
	h := handlerFunc(func(context.Context, Job, Runtime) error {
		if runs.Add(1) == 1 {
			return &Pause{Reason: "low_disk"}
		}
		return nil
	})
	r := e.runner(t, map[Kind]Handler{KindScan: h})
	ctx := context.Background()
	startScan := func() (Record, bool) {
		t.Helper()
		var rec Record
		var coalesced bool
		err := r.Write(ctx, func(tx *Tx) (err error) {
			rec, coalesced, err = tx.StartScan("a")
			return err
		})
		if err != nil {
			t.Fatalf("StartScan: %v", err)
		}
		return rec, coalesced
	}

	first, coalesced := startScan()
	if coalesced || first.State != domain.JobQueued || first.Kind != KindScan {
		t.Fatalf("first StartScan = %+v coalesced=%v", first, coalesced)
	}
	if again, coalesced := startScan(); !coalesced || again.ID != first.ID {
		t.Fatalf("StartScan while queued = %s coalesced=%v, want %s coalesced", again.ID, coalesced, first.ID)
	}
	// The unique index refuses a second active scan even past StartScan.
	err := r.Write(ctx, func(tx *Tx) error {
		_, err := tx.Enqueue(Spec{Kind: KindScan, SourceID: "a"})
		return err
	})
	if !isUniqueViolation(err) {
		t.Fatalf("second active scan insert: err = %v, want unique violation", err)
	}

	start(t, r)
	waitJob(t, r, first.ID, "paused", inState(domain.JobPaused))
	resumed, coalesced := startScan()
	if !coalesced || resumed.ID != first.ID || resumed.State != domain.JobQueued || resumed.PauseReason != "" {
		t.Fatalf("StartScan while paused = %+v coalesced=%v, want resumed", resumed, coalesced)
	}
	waitJob(t, r, first.ID, "succeeded", inState(domain.JobSucceeded))
	next, coalesced := startScan()
	if coalesced || next.ID == first.ID {
		t.Fatalf("StartScan after completion = %s coalesced=%v, want a new job", next.ID, coalesced)
	}
}

func TestProgressBatching(t *testing.T) {
	e := newEnv(t)
	reported := make(chan struct{})
	proceed := make(chan struct{})
	h := handlerFunc(func(_ context.Context, _ Job, rt Runtime) error {
		for i := int64(1); i <= 7; i++ {
			rt.Progress(map[string]int64{"entries": i})
		}
		rt.Progress(map[string]int64{"dirs": 1}) // other keys are kept
		close(reported)
		<-proceed
		return nil
	})
	r := e.runner(t, map[Kind]Handler{"test": h}, func(o *Options) {
		o.ProgressBatch = 3
		o.ProgressInterval = time.Second
	})
	start(t, r)
	rec := enqueue(t, r, Spec{Kind: "test"})
	recv(t, reported, "progress reported")

	progress := func() []map[string]int64 {
		var out []map[string]int64
		for _, ev := range jobEvents(t, e.st, rec.ID) {
			if ev.Type == eventProgress {
				out = append(out, ev.Event.Progress)
			}
		}
		return out
	}
	if got := progress(); len(got) != 2 || got[0]["entries"] != 3 || got[1]["entries"] != 6 {
		t.Fatalf("batched progress events = %v, want entries 3 and 6", got)
	}
	tick(t, r) // the interval has not elapsed since the last flush
	if got := len(progress()); got != 2 {
		t.Fatalf("progress events before the interval = %d, want 2", got)
	}
	e.clock.Advance(time.Second)
	tick(t, r)
	got := progress()
	if len(got) != 3 || got[2]["entries"] != 7 || got[2]["dirs"] != 1 {
		t.Fatalf("progress after the interval = %v", got)
	}
	e.clock.Advance(time.Second)
	tick(t, r) // nothing pending
	if n := len(progress()); n != 3 {
		t.Fatalf("progress events with nothing pending = %d, want 3", n)
	}
	close(proceed)
	final := waitJob(t, r, rec.ID, "succeeded", inState(domain.JobSucceeded))
	if final.Progress["entries"] != 7 || final.Progress["dirs"] != 1 {
		t.Fatalf("final progress = %v", final.Progress)
	}
}

// A graceful stop puts interrupted jobs back in the queue without using an attempt.
func TestStopRequeuesInterruptedJob(t *testing.T) {
	e := newEnv(t)
	entered := make(chan Job, 2)
	h := handlerFunc(func(ctx context.Context, job Job, _ Runtime) error {
		entered <- job
		<-ctx.Done()
		return ctx.Err()
	})
	r := e.runner(t, map[Kind]Handler{"test": h})
	start(t, r)
	rec := enqueue(t, r, Spec{Kind: "test"})
	recv(t, entered, "attempt")
	stopNow(t, r)
	if got := mustGet(t, r, rec.ID); got.State != domain.JobQueued || got.Attempts != 0 {
		t.Fatalf("after Stop: %s attempts %d, want queued with 0", got.State, got.Attempts)
	}
	next := e.runner(t, map[Kind]Handler{"test": h})
	start(t, next)
	if job := recv(t, entered, "attempt after restart"); job.Attempt != 1 {
		t.Fatalf("attempt after graceful restart = %d, want 1", job.Attempt)
	}
}

// lockedBuffer is a buffer safe for the runner's goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A running job whose source is removed (remove-source cancels the job and
// its row goes away with the source) ends quietly: its attempt is cancelled
// and its outcome discarded, with no error logged.
func TestJobRemovedWhileRunning(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	entered := make(chan struct{})
	h := handlerFunc(func(ctx context.Context, _ Job, _ Runtime) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	var logs lockedBuffer
	r := e.runner(t, map[Kind]Handler{"hash": h}, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))
	})
	start(t, r)
	rec := enqueue(t, r, Spec{Kind: "hash", SourceID: "a"})
	recv(t, entered, "attempt")
	if err := r.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.Cancel(rec.ID); err != nil {
			return err
		}
		_, err := tx.SQL().Exec(`DELETE FROM sources WHERE id = 'a'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := logs.String(); got != "" {
		t.Errorf("errors logged: %s", got)
	}
}
