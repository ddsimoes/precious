package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"precious/internal/clock"
	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/relations"
	"precious/internal/review"
	"precious/internal/search"
)

// Opportunities, review lists, Gems, and Compare (R2 design D11–D14 and the
// Interfaces' read endpoints).

type opportunitiesBody struct {
	Cards      []cardJSON   `json:"cards"`
	Coverage   coverageJSON `json:"coverage"`
	ComputedAt *time.Time   `json:"computed_at"`
}

// opportunities serves GET /api/opportunities[?source=ID]: the seven cards
// of the source (every source without one), largest first, the global
// coverage, and when the visible review rows were computed (null before
// the first relate pass). An unknown source is not_found.
func (h *handler) opportunities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := checkParams(q, "source"); err != nil {
		h.fail(w, err)
		return
	}
	src := domain.SourceID(q.Get("source"))
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		if err := sourceExists(ctx, tx, src); err != nil {
			return nil, err
		}
		var (
			body opportunitiesBody
			at   sql.NullInt64
			err  error
		)
		if body.Cards, err = readCards(ctx, tx, src); err != nil {
			return nil, err
		}
		if body.Coverage, err = readCoverage(ctx, tx, ""); err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT computed_at FROM review_state WHERE id = 1`).Scan(&at); err != nil {
			return nil, fmt.Errorf("api: review state: %w", err)
		}
		if at.Valid {
			t := clock.FromMillis(at.Int64)
			body.ComputedAt = &t
		}
		return body, nil
	})
}

// sourceExists fails with not_found when src is set and names no source.
func sourceExists(ctx context.Context, tx *sql.Tx, src domain.SourceID) error {
	if src == "" {
		return nil
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM sources WHERE id = ?`, string(src)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeNotFound, "source %q not found", src)
	}
	return err
}

// reviewRowJSON is RowJSON.
type reviewRowJSON struct {
	ID       string        `json:"id"`
	Bytes    int64         `json:"bytes"`
	Files    int64         `json:"files"`
	Entry    *entryRow     `json:"entry"`
	Relation *relationJSON `json:"relation"`
	Copies   []copyJSON    `json:"copies"`
	Summary  summaryJSON   `json:"summary"`
}

// summaryJSON holds what the client builds a row's summary line from
// (design D13).
type summaryJSON struct {
	Category *domain.Category `json:"category"`
	Years    *[2]*int         `json:"years"`
	Files    int64            `json:"files"`
	Bytes    int64            `json:"bytes"`
	Signals  []string         `json:"signals"`
}

