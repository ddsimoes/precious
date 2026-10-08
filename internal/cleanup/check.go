package cleanup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"precious/internal/cleanup/stale"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/executor"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/store"
)

// The pre-delete check (r4 design D7, D8, D10). A check is a read-only job
// over a chosen set of quarantined top items of one source, the
// purge_check_items rows the check-purge command writes with the check in
// state running. The job:
//
//  1. defers while an organize job of the source is queued or running;
//  2. records every entry below each item, of every kind, with the
//     identity an lstat gives it, into purge_check_files: files are read in
//     full (content.HashEntry), a complete archive once (HashArchive, one
//     record per file member); an archive that is not complete is one
//     opaque_archive file; empty files, folders, links, and special files
//     are no_content. An item that holds a folder the scan could not read,
//     a partial folder, or a mount point is unreadable: Precious cannot see
//     what it would delete (I7);
//  3. looks for a copy of each digest outside the set and the quarantine,
//     and reads it in full, identity-checked, on an online source, before it
//     counts (copies.go);
//  4. ends in one transaction: ready only while still running and with
//     every item at its recorded quarantine path, else stale (I9).
//
// It writes no file_content row, never writes to a disk, and is not an
// executor step (I2).

// KindPurgeCheck is the job of one check (D7), registered with
// ClassInteractive and bound to the check's source. Its scope is
// "purge_check:<check_id>" and its payload {"check_id":"…"}. Its progress
// holds files and bytes read so far, of of_files and of_bytes.
const KindPurgeCheck jobs.Kind = "purge_check"

// Check states (purge_checks.state).
const (
	checkRunning = "running"
	checkReady   = "ready"
	checkFailed  = "failed"
	checkStale   = "stale"
)

// Verdicts and classes of purge_check_files (D7, D8).
const (
	verdictSafe        = "safe"
	verdictCopyOffline = "copy_offline"
	verdictUnique      = "unique"
	verdictUnreadable  = "unreadable"
	verdictOpaque      = "opaque_archive"
	verdictNoContent   = "no_content"

	classValuable  = "possibly_valuable"
	classJunk      = "likely_junk"
	classUncertain = "uncertain"
)

const (
	// organizingDelay is how long a check waits while its source organizes,
	// as a scan does.
	organizingDelay = 3 * time.Second
	// pageRows is how many entries one read of an item's subtree fetches.
	pageRows = 256
	// flushRecords is how many records the check buffers before it writes
	// them; it also writes at the end of each item.
	flushRecords = 256
)

// errNotRunning stops a check that is no longer running: it went stale, or
// failed, while it ran.
var errNotRunning = errors.New("cleanup: the check is no longer running")

// checkPayload is the payload of a purge_check job.
type checkPayload struct {
	CheckID string `json:"check_id"`
}

// checkScope is the scope of the job of one check.
func checkScope(check int64) string { return "purge_check:" + strconv.FormatInt(check, 10) }

// enqueueCheck starts the job of check, bound to src, and records it in
// purge_checks.job_id. The caller has inserted the check (state running)
// and its items in tx.
func enqueueCheck(tx *jobs.Tx, src domain.SourceID, check int64) (jobs.Record, error) {
	p, err := json.Marshal(checkPayload{CheckID: strconv.FormatInt(check, 10)})
	if err != nil {
		return jobs.Record{}, err
	}
	rec, err := tx.Enqueue(jobs.Spec{Kind: KindPurgeCheck, SourceID: src, ScopeKey: checkScope(check), Payload: p})
	if err != nil {
		return jobs.Record{}, fmt.Errorf("cleanup: enqueue check %d: %w", check, err)
	}
	if _, err := tx.SQL().Exec(`UPDATE purge_checks SET job_id = ? WHERE id = ?`, int64(rec.ID), check); err != nil {
		return jobs.Record{}, err
	}
	return rec, nil
}

