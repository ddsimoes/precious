package executor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"precious/internal/clock"
	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/store"
)

// A cleanup item (r4 D3) is three steps of a cleanup action: the mkdir of
// its <seq> folder in the plan folder, the rename of its entry into <seq>
// (whose to_dir_seq names that mkdir), and the record <seq>.json beside it
// (whose entry_id is the rename's). Plan-level mkdirs, of the quarantine
// and of <plan>, have no rename into them.

// insideQuarantine reports whether path lies strictly below its source's
// quarantine folder: the only folders where the executor creates or unlinks
// a file (r4 D12).
func insideQuarantine(path []byte) bool {
	return index.IsQuarantinePath(path) && string(path) != index.QuarantineName
}

// errOutsideQuarantine refuses CreateExclusive and Unlink through a folder
// outside the quarantine (r4 D12).
var errOutsideQuarantine = errors.New("executor: a file is created or unlinked only inside the quarantine")

// quarantineWriter is the write surface of f for CreateExclusive and
// Unlink: only a folder whose index path lies inside the quarantine has one
// (r4 D12, checked again in the step after its intent).
func quarantineWriter(f *folder) (fsaccess.Writer, error) {
	if !insideQuarantine(f.path) {
		return nil, errOutsideQuarantine
	}
	return writer(f.dir)
}

// quarantineFolder returns the source's quarantine folder when Precious made
// it (sources.quarantine_entry_id) and it is a present folder at the
// reserved name.
func quarantineFolder(ctx context.Context, q *sql.Tx, src domain.SourceID) (int64, bool, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT e.id FROM sources s JOIN entries e ON e.id = s.quarantine_entry_id
		WHERE s.id = ? AND e.source_id = s.id AND e.state = 'present' AND e.kind = 'directory' AND e.path = ?`,
		string(src), []byte(index.QuarantineName)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// preflight is what a cleanup item's preflight read before its intent,
// outside any transaction (r4 D3, D5).
type preflight struct {
	// first is set for the mkdir of <seq>, whose intent re-checks the whole
	// item and ends rest with it on a failure.
	first bool
	ren   item
	rest  []int64
	// seen and info are the lstat of the entry before the first step.
	seen bool
	info fsaccess.EntryInfo
	// copies are the verified copies of a duplicate-ground item.
	copies *copyCheck
}

func (p *preflight) group() []int64 { return p.rest }

// preflightCleanup reads what a cleanup item's intent re-checks: before its
// mkdir of <seq>, the entry's lstat; before that mkdir and before its
// rename, for a duplicate ground, one staying copy of each file read in full
// (once per attempt). It returns nil for every other item.
func (r *run) preflightCleanup(it item) (*preflight, error) {
	if r.kind != kindCleanup {
		return nil, nil
	}
	switch it.op {
	case opMkdir:
		ren, err := scanItem(r.e.st.Reader().QueryRowContext(r.ctx, `SELECT `+itemColumns+` FROM action_items i
			WHERE i.action_id = ? AND i.op = 'rename' AND i.to_dir_seq = ? AND i.state = 'planned'
			ORDER BY i.seq LIMIT 1`, r.action, it.seq))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		p := &preflight{first: true, ren: ren, rest: []int64{ren.id}}
		rows, err := r.e.st.Reader().QueryContext(r.ctx, `SELECT id FROM action_items WHERE action_id = ? AND op = 'record'
			AND entry_id = ? AND state = 'planned'`, r.action, ren.entry)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			p.rest = append(p.rest, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if f, err := r.openFolder(ren.fromParent); err == nil {
			p.info, err = r.lstat(f.dir, ren.fromName)
			p.seen = err == nil
			f.close()
		} else if !isDisk(err) {
			return nil, err
		}
		if r.ground == groundDuplicate {
			if p.copies, err = r.verifiedCopies(ren); err != nil {
				return nil, err
			}
		}
		return p, nil
	case opRename:
		if r.ground != groundDuplicate {
			return nil, nil
		}
		c, err := r.verifiedCopies(it)
		if err != nil {
			return nil, err
		}
		return &preflight{ren: it, copies: c}, nil
	}
	return nil, nil
}

// firstCheck re-checks a whole cleanup item at the intent of its first step
// (r4 D3): the entry where it was drafted, its index row and its lstat
// matching the draft, then cleanupRechecks.
func (r *run) firstCheck(ctx context.Context, q *sql.Tx, pre *preflight) (*end, error) {
	ren := pre.ren
	ent, ok, err := loadEntry(ctx, q, ren.entry)
	if err != nil {
		return nil, err
	}
	if !ok || domain.SourceID(ent.source) != r.src || ent.state != "present" || ent.parent != ren.fromParent ||
		!bytes.Equal(ent.name, ren.fromName) {
		return &end{state: stateChanged, reason: reasonIdentityChanged}, nil
	}
	if index.IsQuarantinePath(ent.path) {
		return &end{state: stateRefused, reason: reasonInQuarantine}, nil
	}
	if !pre.seen || !r.draftOnDisk(ren, pre.info) {
		return &end{state: stateChanged, reason: reasonIdentityChanged}, nil
	}
	return r.cleanupRechecks(ctx, q, ren, ent, pre.copies)
}

// cleanupRechecks are the tests both intents of a cleanup item run (r4 D3,
// D5): the index row matches the draft, the entry's own decision is still
// discard, nothing in its inclusive subtree is effectively kept, and, for a
// duplicate ground, each verified copy still holds.
func (r *run) cleanupRechecks(ctx context.Context, q *sql.Tx, ren item, ent entryRow, copies *copyCheck) (*end, error) {
	if !draftInIndex(ren, ent) {
		return &end{state: stateChanged, reason: reasonIdentityChanged}, nil
	}
	if !ent.decision.Valid || ent.decision.String != "discard" {
		return &end{state: stateChanged, reason: reasonDecisionChanged}, nil
	}
	kept, err := keptWithin(ctx, q, r.src, ent.path)
	if err != nil {
		return nil, err
	}
	if kept {
		return &end{state: stateBlocked, reason: reasonHoldsKept}, nil
	}
	if r.ground == groundDuplicate {
		ok, err := r.copiesHold(ctx, q, ren, copies)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &end{state: stateChanged, reason: reasonNoVerifiedCopy}, nil
		}
	}
	return nil, nil
}

// keptWithin reports an entry of src at or below path whose effective
// decision is keep (I5), in any state.
func keptWithin(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	lo, hi := below(path)
	var kept bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE source_id = ? AND eff_decision = 'keep'
		AND (path = ? OR (path >= ? AND path < ?)))`, string(src), path, lo, hi).Scan(&kept)
	return kept, err
}

