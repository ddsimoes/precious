package executor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/sources"
)

// Item states (D4, D5).
const (
	statePlanned        = "planned"
	stateRefused        = "refused"
	stateConflict       = "conflict"
	stateIntent         = "intent"
	stateDone           = "done"
	stateNotPermitted   = "not_permitted"
	stateOffline        = "offline"
	stateChanged        = "changed"
	stateFailed         = "failed"
	stateNoSafeRename   = "no_safe_rename"
	stateNotEmpty       = "not_empty"
	stateManualRecovery = "manual_recovery"
	stateNotAttempted   = "not_attempted"
)

// Item reasons the executor records (the item JSON's reason).
const (
	reasonIntoItself         = "into_itself"
	reasonOtherFilesystem    = "other_filesystem"
	reasonContainsMount      = "contains_mount"
	reasonNameTaken          = "name_taken"
	reasonNameTakenByMissing = "name_taken_by_missing"
	reasonWouldLoseKeep      = "would_lose_keep"
	reasonAlreadyUndone      = "already_undone"
)

// Item ops.
const (
	opRename = "rename"
	opMkdir  = "mkdir"
	opRmdir  = "rmdir"
)

// Audit event written when the executor turns a source's writes off (D2):
// the kind set-source-writes writes, with the same detail and a reason.
const (
	auditSourceWritesSet = "source_writes_set"
	auditActor           = "system"
	auditReason          = "no_replace_rename"
)

// item is one action_items row. IDs are 0 when NULL.
type item struct {
	id, action, seq int64
	op              string
	entry           int64
	fromParent      int64
	fromName        []byte
	fromPath        []byte
	toParent        int64
	toDirSeq        int64
	toName          []byte
	toPath          []byte
	kind            string
	dev, ino        sql.NullInt64
	size, mtime     sql.NullInt64
	reverses        int64
	state           string
}

const itemColumns = `i.id, i.action_id, i.seq, i.op, i.entry_id, i.from_parent, i.from_name, i.from_path,
	i.to_parent, i.to_dir_seq, i.to_name, i.to_path, i.kind, i.dev, i.ino, i.size, i.mtime_ns, i.reverses, i.state`

func scanItem(s interface{ Scan(...any) error }) (item, error) {
	var (
		it                                              item
		entry, fromParent, toParent, toDirSeq, reverses sql.NullInt64
		kind                                            sql.NullString
	)
	err := s.Scan(&it.id, &it.action, &it.seq, &it.op, &entry, &fromParent, &it.fromName, &it.fromPath,
		&toParent, &toDirSeq, &it.toName, &it.toPath, &kind, &it.dev, &it.ino, &it.size, &it.mtime, &reverses,
		&it.state)
	it.entry, it.fromParent, it.toParent = entry.Int64, fromParent.Int64, toParent.Int64
	it.toDirSeq, it.reverses, it.kind = toDirSeq.Int64, reverses.Int64, kind.String
	return it, err
}

// nextPlanned returns the action's planned item with the lowest seq.
func (r *run) nextPlanned() (item, bool, error) {
	it, err := scanItem(r.e.st.Reader().QueryRowContext(r.ctx, `SELECT `+itemColumns+` FROM action_items i
		WHERE i.action_id = ? AND i.state = 'planned' ORDER BY i.seq LIMIT 1`, r.action))
	if errors.Is(err, sql.ErrNoRows) {
		return item{}, false, nil
	}
	return it, err == nil, err
}