type reviewPageBody struct {
	Card       cardJSON        `json:"card"`
	Items      []reviewRowJSON `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

// reviewList serves GET /api/opportunities/{list}?source=&decided=0|1&
// cursor=&limit=: a page of a card's open rows (decided=1: the rows no
// longer open), with the card. An entry row has its entry; a duplicates
// row is a relation, whose entry is side a and whose relation's other is
// side b, or a group of copies of one content, which lists its copies. An
// unknown list, or a Gems section, is not_found; so is an unknown source.
func (h *handler) reviewList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list := review.List(r.PathValue("list"))
	err := checkParams(q, "source", "decided", "cursor", "limit")
	if err == nil && !list.IsCard() {
		err = domain.Errorf(domain.CodeNotFound, "review list %q not found", list)
	}
	var (
		decided bool
		limit   int
	)
	if err == nil {
		switch q.Get("decided") {
		case "", "0":
		case "1":
			decided = true
		default:
			err = domain.Errorf(domain.CodeInvalidRequest, "decided must be 0 or 1, not %q", q.Get("decided"))
		}
	}
	if err == nil {
		limit, err = parseLimit(q.Get("limit"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	src := domain.SourceID(q.Get("source"))
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		if err := sourceExists(ctx, tx, src); err != nil {
			return nil, err
		}
		cards, err := readCards(ctx, tx, src)
		if err != nil {
			return nil, err
		}
		body := reviewPageBody{}
		for _, c := range cards {
			if c.List == list {
				body.Card = c
			}
		}
		page, err := review.Rows(ctx, tx, list, src, decided, q.Get("cursor"), limit)
		if err != nil {
			return nil, err
		}
		if body.Items, err = h.reviewRows(ctx, tx, page.Items); err != nil {
			return nil, err
		}
		if page.NextCursor != "" {
			body.NextCursor = &page.NextCursor
		}
		return body, nil
	})
}

// groupCopies is how many other copies a duplicate group row lists beside
// its first copy.
const groupCopies = 100

// reviewRows renders review rows as RowJSON. An unpacked_archives row
// carries its archive's relation with the folder it was unpacked into,
// seen from the archive (side a).
func (h *handler) reviewRows(ctx context.Context, tx *sql.Tx, items []review.Row) ([]reviewRowJSON, error) {
	var relIDs []int64
	unpacked := map[int64]int64{} // review row ID → relation ID
	for _, it := range items {
		switch {
		case it.Relation != 0:
			relIDs = append(relIDs, it.Relation)
		case it.List == review.ListUnpackedArchives && it.Group != 0:
			id, err := unpackedRelation(ctx, tx, it.Entry, it.Group)
			if err != nil {
				return nil, err
			}
			if id != 0 {
				unpacked[it.ID] = id
				relIDs = append(relIDs, id)
			}
		}
	}
	rels, err := relationsByID(ctx, tx, relIDs)
	if err != nil {
		return nil, err
	}
	relJSON := func(rel relations.Relation) (*relationJSON, error) {
		rj, err := h.relationsJSON(ctx, tx, []relations.Relation{rel}, func(relations.Relation) string { return "a" })
		if err != nil || len(rj) == 0 {
			return nil, err
		}
		return &rj[0], nil
	}
	var refs []domain.Ref
	for _, it := range items {
		switch {
		case it.Entry != 0:
			refs = append(refs, domain.Ref{Entry: it.Entry})
		case it.Relation != 0:
			if rel, ok := rels[it.Relation]; ok {
				refs = append(refs, rel.A)
			}
		}
	}
	rows, err := h.loadRows(ctx, tx, refs)
	if err != nil {
		return nil, err
	}
	signals, err := notableSignals(ctx, tx, refs)
	if err != nil {
		return nil, err
	}
	out := make([]reviewRowJSON, 0, len(items))
	for _, it := range items {
		j := reviewRowJSON{ID: strconv.FormatInt(it.ID, 10), Bytes: it.Bytes, Files: it.Files,
			Summary: summaryJSON{Files: it.Files, Bytes: it.Bytes, Signals: []string{}}}
		var self *search.Row
		switch {
		case it.Entry != 0:
			self, _ = rows.get(domain.Ref{Entry: it.Entry})
			if self == nil {
				continue // deleted since the rows were computed
			}
			if rel, ok := rels[unpacked[it.ID]]; ok {
				if j.Relation, err = relJSON(rel); err != nil {
					return nil, err
				}
			}
		case it.Relation != 0:
			rel, ok := rels[it.Relation]
			if !ok {
				continue
			}
			if self, _ = rows.get(rel.A); self == nil {
				continue
			}
			// Side a's files as the Map counts them: its own, members of
			// the archives below it not included (the stored a_files counts
			// them).
			j.Files, j.Summary.Files = self.TotalFiles, self.TotalFiles
			if j.Relation, err = relJSON(rel); err != nil {
				return nil, err
			}
			if j.Relation == nil {
				continue
			}
		default:
			if it.Copy == (domain.Ref{}) {
				continue
			}
			if j.Copies, err = groupCopyList(ctx, tx, it.Copy); err != nil {
				return nil, err
			}
		}
		if self != nil {
			e := rowJSON(self)
			j.Entry = &e
			j.Summary.Category = e.Category
			j.Summary.Years = years(self)
			j.Summary.Signals = signals[rowKey(refOf(self))]
			if j.Summary.Signals == nil {
				j.Summary.Signals = []string{}
			}
		}
		out = append(out, j)
	}
	return out, nil
}

// unpackedRelation is the ID of the visible generation's same or inside
// relation of the archive file archive (side a) with the folder it was
// unpacked into (side b), 0 for none.
func unpackedRelation(ctx context.Context, tx *sql.Tx, archive, folder domain.EntryID) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM relations
		WHERE a_entry = ? AND a_member IS NULL AND b_entry = ? AND b_member IS NULL AND kind IN ('same', 'inside')
			AND gen = (SELECT gen FROM review_state WHERE id = 1)
		ORDER BY id LIMIT 1`, int64(archive), int64(folder)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("api: relation of unpacked archive %s: %w", archive, err)
	}
	return id, nil
}

