package executor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/sources"
	"precious/internal/store"
)

// A purge action (r4 D10, D11) holds its check in actions.check_id: one
// verify item, then one purge item per checked item (entry_id the item, a
// quarantined top entry; from_parent its <seq> folder), then the sweep's
// rmdir and unlink items.

// gateSQL reports a running or ready check's state and source, and whether
// a file it recorded still needs a confirmation (design Interfaces, U10): a
// unique file not likely junk, or likely junk before its group was
// confirmed; a copy_offline or unreadable one; an opaque archive without a
// verified copy.
const gateSQL = `SELECT c.source_id, c.state, EXISTS (SELECT 1 FROM purge_check_files f WHERE f.check_id = c.id
		AND f.confirmed_at IS NULL AND (f.verdict IN ('copy_offline', 'unreadable')
			OR (f.verdict = 'opaque_archive' AND f.copy_path IS NULL)
			OR (f.verdict = 'unique' AND (f.class IS NOT 'likely_junk' OR c.junk_confirmed_at IS NULL))))
	FROM purge_checks c WHERE c.id = ?`

// checkUsable ends a purge item whose check is no longer ready for this
// source, or whose gate is not satisfied (r4 D11): changed check_stale.
func (r *run) checkUsable(ctx context.Context, q store.Queryer, check int64) (*end, error) {
	if check == 0 {
		return &end{state: stateChanged, reason: reasonCheckStale}, nil
	}
	var (
		src, state  string
		unconfirmed bool
	)
	err := q.QueryRowContext(ctx, gateSQL, check).Scan(&src, &state, &unconfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return &end{state: stateChanged, reason: reasonCheckStale}, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case domain.SourceID(src) != r.src || state != "ready":
		return &end{state: stateChanged, reason: reasonCheckStale}, nil
	case unconfirmed:
		return &end{state: stateChanged, reason: reasonCheckStale, detail: "the check has unconfirmed files"}, nil
	}
	return nil, nil
}