// endItem records an item's end, from planned or intent.
func endItem(ctx context.Context, tx *sql.Tx, id int64, state, reason, detail string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE action_items SET state = ?, reason = ?, detail = ?, finished_at = ?
		WHERE id = ? AND state IN ('planned', 'intent')`, state, nullString(reason), nullString(detail),
		clock.Millis(now), id)
	return err
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// end is an item's end decided before or after its step.
type end struct {
	state, reason, detail string
	stop                  bool // the action stops
	writesOff             bool // turn the source's writes off (no_safe_rename)
}

// record ends the item with e in its own transaction, stopping the action
// when e says so.
func (r *run) record(it item, e end) (verdict, error) {
	err := r.write(func(tx *jobs.Tx) error { return r.recordIn(tx, it, e) })
	if err != nil {
		return halt, err
	}
	if e.stop {
		return halt, nil
	}
	return goOn, nil
}

func (r *run) recordIn(tx *jobs.Tx, it item, e end) error {
	if err := endItem(r.bg, tx.SQL(), it.id, e.state, e.reason, e.detail, tx.Now()); err != nil {
		return err
	}
	if e.writesOff {
		if err := r.writesOff(tx); err != nil {
			return err
		}
	}
	if e.stop && r.action != 0 {
		return r.stop(tx)
	}
	return nil
}

// writesOff turns the source's write permission off with the audit event
// set-source-writes writes (D2): its filesystem refused the no-replace flag.
func (r *run) writesOff(tx *jobs.Tx) error {
	res, err := tx.SQL().ExecContext(r.bg, `UPDATE sources SET write_enabled = 0 WHERE id = ? AND write_enabled = 1`,
		string(r.src))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	r.e.log.Warn("executor: the filesystem refused the no-replace rename; writes turned off", "source", r.src)
	return auth.WriteAudit(r.bg, tx.SQL(), auth.AuditEvent{At: tx.Now(), Kind: auditSourceWritesSet, Actor: auditActor,
		Detail: map[string]any{"source_id": r.src, "enabled": false, "previous_enabled": true, "reason": auditReason}})
}

// item runs one planned item: intent, then its step.
func (r *run) item(it item) (verdict, error) {
	it, v, err := r.intent(it)
	if err != nil || v != goOn || it.state != stateIntent {
		return v, err
	}
	switch it.op {
	case opRename:
		return r.stepRename(it)
	case opMkdir:
		return r.stepMkdir(it)
	default:
		return r.stepRmdir(it)
	}
}

// entryRow is the index's view of an entry at intent.
type entryRow struct {
	id, parent   int64
	source       string
	name, path   []byte
	kind, state  string
	dev, ino     sql.NullInt64
	size         int64
	mtime        sql.NullInt64
	boundary     bool
	decision     sql.NullString
	eff          string
	mountsInside int64
}

func loadEntry(ctx context.Context, q *sql.Tx, id int64) (entryRow, bool, error) {
	var (
		e      entryRow
		parent sql.NullInt64
		mounts sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT e.id, e.parent_id, e.source_id, e.name, e.path, e.kind, e.state, e.dev,
			e.ino, e.size, e.mtime_ns, e.mount_boundary, e.decision, e.eff_decision, d.mount_boundaries
		FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id WHERE e.id = ?`, id).
		Scan(&e.id, &parent, &e.source, &e.name, &e.path, &e.kind, &e.state, &e.dev, &e.ino, &e.size, &e.mtime,
			&e.boundary, &e.decision, &e.eff, &mounts)
	if errors.Is(err, sql.ErrNoRows) {
		return entryRow{}, false, nil
	}
	e.parent, e.mountsInside = parent.Int64, mounts.Int64
	return e, err == nil, err
}

// childPath is the index path of name inside the folder at parent.
func childPath(parent, name []byte) []byte {
	if len(parent) == 0 {
		return bytes.Clone(name)
	}
	p := make([]byte, 0, len(parent)+1+len(name))
	p = append(p, parent...)
	p = append(p, '/')
	return append(p, name...)
}

