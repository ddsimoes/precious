package stale

import (
	"context"
	"database/sql"
	"maps"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

const q = ".precious-quarantine"

// fixture holds checks of two sources, s1 and s2, each with a set item, a
// recorded file, and, for some, a recorded copy.
type fixture struct {
	t  *testing.T
	st *store.Store
}

func (f fixture) exec(query string, args ...any) int64 {
	f.t.Helper()
	res, err := f.st.Writer().Exec(query, args...)
	if err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// check inserts a check of src in state with its set item at item, a
// recorded file below it, and a copy on copySource at copyPath when given.
func (f fixture) check(src, state, item, copySource, copyPath string) int64 {
	f.t.Helper()
	var parent int64
	if err := f.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = X''`, src).Scan(&parent); err != nil {
		f.t.Fatal(err)
	}
	entry := f.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, ?, ?, ?, 'directory', 'present', 0, 0, 0)`, src, parent, []byte(item), []byte(item))
	id := f.exec(`INSERT INTO purge_checks (source_id, state, created_at) VALUES (?, ?, 0)`, src, state)
	f.exec(`INSERT INTO purge_check_items (check_id, entry_id, path, readable) VALUES (?, ?, ?, 1)`, id, entry, []byte(item))
	var cs, cp any
	if copyPath != "" {
		cs, cp = copySource, []byte(copyPath)
	}
	f.exec(`INSERT INTO purge_check_files (check_id, item_id, entry_id, kind, path, size, verdict, copy_source, copy_path)
		VALUES (?, ?, ?, 'file', ?, 1, 'safe', ?, ?)`, id, entry, entry, []byte(item+"/f/a.jpg"), cs, cp)
	return id
}

func (f fixture) states() map[int64]string {
	f.t.Helper()
	rows, err := f.st.Reader().Query(`SELECT id, state || coalesce('/' || stale_reason, '') FROM purge_checks`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var s string
		if err := rows.Scan(&id, &s); err != nil {
			f.t.Fatal(err)
		}
		out[id] = s
	}
	return out
}

func (f fixture) mark(src domain.SourceID, path string) {
	f.t.Helper()
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error { return MarkStale(context.Background(), tx, src, []byte(path)) })
	if err != nil {
		f.t.Fatal(err)
	}
}

// r4 task 1.6: MarkStale marks a check when the folder holding a copy it
// recorded is passed, on the copy's source, and leaves alone checks of
// other paths and other sources, checks no longer running or ready, and
// paths that only share a prefix. Set items and recorded files match on
// the check's own source, and the top folder matches everything of it.
func TestMarkStale(t *testing.T) {
	f := fixture{t: t, st: storetest.Open(t)}
	for _, src := range []string{"s1", "s2"} {
		f.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, capabilities, state,
			created_at) VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', '{}', 'online', 0)`, src, src, "v-"+src)
		f.exec(`INSERT INTO entries (source_id, name, path, kind, state, first_seen, last_seen, scan_gen)
			VALUES (?, X'', X'', 'directory', 'present', 0, 0, 0)`, src)
	}
	c1 := f.check("s1", "ready", q+"/7/1", "s2", "Fotos/2004/a.jpg")
	c2 := f.check("s1", "running", q+"/7/2", "s1", "Docs/b.jpg")
	c3 := f.check("s2", "ready", q+"/1/1", "s1", "Fotos/2004/a.jpg")
	c4 := f.check("s1", "failed", q+"/7/3", "s2", "Fotos/2004/x.jpg")
	c5 := f.check("s1", "ready", q+"/8/1", "", "")
	c6 := f.check("s2", "ready", q+"/2/1", "s1", "Z/z.jpg")
	want := f.states()
	stale := "stale/" + ReasonIndexChanged
	step := func(src domain.SourceID, path string, marked ...int64) {
		t.Helper()
		f.mark(src, path)
		for _, id := range marked {
			want[id] = stale
		}
		if got := f.states(); !maps.Equal(got, want) {
			t.Fatalf("after MarkStale(%s, %q): %v, want %v", src, path, got, want)
		}
	}

	// The folder of c1's copy, on the copy's source: only c1. c3 records a
	// copy at the same path on the other source; c4 is failed.
	step("s2", "Fotos/2004", c1)
	// A name sharing a prefix, and a path beside the copies, mark nothing.
	step("s1", "Fotos/200")
	step("s1", "Fotos/20040")
	step("s1", "Docs/b")
	// The copy itself.
	step("s1", "Docs/b.jpg", c2)
	// A set item's folder, and a recorded file, on the check's source only.
	step("s1", q+"/2")
	step("s2", q+"/8/1")
	step("s1", q+"/8", c5)
	step("s2", q+"/1/1/f/a.jpg", c3)
	// The top folder of s1 holds c6's copy.
	step("s1", "", c6)
	if want[c4] != "failed" {
		t.Errorf("the failed check became %s", want[c4])
	}

	// The copy branch reads the copy index; each check's rows by its own.
	lo, hi := []byte("a/"), []byte("a0")
	rows, err := f.st.Reader().Query(`EXPLAIN QUERY PLAN `+markBelowSQL,
		ReasonIndexChanged, "s1", []byte("a"), lo, hi, []byte("a"), lo, hi, "s1", []byte("a"), lo, hi)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	all := strings.Join(plan, "\n")
	for _, idx := range []string{"purge_check_files_copy", "purge_check_files_path"} {
		if !strings.Contains(all, idx) {
			t.Errorf("the plan does not use %s:\n%s", idx, all)
		}
	}
}
