package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"precious/internal/clock"
	"precious/internal/domain"
)

// Spec describes a job to enqueue.
type Spec struct {
	Kind Kind
	// PayloadVersion defaults to 1.
	PayloadVersion int
	// Payload defaults to {}.
	Payload json.RawMessage
	// SourceID binds the job to a source. The runner claims it under the
	// source's device key, computed at claim time (design D14).
	SourceID domain.SourceID
	// ScopeKey, when set, allows one active job per (Kind, ScopeKey); see EnqueueOnce.
	ScopeKey string
}

// Tx is one write transaction over the job tables. Every job change made
// through it records a job_events row in the same transaction; subscribers,
// the scheduler, and running handlers are signalled after the commit.
type Tx struct {
	ctx context.Context
	tx  *sql.Tx
	now time.Time
	r   *Runner

	events  bool
	wake    bool
	cancels []domain.JobID
}

// Write runs fn in one write transaction and, once it commits, publishes the
// job events it wrote, wakes the scheduler, and cancels running attempts whose
// cancellation it requested. Callers may add their own statements through
// Tx.SQL so they commit atomically with the job changes.
func (r *Runner) Write(ctx context.Context, fn func(tx *Tx) error) error {
	var t *Tx
	err := r.store.Write(ctx, func(tx *sql.Tx) error {
		t = &Tx{ctx: ctx, tx: tx, now: r.clock.Now(), r: r}
		return fn(t)
	})
	if err != nil {
		return err
	}
	t.afterCommit()
	return nil
}

func (t *Tx) afterCommit() {
	if t.events {
		t.r.hub.notify()
	}
	if t.wake {
		t.r.queueGen.Add(1)
		t.r.wakeUp()
	}
	for _, id := range t.cancels {
		t.r.cancelAttempt(id, errCancelRequested)
	}
}

// SQL is the underlying transaction.
func (t *Tx) SQL() *sql.Tx { return t.tx }

// Now is the transaction's timestamp.
func (t *Tx) Now() time.Time { return t.now }

// Get loads one job; a missing job is a domain not_found error.
func (t *Tx) Get(id domain.JobID) (Record, error) { return getRecord(t.ctx, t.tx, id) }

// Enqueue inserts a queued job. A second active scan for one source violates
// the jobs_one_active_scan index, and a second active job with the same kind
// and scope key violates jobs_one_active_scope; use StartScan and EnqueueOnce.
func (t *Tx) Enqueue(spec Spec) (Record, error) {
	if spec.Kind == "" {
		return Record{}, errors.New("jobs: enqueue: empty kind")
	}
	version := spec.PayloadVersion
	if version == 0 {
		version = 1
	}
	payload := spec.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if !json.Valid(payload) {
		return Record{}, errors.New("jobs: enqueue: payload is not valid JSON")
	}
	now := clock.Millis(t.now)
	var id int64
	err := t.tx.QueryRowContext(t.ctx, `INSERT INTO jobs (kind, payload_version, payload, source_id,
		scope_key, state, max_attempts, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?) RETURNING id`,
		string(spec.Kind), version, string(payload), optString(string(spec.SourceID)),
		optString(spec.ScopeKey), t.r.cfg.MaxAttempts, now, now, now).Scan(&id)
	if err != nil {
		return Record{}, err
	}
	rec, err := t.Get(domain.JobID(id))
	if err != nil {
		return Record{}, err
	}
	if err := t.event(eventState, rec); err != nil {
		return Record{}, err
	}
	t.wake = true
	return rec, nil
}

// StartScan returns the active scan of sourceID or enqueues a new one with the
// default payload. See StartScanWith.
func (t *Tx) StartScan(sourceID domain.SourceID) (rec Record, coalesced bool, err error) {
	return t.StartScanWith(Spec{Kind: KindScan, SourceID: sourceID})
}

// StartScanWith returns the active scan of spec.SourceID, whatever its
// payload, or enqueues spec (which must be a scan) when there is none. A paused
// scan is resumed. coalesced reports that an existing job was returned. A
// concurrent insert that wins the jobs_one_active_scan index is read back
// instead of creating a second job.
func (t *Tx) StartScanWith(spec Spec) (rec Record, coalesced bool, err error) {
	if spec.Kind != KindScan || spec.SourceID == "" {
		return Record{}, false, errors.New("jobs: StartScanWith needs a scan spec with a source")
	}
	return t.once(spec, func() (Record, bool, error) { return t.ActiveScan(spec.SourceID) })
}

// EnqueueOnce returns the active job with spec's kind and scope key, or
// enqueues spec when there is none. A paused job is resumed. coalesced reports
// that an existing job was returned. spec.ScopeKey is required.
func (t *Tx) EnqueueOnce(spec Spec) (rec Record, coalesced bool, err error) {
	if spec.ScopeKey == "" {
		return Record{}, false, errors.New("jobs: EnqueueOnce needs a scope key")
	}
	return t.once(spec, func() (Record, bool, error) { return t.activeScoped(spec.Kind, spec.ScopeKey) })
}

