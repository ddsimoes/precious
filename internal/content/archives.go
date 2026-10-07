package content

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"precious/internal/archive"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
)

// Archives (design D7). A zip is listed from its central directory alone;
// its file members are hashed later, by member size, when the size is
// shared. A streamed archive (tar, tar.gz, tar.bz2, gzip, bzip2) is read
// once from start to end: every file member is hashed in that pass, and the
// archive file's own digest is kept when every byte was read with an
// unchanged identity. A listing is written as archives.state 'listing' with
// the identity of the read, then its members in batches, each re-checking
// that row; the last transaction flips it to complete. A budget, a damaged
// or unsafe archive, or a file that changed or could not be read leaves the
// archive in that state with no members, and it is not read again until a
// rescan updates its entry.

// errAbandoned stops a listing whose archives row is gone: a rescan
// updated the entry.
var errAbandoned = errors.New("content: the archive's entry changed during its listing")

// limits are the budgets of one opening ([archives], design D7).
func (s *Service) limits() archive.Limits {
	return archive.Limits{MaxEntries: s.a.MaxMembers, MaxBytes: s.a.MaxUnpackedBytes, MaxRatio: s.a.MaxRatio,
		Deadline: s.clk.Now().Add(s.a.MaxTime.Duration), Now: s.clk.Now}
}

// addRows records rows of size bytes in total entering (n > 0) or leaving
// (n < 0) coverage with state.
func (d deltas) addRows(src string, state domain.ContentState, n, size int64) {
	b := bucket(state)
	if b == 0 || n == 0 {
		return
	}
	c := d[src]
	if c == nil {
		c = new([8]int64)
		d[src] = c
	}
	c[0] += n
	c[1] += size
	c[2*b] += n
	c[2*b+1] += size
}

