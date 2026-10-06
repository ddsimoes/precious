package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"precious/internal/domain"
	"precious/internal/search"
)

const (
	// childrenDefaultLimit and childrenMaxLimit bound a children page.
	childrenDefaultLimit = 200
	childrenMaxLimit     = 1000
	// treemapItems is the number of children a treemap level shows; the
	// rest make its other bucket.
	treemapItems = 300
)

// childOrder is a validated children order. Each sort key has its index
// (parent_id, key, id), so a page is one range of that index read in order
// (design D11).
type childOrder struct {
	sort string // a search.Sort* key
	col  string // its column
	desc bool
}

// parseChildOrder reads sort (bytes, files, newest, or name; default bytes)
// and order (desc or asc; default desc, asc for name).
func parseChildOrder(sort, order string) (childOrder, error) {
	var o childOrder
	switch sort {
	case "", search.SortBytes:
		o.sort, o.col = search.SortBytes, "e.total_bytes"
	case search.SortFiles:
		o.sort, o.col = search.SortFiles, "e.total_files"
	case search.SortNewest:
		o.sort, o.col = search.SortNewest, "e.newest_ns"
	case search.SortName:
		o.sort, o.col = search.SortName, "e.name"
	default:
		return childOrder{}, domain.Errorf(domain.CodeInvalidRequest, "unknown sort %q", sort)
	}
	switch order {
	case "":
		o.desc = o.sort != search.SortName
	case search.OrderDesc:
		o.desc = true
	case search.OrderAsc:
	default:
		return childOrder{}, domain.Errorf(domain.CodeInvalidRequest, "unknown order %q", order)
	}
	return o, nil
}

func (o childOrder) order() string {
	if o.desc {
		return search.OrderDesc
	}
	return search.OrderAsc
}

// childCursor is the continuation token of a children page: base64url of
// this JSON, holding the order and the last row's sort key and ID. N is
// the key of a numeric sort, nil for a folder with no newest time (NULL);
// B is the name.
type childCursor struct {
	Sort  string         `json:"s"`
	Order string         `json:"o"`
	N     *int64         `json:"n,omitempty"`
	B     []byte         `json:"b,omitempty"`
	ID    domain.EntryID `json:"i"`
}

// cursorAfter is the cursor continuing after r.
func (o childOrder) cursorAfter(r *search.Row) string {
	c := childCursor{Sort: o.sort, Order: o.order(), ID: r.ID}
	switch o.sort {
	case search.SortBytes:
		c.N = &r.TotalBytes
	case search.SortFiles:
		c.N = &r.TotalFiles
	case search.SortNewest:
		if !r.Newest.IsZero() {
			n := r.Newest.UnixNano()
			c.N = &n
		}
	case search.SortName:
		c.B = r.Name
	}
	// The fields are plain values, so Marshal cannot fail.
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseCursor reads a cursor made by cursorAfter for the same order.
func (o childOrder) parseCursor(token string) (*childCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
	}
	var c childCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID <= 0 {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
	}
	ok := c.Sort == o.sort && c.Order == o.order()
	switch o.sort {
	case search.SortName:
		ok = ok && c.N == nil
	case search.SortNewest:
		ok = ok && c.B == nil
	default:
		ok = ok && c.B == nil && c.N != nil
	}
	if !ok {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "the cursor belongs to another sort order")
	}
	return &c, nil
}

// segment is a condition, after e.parent_id = ?, selecting one range of
// the order's index.
type segment struct {
	cond string
	args []any
}

// segments are the ranges of the children after c (all children when c is
// nil), in order. A row-value comparison never matches a NULL key, so the
// folders without a newest time, which SQLite sorts first, are their own
// range: last in descending order, first in ascending order.
func (o childOrder) segments(c *childCursor) []segment {
	if c == nil {
		return []segment{{}}
	}
	id := int64(c.ID)
	cmp := ">"
	if o.desc {
		cmp = "<"
	}
	if o.sort == search.SortNewest && c.N == nil {
		after := segment{cond: "e.newest_ns IS NULL AND e.id " + cmp + " ?", args: []any{id}}
		if o.desc {
			return []segment{after}
		}
		return []segment{after, {cond: "e.newest_ns IS NOT NULL"}}
	}
	var key any
	if o.sort == search.SortName {
		key = c.B
		if c.B == nil {
			// A nil slice binds as NULL; an empty name is the empty BLOB.
			key = []byte{}
		}
	} else {
		key = *c.N
	}
	after := segment{cond: "(" + o.col + ", e.id) " + cmp + " (?, ?)", args: []any{key, id}}
	if o.sort == search.SortNewest && o.desc {
		return []segment{after, {cond: "e.newest_ns IS NULL"}}
	}
	return []segment{after}
}

// sql is the statement reading up to limit children of a parent in seg.
func (o childOrder) sql(seg segment) string {
	dir := " ASC"
	if o.desc {
		dir = " DESC"
	}
	var b strings.Builder
	b.WriteString(`SELECT `)
	b.WriteString(search.Columns)
	b.WriteString(` FROM `)
	b.WriteString(search.From)
	b.WriteString(` WHERE e.parent_id = ?`)
	if seg.cond != "" {
		b.WriteString(` AND `)
		b.WriteString(seg.cond)
	}
	b.WriteString(` ORDER BY `)
	b.WriteString(o.col)
	b.WriteString(dir)
	b.WriteString(`, e.id`)
	b.WriteString(dir)
	b.WriteString(` LIMIT ?`)
	return b.String()
}

