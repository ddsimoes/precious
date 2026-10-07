package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"precious/internal/domain"
	"precious/internal/search"
)

type searchBody struct {
	Items      []entryRow `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type countBody struct {
	Count matchCount `json:"count"`
}

// matchCount is the count of a search: a number up to search.CountCap, the
// string "10000+" beyond.
type matchCount struct {
	n      int
	capped bool
}

func (c matchCount) MarshalJSON() ([]byte, error) {
	if c.capped {
		return json.Marshal(fmt.Sprintf("%d+", search.CountCap))
	}
	return json.Marshal(c.n)
}

// search serves GET /api/search: the parameters of search.Parse plus
// cursor and limit (default 200, at most 1,000), answered by search.Page
// in one read transaction. With count=only it answers the match count
// instead (search.Count), a request of its own so the page never waits for
// it (r2b design D8); cursor and limit are then ignored.
func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	query, err := search.Parse(v)
	if err == nil && (len(v["cursor"]) > 1 || len(v["limit"]) > 1 || len(v["count"]) > 1) {
		err = domain.Errorf(domain.CodeInvalidRequest, "cursor, limit, and count take one value each")
	}
	countOnly := false
	if err == nil {
		switch c := v.Get("count"); c {
		case "":
		case "only":
			countOnly = true
		default:
			err = domain.Errorf(domain.CodeInvalidRequest, "count must be only, not %q", c)
		}
	}
	var limit int
	if err == nil && !countOnly {
		limit, err = parseLimit(v.Get("limit"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if countOnly {
		h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
			n, capped, err := search.Count(ctx, tx, query)
			if err != nil {
				return nil, err
			}
			return countBody{Count: matchCount{n: n, capped: capped}}, nil
		})
		return
	}
	cursor := v.Get("cursor")
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		res, err := search.Page(ctx, tx, query, cursor, limit)
		if err != nil {
			return nil, err
		}
		body := searchBody{Items: rowsJSON(res.Items)}
		if res.NextCursor != "" {
			body.NextCursor = &res.NextCursor
		}
		return body, nil
	})
}
