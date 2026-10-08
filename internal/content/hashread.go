package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"hash"
	"io"

	"precious/internal/archive"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/store"
)

// Hashing on demand (R4 design D9): the pre-delete check and the
// executor's verifications read a file, a complete archive, or one member
// in full through the identity-checked openers (OpenAt, OpenArchive), in
// chunks of at most read_chunk_bytes with ctx checked before each, and
// fstat the open file once read. A file or archive that is not what the
// index records, or that changed during the read, is invalid_entry_state.
// They write nothing: the database is only read through q.

// HashEntry reads in full the file r describes below root, opened through
// OpenAt, and returns its SHA-256 and the lstat that OpenAt matched against
// r. The reads are chunks of at most the default read_chunk_bytes, with ctx
// checked before each; the end of the file is confirmed by a read there.
// A path that is gone, replaced, changed, unreadable, shorter or longer
// than its size, or whose open file's fstat differs from that lstat once
// read is invalid_entry_state; a cancel returns ctx's error.
func HashEntry(ctx context.Context, root fsaccess.Dir, r Row, caps fsaccess.Capabilities) (sum [32]byte,
	info fsaccess.EntryInfo, err error) {
	if err := ctx.Err(); err != nil {
		return sum, fsaccess.EntryInfo{}, err
	}
	f, info, err := OpenAt(root, r, caps)
	if err != nil {
		return sum, fsaccess.EntryInfo{}, err
	}
	defer f.Close()
	fr := newFileReader(ctx, f, info.Size, config.Defaults().Hashing.ReadChunkBytes)
	h := sha256.New()
	if _, err := fr.WriteTo(h); err != nil {
		return sum, fsaccess.EntryInfo{}, entryStateError(err, r.Path)
	}
	if err := restat(f, info); err != nil {
		return sum, fsaccess.EntryInfo{}, entryStateError(err, r.Path)
	}
	h.Sum(sum[:0])
	return sum, info, nil
}

// HashArchive opens or streams a complete archive once and calls fn for
// every file member.
//
// The archive entry id is opened through OpenArchive, so a missing entry is
// not_found, an archive that is not complete (partial, corrupt, unsupported,
// never listed) or that changed since its listing is invalid_entry_state,
// and an offline source is source_offline. A zip's central directory and
// its file members are read from that one open file; a streamed archive
// (tar, tar.gz, tar.bz2, gzip, bzip2) is read once from start to end. Each
// file member is matched by path to its archive_members row: a zip member
// also by its central-directory index, a tar hard link by the member it
// names, which gives its content. Folders, symlinks, and special members are
// skipped. A file member the listing lacks, a listed file member the read
// does not find, a size other than listed, or an archive outcome other than
// complete is invalid_entry_state, as for OpenMember.
//
// fn is called with each member's ID, SHA-256, and size, in the order read
// (a zip's central-directory order, a stream's member order), only once the
// whole archive was read and an fstat showed the file unchanged; its error
// stops the calls and is returned.
func (s *Service) HashArchive(ctx context.Context, q store.Queryer, id domain.EntryID,
	fn func(member int64, sum [32]byte, size int64) error) error {
	a, err := OpenArchive(ctx, q, s.src, id)
	if err != nil {
		return err
	}
	defer a.Close()
	rows, err := archiveFiles(ctx, q, id)
	if err != nil {
		return err
	}
	fr := s.archiveReader(ctx, a)
	var sums []memberSum
	if a.Format.Streamed() {
		sums, err = s.hashStream(ctx, a, fr, rows)
	} else {
		sums, err = s.hashZip(ctx, a, fr, rows)
	}
	if err != nil {
		return archiveReadError(ctx, a, fr, err)
	}
	if err := restat(a.File, a.Info); err != nil {
		return entryStateError(err, a.Row.Path)
	}
	for _, m := range sums {
		if err := fn(m.id, m.sum, m.size); err != nil {
			return err
		}
	}
	return nil
}

