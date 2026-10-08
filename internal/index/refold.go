package index

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"precious/internal/domain"
	"precious/internal/rules"
)

// rootName is the name the folder rules match for a source's root folder:
// the last component of its folder on the volume (relRoot), or the base of
// the mount point when the source is the whole volume. A scan and a refold
// both use it, so they classify the root alike.
func rootName(relRoot []byte, mountPoint string) []byte {
	if len(relRoot) > 0 {
		return relRoot[bytes.LastIndexByte(relRoot, '/')+1:]
	}
	if mountPoint == "" {
		return []byte{}
	}
	return []byte(filepath.Base(mountPoint))
}

// Refolder re-derives what a scan's fold computes, for the entries a step
// of the organize executor touched, from the stored rows (r3 design D6 step
// 7). It is safe for concurrent use.
type Refolder struct {
	pol *rules.Policy
}

// NewRefolder returns a Refolder classifying with pol, the policy the scans
// use.
func NewRefolder(pol *rules.Policy) *Refolder { return &Refolder{pol: pol} }

// Refold re-derives touched entries and both of their ancestor chains (D6
// step 7), in tx, writing only the rows that differ, as a scan would:
//   - every touched folder, and every folder above a touched entry, is
//     folded again, deepest first, from its stored children (their rows and
//     dir_stats), with the fold a scan uses: its totals, time range, main
//     kind, dir_stats, and classification, with the owner's override;
//   - the regular files of every such folder are classified again, so a
//     moved or renamed file gets the classification of its name, and the
//     files whose kind depends on a sibling (a .bin beside its .cue) follow
//     a sibling that came or went.
//
// For a move, touched holds the moved entry and the folder it left; for a
// mkdir, the new folder; for an rmdir, the folder that held it. Entries no
// longer indexed are skipped. A folder that is unreadable or a mount
// boundary keeps its stored row and dir_stats, as a scan keeps what it
// cannot list.
func (rf *Refolder) Refold(ctx context.Context, tx *sql.Tx, src domain.SourceID, touched []domain.EntryID) error {
	r := &refold{ctx: ctx, tx: tx, src: src, pol: rf.pol, codec: newCodec(), tokens: tokens{held: map[uint64]int32{}}}
	folders := map[domain.EntryID]*place{}
	for _, id := range touched {
		p, err := placeByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if p == nil {
			continue
		}
		if p.source != src {
			return fmt.Errorf("index: entry %d is on source %q, not %q", id, p.source, src)
		}
		if p.kind == string(domain.EntryDirectory) {
			folders[p.id] = p
		}
		for parent := p.parent; parent.Valid; {
			id := domain.EntryID(parent.Int64)
			if _, ok := folders[id]; ok {
				break // its ancestors are in already
			}
			a, err := placeByID(ctx, tx, id)
			if err != nil {
				return err
			}
			if a == nil {
				return fmt.Errorf("index: the folder above entry %d is gone", p.id)
			}
			folders[id] = a
			parent = a.parent
		}
	}
	// Deepest first: a folder's path is longer than its parent's, so every
	// folder is folded after the folders below it.
	order := make([]*place, 0, len(folders))
	for _, p := range folders {
		order = append(order, p)
	}
	slices.SortFunc(order, func(a, b *place) int {
		return cmp.Or(cmp.Compare(len(b.path), len(a.path)), bytes.Compare(a.path, b.path))
	})
	for _, p := range order {
		if err := r.folder(p); err != nil {
			return err
		}
	}
	return nil
}

// Statements of a refold.
const (
	// storedByIDQuery reads a stored entry and its dir_stats.
	storedByIDQuery = `SELECT ` + storedColumns + ` FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id
		WHERE e.id = ?`
	// ownersQuery reads the owner's overrides of a folder's children and of
	// the folder itself: folder, folder.
	ownersQuery = `SELECT o.entry_id, o.category, o.group_mark FROM entries e
		JOIN entry_overrides o ON o.entry_id = e.id WHERE e.parent_id = ?
		UNION ALL SELECT entry_id, category, group_mark FROM entry_overrides WHERE entry_id = ?`
	// refoldRowSQL writes the row columns a fold computes, leaving the
	// times a scan saw it as they are.
	refoldRowSQL = `UPDATE entries SET ` + rowSet + ` WHERE id = ?`
)

// refold is one Refold call.
type refold struct {
	ctx   context.Context
	tx    *sql.Tx
	src   domain.SourceID
	pol   *rules.Policy
	codec *codec
	tokens
	sigs []rules.SignalID
	args []any
	// root is the root folder's name for the rules, read once.
	root []byte
}

