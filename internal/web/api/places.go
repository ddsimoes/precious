package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/relations"
	"precious/internal/rules"
	"precious/internal/search"
)

// Places on read (r5c design D2, D5, D6, D9, D10, D11, D14). With places=1
// every row of children, treemap, and the entry carries its place: a file
// or member file live (search.PlaceSQL, search.MemberPlaceSQL), a folder or
// complete archive from its stored entry_places row, a member folder from
// its stored member_places row. Nothing is cached: every request reads the
// live places.

// parsePlaces reads places: "" (off) or "1".
func parsePlaces(v string) (bool, error) {
	switch v {
	case "":
		return false, nil
	case "1":
		return true, nil
	}
	return false, domain.Errorf(domain.CodeInvalidRequest, "places must be 1, not %q", v)
}

// parseHide reads hide: "" or other_source, which needs places=1.
func parseHide(v string, places bool) (bool, error) {
	switch v {
	case "":
		return false, nil
	case string(domain.PlaceOtherSource):
		if !places {
			return false, domain.Errorf(domain.CodeInvalidRequest, "hide=other_source needs places=1")
		}
		return true, nil
	}
	return false, domain.Errorf(domain.CodeInvalidRequest, "hide must be other_source, not %q", v)
}

// sumsColumns are a places row's figures (alias t: entry_places,
// archive_places, or member_places), 0 without one, in sumsDest's order.
func sumsColumns(t string) string {
	return `ifnull(` + t + `.only_copy_files, 0), ifnull(` + t + `.only_copy_bytes, 0),
	ifnull(` + t + `.this_source_files, 0), ifnull(` + t + `.this_source_bytes, 0),
	ifnull(` + t + `.other_source_files, 0), ifnull(` + t + `.other_source_bytes, 0),
	ifnull(` + t + `.unchecked_files, 0), ifnull(` + t + `.unchecked_bytes, 0),
	ifnull(` + t + `.unreadable_files, 0), ifnull(` + t + `.unreadable_bytes, 0),
	ifnull(` + t + `.disposable_files, 0), ifnull(` + t + `.disposable_bytes, 0)`
}

func sumsDest(p *domain.PlaceSums) []any {
	return []any{&p.OnlyCopy.Files, &p.OnlyCopy.Bytes, &p.ThisSource.Files, &p.ThisSource.Bytes,
		&p.OtherSource.Files, &p.OtherSource.Bytes, &p.Unchecked.Files, &p.Unchecked.Bytes,
		&p.Unreadable.Files, &p.Unreadable.Bytes, &p.Disposable.Files, &p.Disposable.Bytes}
}

// placeRow is what an entry's place reads besides its file place: its
// kind, size, completeness facts, whether it is in the quarantine or a
// complete archive, its D14 flag, and its stored sums.
type placeRow struct {
	id                       int64
	kind                     string
	size                     int64
	facts                    placeFacts
	quarantined, archive     bool
	stored, disposable       bool
	sums                     domain.PlaceSums
	file                     domain.Place // a plain file's place ("" for null)
	offline                  bool
	contentState             sql.NullString
	otherSource, anotherCopy bool // the set-based answers (kidsSQL)
}

// placeRowColumns read the leading fields of a placeRow (placeRowDest) from
// entries e with dir_stats ds, archives ar, and entry_places ep.
var placeRowColumns = `e.id, e.kind, e.size, e.state, e.partial, e.mount_boundary,
	ifnull(ds.unreadable, 0), ifnull(ds.mount_boundaries, 0), NOT ` + index.NotQuarantined("e") + `,
	e.kind = 'file' AND ifnull(ar.state = 'complete', 0), ep.entry_id IS NOT NULL, ` +
	search.DisposableSQL("e") + `, ` + sumsColumns("ep")

func (r *placeRow) dest() []any {
	return append([]any{&r.id, &r.kind, &r.size, &r.facts.State, &r.facts.Partial, &r.facts.MountBoundary,
		&r.facts.UnreadableBelow, &r.facts.MountsBelow, &r.quarantined, &r.archive, &r.stored, &r.disposable},
		sumsDest(&r.sums)...)
}

