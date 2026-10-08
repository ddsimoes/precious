package organize

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"precious/internal/dates"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/media"
)

// maxSuffix is the last k a date organize tries in "stem (k)ext" (r5 D17).
const maxSuffix = 999

// setMtimePlanResponse answers plan-set-mtime (201): the plan, and the
// files already at their date, which have no item (r5 D14).
type setMtimePlanResponse struct {
	PlanResponse
	Summary setMtimeSummary `json:"summary"`
}

type setMtimeSummary struct {
	Unchanged int64 `json:"unchanged"`
}

// dateOrganizePlanResponse answers plan-date-organize (201) with the
// counts its preview warns of (r5 D16, D17).
type dateOrganizePlanResponse struct {
	PlanResponse
	Summary dateOrganizeSummary `json:"summary"`
}

type dateOrganizeSummary struct {
	// FilesWithCopies counts the planned files with a hashed copy among the
	// targets or at or below the destination.
	FilesWithCopies int64 `json:"files_with_copies"`
	// SplitSiblings counts the planned files that leave a same-stem
	// sibling behind, which their detail names.
	SplitSiblings int64 `json:"split_siblings"`
}

// mediaFile is a target of a date plan with what the plan reads of it: its
// entry, index facts, freshly derived date (media_dates), and digest.
type mediaFile struct {
	n         *node
	mtime     sql.NullInt64
	nlink     sql.NullInt64
	ext       sql.NullString
	eff       sql.NullInt64
	local     sql.NullString
	offset    sql.NullInt64
	precision sql.NullString
	meta      sql.NullString
	content   sql.NullInt64
}

// notDatedYet reports whether the file's metadata, of a format the media
// job reads, is not read yet (r5 D14).
func (f *mediaFile) notDatedYet() bool {
	state := media.MetaState(f.meta.String)
	if !f.meta.Valid {
		state = media.MetaPending
	}
	return state == media.MetaPending && media.FormatOf(f.ext.String) != media.FormatNone
}

// date is the file's effective date, or nil for none.
func (f *mediaFile) date() (*media.Date, error) {
	if !f.eff.Valid {
		return nil, nil
	}
	var off *int
	if f.offset.Valid {
		o := int(f.offset.Int64)
		off = &o
	}
	d, err := media.DateFromRow(f.eff.Int64, f.local.String, off, f.precision.String)
	if err != nil {
		return nil, fmt.Errorf("organize: the date of entry %d: %w", f.n.id, err)
	}
	return &d, nil
}

// indexMtime is the file's index modification time, 0 when unknown: the
// time a refused set_mtime item carries, as the schema requires one.
func (f *mediaFile) indexMtime() int64 { return f.mtime.Int64 }

// The date plans' queries, which the plan guard checks (r5 Interfaces):
// the targets by primary key, a folder's siblings by entries_by_name, and
// a digest's copies by file_content_by_content.
const (
	mediaFilesSQL = `SELECT ` + nodeColumns + `, e.mtime_ns, e.nlink, e.ext, md.effective_ns, md.local, md.offset_min,
		md.precision, md.meta_state, fc.content_id
		FROM ` + nodeFrom + ` LEFT JOIN media_dates md ON md.entry_id = e.id
		LEFT JOIN file_content fc ON fc.entry_id = e.id AND fc.state = 'hashed'
		WHERE e.id IN `
	siblingsSQL = `SELECT id, name FROM entries WHERE parent_id = ? AND kind <> 'directory' AND state <> 'missing'
		ORDER BY name`
	copiesSQL = `SELECT e.id, e.path FROM file_content fc JOIN entries e ON e.id = fc.entry_id
		WHERE fc.content_id = ? AND fc.state = 'hashed' AND e.source_id = ? AND e.state = 'present' AND e.id <> ?`
)