// settleChecks ends, as failed, every running check whose job is no longer
// queued, running, or paused: a job cancelled before it ran, or ended by
// the runner without its handler (a worker lost too often). The check-purge
// command and the check reads call it in their transaction, so such a check
// never blocks a new one.
func settleChecks(ctx context.Context, tx *sql.Tx, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE purge_checks SET state = 'failed', finished_at = ?
		WHERE state = 'running' AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id = purge_checks.job_id
			AND j.state IN ('queued', 'running', 'paused'))`, clock.Millis(now))
	return err
}

// Register registers the purge_check handler in the interactive class.
func (s *Service) Register(r *jobs.Runner) {
	if s.runner == nil {
		s.runner = r
	}
	r.RegisterClass(KindPurgeCheck, &checkHandler{s: s}, jobs.ClassInteractive)
}

type checkHandler struct{ s *Service }

// Run runs one attempt of a check. An attempt starts over: it deletes the
// records a previous attempt left.
func (h *checkHandler) Run(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	var p checkPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return domain.Errorf(domain.CodeInvalidRequest, "purge_check payload: %v", err)
	}
	id, err := strconv.ParseInt(p.CheckID, 10, 64)
	if err != nil || id <= 0 {
		return domain.Errorf(domain.CodeInvalidRequest, "purge_check payload: check_id %q", p.CheckID)
	}
	return h.s.runCheck(ctx, job, rt, id)
}

// runCheck runs check id and records its end.
func (s *Service) runCheck(ctx context.Context, job jobs.Job, rt jobs.Runtime, id int64) error {
	var src, state string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT source_id, state FROM purge_checks WHERE id = ?`, id).
		Scan(&src, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // deleted with its source
	}
	if err != nil {
		return err
	}
	if domain.SourceID(src) != job.SourceID {
		return domain.Errorf(domain.CodeInvalidRequest, "check %d is of source %q, not of the job's %q", id, src,
			job.SourceID)
	}
	if state != checkRunning {
		return s.settle(ctx, id)
	}
	active, err := executor.OrganizeActive(ctx, s.st.Reader(), job.SourceID)
	if err != nil {
		return err
	}
	if active {
		return &jobs.Defer{Until: s.clk.Now().Add(organizingDelay), Reason: index.DeferOrganizing}
	}

	c := newChecker(s, rt, id, job.SourceID)
	defer c.close()
	err = c.run(ctx)
	if err == nil {
		err = c.finish(ctx)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotRunning):
		return s.settle(context.WithoutCancel(ctx), id)
	case ctx.Err() != nil:
		// A cancel request fails the check; a stop or a lost lease leaves it
		// running for the next attempt.
		if s.cancelRequested(context.WithoutCancel(ctx), job.ID) {
			if ferr := s.fail(context.WithoutCancel(ctx), id); ferr != nil {
				s.log.Error("cleanup: record a cancelled check", "check", id, "err", ferr)
			}
		}
		return err
	default:
		if ferr := s.fail(context.WithoutCancel(ctx), id); ferr != nil {
			s.log.Error("cleanup: record a failed check", "check", id, "err", ferr)
		}
		return err
	}
}

// cancelRequested reports whether job has a cancel request.
func (s *Service) cancelRequested(ctx context.Context, job domain.JobID) bool {
	var requested bool
	err := s.st.Reader().QueryRowContext(ctx, `SELECT cancel_requested FROM jobs WHERE id = ?`, int64(job)).
		Scan(&requested)
	return err == nil && requested
}

// fail ends a running check as failed.
func (s *Service) fail(ctx context.Context, id int64) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE purge_checks SET state = 'failed', finished_at = ?
			WHERE id = ? AND state = 'running'`, clock.Millis(s.clk.Now()), id)
		return err
	})
}

// settle records when a check that stopped running (stale or failed meanwhile)
// ended, once.
func (s *Service) settle(ctx context.Context, id int64) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE purge_checks SET finished_at = ?
			WHERE id = ? AND state IN ('stale', 'failed') AND finished_at IS NULL`, clock.Millis(s.clk.Now()), id)
		return err
	})
}

// setItem is one item of the set: a quarantined top entry, at its recorded
// quarantine path.
type setItem struct {
	entry    int64
	path     []byte
	readable bool
}

// setEntry is an indexed entry of the set, with its archive's state when it
// has an archives row.
type setEntry struct {
	id                                      int64
	path                                    []byte
	kind                                    domain.EntryKind
	state                                   string
	size                                    int64
	mtime, ctime, ino, dev, nlink, alloc    sql.NullInt64
	category, family, traits, fileKind, ext sql.NullString
	archive                                 sql.NullString
}

