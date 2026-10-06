package jobs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
)

// attempt is one running handler invocation. It implements Runtime.
type attempt struct {
	r      *Runner
	job    Job
	token  string // lease_owner value of this attempt
	ctx    context.Context
	cancel context.CancelCauseFunc
	class  Class // its kind's priority class (design D13)

	// Guarded by Runner.mu. slot is the slot the attempt holds, released
	// when it finishes: the one it was claimed under until UseSource moves
	// it to another key (design D13). yields is set while it holds a slot
	// of a shared device key and is not a placeholder, so that it offers
	// its slot in Yield.
	slot   slot
	yields bool
	// Guarded by Runner.mu. vacated is set while the attempt holds no slot
	// of its key, having given it up in Yield or UseSource; since is when it
	// did, and granted is closed when the arbiter hands it a slot. moved is
	// set while vacated after UseSource: the attempt then counts as running
	// in no source either, until the grant takes slot as a claim does.
	vacated bool
	moved   bool
	since   int64
	granted chan struct{}

	flushMu sync.Mutex // serializes progress writes

	mu        sync.Mutex
	finished  bool
	source    domain.SourceID // watched by FSCall: job.SourceID until UseSource moves the job
	deviceKey string          // persisted as jobs.device_key: the claim's key, then each move's
	progress  map[string]int64
	version   uint64 // incremented by every Progress call
	flushed   uint64 // version last persisted
	pending   int    // Progress calls since the last flush
	lastFlush time.Time
	lastRenew time.Time
	calls     map[uint64]*fsCall
	callSeq   uint64
}

type fsCall struct {
	op       string
	source   domain.SourceID // the attempt's source when the call started
	start    time.Time
	reported bool // the watchdog reported it
	flagged  bool // the watchdog flagged its source unresponsive
}

func (r *Runner) newAttempt(rec Record, s slot, class Class, now time.Time) *attempt {
	ctx, cancel := context.WithCancelCause(r.baseCtx)
	return &attempt{
		r: r,
		job: Job{
			ID:             rec.ID,
			Kind:           rec.Kind,
			PayloadVersion: rec.PayloadVersion,
			Payload:        rec.Payload,
			SourceID:       rec.SourceID,
			Attempt:        rec.Attempts + 1,
		},
		slot:      s,
		token:     rec.leaseOwner,
		ctx:       ctx,
		cancel:    cancel,
		class:     class,
		yields:    sharedKey(s.key) && !s.placeholder,
		source:    rec.SourceID,
		deviceKey: s.key,
		progress:  cloneProgress(rec.Progress),
		lastFlush: now,
		lastRenew: now,
		calls:     map[uint64]*fsCall{},
	}
}

// Progress implements Runtime: each key overwrites the stored counter; keys
// not passed keep their value. Progress is persisted after ProgressBatch calls
// or ProgressInterval, whichever comes first.
func (a *attempt) Progress(counters map[string]int64) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	for k, v := range counters {
		a.progress[k] = v
	}
	a.version++
	a.pending++
	full := a.pending >= a.r.progressBatch
	a.mu.Unlock()
	if full {
		a.flush()
	}
}

// FSCall implements Runtime: the call is tracked until done is called.
func (a *attempt) FSCall(op string) (done func()) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return func() {}
	}
	a.callSeq++
	id := a.callSeq
	c := &fsCall{op: op, source: a.source, start: a.r.clock.Now()}
	a.calls[id] = c
	a.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			_, tracked := a.calls[id]
			delete(a.calls, id)
			flagged := tracked && c.flagged
			a.mu.Unlock()
			if flagged {
				a.r.clearUnresponsive(c.source)
			}
		})
	}
}

// Yield implements Runtime (design D13). An attempt on a shared device key
// offers its slot to the jobs waiting for that key; a placeholder attempt, a
// pool attempt, and one without a source return at once. Yield returns nil
// at once unless the arbiter grants the slot to another job. Then the
// attempt gives its slot up and waits, still running and with its lease
// renewed, until the arbiter grants the slot back. When ctx (or the
// attempt's own context) ends first, Yield returns its error without a
// slot, and the handler only winds down.
func (a *attempt) Yield(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	r := a.r
	r.mu.Lock()
	yields := a.yields
	r.mu.Unlock()
	if !yields {
		return nil
	}
	if err := r.refreshQueued(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Without the queued jobs the arbiter cannot rank; the attempt
		// keeps its slot until its next Yield.
		r.log.Error("jobs: yield: read queued jobs", "job", a.job.ID, "err", err)
		return nil
	}
	r.mu.Lock()
	vacated := r.offerLocked(a, clock.Millis(r.clock.Now()))
	granted := a.granted
	r.mu.Unlock()
	if !vacated {
		return nil
	}
	return a.await(ctx, granted)
}

