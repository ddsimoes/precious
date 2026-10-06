package decisions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/search"
)

// SelectionTTL is how long a selection can be named after it is created
// (§11.3).
const SelectionTTL = time.Hour

// selectionTombstone is how long an expired selection's row is kept after
// its entries are pruned, so that a request naming it still answers
// selection_expired rather than not_found.
const selectionTombstone = 24 * time.Hour

// selectionChunk bounds the IDs of one selection_entries insert.
const selectionChunk = 32768

// Selection is the explicit set of entries a search resolved to when it was
// created (design D10), with what the owner confirms before a bulk request:
// the count of entries, their bytes, and the count and bytes of the kept
// ones among them (effective decision keep). Bytes are total_bytes (a
// file's size, a folder's subtree sum) counted once: an entry inside a
// selected folder adds nothing to Bytes, and a kept entry inside a selected
// kept folder adds nothing to KeptBytes.
type Selection struct {
	ID        string
	Count     int64
	Bytes     int64
	KeptCount int64
	KeptBytes int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSelection resolves q against the committed index inside tx and
// stores the matching entry IDs, expiring after SelectionTTL. Entries a scan
// indexes later never join it. A bad query value is invalid_request, as is a
// query matching more than search.MaxResolve entries. It first prunes the
// entries of expired selections.
func (s *Service) CreateSelection(ctx context.Context, tx *sql.Tx, q search.Query) (Selection, error) {
	ids, err := search.Resolve(ctx, tx, q, search.MaxResolve)
	if err != nil {
		return Selection{}, err
	}
	query, err := json.Marshal(q)
	if err != nil {
		return Selection{}, fmt.Errorf("decisions: encode selection query: %w", err)
	}
	return s.NewSelection(ctx, tx, query, ids)
}

// NewSelection stores a selection of the given entries, which another
// resolver (a review list, R2 design D13) found inside tx, with query as the
// JSON that describes them. Counts, bytes, kept figures, and expiry are those
// of CreateSelection. Repeated IDs count once; more than search.MaxResolve
// entries is invalid_request, and an ID with no entry is not_found. It first
// prunes the entries of expired selections.
func (s *Service) NewSelection(ctx context.Context, tx *sql.Tx, query json.RawMessage, ids []domain.EntryID) (Selection, error) {
	if !json.Valid(query) {
		return Selection{}, errors.New("decisions: selection query is not JSON")
	}
	if !strictlyAscending(ids) {
		ids = slices.Clone(ids)
		slices.Sort(ids)
		ids = slices.Compact(ids)
	}
	if len(ids) > search.MaxResolve {
		return Selection{}, domain.Errorf(domain.CodeInvalidRequest,
			"a selection holds at most %d entries; this one has %d", search.MaxResolve, len(ids))
	}
	now := s.clk.Now()
	if err := pruneSelections(ctx, tx, now); err != nil {
		return Selection{}, err
	}
	sel := Selection{ID: rand.Text(), Count: int64(len(ids)), CreatedAt: now, ExpiresAt: now.Add(SelectionTTL)}
	if _, err := tx.ExecContext(ctx, `INSERT INTO selections (id, query, count, bytes, kept, created_at, expires_at)
		VALUES (?, ?, ?, 0, 0, ?, ?)`, sel.ID, string(query), sel.Count, clock.Millis(now), clock.Millis(sel.ExpiresAt)); err != nil {
		return Selection{}, fmt.Errorf("decisions: create selection: %w", err)
	}
	for rest := ids; len(rest) > 0; {
		chunk := rest[:min(len(rest), selectionChunk)]
		rest = rest[len(chunk):]
		if _, err := tx.ExecContext(ctx, `INSERT INTO selection_entries (selection_id, entry_id) SELECT ?, value FROM json_each(?)`,
			sel.ID, idArray(chunk)); err != nil {
			return Selection{}, fmt.Errorf("decisions: store selection entries: %w", err)
		}
	}
	var found int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM selection_entries s JOIN entries e ON e.id = s.entry_id
		WHERE s.selection_id = ?`, sel.ID).Scan(&found); err != nil {
		return Selection{}, fmt.Errorf("decisions: create selection: %w", err)
	}
	if found != sel.Count {
		return Selection{}, domain.Errorf(domain.CodeNotFound, "%d of the selection's entries do not exist", sel.Count-found)
	}
	if err := tx.QueryRowContext(ctx, selectionMeasureSQL, sel.ID).Scan(&sel.Bytes, &sel.KeptCount, &sel.KeptBytes); err != nil {
		return Selection{}, fmt.Errorf("decisions: measure selection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE selections SET bytes = ?, kept = ? WHERE id = ?`,
		sel.Bytes, sel.KeptCount, sel.ID); err != nil {
		return Selection{}, fmt.Errorf("decisions: create selection: %w", err)
	}
	return sel, nil
}

