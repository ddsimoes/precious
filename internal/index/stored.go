package index

import (
	"context"
	"database/sql"
	"errors"

	"precious/internal/domain"
)

// stored is an entry's row as the previous scans left it, with its folder's
// dir_stats when it has them.
type stored struct {
	id       domain.EntryID
	name     []byte
	link     []byte
	row      row
	hasStats bool
	stats    statsCols
	// seen is set when a complete listing showed the entry again.
	seen bool
}

// storedColumns are what readStored scans, for the entries row e and its
// dir_stats d.
const storedColumns = `e.id, e.name, e.link_text, e.kind, e.special_kind, e.size, e.alloc, e.total_bytes,
	e.total_files, e.mtime_ns, e.ctime_ns, e.newest_ns, e.oldest_ns, e.dev, e.ino, e.nlink, e.mode, e.ext,
	e.file_kind, e.main_kind, e.category, e.family, e.traits, e.triage, e.is_group, e.veto, e.rule_ids,
	e.state, e.partial, e.mount_boundary, d.entry_id IS NOT NULL, d.dirs, d.files, d.symlinks, d.specials,
	d.unreadable, d.mount_boundaries, d.by_kind, d.by_year, d.by_family, d.signals, d.indicators, d.inside`

const (
	childrenQuery = `SELECT ` + storedColumns + ` FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id
		WHERE e.parent_id = ?`
	rootQuery = `SELECT ` + storedColumns + ` FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id
		WHERE e.source_id = ? AND e.path = X''`
)

type scanner interface{ Scan(dest ...any) error }

func scanStored(r scanner) (*stored, error) {
	var (
		s                                                     stored
		group, veto, partial, boundary                        int64
		dirs, files, symlinks, specials, unreadable, mount    opt
		byKind, byYear, byFamily, signals, indicators, inside text
	)
	r0 := &s.row
	err := r.Scan(&s.id, &s.name, &s.link, &r0.kind, &r0.special, &r0.size, &r0.alloc, &r0.totalBytes,
		&r0.totalFiles, &r0.mtime, &r0.ctime, &r0.newest, &r0.oldest, &r0.dev, &r0.ino, &r0.nlink, &r0.mode,
		&r0.ext, &r0.fileKind, &r0.mainKind, &r0.category, &r0.family, &r0.traits, &r0.triage, &group, &veto,
		&r0.ruleIDs, &r0.state, &partial, &boundary, &s.hasStats, &dirs, &files, &symlinks, &specials,
		&unreadable, &mount, &byKind, &byYear, &byFamily, &signals, &indicators, &inside)
	if err != nil {
		return nil, err
	}
	r0.group, r0.veto, r0.partial, r0.boundary = group != 0, veto != 0, partial != 0, boundary != 0
	s.stats = statsCols{
		dirs: dirs.v, files: files.v, symlinks: symlinks.v, specials: specials.v, unreadable: unreadable.v,
		mounts: mount.v, byKind: string(byKind), byYear: string(byYear), byFamily: string(byFamily),
		signals: string(signals), indicators: string(indicators), inside: string(inside),
	}
	return &s, nil
}

// readRoot returns the source's root row, or nil before its first scan when
// no row exists yet.
func readRoot(ctx context.Context, q *sql.DB, source domain.SourceID) (*stored, error) {
	s, err := scanStored(q.QueryRowContext(ctx, rootQuery, string(source)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// readChildren fills into with the stored children of the folder id, by raw
// name.
func readChildren(ctx context.Context, stmt *sql.Stmt, id domain.EntryID, into map[string]*stored) error {
	rows, err := stmt.QueryContext(ctx, int64(id))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		s, err := scanStored(rows)
		if err != nil {
			return err
		}
		into[string(s.name)] = s
	}
	return rows.Err()
}
