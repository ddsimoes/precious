package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/store"
)

// Options configures NewRunner.
type Options struct {
	Store *store.Store
	// Registry supplies device keys and receives the watchdog's
	// unresponsive-source flags; nil claims every source under its
	// placeholder key.
	Registry Registry
	// Config holds leases, attempts, per-device workers, the watchdog, and
	// event retention. Zero fields take the config.Defaults values.
	Config config.Jobs
	// Clock drives every time-based decision. Default clock.Real.
	Clock  clock.Clock
	Logger *slog.Logger
	// TickInterval is how often the runner polls for due jobs, renews leases,
	// requeues expired ones, runs the watchdog, flushes progress, and applies
	// event retention. Default 1s.
	TickInterval time.Duration
	// ProgressBatch flushes a job's progress after this many Progress calls.
	// Default 1000.
	ProgressBatch int
	// ProgressInterval flushes pending progress at least this often. Default 1s.
	ProgressInterval time.Duration
}

// pruneInterval is how often event retention runs.
const pruneInterval = time.Minute

// Cancellation causes of an attempt's context.
var (
	errCancelRequested = errors.New("jobs: cancellation requested")
	errLeaseLost       = errors.New("jobs: lease lost")
	errShutdown        = errors.New("jobs: runner stopping")
	errAttemptDone     = errors.New("jobs: attempt finished")
)

// Runner is the single in-process scheduler (design D11). It claims queued
// jobs under a lease, runs their handlers within per-device worker limits or
// their pool's capacity, renews leases, recovers jobs of lost workers, and
// publishes job events.
type Runner struct {
	store            *store.Store
	reg              Registry
	cfg              config.Jobs
	clock            clock.Clock
	log              *slog.Logger
	tickInterval     time.Duration
	progressBatch    int
	progressInterval time.Duration
	owner            string
	hub              *hub
	wake             chan struct{}

	mu       sync.Mutex
	handlers map[Kind]Handler
	classes  map[Kind]Class  // priority class of each device-key kind (design D13)
	pools    map[Kind]string // claim key "workers:<name>" of each pool kind
	started  bool
	stopped  bool
	attempts map[domain.JobID]*attempt
	slots    *deviceSlots
	arb      arbiter
	seq      uint64

	// queueGen counts the commits that made a job queued; the arbiter reads
	// the queued jobs again when it has not seen the latest one.
	queueGen atomic.Uint64

	unrespMu     sync.Mutex
	unresponsive map[domain.SourceID]int

	lastPrune time.Time

	baseCtx    context.Context
	baseCancel context.CancelCauseFunc
	loopCancel context.CancelFunc
	loopDone   chan struct{}
	wg         sync.WaitGroup

	// manualTick replaces the periodic ticker when non-nil (tests): the loop
	// runs one tick per received channel and closes it afterwards.
	manualTick chan chan struct{}
}