const entryColumns = `e.id, e.path, e.kind, e.state, e.size, e.mtime_ns, e.ctime_ns, e.ino, e.dev, e.nlink, e.alloc,
	e.category, e.family, e.traits, e.file_kind, e.ext, a.state`

func scanEntry(sc interface{ Scan(...any) error }, e *setEntry) error {
	return sc.Scan(&e.id, &e.path, &e.kind, &e.state, &e.size, &e.mtime, &e.ctime, &e.ino, &e.dev, &e.nlink,
		&e.alloc, &e.category, &e.family, &e.traits, &e.fileKind, &e.ext, &e.archive)
}

// Reads of a set item's subtree, by the (source_id, path) index. The bounds
// are BLOBs: lo is the item's path and '/', hi its path and '0'. The item
// and its subtree are the range [path, hi) less the siblings between path
// and lo (such as "a.txt" next to "a"), which a residual drops.
const (
	// itemEntrySQL: id.
	itemEntrySQL = `SELECT e.source_id, ` + entryColumns + ` FROM entries e LEFT JOIN archives a ON a.entry_id = e.id
		WHERE e.id = ?`
	// subtreePageSQL: src, after, hi, limit.
	subtreePageSQL = `SELECT ` + entryColumns + ` FROM entries e LEFT JOIN archives a ON a.entry_id = e.id
		WHERE e.source_id = ? AND e.path > ? AND e.path < ? ORDER BY e.path LIMIT ?`
	// blindSQL: src, path, lo, hi. The item, or an entry below it, is a
	// folder the scan could not read or read in part, or a mount point.
	blindSQL = `SELECT EXISTS (SELECT 1 FROM entries e WHERE e.source_id = ?1 AND e.path >= ?2 AND e.path < ?4
		AND (e.path = ?2 OR e.path > ?3)
		AND ((e.kind = 'directory' AND (e.state = 'unreadable' OR e.partial = 1)) OR e.mount_boundary = 1))`
	// toReadSQL: src, path, lo, hi. The files a check reads in a readable
	// item.
	toReadSQL = `SELECT count(*), coalesce(sum(e.size), 0) FROM entries e WHERE e.source_id = ?1 AND e.path >= ?2
		AND e.path < ?4 AND (e.path = ?2 OR e.path > ?3) AND e.kind = 'file' AND e.state = 'present' AND e.size > 0`
)

// bounds returns the half-open range (lo, hi) of the paths below path.
func bounds(path []byte) (lo, hi []byte) {
	lo = append(append(make([]byte, 0, len(path)+1), path...), '/')
	hi = append(append(make([]byte, 0, len(path)+1), path...), '0')
	return lo, hi
}

// identity is what a record holds of an lstat (or, for an unreadable item,
// of the index row).
type identity struct {
	mtime, ctime, ino, dev, nlink, alloc sql.NullInt64
}

func identityOf(info fsaccess.EntryInfo) identity {
	id := identity{
		mtime: sql.NullInt64{Int64: info.ModTime.UnixNano(), Valid: true},
		ino:   sql.NullInt64{Int64: int64(info.Ino), Valid: true},
		dev:   sql.NullInt64{Int64: int64(info.Dev), Valid: true},
		nlink: sql.NullInt64{Int64: int64(info.Nlink), Valid: true},
		alloc: sql.NullInt64{Int64: info.Blocks * 512, Valid: true},
	}
	if !info.Ctime.IsZero() {
		id.ctime = sql.NullInt64{Int64: info.Ctime.UnixNano(), Valid: true}
	}
	return id
}

// found is a verified copy: the copy's file (the archive file of a member)
// as an lstat showed it when it was read.
type found struct {
	source domain.SourceID
	path   []byte
	entry  int64
	member int64 // 0 for a file
	info   fsaccess.EntryInfo
}

// record is one purge_check_files row.
type record struct {
	item   int64
	entry  int64
	member int64 // 0 for an entry of the disk
	kind   domain.EntryKind
	path   []byte
	size   int64
	id     identity
	// own is the lstat of the recorded file, for the hard-link test.
	own     *fsaccess.EntryInfo
	sum     *[32]byte
	verdict string
	class   string
	copy    *found
}