// below returns the half-open range [path/, path0) of the paths below path.
func below(path []byte) (lo, hi []byte) {
	lo = append(append(make([]byte, 0, len(path)+1), path...), '/')
	hi = append(append(make([]byte, 0, len(path)+1), path...), '0')
	return lo, hi
}

// atOrBelow reports whether path is p or lies below it.
func atOrBelow(path, p []byte) bool {
	return bytes.Equal(path, p) || (len(path) > len(p) && path[len(p)] == '/' && bytes.HasPrefix(path, p))
}

// draftInIndex compares a cleanup rename's draft-time identity with the
// entry's index row: every draft column that is set must hold the row's
// value, and the kind always.
func draftInIndex(ren item, ent entryRow) bool {
	same := func(draft, row sql.NullInt64) bool { return !draft.Valid || (row.Valid && draft.Int64 == row.Int64) }
	if ren.kind != ent.kind || !same(ren.dev, ent.dev) || !same(ren.ino, ent.ino) ||
		!same(ren.mtime, ent.mtime) || !same(ren.ctime, ent.ctime) {
		return false
	}
	if ent.kind == "directory" {
		return (!ren.draftBytes.Valid || ren.draftBytes.Int64 == ent.totalBytes) &&
			(!ren.draftFiles.Valid || ren.draftFiles.Int64 == ent.totalFiles)
	}
	return ren.size.Valid && ren.size.Int64 == ent.size
}