// NewRunner validates opts and returns a stopped runner. Register handlers,
// then call Start.
func NewRunner(opts Options) (*Runner, error) {
	if opts.Store == nil {
		return nil, errors.New("jobs: NewRunner: nil Store")
	}
	cfg := withDefaults(opts.Config)
	r := &Runner{
		store:            opts.Store,
		reg:              opts.Registry,
		cfg:              cfg,
		clock:            opts.Clock,
		log:              opts.Logger,
		tickInterval:     opts.TickInterval,
		progressBatch:    opts.ProgressBatch,
		progressInterval: opts.ProgressInterval,
		hub:              newHub(),
		wake:             make(chan struct{}, 1),
		handlers:         map[Kind]Handler{},
		classes:          map[Kind]Class{},
		pools:            map[Kind]string{},
		attempts:         map[domain.JobID]*attempt{},
		slots:            newDeviceSlots(cfg.WorkersPerDevice),
		arb:              newArbiter(),
		unresponsive:     map[domain.SourceID]int{},
	}
	if r.clock == nil {
		r.clock = clock.Real{}
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.tickInterval <= 0 {
		r.tickInterval = time.Second
	}
	if r.progressBatch <= 0 {
		r.progressBatch = 1000
	}
	if r.progressInterval <= 0 {
		r.progressInterval = time.Second
	}
	host, _ := os.Hostname()
	var nonce [6]byte
	_, _ = rand.Read(nonce[:])
	r.owner = fmt.Sprintf("%s:%d:%s", host, os.Getpid(), hex.EncodeToString(nonce[:]))
	return r, nil
}

func withDefaults(c config.Jobs) config.Jobs {
	d := config.Defaults().Jobs
	if c.WorkersPerDevice <= 0 {
		c.WorkersPerDevice = d.WorkersPerDevice
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.Lease.Duration <= 0 {
		c.Lease = d.Lease
	}
	if c.LeaseRenew.Duration <= 0 {
		c.LeaseRenew = d.LeaseRenew
	}
	if c.CallWatchdog.Duration <= 0 {
		c.CallWatchdog = d.CallWatchdog
	}
	if c.EventRetentionRows <= 0 {
		c.EventRetentionRows = d.EventRetentionRows
	}
	if c.EventRetentionAge.Duration <= 0 {
		c.EventRetentionAge = d.EventRetentionAge
	}
	return c
}

// Register installs the handler of kind, of class ClassReconciliation. It
// must be called before Start; registering a kind twice panics.
func (r *Runner) Register(kind Kind, h Handler) {
	r.register("Register", kind, h, ClassReconciliation)
}

// RegisterClass registers h for kind with a priority class (design D13).
// Register(kind, h) is RegisterClass(kind, h, ClassReconciliation). It panics
// as Register does. Pool kinds (RegisterPool) have no class.
func (r *Runner) RegisterClass(kind Kind, h Handler, class Class) {
	r.register("RegisterClass", kind, h, class)
}

func (r *Runner) register(method string, kind Kind, h Handler, class Class) {
	if class < ClassReconciliation || class > ClassBulk {
		panic(fmt.Sprintf("jobs: %s(%q): unknown class %d", method, kind, class))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkRegister(method, kind)
	r.handlers[kind] = h
	r.classes[kind] = class
}

// poolKeyPrefix starts the claim key of a pool, "workers:<name>". Registry
// device keys never start with it.
const poolKeyPrefix = "workers:"

// RegisterPool registers h for kind, claimed under the slot key "workers:<pool>"
// with its own capacity instead of a per-device filesystem slot (design D11).
// Pool jobs never take part in placeholder exclusivity and never hold a
// device slot. Kinds registered with one pool share its capacity. Like
// Register, it must be called before Start, and registering a kind twice
// panics; so does a capacity below 1, or a pool given two capacities.
func (r *Runner) RegisterPool(kind Kind, h Handler, pool string, capacity int) {
	if capacity < 1 {
		panic(fmt.Sprintf("jobs: pool %q capacity %d is below 1", pool, capacity))
	}
	key := poolKeyPrefix + pool
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkRegister("RegisterPool", kind)
	if !r.slots.addPool(key, capacity) {
		panic(fmt.Sprintf("jobs: pool %q registered with capacity %d and %d", pool, r.slots.capacityOf(key), capacity))
	}
	r.handlers[kind] = h
	r.pools[kind] = key
}

// checkRegister panics when kind may not be registered now. The caller holds r.mu.
func (r *Runner) checkRegister(method string, kind Kind) {
	if r.started {
		panic("jobs: " + method + " after Start")
	}
	if _, dup := r.handlers[kind]; dup {
		panic(fmt.Sprintf("jobs: handler for %q registered twice", kind))
	}
}

// Start requeues every job a previous process left running (attempts+1, or
// failed with attempts_exhausted at the maximum) and starts scheduling. Jobs
// run until Stop or until ctx is cancelled.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("jobs: runner already started")
	}
	r.started = true
	r.mu.Unlock()

	if err := r.recoverOrphans(ctx); err != nil {
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
		return fmt.Errorf("jobs: recover running jobs: %w", err)
	}
	r.baseCtx, r.baseCancel = context.WithCancelCause(ctx)
	loopCtx, loopCancel := context.WithCancel(ctx)
	r.loopCancel = loopCancel
	r.loopDone = make(chan struct{})
	go r.loop(loopCtx)
	return nil
}

// Stop stops claiming jobs, cancels running handlers, and waits for them to
// return. Interrupted jobs go back to queued without using an attempt. If ctx
// ends first, Stop returns its error and the remaining jobs stay running in
// the database, to be recovered at the next start.
func (r *Runner) Stop(ctx context.Context) error {
	r.mu.Lock()
	if !r.started || r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	r.mu.Unlock()

	r.loopCancel()
	<-r.loopDone
	r.baseCancel(errShutdown)
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Enqueue inserts a queued job.
func (r *Runner) Enqueue(ctx context.Context, spec Spec) (rec Record, err error) {
	err = r.Write(ctx, func(t *Tx) error {
		rec, err = t.Enqueue(spec)
		return err
	})
	return rec, err
}

// Cancel requests cancellation of a job (see Tx.Cancel).
func (r *Runner) Cancel(ctx context.Context, id domain.JobID) (rec Record, err error) {
	err = r.Write(ctx, func(t *Tx) error {
		rec, err = t.Cancel(id)
		return err
	})
	return rec, err
}

// Resume requeues a paused job (see Tx.Resume).
func (r *Runner) Resume(ctx context.Context, id domain.JobID) (rec Record, err error) {
	err = r.Write(ctx, func(t *Tx) error {
		rec, err = t.Resume(id)
		return err
	})
	return rec, err
}

// Get reads one job from the read pool; a missing job is a domain not_found error.
func (r *Runner) Get(ctx context.Context, id domain.JobID) (Record, error) {
	return getRecord(ctx, r.store.Reader(), id)
}

// Yielded returns how many running jobs are blocked in Runtime.Yield or
// Runtime.UseSource, waiting for the arbiter to grant them a device slot
// (design D13). Such a job is running in the database but performs no work
// until then.
func (r *Runner) Yielded() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, list := range r.arb.yielded {
		n += len(list)
	}
	return n
}

func (r *Runner) wakeUp() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) loop(ctx context.Context) {
	defer close(r.loopDone)
	var tick <-chan time.Time
	if r.manualTick == nil {
		t := time.NewTicker(r.tickInterval)
		defer t.Stop()
		tick = t.C
	}
	r.dispatch(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			r.dispatch(ctx)
		case <-tick:
			r.tick(ctx)
		case done := <-r.manualTick:
			r.tick(ctx)
			close(done)
		}
	}
}

