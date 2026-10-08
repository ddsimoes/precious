package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/web/api"
)

// itemStates are the states of an item, every one counted in an Action.
var itemStates = []string{"planned", "refused", "conflict", "intent", "done", "not_permitted", "offline", "changed",
	"failed", "no_safe_rename", "not_empty", "manual_recovery", "not_attempted", "resolved", "blocked"}

// Why an action cannot be undone (Action.undo.reason).
const (
	undoNotDone       = "not_done"
	undoAlreadyUndone = "already_undone"
	undoNothingDone   = "nothing_done"
)

// actionJSON is Action of the Interfaces section.
type actionJSON struct {
	ID          string           `json:"id"`
	Kind        string           `json:"kind"`
	SourceID    domain.SourceID  `json:"source_id"`
	State       string           `json:"state"`
	CreatedAt   time.Time        `json:"created_at"`
	ExpiresAt   *time.Time       `json:"expires_at"`
	StartedAt   *time.Time       `json:"started_at"`
	FinishedAt  *time.Time       `json:"finished_at"`
	Destination *api.EntryRow    `json:"destination"`
	JobID       *string          `json:"job_id"`
	UndoOf      *string          `json:"undo_of"`
	Bulk        bool             `json:"bulk"`
	Counts      map[string]int64 `json:"counts"`
	// Bytes and Files are those of the items that are planned, under way,
	// or done: what the action moves or moved.
	Bytes    int64    `json:"bytes"`
	Files    int64    `json:"files"`
	KeptLost int64    `json:"kept_lost"`
	Reversed int64    `json:"reversed"`
	Undo     undoJSON `json:"undo"`
}

type undoJSON struct {
	Possible bool    `json:"possible"`
	Reason   *string `json:"reason"`
}

// pathJSON is a path in its display form and its raw bytes.
type pathJSON struct {
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
}

// pathOf is a stored path, null when NULL; the empty path is a source's
// top folder.
func pathOf(p sql.Null[[]byte]) *pathJSON {
	if !p.Valid {
		return nil
	}
	if p.V == nil {
		p.V = []byte{}
	}
	return &pathJSON{Path: domain.DisplayName(p.V), PathB64: p.V}
}

// foundJSON is what a step that could not be confirmed found at each name.
type foundJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// itemJSON is Item of the Interfaces section.
type itemJSON struct {
	ID            string        `json:"id"`
	Seq           int64         `json:"seq"`
	Op            string        `json:"op"`
	Entry         *api.EntryRow `json:"entry"`
	From          *pathJSON     `json:"from"`
	To            *pathJSON     `json:"to"`
	State         string        `json:"state"`
	Reason        *string       `json:"reason"`
	DecisionAfter *string       `json:"decision_after"`
	Detail        *string       `json:"detail"`
	Found         *foundJSON    `json:"found"`
	Reversed      bool          `json:"reversed"`
	Bytes         int64         `json:"bytes"`
	Files         int64         `json:"files"`
}

