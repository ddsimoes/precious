// Package executor runs organize actions on disk (r3 design D3–D5, D7, D9,
// D10): it is the only package that changes a source, and the only caller of
// fsaccess.Writer (§5 I2).
//
// An action's items (the action_items rows) are both its history and its
// journal. One organize job serves one action, bound to its source, and runs
// one item at a time, in seq order:
//
//  1. Intent. A write transaction re-checks the item (the source's write
//     permission, no running scan, the action not cancelled, the entry still
//     where the plan found it, the destination present, no missing row with
//     owner intent at the destination, no lost keep in a bulk action, an undo
//     not done twice) and records state intent with the resolved names,
//     paths, and the identity the entry is expected to have.
//  2. Step. Both folders are opened by rooted descent from the source root,
//     each checked against the identity the index holds; the entry is lstat'd
//     and compared with the expected identity, under the source's
//     capabilities, and the devices of the live handles must agree. Then one
//     Writer call: a rename that never replaces, a mkdir, or an rmdir of an
//     empty folder.
//  3. Sync of every folder the step changed, then a confirming lstat of both
//     names.
//  4. Outcome. One transaction records the item's end and updates the index
//     through Index (D6). When the index update fails, the item ends
//     manual_recovery in a second transaction instead.
//
// A crash between intent and outcome leaves the item intent. Every organize
// attempt reconciles its source's intent items before running anything, by
// looking at both names (D4): the old name holding the expected identity and
// the new one free sends the item back to planned; the reverse is a done step
// whose index update is applied then; anything else ends manual_recovery with
// the findings. Startup only sweeps and enqueues; it never reads a disk.
//
// Actions of one source run one at a time, oldest first, and never while a
// scan of the source is running (D10): the job defers by one second.
//
// R4 runs cleanup, restore, and purge actions on the same journal (r4 D3–D6,
// D10–D13). A cleanup item is the mkdir of its folder in the quarantine,
// the rename of its entry into it, and its origin record (created
// exclusively); its first step re-checks the whole item against its draft,
// and, for a duplicate ground, against copies read in full beforehand. A
// restore renames back, unlinks the record, and removes the emptied
// folders. A purge verifies its check against the disk, then deletes one
// checked item per step, compared whole before its first deletion. Files
// are created and unlinked only through folders inside the quarantine, and
// every outcome marks stale the pre-delete checks relying on its paths.
//
// R5 sets a file's modification time (r5 D13): a set_mtime item re-checks at
// intent that the file is present, outside the quarantine, has one link, and
// would change, and records the index's identity with its change time. The
// step lstats the name through its folder, journals the time it found, and
// calls SetModTime, with no folder sync; the outcome carries the index and
// its content rows to the new time. A file the service does not own ends
// failed (not_owner) and the action goes on; a set_mtime never turns writes
// off.
package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"precious/internal/clock"
	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/sources"
	"precious/internal/store"
)

// KindOrganize is the job kind that runs one action (D10), registered with
// ClassInteractive. A job with no action in its payload only sweeps and
// reconciles its source (Startup).
const KindOrganize jobs.Kind = "organize"

// Index makes the index follow a done step (D6), inside the outcome's
// transaction. organize implements it over index, decisions, the Refolder,
// and relations.
type Index interface {
	ApplyRename(ctx context.Context, tx *sql.Tx, m index.Move) error
	ApplyMkdir(ctx context.Context, tx *sql.Tx, f index.NewFolder) (domain.EntryID, error)
	ApplyRmdir(ctx context.Context, tx *sql.Tx, src domain.SourceID, id domain.EntryID, parentFacts index.PostFacts) error
	// ActionDone runs once when an action with a done item ends
	// (relations.RequestRefresh).
	ActionDone(ctx context.Context, tx *jobs.Tx, src domain.SourceID) error
	// MissingIntentAt is index.MissingIntentAt, so the executor builds before slice 2.1 lands.
	MissingIntentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error)
	// IntentBelow is index.IntentBelow: owner intent on an entry below a
	// folder, which keeps an undo from removing it (design V2).
	IntentBelow(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error)
	// ApplyPurge deletes what a purge step removed (r4 D11): the subtree of
	// whole when the step removed the whole item, else the entries removed,
	// and refolds the quarantine chain above them.
	ApplyPurge(ctx context.Context, tx *sql.Tx, src domain.SourceID, removed []domain.EntryID, whole domain.EntryID) error
	// ApplyUnlink drops the row at path, a record a scan indexed, after an
	// unlink (r4 D4, D6); a path not indexed is nothing to drop.
	ApplyUnlink(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte) error
	// ApplyModTime follows a done set_mtime (r5 D13, D15): index.ApplyModTime,
	// then a refold of the entry, so its folders' newest, oldest, and
	// by-year figures follow.
	ApplyModTime(ctx context.Context, tx *sql.Tx, m index.ModTime) error
}