// tick runs the periodic duties. Every decision uses the injected clock.
func (r *Runner) tick(ctx context.Context) {
	now := r.clock.Now()
	r.renewLeases(ctx, now)
	r.requeueExpired(ctx, now)
	r.watchdog(now)
	r.flushDue(now)
	if now.Sub(r.lastPrune) >= pruneInterval {
		if err := r.pruneEvents(ctx, now); err != nil {
			r.log.Error("jobs: event retention", "err", err)
		} else {
			r.lastPrune = now
		}
	}
	r.dispatch(ctx)
}

func (r *Runner) pruneEvents(ctx context.Context, now time.Time) error {
	return r.store.Write(ctx, func(tx *sql.Tx) error {
		return pruneEvents(ctx, tx, now, r.cfg.EventRetentionRows, r.cfg.EventRetentionAge.Duration)
	})
}

// recoverOrphans requeues the jobs a previous process left running.
func (r *Runner) recoverOrphans(ctx context.Context) error {
	return r.Write(ctx, func(t *Tx) error {
		recs, err := t.selectRecords(`state = 'running'`)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			next, err := t.requeueLost(rec)
			if err != nil {
				return err
			}
			r.log.Warn("jobs: requeued job of a lost worker", "job", rec.ID, "state", next.State, "attempts", next.Attempts)
		}
		return nil
	})
}

