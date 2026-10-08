package dates

import (
	"context"
	"database/sql"
	"fmt"

	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/media"
)

// idSpan is the span of entry IDs one window of pass 1 or pass 3 reads.
const idSpan = 50_000

// sourceIDsSQL reads the IDs around src's entries, through an index that
// leads with source_id: the ID before its first one and its last one.
const sourceIDsSQL = `SELECT coalesce(min(id), 1) - 1, coalesce(max(id), 0) FROM entries WHERE source_id = ?`

// sourceIDs returns the ID before src's first entry and its last entry's
// ID, read once per job: pass 1's and pass 3's windows walk the rowid range
// between them only, so a job never reads the other sources' entries past
// either end (Addendum K1). Entries a scan adds later get the next job.
func (s *Service) sourceIDs(ctx context.Context, src domain.SourceID) (after, last int64, err error) {
	if err := s.st.Reader().QueryRowContext(ctx, sourceIDsSQL, string(src)).Scan(&after, &last); err != nil {
		return 0, 0, fmt.Errorf("dates: read the entry IDs of %q: %w", src, err)
	}
	return after, last, nil
}

// planWindowSQL reads the media of one span of entry IDs without a
// media_meta row. entries is read NOT INDEXED, by its rowid range, for the
// reason deriveWindowSQL is (Addendum G1).
var planWindowSQL = `SELECT e.id, e.ext, e.size, e.mtime_ns, e.ctime_ns, e.ino FROM entries e NOT INDEXED
				WHERE e.id > ? AND e.id <= ? AND e.source_id = ? AND ` + MediaCond("e") + `
					AND NOT EXISTS (SELECT 1 FROM media_meta m WHERE m.entry_id = e.id)`

// plan is pass 1 (D3, D4): a media_meta row for every media file of src
// (MediaCond) without one, with the identity of its entries row: pending
// for a format media.FormatOf reads, none for any other. Entry IDs are
// taken in spans of idSpan between src's first and last entry, one write
// each; the job yields after a window that enrolled anything.
func (s *Service) plan(ctx context.Context, rt jobs.Runtime, src domain.SourceID) error {
	first, last, err := s.sourceIDs(ctx, src)
	if err != nil {
		return err
	}
	for from := first; from < last; from += idSpan {
		var n int
		err := s.st.Write(ctx, func(tx *sql.Tx) error {
			n = 0
			rows, err := tx.QueryContext(ctx, planWindowSQL, from, min(from+idSpan, last), string(src))
			if err != nil {
				return err
			}
			type enrol struct {
				id                int64
				state             media.MetaState
				size              int64
				mtime, ctime, ino sql.NullInt64
			}
			var add []enrol
			for rows.Next() {
				var (
					r   enrol
					ext sql.NullString
				)
				if err := rows.Scan(&r.id, &ext, &r.size, &r.mtime, &r.ctime, &r.ino); err != nil {
					rows.Close()
					return err
				}
				r.state = media.MetaPending
				if media.FormatOf(ext.String) == media.FormatNone {
					r.state = media.MetaNone
				}
				add = append(add, r)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(add) == 0 {
				return nil
			}
			ins, err := tx.PrepareContext(ctx, `INSERT INTO media_meta (entry_id, source_id, state, size, mtime_ns,
				ctime_ns, ino) VALUES (?, ?, ?, ?, ?, ?, ?)`)
			if err != nil {
				return err
			}
			defer ins.Close()
			for _, r := range add {
				if _, err := ins.ExecContext(ctx, r.id, string(src), string(r.state), r.size, r.mtime, r.ctime,
					r.ino); err != nil {
					return err
				}
			}
			n = len(add)
			return nil
		})
		if err != nil {
			return fmt.Errorf("dates: plan the media of %q: %w", src, err)
		}
		if n > 0 {
			if err := rt.Yield(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
