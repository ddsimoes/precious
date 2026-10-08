package executor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
)

// R5's set_mtime step (r5 D12, D13): the modification time of one regular
// file outside the quarantine is set through its folder, without opening
// the file or following a link, and nothing else of it changes. The time the
// step finds on disk is journaled before the call, so an undo restores what
// the disk held. A file that changed since the index saw it, its change time
// included, or that has another hard link, is never written.

// intentSetMtime re-checks a set_mtime and resolves its record: the entry is
// a present regular file of the source, where the plan found it, outside the
// quarantine, with no other hard link in the index, the new time is one the
// index reads as known, from 1970-01-02 (r5 K3), and the source's
// filesystem stores as given (r5 H1; an undo's time, which that disk held,
// is not checked), and it differs from the index's under the source's
// capabilities. An undo item needs the index's time to still be the one the
// original wrote. The identity expected at the step, its change time
// included, is the index row's.
func (r *run) intentSetMtime(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	ent, ok, err := loadEntry(ctx, q, it.entry)
	if err != nil {
		return it, nil, err
	}
	if !ok || domain.SourceID(ent.source) != r.src || ent.state != "present" || ent.kind != string(domain.EntryFile) ||
		(it.fromParent != 0 && ent.parent != it.fromParent) || (it.fromName != nil && !bytes.Equal(ent.name, it.fromName)) {
		return it, &end{state: stateChanged}, nil
	}
	var refusal string
	if it.reverses == 0 && it.newMtime.Valid {
		refusal = timeRefusal(r.fsType, it.newMtime.Int64)
	}
	switch {
	case !it.newMtime.Valid:
		return it, &end{state: stateFailed, detail: "a set_mtime item without a time"}, nil
	case index.IsQuarantinePath(ent.path):
		return it, &end{state: stateRefused, reason: reasonInQuarantine}, nil
	case ent.nlink.Valid && ent.nlink.Int64 > 1:
		return it, &end{state: stateRefused, reason: reasonHardLink}, nil
	case refusal != "":
		return it, &end{state: stateRefused, reason: refusal}, nil
	case ent.mtime.Valid && sameTime(ent.mtime.Int64, it.newMtime.Int64, r.caps):
		return it, &end{state: stateRefused, reason: reasonNoChange}, nil
	}
	if it.reverses != 0 {
		var written sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT new_mtime_ns FROM action_items WHERE id = ? AND op = 'set_mtime'`,
			it.reverses).Scan(&written)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return it, nil, err
		}
		if !written.Valid || !ent.mtime.Valid || !stillWritten(written.Int64, ent.mtime.Int64, r.caps) {
			return it, &end{state: stateChanged, reason: reasonIdentityChanged}, nil
		}
	}
	rec := it
	rec.fromParent, rec.fromName, rec.fromPath = ent.parent, ent.name, ent.path
	rec.toParent, rec.toName, rec.toPath = 0, nil, nil
	rec.kind, rec.dev, rec.ino, rec.mtime, rec.ctime = ent.kind, ent.dev, ent.ino, ent.mtime, ent.ctime
	rec.size = sql.NullInt64{Int64: ent.size, Valid: true}
	return rec, nil, nil
}

// stillWritten reports whether the index's time is still the one a done
// set_mtime wrote: equal within the filesystem's resolution, which is all the
// disk may have rounded. The local-time filesystems' hour is no excuse here:
// a time an hour off was not written by Precious.
func stillWritten(written, indexed int64, caps fsaccess.Capabilities) bool {
	return abs(indexed-written) <= int64(caps.TimeResolution)
}

// timeRefusal is the reason a set_mtime may not write ns on a filesystem of
// type fsType, or "" when it may: date_before_1970 for a time the index
// reads back as unknown (domain.KnownModTime: before 1970-01-02), whatever
// the disk holds (r5 K3), and date_out_of_range for one the filesystem does
// not store as given, which Linux would clamp without an error (r5 H1).
// plan-set-mtime refuses the same times by the same test.
func timeRefusal(fsType string, ns int64) string {
	switch {
	case !domain.KnownModTime(ns):
		return reasonDateBefore1970
	case !fsaccess.StoresModTime(fsType, ns):
		return reasonDateOutOfRange
	}
	return ""
}

// stepSetMtime runs an intent set_mtime (r5 D13): lstat the name through its
// folder and compare it with the identity recorded at intent, change time
// included; journal the time found; set the new one; confirm. No folder is
// synced: a folder's fsync does not persist a file's times.
func (r *run) stepSetMtime(it item) (verdict, error) {
	f, err := r.openFolder(it.fromParent)
	if err != nil {
		return r.preflight(it, err)
	}
	defer f.close()
	info, err := r.lstat(f.dir, it.fromName)
	if err != nil {
		return r.preflight(it, err)
	}
	// A link made since the scan advanced the change time too; the link
	// count is checked first so the item says why it never writes.
	switch {
	case info.MountBoundary:
		return r.record(it, end{state: stateRefused, reason: reasonOtherFilesystem})
	case info.Kind == domain.EntryFile && info.Nlink > 1:
		return r.record(it, end{state: stateRefused, reason: reasonHardLink})
	case info.Kind != domain.EntryFile || !r.matches(it, info) || !r.sameChange(it, info):
		return r.record(it, end{state: stateChanged, reason: reasonIdentityChanged})
	}
	if err := r.write(func(tx *jobs.Tx) error {
		res, err := tx.SQL().ExecContext(r.bg, `UPDATE action_items SET prev_mtime_ns = ? WHERE id = ? AND state = 'intent'`,
			info.ModTime.UnixNano(), it.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("executor: set_mtime item left intent before its journal")
		}
		return nil
	}); err != nil {
		return halt, err
	}
	w, err := writer(f.dir)
	if err != nil {
		return r.failedSetMtime(it, err)
	}
	if err := r.before(it); err != nil {
		return halt, err
	}
	done := r.rt.FSCall("set_mtime")
	err = w.SetModTime(it.fromName, time.Unix(0, it.newMtime.Int64))
	done()
	if err := r.after(it); err != nil {
		return halt, err
	}
	if err != nil {
		return r.failedSetMtime(it, err)
	}
	return r.settle(it, settleConfirm, end{})
}

// sameChange reports whether info's change time is the one recorded at
// intent, when both are known (r5 D13): a file edited in place since the
// index saw it, even with its modification time put back, differs here.
func (r *run) sameChange(it item, info fsaccess.EntryInfo) bool {
	return !it.ctime.Valid || it.ctime.Int64 == 0 || info.Ctime.IsZero() ||
		sameTime(it.ctime.Int64, info.Ctime.UnixNano(), r.caps)
}

// failedSetMtime ends a set_mtime whose call the filesystem refused (r5
// D13). Ownership can differ by file, so not_owner ends that item only, with
// writes still on; a read-only filesystem stops the action, as for any other
// step. A set_mtime never ends no_safe_rename and never turns writes off.
func (r *run) failedSetMtime(it item, err error) (verdict, error) {
	switch {
	case errors.Is(err, fsaccess.ErrPermission):
		return r.record(it, end{state: stateFailed, reason: reasonNotOwner, detail: osText(err)})
	case errors.Is(err, fsaccess.ErrReadOnly):
		return r.record(it, end{state: stateFailed, detail: osText(err), stop: true})
	}
	if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeAbsent {
		return r.record(it, end{state: stateChanged})
	}
	return r.record(it, end{state: stateFailed, detail: osText(err)})
}

// settleSetMtime decides an intent set_mtime by looking at its name (r5
// D13): the recorded identity (kind, device and inode where stable, size)
// with the new time is done, and the index follows; with the old time, while
// reconciling, it goes back to planned and runs once; anything else, or the
// old time right after the call, is manual_recovery with the findings. An
// error means the name could not be looked at; the item stays intent.
func (r *run) settleSetMtime(it item, mode settleMode) (verdict, error) {
	found, f, info, err := r.lookIn(it, it.fromParent, it.fromName)
	if err != nil {
		return halt, err
	}
	if f != nil {
		defer f.close()
	}
	if found != foundAbsent {
		found = foundOther
		if f != nil && !info.MountBoundary && r.sameFile(it, info) {
			at := info.ModTime.UnixNano()
			isNew := sameTime(it.newMtime.Int64, at, r.caps)
			isOld := it.mtime.Valid && sameTime(it.mtime.Int64, at, r.caps)
			switch {
			case isNew && !isOld:
				m := index.ModTime{Source: r.src, Entry: domain.EntryID(it.entry), Facts: postFacts(info)}
				return r.outcome(it, func(tx *jobs.Tx) error { return r.e.idx.ApplyModTime(r.bg, tx.SQL(), m) },
					findings(foundSame, foundAbsent))
			case isOld && !isNew && mode == settleReconcile:
				return r.notDone(it, mode, end{})
			}
		}
	}
	return r.record(it, end{state: stateManualRecovery, detail: findings(found, foundAbsent),
		stop: mode != settleReconcile})
}

// sameFile reports whether info is the regular file the item recorded, by
// kind, device and inode where identity is stable, and size: its times are
// what the step changes, so they are compared apart.
func (r *run) sameFile(it item, info fsaccess.EntryInfo) bool {
	if info.Kind != domain.EntryFile || it.kind != string(domain.EntryFile) {
		return false
	}
	if r.caps.StableIdentity &&
		(!it.dev.Valid || !it.ino.Valid || uint64(it.dev.Int64) != info.Dev || uint64(it.ino.Int64) != info.Ino) {
		return false
	}
	return it.size.Valid && it.size.Int64 == info.Size
}