// draftOnDisk compares an lstat with a cleanup rename's draft-time identity
// under the source's capabilities: the kind; device and inode where identity
// is stable; for anything but a folder, the size; the modification time
// within the resolution, and the change time likewise when both are known
// (a folder's times change when its entries do).
func (r *run) draftOnDisk(ren item, info fsaccess.EntryInfo) bool {
	if info.MountBoundary || kindColumn(info.Kind) != ren.kind {
		return false
	}
	if r.caps.StableIdentity &&
		(!ren.dev.Valid || !ren.ino.Valid || uint64(ren.dev.Int64) != info.Dev || uint64(ren.ino.Int64) != info.Ino) {
		return false
	}
	if ren.kind != "directory" && (!ren.size.Valid || ren.size.Int64 != info.Size) {
		return false
	}
	if ren.kind != "directory" && !ren.mtime.Valid {
		return false
	}
	if ren.mtime.Valid && !sameTime(ren.mtime.Int64, info.ModTime.UnixNano(), r.caps) {
		return false
	}
	return !ren.ctime.Valid || ren.ctime.Int64 == 0 || info.Ctime.IsZero() ||
		sameTime(ren.ctime.Int64, info.Ctime.UnixNano(), r.caps)
}

// copyRef is one staying copy read in full before an item's intent (r4 D5):
// an entry, or a member of the complete archive entry, with the index
// identity the read matched.
type copyRef struct {
	source            domain.SourceID
	entry, member     int64
	content           int64
	path              []byte
	size              int64
	mtime, ctime, ino sql.NullInt64
}

// copyCheck is the verification of a duplicate-ground item: missing when a
// file below it has no copy that was read and matched.
type copyCheck struct {
	missing bool
	copies  []copyRef
}

// verifiedCopies returns the item's verified copies, read once per attempt.
func (r *run) verifiedCopies(ren item) (*copyCheck, error) {
	if c, ok := r.copies[ren.id]; ok {
		return c, nil
	}
	c, err := r.findCopies(ren)
	if err != nil {
		return nil, err
	}
	if r.copies == nil {
		r.copies = map[int64]*copyCheck{}
	}
	r.copies[ren.id] = c
	return c, nil
}

