package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/sources"
	"precious/internal/store"
)

// Deferral reasons of an organize job (D10).
const (
	// deferScan: a scan of the source is running.
	deferScan = "scan_running"
	// deferTurn: an older action of the source is queued or running, or a
	// reconcile job of the source is running.
	deferTurn = "waiting_turn"
	// deferOffline: a reconcile job waits for its source to come back.
	deferOffline = "source_offline"
)

// deferFor is how long an organize job waits for a scan or its turn (D10).
const deferFor = time.Second

// deferOfflineFor is how long a reconcile job waits for an offline source:
// the registry refreshes availability every minute.
const deferOfflineFor = sources.RefreshInterval

type handler struct{ e *Executor }

// Run is one attempt of an organize job: sweep, wait for a running scan or
// an older action, reconcile the source's intent items, then run the
// action's planned items (design Concurrency).
func (h *handler) Run(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	var p payload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return domain.Wrap(domain.CodeInvalidRequest, err, "organize job %d: payload", job.ID)
		}
	}
	r := &run{e: h.e, ctx: ctx, bg: context.WithoutCancel(ctx), job: job, rt: rt, src: job.SourceID}
	if p.ActionID != "" {
		id, err := strconv.ParseInt(p.ActionID, 10, 64)
		if err != nil || id <= 0 {
			return domain.Errorf(domain.CodeInvalidRequest, "organize job %d: action_id %q", job.ID, p.ActionID)
		}
		r.action = id
	}
	if r.src == "" {
		return domain.Errorf(domain.CodeInvalidRequest, "organize job %d has no source", job.ID)
	}
	return r.do()
}

// run is one attempt of an organize job.
type run struct {
	e *Executor
	// ctx ends on a cancel request, a lost lease, or shutdown; bg does not,
	// and serves everything after an intent commits, so a step in flight is
	// always finished and recorded.
	ctx, bg context.Context
	job     jobs.Job
	rt      jobs.Runtime
	src     domain.SourceID
	action  int64 // 0 for a reconcile job

	bulk  bool
	root  fsaccess.Dir
	caps  fsaccess.Capabilities
	total int64
	// fsType is the source's filesystem type, as recorded when it was
	// added.
	fsType string

	// The action's kind, its cleanup ground, and the check a purge acts on
	// (r4 D3, D5, D11); empty and 0 for a reconcile job.
	kind, ground string
	checkID      int64
	// copies are the staying copies each duplicate-ground cleanup rename
	// item was verified against in this attempt (r4 D5), by item ID.
	copies map[int64]*copyCheck
	// unlinked are the inodes of which this attempt, or an earlier step of
	// its purge, removed a name (r4 D10's hard-link tolerance), by check.
	unlinked map[int64]map[inode]bool
}

// verdict is what follows an item.
type verdict int

const (
	goOn  verdict = iota // run the next item
	halt                 // the action stopped; its end is recorded
	leave                // the action is no longer running: end the attempt
)

func (r *run) do() error {
	wait, proceed, err := r.start()
	if err != nil {
		return err
	}
	if wait != "" {
		return r.deferral(wait)
	}
	if !proceed {
		return nil
	}

	opened, err := r.e.src.Open(r.ctx, r.src)
	if err != nil {
		if r.ctx.Err() != nil {
			return r.interrupted(r.ctx.Err())
		}
		switch code := domain.CodeOf(err); {
		case r.action == 0 && code == domain.CodeSourceOffline:
			// The intent items wait for the disk; scans of the source
			// wait for them (OrganizeActive).
			return &jobs.Defer{Until: r.e.clk.Now().Add(deferOfflineFor), Reason: deferOffline}
		case r.action != 0 && (code == domain.CodeSourceOffline || code == domain.CodeUnknownSource):
			// Nothing can be reconciled or run: the first item records
			// why, and the action stops.
			return r.stopOffline()
		}
		return err
	}
	defer opened.Root.Close()
	r.root, r.caps, r.fsType = opened.Root, opened.Source.Caps, opened.Source.Volume.FSType

	if err := r.reconcile(); err != nil {
		if r.ctx.Err() != nil {
			return r.interrupted(r.ctx.Err())
		}
		var crash *crashError
		if r.action != 0 && !errors.As(err, &crash) {
			if serr := r.write(func(tx *jobs.Tx) error { return r.stop(tx) }); serr != nil {
				return errors.Join(err, serr)
			}
		}
		return err
	}
	if r.action == 0 {
		return nil
	}
	return r.items()
}