// requeueExpired requeues running jobs whose lease expired: their worker
// stopped renewing (it hung, or its process died).
func (r *Runner) requeueExpired(ctx context.Context, now time.Time) {
	var expired bool
	err := r.store.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs
		WHERE state = 'running' AND lease_expires_at < ?)`, clock.Millis(now)).Scan(&expired)
	if err != nil {
		r.log.Error("jobs: lease expiry check", "err", err)
		return
	}
	if !expired {
		return
	}
	err = r.Write(ctx, func(t *Tx) error {
		recs, err := t.selectRecords(`state = 'running' AND lease_expires_at < ?`, clock.Millis(t.now))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			next, err := t.requeueLost(rec)
			if err != nil {
				return err
			}
			r.log.Warn("jobs: lease expired; requeued", "job", rec.ID, "owner", rec.leaseOwner, "state", next.State, "attempts", next.Attempts)
		}
		return nil
	})
	if err != nil {
		r.log.Error("jobs: requeue expired leases", "err", err)
	}
}

func (t *Tx) selectRecords(where string, args ...any) ([]Record, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT `+recordColumns+` FROM jobs WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// requeueLost ends an attempt whose worker is gone: the job is queued again
// with one more attempt counted, fails with attempts_exhausted once the count
// reaches the maximum, or is cancelled when cancellation was requested.
func (t *Tx) requeueLost(rec Record) (Record, error) {
	next := rec
	next.Attempts++
	next.leaseOwner = ""
	next.leaseExpires = 0
	switch {
	case rec.CancelRequested:
		next.State = domain.JobCancelled
		next.TerminalDetail = "cancelled on request; worker lost"
		next.FinishedAt = &t.now
	case next.Attempts >= rec.MaxAttempts:
		next.State = domain.JobFailed
		next.TerminalCode = domain.CodeAttemptsExhausted
		next.TerminalDetail = fmt.Sprintf("worker lost on all %d attempts", next.Attempts)
		next.FinishedAt = &t.now
	default:
		next.State = domain.JobQueued
		next.availableAt = clock.Millis(t.now)
	}
	return t.change(rec, next, rec.leaseOwner, eventState)
}

// dispatch grants free slots while jobs wait for them: it starts the queued
// jobs it claims, and hands slots to jobs blocked in Yield or UseSource.
func (r *Runner) dispatch(ctx context.Context) {
	for ctx.Err() == nil {
		a, h, again, err := r.claim(ctx)
		if err != nil {
			r.log.Error("jobs: claim", "err", err)
			return
		}
		if a == nil {
			if !again {
				return
			}
			continue
		}
		r.wg.Add(1)
		go r.run(a, h)
	}
}

// deviceKey is the key a job of source is claimed under (design D15): the
// registry's device key, or the placeholder "source:<id>" while the device
// is unknown (no registry, a registry error, or an empty key). q is the view
// the claim is decided on.
func (r *Runner) deviceKey(ctx context.Context, q store.Queryer, source domain.SourceID) slot {
	if r.reg != nil {
		key, err := r.reg.DeviceKey(ctx, q, source)
		if err == nil && key != "" {
			return slot{key: key, source: source}
		}
		if err != nil && ctx.Err() == nil {
			r.log.Warn("jobs: device key unknown; claiming under the source's placeholder", "source", source, "err", err)
		}
	}
	return slot{key: "source:" + string(source), source: source, placeholder: true}
}

// sourceSlot is the slot a job of source is claimed under (deviceKey), for
// UseSource. A source with no row is a domain unknown_source error.
func (r *Runner) sourceSlot(ctx context.Context, source domain.SourceID) (slot, error) {
	q := r.store.Reader()
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM sources WHERE id = ?`, string(source)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return slot{}, domain.Errorf(domain.CodeUnknownSource, "unknown source %q", source)
	}
	if err != nil {
		return slot{}, err
	}
	return r.deviceKey(ctx, q, source), nil
}

// claim grants one free slot to the job the arbiter picks (nextGrantLocked,
// design D13). A queued job moves to running under a fresh lease and is
// returned with its handler; a job blocked in Yield or UseSource gets its
// slot, which again reports. The job's key is its pool's "workers:<name>"
// for a pool kind (design D11), and otherwise its source's device key
// (deviceKey), decided in the claim; it is recorded in jobs.device_key. A
// queued job may start when its key has a free slot and, unless it is a
// pool job, its source is not exclusive; a placeholder job also needs its
// source to have no running attempt, and then makes the source exclusive
// until it finishes (deviceSlots). Pool jobs are never placeholders and
// count toward no source's running attempts.
func (r *Runner) claim(ctx context.Context) (a *attempt, h Handler, again bool, err error) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil, nil, false, nil
	}
	snap := r.snapshotLocked()
	r.mu.Unlock()

	// Decide on the read pool first, so that an idle poll, or a slot handed
	// back to a yielded job, takes no write lock.
	now := r.clock.Now()
	heads, err := r.queryHeads(ctx, r.store.Reader(), snap, now)
	if err != nil {
		return nil, nil, false, err
	}
	r.mu.Lock()
	r.setQueuedLocked(heads, snap.gen)
	g, found := r.nextGrantLocked(clock.Millis(now))
	if found && g.w.att != nil {
		again = r.grantLocked(g)
	}
	r.mu.Unlock()
	if !found || g.w.att != nil {
		return nil, nil, again, nil
	}

	var (
		reserved bool
		s        slot
	)
	err = r.Write(ctx, func(t *Tx) error {
		// Decide again on the transaction's snapshot, which no job change
		// can overtake before the claim commits.
		heads, err := r.queryHeads(t.ctx, t.tx, snap, t.now)
		if err != nil {
			return err
		}
		var token string
		r.mu.Lock()
		r.setQueuedLocked(heads, snap.gen)
		g, found := r.nextGrantLocked(clock.Millis(t.now))
		switch {
		case !found:
		case g.w.att != nil:
			again = r.grantLocked(g)
		default:
			s = g.w.slot
			reserved = r.grantLocked(g)
			r.seq++
			token = fmt.Sprintf("%s#%d", r.owner, r.seq)
		}
		r.mu.Unlock()
		if !reserved {
			return nil
		}
		rec, err := t.Get(g.w.id)
		if err != nil {
			return err
		}
		r.mu.Lock()
		h = r.handlers[rec.Kind]
		class := r.classes[rec.Kind]
		r.mu.Unlock()
		next := rec
		next.State = domain.JobRunning
		next.DeviceKey = s.key
		next.TerminalDetail = "" // the deferral that queued it, if any, is over
		next.leaseOwner = token
		next.leaseExpires = clock.Millis(t.now.Add(r.cfg.Lease.Duration))
		if next.StartedAt == nil {
			next.StartedAt = &t.now
		}
		next, err = t.change(rec, next, "", eventState)
		if err != nil {
			return err
		}
		a = r.newAttempt(next, s, class, t.now)
		return nil
	})
	if err != nil || a == nil {
		if reserved {
			r.mu.Lock()
			r.slots.release(s, true)
			r.mu.Unlock()
		}
		return nil, nil, again, err
	}
	r.mu.Lock()
	r.attempts[a.job.ID] = a
	r.mu.Unlock()
	return a, h, false, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func (r *Runner) run(a *attempt, h Handler) {
	defer r.wg.Done()
	err := runHandler(a.ctx, h, a.job, a)
	r.finish(a, err)
}