// loadMediaFiles reads ids, in that order.
func loadMediaFiles(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) ([]*mediaFile, error) {
	byID := make(map[int64]*mediaFile, len(ids))
	for chunk := range slices.Chunk(ids, 500) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = int64(id)
		}
		rows, err := tx.QueryContext(ctx, mediaFilesSQL+`(`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("organize: read the media files: %w", err)
		}
		for rows.Next() {
			var (
				f      mediaFile
				parent sql.NullInt64
				n      node
			)
			if err := rows.Scan(&n.id, &n.source, &parent, &n.name, &n.path, &n.kind, &n.state, &n.boundary,
				&n.mounts, &n.decision, &n.eff, &n.bytes, &n.files, &f.mtime, &f.nlink, &f.ext, &f.eff, &f.local,
				&f.offset, &f.precision, &f.meta, &f.content); err != nil {
				rows.Close()
				return nil, fmt.Errorf("organize: read the media files: %w", err)
			}
			n.parent = parent.Int64
			f.n = &n
			byID[n.id] = &f
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("organize: read the media files: %w", err)
		}
	}
	out := make([]*mediaFile, 0, len(ids))
	for _, id := range ids {
		if f, ok := byID[int64(id)]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// datePlanSource returns the source of a date plan's expanded targets,
// which must be one; fallback is the destination's, for targets that name
// only archive members.
func datePlanSource(ex dates.Expanded, fallback domain.SourceID) (domain.SourceID, error) {
	if ex.Source != "" {
		return ex.Source, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", domain.Errorf(domain.CodeInvalidRequest, "the targets name no file on a source; choose photos or videos")
}

// rederive derives the targets' dates afresh in the plan's transaction
// (r5 D9, D14, D16), so a move or a written time since the last media job
// is planned from.
func (s *Service) rederive(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) error {
	if s.dates == nil {
		return errors.New("organize: the date plans need Options.Dates")
	}
	return s.dates.Rederive(ctx, tx, ids)
}

// skipItem is the refused item of a target ExpandTargets skipped: a member
// ref, or an entry that is not a media file (not_media).
func (p *plan) skipItem(op string, sk dates.Skip) (*item, error) {
	it := &item{op: op, state: stateRefused, reason: reasonNotMedia}
	if op == opSetMtime {
		zero := int64(0)
		it.newMtime = &zero
	}
	if sk.Entry.IsMember() {
		c, err := memberCandidate(p.ctx, p.tx, sk.Entry, sk.Entry.String())
		if err != nil {
			return nil, err
		}
		it.fromPath = c.path
		return it, nil
	}
	n, err := loadNode(p.ctx, p.tx, int64(sk.Entry.Entry))
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, fmt.Errorf("organize: skipped entry %d is gone", sk.Entry.Entry)
	}
	it.entry, it.fromParent, it.fromName, it.fromPath = n.id, n.parent, n.name, n.path
	if op == opSetMtime {
		var mt sql.NullInt64
		if err := p.tx.QueryRowContext(p.ctx, `SELECT mtime_ns FROM entries WHERE id = ?`, n.id).Scan(&mt); err != nil {
			return nil, err
		}
		*it.newMtime = mt.Int64
	}
	return it, nil
}

// truncateTime rounds ns down to a multiple of res (r5 D14): a 2-second
// boundary on FAT.
func truncateTime(ns int64, res time.Duration) int64 {
	r := int64(res)
	if r <= 1 {
		return ns
	}
	m := ns % r
	if m < 0 {
		m += r
	}
	return ns - m
}

// sameTime is the unchanged-time test of R1 design D8 under the source's
// capabilities: within its resolution, or an hour apart within it on a
// local-time filesystem. The executor's set_mtime intent refuses no_change
// by the same test (r5 D13).
func sameTime(stored, observed int64, caps fsaccess.Capabilities) bool {
	res := int64(caps.TimeResolution)
	d := observed - stored
	if abs(d) <= res {
		return true
	}
	h := int64(time.Hour)
	return caps.LocalTime && (abs(d-h) <= res || abs(d+h) <= res)
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// writableTime reports whether a set_mtime may write ns on a filesystem of
// type fsType (r5 H1): a time the filesystem stores as given, which Linux
// would otherwise clamp without an error, and a known one, which the index
// reads back as a date. The executor's set_mtime intent checks the same.
func writableTime(fsType string, ns int64) bool {
	return domain.KnownModTime(ns) && fsaccess.StoresModTime(fsType, ns)
}

// planSetMtime plans plan-set-mtime (r5 D14): its targets expanded and
// re-derived, then one set_mtime item per media file to its effective
// instant, truncated to the source's time resolution. A file already at
// that time is counted in summary.unchanged, with no item. Refused:
// not_dated_yet, date_too_coarse, hard_link, date_out_of_range (a time the
// source's filesystem would clamp, or one that reads as unknown; r5 H1), and
// not_media. It is a bulk action, so it is always previewed.
func (s *Service) planSetMtime(ctx context.Context, tx *jobs.Tx, req planSetMtimeRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	ex, err := dates.ExpandTargets(ctx, q, req.Targets, maxItems)
	if err != nil {
		return 0, nil, err
	}
	src, err := datePlanSource(ex, "")
	if err != nil {
		return 0, nil, err
	}
	if err := s.checkSource(ctx, q, src); err != nil {
		return 0, nil, err
	}
	if err := s.rederive(ctx, q, ex.Media); err != nil {
		return 0, nil, err
	}
	if err := Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, src, true)
	if err != nil {
		return 0, nil, err
	}
	files, err := loadMediaFiles(ctx, q, ex.Media)
	if err != nil {
		return 0, nil, err
	}
	var sum setMtimeSummary
	for _, f := range files {
		n := f.n
		mt := f.indexMtime()
		it := &item{op: opSetMtime, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			bytes: n.bytes, files: n.files, state: statePlanned, newMtime: &mt}
		d, err := f.date()
		if err != nil {
			return 0, nil, err
		}
		switch {
		case f.notDatedYet():
			err = p.refused(it, reasonNotDatedYet)
		case d == nil || d.Precision != media.PrecisionSecond:
			err = p.refused(it, reasonDateTooCoarse)
		case f.nlink.Int64 > 1:
			err = p.refused(it, reasonHardLink)
		default:
			t := truncateTime(d.Instant.UnixNano(), p.caps.TimeResolution)
			if !writableTime(p.fsType, t) {
				err = p.refused(it, reasonDateOutOfRange)
				break
			}
			if f.mtime.Valid && domain.KnownModTime(f.mtime.Int64) && sameTime(f.mtime.Int64, t, p.caps) {
				sum.Unchanged++
				continue
			}
			it.newMtime = &t
			err = p.push(it)
		}
		if err != nil {
			return 0, nil, err
		}
	}
	for _, sk := range ex.Skipped {
		it, err := p.skipItem(opSetMtime, sk)
		if err != nil {
			return 0, nil, err
		}
		if err := p.push(it); err != nil {
			return 0, nil, err
		}
	}
	res, err := s.write(ctx, tx, p, "set_mtime", 0, 0)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, setMtimePlanResponse{PlanResponse: res, Summary: sum}, nil
}

// placement is where a date organize puts a file: the folder path below
// the destination and the name, or why it cannot (a refusal reason).
type placement struct {
	f      *mediaFile
	rel    []byte
	name   []byte
	reason string
}

// organizer plans the items of a date organize (r5 D16, D17).
type organizer struct {
	p    *plan
	m    *merger
	tmpl media.Template
	base dest
	// placed are the entries the plan's renames put in each folder (by
	// dest.key), by the name they take there.
	placed map[int64]*names
	// destOf is the folder (dest.key) each planned file goes to.
	destOf map[int64]int64
	// contents are the digests of the targets, by entry.
	contents map[int64]int64
}

// planDateOrganize plans plan-date-organize (r5 D16, D17): the targets
// expanded and re-derived, then, in path order, each media file moved
// below the destination into the folders the template names for its
// effective date (missing ones made first, once each), under its name, or
// "YYYYMMDD_HHMMSS_<name>" with rename. A name taken by an identical
// hashed copy refuses the file identical_copy; one taken otherwise gives it
// the first free "stem (k)ext". It is a bulk action.
func (s *Service) planDateOrganize(ctx context.Context, tx *jobs.Tx, req planDateOrganizeRequest, tmpl media.Template) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	dst, err := folderArg(ctx, q, "destination_id", req.DestinationID)
	if err != nil {
		return 0, nil, err
	}
	ex, err := dates.ExpandTargets(ctx, q, req.Targets, maxItems)
	if err != nil {
		return 0, nil, err
	}
	src, err := datePlanSource(ex, dst.source)
	if err != nil {
		return 0, nil, err
	}
	if src != dst.source {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest,
			"the destination is on source %q and the targets on %q; files move only inside one source", dst.source, src)
	}
	if err := s.checkSource(ctx, q, src); err != nil {
		return 0, nil, err
	}
	if err := s.rederive(ctx, q, ex.Media); err != nil {
		return 0, nil, err
	}
	if err := Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, src, true)
	if err != nil {
		return 0, nil, err
	}
	p.template, p.rename = tmpl.String(), req.Rename
	files, err := loadMediaFiles(ctx, q, ex.Media)
	if err != nil {
		return 0, nil, err
	}
	o := &organizer{p: p, tmpl: tmpl, base: folderDest(dst), placed: map[int64]*names{}, destOf: map[int64]int64{},
		contents: map[int64]int64{}}
	o.m = &merger{p: p, folders: map[string]folderAt{"": {d: o.base}}, made: map[int64]*names{}}
	for _, f := range files {
		if f.content.Valid {
			o.contents[f.n.id] = f.content.Int64
		}
	}

	// Where each file goes, then the folders on the way, made before any
	// file goes into them, then one move per file, in path order.
	places := make([]placement, len(files))
	for i, f := range files {
		if places[i], err = o.place(f, req.Rename); err != nil {
			return 0, nil, err
		}
	}
	for _, pl := range places {
		if pl.reason == "" {
			if _, err := o.m.resolve(pl.rel); err != nil {
				return 0, nil, err
			}
		}
	}
	for _, pl := range places {
		if err := o.move(pl); err != nil {
			return 0, nil, err
		}
	}
	for _, sk := range ex.Skipped {
		it, err := p.skipItem(opRename, sk)
		if err != nil {
			return 0, nil, err
		}
		if err := p.push(it); err != nil {
			return 0, nil, err
		}
	}
	p.dropUnusedMkdirs()
	var sum dateOrganizeSummary
	if sum.SplitSiblings, err = o.siblings(); err != nil {
		return 0, nil, err
	}
	if sum.FilesWithCopies, err = o.filesWithCopies(ex.Media); err != nil {
		return 0, nil, err
	}
	res, err := s.write(ctx, tx, p, "date_organize", dst.id, 0)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, dateOrganizePlanResponse{PlanResponse: res, Summary: sum}, nil
}

