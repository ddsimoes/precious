package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
)

// wakeJob runs Tx.WakeOnce for the interactive job of source "s" in its own
// write transaction.
func wakeJob(t *testing.T, r *Runner, payload string) (Record, bool) {
	t.Helper()
	var rec Record
	var coalesced bool
	err := r.Write(context.Background(), func(tx *Tx) (err error) {
		rec, coalesced, err = tx.WakeOnce(Spec{Kind: kindInteractive, SourceID: "s", ScopeKey: "interactive:s",
			Payload: json.RawMessage(payload)})
		return err
	})
	if err != nil {
		t.Fatalf("WakeOnce: %v", err)
	}
	return rec, coalesced
}

// Task 1.4 (design D7): WakeOnce creates the job when none is active. On a
// queued job it coalesces: an available_at later than now moves to now, with
// attempts and payload unchanged and the deferral no longer shown, recorded as
// one state event; an available_at already past is kept, so the job keeps its
// place in the claim order.
func TestWakeOnceCoalescesOntoQueuedJob(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "s")
	r := e.runner(t, nil) // never started: nothing claims the job
	now := clock.Millis(e.clock.Now())

	created, coalesced := wakeJob(t, r, `{"n":1}`)
	if coalesced || created.State != domain.JobQueued || created.Kind != kindInteractive || created.ScopeKey != "interactive:s" ||
		created.availableAt != now || string(created.Payload) != `{"n":1}` {
		t.Fatalf("WakeOnce with no active job = %+v available at %d, coalesced=%v; want a new queued job available at %d",
			created, created.availableAt, coalesced, now)
	}

	// Two attempts were lost to dead workers, then the handler deferred the
	// job for an hour.
	later := e.clock.Now().Add(time.Hour)
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE jobs SET attempts = 2, available_at = ?, terminal_detail = ? WHERE id = ?`,
			clock.Millis(later), deferDetail(later, "next poll"), int64(created.ID))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	now = clock.Millis(e.clock.Now())
	events := len(jobEvents(t, e.st, created.ID))

	woken, coalesced := wakeJob(t, r, `{"n":2}`)
	stored := mustGet(t, r, created.ID)
	for _, got := range []Record{woken, stored} {
		if !coalesced || got.ID != created.ID || got.State != domain.JobQueued || got.availableAt != now ||
			got.Attempts != 2 || string(got.Payload) != `{"n":1}` || got.TerminalDetail != "" {
			t.Fatalf("woken job = %+v available at %d, coalesced=%v; want job %s available at %d with 2 attempts, "+
				"its payload, and no deferral shown", got, got.availableAt, coalesced, created.ID, now)
		}
	}
	evs := jobEvents(t, e.st, created.ID)
	if len(evs) != events+1 {
		t.Fatalf("wake recorded %d events, want 1", len(evs)-events)
	}
	if last := evs[len(evs)-1]; last.Type != eventState || last.Event.State != domain.JobQueued || last.Event.Attempts != 2 {
		t.Fatalf("wake event = %+v, want a queued state event with 2 attempts", last)
	}

	e.clock.Advance(time.Minute)
	again, coalesced := wakeJob(t, r, `{}`)
	if !coalesced || again.ID != created.ID || again.availableAt != now || !again.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("waking an available job = %+v available at %d, coalesced=%v; want it unchanged, available at %d",
			again, again.availableAt, coalesced, now)
	}
	if n := len(jobEvents(t, e.st, created.ID)); n != len(evs) {
		t.Fatalf("waking an available job recorded %d events, want none", n-len(evs))
	}
}

// Task 1.4 (design D7): a job its handler deferred is claimed at once when
// WakeOnce wakes it, with the clock not advanced and no attempt used. Waking
// the running job returns it unchanged.
func TestWakeOnceClaimsDeferredJob(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "s")
	recordDevice(e.reg, "s", 7)
	var runs atomic.Int64
	ran := make(chan Job, 1)
	release := make(chan struct{})
	r := e.runner(t, nil)
	r.RegisterClass(kindInteractive, handlerFunc(func(ctx context.Context, job Job, _ Runtime) error {
		if runs.Add(1) == 1 {
			return &Defer{Until: e.clock.Now().Add(time.Hour), Reason: "next poll"}
		}
		ran <- job
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), ClassInteractive)
	start(t, r)

	first, _ := wakeJob(t, r, `{}`)
	waitJob(t, r, first.ID, "deferred", deferredOnce)
	tick(t, r) // every due job has been considered for dispatch
	if got := mustGet(t, r, first.ID); got.State != domain.JobQueued {
		t.Fatalf("deferred job is %s before the wake, want queued", got.State)
	}

	if woken, coalesced := wakeJob(t, r, `{}`); !coalesced || woken.ID != first.ID {
		t.Fatalf("wake = %s coalesced=%v, want job %s coalesced", woken.ID, coalesced, first.ID)
	}
	if job := recv(t, ran, "attempt of the woken job"); job.ID != first.ID || job.Attempt != 1 {
		t.Fatalf("attempt after the wake = %+v, want attempt 1 of job %s", job, first.ID)
	}

	running := mustGet(t, r, first.ID)
	events := len(jobEvents(t, e.st, first.ID))
	again, coalesced := wakeJob(t, r, `{}`)
	if !coalesced || again.ID != first.ID || again.State != domain.JobRunning || !again.UpdatedAt.Equal(running.UpdatedAt) {
		t.Fatalf("waking the running job = %+v coalesced=%v, want it unchanged", again, coalesced)
	}
	if n := len(jobEvents(t, e.st, first.ID)); n != events {
		t.Fatalf("waking the running job recorded %d events, want none", n-events)
	}
	close(release)
	if final := waitJob(t, r, first.ID, "succeeded", inState(domain.JobSucceeded)); final.Attempts != 0 {
		t.Fatalf("woken job used %d attempts, want none", final.Attempts)
	}
}
