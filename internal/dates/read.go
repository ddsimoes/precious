package dates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"precious/internal/clock"
	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/media"
)

// readCommit is the number of read results pass 2 commits per write.
const readCommit = 64

// openedSource is a source's root, open for the read pass, with the folder
// chain its files are opened through.
type openedSource struct {
	root   fsaccess.Dir
	caps   fsaccess.Capabilities
	opener *content.Opener
}

// openSource opens src's root for the read pass; nil, without an error,
// when the source is offline or unavailable, whose job runs passes 3 and 4
// only (D4).
func (s *Service) openSource(ctx context.Context, rt jobs.Runtime, src domain.SourceID) (*openedSource, error) {
	done := rt.FSCall("OpenRoot")
	opened, err := s.src.Open(ctx, src)
	done()
	if domain.CodeOf(err) == domain.CodeSourceOffline {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &openedSource{root: opened.Root, caps: opened.Source.Caps, opener: content.NewOpener(opened.Root, rt)}, nil
}

func (o *openedSource) close(rt jobs.Runtime) {
	o.opener.Close()
	done := rt.FSCall("Close")
	_ = o.root.Close()
	done()
}

// errSourceGone ends the read pass when the source's root stops answering;
// the job goes on with passes 3 and 4.
var errSourceGone = errors.New("dates: the source became unavailable during the read pass")

// pendingFile is a pending media_meta row with the identity of its entries
// row, loaded when its read starts (D3).
type pendingFile struct {
	id  int64
	ext string
	row content.Row
	dev sql.NullInt64
}

// readResult is the outcome of reading one file: state is read, none, or
// unreadable; empty when the file changed (nothing is written).
type readResult struct {
	f     pendingFile
	state media.MetaState
	meta  media.Meta
}

// readPass is pass 2 (D3, D4): each pending row of a media file of the
// source (MediaCond, so never in the quarantine) is opened through the
// folder chain when the disk matches its entries row under content.Matches,
// read with media.Read, restatted, and its result committed, readCommit per
// write, only while its entries row is still the identity loaded at the
// read's start and its row still pending. It yields between files. A file
// found changed stays pending.
func (s *Service) readPass(ctx context.Context, job jobs.Job, rt jobs.Runtime, o *openedSource, p *progress) error {
	src := job.SourceID
	var total int64
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM media_meta m JOIN entries e ON e.id = m.entry_id
		WHERE m.source_id = ? AND m.state = 'pending' AND `+MediaCond("e"), string(src)).Scan(&total); err != nil {
		return fmt.Errorf("dates: count the media to read of %q: %w", src, err)
	}
	p.set(progOfFiles, total)
	p.publish()
	var after int64
	for {
		batch, err := s.pending(ctx, src, after)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		results := make([]readResult, 0, len(batch))
		var stop error
		for i := range batch {
			after = batch[i].id
			res, err := s.readOne(ctx, rt, o, &batch[i], p)
			if err != nil {
				stop = err
				break
			}
			results = append(results, res)
			p.add(progFiles, 1)
			p.publish()
			if err := rt.Yield(ctx); err != nil {
				stop = err
				break
			}
		}
		if stop != nil {
			// The results finished before are kept, also after a cancel.
			if err := s.commitReads(context.WithoutCancel(ctx), src, results); err != nil {
				return errors.Join(stop, err)
			}
			if errors.Is(stop, errSourceGone) {
				s.log.Warn("dates: the source became unavailable during the read pass", "source", src)
				return nil
			}
			return stop
		}
		if err := s.stage(ctx, job, stageReadCommit); err != nil {
			return err
		}
		if err := s.commitReads(ctx, src, results); err != nil {
			return err
		}
	}
}

// pending loads the next pending media files of src after the entry ID
// after, with their entries identity, in entry ID order.
func (s *Service) pending(ctx context.Context, src domain.SourceID, after int64) ([]pendingFile, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT m.entry_id, coalesce(e.ext, ''), e.path, e.size, e.mtime_ns,
		e.ctime_ns, e.ino, e.dev FROM media_meta m JOIN entries e ON e.id = m.entry_id
		WHERE m.source_id = ? AND m.state = 'pending' AND m.entry_id > ? AND `+MediaCond("e")+`
		ORDER BY m.entry_id LIMIT ?`, string(src), after, readCommit)
	if err != nil {
		return nil, fmt.Errorf("dates: list the media to read of %q: %w", src, err)
	}
	defer rows.Close()
	var out []pendingFile
	for rows.Next() {
		var f pendingFile
		if err := rows.Scan(&f.id, &f.ext, &f.row.Path, &f.row.Size, &f.row.MtimeNs, &f.row.CtimeNs, &f.row.Ino,
			&f.dev); err != nil {
			return nil, fmt.Errorf("dates: list the media to read of %q: %w", src, err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dates: list the media to read of %q: %w", src, err)
	}
	return out, nil
}

// readOne opens, reads, and restats one file. Its error is ctx's,
// errSourceGone, or a failure of the store.
func (s *Service) readOne(ctx context.Context, rt jobs.Runtime, o *openedSource, f *pendingFile, p *progress) (readResult, error) {
	res := readResult{f: *f}
	format := media.FormatOf(f.ext)
	if format == media.FormatNone {
		res.state = media.MetaNone
		return res, nil
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	fh, info, err := o.opener.Open(f.row, o.caps)
	if err != nil {
		res.state, err = s.classify(rt, o, err, p)
		return res, err
	}
	defer func() {
		done := rt.FSCall("Close")
		_ = fh.Close()
		done()
	}()
	r := &watchedFile{ctx: ctx, rt: rt, f: fh, p: p}
	meta, err := media.Read(r, f.row.Size, format)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return res, cerr
		}
		res.state, err = s.classify(rt, o, err, p)
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	done := rt.FSCall("FileStat")
	got, err := fh.Stat()
	done()
	if err != nil {
		res.state, err = s.classify(rt, o, err, p)
		return res, err
	}
	if !sameFile(got, info) {
		p.add(progChanged, 1)
		return res, nil
	}
	res.state, res.meta = media.MetaRead, meta
	return res, nil
}

// classify turns a failed open, read, or fstat into a result state
// (Addendum C1): unreadable for an unreadable outcome; changed ("") for a
// file gone, replaced, or no longer matching its row; any other failure is
// checked against the source root, which ends the pass (errSourceGone)
// when it no longer answers, and is otherwise unreadable.
func (s *Service) classify(rt jobs.Runtime, o *openedSource, err error, p *progress) (media.MetaState, error) {
	o2, ok := fsaccess.OutcomeOf(err)
	switch {
	case ok && o2 == domain.OutcomeUnreadable:
		p.add(progUnreadable, 1)
		return media.MetaUnreadable, nil
	case ok && (o2 == domain.OutcomeAbsent || o2 == domain.OutcomeChangedDuringObservation),
		(!ok || o2 == "") && domain.CodeOf(err) == domain.CodeInvalidEntryState:
		p.add(progChanged, 1)
		return "", nil
	}
	done := rt.FSCall("FSInfo")
	_, rerr := o.root.FSInfo()
	done()
	if rerr != nil {
		return "", errSourceGone
	}
	s.log.Warn("dates: a media file could not be read", "err", err)
	p.add(progUnreadable, 1)
	return media.MetaUnreadable, nil
}

// sameFile reports whether an open file's fstat still shows the file its
// lstat described: same kind, device, inode, size, and times.
func sameFile(got, want fsaccess.EntryInfo) bool {
	return got.Kind == domain.EntryFile && got.Dev == want.Dev && got.Ino == want.Ino && got.Size == want.Size &&
		got.ModTime.Equal(want.ModTime) && got.Ctime.Equal(want.Ctime)
}

// watchedFile is an open file as media.Read sees it: each ReadAt is a
// watched call, checks ctx first, and counts its bytes.
type watchedFile struct {
	ctx context.Context
	rt  jobs.Runtime
	f   fsaccess.File
	p   *progress
}

func (w *watchedFile) ReadAt(b []byte, off int64) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	done := w.rt.FSCall("ReadAt")
	n, err := w.f.ReadAt(b, off)
	done()
	w.p.add(progBytes, int64(n))
	return n, err
}

// commitReads writes results in one transaction, each only while its entry
// is present with exactly the identity loaded at its read's start (path,
// size, times, inode, device) and its media_meta row is still pending; the
// row takes that identity (D3, I9). A changed file writes nothing.
func (s *Service) commitReads(ctx context.Context, src domain.SourceID, results []readResult) error {
	if len(results) == 0 {
		return nil
	}
	now := clock.Millis(s.clk.Now())
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		up, err := tx.PrepareContext(ctx, `UPDATE media_meta SET state = ?, size = ?, mtime_ns = ?, ctime_ns = ?,
			ino = ?, capture_local = ?, capture_offset_min = ?, gps_ns = ?, container_ns = ?, make = ?, model = ?,
			serial = ?, read_at = ?
			WHERE entry_id = ? AND state = 'pending' AND EXISTS (SELECT 1 FROM entries e WHERE e.id = ?
				AND e.state = 'present' AND e.path = ? AND e.size = ? AND e.mtime_ns IS ? AND e.ctime_ns IS ?
				AND e.ino IS ? AND e.dev IS ?)`)
		if err != nil {
			return err
		}
		defer up.Close()
		for _, r := range results {
			if r.state == "" {
				continue
			}
			var (
				capture, mk, model, serial any
				offset, gps, container     any
			)
			if r.state == media.MetaRead {
				m := r.meta
				capture, mk, model, serial = nullText(m.CaptureLocal), nullText(m.Make), nullText(m.Model),
					nullText(m.Serial)
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
			f := r.f
			if _, err := up.ExecContext(ctx, string(r.state), f.row.Size, f.row.MtimeNs, f.row.CtimeNs, f.row.Ino,
				capture, offset, gps, container, mk, model, serial, now,
				f.id, f.id, f.row.Path, f.row.Size, f.row.MtimeNs, f.row.CtimeNs, f.row.Ino, f.dev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("dates: record the media read of %q: %w", src, err)
	}
	return nil
}

// nullText is s, or NULL when empty.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
