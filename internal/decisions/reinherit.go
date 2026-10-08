package decisions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"precious/internal/domain"
)

// Reinherit recomputes the effective decisions of entry id and its subtree
// under the entry's current ancestors, after a move gave it a new parent
// (r3 design D6 step 6), in the move's transaction. The entry inherits its
// new parent's effective decision unless it has its own; then the folder's
// effective decision is propagated below it with the cut points of the
// folders that have their own, which keep theirs and pass them on. Own
// decisions never change. An unknown entry is not_found.
func Reinherit(ctx context.Context, tx *sql.Tx, id domain.EntryID) error {
	var (
		src    string
		path   []byte
		kind   string
		parent sql.NullInt64
		own    sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT source_id, path, kind, parent_id, decision FROM entries WHERE id = ?`,
		int64(id)).Scan(&src, &path, &kind, &parent, &own)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeNotFound, "entry %d not found", id)
	}
	if err != nil {
		return fmt.Errorf("decisions: read entry: %w", err)
	}
	eff, from := domain.Decision(own.String), &id
	if !own.Valid {
		if eff, from, err = parentEffective(ctx, tx, parent); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE entries SET eff_decision = ?, eff_from = ? WHERE id = ?
		AND (eff_decision <> ? OR eff_from IS NOT ?)`,
		string(eff), nullID(from), int64(id), string(eff), nullID(from)); err != nil {
		return fmt.Errorf("decisions: set effective decision: %w", err)
	}
	if kind != string(domain.EntryDirectory) {
		return nil
	}
	return propagate(ctx, tx, domain.SourceID(src), nonNil(path), eff, from)
}
