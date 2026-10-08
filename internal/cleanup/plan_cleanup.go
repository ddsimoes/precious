package cleanup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/organize"
	"precious/internal/review"
	"precious/internal/store"
)

// summaryJSON is the summary plan-cleanup answers, from the index only
// (Interfaces): of the files below the planned items, the bytes with a
// known copy outside the plan and the quarantine, the bytes with none, and
// the bytes not checked for copies yet; and the planned items that hold
// personal material.
type summaryJSON struct {
	WithCopyBytes  int64 `json:"with_copy_bytes"`
	NoCopyBytes    int64 `json:"no_copy_bytes"`
	UncheckedBytes int64 `json:"unchecked_bytes"`
	PersonalItems  int64 `json:"personal_items"`
}

// cleanupPlanResponse answers plan-cleanup (201).
type cleanupPlanResponse struct {
	organize.PlanResponse
	Summary summaryJSON `json:"summary"`
}

// genSQL is the visible generation of the review rows.
const genSQL = `(SELECT gen FROM review_state WHERE id = 1)`

var notQuarantinedE = index.NotQuarantined("e")

// planCleanup plans plan-cleanup (D1, D3–D5): the topmost entries of the
// source with their own discard (or those of a review list's rows),
// discards nested inside them folded in, each refused, blocked by a keep
// inside it, or planned as the mkdir of its <seq> folder, its rename into
// it with its draft-time identity, and its origin record, after the mkdirs
// of the quarantine and of the plan folder. It reads the index only.
func (s *Service) planCleanup(ctx context.Context, tx *jobs.Tx, req planCleanupRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	src := domain.SourceID(req.SourceID)
	list := review.List(req.List)
	if req.List != "" && !list.Valid() {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "unknown review list %q", req.List)
	}
	if err := organize.CheckSource(ctx, q, src, s.allowWrites); err != nil {
		return 0, nil, err
	}
	quarantine, taken, err := quarantineOf(ctx, q, src)
	if err != nil {
		return 0, nil, err
	}
	if taken {
		return 0, nil, domain.Errorf(domain.CodeQuarantineNameTaken,
			"the top folder of source %q holds a %q that Precious did not make; rename it, then draft again", src,
			index.QuarantineName)
	}
	if err := organize.Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	d := &drafter{ctx: ctx, q: q, src: src, refused: map[int64]string{}, chosen: map[string]bool{}}
	ground := groundDiscard
	switch {
	case req.List == "":
		err = d.discards()
	case list == review.ListDuplicates:
		ground = groundDuplicate
		err = d.duplicates()
	default:
		err = d.listRows(list)
	}
	if err != nil {
		return 0, nil, err
	}
	if len(d.cands) == 0 {
		what := "source " + strconv.Quote(string(src))
		if req.List != "" {
			what = "the list " + req.List + " on that source"
		}
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "nothing in %s is discarded; discard what to clean up first", what)
	}
	items, err := d.items()
	if err != nil {
		return 0, nil, err
	}
	planned := 0
	for _, it := range items {
		if it.state == statePlanned {
			planned++
		}
	}
	n := len(items) + 2*planned
	if planned > 0 {
		n += 2
		if quarantine != 0 {
			n--
		}
	}
	if n > maxSteps {
		return 0, nil, tooMany(n)
	}
	id, err := insertAction(ctx, tx, action{kind: kindCleanup, src: src, bulk: true, ground: ground, list: req.List,
		ttl: cleanupTTL})
	if err != nil {
		return 0, nil, err
	}
	var top int64
	if err := q.QueryRowContext(ctx, `SELECT id FROM entries WHERE source_id = ? AND parent_id IS NULL`, string(src)).
		Scan(&top); err != nil {
		return 0, nil, fmt.Errorf("cleanup: top folder of %q: %w", src, err)
	}
	steps := cleanupSteps(items, id, top, quarantine)
	if err := insertSteps(ctx, q, id, steps); err != nil {
		return 0, nil, err
	}
	sum, err := d.summary(items)
	if err != nil {
		return 0, nil, err
	}
	res, err := organize.ReadPlan(ctx, q, id, now)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, cleanupPlanResponse{PlanResponse: res, Summary: sum}, nil
}

// cleanupItem is one entry of a cleanup plan.
type cleanupItem struct {
	n             *node
	state, reason string
}