// strictlyAscending reports whether ids are sorted with no repeats, as
// search.Resolve returns them.
func strictlyAscending(ids []domain.EntryID) bool {
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			return false
		}
	}
	return true
}

// selectionMeasureSQL measures a selection in one sorted pass. Each entry
// gets the key "/" + path + "/" ("/" for the root), so that the keys of a
// folder's descendants are exactly those that extend the folder's key: a
// contiguous run right after it in key order, ending before the folder's
// key with its last "/" made "0". (Plain paths would not do: "x!/…" sorts
// between "x" and "x/…".) An entry lies inside a selected folder when the
// largest run end of the selected folders before it, in its source, is
// above its key: runs are nested or disjoint, so the largest end is that of
// the outermost open folder.
const selectionMeasureSQL = `WITH sel AS (
		SELECT e.source_id, e.total_bytes, e.eff_decision = 'keep' AS kept, e.kind = 'directory' AS dir,
			CASE WHEN e.path = X'' THEN X'2F' ELSE CAST(X'2F' || e.path || X'2F' AS BLOB) END AS k,
			CASE WHEN e.path = X'' THEN X'30' ELSE CAST(X'2F' || e.path || X'30' AS BLOB) END AS run_end
		FROM selection_entries s JOIN entries e ON e.id = s.entry_id WHERE s.selection_id = ?
	), covered AS (
		SELECT total_bytes, kept, k,
			max(CASE WHEN dir THEN run_end END) OVER before AS folder_end,
			max(CASE WHEN dir AND kept THEN run_end END) OVER before AS kept_folder_end
		FROM sel
		WINDOW before AS (PARTITION BY source_id ORDER BY k ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING)
	)
	SELECT coalesce(sum(total_bytes) FILTER (WHERE folder_end IS NULL OR folder_end <= k), 0),
		count(*) FILTER (WHERE kept),
		coalesce(sum(total_bytes) FILTER (WHERE kept AND (kept_folder_end IS NULL OR kept_folder_end <= k)), 0)
	FROM covered`

// pruneSelections deletes the entries of every expired selection, and the
// rows of selections expired for longer than selectionTombstone.
func pruneSelections(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM selection_entries
		WHERE selection_id IN (SELECT id FROM selections WHERE expires_at <= ?)`, clock.Millis(now)); err != nil {
		return fmt.Errorf("decisions: prune selections: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM selections WHERE expires_at <= ?`,
		clock.Millis(now.Add(-selectionTombstone))); err != nil {
		return fmt.Errorf("decisions: prune selections: %w", err)
	}
	return nil
}

// selectionTargets returns the entries of selection id: selection_expired
// once it has expired, not_found when no such selection is known or when one
// of its entries no longer exists (its source was removed).
func selectionTargets(ctx context.Context, tx *sql.Tx, now time.Time, id string) (targets, error) {
	var count, expires int64
	err := tx.QueryRowContext(ctx, `SELECT count, expires_at FROM selections WHERE id = ?`, id).Scan(&count, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return targets{}, domain.Errorf(domain.CodeNotFound, "selection %q not found", id)
	}
	if err != nil {
		return targets{}, fmt.Errorf("decisions: read selection: %w", err)
	}
	if expires <= clock.Millis(now) {
		return targets{}, domain.Errorf(domain.CodeSelectionExpired, "the selection expired at %s; select again",
			clock.FromMillis(expires).Format(time.RFC3339))
	}
	t := targets{sub: `SELECT entry_id AS id FROM selection_entries WHERE selection_id = ?`, args: []any{id}, n: int(count), selection: id}
	var present int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM selection_entries s JOIN entries e ON e.id = s.entry_id
		WHERE s.selection_id = ?`, id).Scan(&present); err != nil {
		return targets{}, fmt.Errorf("decisions: check selection entries: %w", err)
	}
	if present != count {
		return targets{}, domain.Errorf(domain.CodeNotFound, "%d entries of the selection no longer exist; select again", count-present)
	}
	return t, nil
}
