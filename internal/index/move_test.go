package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	"precious/internal/corpus"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/store"
)

// writable is posix with the no-replace rename synthfs needs to rename.
var writable = func() fsaccess.Capabilities {
	c := posix
	c.NoReplaceRename = true
	return c
}()

// splitPath returns the folder holding the raw path p and p's name.
func splitPath(p string) (dir, name string) {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// disk reaches the folders of a synthfs source by path, as the executor
// reaches them through rooted handles.
type disk struct {
	t    *testing.T
	e    *env
	root string // the source's absolute root
}

// open opens the folder at the raw path p below the root.
func (d disk) open(p string) fsaccess.Dir {
	d.t.Helper()
	dir, err := d.e.sfs.OpenRoot(d.root)
	if err != nil {
		d.t.Fatal(err)
	}
	if p == "" {
		return dir
	}
	for _, name := range strings.Split(p, "/") {
		info, err := dir.Lstat([]byte(name))
		if err != nil {
			d.t.Fatalf("lstat %q in %q: %v", name, p, err)
		}
		next, err := dir.OpenDir([]byte(name), info)
		dir.Close()
		if err != nil {
			d.t.Fatalf("open %q in %q: %v", name, p, err)
		}
		dir = next
	}
	return dir
}

// facts lstats the entry at the raw path p, as the executor confirms a
// step.
func (d disk) facts(p string) PostFacts {
	d.t.Helper()
	var info fsaccess.EntryInfo
	if p == "" {
		dir := d.open("")
		info = dir.Self()
		dir.Close()
	} else {
		parent, name := splitPath(p)
		dir := d.open(parent)
		var err error
		info, err = dir.Lstat([]byte(name))
		dir.Close()
		if err != nil {
			d.t.Fatalf("lstat %q: %v", p, err)
		}
	}
	f := PostFacts{Dev: info.Dev, Ino: info.Ino, MtimeNs: info.ModTime.UnixNano()}
	if !info.Ctime.IsZero() {
		f.CtimeNs = info.Ctime.UnixNano()
	}
	return f
}

// rename renames the entry at from to the folder to under name on disk; it
// reports the refusal when the disk refuses.
func (d disk) rename(from, to, name string) error {
	d.t.Helper()
	parent, old := splitPath(from)
	src, dst := d.open(parent), d.open(to)
	defer src.Close()
	defer dst.Close()
	w, ok := fsaccess.AsWriter(src)
	if !ok {
		d.t.Fatal("synthfs folders write")
	}
	return w.RenameNoReplace([]byte(old), dst, []byte(name))
}

// mover moves entries of one source on disk and in the index, as the
// executor's outcome transaction does (r3 design D6).
type mover struct {
	t   *testing.T
	e   *env
	d   disk
	src domain.SourceID
	rf  *Refolder
}

func newMover(t *testing.T, e *env, src domain.SourceID, root string) *mover {
	return &mover{t: t, e: e, d: disk{t: t, e: e, root: root}, src: src, rf: NewRefolder(rules.Default())}
}

// move moves the indexed entry at from into the folder to under name, on
// disk and in the index. It reports false, changing nothing, when the disk
// refuses.
func (m *mover) move(rows map[string]entry, from, to, name string) bool {
	m.t.Helper()
	if err := m.d.rename(from, to, name); err != nil {
		m.t.Logf("disk refused %q -> %q: %v", from, join(to, name), err)
		return false
	}
	parent, _ := splitPath(from)
	mv := Move{Source: m.src, Entry: domain.EntryID(get(m.t, rows, from).ID),
		NewParent: domain.EntryID(get(m.t, rows, to).ID), NewName: []byte(name),
		Facts: m.d.facts(join(to, name)), OldParentFacts: m.d.facts(parent), NewParentFacts: m.d.facts(to)}
	oldParent := domain.EntryID(get(m.t, rows, parent).ID)
	err := m.e.st.Write(context.Background(), func(tx *sql.Tx) error {
		old, new, err := MoveEntry(context.Background(), tx, mv)
		if err != nil {
			return err
		}
		if string(old) != from || string(new) != join(to, name) {
			return fmt.Errorf("MoveEntry moved %q to %q", old, new)
		}
		if err := decisions.Reinherit(context.Background(), tx, mv.Entry); err != nil {
			return err
		}
		return m.rf.Refold(context.Background(), tx, m.src, []domain.EntryID{mv.Entry, oldParent})
	})
	if err != nil {
		m.t.Fatalf("move %q -> %q: %v", from, join(to, name), err)
	}
	return true
}

// intentRows reads the owner's intent and the content rows of src by entry
// ID: own decisions, tags, overrides, and the file_content and archives
// rows.
func intentRows(t *testing.T, e *env, src domain.SourceID) map[string]string {
	t.Helper()
	out := map[string]string{}
	for what, q := range map[string]string{
		"decision": `SELECT id, decision FROM entries WHERE source_id = ? AND decision IS NOT NULL`,
		"tag":      `SELECT t.entry_id, group_concat(t.tag_id) FROM entry_tags t JOIN entries e ON e.id = t.entry_id WHERE e.source_id = ? GROUP BY 1`,
		"override": `SELECT o.entry_id, coalesce(o.category, '') || '/' || coalesce(o.group_mark, '') FROM entry_overrides o JOIN entries e ON e.id = o.entry_id WHERE e.source_id = ?`,
		"content":  `SELECT entry_id, state || '/' || coalesce(content_id, '') FROM file_content WHERE source_id = ?`,
		"archive":  `SELECT a.entry_id, a.format || '/' || a.state FROM archives a JOIN entries e ON e.id = a.entry_id WHERE e.source_id = ?`,
	} {
		rows, err := e.st.Reader().Query(q, string(src))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			var v string
			if err := rows.Scan(&id, &v); err != nil {
				t.Fatal(err)
			}
			out[fmt.Sprintf("%s %d", what, id)] = v
		}
		rows.Close()
	}
	return out
}

