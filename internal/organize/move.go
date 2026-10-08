package organize

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
)

// planMove plans plan-move (r3 design D7, D8, D14): its targets, in path
// order, each folded into a moving folder that holds it, into the
// destination folder under their names. One entry_id is an individual
// move; entry_ids and a selection are bulk.
func (s *Service) planMove(ctx context.Context, tx *jobs.Tx, req planMoveRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	d, err := folderArg(ctx, q, "destination_id", req.DestinationID)
	if err != nil {
		return 0, nil, err
	}
	var cs []candidate
	switch {
	case req.SelectionID != "":
		cs, err = selectionCandidates(ctx, q, now, req.SelectionID)
	case req.EntryIDs != nil:
		cs, err = idCandidates(ctx, q, req.EntryIDs)
	default:
		cs, err = idCandidates(ctx, q, []string{req.EntryID})
	}
	if err != nil {
		return 0, nil, err
	}
	if err := s.checkSource(ctx, q, d.source); err != nil {
		return 0, nil, err
	}
	if err := Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, d.source, req.EntryID == "")
	if err != nil {
		return 0, nil, err
	}
	// One entry_id may move a quarantined entry out (r4 D13).
	p.moveOut = req.EntryID != ""
	if err := p.moveAll(cs, folderDest(d)); err != nil {
		return 0, nil, err
	}
	return s.finish(ctx, tx, p, "move", d.id, 0)
}

// moveAll plans candidates cs into d under their names, folded and in path
// order.
func (p *plan) moveAll(cs []candidate, d dest) error {
	slices.SortFunc(cs, compareCandidates)
	cs = fold(cs, func(n *node) bool { return p.refusal(n, d, n.name) == "" })
	if len(cs) > maxItems {
		return tooMany(len(cs))
	}
	for _, c := range cs {
		if c.member != nil {
			name := c.member.fromPath[bytes.LastIndexByte(c.member.fromPath, '/')+1:]
			c.member.toName, c.member.toPath = name, joinPath(d.path, name)
			if err := p.refused(c.member, reasonInsideArchive); err != nil {
				return err
			}
			continue
		}
		if _, err := p.move(c.n, d, c.n.name, 0); err != nil {
			return err
		}
	}
	return nil
}

// finish writes the plan as an action and answers 201 with it.
func (s *Service) finish(ctx context.Context, tx *jobs.Tx, p *plan, kind string, destination, undoOf int64) (int, any, error) {
	res, err := s.write(ctx, tx, p, kind, destination, undoOf)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, res, nil
}

// write writes the plan as an action and reads it back as a plan's answer.
func (s *Service) write(ctx context.Context, tx *jobs.Tx, p *plan, kind string, destination, undoOf int64) (PlanResponse, error) {
	id, err := p.insert(tx, kind, destination, undoOf)
	if err != nil {
		return PlanResponse{}, err
	}
	return ReadPlan(ctx, tx.SQL(), id, tx.Now())
}

