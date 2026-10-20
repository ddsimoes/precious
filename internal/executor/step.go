package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"syscall"

	"precious/internal/cleanup/stale"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
)

// isDisk reports whether err comes from the disk (an fsaccess error or a
// mismatch with the index) rather than from the database.
func isDisk(err error) bool {
	var fe *fsaccess.Error
	return errors.Is(err, errChanged) || errors.As(err, &fe)
}

// preflight ends an item whose folders or entry could not be checked before
// its step: changed when something moved or differs, refused
// other_filesystem at a mount boundary, failed (with the error) otherwise.
func (r *run) preflight(it item, err error) (verdict, error) {
	if !isDisk(err) {
		return halt, err
	}
	switch {
	case errors.Is(err, errChanged), errors.Is(err, fsaccess.ErrNotDirectory), errors.Is(err, fsaccess.ErrIdentityChanged):
		return r.record(it, end{state: stateChanged})
	case errors.Is(err, fsaccess.ErrMountBoundary):
		return r.record(it, end{state: stateRefused, reason: reasonOtherFilesystem})
	}
	switch o, _ := fsaccess.OutcomeOf(err); o {
	case domain.OutcomeAbsent, domain.OutcomeChangedDuringObservation:
		return r.record(it, end{state: stateChanged})
	}
	return r.record(it, end{state: stateFailed, detail: osText(err)})
}

// osText is the operating system's text of err, else err's own.
func osText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return err.Error()
}

func (r *run) before(it item) error {
	if h := r.e.hooks.BeforeStep; h != nil {
		if err := h(it.id); err != nil {
			return &crashError{err}
		}
	}
	return nil
}

func (r *run) after(it item) error {
	if h := r.e.hooks.AfterStep; h != nil {
		if err := h(it.id); err != nil {
			return &crashError{err}
		}
	}
	return nil
}

// writer is the write surface of a folder. A Dir without one cannot change
// anything safely.
func writer(d fsaccess.Dir) (fsaccess.Writer, error) {
	w, ok := fsaccess.AsWriter(d)
	if !ok {
		return nil, &fsaccess.Error{Op: "AsWriter", Err: fsaccess.ErrNoReplaceUnsupported}
	}
	return w, nil
}

// stepRename runs an intent rename: preflight, the no-replace rename, sync,
// then confirm and outcome.
func (r *run) stepRename(it item) (verdict, error) {
	from, err := r.openFolder(it.fromParent)
	if err != nil {
		return r.preflight(it, err)
	}
	defer from.close()
	to := from
	if it.toParent != it.fromParent {
		if to, err = r.openFolder(it.toParent); err != nil {
			return r.preflight(it, err)
		}
		defer to.close()
	}
	info, err := r.lstat(from.dir, it.fromName)
	if err != nil {
		return r.preflight(it, err)
	}
	switch {
	case info.MountBoundary:
		return r.record(it, end{state: stateRefused, reason: reasonOtherFilesystem})
	case r.kind == kindCleanup && !r.draftOnDisk(it, info):
		// r4 D3: a cleanup item moves only what was drafted.
		return r.record(it, end{state: stateChanged, reason: reasonIdentityChanged})
	case !r.matches(it, info):
		return r.record(it, end{state: stateChanged})
	case info.Dev != from.dir.Self().Dev || to.dir.Self().Dev != from.dir.Self().Dev:
		// D7: devices of the live handles, never of stored rows.
		return r.record(it, end{state: stateRefused, reason: reasonOtherFilesystem})
	}
	w, err := writer(from.dir)
	if err != nil {
		return r.failed(it, err)
	}
	if err := r.before(it); err != nil {
		return halt, err
	}
	done := r.rt.FSCall("rename")
	err = w.RenameNoReplace(it.fromName, to.dir, it.toName)
	done()
	if err := r.after(it); err != nil {
		return halt, err
	}
	if err != nil {
		return r.failed(it, err)
	}
	if v, err, ok := r.syncAll(from, to); !ok {
		return v, err
	}
	return r.settle(it, settleConfirm, end{state: stateFailed, detail: "the rename reported success, but nothing moved"})
}