// purgeCheck returns the check of the item's purge action.
func (r *run) purgeCheck(ctx context.Context, q store.Queryer, it item) (int64, error) {
	if r.action == it.action && r.action != 0 {
		return r.checkID, nil
	}
	var check sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT check_id FROM actions WHERE id = ? AND kind = 'purge'`, it.action).Scan(&check)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return check.Int64, err
}

// intentVerify re-checks the check before verify reads the disk: a check
// that is not ready, or not confirmed, stops the action.
func (r *run) intentVerify(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	check, err := r.purgeCheck(ctx, q, it)
	if err != nil {
		return it, nil, err
	}
	e, err := r.checkUsable(ctx, q, check)
	if e != nil {
		e.stop = true
	}
	return it, e, err
}

// stepVerify compares the disk with the check before anything is deleted
// (r4 D10): every recorded entry of the set, walked whole, and every copy
// relied on. Any difference marks the check stale and stops the action with
// nothing deleted.
func (r *run) stepVerify(it item) (verdict, error) {
	check, err := r.purgeCheck(r.bg, r.e.st.Reader(), it)
	if err != nil {
		return halt, err
	}
	reason, detail, err := r.verifyCheck(check)
	if err != nil {
		return halt, err
	}
	if reason != "" {
		r.e.log.Warn("executor: the disk no longer matches the check; nothing is deleted", "check", check,
			"reason", reason, "detail", detail)
		return r.record(it, end{state: stateChanged, reason: reason, detail: detail, stop: true, staleCheck: check})
	}
	return r.outcome(it, noIndexChange, "")
}

// verifyCheck returns file_changed or copy_changed, with the display path
// that differs, or an empty reason when the disk matches the check.
func (r *run) verifyCheck(check int64) (reason, detail string, err error) {
	rd := r.e.st.Reader()
	type setItem struct {
		entry int64
		path  []byte
	}
	// An unreadable item is never purged; its records hold the index's
	// identity, not the disk's.
	rows, err := rd.QueryContext(r.bg, `SELECT entry_id, path FROM purge_check_items WHERE check_id = ? AND readable = 1
		ORDER BY path`,
		check)
	if err != nil {
		return "", "", err
	}
	var set []setItem
	for rows.Next() {
		var s setItem
		if err := rows.Scan(&s.entry, &s.path); err != nil {
			rows.Close()
			return "", "", err
		}
		set = append(set, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	tol, err := r.unlinkedFor(check)
	if err != nil {
		return "", "", err
	}
	for _, s := range set {
		var (
			parent sql.NullInt64
			path   []byte
			state  string
		)
		err := rd.QueryRowContext(r.bg, `SELECT parent_id, path, state FROM entries WHERE id = ? AND source_id = ?`,
			s.entry, string(r.src)).Scan(&parent, &path, &state)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (state != "present" || !bytes.Equal(path, s.path))) {
			return reasonFileChanged, domain.DisplayName(s.path), nil
		}
		if err != nil {
			return "", "", err
		}
		recs, err := r.checkRecords(check, s.entry, s.path)
		if err != nil {
			return "", "", err
		}
		seq, err := r.openFolder(parent.Int64)
		if err != nil {
			if !isDisk(err) {
				return "", "", err
			}
			return reasonFileChanged, domain.DisplayName(s.path), nil
		}
		name := s.path[bytes.LastIndexByte(s.path, '/')+1:]
		tree, err := r.collect(seq.dir, name, s.path)
		seq.close()
		if err != nil {
			if !isDisk(err) {
				return "", "", err
			}
			return reasonFileChanged, domain.DisplayName(s.path), nil
		}
		if _, diff := r.compareTree(tree, recs, false, tol); diff != nil {
			return reasonFileChanged, domain.DisplayName(diff), nil
		}
	}
	return r.verifyCopies(check)
}

// verifyCopies compares every copy the check relied on (a recorded copy of
// a file that needs no confirmation of its own) with the disk: its index
// row present at its path and outside the quarantine, its source online,
// and its lstat the identity recorded.
func (r *run) verifyCopies(check int64) (reason, detail string, err error) {
	rd := r.e.st.Reader()
	rows, err := rd.QueryContext(r.bg, `SELECT DISTINCT copy_source, copy_path, copy_entry, copy_size, copy_mtime_ns,
			copy_ctime_ns, copy_ino, copy_dev
		FROM purge_check_files WHERE check_id = ? AND copy_path IS NOT NULL AND confirmed_at IS NULL
			AND verdict <> 'copy_offline'
		ORDER BY copy_source, copy_path`, check)
	if err != nil {
		return "", "", err
	}
	type relied struct {
		src                      domain.SourceID
		path                     []byte
		entry, size              sql.NullInt64
		mtime, ctime, ino, devNo sql.NullInt64
	}
	var list []relied
	for rows.Next() {
		var (
			c   relied
			src sql.NullString
		)
		if err := rows.Scan(&src, &c.path, &c.entry, &c.size, &c.mtime, &c.ctime, &c.ino, &c.devNo); err != nil {
			rows.Close()
			return "", "", err
		}
		c.src = domain.SourceID(src.String)
		list = append(list, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	roots := &copyRoots{r: r, open: map[domain.SourceID]*sourceRoot{}}
	defer roots.close()
	for _, c := range list {
		bad := func() (string, string, error) {
			return reasonCopyChanged, domain.DisplayName(c.path), nil
		}
		var (
			src, state, kind string
			path             []byte
		)
		err := rd.QueryRowContext(r.bg, `SELECT source_id, path, state, kind FROM entries WHERE id = ?`, c.entry.Int64).
			Scan(&src, &path, &state, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			return bad()
		}
		if err != nil {
			return "", "", err
		}
		if domain.SourceID(src) != c.src || state != "present" || kind != "file" || !bytes.Equal(path, c.path) ||
			index.IsQuarantinePath(path) {
			return bad()
		}
		s := roots.get(r.bg, c.src)
		if !s.ok {
			return bad()
		}
		info, err := lstatPath(s.root, c.path)
		if err != nil {
			if isDisk(err) {
				return bad()
			}
			return "", "", err
		}
		if info.Kind != domain.EntryFile || info.MountBoundary || !c.size.Valid || c.size.Int64 != info.Size ||
			!c.mtime.Valid || !sameTime(c.mtime.Int64, info.ModTime.UnixNano(), s.caps) ||
			(c.ctime.Valid && c.ctime.Int64 != 0 && !info.Ctime.IsZero() &&
				!sameTime(c.ctime.Int64, info.Ctime.UnixNano(), s.caps)) ||
			(s.caps.StableIdentity && (!c.ino.Valid || uint64(c.ino.Int64) != info.Ino ||
				(c.devNo.Valid && uint64(c.devNo.Int64) != info.Dev))) {
			return bad()
		}
	}
	return "", "", nil
}

// lstatPath lstats the source-relative path below root, descending one
// component at a time without following a link or crossing a mount.
func lstatPath(root fsaccess.Dir, path []byte) (fsaccess.EntryInfo, error) {
	parts := bytes.Split(path, []byte{'/'})
	d := root
	for i, p := range parts {
		info, err := d.Lstat(p)
		if err != nil || i == len(parts)-1 {
			if d != root {
				d.Close()
			}
			return info, err
		}
		if info.Kind != domain.EntryDirectory || info.MountBoundary {
			if d != root {
				d.Close()
			}
			return fsaccess.EntryInfo{}, errChanged
		}
		next, err := d.OpenDir(p, info)
		if d != root {
			d.Close()
		}
		if err != nil {
			return fsaccess.EntryInfo{}, err
		}
		d = next
	}
	return fsaccess.EntryInfo{}, errChanged
}

// checkFile is one entry on disk a check recorded below a set item.
type checkFile struct {
	entry           int64
	kind            string
	path            []byte
	size            int64
	mtime, ctime    sql.NullInt64
	ino, dev, nlink sql.NullInt64
	alloc           sql.NullInt64
}

// checkRecords returns the on-disk records (member_id NULL) of the check's
// set item at path, by path.
func (r *run) checkRecords(check, item int64, path []byte) (map[string]checkFile, error) {
	lo, hi := below(path)
	rows, err := r.e.st.Reader().QueryContext(r.bg, `SELECT entry_id, kind, path, size, mtime_ns, ctime_ns, ino, dev,
			nlink, alloc
		FROM purge_check_files WHERE check_id = ? AND (path = ? OR (path >= ? AND path < ?))
			AND item_id = ? AND member_id IS NULL`, check, path, lo, hi, item)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]checkFile{}
	for rows.Next() {
		var (
			f     checkFile
			entry sql.NullInt64
		)
		if err := rows.Scan(&entry, &f.kind, &f.path, &f.size, &f.mtime, &f.ctime, &f.ino, &f.dev, &f.nlink,
			&f.alloc); err != nil {
			return nil, err
		}
		f.entry = entry.Int64
		out[string(f.path)] = f
	}
	return out, rows.Err()
}

// inode names a file's data across its names.
type inode struct{ dev, ino uint64 }

// unlinkedFor returns the inodes of the check of which a name was removed
// by this purge: in this attempt, or by a step recorded before (a recorded
// file whose entry the index no longer holds), whose other names then show
// a new change time and a lower link count (r4 D10).
func (r *run) unlinkedFor(check int64) (map[inode]bool, error) {
	if set, ok := r.unlinked[check]; ok {
		return set, nil
	}
	rows, err := r.e.st.Reader().QueryContext(r.bg, `SELECT DISTINCT f.dev, f.ino FROM purge_check_files f
		WHERE f.check_id = ? AND f.member_id IS NULL AND f.kind = 'file' AND f.ino IS NOT NULL AND f.dev IS NOT NULL
			AND f.entry_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.id = f.entry_id)`, check)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[inode]bool{}
	for rows.Next() {
		var dev, ino int64
		if err := rows.Scan(&dev, &ino); err != nil {
			return nil, err
		}
		set[inode{uint64(dev), uint64(ino)}] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if r.unlinked == nil {
		r.unlinked = map[int64]map[inode]bool{}
	}
	r.unlinked[check] = set
	return set, nil
}

// node is one entry of an item's tree on disk, as walked.
type node struct {
	name, path []byte
	info       fsaccess.EntryInfo
	kids       []*node
}

// collect walks the entry name in d, at index path path, and everything
// below it, through handles opened against each lstat, one folder open per
// level. A nil node means the name is absent.
func (r *run) collect(d fsaccess.Dir, name, path []byte) (*node, error) {
	info, err := r.lstat(d, name)
	if err != nil {
		if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeAbsent {
			return nil, nil
		}
		return nil, err
	}
	n := &node{name: bytes.Clone(name), path: path, info: info}
	if info.Kind != domain.EntryDirectory || info.MountBoundary {
		return n, nil
	}
	done := r.rt.FSCall("open_dir")
	sub, err := d.OpenDir(name, info)
	done()
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	for {
		done := r.rt.FSCall("read_batch")
		batch, err := sub.ReadBatch(256)
		done()
		for _, e := range batch {
			k, err := r.collect(sub, e.Name, childPath(path, e.Name))
			if err != nil {
				return nil, err
			}
			if k != nil {
				n.kids = append(n.kids, k)
			}
		}
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// compareTree compares a walked tree with its records (r4 D10, D11): every
// entry walked must be recorded and the same, and every record walked. In a
// replay, records no longer on disk are the part a crash left deleted: they
// are returned, and their inodes join tol first. diff is the first path
// that differs, nil when none.
func (r *run) compareTree(tree *node, recs map[string]checkFile, replay bool, tol map[inode]bool) (gone []checkFile, diff []byte) {
	seen := make(map[string]bool, len(recs))
	var mark func(n *node)
	mark = func(n *node) {
		seen[string(n.path)] = true
		for _, k := range n.kids {
			mark(k)
		}
	}
	if tree != nil {
		mark(tree)
	}
	for p, rc := range recs {
		if seen[p] {
			continue
		}
		if !replay {
			return nil, rc.path
		}
		gone = append(gone, rc)
		if rc.kind == "file" && rc.dev.Valid && rc.ino.Valid {
			tol[inode{uint64(rc.dev.Int64), uint64(rc.ino.Int64)}] = true
		}
	}
	var walk func(n *node) []byte
	walk = func(n *node) []byte {
		rc, ok := recs[string(n.path)]
		if !ok || !r.sameAsRecorded(rc, n.info, !replay, tol) {
			return n.path
		}
		for _, k := range n.kids {
			if d := walk(k); d != nil {
				return d
			}
		}
		return nil
	}
	if tree != nil {
		if d := walk(tree); d != nil {
			return gone, d
		}
	}
	return gone, nil
}

// sameAsRecorded compares an lstat with a check's record (r4 D10): the kind;
// device and inode where identity is stable; for anything but a folder, the
// size, the modification time within the resolution, and the change time
// likewise when both are known, except for an inode of which the purge
// removed a name (tol); for a folder, its times only when dirTimes (its
// contents are not being deleted).
func (r *run) sameAsRecorded(rc checkFile, info fsaccess.EntryInfo, dirTimes bool, tol map[inode]bool) bool {
	if info.MountBoundary || kindColumn(info.Kind) != rc.kind {
		return false
	}
	if r.caps.StableIdentity && (!rc.ino.Valid || uint64(rc.ino.Int64) != info.Ino ||
		(rc.dev.Valid && uint64(rc.dev.Int64) != info.Dev)) {
		return false
	}
	ctimeOK := !rc.ctime.Valid || rc.ctime.Int64 == 0 || info.Ctime.IsZero() ||
		sameTime(rc.ctime.Int64, info.Ctime.UnixNano(), r.caps)
	mtimeOK := rc.mtime.Valid && sameTime(rc.mtime.Int64, info.ModTime.UnixNano(), r.caps)
	if rc.kind == "directory" {
		return !dirTimes || ((!rc.mtime.Valid || mtimeOK) && ctimeOK)
	}
	if rc.size != info.Size || !mtimeOK {
		return false
	}
	return ctimeOK || tol[inode{info.Dev, info.Ino}]
}

// intentPurge re-checks a purge item (r4 D11): the check ready and its gate
// satisfied (else the action stops, check_stale); the item in the check's
// set and readable; the entry where the check found it, strictly inside the
// quarantine below a folder of it; its own decision still discard and
// nothing in it kept.
func (r *run) intentPurge(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	check, err := r.purgeCheck(ctx, q, it)
	if err != nil {
		return it, nil, err
	}
	if e, err := r.checkUsable(ctx, q, check); err != nil || e != nil {
		if e != nil {
			e.stop = true
		}
		return it, e, err
	}
	var (
		setPath  []byte
		readable bool
	)
	err = q.QueryRowContext(ctx, `SELECT path, readable FROM purge_check_items WHERE check_id = ? AND entry_id = ?`,
		check, it.entry).Scan(&setPath, &readable)
	if errors.Is(err, sql.ErrNoRows) {
		return it, &end{state: stateChanged, reason: reasonCheckStale}, nil
	}
	if err != nil {
		return it, nil, err
	}
	if !readable {
		return it, &end{state: stateRefused, reason: reasonUnreadable}, nil
	}
	ent, e, err := r.purgeEntry(ctx, q, it, setPath)
	if err != nil || e != nil {
		return it, e, err
	}
	rec := it
	rec.fromParent, rec.fromName, rec.fromPath = ent.parent, ent.name, ent.path
	rec.kind, rec.dev, rec.ino, rec.mtime = ent.kind, ent.dev, ent.ino, ent.mtime
	rec.size = sql.NullInt64{Int64: ent.size, Valid: true}
	return rec, nil, nil
}

// purgeEntry loads a purge item's entry and checks its place and decisions.
func (r *run) purgeEntry(ctx context.Context, q *sql.Tx, it item, setPath []byte) (entryRow, *end, error) {
	changed := &end{state: stateChanged, reason: reasonFileChanged}
	ent, ok, err := loadEntry(ctx, q, it.entry)
	if err != nil || !ok {
		return ent, changed, err
	}
	if domain.SourceID(ent.source) != r.src || ent.state != "present" || !bytes.Equal(ent.path, setPath) {
		return ent, changed, nil
	}
	parent, ok, err := loadEntry(ctx, q, ent.parent)
	if err != nil {
		return ent, nil, err
	}
	// Deletion stays inside the quarantine (r4 D12): the item and the
	// folder holding it.
	if !ok || !insideQuarantine(ent.path) || !insideQuarantine(parent.path) {
		return ent, &end{state: stateChanged}, nil
	}
	if !ent.decision.Valid || ent.decision.String != "discard" {
		return ent, &end{state: stateChanged, reason: reasonDecisionChanged}, nil
	}
	kept, err := keptWithin(ctx, q, r.src, ent.path)
	if err != nil {
		return ent, nil, err
	}
	if kept {
		return ent, &end{state: stateChanged, reason: reasonDecisionChanged}, nil
	}
	return ent, nil, nil
}

// purgeRechecks are a purge item's intent re-checks, run again before a
// purge left intent is replayed (r4 D11): writes, the check, the item's
// place and decisions.
func (r *run) purgeRechecks(ctx context.Context, q *sql.Tx, it item) (*end, error) {
	if err := sources.CheckWrites(ctx, q, r.src, r.e.allowWrites); err != nil {
		switch domain.CodeOf(err) {
		case domain.CodeWritesUnavailable, domain.CodeWritesDisabled, domain.CodeSourceOffline,
			domain.CodeUnknownSource:
			return &end{state: stateChanged, reason: reasonWritesOff, stop: true}, nil
		}
		return nil, err
	}
	check, err := r.purgeCheck(ctx, q, it)
	if err != nil {
		return nil, err
	}
	if e, err := r.checkUsable(ctx, q, check); err != nil || e != nil {
		if e != nil {
			e.stop = true
		}
		return e, err
	}
	_, e, err := r.purgeEntry(ctx, q, it, it.fromPath)
	if e != nil && e.reason != reasonDecisionChanged {
		e = &end{state: stateChanged, reason: reasonFileChanged}
	}
	return e, err
}

// purgeResult is what a purge step removed and how it ends.
type purgeResult struct {
	end end
	// removed are the entries removed when the item was not removed whole.
	removed []int64
	// whole: the item is gone; seqGone: its <seq> folder too.
	whole, seqGone bool
	// record is the path of the origin record unlinked.
	record              []byte
	files, bytes, freed int64
}

// count adds a removed entry to the report.
func (p *purgeResult) count(entry int64, kind string, size int64, freed int64) {
	if entry != 0 {
		p.removed = append(p.removed, entry)
	}
	if kind == "file" {
		p.files++
		p.bytes += size
		p.freed += freed
	}
}

// purge holds one purge step's handles and records.
type purge struct {
	r     *run
	it    item
	check int64
	recs  map[string]checkFile
	tol   map[inode]bool
	res   purgeResult
}

// stepPurge deletes one checked item for good (r4 D11). It walks the whole
// tree of the item in its <seq> folder and compares it with the check's
// records before the first deletion: any entry that differs, or that the
// check did not record, ends the item changed with nothing deleted. Then it
// deletes depth first, comparing each entry again just before, unlinks the
// item's origin record, removes <seq>, and syncs. A replay (after a crash,
// its re-checks passed again) takes records no longer on disk as deleted
// before, and finishes the rest.
func (r *run) stepPurge(it item, replay bool) (verdict, error) {
	rd := r.e.st.Reader()
	check, err := r.purgeCheck(r.bg, rd, it)
	if err != nil {
		return halt, err
	}
	p := &purge{r: r, it: it, check: check}
	if p.recs, err = r.checkRecords(check, it.entry, it.fromPath); err != nil {
		return halt, err
	}
	if p.tol, err = r.unlinkedFor(check); err != nil {
		return halt, err
	}
	seq, err := r.openFolder(it.fromParent)
	if err != nil {
		if !replay {
			return r.preflight(it, err)
		}
		if o, _ := fsaccess.OutcomeOf(err); o != domain.OutcomeAbsent {
			if !isDisk(err) {
				return halt, err
			}
			return r.purgeOutcome(it, p.gone(end{state: stateManualRecovery, detail: findings(foundOther, foundAbsent),
				stop: true}, nil))
		}
		// The crash came after <seq> was removed: the item is gone, and
		// only its record may be left.
		all := make([]checkFile, 0, len(p.recs))
		for _, rc := range p.recs {
			all = append(all, rc)
		}
		p.gone(end{state: stateDone}, all)
		p.res.whole, p.res.seqGone = true, true
		if plan, err := r.openFolder(r.parentOf(it.fromParent)); err == nil {
			if err := p.unlinkRecord(plan.dir, plan.path, seqName(it.fromPath)); err != nil {
				plan.close()
				return halt, err
			}
			if p.res.record != nil {
				if err := r.sync(plan); err != nil {
					plan.close()
					return halt, err
				}
			}
			plan.close()
		} else if !isDisk(err) {
			return halt, err
		}
		return r.purgeOutcome(it, p.res)
	}
	defer seq.close()
	if !insideQuarantine(seq.path) {
		return r.record(it, end{state: stateChanged})
	}
	tree, err := r.collect(seq.dir, it.fromName, it.fromPath)
	if err != nil {
		if !isDisk(err) || replay {
			return halt, err
		}
		return r.record(it, end{state: stateFailed, detail: osText(err)})
	}
	if !replay && (tree == nil || len(p.recs) == 0) {
		// The item is gone, or the check recorded nothing of it.
		return r.record(it, end{state: stateChanged, reason: reasonFileChanged, detail: domain.DisplayName(it.fromPath),
			staleCheck: check})
	}
	gone, diff := r.compareTree(tree, p.recs, replay, p.tol)
	if diff != nil {
		e := end{state: stateChanged, reason: reasonFileChanged, detail: domain.DisplayName(diff), staleCheck: check}
		if !replay {
			return r.record(it, e)
		}
		return r.purgeOutcome(it, p.gone(e, gone))
	}
	p.gone(end{state: stateDone}, gone)
	if tree != nil {
		stop, err := p.remove(seq.dir, seq.path, tree)
		if err != nil {
			return halt, err
		}
		if stop != nil {
			// The item itself, deleted last, is still in <seq>; the folders
			// below it that changed were synced on the way out.
			p.res.end = *stop
			return r.purgeOutcome(it, p.res)
		}
	}
	p.res.whole, p.res.removed = true, nil
	if seq.parent != nil {
		plan := &folder{path: parentPath(seq.path), dir: seq.parent}
		if err := p.unlinkRecord(seq.parent, plan.path, seq.name); err != nil {
			return halt, err
		}
		if err := r.before(it); err != nil {
			return halt, err
		}
		w, werr := writer(seq.parent)
		if werr == nil {
			done := r.rt.FSCall("rmdir")
			werr = w.Rmdir(seq.name)
			done()
		}
		if err := r.after(it); err != nil {
			return halt, err
		}
		if werr == nil {
			p.res.seqGone = true
		} else {
			r.e.log.Warn("executor: an emptied item folder stays in the quarantine", "path",
				domain.DisplayName(seq.path), "err", werr)
		}
		if p.res.seqGone || p.res.record != nil {
			if v, err, ok := r.syncAll(plan); !ok {
				return v, err
			}
		}
	}
	if !p.res.seqGone {
		if v, err, ok := r.syncAll(seq); !ok {
			return v, err
		}
	}
	return r.purgeOutcome(it, p.res)
}

// gone records the entries a replay found deleted, and the end.
func (p *purge) gone(e end, recs []checkFile) purgeResult {
	p.res.end = e
	for _, rc := range recs {
		var freed int64
		if rc.nlink.Int64 == 1 {
			freed = rc.alloc.Int64
		}
		p.res.count(rc.entry, rc.kind, rc.size, freed)
	}
	return p.res
}

// remove deletes n, a child of d at index path dpath, depth first. It
// returns the end of a step that stopped partway (a difference just before
// a deletion, or an error the filesystem gave); an error only for a
// simulated crash or the database.
func (p *purge) remove(d fsaccess.Dir, dpath []byte, n *node) (*end, error) {
	r, it := p.r, p.it
	rc, ok := p.recs[string(n.path)]
	if !ok || !insideQuarantine(dpath) || !insideQuarantine(n.path) {
		return &end{state: stateChanged, reason: reasonFileChanged, detail: domain.DisplayName(n.path),
			staleCheck: p.check}, nil
	}
	changedNow := func() *end {
		return &end{state: stateChanged, reason: reasonFileChanged, detail: domain.DisplayName(n.path),
			staleCheck: p.check}
	}
	info, err := r.lstat(d, n.name)
	if err != nil {
		if isDisk(err) {
			return changedNow(), nil
		}
		return nil, err
	}
	if !r.sameAsRecorded(rc, info, false, p.tol) {
		return changedNow(), nil
	}
	w, err := writer(d)
	if err != nil {
		return &end{state: stateFailed, detail: osText(err)}, nil
	}
	if rc.kind == "directory" {
		done := r.rt.FSCall("open_dir")
		sub, err := d.OpenDir(n.name, info)
		done()
		if err != nil {
			if isDisk(err) {
				return changedNow(), nil
			}
			return nil, err
		}
		before := len(p.res.removed)
		for _, k := range n.kids {
			stop, err := p.remove(sub, n.path, k)
			if err != nil || stop != nil {
				if err == nil && len(p.res.removed) > before {
					if serr := syncDir(r, sub); serr != nil {
						r.e.log.Warn("executor: sync after a partial purge", "path", domain.DisplayName(n.path),
							"err", serr)
					}
				}
				sub.Close()
				return stop, err
			}
		}
		sub.Close()
		if err := r.before(it); err != nil {
			return nil, err
		}
		done = r.rt.FSCall("rmdir")
		err = w.Rmdir(n.name)
		done()
		if err := r.after(it); err != nil {
			return nil, err
		}
		if err != nil {
			return p.refused(err, n), nil
		}
		p.res.count(rc.entry, rc.kind, 0, 0)
		return nil, nil
	}
	qw, err := quarantineWriter(&folder{path: dpath, dir: d})
	if err != nil {
		return &end{state: stateChanged, detail: err.Error()}, nil
	}
	if err := r.before(it); err != nil {
		return nil, err
	}
	done := r.rt.FSCall("unlink")
	err = qw.Unlink(n.name)
	done()
	if err := r.after(it); err != nil {
		return nil, err
	}
	if err != nil {
		return p.refused(err, n), nil
	}
	var freed int64
	if info.Nlink <= 1 {
		freed = info.Blocks * 512
	} else {
		p.tol[inode{info.Dev, info.Ino}] = true
	}
	p.res.count(rc.entry, rc.kind, info.Size, freed)
	return nil, nil
}

// refused is the end of a purge step whose deletion the filesystem refused.
func (p *purge) refused(err error, n *node) *end {
	switch {
	case errors.Is(err, fsaccess.ErrIsDir), errors.Is(err, fsaccess.ErrNotEmpty):
		return &end{state: stateChanged, reason: reasonFileChanged, detail: domain.DisplayName(n.path),
			staleCheck: p.check}
	case errors.Is(err, fsaccess.ErrReadOnly):
		return &end{state: stateFailed, detail: osText(err), stop: true}
	}
	if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeAbsent {
		return &end{state: stateChanged, reason: reasonFileChanged, detail: domain.DisplayName(n.path),
			staleCheck: p.check}
	}
	return &end{state: stateFailed, detail: osText(err)}
}

// syncDir fsyncs a folder handle a purge changed.
func syncDir(r *run, d fsaccess.Dir) error {
	return r.sync(&folder{dir: d})
}

// unlinkRecord removes the item's origin record <seq>.json from the plan
// folder d (at index path path), when it is a record of the item's entry;
// another file of that name stays.
func (p *purge) unlinkRecord(d fsaccess.Dir, path, seq []byte) error {
	r, it := p.r, p.it
	if !insideQuarantine(path) || len(seq) == 0 {
		return nil
	}
	name := append(bytes.Clone(seq), ".json"...)
	info, err := r.lstat(d, name)
	if err != nil {
		if isDisk(err) {
			return nil
		}
		return err
	}
	rec, ok, err := r.readRecord(d, name, info)
	if err != nil || !ok || rec.EntryID != strconv.FormatInt(it.entry, 10) {
		return err
	}
	qw, err := quarantineWriter(&folder{path: path, dir: d})
	if err != nil {
		return nil
	}
	if err := r.before(it); err != nil {
		return err
	}
	done := r.rt.FSCall("unlink")
	uerr := qw.Unlink(name)
	done()
	if err := r.after(it); err != nil {
		return err
	}
	if uerr != nil {
		r.e.log.Warn("executor: an origin record stays in the quarantine", "path",
			domain.DisplayName(childPath(path, name)), "err", uerr)
		return nil
	}
	p.res.record = childPath(path, name)
	return nil
}

// parentPath is the index path of the folder holding path.
func parentPath(path []byte) []byte {
	if i := bytes.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return nil
}

// seqName is the name of the folder holding the entry at path.
func seqName(path []byte) []byte {
	pp := parentPath(path)
	return pp[bytes.LastIndexByte(pp, '/')+1:]
}

// parentOf returns the folder holding entry id in the index, 0 when none.
func (r *run) parentOf(id int64) int64 {
	var parent sql.NullInt64
	if err := r.e.st.Reader().QueryRowContext(r.bg, `SELECT parent_id FROM entries WHERE id = ?`, id).
		Scan(&parent); err != nil {
		return 0
	}
	return parent.Int64
}

// purgeOutcome records a purge step's end in one transaction (r4 D11): the
// item's state, the index rows of what it removed (the <seq> folder's
// subtree, the item's, or the entries removed), the record's row, the
// action's report, the checks relying on its paths, and the check made
// stale by a difference. The purge's own check stays ready after a whole
// item. When the transaction fails, the item ends manual_recovery.
func (r *run) purgeOutcome(it item, res purgeResult) (verdict, error) {
	e := res.end
	err := r.write(func(tx *jobs.Tx) error {
		q := tx.SQL()
		n, err := q.ExecContext(r.bg, `UPDATE action_items SET state = ?, reason = ?, detail = ?, finished_at = ?
			WHERE id = ? AND state = 'intent'`, e.state, nullString(e.reason), nullString(e.detail),
			clock.Millis(tx.Now()), it.id)
		if err != nil {
			return err
		}
		if k, _ := n.RowsAffected(); k == 0 {
			return nil
		}
		var removed []domain.EntryID
		var whole domain.EntryID
		switch {
		case res.seqGone && it.fromParent != 0:
			whole = domain.EntryID(it.fromParent)
		case res.whole:
			whole = domain.EntryID(it.entry)
		default:
			for _, id := range res.removed {
				removed = append(removed, domain.EntryID(id))
			}
		}
		if whole != 0 || len(removed) > 0 {
			if err := r.e.idx.ApplyPurge(r.bg, q, r.src, removed, whole); err != nil {
				return &indexError{err}
			}
		}
		if res.record != nil {
			if err := r.e.idx.ApplyUnlink(r.bg, q, r.src, res.record); err != nil {
				return &indexError{err}
			}
		}
		if _, err := q.ExecContext(r.bg, `UPDATE actions SET deleted_files = deleted_files + ?,
			deleted_bytes = deleted_bytes + ?, freed_bytes = freed_bytes + ? WHERE id = ?`,
			res.files, res.bytes, res.freed, it.action); err != nil {
			return err
		}
		if err := r.markStale(tx, it, e.state == stateDone, parentPath(it.fromPath), res.record); err != nil {
			return err
		}
		if e.staleCheck != 0 {
			if err := markCheckStale(r.bg, q, e.staleCheck); err != nil {
				return err
			}
		}
		if e.stop && r.action != 0 {
			return r.stop(tx)
		}
		return nil
	})
	if err != nil {
		var ie *indexError
		if !errors.As(err, &ie) {
			return halt, err
		}
		r.e.log.Error("executor: recording a purge failed; the item needs a check", "item", it.id, "err", err)
		return r.recordUnsure(it, fmt.Sprintf(`{"removed":%d}`, len(res.removed)))
	}
	if e.stop {
		return halt, nil
	}
	return goOn, nil
}

// reconcilePurge decides a purge left intent (r4 D11). When its intent's
// re-checks pass again, the step is replayed and deletes the rest once.
// Otherwise the item ends changed with the re-check's reason, the part the
// crash left deleted recorded, and nothing more unlinked.
func (r *run) reconcilePurge(it item) (verdict, error) {
	var e *end
	if err := r.write(func(tx *jobs.Tx) error {
		var err error
		e, err = r.purgeRechecks(r.bg, tx.SQL(), it)
		return err
	}); err != nil {
		return halt, err
	}
	if e == nil {
		return r.stepPurge(it, true)
	}
	check, err := r.purgeCheck(r.bg, r.e.st.Reader(), it)
	if err != nil {
		return halt, err
	}
	p := &purge{r: r, it: it, check: check}
	if p.recs, err = r.checkRecords(check, it.entry, it.fromPath); err != nil {
		return halt, err
	}
	p.tol = map[inode]bool{}
	seq, err := r.openFolder(it.fromParent)
	if err != nil {
		if o, _ := fsaccess.OutcomeOf(err); o != domain.OutcomeAbsent {
			return halt, err
		}
		// The item and its folder are gone.
		res := p.gone(*e, nil)
		res.whole, res.seqGone = true, true
		for _, rc := range p.recs {
			var freed int64
			if rc.nlink.Int64 == 1 {
				freed = rc.alloc.Int64
			}
			res.count(0, rc.kind, rc.size, freed)
		}
		return r.purgeOutcome(it, res)
	}
	tree, err := r.collect(seq.dir, it.fromName, it.fromPath)
	seq.close()
	if err != nil {
		return halt, err
	}
	gone, _ := r.compareTree(tree, p.recs, true, p.tol)
	res := p.gone(*e, gone)
	res.whole = tree == nil
	return r.purgeOutcome(it, res)
}
