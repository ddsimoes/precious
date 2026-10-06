package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"precious/internal/clock"
)

// Event types stored in job_events.type. Every stored event is streamed as an
// SSE `job` event; the type only records why it was written.
const (
	eventState    = "state"
	eventProgress = "progress"
)

// hub wakes event-stream subscribers after events are committed. Subscribers
// read the events themselves from job_events, so a wake-up carries no data and
// coalescing several of them loses nothing.
type hub struct {
	mu sync.Mutex
	ch chan struct{}
}

func newHub() *hub { return &hub{ch: make(chan struct{})} }

// changed returns a channel closed by the next notify. Take it before reading
// events so a commit between the read and the wait is not missed.
func (h *hub) changed() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ch
}

func (h *hub) notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.ch)
	h.ch = make(chan struct{})
}

// insertEvent records the current state of rec as one job_events row.
func insertEvent(ctx context.Context, tx *sql.Tx, typ string, rec Record, now time.Time) error {
	payload, err := json.Marshal(rec.Event())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO job_events (job_id, type, payload, created_at) VALUES (?, ?, ?, ?)`,
		int64(rec.ID), typ, string(payload), clock.Millis(now))
	return err
}

// storedEvent is one job_events row as streamed to clients.
type storedEvent struct {
	ID      int64
	Payload string
}

// eventsAfter returns at most limit events with an ID greater than after, in ID order.
func eventsAfter(ctx context.Context, db *sql.DB, after int64, limit int) ([]storedEvent, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, payload FROM job_events WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var e storedEvent
		if err := rows.Scan(&e.ID, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// eventWindow returns the oldest retained event ID and the newest ID ever
// assigned. When every event has been pruned, oldest is head+1.
func eventWindow(ctx context.Context, db *sql.DB) (oldest, head int64, err error) {
	var minID, seq sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT (SELECT MIN(id) FROM job_events),
		(SELECT seq FROM sqlite_sequence WHERE name = 'job_events')`).Scan(&minID, &seq)
	if err != nil {
		return 0, 0, err
	}
	head = seq.Int64
	if minID.Valid {
		return minID.Int64, head, nil
	}
	return head + 1, head, nil
}

// EventHead returns the newest job event ID ever assigned, 0 before the
// first event. A page reads it before its data and opens the event stream
// with ?last_event_id=<head>, so an event committed while the page was being
// built is replayed instead of lost.
func EventHead(ctx context.Context, db *sql.DB) (int64, error) {
	_, head, err := eventWindow(ctx, db)
	return head, err
}

// pruneEvents applies retention: events older than maxAge, and all but the
// newest maxRows events, are deleted.
func pruneEvents(ctx context.Context, tx *sql.Tx, now time.Time, maxRows int, maxAge time.Duration) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_events WHERE created_at < ?`,
		clock.Millis(now.Add(-maxAge))); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM job_events WHERE id <
		(SELECT id FROM job_events ORDER BY id DESC LIMIT 1 OFFSET ?)`, maxRows-1)
	return err
}
