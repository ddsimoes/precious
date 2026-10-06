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
	Count      matchCount `json:"count"`
}

// matchCount is the count of a search page: a number up to
// search.CountCap, the string "10000+" beyond.
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
// in one read transaction so the page and its count agree.
func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	query, err := search.Parse(v)
	if err == nil && (len(v["cursor"]) > 1 || len(v["limit"]) > 1) {
		err = domain.Errorf(domain.CodeInvalidRequest, "cursor and limit take one value each")
	}
	var limit int
	if err == nil {
		limit, err = parseLimit(v.Get("limit"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	cursor := v.Get("cursor")
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		res, err := search.Page(ctx, tx, query, cursor, limit)
		if err != nil {
			return nil, err
		}
		body := searchBody{Items: rowsJSON(res.Items), Count: matchCount{n: res.Count, capped: res.CountCapped}}
		if res.NextCursor != "" {
			body.NextCursor = &res.NextCursor
		}
		return body, nil
	})
}