// place decides the folder path and name of f, or why it cannot go:
// not_dated_yet, date_too_coarse (no date, or one coarser than the template
// or the rename needs), or invalid_name (a folder or name the source's
// filesystem cannot hold).
func (o *organizer) place(f *mediaFile, rename bool) (placement, error) {
	pl := placement{f: f, name: f.n.name}
	if f.notDatedYet() {
		pl.reason = reasonNotDatedYet
		return pl, nil
	}
	d, err := f.date()
	if err != nil {
		return pl, err
	}
	if d == nil {
		pl.reason = reasonDateTooCoarse
		return pl, nil
	}
	comps, err := o.tmpl.Folders(*d, media.EventName(parentName(f.n.path)))
	if errors.Is(err, media.ErrTooCoarse) {
		pl.reason = reasonDateTooCoarse
		return pl, nil
	}
	if err != nil {
		return pl, err
	}
	if rename {
		name, err := media.RenamedName(f.n.name, *d)
		if errors.Is(err, media.ErrTooCoarse) {
			pl.reason = reasonDateTooCoarse
			return pl, nil
		}
		if err != nil {
			return pl, err
		}
		pl.name = name
	}
	pl.rel = bytes.Join(comps, []byte("/"))
	for i, c := range comps {
		if !o.holdable(c) || i == 0 && len(o.base.path) == 0 && string(c) == index.QuarantineName {
			pl.reason = reasonInvalidName
			return pl, nil
		}
	}
	if !o.holdable(pl.name) {
		pl.reason = reasonInvalidName
	}
	return pl, nil
}

