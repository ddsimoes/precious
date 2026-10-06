// Package review turns the index, the rules' classification, and the
// relations into the opportunity cards, their review lists, and Gems (R2
// design D12–D14, §11.4–§11.7).
//
// Refresh, the relate job's after hook, writes one generation of
// review_rows (and review_row_sources for duplicates rows) from the index
// and that generation's relations. Readers see the generation named by
// review_state.gen, which the relate job flips after Refresh returns, and
// join every row live with the current decisions: a decision is never
// stored in a derived row (D6), so it shows at once.
//
// Row encoding per list:
//
//   - The six rule and archive cards (system_junk, installers, programs,
//     caches, leftovers, unpacked_archives): entry_id is the row's entry,
//     source_id its source, bytes its total_bytes, sort_key = bytes.
//   - duplicates: a same or inside relation (relation_id, bytes = its
//     redundant bytes), or a duplicate group (content_id) with a copy
//     outside every listed relation. source_id is NULL; review_row_sources
//     names every source holding one of its copies.
//   - gems_unique: entry_id the file, sort_key its mtime_ns.
//   - gems_rescue: entry_id the indicator, group_id its outermost programs
//     or disposable group, sort_key the group's rank (largest group first).
//   - gems_only_in_copy: entry_id the file, group_id the overlap side that
//     holds it, sort_key the relation's ID.
//
// Cards page by sort_key then id, both descending (largest first); Gems
// page by sort_key then id, both ascending.
package review

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"precious/internal/domain"
	"precious/internal/store"
)

// List is a review list: a review_rows.list value.
type List string

// The seven cards' lists and the three Gems sections.
const (
	ListDuplicates       List = "duplicates"
	ListUnpackedArchives List = "unpacked_archives"
	ListSystemJunk       List = "system_junk"
	ListInstallers       List = "installers"
	ListPrograms         List = "programs"
	ListCaches           List = "caches"
	ListLeftovers        List = "leftovers"
	ListGemsUnique       List = "gems_unique"
	ListGemsRescue       List = "gems_rescue"
	ListGemsOnlyInCopy   List = "gems_only_in_copy"
)

// CardLists are the lists of the opportunity cards, in their fixed order
// (the tie break of the ranking by bytes).
var CardLists = []List{ListDuplicates, ListUnpackedArchives, ListSystemJunk, ListInstallers,
	ListPrograms, ListCaches, ListLeftovers}

// GemLists are the Gems sections, in display order.
var GemLists = []List{ListGemsUnique, ListGemsRescue, ListGemsOnlyInCopy}

// IsCard reports whether l is a card's list.
func (l List) IsCard() bool {
	for _, c := range CardLists {
		if l == c {
			return true
		}
	}
	return false
}

// IsGems reports whether l is a Gems section.
func (l List) IsGems() bool {
	return l == ListGemsUnique || l == ListGemsRescue || l == ListGemsOnlyInCopy
}

// Valid reports whether l is a known list.
func (l List) Valid() bool { return l.IsCard() || l.IsGems() }

// Basis says what a card is based on.
const (
	BasisRules   = "rules"
	BasisContent = "content"
)

// Basis returns the card's basis: same content for duplicates and
// unpacked archives, the rules for the others.
func (l List) Basis() string {
	if l == ListDuplicates || l == ListUnpackedArchives {
		return BasisContent
	}
	return BasisRules
}

// Card is one opportunity card: the bytes and count of its open rows.
type Card struct {
	List  List
	Bytes int64
	Rows  int64
	Basis string
}

// Page sizes.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// Row is one review row.
type Row struct {
	ID   int64
	List List
	// Source is the entry's source; "" for duplicates rows.
	Source domain.SourceID
	// Entry is the row's entry (0 for duplicates rows).
	Entry domain.EntryID
	// Relation is the relation of a duplicates relation row, and the overlap
	// relation of a gems_only_in_copy row.
	Relation int64
	// Content is the content of a duplicate group row.
	Content int64
	// Copy is one present copy of a duplicate group row's content (the
	// lowest entry, else the lowest member), the ref content.Copies lists
	// the group from; zero when no copy is left.
	Copy domain.Ref
	// Group is the outermost programs or disposable group of a gems_rescue
	// row, and the overlap side holding a gems_only_in_copy row's file.
	Group   domain.EntryID
	Bytes   int64
	Files   int64
	SortKey int64
}

// Page is one page of a review list.
type Page struct {
	Items []Row
	// NextCursor continues after the last item; "" on the last page.
	NextCursor string
}

// genSQL is the visible generation.
const genSQL = `(SELECT gen FROM review_state WHERE id = 1)`

// copyKeySQL is a file copy's identity: its hard-link set (same dev and ino
// on a source with stable identity, design D3) or the entry itself.
const copyKeySQL = `CASE WHEN e.nlink > 1 AND e.ino IS NOT NULL AND e.dev IS NOT NULL
	AND (SELECT json_extract(s.capabilities, '$.stable_identity') FROM sources s WHERE s.id = e.source_id) = 1
	THEN 'h' || e.source_id || '/' || e.dev || '/' || e.ino ELSE e.id END`

