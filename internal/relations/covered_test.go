package relations

import (
	"context"
	"database/sql"
	"testing"

	"precious/internal/domain"
	"precious/internal/store"
)

// countingQueryer counts the statements run through it.
type countingQueryer struct {
	q store.Queryer
	n int
}

func (c *countingQueryer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.n++
	return c.q.QueryRowContext(ctx, query, args...)
}

func (c *countingQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.n++
	return c.q.QueryContext(ctx, query, args...)
}

// CoveredBy picks, per ref, the current generation's same relation with
// the ref on either side or inside relation with the ref as side a, never
// an overlap, by most matched bytes, then lowest ID; a member folder is
// matched by its member ref, a whole archive by its entry. One statement
// serves every ref.
func TestCoveredBy(t *testing.T) {
	w := newWorld(t)
	for _, p := range []string{"x", "y", "z", "v", "u", "t"} {
		w.file(p+"/f.txt", 10, "")
	}
	w.archive(wf{path: "bkp.zip", size: 700}, mem("home/a.txt", 100, ""))
	w.seed()
	if _, err := w.st.Writer().Exec(`UPDATE review_state SET gen = 3 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	rel := func(gen int64, kind string, a, b domain.Ref, matched int64) int64 {
		t.Helper()
		var am, bm any
		if a.IsMember() {
			am = int64(a.Member)
		}
		if b.IsMember() {
			bm = int64(b.Member)
		}
		var id int64
		if err := w.st.Writer().QueryRow(`INSERT INTO relations (gen, kind, a_entry, a_member, b_entry, b_member,
				matched_bytes, redundant_bytes, a_bytes, a_files, b_bytes, b_files,
				a_only_files, a_only_bytes, b_only_files, b_only_bytes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 1, 2, 3, 4) RETURNING id`,
			gen, kind, int64(a.Entry), am, int64(b.Entry), bm, matched, matched/2).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	x, y, z, v, u, tt := w.ref("x"), w.ref("y"), w.ref("z"), w.ref("v"), w.ref("u"), w.ref("t")
	zip, home := w.ref("bkp.zip"), w.ref("bkp.zip!home")
	sameXY := rel(3, KindSame, x, y, 100)
	rel(3, KindSame, tt, x, 60)              // a lower ID than sameZipU, fewer bytes
	rel(3, KindInside, x, z, 50)             // x inside z, less than sameXY
	insideZ := rel(3, KindInside, z, v, 500) // z inside v
	rel(3, KindOverlap, y, x, 1000)          // never
	rel(3, KindOverlap, x, z, 1000)          // never
	rel(2, KindSame, z, y, 9000)             // an old generation
	rel(4, KindSame, tt, y, 9000)            // not visible yet
	tieLow := rel(3, KindSame, u, y, 70)
	rel(3, KindSame, x, u, 70)
	insideHome := rel(3, KindInside, home, v, 100)
	sameZipU := rel(3, KindSame, zip, tt, 80)

	refs := []domain.Ref{x, y, z, v, u, tt, zip, {Member: home.Member}}
	cq := &countingQueryer{q: w.st.Reader()}
	got, err := CoveredBy(context.Background(), cq, refs)
	if err != nil {
		t.Fatal(err)
	}
	if cq.n != 1 {
		t.Errorf("%d statements for %d refs, want 1", cq.n, len(refs))
	}
	want := map[domain.Ref]int64{
		x: sameXY, // same as side a; the overlaps' 1000 bytes never count
		y: sameXY, // same as side b, over the old generation's 9000
		z: insideZ,
		// v contains z and home, never covered by them; nothing else.
		u:                     tieLow,   // two of 70 bytes: the lower ID
		tt:                    sameZipU, // most bytes, over a lower ID
		zip:                   sameZipU, // not the member folder's inside
		{Member: home.Member}: insideHome,
	}
	for ref, id := range want {
		if r, ok := got[ref]; !ok || r.ID != id {
			t.Errorf("CoveredBy(%s) = %+v (%v), want relation %d", ref, r, ok, id)
		}
	}
	if r, ok := got[v]; ok {
		t.Errorf("v, a container only, is covered by %+v", r)
	}
	if len(got) != len(want) {
		t.Errorf("%d refs answered, want %d", len(got), len(want))
	}
	if r := got[y]; r.A != x || r.B != y || r.Kind != KindSame || r.MatchedBytes != 100 || r.RedundantBytes != 50 ||
		r.AOnlyFiles != 1 || r.AOnlyBytes != 2 || r.BOnlyFiles != 3 || r.BOnlyBytes != 4 {
		t.Errorf("relation %+v is not as stored", r)
	}
	if got, err := CoveredBy(context.Background(), w.st.Reader(), nil); err != nil || len(got) != 0 {
		t.Errorf("no refs: %v, %v", got, err)
	}
}

// After a relate pass, a whole archive the same as a folder is covered by
// that relation, from either side, and a second pass's generation replaces
// the first's.
func TestCoveredByAfterRelate(t *testing.T) {
	w := newWorld(t)
	w.file("x/a.txt", 100, "a")
	w.file("x/b.txt", 200, "b")
	w.archive(wf{path: "z/bkp.zip", size: 250}, mem("a.txt", 100, "a"), mem("b.txt", 200, "b"))
	h := w.handler(nil)
	for range 2 {
		runJob(t, h)
		rels := visible(t, w.st)
		if len(rels) != 1 {
			t.Fatalf("relations %+v", rels)
		}
		got, err := CoveredBy(context.Background(), w.st.Reader(), []domain.Ref{w.ref("x"), w.ref("z/bkp.zip")})
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range []domain.Ref{w.ref("x"), w.ref("z/bkp.zip")} {
			if r, ok := got[ref]; !ok || r.ID != rels[0].ID || r.Kind != KindSame || r.MatchedBytes != 300 {
				t.Errorf("CoveredBy(%s) = %+v (%v), want %+v", ref, r, ok, rels[0])
			}
		}
	}
}
