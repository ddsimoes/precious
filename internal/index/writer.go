package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"sync/atomic"

	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/store"
)

// queueBatches is how many full batches may wait for the writer (design D7):
// beyond them the walker blocks, so a slow database slows the walk instead of
// filling memory.
const queueBatches = 4

// opKind is what one row operation does.
type opKind uint8

const (
	// opInsert inserts a new entry under its parent and, for any entry but
	// the root, its entry_names row.
	opInsert opKind = iota
	// opUpdate rewrites a stored entry's row.
	opUpdate
	// opFinish writes a folder's final row and its dir_stats, unless they
	// equal the stored ones.
	opFinish
	// opMissing marks a stored entry, its stored subtree, or both missing.
	opMissing
	// opDelete deletes a stored entry and its subtree (a change of kind at its
	// path, design D6).
	opDelete
	// opPartial marks a folder partial: a cancelled scan did not finish it.
	opPartial
)

// span is a byte range of a batch's arena.
type span struct{ off, n uint32 }

// op is one row operation. Entries are named by ID when the walker knows it
// (a stored row) and otherwise by path, which the writer resolves to the ID
// it assigned (design D7).
type op struct {
	kind opKind
	// id is the target row, or the parent of an insert; 0 resolves path (or
	// parent) through the writer's map of new folders.
	id     domain.EntryID
	path   span
	parent span
	name   span
	link   span
	// hasLink distinguishes an empty link text from none.
	hasLink bool
	root    bool
	// dropContent, on an opUpdate, deletes the entry's file_content,
	// archives, and media_meta rows: its own facts changed, so a digest,
	// listing, or header read from the old file no longer applies (R2
	// design D4, r5 design D3). Its date_corrections row is the owner's and
	// stays (I4).
	dropContent bool
	// self and subtree select what opMissing and opDelete touch.
	self, subtree bool
	// token, when not 0, records the ID an insert assigns for the indicator
	// and inside lists of later folder finishes.
	token uint64
	row   row
	stats *dirStats
	// old is the stored row and stats an opFinish compares with; nil for a
	// new folder.
	old *stored
}

// dirStats is a folder's dir_stats as the walker computes them. The
// indicators and inside columns are built by the writer, which knows the IDs
// of entries the scan inserted.
type dirStats struct {
	cols   statsCols
	refs   []indicatorRef
	inside []insideRef
	// release lists tokens no later folder refers to.
	release []uint64
}

// indicatorRef is one example in a folder's indicators list: a stored entry
// by ID, or one the scan inserted by token.
type indicatorRef struct {
	id     domain.EntryID
	token  uint64
	path   []byte
	signal rules.SignalID
}

// batch is up to scan.batch_size operations committed in one transaction.
// The byte fields of its operations live in buf.
type batch struct {
	ops []op
	buf []byte
}

func (b *batch) reset() {
	clear(b.ops) // drop references to stats and stored rows
	b.ops = b.ops[:0]
	b.buf = b.buf[:0]
}

func (b *batch) add(p []byte) span {
	off := len(b.buf)
	b.buf = append(b.buf, p...)
	return span{uint32(off), uint32(len(p))}
}

// join adds the path of name inside the folder at dir.
func (b *batch) join(dir, name []byte) span {
	off := len(b.buf)
	if len(dir) > 0 {
		b.buf = append(b.buf, dir...)
		b.buf = append(b.buf, '/')
	}
	b.buf = append(b.buf, name...)
	return span{uint32(off), uint32(len(b.buf) - off)}
}

// bytes returns the span's bytes, never nil, so that an empty value binds as
// an empty BLOB.
func (b *batch) bytes(s span) []byte {
	if s.n == 0 {
		return []byte{}
	}
	return b.buf[s.off : s.off+s.n : s.off+s.n]
}

// deferForeignKeys defers the foreign key checks of the current transaction
// to its commit; SQLite turns it off again at the commit or rollback.
const deferForeignKeys = `PRAGMA defer_foreign_keys = ON`

// errLeaseLost ends a scan whose attempt no longer owns its job.
var errLeaseLost = errors.New("index: the scan's job is no longer this attempt's")