// findCopies reads in full, for each content of the files at or below the
// item's entry, one copy that stays: present, outside the quarantine,
// outside this plan, not being quarantined by another plan, on an online
// source, and identical to the digest the index holds (r4 D5, D9). A file
// that is not hashed, or whose content has no such copy, makes the item
// missing a copy. Empty files hold no content and need none. It reads
// outside every transaction.
func (r *run) findCopies(ren item) (*copyCheck, error) {
	ctx, rd := r.ctx, r.e.st.Reader()
	var path []byte
	err := rd.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ? AND source_id = ? AND state = 'present'`,
		ren.entry, string(r.src)).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return &copyCheck{missing: true}, nil
	}
	if err != nil {
		return nil, err
	}
	lo, hi := below(path)
	rows, err := rd.QueryContext(ctx, `SELECT e.size, fc.state, fc.content_id, c.sha256 FROM entries e
		LEFT JOIN file_content fc ON fc.entry_id = e.id LEFT JOIN contents c ON c.id = fc.content_id
		WHERE e.source_id = ? AND e.kind = 'file' AND e.state <> 'missing' AND (e.path = ? OR (e.path >= ? AND e.path < ?))`,
		string(r.src), path, lo, hi)
	if err != nil {
		return nil, err
	}
	sums := map[int64][]byte{}
	missing := false
	for rows.Next() {
		var (
			size    int64
			state   sql.NullString
			content sql.NullInt64
			sum     []byte
		)
		if err := rows.Scan(&size, &state, &content, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case size == 0:
		case state.String != "hashed" || !content.Valid || len(sum) != 32:
			missing = true
		default:
			sums[content.Int64] = sum
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if missing {
		return &copyCheck{missing: true}, nil
	}
	ids := make([]int64, 0, len(sums))
	for id := range sums {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	roots := &copyRoots{r: r, open: map[domain.SourceID]*sourceRoot{}}
	defer roots.close()
	out := &copyCheck{}
	for _, id := range ids {
		c, ok, err := r.readCopy(roots, id, sums[id])
		if err != nil {
			return nil, err
		}
		if !ok {
			return &copyCheck{missing: true}, nil
		}
		out.copies = append(out.copies, c)
	}
	return out, nil
}

// sourceRoot is a source opened to read a copy; ok is false when it could
// not be opened (offline).
type sourceRoot struct {
	root fsaccess.Dir
	caps fsaccess.Capabilities
	own  bool
	ok   bool
}

// copyRoots opens each source of a copy once.
type copyRoots struct {
	r    *run
	open map[domain.SourceID]*sourceRoot
}

func (c *copyRoots) get(ctx context.Context, src domain.SourceID) *sourceRoot {
	if s, ok := c.open[src]; ok {
		return s
	}
	s := &sourceRoot{}
	if src == c.r.src {
		s.root, s.caps, s.own, s.ok = c.r.root, c.r.caps, true, true
	} else if o, err := c.r.e.src.Open(ctx, src); err == nil {
		s.root, s.caps, s.ok = o.Root, o.Source.Caps, true
	}
	c.open[src] = s
	return s
}

func (c *copyRoots) close() {
	for _, s := range c.open {
		if s.ok && !s.own {
			s.root.Close()
		}
	}
}

// readCopy finds and reads one staying copy of the content id: entries
// first, own source first, then members of complete archives (through
// Options.Content).
func (r *run) readCopy(roots *copyRoots, id int64, want []byte) (copyRef, bool, error) {
	ctx, rd := r.ctx, r.e.st.Reader()
	cands, err := copyCandidates(ctx, rd, `SELECT e.id, 0, e.source_id, e.path, e.size, e.mtime_ns, e.ctime_ns, e.ino
		FROM file_content fc JOIN entries e ON e.id = fc.entry_id
		WHERE fc.content_id = ?1 AND fc.state = 'hashed' AND e.state = 'present' AND e.kind = 'file'
			AND `+index.NotQuarantined("e")+`
		ORDER BY e.source_id <> ?2, e.id`, id, string(r.src))
	if err != nil {
		return copyRef{}, false, err
	}
	if r.e.content != nil {
		members, err := copyCandidates(ctx, rd, `SELECT e.id, m.id, e.source_id, e.path, e.size, e.mtime_ns, e.ctime_ns,
				e.ino
			FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = m.archive_id
			WHERE m.content_id = ?1 AND m.kind = 'file' AND m.state = 'hashed' AND a.state = 'complete'
				AND e.state = 'present' AND `+index.NotQuarantined("e")+`
			ORDER BY e.source_id <> ?2, m.id`, id, string(r.src))
		if err != nil {
			return copyRef{}, false, err
		}
		cands = append(cands, members...)
	}
	for _, c := range cands {
		c.content = id
		stays, err := r.copyStays(ctx, rd, c)
		if err != nil {
			return copyRef{}, false, err
		}
		if !stays {
			continue
		}
		s := roots.get(ctx, c.source)
		if !s.ok {
			continue
		}
		var sum [32]byte
		if c.member != 0 {
			sum, _, err = r.e.content.HashMember(ctx, rd, domain.Ref{Entry: domain.EntryID(c.entry),
				Member: domain.MemberID(c.member)})
		} else {
			sum, _, err = content.HashEntry(ctx, s.root, content.Row{Path: c.path, Size: c.size, MtimeNs: c.mtime,
				CtimeNs: c.ctime, Ino: c.ino}, s.caps)
		}
		if ctx.Err() != nil {
			return copyRef{}, false, ctx.Err()
		}
		if err != nil {
			r.e.log.Info("executor: a copy could not be verified", "source", c.source, "entry", c.entry,
				"member", c.member, "err", err)
			continue
		}
		if bytes.Equal(sum[:], want) {
			return c, true, nil
		}
	}
	return copyRef{}, false, nil
}

func copyCandidates(ctx context.Context, q store.Queryer, query string, args ...any) ([]copyRef, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []copyRef
	for rows.Next() {
		var (
			c   copyRef
			src string
		)
		if err := rows.Scan(&c.entry, &c.member, &src, &c.path, &c.size, &c.mtime, &c.ctime, &c.ino); err != nil {
			return nil, err
		}
		c.source = domain.SourceID(src)
		out = append(out, c)
	}
	return out, rows.Err()
}

// copyStays reports whether the copy's file is outside the quarantine,
// outside this plan (a planned or intent rename of the action at or above
// it), and not at or below the entry of a cleanup rename in state intent, on
// any source (r4 D5).
func (r *run) copyStays(ctx context.Context, q store.Queryer, c copyRef) (bool, error) {
	if index.IsQuarantinePath(c.path) {
		return false, nil
	}
	anc := ancestors(c.path)
	args := make([]any, 0, len(anc)+2)
	args = append(args, string(c.source), r.action)
	marks := make([]byte, 0, 2*len(anc))
	for i, p := range anc {
		if i > 0 {
			marks = append(marks, ',')
		}
		marks = append(marks, '?')
		args = append(args, p)
	}
	var inPlan bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries e JOIN action_items i ON i.entry_id = e.id
		WHERE e.source_id = ? AND i.action_id = ? AND i.op = 'rename' AND i.state IN ('planned', 'intent')
			AND e.path IN (`+string(marks)+`))`, args...).Scan(&inPlan); err != nil {
		return false, err
	}
	if inPlan {
		return false, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT a.source_id, i.from_path FROM action_items i JOIN actions a ON a.id = i.action_id
		WHERE i.state = 'intent' AND i.op = 'rename' AND a.kind = 'cleanup'`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			src  string
			from []byte
		)
		if err := rows.Scan(&src, &from); err != nil {
			return false, err
		}
		if domain.SourceID(src) == c.source && from != nil && atOrBelow(c.path, from) {
			return false, nil
		}
	}
	return true, rows.Err()
}

// ancestors returns path and every folder path above it, the top excluded.
func ancestors(path []byte) [][]byte {
	out := [][]byte{path}
	for i := len(path) - 1; i > 0; i-- {
		if path[i] == '/' {
			out = append(out, path[:i])
		}
	}
	return out
}

// copiesHold re-checks, inside the intent's transaction, every copy the
// preflight verified (r4 D5): its index row has the identity the read
// matched, a member still has its content in a complete archive, and the
// copy stays (copyStays).
func (r *run) copiesHold(ctx context.Context, q *sql.Tx, ren item, c *copyCheck) (bool, error) {
	if c == nil || c.missing {
		return false, nil
	}
	for _, cp := range c.copies {
		var (
			src, state, kind  string
			path              []byte
			size              int64
			mtime, ctime, ino sql.NullInt64
		)
		err := q.QueryRowContext(ctx, `SELECT source_id, path, state, kind, size, mtime_ns, ctime_ns, ino FROM entries
			WHERE id = ?`, cp.entry).Scan(&src, &path, &state, &kind, &size, &mtime, &ctime, &ino)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if domain.SourceID(src) != cp.source || state != "present" || kind != "file" || !bytes.Equal(path, cp.path) ||
			size != cp.size || mtime != cp.mtime || ctime != cp.ctime || ino != cp.ino {
			return false, nil
		}
		if cp.member != 0 {
			var held bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM archive_members m
				JOIN archives a ON a.entry_id = m.archive_id
				WHERE m.id = ? AND m.archive_id = ? AND m.content_id = ? AND a.state = 'complete')`,
				cp.member, cp.entry, cp.content).Scan(&held); err != nil {
				return false, err
			}
			if !held {
				return false, nil
			}
		} else {
			var held bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM file_content WHERE entry_id = ?
				AND state = 'hashed' AND content_id = ?)`, cp.entry, cp.content).Scan(&held); err != nil {
				return false, err
			}
			if !held {
				return false, nil
			}
		}
		stays, err := r.copyStays(ctx, q, cp)
		if err != nil || !stays {
			return false, err
		}
	}
	return true, nil
}

// originRecord is the record <seq>.json beside a quarantined item (r4 D3):
// the source, the original path in display form and as raw bytes (base64),
// the entry, the plan, and the time its move was recorded. IDs are strings,
// as in the API.
type originRecord struct {
	Version       int        `json:"version"`
	SourceID      string     `json:"source_id"`
	EntryID       string     `json:"entry_id"`
	PlanID        string     `json:"plan_id"`
	Original      originPath `json:"original"`
	QuarantinedAt string     `json:"quarantined_at"`
}

type originPath struct {
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
}

// maxRecordBytes bounds what is read of a file taken for an origin record.
const maxRecordBytes = 64 << 10

// recordData returns the bytes of a record item: built from its cleanup
// rename (the action's rename of the record's entry), which must be done.
// They are the same at every call, so reconciliation recognizes them.
func (r *run) recordData(ctx context.Context, q store.Queryer, it item) ([]byte, bool, error) {
	var (
		state      string
		from       []byte
		finishedAt sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT state, from_path, finished_at FROM action_items WHERE action_id = ?
		AND op = 'rename' AND entry_id = ? ORDER BY seq LIMIT 1`, it.action, it.entry).Scan(&state, &from, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if state != stateDone || from == nil || !finishedAt.Valid {
		return nil, false, nil
	}
	rec := originRecord{Version: 1, SourceID: string(r.src), EntryID: domain.EntryID(it.entry).String(),
		PlanID:        domain.EntryID(it.action).String(),
		Original:      originPath{Path: domain.DisplayName(from), PathB64: from},
		QuarantinedAt: clock.FromMillis(finishedAt.Int64).Format(time.RFC3339Nano)}
	data, err := marshalRecord(rec)
	return data, err == nil, err
}

