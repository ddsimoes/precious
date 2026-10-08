package index

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"
)

// fullScan matches a plan step that reads every row of a table, through the
// table or a whole index.
var fullScan = regexp.MustCompile(`\bSCAN (entries|e|dir_stats|d|entry_tags|t|entry_overrides|o)\b`)

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

// The statements that make the index follow a step (r3 design D6) read a
// subtree through the (source_id, path) index, and a folder's children
// through parent_id, never every entry: a move costs its subtree and the
// folders above it, not the index. Run with -v to see the plans.
func TestMoveStatementsUseIndexes(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", writable)
	root.Dir("Fotos").Dir("2004").File("a.jpg", 1, testNow)
	e.scan("disk")
	lo, hi := subtree([]byte("Fotos"))
	src, path := "disk", []byte("Fotos")
	stmts := []struct {
		name, query string
		args        []any
	}{
		{"intent at and below", intentSQL, []any{src, path, src, lo, hi}},
		{"intent below", intentBelowSQL, []any{src, lo, hi}},
		{"present below", presentBelowSQL, []any{src, lo, hi}},
		{"delete names below", writerQueries[stDeleteSubNames], []any{src, lo, hi}},
		{"delete below", deleteBelowSQL, []any{src, lo, hi}},
		{"move paths", movePathsSQL, []any{[]byte("Novo"), len(path) + 1, src, lo, hi}},
		{"folder lists below", folderListsSQL, []any{src, lo, hi}},
		{"children", childrenQuery, []any{1}},
		{"stored by id", storedByIDQuery, []any{1}},
		{"owners", ownersQuery, []any{1, 1}},
		{"refold row", refoldRowSQL, append((&row{kind: "directory", state: "present"}).args(nil, nil), 1)},
	}
	err := e.st.Write(t.Context(), func(tx *sql.Tx) error {
		for _, s := range stmts {
			p := plan(t, tx, s.query, s.args)
			t.Logf("%s:\n%s", s.name, p)
			if fullScan.MatchString(p) {
				t.Errorf("%s reads a whole table:\n%s", s.name, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
