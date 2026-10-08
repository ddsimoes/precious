package organize

import (
	"context"
	"database/sql"
	"fmt"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
)

// done is a done item of the action plan-undo reverses.
type done struct {
	id         int64
	op         string
	entry      int64
	fromParent int64
	fromName   []byte
	fromPath   []byte
}

// planUndo plans plan-undo (r3 design D11): the reverse of the action's done
// items that no undo has reversed yet, in reverse seq order. A rename goes
// back from wherever its entry is now to its previous folder and name; a
// folder the action created is removed. Either is refused in_quarantine
// while its entry is in the quarantine (r4 D13). An item whose previous name is
// taken, or whose previous folder is gone, is a conflict, unless
// destination_id names a folder for those items, which then go there under
// their previous names. An undo is an individual action: it may take an
// effective keep away, which kept_lost counts (D14).
func (s *Service) planUndo(ctx context.Context, tx *jobs.Tx, req planUndoRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	action, err := parseID("action", req.ActionID)
	if err != nil {
		return 0, nil, err
	}
	state, src, _, err := actionState(ctx, q, action)
	if err != nil {
		return 0, nil, err
	}
	if state != "done" && state != "stopped" {
		return 0, nil, domain.Errorf(domain.CodeActionNotUndoable, "action %d is %s; only a change that ran can be undone",
			action, state)
	}
	var kind string
	if err := q.QueryRowContext(ctx, `SELECT kind FROM actions WHERE id = ?`, action).Scan(&kind); err != nil {
		return 0, nil, err
	}
	if !undoable(kind) {
		return 0, nil, domain.Errorf(domain.CodeActionNotUndoable,
			"a %s is not undone: a cleanup is reversed by restoring its items, and a purge cannot be reversed", kind)
	}
	var alt *dest
	if req.DestinationID != "" {
		d, err := folderArg(ctx, q, "destination_id", req.DestinationID)
		if err != nil {
			return 0, nil, err
		}
		if d.source != src {
			return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "the destination is on another source than the change")
		}
		fd := folderDest(d)
		alt = &fd
	}
	items, err := reversible(ctx, q, action)
	if err != nil {
		return 0, nil, err
	}
	if len(items) == 0 {
		return 0, nil, domain.Errorf(domain.CodeActionNotUndoable, "action %d has nothing left to undo", action)
	}
	if err := s.checkSource(ctx, q, src); err != nil {
		return 0, nil, err
	}
	if err := Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, src, false)
	if err != nil {
		return 0, nil, err
	}
	for _, it := range items {
		n, err := loadNode(ctx, q, it.entry)
		if err != nil {
			return 0, nil, err
		}
		if n == nil {
			continue // gone from the index since: nothing to reverse
		}
		if it.op == opMkdir {
			rm := &item{op: opRmdir, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
				state: statePlanned, reverses: it.id}
			switch {
			case n.source != src || n.state == "missing" || !n.dir():
				rm.state, rm.reason = stateRefused, reasonMissing
			case index.IsQuarantinePath(n.path):
				// A created folder since quarantined is restored or purged,
				// never removed by an undo (r4 D13).
				rm.state, rm.reason = stateRefused, reasonInQuarantine
			}
			if err := p.push(rm); err != nil {
				return 0, nil, err
			}
			continue
		}
		if err := p.reverse(n, it, alt); err != nil {
			return 0, nil, err
		}
	}
	if len(p.items) == 0 {
		return 0, nil, domain.Errorf(domain.CodeActionNotUndoable, "action %d has nothing left to undo", action)
	}
	var destination int64
	if alt != nil {
		destination = alt.id
	}
	return s.finish(ctx, tx, p, "undo", destination, action)
}

// reverse plans the rename of n back to the previous place of done item it,
// or into alt under its previous name when that place is taken or gone. A
// quarantined entry is refused, and a previous folder in the quarantine is
// gone (r4 D13).
func (p *plan) reverse(n *node, it done, alt *dest) error {
	if index.IsQuarantinePath(n.path) {
		return p.refused(&item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			toName: it.fromName, toPath: it.fromPath, bytes: n.bytes, files: n.files, reverses: it.id},
			reasonInQuarantine)
	}
	var prev *node
	if it.fromParent != 0 {
		var err error
		if prev, err = loadNode(p.ctx, p.tx, it.fromParent); err != nil {
			return err
		}
	}
	if prev == nil || prev.source != p.src || !prev.presentFolder() || index.IsQuarantinePath(prev.path) {
		if alt != nil {
			_, err := p.move(n, *alt, it.fromName, it.id)
			return err
		}
		return p.push(&item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			toName: it.fromName, toPath: it.fromPath, bytes: n.bytes, files: n.files, state: stateConflict,
			reason: reasonPreviousFolderGone, reverses: it.id})
	}
	d := folderDest(prev)
	if alt != nil && p.refusal(n, d, it.fromName) == "" {
		r, err := p.conflict(n, d, it.fromName)
		if err != nil {
			return err
		}
		if r != "" {
			_, err := p.move(n, *alt, it.fromName, it.id)
			return err
		}
	}
	_, err := p.move(n, d, it.fromName, it.id)
	return err
}

// reversible returns the done items of action that no undo reversed yet
// and that an undo can reverse, in reverse seq order: renames, and the
// folders the action created.
func reversible(ctx context.Context, tx *sql.Tx, action int64) ([]done, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, op, entry_id, from_parent, from_name, from_path FROM action_items
		WHERE action_id = ? AND state = 'done' AND reversed_by IS NULL AND entry_id IS NOT NULL
			AND (op = 'rename' OR op = 'mkdir' AND created = 1)
		ORDER BY seq DESC`, action)
	if err != nil {
		return nil, fmt.Errorf("organize: read done items: %w", err)
	}
	defer rows.Close()
	var out []done
	for rows.Next() {
		var (
			d          done
			fromParent sql.NullInt64
		)
		if err := rows.Scan(&d.id, &d.op, &d.entry, &fromParent, &d.fromName, &d.fromPath); err != nil {
			return nil, err
		}
		d.fromParent = fromParent.Int64
		out = append(out, d)
	}
	return out, rows.Err()
}

// undoable reports whether an action of kind can be undone: a cleanup is
// reversed by restoring its items, and a restore or a purge has no undo
// (r4 D13).
func undoable(kind string) bool {
	switch kind {
	case "cleanup", "restore", "purge":
		return false
	}
	return true
}
