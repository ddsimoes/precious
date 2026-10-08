package decisions

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"precious/internal/cleanup/stale"
	"precious/internal/domain"
)

// The quarantine of a source is the folder quarantineName at its top (r4
// design D1, D2), which every reader that shows or counts the disk leaves
// out. index exports the same name and conditions (index.QuarantineName,
// NotQuarantined, IsQuarantinePath); they are spelled out here because
// index's own tests import this package, so it cannot import index. A test
// checks that both agree.
const quarantineName = ".precious-quarantine"

// The quarantine's path bounds as SQL BLOB literals: the folder's own path,
// and the half-open range [Q/, Q0) of the paths below it.
var (
	quarantineHex = fmt.Sprintf("%x", quarantineName)
	quarantineSQL = "X'" + quarantineHex + "'"
	quarantineLo  = "X'" + quarantineHex + "2f'"
	quarantineHi  = "X'" + quarantineHex + "30'"
)

// notQuarantined is index.NotQuarantined: a residual condition, on the
// entries row aliased alias, that holds outside the quarantine. alias is
// always a constant identifier of this package's statements.
func notQuarantined(alias string) string {
	col := alias + ".path"
	return "NOT (" + col + " = " + quarantineSQL + " OR (" + col + " >= " + quarantineLo + " AND " + col + " < " +
		quarantineHi + "))"
}

// quarantinePath is index.IsQuarantinePath: whether an entry path of a
// source is its quarantine folder or lies below it.
func quarantinePath(path []byte) bool {
	n := len(quarantineName)
	return string(path) == quarantineName ||
		len(path) > n && path[n] == '/' && string(path[:n]) == quarantineName
}

// frozen refuses targets of which one lies in a quarantine (r4 D13): 409
// in_quarantine, before anything changes. A quarantined entry keeps the
// decisions, tags, and overrides it was quarantined with until it is
// restored, purged, or moved out.
func frozen(ctx context.Context, tx *sql.Tx, t targets) error {
	var (
		id   int64
		path []byte
	)
	err := tx.QueryRowContext(ctx, `SELECT e.id, e.path FROM entries e WHERE e.id IN (`+t.sub+`) AND NOT `+
		notQuarantined("e")+` ORDER BY e.id LIMIT 1`, t.args...).Scan(&id, &path)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("decisions: check quarantined targets: %w", err)
	}
	return domain.Errorf(domain.CodeInQuarantine, "entry %d (%s) is in the quarantine; restore it, or move it out, first",
		id, domain.DisplayName(path))
}

// markStale marks stale every pre-delete check that relies on an entry at
// or below a target that guard selects (r4 D10): a decision, a category,
// or a group mark set on a copy a check verified, or on a folder above it,
// makes the check stale at once. Nothing is read while no check is live.
func markStale(ctx context.Context, tx *sql.Tx, t targets, guard string) error {
	var live bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM purge_checks WHERE state IN ('running', 'ready'))`).
		Scan(&live); err != nil {
		return fmt.Errorf("decisions: read live checks: %w", err)
	}
	if !live {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT source_id, path FROM entries WHERE id IN (`+t.sub+`)`+guard+`
		ORDER BY source_id, path`, t.args...)
	if err != nil {
		return fmt.Errorf("decisions: read target paths: %w", err)
	}
	type target struct {
		src  domain.SourceID
		path []byte
	}
	var (
		list []target
		last *target
	)
	for rows.Next() {
		var tg target
		if err := rows.Scan(&tg.src, &tg.path); err != nil {
			rows.Close()
			return fmt.Errorf("decisions: read target paths: %w", err)
		}
		// A target below the previous one of its source is covered by it.
		if last != nil && last.src == tg.src && (len(last.path) == 0 || bytes.HasPrefix(tg.path, last.path) &&
			len(tg.path) > len(last.path) && tg.path[len(last.path)] == '/') {
			continue
		}
		list = append(list, tg)
		last = &list[len(list)-1]
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("decisions: read target paths: %w", err)
	}
	for _, tg := range list {
		if err := stale.MarkStale(ctx, tx, tg.src, tg.path); err != nil {
			return err
		}
	}
	return nil
}