// writer commits a scan's operations in batches, on its own goroutine, so
// that walking and writing overlap (design D7).
type writer struct {
	st     *store.Store
	ctx    context.Context
	source domain.SourceID
	job    jobs.Job
	gen    int64
	now    int64
	// stopWalk cancels the walk when a batch fails.
	stopWalk context.CancelCauseFunc

	in   chan *batch
	free chan *batch
	dead chan struct{} // closed when a batch failed
	done chan struct{}
	err  error

	// folders maps the paths of folders the scan inserted to their IDs
	// until they finish; tokens maps indicator tokens to inserted IDs.
	folders map[string]domain.EntryID
	tokens  map[uint64]domain.EntryID

	stmts [nStmts]*sql.Stmt
	args  []any
	// names is the JSON array of the batch's FTS rows (addName).
	names []byte
	// inherit caches inheritFrom within one transaction.
	inherit map[domain.EntryID]inherited
	// lastID is the largest entry ID, as the transaction assigns them.
	lastID int64
	// pending holds the arguments of the queued rows; inserts[i] inserts
	// 1<<i rows.
	pending []any
	queued  int
	inserts [insertSizes]*sql.Stmt

	written atomic.Int64
	missing atomic.Int64
}

// Prepared statements, by index into writerQueries.
const (
	stCheck = iota
	stMaxID
	stInherit
	stUpdate
	stStats
	stNames
	stMissingSelf
	stMissingSub
	stDeleteSelf
	stDeleteSubNames
	stDeleteNames
	stPartial
	stDropContent
	stDropArchive
	stDropMedia
	nStmts
)

// writerQueries are the statements of the st* constants. Writes are OR FAIL
// and foreign keys are deferred to the commit (deferForeignKeys), because a
// failure rolls the whole batch back anyway: no statement then needs a
// statement journal, whose savepoint would also make FTS5 flush its pending
// rows.
var writerQueries = [nStmts]string{
	// check: the source row exists and the job is still this attempt's
	// (transaction boundaries, "Scan batch").
	`SELECT (SELECT count(*) FROM sources WHERE id = ?),
		(SELECT attempts + 1 FROM jobs WHERE id = ? AND state = 'running')`,
	`SELECT COALESCE(MAX(id), 0) FROM entries`,
	`SELECT eff_decision, eff_from FROM entries WHERE id = ?`,
	`UPDATE OR FAIL entries SET ` + rowSet + `, missing_since = NULL, last_seen = ?, scan_gen = ? WHERE id = ?`,
	`INSERT OR FAIL INTO dir_stats (entry_id, dirs, files, symlinks, specials, unreadable, mount_boundaries,
		by_kind, by_year, by_family, signals, indicators, inside) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id) DO UPDATE SET dirs = excluded.dirs, files = excluded.files,
		symlinks = excluded.symlinks, specials = excluded.specials, unreadable = excluded.unreadable,
		mount_boundaries = excluded.mount_boundaries, by_kind = excluded.by_kind,
		by_year = excluded.by_year, by_family = excluded.by_family, signals = excluded.signals,
		indicators = excluded.indicators, inside = excluded.inside`,
	// names: the batch's FTS rows in one statement, last in the transaction.
	// FTS5 flushes its pending rows to a new segment whenever a statement
	// opens a savepoint, as the entries statements do; inserting them last
	// writes one segment per batch instead of one per row.
	`INSERT INTO entry_names (rowid, name) SELECT j.value ->> 0, j.value ->> 1 FROM json_each(?) j`,
	`UPDATE OR FAIL entries SET state = 'missing', missing_since = ? WHERE id = ? AND state <> 'missing'`,
	`UPDATE OR FAIL entries SET state = 'missing', missing_since = ?
		WHERE source_id = ? AND path >= ? AND path < ? AND state <> 'missing'`,
	`DELETE FROM entries WHERE id = ?`,
	`DELETE FROM entry_names WHERE rowid IN
		(SELECT id FROM entries WHERE source_id = ? AND path >= ? AND path < ?)`,
	`DELETE FROM entry_names WHERE rowid = ?`,
	`UPDATE OR FAIL entries SET partial = 1 WHERE id = ? AND partial = 0`,
	`DELETE FROM file_content WHERE entry_id = ?`,
	`DELETE FROM archives WHERE entry_id = ?`,
	`DELETE FROM media_meta WHERE entry_id = ?`,
}