func runHandler(ctx context.Context, h Handler, job Job, rt Runtime) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("jobs: handler panic: %v\n%s", p, debug.Stack())
		}
	}()
	return h.Run(ctx, job, rt)
}

// finish records the outcome of an attempt (contract.go's Handler mapping)
// unless its lease passed to another worker meanwhile. A deferral is queued
// for its time without using an attempt, even when the runner is stopping.
func (r *Runner) finish(a *attempt, herr error) {
	progress, deviceKey := a.close()
	interrupted := a.ctx.Err() != nil
	cause := context.Cause(a.ctx)
	a.cancel(errAttemptDone)

	var stale bool
	err := r.Write(context.WithoutCancel(r.baseCtx), func(t *Tx) error {
		rec, err := t.Get(a.job.ID)
		if err != nil {
			return err
		}
		if rec.State != domain.JobRunning || rec.leaseOwner != a.token {
			stale = true
			return nil
		}
		next := rec
		next.Progress = progress
		next.DeviceKey = deviceKey
		next.leaseOwner = ""
		next.leaseExpires = 0
		var pause *Pause
		var deferral *Defer
		var de *domain.Error
		switch {
		case herr == nil:
			next.State = domain.JobSucceeded
		case rec.CancelRequested:
			next.State = domain.JobCancelled
			next.TerminalDetail = "cancelled on request"
		case errors.As(herr, &deferral):
			until := max(clock.Millis(deferral.Until), clock.Millis(t.now))
			next.State = domain.JobQueued
			next.availableAt = until
			next.TerminalDetail = deferDetail(clock.FromMillis(until), deferral.Reason)
		case interrupted:
			// Stopped by shutdown: run it again later without using an attempt.
			next.State = domain.JobQueued
			next.availableAt = clock.Millis(t.now)
		case errors.As(herr, &pause):
			next.State = domain.JobPaused
			next.PauseReason = pause.Reason
			next.TerminalDetail = pause.Detail
		case errors.As(herr, &de):
			next.State = domain.JobFailed
			next.TerminalCode = de.Code
			next.TerminalDetail = de.Message
		default:
			next.State = domain.JobFailed
			next.TerminalCode = domain.CodeInternal
			next.TerminalDetail = "internal error"
		}
		if next.State.Terminal() {
			next.FinishedAt = &t.now
		}
		_, err = t.change(rec, next, a.token, eventState)
		if err == nil && next.State == domain.JobFailed {
			r.log.Error("jobs: job failed", "job", rec.ID, "kind", rec.Kind, "code", next.TerminalCode, "err", herr)
		}
		return err
	})
	switch {
	case err != nil:
		r.log.Error("jobs: record job outcome", "job", a.job.ID, "handler_err", herr, "err", err)
	case stale:
		r.log.Warn("jobs: lease lost; attempt outcome discarded", "job", a.job.ID, "cause", cause, "handler_err", herr)
	}

	for _, src := range a.releaseCalls() {
		r.clearUnresponsive(src)
	}
	r.mu.Lock()
	if r.attempts[a.job.ID] == a {
		delete(r.attempts, a.job.ID)
	}
	r.releaseLocked(a)
	r.mu.Unlock()
	r.wakeUp()
}