// memberCoverage records the archive's file members leaving (sign -1) or
// entering (sign 1) coverage.
func memberCoverage(ctx context.Context, tx *sql.Tx, d deltas, src string, archiveID int64, sign int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT state, count(*), coalesce(sum(size), 0) FROM archive_members
		WHERE archive_id = ? AND kind = 'file' GROUP BY state`, archiveID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state domain.ContentState
		var n, size int64
		if err := rows.Scan(&state, &n, &size); err != nil {
			return err
		}
		d.addRows(src, state, sign*n, sign*size)
	}
	return rows.Err()
}

// detailJSON is archives.detail for an outcome other than complete.
func detailJSON(state domain.ArchiveState, detail string, member []byte) string {
	m := map[string]any{}
	if state == domain.ArchivePartial {
		m["budget"] = strings.TrimSuffix(detail, " budget")
	} else if detail != "" {
		m["error"] = detail
	}
	if member != nil {
		m["member_b64"] = member
	}
	b, _ := json.Marshal(m) // a map of strings and bytes always marshals
	return string(b)
}

// arcDir is one folder of an archive being listed.
type arcDir struct {
	id           int64 // 0 until written; the top level's stays 0
	parent       *arcDir
	children     map[string]*arcDir
	path         []byte
	bytes, files int64
	mtime        bool
}

// arcRef is a file member a later tar hard link may name.
type arcRef struct {
	id, content, size int64
	state             domain.ContentState
}

// memberRow is one queued write: a member row, or, with update set, the
// modification time of a folder written earlier.
type memberRow struct {
	kind    domain.MemberKind
	parent  *arcDir
	folder  *arcDir
	update  *arcDir
	name    []byte
	path    []byte
	size    int64
	mtime   int64 // 0: none
	link    []byte
	locator int64 // -1: none
	stored  bool
	digest  []byte // streamed file: its content
	target  string // tar hard link: its target's path
}

// listing is one archive being listed.
type listing struct {
	r        *run
	f        *fileRow
	format   domain.ArchiveFormat
	started  bool
	top      arcDir
	folders  []*arcDir
	queue    []memberRow
	files    map[string]*arcRef
	rows     int64
	unpacked int64
	sum      hash.Hash
	buf      []byte
}

// listArchive lists f, a supported archive, as its format reads (design
// D4, D7). It reports whether members were added, and whether the archive
// file's own digest was recorded.
func (r *run) listArchive(f *fileRow) (added, digested bool, err error) {
	l := &listing{r: r, f: f, format: f.format, files: map[string]*arcRef{}, sum: sha256.New(), buf: r.buf}
	if f.arcState == string(domain.ArchiveListing) {
		if err := l.drop(r.ctx); err != nil {
			return false, false, err
		}
	}
	fh, info, fail, err := r.openRow(f)
	if err != nil {
		return false, false, err
	}
	if fail != nil {
		return false, false, l.finish(r.ctx, failState(fail), detailJSON("", fail.detail, nil))
	}
	defer r.closeFile(fh)
	r.opened++
	if ok, err := l.start(); err != nil || !ok {
		return false, false, err
	}
	if r.abuf == nil {
		r.abuf = make([]byte, r.s.h.ReadChunkBytes)
	}
	cf := &chunkFile{r: r, fh: fh, size: f.size, buf: r.abuf}
	if f.format.Streamed() {
		cf.sum = sha256.New()
		err = archive.Stream(r.ctx, f.format, f.name, cf, r.s.limits(), l.visit)
		if err == nil {
			_, err = io.Copy(io.Discard, cf) // the rest, so that the digest covers the whole file
		}
	} else {
		var z *archive.Zip
		if z, err = archive.OpenZip(cf, f.size, r.s.limits()); err == nil {
			for _, m := range z.Members() {
				if err = l.visit(m, nil); err != nil {
					break
				}
			}
		}
	}
	r.listed++
	if fail, ferr := cf.stopErr(); ferr != nil || fail != nil {
		if ferr != nil {
			return false, false, l.abandon(ferr)
		}
		return false, false, l.finish(r.ctx, failState(fail), detailJSON("", fail.detail, nil))
	}
	var stop *archive.Stop
	switch {
	case errors.Is(err, errAbandoned):
		return false, false, nil
	case r.ctx.Err() != nil:
		return false, false, l.abandon(r.ctx.Err())
	case errors.As(err, &stop):
	case err != nil:
		return false, false, l.abandon(err)
	}
	// A file rewritten during the read may look damaged: the identity
	// decides first.
	if fail, err := r.restat(fh, info); err != nil || fail != nil {
		if err != nil {
			return false, false, l.abandon(err)
		}
		return false, false, l.finish(r.ctx, failState(fail), detailJSON("", fail.detail, nil))
	}
	if stop != nil {
		return false, false, l.finish(r.ctx, stop.State, detailJSON(stop.State, stop.Detail, stop.Member))
	}
	digest := cf.digest()
	if err := l.complete(digest); err != nil {
		if errors.Is(err, errAbandoned) {
			return false, false, nil
		}
		return false, false, l.abandon(err)
	}
	return l.rows > 0, digest != nil, nil
}

// failState is the archive state of a failed read of its file.
func failState(f *failure) domain.ArchiveState {
	if f.state == domain.ContentUnreadable {
		return domain.ArchiveUnreadable
	}
	return domain.ArchiveChanged
}

// entryLive is the I9 check of a listing write: the entry is present with
// the row's identity.
func (l *listing) entryLive(ctx context.Context, tx *sql.Tx) (bool, error) {
	f := l.f
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE id = ? AND state = 'present'
		AND size = ? AND mtime_ns IS ? AND ctime_ns IS ? AND ino IS ? AND dev IS ?)`,
		f.id, f.size, f.mtime, f.ctime, f.ino, f.dev).Scan(&ok)
	return ok, err
}