// refOf is the ref of a row.
func refOf(r *search.Row) domain.Ref {
	if r.Member != 0 {
		return domain.Ref{Member: r.Member}
	}
	return domain.Ref{Entry: r.ID}
}

// years is a row's oldest and newest year (a folder's range, a file's own
// time), or nil when neither is known.
func years(r *search.Row) *[2]*int {
	from, to := r.Oldest, r.Newest
	if r.Kind != domain.EntryDirectory {
		from, to = r.MTime, r.MTime
	}
	if !search.KnownTime(from) && !search.KnownTime(to) {
		return nil
	}
	var y [2]*int
	if search.KnownTime(from) {
		v := from.Year()
		y[0] = &v
	}
	if search.KnownTime(to) {
		v := to.Year()
		y[1] = &v
	}
	return &y
}

// notableTraits are the traits a summary names, in this order.
var notableTraits = []domain.Trait{domain.TraitContainsVCS, domain.TraitContainsDatabase,
	domain.TraitContainsCredentials, domain.TraitContainsUserMaterial}

// summarySignals is the most signals a summary names.
const summarySignals = 2

// notableSignals reads up to two notable signals of each entry among refs:
// its notable traits, then the distinct signals of its folder's
// indicators. Members have none.
func notableSignals(ctx context.Context, tx *sql.Tx, refs []domain.Ref) (map[domain.Ref][]string, error) {
	var ids []any
	for _, r := range refs {
		if !r.IsMember() && r.Entry != 0 {
			ids = append(ids, int64(r.Entry))
		}
	}
	out := map[domain.Ref][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.traits, ds.indicators FROM entries e
		LEFT JOIN dir_stats ds ON ds.entry_id = e.id WHERE e.id IN (`+marks(len(ids))+`)`, ids...)
	if err != nil {
		return nil, fmt.Errorf("api: signals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id               int64
			traitsJ, indicsJ []byte
			traits           []domain.Trait
			indicators       []indicator
		)
		if err := rows.Scan(&id, &traitsJ, &indicsJ); err != nil {
			return nil, fmt.Errorf("api: signals: %w", err)
		}
		if err := unmarshalOpt(traitsJ, &traits); err != nil {
			return nil, fmt.Errorf("api: traits of entry %d: %w", id, err)
		}
		if err := unmarshalOpt(indicsJ, &indicators); err != nil {
			return nil, fmt.Errorf("api: indicators of entry %d: %w", id, err)
		}
		var s []string
		for _, t := range notableTraits {
			if slices.Contains(traits, t) && len(s) < summarySignals {
				s = append(s, string(t))
			}
		}
		for _, in := range indicators {
			if len(s) < summarySignals && !slices.Contains(s, in.Signal) {
				s = append(s, in.Signal)
			}
		}
		out[domain.Ref{Entry: domain.EntryID(id)}] = s
	}
	return out, rows.Err()
}

// groupCopyList lists a duplicate group's copies: the copy ref the row
// names, then up to groupCopies others (content.Copies).
func groupCopyList(ctx context.Context, tx *sql.Tx, ref domain.Ref) ([]copyJSON, error) {
	first, err := selfCopy(ctx, tx, ref)
	if err != nil {
		return nil, err
	}
	others, _, _, err := content.Copies(ctx, tx, ref, "", groupCopies)
	if err != nil {
		return nil, err
	}
	return copiesJSON(append([]content.Copy{first}, others...)), nil
}

// selfCopy is ref as a copy of its own content.
func selfCopy(ctx context.Context, tx *sql.Tx, ref domain.Ref) (content.Copy, error) {
	var (
		c        content.Copy
		src, eff string
		path     []byte
		err      error
	)
	if ref.IsMember() {
		var (
			archive int64
			mpath   []byte
			zip     bool
		)
		err = tx.QueryRowContext(ctx, `SELECT m.archive_id, e.source_id, e.path, m.path, a.format = 'zip', s.state <> 'online',
				e.eff_decision
			FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = m.archive_id
			JOIN sources s ON s.id = e.source_id
			WHERE m.id = ?`, int64(ref.Member)).Scan(&archive, &src, &path, &mpath, &zip, &c.Offline, &eff)
		if err == nil {
			a := domain.EntryID(archive)
			c.Ref, c.ArchiveID = domain.Ref{Entry: a, Member: ref.Member}, &a
			c.Path = domain.DisplayName(path) + "!" + domain.MemberDisplayName(mpath, zip)
			c.PathB64 = append(append(path, '!'), mpath...)
		}
	} else {
		var decision sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT e.source_id, e.path, s.state <> 'online', e.decision, e.eff_decision
			FROM entries e JOIN sources s ON s.id = e.source_id WHERE e.id = ?`, int64(ref.Entry)).
			Scan(&src, &path, &c.Offline, &decision, &eff)
		c.Ref, c.Path, c.PathB64, c.Decision = ref, domain.DisplayName(path), path, domain.Decision(decision.String)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return content.Copy{}, domain.Errorf(domain.CodeNotFound, "%s not found", ref)
	}
	if err != nil {
		return content.Copy{}, fmt.Errorf("api: copy %s: %w", ref, err)
	}
	c.SourceID, c.EffDecision = domain.SourceID(src), domain.Decision(eff)
	return c, nil
}

