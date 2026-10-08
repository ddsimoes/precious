package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/organize"
	"precious/internal/store"
)

// planRestore plans plan-restore (D6, E4, E7): for each quarantined top item
// named (or every one of a plan folder), the rename back to its original
// folder and name, the unlink of its origin record, and the rmdir of its
// <seq> folder; then the sweep of each plan folder every item leaves. An
// item whose original name is taken, or whose original folder is gone, not
// a present folder, or in the quarantine, or whose origin is unknown, goes
// into destination_id under its original name when one is given, and is a
// conflict otherwise. A restore never merges or overwrites: the executor's
// rename is no-replace.
func (s *Service) planRestore(ctx context.Context, tx *jobs.Tx, req planRestoreRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	var (
		tops []*topItem
		err  error
	)
	if req.PlanID != "" {
		tops, err = planItems(ctx, q, req.PlanID)
	} else {
		tops, err = topItems(ctx, q, "entry_ids", req.EntryIDs)
	}
	if err != nil {
		return 0, nil, err
	}
	src := tops[0].n.source
	var alt *node
	if req.DestinationID != "" {
		if alt, err = destinationArg(ctx, q, req.DestinationID); err != nil {
			return 0, nil, err
		}
		if alt.source != src {
			return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "the destination is on another source than the items")
		}
	}
	if err := organize.CheckSource(ctx, q, src, s.allowWrites); err != nil {
		return 0, nil, err
	}
	if err := organize.Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	sensitive, err := caseSensitive(ctx, q, src)
	if err != nil {
		return 0, nil, err
	}
	slices.SortFunc(tops, func(a, b *topItem) int { return bytes.Compare(a.n.path, b.n.path) })
	r := &restorer{ctx: ctx, q: q, src: src, sensitive: sensitive, alt: alt, names: map[int64]*folderNames{}}
	var steps []*step
	leaving := map[int64]map[int64]bool{} // plan folder → the <seq> folders whose item leaves
	for _, t := range tops {
		ren, err := r.restore(t)
		if err != nil {
			return 0, nil, err
		}
		steps = append(steps, ren)
		if ren.state != statePlanned {
			continue
		}
		rec := append(bytes.Clone(t.seq.name), ".json"...)
		steps = append(steps,
			&step{op: opUnlink, fromParent: t.plan.id, fromName: rec, fromPath: joinPath(t.plan.path, rec),
				state: statePlanned},
			&step{op: opRmdir, entry: t.seq.id, fromParent: t.plan.id, fromName: t.seq.name, fromPath: t.seq.path,
				state: statePlanned})
		if leaving[t.plan.id] == nil {
			leaving[t.plan.id] = map[int64]bool{}
		}
		leaving[t.plan.id][t.seq.id] = true
	}
	sweeps, err := sweepAll(ctx, q, tops, leaving)
	if err != nil {
		return 0, nil, err
	}
	steps = append(steps, sweeps...)
	if len(steps) > maxSteps {
		return 0, nil, tooMany(len(steps))
	}
	id, err := insertAction(ctx, tx, action{kind: kindRestore, src: src, bulk: true, destination: idOf(alt),
		ttl: restoreTTL})
	if err != nil {
		return 0, nil, err
	}
	if err := insertSteps(ctx, q, id, steps); err != nil {
		return 0, nil, err
	}
	res, err := organize.ReadPlan(ctx, q, id, now)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, res, nil
}

func idOf(n *node) int64 {
	if n == nil {
		return 0
	}
	return n.id
}