// placeJoins are the joins placeRowColumns read, after entries e.
const placeJoins = ` LEFT JOIN dir_stats ds ON ds.entry_id = e.id
	LEFT JOIN file_content fc ON fc.entry_id = e.id
	LEFT JOIN archives ar ON ar.entry_id = e.id
	LEFT JOIN entry_places ep ON ep.entry_id = e.id`

// placed is r's place: null for a missing or quarantined entry; a folder's
// or complete archive's from its sums and completeness, not_computed
// without a row; a file's as read.
func (r *placeRow) placed() *placed {
	p := &placed{disposable: r.disposable, size: r.size}
	switch {
	case r.facts.State == "missing" || r.quarantined:
	case r.kind == string(domain.EntryDirectory) || r.archive:
		p.level = true
		if !r.stored {
			p.place = domain.PlaceNotComputed
			break
		}
		sums := r.sums
		p.sums = &sums
		p.place = sums.Place(complete(r.facts))
	default:
		p.place = r.file
		p.offline = r.offline && p.place == domain.PlaceOtherSource
	}
	return p
}

// placesSQL reads the placeRows of n entries by ID, each file's place by
// search.PlaceSQL and its mark by search.OfflineOnlySQL.
func placesSQL(n int) string {
	return `SELECT ` + placeRowColumns + `,
		CASE WHEN e.kind = 'file' AND ifnull(ar.state = 'complete', 0) THEN NULL ELSE ` + search.PlaceSQL("e", "fc") + ` END,
		` + search.OfflineOnlySQL("e", "fc") + `
		FROM entries e` + placeJoins + ` WHERE e.id IN (` + marks(n) + `)`
}

// placesOf reads the places of the entries ids.
func placesOf(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) (map[domain.EntryID]*placed, error) {
	out := make(map[domain.EntryID]*placed, len(ids))
	const chunk = 500
	for len(ids) > 0 {
		n := min(len(ids), chunk)
		args := make([]any, n)
		for i, id := range ids[:n] {
			args[i] = int64(id)
		}
		rows, err := tx.QueryContext(ctx, placesSQL(n), args...)
		if err != nil {
			return nil, fmt.Errorf("api: places: %w", err)
		}
		for rows.Next() {
			var (
				r    placeRow
				file sql.NullString
			)
			if err := rows.Scan(append(r.dest(), &file, &r.offline)...); err != nil {
				rows.Close()
				return nil, fmt.Errorf("api: places: %w", err)
			}
			r.file = domain.Place(file.String)
			out[domain.EntryID(r.id)] = r.placed()
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("api: places: %w", err)
		}
		rows.Close()
		ids = ids[n:]
	}
	return out, nil
}