// intent re-checks a planned item and records it intent (design
// Concurrency, per item). The returned item holds what was recorded; an item
// that ended here comes back in its end state.
func (r *run) intent(it item) (item, verdict, error) {
	var (
		v    = goOn
		wait bool
		out  = it
	)
	err := r.e.runner.Write(r.ctx, func(tx *jobs.Tx) error {
		v, wait, out = goOn, false, it
		q := tx.SQL()
		ctx := r.ctx
		ends := func(e end) error {
			out.state = e.state
			if e.stop {
				v = halt
			}
			return r.recordIn(tx, it, e)
		}
		if err := sources.CheckWrites(ctx, q, r.src, r.e.allowWrites); err != nil {
			switch domain.CodeOf(err) {
			case domain.CodeWritesUnavailable, domain.CodeWritesDisabled:
				return ends(end{state: stateNotPermitted, stop: true})
			case domain.CodeSourceOffline, domain.CodeUnknownSource:
				return ends(end{state: stateOffline, stop: true})
			}
			return err
		}
		scanning, err := scanRunning(ctx, q, r.src)
		if err != nil {
			return err
		}
		if scanning {
			wait = true
			return nil
		}
		var actionState, itemState string
		if err := q.QueryRowContext(ctx, `SELECT a.state, i.state FROM action_items i JOIN actions a ON a.id = i.action_id
			WHERE i.id = ?`, it.id).Scan(&actionState, &itemState); err != nil {
			return err
		}
		if actionState != actionRunning {
			v = leave
			return nil
		}
		if itemState != statePlanned {
			out.state = itemState
			return nil
		}
		var recovery bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM action_items i JOIN actions a ON a.id = i.action_id
			WHERE i.state = 'manual_recovery' AND a.source_id = ?)`, string(r.src)).Scan(&recovery); err != nil {
			return err
		}
		if recovery {
			return ends(end{state: stateNotAttempted, stop: true})
		}
		if it.reverses != 0 {
			var reversedBy sql.NullInt64
			err := q.QueryRowContext(ctx, `SELECT reversed_by FROM action_items WHERE id = ?`, it.reverses).
				Scan(&reversedBy)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if reversedBy.Valid {
				return ends(end{state: stateChanged, reason: reasonAlreadyUndone})
			}
		}
		var rec item
		var e *end
		switch it.op {
		case opRename:
			rec, e, err = r.intentRename(ctx, q, it)
		case opMkdir:
			rec, e, err = r.intentMkdir(ctx, q, it)
		case opRmdir:
			rec, e, err = r.intentRmdir(ctx, q, it)
		default:
			e = &end{state: stateFailed, detail: "unknown step " + it.op}
		}
		if err != nil {
			return err
		}
		if e != nil {
			return ends(*e)
		}
		res, err := q.ExecContext(ctx, `UPDATE action_items SET state = 'intent', entry_id = ?, from_parent = ?,
			from_name = ?, from_path = ?, to_parent = ?, to_name = ?, to_path = ?, kind = ?, dev = ?, ino = ?,
			size = ?, mtime_ns = ?
			WHERE id = ? AND state = 'planned'`, nullID(rec.entry), nullID(rec.fromParent), rec.fromName,
			rec.fromPath, nullID(rec.toParent), rec.toName, rec.toPath, nullString(rec.kind), rec.dev, rec.ino,
			rec.size, rec.mtime, it.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("executor: item changed under its intent")
		}
		rec.state = stateIntent
		out = rec
		return nil
	})
	if err != nil {
		if r.ctx.Err() != nil {
			return it, halt, r.interrupted(r.ctx.Err())
		}
		return it, halt, err
	}
	if wait {
		return it, halt, r.deferral(deferScan)
	}
	return out, v, nil
}

// destination resolves the folder an item goes into: to_parent, or the
// folder its to_dir_seq item made. ok is false when it is no longer a
// present folder of the source.
func (r *run) destination(ctx context.Context, q *sql.Tx, it item) (entryRow, bool, error) {
	id := it.toParent
	if id == 0 && it.toDirSeq != 0 {
		var (
			state string
			made  sql.NullInt64
		)
		err := q.QueryRowContext(ctx, `SELECT state, entry_id FROM action_items WHERE action_id = ? AND seq = ?`,
			it.action, it.toDirSeq).Scan(&state, &made)
		if errors.Is(err, sql.ErrNoRows) {
			return entryRow{}, false, nil
		}
		if err != nil {
			return entryRow{}, false, err
		}
		if state != stateDone || !made.Valid {
			return entryRow{}, false, nil
		}
		id = made.Int64
	}
	if id == 0 {
		return entryRow{}, false, nil
	}
	d, ok, err := loadEntry(ctx, q, id)
	if err != nil || !ok {
		return entryRow{}, false, err
	}
	if domain.SourceID(d.source) != r.src || d.state != "present" || d.kind != "directory" {
		return entryRow{}, false, nil
	}
	return d, true, nil
}

// intentRename re-checks a rename (move or rename) and resolves its record.
func (r *run) intentRename(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	changed := &end{state: stateChanged}
	ent, ok, err := loadEntry(ctx, q, it.entry)
	if err != nil {
		return it, nil, err
	}
	if !ok || domain.SourceID(ent.source) != r.src || ent.state != "present" ||
		(it.fromParent != 0 && ent.parent != it.fromParent) || (it.fromName != nil && !bytes.Equal(ent.name, it.fromName)) {
		return it, changed, nil
	}
	dest, ok, err := r.destination(ctx, q, it)
	if err != nil || !ok {
		return it, changed, err
	}
	toPath := childPath(dest.path, it.toName)
	switch {
	case ent.boundary:
		return it, &end{state: stateRefused, reason: reasonOtherFilesystem}, nil
	case ent.kind == "directory" && ent.mountsInside > 0:
		return it, &end{state: stateRefused, reason: reasonContainsMount}, nil
	case ent.kind == "directory" && (bytes.Equal(toPath, ent.path) || bytes.HasPrefix(toPath, append(bytes.Clone(ent.path), '/'))):
		return it, &end{state: stateRefused, reason: reasonIntoItself}, nil
	}
	taken, err := r.e.idx.MissingIntentAt(ctx, q, r.src, toPath)
	if err != nil {
		return it, nil, err
	}
	if taken {
		return it, &end{state: stateConflict, reason: reasonNameTakenByMissing}, nil
	}
	// D14: the decision after the move, re-derived from the destination's
	// current effective decision. A move never writes a decision.
	var after sql.NullString
	if !ent.decision.Valid && dest.eff != ent.eff {
		after = sql.NullString{String: dest.eff, Valid: true}
		if r.bulk && ent.eff == "keep" {
			return it, &end{state: stateChanged, reason: reasonWouldLoseKeep}, nil
		}
	}
	if _, err := q.ExecContext(ctx, `UPDATE action_items SET decision_after = ? WHERE id = ?`, after, it.id); err != nil {
		return it, nil, err
	}
	rec := it
	rec.fromParent, rec.fromName, rec.fromPath = ent.parent, ent.name, ent.path
	rec.toParent, rec.toPath = dest.id, toPath
	rec.kind, rec.dev, rec.ino, rec.mtime = ent.kind, ent.dev, ent.ino, ent.mtime
	rec.size = sql.NullInt64{Int64: ent.size, Valid: true}
	return rec, nil, nil
}

// intentMkdir re-checks a new folder and resolves its record.
func (r *run) intentMkdir(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	dest, ok, err := r.destination(ctx, q, it)
	if err != nil || !ok {
		return it, &end{state: stateChanged}, err
	}
	toPath := childPath(dest.path, it.toName)
	taken, err := r.e.idx.MissingIntentAt(ctx, q, r.src, toPath)
	if err != nil {
		return it, nil, err
	}
	if taken {
		return it, &end{state: stateConflict, reason: reasonNameTakenByMissing}, nil
	}
	rec := it
	rec.toParent, rec.toPath, rec.kind = dest.id, toPath, "directory"
	rec.dev, rec.ino, rec.size, rec.mtime = sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}
	return rec, nil, nil
}

// intentRmdir re-checks the removal of a folder the action's original made
// and resolves its record.
func (r *run) intentRmdir(ctx context.Context, q *sql.Tx, it item) (item, *end, error) {
	ent, ok, err := loadEntry(ctx, q, it.entry)
	if err != nil {
		return it, nil, err
	}
	if !ok || domain.SourceID(ent.source) != r.src || ent.state != "present" || ent.kind != "directory" ||
		(it.fromParent != 0 && ent.parent != it.fromParent) || (it.fromName != nil && !bytes.Equal(ent.name, it.fromName)) {
		return it, &end{state: stateChanged}, nil
	}
	var children bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE parent_id = ? AND state <> 'missing')`,
		ent.id).Scan(&children); err != nil {
		return it, nil, err
	}
	kept, err := r.e.idx.MissingIntentAt(ctx, q, r.src, ent.path)
	if err != nil {
		return it, nil, err
	}
	if children || kept {
		return it, &end{state: stateNotEmpty}, nil
	}
	rec := it
	rec.fromParent, rec.fromName, rec.fromPath = ent.parent, ent.name, ent.path
	rec.kind, rec.dev, rec.ino, rec.mtime = ent.kind, ent.dev, ent.ino, ent.mtime
	rec.size = sql.NullInt64{Int64: ent.size, Valid: true}
	return rec, nil, nil
}