// stepMkdir runs an intent mkdir.
func (r *run) stepMkdir(it item) (verdict, error) {
	parent, err := r.openFolder(it.toParent)
	if err != nil {
		return r.preflight(it, err)
	}
	defer parent.close()
	w, err := writer(parent.dir)
	if err != nil {
		return r.failed(it, err)
	}
	if err := r.before(it); err != nil {
		return halt, err
	}
	done := r.rt.FSCall("mkdir")
	err = w.Mkdir(it.toName)
	done()
	if err := r.after(it); err != nil {
		return halt, err
	}
	if err != nil {
		return r.failed(it, err)
	}
	if v, err, ok := r.syncAll(parent); !ok {
		return v, err
	}
	return r.settle(it, settleConfirm, end{state: stateFailed, detail: "the new folder reported success, but is not there"})
}

// stepRmdir runs an intent rmdir of an empty folder the action's original
// made.
func (r *run) stepRmdir(it item) (verdict, error) {
	parent, err := r.openFolder(it.fromParent)
	if err != nil {
		return r.preflight(it, err)
	}
	defer parent.close()
	info, err := r.lstat(parent.dir, it.fromName)
	if err != nil {
		return r.preflight(it, err)
	}
	switch {
	case info.MountBoundary:
		return r.record(it, end{state: stateRefused, reason: reasonOtherFilesystem})
	case !r.matches(it, info):
		return r.record(it, end{state: stateChanged})
	}
	w, err := writer(parent.dir)
	if err != nil {
		return r.failed(it, err)
	}
	if err := r.before(it); err != nil {
		return halt, err
	}
	done := r.rt.FSCall("rmdir")
	err = w.Rmdir(it.fromName)
	done()
	if err := r.after(it); err != nil {
		return halt, err
	}
	if err != nil {
		return r.failed(it, err)
	}
	if v, err, ok := r.syncAll(parent); !ok {
		return v, err
	}
	return r.settle(it, settleConfirm, end{state: stateFailed, detail: "the folder removal reported success, but it is still there"})
}

// mayLackNoReplace reports whether drivers of the filesystem type may lack
// RENAME_NOREPLACE while claiming it (older ZFS on Linux). Every other type
// that allows writes honours the flag (fsaccess's capabilities), so its
// EINVAL on a rename refuses the name, as a FAT-family filesystem refuses
// its reserved characters (design V3).
func mayLackNoReplace(fsType string) bool { return fsType == "zfs" }

// failed ends an item whose step the filesystem refused (D5).
func (r *run) failed(it item, err error) (verdict, error) {
	switch {
	case errors.Is(err, fsaccess.ErrExist):
		return r.record(it, end{state: stateConflict, reason: reasonNameTaken})
	case errors.Is(err, fsaccess.ErrIntoItself):
		return r.record(it, end{state: stateRefused, reason: reasonIntoItself})
	case errors.Is(err, fsaccess.ErrNoReplaceUnsupported) && errors.Is(err, syscall.EINVAL) &&
		!mayLackNoReplace(r.fsType):
		return r.record(it, end{state: stateFailed, detail: osText(err)})
	case errors.Is(err, fsaccess.ErrNoReplaceUnsupported):
		return r.record(it, end{state: stateNoSafeRename, stop: true, writesOff: true})
	case errors.Is(err, fsaccess.ErrCrossDevice):
		return r.record(it, end{state: stateRefused, reason: reasonOtherFilesystem})
	case errors.Is(err, fsaccess.ErrReadOnly):
		return r.record(it, end{state: stateFailed, detail: osText(err), stop: true})
	case errors.Is(err, fsaccess.ErrPermission):
		return r.record(it, end{state: stateFailed, detail: osText(err)})
	case errors.Is(err, fsaccess.ErrNotEmpty):
		return r.record(it, end{state: stateNotEmpty})
	case errors.Is(err, fsaccess.ErrIsDir):
		// A record became a folder: nothing was removed.
		return r.record(it, end{state: stateChanged})
	}
	if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeAbsent {
		return r.record(it, end{state: stateChanged})
	}
	// Anything else (EIO, …) is confirmed as after a crash.
	r.e.log.Warn("executor: step failed; looking at both names", "item", it.id, "op", it.op, "err", err)
	return r.settle(it, settleAfterError, end{state: stateFailed, detail: osText(err)})
}

