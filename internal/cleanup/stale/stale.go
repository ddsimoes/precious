// Package stale marks pre-delete checks stale when something they rely on
// changes in the index (r4 design D10, I9). A check is for one set of
// quarantined items and the copies it found: a step, a decision, or a
// classification at or above any of them makes it stale, and a stale check
// is never acted on.
//
// It is a leaf: it reads and writes the purge_checks tables through the
// caller's transaction and imports nothing of Precious but domain, so the
// executor, organize, and decisions can call it in their own transactions.
package stale

import (
	"context"
	"database/sql"
	"fmt"

	"precious/internal/domain"
)

// ReasonIndexChanged is the stale_reason MarkStale records: an entry at or
// below a path the check recorded changed in the index.
const ReasonIndexChanged = "index_changed"

// Statements of MarkStale. The live checks of the source are few, and each
// reads its own rows by the (check_id, path) index or its primary key; the
// copies are read by the (copy_source, copy_path) index. A path is a BLOB,
// and so are the bounds, which are bound, never spliced.
const (
	// markBelowSQL: reason, src, path, lo, hi, path, lo, hi, src, path, lo, hi.
	markBelowSQL = `UPDATE purge_checks SET state = 'stale', stale_reason = ?
		WHERE state IN ('running', 'ready') AND (
		  (source_id = ? AND (
		    EXISTS (SELECT 1 FROM purge_check_items i WHERE i.check_id = purge_checks.id
		      AND (i.path = ? OR (i.path >= ? AND i.path < ?)))
		    OR EXISTS (SELECT 1 FROM purge_check_files f WHERE f.check_id = purge_checks.id
		      AND (f.path = ? OR (f.path >= ? AND f.path < ?)))))
		  OR id IN (SELECT f.check_id FROM purge_check_files f WHERE f.copy_source = ?
		      AND (f.copy_path = ? OR (f.copy_path >= ? AND f.copy_path < ?))))`
	// markSourceSQL, for the source's top folder: reason, src, src.
	markSourceSQL = `UPDATE purge_checks SET state = 'stale', stale_reason = ?
		WHERE state IN ('running', 'ready') AND (source_id = ?
		  OR id IN (SELECT f.check_id FROM purge_check_files f WHERE f.copy_source = ? AND f.copy_path IS NOT NULL))`
)

// MarkStale marks stale, in tx, every running or ready check that relies on
// an entry of src at or below path: a check of src with a set item or a
// recorded file there, and a check of any source with a recorded copy there
// (its copy_source and copy_path). An empty path is the source's top
// folder, which holds every entry of src.
func MarkStale(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte) error {
	var err error
	if len(path) == 0 {
		_, err = tx.ExecContext(ctx, markSourceSQL, ReasonIndexChanged, string(src), string(src))
	} else {
		p := append([]byte(nil), path...)
		lo := append(append(make([]byte, 0, len(path)+1), path...), '/')
		hi := append(append(make([]byte, 0, len(path)+1), path...), '0')
		_, err = tx.ExecContext(ctx, markBelowSQL, ReasonIndexChanged, string(src), p, lo, hi, p, lo, hi,
			string(src), p, lo, hi)
	}
	if err != nil {
		return fmt.Errorf("stale: mark the checks relying on %q of source %q: %w", domain.DisplayName(path), src, err)
	}
	return nil
}
