package relations

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

// Relation is one stored relation of the visible generation (design D9).
// Side a is the contained side of inside, the archive or path-later side of
// same, and the side with the larger matched share of overlap. A side
// inside an archive has the archive's entry and the member folder.
type Relation struct {
	ID                                             int64
	Kind                                           string
	A, B                                           domain.Ref
	MatchedBytes, RedundantBytes                   int64
	AOnlyFiles, AOnlyBytes, BOnlyFiles, BOnlyBytes int64
}

// relationColumns are the columns scanRelation reads.
const relationColumns = `id, kind, a_entry, COALESCE(a_member, 0), b_entry, COALESCE(b_member, 0),
	matched_bytes, redundant_bytes, a_only_files, a_only_bytes, b_only_files, b_only_bytes`

func scanRelation(r interface{ Scan(...any) error }) (Relation, error) {
	var rel Relation
	var ae, am, be, bm int64
	err := r.Scan(&rel.ID, &rel.Kind, &ae, &am, &be, &bm, &rel.MatchedBytes, &rel.RedundantBytes,
		&rel.AOnlyFiles, &rel.AOnlyBytes, &rel.BOnlyFiles, &rel.BOnlyBytes)
	rel.A = domain.Ref{Entry: domain.EntryID(ae), Member: domain.MemberID(am)}
	rel.B = domain.Ref{Entry: domain.EntryID(be), Member: domain.MemberID(bm)}
	return rel, err
}

