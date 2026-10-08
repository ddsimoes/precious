package organize

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/relations"
)

// mergePage is how many Compare items one read of the group lists.
const mergePage = 1000

// planMerge plans plan-merge (r3 design D13): every file of Compare's
// only-on-one-side group of the side from goes to its same relative path
// inside the other side, under the wrapper folder Compare left out of that
// side. The folders missing on the way are made first, one mkdir item each,
// parents first; a later item names its folder by the mkdir's seq. A
// non-folder where a folder is needed makes the items below it a conflict.
// It is a bulk action.
func (s *Service) planMerge(ctx context.Context, tx *jobs.Tx, req planMergeRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	left, err := mergeSide(ctx, q, "left_id", req.LeftID)
	if err != nil {
		return 0, nil, err
	}
	right, err := mergeSide(ctx, q, "right_id", req.RightID)
	if err != nil {
		return 0, nil, err
	}
	if left.source != right.source {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "the two folders are on different sources; files move only inside one source")
	}
	lref, rref := domain.Ref{Entry: domain.EntryID(left.id)}, domain.Ref{Entry: domain.EntryID(right.id)}
	lw, rw, err := relations.Wrappers(ctx, q, lref, rref)
	if err != nil {
		return 0, nil, err
	}
	bucket, to, wrap := relations.BucketOnlyRight, left, lw
	if req.From == "left" {
		bucket, to, wrap = relations.BucketOnlyLeft, right, rw
	}
	if err := s.checkSource(ctx, q, to.source); err != nil {
		return 0, nil, err
	}
	if err := prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, to.source, true)
	if err != nil {
		return 0, nil, err
	}
	m := &merger{p: p, folders: map[string]folderAt{}, made: map[int64]*names{}}
	base := folderDest(to)
	if wrap != nil {
		// The wrapper is the single top folder of the destination's files,
		// so it is there.
		if base, err = m.folder(base, wrap); err != nil {
			return 0, nil, err
		}
	}
	m.folders[""] = folderAt{d: base}
	for cursor := ""; ; {
		res, err := relations.Compare(ctx, q, lref, rref, bucket, cursor, mergePage)
		if err != nil {
			return 0, nil, err
		}
		if n := res.Summary[bucket].Files; n > maxItems {
			return 0, nil, tooMany(int(n))
		}
		for _, it := range res.Items {
			ref, rel := it.Left, it.LeftPath
			if req.From == "right" {
				ref, rel = it.Right, it.RightPath
			}
			if err := m.add(ref, rel); err != nil {
				return 0, nil, err
			}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	p.dropUnusedMkdirs()
	return s.finish(ctx, tx, p, "merge", to.id, 0)
}

// dropUnusedMkdirs drops every mkdir item with no planned rename at or
// below its folder (design V4): a merge plans the folders on the way before
// deciding the files bound there, and when each of those ends refused or in
// conflict, the folder would be made empty. The items left are numbered
// again from 1, and each to_dir_seq follows its folder's new seq; an item
// that is not planned and named a dropped folder keeps only its path.
func (p *plan) dropUnusedMkdirs() {
	// A folder is made before anything goes into it, so one pass from the
	// end marks each needed mkdir before its parent is reached.
	needed := make([]bool, len(p.items)+1) // by seq
	for i := len(p.items) - 1; i >= 0; i-- {
		it := p.items[i]
		if it.toDirSeq != 0 && (it.op == opRename && it.state == statePlanned || it.op == opMkdir && needed[i+1]) {
			needed[it.toDirSeq] = true
		}
	}
	renumbered := make([]int, len(p.items)+1)
	out := p.items[:0]
	for i, it := range p.items {
		if it.op == opMkdir && !needed[i+1] {
			continue
		}
		if it.toDirSeq != 0 {
			it.toDirSeq = renumbered[it.toDirSeq] // 0 for a dropped folder
		}
		out = append(out, it)
		renumbered[i+1] = len(out)
	}
	clear(p.items[len(out):])
	p.items = out
}

// mergeSide loads a side of plan-merge: a folder present on its disk. An
// archive, a member, or any other file is invalid_request; an unknown or
// missing side is not_found, as in Compare.
func mergeSide(ctx context.Context, tx *sql.Tx, field, s string) (*node, error) {
	ref, err := domain.ParseRef(s)
	if err != nil {
		return nil, notFound(field, s)
	}
	if ref.IsMember() {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "%s %s is inside an archive, whose files cannot move", field, s)
	}
	n, err := loadNode(ctx, tx, int64(ref.Entry))
	if err != nil {
		return nil, err
	}
	if n == nil || n.state == "missing" {
		return nil, notFound(field, s)
	}
	if !n.presentFolder() {
		return nil, domain.Errorf(domain.CodeInvalidRequest,
			"%s %s is not a folder present on its disk (an archive's files cannot move)", field, s)
	}
	return n, nil
}

