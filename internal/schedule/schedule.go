// Package schedule runs scheduled rescans (§7, r2b design D6). A source with
// a rescan schedule has its next due time in sources.next_scan_at. Every
// minute, RunDue looks at the sources whose time has come: an online source
// gets a scan through index.StartScan, which coalesces into a scan already
// queued, running, or paused, and any other source has the skip recorded with
// its state. Either way its next due time becomes the schedule's first time
// after now, in the same transaction.
//
// That one rule gives the guarantees: a due time is consumed once, so a scan
// never runs twice for it; a server that was down for several due times
// starts one scan when it is back, because the next time is computed from
// now and not from the missed time; and the state survives restarts because
// it is persisted. A scheduled scan is a scan like any other, so hashing and
// relations follow it.
package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/sources"
	"precious/internal/store"
)

// Interval is how often Run looks for due sources, so a scan starts within
// a minute of its time.
const Interval = time.Minute

// refreshWait bounds the availability refresh before due sources are
// looked at; past it the states last recorded decide.
const refreshWait = 10 * time.Second

// ReasonInvalidSchedule is the skip reason of a stored schedule that no
// longer validates, such as a time zone the binary does not know. Its next
// scan is cleared until the owner sets the schedule again.
const ReasonInvalidSchedule = "invalid_schedule"

// Scheduler starts the scans of due sources.
type Scheduler struct {
	st     *store.Store
	runner *jobs.Runner
	src    *sources.Service
	clk    clock.Clock
	log    *slog.Logger
}

// New returns the scheduler over the store, the job runner that scans run
// on, and the source registry whose availability decides.
func New(st *store.Store, runner *jobs.Runner, src *sources.Service, clk clock.Clock, log *slog.Logger) *Scheduler {
	if clk == nil {
		clk = clock.Real{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{st: st, runner: runner, src: src, clk: clk, log: log}
}

// Run calls RunDue with the clock's time every Interval, starting at once,
// until ctx ends. Failures are logged.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if err := s.RunDue(ctx, s.clk.Now()); err != nil && ctx.Err() == nil {
			s.log.Error("schedule: starting due scans failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// due is one source whose next scan time has come.
type due struct {
	id       domain.SourceID
	state    string
	at       int64
	schedule sql.NullString
}

// RunDue handles every source whose next scan is at or before now: an online
// source's scan is started, or joined when one is active; any other source
// records the skip, at its due time and with its state. Each one's next scan
// becomes its schedule's first time after now. It refreshes availability
// first when a source is due, so a disk unplugged since the last refresh is
// seen as offline.
func (s *Scheduler) RunDue(ctx context.Context, now time.Time) error {
	var pending bool
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sources WHERE next_scan_at <= ?)`,
		clock.Millis(now)).Scan(&pending); err != nil || !pending {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, refreshWait)
	err := s.src.Refresh(rctx)
	cancel()
	if err != nil && ctx.Err() == nil {
		s.log.Warn("schedule: availability refresh failed; using the states last recorded", "err", err)
	}
	return s.runner.Write(ctx, func(tx *jobs.Tx) error {
		// Read again under the writer lock: a due time is consumed once.
		list, err := dueSources(ctx, tx.SQL(), now)
		if err != nil {
			return err
		}
		for _, d := range list {
			if err := s.handle(ctx, tx, d, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func dueSources(ctx context.Context, q store.Queryer, now time.Time) ([]due, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, state, next_scan_at, scan_schedule FROM sources
		WHERE next_scan_at <= ? ORDER BY next_scan_at, id`, clock.Millis(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.state, &d.at, &d.schedule); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Scheduler) handle(ctx context.Context, tx *jobs.Tx, d due, now time.Time) error {
	next, err := nextAfter(d.schedule, now)
	if err != nil {
		s.log.Error("schedule: a stored schedule is invalid; its scans stop until it is set again",
			"source", d.id, "err", err)
		_, err := tx.SQL().ExecContext(ctx, `UPDATE sources SET next_scan_at = NULL, schedule_skipped_at = ?,
			schedule_skip_reason = ? WHERE id = ?`, d.at, ReasonInvalidSchedule, string(d.id))
		return err
	}
	if d.state != string(sources.StateOnline) {
		_, err := tx.SQL().ExecContext(ctx, `UPDATE sources SET next_scan_at = ?, schedule_skipped_at = ?,
			schedule_skip_reason = ? WHERE id = ?`, next, d.at, d.state, string(d.id))
		return err
	}
	acc, err := index.StartScan(ctx, tx, d.id)
	if err != nil {
		return err
	}
	s.log.Info("schedule: scan due", "source", d.id, "job", acc.JobID, "coalesced", acc.Coalesced)
	_, err = tx.SQL().ExecContext(ctx, `UPDATE sources SET next_scan_at = ?, schedule_skipped_at = NULL,
		schedule_skip_reason = NULL WHERE id = ?`, next, string(d.id))
	return err
}

// nextAfter is the stored schedule's first time after now, as stored.
func nextAfter(stored sql.NullString, now time.Time) (int64, error) {
	if !stored.Valid {
		return 0, errors.New("a next scan without a schedule")
	}
	var sch domain.Schedule
	if err := json.Unmarshal([]byte(stored.String), &sch); err != nil {
		return 0, err
	}
	next, err := sch.Next(now)
	if err != nil {
		return 0, err
	}
	return clock.Millis(next), nil
}