// cleanupSteps lays out a cleanup action's items (E1): with any item
// planned, the mkdir of the quarantine (when Precious has not made it yet)
// and of the plan folder, named by the action's ID; then, in path order,
// each planned entry's mkdir of <k>, rename into it, and record <k>.json,
// and each refused or blocked entry's single rename item.
func cleanupSteps(items []cleanupItem, action, top, quarantine int64) []*step {
	var steps []*step
	planSeq := 0
	qpath := []byte(index.QuarantineName)
	planPath := joinPath(qpath, []byte(strconv.FormatInt(action, 10)))
	if slices.ContainsFunc(items, func(it cleanupItem) bool { return it.state == statePlanned }) {
		mk := &step{op: opMkdir, toName: []byte(strconv.FormatInt(action, 10)), toPath: planPath, state: statePlanned}
		if quarantine == 0 {
			steps = append(steps, &step{op: opMkdir, toParent: top, toName: qpath, toPath: qpath, state: statePlanned})
			mk.toDirSeq = len(steps)
		} else {
			mk.toParent = quarantine
		}
		steps = append(steps, mk)
		planSeq = len(steps)
	}
	k := 0
	for _, it := range items {
		n := it.n
		ren := &step{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			toName: n.name, bytes: n.bytes, files: n.files, state: it.state, reason: it.reason}
		if it.state != statePlanned {
			steps = append(steps, ren)
			continue
		}
		k++
		seqName := []byte(strconv.Itoa(k))
		seqPath := joinPath(planPath, seqName)
		steps = append(steps, &step{op: opMkdir, toDirSeq: planSeq, toName: seqName, toPath: seqPath,
			state: statePlanned})
		ren.toDirSeq, ren.toPath = len(steps), joinPath(seqPath, n.name)
		// The draft-time identity (D3, E1): the index row the plan read.
		ren.kind, ren.dev, ren.ino, ren.mtime, ren.ctime = n.kind, n.dev, n.ino, n.mtime, n.ctime
		ren.size = sql.NullInt64{Int64: n.size, Valid: true}
		if n.dir() {
			ren.draftBytes = sql.NullInt64{Int64: n.bytes, Valid: true}
			ren.draftFiles = sql.NullInt64{Int64: n.files, Valid: true}
		}
		steps = append(steps, ren)
		rec := append(bytes.Clone(seqName), ".json"...)
		steps = append(steps, &step{op: opRecord, entry: n.id, toDirSeq: planSeq, toName: rec,
			toPath: joinPath(planPath, rec), state: statePlanned})
	}
	return steps
}

// drafter collects the entries of a cleanup plan.
type drafter struct {
	ctx context.Context
	q   store.Queryer
	src domain.SourceID
	// cands are the chosen entries; refused holds the duplicates rules'
	// refusals (D5) by entry, which win over a choice.
	cands   []*node
	refused map[int64]string
	// chosen holds the paths of the chosen entries that are not refused,
	// the plan so far for the duplicates rules.
	chosen map[string]bool
}

// choose adds n to the plan, once.
func (d *drafter) choose(n *node) {
	if d.chosen[string(n.path)] {
		return
	}
	if _, ok := d.refused[n.id]; ok {
		return
	}
	d.chosen[string(n.path)] = true
	d.cands = append(d.cands, n)
}

// refuse refuses n with reason, also when it was chosen before.
func (d *drafter) refuse(n *node, reason string) {
	if _, ok := d.refused[n.id]; ok {
		return
	}
	d.refused[n.id] = reason
	if d.chosen[string(n.path)] {
		delete(d.chosen, string(n.path))
		return
	}
	d.cands = append(d.cands, n)
}

// covered reports whether path of src lies at or below an entry the plan
// holds.
func (d *drafter) covered(src domain.SourceID, path []byte) bool {
	if src != d.src {
		return false
	}
	if d.chosen[""] || d.chosen[string(path)] {
		return true
	}
	for i := len(path) - 1; i > 0; i-- {
		if path[i] == '/' && d.chosen[string(path[:i])] {
			return true
		}
	}
	return false
}

// eligible reports whether n is an entry of the source with its own
// discard: an item of a plan drafted from a list's rows.
func (d *drafter) eligible(n *node) bool { return n != nil && n.source == d.src && n.discarded() }

// discards chooses every entry of the source with its own discard, outside
// the quarantine, where everything already holds one (D2).
func (d *drafter) discards() error {
	nodes, err := loadNodes(d.ctx, d.q, `SELECT `+nodeCols+` FROM `+nodeFrom+` WHERE e.source_id = ?
		AND e.decision = 'discard' AND `+notQuarantinedE+` ORDER BY e.path`, string(d.src))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		d.choose(n)
	}
	return nil
}

// listRows chooses the entries of a review list's rows on the source that
// have their own discard (the entry lists: every card but duplicates).
func (d *drafter) listRows(list review.List) error {
	nodes, err := loadNodes(d.ctx, d.q, `SELECT `+nodeCols+` FROM `+nodeFrom+` WHERE e.id IN (
			SELECT rr.entry_id FROM review_rows rr WHERE rr.gen = `+genSQL+` AND rr.list = ? AND rr.source_id = ?
				AND rr.entry_id IS NOT NULL)
		AND e.decision = 'discard' ORDER BY e.path`, string(list), string(d.src))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		d.choose(n)
	}
	return nil
}