// intentRecord re-checks the record of a cleanup item: its rename is done
// (else it ends not_attempted with the item), and the plan folder is a
// present folder inside the quarantine.
func (r *run) intentRecord(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	if _, ok, err := r.recordData(ctx, q, it); err != nil || !ok {
		return it, &end{state: stateNotAttempted}, err
	}
	dest, ok, err := r.destination(ctx, q, it)
	if err != nil || !ok {
		return it, &end{state: stateChanged}, err
	}
	if !insideQuarantine(dest.path) {
		return it, &end{state: stateFailed, detail: errOutsideQuarantine.Error()}, nil
	}
	rec := it
	rec.toParent, rec.toPath, rec.kind = dest.id, childPath(dest.path, it.toName), "file"
	rec.dev, rec.ino, rec.size, rec.mtime = sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
	return rec, nil, nil
}

// stepRecord creates the record exclusively (never replacing a name: a
// taken one ends the item conflict), syncs its folder, and confirms it.
func (r *run) stepRecord(it item) (verdict, error) {
	data, ok, err := r.recordData(r.bg, r.e.st.Reader(), it)
	if err != nil {
		return halt, err
	}
	if !ok {
		return r.record(it, end{state: stateNotAttempted})
	}
	f, err := r.openFolder(it.toParent)
	if err != nil {
		return r.preflight(it, err)
	}
	defer f.close()
	w, err := quarantineWriter(f)
	if err != nil {
		return r.record(it, end{state: stateFailed, detail: err.Error()})
	}
	if err := r.before(it); err != nil {
		return halt, err
	}
	done := r.rt.FSCall("create")
	err = w.CreateExclusive(it.toName, data)
	done()
	if err := r.after(it); err != nil {
		return halt, err
	}
	if err != nil {
		return r.failed(it, err)
	}
	if v, err, ok := r.syncAll(f); !ok {
		return v, err
	}
	return r.settle(it, settleConfirm, end{state: stateFailed, detail: "the origin record reported success, but is not there"})
}

