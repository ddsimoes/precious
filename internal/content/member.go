package content

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"precious/internal/archive"
	"precious/internal/domain"
	"precious/internal/store"
)

// Opened is an archive member open for reading (design D17). Content
// yields its bytes and closes the archive; Seeker, when not nil, reads the
// same bytes with ranges.
type Opened struct {
	Size    int64
	ModTime time.Time
	Content io.ReadCloser
	Seeker  io.ReadSeeker
}

// errFound ends a stream once the member was copied.
var errFound = errors.New("content: member found")

// OpenMember opens the file member ref of a complete archive, in memory
// only (design D17, ADR 0007): the archive file is opened through
// OpenArchive's identity checks; a stored zip member is a section of it,
// with ranges; a deflated zip member of at most archives.view_max_bytes is
// inflated into memory, with ranges; a larger one, and every member of a
// streamed archive, is streamed without ranges, the archive read up to the
// member under the budgets. An unknown member is not_found; a member that
// is not a file, an archive that is not complete or changed, or a listing
// the archive no longer matches is invalid_entry_state; an offline source is
// source_offline.
func (s *Service) OpenMember(ctx context.Context, q store.Queryer, ref domain.Ref) (Opened, error) {
	if !ref.IsMember() {
		return Opened{}, domain.Errorf(domain.CodeInvalidRequest, "%s is not an archive member", ref)
	}
	var (
		archiveID      int64
		kind           string
		path, target   []byte
		size           int64
		mtime, locator sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT m.archive_id, m.kind, m.path, coalesce(t.path, m.path), m.size, m.mtime_ns,
			coalesce(t.locator, m.locator)
		FROM archive_members m LEFT JOIN archive_members t ON t.id = m.link_member WHERE m.id = ?`, int64(ref.Member)).
		Scan(&archiveID, &kind, &path, &target, &size, &mtime, &locator)
	if errors.Is(err, sql.ErrNoRows) {
		return Opened{}, domain.Errorf(domain.CodeNotFound, "member %s not found", ref)
	}
	if err != nil {
		return Opened{}, fmt.Errorf("content: read member %s: %w", ref, err)
	}
	if kind != string(domain.MemberFile) {
		return Opened{}, domain.Errorf(domain.CodeInvalidEntryState, "member %s is a %s, not a file", ref, kind)
	}
	a, err := OpenArchive(ctx, q, s.src, domain.EntryID(archiveID))
	if err != nil {
		return Opened{}, err
	}
	o := Opened{Size: size, ModTime: a.Info.ModTime}
	if mtime.Valid {
		o.ModTime = time.Unix(0, mtime.Int64)
	}
	changed := func() error {
		return domain.Errorf(domain.CodeInvalidEntryState,
			"%s no longer matches its listing; a rescan updates it", domain.DisplayName(a.Row.Path))
	}
	if a.Format.Streamed() {
		o.Content = s.streamMember(ctx, a, target, changed)
		return o, nil
	}
	z, err := archive.OpenZip(a.File, a.Info.Size, s.limits())
	if err != nil {
		a.Close()
		var stop *archive.Stop
		if errors.As(err, &stop) {
			return Opened{}, changed()
		}
		return Opened{}, err
	}
	var m *archive.Member
	for i, x := range z.Members() {
		if locator.Valid && int64(x.Index) == locator.Int64 && bytes.Equal(joinPath(x.Path), target) &&
			x.Kind == domain.MemberFile && x.Size == size {
			m = &z.Members()[i]
			break
		}
	}
	if m == nil {
		a.Close()
		return Opened{}, changed()
	}
	if sec, ok := z.Section(m.Index); ok {
		o.Content, o.Seeker = &closer{Reader: sec, close: a.Close}, sec
		return o, nil
	}
	rc, err := z.Open(m.Index)
	if err != nil {
		a.Close()
		return Opened{}, err
	}
	if size > s.a.ViewMaxBytes {
		o.Content = &closer{Reader: rc, close: func() error { return errors.Join(rc.Close(), a.Close()) }}
		return o, nil
	}
	data, err := io.ReadAll(io.LimitReader(rc, size+1))
	rc.Close()
	a.Close()
	if err != nil {
		var stop *archive.Stop
		if errors.As(err, &stop) {
			return Opened{}, changed()
		}
		return Opened{}, err
	}
	if int64(len(data)) != size {
		return Opened{}, changed()
	}
	r := bytes.NewReader(data)
	o.Content, o.Seeker = io.NopCloser(r), r
	return o, nil
}

// streamMember streams the member at target of the streamed archive a: the
// archive is read in a goroutine up to the member, whose bytes go through a
// pipe. Closing the content stops the read and closes the archive.
func (s *Service) streamMember(ctx context.Context, a *Archive, target []byte, changed func() error) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := archive.Stream(ctx, a.Format, a.Name, io.NewSectionReader(a.File, 0, a.Info.Size), s.limits(),
			func(m archive.Member, data io.Reader) error {
				if m.Kind != domain.MemberFile || data == nil || !bytes.Equal(joinPath(m.Path), target) {
					return nil
				}
				if _, err := io.Copy(pw, data); err != nil {
					return err
				}
				return errFound
			})
		var stop *archive.Stop
		switch {
		case errors.Is(err, errFound):
			pw.Close()
		case err == nil, errors.As(err, &stop):
			pw.CloseWithError(changed())
		default:
			pw.CloseWithError(err)
		}
	}()
	return &closer{Reader: pr, close: func() error {
		cancel()
		pr.Close()
		<-done
		return a.Close()
	}}
}

// closer is a reader with its own Close.
type closer struct {
	io.Reader
	close func() error
}

func (c *closer) Close() error { return c.close() }