// idCandidates loads the targets named by IDs, each once. An unknown or
// malformed ID is not_found; a member of an archive is a candidate refused
// inside_archive.
func idCandidates(ctx context.Context, tx *sql.Tx, ids []string) ([]candidate, error) {
	var out []candidate
	seen := map[domain.Ref]bool{}
	for _, s := range ids {
		ref, err := domain.ParseRef(s)
		if err != nil {
			return nil, notFound("entry", s)
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if ref.IsMember() {
			c, err := memberCandidate(ctx, tx, ref, s)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
			continue
		}
		n, err := loadNode(ctx, tx, int64(ref.Entry))
		if err != nil {
			return nil, err
		}
		if n == nil {
			return nil, notFound("entry", s)
		}
		out = append(out, entryCandidate(n))
	}
	return out, nil
}

// memberCandidate is the refused candidate of an archive member: it names
// no entry, and its path is the archive's path, then the member's.
func memberCandidate(ctx context.Context, tx *sql.Tx, ref domain.Ref, s string) (candidate, error) {
	var (
		src        domain.SourceID
		arc, inner []byte
	)
	err := tx.QueryRowContext(ctx, `SELECT e.source_id, e.path, m.path FROM archive_members m
		JOIN entries e ON e.id = m.archive_id WHERE m.id = ?`, int64(ref.Member)).Scan(&src, &arc, &inner)
	if errors.Is(err, sql.ErrNoRows) {
		return candidate{}, notFound("entry", s)
	}
	if err != nil {
		return candidate{}, fmt.Errorf("organize: read member %s: %w", s, err)
	}
	path := joinPath(arc, inner)
	return candidate{src: src, path: path, member: &item{op: opRename, fromPath: path}}, nil
}

// selectionCandidates loads the entries of a selection: selection_expired
// once it expired, not_found when it is unknown or one of its entries no
// longer exists, as for set-decision.
func selectionCandidates(ctx context.Context, tx *sql.Tx, now time.Time, id string) ([]candidate, error) {
	var count, expires int64
	err := tx.QueryRowContext(ctx, `SELECT count, expires_at FROM selections WHERE id = ?`, id).Scan(&count, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("selection", id)
	}
	if err != nil {
		return nil, fmt.Errorf("organize: read selection: %w", err)
	}
	if expires <= clock.Millis(now) {
		return nil, domain.Errorf(domain.CodeSelectionExpired, "the selection expired at %s; select again",
			clock.FromMillis(expires).UTC().Format(time.RFC3339))
	}
	nodes, err := loadNodes(ctx, tx, `SELECT `+nodeColumns+` FROM selection_entries s JOIN entries e ON e.id = s.entry_id
		LEFT JOIN dir_stats d ON d.entry_id = e.id WHERE s.selection_id = ?`, id)
	if err != nil {
		return nil, err
	}
	if int64(len(nodes)) != count {
		return nil, domain.Errorf(domain.CodeNotFound, "%d entries of the selection no longer exist; select again",
			count-int64(len(nodes)))
	}
	out := make([]candidate, len(nodes))
	for i, n := range nodes {
		out[i] = entryCandidate(n)
	}
	return out, nil
}

// planRescue plans plan-rescue (D12): the outermost entries inside the
// folder with their own keep, flat into the destination under their names,
// as a bulk move.
func (s *Service) planRescue(ctx context.Context, tx *jobs.Tx, req planRescueRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	fid, err := parseEntryID("folder_id", req.FolderID)
	if err != nil {
		return 0, nil, err
	}
	f, err := loadNode(ctx, q, fid)
	if err != nil {
		return 0, nil, err
	}
	if f == nil {
		return 0, nil, notFound("folder_id", req.FolderID)
	}
	if !f.dir() {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "folder_id %s is not a folder", req.FolderID)
	}
	if err := frozen("folder_id", req.FolderID, f); err != nil {
		return 0, nil, err
	}
	d, err := folderArg(ctx, q, "destination_id", req.DestinationID)
	if err != nil {
		return 0, nil, err
	}
	if d.source == f.source && below(d.path, f.path) {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "the destination is inside the folder; choose one outside it")
	}
	if f.eff == keep {
		return 0, nil, domain.Errorf(domain.CodeInvalidEntryState,
			"the folder is kept, so nothing inside it needs rescuing")
	}
	query := `SELECT ` + nodeColumns + ` FROM ` + nodeFrom + ` WHERE e.source_id = ? AND e.decision = 'keep'`
	args := []any{string(f.source)}
	if len(f.path) == 0 {
		query += ` AND e.parent_id IS NOT NULL`
	} else {
		// The range [path+"/", path+"0") holds exactly the paths below it.
		query += ` AND e.path >= ? AND e.path < ?`
		args = append(args, joinPath(f.path, nil), append(append([]byte{}, f.path...), '0'))
	}
	kept, err := loadNodes(ctx, q, query+` ORDER BY e.path`, args...)
	if err != nil {
		return 0, nil, err
	}
	cs := make([]candidate, len(kept))
	for i, n := range kept {
		cs[i] = entryCandidate(n)
	}
	// Outermost own keeps only, whatever the plan makes of them.
	cs = fold(cs, func(*node) bool { return true })
	if len(cs) == 0 {
		return 0, nil, domain.Errorf(domain.CodeInvalidEntryState, "nothing inside the folder is kept on its own")
	}
	if err := s.checkSource(ctx, q, d.source); err != nil {
		return 0, nil, err
	}
	if err := Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, d.source, true)
	if err != nil {
		return 0, nil, err
	}
	if err := p.moveAll(cs, folderDest(d)); err != nil {
		return 0, nil, err
	}
	return s.finish(ctx, tx, p, "rescue", d.id, 0)
}