// settleRecord decides a record item by its name (r4 D4): absent, it did
// not happen (it runs once); a regular file with exactly the record's bytes,
// it is done; anything else is manual_recovery.
func (r *run) settleRecord(it item, mode settleMode, notDone end) (verdict, error) {
	data, ok, err := r.recordData(r.bg, r.e.st.Reader(), it)
	if err != nil {
		return halt, err
	}
	f, found, err := r.openToLook(it.toParent)
	if err != nil {
		return halt, err
	}
	if f != nil {
		defer f.close()
	}
	if found == foundAbsent {
		return r.notDone(it, mode, notDone)
	}
	if found == "" {
		info, err := r.lstat(f.dir, it.toName)
		switch o, _ := fsaccess.OutcomeOf(err); {
		case err != nil && o == domain.OutcomeAbsent:
			return r.notDone(it, mode, notDone)
		case err != nil:
			return halt, err
		}
		found = foundOther
		if ok {
			held, err := r.holds(f.dir, it.toName, info, data)
			if err != nil && !isDisk(err) {
				return halt, err
			}
			if held {
				found = foundSame
			}
		}
	}
	if found != foundSame {
		return r.record(it, end{state: stateManualRecovery, detail: findings(foundAbsent, found),
			stop: mode != settleReconcile})
	}
	if mode == settleReconcile {
		if err := r.sync(f); err != nil {
			return halt, err
		}
	}
	return r.outcome(it, noIndexChange, findings(foundAbsent, foundSame))
}