// holdable reports whether the source's filesystem can hold name: a valid
// name of at most 255 bytes, and one its type allows (design V3).
func (o *organizer) holdable(name []byte) bool {
	return fsaccess.ValidateName("name", name) == nil && len(name) <= maxNameBytes && o.p.holdable(name) == nil
}

// parentName is the name of the folder holding path ("" at a source's
// top).
func parentName(path []byte) []byte {
	i := bytes.LastIndexByte(path, '/')
	if i < 0 {
		return nil
	}
	dir := path[:i]
	return dir[bytes.LastIndexByte(dir, '/')+1:]
}

// move plans the item of placement pl.
func (o *organizer) move(pl placement) error {
	p, n := o.p, pl.f.n
	if pl.reason != "" {
		it := &item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			bytes: n.bytes, files: n.files}
		if pl.rel != nil || pl.reason == reasonInvalidName {
			it.toName, it.toPath = pl.name, joinPath(joinPath(o.base.path, pl.rel), pl.name)
		}
		return p.refused(it, pl.reason)
	}
	f, err := o.m.resolve(pl.rel)
	if err != nil {
		return err
	}
	if f.blocked != "" {
		return p.push(&item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			toName: pl.name, toPath: joinPath(f.d.path, pl.name), bytes: n.bytes, files: n.files, state: stateConflict,
			reason: f.blocked})
	}
	d := f.d
	if r := p.refusal(n, d, pl.name); r != "" {
		_, err := p.move(n, d, pl.name, 0)
		return err
	}
	name, copyOf, reason, err := o.freeName(n, d, pl.name)
	if err != nil {
		return err
	}
	switch {
	case copyOf != 0:
		return p.push(&item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			toParent: d.id, toDirSeq: d.seq, toName: pl.name, toPath: joinPath(d.path, pl.name), bytes: n.bytes,
			files: n.files, state: stateRefused, reason: reasonIdenticalCopy, copyOf: copyOf})
	case reason != "":
		return p.push(&item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
			toParent: d.id, toDirSeq: d.seq, toName: pl.name, toPath: joinPath(d.path, pl.name), bytes: n.bytes,
			files: n.files, state: stateConflict, reason: reason})
	}
	it, err := p.move(n, d, name, 0)
	if err != nil {
		return err
	}
	if it.state == statePlanned {
		o.placedNames(d).add(name, n.id)
		o.destOf[n.id] = d.key()
	}
	return nil
}

