package organize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"precious/internal/domain"
	"precious/internal/executor"
)

// unconfirmedListed bounds the files a purge_not_allowed error names.
const unconfirmedListed = 10

// unconfirmedSQL lists, by path, the files of a check that still need the
// owner's confirmation (r4 D8, U10), as executor.PurgeGate counts them:
// check, limit. A member reads as its path inside its archive's. The records
// of an item that is not readable are left out: it is refused, never purged.
const unconfirmedSQL = `SELECT f.path, m.path FROM purge_check_files f JOIN purge_checks c ON c.id = f.check_id
	LEFT JOIN archive_members m ON m.id = f.member_id
	WHERE f.check_id = ? AND f.confirmed_at IS NULL AND (f.verdict IN ('copy_offline', 'unreadable')
		OR (f.verdict = 'opaque_archive' AND f.copy_path IS NULL)
		OR (f.verdict = 'unique' AND (f.class IS NOT 'likely_junk' OR c.junk_confirmed_at IS NULL)))
		AND NOT EXISTS (SELECT 1 FROM purge_check_items i WHERE i.check_id = f.check_id AND i.entry_id = f.item_id
			AND i.readable = 0)
	ORDER BY f.path, f.id LIMIT ?`

// GatePurge refuses acting on the pre-delete check check of source src (r4
// D10, D11): 409 check_running while it runs, check_stale once it is stale,
// failed, gone, or of another source, and purge_not_allowed, naming the
// files still unconfirmed, while one is. plan-purge and run-action call it
// in their transaction.
func GatePurge(ctx context.Context, tx *sql.Tx, src domain.SourceID, check int64) error {
	csrc, state, unconfirmed, found, err := executor.PurgeGate(ctx, tx, check)
	if err != nil {
		return err
	}
	switch {
	case !found || csrc != src:
		return domain.Errorf(domain.CodeCheckStale, "the check of this purge is gone; check the items again")
	case state == "running":
		return domain.Errorf(domain.CodeCheckRunning, "check %d is still running; wait for it to end", check)
	case state != "ready":
		return domain.Errorf(domain.CodeCheckStale,
			"check %d is out of date: something it relied on has changed since; check the items again", check)
	case !unconfirmed:
		return nil
	}
	rows, err := tx.QueryContext(ctx, unconfirmedSQL, check, unconfirmedListed+1)
	if err != nil {
		return fmt.Errorf("organize: unconfirmed files of check %d: %w", check, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var (
			path   []byte
			member sql.Null[[]byte]
		)
		if err := rows.Scan(&path, &member); err != nil {
			return err
		}
		name := domain.DisplayName(path)
		if member.Valid {
			name = domain.DisplayName(member.V) + " inside " + name
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	more := ""
	if len(names) > unconfirmedListed {
		names, more = names[:unconfirmedListed], ", and more"
	}
	return domain.Errorf(domain.CodePurgeNotAllowed,
		"these files have no verified copy and are not confirmed yet: %s%s; confirm them, or take them out of the set and check it again",
		strings.Join(names, ", "), more)
}

// gateAction gates running a purge action on its check (GatePurge); every
// other kind passes.
func gateAction(ctx context.Context, tx *sql.Tx, id int64) error {
	var (
		kind  string
		src   domain.SourceID
		check sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `SELECT kind, source_id, check_id FROM actions WHERE id = ?`, id).Scan(&kind, &src, &check)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("action", fmt.Sprint(id))
	}
	if err != nil || kind != "purge" {
		return err
	}
	if !check.Valid {
		return domain.Errorf(domain.CodeCheckStale, "the check of this purge is gone; check the items again")
	}
	return GatePurge(ctx, tx, src, check.Int64)
}
