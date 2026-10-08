package index

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"slices"

	"precious/internal/domain"
)

// QuarantineName is the folder at the top of each source that holds its
// quarantined entries (r4 design D1). Scans walk it, so its rows follow the
// disk, but no fold above it counts it, and every reader that shows or
// counts the disk leaves its path out (D2, ADR 0011).
const QuarantineName = ".precious-quarantine"

// The bounds of the quarantine's paths as SQL BLOB literals, computed once:
// the folder's own path, and the half-open range [Q/, Q0) of the paths below
// it. A Q || '/' in SQL would be TEXT, which compares above every BLOB path.
var (
	quarantineHex = fmt.Sprintf("%x", QuarantineName)
	quarantineSQL = "X'" + quarantineHex + "'"
	quarantineLo  = "X'" + quarantineHex + "2f'"
	quarantineHi  = "X'" + quarantineHex + "30'"
)

// InQuarantine returns a condition, for the entries row aliased alias, that
// holds for the quarantine folder of its source and every entry below it.
// It is a residual: SQLite reads it on the rows it fetches, so it never
// changes an index plan. alias must be a plain SQL identifier.
func InQuarantine(alias string) string {
	col := column(alias)
	return "(" + col + " = " + quarantineSQL + " OR (" + col + " >= " + quarantineLo + " AND " + col + " < " +
		quarantineHi + "))"
}

// NotQuarantined returns the negation of InQuarantine(alias): the condition
// every reader that shows or counts the disk adds on the entries row it
// already fetches (r4 design D2).
func NotQuarantined(alias string) string { return "NOT " + InQuarantine(alias) }

// column returns alias.path, refusing an alias that is not a plain
// identifier: the conditions are spliced into SQL text.
func column(alias string) string {
	if alias == "" || !isIdent(alias) {
		panic(fmt.Sprintf("index: %q is not an SQL alias", alias))
	}
	return alias + ".path"
}

func isIdent(s string) bool {
	for i, c := range []byte(s) {
		switch {
		case c == '_', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case i > 0 && '0' <= c && c <= '9':
		default:
			return false
		}
	}
	return true
}

// IsQuarantinePath reports whether the entry path of a source is its
// quarantine folder or lies below it.
func IsQuarantinePath(path []byte) bool {
	return isQuarantineName(path) ||
		len(path) > len(QuarantineName) && path[len(QuarantineName)] == '/' && isQuarantineName(path[:len(QuarantineName)])
}

// isQuarantineName reports whether a name of the top folder is the
// quarantine's.
func isQuarantineName(name []byte) bool { return string(name) == QuarantineName }

// DeleteSubtree deletes the entry id, everything below it, and their
// entry_names rows, in tx: the index side of a purge that removed the whole
// item (r4 design D11). The rows that reference them go by their foreign
// keys. An entry no longer indexed is nothing to delete; a source's top
// folder is refused. The caller refolds the folders above it.
func DeleteSubtree(ctx context.Context, tx *sql.Tx, id domain.EntryID) error {
	p, err := placeByID(ctx, tx, id)
	switch {
	case err != nil:
		return err
	case p == nil:
		return nil
	case !p.parent.Valid:
		return fmt.Errorf("index: the root of source %q cannot be deleted", p.source)
	}
	return deleteTree(ctx, tx, p)
}

// DeleteEntries deletes the entries ids, with whatever is still indexed
// below each, and their entry_names rows, in tx: the index side of a purge
// step that stopped partway, which removed those entries only (r4 design
// D11). IDs no longer indexed, or listed twice, are skipped. A source's top
// folder is refused, and nothing changes then. The caller refolds the
// folders above them.
func DeleteEntries(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) error {
	var places []*place
	for _, id := range ids {
		p, err := placeByID(ctx, tx, id)
		switch {
		case err != nil:
			return err
		case p == nil:
			continue
		case !p.parent.Valid:
			return fmt.Errorf("index: the root of source %q cannot be deleted", p.source)
		}
		places = append(places, p)
	}
	// Shallowest first: deleting a folder takes the entries below it, which
	// are then no longer indexed and are skipped.
	slices.SortFunc(places, func(a, b *place) int { return bytes.Compare(a.path, b.path) })
	for _, p := range places {
		var indexed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE id = ?)`,
			int64(p.id)).Scan(&indexed); err != nil {
			return fmt.Errorf("index: read entry %d: %w", p.id, err)
		}
		if !indexed {
			continue
		}
		if err := deleteTree(ctx, tx, p); err != nil {
			return err
		}
	}
	return nil
}
