package index

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

// r4 task 1.4: the quarantine (design D1, D2; ADR 0011). Scans walk it, so
// its rows follow the disk, but the top folder's fold leaves it out, in a
// scan and in Refold alike; readers leave its paths out through the
// rendered predicate.

// The rendered predicates, run against rows of a source: the quarantine and
// everything below it are in quarantine; a sibling whose name only starts
// with the quarantine's, a nested folder of that name, and paths that sort
// next to the bounds are not. IsQuarantinePath agrees with the SQL on every
// row.
func TestQuarantinePredicate(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", posix)
	q := root.Dir(QuarantineName)
	q.Dir("1").Dir("1").File("a.jpg", 1, testNow)
	q.File("1.json", 1, testNow)
	root.Dir(QuarantineName+".x").File("b.jpg", 1, testNow)
	root.Dir(QuarantineName+"0").File("c.jpg", 1, testNow)
	root.Dir(QuarantineName+"-old").File("d.jpg", 1, testNow)
	root.Dir("Fotos").Dir(QuarantineName).File("e.jpg", 1, testNow)
	e.scan("disk")

	want := map[string]bool{
		QuarantineName:                       true,
		QuarantineName + "/1":                true,
		QuarantineName + "/1/1":              true,
		QuarantineName + "/1/1/a.jpg":        true,
		QuarantineName + "/1.json":           true,
		"":                                   false,
		QuarantineName + ".x":                false,
		QuarantineName + ".x/b.jpg":          false,
		QuarantineName + "0":                 false,
		QuarantineName + "0/c.jpg":           false,
		QuarantineName + "-old":              false,
		QuarantineName + "-old/d.jpg":        false,
		"Fotos":                              false,
		"Fotos/" + QuarantineName:            false,
		"Fotos/" + QuarantineName + "/e.jpg": false,
	}
	rows := e.entries("disk")
	if got := slices.Sorted(maps.Keys(rows)); !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("indexed %q", got)
	}
	query := func(cond string) map[string]bool {
		t.Helper()
		r, err := e.st.Reader().Query(`SELECT e.path FROM entries e WHERE e.source_id = 'disk' AND `+cond, nil...)
		if err != nil {
			t.Fatalf("%s: %v", cond, err)
		}
		defer r.Close()
		out := map[string]bool{}
		for r.Next() {
			var p []byte
			if err := r.Scan(&p); err != nil {
				t.Fatal(err)
			}
			out[string(p)] = true
		}
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	in, out := query(InQuarantine("e")), query(NotQuarantined("e"))
	for p, quarantined := range want {
		if in[p] != quarantined || out[p] == quarantined {
			t.Errorf("%q: InQuarantine %v, NotQuarantined %v; want in quarantine %v", p, in[p], out[p], quarantined)
		}
		if IsQuarantinePath([]byte(p)) != quarantined {
			t.Errorf("IsQuarantinePath(%q) = %v", p, !quarantined)
		}
	}
	if len(in)+len(out) != len(want) {
		t.Errorf("the predicates split %d rows into %d and %d", len(want), len(in), len(out))
	}
	// The bounds are BLOB literals: a TEXT bound would compare above every
	// BLOB path and keep nothing out.
	if c := NotQuarantined("e"); strings.Contains(c, "||") || !strings.Contains(c, "X'2e707265") {
		t.Errorf("NotQuarantined = %s", c)
	}
	for _, alias := range []string{"", "e.x", "e;--", "1e"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("InQuarantine(%q) did not refuse the alias", alias)
				}
			}()
			InQuarantine(alias)
		}()
	}
}

// mkdir makes the folder name in the folder at parent, on disk and in the
// index, as the executor's mkdir outcome does, and returns its ID.
func (m *mover) mkdir(parent, name string) domain.EntryID {
	m.t.Helper()
	dir := m.d.open(parent)
	w, _ := fsaccess.AsWriter(dir)
	if err := w.Mkdir([]byte(name)); err != nil {
		m.t.Fatal(err)
	}
	dir.Close()
	p := domain.EntryID(get(m.t, m.e.entries(m.src), parent).ID)
	var id domain.EntryID
	err := m.e.st.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		if id, err = InsertFolder(context.Background(), tx, NewFolder{Source: m.src, Parent: p, Name: []byte(name),
			Facts: m.d.facts(join(parent, name)), ParentFacts: m.d.facts(parent)}); err != nil {
			return err
		}
		return m.rf.Refold(context.Background(), tx, m.src, []domain.EntryID{id})
	})
	if err != nil {
		m.t.Fatalf("mkdir %q: %v", join(parent, name), err)
	}
	return id
}

