package decisions

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/search"
)

// fullScan matches a plan step that reads every row of a table, through the
// table or a whole index.
var fullScan = regexp.MustCompile(`\bSCAN (entries|selection_entries|e|s|p)\b`)

// plan returns the EXPLAIN QUERY PLAN of a statement, one step per line.
func plan(t *testing.T, tx *sql.Tx, query string, args []any) string {
	t.Helper()
	rows, err := tx.Query(`EXPLAIN QUERY PLAN `+query, args...)
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

// The subtree updates, the cut point read, the target statements, and the
// totals read go through indexes, never through every entry: a decision on
// a folder costs its subtree, not the index. Run with -v to see the plans.
func TestStatementsUseIndexes(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	sel := e.createSelection(t, search.Query{Source: s.Source})
	from := s.ID("Fotos")
	err := e.write(func(tx *sql.Tx) error {
		ids, err := resolveTargets(context.Background(), tx, start, []domain.EntryID{from}, "")
		if err != nil {
			return err
		}
		selection, err := resolveTargets(context.Background(), tx, start, nil, sel.ID)
		if err != nil {
			return err
		}
		type stmt struct {
			name, query string
			args        []any
		}
		var stmts []stmt
		add := func(name, query string, args []any) { stmts = append(stmts, stmt{name, query, args}) }
		q, a := setRangeSQL(s.Source, descendants([]byte("Fotos")), discard, &from)
		add("subtree update", q, a)
		q, a = setRangeSQL(s.Source, descendants(nil), discard, &from)
		add("root subtree update", q, a)
		q, a = cutPointsSQL(s.Source, descendants([]byte("Fotos")))
		add("cut points", q, a)
		add("totals", totalsSQL, []any{string(s.Source)})
		add("selection measure", selectionMeasureSQL, []any{sel.ID})
		for name, tg := range map[string]targets{"entry_ids": ids, "selection": selection} {
			add(name+" own update", `UPDATE entries SET decision = 'keep' WHERE id IN (`+tg.sub+`) AND eff_decision <> 'keep'`, tg.args)
			add(name+" leaf inherit", `UPDATE entries SET (eff_decision, eff_from) =
				(SELECT p.eff_decision, p.eff_from FROM entries p WHERE p.id = entries.parent_id)
				WHERE id IN (`+tg.sub+`) AND kind <> 'directory'`, tg.args)
			add(name+" tag removal", `DELETE FROM entry_tags WHERE entry_id IN (`+tg.sub+`) AND tag_id IN (SELECT value FROM json_each(?))`,
				append(tg.args, "[1]"))
		}
		for _, st := range stmts {
			p := plan(t, tx, st.query, st.args)
			t.Logf("%s:\n%s", st.name, p)
			if fullScan.MatchString(p) {
				t.Errorf("%s reads a whole table:\n%s", st.name, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