// openSQL holds for an open row rr (D12): an entry row while its entry is
// undecided; a relation row while both sides are; a duplicate group row
// while at least two of its present copies are (a member reads its
// archive's decision, a hard-link set is one copy). Gems rows are always
// listed. The undecided file copies are counted distinct only when one of
// them has hard links, which saves a temporary table per row otherwise.
const openSQL = `(CASE
	WHEN rr.list IN ('gems_unique', 'gems_rescue', 'gems_only_in_copy') THEN 1
	WHEN rr.entry_id IS NOT NULL THEN
		(SELECT e.eff_decision = 'undecided' FROM entries e WHERE e.id = rr.entry_id)
	WHEN rr.relation_id IS NOT NULL THEN
		(SELECT ea.eff_decision = 'undecided' AND eb.eff_decision = 'undecided'
			FROM relations r JOIN entries ea ON ea.id = r.a_entry JOIN entries eb ON eb.id = r.b_entry
			WHERE r.id = rr.relation_id)
	ELSE (SELECT CASE
			WHEN max(coalesce(e.nlink, 1)) <= 1 AND count(*) >= 2 THEN 1
			ELSE CASE WHEN max(coalesce(e.nlink, 1)) > 1 THEN
					(SELECT count(DISTINCT ` + copyKeySQL + `) FROM file_content fc JOIN entries e ON e.id = fc.entry_id
						WHERE fc.content_id = rr.content_id AND e.state = 'present' AND e.eff_decision = 'undecided')
					ELSE count(*) END
				+ (SELECT count(*) FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
					JOIN entries ae ON ae.id = a.entry_id
					WHERE m.content_id = rr.content_id AND a.state = 'complete' AND ae.state = 'present'
						AND ae.eff_decision = 'undecided') >= 2
			END
		FROM file_content fc JOIN entries e ON e.id = fc.entry_id
		WHERE fc.content_id = rr.content_id AND e.state = 'present' AND e.eff_decision = 'undecided')
	END)`

// sourceSQL holds for a row rr touching source :src (empty: every source): an
// entry row of that source, or a duplicates row with a copy there.
const sourceSQL = `(:src = '' OR rr.source_id = :src OR (rr.source_id IS NULL AND EXISTS (
	SELECT 1 FROM review_row_sources rs WHERE rs.source_id = :src AND rs.row_id = rr.id)))`

// Cards returns the seven cards for source src ("" = all sources), largest
// first: each card's bytes and row count are the sums over its open rows,
// with the filter Rows uses, so a card always equals its list (R2.5).
func Cards(ctx context.Context, q store.Queryer, src domain.SourceID) ([]Card, error) {
	rows, err := q.QueryContext(ctx, `SELECT rr.list, coalesce(sum(rr.bytes), 0), count(*)
		FROM review_rows rr
		WHERE rr.gen = `+genSQL+` AND rr.list IN ('duplicates', 'unpacked_archives', 'system_junk',
			'installers', 'programs', 'caches', 'leftovers')
			AND `+sourceSQL+` AND `+openSQL+`
		GROUP BY rr.list`, sql.Named("src", string(src)))
	if err != nil {
		return nil, fmt.Errorf("review: cards: %w", err)
	}
	defer rows.Close()
	sums := make(map[List][2]int64, len(CardLists))
	for rows.Next() {
		var (
			l            string
			bytes, count int64
		)
		if err := rows.Scan(&l, &bytes, &count); err != nil {
			return nil, fmt.Errorf("review: cards: %w", err)
		}
		sums[List(l)] = [2]int64{bytes, count}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("review: cards: %w", err)
	}
	cards := make([]Card, len(CardLists))
	for i, l := range CardLists {
		s := sums[l]
		cards[i] = Card{List: l, Bytes: s[0], Rows: s[1], Basis: l.Basis()}
	}
	// A stable sort keeps the fixed order among equal bytes.
	for i := 1; i < len(cards); i++ {
		for j := i; j > 0 && cards[j].Bytes > cards[j-1].Bytes; j-- {
			cards[j], cards[j-1] = cards[j-1], cards[j]
		}
	}
	return cards, nil
}

