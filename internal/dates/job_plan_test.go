package dates

import (
	"strings"
	"testing"

	"precious/internal/dates/datestest"
)

// r5 review, Addendum G1: the store never runs ANALYZE, so without
// statistics pass 1's and pass 3's window queries must still read entries
// by its rowid range, in rowid order, and never walk the source through an
// index of entries and sort.
func TestJobWindowPlansWithoutStatistics(t *testing.T) {
	e, _, _ := newCorpusEnv(t)
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	if n := e.count(`SELECT count(*) FROM sqlite_master WHERE name = 'sqlite_stat1'`); n != 0 {
		t.Fatal("the corpus database holds statistics; this guard needs none")
	}
	for _, c := range []struct {
		name, query string
		args        []any
	}{
		{"pass 1", planWindowSQL, []any{0, planWindow, string(corpusSource)}},
		{"pass 3", deriveWindowSQL, []any{0, string(corpusSource), deriveWindow}},
	} {
		p := planB(t, e.st.Reader(), c.query, c.args)
		t.Logf("%s:\n%s", c.name, p)
		if !strings.Contains(p, "SEARCH e USING INTEGER PRIMARY KEY (rowid>?") || scanOrSortB.MatchString(p) {
			t.Errorf("%s does not read entries by its rowid range without a sort:\n%s", c.name, p)
		}
	}
}