// live checks that the archives row is still this listing.
func (l *listing) live(ctx context.Context, tx *sql.Tx) error {
	f := l.f
	var ok bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM archives WHERE entry_id = ? AND state = 'listing'
		AND size = ? AND mtime_ns IS ? AND ctime_ns IS ? AND ino IS ?)`, f.id, f.size, f.mtime, f.ctime, f.ino).
		Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errAbandoned
	}
	return nil
}

// start writes the archives row in state listing with the identity of the
// read, when the entry is unchanged.
func (l *listing) start() (bool, error) {
	ctx, f := l.r.ctx, l.f
	err := l.r.s.st.Write(ctx, func(tx *sql.Tx) error {
		ok, err := l.entryLive(ctx, tx)
		if err != nil || !ok {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO archives (entry_id, format, state, size, mtime_ns, ctime_ns, ino)
			VALUES (?, ?, 'listing', ?, ?, ?, ?)`, f.id, string(l.format), f.size, f.mtime.Int64, f.ctime, f.ino); err != nil {
			return err
		}
		l.started = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("content: start listing %s: %w", domain.DisplayName(f.path), err)
	}
	return l.started, nil
}

// drop deletes an abandoned listing of the entry, members included.
func (l *listing) drop(ctx context.Context) error {
	return l.r.s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM archives WHERE entry_id = ? AND state = 'listing'`, l.f.id)
		return err
	})
}

// abandon ends a listing that cannot finish: its rows are deleted, and the
// next job lists the archive again.
func (l *listing) abandon(err error) error {
	if !l.started {
		return err
	}
	if derr := l.drop(context.WithoutCancel(l.r.ctx)); derr != nil {
		return errors.Join(err, derr)
	}
	return err
}

// finish records an outcome other than complete: no member is kept.
func (l *listing) finish(ctx context.Context, state domain.ArchiveState, detail string) error {
	f := l.f
	err := l.r.s.st.Write(ctx, func(tx *sql.Tx) error {
		ok, err := l.entryLive(ctx, tx)
		if err != nil || !ok {
			return err
		}
		if l.started {
			if err := l.live(ctx, tx); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM archive_members WHERE archive_id = ?`, f.id); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE archives SET state = ?, detail = ?, members = 0, unpacked_bytes = 0,
				listed_at = ? WHERE entry_id = ?`, string(state), detail, l.r.s.now(), f.id)
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO archives (entry_id, format, state, detail, size, mtime_ns, ctime_ns,
				ino, listed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (entry_id) DO UPDATE SET state = excluded.state, detail = excluded.detail,
				members = 0, unpacked_bytes = 0, listed_at = excluded.listed_at`,
			f.id, string(l.format), string(state), detail, f.size, f.mtime.Int64, f.ctime, f.ino, l.r.s.now())
		return err
	})
	if errors.Is(err, errAbandoned) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("content: record archive %s as %s: %w", domain.DisplayName(f.path), state, err)
	}
	l.r.changed = true
	return nil
}

// visit records one member: its row, after the folders its path implies.
// A streamed file member's data is hashed into its content; a zip member
// gets its central-directory index as locator.
func (l *listing) visit(m archive.Member, data io.Reader) error {
	if len(m.Path) == 0 {
		return fmt.Errorf("content: archive member of kind %s without a path", m.Kind)
	}
	parent := l.folder(m.Path[:len(m.Path)-1])
	name := m.Path[len(m.Path)-1]
	locator := int64(-1)
	if m.Index >= 0 {
		locator = int64(m.Index)
	}
	row := memberRow{kind: m.Kind, parent: parent, name: name, path: joinPath(m.Path), mtime: mtimeNs(m.Mtime),
		locator: locator, stored: m.Stored}
	switch m.Kind {
	case domain.MemberDirectory:
		if f := parent.children[string(name)]; f != nil {
			if row.mtime != 0 && !f.mtime {
				f.mtime = true
				l.queue = append(l.queue, memberRow{update: f, mtime: row.mtime})
			}
		} else {
			l.newFolder(parent, name, row.mtime, locator)
		}
		return l.maybeFlush()
	case domain.MemberFile:
		row.size = m.Size
		switch {
		case m.LinkTo != nil:
			row.target = string(joinPath(m.LinkTo))
			t := l.files[row.target]
			if t == nil {
				return fmt.Errorf("content: hard link %s names no earlier member", domain.DisplayName(row.path))
			}
			row.size = t.size
		case data != nil:
			l.sum.Reset()
			n, err := io.CopyBuffer(l.sum, data, l.buf)
			if err != nil {
				return err
			}
			row.size, row.digest = n, l.sum.Sum(nil)
		}
		l.unpacked += row.size
		for d := parent; d != nil; d = d.parent {
			d.bytes += row.size
			d.files++
		}
		l.files[string(row.path)] = &arcRef{size: row.size}
	case domain.MemberSymlink:
		row.link = m.LinkText
	}
	l.queue = append(l.queue, row)
	l.rows++
	return l.maybeFlush()
}

