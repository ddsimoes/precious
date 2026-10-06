package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
)

type tagsBody struct {
	Tags []tagJSON `json:"tags"`
}

type tagJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// OwnCount is the number of entries carrying the tag as their own;
	// their descendants inherit it without being counted.
	OwnCount int64 `json:"own_count"`
}

// tags serves GET /api/tags: every tag by name (case-insensitive), with
// its own count.
func (h *handler) tags(w http.ResponseWriter, r *http.Request) {
	if err := checkParams(r.URL.Query()); err != nil {
		h.fail(w, err)
		return
	}
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		rows, err := tx.QueryContext(ctx, `SELECT t.id, t.name,
			(SELECT count(*) FROM entry_tags et WHERE et.tag_id = t.id)
			FROM tags t ORDER BY t.name, t.id`)
		if err != nil {
			return nil, fmt.Errorf("api: tags: %w", err)
		}
		defer rows.Close()
		body := tagsBody{Tags: []tagJSON{}}
		for rows.Next() {
			var t tagJSON
			if err := rows.Scan(&t.ID, &t.Name, &t.OwnCount); err != nil {
				return nil, fmt.Errorf("api: tags: %w", err)
			}
			body.Tags = append(body.Tags, t)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("api: tags: %w", err)
		}
		return body, nil
	})
}
