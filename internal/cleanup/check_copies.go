package cleanup

import (
	"context"
	"database/sql"

	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
)

// The copy search of a check (r4 design D7 step 2). A copy counts only
// once it was read in full during the check, identity-checked, on an
// online source, and found to hold the digest. The candidates, in order,
// each outside the quarantine (and so outside the set, which lies in it):
//
//  1. present files whose recorded content has the digest;
//  2. file members of complete archives whose recorded content has it;
//  3. present files of the same size with no digest (not hashed yet,
//     unique by size, sampled, changed, or unreadable when last read), on
//     every online source.
//
// The search stops at the first match. A candidate of the first two kinds
// on a source that is not online makes the file copy_offline when nothing
// else matches; a same-size file there proves nothing. A candidate that
// fails its read (changed, gone, unreadable) is passed over. Each digest is
// searched once per check, and each candidate read once.

// Candidates, each by an index: contents by its unique sha256, then
// file_content by content and archive_members by content; the same-size
// files by file_content(source_id, state, size). The quarantine test is a
// residual on the entries row already fetched.
var (
	// copyFilesSQL: sha256.
	copyFilesSQL = `SELECT e.id, e.source_id, e.path, e.size, e.mtime_ns, e.ctime_ns, e.ino
		FROM contents c JOIN file_content f ON f.content_id = c.id JOIN entries e ON e.id = f.entry_id
		WHERE c.sha256 = ? AND e.kind = 'file' AND e.state = 'present' AND ` + index.NotQuarantined("e") + `
		ORDER BY e.id`
	// copyMembersSQL: sha256.
	copyMembersSQL = `SELECT m.id, e.id, e.source_id, e.path, e.size, e.mtime_ns, e.ctime_ns, e.ino
		FROM contents c JOIN archive_members m ON m.content_id = c.id JOIN archives a ON a.entry_id = m.archive_id
		JOIN entries e ON e.id = a.entry_id
		WHERE c.sha256 = ? AND m.kind = 'file' AND a.state = 'complete' AND e.kind = 'file' AND e.state = 'present'
		AND ` + index.NotQuarantined("e") + ` ORDER BY m.id`
	// sameSizeSQL: source, size, size.
	sameSizeSQL = `SELECT e.id, e.source_id, e.path, e.size, e.mtime_ns, e.ctime_ns, e.ino
		FROM file_content f JOIN entries e ON e.id = f.entry_id
		WHERE f.source_id = ? AND f.state IN ('unique_size', 'pending', 'sampled', 'changed', 'unreadable')
		AND f.size = ? AND e.size = ? AND e.kind = 'file' AND e.state = 'present' AND ` + index.NotQuarantined("e") + `
		ORDER BY e.id`
)

// copyResult is what the search found for one digest: a verified copy, or
// none, and then whether a candidate was on a source that is not online.
type copyResult struct {
	copy    *found
	offline bool
}

// candidate is a file, or a member of the archive file it names, that may
// hold a digest.
type candidate struct {
	member int64 // 0 for a file
	entry  int64
	source domain.SourceID
	path   []byte
	size   int64
	mtime  sql.NullInt64
	ctime  sql.NullInt64
	ino    sql.NullInt64
}

func (k candidate) key() candidateKey { return candidateKey{entry: k.entry, member: k.member} }

func (k candidate) row() content.Row {
	return content.Row{Path: k.path, Size: k.size, MtimeNs: k.mtime, CtimeNs: k.ctime, Ino: k.ino}
}

type candidateKey struct{ entry, member int64 }

// candidateSum is a candidate read once: its digest and the lstat of its
// file, or ok false when the read failed.
type candidateSum struct {
	ok   bool
	sum  [32]byte
	info fsaccess.EntryInfo
}

// findCopy returns the copy search's result for a digest of size bytes.
func (c *checker) findCopy(ctx context.Context, sum [32]byte, size int64) (copyResult, error) {
	if r, ok := c.copies[sum]; ok {
		return r, nil
	}
	r, err := c.searchCopy(ctx, sum, size)
	if err != nil {
		return copyResult{}, err
	}
	c.copies[sum] = r
	return r, nil
}