// UseSource implements Runtime (design D13). It computes source's key as a
// claim does (deviceKey). On the attempt's current key it returns at once,
// and only makes source the one FSCall watches. Otherwise the attempt
// releases its slot as finishing would and waits, still running and with
// its lease renewed, among the new key's waiters, in its class and from
// now, until the arbiter grants it a slot of that key as it grants one to a
// claim: a placeholder key also needs the source to have no other running
// attempt, and makes the source exclusive while the attempt holds it. The
// new key reaches jobs.device_key with the attempt's next progress write.
// When ctx (or the attempt's own context) ends first, UseSource returns its
// error holding no slot, and the handler only winds down.
func (a *attempt) UseSource(ctx context.Context, source domain.SourceID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	r := a.r
	r.mu.Lock()
	pool := strings.HasPrefix(a.slot.key, poolKeyPrefix)
	r.mu.Unlock()
	if pool {
		return fmt.Errorf("jobs: UseSource: job %s of pool kind %q cannot move to source %q", a.job.ID, a.job.Kind, source)
	}
	next, err := r.sourceSlot(ctx, source)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	var granted chan struct{} // nil: the attempt already holds a slot of next's key
	r.mu.Lock()
	if next.key != a.slot.key {
		r.moveLocked(a, next, clock.Millis(r.clock.Now()))
		granted = a.granted
	}
	r.mu.Unlock()
	if granted != nil {
		if err := a.await(ctx, granted); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.source, a.deviceKey = source, next.key
	a.mu.Unlock()
	return nil
}

// await waits for granted, closed when the arbiter hands the attempt a slot
// (Yield, UseSource). When ctx (or the attempt's own context) ends first,
// await returns its error and the attempt holds no slot; a slot granted
// meanwhile is released when the attempt finishes.
func (a *attempt) await(ctx context.Context, granted <-chan struct{}) error {
	r := a.r
	r.wakeUp()
	select {
	case <-granted:
		return nil
	case <-ctx.Done():
	case <-a.ctx.Done():
	}
	r.mu.Lock()
	r.arb.remove(a)
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.ctx.Err()
}

// overdue marks and returns the calls that just exceeded the watchdog. The
// caller holds Runner.unrespMu, so a call's done func cannot clear a flag
// before it is set.
func (a *attempt) overdue(now time.Time, watchdog time.Duration) []fsCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []fsCall
	for _, c := range a.calls {
		if !c.reported && now.Sub(c.start) >= watchdog {
			c.reported = true
			c.flagged = c.source != ""
			out = append(out, *c)
		}
	}
	return out
}

// releaseCalls stops tracking calls the handler never completed and returns
// the source of each flagged one, so its unresponsive flag is cleared.
func (a *attempt) releaseCalls() []domain.SourceID {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []domain.SourceID
	for id, c := range a.calls {
		if c.flagged {
			out = append(out, c.source)
		}
		delete(a.calls, id)
	}
	return out
}

// close ends the attempt's runtime and returns its final progress and
// device key.
func (a *attempt) close() (progress map[string]int64, deviceKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finished = true
	return cloneProgress(a.progress), a.deviceKey
}

func (a *attempt) renewDue(now time.Time, every time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return now.Sub(a.lastRenew) >= every
}

func (a *attempt) renewed(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastRenew = now
}

func (a *attempt) flushDue(now time.Time, every time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.version != a.flushed && now.Sub(a.lastFlush) >= every
}

// flush persists the current progress and device key and records a progress
// event, unless nothing changed since the last flush or the attempt no longer
// holds its lease.
func (a *attempt) flush() {
	a.flushMu.Lock()
	defer a.flushMu.Unlock()
	a.mu.Lock()
	if a.finished || a.version == a.flushed {
		a.mu.Unlock()
		return
	}
	snapshot := cloneProgress(a.progress)
	key := a.deviceKey
	version := a.version
	a.mu.Unlock()

	var now time.Time
	err := a.r.Write(context.WithoutCancel(a.r.baseCtx), func(t *Tx) error {
		now = t.now
		rec, err := t.Get(a.job.ID)
		if err != nil {
			return err
		}
		if rec.State != domain.JobRunning || rec.leaseOwner != a.token {
			return nil
		}
		next := rec
		next.Progress = snapshot
		next.DeviceKey = key
		_, err = t.change(rec, next, a.token, eventProgress)
		return err
	})
	if err != nil {
		a.r.log.Error("jobs: persist progress", "job", a.job.ID, "err", err)
		return
	}
	a.mu.Lock()
	a.flushed = version
	a.pending = 0
	a.lastFlush = now
	a.mu.Unlock()
}
