package api

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/search"
	"precious/internal/store"
)

// scanOrSort matches a plan step that reads a whole table or index, or
// sorts rows after reading them.
var scanOrSort = regexp.MustCompile(`\bSCAN\b|TEMP B-TREE`)

// statsByKey is the plan step reading a row's dir_stats (search.From) by
// its primary key.
const statsByKey = "SEARCH ds USING INTEGER PRIMARY KEY (rowid=?) LEFT-JOIN"

// plan returns the EXPLAIN QUERY PLAN of a statement, one step per line.
func plan(t *testing.T, q store.Queryer, query string, args []any) string {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, query)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		b.WriteString(detail)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestChildrenPlansUseTheirIndex checks that every statement of a children
// page, for each sort, order, and kind of cursor, reads one range of the
// sort's index (parent_id, key, id) in order: no table or index scan, and
// no sort after reading (design D11, R1.10), and reads each row's dir_stats
// by its primary key. The treemap's statements read the parent's range of
// entries_by_bytes. Run with -v to see the plans.
func TestChildrenPlansUseTheirIndex(t *testing.T) {
	e := newEnv(t)
	// A few rows, so the plans are those of a populated table.
	indextest.Seed(t, e.st, indextest.Tree{Source: "s", CreateSource: true, Nodes: []indextest.Node{
		{Path: "a/x", Size: 1}, {Path: "b", Kind: domain.EntryDirectory},
	}})
	index := map[string]string{
		search.SortBytes:  "entries_by_bytes",
		search.SortFiles:  "entries_by_files",
		search.SortNewest: "entries_by_newest",
		search.SortName:   "entries_by_name",
	}
	n := int64(5)
	cursors := map[string]*childCursor{
		"first page": nil,
		"key":        {N: &n, B: []byte("x"), ID: 3},
		"null key":   {ID: 3},
	}
	for _, sort := range []string{search.SortBytes, search.SortFiles, search.SortNewest, search.SortName} {
		for _, order := range []string{search.OrderDesc, search.OrderAsc} {
			o, err := parseChildOrder(sort, order)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []domain.EntryKind{"", domain.EntryDirectory} {
				o.kind = kind
				for what, c := range cursors {
					if what == "null key" && sort != search.SortNewest {
						continue
					}
					if c != nil && sort == search.SortName {
						c = &childCursor{B: c.B, ID: c.ID}
					}
					for i, seg := range o.segments(c) {
						args := append(append([]any{int64(1)}, seg.args...), 201)
						p := plan(t, e.st.Reader(), o.sql(seg), args)
						t.Logf("%s %s kind %q, %s, segment %d:\n%s", sort, order, kind, what, i, p)
						if scanOrSort.MatchString(p) || !strings.Contains(p, "USING INDEX "+index[sort]+" ") ||
							!strings.Contains(p, statsByKey) {
							t.Errorf("%s %s kind %q, %s, segment %d does not read one range of %s in order:\n%s",
								sort, order, kind, what, i, index[sort], p)
						}
					}
				}
			}
		}
	}
	for _, stmt := range []struct {
		name, query string
		args        []any
	}{
		{"treemap items", treemapSQL, []any{int64(1), treemapItems}},
		{"treemap rest", treemapRestSQL, []any{int64(1)}},
	} {
		p := plan(t, e.st.Reader(), stmt.query, stmt.args)
		t.Logf("%s:\n%s", stmt.name, p)
		if scanOrSort.MatchString(p) || !strings.Contains(p, "(parent_id=?)") ||
			(stmt.query == treemapSQL && !strings.Contains(p, statsByKey)) {
			t.Errorf("%s does not read the parent's range in order:\n%s", stmt.name, p)
		}
	}
}