func newWriter(ctx context.Context, st *store.Store, job jobs.Job, gen, now int64, stopWalk context.CancelCauseFunc) (*writer, error) {
	w := &writer{
		st: st, ctx: context.WithoutCancel(ctx), source: job.SourceID, job: job, gen: gen, now: now,
		stopWalk: stopWalk,
		in:       make(chan *batch, queueBatches),
		free:     make(chan *batch, queueBatches+2),
		dead:     make(chan struct{}),
		done:     make(chan struct{}),
		folders:  map[string]domain.EntryID{},
		tokens:   map[uint64]domain.EntryID{},
		inherit:  map[domain.EntryID]inherited{},
	}
	for i, q := range writerQueries {
		stmt, err := st.Writer().PrepareContext(ctx, q)
		if err != nil {
			w.closeStmts()
			return nil, fmt.Errorf("index: prepare: %w", err)
		}
		w.stmts[i] = stmt
	}
	for i := range w.inserts {
		stmt, err := st.Writer().PrepareContext(ctx, insertQuery(1<<i))
		if err != nil {
			w.closeStmts()
			return nil, fmt.Errorf("index: prepare: %w", err)
		}
		w.inserts[i] = stmt
	}
	go w.run()
	return w, nil
}

func (w *writer) closeStmts() {
	for _, stmt := range w.stmts {
		if stmt != nil {
			stmt.Close()
		}
	}
	for _, stmt := range w.inserts {
		if stmt != nil {
			stmt.Close()
		}
	}
}

func (w *writer) run() {
	defer close(w.done)
	defer w.closeStmts()
	for b := range w.in {
		if w.err == nil {
			if err := w.commit(b); err != nil {
				w.err = err
				close(w.dead)
				w.stopWalk(err)
			}
		}
		b.reset()
		select {
		case w.free <- b:
		default:
		}
	}
}

// commit applies one batch in one write transaction.
func (w *writer) commit(b *batch) error {
	return w.st.Write(w.ctx, func(tx *sql.Tx) error {
		t := txStmts{tx: tx, ctx: w.ctx, w: w}
		if err := checkOwner(t.stmt(stCheck).QueryRowContext(w.ctx, string(w.source), int64(w.job.ID)), w); err != nil {
			return err
		}
		if _, err := tx.ExecContext(w.ctx, deferForeignKeys); err != nil {
			return err
		}
		if err := t.stmt(stMaxID).QueryRowContext(w.ctx).Scan(&w.lastID); err != nil {
			return err
		}
		w.names, w.pending, w.queued = w.names[:0], w.pending[:0], 0
		clear(w.inherit)
		// Deletes first: they free the paths of entries whose kind changed
		// for the inserts of new ones. Then the inserts, many rows per
		// statement, then everything that updates rows by then inserted.
		for _, phase := range [...]func(opKind) bool{
			func(k opKind) bool { return k == opDelete },
			func(k opKind) bool { return k == opInsert },
			func(k opKind) bool { return k != opDelete && k != opInsert },
		} {
			for i := range b.ops {
				if o := &b.ops[i]; phase(o.kind) {
					if err := w.apply(&t, b, o); err != nil {
						return err
					}
				}
			}
			if err := w.flushInserts(&t); err != nil {
				return err
			}
		}
		if len(w.names) == 0 {
			return nil
		}
		_, err := t.exec(stNames, string(append(w.names, ']')))
		return err
	})
}

// txStmts binds the prepared statements to one transaction on first use.
type txStmts struct {
	tx    *sql.Tx
	ctx   context.Context
	w     *writer
	bound [nStmts]*sql.Stmt
}

func (t *txStmts) stmt(i int) *sql.Stmt {
	if t.bound[i] == nil {
		t.bound[i] = t.tx.StmtContext(t.ctx, t.w.stmts[i])
	}
	return t.bound[i]
}

// checkOwner fails a transaction whose source row is gone or whose job is no
// longer this attempt's: a lost lease requeues the job with one more
// attempt, so the running attempt number differs.
func checkOwner(r *sql.Row, w *writer) error {
	var (
		sources int
		attempt sql.NullInt64
	)
	if err := r.Scan(&sources, &attempt); err != nil {
		return err
	}
	if sources == 0 {
		return domain.Errorf(domain.CodeUnknownSource, "source %q was removed during its scan", w.source)
	}
	if !attempt.Valid || attempt.Int64 != int64(w.job.Attempt) {
		return errLeaseLost
	}
	return nil
}

func (t *txStmts) exec(i int, args ...any) (sql.Result, error) {
	return t.stmt(i).ExecContext(t.ctx, args...)
}