func (o *organizer) placedNames(d dest) *names {
	s, ok := o.placed[d.key()]
	if !ok {
		s = newNames(o.p.sensitive)
		o.placed[d.key()] = s
	}
	return s
}

// Who holds a name in a destination folder (r5 D17).
const (
	heldByNone = iota
	heldByEntry
	heldByMissing
)

// holder says who takes name in d for n: a present child, or an earlier
// item of the plan (the entry it moves, or 0 for a folder it makes), or a
// missing entry with the owner's intent there.
func (o *organizer) holder(n *node, d dest, name []byte) (int, int64, error) {
	children, err := o.p.childNames(d)
	if err != nil {
		return 0, 0, err
	}
	if id, ok := children.holder(name, n.id); ok {
		return heldByEntry, id, nil
	}
	if id, ok := o.placedNames(d).holder(name, -1); ok {
		return heldByEntry, id, nil
	}
	if _, ok := o.p.plannedNames(d).holder(name, -1); ok {
		return heldByEntry, 0, nil
	}
	missing, err := index.MissingIntentAt(o.p.ctx, o.p.tx, o.p.src, joinPath(d.path, name))
	if err != nil {
		return 0, 0, err
	}
	if missing {
		return heldByMissing, 0, nil
	}
	return heldByNone, 0, nil
}

// freeName is the name n takes in d (r5 D17): name when free; else, when
// an entry with the same hashed content holds it, none, refused as a copy
// of that entry; else the first free "stem (k)ext", or a conflict past
// maxSuffix. A name a missing entry holds with the owner's intent (a date
// correction included) is a conflict, name_taken_by_missing.
func (o *organizer) freeName(n *node, d dest, name []byte) (free []byte, copyOf int64, reason string, err error) {
	by, id, err := o.holder(n, d, name)
	if err != nil {
		return nil, 0, "", err
	}
	switch by {
	case heldByNone:
		return name, 0, "", nil
	case heldByMissing:
		return nil, 0, reasonNameTakenByMissing, nil
	}
	if id != 0 {
		same, err := o.sameContent(n.id, id)
		if err != nil {
			return nil, 0, "", err
		}
		if same {
			return nil, id, "", nil
		}
	}
	stem, ext := splitExt(name)
	for k := 1; k <= maxSuffix; k++ {
		cand := make([]byte, 0, len(name)+8)
		cand = append(cand, stem...)
		cand = append(cand, " ("...)
		cand = strconv.AppendInt(cand, int64(k), 10)
		cand = append(cand, ')')
		cand = append(cand, ext...)
		if len(cand) > maxNameBytes {
			break
		}
		by, _, err := o.holder(n, d, cand)
		if err != nil {
			return nil, 0, "", err
		}
		if by == heldByNone {
			return cand, 0, "", nil
		}
	}
	return nil, 0, reasonNameTaken, nil
}