// HashMember reads in full the file member ref of a complete archive and
// returns its SHA-256 and size. It finds the member as OpenMember does,
// opens the archive through OpenArchive, and reads it as HashArchive does:
// a zip member from the archive file, CRC-checked; a streamed archive up to
// the member. The errors are OpenMember's; besides, an archive whose open
// file's fstat differs once read, or a member of another size than listed,
// is invalid_entry_state.
func (s *Service) HashMember(ctx context.Context, q store.Queryer, ref domain.Ref) (sum [32]byte, size int64,
	err error) {
	m, err := lookupMember(ctx, q, ref)
	if err != nil {
		return sum, 0, err
	}
	a, err := OpenArchive(ctx, q, s.src, m.archive)
	if err != nil {
		return sum, 0, err
	}
	defer a.Close()
	fr := s.archiveReader(ctx, a)
	h, buf := sha256.New(), make([]byte, s.h.ReadChunkBytes)
	n := int64(-1)
	if a.Format.Streamed() {
		err = archive.Stream(ctx, a.Format, a.Name, fr, s.limits(), func(x archive.Member, data io.Reader) error {
			if x.Kind != domain.MemberFile || data == nil || !bytes.Equal(joinPath(x.Path), m.target) {
				return nil
			}
			k, err := hashData(ctx, h, data, buf)
			if err != nil {
				return err
			}
			n = k
			return errFound
		})
		if errors.Is(err, errFound) {
			err = nil
		}
	} else {
		n, err = hashZipMember(ctx, a, fr, s.limits(), m, h, buf)
	}
	if err != nil {
		return sum, 0, archiveReadError(ctx, a, fr, err)
	}
	if n != m.size {
		return sum, 0, listingChanged(a.Row.Path)
	}
	if err := restat(a.File, a.Info); err != nil {
		return sum, 0, entryStateError(err, a.Row.Path)
	}
	h.Sum(sum[:0])
	return sum, n, nil
}

// hashZipMember hashes the member m of the zip a, found as OpenMember finds
// it: its central-directory index, path, kind, and size as listed. It
// returns -1 when the zip holds no such member.
func hashZipMember(ctx context.Context, a *Archive, fr *fileReader, lim archive.Limits, m listedMember,
	h hash.Hash, buf []byte) (int64, error) {
	z, err := archive.OpenZip(fr, a.Info.Size, lim)
	if err != nil {
		return 0, err
	}
	for _, x := range z.Members() {
		if m.locator.Valid && int64(x.Index) == m.locator.Int64 && bytes.Equal(joinPath(x.Path), m.target) &&
			x.Kind == domain.MemberFile && x.Size == m.size {
			rc, err := z.Open(x.Index)
			if err != nil {
				return 0, err
			}
			defer rc.Close()
			return hashData(ctx, h, rc, buf)
		}
	}
	return -1, nil
}

// memberSum is one file member's digest, for HashArchive's fn.
type memberSum struct {
	id   int64
	sum  [32]byte
	size int64
}

// fileMember is a file member's archive_members row: target is the path of
// the member holding its bytes (a hard link's target), locator its zip
// central-directory index.
type fileMember struct {
	id      int64
	target  []byte
	size    int64
	locator sql.NullInt64
}