// resolve returns the ID of an operation's target: its own, or the one the
// scan assigned to the folder at path.
func (w *writer) resolve(id domain.EntryID, b *batch, path span) (domain.EntryID, error) {
	if id != 0 {
		return id, nil
	}
	id, ok := w.folders[string(b.bytes(path))]
	if !ok {
		return 0, fmt.Errorf("index: no folder was inserted at %q", domain.DisplayName(b.bytes(path)))
	}
	return id, nil
}

func (w *writer) apply(t *txStmts, b *batch, o *op) error {
	switch o.kind {
	case opInsert:
		return w.insert(t, b, o)
	case opUpdate:
		w.args = o.row.args(w.args[:0], w.link(b, o))
		if _, err := t.exec(stUpdate, append(w.args, w.now, w.gen, int64(o.id))...); err != nil {
			return err
		}
		w.written.Add(1)
		if o.dropContent {
			if _, err := t.exec(stDropContent, int64(o.id)); err != nil {
				return err
			}
			if _, err := t.exec(stDropArchive, int64(o.id)); err != nil {
				return err
			}
			if _, err := t.exec(stDropMedia, int64(o.id)); err != nil {
				return err
			}
		}
		return nil
	case opFinish:
		return w.finish(t, b, o)
	case opMissing:
		if o.self {
			res, err := t.exec(stMissingSelf, w.now, int64(o.id))
			if err != nil {
				return err
			}
			w.count(res, &w.missing)
		}
		if o.subtree {
			lo, hi := subtree(b.bytes(o.path))
			res, err := t.exec(stMissingSub, w.now, string(w.source), lo, hi)
			if err != nil {
				return err
			}
			w.count(res, &w.missing)
		}
		return nil
	case opDelete:
		if o.subtree {
			lo, hi := subtree(b.bytes(o.path))
			if _, err := t.exec(stDeleteSubNames, string(w.source), lo, hi); err != nil {
				return err
			}
		}
		if _, err := t.exec(stDeleteNames, int64(o.id)); err != nil {
			return err
		}
		_, err := t.exec(stDeleteSelf, int64(o.id))
		return err
	case opPartial:
		id, err := w.resolve(o.id, b, o.path)
		if err != nil {
			return err
		}
		_, err = t.exec(stPartial, int64(id))
		return err
	}
	return fmt.Errorf("index: unknown operation %d", o.kind)
}

func (w *writer) link(b *batch, o *op) []byte {
	if !o.hasLink {
		return nil
	}
	return b.bytes(o.link)
}

func (w *writer) count(res sql.Result, into *atomic.Int64) {
	if n, err := res.RowsAffected(); err == nil {
		into.Add(n)
	}
}

// insert assigns a new entry the next ID and queues its row for a
// multi-row insert (flushInserts). The transaction holds the database's
// write lock, so the IDs above the largest one it read are free.
func (w *writer) insert(t *txStmts, b *batch, o *op) error {
	in := inherited{eff: string(domain.DecisionUndecided)}
	var parent any
	if !o.root {
		id, err := w.resolve(o.id, b, o.parent)
		if err != nil {
			return err
		}
		if in, err = w.inheritFrom(t, id); err != nil {
			return err
		}
		parent = int64(id)
	}
	w.lastID++
	id := domain.EntryID(w.lastID)
	w.pending = append(w.pending, w.lastID, string(w.source), parent, b.bytes(o.name), b.bytes(o.path))
	w.pending = o.row.args(w.pending, w.link(b, o))
	w.pending = append(w.pending, w.now, w.now, w.gen, in.eff, in.from.arg())
	w.queued++
	w.written.Add(1)
	if o.row.kind == string(domain.EntryDirectory) {
		w.folders[string(b.bytes(o.path))] = id
		// Its children inherit what it did; it has no own decision yet.
		w.inherit[id] = in
	}
	if o.token != 0 {
		w.tokens[o.token] = id
	}
	if !o.root {
		w.addName(w.lastID, b.bytes(o.name))
	}
	if w.queued == insertRows {
		return w.flushInserts(t)
	}
	return nil
}

// insertRows is the most rows one insert statement writes. The writer
// prepares one statement per power of two up to it, so any count is a few
// statements.
const (
	insertSizes = 8
	insertRows  = 1 << (insertSizes - 1)
)

