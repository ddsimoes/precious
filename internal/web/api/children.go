package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"precious/internal/domain"
	"precious/internal/index"
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
// (design D11). kind limits the children to one kind: "" for every child,
// or domain.EntryDirectory for folders only (r3 design D16).
type childOrder struct {
	sort string // a search.Sort* key
	col  string // its column
	desc bool
	kind domain.EntryKind
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

// parseChildKind reads kind: empty for every child, or directory for folders
// only, which leaves archives out since an archive is a file.
func parseChildKind(kind string) (domain.EntryKind, error) {
	switch domain.EntryKind(kind) {
	case "", domain.EntryDirectory:
		return domain.EntryKind(kind), nil
	}
	return "", domain.Errorf(domain.CodeInvalidRequest, "unknown kind %q; only directory is accepted", kind)
}

func (o childOrder) order() string {
	if o.desc {
		return search.OrderDesc
	}
	return search.OrderAsc
}

// childCursor is the continuation token of a children page: base64url of
// this JSON, holding the order, the kind filter, and the last row's sort key
// and ID (an entry ID, or a member ID inside an archive). N is the key of a
// numeric sort, nil for a folder with no newest time (NULL); B is the name;
// K is the kind filter, empty for every child.
type childCursor struct {
	Sort  string `json:"s"`
	Order string `json:"o"`
	Kind  string `json:"k,omitempty"`
	N     *int64 `json:"n,omitempty"`
	B     []byte `json:"b,omitempty"`
	ID    int64  `json:"i"`
}

// cursorAfter is the cursor continuing after r.
func (o childOrder) cursorAfter(r *search.Row) string {
	c := childCursor{Sort: o.sort, Order: o.order(), Kind: string(o.kind), ID: int64(r.ID)}
	if r.Member != 0 {
		c.ID = int64(r.Member)
	}
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

// parseCursor reads a cursor made by cursorAfter for the same order and kind
// filter.
func (o childOrder) parseCursor(token string) (*childCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
	}
	var c childCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID <= 0 {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
	}
	ok := c.Sort == o.sort && c.Order == o.order() && c.Kind == string(o.kind)
	switch o.sort {
	case search.SortName:
		ok = ok && c.N == nil
	case search.SortNewest:
		ok = ok && c.B == nil
	default:
		ok = ok && c.B == nil && c.N != nil
	}
	if !ok {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "the cursor belongs to another sort order or kind")
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
	id := c.ID
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

// sql is the statement reading up to limit children of a parent in seg, of
// the order's kind when it has one, outside the quarantine (r4 design D2):
// the residual hides the quarantine folder among the top's children, and
// whatever lies below it.
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
	b.WriteString(` WHERE e.parent_id = ? AND `)
	b.WriteString(index.NotQuarantined("e"))
	if o.kind != "" {
		// The kind is one of the validated constants, never the request's text.
		b.WriteString(` AND e.kind = '` + string(o.kind) + `'`)
	}
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

// children serves GET /api/entries/{ref}/children: one page of the entry's
// children in every state, sorted and continued by keyset cursor; with
// kind=directory, only its folders (r3 design D16), and any other kind is
// invalid_request. A file has no children, except a complete archive, whose
// top members are its children; a member folder's children are its members
// (R2 design D16), sorted in memory, which one archive bounds.
func (h *handler) children(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		parent domain.Ref
		o      childOrder
		after  *childCursor
		limit  int
	)
	err := checkParams(q, "sort", "order", "cursor", "limit", "kind")
	if err == nil {
		parent, err = pathRef(r)
	}
	if err == nil {
		o, err = parseChildOrder(q.Get("sort"), q.Get("order"))
	}
	if err == nil {
		o.kind, err = parseChildKind(q.Get("kind"))
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
		l, err := resolveLevel(ctx, tx, h.pol, parent)
		if err != nil {
			return nil, err
		}
		var (
			items []search.Row
			next  string
		)
		if l.entry != 0 {
			items, next, err = childPage(ctx, tx, l.entry, o, after, limit)
		} else {
			items, next, err = h.memberPage(ctx, tx, l, o, after, limit)
		}
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

// memberPage reads up to limit members of an archive level after the
// cursor, and the cursor of the next page ("" on the last).
func (h *handler) memberPage(ctx context.Context, tx *sql.Tx, l level, o childOrder, after *childCursor, limit int) ([]search.Row, string, error) {
	ms, err := l.members(ctx, tx, h.pol)
	if err != nil {
		return nil, "", err
	}
	if o.kind != "" {
		ms = slices.DeleteFunc(ms, func(m member) bool { return m.row.Kind != o.kind })
	}
	o.sortMembers(ms)
	items := make([]search.Row, 0, min(limit+1, len(ms)))
	for i := range ms {
		if after != nil && !o.afterCursor(&ms[i].row, after) {
			continue
		}
		items = append(items, ms[i].row)
		if len(items) > limit {
			break
		}
	}
	var next string
	if len(items) > limit {
		items = items[:limit]
		next = o.cursorAfter(&items[limit-1])
	}
	return items, next, nil
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
// longer count in their folder's total, so they get no area, and neither
// does the quarantine, which no fold counts (r4 design D2).
var treemapSQL = `SELECT ` + search.Columns + ` FROM ` + search.From + `
	WHERE e.parent_id = ? AND e.state <> 'missing' AND ` + index.NotQuarantined("e") + `
	ORDER BY e.total_bytes DESC, e.id DESC LIMIT ?`

// treemapRestSQL counts and sums every child taking space.
var treemapRestSQL = `SELECT count(*), ifnull(sum(e.total_bytes), 0) FROM entries e
	WHERE e.parent_id = ? AND e.state <> 'missing' AND ` + index.NotQuarantined("e")

// treemap serves GET /api/entries/{ref}/treemap: the entry's EntryRow, its
// 300 largest children by total bytes (ties by descending ID, as the bytes
// sort of children), and the count and bytes of the others. Missing
// children are left out of both. A complete archive and a member folder
// lay out their members (R2 design D16).
func (h *handler) treemap(w http.ResponseWriter, r *http.Request) {
	ref, err := pathRef(r)
	if err == nil {
		err = checkParams(r.URL.Query())
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		l, err := resolveLevel(ctx, tx, h.pol, ref)
		if err != nil {
			return nil, err
		}
		if l.entry == 0 {
			return h.memberTreemap(ctx, tx, l)
		}
		id := l.entry
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

// memberTreemap is the treemap of an archive level: the archive's or the
// member folder's row, its largest members, and the others.
func (h *handler) memberTreemap(ctx context.Context, tx *sql.Tx, l level) (treemapBody, error) {
	var self search.Row
	switch {
	case l.leaf != nil:
		self = l.leaf.row
	case l.folder != nil:
		if _, err := fillFolder(ctx, tx, h.pol, l.folder); err != nil {
			return treemapBody{}, err
		}
		self = l.folder.row
	default:
		rows, err := appendRows(ctx, tx, make([]search.Row, 0, 1),
			`SELECT `+search.Columns+` FROM `+search.From+` WHERE e.id = ?`, []any{int64(l.archive)})
		if err != nil {
			return treemapBody{}, fmt.Errorf("api: treemap of %s: %w", l.archive, err)
		}
		if err := search.LoadTags(ctx, tx, rows); err != nil {
			return treemapBody{}, err
		}
		self = rows[0]
	}
	ms, err := l.members(ctx, tx, h.pol)
	if err != nil {
		return treemapBody{}, err
	}
	childOrder{sort: search.SortBytes, desc: true}.sortMembers(ms)
	body := treemapBody{Entry: rowJSON(&self), Items: make([]entryRow, 0, min(len(ms), treemapItems))}
	for i := range ms {
		if i < treemapItems {
			body.Items = append(body.Items, rowJSON(&ms[i].row))
			continue
		}
		body.Other.Count++
		body.Other.Bytes += ms[i].row.TotalBytes
	}
	return body, nil
}
