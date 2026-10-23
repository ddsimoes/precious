package api

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/rules"
	"precious/internal/search"
)

// Archive members as EntryRows (R2 design D7, D16). A member row has the ID
// "m<id>", its archive's source, the member's name, and the path
// "<archive path>!<member path>"; it is present, has no decision or tags of
// its own, and reads its archive's effective decision. A file member's kind
// comes from its name by the rules' file kind table; it has no category. A
// member folder's figures (newest and oldest time, main kind, composition,
// candidate, checked, and duplicated bytes) are computed on read from the
// members below it, which one archive bounds (design D10).

// memberColumns and memberFrom read a member of a complete archive outside
// the quarantine (r4 design D2; a WHERE condition follows memberFrom): the
// columns memberRows scans. A file member's copies count it and every other
// physical copy.
var memberColumns = `m.id, m.archive_id, ae.source_id, m.name, ae.path, m.path, a.format = 'zip', m.kind, m.size,
	m.total_bytes, m.total_files, m.mtime_ns, ae.eff_decision, m.state,
	CASE WHEN m.state = 'hashed' THEN ` + search.CopiesSQL("m.content_id") + `
		WHEN m.state = 'unique_size' THEN 1 END`

var memberFrom = `
	FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id AND a.state = 'complete'
	JOIN entries ae ON ae.id = m.archive_id AND ` + index.NotQuarantined("ae")

// memberPlaceColumns are a member's own place and mark (r5c design D6),
// before its archive's coverage, and whether it has a member_places row
// (mp) with its figures, as placeMembers reads them.
var memberPlaceColumns = search.MemberPlaceSQL("m", "ae") + `, ` + search.MemberOfflineOnlySQL("m", "ae") + `,
	mp.member_id IS NOT NULL, ` + sumsColumns("mp")

// memberCopySQL holds for a hashed file member m with another physical
// copy outside the quarantine: a present file entry, or a member of a
// complete archive other than m and its tar hard links.
var memberCopySQL = `CASE WHEN m.state = 'hashed' THEN
	EXISTS (SELECT 1 FROM file_content of JOIN entries oe ON oe.id = of.entry_id
		WHERE of.content_id = m.content_id AND oe.state = 'present' AND ` + index.NotQuarantined("oe") + `)
	OR EXISTS (SELECT 1 FROM archive_members om JOIN archives oa ON oa.entry_id = om.archive_id
		JOIN entries oae ON oae.id = oa.entry_id
		WHERE om.content_id = m.content_id AND om.kind = 'file'
			AND coalesce(om.link_member, om.id) <> coalesce(m.link_member, m.id)
			AND oa.state = 'complete' AND oae.state = 'present' AND ` + index.NotQuarantined("oae") + `)
	ELSE 0 END`

// member is a member's row with the facts its folder figures need, and,
// once placed (placeMembers), its own place and mark (own, offline: before
// its archive's coverage), a member folder's stored sums (nil without a
// member_places row), and its place.
type member struct {
	row     search.Row
	archive domain.EntryID
	path    []byte // the path inside the archive
	own     sql.NullString
	offline bool
	sums    *domain.PlaceSums
	pl      *placed
}