// planItems returns the quarantined top items of the plan folder named
// planID: not_found when no source's quarantine holds such a folder,
// invalid_request when it holds no item.
func planItems(ctx context.Context, q store.Queryer, planID string) ([]*topItem, error) {
	if _, err := strconv.ParseInt(planID, 10, 64); err != nil {
		return nil, notFound("plan", planID)
	}
	rows, err := q.QueryContext(ctx, `SELECT e.id FROM sources s JOIN entries qf ON qf.id = s.quarantine_entry_id
		JOIN entries p ON p.parent_id = qf.id AND p.name = ? AND p.kind = 'directory'
		JOIN entries sq ON sq.parent_id = p.id AND sq.kind = 'directory'
		JOIN entries e ON e.parent_id = sq.id AND e.state <> 'missing'
		ORDER BY e.path`, []byte(planID))
	if err != nil {
		return nil, fmt.Errorf("cleanup: items of plan %s: %w", planID, err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		var known bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sources s JOIN entries p
			ON p.parent_id = s.quarantine_entry_id AND p.name = ?)`, []byte(planID)).Scan(&known); err != nil {
			return nil, err
		}
		if !known {
			return nil, notFound("plan", planID)
		}
		return nil, domain.Errorf(domain.CodeInvalidRequest, "plan %s has no item left in the quarantine", planID)
	}
	out := make([]*topItem, 0, len(ids))
	for _, id := range ids {
		t, err := loadTop(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		if len(out) > 0 && t.n.source != out[0].n.source {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "plan %s is on more than one source", planID)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "plan %s has no item left in the quarantine", planID)
	}
	return out, nil
}

// destinationArg loads the destination of a restore: unknown is not_found;
// anything but a present folder, or a folder in the quarantine, is
// invalid_request (D6).
func destinationArg(ctx context.Context, q store.Queryer, s string) (*node, error) {
	id, err := parseEntryID("destination_id", s)
	if err != nil {
		return nil, err
	}
	n, err := loadNode(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, notFound("destination_id", s)
	}
	if !n.presentFolder() {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "destination_id %s is not a folder present on its disk", s)
	}
	if index.IsQuarantinePath(n.path) {
		return nil, domain.Errorf(domain.CodeInvalidRequest,
			"destination_id %s is in the quarantine; choose a folder outside it", s)
	}
	return n, nil
}

// caseSensitive reads whether names on src differ by letter case, from its
// recorded capabilities.
func caseSensitive(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT capabilities FROM sources WHERE id = ?`, string(src)).Scan(&raw); err != nil {
		return false, fmt.Errorf("cleanup: capabilities of %q: %w", src, err)
	}
	var caps fsaccess.Capabilities
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return false, fmt.Errorf("cleanup: capabilities of %q: %w", src, err)
	}
	return caps.CaseSensitive, nil
}

// folderNames are the names in one folder: those of its entries that are
// not missing, and those the plan's items take.
type folderNames struct {
	present, planned [][]byte
}

// restorer plans the renames of a restore.
type restorer struct {
	ctx       context.Context
	q         store.Queryer
	src       domain.SourceID
	sensitive bool
	alt       *node
	names     map[int64]*folderNames
}

func (r *restorer) same(a, b []byte) bool {
	if r.sensitive {
		return bytes.Equal(a, b)
	}
	return bytes.EqualFold(a, b)
}