// openToLook opens the folder id to look at a name in it. A folder that is
// gone makes the name absent, one that changed makes it other (found is set
// and f is nil then); an empty found means f is open.
func (r *run) openToLook(id int64) (f *folder, found string, err error) {
	f, err = r.openFolder(id)
	if err == nil {
		return f, "", nil
	}
	switch {
	case errors.Is(err, errChanged), errors.Is(err, fsaccess.ErrMountBoundary), errors.Is(err, fsaccess.ErrNotDirectory):
		return nil, foundOther, nil
	}
	switch o, _ := fsaccess.OutcomeOf(err); o {
	case domain.OutcomeAbsent:
		return nil, foundAbsent, nil
	case domain.OutcomeChangedDuringObservation:
		return nil, foundOther, nil
	}
	return nil, "", err
}

// holds reports whether name in d is a regular file holding exactly data.
func (r *run) holds(d fsaccess.Dir, name []byte, info fsaccess.EntryInfo, data []byte) (bool, error) {
	if info.Kind != domain.EntryFile || info.MountBoundary || info.Size != int64(len(data)) {
		return false, nil
	}
	got, err := r.readSmall(d, name, info)
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, data), nil
}

// readSmall reads a small regular file in full, through OpenFile against
// its lstat.
func (r *run) readSmall(d fsaccess.Dir, name []byte, info fsaccess.EntryInfo) ([]byte, error) {
	if info.Size > maxRecordBytes {
		return nil, nil
	}
	done := r.rt.FSCall("open_file")
	f, err := d.OpenFile(name, info)
	done()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, info.Size)
	for off := 0; off < len(buf); {
		n, err := f.ReadAt(buf[off:], int64(off))
		off += n
		if err != nil && off < len(buf) {
			return nil, err
		}
		if n == 0 && off < len(buf) {
			return nil, errChanged
		}
	}
	return buf, nil
}

// readRecord reads name in d as an origin record of this source: a regular
// file, not too large, that parses as one. ok is false for anything else.
func (r *run) readRecord(d fsaccess.Dir, name []byte, info fsaccess.EntryInfo) (originRecord, bool, error) {
	if info.Kind != domain.EntryFile || info.MountBoundary || info.Size > maxRecordBytes {
		return originRecord{}, false, nil
	}
	data, err := r.readSmall(d, name, info)
	if err != nil {
		if isDisk(err) {
			return originRecord{}, false, nil
		}
		return originRecord{}, false, err
	}
	rec, ok := parseRecord(data)
	if !ok || rec.SourceID != string(r.src) || rec.EntryID == "" || rec.PlanID == "" {
		return originRecord{}, false, nil
	}
	return rec, true, nil
}