// insertColumns are the columns of an inserted row, in pending's order.
const insertColumns = `id, source_id, parent_id, name, path, ` + rowColumns + `,
	first_seen, last_seen, scan_gen, eff_decision, eff_from`

const insertParams = `(?, ?, ?, ?, ?, ` + rowParams + `, ?, ?, ?, ?, ?)`

// insertQuery inserts rows rows.
func insertQuery(rows int) string {
	return `INSERT OR FAIL INTO entries (` + insertColumns + `) VALUES ` +
		strings.Repeat(insertParams+`, `, rows-1) + insertParams
}

// flushInserts inserts the queued rows, the largest power of two of them at
// a time.
func (w *writer) flushInserts(t *txStmts) error {
	args := w.pending
	for n := w.queued; n > 0; {
		i := bits.Len(uint(n)) - 1
		rows := 1 << i
		k := len(args) / n * rows
		if _, err := t.tx.StmtContext(t.ctx, w.inserts[i]).ExecContext(t.ctx, args[:k]...); err != nil {
			return err
		}
		args, n = args[k:], n-rows
	}
	clear(w.pending)
	w.pending, w.queued = w.pending[:0], 0
	return nil
}

// inherited is the effective decision a new child of a folder takes.
type inherited struct {
	eff  string
	from opt
}

// inheritFrom returns what a new child of the folder parent inherits: the
// folder's effective decision and its origin as this transaction sees them
// (design D10), read once per folder and batch.
func (w *writer) inheritFrom(t *txStmts, parent domain.EntryID) (inherited, error) {
	if in, ok := w.inherit[parent]; ok {
		return in, nil
	}
	var in inherited
	err := t.stmt(stInherit).QueryRowContext(t.ctx, int64(parent)).Scan(&in.eff, &in.from)
	if errors.Is(err, sql.ErrNoRows) {
		return inherited{}, fmt.Errorf("index: folder %d's row is gone", parent)
	}
	if err != nil {
		return inherited{}, err
	}
	w.inherit[parent] = in
	return in, nil
}

// addName queues the FTS row of an inserted entry: its display name under
// its ID, as one element of the JSON array the names statement reads.
func (w *writer) addName(id int64, name []byte) {
	if len(w.names) == 0 {
		w.names = append(w.names, '[')
	} else {
		w.names = append(w.names, ',')
	}
	w.names = append(w.names, '[')
	w.names = strconv.AppendInt(w.names, id, 10)
	w.names = append(w.names, ',', '"')
	for _, c := range []byte(domain.DisplayName(name)) {
		switch {
		case c == '"' || c == '\\':
			w.names = append(w.names, '\\', c)
		case c < 0x20:
			w.names = append(w.names, `\u00`...)
			w.names = append(w.names, hex[c>>4], hex[c&15])
		default:
			w.names = append(w.names, c)
		}
	}
	w.names = append(w.names, '"', ']')
}

const hex = "0123456789abcdef"

// finish writes a folder's final row and dir_stats, unless both equal the
// stored ones, and forgets what no later operation refers to.
func (w *writer) finish(t *txStmts, b *batch, o *op) error {
	id, err := w.resolve(o.id, b, o.path)
	if err != nil {
		return err
	}
	if o.id == 0 {
		delete(w.folders, string(b.bytes(o.path)))
	}
	cols := o.stats.cols
	cols.indicators, err = encodeIndicators(o.stats.refs, w.tokens)
	if err != nil {
		return err
	}
	cols.inside, err = encodeInside(o.stats.inside, w.tokens)
	if err != nil {
		return err
	}
	for _, tok := range o.stats.release {
		delete(w.tokens, tok)
	}
	if o.old != nil && o.old.hasStats && o.old.row == o.row && o.old.stats == cols {
		return nil
	}
	if o.old == nil || o.old.row != o.row {
		w.args = o.row.args(w.args[:0], nil)
		if _, err := t.exec(stUpdate, append(w.args, w.now, w.gen, int64(id))...); err != nil {
			return err
		}
		w.written.Add(1)
	}
	_, err = t.exec(stStats, int64(id), cols.dirs, cols.files, cols.symlinks, cols.specials,
		cols.unreadable, cols.mounts, cols.byKind, cols.byYear, cols.byFamily, cols.signals, cols.indicators,
		cols.inside)
	return err
}