// archiveFiles returns the file members of the archive id, by path.
func archiveFiles(ctx context.Context, q store.Queryer, id domain.EntryID) (map[string]fileMember, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.id, m.path, coalesce(t.path, m.path), m.size,
			coalesce(t.locator, m.locator)
		FROM archive_members m LEFT JOIN archive_members t ON t.id = m.link_member
		WHERE m.archive_id = ? AND m.kind = 'file'`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("content: read the members of archive %s: %w", id, err)
	}
	defer rows.Close()
	out := map[string]fileMember{}
	for rows.Next() {
		var (
			m    fileMember
			path []byte
		)
		if err := rows.Scan(&m.id, &path, &m.target, &m.size, &m.locator); err != nil {
			return nil, fmt.Errorf("content: read the members of archive %s: %w", id, err)
		}
		out[string(path)] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: read the members of archive %s: %w", id, err)
	}
	return out, nil
}

// hashStream reads the streamed archive a once and hashes every file
// member of rows, which it empties. A hard link takes the digest of the
// member it names, which its row must name too.
func (s *Service) hashStream(ctx context.Context, a *Archive, fr *fileReader, rows map[string]fileMember) (
	[]memberSum, error) {
	type read struct {
		sum    [32]byte
		size   int64
		origin []byte // the member holding the bytes
	}
	done := make(map[string]read, len(rows))
	out := make([]memberSum, 0, len(rows))
	h, buf := sha256.New(), make([]byte, s.h.ReadChunkBytes)
	err := archive.Stream(ctx, a.Format, a.Name, fr, s.limits(), func(m archive.Member, data io.Reader) error {
		if m.Kind != domain.MemberFile {
			return nil
		}
		path := joinPath(m.Path)
		row, ok := rows[string(path)]
		if !ok {
			return listingChanged(a.Row.Path)
		}
		delete(rows, string(path))
		var x read
		switch {
		case m.LinkTo != nil:
			if x, ok = done[string(joinPath(m.LinkTo))]; !ok {
				return listingChanged(a.Row.Path)
			}
		case data != nil:
			n, err := hashData(ctx, h, data, buf)
			if err != nil {
				return err
			}
			h.Sum(x.sum[:0])
			x.size, x.origin = n, path
		default:
			return fmt.Errorf("content: file member %s of %s has no data", domain.DisplayName(path),
				domain.DisplayName(a.Row.Path))
		}
		if x.size != row.size || !bytes.Equal(x.origin, row.target) {
			return listingChanged(a.Row.Path)
		}
		done[string(path)] = x
		out = append(out, memberSum{id: row.id, sum: x.sum, size: x.size})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(rows) != 0 {
		return nil, listingChanged(a.Row.Path)
	}
	return out, nil
}

// hashZip reads the zip a's central directory and every file member of
// rows, which it empties, from the one open file.
func (s *Service) hashZip(ctx context.Context, a *Archive, fr *fileReader, rows map[string]fileMember) (
	[]memberSum, error) {
	z, err := archive.OpenZip(fr, a.Info.Size, s.limits())
	if err != nil {
		return nil, err
	}
	out := make([]memberSum, 0, len(rows))
	h, buf := sha256.New(), make([]byte, s.h.ReadChunkBytes)
	for _, m := range z.Members() {
		if m.Kind != domain.MemberFile {
			continue
		}
		path := joinPath(m.Path)
		row, ok := rows[string(path)]
		if !ok || !row.locator.Valid || row.locator.Int64 != int64(m.Index) || row.size != m.Size ||
			!bytes.Equal(row.target, path) {
			return nil, listingChanged(a.Row.Path)
		}
		delete(rows, string(path))
		rc, err := z.Open(m.Index)
		if err != nil {
			return nil, err
		}
		n, err := hashData(ctx, h, rc, buf)
		rc.Close()
		if err != nil {
			return nil, err
		}
		if n != row.size {
			return nil, listingChanged(a.Row.Path)
		}
		ms := memberSum{id: row.id, size: n}
		h.Sum(ms.sum[:0])
		out = append(out, ms)
	}
	if len(rows) != 0 {
		return nil, listingChanged(a.Row.Path)
	}
	return out, nil
}

// hashData resets h and hashes data through buf, with ctx checked before
// each read, returning the bytes read.
func hashData(ctx context.Context, h hash.Hash, data io.Reader, buf []byte) (int64, error) {
	h.Reset()
	return io.CopyBuffer(h, readerFunc(func(p []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return data.Read(p)
	}), buf)
}

// archiveReadError maps the error that ended a read of the archive a: the archive
// file's own failure first (invalid_entry_state where the file changed or
// cannot be read), then ctx's end, then an archive outcome, which for a
// complete listing means the archive changed (as in OpenMember).
func archiveReadError(ctx context.Context, a *Archive, fr *fileReader, err error) error {
	if fr.err != nil {
		if cerr := ctx.Err(); cerr != nil && errors.Is(fr.err, cerr) {
			return cerr
		}
		return entryStateError(fr.err, a.Row.Path)
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	var stop *archive.Stop
	if errors.As(err, &stop) {
		return listingChanged(a.Row.Path)
	}
	return err
}

// restat checks that the open file f is still the one lstat described as
// info; a difference is errMismatch.
func restat(f fsaccess.File, info fsaccess.EntryInfo) error {
	got, err := f.Stat()
	if err != nil {
		return err
	}
	if !sameFile(got, info) {
		return errMismatch
	}
	return nil
}

// archiveReader reads the open file of a in chunks of at most
// read_chunk_bytes.
func (s *Service) archiveReader(ctx context.Context, a *Archive) *fileReader {
	return newFileReader(ctx, a.File, a.Info.Size, s.h.ReadChunkBytes)
}

// fileReader reads an open file of a known size for the hashes above, as
// chunkFile reads one for a hashing job, without a job's watchdog and
// yields: every read of the file is one call of at most chunk bytes, with
// ctx checked before it, into a read-ahead window of one chunk filled from
// the first offset not buffered. A call whose window reaches the size asks
// one byte more, so the end of the file is confirmed in the same call. A
// read that fails sticks in err; one that returns fewer bytes than the
// size holds, or finds more, sticks as errMismatch; a cancel sticks as
// ctx's error.
type fileReader struct {
	ctx  context.Context
	fh   fsaccess.File
	size int64
	buf  []byte

	off int64 // the file offset of buf[0]
	n   int64 // the bytes of buf holding file content
	pos int64 // Read's next offset
	eof bool  // a read at the size returned io.EOF
	err error
}

// newFileReader returns a reader of fh with a window of chunk bytes, or of
// size+1 for a smaller file.
func newFileReader(ctx context.Context, fh fsaccess.File, size, chunk int64) *fileReader {
	return &fileReader{ctx: ctx, fh: fh, size: size, buf: make([]byte, max(1, min(chunk, size+1)))}
}

// fill reads the chunk at start into the window, in one call.
func (f *fileReader) fill(start int64) error {
	if f.err != nil {
		return f.err
	}
	f.n = 0
	if err := f.ctx.Err(); err != nil {
		f.err = err
		return err
	}
	chunk := int64(len(f.buf))
	want := max(0, min(chunk, f.size-start))
	ask := want
	if want < chunk {
		ask = want + 1 // the end of the file, checked in the same call
	}
	k, err := f.fh.ReadAt(f.buf[:ask], start)
	switch {
	case err != nil && err != io.EOF:
		f.err = err
		return err
	case int64(k) != want, ask > want && err != io.EOF:
		f.err = errMismatch // shorter or longer than its size
		return f.err
	}
	f.off, f.n = start, want
	f.eof = f.eof || ask > want || (err == io.EOF && start+want == f.size)
	return nil
}

func (f *fileReader) holds(off int64) bool { return off >= f.off && off < f.off+f.n }

// ReadAt reads the file through the window.
func (f *fileReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("content: read a file at negative offset %d", off)
	}
	n := 0
	for n < len(p) {
		if f.err != nil {
			return n, f.err
		}
		o := off + int64(n)
		if o >= f.size {
			return n, io.EOF
		}
		if !f.holds(o) {
			if err := f.fill(o); err != nil {
				return n, err
			}
		}
		n += copy(p[n:], f.buf[o-f.off:f.n])
	}
	return n, nil
}

// Read reads the file sequentially from its start, and confirms its end
// before it returns io.EOF.
func (f *fileReader) Read(p []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := f.next(); err != nil {
		return 0, err
	}
	k := copy(p, f.buf[f.pos-f.off:f.n])
	f.pos += int64(k)
	return k, nil
}

// WriteTo writes the rest of the file to w straight from the window, and
// confirms its end; io.Copy uses it.
func (f *fileReader) WriteTo(w io.Writer) (int64, error) {
	var total int64
	for {
		if f.err != nil {
			return total, f.err
		}
		if err := f.next(); err == io.EOF {
			return total, nil
		} else if err != nil {
			return total, err
		}
		k, err := w.Write(f.buf[f.pos-f.off : f.n])
		f.pos += int64(k)
		total += int64(k)
		if err != nil {
			return total, err
		}
	}
}

// next makes the window hold pos, or returns io.EOF at the size once the
// end is confirmed.
func (f *fileReader) next() error {
	if f.holds(f.pos) {
		return nil
	}
	if f.pos >= f.size {
		if !f.eof {
			if err := f.fill(f.size); err != nil {
				return err
			}
		}
		return io.EOF
	}
	return f.fill(f.pos)
}