// rowsPlaced returns the places of entry rows, in their order.
func rowsPlaced(ctx context.Context, tx *sql.Tx, rows []search.Row) ([]*placed, error) {
	ids := make([]domain.EntryID, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	byID, err := placesOf(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*placed, len(rows))
	for i := range rows {
		if out[i] = byID[rows[i].ID]; out[i] == nil {
			return nil, fmt.Errorf("api: places: entry %s vanished", rows[i].ID)
		}
	}
	return out, nil
}

// physKey is the physical identity of the file entry aliased x on its
// source aliased s, as search's copies count it: its hard-link set on a
// source with stable identity, else the entry itself.
func physKey(x, s string) string {
	return `CASE WHEN ` + x + `.nlink > 1 AND ` + x + `.ino IS NOT NULL AND ` + x + `.dev IS NOT NULL
		AND json_extract(` + s + `.capabilities, '$.stable_identity') = 1
		THEN 'h' || ` + x + `.source_id || '/' || ` + x + `.dev || '/' || ` + x + `.ino ELSE ` + x + `.id END`
}

// kidsSQL reads every child of folder ?1 (of one kind when kind is set),
// outside the quarantine, with what sort and hide need (D10): its sort
// columns, its placeRow, and, for a present hashed file, two answers about
// its content's other physical copies, as search.PlaceSQL asks them: is one
// on a source other than ?2 (the folder's), and is there one besides the
// file's own. Each distinct content is looked up once through
// file_content_by_content and archive_members_content; the second answer
// holds when the content's copies have two physical identities, one of
// which is the file's own (a member counts as one apart from every entry).
func kidsSQL(kind domain.EntryKind) string {
	cond := ""
	if kind != "" {
		// The kind is one of the validated constants, never the request's text.
		cond = ` AND e.kind = '` + string(kind) + `'`
	}
	return `WITH kid AS MATERIALIZED (
		SELECT ` + placeRowColumns + `, e.total_bytes, e.total_files, e.newest_ns, e.name,
			fc.state AS cs, fc.content_id AS cid,
			ifnull(e.state = 'present' AND e.kind = 'file' AND e.size > 0 AND fc.state = 'hashed'
				AND ifnull(ar.state, '') <> 'complete', 0) AS probed
		FROM entries e` + placeJoins + `
		WHERE e.parent_id = ?1 AND ` + index.NotQuarantined("e") + cond + `
	),
	probe AS (SELECT DISTINCT cid FROM kid WHERE probed AND cid IS NOT NULL),
	copy AS (
		SELECT p.cid AS cid, oe.source_id AS src, ` + physKey("oe", "os") + ` AS pk
		FROM probe p CROSS JOIN file_content of ON of.content_id = p.cid JOIN entries oe ON oe.id = of.entry_id
			JOIN sources os ON os.id = oe.source_id
		WHERE oe.state = 'present' AND ` + index.NotQuarantined("oe") + `
		UNION ALL
		SELECT p.cid, oae.source_id, 'm'
		FROM probe p CROSS JOIN archive_members om ON om.content_id = p.cid
			JOIN archives oa ON oa.entry_id = om.archive_id JOIN entries oae ON oae.id = oa.entry_id
		WHERE om.kind = 'file' AND oa.state = 'complete' AND oae.state = 'present' AND ` + index.NotQuarantined("oae") + `
	),
	ans AS (SELECT cid, max(src <> ?2) AS other, min(pk) IS NOT max(pk) AS another FROM copy GROUP BY cid)
	SELECT kid.*, ifnull(ans.other, 0), ifnull(ans.another, 0)
	FROM kid LEFT JOIN ans ON kid.probed AND ans.cid = kid.cid`
}

// filePlace is search.PlaceSQL's place of a file entry outside the
// quarantine from its content state cs and the two answers about its other
// copies, for the set-based sort and hide (D10).
func filePlace(state, kind string, size int64, cs sql.NullString, otherSource, another bool) domain.Place {
	switch {
	case state != "present":
		return ""
	case kind != string(domain.EntryFile) || size <= 0:
		return domain.PlaceNone
	case !cs.Valid:
		return domain.PlaceUnchecked
	}
	switch domain.ContentState(cs.String) {
	case domain.ContentUniqueSize, domain.ContentSampled:
		return domain.PlaceOnlyCopy
	case domain.ContentPending, domain.ContentChanged:
		return domain.PlaceUnchecked
	case domain.ContentUnreadable:
		return domain.PlaceUnreadable
	case domain.ContentHashed:
		switch {
		case otherSource:
			return domain.PlaceOtherSource
		case another:
			return domain.PlaceThisSource
		}
		return domain.PlaceOnlyCopy
	}
	return ""
}

// sorted is one child in a set-based order: its position and its place.
type sorted struct {
	key orderKey
	pl  *placed
}

// childPlacePage reads up to limit children of parent after the cursor in
// an order that needs every child's place: sort=only_here, or hide (D10).
// One statement places every child (kidsSQL); they are filtered and sorted
// here, and only the page's rows are read whole, with their places by
// search.PlaceSQL.
func childPlacePage(ctx context.Context, tx *sql.Tx, parent domain.EntryID, o childOrder, after *childCursor,
	limit int) ([]search.Row, []*placed, string, error) {
	var src string
	if err := tx.QueryRowContext(ctx, `SELECT source_id FROM entries WHERE id = ?`, int64(parent)).Scan(&src); err != nil {
		return nil, nil, "", fmt.Errorf("api: children of %s: %w", parent, err)
	}
	rows, err := tx.QueryContext(ctx, kidsSQL(o.kind), int64(parent), src)
	if err != nil {
		return nil, nil, "", fmt.Errorf("api: children of %s: %w", parent, err)
	}
	defer rows.Close()
	var all []sorted
	for rows.Next() {
		var (
			r      placeRow
			at     search.Row
			newest sql.NullInt64
			cid    sql.NullInt64
			probed bool
		)
		dest := append(r.dest(), &at.TotalBytes, &at.TotalFiles, &newest, &at.Name, &r.contentState, &cid, &probed,
			&r.otherSource, &r.anotherCopy)
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, "", fmt.Errorf("api: children of %s: %w", parent, err)
		}
		r.file = filePlace(r.facts.State, r.kind, r.size, r.contentState, r.otherSource, r.anotherCopy)
		p := r.placed()
		if o.hide && !p.kept() {
			continue
		}
		at.ID, at.Newest = domain.EntryID(r.id), search.NsTime(newest)
		all = append(all, sorted{key: o.keyOf(&at, p), pl: p})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, "", fmt.Errorf("api: children of %s: %w", parent, err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, "", fmt.Errorf("api: children of %s: %w", parent, err)
	}
	slices.SortFunc(all, func(a, b sorted) int { return o.cmpDir(a.key, b.key) })
	start := 0
	if after != nil {
		at := after.key()
		start, _ = slices.BinarySearchFunc(all, at, func(s sorted, at orderKey) int {
			if o.cmpDir(s.key, at) <= 0 {
				return -1
			}
			return 1
		})
	}
	page := all[start:min(len(all), start+limit+1)]
	var next string
	if len(page) > limit {
		page = page[:limit]
		next = o.cursorAt(page[limit-1].key)
	}
	if len(page) == 0 {
		return nil, nil, next, nil
	}
	args := make([]any, len(page))
	at := make(map[domain.EntryID]int, len(page))
	for i, s := range page {
		args[i] = s.key.id
		at[domain.EntryID(s.key.id)] = i
	}
	got, err := appendRows(ctx, tx, nil, `SELECT `+search.Columns+` FROM `+search.From+
		` WHERE e.id IN (`+marks(len(args))+`)`, args)
	if err != nil {
		return nil, nil, "", fmt.Errorf("api: children of %s: %w", parent, err)
	}
	items := make([]search.Row, len(page))
	for i := range got {
		items[at[got[i].ID]] = got[i]
	}
	if err := search.LoadTags(ctx, tx, items); err != nil {
		return nil, nil, "", err
	}
	pl, err := rowsPlaced(ctx, tx, items)
	if err != nil {
		return nil, nil, "", err
	}
	return items, pl, next, nil
}

// cover is a complete archive's own coverage of its members (D5, D6): the
// archive file's place by its own content and its mark, read live once per
// request by search.PlaceSQL and search.OfflineOnlySQL on its entry.
type cover struct {
	archive domain.EntryID
	own     domain.Place
	offline bool
}

// coverOf reads the coverage of complete archive id.
func coverOf(ctx context.Context, tx *sql.Tx, id domain.EntryID) (*cover, error) {
	c := &cover{archive: id}
	var own sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT `+search.PlaceSQL("e", "fc")+`, `+search.OfflineOnlySQL("e", "fc")+`
		FROM entries e LEFT JOIN file_content fc ON fc.entry_id = e.id WHERE e.id = ?`, int64(id)).Scan(&own, &c.offline)
	if err != nil {
		return nil, fmt.Errorf("api: coverage of archive %s: %w", id, err)
	}
	c.own = domain.Place(own.String)
	return c, nil
}

// apply returns the place of a member whose own place is own under the
// archive's coverage, and whether the coverage gave it: an archive placed
// other_source makes every member with content other_source; one placed
// this_source makes its only_copy members this_source (D5).
func (c *cover) apply(own domain.Place) (domain.Place, bool) {
	switch {
	case own == "" || own == domain.PlaceNone:
	case c.own == domain.PlaceOtherSource && own != domain.PlaceOtherSource:
		return domain.PlaceOtherSource, true
	case c.own == domain.PlaceThisSource && own == domain.PlaceOnlyCopy:
		return domain.PlaceThisSource, true
	}
	return own, false
}

// memberDisposable is D14's test for a member: its file kind from its name,
// its category and triage from the file rules.
func memberDisposable(pol *rules.Policy, name []byte, kind domain.FileKind, size int64) bool {
	res := pol.ClassifyFile(rules.FileFacts{Name: name, Kind: kind, Size: size})
	return domain.IsDisposable(res.Category, kind, res.Triage)
}

// placedOf is the place of member m of c's archive: a folder's from its
// stored sums (not_computed without a member_places row), a file's from its
// own place under c. A member placed through c is marked as c's archive is;
// one placed by its own copies keeps its own mark.
func (c *cover) placedOf(m *member, pol *rules.Policy) *placed {
	r := &m.row
	if r.Kind == domain.EntryDirectory {
		p := &placed{level: true, sums: m.sums, place: domain.PlaceNotComputed}
		if m.sums != nil {
			p.place = m.sums.Place(true)
		}
		return p
	}
	p := &placed{size: r.Size}
	p.place, p.via = c.apply(domain.Place(m.own.String))
	offline := m.offline
	if p.via {
		offline = c.offline
	}
	p.offline = offline && p.place == domain.PlaceOtherSource
	if r.Kind == domain.EntryFile {
		p.disposable = memberDisposable(pol, r.Name, r.FileKind, r.Size)
	}
	return p
}

// archivePlaces is complete archive id's member_places (D5, D6): its
// stored archive_places row, the unpacked sums of its members under its
// own coverage; nil without a row.
func archivePlaces(ctx context.Context, tx *sql.Tx, id domain.EntryID) (*domain.PlaceSums, error) {
	var s domain.PlaceSums
	err := tx.QueryRowContext(ctx, `SELECT `+sumsColumns("ap")+` FROM archive_places ap WHERE ap.entry_id = ?`,
		int64(id)).Scan(sumsDest(&s)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("api: member places of archive %s: %w", id, err)
	}
	return &s, nil
}

// rowRef is the ref of a row: its member, or its entry.
func rowRef(r *search.Row) domain.Ref {
	if r.Member != 0 {
		return domain.Ref{Member: r.Member}
	}
	return domain.Ref{Entry: r.ID}
}

// fillCopies sets D9's names on a page of rows with their places: the
// other copy of each file or member placed this_source or other_source, a
// member placed through its archive's coverage naming the archive's copy
// (c), and the covering relation of each folder, complete archive, and
// member folder. One read serves each, for the whole page.
func fillCopies(ctx context.Context, tx *sql.Tx, rows []search.Row, pl []*placed, c *cover) error {
	want := map[domain.Ref]domain.Place{}
	var levels []domain.Ref
	for i := range rows {
		p := pl[i]
		switch {
		case p.level:
			levels = append(levels, rowRef(&rows[i]))
		case p.place != domain.PlaceThisSource && p.place != domain.PlaceOtherSource:
		case p.via:
			want[domain.Ref{Entry: c.archive}] = c.own
		default:
			want[rowRef(&rows[i])] = p.place
		}
	}
	copies, err := content.PreferredCopies(ctx, tx, want)
	if err != nil {
		return err
	}
	rels, err := relations.CoveredBy(ctx, tx, levels)
	if err != nil {
		return err
	}
	others := make([]domain.Ref, 0, len(rels))
	for ref, r := range rels {
		others = append(others, otherSide(r, ref))
	}
	sides, err := sidesOf(ctx, tx, others)
	if err != nil {
		return err
	}
	for i := range rows {
		p, ref := pl[i], rowRef(&rows[i])
		if p.level {
			if r, ok := rels[ref]; ok {
				if s, ok := sides[otherSide(r, ref)]; ok {
					p.covered = &coveredByJSON{Kind: r.Kind, Other: s}
				}
			}
			continue
		}
		if p.via {
			ref = domain.Ref{Entry: c.archive}
		}
		if pc, ok := copies[ref]; ok && (p.place == domain.PlaceThisSource || p.place == domain.PlaceOtherSource) {
			p.copy = &otherCopyJSON{Ref: pc.Ref.String(), SourceID: pc.SourceID, Path: pc.Path, PathB64: pc.PathB64,
				Offline: pc.Offline, ViaArchive: p.via, More: pc.More}
			if pc.ArchiveID != nil {
				s := pc.ArchiveID.String()
				p.copy.ArchiveID = &s
				p.copy.ArchivePath, p.copy.MemberPath = &pc.ArchivePath, &pc.MemberPath
			}
		}
	}
	return nil
}

// otherSide is the side of relation r that is not ref.
func otherSide(r relations.Relation, ref domain.Ref) domain.Ref {
	if sameSide(r.A, ref) {
		return r.B
	}
	return r.A
}

// sidesOf names relation sides: an entry by its path, a member folder by
// its archive's path, '!', and its own path. A side that no longer exists
// is absent.
func sidesOf(ctx context.Context, tx *sql.Tx, refs []domain.Ref) (map[domain.Ref]sideJSON, error) {
	out := make(map[domain.Ref]sideJSON, len(refs))
	var entries, members []any
	for _, r := range refs {
		if r.IsMember() {
			members = append(members, int64(r.Member))
		} else {
			entries = append(entries, int64(r.Entry))
		}
	}
	if len(entries) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT id, source_id, path FROM entries WHERE id IN (`+marks(len(entries))+`)`,
			entries...)
		if err != nil {
			return nil, fmt.Errorf("api: relation sides: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id   int64
				s    sideJSON
				path []byte
			)
			if err := rows.Scan(&id, &s.SourceID, &path); err != nil {
				return nil, fmt.Errorf("api: relation sides: %w", err)
			}
			if path == nil {
				path = []byte{}
			}
			s.ID, s.Path, s.PathB64 = domain.EntryID(id).String(), domain.DisplayName(path), path
			out[domain.Ref{Entry: domain.EntryID(id)}] = s
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("api: relation sides: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("api: relation sides: %w", err)
		}
	}
	if len(members) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT m.id, m.archive_id, ae.source_id, ae.path, m.path, a.format = 'zip'
			FROM archive_members m JOIN entries ae ON ae.id = m.archive_id JOIN archives a ON a.entry_id = m.archive_id
			WHERE m.id IN (`+marks(len(members))+`)`, members...)
		if err != nil {
			return nil, fmt.Errorf("api: relation sides: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id, archive  int64
				s            sideJSON
				apath, mpath []byte
				zip          bool
			)
			if err := rows.Scan(&id, &archive, &s.SourceID, &apath, &mpath, &zip); err != nil {
				return nil, fmt.Errorf("api: relation sides: %w", err)
			}
			ref := domain.Ref{Entry: domain.EntryID(archive), Member: domain.MemberID(id)}
			s.ID = ref.String()
			s.PathB64 = append(append(apath[:len(apath):len(apath)], '!'), mpath...)
			s.Path = domain.DisplayName(apath) + "!" + domain.MemberDisplayName(mpath, zip)
			out[ref] = s
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("api: relation sides: %w", err)
		}
	}
	return out, nil
}

// placesUpdating reports whether the stored places may still change (D8):
// the relations are dirty, or a relate job is queued or running.
func placesUpdating(ctx context.Context, tx *sql.Tx) (bool, error) {
	var b bool
	err := tx.QueryRowContext(ctx, `SELECT ifnull((SELECT dirty FROM review_state WHERE id = 1), 0)
		OR EXISTS (SELECT 1 FROM jobs WHERE kind = ? AND state IN ('queued', 'running'))`,
		string(relations.KindRelate)).Scan(&b)
	if err != nil {
		return false, fmt.Errorf("api: places updating: %w", err)
	}
	return b, nil
}