// namesIn returns the names in folder f, read once.
func (r *restorer) namesIn(f *node) (*folderNames, error) {
	if fn, ok := r.names[f.id]; ok {
		return fn, nil
	}
	fn := &folderNames{}
	rows, err := r.q.QueryContext(r.ctx, `SELECT name FROM entries WHERE parent_id = ? AND state <> 'missing'`, f.id)
	if err != nil {
		return nil, fmt.Errorf("cleanup: names in folder %d: %w", f.id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name []byte
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		fn.present = append(fn.present, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	r.names[f.id] = fn
	return fn, nil
}

// taken is why name is taken in folder f, or "": an entry there, another
// item of the plan, or a missing entry there with the owner's intent.
func (r *restorer) taken(f *node, name []byte) (string, error) {
	fn, err := r.namesIn(f)
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(fn.present, func(n []byte) bool { return r.same(n, name) }) {
		return reasonNameTaken, nil
	}
	if slices.ContainsFunc(fn.planned, func(n []byte) bool { return r.same(n, name) }) {
		return reasonNameTakenInPlan, nil
	}
	missing, err := index.MissingIntentAt(r.ctx, r.q, r.src, joinPath(f.path, name))
	if err != nil {
		return "", err
	}
	if missing {
		return reasonNameTakenByMissing, nil
	}
	return "", nil
}

// restore plans the rename of the top item t: back to its original place,
// or into the destination when that place is taken or gone (or unknown).
func (r *restorer) restore(t *topItem) (*step, error) {
	n := t.n
	ren := &step{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
		toName: n.name, bytes: n.bytes, files: n.files, state: statePlanned}
	o, err := originOf(r.ctx, r.q, n)
	if err != nil {
		return nil, err
	}
	var prev *node
	if o != nil {
		ren.toName, ren.toPath = o.fromName, o.fromPath
		if o.fromParent != 0 {
			if prev, err = loadNode(r.ctx, r.q, o.fromParent); err != nil {
				return nil, err
			}
		}
	}
	reason := ""
	if prev == nil || prev.source != r.src || !prev.presentFolder() || index.IsQuarantinePath(prev.path) {
		reason = reasonPreviousFolderGone
	} else if reason, err = r.into(ren, prev); err != nil {
		return nil, err
	}
	if reason == "" {
		return ren, nil
	}
	if r.alt != nil {
		if reason, err = r.into(ren, r.alt); err != nil {
			return nil, err
		}
		if reason == "" {
			return ren, nil
		}
	}
	ren.toParent = 0
	ren.state, ren.reason = stateConflict, reason
	if reason == reasonReservedName {
		ren.state = stateRefused
	}
	return ren, nil
}

// into plans ren into folder f under its name, or returns why it cannot go
// there.
func (r *restorer) into(ren *step, f *node) (string, error) {
	if len(f.path) == 0 && string(ren.toName) == index.QuarantineName {
		return reasonReservedName, nil
	}
	why, err := r.taken(f, ren.toName)
	if err != nil || why != "" {
		return why, err
	}
	ren.toParent, ren.toPath = f.id, joinPath(f.path, ren.toName)
	fn, err := r.namesIn(f)
	if err != nil {
		return "", err
	}
	fn.planned = append(fn.planned, ren.toName)
	return "", nil
}

// sweepAll plans the sweep of each plan folder of tops that every top item
// below it leaves in this action, as leaving says (D6, D11).
func sweepAll(ctx context.Context, q store.Queryer, tops []*topItem, leaving map[int64]map[int64]bool) ([]*step, error) {
	var (
		out  []*step
		done = map[int64]bool{}
	)
	for _, t := range tops {
		if done[t.plan.id] || leaving[t.plan.id] == nil {
			continue
		}
		done[t.plan.id] = true
		steps, err := sweep(ctx, q, t.plan, t.quarantine, leaving[t.plan.id])
		if err != nil {
			return nil, err
		}
		out = append(out, steps...)
	}
	return out, nil
}

// sweep plans the sweep of plan folder p when every top item below it is
// in leaving (by its <seq> folder), whose own steps remove their <seq>
// folder and record: the rmdir of every other <seq> folder holding nothing,
// the unlink of every other record (one the cleanup wrote, or one a scan
// indexed) whose item is gone, and the rmdir of p. Anything else in p keeps
// p, which is then left alone.
func sweep(ctx context.Context, q store.Queryer, p *node, quarantine int64, leaving map[int64]bool) ([]*step, error) {
	children, err := loadNodes(ctx, q, `SELECT `+nodeCols+` FROM `+nodeFrom+` WHERE e.parent_id = ?
		AND e.state <> 'missing' ORDER BY e.path`, p.id)
	if err != nil {
		return nil, err
	}
	handled := map[string]bool{} // the records the items' own steps remove
	for _, c := range children {
		if leaving[c.id] {
			handled[string(c.name)+".json"] = true
		}
	}
	var (
		out     []*step
		records = map[string]bool{}
		keep    bool
	)
	for _, c := range children {
		switch {
		case leaving[c.id]:
		case c.dir():
			var held bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE parent_id = ?
				AND state <> 'missing')`, c.id).Scan(&held); err != nil {
				return nil, err
			}
			if held {
				// An item that stays (with its <seq> folder, it keeps p).
				return nil, nil
			}
			out = append(out, &step{op: opRmdir, entry: c.id, fromParent: p.id, fromName: c.name, fromPath: c.path,
				state: statePlanned})
		case c.kind == string(domain.EntryFile) && bytes.HasSuffix(c.name, []byte(".json")):
			records[string(c.name)] = true
		default:
			keep = true
		}
	}
	// The records the cleanup wrote that no scan indexed: those of its done
	// record items that no unlink removed since.
	rows, err := q.QueryContext(ctx, `SELECT i.to_name FROM action_items i JOIN actions a ON a.id = i.action_id
		WHERE i.op = 'record' AND i.state = 'done' AND i.to_parent = ? AND a.kind = 'cleanup'
			AND NOT EXISTS (SELECT 1 FROM action_items u WHERE u.op = 'unlink' AND u.state = 'done'
				AND u.from_parent = i.to_parent AND u.from_name = i.to_name)`, p.id)
	if err != nil {
		return nil, fmt.Errorf("cleanup: records of plan folder %d: %w", p.id, err)
	}
	for rows.Next() {
		var name []byte
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		records[string(name)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(records))
	for name := range records {
		if !handled[name] {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		out = append(out, &step{op: opUnlink, fromParent: p.id, fromName: []byte(name),
			fromPath: joinPath(p.path, []byte(name)), state: statePlanned})
	}
	if !keep {
		out = append(out, &step{op: opRmdir, entry: p.id, fromParent: quarantine, fromName: p.name, fromPath: p.path,
			state: statePlanned})
	}
	return out, nil
}
