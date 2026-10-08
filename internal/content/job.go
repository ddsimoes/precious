package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"slices"
	"strings"
	"time"

	"precious/internal/archive"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
)

// Progress keys of the hashing jobs (design Interfaces).
const (
	ProgressPhase          = "phase"
	ProgressCandidateFiles = "candidate_files"
	ProgressCandidateBytes = "candidate_bytes"
	ProgressCheckedFiles   = "checked_files"
	ProgressCheckedBytes   = "checked_bytes"
	ProgressReadBytes      = "read_bytes"
	ProgressArchivesListed = "archives_listed"
	ProgressUnreadable     = "unreadable"
)

// The phases a job reports (design D4).
const (
	PhaseListing = 1
	PhaseLarge   = 2
	PhaseSmall   = 3
)

// nowPayload is the payload of a hash_now job: the folders and archive
// files to check first, by entry ID. check-now adds to it while the job is
// active, and the job reads it again until it holds nothing new.
type nowPayload struct {
	Entries []domain.EntryID `json:"entries"`
}

// handler runs hash (now false) or hash_now (now true) jobs.
type handler struct {
	s   *Service
	now bool
}

var _ jobs.Handler = (*handler)(nil)

// fileRow is a file as the index records it, with its content row.
type fileRow struct {
	id                            int64
	path, name                    []byte
	size                          int64
	mtime, ctime, dev, ino, nlink sql.NullInt64
	state                         domain.ContentState
	sample                        []byte
	arcState                      string // archives.state, "" when unlisted (archive candidates only)
	format                        domain.ArchiveFormat
	key                           int64 // the size a queue orders it by
}

// fileCols select a fileRow from entries e LEFT JOIN file_content f.
const fileCols = `e.id, e.path, e.name, e.size, e.mtime_ns, e.ctime_ns, e.dev, e.ino, e.nlink,
	coalesce(f.state, ''), f.sample`

func scanFile(rows *sql.Rows, extra ...any) (fileRow, error) {
	var f fileRow
	err := rows.Scan(append([]any{&f.id, &f.path, &f.name, &f.size, &f.mtime, &f.ctime, &f.dev, &f.ino, &f.nlink,
		&f.state, &f.sample}, extra...)...)
	return f, err
}

func (f *fileRow) row() Row {
	return Row{Path: f.path, Size: f.size, MtimeNs: f.mtime, CtimeNs: f.ctime, Ino: f.ino}
}

// inode is f's physical file when other names of it may exist.
func (f *fileRow) inode(caps fsaccess.Capabilities) ([2]int64, bool) {
	if !caps.StableIdentity || f.nlink.Int64 < 2 || !f.dev.Valid || !f.ino.Valid {
		return [2]int64{}, false
	}
	return [2]int64{f.dev.Int64, f.ino.Int64}, true
}

// sameStat reports whether two rows of one inode were recorded alike.
func sameStat(a, b *fileRow) bool {
	return a.size == b.size && a.mtime == b.mtime && a.ctime == b.ctime
}

// span is a half-open range [from, to) of entries.path; to nil is
// unbounded, an empty from is no lower bound.
type span struct{ from, to []byte }

func (s span) contains(p []byte) bool {
	return bytes.Compare(p, s.from) >= 0 && (s.to == nil || bytes.Compare(p, s.to) < 0)
}

// spanWhere is the SQL condition on e.path of being in one of spans; nil
// spans is no condition.
func spanWhere(spans []span) (string, []any) {
	if spans == nil {
		return "", nil
	}
	if len(spans) == 0 {
		return " AND 0", nil
	}
	var b strings.Builder
	var args []any
	b.WriteString(" AND (")
	for i, s := range spans {
		if i > 0 {
			b.WriteString(" OR ")
		}
		var conds []string
		if len(s.from) > 0 {
			conds, args = append(conds, "e.path >= ?"), append(args, s.from)
		}
		if s.to != nil {
			conds, args = append(conds, "e.path < ?"), append(args, s.to)
		}
		if len(conds) == 0 {
			conds = []string{"1"}
		}
		b.WriteString("(" + strings.Join(conds, " AND ") + ")")
	}
	b.WriteString(")")
	return b.String(), args
}