const insertRecordSQL = `INSERT INTO purge_check_files (check_id, item_id, entry_id, member_id, kind, path, size,
	mtime_ns, ctime_ns, ino, dev, nlink, alloc, sha256, verdict, class, copy_source, copy_path, copy_entry,
	copy_member, copy_size, copy_mtime_ns, copy_ctime_ns, copy_ino, copy_dev, copy_hard_link)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// args are the record's insert arguments for check.
func (r *record) args(check int64) []any {
	var member, sum, class any
	if r.member != 0 {
		member = r.member
	}
	if r.sum != nil {
		sum = r.sum[:]
	}
	if r.class != "" {
		class = r.class
	}
	var (
		cSource, cPath, cEntry, cMember, cSize, cMtime, cCtime, cIno, cDev any
		hardLink                                                           int
	)
	if c := r.copy; c != nil {
		ci := identityOf(c.info)
		cSource, cPath, cEntry, cSize = string(c.source), c.path, c.entry, c.info.Size
		cMtime, cCtime, cIno, cDev = ci.mtime, ci.ctime, ci.ino, ci.dev
		if c.member != 0 {
			cMember = c.member
		} else if r.own != nil && c.info.Ino != 0 && c.info.Dev == r.own.Dev && c.info.Ino == r.own.Ino {
			hardLink = 1
		}
	}
	return []any{check, r.item, r.entry, member, string(r.kind), r.path, r.size, r.id.mtime, r.id.ctime, r.id.ino,
		r.id.dev, r.id.nlink, r.id.alloc, sum, r.verdict, class, cSource, cPath, cEntry, cMember, cSize, cMtime,
		cCtime, cIno, cDev, hardLink}
}

// checker is one attempt of one check.
type checker struct {
	s   *Service
	rt  jobs.Runtime
	q   store.Queryer
	id  int64
	src domain.SourceID
	// own is the check's source, opened at the start.
	own *openSource
	// opened holds the sources opened for copies; nil marks one that is
	// not online.
	opened map[domain.SourceID]*openSource
	// sourceIDs lists every source, for the same-size search.
	sourceIDs []domain.SourceID
	// copies is the search result of each digest read so far; sums the
	// digest of each candidate already read (ok false when it failed).
	copies map[[32]byte]copyResult
	sums   map[candidateKey]candidateSum
	// relied holds the state of each entry a found copy relies on, by ID
	// (rely).
	relied map[int64]string

	items                          []setItem
	pending                        []record
	files, bytes, ofFiles, ofBytes int64
}

func newChecker(s *Service, rt jobs.Runtime, id int64, src domain.SourceID) *checker {
	return &checker{s: s, rt: rt, q: s.st.Reader(), id: id, src: src, opened: map[domain.SourceID]*openSource{},
		copies: map[[32]byte]copyResult{}, sums: map[candidateKey]candidateSum{}, relied: map[int64]string{}}
}

// close closes every source the check opened.
func (c *checker) close() {
	if c.own != nil {
		c.own.close()
	}
	for _, o := range c.opened {
		if o != nil {
			o.close()
		}
	}
}

// run records the set and its copies.
func (c *checker) run(ctx context.Context) error {
	if err := c.loadItems(ctx); err != nil {
		return err
	}
	if err := c.start(ctx); err != nil {
		return err
	}
	opened, err := c.s.src.Open(ctx, c.src)
	if err != nil {
		return err
	}
	c.own = &openSource{root: opened.Root, caps: opened.Source.Caps, w: &walker{root: opened.Root}}
	c.progress()
	for _, it := range c.items {
		if err := c.item(ctx, it); err != nil {
			return err
		}
		if err := c.flush(ctx); err != nil {
			return err
		}
	}
	return nil
}

// loadItems reads the set, finds which items are readable, and counts what
// the check will read. An item no longer at its recorded path makes the
// check stale at once.
func (c *checker) loadItems(ctx context.Context) error {
	rows, err := c.q.QueryContext(ctx, `SELECT entry_id, path FROM purge_check_items WHERE check_id = ? ORDER BY path`,
		c.id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var it setItem
		if err := rows.Scan(&it.entry, &it.path); err != nil {
			rows.Close()
			return err
		}
		c.items = append(c.items, it)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range c.items {
		it := &c.items[i]
		var src string
		var e setEntry
		err := scanEntry(rowScanner{c.q.QueryRowContext(ctx, itemEntrySQL, it.entry), &src}, &e)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (domain.SourceID(src) != c.src || !bytes.Equal(e.path, it.path) ||
			e.state == "missing") {
			return c.misplaced(ctx, it.path)
		}
		if err != nil {
			return err
		}
		lo, hi := bounds(it.path)
		var blind bool
		if err := c.q.QueryRowContext(ctx, blindSQL, string(c.src), it.path, lo, hi).Scan(&blind); err != nil {
			return err
		}
		it.readable = !blind
		if !it.readable {
			continue
		}
		var n, b int64
		if err := c.q.QueryRowContext(ctx, toReadSQL, string(c.src), it.path, lo, hi).Scan(&n, &b); err != nil {
			return err
		}
		c.ofFiles, c.ofBytes = c.ofFiles+n, c.ofBytes+b
	}
	return nil
}

// rowScanner scans the source column first, then the entry's.
type rowScanner struct {
	row *sql.Row
	src *string
}

func (r rowScanner) Scan(dest ...any) error { return r.row.Scan(append([]any{r.src}, dest...)...) }

// misplaced marks the check, and every check relying on path, stale: a set
// item is no longer at its recorded quarantine path (D10).
func (c *checker) misplaced(ctx context.Context, path []byte) error {
	err := c.s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := stale.MarkStale(ctx, tx, c.src, path); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE purge_checks SET state = 'stale', stale_reason = ?
			WHERE id = ? AND state = 'running'`, stale.ReasonIndexChanged, c.id)
		return err
	})
	if err != nil {
		return err
	}
	return errNotRunning
}