// Options configures New.
type Options struct {
	Store   *store.Store
	Sources *sources.Service
	Index   Index
	// AllowWrites is [sources] allow_writes, re-checked in every intent.
	AllowWrites bool
	// Clock defaults to clock.Real; Logger to slog.Default.
	Clock  clock.Clock
	Logger *slog.Logger
	Hooks  Hooks
	// Content hashes the files a duplicate-ground cleanup relies on (r4 D5,
	// D9).
	Content *content.Service
}

// Hooks run around each Writer call. Tests only: an error simulates a crash
// at that point, ending the attempt at once with nothing more recorded.
type Hooks struct {
	BeforeStep, AfterStep func(itemID int64) error
}

// Executor runs organize jobs.
type Executor struct {
	st          *store.Store
	src         *sources.Service
	idx         Index
	allowWrites bool
	clk         clock.Clock
	log         *slog.Logger
	hooks       Hooks
	content     *content.Service
	runner      *jobs.Runner
}

// New returns an executor. Call Register before the runner starts.
func New(o Options) *Executor {
	e := &Executor{st: o.Store, src: o.Sources, idx: o.Index, allowWrites: o.AllowWrites, clk: o.Clock,
		log: o.Logger, hooks: o.Hooks, content: o.Content}
	if e.clk == nil {
		e.clk = clock.Real{}
	}
	if e.log == nil {
		e.log = slog.Default()
	}
	return e
}

// Register registers the organize handler in the interactive class (D10).
// The executor commits through r.
func (e *Executor) Register(r *jobs.Runner) {
	e.runner = r
	r.RegisterClass(KindOrganize, &handler{e: e}, jobs.ClassInteractive)
}

// Startup sweeps actions whose job is terminal (D10) and enqueues a reconcile
// job for each source with an intent item and no active organize job. It
// reads nothing on disk (D4), so it never races an attempt that the runner
// requeued.
func (e *Executor) Startup(ctx context.Context, r *jobs.Runner) error {
	if e.runner == nil {
		e.runner = r
	}
	return r.Write(ctx, func(tx *jobs.Tx) error {
		if err := e.sweep(ctx, tx, ""); err != nil {
			return err
		}
		ids, err := sourceIDs(ctx, tx.SQL(), `SELECT DISTINCT a.source_id FROM action_items i
			JOIN actions a ON a.id = i.action_id
			WHERE i.state = 'intent' AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.kind = ?
				AND j.source_id = a.source_id AND j.state IN ('queued', 'running', 'paused'))
			ORDER BY a.source_id`, string(KindOrganize))
		if err != nil {
			return err
		}
		for _, src := range ids {
			if _, _, err := tx.EnqueueOnce(reconcileSpec(src)); err != nil {
				return err
			}
		}
		return nil
	})
}

