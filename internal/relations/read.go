package relations

import (
	"context"
	"fmt"

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