// quarantineTree builds the tree both quarantine tests start from. A
// nested folder at the quarantine's name is an ordinary folder.
func quarantineTree(root *synthfs.Node) {
	old := testNow.Add(-3 * 365 * 24 * time.Hour)
	fotos := root.Dir("Fotos")
	fotos.File("a.jpg", 100, old)
	fotos.File("b.jpg", 200, testNow)
	docs := root.Dir("Documentos")
	docs.File("relatorio.doc", 300, testNow)
	docs.Dir("Velho").File("c.txt", 40, old.Add(-365*24*time.Hour))
	docs.Dir(QuarantineName).File("z.txt", 3, testNow)
	root.File("setup.exe", 500, testNow)
	root.File("notas.txt", 7, testNow)
}

// foldOf is what a folder's fold computes, but the IDs and paths its
// lists name.
func foldOf(r entry) entry {
	return entry{Kind: r.Kind, TotalBytes: r.TotalBytes, TotalFiles: r.TotalFiles, Newest: r.Newest, Oldest: r.Oldest,
		MainKind: r.MainKind, Category: r.Category, Family: r.Family, Traits: r.Traits, Triage: r.Triage,
		RuleIDs: r.RuleIDs, Group: r.Group, Veto: r.Veto, Partial: r.Partial, State: r.State, Dirs: r.Dirs,
		Files: r.Files, Symlinks: r.Symlinks, Specials: r.Specials, Unreadable: r.Unreadable, Mounts: r.Mounts,
		ByKind: r.ByKind, ByYear: r.ByYear, ByFamily: r.ByFamily, Signals: r.Signals}
}