// folderAt is a folder on the way of a merge: where its items go, or why
// they cannot (a conflict reason).
type folderAt struct {
	d       dest
	blocked string
}

// merger plans the items of a merge.
type merger struct {
	p *plan
	// folders are the destination folders by relative path ("" is the
	// destination, wrapper included).
	folders map[string]folderAt
	// made are the folders the plan makes in each folder (by dest.key),
	// each named with its mkdir's seq.
	made map[int64]*names
}

// add plans the file ref at relative path rel.
func (m *merger) add(ref *domain.Ref, rel []byte) error {
	dir, name := []byte(nil), rel
	if i := bytes.LastIndexByte(rel, '/'); i >= 0 {
		dir, name = rel[:i], rel[i+1:]
	}
	f, err := m.resolve(dir)
	if err != nil {
		return err
	}
	if ref == nil || ref.IsMember() {
		return m.p.refused(&item{op: opRename, toName: name, toPath: joinPath(f.d.path, name)}, reasonInsideArchive)
	}
	n, err := loadNode(m.p.ctx, m.p.tx, int64(ref.Entry))
	if err != nil {
		return err
	}
	if n == nil {
		return fmt.Errorf("organize: Compare listed entry %d, which is gone", ref.Entry)
	}
	if f.blocked != "" {
		return m.p.push(&item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			toName: name, toPath: joinPath(f.d.path, name), bytes: n.bytes, files: n.files, state: stateConflict,
			reason: f.blocked})
	}
	_, err = m.p.move(n, f.d, name, 0)
	return err
}

// resolve returns the destination folder at relative path rel, planning a
// mkdir for each missing folder on the way, parents first.
func (m *merger) resolve(rel []byte) (folderAt, error) {
	if f, ok := m.folders[string(rel)]; ok {
		return f, nil
	}
	parentRel, name := []byte(nil), rel
	if i := bytes.LastIndexByte(rel, '/'); i >= 0 {
		parentRel, name = rel[:i], rel[i+1:]
	}
	parent, err := m.resolve(parentRel)
	if err != nil {
		return folderAt{}, err
	}
	f := folderAt{d: dest{path: joinPath(parent.d.path, name)}, blocked: parent.blocked}
	if parent.blocked == "" {
		d, err := m.folder(parent.d, name)
		var b blocked
		switch {
		case errors.As(err, &b):
			f.blocked = string(b)
		case err != nil:
			return folderAt{}, err
		default:
			f.d = d
		}
	}
	m.folders[string(rel)] = f
	return f, nil
}

// blocked is why a folder on the way cannot be used or made.
type blocked string

func (b blocked) Error() string { return string(b) }

// folder returns the folder name inside d: the one there, compared under
// the source's capabilities, the one the plan makes, or a new mkdir item.
// A non-folder there, or a missing entry with the owner's intent, blocks it.
func (m *merger) folder(d dest, name []byte) (dest, error) {
	p := m.p
	if d.id != 0 {
		var (
			found         bool
			id            int64
			path          []byte
			kind, st, eff string
		)
		rows, err := p.tx.QueryContext(p.ctx, `SELECT id, name, path, kind, state, eff_decision FROM entries
			WHERE parent_id = ? AND state <> 'missing'`, d.id)
		if err != nil {
			return dest{}, err
		}
		for rows.Next() {
			var (
				cid              int64
				cname, cpath     []byte
				ckind, cst, ceff string
			)
			if err := rows.Scan(&cid, &cname, &cpath, &ckind, &cst, &ceff); err != nil {
				rows.Close()
				return dest{}, err
			}
			if !found && sameName(p.sensitive, cname, name) {
				found, id, path, kind, st, eff = true, cid, cpath, ckind, cst, ceff
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return dest{}, err
		}
		if found {
			if kind != string(domain.EntryDirectory) || st != "present" {
				return dest{}, blocked(reasonNameTaken)
			}
			return dest{id: id, path: path, eff: eff}, nil
		}
		missing, err := index.MissingIntentAt(p.ctx, p.tx, p.src, joinPath(d.path, name))
		if err != nil {
			return dest{}, err
		}
		if missing {
			return dest{}, blocked(reasonNameTakenByMissing)
		}
	}
	made, ok := m.made[d.key()]
	if !ok {
		made = newNames(p.sensitive)
		m.made[d.key()] = made
	}
	if seq, ok := made.holder(name, -1); ok {
		return dest{seq: int(seq), path: joinPath(d.path, name), eff: d.eff}, nil
	}
	if _, taken := p.plannedNames(d).holder(name, -1); taken {
		return dest{}, blocked(reasonNameTakenInPlan)
	}
	nd, err := p.mkdir(d, name)
	if err != nil {
		return dest{}, err
	}
	made.add(name, int64(nd.seq))
	return nd, nil
}