// memberRows reads the members matching where (a condition on m), in no
// particular order, without their folder figures or places.
func memberRows(ctx context.Context, tx *sql.Tx, pol *rules.Policy, where string, args ...any) ([]member, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+memberColumns+memberFrom+` WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("api: members: %w", err)
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		var (
			m                        member
			id, archive              int64
			source, kind, eff        string
			name, archivePath, mpath []byte
			mtime                    sql.NullInt64
			state                    sql.NullString
		)
		r := &m.row
		if err := rows.Scan(&id, &archive, &source, &name, &archivePath, &mpath, &r.Zip, &kind, &r.Size,
			&r.TotalBytes, &r.TotalFiles, &mtime, &eff, &state, &r.Copies); err != nil {
			return nil, fmt.Errorf("api: members: %w", err)
		}
		m.archive, m.path = domain.EntryID(archive), mpath
		r.Member, r.ArchiveID, r.Source = domain.MemberID(id), m.archive, domain.SourceID(source)
		r.Name, r.MemberPath = name, mpath
		r.Path = append(append(archivePath[:len(archivePath):len(archivePath)], '!'), mpath...)
		r.State, r.EffDecision = "present", domain.Decision(eff)
		r.ContentState = domain.ContentState(state.String)
		r.MTime = search.NsTime(mtime)
		switch domain.MemberKind(kind) {
		case domain.MemberDirectory:
			r.Kind = domain.EntryDirectory
			r.Composition = []search.FamilyAmount{}
		case domain.MemberFile:
			r.Kind = domain.EntryFile
			r.FileKind = pol.FileKind(name)
			r.Newest, r.Oldest = r.MTime, r.MTime
			r.Composition = []search.FamilyAmount{{Family: domain.FileFamily("", r.FileKind), Bytes: r.Size, Files: 1}}
		case domain.MemberSymlink:
			r.Kind, r.Composition = domain.EntrySymlink, []search.FamilyAmount{}
		default:
			r.Kind, r.Composition = domain.EntryUnknown, []search.FamilyAmount{}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: members: %w", err)
	}
	return out, nil
}

// placeMembers places the members ms of c's archive (r5c design D6), in
// one read per chunk of IDs: a file's own place and mark live, by
// search.MemberPlaceSQL, then its place under c (placedOf); a folder's from
// its member_places row, which the relate pass computed under c.
func placeMembers(ctx context.Context, tx *sql.Tx, pol *rules.Policy, c *cover, ms []*member) error {
	const chunk = 500
	byID := make(map[int64]*member, min(len(ms), chunk))
	for len(ms) > 0 {
		n := min(len(ms), chunk)
		args := make([]any, n)
		clear(byID)
		for i, m := range ms[:n] {
			args[i] = int64(m.row.Member)
			byID[int64(m.row.Member)] = m
		}
		rows, err := tx.QueryContext(ctx, `SELECT m.id, `+memberPlaceColumns+`
			FROM archive_members m JOIN entries ae ON ae.id = m.archive_id
			LEFT JOIN member_places mp ON mp.member_id = m.id
			WHERE m.id IN (`+marks(n)+`)`, args...)
		if err != nil {
			return fmt.Errorf("api: places of members: %w", err)
		}
		for rows.Next() {
			var (
				id     int64
				own    sql.NullString
				off    bool
				stored bool
				sums   domain.PlaceSums
			)
			if err := rows.Scan(append([]any{&id, &own, &off, &stored}, sumsDest(&sums)...)...); err != nil {
				rows.Close()
				return fmt.Errorf("api: places of members: %w", err)
			}
			m := byID[id]
			m.own, m.offline = own, off
			if stored && m.row.Kind == domain.EntryDirectory {
				m.sums = &sums
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("api: places of members: %w", err)
		}
		for _, m := range ms[:n] {
			m.pl = c.placedOf(m, pol)
		}
		ms = ms[n:]
	}
	return nil
}

// memberPtrs points at each of ms.
func memberPtrs(ms []member) []*member {
	out := make([]*member, len(ms))
	for i := range ms {
		out[i] = &ms[i]
	}
	return out
}

// memberAgg sums the members below a member folder or an archive.
type memberAgg struct {
	dirs, files                    int64
	candidate, checked, duplicated int64
	newest, oldest                 int64
	dated                          bool
	b                              *breakdowns
}

// newMemberAgg is an empty aggregate.
func newMemberAgg() *memberAgg {
	return &memberAgg{b: newBreakdowns()}
}

// add counts one member below the folder.
func (a *memberAgg) add(pol *rules.Policy, kind string, name []byte, size int64, mtime sql.NullInt64,
	state sql.NullString, copied bool) {
	switch domain.MemberKind(kind) {
	case domain.MemberDirectory:
		a.dirs++
		return
	case domain.MemberFile:
	default:
		return
	}
	a.files++
	fk := pol.FileKind(name)
	one := amount{Files: 1, Bytes: size}
	k := a.b.kinds[fk]
	k.add(one)
	a.b.kinds[fk] = k
	fam := domain.FileFamily("", fk)
	f := a.b.families[fam]
	f.add(one)
	a.b.families[fam] = f
	y := unknownYear
	if t := search.NsTime(mtime); search.KnownTime(t) {
		y = t.Year()
		if !a.dated || mtime.Int64 > a.newest {
			a.newest = mtime.Int64
		}
		if !a.dated || mtime.Int64 < a.oldest {
			a.oldest = mtime.Int64
		}
		a.dated = true
	}
	c := a.b.years[y]
	c.add(one)
	a.b.years[y] = c
	switch st := domain.ContentState(state.String); {
	case !state.Valid, st == domain.ContentUniqueSize, st == domain.ContentUnreadable:
	case st == domain.ContentHashed, st == domain.ContentSampled:
		a.candidate += size
		a.checked += size
	default:
		a.candidate += size
	}
	if copied {
		a.duplicated += size
	}
}

// apply sets the folder figures of r from a.
func (a *memberAgg) apply(r *search.Row) {
	if a.dated {
		r.Newest, r.Oldest = time.Unix(0, a.newest).UTC(), time.Unix(0, a.oldest).UTC()
	}
	var (
		best domain.FileKind
		bt   amount
	)
	for k, t := range a.b.kinds {
		if best == "" || cmp.Or(cmp.Compare(t.Bytes, bt.Bytes), cmp.Compare(t.Files, bt.Files), cmp.Compare(best, k)) > 0 {
			best, bt = k, t
		}
	}
	r.MainKind = best
	r.Composition = []search.FamilyAmount{}
	for _, f := range a.b.familyList() {
		if f.Bytes != 0 || f.Files != 0 {
			r.Composition = append(r.Composition, f)
		}
	}
	r.CandidateBytes = sql.NullInt64{Int64: a.candidate, Valid: true}
	r.CheckedBytes = sql.NullInt64{Int64: a.checked, Valid: true}
	r.DuplicatedBytes = sql.NullInt64{Int64: a.duplicated, Valid: true}
}

// memberSubtree passes every member of archive whose path lies below prefix
// (a folder's path and "/", or empty for the whole archive) to the
// aggregate at returns for its path relative to prefix; at returns nil to
// skip a member. It is one read of the archive's (archive_id, path) index.
func memberSubtree(ctx context.Context, tx *sql.Tx, pol *rules.Policy, archive domain.EntryID, prefix []byte,
	at func(rel []byte) *memberAgg) error {
	q := `SELECT m.kind, m.name, m.path, m.size, m.mtime_ns, m.state, ` + memberCopySQL + `
		FROM archive_members m WHERE m.archive_id = ?`
	args := []any{int64(archive)}
	if len(prefix) > 0 {
		q += ` AND m.path >= ? AND m.path < ?`
		end := append(prefix[:len(prefix)-1:len(prefix)-1], '0')
		args = append(args, prefix, end)
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("api: members of archive %s: %w", archive, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			kind       string
			name, path []byte
			size       int64
			mtime      sql.NullInt64
			state      sql.NullString
			copied     bool
		)
		if err := rows.Scan(&kind, &name, &path, &size, &mtime, &state, &copied); err != nil {
			return fmt.Errorf("api: members of archive %s: %w", archive, err)
		}
		if a := at(path[len(prefix):]); a != nil {
			a.add(pol, kind, name, size, mtime, state, copied)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("api: members of archive %s: %w", archive, err)
	}
	return nil
}

// folderPrefix is the path prefix of the members below a member folder.
func folderPrefix(path []byte) []byte {
	return append(path[:len(path):len(path)], '/')
}

// fillFolder computes the figures of one member folder.
func fillFolder(ctx context.Context, tx *sql.Tx, pol *rules.Policy, m *member) (*memberAgg, error) {
	a := newMemberAgg()
	if err := memberSubtree(ctx, tx, pol, m.archive, folderPrefix(m.path), func([]byte) *memberAgg { return a }); err != nil {
		return nil, err
	}
	a.apply(&m.row)
	return a, nil
}

// fillChildren computes the figures of the folders among ms, the children
// of one level (prefix as memberSubtree takes it), in one read of the
// level's members.
func fillChildren(ctx context.Context, tx *sql.Tx, pol *rules.Policy, archive domain.EntryID, prefix []byte, ms []member) error {
	folders := map[string]*memberAgg{}
	for i := range ms {
		if ms[i].row.Kind == domain.EntryDirectory {
			folders[string(ms[i].row.Name)] = newMemberAgg()
		}
	}
	if len(folders) == 0 {
		return nil
	}
	err := memberSubtree(ctx, tx, pol, archive, prefix, func(rel []byte) *memberAgg {
		i := bytes.IndexByte(rel, '/')
		if i < 0 {
			return nil // a child itself
		}
		return folders[string(rel[:i])]
	})
	if err != nil {
		return err
	}
	for i := range ms {
		if a := folders[string(ms[i].row.Name)]; a != nil && ms[i].row.Kind == domain.EntryDirectory {
			a.apply(&ms[i].row)
		}
	}
	return nil
}

// memberByID reads one member with its folder figures, and the folder's
// sums (nil for anything but a folder); with places, also its place under
// its archive's coverage. An unknown member, or one of an archive that is
// not complete, is not_found.
func memberByID(ctx context.Context, tx *sql.Tx, pol *rules.Policy, id domain.MemberID, places bool) (*member, *memberAgg, error) {
	ms, err := memberRows(ctx, tx, pol, `m.id = ?`, int64(id))
	if err != nil {
		return nil, nil, err
	}
	if len(ms) == 0 {
		return nil, nil, domain.Errorf(domain.CodeNotFound, "member %s not found", domain.Ref{Member: id})
	}
	m := &ms[0]
	if places {
		c, err := coverOf(ctx, tx, m.archive)
		if err != nil {
			return nil, nil, err
		}
		if err := placeMembers(ctx, tx, pol, c, []*member{m}); err != nil {
			return nil, nil, err
		}
	}
	var a *memberAgg
	if m.row.Kind == domain.EntryDirectory {
		if a, err = fillFolder(ctx, tx, pol, m); err != nil {
			return nil, nil, err
		}
	}
	return m, a, nil
}

// memberAncestors lists what holds member m, from the source root: the
// archive's folders, the archive entry, then the member folders above m,
// each with its only_child as ancestors computes it.
func memberAncestors(ctx context.Context, tx *sql.Tx, m *member) ([]ancestor, error) {
	out, err := ancestors(ctx, tx, m.archive)
	if err != nil {
		return nil, err
	}
	var name []byte
	if err := tx.QueryRowContext(ctx, `SELECT name FROM entries WHERE id = ?`, int64(m.archive)).Scan(&name); err != nil {
		return nil, fmt.Errorf("api: archive %s: %w", m.archive, err)
	}
	out = append(out, ancestor{ID: m.archive.String(), Name: domain.DisplayName(name), NameB64: name})
	archiveAt := len(out) - 1
	// top is the member at the top of the archive that holds m, m included.
	top := int64(m.row.Member)
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE up(id, child, depth) AS (
			SELECT parent_id, id, 1 FROM archive_members WHERE id = ? AND parent_id IS NOT NULL
			UNION ALL
			SELECT m.parent_id, m.id, u.depth + 1 FROM up u JOIN archive_members m ON m.id = u.id WHERE m.parent_id IS NOT NULL
		)
		SELECT m.id, m.name, NOT EXISTS (SELECT 1 FROM archive_members c WHERE c.parent_id = u.id AND c.id <> u.child)
		FROM up u JOIN archive_members m ON m.id = u.id ORDER BY u.depth DESC`, int64(m.row.Member))
	if err != nil {
		return nil, fmt.Errorf("api: ancestors of %s: %w", domain.Ref{Member: m.row.Member}, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			only bool
		)
		if err := rows.Scan(&id, &name, &only); err != nil {
			return nil, err
		}
		if len(out) == archiveAt+1 {
			top = id
		}
		out = append(out, ancestor{ID: domain.Ref{Member: domain.MemberID(id)}.String(),
			Name: domain.MemberDisplayName(name, m.row.Zip), NameB64: name, OnlyChild: only})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM archive_members
		WHERE archive_id = ? AND parent_id IS NULL AND id <> ?)`, int64(m.archive), top).Scan(&out[archiveAt].OnlyChild); err != nil {
		return nil, fmt.Errorf("api: archive %s: %w", m.archive, err)
	}
	return out, nil
}

// level is what children and treemap list: an entry's children, or the
// members at the top of a complete archive or inside a member folder.
type level struct {
	// entry is the entry whose children are listed, when the level is no
	// archive's.
	entry domain.EntryID
	// archive is set for an archive's level; folder is then the member
	// folder (nil for the archive's top).
	archive domain.EntryID
	folder  *member
	// leaf is a member that is no folder: it has no children.
	leaf *member
	// cover is set, with places, on an archive's level and a leaf: the
	// archive's own coverage, read once for the request (r5c design D6).
	cover *cover
}

// resolveLevel resolves a ref to the level children and treemap list, with
// its archive's coverage, and the place of the leaf or of the member folder
// listed, when places is set. An unknown entry, or an unknown member, is
// not_found.
func resolveLevel(ctx context.Context, tx *sql.Tx, pol *rules.Policy, ref domain.Ref, places bool) (level, error) {
	var l level
	if ref.IsMember() {
		ms, err := memberRows(ctx, tx, pol, `m.id = ?`, int64(ref.Member))
		if err != nil {
			return level{}, err
		}
		if len(ms) == 0 {
			return level{}, domain.Errorf(domain.CodeNotFound, "member %s not found", ref)
		}
		if ms[0].row.Kind != domain.EntryDirectory {
			l.leaf = &ms[0]
		} else {
			l.archive, l.folder = ms[0].archive, &ms[0]
		}
		if places {
			if l.cover, err = coverOf(ctx, tx, ms[0].archive); err != nil {
				return level{}, err
			}
			if err := placeMembers(ctx, tx, pol, l.cover, []*member{&ms[0]}); err != nil {
				return level{}, err
			}
		}
		return l, nil
	}
	var complete bool
	err := tx.QueryRowContext(ctx, `SELECT e.kind = 'file' AND ifnull(a.state = 'complete', 0)
		FROM entries e LEFT JOIN archives a ON a.entry_id = e.id WHERE e.id = ?`, int64(ref.Entry)).Scan(&complete)
	if errors.Is(err, sql.ErrNoRows) {
		return level{}, notFound(ref.Entry)
	}
	if err != nil {
		return level{}, fmt.Errorf("api: entry %s: %w", ref.Entry, err)
	}
	if !complete {
		return level{entry: ref.Entry}, nil
	}
	l.archive = ref.Entry
	if places {
		if l.cover, err = coverOf(ctx, tx, ref.Entry); err != nil {
			return level{}, err
		}
	}
	return l, nil
}

// members reads the members of an archive level with their folder
// figures, without their places (place); a leaf has none.
func (l level) members(ctx context.Context, tx *sql.Tx, pol *rules.Policy) ([]member, error) {
	if l.leaf != nil {
		return nil, nil
	}
	var (
		ms     []member
		prefix []byte
		err    error
	)
	if l.folder == nil {
		ms, err = memberRows(ctx, tx, pol, `m.archive_id = ? AND m.parent_id IS NULL`, int64(l.archive))
	} else {
		prefix = folderPrefix(l.folder.path)
		ms, err = memberRows(ctx, tx, pol, `m.parent_id = ?`, int64(l.folder.row.Member))
	}
	if err != nil {
		return nil, err
	}
	if err := fillChildren(ctx, tx, pol, l.archive, prefix, ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// place places members ms of the level when it has its archive's coverage
// (placeMembers); without, it does nothing.
func (l level) place(ctx context.Context, tx *sql.Tx, pol *rules.Policy, ms []*member) error {
	if l.cover == nil {
		return nil
	}
	return placeMembers(ctx, tx, pol, l.cover, ms)
}

// sortMembers orders the rows of a member level as o orders children: by
// the key, then by member ID, in the order's direction; a folder without a
// newest time sorts as the smallest key.
func (o childOrder) sortMembers(ms []member) {
	slices.SortFunc(ms, func(a, b member) int {
		return o.cmpDir(o.keyOf(&a.row, a.pl), o.keyOf(&b.row, b.pl))
	})
}