// resolveToken returns the ID the scan assigned to the entry inserted with
// token, among ids.
func resolveToken(ids map[uint64]domain.EntryID, token uint64, path []byte) (domain.EntryID, error) {
	id, ok := ids[token]
	if !ok {
		return 0, fmt.Errorf("index: %q was listed but never inserted", domain.DisplayName(path))
	}
	return id, nil
}

// indicatorJSON is one element of dir_stats.indicators.
type indicatorJSON struct {
	EntryID string `json:"entry_id"`
	PathB64 []byte `json:"path_b64"`
	Path    string `json:"path"`
	Signal  string `json:"signal"`
}

// encodeIndicators renders an indicators list, resolving the tokens of the
// entries a scan inserted through ids.
func encodeIndicators(refs []indicatorRef, ids map[uint64]domain.EntryID) (string, error) {
	if len(refs) == 0 {
		return "[]", nil
	}
	out := make([]indicatorJSON, len(refs))
	for i, r := range refs {
		id := r.id
		if r.token != 0 {
			var err error
			if id, err = resolveToken(ids, r.token, r.path); err != nil {
				return "", err
			}
		}
		out[i] = indicatorJSON{EntryID: id.String(), PathB64: r.path, Path: displayPath(r.path), Signal: string(r.signal)}
	}
	j, err := json.Marshal(out)
	return string(j), err
}

// insideJSON is one element of dir_stats.inside (design D21). Category and
// family are null when the entry has none.
type insideJSON struct {
	EntryID  string  `json:"entry_id"`
	PathB64  []byte  `json:"path_b64"`
	Path     string  `json:"path"`
	Category *string `json:"category"`
	Family   *string `json:"family"`
	Group    bool    `json:"group"`
	Bytes    int64   `json:"bytes"`
	Files    int64   `json:"files"`
}

// encodeInside renders an inside list, resolving the tokens of the entries a
// scan inserted through ids.
func encodeInside(refs []insideRef, ids map[uint64]domain.EntryID) (string, error) {
	if len(refs) == 0 {
		return "[]", nil
	}
	out := make([]insideJSON, len(refs))
	for i := range refs {
		r := &refs[i]
		id := r.id
		if r.token != 0 {
			var err error
			if id, err = resolveToken(ids, r.token, r.path); err != nil {
				return "", err
			}
		}
		out[i] = insideJSON{EntryID: id.String(), PathB64: r.path, Path: displayPath(r.path),
			Category: nullable(r.category), Family: nullable(r.family), Group: r.group, Bytes: r.bytes, Files: r.files}
	}
	j, err := json.Marshal(out)
	return string(j), err
}

func nullable(t text) *string {
	if t == "" {
		return nil
	}
	s := string(t)
	return &s
}

// displayPath renders a raw '/'-joined path component by component.
func displayPath(p []byte) string {
	var sb strings.Builder
	for len(p) > 0 {
		i := 0
		for i < len(p) && p[i] != '/' {
			i++
		}
		sb.WriteString(domain.DisplayName(p[:i]))
		if i < len(p) {
			sb.WriteByte('/')
			i++
		}
		p = p[i:]
	}
	return sb.String()
}

// subtree is the half-open range of the paths strictly below the folder at
// the non-empty path p.
func subtree(p []byte) (lo, hi []byte) {
	lo = append(append(make([]byte, 0, len(p)+1), p...), '/')
	hi = append(append(make([]byte, 0, len(p)+1), p...), '0')
	return lo, hi
}

// finishScan records a complete scan on its source (design D7, transaction
// boundaries "Scan finish"), if this attempt still owns its job. It clears
// the source's rescan_requested flag and reports whether it was set: an
// owner override arrived while this scan ran, which may have read the
// overrides before it (r2b design D3).
func (w *writer) finishScan(ctx context.Context, rulesVersion string) (again bool, err error) {
	err = w.st.Write(ctx, func(tx *sql.Tx) error {
		if err := checkOwner(tx.QueryRowContext(ctx, writerQueries[stCheck], string(w.source), int64(w.job.ID)), w); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT rescan_requested FROM sources WHERE id = ?`,
			string(w.source)).Scan(&again); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sources SET scan_gen = ?, last_scan_at = ?, last_scan_job = ?,
			rules_version = ?, rescan_requested = 0 WHERE id = ?`, w.gen, w.now, int64(w.job.ID), rulesVersion, string(w.source))
		return err
	})
	return again, err
}
