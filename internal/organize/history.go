package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"precious/internal/domain"
	"precious/internal/web/apierr"
)

// Routes serves the history read API on mux (r3 design Interfaces, r4
// Interfaces); authentication is the caller's middleware.
//
//   - GET /api/history?source=&cursor=&limit= lists the actions that were
//     run, newest first: {"items":[Action],"next_cursor"};
//   - GET /api/history/{id} is one Action, in any state;
//   - GET /api/history/{id}/items?state=&op=&cursor=&limit= lists its items
//     in seq order, state and op repeating: {"items":[Item],"next_cursor"};
//   - GET /api/history/{id}/items/{item}/kept?cursor= lists the entries at
//     or below an item that the owner keeps, which block a cleanup item,
//     100 to a page: {"count","items":[EntryRow],"next_cursor"};
//   - GET /api/history/{id}/export.csv is every item of the action as CSV
//     (r4 D16).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("GET /api/history/{id}", s.action)
	mux.HandleFunc("GET /api/history/{id}/items", s.items)
	mux.HandleFunc("GET /api/history/{id}/items/{item}/kept", s.kept)
	mux.HandleFunc("GET /api/history/{id}/export.csv", s.export)
}

// serve answers r with what read returns, read in one read transaction, or
// with the error envelope.
func (s *Service) serve(w http.ResponseWriter, r *http.Request, read func(ctx context.Context, tx *sql.Tx) (any, error)) {
	var body any
	err := s.st.Read(r.Context(), func(tx *sql.Tx) error {
		var err error
		body, err = read(r.Context(), tx)
		return err
	})
	if err != nil {
		var de *domain.Error
		if !errors.As(err, &de) {
			s.log.Error("organize: internal error", "err", err)
		}
		apierr.FromError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// checkParams refuses a parameter not in allowed, and a repeated one other
// than those in repeats.
func checkParams(q url.Values, allowed, repeats []string) error {
	for k, vs := range q {
		if !slices.Contains(allowed, k) {
			return domain.Errorf(domain.CodeInvalidRequest, "unknown query parameter %q", k)
		}
		if len(vs) > 1 && !slices.Contains(repeats, k) {
			return domain.Errorf(domain.CodeInvalidRequest, "query parameter %q is repeated", k)
		}
	}
	return nil
}

// limitOf reads a page size: def when s is empty, else a positive integer
// capped at most.
func limitOf(s string, def, most int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, domain.Errorf(domain.CodeInvalidRequest, "limit must be a positive integer, not %q", s)
	}
	return min(n, most), nil
}

// pathID is the action ID of the request path; a malformed one names
// nothing.
func pathID(r *http.Request) (int64, error) {
	return parseID("action", r.PathValue("id"))
}

// history lists the actions that were run (queued, running, done, or
// stopped), newest first. The cursor is the ID of the last action given.
func (s *Service) history(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		q := r.URL.Query()
		if err := checkParams(q, []string{"source", "cursor", "limit"}, nil); err != nil {
			return nil, err
		}
		limit, err := limitOf(q.Get("limit"), historyDefaultLimit, historyMaxLimit)
		if err != nil {
			return nil, err
		}
		query := `SELECT id FROM actions WHERE state IN ('queued', 'running', 'done', 'stopped')`
		var args []any
		if src := q.Get("source"); src != "" {
			var known bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sources WHERE id = ?)`, src).Scan(&known); err != nil {
				return nil, err
			}
			if !known {
				return nil, notFound("source", src)
			}
			query += ` AND source_id = ?`
			args = append(args, src)
		}
		if c := q.Get("cursor"); c != "" {
			before, err := strconv.ParseInt(c, 10, 64)
			if err != nil || before < 1 {
				return nil, domain.Errorf(domain.CodeInvalidRequest, "invalid cursor %q", c)
			}
			query += ` AND id < ?`
			args = append(args, before)
		}
		query += ` ORDER BY id DESC LIMIT ?`
		args = append(args, limit+1)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		var next *string
		if len(ids) > limit {
			ids = ids[:limit]
			c := strconv.FormatInt(ids[limit-1], 10)
			next = &c
		}
		items, err := readActions(ctx, tx, ids, s.clk.Now())
		if err != nil {
			return nil, err
		}
		return page[actionJSON]{Items: items, NextCursor: next}, nil
	})
}

// action answers one action in any state.
func (s *Service) action(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		if err := checkParams(r.URL.Query(), nil, nil); err != nil {
			return nil, err
		}
		id, err := pathID(r)
		if err != nil {
			return nil, err
		}
		return readAction(ctx, tx, id, s.clk.Now())
	})
}

// items lists an action's items in seq order, of the states and ops given
// (each repeating; none is every one). The cursor is the seq of the last
// item given.
func (s *Service) items(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		q := r.URL.Query()
		if err := checkParams(q, []string{"state", "op", "cursor", "limit"}, []string{"state", "op"}); err != nil {
			return nil, err
		}
		id, err := pathID(r)
		if err != nil {
			return nil, err
		}
		states := q["state"]
		for _, st := range states {
			if !slices.Contains(itemStates, st) {
				return nil, domain.Errorf(domain.CodeInvalidRequest, "unknown item state %q", st)
			}
		}
		ops := q["op"]
		for _, op := range ops {
			if !slices.Contains(itemOps, op) {
				return nil, domain.Errorf(domain.CodeInvalidRequest, "unknown item op %q", op)
			}
		}
		limit, err := limitOf(q.Get("limit"), itemsDefaultLimit, itemsMaxLimit)
		if err != nil {
			return nil, err
		}
		after, err := parseSeqCursor(q.Get("cursor"))
		if err != nil {
			return nil, err
		}
		if _, _, _, err := actionState(ctx, tx, id); err != nil {
			return nil, err
		}
		return readItems(ctx, tx, id, states, ops, after, limit)
	})
}