// syncAll fsyncs every folder a step changed, once each. A sync error leaves
// the item intent and stops the action (D4 step 4), and a reconcile job of
// the source is enqueued to decide the item; ok is false then.
func (r *run) syncAll(folders ...*folder) (verdict, error, bool) {
	for i, f := range folders {
		if i > 0 && f == folders[0] {
			continue
		}
		if err := r.sync(f); err != nil {
			r.e.log.Error("executor: sync after a step failed; the item waits for reconciliation",
				"source", r.src, "folder", string(f.path), "err", err)
			if r.action == 0 {
				return halt, fmt.Errorf("executor: sync: %w", err), false
			}
			if werr := r.write(func(tx *jobs.Tx) error {
				if err := r.stop(tx); err != nil {
					return err
				}
				_, _, err := tx.EnqueueOnce(reconcileSpec(r.src))
				return err
			}); werr != nil {
				return halt, werr, false
			}
			return halt, nil, false
		}
	}
	return goOn, nil, true
}

// How settle was reached.
type settleMode int

const (
	settleConfirm    settleMode = iota // after a successful step, folders synced
	settleAfterError                   // after a step error (D5's last row)
	settleReconcile                    // an intent found at the start of an attempt (D4)
)

// settle decides an intent item by looking at both names (D4): clearly done
// records the outcome; clearly not done ends the item notDone (back to
// planned when reconciling); anything else is manual_recovery with the
// findings, which stops the running action except while reconciling. An
// error means the names could not be looked at; the item stays intent.
func (r *run) settle(it item, mode settleMode, notDone end) (verdict, error) {
	switch it.op {
	case opRecord:
		return r.settleRecord(it, mode, notDone)
	case opUnlink:
		return r.settleUnlink(it, mode, notDone)
	case opVerify:
		// r4 D4: verify only reads, so it runs again.
		return r.notDone(it, mode, notDone)
	case opPurge:
		// r4 D11: a purge is decided by reconcilePurge; its step never
		// settles.
		if mode == settleReconcile {
			return r.reconcilePurge(it)
		}
		return r.record(it, notDone)
	case opSetMtime:
		// r5 D13: one name, decided by its identity and time.
		return r.settleSetMtime(it, mode)
	}
	var (
		donev      bool
		from, to   string
		changedDir []*folder
		// prepare reads the facts the index update needs and returns that
		// update. It runs before the outcome transaction, never inside it:
		// the source root's facts reopen the source, which may record its
		// availability through the store's single writer (design V1).
		prepare func() func(tx *jobs.Tx) error
		close   []*folder
	)
	defer func() {
		for _, f := range close {
			f.close()
		}
	}()
	switch it.op {
	case opRename:
		var (
			ff, tf *folder
			info   fsaccess.EntryInfo
			err    error
		)
		from, ff, _, err = r.lookIn(it, it.fromParent, it.fromName)
		if err != nil {
			return halt, err
		}
		if ff != nil {
			close = append(close, ff)
		}
		to, tf, info, err = r.lookIn(it, it.toParent, it.toName)
		if err != nil {
			return halt, err
		}
		if tf != nil {
			close = append(close, tf)
		}
		switch {
		case from == foundAbsent && to == foundSame && ff != nil:
			donev, changedDir = true, []*folder{ff, tf}
			prepare = func() func(tx *jobs.Tx) error {
				oldFacts := r.folderFacts(ff)
				newFacts := oldFacts
				if it.toParent != it.fromParent {
					newFacts = r.folderFacts(tf)
				}
				m := index.Move{Source: r.src, Entry: domain.EntryID(it.entry), NewParent: domain.EntryID(it.toParent),
					NewName: it.toName, Facts: postFacts(info), OldParentFacts: oldFacts, NewParentFacts: newFacts}
				return func(tx *jobs.Tx) error { return r.e.idx.ApplyRename(r.bg, tx.SQL(), m) }
			}
		case from == foundSame && to == foundAbsent:
			return r.notDone(it, mode, notDone)
		}
	case opMkdir:
		from = foundAbsent
		found, pf, info, err := r.lookIn(it, it.toParent, it.toName)
		if err != nil {
			return halt, err
		}
		if pf != nil {
			close = append(close, pf)
		}
		to = found
		switch {
		case found == foundAbsent && pf != nil:
			return r.notDone(it, mode, notDone)
		case pf != nil && info.Kind == domain.EntryDirectory:
			empty, err := r.emptyFolder(pf, it.toName, info)
			if err != nil && !isDisk(err) {
				return halt, err
			}
			indexed, err := r.indexed(it.toPath)
			if err != nil {
				return halt, err
			}
			if empty && !indexed {
				donev, changedDir = true, []*folder{pf}
				prepare = func() func(tx *jobs.Tx) error {
					nf := index.NewFolder{Source: r.src, Parent: domain.EntryID(it.toParent), Name: it.toName,
						Facts: postFacts(info), ParentFacts: r.folderFacts(pf)}
					return func(tx *jobs.Tx) error {
						id, err := r.e.idx.ApplyMkdir(r.bg, tx.SQL(), nf)
						if err != nil {
							return err
						}
						_, err = tx.SQL().ExecContext(r.bg, `UPDATE action_items SET entry_id = ?, created = 1 WHERE id = ?`,
							int64(id), it.id)
						if err != nil || string(it.toPath) != index.QuarantineName {
							return err
						}
						// r4 D1: the quarantine folder a cleanup made.
						_, err = tx.SQL().ExecContext(r.bg, `UPDATE sources SET quarantine_entry_id = ? WHERE id = ?
							AND EXISTS (SELECT 1 FROM actions WHERE id = ? AND kind = 'cleanup')`, int64(id),
							string(r.src), it.action)
						return err
					}
				}
			} else {
				to = foundOther
			}
		}
	case opRmdir:
		to = foundAbsent
		found, pf, _, err := r.lookIn(it, it.fromParent, it.fromName)
		if err != nil {
			return halt, err
		}
		if pf != nil {
			close = append(close, pf)
		}
		from = found
		switch {
		case found == foundAbsent && pf != nil:
			donev, changedDir = true, []*folder{pf}
			prepare = func() func(tx *jobs.Tx) error {
				parentFacts := r.folderFacts(pf)
				return func(tx *jobs.Tx) error {
					return r.e.idx.ApplyRmdir(r.bg, tx.SQL(), r.src, domain.EntryID(it.entry), parentFacts)
				}
			}
		case found == foundSame:
			return r.notDone(it, mode, notDone)
		}
	}
	if !donev {
		return r.record(it, end{state: stateManualRecovery, detail: findings(from, to), stop: mode != settleReconcile})
	}
	switch mode {
	case settleReconcile:
		// A step found done is synced before it is recorded; when that
		// fails, the item stays intent and the attempt ends.
		for i, f := range changedDir {
			if i > 0 && f == changedDir[0] {
				continue
			}
			if err := r.sync(f); err != nil {
				return halt, fmt.Errorf("executor: sync %q: %w", f.path, err)
			}
		}
	case settleAfterError:
		if v, err, ok := r.syncAll(changedDir...); !ok {
			return v, err
		}
	}
	return r.outcome(it, prepare(), findings(from, to))
}

