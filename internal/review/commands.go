package review

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"precious/internal/commands"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/search"
)

// CommandSelectList is the select-list command's name.
const CommandSelectList = "select-list"

// RegisterCommands installs select-list on h, storing selections through d.
func RegisterCommands(h *commands.Handler, d *decisions.Service) {
	h.Register(CommandSelectList, func(body []byte) (commands.Operation, error) { return decodeSelectList(d, body) })
}

// selectListRequest is {"list":"system_junk","source_id":"…"?}. Its
// canonical form is also the selection's stored query.
type selectListRequest struct {
	List     string `json:"list"`
	SourceID string `json:"source_id,omitempty"`
}

// The response is create-selection's.
type keptJSON struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

type selectionResponse struct {
	SelectionID string    `json:"selection_id"`
	Count       int64     `json:"count"`
	Bytes       int64     `json:"bytes"`
	Kept        keptJSON  `json:"kept"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// selectListOp resolves a review list's open rows into an R1 selection
// (D13): the rows of the visible generation, joined with the current
// decisions inside the command's transaction.
type selectListOp struct {
	d         *decisions.Service
	req       selectListRequest
	canonical []byte
}

func decodeSelectList(d *decisions.Service, body []byte) (commands.Operation, error) {
	var req selectListRequest
	if err := commands.DecodeStrict(body, &req); err != nil {
		return nil, err
	}
	l := List(req.List)
	switch {
	case req.List == "":
		return nil, domain.Errorf(domain.CodeInvalidRequest, "list is required")
	case !l.Valid():
		return nil, domain.Errorf(domain.CodeInvalidRequest, "unknown review list %q", req.List)
	case l == ListDuplicates:
		return nil, domain.Errorf(domain.CodeInvalidRequest, "the duplicates list has no select-all; decide its copies one by one")
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return &selectListOp{d: d, req: req, canonical: canonical}, nil
}

func (o *selectListOp) Canonical() []byte             { return o.canonical }
func (o *selectListOp) Prepare(context.Context) error { return nil }

func (o *selectListOp) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	q := tx.SQL()
	if o.req.SourceID != "" {
		var one int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM sources WHERE id = ?`, o.req.SourceID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil, domain.Errorf(domain.CodeUnknownSource, "no source %q", o.req.SourceID)
		}
		if err != nil {
			return 0, nil, err
		}
	}
	ids, err := Resolve(ctx, q, List(o.req.List), domain.SourceID(o.req.SourceID), search.MaxResolve)
	if err != nil {
		return 0, nil, err
	}
	sel, err := o.d.NewSelection(ctx, q, o.canonical, ids)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, selectionResponse{
		SelectionID: sel.ID, Count: sel.Count, Bytes: sel.Bytes,
		Kept:      keptJSON{Count: sel.KeptCount, Bytes: sel.KeptBytes},
		ExpiresAt: sel.ExpiresAt.UTC(),
	}, nil
}
