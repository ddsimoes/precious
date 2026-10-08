package organize

import (
	"context"
	"database/sql"
	"fmt"

	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/executor"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/store"
)

// Index returns the executor's view of the index (r3 design D6): each
// done step updates the entries, decisions, and folds in the outcome's
// transaction, and an action that ended with a done step asks for a
// relations refresh.
func (s *Service) Index() executor.Index { return indexAdapter{rf: s.rf} }

type indexAdapter struct{ rf *index.Refolder }

// ApplyRename moves the entry in the index (D6 steps 1–5), re-derives the
// effective decisions of its subtree under its new ancestors (step 6), and
// refolds the entry and both ancestor chains (step 7).
func (a indexAdapter) ApplyRename(ctx context.Context, tx *sql.Tx, m index.Move) error {
	oldParent, err := parentOf(ctx, tx, m.Entry)
	if err != nil {
		return err
	}
	if _, _, err := index.MoveEntry(ctx, tx, m); err != nil {
		return err
	}
	if err := decisions.Reinherit(ctx, tx, m.Entry); err != nil {
		return err
	}
	return a.rf.Refold(ctx, tx, m.Source, []domain.EntryID{m.Entry, oldParent})
}

// ApplyMkdir inserts the new folder, which inherits its parent's effective
// decision, and refolds it and its ancestors.
func (a indexAdapter) ApplyMkdir(ctx context.Context, tx *sql.Tx, f index.NewFolder) (domain.EntryID, error) {
	id, err := index.InsertFolder(ctx, tx, f)
	if err != nil {
		return 0, err
	}
	return id, a.rf.Refold(ctx, tx, f.Source, []domain.EntryID{id})
}

// ApplyRmdir deletes the folder and refolds the folder that held it.
func (a indexAdapter) ApplyRmdir(ctx context.Context, tx *sql.Tx, src domain.SourceID, id domain.EntryID,
	parentFacts index.PostFacts) error {
	parent, err := parentOf(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := index.RemoveFolder(ctx, tx, id, parentFacts); err != nil {
		return err
	}
	return a.rf.Refold(ctx, tx, src, []domain.EntryID{parent})
}

// ActionDone marks relations and review rows dirty once per action.
func (indexAdapter) ActionDone(_ context.Context, tx *jobs.Tx, _ domain.SourceID) error {
	return relations.RequestRefresh(tx)
}

func (indexAdapter) MissingIntentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	return index.MissingIntentAt(ctx, q, src, path)
}

func (indexAdapter) IntentBelow(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	return index.IntentBelow(ctx, q, src, path)
}

// parentOf returns the folder holding entry id.
func parentOf(ctx context.Context, tx *sql.Tx, id domain.EntryID) (domain.EntryID, error) {
	var parent sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT parent_id FROM entries WHERE id = ?`, int64(id)).Scan(&parent); err != nil {
		return 0, fmt.Errorf("organize: the folder holding entry %d: %w", id, err)
	}
	return domain.EntryID(parent.Int64), nil
}
