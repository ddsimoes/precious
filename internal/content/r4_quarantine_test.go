package content

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"testing"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/review"
	"precious/internal/rules"
)

// quarantine moves the entry at path of src into its source's quarantine
// as a cleanup plan does (r4 design D1, D2): with index.MoveEntry, into a
// new .precious-quarantine/1/<seq> under its own name, creating the folders
// it needs, then refolds. It returns the entry's new path.
func (e *env) quarantine(src domain.SourceID, path string) string {
	e.t.Helper()
	ctx := context.Background()
	var moved []byte
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		folder := func(parent int64, name string) (int64, error) {
			var id int64
			err := tx.QueryRow(`SELECT id FROM entries WHERE parent_id = ? AND name = ?`, parent, []byte(name)).Scan(&id)
			if !errors.Is(err, sql.ErrNoRows) {
				return id, err
			}
			created, err := index.InsertFolder(ctx, tx, index.NewFolder{Source: src, Parent: domain.EntryID(parent),
				Name: []byte(name)})
			return int64(created), err
		}
		var top int64
		if err := tx.QueryRow(`SELECT id FROM entries WHERE source_id = ? AND parent_id IS NULL`, string(src)).
			Scan(&top); err != nil {
			return err
		}
		q, err := folder(top, index.QuarantineName)
		if err != nil {
			return err
		}
		plan, err := folder(q, "1")
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM entries WHERE parent_id = ? AND kind = 'directory'`, plan).
			Scan(&n); err != nil {
			return err
		}
		seq, err := folder(plan, strconv.Itoa(n+1))
		if err != nil {
			return err
		}
		var id, parent int64
		var name []byte
		if err := tx.QueryRow(`SELECT id, parent_id, name FROM entries WHERE source_id = ? AND path = ?`,
			string(src), []byte(path)).Scan(&id, &parent, &name); err != nil {
			return err
		}
		if _, moved, err = index.MoveEntry(ctx, tx, index.Move{Source: src, Entry: domain.EntryID(id),
			NewParent: domain.EntryID(seq), NewName: name}); err != nil {
			return err
		}
		return index.NewRefolder(rules.Default()).Refold(ctx, tx, src, []domain.EntryID{domain.EntryID(seq),
			domain.EntryID(parent)})
	})
	if err != nil {
		e.t.Fatalf("quarantine %s:%q: %v", src, path, err)
	}
	if !index.IsQuarantinePath(moved) {
		e.t.Fatalf("%q moved to %q, outside the quarantine", path, moved)
	}
	return string(moved)
}

// copiesOf returns the refs content.Copies lists for ref, and its total.
func (e *env) copiesOf(ref domain.Ref) ([]domain.Ref, int) {
	e.t.Helper()
	cs, total, _, err := Copies(context.Background(), e.st.Reader(), ref, "", 100)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]domain.Ref, len(cs))
	for i, c := range cs {
		out[i] = c.Ref
	}
	return out, total
}

// relate runs one relate pass with the review lists' Refresh as its after
// hook, as the server wires them.
func (e *env) relate() {
	e.t.Helper()
	h := relations.NewHandler(e.st, fixedClock{testNow}, config.Defaults().Duplicates,
		func(ctx context.Context, gen int64) error { return review.Refresh(ctx, e.st, gen) })
	if err := h.Run(context.Background(), jobs.Job{Kind: relations.KindRelate}, &fakeRuntime{}); err != nil {
		e.t.Fatal(err)
	}
}

// sidePaths are the entry paths of the visible relations' sides, by
// relation ID.
func (e *env) sidePaths() map[int64][2]string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT r.id, ea.path, eb.path FROM relations r
		JOIN entries ea ON ea.id = r.a_entry JOIN entries eb ON eb.id = r.b_entry
		WHERE r.gen = (SELECT gen FROM review_state WHERE id = 1)`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64][2]string{}
	for rows.Next() {
		var id int64
		var a, b []byte
		if err := rows.Scan(&id, &a, &b); err != nil {
			e.t.Fatal(err)
		}
		out[id] = [2]string{string(a), string(b)}
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// duplicatesRows reads every row of the duplicates list, open and decided.
func (e *env) duplicatesRows() []review.Row {
	e.t.Helper()
	var out []review.Row
	for _, decided := range []bool{false, true} {
		cursor := ""
		for {
			p, err := review.Rows(context.Background(), e.st.Reader(), review.ListDuplicates, "", decided, cursor,
				review.MaxLimit)
			if err != nil {
				e.t.Fatal(err)
			}
			out = append(out, p.Items...)
			if cursor = p.NextCursor; cursor == "" {
				break
			}
		}
	}
	return out
}

// compared returns every file Compare lists for left against right, in any
// bucket, as refs and side-relative paths.
func (e *env) compared(left, right domain.Ref) (refs []domain.Ref, paths []string) {
	e.t.Helper()
	for _, b := range relations.Buckets {
		cursor := ""
		for {
			res, err := relations.Compare(context.Background(), e.st.Reader(), left, right, b, cursor, 1000)
			if err != nil {
				e.t.Fatal(err)
			}
			for _, it := range res.Items {
				for _, r := range []*domain.Ref{it.Left, it.Right, it.Twin} {
					if r != nil {
						refs = append(refs, *r)
					}
				}
				for _, p := range [][]byte{it.LeftPath, it.RightPath, it.TwinPath} {
					if p != nil {
						paths = append(paths, string(p))
					}
				}
			}
			if cursor = res.NextCursor; cursor == "" {
				break
			}
		}
	}
	return refs, paths
}

func under(p, dir string) bool {
	return p == dir || len(p) > len(dir) && p[len(dir)] == '/' && p[:len(dir)] == dir
}

// Scenario "Quarantining one of two copies" (r4 design D2, slice 2.2): on
// the corpus scanned and hashed, one of the two copies of a song moves into
// the quarantine. Then content.Copies of the other lists no copy, while the
// quarantined one still lists the staying copy; coverage no longer counts
// it; a relate pass with its review refresh leaves no relation with a side
// in the quarantine or in the folder it left, and no duplicates row for the
// pair's content; Compare of the corpus's top with another source's lists
// no quarantined file, while the quarantined folder still compares with its
// own file.
func TestQuarantiningOneOfTwoCopies(t *testing.T) {
	e := newEnv(t)
	buildCorpus(t, e)
	pen := e.disk("pen", "/mnt/pen", posix)
	pen.Dir("musicas").File("outra.mp3", 4321, fileTime)
	e.scan("pen")
	e.hash("corpus")
	const (
		kept    = "Downloads/mp3/Skank - Garota Nacional.mp3"
		moved   = "Musicas/Skank/Skank - Garota Nacional.mp3"
		movedIn = "Musicas/Skank"
		size    = 280000
	)
	keptRef, movedRef := domain.Ref{Entry: e.id("corpus", kept)}, domain.Ref{Entry: e.id("corpus", moved)}
	if got, total := e.copiesOf(keptRef); total != 1 || !slices.Equal(got, []domain.Ref{movedRef}) {
		t.Fatalf("before: copies of %q = %v (total %d); want the other copy", kept, got, total)
	}
	var contentID int64
	if err := e.st.Reader().QueryRow(`SELECT content_id FROM file_content WHERE entry_id = ?`,
		int64(keptRef.Entry)).Scan(&contentID); err != nil {
		t.Fatal(err)
	}
	covers := func(rows []review.Row, sides map[int64][2]string, path string) []review.Row {
		var out []review.Row
		for _, r := range rows {
			s, ok := sides[r.Relation]
			if r.Content == contentID || ok && (under(path, s[0]) || under(path, s[1])) {
				out = append(out, r)
			}
		}
		return out
	}
	e.relate()
	if got := covers(e.duplicatesRows(), e.sidePaths(), moved); len(got) == 0 {
		t.Fatalf("before: no duplicates row covers %q", moved)
	}
	corpusTop, penTop := domain.Ref{Entry: e.id("corpus", "")}, domain.Ref{Entry: e.id("pen", "")}
	if refs, _ := e.compared(corpusTop, penTop); !slices.Contains(refs, movedRef) {
		t.Fatalf("before: Compare of the tops does not list %q", moved)
	}
	before := e.coverage("corpus")

	newPath := e.quarantine("corpus", moved)

	if got, total := e.copiesOf(keptRef); total != 0 || len(got) != 0 {
		t.Errorf("copies of %q = %v (total %d); want none: its only copy is quarantined", kept, got, total)
	}
	if got, total := e.copiesOf(movedRef); total != 1 || !slices.Equal(got, []domain.Ref{keptRef}) {
		t.Errorf("copies of the quarantined copy = %v (total %d); want the staying copy", got, total)
	}
	e.hash("corpus") // plans again and checks the published coverage against coverageRows
	after := e.coverage("corpus")
	if after.CandidateFiles != before.CandidateFiles-1 || after.CandidateBytes != before.CandidateBytes-size ||
		after.CheckedFiles != before.CheckedFiles-1 {
		t.Errorf("coverage %+v after quarantining, %+v before; want one checked file of %d bytes less",
			after, before, size)
	}
	if s := e.state("corpus", kept); s != domain.ContentHashed {
		t.Errorf("the staying copy is %s, want hashed (it keeps its digest)", s)
	}

	e.relate()
	sides := e.sidePaths()
	for id, s := range sides {
		for _, p := range s {
			if index.IsQuarantinePath([]byte(p)) || p == movedIn {
				t.Errorf("relation %d has side %q after quarantining %q", id, p, moved)
			}
		}
	}
	for _, r := range e.duplicatesRows() {
		if r.Content == contentID {
			t.Errorf("the duplicates card still lists the pair's content: row %+v", r)
		}
		if r.Copy == movedRef {
			t.Errorf("duplicates row %d offers the quarantined copy", r.ID)
		}
	}
	if got := covers(e.duplicatesRows(), sides, newPath); len(got) != 0 {
		t.Errorf("duplicates rows %+v cover the quarantined copy", got)
	}
	refs, paths := e.compared(corpusTop, penTop)
	if slices.Contains(refs, movedRef) {
		t.Errorf("Compare of the tops lists the quarantined copy")
	}
	for _, p := range paths {
		if index.IsQuarantinePath([]byte(p)) {
			t.Errorf("Compare of the tops lists %q", p)
		}
	}
	seq := newPath[:len(newPath)-len("/Skank - Garota Nacional.mp3")]
	if refs, _ := e.compared(domain.Ref{Entry: e.id("corpus", seq)}, penTop); !slices.Contains(refs, movedRef) {
		t.Errorf("Compare of the quarantine folder %q lists %v; want its own file", seq, refs)
	}
}

// Hashing never enrolls or reads a quarantined file (r4 design D2): a file
// quarantined before planning gets no file_content row, and a pending file
// quarantined afterwards stays pending and unread; neither keeps its size
// group alive, and coverage counts neither.
func TestHashingSkipsTheQuarantine(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/mnt/disk", posix)
	d := root.Dir("d")
	pairOf(d, 5000, "a.bin", "b.bin")  // a is quarantined while pending
	pairOf(d, 7000, "c.bin", "dd.bin") // c is quarantined before planning
	d.File("f.bin", 3000, fileTime).Seed(9)
	d.File("g.bin", 3000, fileTime).Seed(9)
	e.scan("disk")
	c := e.quarantine("disk", "d/c.bin")
	e.plan()
	if s := e.state("disk", "d/a.bin"); s != domain.ContentPending {
		t.Fatalf("a.bin is %q after planning, want pending", s)
	}
	if s := e.state("disk", c); s != "" {
		t.Errorf("the file quarantined before planning was enrolled: %s", s)
	}
	a := e.quarantine("disk", "d/a.bin")
	e.rec.Reset()
	e.hash("disk")
	opened := e.opened()
	for _, p := range []string{a, c} {
		if slices.Contains(opened, p) {
			t.Errorf("hashing opened the quarantined %q", p)
		}
	}
	if s := e.state("disk", a); s != domain.ContentPending {
		t.Errorf("the quarantined pending file is %q, want pending", s)
	}
	if s := e.state("disk", c); s != "" {
		t.Errorf("the file quarantined before planning is %q, want no row", s)
	}
	for _, p := range []string{"d/b.bin", "d/dd.bin"} {
		if s := e.state("disk", p); s != domain.ContentUniqueSize {
			t.Errorf("%s is %s, want unique_size: its size partner is quarantined", p, s)
		}
		if slices.Contains(opened, p) {
			t.Errorf("hashing read %s, alone in its size outside the quarantine", p)
		}
	}
	for _, p := range []string{"d/f.bin", "d/g.bin"} {
		if s := e.state("disk", p); s != domain.ContentHashed {
			t.Errorf("%s is %s, want hashed", p, s)
		}
	}
	if cov := e.coverage("disk"); cov.CandidateFiles != 2 || cov.CheckedFiles != 2 || cov.UncheckedFiles != 0 ||
		cov.CandidateBytes != 6000 {
		t.Errorf("coverage %+v; want the two hashed files only, the quarantined pending file left out", cov)
	}
}