// start runs the attempt's opening transaction: sweep, then wait for a
// running scan of the source or an older action, then mark the action
// running. wait is a deferral reason; proceed is false when there is
// nothing to do.
func (r *run) start() (wait string, proceed bool, err error) {
	err = r.e.runner.Write(r.ctx, func(tx *jobs.Tx) error {
		wait, proceed = "", false
		if err := r.e.sweep(r.ctx, tx, r.src); err != nil {
			return err
		}
		q := tx.SQL()
		scanning, err := scanRunning(r.ctx, q, r.src)
		if err != nil {
			return err
		}
		if scanning {
			wait = deferScan
			return nil
		}
		if r.action == 0 {
			// A queued or running action's own job reconciles; a reconcile
			// job never runs beside it.
			var busy bool
			if err := q.QueryRowContext(r.ctx, `SELECT EXISTS (SELECT 1 FROM actions WHERE source_id = ?
				AND state IN ('queued', 'running'))`, string(r.src)).Scan(&busy); err != nil {
				return err
			}
			proceed = !busy
			return nil
		}
		var (
			state, src string
			bulk       bool
			kind       string
			ground     sql.NullString
			check      sql.NullInt64
		)
		err = q.QueryRowContext(r.ctx, `SELECT state, source_id, bulk, kind, ground, check_id FROM actions WHERE id = ?`,
			r.action).Scan(&state, &src, &bulk, &kind, &ground, &check)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if domain.SourceID(src) != r.src || (state != actionQueued && state != actionRunning) {
			return nil
		}
		r.bulk, r.kind, r.ground, r.checkID = bulk, kind, ground.String, check.Int64
		var blocked bool
		if err := q.QueryRowContext(r.ctx, `SELECT EXISTS (SELECT 1 FROM actions WHERE source_id = ?1 AND id < ?2
				AND state IN ('queued', 'running'))
			OR EXISTS (SELECT 1 FROM jobs WHERE kind = ?3 AND source_id = ?1 AND state = 'running' AND id <> ?4
				AND scope_key = ?5)`, string(r.src), r.action, string(KindOrganize), int64(r.job.ID),
			reconcileSpec(r.src).ScopeKey).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			wait = deferTurn
			return nil
		}
		if _, err := q.ExecContext(r.ctx, `UPDATE actions SET state = 'running', started_at = COALESCE(started_at, ?)
			WHERE id = ? AND state = 'queued'`, clock.Millis(tx.Now()), r.action); err != nil {
			return err
		}
		proceed = true
		return nil
	})
	return wait, proceed, err
}

// scanRunning reports whether a scan of src is running.
func scanRunning(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error) {
	var running bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = ? AND source_id = ?
		AND state = 'running')`, string(jobs.KindScan), string(src)).Scan(&running)
	return running, err
}

func (r *run) deferral(reason string) error {
	return &jobs.Defer{Until: r.e.clk.Now().Add(deferFor), Reason: reason}
}

// write runs fn in a write transaction that a cancellation does not abort.
func (r *run) write(fn func(tx *jobs.Tx) error) error {
	return r.e.runner.Write(r.bg, fn)
}

// items runs the action's planned items in seq order.
func (r *run) items() error {
	if err := r.progress(); err != nil {
		return err
	}
	for {
		if err := r.ctx.Err(); err != nil {
			return r.interrupted(err)
		}
		it, ok, err := r.nextPlanned()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		v, err := r.item(it)
		if err != nil {
			return err
		}
		if err := r.progress(); err != nil {
			return err
		}
		switch v {
		case halt, leave:
			return nil
		}
		if err := r.rt.Yield(r.ctx); err != nil {
			return r.interrupted(err)
		}
	}
	return r.write(func(tx *jobs.Tx) error {
		res, err := tx.SQL().ExecContext(r.bg, `UPDATE actions SET state = 'done', finished_at = ?
			WHERE id = ? AND state = 'running'`, clock.Millis(tx.Now()), r.action)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		return r.e.actionDone(r.bg, tx, r.action, r.src)
	})
}

// progress reports {"items":n,"done":n} of the action.
func (r *run) progress() error {
	var total, done int64
	if err := r.e.st.Reader().QueryRowContext(r.bg, `SELECT count(*), coalesce(sum(state = 'done'), 0)
		FROM action_items WHERE action_id = ?`, r.action).Scan(&total, &done); err != nil {
		return err
	}
	r.rt.Progress(map[string]int64{"items": total, "done": done})
	return nil
}

// interrupted ends an attempt whose context ended between items. On a cancel
// request the action stops; on shutdown or a lost lease it stays running for
// the next attempt.
func (r *run) interrupted(cause error) error {
	if r.action == 0 {
		return cause
	}
	var cancelled bool
	if err := r.e.st.Reader().QueryRowContext(r.bg, `SELECT cancel_requested FROM jobs WHERE id = ?`,
		int64(r.job.ID)).Scan(&cancelled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.Join(cause, err)
	}
	if !cancelled {
		return cause
	}
	if err := r.write(func(tx *jobs.Tx) error { return r.stop(tx) }); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// stop stops the action: its planned items become not_attempted. An action
// with a done item gets its ActionDone.
func (r *run) stop(tx *jobs.Tx) error {
	if err := stopAction(r.bg, tx.SQL(), r.action, tx.Now()); err != nil {
		return err
	}
	return r.e.actionDone(r.bg, tx, r.action, r.src)
}

// stopOffline ends the first planned item offline and stops the action,
// when the source cannot be opened.
func (r *run) stopOffline() error {
	return r.write(func(tx *jobs.Tx) error {
		var id int64
		err := tx.SQL().QueryRowContext(r.bg, `SELECT id FROM action_items WHERE action_id = ? AND state = 'planned'
			ORDER BY seq LIMIT 1`, r.action).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			if err := endItem(r.bg, tx.SQL(), id, stateOffline, "", "", tx.Now()); err != nil {
				return err
			}
		}
		return r.stop(tx)
	})
}

// crashError is a Hooks error: the attempt ends at once, as if the process
// died there.
type crashError struct{ err error }

func (c *crashError) Error() string { return fmt.Sprintf("executor: simulated crash: %v", c.err) }
func (c *crashError) Unwrap() error { return c.err }
