package jobs

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
)

// deferredOnce reports whether rec is queued again after an attempt.
func deferredOnce(rec Record) bool { return rec.State == domain.JobQueued && rec.StartedAt != nil }

// Spec scenario "Restart during backoff" (design D11): a job deferred for
// 30 s is still queued after a restart 10 s later, is claimed no earlier than
// its deferred time, and its attempt count is unchanged.
func TestRestartDuringBackoff(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "s")
	until := e.clock.Now().Add(30 * time.Second)
	first := e.runner(t, nil)
	first.RegisterPool(kindClassify, handlerFunc(func(context.Context, Job, Runtime) error {
		return &Defer{Until: until, Reason: "rate_limited"}
	}), "classifier", 2)
	start(t, first)
	rec := enqueue(t, first, Spec{Kind: kindClassify, SourceID: "s"})

	deferred := waitJob(t, first, rec.ID, "deferred", deferredOnce)
	if deferred.availableAt != clock.Millis(until) || deferred.Attempts != 0 || deferred.leaseOwner != "" ||
		deferred.TerminalDetail != "deferred until 2026-10-01T12:00:30Z: rate_limited" {
		t.Fatalf("deferred job = %+v, available at %d; want available at %d (%s), no attempt used, reason shown",
			deferred, deferred.availableAt, clock.Millis(until), until)
	}
	want := []domain.JobState{domain.JobQueued, domain.JobRunning, domain.JobQueued}
	if got := states(jobEvents(t, e.st, rec.ID)); !slices.Equal(got, want) {
		t.Fatalf("event states = %v, want %v", got, want)
	}

	e.clock.Advance(10 * time.Second)
	stopNow(t, first)
	ran := make(chan Job, 1)
	second := e.runner(t, nil)
	second.RegisterPool(kindClassify, handlerFunc(func(_ context.Context, job Job, _ Runtime) error {
		ran <- job
		return nil
	}), "classifier", 2)
	start(t, second)
	e.clock.Advance(20*time.Second - time.Millisecond)
	tick(t, second) // every due job has been considered for dispatch
	if got := mustGet(t, second, rec.ID); got.State != domain.JobQueued || got.Attempts != 0 ||
		got.availableAt != clock.Millis(until) {
		t.Fatalf("1 ms before the deferred time: %s attempts %d available at %d", got.State, got.Attempts, got.availableAt)
	}

	e.clock.Advance(time.Millisecond)
	tick(t, second)
	if job := recv(t, ran, "attempt at the deferred time"); job.ID != rec.ID || job.Attempt != 1 {
		t.Fatalf("attempt after the deferral = %+v, want attempt 1 of job %s", job, rec.ID)
	}
	final := waitJob(t, second, rec.ID, "succeeded", inState(domain.JobSucceeded))
	if final.Attempts != 0 || final.TerminalDetail != "" {
		t.Fatalf("finished job = %+v; want no attempt used and the deferral cleared", final)
	}
}

// Design D11: a cancel request ends a deferral. A deferred job is cancelled
// at once, and a cancel request made while the attempt runs wins over the
// deferral its handler then returns.
func TestCancelDuringDeferral(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	entered := make(chan Job, 4)
	h := handlerFunc(func(ctx context.Context, job Job, _ Runtime) error {
		entered <- job
		var p struct{ Wait bool }
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return err
		}
		if p.Wait {
			<-ctx.Done()
		}
		return &Defer{Until: e.clock.Now().Add(30 * time.Second), Reason: "rate_limited"}
	})
	r := e.runner(t, nil)
	r.RegisterPool(kindClassify, h, "classifier", 2)
	start(t, r)

	deferred := enqueue(t, r, Spec{Kind: kindClassify})
	recv(t, entered, "attempt")
	waitJob(t, r, deferred.ID, "deferred", deferredOnce)
	got, err := r.Cancel(ctx, deferred.ID)
	if err != nil || got.State != domain.JobCancelled || got.FinishedAt == nil || got.TerminalDetail != "cancelled on request" {
		t.Fatalf("Cancel deferred = %+v, %v; want cancelled at once", got, err)
	}

	running := enqueue(t, r, Spec{Kind: kindClassify, Payload: json.RawMessage(`{"wait":true}`)})
	recv(t, entered, "waiting attempt")
	if _, err := r.Cancel(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, r, running.ID, "cancelled", inState(domain.JobCancelled))
	if final.Attempts != 0 || final.TerminalDetail != "cancelled on request" {
		t.Fatalf("job cancelled while running = %+v", final)
	}

	e.clock.Advance(time.Minute)
	tick(t, r)
	select {
	case job := <-entered:
		t.Fatalf("cancelled job %s ran again after its deferred time", job.ID)
	default:
	}
}