// intentUnlink re-checks the unlink of an origin record (r4 D6): a name
// <seq>.json in a present folder strictly inside the quarantine, while its
// item folder <seq> holds no entry in the index (else it ends
// not_attempted, and the record stays with its item). The identity
// expected is the index row's when a scan indexed the record; otherwise
// only its kind.
func (r *run) intentUnlink(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	parent, ok, err := loadEntry(ctx, q, it.fromParent)
	if err != nil {
		return it, nil, err
	}
	if !ok || domain.SourceID(parent.source) != r.src || parent.state != "present" || parent.kind != "directory" ||
		!insideQuarantine(parent.path) {
		return it, &end{state: stateChanged}, nil
	}
	stem, ok := bytes.CutSuffix(it.fromName, []byte(".json"))
	if !ok || len(stem) == 0 {
		return it, &end{state: stateFailed, detail: "only an origin record is unlinked"}, nil
	}
	lo, hi := below(childPath(parent.path, stem))
	var held bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE source_id = ? AND state <> 'missing'
		AND path >= ? AND path < ?)`, string(r.src), lo, hi).Scan(&held); err != nil {
		return it, nil, err
	}
	if held {
		return it, &end{state: stateNotAttempted}, nil
	}
	rec := it
	rec.fromPath = childPath(parent.path, it.fromName)
	rec.kind = "file"
	rec.dev, rec.ino, rec.size, rec.mtime = sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
	var (
		id    int64
		kind  string
		size  int64
		dev   sql.NullInt64
		ino   sql.NullInt64
		mtime sql.NullInt64
	)
	err = q.QueryRowContext(ctx, `SELECT id, kind, size, dev, ino, mtime_ns FROM entries WHERE source_id = ? AND path = ?
		AND state <> 'missing'`, string(r.src), rec.fromPath).Scan(&id, &kind, &size, &dev, &ino, &mtime)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return it, nil, err
	case kind != "file":
		return it, &end{state: stateChanged}, nil
	default:
		rec.entry, rec.dev, rec.ino, rec.mtime = id, dev, ino, mtime
		rec.size = sql.NullInt64{Int64: size, Valid: true}
	}
	return rec, nil, nil
}

// isRecordAt reports whether info, an lstat of the unlink item's name, is
// what it may remove: a regular file with the identity expected (when one
// was), holding an origin record of this source and of the plan folder
// that holds it.
func (r *run) isRecordAt(f *folder, it item, info fsaccess.EntryInfo) (bool, error) {
	if info.Kind != domain.EntryFile || info.MountBoundary {
		return false, nil
	}
	if it.size.Valid && !r.matches(it, info) {
		return false, nil
	}
	rec, ok, err := r.readRecord(f.dir, it.fromName, info)
	if err != nil || !ok {
		return false, err
	}
	return rec.PlanID == string(f.name), nil
}

// stepUnlink removes an origin record: through a folder inside the
// quarantine, only a file that is still a record of its plan. A name
// already gone is done.
func (r *run) stepUnlink(it item) (verdict, error) {
	f, err := r.openFolder(it.fromParent)
	if err != nil {
		return r.preflight(it, err)
	}
	defer f.close()
	w, err := quarantineWriter(f)
	if err != nil {
		return r.record(it, end{state: stateChanged, detail: err.Error()})
	}
	info, err := r.lstat(f.dir, it.fromName)
	if o, _ := fsaccess.OutcomeOf(err); err != nil && o == domain.OutcomeAbsent {
		return r.settle(it, settleConfirm, end{state: stateFailed})
	}
	if err != nil {
		return r.preflight(it, err)
	}
	ok, err := r.isRecordAt(f, it, info)
	if err != nil {
		return halt, err
	}
	if !ok {
		return r.record(it, end{state: stateChanged})
	}
	if err := r.before(it); err != nil {
		return halt, err
	}
	done := r.rt.FSCall("unlink")
	err = w.Unlink(it.fromName)
	done()
	if err := r.after(it); err != nil {
		return halt, err
	}
	if err != nil {
		return r.failed(it, err)
	}
	if v, err, ok := r.syncAll(f); !ok {
		return v, err
	}
	return r.settle(it, settleConfirm, end{state: stateFailed, detail: "the record's removal reported success, but it is still there"})
}

// settleUnlink decides an unlink by its name (r4 D4): absent, it is done
// (the index drops a row a scan made); present and still the record, it
// did not happen (it runs once); anything else is manual_recovery.
func (r *run) settleUnlink(it item, mode settleMode, notDone end) (verdict, error) {
	f, found, err := r.openToLook(it.fromParent)
	if err != nil {
		return halt, err
	}
	if f != nil {
		defer f.close()
		info, err := r.lstat(f.dir, it.fromName)
		switch o, _ := fsaccess.OutcomeOf(err); {
		case err != nil && o == domain.OutcomeAbsent:
			found = foundAbsent
		case err != nil:
			return halt, err
		default:
			rec, err := r.isRecordAt(f, it, info)
			if err != nil {
				return halt, err
			}
			found = foundOther
			if rec {
				found = foundSame
			}
		}
	}
	switch found {
	case foundSame:
		return r.notDone(it, mode, notDone)
	case foundOther:
		return r.record(it, end{state: stateManualRecovery, detail: findings(found, foundAbsent),
			stop: mode != settleReconcile})
	}
	if f != nil && mode == settleReconcile {
		if err := r.sync(f); err != nil {
			return halt, err
		}
	}
	path := it.fromPath
	return r.outcome(it, func(tx *jobs.Tx) error { return r.e.idx.ApplyUnlink(r.bg, tx.SQL(), r.src, path) },
		findings(foundAbsent, foundAbsent))
}

// marshalRecord renders a record as one line of JSON.
func marshalRecord(rec originRecord) ([]byte, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// parseRecord reads data as an origin record of version 1.
func parseRecord(data []byte) (originRecord, bool) {
	var rec originRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.Version != 1 {
		return originRecord{}, false
	}
	return rec, true
}

// noIndexChange is the index update of a step the index does not see: a
// record created, or a check verified.
func noIndexChange(*jobs.Tx) error { return nil }