// The scenario "A rescan after quarantining": entries moved into the
// quarantine's rows with MoveEntry keep their IDs, state, decisions, and
// tags; the top folder's fold, from Refold, leaves them out exactly as a
// scan of the same tree without the quarantine computes it, while the
// quarantine folder's row folds them; a rescan agrees, adding, marking
// missing, and writing nothing. A file added by hand under the quarantine
// is indexed by the next scan, which still leaves it out of the top.
func TestRescanAfterQuarantining(t *testing.T) {
	e := newEnv(t)
	root := e.disk("q", "/q/casa", writable)
	quarantineTree(root)
	e.scan("q")
	before := e.entries("q")
	a, velho := get(t, before, "Fotos/a.jpg"), get(t, before, "Documentos/Velho")
	if _, err := e.st.Writer().Exec(`INSERT INTO tags (id, name, created_at) VALUES (1, 'antigo', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO entry_tags (entry_id, tag_id, added_at) VALUES (?, 1, 0)`, velho.ID); err != nil {
		t.Fatal(err)
	}
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, id := range []int64{a.ID, velho.ID} {
			if _, err := tx.Exec(`UPDATE entries SET decision = 'discard', decision_at = 0 WHERE id = ?`, id); err != nil {
				return err
			}
			if err := decisions.Reinherit(context.Background(), tx, domain.EntryID(id)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	intent := intentRows(t, e, "q")

	m := newMover(t, e, "q", "/q/casa")
	quarantine := m.mkdir("", QuarantineName)
	m.mkdir(QuarantineName, "7")
	m.mkdir(QuarantineName+"/7", "1")
	m.mkdir(QuarantineName+"/7", "2")
	// A scan settles the new folders' own size, which a mkdir does not
	// record (as in R3).
	e.scan("q")
	rows := e.entries("q")
	if !m.move(rows, "Fotos/a.jpg", QuarantineName+"/7/1", "a.jpg") ||
		!m.move(rows, "Documentos/Velho", QuarantineName+"/7/2", "Velho") {
		t.Fatal("the disk refused a move into the quarantine")
	}

	moved := e.entries("q")
	checkTree(t, moved)
	checkEffective(t, moved)
	for from, to := range map[string]string{
		"Fotos/a.jpg":            QuarantineName + "/7/1/a.jpg",
		"Documentos/Velho":       QuarantineName + "/7/2/Velho",
		"Documentos/Velho/c.txt": QuarantineName + "/7/2/Velho/c.txt",
	} {
		if r := get(t, moved, to); r.ID != get(t, before, from).ID || r.State != "present" {
			t.Errorf("%q moved to %q as entry %d %s, want entry %d present", from, to, r.ID, r.State, get(t, before, from).ID)
		}
	}
	if got := intentRows(t, e, "q"); !maps.Equal(got, intent) {
		t.Errorf("the owner's intent changed with the move:\n before %v\n after  %v", intent, got)
	}
	top, q := get(t, moved, ""), get(t, moved, QuarantineName)
	if q.ID != int64(quarantine) || q.TotalBytes != 140 || q.TotalFiles != 2 || q.Dirs.Int64 != 4 {
		t.Errorf("the quarantine folds %d bytes, %d files, %d folders; want 140, 2, 4", q.TotalBytes, q.TotalFiles, q.Dirs.Int64)
	}
	if top.TotalBytes != get(t, before, "").TotalBytes-140 || top.TotalFiles != get(t, before, "").TotalFiles-2 {
		t.Errorf("the top folds %d bytes, %d files; want %d, %d", top.TotalBytes, top.TotalFiles,
			get(t, before, "").TotalBytes-140, get(t, before, "").TotalFiles-2)
	}
	for _, list := range []string{top.Indicators.String, top.Inside.String, top.Signals.String} {
		if strings.Contains(list, "quarantine") || strings.Contains(list, "Velho") {
			t.Errorf("the top's dir_stats name the quarantine: %s", list)
		}
	}

	// The same tree without the quarantine, scanned: its top folds the same.
	plain := e.disk("plain", "/plain/casa", writable)
	quarantineTree(plain)
	plain.Child("Fotos").Remove("a.jpg")
	plain.Child("Documentos").Remove("Velho")
	e.scan("plain")
	if got, want := foldOf(top), foldOf(get(t, e.entries("plain"), "")); got != want {
		t.Errorf("the top folds differently from the same tree without the quarantine:\n got  %+v\n want %+v", got, want)
	}

	rescanned := rescanWritesNothing(t, e, "q")
	if got := intentRows(t, e, "q"); !maps.Equal(got, intent) {
		t.Errorf("the rescan changed the owner's intent")
	}

	// A file added by hand in the quarantine is indexed; only the
	// quarantine's row counts it.
	root.Child(QuarantineName).Child("7").Child("1").File("solto.txt", 5, testNow)
	e.scan("q")
	added := e.entries("q")
	if r, ok := added[QuarantineName+"/7/1/solto.txt"]; !ok || r.State != "present" || r.Size != 5 {
		t.Errorf("the file added by hand is indexed as %+v (%v)", r, ok)
	}
	if got := get(t, added, QuarantineName); got.TotalBytes != 145 || got.TotalFiles != 3 {
		t.Errorf("the quarantine folds %d bytes, %d files; want 145, 3", got.TotalBytes, got.TotalFiles)
	}
	if got := foldOf(get(t, added, "")); got != foldOf(get(t, rescanned, "")) {
		t.Errorf("a file added in the quarantine changed the top's fold")
	}
	rescanWritesNothing(t, e, "q")
}

// A file, not a folder, at the quarantine's name at the top is indexed and
// classified, but the top does not count it either, in a scan and in a
// refold; a folder of that name below the top counts as any folder.
func TestTopEntryAtQuarantineNameIsNotCounted(t *testing.T) {
	e := newEnv(t)
	root := e.disk("q", "/q/casa", writable)
	quarantineTree(root)
	root.File(QuarantineName, 1000, testNow)
	e.scan("q")
	plain := e.disk("plain", "/plain/casa", writable)
	quarantineTree(plain)
	e.scan("plain")

	rows := e.entries("q")
	if r := get(t, rows, QuarantineName); r.Kind != "file" || r.State != "present" || !r.FileKind.Valid || r.FTS != 1 {
		t.Errorf("the file at the quarantine's name is indexed as %+v", r)
	}
	if got, want := foldOf(get(t, rows, "")), foldOf(get(t, e.entries("plain"), "")); got != want {
		t.Errorf("the top counts the file at the quarantine's name:\n got  %+v\n want %+v", got, want)
	}
	if d := get(t, rows, "Documentos"); d.TotalFiles != 3 || d.TotalBytes != 343 {
		t.Errorf("Documentos folds %d files, %d bytes; want its nested %s counted", d.TotalFiles, d.TotalBytes, QuarantineName)
	}

	// A refold of the top, after a move there, leaves it out as well.
	m := newMover(t, e, "q", "/q/casa")
	if !m.move(rows, "notas.txt", "Fotos", "notas.txt") {
		t.Fatal("the disk refused the move")
	}
	rescanWritesNothing(t, e, "q")
	m = newMover(t, e, "plain", "/plain/casa")
	if !m.move(e.entries("plain"), "notas.txt", "Fotos", "notas.txt") {
		t.Fatal("the disk refused the move")
	}
	if got, want := foldOf(get(t, e.entries("q"), "")), foldOf(get(t, e.entries("plain"), "")); got != want {
		t.Errorf("the refolded top counts the file at the quarantine's name:\n got  %+v\n want %+v", got, want)
	}
}

// DeleteSubtree and DeleteEntries remove index rows with their name index
// rows, as a purge's outcome does; with the folders above refolded and the
// disk changed alike, a rescan writes nothing. Neither deletes a source's
// top folder, and IDs no longer indexed are nothing to delete.
func TestDeleteSubtreeAndEntries(t *testing.T) {
	e := newEnv(t)
	root := e.disk("q", "/q/casa", writable)
	quarantineTree(root)
	q := root.Dir(QuarantineName).Dir("7")
	q.Dir("1").Dir("Velho").File("c.txt", 40, testNow)
	q.Child("1").Child("Velho").Dir("sub").File("d.txt", 4, testNow)
	q.Dir("2").File("a.jpg", 100, testNow)
	q.Child("2").File("b.jpg", 10, testNow)
	q.File("1.json", 2, testNow)
	e.scan("q")
	rows := e.entries("q")
	rf := newMover(t, e, "q", "/q/casa").rf
	id := func(p string) domain.EntryID { return domain.EntryID(get(t, rows, p).ID) }
	names := func(ids ...domain.EntryID) int {
		var n int
		for _, id := range ids {
			var c int
			if err := e.st.Reader().QueryRow(`SELECT count(*) FROM entry_names WHERE rowid = ?`, int64(id)).Scan(&c); err != nil {
				t.Fatal(err)
			}
			n += c
		}
		return n
	}
	write := func(fn func(tx *sql.Tx) error) error { return e.st.Write(context.Background(), fn) }

	for _, refuse := range []func(tx *sql.Tx) error{
		func(tx *sql.Tx) error { return DeleteSubtree(context.Background(), tx, id("")) },
		func(tx *sql.Tx) error {
			return DeleteEntries(context.Background(), tx, []domain.EntryID{id(QuarantineName + "/7/2/a.jpg"), id("")})
		},
	} {
		if err := write(func(tx *sql.Tx) error {
			if err := refuse(tx); err == nil {
				t.Errorf("the top folder was deleted")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if d := diffRows(rows, e.entries("q")); len(d) > 0 {
		t.Fatalf("a refused delete changed rows:\n%s", strings.Join(d, "\n"))
	}

	// The whole item 7/1, then its folder's chain refolded.
	whole := []domain.EntryID{id(QuarantineName + "/7/1"), id(QuarantineName + "/7/1/Velho"),
		id(QuarantineName + "/7/1/Velho/c.txt"), id(QuarantineName + "/7/1/Velho/sub"),
		id(QuarantineName + "/7/1/Velho/sub/d.txt")}
	if err := write(func(tx *sql.Tx) error {
		if err := DeleteSubtree(context.Background(), tx, whole[0]); err != nil {
			return err
		}
		if err := DeleteSubtree(context.Background(), tx, whole[0]); err != nil {
			return err // no longer indexed: nothing to delete
		}
		return rf.Refold(context.Background(), tx, "q", []domain.EntryID{id(QuarantineName + "/7")})
	}); err != nil {
		t.Fatal(err)
	}
	after := e.entries("q")
	for _, p := range []string{"/7/1", "/7/1/Velho", "/7/1/Velho/c.txt", "/7/1/Velho/sub", "/7/1/Velho/sub/d.txt"} {
		if _, ok := after[QuarantineName+p]; ok {
			t.Errorf("%s is still indexed", p)
		}
	}
	if n := names(whole...); n != 0 {
		t.Errorf("%d name index rows outlived their entries", n)
	}
	if got := get(t, after, QuarantineName); got.TotalBytes != 112 {
		t.Errorf("the quarantine folds %d bytes after the delete, want 112", got.TotalBytes)
	}
	q.Remove("1")
	rescanWritesNothing(t, e, "q")

	// Part of item 7/2: one file, listed twice, and an ID not indexed.
	part := []domain.EntryID{id(QuarantineName + "/7/2/a.jpg"), id(QuarantineName + "/7/2/a.jpg"), whole[2]}
	if err := write(func(tx *sql.Tx) error {
		if err := DeleteEntries(context.Background(), tx, part); err != nil {
			return err
		}
		return rf.Refold(context.Background(), tx, "q", []domain.EntryID{id(QuarantineName + "/7/2")})
	}); err != nil {
		t.Fatal(err)
	}
	after = e.entries("q")
	if _, ok := after[QuarantineName+"/7/2/a.jpg"]; ok || names(part[0]) != 0 {
		t.Errorf("the removed file is still indexed")
	}
	if _, ok := after[QuarantineName+"/7/2/b.jpg"]; !ok {
		t.Errorf("the file not removed went too")
	}
	q.Child("2").Remove("a.jpg")
	rescanWritesNothing(t, e, "q")

	// A folder and what is below it, given in any order.
	rows = e.entries("q")
	if err := write(func(tx *sql.Tx) error {
		return DeleteEntries(context.Background(), tx, []domain.EntryID{id(QuarantineName + "/7/2/b.jpg"),
			id(QuarantineName + "/7/2"), id(QuarantineName + "/7/1.json")})
	}); err != nil {
		t.Fatal(err)
	}
	after = e.entries("q")
	if got := slices.Sorted(maps.Keys(after)); slices.ContainsFunc(got, func(p string) bool {
		return strings.HasPrefix(p, QuarantineName+"/7/")
	}) {
		t.Errorf("entries below 7 are still indexed: %q", got)
	}
}
