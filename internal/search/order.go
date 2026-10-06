package search

import (
	"encoding/base64"
	"encoding/json"

	"precious/internal/domain"
)

// sortSpec is a validated result order.
type sortSpec struct {
	key  string // Sort* constant
	desc bool
	// expr is the SQL sort key. It is never NULL, so a cursor compares with
	// one row value: a folder with no files sorts as the oldest.
	expr string
}

func (q Query) sortSpec() sortSpec {
	s := sortSpec{key: q.Sort}
	switch q.Sort {
	case SortFiles:
		s.expr = `e.total_files`
	case SortNewest:
		s.expr = `ifnull(e.newest_ns, -9223372036854775807 - 1)`
	case SortName:
		s.expr = `e.name`
	default:
		s.key, s.expr = SortBytes, `e.total_bytes`
	}
	switch q.Order {
	case OrderAsc:
	case OrderDesc:
		s.desc = true
	default:
		s.desc = s.key != SortName
	}
	return s
}

func (s sortSpec) order() string {
	if s.desc {
		return OrderDesc
	}
	return OrderAsc
}

// position is a row's place in an order: its sort key (N, or B for name)
// and its ID.
type position struct {
	N  int64          `json:"n,omitempty"`
	B  []byte         `json:"b,omitempty"`
	ID domain.EntryID `json:"i"`
}

// key is the bound value of the sort key.
func (p position) key(s sortSpec) any {
	if s.key != SortName {
		return p.N
	}
	if p.B == nil {
		// A nil slice binds as NULL; the root's name is the empty BLOB.
		return []byte{}
	}
	return p.B
}

// cursor is the opaque continuation token: base64url of this JSON.
type cursor struct {
	Sort  string `json:"s"`
	Order string `json:"o"`
	position
}

func encodeCursor(s sortSpec, p position) string {
	// The fields are plain values, so Marshal cannot fail.
	b, _ := json.Marshal(cursor{Sort: s.key, Order: s.order(), position: p})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reads a cursor made by encodeCursor for the same order.
func decodeCursor(token string, s sortSpec) (position, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return position{}, badCursor()
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID <= 0 {
		return position{}, badCursor()
	}
	if c.Sort != s.key || c.Order != s.order() || (c.B != nil && s.key != SortName) || (c.N != 0 && s.key == SortName) {
		return position{}, domain.Errorf(domain.CodeInvalidRequest, "the cursor belongs to another sort order")
	}
	return c.position, nil
}

func badCursor() error {
	return domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
}
