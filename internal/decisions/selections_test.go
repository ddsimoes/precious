package decisions

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/search"
)

func TestSelectionCounts(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	e.decide(t, one(s.ID("Fotos/2006/natal"), keep))
	e.decide(t, one(s.ID("Fotos/IMG_0042.JPG"), keep))
	sel := e.createSelection(t, search.Query{Within: ptr(s.ID("Fotos")), Ext: []string{"jpg"}})
	// The six photos under Fotos; IMG_0001.JPG under natal and IMG_0042.JPG
	// are kept.
	want := Selection{Count: 5, Bytes: 10 + 11 + 12 + 13 + 14, KeptCount: 2, KeptBytes: 13 + 14,
		CreatedAt: start, ExpiresAt: start.Add(time.Hour)}
	if sel.ID == "" || len(sel.ID) < 20 {
		t.Errorf("selection id %q, want a random token", sel.ID)
	}
	sel.ID = ""
	if sel != want {
		t.Errorf("selection %+v, want %+v", sel, want)
	}

	var count, bytes, kept, created, expires int64
	var query string
	if err := e.st.Reader().QueryRow(`SELECT query, count, bytes, kept, created_at, expires_at FROM selections`).
		Scan(&query, &count, &bytes, &kept, &created, &expires); err != nil {
		t.Fatal(err)
	}
	if count != 5 || bytes != want.Bytes || kept != 2 || created != clock.Millis(start) || expires != clock.Millis(start.Add(time.Hour)) {
		t.Errorf("stored selection %d %d %d %d %d", count, bytes, kept, created, expires)
	}
	if wantQuery := `{"ext":["jpg"],"within":"` + s.ID("Fotos").String() + `"}`; query != wantQuery {
		t.Errorf("stored query %s, want %s", query, wantQuery)
	}

	if _, err := e.trySelection(search.Query{FileKinds: []domain.FileKind{"pictures"}}); domain.CodeOf(err) != domain.CodeInvalidRequest {
		t.Errorf("a bad query: %v, want invalid_request", err)
	}
}

// A folder selected together with entries inside it counts its bytes once,
// including folders whose subtrees sort out of name order ("jpg!/" before
// "jpg/"); kept bytes count a kept entry once too, but do count a kept
// entry inside a selected folder that is not kept.
func TestSelectionBytesCountedOnce(t *testing.T) {
	e := newEnv(t)
	s := e.seed(t,
		file("jpgs/a.jpg", 10), file("jpgs/b.jpg", 20), file("jpgs/c.png", 40), file("x.jpg", 5),
		file("jpg!/k.jpg", 3), file("jpg/m.jpg", 7), file("outro.png", 100),
	)
	sel := e.createSelection(t, search.Query{Source: s.Source, Name: "jpg"})
	// jpgs, a.jpg, b.jpg, x.jpg, jpg!, k.jpg, jpg, m.jpg.
	if want := (Selection{Count: 8, Bytes: 70 + 5 + 3 + 7}); sel.Count != want.Count || sel.Bytes != want.Bytes || sel.KeptCount != 0 || sel.KeptBytes != 0 {
		t.Errorf("selection count %d bytes %d kept %d/%d, want %d and %d, none kept",
			sel.Count, sel.Bytes, sel.KeptCount, sel.KeptBytes, want.Count, want.Bytes)
	}
	var stored int64
	if err := e.st.Reader().QueryRow(`SELECT bytes FROM selections WHERE id = ?`, sel.ID).Scan(&stored); err != nil || stored != sel.Bytes {
		t.Errorf("stored bytes %d (%v), want %d", stored, err, sel.Bytes)
	}

	e.decide(t, one(s.ID("jpgs"), keep))
	e.decide(t, one(s.ID("jpg/m.jpg"), keep))
	sel = e.createSelection(t, search.Query{Source: s.Source, Name: "jpg"})
	// Kept: jpgs, a.jpg, b.jpg (inherited), m.jpg; bytes jpgs + m.jpg.
	if sel.Bytes != 85 || sel.KeptCount != 4 || sel.KeptBytes != 70+7 {
		t.Errorf("selection bytes %d kept %d/%d, want 85 and 4/77", sel.Bytes, sel.KeptCount, sel.KeptBytes)
	}

	// The root selected with everything counts the source once.
	sel = e.createSelection(t, search.Query{Source: s.Source})
	if sel.Bytes != 10+20+40+5+3+7+100 {
		t.Errorf("whole-source selection bytes %d, want %d", sel.Bytes, 10+20+40+5+3+7+100)
	}
}

func (e *env) trySelection(q search.Query) (Selection, error) {
	var sel Selection
	err := e.write(func(tx *sql.Tx) error {
		var err error
		sel, err = e.svc.CreateSelection(reqCtx(), tx, q)
		return err
	})
	return sel, err
}

