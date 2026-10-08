package api

import (
	"context"
	"database/sql"
	"fmt"

	"precious/internal/domain"
	"precious/internal/search"
)

// EntryRow is the EntryRow JSON of the Interfaces section, for the responses
// of other packages that name entries: organize's actions and their items
// (r3 design Interfaces), which then read exactly like a children row.
type EntryRow = entryRow

// EntryRows reads the EntryRows of the entries ids inside tx, each with its
// own tags. An ID naming no entry is left out; repeated IDs are read once.
func EntryRows(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) (map[domain.EntryID]*EntryRow, error) {
	out := make(map[domain.EntryID]*EntryRow, len(ids))
	args := make([]any, 0, len(ids))
	seen := make(map[domain.EntryID]bool, len(ids))
	for _, id := range ids {
		if id != 0 && !seen[id] {
			seen[id] = true
			args = append(args, int64(id))
		}
	}
	const chunk = 500
	for len(args) > 0 {
		n := min(len(args), chunk)
		rows, err := appendRows(ctx, tx, nil, `SELECT `+search.Columns+` FROM `+search.From+
			` WHERE e.id IN (`+marks(n)+`)`, args[:n])
		if err != nil {
			return nil, fmt.Errorf("api: entry rows: %w", err)
		}
		if err := search.LoadTags(ctx, tx, rows); err != nil {
			return nil, err
		}
		for i := range rows {
			r := rowJSON(&rows[i])
			out[rows[i].ID] = &r
		}
		args = args[n:]
	}
	return out, nil
}