// checkEffective fails on any entry of rows whose effective decision is not
// its nearest own decision above it, or the default.
func checkEffective(t *testing.T, rows map[string]entry) {
	t.Helper()
	byID := map[int64]entry{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	for p, r := range rows {
		eff, from := string(domain.DecisionUndecided), int64(0)
		for a := r; ; a = byID[a.Parent.Int64] {
			if a.Decision.Valid {
				eff, from = a.Decision.String, a.ID
				break
			}
			if !a.Parent.Valid {
				break
			}
		}
		if r.EffDecision != eff || r.EffFrom.Int64 != from {
			t.Errorf("%q: effective %q from %d, want %q from %d", p, r.EffDecision, r.EffFrom.Int64, eff, from)
		}
	}
}

// checkTree fails on any row whose path is not its parent's path and its
// name.
func checkTree(t *testing.T, rows map[string]entry) {
	t.Helper()
	byID := map[int64]entry{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	for p, r := range rows {
		if p == "" {
			continue
		}
		if want := join(byID[r.Parent.Int64].Path, r.Name); p != want {
			t.Errorf("%q has parent %q and name %q", p, byID[r.Parent.Int64].Path, r.Name)
		}
	}
}

// diffRows reports the paths whose rows differ between two reads.
func diffRows(before, after map[string]entry) []string {
	var out []string
	for p, b := range before {
		if a, ok := after[p]; !ok {
			out = append(out, fmt.Sprintf("%q gone", p))
		} else if a != b {
			out = append(out, fmt.Sprintf("%q:\n before %+v\n after  %+v", p, b, a))
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			out = append(out, fmt.Sprintf("%q added", p))
		}
	}
	sort.Strings(out)
	return out
}

// rescanWritesNothing rescans src and fails unless the scan adds, updates,
// deletes, or marks missing no entries or dir_stats row, and leaves every
// row and its name index row as they were.
func rescanWritesNothing(t *testing.T, e *env, src domain.SourceID) map[string]entry {
	t.Helper()
	before := e.entries(src)
	writes := e.writes()
	rt := e.scan(src)
	after := e.entries(src)
	if n := writes(); n != 0 || rt.progress[ProgressMissing] != 0 || rt.progress[ProgressWritten] != 0 {
		t.Errorf("the rescan wrote %d rows (%d counted, %d missing)", n, rt.progress[ProgressWritten], rt.progress[ProgressMissing])
	}
	if d := diffRows(before, after); len(d) > 0 {
		t.Errorf("the rescan changed %d rows:\n%s", len(d), strings.Join(d[:min(len(d), 8)], "\n"))
	}
	return after
}

// giveIntent gives some entries of src the owner's intent and content rows,
// deterministically by path: own decisions, tags, overrides (applied by a
// rescan), and file_content and archives rows matching the files as
// indexed. It returns the moved-entry candidates it touched.
func giveIntent(t *testing.T, e *env, src domain.SourceID) {
	t.Helper()
	rows := e.entries(src)
	paths := make([]string, 0, len(rows))
	for p := range rows {
		if p != "" {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	ctx := context.Background()
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO tags (id, name, created_at) VALUES (1, 'scan', 0), (2, 'family', 0)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO contents (id, sha256, size) VALUES (1, zeroblob(32), 1)`); err != nil {
			return err
		}
		ds := []domain.Decision{domain.DecisionKeep, domain.DecisionDiscard, domain.DecisionLater}
		var decided []domain.EntryID
		for i, p := range paths {
			r := rows[p]
			id := domain.EntryID(r.ID)
			if i%9 == 4 {
				if _, err := tx.Exec(`UPDATE entries SET decision = ?, decision_at = 0 WHERE id = ?`,
					string(ds[(i/9)%3]), r.ID); err != nil {
					return err
				}
				decided = append(decided, id)
			}
			if i%7 == 2 {
				if _, err := tx.Exec(`INSERT INTO entry_tags (entry_id, tag_id, added_at) VALUES (?, ?, 0)`,
					r.ID, 1+i%2); err != nil {
					return err
				}
			}
			if i%13 == 6 {
				q := `INSERT INTO entry_overrides (entry_id, category, updated_at) VALUES (?, 'documents', 0)`
				if r.Kind == "directory" {
					q = `INSERT INTO entry_overrides (entry_id, group_mark, updated_at) VALUES (?, 1, 0)`
				}
				if _, err := tx.Exec(q, r.ID); err != nil {
					return err
				}
			}
			if r.Kind == "file" && r.Size > 0 && i%3 == 0 {
				if _, err := tx.Exec(`INSERT INTO file_content (entry_id, source_id, state, size, mtime_ns, ctime_ns,
					ino, content_id, checked_at) SELECT id, source_id, 'hashed', size, mtime_ns, ctime_ns, ino, 1, 0
					FROM entries WHERE id = ?`, r.ID); err != nil {
					return err
				}
				if i%2 == 0 {
					if _, err := tx.Exec(`INSERT INTO archives (entry_id, format, state, size, mtime_ns, ctime_ns, ino)
						SELECT id, 'zip', 'complete', size, mtime_ns, ctime_ns, ino FROM entries WHERE id = ?`, r.ID); err != nil {
						return err
					}
				}
			}
		}
		// Parents before children: each pass leaves the cut points below.
		for _, id := range decided {
			if err := decisions.Reinherit(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.scan(src) // applies the overrides
	checkEffective(t, e.entries(src))
}

// interesting are names that change what the rules see.
var interesting = []string{"node_modules", "Thumbs.db", "Meus documentos", ".git", "jogo.bin", "jogo.cue",
	"build", "Temp", "desktop.ini", "relatorio.doc", "caf\xe9", "setup.exe", "IMG_0001.JPG", "cache"}

// Task 2.2, R3.5 at the index level: random renames and moves of files and
// folders of the corpus, each mirrored on disk and followed by MoveEntry,
// Reinherit, and Refold with the post-step facts, leave the index exactly as
// a scan writes it: a full rescan afterwards adds no entry, marks none
// missing, and writes no row, aggregate, classification, dir_stats, or name
// index change. Moved entries keep their IDs, own decisions, tags,
// overrides, and content rows, and their effective decisions come from
// their new folders.
func TestR3_5RescanAfterMovesWritesNothing(t *testing.T) {
	e := newEnv(t)
	root, _ := corpus.BuildSynth(e.sfs, "/corpus", corpus.Corpus())
	e.addSource("corpus", "/corpus", root, writable)
	e.scan("corpus")
	giveIntent(t, e, "corpus")
	start := rescanWritesNothing(t, e, "corpus")
	intent := intentRows(t, e, "corpus")
	m := newMover(t, e, "corpus", "/corpus")

	rows := start
	moved := map[int64]bool{}
	var folderMoves, renames int
	step := func(from, to, name string) {
		t.Helper()
		r := get(t, rows, from)
		if m.move(rows, from, to, name) {
			moved[r.ID] = true
			if r.Kind == "directory" {
				folderMoves++
			}
			if r.Name != name {
				renames++
			}
			rows = e.entries("corpus")
			if got := get(t, rows, join(to, name)); got.ID != r.ID {
				t.Fatalf("%q is entry %d after the move, want %d", join(to, name), got.ID, r.ID)
			}
		}
	}

	// A disk image pair coming together in one folder and parting again
	// (the .bin's kind depends on its sibling .cue).
	var files, folders []string
	for p, r := range rows {
		switch {
		case r.Kind == "file":
			files = append(files, p)
		case r.Kind == "directory" && p != "" && r.State == "present" && !r.Boundary:
			folders = append(folders, p)
		}
	}
	slices.Sort(files)
	slices.Sort(folders)
	if len(files) < 3 || len(folders) < 3 {
		t.Fatalf("corpus too small: %d files, %d folders", len(files), len(folders))
	}
	pairDir := folders[len(folders)/2]
	step(files[0], pairDir, "jogo.bin")
	step(files[1], pairDir, "jogo.cue")
	if get(t, rows, join(pairDir, "jogo.bin")).FileKind.String == "other" {
		t.Errorf("jogo.bin beside jogo.cue is of kind other")
	}
	step(join(pairDir, "jogo.cue"), "", "jogo.cue")

	rng := rand.New(rand.NewPCG(3, 5))
	const steps = 60
	done := 0
	for try := 0; done < steps && try < steps*20; try++ {
		var all, dirs []string
		for p, r := range rows {
			if p == "" || r.State != "present" && r.State != "unreadable" || r.Boundary || r.Mounts.Int64 > 0 {
				continue
			}
			all = append(all, p)
			if r.Kind == "directory" && r.State == "present" {
				dirs = append(dirs, p)
			}
		}
		dirs = append(dirs, "")
		slices.Sort(all)
		slices.Sort(dirs)
		from := all[rng.IntN(len(all))]
		to := dirs[rng.IntN(len(dirs))]
		_, name := splitPath(from)
		if rng.IntN(3) == 0 {
			name = interesting[rng.IntN(len(interesting))]
		}
		if to == from || strings.HasPrefix(to, from+"/") {
			continue
		}
		if _, taken := rows[join(to, name)]; taken {
			continue
		}
		step(from, to, name)
		done++
	}
	if done < steps {
		t.Fatalf("only %d of %d moves done", done, steps)
	}

	after := rescanWritesNothing(t, e, "corpus")
	checkTree(t, after)
	checkEffective(t, after)
	ids := func(rows map[string]entry) []int64 {
		var out []int64
		for _, r := range rows {
			out = append(out, r.ID)
		}
		slices.Sort(out)
		return out
	}
	if !slices.Equal(ids(start), ids(after)) {
		t.Errorf("the moves changed the set of entry IDs")
	}
	for p, r := range after {
		if r.State == "missing" {
			t.Errorf("%q is missing", p)
		}
	}
	if got := intentRows(t, e, "corpus"); fmt.Sprint(got) != fmt.Sprint(intent) {
		t.Errorf("own decisions, tags, overrides, or content rows changed:\n before %v\n after  %v", intent, got)
	}
	// The moved files' content rows took their new change time.
	var stale int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM file_content f JOIN entries e ON e.id = f.entry_id
		WHERE f.ctime_ns IS NOT e.ctime_ns`).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("%d file_content rows have another change time than their entry (%v)", stale, err)
	}
	if len(moved) < steps/2 || folderMoves < 5 || renames < 5 {
		t.Errorf("only %d entries moved, %d folder moves, %d renames", len(moved), folderMoves, renames)
	}
}

// missingFixture is a scanned disk where "Old" (a folder with b.txt) and
// "c.txt" went missing.
func missingFixture(t *testing.T) (*env, *mover, map[string]entry) {
	t.Helper()
	e := newEnv(t)
	root := e.disk("disk", "/disk", writable)
	root.Dir("Fotos").File("a.jpg", 10, testNow)
	root.Dir("Old").File("b.txt", 20, testNow)
	root.File("c.txt", 30, testNow)
	root.File("d.txt", 40, testNow)
	e.scan("disk")
	root.Remove("Old")
	root.Remove("c.txt")
	e.scan("disk")
	rows := e.entries("disk")
	for _, p := range []string{"Old", "Old/b.txt", "c.txt"} {
		if get(t, rows, p).State != "missing" {
			t.Fatalf("%q is not missing", p)
		}
	}
	return e, newMover(t, e, "disk", "/disk"), rows
}

func missingIntent(t *testing.T, e *env, path string) bool {
	t.Helper()
	got, err := MissingIntentAt(context.Background(), e.st.Reader(), "disk", []byte(path))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Task 2.2: a missing row carrying the owner's intent (a tag, an own
// decision, or an override) at the destination, or below it, is reported by
// MissingIntentAt, and MoveEntry refuses the move with ErrMissingIntent,
// changing nothing; one without intent is deleted with its subtree and
// their name index rows.
func TestMoveOntoMissingEntries(t *testing.T) {
	e, m, rows := missingFixture(t)
	for _, p := range []string{"Old", "Old/b.txt", "c.txt", "Fotos", "nothing"} {
		if missingIntent(t, e, p) {
			t.Errorf("MissingIntentAt(%q) without intent = true", p)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.Writer().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO tags (id, name, created_at) VALUES (1, 'x', 0)`)
	b, c := get(t, rows, "Old/b.txt").ID, get(t, rows, "c.txt").ID
	for _, intent := range []struct {
		name       string
		set, unset string
	}{
		{"tag", `INSERT INTO entry_tags (entry_id, tag_id, added_at) VALUES (?, 1, 0)`, `DELETE FROM entry_tags WHERE entry_id = ?`},
		{"decision", `UPDATE entries SET decision = 'keep' WHERE id = ?`, `UPDATE entries SET decision = NULL WHERE id = ?`},
		{"override", `INSERT INTO entry_overrides (entry_id, category, updated_at) VALUES (?, 'documents', 0)`, `DELETE FROM entry_overrides WHERE entry_id = ?`},
	} {
		exec(intent.set, b)
		exec(intent.set, c)
		for _, p := range []string{"Old", "Old/b.txt", "c.txt"} {
			if !missingIntent(t, e, p) {
				t.Errorf("%s: MissingIntentAt(%q) = false", intent.name, p)
			}
		}
		if missingIntent(t, e, "Fotos") {
			t.Errorf("%s: MissingIntentAt of a present folder = true", intent.name)
		}
		// Refused, and nothing changes even when the caller commits.
		before := e.entries("disk")
		for _, mv := range []struct{ from, name string }{{"Fotos", "Old"}, {"d.txt", "c.txt"}} {
			err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
				_, _, err := MoveEntry(context.Background(), tx, Move{Source: "disk",
					Entry: domain.EntryID(get(t, rows, mv.from).ID), NewParent: domain.EntryID(get(t, rows, "").ID),
					NewName: []byte(mv.name)})
				if !errors.Is(err, ErrMissingIntent) {
					t.Errorf("%s: moving %q onto %q: %v, want ErrMissingIntent", intent.name, mv.from, mv.name, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if d := diffRows(before, e.entries("disk")); len(d) > 0 {
			t.Errorf("%s: a refused move changed rows:\n%s", intent.name, strings.Join(d, "\n"))
		}
		exec(intent.unset, b)
		exec(intent.unset, c)
	}

	// Without intent, the missing rows go with their name index rows.
	old := get(t, rows, "Old").ID
	if !m.move(rows, "Fotos", "", "Old") || !m.move(rows, "d.txt", "", "c.txt") {
		t.Fatal("the disk refused")
	}
	after := e.entries("disk")
	if got := get(t, after, "Old"); got.ID != get(t, rows, "Fotos").ID || got.State != "present" || got.FTS != 1 {
		t.Errorf("Old is %+v, want the moved Fotos", got)
	}
	if got := get(t, after, "c.txt"); got.ID != get(t, rows, "d.txt").ID || got.FTS != 1 {
		t.Errorf("c.txt is %+v, want the moved d.txt", got)
	}
	for _, id := range []int64{old, b, c} {
		var n int
		if err := e.st.Reader().QueryRow(`SELECT (SELECT count(*) FROM entries WHERE id = ?)
			+ (SELECT count(*) FROM entry_names WHERE rowid = ?)`, id, id).Scan(&n); err != nil || n != 0 {
			t.Errorf("entry %d left %d rows (%v)", id, n, err)
		}
	}
	if _, ok := after["Old/b.txt"]; ok {
		t.Errorf("the missing Old/b.txt is still indexed")
	}
	if got := get(t, after, "Old/a.jpg"); got.ID != get(t, rows, "Fotos/a.jpg").ID {
		t.Errorf("Old/a.jpg is entry %d, want %d", got.ID, get(t, rows, "Fotos/a.jpg").ID)
	}
	// The name index finds the moved entries by their new names.
	var found int64
	if err := e.st.Reader().QueryRow(`SELECT rowid FROM entry_names WHERE entry_names MATCH '"c.txt"'`).Scan(&found); err != nil || found != get(t, rows, "d.txt").ID {
		t.Errorf("c.txt finds entry %d (%v), want %d", found, err, get(t, rows, "d.txt").ID)
	}
	rescanWritesNothing(t, e, "disk")
}

// MoveEntry refuses a folder moved into itself or below itself, a move to
// another source, the root, a missing entry, a destination that is not a
// present folder, an invalid name, and a present entry at the new path,
// changing nothing.
func TestMoveEntryRefusals(t *testing.T) {
	e, _, rows := missingFixture(t)
	other := e.disk("other", "/other", writable)
	other.Dir("X")
	e.scan("other")
	x := domain.EntryID(get(t, e.entries("other"), "X").ID)
	root := domain.EntryID(get(t, rows, "").ID)
	id := func(p string) domain.EntryID { return domain.EntryID(get(t, rows, p).ID) }
	e.disk("deep", "/deep", writable).Dir("A").Dir("B")
	e.scan("deep")
	deep := e.entries("deep")
	for _, c := range []struct {
		what string
		m    Move
	}{
		{"into itself", Move{Source: "deep", Entry: domain.EntryID(get(t, deep, "A").ID), NewParent: domain.EntryID(get(t, deep, "A").ID), NewName: []byte("A")}},
		{"below itself", Move{Source: "deep", Entry: domain.EntryID(get(t, deep, "A").ID), NewParent: domain.EntryID(get(t, deep, "A/B").ID), NewName: []byte("A")}},
		{"another source", Move{Source: "disk", Entry: id("d.txt"), NewParent: x, NewName: []byte("d.txt")}},
		{"wrong source", Move{Source: "other", Entry: id("d.txt"), NewParent: x, NewName: []byte("d.txt")}},
		{"the root", Move{Source: "disk", Entry: root, NewParent: id("Fotos"), NewName: []byte("r")}},
		{"missing", Move{Source: "disk", Entry: id("c.txt"), NewParent: id("Fotos"), NewName: []byte("c.txt")}},
		{"into a file", Move{Source: "disk", Entry: id("d.txt"), NewParent: id("Fotos/a.jpg"), NewName: []byte("d.txt")}},
		{"into a missing folder", Move{Source: "disk", Entry: id("d.txt"), NewParent: id("Old"), NewName: []byte("d.txt")}},
		{"a slash", Move{Source: "disk", Entry: id("d.txt"), NewParent: root, NewName: []byte("a/b")}},
		{"dot dot", Move{Source: "disk", Entry: id("d.txt"), NewParent: root, NewName: []byte("..")}},
		{"same place", Move{Source: "disk", Entry: id("d.txt"), NewParent: root, NewName: []byte("d.txt")}},
		{"a present name", Move{Source: "disk", Entry: id("d.txt"), NewParent: root, NewName: []byte("Fotos")}},
	} {
		before := e.entries(c.m.Source)
		err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
			if _, _, err := MoveEntry(context.Background(), tx, c.m); err == nil {
				t.Errorf("%s: moved", c.what)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if d := diffRows(before, e.entries(c.m.Source)); len(d) > 0 {
			t.Errorf("%s: changed rows:\n%s", c.what, strings.Join(d, "\n"))
		}
	}
}

// A done mkdir and rmdir (r3 design D6): InsertFolder adds the empty folder
// with its name index row, the decision its parent passes on, and empty
// dir_stats, which Refold classifies; RemoveFolder takes it out again with
// the missing entries below it. A rescan after each adds and marks missing
// nothing, and changes only the new folder's own size facts, which a mkdir's
// facts do not carry. RemoveFolder refuses a folder holding a present entry
// or a missing one carrying the owner's intent, changing nothing.
func TestInsertAndRemoveFolder(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/disk", writable)
	root.Dir("Fotos").File("a.jpg", 10, testNow)
	e.scan("disk")
	if _, err := e.st.Writer().Exec(`UPDATE entries SET decision = 'keep', eff_decision = 'keep', eff_from = id
		WHERE source_id = 'disk' AND path = CAST('Fotos' AS BLOB)`); err != nil {
		t.Fatal(err)
	}
	rows := e.entries("disk")
	d := disk{t: t, e: e, root: "/disk"}
	rf := NewRefolder(rules.Default())
	fotos := domain.EntryID(get(t, rows, "Fotos").ID)

	dir := d.open("Fotos")
	w, _ := fsaccess.AsWriter(dir)
	if err := w.Mkdir([]byte("2024")); err != nil {
		t.Fatal(err)
	}
	dir.Close()
	var id domain.EntryID
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		if id, err = InsertFolder(context.Background(), tx, NewFolder{Source: "disk", Parent: fotos,
			Name: []byte("2024"), Facts: d.facts("Fotos/2024"), ParentFacts: d.facts("Fotos")}); err != nil {
			return err
		}
		return rf.Refold(context.Background(), tx, "disk", []domain.EntryID{id})
	})
	if err != nil {
		t.Fatal(err)
	}
	inserted := e.entries("disk")
	n := get(t, inserted, "Fotos/2024")
	if domain.EntryID(n.ID) != id || n.Kind != "directory" || n.State != "present" || n.FTS != 1 || !n.HasStats ||
		n.EffDecision != "keep" || n.EffFrom.Int64 != int64(fotos) || n.TotalFiles != 0 {
		t.Errorf("the new folder is %+v", n)
	}
	writes := e.writes()
	e.scan("disk")
	rescanned := e.entries("disk")
	if len(rescanned) != len(inserted) {
		t.Errorf("the rescan indexed %d entries, want %d", len(rescanned), len(inserted))
	}
	for p, b := range inserted {
		a := get(t, rescanned, p)
		if p == "Fotos/2024" {
			b.Size, b.Alloc, a.Size, a.Alloc = 0, sql.NullInt64{}, 0, sql.NullInt64{}
			a.ScanGen = b.ScanGen
		}
		if a != b {
			t.Errorf("the rescan changed %q:\n before %+v\n after  %+v", p, b, a)
		}
	}
	if n := writes(); n > 2 {
		t.Errorf("the rescan wrote %d rows, want at most the new folder's row and dir_stats", n)
	}

	// Refusals: a present entry below, a missing one with intent below.
	root.Child("Fotos").Child("2024").Dir("x")
	e.scan("disk")
	refused := func(what string, want error) {
		t.Helper()
		before := e.entries("disk")
		err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
			err := RemoveFolder(context.Background(), tx, id, PostFacts{})
			if err == nil || want != nil && !errors.Is(err, want) {
				t.Errorf("%s: RemoveFolder = %v, want %v", what, err, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if d := diffRows(before, e.entries("disk")); len(d) > 0 {
			t.Errorf("%s: changed rows:\n%s", what, strings.Join(d, "\n"))
		}
	}
	refused("a present child", nil)
	root.Child("Fotos").Child("2024").Remove("x")
	e.scan("disk")
	x := get(t, e.entries("disk"), "Fotos/2024/x")
	if x.State != "missing" {
		t.Fatalf("x is %s", x.State)
	}
	if _, err := e.st.Writer().Exec(`UPDATE entries SET decision = 'later' WHERE id = ?`, x.ID); err != nil {
		t.Fatal(err)
	}
	refused("a missing child with intent", ErrMissingIntent)
	if _, err := e.st.Writer().Exec(`UPDATE entries SET decision = NULL WHERE id = ?`, x.ID); err != nil {
		t.Fatal(err)
	}

	dir = d.open("Fotos")
	w, _ = fsaccess.AsWriter(dir)
	if err := w.Rmdir([]byte("2024")); err != nil {
		t.Fatal(err)
	}
	dir.Close()
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		if err := RemoveFolder(context.Background(), tx, id, d.facts("Fotos")); err != nil {
			return err
		}
		return rf.Refold(context.Background(), tx, "disk", []domain.EntryID{fotos})
	})
	if err != nil {
		t.Fatal(err)
	}
	removed := e.entries("disk")
	if _, ok := removed["Fotos/2024"]; ok {
		t.Errorf("the removed folder is still indexed")
	}
	for _, gone := range []int64{int64(id), x.ID} {
		var n int
		if err := e.st.Reader().QueryRow(`SELECT count(*) FROM entry_names WHERE rowid = ?`, gone).Scan(&n); err != nil || n != 0 {
			t.Errorf("entry %d keeps %d name rows (%v)", gone, n, err)
		}
	}
	rescanWritesNothing(t, e, "disk")
}

// D10: a scan given DeferWhile checks it before it opens its source, and
// waits 3 s with reason DeferOrganizing while it reports true.
func TestScanDefersWhileOrganizing(t *testing.T) {
	e := newEnv(t)
	e.disk("disk", "/disk", posix).File("a.txt", 1, testNow)
	busy := true
	var asked []domain.SourceID
	e.active = func(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error) {
		asked = append(asked, src)
		return busy, nil
	}
	err := e.scanWith(context.Background(), "disk", &fakeRuntime{})
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != DeferOrganizing || !d.Until.Equal(testNow.Add(organizingDelay)) {
		t.Fatalf("scan = %v, want a defer for organizing until %v", err, testNow.Add(organizingDelay))
	}
	if n := len(e.entries("disk")); n != 1 {
		t.Errorf("the deferred scan indexed %d entries", n)
	}
	busy = false
	e.scan("disk")
	if _, ok := e.entries("disk")["a.txt"]; !ok || len(asked) != 2 || asked[0] != "disk" {
		t.Errorf("the scan after organizing indexed %v, asked %v", e.entries("disk"), asked)
	}
	e.active = func(context.Context, store.Queryer, domain.SourceID) (bool, error) {
		return false, errors.New("boom")
	}
	if err := e.scanWith(context.Background(), "disk", &fakeRuntime{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failing check gives %v", err)
	}
}