func (c *checker) searchCopy(ctx context.Context, sum [32]byte, size int64) (copyResult, error) {
	var res copyResult
	for _, q := range []string{copyFilesSQL, copyMembersSQL} {
		cands, err := c.candidates(ctx, q, q == copyMembersSQL, sum[:])
		if err != nil {
			return res, err
		}
		for _, k := range cands {
			f, offline, err := c.verify(ctx, k, sum)
			if err != nil {
				return res, err
			}
			if f != nil {
				return copyResult{copy: f}, nil
			}
			res.offline = res.offline || offline
		}
	}
	ids, err := c.sources(ctx)
	if err != nil {
		return res, err
	}
	for _, src := range ids {
		o, err := c.source(ctx, src)
		if err != nil {
			return res, err
		}
		if o == nil {
			continue
		}
		cands, err := c.candidates(ctx, sameSizeSQL, false, string(src), size, size)
		if err != nil {
			return res, err
		}
		for _, k := range cands {
			f, _, err := c.verify(ctx, k, sum)
			if err != nil {
				return res, err
			}
			if f != nil {
				return copyResult{copy: f}, nil
			}
		}
	}
	return res, nil
}

// candidates runs a candidate query; members tells its column shape.
func (c *checker) candidates(ctx context.Context, query string, members bool, args ...any) ([]candidate, error) {
	rows, err := c.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var k candidate
		var src string
		dest := []any{&k.entry, &src, &k.path, &k.size, &k.mtime, &k.ctime, &k.ino}
		if members {
			dest = append([]any{&k.member}, dest...)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		k.source = domain.SourceID(src)
		out = append(out, k)
	}
	return out, rows.Err()
}

// verify reads the candidate k in full, unless an earlier search did, and
// returns it as a copy when it holds sum. offline reports a candidate whose
// source is not online. Only a cancel or a database failure is an error.
func (c *checker) verify(ctx context.Context, k candidate, sum [32]byte) (f *found, offline bool, err error) {
	got, ok := c.sums[k.key()]
	if !ok {
		o, err := c.source(ctx, k.source)
		if err != nil {
			return nil, false, err
		}
		if o == nil {
			return nil, true, nil
		}
		got, err = c.read(ctx, o, k)
		if err != nil {
			return nil, false, err
		}
		c.sums[k.key()] = got
	}
	if !got.ok || got.sum != sum {
		return nil, false, nil
	}
	return &found{source: k.source, path: k.path, entry: k.entry, member: k.member, info: got.info}, false, nil
}

// read reads the candidate k of the online source o. A file is read through
// HashEntry; a member through HashMember, after an lstat of its archive
// file that must match the index. A failed read is a candidate passed
// over.
func (c *checker) read(ctx context.Context, o *openSource, k candidate) (candidateSum, error) {
	var (
		got candidateSum
		err error
	)
	if k.member == 0 {
		got.sum, got.info, err = content.HashEntry(ctx, o.root, k.row(), o.caps)
	} else {
		got.info, err = o.w.lstat(k.path)
		if err == nil && !content.Matches(k.row(), got.info, o.caps) {
			err = errMismatch
		}
		if err == nil {
			got.sum, _, err = c.s.content.HashMember(ctx, c.q, domain.Ref{Entry: domain.EntryID(k.entry),
				Member: domain.MemberID(k.member)})
		}
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return candidateSum{}, cerr
		}
		return candidateSum{}, nil
	}
	got.ok = true
	return got, nil
}

// sources lists every source, once per check.
func (c *checker) sources(ctx context.Context) ([]domain.SourceID, error) {
	if c.sourceIDs != nil {
		return c.sourceIDs, nil
	}
	rows, err := c.q.QueryContext(ctx, `SELECT id FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []domain.SourceID{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, domain.SourceID(id))
	}
	c.sourceIDs = ids
	return ids, rows.Err()
}

// source opens src for reading copies, once per check; nil when it is not
// online (or cannot be opened, so nothing on it is verified). The check's
// own source is the one opened at the start.
func (c *checker) source(ctx context.Context, src domain.SourceID) (*openSource, error) {
	if src == c.src {
		return c.own, nil
	}
	if o, ok := c.opened[src]; ok {
		return o, nil
	}
	opened, err := c.s.src.Open(ctx, src)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		c.opened[src] = nil
		return nil, nil
	}
	o := &openSource{root: opened.Root, caps: opened.Source.Caps, w: &walker{root: opened.Root}}
	c.opened[src] = o
	return o, nil
}