func joinPath(comps [][]byte) []byte {
	n := len(comps)
	for _, c := range comps {
		n += len(c)
	}
	out := make([]byte, 0, n)
	for i, c := range comps {
		if i > 0 {
			out = append(out, '/')
		}
		out = append(out, c...)
	}
	return out
}

func mtimeNs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// folder returns the folder at comps, creating the folders it implies.
func (l *listing) folder(comps [][]byte) *arcDir {
	f := &l.top
	for _, c := range comps {
		next := f.children[string(c)]
		if next == nil {
			next = l.newFolder(f, c, 0, -1)
		}
		f = next
	}
	return f
}

func (l *listing) newFolder(parent *arcDir, name []byte, mtime, locator int64) *arcDir {
	path := name
	if parent != &l.top {
		path = joinPath([][]byte{parent.path, name})
	}
	f := &arcDir{parent: parent, mtime: mtime != 0, path: path}
	if parent.children == nil {
		parent.children = map[string]*arcDir{}
	}
	parent.children[string(name)] = f
	l.folders = append(l.folders, f)
	l.queue = append(l.queue, memberRow{kind: domain.MemberDirectory, parent: parent, folder: f, name: name,
		path: path, mtime: mtime, locator: locator})
	l.rows++
	return f
}

func (l *listing) maybeFlush() error {
	if len(l.queue) < listBatch {
		return nil
	}
	return l.flush()
}