// unit is one file result waiting for its commit.
type unit struct {
	f   fileRow
	res result
}

// linked is the result of an inode with several names, for its other names.
type linked struct {
	f   fileRow
	res result
}

// run is one attempt of a hashing job over its source.
type run struct {
	s      *Service
	ctx    context.Context
	rt     jobs.Runtime
	job    jobs.Job
	source domain.SourceID
	root   fsaccess.Dir
	caps   fsaccess.Capabilities
	chain  *chain

	buf, abuf  []byte
	sum        hash.Hash
	sinceYield int64

	units  []unit
	inodes map[[2]int64]linked
	// skip holds the entries this attempt has dealt with in a pass that
	// another pass must not read again.
	skip map[int64]bool
	// items holds the streamed archives this attempt has listed.
	items map[int64]bool

	readBytes, opened, listed int64
	changed                   bool
	// added: a streamed listing added members since the last plan.
	added       bool
	lastRefresh time.Time
	phase       int64
	cov         Coverage
}

// Run hashes the job's source (design D4, D5).
func (h *handler) Run(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	s := h.s
	r := &run{s: s, ctx: ctx, rt: rt, job: job, source: job.SourceID, buf: make([]byte, s.h.ReadChunkBytes),
		sum: sha256.New(), inodes: map[[2]int64]linked{}, skip: map[int64]bool{}, items: map[int64]bool{},
		lastRefresh: s.clk.Now(),
		phase:       PhaseListing}
	changed, err := s.plan(ctx, rt, job.SourceID)
	if err != nil {
		return err
	}
	r.changed = changed
	done := rt.FSCall("OpenRoot")
	opened, err := s.src.Open(ctx, job.SourceID)
	done()
	if err != nil {
		return err
	}
	r.root, r.caps = opened.Root, opened.Source.Caps
	r.chain = &chain{root: opened.Root, calls: rt}
	defer func() {
		r.chain.close()
		done := rt.FSCall("Close")
		_ = opened.Root.Close()
		done()
	}()
	r.report()
	if h.now {
		err = r.checkNow()
	} else {
		err = r.pass(nil, true)
	}
	if err != nil {
		return r.stop(err)
	}
	return r.finish()
}

// pass hashes what is not checked yet in spans (nil: the whole source), in
// the order of design D4. withCandidates reads the small files of candidate
// folder pairs before the other small files.
func (r *run) pass(spans []span, withCandidates bool) error {
	r.phase = PhaseListing
	r.report()
	streamed, members, err := r.listZips(spans)
	if err != nil {
		return err
	}
	if members {
		changed, err := r.s.plan(r.ctx, r.rt, r.source)
		if err != nil {
			return err
		}
		r.changed = r.changed || changed
	}
	r.phase = PhaseLarge
	r.report()
	if err := r.samplePass(spans); err != nil {
		return err
	}
	large, small, err := r.zipItems(spans)
	if err != nil {
		return err
	}
	if err := r.largePass(spans, append(streamed, large...)); err != nil {
		return err
	}
	if err := r.commit(); err != nil {
		return err
	}
	if r.added {
		// Streamed archives added members: their sizes join the groups, so
		// the files, zip members, and archive files just listed of those
		// sizes are read too.
		r.added = false
		changed, err := r.s.plan(r.ctx, r.rt, r.source)
		if err != nil {
			return err
		}
		r.changed = r.changed || changed
		for i := range streamed {
			delete(r.skip, streamed[i].id)
		}
		if err := r.samplePass(spans); err != nil {
			return err
		}
		if large, small, err = r.zipItems(spans); err != nil {
			return err
		}
		if err := r.largePass(spans, large); err != nil {
			return err
		}
		if err := r.commit(); err != nil {
			return err
		}
	}
	r.phase = PhaseSmall
	r.report()
	if withCandidates {
		cands, err := r.candidateSpans(spans)
		if err != nil {
			return err
		}
		for _, sp := range cands {
			var inside []fileRow
			for _, z := range small {
				if sp.contains(z.path) {
					inside = append(inside, z)
				}
			}
			if err := r.smallPass([]span{sp}, inside, true); err != nil {
				return err
			}
		}
		if err := r.commit(); err != nil {
			return err
		}
	}
	if err := r.smallPass(spans, small, false); err != nil {
		return err
	}
	return r.commit()
}