// page is a page of actions or items.
type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// planResponse answers a plan-* command (201).
type planResponse struct {
	Action     actionJSON `json:"action"`
	Items      []itemJSON `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

func timeOf(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := clock.FromMillis(ms.Int64).UTC()
	return &t
}

func idString(id sql.NullInt64) *string {
	if !id.Valid {
		return nil
	}
	s := strconv.FormatInt(id.Int64, 10)
	return &s
}

func strPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// readAction returns the Action of id; not_found when there is none.
func readAction(ctx context.Context, tx *sql.Tx, id int64, now time.Time) (actionJSON, error) {
	out, err := readActions(ctx, tx, []int64{id}, now)
	if err != nil {
		return actionJSON{}, err
	}
	if len(out) == 0 {
		return actionJSON{}, notFound("action", strconv.FormatInt(id, 10))
	}
	return out[0], nil
}

// readActions returns the Actions of ids, in that order; an unknown ID is
// left out. A planned action past its expiry reads expired.
func readActions(ctx context.Context, tx *sql.Tx, ids []int64, now time.Time) ([]actionJSON, error) {
	if len(ids) == 0 {
		return []actionJSON{}, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	in := placeholders(len(ids))
	byID := make(map[int64]*actionJSON, len(ids))
	var dests []domain.EntryID
	destOf := map[int64]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, source_id, state, bulk, destination_id, undo_of, kept_lost,
		job_id, created_at, expires_at, started_at, finished_at FROM actions WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("organize: read actions: %w", err)
	}
	for rows.Next() {
		var (
			a                                        actionJSON
			id, created                              int64
			dest, undoOf, job, expires, start, finsh sql.NullInt64
		)
		if err := rows.Scan(&id, &a.Kind, &a.SourceID, &a.State, &a.Bulk, &dest, &undoOf, &a.KeptLost, &job, &created,
			&expires, &start, &finsh); err != nil {
			rows.Close()
			return nil, err
		}
		a.ID, a.UndoOf, a.JobID = strconv.FormatInt(id, 10), idString(undoOf), idString(job)
		a.CreatedAt, a.ExpiresAt, a.StartedAt, a.FinishedAt = clock.FromMillis(created).UTC(), timeOf(expires),
			timeOf(start), timeOf(finsh)
		if a.State == "planned" && expires.Valid && expires.Int64 <= clock.Millis(now) {
			a.State = "expired"
		}
		a.Counts = make(map[string]int64, len(itemStates))
		for _, s := range itemStates {
			a.Counts[s] = 0
		}
		if dest.Valid {
			dests = append(dests, domain.EntryID(dest.Int64))
			destOf[id] = dest.Int64
		}
		byID[id] = &a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Counts by state, bytes and files of what moves, reversed items, and
	// the reversible done items left (D11).
	rows, err = tx.QueryContext(ctx, `SELECT action_id, state, count(*),
			coalesce(sum(bytes) FILTER (WHERE state IN ('planned', 'intent', 'done')), 0),
			coalesce(sum(files) FILTER (WHERE state IN ('planned', 'intent', 'done')), 0),
			count(*) FILTER (WHERE reversed_by IS NOT NULL),
			count(*) FILTER (WHERE state = 'done' AND entry_id IS NOT NULL AND (op = 'rename' OR op = 'mkdir' AND created = 1)),
			count(*) FILTER (WHERE state = 'done' AND entry_id IS NOT NULL AND (op = 'rename' OR op = 'mkdir' AND created = 1)
				AND reversed_by IS NULL)
		FROM action_items WHERE action_id IN (`+in+`) GROUP BY action_id, state`, args...)
	if err != nil {
		return nil, fmt.Errorf("organize: count items: %w", err)
	}
	reversible, left := map[int64]int64{}, map[int64]int64{}
	for rows.Next() {
		var (
			id, n, bytes, files, reversed, rev, lft int64
			state                                   string
		)
		if err := rows.Scan(&id, &state, &n, &bytes, &files, &reversed, &rev, &lft); err != nil {
			rows.Close()
			return nil, err
		}
		a := byID[id]
		a.Counts[state] = n
		a.Bytes += bytes
		a.Files += files
		a.Reversed += reversed
		reversible[id] += rev
		left[id] += lft
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	destRows, err := api.EntryRows(ctx, tx, dests)
	if err != nil {
		return nil, err
	}
	out := make([]actionJSON, 0, len(ids))
	for _, id := range ids {
		a, ok := byID[id]
		if !ok {
			continue
		}
		if d, ok := destOf[id]; ok {
			a.Destination = destRows[domain.EntryID(d)]
		}
		var reason string
		switch {
		case a.State != "done" && a.State != "stopped":
			reason = undoNotDone
		case reversible[id] == 0:
			reason = undoNothingDone
		case left[id] == 0:
			reason = undoAlreadyUndone
		}
		a.Undo = undoJSON{Possible: reason == ""}
		if reason != "" {
			a.Undo.Reason = &reason
		}
		out = append(out, *a)
	}
	return out, nil
}

// readItems returns a page of the items of action after seq, in seq order,
// only those in states when it is not empty.
func readItems(ctx context.Context, tx *sql.Tx, action int64, states []string, after int64, limit int) (page[itemJSON], error) {
	query := `SELECT id, seq, op, entry_id, from_path, to_path, state, reason, decision_after, detail,
		reversed_by IS NOT NULL, bytes, files FROM action_items WHERE action_id = ? AND seq > ?`
	args := []any{action, after}
	if len(states) > 0 {
		query += ` AND state IN (` + placeholders(len(states)) + `)`
		for _, s := range states {
			args = append(args, s)
		}
	}
	query += ` ORDER BY seq LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return page[itemJSON]{}, fmt.Errorf("organize: read items: %w", err)
	}
	items := []itemJSON{}
	entryOf := map[int]domain.EntryID{}
	var entries []domain.EntryID
	for rows.Next() {
		var (
			it               itemJSON
			id               int64
			entry            sql.NullInt64
			from, to         sql.Null[[]byte]
			reason, after, d sql.NullString
		)
		if err := rows.Scan(&id, &it.Seq, &it.Op, &entry, &from, &to, &it.State, &reason, &after, &d, &it.Reversed,
			&it.Bytes, &it.Files); err != nil {
			rows.Close()
			return page[itemJSON]{}, err
		}
		it.ID, it.From, it.To = strconv.FormatInt(id, 10), pathOf(from), pathOf(to)
		it.Reason, it.DecisionAfter, it.Detail = strPtr(reason), strPtr(after), strPtr(d)
		if d.Valid && (it.State == "manual_recovery" || it.State == "resolved") {
			var f foundJSON
			if err := json.Unmarshal([]byte(d.String), &f); err == nil && f.From != "" && f.To != "" {
				it.Found, it.Detail = &f, nil
			}
		}
		if entry.Valid {
			entryOf[len(items)] = domain.EntryID(entry.Int64)
			entries = append(entries, domain.EntryID(entry.Int64))
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page[itemJSON]{}, err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		c := strconv.FormatInt(items[limit-1].Seq, 10)
		next = &c
	}
	rowsOf, err := api.EntryRows(ctx, tx, entries)
	if err != nil {
		return page[itemJSON]{}, err
	}
	for i := range items {
		if e, ok := entryOf[i]; ok {
			items[i].Entry = rowsOf[e]
		}
	}
	return page[itemJSON]{Items: items, NextCursor: next}, nil
}

// parseSeqCursor reads an items cursor: the seq of the last item given.
func parseSeqCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		return 0, domain.Errorf(domain.CodeInvalidRequest, "invalid cursor %q", s)
	}
	return n, nil
}

// actionState reads an action's stored state and source; not_found when
// there is none.
func actionState(ctx context.Context, tx *sql.Tx, id int64) (state string, src domain.SourceID, expires sql.NullInt64, err error) {
	err = tx.QueryRowContext(ctx, `SELECT state, source_id, expires_at FROM actions WHERE id = ?`, id).
		Scan(&state, &src, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", expires, notFound("action", strconv.FormatInt(id, 10))
	}
	return state, src, expires, err
}

// planResult reads the response of a plan: the action and its first page
// of items.
func planResult(ctx context.Context, tx *sql.Tx, id int64, now time.Time) (planResponse, error) {
	a, err := readAction(ctx, tx, id, now)
	if err != nil {
		return planResponse{}, err
	}
	items, err := readItems(ctx, tx, id, nil, 0, itemsDefaultLimit)
	if err != nil {
		return planResponse{}, err
	}
	return planResponse{Action: a, Items: items.Items, NextCursor: items.NextCursor}, nil
}