type childrenBody struct {
	Items      []entryRow `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

// children serves GET /api/entries/{id}/children: one page of the entry's
// children in every state, sorted and continued by keyset cursor. A file
// has no children.
func (h *handler) children(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		parent domain.EntryID
		o      childOrder
		after  *childCursor
		limit  int
	)
	err := checkParams(q, "sort", "order", "cursor", "limit")
	if err == nil {
		parent, err = pathID(r)
	}
	if err == nil {
		o, err = parseChildOrder(q.Get("sort"), q.Get("order"))
	}
	if err == nil && q.Get("cursor") != "" {
		after, err = o.parseCursor(q.Get("cursor"))
	}
	if err == nil {
		limit, err = parseLimit(q.Get("limit"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if limit == 0 {
		limit = childrenDefaultLimit
	}
	limit = min(limit, childrenMaxLimit)
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		if err := entryExists(ctx, tx, parent); err != nil {
			return nil, err
		}
		items, next, err := childPage(ctx, tx, parent, o, after, limit)
		if err != nil {
			return nil, err
		}
		body := childrenBody{Items: rowsJSON(items)}
		if next != "" {
			body.NextCursor = &next
		}
		return body, nil
	})
}

// childPage reads up to limit children of parent after the cursor, with
// their own tags, and the cursor of the next page ("" on the last).
func childPage(ctx context.Context, tx *sql.Tx, parent domain.EntryID, o childOrder, after *childCursor, limit int) ([]search.Row, string, error) {
	items := make([]search.Row, 0, limit+1)
	for _, seg := range o.segments(after) {
		args := make([]any, 0, len(seg.args)+2)
		args = append(args, int64(parent))
		args = append(args, seg.args...)
		args = append(args, limit+1-len(items))
		var err error
		if items, err = appendRows(ctx, tx, items, o.sql(seg), args); err != nil {
			return nil, "", fmt.Errorf("api: children of %s: %w", parent, err)
		}
		if len(items) > limit {
			break
		}
	}
	var next string
	if len(items) > limit {
		items = items[:limit]
		next = o.cursorAfter(&items[limit-1])
	}
	if err := search.LoadTags(ctx, tx, items); err != nil {
		return nil, "", err
	}
	return items, next, nil
}

// appendRows appends the rows of a statement selecting search.Columns.
func appendRows(ctx context.Context, tx *sql.Tx, dst []search.Row, query string, args []any) ([]search.Row, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := search.ScanRow(rows)
		if err != nil {
			return nil, err
		}
		dst = append(dst, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return dst, rows.Close()
}

type treemapBody struct {
	Entry entryRow   `json:"entry"`
	Items []entryRow `json:"items"`
	Other otherArea  `json:"other"`
}

type otherArea struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// treemapSQL lists the largest children taking space: missing entries no
// longer count in their folder's total, so they get no area.
const treemapSQL = `SELECT ` + search.Columns + ` FROM ` + search.From + `
	WHERE e.parent_id = ? AND e.state <> 'missing' ORDER BY e.total_bytes DESC, e.id DESC LIMIT ?`

// treemapRestSQL counts and sums every child taking space.
const treemapRestSQL = `SELECT count(*), ifnull(sum(e.total_bytes), 0) FROM entries e
	WHERE e.parent_id = ? AND e.state <> 'missing'`

// treemap serves GET /api/entries/{id}/treemap: the entry's EntryRow, its
// 300 largest children by total bytes (ties by descending ID, as the bytes
// sort of children), and the count and bytes of the others. Missing
// children are left out of both.
func (h *handler) treemap(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = checkParams(r.URL.Query())
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		entry, err := appendRows(ctx, tx, make([]search.Row, 0, 1),
			`SELECT `+search.Columns+` FROM `+search.From+` WHERE e.id = ?`, []any{int64(id)})
		if err != nil {
			return nil, fmt.Errorf("api: treemap of %s: %w", id, err)
		}
		if len(entry) == 0 {
			return nil, notFound(id)
		}
		items, err := appendRows(ctx, tx, make([]search.Row, 0, treemapItems), treemapSQL, []any{int64(id), treemapItems})
		if err != nil {
			return nil, fmt.Errorf("api: treemap of %s: %w", id, err)
		}
		var other otherArea
		if err := tx.QueryRowContext(ctx, treemapRestSQL, int64(id)).Scan(&other.Count, &other.Bytes); err != nil {
			return nil, fmt.Errorf("api: treemap of %s: %w", id, err)
		}
		other.Count -= int64(len(items))
		for i := range items {
			other.Bytes -= items[i].TotalBytes
		}
		if err := search.LoadTags(ctx, tx, entry); err != nil {
			return nil, err
		}
		if err := search.LoadTags(ctx, tx, items); err != nil {
			return nil, err
		}
		return treemapBody{Entry: rowJSON(&entry[0]), Items: rowsJSON(items), Other: other}, nil
	})
}
