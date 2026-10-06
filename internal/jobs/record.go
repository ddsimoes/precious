package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/store"
)

// Record is the persisted state of one job.
type Record struct {
	ID             domain.JobID
	Kind           Kind
	PayloadVersion int
	Payload        json.RawMessage
	SourceID       domain.SourceID // empty when the job is not bound to a source
	// DeviceKey is the worker key of the latest claim: the per-device key
	// (design D14), or "workers:<name>" for a kind registered with RegisterPool
	// (design D11). Empty before the first claim and for a job without a
	// source that is not a pool job. A job that moves with Runtime.UseSource
	// (design D13) records the key it moved to with its next progress write.
	DeviceKey string
	// ScopeKey makes the job single-flight per (kind, scope key) while active,
	// for example "node:42" for an aggregate walk. Empty when unscoped.
	ScopeKey string
	State    domain.JobState
	// CancelRequested is set by a cancel request and stays set; while the job
	// is not terminal it is displayed as StateCancelRequested.
	CancelRequested bool
	PauseReason     string
	TerminalCode    domain.ErrorCode
	// TerminalDetail explains a terminal outcome, the pause while paused, or
	// the deferral while queued after a Defer (cleared when claimed again or
	// woken by Tx.WakeOnce).
	TerminalDetail string
	// Attempts counts attempts lost to a dead worker (crash or lease expiry).
	// The attempt currently running is number Attempts+1.
	Attempts    int
	MaxAttempts int
	Progress    map[string]int64
	CreatedAt   time.Time
	StartedAt   *time.Time
	UpdatedAt   time.Time
	FinishedAt  *time.Time

	leaseOwner   string
	leaseExpires int64
	availableAt  int64
}

// DisplayState is the state shown to clients (see DisplayState).
func (r Record) DisplayState() domain.JobState { return DisplayState(r.State, r.CancelRequested) }

// Accepted is the 202 response body of a command that starts or changes a
// job.
type Accepted struct {
	JobID string `json:"job_id"`
	// State is the displayed job state (cancel_requested while a cancellation
	// is pending).
	State domain.JobState `json:"state"`
	// Coalesced reports that an existing job was returned instead of a new one.
	Coalesced bool `json:"coalesced"`
}

// Accepted is the command response describing r; coalesced reports that r
// already existed.
func (r Record) Accepted(coalesced bool) Accepted {
	return Accepted{JobID: r.ID.String(), State: r.DisplayState(), Coalesced: coalesced}
}

// Event is the event payload describing r.
func (r Record) Event() Event {
	progress := r.Progress
	if progress == nil {
		progress = map[string]int64{}
	}
	return Event{
		JobID:           r.ID.String(),
		Kind:            r.Kind,
		SourceID:        r.SourceID,
		State:           r.DisplayState(),
		CancelRequested: r.CancelRequested,
		PauseReason:     r.PauseReason,
		TerminalCode:    r.TerminalCode,
		Progress:        progress,
		Attempts:        r.Attempts,
	}
}

const recordColumns = `id, kind, payload_version, payload, source_id, device_key, state,
	cancel_requested, pause_reason, terminal_code, terminal_detail, attempts, max_attempts,
	lease_owner, lease_expires_at, available_at, progress, created_at, started_at, updated_at,
	finished_at, scope_key`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(s rowScanner) (Record, error) {
	var (
		r                                   Record
		payload, progress                   string
		sourceID, deviceKey, pauseReason    sql.NullString
		scopeKey                            sql.NullString
		terminalCode, terminalDetail, owner sql.NullString
		leaseExpires, startedAt, finishedAt sql.NullInt64
		cancelRequested                     int64
		createdAt, updatedAt, availableAt   int64
		kind, state                         string
	)
	err := s.Scan(&r.ID, &kind, &r.PayloadVersion, &payload, &sourceID, &deviceKey, &state,
		&cancelRequested, &pauseReason, &terminalCode, &terminalDetail, &r.Attempts, &r.MaxAttempts,
		&owner, &leaseExpires, &availableAt, &progress, &createdAt, &startedAt, &updatedAt,
		&finishedAt, &scopeKey)
	if err != nil {
		return Record{}, err
	}
	r.Kind = Kind(kind)
	r.State = domain.JobState(state)
	r.Payload = json.RawMessage(payload)
	r.SourceID = domain.SourceID(sourceID.String)
	r.DeviceKey = deviceKey.String
	r.ScopeKey = scopeKey.String
	r.CancelRequested = cancelRequested != 0
	r.PauseReason = pauseReason.String
	r.TerminalCode = domain.ErrorCode(terminalCode.String)
	r.TerminalDetail = terminalDetail.String
	r.leaseOwner = owner.String
	r.leaseExpires = leaseExpires.Int64
	r.availableAt = availableAt
	r.CreatedAt = clock.FromMillis(createdAt)
	r.UpdatedAt = clock.FromMillis(updatedAt)
	r.StartedAt = optTime(startedAt)
	r.FinishedAt = optTime(finishedAt)
	r.Progress = map[string]int64{}
	if err := json.Unmarshal([]byte(progress), &r.Progress); err != nil {
		return Record{}, fmt.Errorf("jobs: job %d progress: %w", r.ID, err)
	}
	return r, nil
}

func optTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := clock.FromMillis(v.Int64)
	return &t
}

func optMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return clock.Millis(*t)
}

func optString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// getRecord loads one job; a missing job is a domain not_found error.
func getRecord(ctx context.Context, q store.Queryer, id domain.JobID) (Record, error) {
	rec, err := scanRecord(q.QueryRowContext(ctx, `SELECT `+recordColumns+` FROM jobs WHERE id = ?`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, domain.Errorf(domain.CodeNotFound, "job %s not found", id)
	}
	return rec, err
}

// saveRecord writes every mutable column of next, provided the stored row is
// still in state from (and, when owner is non-empty, leased by owner). It
// reports false when that precondition no longer holds.
func saveRecord(ctx context.Context, tx *sql.Tx, from domain.JobState, owner string, next Record) (bool, error) {
	if err := checkTransition(from, next.State); err != nil {
		return false, err
	}
	progress, err := json.Marshal(next.Progress)
	if err != nil {
		return false, err
	}
	if next.Progress == nil {
		progress = []byte("{}")
	}
	var leaseExpires any
	if next.leaseOwner != "" {
		leaseExpires = next.leaseExpires
	}
	query := `UPDATE jobs SET state = ?, cancel_requested = ?, pause_reason = ?, terminal_code = ?,
		terminal_detail = ?, attempts = ?, lease_owner = ?, lease_expires_at = ?, available_at = ?,
		progress = ?, started_at = ?, updated_at = ?, finished_at = ?, device_key = ?
		WHERE id = ? AND state = ?`
	args := []any{string(next.State), boolInt(next.CancelRequested), optString(next.PauseReason),
		optString(string(next.TerminalCode)), optString(next.TerminalDetail), next.Attempts,
		optString(next.leaseOwner), leaseExpires, next.availableAt, string(progress),
		optMillis(next.StartedAt), clock.Millis(next.UpdatedAt), optMillis(next.FinishedAt),
		optString(next.DeviceKey), int64(next.ID), string(from)}
	if owner != "" {
		query += ` AND lease_owner = ?`
		args = append(args, owner)
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