// WakeOnce is EnqueueOnce that also makes a queued job whose available_at is
// later than now, such as a deferred job, available now, without changing its
// attempts or payload. The deferral it ends no longer shows in TerminalDetail,
// and the scheduler is woken to claim the job. It creates the job when no
// active job has spec's kind and scope key; a running or paused job is handled
// as EnqueueOnce handles it.
func (t *Tx) WakeOnce(spec Spec) (rec Record, coalesced bool, err error) {
	rec, coalesced, err = t.EnqueueOnce(spec)
	now := clock.Millis(t.now)
	if err != nil || !coalesced || rec.State != domain.JobQueued || rec.availableAt <= now {
		return rec, coalesced, err
	}
	next := rec
	next.availableAt = now
	next.TerminalDetail = ""
	if rec, err = t.change(rec, next, "", eventState); err != nil {
		return Record{}, false, err
	}
	t.wake = true
	return rec, true, nil
}

func (t *Tx) once(spec Spec, active func() (Record, bool, error)) (rec Record, coalesced bool, err error) {
	for range 2 {
		var found bool
		rec, found, err = active()
		if err != nil {
			return Record{}, false, err
		}
		if found {
			if rec.State == domain.JobPaused {
				rec, err = t.resume(rec)
			}
			return rec, true, err
		}
		rec, err = t.Enqueue(spec)
		if err == nil {
			return rec, false, nil
		}
		if !isUniqueViolation(err) {
			return Record{}, false, err
		}
	}
	return Record{}, false, fmt.Errorf("jobs: %s job for %q: active job neither found nor created", spec.Kind, spec.SourceID)
}

// ActiveScan returns the queued, running, or paused scan of source, if any.
func (t *Tx) ActiveScan(source domain.SourceID) (Record, bool, error) {
	return t.activeWhere(`source_id = ? AND kind = 'scan'`, string(source))
}

func (t *Tx) activeScoped(kind Kind, scopeKey string) (Record, bool, error) {
	return t.activeWhere(`kind = ? AND scope_key = ?`, string(kind), scopeKey)
}

func (t *Tx) activeWhere(where string, args ...any) (Record, bool, error) {
	rec, err := scanRecord(t.tx.QueryRowContext(t.ctx, `SELECT `+recordColumns+` FROM jobs
		WHERE `+where+` AND state IN ('queued', 'running', 'paused')`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	return rec, err == nil, err
}

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// Cancel requests cancellation. A queued or paused job is cancelled at once; a
// running job keeps running, shown as cancel_requested, until its handler
// returns; a terminal job is returned unchanged.
func (t *Tx) Cancel(id domain.JobID) (Record, error) {
	rec, err := t.Get(id)
	if err != nil {
		return Record{}, err
	}
	next := rec
	next.CancelRequested = true
	switch rec.State {
	case domain.JobQueued, domain.JobPaused:
		next.State = domain.JobCancelled
		next.TerminalDetail = "cancelled on request"
		next.FinishedAt = &t.now
	case domain.JobRunning:
		t.cancels = append(t.cancels, id)
		if rec.CancelRequested {
			return rec, nil
		}
	default:
		return rec, nil
	}
	return t.change(rec, next, "", eventState)
}

// Resume requeues a paused job. Queued and running jobs are returned
// unchanged; a terminal job cannot be resumed.
func (t *Tx) Resume(id domain.JobID) (Record, error) {
	rec, err := t.Get(id)
	if err != nil {
		return Record{}, err
	}
	switch {
	case rec.State == domain.JobPaused:
		return t.resume(rec)
	case rec.State.Terminal():
		return Record{}, domain.Errorf(domain.CodeInvalidRequest, "job %s is %s and cannot be resumed", id, rec.State)
	default:
		return rec, nil
	}
}

func (t *Tx) resume(rec Record) (Record, error) {
	next := rec
	next.State = domain.JobQueued
	next.PauseReason = ""
	next.TerminalDetail = ""
	next.availableAt = clock.Millis(t.now)
	return t.change(rec, next, "", eventState)
}

// change persists next over prev (guarded by prev's state and, when owner is
// set, by the lease owner) and records an event of type typ. A lost guard is
// reported as errStale.
func (t *Tx) change(prev, next Record, owner, typ string) (Record, error) {
	next.UpdatedAt = t.now
	ok, err := saveRecord(t.ctx, t.tx, prev.State, owner, next)
	if err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, errStale
	}
	if next.State == domain.JobQueued && prev.State != domain.JobQueued {
		t.wake = true
	}
	return next, t.event(typ, next)
}

func (t *Tx) event(typ string, rec Record) error {
	t.events = true
	return insertEvent(t.ctx, t.tx, typ, rec, t.now)
}

// errStale reports that a job changed underneath an update (another writer
// moved it, or the lease passed to another worker).
var errStale = errors.New("jobs: job changed concurrently")