func (r *Runner) cancelAttempt(id domain.JobID, cause error) {
	r.mu.Lock()
	a := r.attempts[id]
	r.mu.Unlock()
	if a != nil {
		a.cancel(cause)
	}
}

func (r *Runner) running() []*attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*attempt, 0, len(r.attempts))
	for _, a := range r.attempts {
		out = append(out, a)
	}
	return out
}

// renewLeases extends the leases that are due for renewal. A lost lease
// cancels its attempt; a cancel flag set in the database cancels it too.
func (r *Runner) renewLeases(ctx context.Context, now time.Time) {
	var due []*attempt
	for _, a := range r.running() {
		if a.renewDue(now, r.cfg.LeaseRenew.Duration) {
			due = append(due, a)
		}
	}
	if len(due) == 0 {
		return
	}
	var lost, cancelled, renewed []*attempt
	err := r.store.Write(ctx, func(tx *sql.Tx) error {
		lost, cancelled, renewed = nil, nil, nil
		for _, a := range due {
			var cancel bool
			err := tx.QueryRowContext(ctx, `UPDATE jobs SET lease_expires_at = ?
				WHERE id = ? AND state = 'running' AND lease_owner = ? RETURNING cancel_requested`,
				clock.Millis(now.Add(r.cfg.Lease.Duration)), int64(a.job.ID), a.token).Scan(&cancel)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				lost = append(lost, a)
			case err != nil:
				return err
			default:
				renewed = append(renewed, a)
				if cancel {
					cancelled = append(cancelled, a)
				}
			}
		}
		return nil
	})
	if err != nil {
		r.log.Error("jobs: renew leases", "err", err)
		return
	}
	for _, a := range renewed {
		a.renewed(now)
	}
	for _, a := range lost {
		r.log.Warn("jobs: lease lost", "job", a.job.ID)
		a.cancel(errLeaseLost)
	}
	for _, a := range cancelled {
		a.cancel(errCancelRequested)
	}
}

// watchdog flags the source of every filesystem call that has been in flight
// longer than the call watchdog. Holding unrespMu while calls are marked
// keeps a racing done func from clearing the flag before it is set.
func (r *Runner) watchdog(now time.Time) {
	r.unrespMu.Lock()
	defer r.unrespMu.Unlock()
	for _, a := range r.running() {
		for _, c := range a.overdue(now, r.cfg.CallWatchdog.Duration) {
			r.log.Warn("jobs: filesystem call exceeded the watchdog", "job", a.job.ID,
				"source", c.source, "op", c.op, "since", c.start)
			if !c.flagged {
				continue
			}
			r.unresponsive[c.source]++
			if r.unresponsive[c.source] > 1 {
				continue
			}
			if r.reg == nil {
				continue
			}
			if err := r.reg.SetUnresponsive(context.WithoutCancel(r.baseCtx), c.source, c.start); err != nil {
				r.log.Error("jobs: flag source unresponsive", "source", c.source, "err", err)
			}
		}
	}
}

func (r *Runner) clearUnresponsive(src domain.SourceID) {
	r.unrespMu.Lock()
	defer r.unrespMu.Unlock()
	r.unresponsive[src]--
	if r.unresponsive[src] > 0 {
		return
	}
	delete(r.unresponsive, src)
	if r.reg == nil {
		return
	}
	if err := r.reg.SetUnresponsive(context.WithoutCancel(r.baseCtx), src, time.Time{}); err != nil {
		r.log.Error("jobs: clear source unresponsive", "source", src, "err", err)
	}
}

// flushDue persists progress that has waited ProgressInterval.
func (r *Runner) flushDue(now time.Time) {
	for _, a := range r.running() {
		if a.flushDue(now, r.progressInterval) {
			a.flush()
		}
	}
}

// cloneProgress copies a progress map; nil becomes empty.
func cloneProgress(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	maps.Copy(out, m)
	return out
}