// gemSections maps the sections of GET /api/gems to their review lists.
var gemSections = map[string]review.List{
	"unique":       review.ListGemsUnique,
	"rescue":       review.ListGemsRescue,
	"only_in_copy": review.ListGemsOnlyInCopy,
}

type gemJSON struct {
	Entry    entryRow      `json:"entry"`
	Group    *entryRow     `json:"group"`
	Relation *relationJSON `json:"relation"`
}

type gemsBody struct {
	Section    string       `json:"section"`
	Items      []gemJSON    `json:"items"`
	NextCursor *string      `json:"next_cursor"`
	Coverage   coverageJSON `json:"coverage"`
}

// gems serves GET /api/gems?section=unique|rescue|only_in_copy&source=&
// cursor=&limit=: a page of a Gems section (design D14) with the global
// coverage its claims carry. A rescue item names its group; an only-in-copy
// item names the overlap side holding it (group) and the relation, seen
// from that side. A missing or unknown section is invalid_request; an
// unknown source is not_found.
func (h *handler) gems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	err := checkParams(q, "section", "source", "cursor", "limit")
	section := q.Get("section")
	list, ok := gemSections[section]
	if err == nil && !ok {
		err = domain.Errorf(domain.CodeInvalidRequest, "section must be unique, rescue, or only_in_copy, not %q", section)
	}
	var limit int
	if err == nil {
		limit, err = parseLimit(q.Get("limit"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	src := domain.SourceID(q.Get("source"))
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		if err := sourceExists(ctx, tx, src); err != nil {
			return nil, err
		}
		page, err := review.Rows(ctx, tx, list, src, false, q.Get("cursor"), limit)
		if err != nil {
			return nil, err
		}
		body := gemsBody{Section: section, Items: make([]gemJSON, 0, len(page.Items))}
		if body.Coverage, err = readCoverage(ctx, tx, ""); err != nil {
			return nil, err
		}
		var (
			refs   []domain.Ref
			relIDs []int64
		)
		for _, it := range page.Items {
			refs = append(refs, domain.Ref{Entry: it.Entry})
			if it.Group != 0 {
				refs = append(refs, domain.Ref{Entry: it.Group})
			}
			if list == review.ListGemsOnlyInCopy && it.Relation != 0 {
				relIDs = append(relIDs, it.Relation)
			}
		}
		rows, err := h.loadRows(ctx, tx, refs)
		if err != nil {
			return nil, err
		}
		rels, err := relationsByID(ctx, tx, relIDs)
		if err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			e, ok := rows.get(domain.Ref{Entry: it.Entry})
			if !ok {
				continue
			}
			g := gemJSON{Entry: rowJSON(e)}
			if gr, ok := rows.get(domain.Ref{Entry: it.Group}); ok && it.Group != 0 {
				j := rowJSON(gr)
				g.Group = &j
			}
			if rel, ok := rels[it.Relation]; ok && list == review.ListGemsOnlyInCopy {
				side := domain.Ref{Entry: it.Group}
				rj, err := h.relationsJSON(ctx, tx, []relations.Relation{rel}, func(r relations.Relation) string {
					if sameSide(r.A, side) {
						return "a"
					}
					return "b"
				})
				if err != nil {
					return nil, err
				}
				if len(rj) > 0 {
					g.Relation = &rj[0]
				}
			}
			body.Items = append(body.Items, g)
		}
		if page.NextCursor != "" {
			body.NextCursor = &page.NextCursor
		}
		return body, nil
	})
}