// notDone ends an item whose step did not happen: back to planned when
// reconciling (not_attempted when its action no longer runs), else notDone.
func (r *run) notDone(it item, mode settleMode, notDone end) (verdict, error) {
	if mode != settleReconcile {
		return r.record(it, notDone)
	}
	err := r.write(func(tx *jobs.Tx) error {
		_, err := tx.SQL().ExecContext(r.bg, `UPDATE action_items SET state = CASE WHEN (SELECT state FROM actions
				WHERE id = action_items.action_id) IN ('queued', 'running') THEN 'planned' ELSE 'not_attempted' END,
			finished_at = CASE WHEN (SELECT state FROM actions WHERE id = action_items.action_id) IN ('queued', 'running')
				THEN NULL ELSE ? END
			WHERE id = ? AND state = 'intent'`, clock.Millis(tx.Now()), it.id)
		return err
	})
	return goOn, err
}

// indexed reports whether the index has a row at path that is not missing.
func (r *run) indexed(path []byte) (bool, error) {
	var found bool
	err := r.e.st.Reader().QueryRowContext(r.bg, `SELECT EXISTS (SELECT 1 FROM entries WHERE source_id = ? AND path = ?
		AND state <> 'missing')`, string(r.src), path).Scan(&found)
	return found, err
}

// indexError marks a failure of the outcome transaction.
type indexError struct{ err error }

func (e *indexError) Error() string { return "executor: index update: " + e.err.Error() }
func (e *indexError) Unwrap() error { return e.err }

