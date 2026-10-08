package organize

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/executor"
	"precious/internal/index"
	"precious/internal/jobs"
)

// runResponse answers run-action (202).
type runResponse struct {
	Action actionJSON      `json:"action"`
	JobID  string          `json:"job_id"`
	State  domain.JobState `json:"state"`
}

// actionResponse answers cancel-action.
type actionResponse struct {
	Action actionJSON `json:"action"`
}

// scanJSON is the scan resolve-recovery starts.
type scanJSON struct {
	JobID     string `json:"job_id"`
	Coalesced bool   `json:"coalesced"`
}

type resolveResponse struct {
	Action actionJSON `json:"action"`
	Scan   scanJSON   `json:"scan"`
}

// runAction queues a planned action for its organize job (D9, D10): it must
// be planned, unexpired, and hold a planned item, and its source must allow
// changes now with nothing waiting for the owner's check.
func (s *Service) runAction(ctx context.Context, tx *jobs.Tx, rawID string) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	id, err := parseID("action", rawID)
	if err != nil {
		return 0, nil, err
	}
	state, src, expires, err := actionState(ctx, q, id)
	if err != nil {
		return 0, nil, err
	}
	if state == "expired" || state == "planned" && expires.Valid && expires.Int64 <= clock.Millis(now) {
		return 0, nil, domain.Errorf(domain.CodeActionExpired, "this change was planned over an hour ago; plan it again")
	}
	if state != "planned" {
		return 0, nil, domain.Errorf(domain.CodeActionNotRunnable, "action %d is %s, not planned", id, state)
	}
	var runnable bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM action_items WHERE action_id = ? AND state = 'planned')`,
		id).Scan(&runnable); err != nil {
		return 0, nil, err
	}
	if !runnable {
		return 0, nil, domain.Errorf(domain.CodeActionNotRunnable, "action %d has nothing to run", id)
	}
	if err := s.checkSource(ctx, q, src); err != nil {
		return 0, nil, err
	}
	if _, err := q.ExecContext(ctx, `UPDATE actions SET state = 'queued' WHERE id = ? AND state = 'planned'`, id); err != nil {
		return 0, nil, err
	}
	rec, err := executor.Enqueue(tx, src, id)
	if err != nil {
		return 0, nil, err
	}
	a, err := readAction(ctx, q, id, now)
	if err != nil {
		return 0, nil, err
	}
	if err := audit(ctx, q, now, AuditActionRun, map[string]any{"action_id": a.ID, "kind": a.Kind,
		"source_id": src, "job_id": rec.ID.String(), "items": a.Counts["planned"]}); err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, runResponse{Action: a, JobID: rec.ID.String(), State: rec.DisplayState()}, nil
}

// cancelAction stops a queued or running action (D9 Cancelling).
func (s *Service) cancelAction(ctx context.Context, tx *jobs.Tx, rawID string) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	id, err := parseID("action", rawID)
	if err != nil {
		return 0, nil, err
	}
	if err := executor.CancelAction(ctx, tx, id); err != nil {
		return 0, nil, err
	}
	a, err := readAction(ctx, q, id, now)
	if err != nil {
		return 0, nil, err
	}
	if err := audit(ctx, q, now, AuditActionCancelled, map[string]any{"action_id": a.ID, "kind": a.Kind,
		"source_id": a.SourceID, "state": a.State}); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, actionResponse{Action: a}, nil
}

// resolveRecovery marks an item that needed the owner's check resolved and
// starts a scan of its source in the same transaction, which brings the
// index in line with what the owner left on the disk (design Concurrency).
// The source must be online for that scan.
func (s *Service) resolveRecovery(ctx context.Context, tx *jobs.Tx, rawID string) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	id, err := parseID("item", rawID)
	if err != nil {
		return 0, nil, err
	}
	var (
		state  string
		action int64
		src    domain.SourceID
	)
	err = q.QueryRowContext(ctx, `SELECT i.state, i.action_id, a.source_id FROM action_items i
		JOIN actions a ON a.id = i.action_id WHERE i.id = ?`, id).Scan(&state, &action, &src)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, notFound("item", rawID)
	}
	if err != nil {
		return 0, nil, err
	}
	if state != "manual_recovery" {
		return 0, nil, domain.Errorf(domain.CodeInvalidEntryState, "item %d is %s, not waiting for your check", id, state)
	}
	if _, err := q.ExecContext(ctx, `UPDATE action_items SET state = 'resolved' WHERE id = ? AND state = 'manual_recovery'`,
		id); err != nil {
		return 0, nil, err
	}
	scan, err := index.StartScan(ctx, tx, src)
	if err != nil {
		return 0, nil, err
	}
	a, err := readAction(ctx, q, action, now)
	if err != nil {
		return 0, nil, err
	}
	if err := audit(ctx, q, now, AuditRecoveryResolved, map[string]any{"action_id": a.ID, "item_id": rawID,
		"source_id": src, "scan_job_id": scan.JobID}); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, resolveResponse{Action: a, Scan: scanJSON{JobID: scan.JobID, Coalesced: scan.Coalesced}}, nil
}
