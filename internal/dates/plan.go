package dates

import (
	"context"
	"database/sql"
	"fmt"

	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/media"
)

// planWindow is the span of entry IDs pass 1 enrols per write.
const planWindow = 50_000

// planWindowSQL reads the media of one window of entry IDs without a
// media_meta row. entries is read NOT INDEXED, by its rowid range, for the
// reason deriveWindowSQL is (Addendum G1).
var planWindowSQL = `SELECT e.id, e.ext, e.size, e.mtime_ns, e.ctime_ns, e.ino FROM entries e NOT INDEXED
				WHERE e.id > ? AND e.id <= ? AND e.source_id = ? AND ` + MediaCond("e") + `
					AND NOT EXISTS (SELECT 1 FROM media_meta m WHERE m.entry_id = e.id)`

// plan is pass 1 (D3, D4): a media_meta row for every media file of src
// (MediaCond) without one, with the identity of its entries row: pending
// for a format media.FormatOf reads, none for any other. Entry IDs are
// taken in windows of planWindow, one write each; the job yields after a
// window that enrolled anything.
func (s *Service) plan(ctx context.Context, rt jobs.Runtime, src domain.SourceID) error {
	var top int64
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM entries`).Scan(&top); err != nil {
		return fmt.Errorf("dates: plan the media of %q: %w", src, err)
	}
	for from := int64(0); from < top; from += planWindow {
		var n int
		err := s.st.Write(ctx, func(tx *sql.Tx) error {
			n = 0
			rows, err := tx.QueryContext(ctx, planWindowSQL, from, from+planWindow, string(src))
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