func TestSelectionExpires(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	sel := e.createSelection(t, search.Query{Within: ptr(s.ID("Fotos"))})
	tag := e.createTag(t, "familia")

	e.clk.Advance(SelectionTTL - time.Millisecond)
	if _, err := e.setDecision(SetDecision{SelectionID: sel.ID, Decision: later}); err != nil {
		t.Fatalf("a selection just before its expiry: %v", err)
	}
	e.decide(t, SetDecision{SelectionID: sel.ID, Inherit: true})
	before := rows(t, e.st)
	audited := len(audits(t, e.st))

	e.clk.Advance(time.Millisecond)
	_, err := e.setDecision(SetDecision{SelectionID: sel.ID, Decision: discard})
	wantCode(t, err, domain.CodeSelectionExpired)
	_, err = e.trySetTags(SetTags{SelectionID: sel.ID, Add: []int64{tag.ID}})
	wantCode(t, err, domain.CodeSelectionExpired)
	if !mapsEqual(before, rows(t, e.st)) || e.tagCount(t, `SELECT count(*) FROM entry_tags`) != 0 {
		t.Error("a request naming an expired selection changed entries")
	}
	if n := len(audits(t, e.st)); n != audited {
		t.Errorf("refused requests wrote %d audit events", n-audited)
	}
}

// Expired selections lose their entries at the next create-selection, and
// their rows a day later; until then a request naming one still answers
// selection_expired.
func TestSelectionPruning(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	old := e.createSelection(t, search.Query{Source: s.Source})
	e.clk.Advance(SelectionTTL)
	fresh := e.createSelection(t, search.Query{Source: s.Source})
	if n := e.tagCount(t, `SELECT count(*) FROM selection_entries WHERE selection_id = ?`, old.ID); n != 0 {
		t.Errorf("an expired selection keeps %d entries", n)
	}
	if n := e.tagCount(t, `SELECT count(*) FROM selection_entries WHERE selection_id = ?`, fresh.ID); int64(n) != fresh.Count || n == 0 {
		t.Errorf("the new selection has %d entries, want %d", n, fresh.Count)
	}
	_, err := e.setDecision(SetDecision{SelectionID: old.ID, Decision: discard})
	wantCode(t, err, domain.CodeSelectionExpired)

	e.clk.Advance(24 * time.Hour)
	e.createSelection(t, search.Query{Source: s.Source})
	if n := e.tagCount(t, `SELECT count(*) FROM selections WHERE id IN (?, ?)`, old.ID, fresh.ID); n != 1 {
		t.Errorf("%d of the two expired selections remain, want only the one expired less than a day ago", n)
	}
	_, err = e.setDecision(SetDecision{SelectionID: old.ID, Decision: discard})
	wantCode(t, err, domain.CodeNotFound)
}

// Entries indexed after a selection was created are not in it.
func TestSelectionExcludesLaterEntries(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	backup := "HD antigo/Backup_PC_2004"
	sel := e.createSelection(t, search.Query{Within: ptr(s.ID(backup))})

	// A scan batch inserts z.tmp, inheriting from its folder.
	var z int64
	if err := e.write(func(tx *sql.Tx) error {
		eff, from, err := InheritFrom(context.Background(), tx, s.ID(backup))
		if err != nil {
			return err
		}
		r, err := tx.Exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, size, total_bytes, total_files,
			state, first_seen, last_seen, scan_gen, eff_decision, eff_from)
			VALUES (?, ?, ?, ?, 'file', 5, 5, 1, 'present', 0, 0, 1, ?, ?)`,
			string(s.Source), int64(s.ID(backup)), []byte("z.tmp"), []byte(backup+"/z.tmp"), string(eff), nullID(from))
		if err != nil {
			return err
		}
		z, err = r.LastInsertId()
		return err
	}); err != nil {
		t.Fatal(err)
	}

	res := e.decide(t, SetDecision{SelectionID: sel.ID, Decision: discard})
	if int64(res.Applied) != sel.Count {
		t.Errorf("applied %d, want the selection's %d", res.Applied, sel.Count)
	}
	in := e.intent(t, domain.EntryID(z))
	if in.Own != inherit || in.Effective != undecided {
		t.Errorf("z.tmp reads own %q effective %q, want no decision from the request", in.Own, in.Effective)
	}
	wantEffective(t, e, s, backup+"/novo.tmp", discard, discard, ptr(backup+"/novo.tmp"))
	checkConsistent(t, e.st)
}

// A selection naming an entry that no longer exists (its source was
// removed) fails whole with not_found.
func TestSelectionWithRemovedEntry(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	sel := e.createSelection(t, search.Query{Within: ptr(s.ID("Fotos"))})
	if _, err := e.st.Writer().Exec(`DELETE FROM entries WHERE id = ?`, int64(s.ID("Fotos/IMG_0042.JPG"))); err != nil {
		t.Fatal(err)
	}
	before := rows(t, e.st)
	_, err := e.setDecision(SetDecision{SelectionID: sel.ID, Decision: discard})
	wantCode(t, err, domain.CodeNotFound)
	if !mapsEqual(before, rows(t, e.st)) {
		t.Error("the refused request changed entries")
	}
}