// outcome records a done step with its index update in one transaction (D4
// step 6), marks stale the pre-delete checks that relied on its paths (r4
// D10), and sets the original's reversed_by for an undo item. When that
// transaction fails, a second one ends the item manual_recovery with the
// findings, and the running action stops.
func (r *run) outcome(it item, apply func(tx *jobs.Tx) error, found string) (verdict, error) {
	err := r.write(func(tx *jobs.Tx) error {
		q := tx.SQL()
		res, err := q.ExecContext(r.bg, `UPDATE action_items SET state = 'done', reason = NULL, detail = NULL,
			finished_at = ? WHERE id = ? AND state = 'intent'`, clock.Millis(tx.Now()), it.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // decided meanwhile
		}
		if err := apply(tx); err != nil {
			return &indexError{err}
		}
		if err := r.markStale(tx, it, true); err != nil {
			return err
		}
		if it.reverses != 0 {
			if _, err := q.ExecContext(r.bg, `UPDATE action_items SET reversed_by = ? WHERE id = ? AND reversed_by IS NULL`,
				it.id, it.reverses); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return goOn, nil
	}
	r.e.log.Error("executor: recording a done step failed; the item needs a check", "item", it.id, "err", err)
	return r.recordUnsure(it, found)
}

// recordUnsure ends an item manual_recovery with the findings when its done
// step could not be recorded, and stops the action. The disk changed where
// the index cannot follow, so the checks relying on the step's paths are
// marked stale too.
func (r *run) recordUnsure(it item, found string) (verdict, error) {
	err := r.write(func(tx *jobs.Tx) error {
		if err := r.recordIn(tx, it, end{state: stateManualRecovery, detail: found, stop: true}); err != nil {
			return err
		}
		return r.markStale(tx, it, false)
	})
	return halt, err
}

// markStale marks stale, in tx, every check relying on a path of the step
// (r4 D10): its old and new paths, and extra. A purge's own check, ready
// before, stays ready when keepOwn: the step is what the check was for.
func (r *run) markStale(tx *jobs.Tx, it item, keepOwn bool, extra ...[]byte) error {
	q := tx.SQL()
	var own int64
	if keepOwn {
		err := q.QueryRowContext(r.bg, `SELECT c.id FROM actions a JOIN purge_checks c ON c.id = a.check_id
			WHERE a.id = ? AND a.kind = 'purge' AND c.state = 'ready'`, it.action).Scan(&own)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	for _, p := range append([][]byte{it.fromPath, it.toPath}, extra...) {
		if len(p) == 0 {
			continue
		}
		if err := stale.MarkStale(r.bg, q, r.src, p); err != nil {
			return err
		}
	}
	if own == 0 {
		return nil
	}
	_, err := q.ExecContext(r.bg, `UPDATE purge_checks SET state = 'ready', stale_reason = NULL
		WHERE id = ? AND state = 'stale'`, own)
	return err
}

// staleDiskChanged is the stale_reason of a check the disk no longer
// matches (r4 D10), found by verify or a purge step.
const staleDiskChanged = "disk_changed"

// markCheckStale marks the check stale for a difference on the disk.
func markCheckStale(ctx context.Context, q *sql.Tx, check int64) error {
	_, err := q.ExecContext(ctx, `UPDATE purge_checks SET state = 'stale', stale_reason = ?
		WHERE id = ? AND state IN ('running', 'ready')`, staleDiskChanged, check)
	return err
}

// reconcile settles every intent item of the source (D4), oldest first. It
// runs after the attempt's scan check and before any item.
func (r *run) reconcile() error {
	rows, err := r.e.st.Reader().QueryContext(r.ctx, `SELECT `+itemColumns+` FROM action_items i
		JOIN actions a ON a.id = i.action_id WHERE i.state = 'intent' AND a.source_id = ? ORDER BY i.action_id, i.seq`,
		string(r.src))
	if err != nil {
		return err
	}
	var open []item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return err
		}
		open = append(open, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	settled := false
	for _, it := range open {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if _, err := r.settle(it, settleReconcile, end{}); err != nil {
			return fmt.Errorf("executor: reconcile item %d: %w", it.id, err)
		}
		settled = true
	}
	if !settled {
		return nil
	}
	return r.write(func(tx *jobs.Tx) error {
		var done bool
		if err := tx.SQL().QueryRowContext(r.bg, `SELECT EXISTS (SELECT 1 FROM action_items i JOIN actions a
			ON a.id = i.action_id WHERE a.source_id = ? AND i.state = 'done' AND i.id IN (`+placeholders(open)+`))`,
			append([]any{string(r.src)}, ids(open)...)...).Scan(&done); err != nil {
			return err
		}
		if !done {
			return nil
		}
		return r.e.idx.ActionDone(r.bg, tx, r.src)
	})
}

func placeholders(items []item) string {
	b := make([]byte, 0, 2*len(items))
	for i := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}

func ids(items []item) []any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out
}