// checkNow hashes the job's folders, reading its payload again until it
// names nothing new (check-now coalesces into an active job).
func (r *run) checkNow() error {
	seen := map[domain.EntryID]bool{}
	for {
		var raw string
		if err := r.s.st.Reader().QueryRowContext(r.ctx, `SELECT payload FROM jobs WHERE id = ?`, int64(r.job.ID)).
			Scan(&raw); err != nil {
			return fmt.Errorf("content: read the payload of job %s: %w", r.job.ID, err)
		}
		var p nowPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return fmt.Errorf("content: payload of job %s: %w", r.job.ID, err)
		}
		var fresh []domain.EntryID
		for _, id := range p.Entries {
			if !seen[id] {
				seen[id] = true
				fresh = append(fresh, id)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		spans, err := r.spansOf(fresh)
		if err != nil {
			return err
		}
		if len(spans) == 0 {
			continue
		}
		if err := r.pass(spans, false); err != nil {
			return err
		}
	}
}

// spansOf returns the path ranges of the present folders and files of the
// job's source among ids: a folder's descendants, a file itself.
func (r *run) spansOf(ids []domain.EntryID) ([]span, error) {
	var out []span
	for _, id := range ids {
		var path []byte
		var kind string
		err := r.s.st.Reader().QueryRowContext(r.ctx, `SELECT path, kind FROM entries
			WHERE id = ? AND source_id = ? AND state <> 'missing'`, int64(id), string(r.source)).Scan(&path, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		switch {
		case kind == string(domain.EntryDirectory) && len(path) == 0:
			out = append(out, span{})
		case kind == string(domain.EntryDirectory):
			out = append(out, span{from: append(slices.Clip(path), '/'), to: append(slices.Clip(path), '0')})
		default:
			out = append(out, span{from: path, to: append(slices.Clip(path), 0)})
		}
	}
	return out, nil
}

// candidateSpans returns this source's sides of the provisional candidate
// folder pairs (design D4 step 4), in the order given.
func (r *run) candidateSpans(scope []span) ([]span, error) {
	if r.s.candidates == nil {
		return nil, nil
	}
	var out []span
	err := r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
		pairs, err := r.s.candidates(r.ctx, tx, r.source)
		if err != nil {
			return err
		}
		for _, p := range pairs {
			for _, side := range p {
				if side.Source != r.source {
					continue
				}
				sp := span{from: side.From, to: side.To}
				if scope == nil || slices.ContainsFunc(scope, func(s span) bool { return s.contains(sp.from) }) {
					out = append(out, sp)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("content: candidate folders of %s: %w", r.source, err)
	}
	return out, nil
}

// archiveExts are the entries.ext values of names Classify may accept.
const archiveExts = `('zip', 'tar', 'gz', 'tgz', 'bz2', 'tbz', 'tbz2', 'old', 'bak', 'orig')`

// listZips lists the unlisted zip archives in spans (design D4 step 1) and
// returns the unlisted streamed archives, for the large queue, and
// whether a listing added members.
func (r *run) listZips(spans []span) (streamed []fileRow, members bool, err error) {
	where, args := spanWhere(spans)
	var zips []fileRow
	err = r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(r.ctx, `SELECT `+fileCols+`, coalesce(a.state, '') FROM entries e
				LEFT JOIN archives a ON a.entry_id = e.id LEFT JOIN file_content f ON f.entry_id = e.id
			WHERE e.source_id = ? AND e.kind = 'file' AND e.state = 'present' AND e.size > 0
				AND e.ext IN `+archiveExts+` AND (a.entry_id IS NULL OR a.state = 'listing')`+where+`
			ORDER BY e.path`, append([]any{string(r.source)}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var arc string
			f, err := scanFile(rows, &arc)
			if err != nil {
				return err
			}
			f.arcState = arc
			format, ok := archive.Classify(f.name)
			if !ok {
				continue
			}
			f.format, f.key = format, f.size
			if format == domain.ArchiveZip {
				zips = append(zips, f)
			} else {
				streamed = append(streamed, f)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("content: find the unlisted archives of %s: %w", r.source, err)
	}
	for i := range zips {
		added, _, err := r.listArchive(&zips[i])
		if err != nil {
			return nil, false, err
		}
		members = members || added
		if err := r.rt.Yield(r.ctx); err != nil {
			return nil, false, err
		}
	}
	for i := range streamed {
		r.skip[streamed[i].id] = true
	}
	return streamed, members, nil
}

// zipItems returns the complete zip archives in spans with members to
// hash, keyed by their largest pending member: those of a large key, and
// the others.
func (r *run) zipItems(spans []span) (large, small []fileRow, err error) {
	where, args := spanWhere(spans)
	err = r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(r.ctx, `SELECT `+fileCols+`, max(m.size) FROM archive_members m
				JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = a.entry_id
				LEFT JOIN file_content f ON f.entry_id = e.id
			WHERE e.source_id = ? AND e.state = 'present' AND a.state = 'complete' AND a.format = 'zip'
				AND m.kind = 'file' AND m.state = 'pending'`+where+`
			GROUP BY e.id ORDER BY e.path`, append([]any{string(r.source)}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key int64
			f, err := scanFile(rows, &key)
			if err != nil {
				return err
			}
			f.format, f.key, f.arcState = domain.ArchiveZip, key, string(domain.ArchiveComplete)
			if key >= LargeFileBytes {
				large = append(large, f)
			} else {
				small = append(small, f)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, fmt.Errorf("content: find the zip members to hash on %s: %w", r.source, err)
	}
	return large, small, nil
}

// samplePass reads the samples of the pending files of at least
// SampleFromBytes in spans whose size samples can settle, largest size
// first, and commits each size with its group decided again (design D3,
// D4). A size with a member, or with a hashed copy whose sample is
// unknown, is read in full instead.
func (r *run) samplePass(spans []span) error {
	var sizes []int64
	err := r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(r.ctx, `SELECT DISTINCT size FROM file_content
			WHERE source_id = ? AND state = 'pending' AND size >= ? ORDER BY size DESC`, string(r.source), SampleFromBytes)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var size int64
			if err := rows.Scan(&size); err != nil {
				return err
			}
			sizes = append(sizes, size)
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("content: sizes to sample on %s: %w", r.source, err)
	}
	where, args := spanWhere(spans)
	for _, size := range sizes {
		var group []fileRow
		var settled bool
		err := r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(r.ctx, `SELECT EXISTS (SELECT 1 FROM archive_members m
					JOIN archives a ON a.entry_id = m.archive_id
					WHERE m.size = ?1 AND m.kind = 'file' AND a.state = 'complete' AND m.state <> 'unique_size')
				OR EXISTS (SELECT 1 FROM file_content WHERE source_id IN (SELECT id FROM sources)
					AND state = 'hashed' AND size = ?1 AND sample IS NULL)`, size).Scan(&settled); err != nil {
				return err
			}
			if settled {
				return nil
			}
			rows, err := tx.QueryContext(r.ctx, `SELECT `+fileCols+` FROM file_content f JOIN entries e ON e.id = f.entry_id
				WHERE f.source_id = ? AND f.state = 'pending' AND f.size = ? AND f.sample IS NULL AND e.state = 'present'`+
				where+` ORDER BY e.id`, append([]any{string(r.source), size}, args...)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				f, err := scanFile(rows)
				if err != nil {
					return err
				}
				group = append(group, f)
			}
			return rows.Err()
		})
		if err != nil {
			return fmt.Errorf("content: files of size %d on %s: %w", size, r.source, err)
		}
		for i := range group {
			if r.skip[group[i].id] {
				continue
			}
			if err := r.readOne(&group[i], true); err != nil {
				return err
			}
		}
		if err := r.commit(); err != nil {
			return err
		}
	}
	return nil
}

// largePass reads, by size descending, the pending files of at least
// LargeFileBytes in spans and the archive items (streamed archives to list,
// zips with members to hash), merged by their keys (design D4 step 3).
func (r *run) largePass(spans []span, items []fileRow) error {
	slices.SortStableFunc(items, func(a, b fileRow) int { return -cmpInt(a.key, b.key) })
	where, args := spanWhere(spans)
	curSize, curID := int64(math.MaxInt64), int64(math.MaxInt64)
	for {
		var page []fileRow
		err := r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(r.ctx, `SELECT `+fileCols+` FROM file_content f JOIN entries e ON e.id = f.entry_id
				WHERE f.source_id = ? AND f.state = 'pending' AND f.size >= ? AND (f.size < ? OR (f.size = ? AND f.entry_id < ?))
					AND e.state = 'present'`+where+`
				ORDER BY f.size DESC, f.entry_id DESC LIMIT ?`,
				append(append([]any{string(r.source), LargeFileBytes, curSize, curSize, curID}, args...), pageRows)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				f, err := scanFile(rows)
				if err != nil {
					return err
				}
				page = append(page, f)
			}
			return rows.Err()
		})
		if err != nil {
			return fmt.Errorf("content: large files of %s: %w", r.source, err)
		}
		for i := range page {
			f := &page[i]
			for len(items) > 0 && items[0].key >= f.size {
				if err := r.item(&items[0]); err != nil {
					return err
				}
				items = items[1:]
			}
			if r.skip[f.id] {
				continue
			}
			if err := r.readOne(f, false); err != nil {
				return err
			}
		}
		if len(page) < pageRows {
			break
		}
		curSize, curID = page[len(page)-1].size, page[len(page)-1].id
	}
	for i := range items {
		if err := r.item(&items[i]); err != nil {
			return err
		}
	}
	return nil
}

// smallPass reads the pending files under LargeFileBytes in spans in path
// order, and the zip items among them by their path (design D4 steps 4
// and 5). With mark, the files it reads are not read again by a later
// pass of this attempt.
func (r *run) smallPass(spans []span, items []fileRow, mark bool) error {
	where, args := spanWhere(spans)
	var cursor []byte
	for {
		var page []fileRow
		cond, cargs := "", []any{}
		if cursor != nil {
			cond, cargs = " AND e.path > ?", []any{cursor}
		}
		err := r.s.st.Read(r.ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(r.ctx, `SELECT `+fileCols+` FROM entries e JOIN file_content f ON f.entry_id = e.id
				WHERE e.source_id = ?`+cond+` AND e.state = 'present' AND f.state = 'pending' AND f.size < ?`+where+`
				ORDER BY e.path LIMIT ?`,
				append(append(append([]any{string(r.source)}, cargs...), LargeFileBytes), append(args, pageRows)...)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				f, err := scanFile(rows)
				if err != nil {
					return err
				}
				page = append(page, f)
			}
			return rows.Err()
		})
		if err != nil {
			return fmt.Errorf("content: small files of %s: %w", r.source, err)
		}
		for i := range page {
			f := &page[i]
			for len(items) > 0 && bytes.Compare(items[0].path, f.path) < 0 {
				if err := r.item(&items[0]); err != nil {
					return err
				}
				items = items[1:]
			}
			if r.skip[f.id] {
				continue
			}
			if mark {
				r.skip[f.id] = true
			}
			if err := r.readOne(f, false); err != nil {
				return err
			}
		}
		if len(page) < pageRows {
			break
		}
		cursor = page[len(page)-1].path
	}
	for i := range items {
		if err := r.item(&items[i]); err != nil {
			return err
		}
	}
	return nil
}

// item reads one archive item: a zip whose pending members are hashed; or a
// streamed archive to list, once per attempt, whose own digest comes with
// its listing or, failing that, from a plain read.
func (r *run) item(f *fileRow) error {
	if f.arcState == string(domain.ArchiveComplete) {
		if err := r.commit(); err != nil {
			return err
		}
		return r.hashMembers(f)
	}
	if r.items[f.id] {
		return nil
	}
	r.items[f.id] = true
	if err := r.commit(); err != nil {
		return err
	}
	added, digested, err := r.listArchive(f)
	r.added = r.added || added
	if err != nil || digested || f.state != domain.ContentPending {
		return err
	}
	return r.readOne(f, false)
}

// readOne reads one file, by samples or in full, and queues its result;
// another name of an inode read already takes that result. It commits
// when CommitFiles results are queued.
func (r *run) readOne(f *fileRow, samples bool) error {
	key, links := f.inode(r.caps)
	if links {
		if l, ok := r.inodes[key]; ok && sameStat(&l.f, f) && (l.res.sample != nil) == samples {
			return r.queue(unit{f: *f, res: l.res})
		}
	}
	res, err := r.readFile(f, samples)
	if err != nil {
		return err
	}
	if links && (res.fail == nil || res.inode) {
		r.inodes[key] = linked{f: *f, res: res}
	}
	if err := r.queue(unit{f: *f, res: res}); err != nil {
		return err
	}
	return nil
}

func (r *run) queue(u unit) error {
	r.units = append(r.units, u)
	if len(r.units) >= CommitFiles {
		return r.commit()
	}
	return nil
}

// stop ends an interrupted attempt: after a cancel, or with the source
// gone, the results finished before are still committed (design D4).
func (r *run) stop(err error) error {
	if r.ctx.Err() == nil && domain.CodeOf(err) != domain.CodeSourceOffline {
		return err
	}
	ctx := context.WithoutCancel(r.ctx)
	if cerr := r.commitCtx(ctx, false); cerr != nil {
		return errors.Join(err, cerr)
	}
	if r.changed {
		if cerr := r.s.runner.Write(ctx, r.s.requestRefreshTx); cerr != nil {
			return errors.Join(err, cerr)
		}
	}
	return err
}

// finish commits what is left and asks for a relate pass when the job
// changed anything (design D5).
func (r *run) finish() error {
	if err := r.commit(); err != nil {
		return err
	}
	if r.changed {
		if err := r.s.runner.Write(r.ctx, r.s.requestRefreshTx); err != nil {
			return err
		}
	}
	r.report()
	return nil
}

func (s *Service) requestRefreshTx(tx *jobs.Tx) error {
	if s.requestRefresh == nil {
		return nil
	}
	return s.requestRefresh(tx)
}

// commit writes the queued results, yielding after each transaction.
func (r *run) commit() error { return r.commitCtx(r.ctx, true) }

// commitCtx writes the queued results, at most CommitFiles per
// transaction (design D4, Transaction boundaries): each result is kept only
// when its entry is present at the path its read used, with the size,
// times, and identity its read observed, and its file_content row exists
// (I9; r3 design D18: a file whose folder was moved since keeps its
// identity, and its read through the old path must not record it changed).
// Coverage deltas apply in the same transaction, the sizes whose samples
// changed are decided again, and a relate pass is requested every
// refresh_interval.
func (r *run) commitCtx(ctx context.Context, yield bool) error {
	for len(r.units) > 0 {
		batch := r.units[:min(len(r.units), CommitFiles)]
		now := r.s.clk.Now()
		var wrote, refreshed bool
		err := r.s.runner.Write(ctx, func(tx *jobs.Tx) error {
			wrote, refreshed = false, false
			d := deltas{}
			sizes := map[int64]bool{}
			for i := range batch {
				ok, err := r.apply(ctx, tx.SQL(), &batch[i], d, clock.Millis(now), sizes)
				if err != nil {
					return err
				}
				wrote = wrote || ok
			}
			for size := range sizes {
				if err := settle(ctx, tx, size, d, r.source); err != nil {
					return err
				}
			}
			if err := d.apply(ctx, tx.SQL(), clock.Millis(now)); err != nil {
				return err
			}
			if (r.changed || wrote) && now.Sub(r.lastRefresh) >= r.s.refresh {
				refreshed = true
				return r.s.requestRefreshTx(tx)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("content: commit hashing results of %s: %w", r.source, err)
		}
		r.changed = r.changed || wrote
		if refreshed {
			r.lastRefresh = now
		}
		r.units = r.units[len(batch):]
		r.report()
		if yield {
			if err := r.rt.Yield(ctx); err != nil {
				return err
			}
		}
	}
	r.units = r.units[:0]
	return nil
}

// apply writes one result after the I9 re-check, which includes the path
// the read used (r3 design D18). It reports whether it wrote.
func (r *run) apply(ctx context.Context, tx *sql.Tx, u *unit, d deltas, now int64, sizes map[int64]bool) (bool, error) {
	f := &u.f
	var old domain.ContentState
	err := tx.QueryRowContext(ctx, `SELECT fc.state FROM file_content fc JOIN entries e ON e.id = fc.entry_id
		WHERE fc.entry_id = ? AND e.state = 'present' AND e.path = ? AND e.size = ? AND e.mtime_ns IS ?
			AND e.ctime_ns IS ? AND e.ino IS ? AND e.dev IS ?`, f.id, f.path, f.size, f.mtime, f.ctime, f.ino, f.dev).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		// The entry changed, moved, or went: the result is dropped, and the
		// file stays as it was, to be read again.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	src := string(r.source)
	switch res := &u.res; {
	case res.digest != nil:
		id, err := contentID(ctx, tx, res.digest, f.size)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE file_content SET state = 'hashed', content_id = ?, mtime_ns = ?,
				ctime_ns = ?, ino = ?, checked_at = ? WHERE entry_id = ?`,
			id, f.mtime, f.ctime, f.ino, now, f.id); err != nil {
			return false, err
		}
		d.move(src, old, domain.ContentHashed, f.size)
	case res.sample != nil:
		if _, err := tx.ExecContext(ctx, `UPDATE file_content SET sample = ?, mtime_ns = ?, ctime_ns = ?, ino = ?,
				checked_at = ? WHERE entry_id = ?`, res.sample, f.mtime, f.ctime, f.ino, now, f.id); err != nil {
			return false, err
		}
		sizes[f.size] = true
	case res.fail != nil:
		q := `UPDATE file_content SET state = 'unreadable', content_id = NULL, checked_at = ? WHERE entry_id = ?`
		if res.fail.state == domain.ContentChanged {
			q = `UPDATE file_content SET state = 'changed', content_id = NULL, sample = NULL, checked_at = ?
				WHERE entry_id = ?`
		}
		if _, err := tx.ExecContext(ctx, q, now, f.id); err != nil {
			return false, err
		}
		d.move(src, old, res.fail.state, f.size)
	default:
		return false, nil
	}
	return true, nil
}

// contentID upserts the contents row of a SHA-256 digest and returns its
// ID.
func contentID(ctx context.Context, tx *sql.Tx, digest []byte, size int64) (int64, error) {
	var id, got int64
	err := tx.QueryRowContext(ctx, `INSERT INTO contents (sha256, size) VALUES (?, ?)
		ON CONFLICT (sha256) DO UPDATE SET size = size RETURNING id, size`, digest, size).Scan(&id, &got)
	if err != nil {
		return 0, fmt.Errorf("content: record a digest: %w", err)
	}
	if got != size {
		return 0, fmt.Errorf("content: digest %x recorded with size %d, read with %d", digest, got, size)
	}
	return id, nil
}

// report publishes the job's progress: the source's coverage, the bytes
// read, and the archives listed.
func (r *run) report() {
	if c, err := CoverageOf(r.ctx, r.s.st.Reader(), r.source); err == nil {
		r.cov = c
	}
	r.rt.Progress(map[string]int64{
		ProgressPhase:          r.phase,
		ProgressCandidateFiles: r.cov.CandidateFiles,
		ProgressCandidateBytes: r.cov.CandidateBytes,
		ProgressCheckedFiles:   r.cov.CheckedFiles,
		ProgressCheckedBytes:   r.cov.CheckedBytes,
		ProgressReadBytes:      r.readBytes,
		ProgressArchivesListed: r.listed,
		ProgressUnreadable:     r.cov.UnreadableFiles,
	})
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
