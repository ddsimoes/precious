package jobs

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"precious/internal/domain"
)

const kindWalk Kind = "walk"

// Job-runner spec "One active aggregate walk per node": EnqueueOnce coalesces
// per (kind, scope key), resumes a paused job, and starts afresh once the
// previous job finished.
func TestEnqueueOnceCoalescesPerScope(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	var runs atomic.Int64
	h := handlerFunc(func(context.Context, Job, Runtime) error {
		if runs.Add(1) == 1 {
			return &Pause{Reason: "test_pause"}
		}
		return nil
	})
	r := e.runner(t, map[Kind]Handler{kindWalk: h})
	ctx := context.Background()
	once := func(scope string) (Record, bool) {
		t.Helper()
		var rec Record
		var coalesced bool
		err := r.Write(ctx, func(tx *Tx) (err error) {
			rec, coalesced, err = tx.EnqueueOnce(Spec{Kind: kindWalk, SourceID: "a",
				ScopeKey: scope, Payload: json.RawMessage(`{"node_id":"7"}`)})
			return err
		})
		if err != nil {
			t.Fatalf("EnqueueOnce(%s): %v", scope, err)
		}
		return rec, coalesced
	}

	first, coalesced := once("node:7")
	if coalesced || first.ScopeKey != "node:7" || first.State != domain.JobQueued {
		t.Fatalf("first = %+v coalesced=%v", first, coalesced)
	}
	if again, coalesced := once("node:7"); !coalesced || again.ID != first.ID {
		t.Fatalf("repeat = %s coalesced=%v, want %s coalesced", again.ID, coalesced, first.ID)
	}
	other, coalesced := once("node:8")
	if coalesced || other.ID == first.ID {
		t.Fatalf("other scope = %s coalesced=%v, want a new job", other.ID, coalesced)
	}
	err := r.Write(ctx, func(tx *Tx) error {
		_, err := tx.Enqueue(Spec{Kind: kindWalk, SourceID: "a", ScopeKey: "node:7"})
		return err
	})
	if !isUniqueViolation(err) {
		t.Fatalf("second active scoped insert: err = %v, want unique violation", err)
	}
	if err := r.Write(ctx, func(tx *Tx) error {
		_, err := tx.Cancel(other.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	start(t, r)
	waitJob(t, r, first.ID, "paused", inState(domain.JobPaused))
	if resumed, coalesced := once("node:7"); !coalesced || resumed.ID != first.ID || resumed.State != domain.JobQueued {
		t.Fatalf("while paused = %+v coalesced=%v, want resumed", resumed, coalesced)
	}
	waitJob(t, r, first.ID, "succeeded", inState(domain.JobSucceeded))
	if next, coalesced := once("node:7"); coalesced || next.ID == first.ID {
		t.Fatalf("after completion = %s coalesced=%v, want a new job", next.ID, coalesced)
	}
}

func TestEnqueueOnceRequiresScope(t *testing.T) {
	e := newEnv(t)
	r := e.runner(t, nil)
	err := r.Write(context.Background(), func(tx *Tx) error {
		_, _, err := tx.EnqueueOnce(Spec{Kind: kindWalk})
		return err
	})
	if err == nil {
		t.Fatal("EnqueueOnce without a scope key succeeded")
	}
}