// duplicates chooses the discarded entries of the duplicates list's rows
// touching the source, under D5's rules, in the list's order: a relation
// row's discarded sides, the later by path refused both_sides when both
// are; then a content group row's discarded copies in path order, until
// one would leave no copy outside the plan and the quarantine that is
// present (a hard-link set, or an archive's member, being one copy): that
// one and the later ones are refused last_copy.
func (d *drafter) duplicates() error {
	rows, err := d.q.QueryContext(d.ctx, `SELECT coalesce(rr.relation_id, 0), coalesce(rr.content_id, 0)
		FROM review_rows rr WHERE rr.gen = `+genSQL+` AND rr.list = 'duplicates' AND EXISTS (
			SELECT 1 FROM review_row_sources rs WHERE rs.row_id = rr.id AND rs.source_id = ?)
		ORDER BY rr.sort_key DESC, rr.id DESC`, string(d.src))
	if err != nil {
		return fmt.Errorf("cleanup: duplicates rows: %w", err)
	}
	var relationIDs, contentIDs []int64
	for rows.Next() {
		var rel, content int64
		if err := rows.Scan(&rel, &content); err != nil {
			rows.Close()
			return err
		}
		if rel != 0 {
			relationIDs = append(relationIDs, rel)
		} else if content != 0 {
			contentIDs = append(contentIDs, content)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, rel := range relationIDs {
		if err := d.relation(rel); err != nil {
			return err
		}
	}
	for _, content := range contentIDs {
		if err := d.group(content); err != nil {
			return err
		}
	}
	return nil
}

// relation applies D5 to one relation row: each side that is an entry
// (not an archive's member) of the source with its own discard is chosen,
// unless both are: the later by path is then refused both_sides.
func (d *drafter) relation(id int64) error {
	var (
		a, b         int64
		am, bm       sql.NullInt64
		sideA, sideB *node
	)
	if err := d.q.QueryRowContext(d.ctx, `SELECT a_entry, a_member, b_entry, b_member FROM relations WHERE id = ?`, id).
		Scan(&a, &am, &b, &bm); err != nil {
		return fmt.Errorf("cleanup: relation %d: %w", id, err)
	}
	var err error
	if !am.Valid {
		if sideA, err = loadNode(d.ctx, d.q, a); err != nil {
			return err
		}
	}
	if !bm.Valid {
		if sideB, err = loadNode(d.ctx, d.q, b); err != nil {
			return err
		}
	}
	okA, okB := d.eligible(sideA), d.eligible(sideB)
	switch {
	case okA && okB:
		first, later := sideA, sideB
		if bytes.Compare(sideB.path, sideA.path) < 0 {
			first, later = sideB, sideA
		}
		d.refuse(later, reasonBothSides)
		d.choose(first)
	case okA:
		d.choose(sideA)
	case okB:
		d.choose(sideB)
	}
	return nil
}

// copyAt is one copy of a content: a file entry, or a member of a
// complete archive (whose archive entry it lives in).
type copyAt struct {
	src  domain.SourceID
	path []byte // the file's, or the archive's
	node *node  // a file entry
}

// copies lists the present copies of content outside the quarantine: file
// entries with it, and members of complete archives.
func (d *drafter) copies(content int64) ([]copyAt, error) {
	nodes, err := loadNodes(d.ctx, d.q, `SELECT `+nodeCols+` FROM file_content fc JOIN entries e ON e.id = fc.entry_id
		LEFT JOIN dir_stats d ON d.entry_id = e.id
		WHERE fc.content_id = ? AND e.state = 'present' AND e.kind = 'file' AND `+notQuarantinedE+`
		ORDER BY e.source_id, e.path`, content)
	if err != nil {
		return nil, err
	}
	out := make([]copyAt, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, copyAt{src: n.source, path: n.path, node: n})
	}
	rows, err := d.q.QueryContext(d.ctx, `SELECT e.source_id, e.path FROM archive_members m
		JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = a.entry_id
		WHERE m.content_id = ? AND a.state = 'complete' AND e.state = 'present' AND `+notQuarantinedE, content)
	if err != nil {
		return nil, fmt.Errorf("cleanup: member copies of content %d: %w", content, err)
	}
	defer rows.Close()
	for rows.Next() {
		var c copyAt
		if err := rows.Scan(&c.src, &c.path); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// group applies D5 to one content group row.
func (d *drafter) group(content int64) error {
	cs, err := d.copies(content)
	if err != nil {
		return err
	}
	var discarded []*node
	for _, c := range cs {
		if c.node != nil && d.eligible(c.node) {
			discarded = append(discarded, c.node)
		}
	}
	slices.SortFunc(discarded, func(a, b *node) int { return bytes.Compare(a.path, b.path) })
	last := false
	for _, n := range discarded {
		if _, refused := d.refused[n.id]; refused || d.covered(n.source, n.path) {
			continue
		}
		if last {
			d.refuse(n, reasonLastCopy)
			continue
		}
		d.chosen[string(n.path)] = true
		stays := false
		for _, c := range cs {
			if !d.covered(c.src, c.path) {
				stays = true
				break
			}
		}
		delete(d.chosen, string(n.path))
		if !stays {
			last = true
			d.refuse(n, reasonLastCopy)
			continue
		}
		d.choose(n)
	}
	return nil
}

// items folds the chosen entries into the topmost ones, in path order,
// and decides each: refused (D5's rules, or as R3 refuses a move), blocked
// by an effective keep at or below it, or planned.
func (d *drafter) items() ([]cleanupItem, error) {
	slices.SortFunc(d.cands, func(a, b *node) int { return bytes.Compare(a.path, b.path) })
	var (
		out  []cleanupItem
		tops = map[string]bool{}
	)
	inside := func(p []byte) bool {
		if tops[""] {
			return true
		}
		for i := len(p) - 1; i > 0; i-- {
			if p[i] == '/' && tops[string(p[:i])] {
				return true
			}
		}
		return false
	}
	for _, n := range d.cands {
		if inside(n.path) {
			continue
		}
		tops[string(n.path)] = true
		it := cleanupItem{n: n, state: stateRefused}
		switch r, ok := d.refused[n.id]; {
		case ok:
			it.reason = r
		case index.IsQuarantinePath(n.path):
			it.reason = reasonInQuarantine
		case n.state == "missing":
			it.reason = reasonMissing
		case n.parent == 0:
			it.reason = reasonSourceRoot
		case n.boundary:
			it.reason = reasonOtherFilesystem
		case n.dir() && n.mounts > 0:
			it.reason = reasonContainsMount
		default:
			kept, err := keptWithin(d.ctx, d.q, d.src, n.path)
			if err != nil {
				return nil, err
			}
			if kept {
				it.state, it.reason = stateBlocked, reasonHoldsKept
			} else {
				it.state = statePlanned
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// summary computes the plan's summary over its planned items, from the
// index (Interfaces): each file below them by its content state, a hashed
// one by whether a present copy of its content lies outside the plan and
// the quarantine; and the items whose own family is personal, or that hold
// indicators or a veto.
func (d *drafter) summary(items []cleanupItem) (summaryJSON, error) {
	var sum summaryJSON
	plan := map[string]bool{}
	for _, it := range items {
		if it.state == statePlanned {
			plan[string(it.n.path)] = true
		}
	}
	d.chosen = plan
	hasCopy := map[int64]bool{}
	for _, it := range items {
		if it.state != statePlanned {
			continue
		}
		n := it.n
		if n.family.String == string(domain.FamilyPersonal) || n.veto ||
			n.indicators.Valid && n.indicators.String != "" && n.indicators.String != "[]" {
			sum.PersonalItems++
		}
		lo, hi := bounds(n.path)
		rows, err := d.q.QueryContext(d.ctx, `SELECT e.size, fc.state, coalesce(fc.content_id, 0) FROM entries e
			JOIN file_content fc ON fc.entry_id = e.id
			WHERE e.source_id = ? AND e.kind = 'file' AND e.state <> 'missing' AND (e.path = ? OR (e.path >= ? AND e.path < ?))`,
			string(d.src), n.path, lo, hi)
		if err != nil {
			return sum, fmt.Errorf("cleanup: files below %q: %w", domain.DisplayName(n.path), err)
		}
		type file struct {
			size    int64
			state   string
			content int64
		}
		var files []file
		for rows.Next() {
			var f file
			if err := rows.Scan(&f.size, &f.state, &f.content); err != nil {
				rows.Close()
				return sum, err
			}
			files = append(files, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return sum, err
		}
		for _, f := range files {
			switch domain.ContentState(f.state) {
			case domain.ContentPending, domain.ContentChanged, domain.ContentUnreadable:
				sum.UncheckedBytes += f.size
			case domain.ContentHashed:
				has, ok := hasCopy[f.content]
				if !ok {
					cs, err := d.copies(f.content)
					if err != nil {
						return sum, err
					}
					has = slices.ContainsFunc(cs, func(c copyAt) bool { return !d.covered(c.src, c.path) })
					hasCopy[f.content] = has
				}
				if has {
					sum.WithCopyBytes += f.size
				} else {
					sum.NoCopyBytes += f.size
				}
			default: // unique_size, sampled
				sum.NoCopyBytes += f.size
			}
		}
	}
	return sum, nil
}
