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
// d, t), dir_stats (ds), entry_tags (et), or file_content (df), through the
// table or a whole index.
var fullScan = regexp.MustCompile(`\bSCAN (e|d|ds|t|et|df|file_content)\b`)

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
// statements of the indexed filters never read all of entries: name
// through the trigram index, Within and tags through the (source_id, path)
// range, decision through entries_eff, the state unreadable through
// entries_unreadable, and the duplicate filters through
// file_content_by_source or the contents with copies
// (file_content_by_content). Run with -v to see the plans.
func TestIndexedFiltersAvoidFullScans(t *testing.T) {
	st, disco, _ := corpus(t)
	tag := disco.Tag(st, "familia", "Fotos/2006")
	fotos := disco.ID("Fotos")
	keep := []domain.Decision{domain.DecisionKeep}
	dup := func(d ...domain.DupFilter) []domain.DupFilter { return d }
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
		{"within, dup", Query{Within: &fotos, Dup: dup(domain.DupElsewhere, domain.DupCopies, domain.DupUnique)}},
		{"name, dup", Query{Name: "natal", Dup: dup(domain.DupUnchecked)}},
		{"tag, name", Query{Tags: []int64{tag}, Name: "img"}},
		{"source, decision", Query{Source: "disco", Decisions: keep}},
		{"decision", Query{Decisions: keep}},
		{"within root, tag, decision", Query{Within: &disco.Root, Tags: []int64{tag}, Decisions: keep}},
		{"copies", Query{Dup: dup(domain.DupCopies)}},
		{"copies, source", Query{Dup: dup(domain.DupCopies), Source: "disco"}},
		{"copies by name", Query{Dup: dup(domain.DupCopies), Sort: SortName}},
		{"unique", Query{Dup: dup(domain.DupUnique)}},
		{"unique, source, ext", Query{Dup: dup(domain.DupUnique), Source: "disco", Ext: []string{"jpg"}}},
		{"unchecked", Query{Dup: dup(domain.DupUnchecked)}},
		{"unchecked, unique ascending", Query{Dup: dup(domain.DupUnchecked, domain.DupUnique), Order: OrderAsc}},
		{"within root, elsewhere", Query{Within: &disco.Root, Dup: dup(domain.DupElsewhere)}},
		{"unreadable", Query{State: StateUnreadable}},
		{"unreadable, source", Query{State: StateUnreadable, Source: "disco"}},
		{"unreadable, name, decision", Query{State: StateUnreadable, Name: "natal", Decisions: keep}},
		{"name, unreadable by name", Query{Name: "natal", State: StateUnreadable, Sort: SortName}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			spec := s.query.sortSpec()
			pf, err := buildFilter(ctx, st.Reader(), s.query, spec.key == SortBytes)
			if err != nil {
				t.Fatal(err)
			}
			cf, err := buildFilter(ctx, st.Reader(), s.query, !copiesOnly(s.query.Dup))
			if err != nil {
				t.Fatal(err)
			}
			f, err := buildFilter(ctx, st.Reader(), s.query, false)
			if err != nil {
				t.Fatal(err)
			}
			pageQ, pageArgs := idsSQL(pf, spec, &position{N: 1, B: []byte("x"), ID: 1}, DefaultLimit)
			countQ, countArgs := countSQL(cf)
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

// TestDupPagesBySizeMerge checks that a page by bytes the duplicate filter
// drives reads each source's files of each content state it needs in size
// order through file_content_by_source and merges them, so it never sorts
// its matches: it stops after the page (r2b design D8).
func TestDupPagesBySizeMerge(t *testing.T) {
	st, _, _ := corpus(t)
	ctx := context.Background()
	for _, q := range []Query{
		{Dup: []domain.DupFilter{domain.DupCopies}},
		{Dup: []domain.DupFilter{domain.DupUnique}, Source: "pen"},
		{Dup: []domain.DupFilter{domain.DupUnchecked, domain.DupCopies}, Order: OrderAsc, Ext: []string{"jpg"}},
	} {
		f, err := buildFilter(ctx, st.Reader(), q, true)
		if err != nil {
			t.Fatal(err)
		}
		sources := 2
		if q.Source != "" {
			sources = 1
		}
		if want := sources * len(statesOf(q.Dup)); len(f.arms) != want {
			t.Errorf("%+v: %d arms, want %d", q, len(f.arms), want)
		}
		stmt, args := idsSQL(f, q.sortSpec(), &position{N: 5, ID: 9}, DefaultLimit)
		p := plan(t, st.Reader(), stmt, args)
		t.Logf("%+v:\n%s", q, p)
		if strings.Contains(p, "TEMP B-TREE") || strings.Count(p, "file_content_by_source (source_id=? AND state=?") != len(f.arms) ||
			len(f.arms) > 1 && !strings.Contains(p, "MERGE (UNION ALL)") {
			t.Errorf("%+v does not merge its size-ordered ranges:\n%s", q, p)
		}
	}
}
