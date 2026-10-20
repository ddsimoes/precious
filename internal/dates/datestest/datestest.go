// Package datestest seeds media dates for tests, as a complete media job
// leaves them before its cameras pass (r5 design D3, D9). It does not
// import package dates, so dates' own tests use it too.
package datestest

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/media"
	"precious/internal/sources"
	"precious/internal/store"
)

// window is the number of entries Seed derives per write, as the dates
// pass does.
const window = 256

// Deriver is what Seed needs of the dates service (*dates.Service).
type Deriver interface {
	Rederive(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) error
}

// Seed reads every media file of src with media.Read, through src's root
// and the identity checks of content.Opener, and writes its media_meta row
// with the identity of its entries row: read, none for a format
// media.FormatOf does not read, or unreadable when the open is refused as
// unreadable. It then derives every media file's date with svc.Rederive,
// which keeps the source's summary. Rows of earlier reads are replaced. The
// cameras pass does not run: no camera_offset bit is set and media_cameras
// stays as it is. A file that changed since the scan, or a source that does
// not open, fails the test.
//
// Media files are dates.MediaCond's, by its Go twin: present regular files
// outside the quarantine whose file kind media.IsMediaKind accepts.
func Seed(t testing.TB, st *store.Store, srcs *sources.Service, svc Deriver, src domain.SourceID) {
	t.Helper()
	ctx := context.Background()
	type file struct {
		id   domain.EntryID
		row  content.Row
		ext  sql.NullString
		meta media.Meta
		st   media.MetaState
	}
	rows, err := st.Reader().QueryContext(ctx, `SELECT id, path, size, mtime_ns, ctime_ns, ino, ext, file_kind
		FROM entries WHERE source_id = ? AND kind = 'file' AND state = 'present' ORDER BY id`, string(src))
	if err != nil {
		t.Fatalf("datestest: list the media of %s: %v", src, err)
	}
	var files []file
	for rows.Next() {
		var (
			f    file
			kind sql.NullString
		)
		if err := rows.Scan(&f.id, &f.row.Path, &f.row.Size, &f.row.MtimeNs, &f.row.CtimeNs, &f.row.Ino, &f.ext,
			&kind); err != nil {
			rows.Close()
			t.Fatalf("datestest: list the media of %s: %v", src, err)
		}
		if media.IsMediaKind(kind.String) && !index.IsQuarantinePath(f.row.Path) {
			files = append(files, f)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("datestest: list the media of %s: %v", src, err)
	}

	opened, err := srcs.Open(ctx, src)
	if err != nil {
		t.Fatalf("datestest: open %s: %v", src, err)
	}
	opener := content.NewOpener(opened.Root, nil)
	for i := range files {
		f := &files[i]
		format := media.FormatOf(f.ext.String)
		if format == media.FormatNone {
			f.st = media.MetaNone
			continue
		}
		fh, _, err := opener.Open(f.row, opened.Source.Caps)
		if o, _ := fsaccess.OutcomeOf(err); err != nil && o == domain.OutcomeUnreadable {
			f.st = media.MetaUnreadable
			continue
		}
		if err != nil {
			t.Fatalf("datestest: open %s: %v", domain.DisplayName(f.row.Path), err)
		}
		f.meta, err = media.Read(fh, f.row.Size, format)
		fh.Close()
		if err != nil {
			t.Fatalf("datestest: read %s: %v", domain.DisplayName(f.row.Path), err)
		}
		f.st = media.MetaRead
	}
	opener.Close()
	opened.Root.Close()

	err = st.Write(ctx, func(tx *sql.Tx) error {
		for _, f := range files {
			var (
				capture, mk, model, serial any
				offset, gps, container     any
			)
			if f.st == media.MetaRead {
				m := f.meta
				capture, mk, model, serial = nullText(m.CaptureLocal), nullText(m.Make), nullText(m.Model), nullText(m.Serial)
				if m.CaptureOffsetMin != nil {
					offset = *m.CaptureOffsetMin
				}
				if m.GPS != nil {
					gps = m.GPS.UnixNano()
				}
				if m.Container != nil {
					container = m.Container.UnixNano()
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO media_meta (entry_id, source_id, state, size,
				mtime_ns, ctime_ns, ino, capture_local, capture_offset_min, gps_ns, container_ns, make, model, serial)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, int64(f.id), string(src), string(f.st), f.row.Size,
				f.row.MtimeNs, f.row.CtimeNs, f.row.Ino, capture, offset, gps, container, mk, model, serial); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("datestest: write the metadata of %s: %v", src, err)
	}
	ids := make([]domain.EntryID, len(files))
	for i, f := range files {
		ids[i] = f.id
	}
	for chunk := range slices.Chunk(ids, window) {
		if err := st.Write(ctx, func(tx *sql.Tx) error { return svc.Rederive(ctx, tx, chunk) }); err != nil {
			t.Fatalf("datestest: derive the dates of %s: %v", src, err)
		}
	}
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