type compareItemJSON struct {
	Path    string    `json:"path"`
	PathB64 []byte    `json:"path_b64"`
	Left    *entryRow `json:"left"`
	Right   *entryRow `json:"right"`
}

type compareBody struct {
	Left       entryRow                    `json:"left"`
	Right      entryRow                    `json:"right"`
	Summary    map[relations.Bucket]amount `json:"summary"`
	Items      []compareItemJSON           `json:"items"`
	NextCursor *string                     `json:"next_cursor"`
}

const (
	compareDefaultLimit = 100
	compareMaxLimit     = 1000
)

var buckets = []relations.Bucket{relations.BucketOnlyLeft, relations.BucketOnlyRight, relations.BucketIdentical,
	relations.BucketDifferent, relations.BucketUnchecked}

// compare serves GET /api/compare?left=&right=&bucket=&cursor=&limit=
// (relations.Compare, design D11): both sides' rows, the five buckets'
// files and bytes, and a page of the bucket's items (none without a
// bucket). A malformed ref, a side that is a file, sides of which one
// contains the other, an unknown bucket, or a bad cursor is
// invalid_request; an unknown side is not_found.
func (h *handler) compare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		left, right domain.Ref
		limit       int
	)
	err := checkParams(q, "left", "right", "bucket", "cursor", "limit")
	if err == nil {
		left, err = domain.ParseRef(q.Get("left"))
	}
	if err == nil {
		right, err = domain.ParseRef(q.Get("right"))
	}
	bucket := relations.Bucket(q.Get("bucket"))
	if err == nil && bucket != "" && !bucket.Valid() {
		err = domain.Errorf(domain.CodeInvalidRequest, "unknown bucket %q", bucket)
	}
	if err == nil {
		limit, err = parseLimit(q.Get("limit"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if limit == 0 {
		limit = compareDefaultLimit
	}
	limit = min(limit, compareMaxLimit)
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		res, err := relations.Compare(ctx, tx, left, right, bucket, q.Get("cursor"), limit)
		if err != nil {
			return nil, err
		}
		refs := []domain.Ref{left, right}
		for _, it := range res.Items {
			for _, s := range []*domain.Ref{it.Left, it.Right} {
				if s != nil {
					refs = append(refs, *s)
				}
			}
		}
		rows, err := h.loadRows(ctx, tx, refs)
		if err != nil {
			return nil, err
		}
		l, okL := rows.get(left)
		rr, okR := rows.get(right)
		if !okL || !okR {
			return nil, domain.Errorf(domain.CodeNotFound, "a side of the comparison is gone")
		}
		body := compareBody{Left: rowJSON(l), Right: rowJSON(rr), Summary: make(map[relations.Bucket]amount, len(buckets)),
			Items: make([]compareItemJSON, 0, len(res.Items))}
		for _, b := range buckets {
			c := res.Summary[b]
			body.Summary[b] = amount{Files: c.Files, Bytes: c.Bytes}
		}
		side := func(ref *domain.Ref) (*entryRow, bool) {
			if ref == nil {
				return nil, false
			}
			row, ok := rows.get(*ref)
			if !ok {
				return nil, false
			}
			j := rowJSON(row)
			return &j, row.Member != 0 && row.Zip
		}
		for _, it := range res.Items {
			lj, lz := side(it.Left)
			rj, rz := side(it.Right)
			body.Items = append(body.Items, compareItemJSON{Path: domain.MemberDisplayName(it.Path, lz || rz), PathB64: it.Path,
				Left: lj, Right: rj})
		}
		if res.NextCursor != "" {
			body.NextCursor = &res.NextCursor
		}
		return body, nil
	})
}