// folder folds the folder p again from its stored children.
func (r *refold) folder(p *place) error {
	self, err := scanStored(r.tx.QueryRowContext(r.ctx, storedByIDQuery, int64(p.id)))
	if err != nil {
		return fmt.Errorf("index: read %q: %w", displayPath(p.path), err)
	}
	if self.row.state != "present" || self.row.boundary {
		return nil // what a scan could not list stays as stored
	}
	var children []*stored
	rows, err := r.tx.QueryContext(r.ctx, childrenQuery, int64(p.id))
	if err != nil {
		return fmt.Errorf("index: read the children of %q: %w", displayPath(p.path), err)
	}
	for rows.Next() {
		c, err := scanStored(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("index: read the children of %q: %w", displayPath(p.path), err)
		}
		if c.row.state != "missing" {
			children = append(children, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("index: read the children of %q: %w", displayPath(p.path), err)
	}
	slices.SortFunc(children, func(a, b *stored) int { return bytes.Compare(a.name, b.name) })
	owners, err := r.owners(p.id)
	if err != nil {
		return err
	}
	stems := map[string]bool{}
	for _, c := range children {
		if c.row.kind == string(domain.EntryFile) {
			if stem, ok := r.pol.PairStem(c.name); ok {
				stems[stem] = true
			}
		}
	}

	a := newAgg()
	for _, c := range children {
		var indicator rules.SignalID
		r.sigs, indicator = a.nameSignals(r.pol, r.sigs, c.name, entryKind(&c.row))
		if indicator != "" && a.refFits(p.path, c.name) {
			r.addRef(&a, indicatorRef{id: c.id, path: joinPath(p.path, c.name), signal: indicator})
		}
		switch c.row.kind {
		case string(domain.EntryDirectory):
			f, inside, err := storedFold(c)
			if err != nil {
				return fmt.Errorf("index: read the dir_stats of %q: %w", displayPath(joinPath(p.path, c.name)), err)
			}
			r.absorb(&a, &finished{a: &f, r: &c.row, id: c.id, path: joinPath(p.path, c.name), inside: inside,
				contribution: contributionOf(&c.row, &f.comp, f.files, f.bytes)})
		case string(domain.EntryFile):
			if err := r.file(&a, p.path, c, stems, owners); err != nil {
				return err
			}
		case string(domain.EntrySymlink):
			a.symlinks++
		default:
			a.specials++
		}
	}

	name := self.name
	if !p.parent.Valid {
		if name, err = r.rootName(); err != nil {
			return err
		}
	}
	out := row{kind: string(domain.EntryDirectory), state: self.row.state}
	out.copyFacts(&self.row)
	out.boundary = self.row.boundary
	_, inside, cols := a.finishFolder(r.pol, r.codec, name, ownerIn(owners, p.id), &out)
	if cols.indicators, err = encodeIndicators(a.refs, nil); err != nil {
		return err
	}
	if cols.inside, err = encodeInside(inside, nil); err != nil {
		return err
	}
	if out != self.row {
		if err := r.writeRow(p.id, &out); err != nil {
			return err
		}
	}
	if !self.hasStats || self.stats != cols {
		if _, err := r.tx.ExecContext(r.ctx, writerQueries[stStats], int64(p.id), cols.dirs, cols.files,
			cols.symlinks, cols.specials, cols.unreadable, cols.mounts, cols.byKind, cols.byYear, cols.byFamily,
			cols.signals, cols.indicators, cols.inside); err != nil {
			return fmt.Errorf("index: write the dir_stats of %q: %w", displayPath(p.path), err)
		}
	}
	return nil
}

// file classifies the stored file c of the folder at dir again, writes its
// row when it differs, and adds it to the fold a, as a scan's file does.
func (r *refold) file(a *agg, dir []byte, c *stored, stems map[string]bool, owners map[domain.EntryID]domain.Override) error {
	out := row{kind: string(domain.EntryFile), state: "present"}
	out.copyFacts(&c.row)
	out.boundary = c.row.boundary
	kind := r.pol.FileKindNear(c.name, stems)
	family := fileRow(r.pol, r.codec, c.name, kind, stems, ownerIn(owners, c.id), &out)
	a.addFile(kind, out.size, out.mtime.v, family)
	var token uint64
	r.notableFile(a, dir, c.name, c.id, &token, &out, family)
	if out == c.row {
		return nil
	}
	return r.writeRow(c.id, &out)
}

func (r *refold) writeRow(id domain.EntryID, out *row) error {
	r.args = out.args(r.args[:0], nil)
	if _, err := r.tx.ExecContext(r.ctx, refoldRowSQL, append(r.args, int64(id))...); err != nil {
		return fmt.Errorf("index: write entry %d: %w", id, err)
	}
	return nil
}

// owners reads the owner's overrides of the folder id and its children.
func (r *refold) owners(id domain.EntryID) (map[domain.EntryID]domain.Override, error) {
	rows, err := r.tx.QueryContext(r.ctx, ownersQuery, int64(id), int64(id))
	if err != nil {
		return nil, fmt.Errorf("index: read overrides: %w", err)
	}
	defer rows.Close()
	var out map[domain.EntryID]domain.Override
	for rows.Next() {
		var (
			eid      domain.EntryID
			category text
			mark     sql.NullBool
		)
		if err := rows.Scan(&eid, &category, &mark); err != nil {
			return nil, fmt.Errorf("index: read overrides: %w", err)
		}
		o := domain.Override{Category: domain.Category(category)}
		if mark.Valid {
			o.Group = &mark.Bool
		}
		if out == nil {
			out = map[domain.EntryID]domain.Override{}
		}
		out[eid] = o
	}
	return out, rows.Err()
}

func ownerIn(owners map[domain.EntryID]domain.Override, id domain.EntryID) *domain.Override {
	if o, ok := owners[id]; ok {
		return &o
	}
	return nil
}

// rootName returns the name the rules match for the source's root folder.
func (r *refold) rootName() ([]byte, error) {
	if r.root != nil {
		return r.root, nil
	}
	var (
		rel   []byte
		point sql.NullString
	)
	if err := r.tx.QueryRowContext(r.ctx, `SELECT rel_root, mount_point FROM sources WHERE id = ?`,
		string(r.src)).Scan(&rel, &point); err != nil {
		return nil, fmt.Errorf("index: read source %q: %w", r.src, err)
	}
	r.root = rootName(rel, point.String)
	return r.root, nil
}

// entryKind is the kind of entry a stored row records.
func entryKind(r *row) domain.EntryKind {
	if r.kind == "special" {
		return domain.EntryKind(r.special)
	}
	return domain.EntryKind(r.kind)
}

// storedFold rebuilds, from a stored folder's row and dir_stats, what its
// parent's fold takes in from it, and its own inside list. The indicator
// count is the length of its stored list, which is not 0 exactly when the
// scan's count is not, the only way a folder's rules read it
// (rules.FolderFacts.Indicators).
func storedFold(s *stored) (agg, []insideRef, error) {
	r := &s.row
	a := agg{files: r.totalFiles, bytes: r.totalBytes, partial: r.partial}
	if r.newest.ok && r.oldest.ok {
		a.newest, a.oldest, a.dated = r.newest.v, r.oldest.v, true
	}
	if !s.hasStats {
		return a, nil, nil
	}
	st := &s.stats
	a.dirs, a.symlinks, a.specials, a.unreadableN, a.mts = st.dirs, st.symlinks, st.specials, st.unreadable, st.mounts
	var (
		cells   map[string]counts
		signals map[string]int
		inds    []indicatorJSON
		ins     []insideJSON
	)
	if err := decodeCells(st.byKind, &cells); err != nil {
		return agg{}, nil, err
	}
	a.byKind = make(map[domain.FileKind]rules.KindTotals, len(cells))
	for k, c := range cells {
		a.byKind[domain.FileKind(k)] = rules.KindTotals{Files: c.files, Bytes: c.bytes}
	}
	if err := decodeCells(st.byYear, &cells); err != nil {
		return agg{}, nil, err
	}
	a.byYear = make(map[int]counts, len(cells))
	for k, c := range cells {
		y, err := strconv.Atoi(k)
		if err != nil {
			return agg{}, nil, fmt.Errorf("by_year key %q: %w", k, err)
		}
		a.byYear[y] = c
	}
	if err := decodeCells(st.byFamily, &cells); err != nil {
		return agg{}, nil, err
	}
	for k, c := range cells {
		if i := familyIndex(domain.Family(k)); i >= 0 {
			a.comp[i] = c
		}
	}
	if err := json.Unmarshal([]byte(st.signals), &signals); err != nil {
		return agg{}, nil, fmt.Errorf("signals: %w", err)
	}
	a.subtreeSignals = make(map[rules.SignalID]int, len(signals))
	for k, n := range signals {
		a.subtreeSignals[rules.SignalID(k)] = n
	}
	if err := json.Unmarshal([]byte(st.indicators), &inds); err != nil {
		return agg{}, nil, fmt.Errorf("indicators: %w", err)
	}
	a.indicators = len(inds)
	a.refs = make([]indicatorRef, len(inds))
	for i, j := range inds {
		id, err := domain.ParseEntryID(j.EntryID)
		if err != nil {
			return agg{}, nil, fmt.Errorf("indicators: %w", err)
		}
		a.refs[i] = indicatorRef{id: id, path: j.PathB64, signal: rules.SignalID(j.Signal)}
	}
	if err := json.Unmarshal([]byte(st.inside), &ins); err != nil {
		return agg{}, nil, fmt.Errorf("inside: %w", err)
	}
	inside := make([]insideRef, len(ins))
	for i, j := range ins {
		id, err := domain.ParseEntryID(j.EntryID)
		if err != nil {
			return agg{}, nil, fmt.Errorf("inside: %w", err)
		}
		inside[i] = insideRef{id: id, path: j.PathB64, category: textOf(j.Category), family: textOf(j.Family),
			group: j.Group, bytes: j.Bytes, files: j.Files}
	}
	return a, inside, nil
}

// decodeCells decodes a dir_stats breakdown {"key":{"files":n,"bytes":n}}.
func decodeCells(doc string, into *map[string]counts) error {
	var cells map[string]struct {
		Files int64 `json:"files"`
		Bytes int64 `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(doc), &cells); err != nil {
		return err
	}
	out := make(map[string]counts, len(cells))
	for k, c := range cells {
		out[k] = counts{files: c.Files, bytes: c.Bytes}
	}
	*into = out
	return nil
}

func textOf(s *string) text {
	if s == nil {
		return ""
	}
	return text(*s)
}
