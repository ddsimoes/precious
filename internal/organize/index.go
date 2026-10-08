package organize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"precious/internal/content"
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

// ActionDone marks relations and review rows dirty once per action, and
// enqueues the source's hashing, whose plan recomputes its coverage once
// files moved into or out of the quarantine (r4 B5).
func (indexAdapter) ActionDone(ctx context.Context, tx *jobs.Tx, src domain.SourceID) error {
	if err := relations.RequestRefresh(tx); err != nil {
		return err
	}
	return content.EnqueueHashing(ctx, tx, src)
}

func (indexAdapter) MissingIntentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	return index.MissingIntentAt(ctx, q, src, path)
}

func (indexAdapter) IntentBelow(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	return index.IntentBelow(ctx, q, src, path)
}

// ApplyPurge deletes what a purge step removed (r4 D11): whole with its
// subtree when the step removed the whole item, else each removed entry with
// whatever is still indexed below it. Then it refolds the folders that held
// them, so the quarantine's row and the folders up to the top fold again.
// Entries no longer indexed are skipped. An entry of another source, or not
// strictly below the quarantine folder, is refused, and nothing changes.
func (a indexAdapter) ApplyPurge(ctx context.Context, tx *sql.Tx, src domain.SourceID, removed []domain.EntryID,
	whole domain.EntryID) error {
	ids := removed
	if whole != 0 {
		ids = []domain.EntryID{whole}
	}
	var parents []domain.EntryID
	seen := map[domain.EntryID]bool{}
	for _, id := range ids {
		var (
			source string
			parent sql.NullInt64
			path   []byte
		)
		err := tx.QueryRowContext(ctx, `SELECT source_id, parent_id, path FROM entries WHERE id = ?`, int64(id)).
			Scan(&source, &parent, &path)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("organize: read entry %d: %w", id, err)
		}
		if domain.SourceID(source) != src {
			return fmt.Errorf("organize: entry %d is on source %q, not %q", id, source, src)
		}
		if !insideQuarantine(path) {
			return fmt.Errorf("organize: entry %d is not inside the quarantine; a purge deletes nothing else", id)
		}
		if p := domain.EntryID(parent.Int64); !seen[p] {
			seen[p] = true
			parents = append(parents, p)
		}
	}
	var err error
	if whole != 0 {
		err = index.DeleteSubtree(ctx, tx, whole)
	} else {
		err = index.DeleteEntries(ctx, tx, removed)
	}
	if err != nil {
		return err
	}
	// A parent the purge removed too is skipped; the folder above the
	// outermost removed entry is not removed, and folds its chain.
	return a.rf.Refold(ctx, tx, src, parents)
}

// ApplyUnlink drops the row of the file at path, a record that a scan
// indexed, and refolds the folder that held it (r4 D4, D6). A path not
// indexed is nothing to drop. A path not strictly below the quarantine
// folder, or a folder's row, is refused.
func (a indexAdapter) ApplyUnlink(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte) error {
	if !insideQuarantine(path) {
		return fmt.Errorf("organize: %q is not inside the quarantine; an unlink drops nothing else", domain.DisplayName(path))
	}
	var (
		id     int64
		parent sql.NullInt64
		kind   string
	)
	err := tx.QueryRowContext(ctx, `SELECT id, parent_id, kind FROM entries WHERE source_id = ? AND path = ?`,
		string(src), path).Scan(&id, &parent, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("organize: read the entry at %q: %w", domain.DisplayName(path), err)
	}
	if kind == string(domain.EntryDirectory) {
		return fmt.Errorf("organize: %q is indexed as a folder, which an unlink does not remove", domain.DisplayName(path))
	}
	if err := index.DeleteSubtree(ctx, tx, domain.EntryID(id)); err != nil {
		return err
	}
	return a.rf.Refold(ctx, tx, src, []domain.EntryID{domain.EntryID(parent.Int64)})
}

// insideQuarantine reports whether path lies strictly below a source's
// quarantine folder.
func insideQuarantine(path []byte) bool {
	return index.IsQuarantinePath(path) && string(path) != index.QuarantineName
}

// parentOf returns the folder holding entry id.
func parentOf(ctx context.Context, tx *sql.Tx, id domain.EntryID) (domain.EntryID, error) {
	var parent sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT parent_id FROM entries WHERE id = ?`, int64(id)).Scan(&parent); err != nil {
		return 0, fmt.Errorf("organize: the folder holding entry %d: %w", id, err)
	}
	return domain.EntryID(parent.Int64), nil
}
