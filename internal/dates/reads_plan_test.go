package dates

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"precious/internal/dates/datestest"
	"precious/internal/store"
)

// scanOrSortB matches a plan step that reads a whole table or index, or
// sorts rows after reading them.
var scanOrSortB = regexp.MustCompile(`\bSCAN\b|TEMP B-TREE`)

// planB returns the EXPLAIN QUERY PLAN of a statement, one step per line.
func planB(t *testing.T, q store.Queryer, query string, args []any) string {
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

// r5 task 2.6, D10: on the seeded corpus, analyzed, every statement of the
// list (each segment, first page and with a cursor) and its count=only
// read one range of the index D10 names: the flag's partial index, by
// date source, by camera, by time, or entries' (source_id, path) range for
// within; the other filters residual; no table scan and no sort after
// reading; and each row's entry, metadata, and correction by primary key.
// The cameras read finds a camera's media through its index. Run with -v
// to see the plans.
func TestDatesListPlans(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	e.exec(`ANALYZE`)
	if n := e.count(`SELECT count(*) FROM sqlite_stat1 WHERE tbl = 'media_dates'`); n == 0 {
		t.Fatal("ANALYZE left no statistics on media_dates")
	}
	ns := int64(1)
	within := &withinFolder{lo: []byte(bahia + "/"), hi: []byte(bahia + "0")}
	cases := []struct {
		name  string
		q     listQuery
		index string
	}{
		{"by date", listQuery{flag: -1}, "media_dates_by_time"},
		{"date_source", listQuery{flag: -1, source: "exif"}, "media_dates_by_source"},
		{"camera", listQuery{flag: -1, camera: sonyKey}, "media_dates_by_camera"},
		{"camera, flag, and date_source", listQuery{flag: 0, camera: sonyKey, source: "exif"}, "media_dates_by_camera"},
		{"flag and date_source", listQuery{flag: 1, source: "exif"}, "media_dates_implausible"},
		{"within", listQuery{flag: -1, within: within}, "sqlite_autoindex_entries_1"},
		{"within the top", listQuery{flag: -1, within: &withinFolder{top: true}}, "sqlite_autoindex_entries_1"},
		{"within, camera, and flag", listQuery{flag: 2, camera: sonyKey, within: within}, "sqlite_autoindex_entries_1"},
	}
	for i, fl := range apiFlags {
		cases = append(cases, struct {
			name  string
			q     listQuery
			index string
		}{"flag " + fl.name, listQuery{flag: i}, fl.index})
	}
	primary := []string{"SEARCH m USING INTEGER PRIMARY KEY (rowid=?)", "SEARCH c USING INTEGER PRIMARY KEY (rowid=?)"}
	for _, c := range cases {
		c.q.src, c.q.limit = corpusSource, listDefaultLimit
		check := func(what, stmt string, args []any, page bool) {
			t.Helper()
			p := planB(t, e.st.Reader(), stmt, args)
			t.Logf("%s, %s:\n%s", c.name, what, p)
			ok := strings.Contains(p, " INDEX "+c.index+" (")
			if c.q.within == nil {
				ok = ok && strings.Contains(p, "SEARCH e USING INTEGER PRIMARY KEY (rowid=?)")
			} else {
				ok = ok && strings.Contains(p, "SEARCH d USING INTEGER PRIMARY KEY (rowid=?)")
			}
			for _, s := range primary {
				ok = ok && (!page || strings.Contains(p, s))
			}
			if !ok || scanOrSortB.MatchString(p) {
				t.Errorf("%s, %s does not read one range of %s in order:\n%s", c.name, what, c.index, p)
			}
		}
		stmt, args := c.q.countSQL()
		check("count=only", stmt, args, false)
		var cursors []*listCursor
		if c.q.within != nil {
			cursors = []*listCursor{nil, {Path: []byte(bahia + "/IMG_0101.JPG")}}
		} else {
			cursors = []*listCursor{nil, {Ns: &ns, ID: 5}, {None: true, ID: 5}}
		}
		for _, cur := range cursors {
			q := c.q
			q.cursor = cur
			for _, seg := range q.segments() {
				stmt, args := q.pageSQL(seg, 201)
				check(map[segment]string{segPath: "path", segDated: "dated", segDateless: "dateless"}[seg]+
					map[bool]string{true: " after a cursor", false: ""}[cur != nil], stmt, args, true)
			}
		}
	}

	p := planB(t, e.st.Reader(), camerasSQL, []any{string(corpusSource)})
	t.Logf("cameras:\n%s", p)
	if !strings.Contains(p, "SEARCH c USING PRIMARY KEY (source_id=?)") ||
		!strings.Contains(p, "INDEX media_dates_by_camera (source_id=? AND camera_key=?)") ||
		!strings.Contains(p, "SEARCH e USING INTEGER PRIMARY KEY (rowid=?)") || strings.Contains(p, "SCAN") {
		t.Errorf("the cameras read does not find each camera's media through its index:\n%s", p)
	}
}