// Rows returns a page of list for source src ("" = all sources): its open
// rows, or with decided its rows that are no longer open, after cursor (""
// for the first page), at most limit (DefaultLimit when limit ≤ 0, at most
// MaxLimit). Gems rows are listed whatever their decision, so a decided
// Gems page is empty. An unknown list or a malformed cursor is
// invalid_request.
func Rows(ctx context.Context, q store.Queryer, list List, src domain.SourceID, decided bool, cursor string, limit int) (Page, error) {
	if !list.Valid() {
		return Page{}, domain.Errorf(domain.CodeInvalidRequest, "unknown review list %q", list)
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	page := Page{Items: []Row{}}
	if decided && list.IsGems() {
		return page, nil
	}
	cmp, order := "<", "DESC"
	if list.IsGems() {
		cmp, order = ">", "ASC"
	}
	open := openSQL
	if decided {
		open = "NOT " + openSQL
	}
	args := []any{sql.Named("src", string(src)), sql.Named("list", string(list)), sql.Named("limit", limit+1)}
	after := ""
	if cursor != "" {
		key, id, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		after = ` AND (rr.sort_key ` + cmp + ` :key OR (rr.sort_key = :key AND rr.id ` + cmp + ` :id))`
		args = append(args, sql.Named("key", key), sql.Named("id", id))
	}
	rows, err := q.QueryContext(ctx, `SELECT rr.id, rr.list, coalesce(rr.source_id, ''), coalesce(rr.entry_id, 0),
			coalesce(rr.relation_id, CASE WHEN rr.list = 'gems_only_in_copy' THEN rr.sort_key END, 0),
			coalesce(rr.content_id, 0), coalesce(rr.group_id, 0), rr.bytes, rr.files, rr.sort_key,
			CASE WHEN rr.content_id IS NOT NULL THEN coalesce(
				(SELECT min(fc.entry_id) FROM file_content fc JOIN entries e ON e.id = fc.entry_id
					WHERE fc.content_id = rr.content_id AND e.state = 'present'), 0) END,
			CASE WHEN rr.content_id IS NOT NULL THEN coalesce(
				(SELECT min(m.id) FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
					WHERE m.content_id = rr.content_id AND a.state = 'complete'), 0) END
		FROM review_rows rr
		WHERE rr.gen = `+genSQL+` AND rr.list = :list AND `+sourceSQL+` AND `+open+after+`
		ORDER BY rr.sort_key `+order+`, rr.id `+order+`
		LIMIT :limit`, args...)
	if err != nil {
		return Page{}, fmt.Errorf("review: rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r            Row
			l, s         string
			copyE, copyM sql.NullInt64
			entry, group int64
		)
		if err := rows.Scan(&r.ID, &l, &s, &entry, &r.Relation, &r.Content, &group, &r.Bytes, &r.Files, &r.SortKey,
			&copyE, &copyM); err != nil {
			return Page{}, fmt.Errorf("review: rows: %w", err)
		}
		r.List, r.Source, r.Entry, r.Group = List(l), domain.SourceID(s), domain.EntryID(entry), domain.EntryID(group)
		if copyE.Int64 != 0 {
			r.Copy = domain.Ref{Entry: domain.EntryID(copyE.Int64)}
		} else if copyM.Int64 != 0 {
			r.Copy = domain.Ref{Member: domain.MemberID(copyM.Int64)}
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("review: rows: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = encodeCursor(last.SortKey, last.ID)
	}
	return page, nil
}

// Resolve returns the entry IDs of list's open rows for source src (""
// = all sources), ascending: the entries a select-all on the list selects
// (D13). The duplicates list has no select-all, and an unknown list is
// invalid_request, as is a list with more than max open rows.
func Resolve(ctx context.Context, q store.Queryer, list List, src domain.SourceID, max int) ([]domain.EntryID, error) {
	if !list.Valid() {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "unknown review list %q", list)
	}
	if list == ListDuplicates {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "the duplicates list has no select-all; decide its copies one by one")
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT rr.entry_id FROM review_rows rr
		WHERE rr.gen = `+genSQL+` AND rr.list = :list AND rr.entry_id IS NOT NULL AND `+sourceSQL+` AND `+openSQL+`
		ORDER BY rr.entry_id LIMIT :limit`,
		sql.Named("src", string(src)), sql.Named("list", string(list)), sql.Named("limit", max+1))
	if err != nil {
		return nil, fmt.Errorf("review: resolve: %w", err)
	}
	defer rows.Close()
	var ids []domain.EntryID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("review: resolve: %w", err)
		}
		ids = append(ids, domain.EntryID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("review: resolve: %w", err)
	}
	if len(ids) > max {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "the list holds more than %d open rows", max)
	}
	return ids, nil
}

// A cursor is the last row's sort key and ID, "<sort_key>.<id>", in
// unpadded base64url.
func encodeCursor(key, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(key, 10) + "." + strconv.FormatInt(id, 10)))
}

func decodeCursor(s string) (key, id int64, err error) {
	bad := domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, bad
	}
	k, i, ok := strings.Cut(string(raw), ".")
	if !ok {
		return 0, 0, bad
	}
	if key, err = strconv.ParseInt(k, 10, 64); err != nil {
		return 0, 0, bad
	}
	if id, err = strconv.ParseInt(i, 10, 64); err != nil || id <= 0 {
		return 0, 0, bad
	}
	return key, id, nil
}
