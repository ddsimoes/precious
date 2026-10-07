package search

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/store"
)

// fullScan matches a plan step that reads every row of entries (aliases e,
// d, t), dir_stats (ds), or entry_tags (et), through the table or a whole
// index.
var fullScan = regexp.MustCompile(`\bSCAN (e|d|ds|t|et)\b`)

// plan returns the EXPLAIN QUERY PLAN of a statement, one step per line,
// indented by depth.
func plan(t *testing.T, q store.Queryer, query string, args []any) string {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, query)
	}
	defer rows.Close()
	depth := map[int]int{}
	var b strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		depth[id] = depth[parent] + 1
		b.WriteString(strings.Repeat("  ", depth[id]-1))
		b.WriteString(detail)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestIndexedFiltersAvoidFullScans checks that the page, count, and resolve
// statements of the indexed filters (name through the trigram index, Within
// and tags through the (source_id, path) range, decision through
// entries_eff) never read all of entries. Run with -v to see the plans.
func TestIndexedFiltersAvoidFullScans(t *testing.T) {
	st, disco, _ := corpus(t)
	tag := disco.Tag(st, "familia", "Fotos/2006")
	fotos := disco.ID("Fotos")
	keep := []domain.Decision{domain.DecisionKeep}
	shapes := []struct {
		name  string
		query Query
	}{
		{"name", Query{Name: "natal"}},
		{"name, source, ext", Query{Name: "natal", Source: "disco", Ext: []string{"jpg"}}},
		{"name by name", Query{Name: "natal", Sort: SortName}},
		{"within", Query{Within: &fotos}},
		{"within, ext, year", Query{Within: &fotos, Ext: []string{"jpg"}, YearFrom: ptr(2004)}},
		{"within, short name", Query{Within: &fotos, Name: "c."}},
		{"within root", Query{Within: &disco.Root}},
		{"tag", Query{Tags: []int64{tag}}},
		{"tag, decision", Query{Tags: []int64{tag}, Decisions: keep}},
		{"within, dup", Query{Within: &fotos, Dup: []domain.DupFilter{domain.DupElsewhere, domain.DupCopies, domain.DupUnique}}},
		{"name, dup", Query{Name: "natal", Dup: []domain.DupFilter{domain.DupUnchecked}}},
		{"tag, name", Query{Tags: []int64{tag}, Name: "img"}},
		{"source, decision", Query{Source: "disco", Decisions: keep}},
		{"decision", Query{Decisions: keep}},
		{"within root, tag, decision", Query{Within: &disco.Root, Tags: []int64{tag}, Decisions: keep}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			f, err := buildFilter(context.Background(), st.Reader(), s.query)
			if err != nil {
				t.Fatal(err)
			}
			pageQ, pageArgs := pageSQL(f, s.query.sortSpec(), &position{N: 1, B: []byte("x"), ID: 1}, DefaultLimit)
			countQ, countArgs := countSQL(f)
			resolveQ, resolveArgs := resolveSQL(f, 10)
			for _, stmt := range []struct {
				what  string
				query string
				args  []any
			}{
				{"page", pageQ, pageArgs},
				{"count", countQ, countArgs},
				{"resolve", resolveQ, resolveArgs},
			} {
				p := plan(t, st.Reader(), stmt.query, stmt.args)
				t.Logf("%s:\n%s", stmt.what, p)
				if fullScan.MatchString(p) {
					t.Errorf("%s reads all entries:\n%s", stmt.what, p)
				}
			}
		})
	}
}