// RelationsOf returns at most limit relations of the visible generation
// with ref on either side, by redundant bytes, then matched bytes,
// descending. A member ref ("m45") matches the member folder's sides; an
// entry ref the entry's own sides (a folder, or a whole archive).
func RelationsOf(ctx context.Context, q store.Queryer, ref domain.Ref, limit int) ([]Relation, error) {
	if limit <= 0 {
		return nil, nil
	}
	var where string
	var id int64
	if ref.Member != 0 {
		where, id = `(a_member = ?1 OR b_member = ?1)`, int64(ref.Member)
	} else {
		where, id = `(a_entry = ?1 AND a_member IS NULL OR b_entry = ?1 AND b_member IS NULL)`, int64(ref.Entry)
	}
	rows, err := q.QueryContext(ctx, `SELECT `+relationColumns+` FROM relations
		WHERE `+where+` AND gen = (SELECT gen FROM review_state WHERE id = 1)
		ORDER BY redundant_bytes DESC, matched_bytes DESC, id LIMIT ?2`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("relations: relations of %s: %w", ref, err)
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CoveredBy returns, for each ref, D9's relation of the visible generation
// (r5c design D9): of kind same with ref on either side, or inside with ref
// as side a (ref is contained), never overlap; the one with the most
// matched bytes, then the lowest ID. Refs are matched as RelationsOf
// matches them: an entry ref by the entry's own sides (a folder, or a whole
// archive), a member ref ("m45") by the member folder's sides. Refs with no
// such relation are absent. Each relation is as stored, like RelationsOf's:
// the caller tells ref's side by comparing it with A and B. One statement
// serves every ref.
func CoveredBy(ctx context.Context, q store.Queryer, refs []domain.Ref) (map[domain.Ref]Relation, error) {
	out := map[domain.Ref]Relation{}
	if len(refs) == 0 {
		return out, nil
	}
	type key struct {
		member bool
		id     int64
	}
	refsOf := make(map[key][]domain.Ref, len(refs))
	arg := make([]byte, 0, 16*len(refs)+2)
	arg = append(arg, '[')
	for _, ref := range refs {
		k := key{member: ref.IsMember(), id: int64(ref.Entry)}
		if k.member {
			k.id = int64(ref.Member)
		}
		if _, ok := refsOf[k]; !ok {
			if len(arg) > 1 {
				arg = append(arg, ',')
			}
			if k.member {
				arg = append(arg, "[1,"...)
			} else {
				arg = append(arg, "[0,"...)
			}
			arg = strconv.AppendInt(arg, k.id, 10)
			arg = append(arg, ']')
		}
		refsOf[k] = append(refsOf[k], ref)
	}
	arg = append(arg, ']')
	rows, err := q.QueryContext(ctx, coveredBySQL, string(arg))
	if err != nil {
		return nil, fmt.Errorf("relations: covered by: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var member bool
		var id int64
		r, err := scanRelation(prefixScan{rows, []any{&member, &id}})
		if err != nil {
			return nil, fmt.Errorf("relations: covered by: %w", err)
		}
		for _, ref := range refsOf[key{member: member, id: id}] {
			out[ref] = r
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("relations: covered by: %w", err)
	}
	return out, nil
}

// prefixScan scans head's columns, then those the caller asks for.
type prefixScan struct {
	rows *sql.Rows
	head []any
}

func (p prefixScan) Scan(dst ...any) error { return p.rows.Scan(append(p.head, dst...)...) }

// coveredBySQL reads ?1, a JSON array of [member, id], and returns each
// ref's relation for CoveredBy: one arm per side and kind of ref, each
// through its side's index.
const coveredBySQL = `WITH want AS (
		SELECT json_extract(value, '$[0]') AS member, json_extract(value, '$[1]') AS id FROM json_each(?1)
	),
	cand AS (
		SELECT w.member AS wm, w.id AS wid, r.* FROM want w CROSS JOIN relations r ON r.a_entry = w.id
		WHERE w.member = 0 AND r.a_member IS NULL AND r.kind IN ('same', 'inside')
		UNION ALL
		SELECT w.member, w.id, r.* FROM want w CROSS JOIN relations r ON r.b_entry = w.id
		WHERE w.member = 0 AND r.b_member IS NULL AND r.kind = 'same'
		UNION ALL
		SELECT w.member, w.id, r.* FROM want w CROSS JOIN relations r ON r.a_member = w.id
		WHERE w.member = 1 AND r.kind IN ('same', 'inside')
		UNION ALL
		SELECT w.member, w.id, r.* FROM want w CROSS JOIN relations r ON r.b_member = w.id
		WHERE w.member = 1 AND r.kind = 'same'
	),
	ranked AS (
		SELECT *, row_number() OVER (PARTITION BY wm, wid ORDER BY matched_bytes DESC, id) AS rn FROM cand
		WHERE gen = (SELECT gen FROM review_state WHERE id = 1)
	)
	SELECT wm, wid, ` + relationColumns + ` FROM ranked WHERE rn = 1`

// Page sizes of Overlaps.
const (
	OverlapsDefaultLimit = 50
	OverlapsMaxLimit     = 500
)

// OverlapPage is one page of Overlaps.
type OverlapPage struct {
	Items []Relation
	// NextCursor continues after the last item; "" on the last page.
	NextCursor string
}

// Overlaps returns a page of the visible generation's overlap relations
// (r2b design D12, the similar folders list) with a side on source src
// ("" = every source), by matched bytes, then ID, descending, after cursor
// ("" for the first page), at most limit (OverlapsDefaultLimit when
// limit ≤ 0, at most OverlapsMaxLimit). A malformed cursor is
// invalid_request. The owner's 280 overlaps need no index.
func Overlaps(ctx context.Context, q store.Queryer, src domain.SourceID, cursor string, limit int) (OverlapPage, error) {
	if limit <= 0 {
		limit = OverlapsDefaultLimit
	}
	limit = min(limit, OverlapsMaxLimit)
	args := []any{sql.Named("src", string(src)), sql.Named("limit", limit+1)}
	after := ""
	if cursor != "" {
		matched, id, err := decodeOverlapCursor(cursor)
		if err != nil {
			return OverlapPage{}, err
		}
		after = ` AND (r.matched_bytes < :matched OR r.matched_bytes = :matched AND r.id < :id)`
		args = append(args, sql.Named("matched", matched), sql.Named("id", id))
	}
	rows, err := q.QueryContext(ctx, `SELECT `+relationColumns+` FROM relations r
		WHERE r.gen = (SELECT gen FROM review_state WHERE id = 1) AND r.kind = 'overlap'
			AND (:src = '' OR EXISTS (SELECT 1 FROM entries e
				WHERE e.id IN (r.a_entry, r.b_entry) AND e.source_id = :src))`+after+`
		ORDER BY r.matched_bytes DESC, r.id DESC
		LIMIT :limit`, args...)
	if err != nil {
		return OverlapPage{}, fmt.Errorf("relations: overlaps: %w", err)
	}
	defer rows.Close()
	page := OverlapPage{Items: []Relation{}}
	for rows.Next() {
		r, err := scanRelation(rows)
		if err != nil {
			return OverlapPage{}, fmt.Errorf("relations: overlaps: %w", err)
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return OverlapPage{}, fmt.Errorf("relations: overlaps: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString(
			[]byte(strconv.FormatInt(last.MatchedBytes, 10) + "." + strconv.FormatInt(last.ID, 10)))
	}
	return page, nil
}

// decodeOverlapCursor reads an Overlaps cursor: the last item's matched
// bytes and ID, "<matched>.<id>", in unpadded base64url.
func decodeOverlapCursor(s string) (matched, id int64, err error) {
	bad := domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, bad
	}
	m, i, ok := strings.Cut(string(raw), ".")
	if !ok {
		return 0, 0, bad
	}
	if matched, err = strconv.ParseInt(m, 10, 64); err != nil {
		return 0, 0, bad
	}
	if id, err = strconv.ParseInt(i, 10, 64); err != nil || id <= 0 {
		return 0, 0, bad
	}
	return matched, id, nil
}