// flush writes the queued rows in one transaction that checks the listing
// is still live.
func (l *listing) flush() error {
	if len(l.queue) == 0 {
		return nil
	}
	ctx := l.r.ctx
	err := l.r.s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := l.live(ctx, tx); err != nil {
			return err
		}
		ins, err := tx.PrepareContext(ctx, `INSERT INTO archive_members (archive_id, parent_id, name, path, kind, size,
			mtime_ns, link_text, total_bytes, total_files, locator, stored, link_member, state, content_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`)
		if err != nil {
			return err
		}
		defer ins.Close()
		for i := range l.queue {
			if err := l.insert(ctx, tx, ins, &l.queue[i]); err != nil {
				return fmt.Errorf("content: write the members of %s: %w", domain.DisplayName(l.f.path), err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	clear(l.queue)
	l.queue = l.queue[:0]
	return nil
}

// insert writes one queued row. Rows are written in the order found, so a
// row's folder and a hard link's target are written before it.
func (l *listing) insert(ctx context.Context, tx *sql.Tx, ins *sql.Stmt, row *memberRow) error {
	if row.update != nil {
		_, err := tx.ExecContext(ctx, `UPDATE archive_members SET mtime_ns = ? WHERE id = ? AND mtime_ns IS NULL`,
			row.mtime, row.update.id)
		return err
	}
	var parent, mtime, link, locator, linkMember, state, content any
	if row.parent.id != 0 {
		parent = row.parent.id
	}
	if row.mtime != 0 {
		mtime = row.mtime
	}
	if row.link != nil {
		link = row.link
	}
	if row.locator >= 0 {
		locator = row.locator
	}
	var totalBytes, totalFiles int64
	ref := &arcRef{size: row.size, state: domain.ContentUniqueSize}
	if row.kind == domain.MemberFile {
		totalBytes, totalFiles = row.size, 1
		switch {
		case row.target != "":
			t := l.files[row.target]
			linkMember = t.id
			ref.content, ref.state = t.content, t.state
		case row.digest != nil && row.size > 0:
			id, err := contentID(ctx, tx, row.digest, row.size)
			if err != nil {
				return err
			}
			ref.content, ref.state = id, domain.ContentHashed
		case row.size > 0 && !l.format.Streamed():
			ref.state = domain.ContentPending
		}
		state = string(ref.state)
		if ref.content != 0 {
			content = ref.content
		}
	}
	var id int64
	if err := ins.QueryRowContext(ctx, l.f.id, parent, row.name, row.path, string(row.kind), row.size, mtime, link,
		totalBytes, totalFiles, locator, row.stored, linkMember, state, content).Scan(&id); err != nil {
		return err
	}
	switch {
	case row.folder != nil:
		row.folder.id = id
	case row.kind == domain.MemberFile:
		// A hard link stands for the file it names, so a chain of links
		// resolves to the first file member.
		ref.id = id
		if row.target != "" {
			ref.id = l.files[row.target].id
		}
		*l.files[string(row.path)] = *ref
	}
	return nil
}

// complete writes the folders' totals and flips the listing to complete,
// with the members' coverage and, for a streamed archive read whole, the
// archive file's own digest (design D6, D7).
func (l *listing) complete(digest []byte) error {
	ctx := l.r.ctx
	if err := l.flush(); err != nil {
		return err
	}
	for rest := l.folders; len(rest) > 0; {
		batch := rest[:min(len(rest), listBatch)]
		rest = rest[len(batch):]
		err := l.r.s.st.Write(ctx, func(tx *sql.Tx) error {
			if err := l.live(ctx, tx); err != nil {
				return err
			}
			for _, f := range batch {
				if _, err := tx.ExecContext(ctx, `UPDATE archive_members SET total_bytes = ?, total_files = ?
					WHERE id = ?`, f.bytes, f.files, f.id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	f := l.f
	src := string(l.r.source)
	return l.r.s.runner.Write(ctx, func(tx *jobs.Tx) error {
		sq := tx.SQL()
		if err := l.live(ctx, sq); err != nil {
			return err
		}
		ok, err := l.entryLive(ctx, sq)
		if err != nil {
			return err
		}
		if !ok {
			return errAbandoned
		}
		now := l.r.s.now()
		if _, err := sq.ExecContext(ctx, `UPDATE archives SET state = 'complete', detail = NULL, members = ?,
			unpacked_bytes = ?, listed_at = ? WHERE entry_id = ?`, l.rows, l.unpacked, now, f.id); err != nil {
			return err
		}
		d := deltas{}
		if err := memberCoverage(ctx, sq, d, src, f.id, 1); err != nil {
			return err
		}
		if digest != nil {
			var old domain.ContentState
			err := sq.QueryRowContext(ctx, `SELECT state FROM file_content WHERE entry_id = ?`, f.id).Scan(&old)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if old == domain.ContentPending || old == domain.ContentChanged {
				id, err := contentID(ctx, sq, digest, f.size)
				if err != nil {
					return err
				}
				if _, err := sq.ExecContext(ctx, `UPDATE file_content SET state = 'hashed', content_id = ?, mtime_ns = ?,
					ctime_ns = ?, ino = ?, checked_at = ? WHERE entry_id = ?`, id, f.mtime, f.ctime, f.ino, now, f.id); err != nil {
					return err
				}
				d.move(src, old, domain.ContentHashed, f.size)
			}
		}
		l.r.changed = true
		return d.apply(ctx, sq, now)
	})
}

// hashMembers hashes the pending file members of the complete zip f, in
// central-directory order, committing at most CommitFiles per transaction
// after an fstat shows the archive file unchanged (design D7). A member
// read that reaches a budget or finds damage leaves the archive in that
// state with no members.
func (r *run) hashMembers(f *fileRow) error {
	type member struct {
		id, locator, size int64
		digest            []byte
	}
	var members []member
	err := r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(r.ctx, `SELECT id, coalesce(locator, -1), size FROM archive_members
			WHERE archive_id = ? AND kind = 'file' AND state = 'pending' ORDER BY locator`, f.id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m member
			if err := rows.Scan(&m.id, &m.locator, &m.size); err != nil {
				return err
			}
			members = append(members, m)
		}
		return rows.Err()
	})
	if err != nil || len(members) == 0 {
		return err
	}
	fh, info, fail, err := r.openRow(f)
	if err != nil {
		return err
	}
	if fail != nil {
		if fail.state == domain.ContentUnreadable {
			return r.membersUnreadable(f, fail)
		}
		return nil // the archive changed: a rescan lists it again
	}
	defer r.closeFile(fh)
	r.opened++
	if r.abuf == nil {
		r.abuf = make([]byte, r.s.h.ReadChunkBytes)
	}
	cf := &chunkFile{r: r, fh: fh, size: f.size, buf: r.abuf}
	// stopped settles a failed read: the file's own failure first, then an
	// archive outcome once an fstat shows the file unchanged.
	stopped := func(err error) error {
		if fail, ferr := cf.stopErr(); ferr != nil || fail != nil {
			if ferr != nil {
				return ferr
			}
			if fail.state == domain.ContentUnreadable {
				return r.membersUnreadable(f, fail)
			}
			return nil
		}
		if cerr := r.ctx.Err(); cerr != nil {
			return cerr
		}
		var stop *archive.Stop
		if !errors.As(err, &stop) {
			return fmt.Errorf("content: read zip %s: %w", domain.DisplayName(f.path), err)
		}
		if fail, err := r.restat(fh, info); err != nil || fail != nil {
			return err
		}
		return r.archiveStop(f, stop)
	}
	z, err := archive.OpenZip(cf, f.size, r.s.limits())
	if err != nil {
		return stopped(err)
	}
	var batch []member
	commit := func() (bool, error) {
		if fail, err := r.restat(fh, info); err != nil || fail != nil {
			return false, err
		}
		live := false
		now := r.s.clk.Now()
		var refreshed bool
		err := r.s.runner.Write(r.ctx, func(tx *jobs.Tx) error {
			live, refreshed = false, false
			sq := tx.SQL()
			if err := sq.QueryRowContext(r.ctx, `SELECT EXISTS (SELECT 1 FROM archives a JOIN entries e ON e.id = a.entry_id
				WHERE a.entry_id = ? AND a.state = 'complete' AND e.state = 'present' AND e.size = ? AND e.mtime_ns IS ?
					AND e.ctime_ns IS ? AND e.ino IS ? AND e.dev IS ?)`, f.id, f.size, f.mtime, f.ctime, f.ino, f.dev).
				Scan(&live); err != nil || !live {
				return err
			}
			d := deltas{}
			for _, m := range batch {
				id, err := contentID(r.ctx, sq, m.digest, m.size)
				if err != nil {
					return err
				}
				res, err := sq.ExecContext(r.ctx, `UPDATE archive_members SET state = 'hashed', content_id = ?
					WHERE id = ? AND state = 'pending'`, id, m.id)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n > 0 {
					d.move(string(r.source), domain.ContentPending, domain.ContentHashed, m.size)
				}
			}
			if err := d.apply(r.ctx, sq, clock.Millis(now)); err != nil {
				return err
			}
			if now.Sub(r.lastRefresh) >= r.s.refresh {
				refreshed = true
				return r.s.requestRefreshTx(tx)
			}
			return nil
		})
		if err != nil {
			return false, fmt.Errorf("content: commit the members of %s: %w", domain.DisplayName(f.path), err)
		}
		if refreshed {
			r.lastRefresh = now
		}
		r.changed = r.changed || live
		batch = batch[:0]
		r.report()
		return live, r.rt.Yield(r.ctx)
	}
	for i := range members {
		m := &members[i]
		if m.locator < 0 {
			return fmt.Errorf("content: zip member %d has no locator", m.id)
		}
		if err := r.ctx.Err(); err != nil {
			return err
		}
		rc, err := z.Open(int(m.locator))
		if err != nil {
			return stopped(err)
		}
		r.sum.Reset()
		n, err := io.CopyBuffer(r.sum, readerFunc(func(p []byte) (int, error) {
			if err := r.ctx.Err(); err != nil {
				return 0, err
			}
			return rc.Read(p)
		}), r.buf)
		rc.Close()
		if err != nil {
			return stopped(err)
		}
		if n != m.size {
			return fmt.Errorf("content: zip member %d unpacked to %d bytes, not the %d listed", m.id, n, m.size)
		}
		m.digest = r.sum.Sum(nil)
		batch = append(batch, *m)
		if len(batch) == CommitFiles || i == len(members)-1 {
			live, err := commit()
			if err != nil || !live {
				return err
			}
		}
	}
	return nil
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// membersUnreadable makes the pending members of an archive whose file
// cannot be read unreadable.
func (r *run) membersUnreadable(f *fileRow, fail *failure) error {
	return r.s.runner.Write(r.ctx, func(tx *jobs.Tx) error {
		sq := tx.SQL()
		var n, size int64
		if err := sq.QueryRowContext(r.ctx, `SELECT count(*), coalesce(sum(size), 0) FROM archive_members
			WHERE archive_id = ? AND kind = 'file' AND state = 'pending'`, f.id).Scan(&n, &size); err != nil {
			return err
		}
		if _, err := sq.ExecContext(r.ctx, `UPDATE archive_members SET state = 'unreadable'
			WHERE archive_id = ? AND kind = 'file' AND state = 'pending'`, f.id); err != nil {
			return err
		}
		d := deltas{}
		d.addRows(string(r.source), domain.ContentPending, -n, -size)
		d.addRows(string(r.source), fail.state, n, size)
		r.changed = true
		return d.apply(r.ctx, sq, r.s.now())
	})
}

// archiveStop turns a complete zip whose member read stopped into that
// outcome, with no members (design D7).
func (r *run) archiveStop(f *fileRow, stop *archive.Stop) error {
	err := r.s.runner.Write(r.ctx, func(tx *jobs.Tx) error {
		sq := tx.SQL()
		var live bool
		if err := sq.QueryRowContext(r.ctx, `SELECT EXISTS (SELECT 1 FROM archives WHERE entry_id = ? AND state = 'complete')`,
			f.id).Scan(&live); err != nil || !live {
			return err
		}
		d := deltas{}
		if err := memberCoverage(r.ctx, sq, d, string(r.source), f.id, -1); err != nil {
			return err
		}
		if _, err := sq.ExecContext(r.ctx, `DELETE FROM archive_members WHERE archive_id = ?`, f.id); err != nil {
			return err
		}
		if _, err := sq.ExecContext(r.ctx, `UPDATE archives SET state = ?, detail = ?, members = 0, unpacked_bytes = 0
			WHERE entry_id = ?`, string(stop.State), detailJSON(stop.State, stop.Detail, stop.Member), f.id); err != nil {
			return err
		}
		r.changed = true
		return d.apply(r.ctx, sq, r.s.now())
	})
	if err != nil {
		return fmt.Errorf("content: record zip %s as %s: %w", domain.DisplayName(f.path), stop.State, err)
	}
	return nil
}