// start deletes what an earlier attempt recorded and records which items
// are readable.
func (c *checker) start(ctx context.Context) error {
	return c.s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := c.stillRunning(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM purge_check_files WHERE check_id = ?`, c.id); err != nil {
			return err
		}
		for _, it := range c.items {
			if _, err := tx.ExecContext(ctx, `UPDATE purge_check_items SET readable = ? WHERE check_id = ? AND entry_id = ?`,
				boolInt(it.readable), c.id, it.entry); err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *checker) stillRunning(ctx context.Context, tx *sql.Tx) error {
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM purge_checks WHERE id = ?`, c.id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errNotRunning
		}
		return err
	}
	if state != checkRunning {
		return errNotRunning
	}
	return nil
}

// flush writes the pending records, while the check is still running.
func (c *checker) flush(ctx context.Context) error {
	if len(c.pending) == 0 {
		return nil
	}
	err := c.s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := c.stillRunning(ctx, tx); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, insertRecordSQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i := range c.pending {
			if _, err := stmt.ExecContext(ctx, c.pending[i].args(c.id)...); err != nil {
				return fmt.Errorf("cleanup: record %q of check %d: %w", domain.DisplayName(c.pending[i].path), c.id, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.pending = c.pending[:0]
	return nil
}

// add buffers a record, writing the buffer once it is full.
func (c *checker) add(ctx context.Context, r record) error {
	c.pending = append(c.pending, r)
	if len(c.pending) >= flushRecords {
		return c.flush(ctx)
	}
	return nil
}

func (c *checker) progress() {
	c.rt.Progress(map[string]int64{"files": c.files, "bytes": c.bytes, "of_files": c.ofFiles, "of_bytes": c.ofBytes})
}

// item records the item it and everything below it, in path order.
func (c *checker) item(ctx context.Context, it setItem) error {
	var src string
	var top setEntry
	err := scanEntry(rowScanner{c.q.QueryRowContext(ctx, itemEntrySQL, it.entry), &src}, &top)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (domain.SourceID(src) != c.src || !bytes.Equal(top.path, it.path)) {
		return c.misplaced(ctx, it.path)
	}
	if err != nil {
		return err
	}
	if err := c.entry(ctx, it, &top); err != nil {
		return err
	}
	after, hi := bounds(it.path)
	for {
		page, err := c.page(ctx, after, hi)
		if err != nil {
			return err
		}
		for i := range page {
			if err := c.entry(ctx, it, &page[i]); err != nil {
				return err
			}
		}
		if len(page) < pageRows {
			return nil
		}
		after = page[len(page)-1].path
	}
}

// page reads the next entries of the subtree after after, below hi.
func (c *checker) page(ctx context.Context, after, hi []byte) ([]setEntry, error) {
	rows, err := c.q.QueryContext(ctx, subtreePageSQL, string(c.src), after, hi, pageRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []setEntry
	for rows.Next() {
		var e setEntry
		if err := scanEntry(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// entry records one indexed entry of the item it. A missing entry is not on
// the disk, so there is nothing to record or delete.
func (c *checker) entry(ctx context.Context, it setItem, e *setEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.state == "missing" {
		return nil
	}
	r := record{item: it.entry, entry: e.id, kind: e.kind, path: e.path, size: e.size}
	if !it.readable {
		// The item cannot be purged (D11): what the index holds is recorded,
		// and nothing below it is read.
		r.id = identity{mtime: e.mtime, ctime: e.ctime, ino: e.ino, dev: e.dev, nlink: e.nlink, alloc: e.alloc}
		r.verdict = verdictUnreadable
		return c.add(ctx, r)
	}
	if e.kind != domain.EntryFile || e.state == "unreadable" || e.size == 0 {
		info, err := c.lstat(e)
		if err != nil {
			return err
		}
		r.id, r.own = identityOf(info), &info
		r.verdict = verdictNoContent
		if e.kind == domain.EntryFile && e.state == "unreadable" {
			r.verdict = verdictUnreadable
		}
		return c.add(ctx, r)
	}
	if err := c.rt.Yield(ctx); err != nil {
		return err
	}
	var err error
	if e.archive.Valid && e.archive.String == string(domain.ArchiveComplete) {
		err = c.archive(ctx, it, e)
	} else {
		err = c.file(ctx, it, e)
	}
	if err != nil {
		return err
	}
	c.files++
	c.bytes += e.size
	c.progress()
	return nil
}

// lstat lstats e on the disk and checks that it is what the index holds.
func (c *checker) lstat(e *setEntry) (fsaccess.EntryInfo, error) {
	info, err := c.own.w.lstat(e.path)
	if err != nil {
		return fsaccess.EntryInfo{}, changedError(err, e.path)
	}
	if !sameEntry(e, info, c.own.caps) {
		return fsaccess.EntryInfo{}, changedError(errMismatch, e.path)
	}
	return info, nil
}

// file reads the file e in full and looks for a copy of it. A file that is
// an archive Precious did not open completely is one opaque_archive file.
func (c *checker) file(ctx context.Context, it setItem, e *setEntry) error {
	opaque := e.archive.Valid || e.fileKind.String == string(domain.FileKindArchive)
	sum, info, err := hashEntry(ctx, c.own, e)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if o, _ := fsaccess.OutcomeOf(err); o != domain.OutcomeUnreadable {
			return err
		}
		// The file cannot be read: it is recorded as it is on the disk, and
		// needs a confirmation.
		linfo, lerr := c.lstat(e)
		if lerr != nil {
			return lerr
		}
		return c.add(ctx, record{item: it.entry, entry: e.id, kind: e.kind, path: e.path, size: e.size,
			id: identityOf(linfo), own: &linfo, verdict: verdictUnreadable})
	}
	res, err := c.findCopy(ctx, sum, e.size)
	if err != nil {
		return err
	}
	r := record{item: it.entry, entry: e.id, kind: e.kind, path: e.path, size: e.size, id: identityOf(info),
		own: &info, sum: &sum}
	switch {
	case res.copy != nil:
		r.verdict, r.copy = verdictSafe, res.copy
		if opaque {
			r.verdict = verdictOpaque
		}
	case res.offline:
		r.verdict = verdictCopyOffline
	case opaque:
		r.verdict, r.class = verdictOpaque, classUncertain
	default:
		r.verdict = verdictUnique
		r.class = fileClass(e.category.String, e.family.String, e.traits.String, e.fileKind.String, e.ext.String)
	}
	return c.add(ctx, r)
}

// memberSum is one file member HashArchive read.
type memberSum struct {
	id   int64
	sum  [32]byte
	size int64
}

// archive records the complete archive e and each of its file members,
// read in one pass. Its own record is no_content: its content is its
// members, each recorded with its verdict. An archive that no longer reads
// as listed is read as one opaque file instead.
func (c *checker) archive(ctx context.Context, it setItem, e *setEntry) error {
	info, err := c.lstat(e)
	if err != nil {
		return err
	}
	names, err := c.memberNames(ctx, e.id)
	if err != nil {
		return err
	}
	var members []memberSum
	err = c.s.content.HashArchive(ctx, c.q, domain.EntryID(e.id), func(member int64, sum [32]byte, size int64) error {
		members = append(members, memberSum{id: member, sum: sum, size: size})
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch domain.CodeOf(err) {
		case domain.CodeInvalidEntryState, domain.CodeNotFound:
			return c.file(ctx, it, &setEntry{id: e.id, path: e.path, kind: e.kind, state: e.state, size: e.size,
				mtime: e.mtime, ctime: e.ctime, ino: e.ino, archive: sql.NullString{String: "changed", Valid: true}})
		}
		return err
	}
	if err := c.add(ctx, record{item: it.entry, entry: e.id, kind: e.kind, path: e.path, size: e.size,
		id: identityOf(info), own: &info, verdict: verdictNoContent}); err != nil {
		return err
	}
	for _, m := range members {
		r := record{item: it.entry, entry: e.id, member: m.id, kind: domain.EntryFile, path: e.path, size: m.size}
		if m.size == 0 {
			r.verdict = verdictNoContent
		} else {
			sum := m.sum
			r.sum = &sum
			res, err := c.findCopy(ctx, sum, m.size)
			if err != nil {
				return err
			}
			switch {
			case res.copy != nil:
				r.verdict, r.copy = verdictSafe, res.copy
			case res.offline:
				r.verdict = verdictCopyOffline
			default:
				r.verdict, r.class = verdictUnique, c.memberClass(names[m.id], m.size)
			}
		}
		if err := c.add(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// memberNames returns the names of the archive's file members.
func (c *checker) memberNames(ctx context.Context, archive int64) (map[int64][]byte, error) {
	rows, err := c.q.QueryContext(ctx, `SELECT id, name FROM archive_members WHERE archive_id = ? AND kind = 'file'`,
		archive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]byte{}
	for rows.Next() {
		var id int64
		var name []byte
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// finish ends the check in one transaction (D7 step 4, I9): ready only
// while it is still running, every item is still present at its recorded
// quarantine path, and nothing a found copy relies on changed (G9);
// otherwise stale, through MarkStale for each item out of place.
func (c *checker) finish(ctx context.Context) error {
	return c.s.st.Write(ctx, func(tx *sql.Tx) error {
		now := clock.Millis(c.s.clk.Now())
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM purge_checks WHERE id = ?`, c.id).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if state != checkRunning {
			_, err := tx.ExecContext(ctx, `UPDATE purge_checks SET finished_at = ? WHERE id = ? AND finished_at IS NULL`,
				now, c.id)
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT i.path, e.path, e.source_id, e.state FROM purge_check_items i
			LEFT JOIN entries e ON e.id = i.entry_id WHERE i.check_id = ?`, c.id)
		if err != nil {
			return err
		}
		var (
			n        int
			misplace [][]byte
		)
		for rows.Next() {
			var at, path []byte
			var src, estate sql.NullString
			if err := rows.Scan(&at, &path, &src, &estate); err != nil {
				rows.Close()
				return err
			}
			n++
			if !src.Valid || domain.SourceID(src.String) != c.src || !bytes.Equal(at, path) || estate.String != "present" {
				misplace = append(misplace, at)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		changed, err := c.reliedChanged(ctx, tx)
		if err != nil {
			return err
		}
		if len(misplace) == 0 && n == len(c.items) && !changed {
			_, err := tx.ExecContext(ctx, `UPDATE purge_checks SET state = 'ready', finished_at = ?
				WHERE id = ? AND state = 'running'`, now, c.id)
			return err
		}
		for _, p := range misplace {
			if err := stale.MarkStale(ctx, tx, c.src, p); err != nil {
				return err
			}
		}
		// An item deleted from the index took its purge_check_items row
		// with it: no path is left to mark by. A copy decided again before
		// its record was written was not marked either.
		_, err = tx.ExecContext(ctx, `UPDATE purge_checks SET state = 'stale',
			stale_reason = coalesce(stale_reason, ?), finished_at = ? WHERE id = ? AND state IN ('running', 'stale')`,
			stale.ReasonIndexChanged, now, c.id)
		return err
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