// splitExt splits name at its extension: the last "." that is not its
// first byte (r5 D17).
func splitExt(name []byte) (stem, ext []byte) {
	if i := bytes.LastIndexByte(name, '.'); i > 0 {
		return name[:i], name[i:]
	}
	return name, nil
}

// sameContent reports whether entries a and b are both hashed with the
// same content.
func (o *organizer) sameContent(a, b int64) (bool, error) {
	ca, ok := o.contents[a]
	if !ok {
		return false, nil
	}
	cb, ok := o.contents[b]
	if !ok {
		var c sql.NullInt64
		err := o.p.tx.QueryRowContext(o.p.ctx, `SELECT content_id FROM file_content WHERE entry_id = ? AND state = 'hashed'`,
			b).Scan(&c)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("organize: digest of entry %d: %w", b, err)
		}
		if !c.Valid {
			return false, nil
		}
		cb = c.Int64
	}
	return ca == cb, nil
}

// siblings writes, on each planned file that leaves a same-stem sibling in
// its folder behind (one not planned into the same destination folder),
// those siblings' names in its detail, comma-separated, and counts those
// files (r5 D16).
func (o *organizer) siblings() (int64, error) {
	p := o.p
	inFolder := map[int64][]named{}
	var count int64
	for _, it := range p.items {
		if it.op != opRename || it.state != statePlanned {
			continue
		}
		kids, ok := inFolder[it.fromParent]
		if !ok {
			rows, err := p.tx.QueryContext(p.ctx, siblingsSQL, it.fromParent)
			if err != nil {
				return 0, fmt.Errorf("organize: siblings in folder %d: %w", it.fromParent, err)
			}
			for rows.Next() {
				var k named
				if err := rows.Scan(&k.id, &k.name); err != nil {
					rows.Close()
					return 0, err
				}
				kids = append(kids, k)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return 0, err
			}
			inFolder[it.fromParent] = kids
		}
		stem, _ := splitExt(it.fromName)
		var left []string
		for _, k := range kids {
			if k.id == it.entry {
				continue
			}
			ks, _ := splitExt(k.name)
			if !sameName(p.sensitive, ks, stem) {
				continue
			}
			if to, ok := o.destOf[k.id]; ok && to == o.destOf[it.entry] {
				continue
			}
			left = append(left, domain.DisplayName(k.name))
		}
		if len(left) > 0 {
			it.detail = strings.Join(left, ", ")
			count++
		}
	}
	return count, nil
}

// filesWithCopies counts the planned files with a hashed copy, another
// present file of the same content, among the targets or at or below the
// destination, outside the quarantine (r5 D17).
func (o *organizer) filesWithCopies(targets []domain.EntryID) (int64, error) {
	p := o.p
	isTarget := make(map[int64]bool, len(targets))
	for _, id := range targets {
		isTarget[int64(id)] = true
	}
	var count int64
	for _, it := range p.items {
		if it.op != opRename || it.state != statePlanned {
			continue
		}
		c, ok := o.contents[it.entry]
		if !ok {
			continue
		}
		rows, err := p.tx.QueryContext(p.ctx, copiesSQL, c, string(p.src), it.entry)
		if err != nil {
			return 0, fmt.Errorf("organize: copies of entry %d: %w", it.entry, err)
		}
		found := false
		for rows.Next() {
			var (
				id   int64
				path []byte
			)
			if err := rows.Scan(&id, &path); err != nil {
				rows.Close()
				return 0, err
			}
			if !found && !index.IsQuarantinePath(path) && (isTarget[id] || below(path, o.base.path)) {
				found = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		if found {
			count++
		}
	}
	return count, nil
}