// OrganizeActive reports whether an organize job of src is queued or running,
// or src has an intent item (D10). The scan defers at its start while it is.
func OrganizeActive(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error) {
	var active bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = ?1 AND source_id = ?2
			AND state IN ('queued', 'running'))
		OR EXISTS (SELECT 1 FROM action_items i JOIN actions a ON a.id = i.action_id
			WHERE i.state = 'intent' AND a.source_id = ?2)`, string(KindOrganize), string(src)).Scan(&active)
	return active, err
}

// payload is an organize job's payload. An empty ActionID is a reconcile
// job (Startup, CancelAction).
type payload struct {
	ActionID string `json:"action_id,omitempty"`
}

// actionScope is the scope of the job of one action (D10).
func actionScope(action int64) string { return "organize:" + strconv.FormatInt(action, 10) }

// reconcileSpec is the job that sweeps and reconciles src without running an
// action: scope "organize-reconcile:<source>", payload {}.
func reconcileSpec(src domain.SourceID) jobs.Spec {
	return jobs.Spec{Kind: KindOrganize, SourceID: src, ScopeKey: "organize-reconcile:" + string(src)}
}

// Enqueue starts the job of one action: scope "organize:<action_id>",
// payload {"action_id":"…"}, bound to src. It also records the job in
// actions.job_id, which the sweep reads.
func Enqueue(tx *jobs.Tx, src domain.SourceID, action int64) (jobs.Record, error) {
	p, err := json.Marshal(payload{ActionID: strconv.FormatInt(action, 10)})
	if err != nil {
		return jobs.Record{}, err
	}
	rec, err := tx.Enqueue(jobs.Spec{Kind: KindOrganize, SourceID: src, ScopeKey: actionScope(action), Payload: p})
	if err != nil {
		return jobs.Record{}, fmt.Errorf("executor: enqueue action %d: %w", action, err)
	}
	if _, err := tx.SQL().Exec(`UPDATE actions SET job_id = ? WHERE id = ?`, int64(rec.ID), action); err != nil {
		return jobs.Record{}, err
	}
	return rec, nil
}

// CancelAction stops a queued or running action in tx (D9 Cancelling). A
// queued action becomes stopped at once, its planned items not_attempted,
// and its job is cancelled. A running action has its job cancelled: a job
// running a step stops after it; a job waiting to run again (deferred) ends
// at once, and a reconcile job is enqueued whose sweep stops the action. An
// unknown action is not_found; one neither queued nor running is
// action_not_runnable.
func CancelAction(ctx context.Context, tx *jobs.Tx, action int64) error {
	var (
		state, src string
		jobID      sql.NullInt64
	)
	err := tx.SQL().QueryRowContext(ctx, `SELECT state, source_id, job_id FROM actions WHERE id = ?`, action).
		Scan(&state, &src, &jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeNotFound, "unknown action %d", action)
	}
	if err != nil {
		return err
	}
	switch state {
	case actionQueued:
		if err := stopAction(ctx, tx.SQL(), action, tx.Now()); err != nil {
			return err
		}
		if jobID.Valid {
			if _, err := tx.Cancel(domain.JobID(jobID.Int64)); err != nil && domain.CodeOf(err) != domain.CodeNotFound {
				return err
			}
		}
		return nil
	case actionRunning:
		if jobID.Valid {
			rec, err := tx.Cancel(domain.JobID(jobID.Int64))
			if err != nil && domain.CodeOf(err) != domain.CodeNotFound {
				return err
			}
			if err == nil && !rec.State.Terminal() {
				return nil // the running attempt stops after its step in flight
			}
		}
		_, _, err := tx.EnqueueOnce(reconcileSpec(domain.SourceID(src)))
		return err
	default:
		return domain.Errorf(domain.CodeActionNotRunnable, "action %d is %s, not queued or running", action, state)
	}
}

// Action states.
const (
	actionQueued  = "queued"
	actionRunning = "running"
	actionDone    = "done"
	actionStopped = "stopped"
)

// sweep stops every queued or running action whose job is terminal or gone
// (D10), of src or, when src is empty, of every source: its planned items
// become not_attempted and its intent items stay for reconciliation. An
// action with a done item gets its ActionDone.
func (e *Executor) sweep(ctx context.Context, tx *jobs.Tx, src domain.SourceID) error {
	rows, err := tx.SQL().QueryContext(ctx, `SELECT a.id, a.source_id FROM actions a
		WHERE a.state IN ('queued', 'running') AND (?1 = '' OR a.source_id = ?1)
			AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id = a.job_id AND j.state IN ('queued', 'running', 'paused'))
		ORDER BY a.id`, string(src))
	if err != nil {
		return err
	}
	type swept struct {
		id  int64
		src domain.SourceID
	}
	var list []swept
	for rows.Next() {
		var s swept
		if err := rows.Scan(&s.id, &s.src); err != nil {
			rows.Close()
			return err
		}
		list = append(list, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range list {
		if err := stopAction(ctx, tx.SQL(), s.id, tx.Now()); err != nil {
			return err
		}
		if err := e.actionDone(ctx, tx, s.id, s.src); err != nil {
			return err
		}
		e.log.Warn("executor: stopped an action whose job ended", "action", s.id, "source", s.src)
	}
	return nil
}

// stopAction marks a queued or running action stopped and its planned items
// not_attempted.
func stopAction(ctx context.Context, tx *sql.Tx, action int64, now time.Time) error {
	ms := clock.Millis(now)
	if _, err := tx.ExecContext(ctx, `UPDATE actions SET state = 'stopped', finished_at = ?
		WHERE id = ? AND state IN ('queued', 'running')`, ms, action); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE action_items SET state = 'not_attempted', finished_at = ?
		WHERE action_id = ? AND state = 'planned'`, ms, action)
	return err
}

// actionDone calls Index.ActionDone when the action has a done item.
func (e *Executor) actionDone(ctx context.Context, tx *jobs.Tx, action int64, src domain.SourceID) error {
	var any bool
	if err := tx.SQL().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM action_items
		WHERE action_id = ? AND state = 'done')`, action).Scan(&any); err != nil {
		return err
	}
	if !any {
		return nil
	}
	return e.idx.ActionDone(ctx, tx, src)
}

// sourceIDs runs a query of one source ID column.
func sourceIDs(ctx context.Context, q store.Queryer, query string, args ...any) ([]domain.SourceID, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SourceID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, domain.SourceID(id))
	}
	return out, rows.Err()
}
