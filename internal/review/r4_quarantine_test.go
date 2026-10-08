package review

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/rules"
)

// quarantine moves entry id of src into its source's quarantine as a
// cleanup plan does (r4 design D1, D2): with index.MoveEntry, into a new
// .precious-quarantine/1/<seq> under its own name, creating the folders it
// needs, then refolds.
func (w *world) quarantine(src domain.SourceID, id domain.EntryID) {
	w.t.Helper()
	ctx := context.Background()
	err := w.st.Write(ctx, func(tx *sql.Tx) error {
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
		if err := tx.QueryRow(`SELECT count(*) FROM entries WHERE parent_id = ?`, plan).Scan(&n); err != nil {
			return err
		}
		seq, err := folder(plan, strconv.Itoa(n+1))
		if err != nil {
			return err
		}
		var parent int64
		var name []byte
		if err := tx.QueryRow(`SELECT parent_id, name FROM entries WHERE id = ?`, int64(id)).Scan(&parent, &name); err != nil {
			return err
		}
		if _, _, err := index.MoveEntry(ctx, tx, index.Move{Source: src, Entry: id, NewParent: domain.EntryID(seq),
			NewName: name}); err != nil {
			return err
		}
		return index.NewRefolder(rules.Default()).Refold(ctx, tx, src, []domain.EntryID{domain.EntryID(seq),
			domain.EntryID(parent)})
	})
	if err != nil {
		w.t.Fatalf("quarantine entry %d: %v", id, err)
	}
	if !index.IsQuarantinePath([]byte(w.path(id))) {
		w.t.Fatalf("entry %d moved to %q, outside the quarantine", id, w.path(id))
	}
}

// Review cards leave the quarantine out (r4 design D2): after a refresh, a
// quarantined Thumbs.db is no system junk row, its duplicate pair is no
// duplicates row, a relation with a quarantined side is neither a
// duplicates nor an unpacked-archives row, and a quarantined programs group
// takes its rescue row along. Before the refresh, the pair's row is no
// longer open, and never offers the quarantined copy.
func TestCardsLeaveTheQuarantineOut(t *testing.T) {
	w := newCorpusWorld(t)
	const (
		thumbs     = "Fotos/2006/Praia/Thumbs.db"
		thumbsCopy = "Fotos - Copia/2006/Praia/Thumbs.db"
		zip        = "Downloads/fotos_2005_do_pendrive.zip"
		unpacked   = "Downloads/fotos_2005_do_pendrive"
	)
	// Quarantine the copy content rows' Copy would name: the lower ID.
	junk, staying := w.corpus.ID(thumbs), w.corpus.ID(thumbsCopy)
	if staying < junk {
		junk, staying = staying, junk
	}
	var pair int64
	if err := w.st.Reader().QueryRow(`SELECT content_id FROM file_content WHERE entry_id = ?`, int64(junk)).
		Scan(&pair); err != nil {
		t.Fatal(err)
	}
	group, rescued := w.corpus.ID(office), w.corpus.ID(orcamento)
	rel := w.relation("same", w.corpus.ID(zip), w.corpus.ID(unpacked))
	w.refresh()

	entryRows := func(list List, id domain.EntryID) int {
		n := 0
		for _, decided := range []bool{false, true} {
			for _, r := range w.all(list, "", decided, MaxLimit) {
				if r.Entry == id {
					n++
				}
			}
		}
		return n
	}
	duplicates := func(decided bool) (content, relation []Row) {
		for _, r := range w.all(ListDuplicates, "", decided, MaxLimit) {
			switch {
			case r.Content == pair:
				content = append(content, r)
			case r.Relation == rel:
				relation = append(relation, r)
			}
		}
		return content, relation
	}
	if entryRows(ListSystemJunk, junk) != 1 {
		t.Fatalf("before: %s is not a system junk row", w.path(junk))
	}
	if c, r := duplicates(false); len(c) != 1 || len(r) != 1 || c[0].Copy != (domain.Ref{Entry: junk}) {
		t.Fatalf("before: open duplicates rows of the pair %+v and of the relation %+v", c, r)
	}
	if entryRows(ListUnpackedArchives, w.corpus.ID(zip)) != 1 {
		t.Fatalf("before: %s is not an unpacked archives row", zip)
	}
	if entryRows(ListRescue, rescued) != 1 {
		t.Fatalf("before: %s is not a rescue row", orcamento)
	}

	w.quarantine("corpus", junk)
	w.quarantine("corpus", w.corpus.ID(unpacked))
	w.quarantine("corpus", group)

	// The visible generation still holds the pair's row: it has one copy
	// outside the quarantine left, so it is no longer open, and it offers
	// the staying copy.
	if c, _ := duplicates(false); len(c) != 0 {
		t.Errorf("the pair's row is still open with one copy outside the quarantine: %+v", c)
	}
	if c, _ := duplicates(true); len(c) != 1 || c[0].Copy != (domain.Ref{Entry: staying}) {
		t.Errorf("the pair's row %+v; want it decided, offering entry %d", c, staying)
	}

	w.relation("same", w.corpus.ID(zip), w.corpus.ID(unpacked)) // as a generation computed before the move
	w.refresh()
	if n := entryRows(ListSystemJunk, junk); n != 0 {
		t.Errorf("the quarantined %s is %d system junk rows, want none", thumbs, n)
	}
	if entryRows(ListSystemJunk, staying) != 1 {
		t.Errorf("the staying %s is no system junk row", w.path(staying))
	}
	for _, decided := range []bool{false, true} {
		if c, _ := duplicates(decided); len(c) != 0 {
			t.Errorf("the pair with a quarantined copy is a duplicates row: %+v", c)
		}
	}
	for _, r := range append(w.all(ListDuplicates, "", false, MaxLimit), w.all(ListDuplicates, "", true, MaxLimit)...) {
		if r.Relation != 0 {
			var a, b int64
			if err := w.st.Reader().QueryRow(`SELECT a_entry, b_entry FROM relations WHERE id = ?`, r.Relation).
				Scan(&a, &b); err != nil {
				t.Fatal(err)
			}
			for _, side := range []int64{a, b} {
				if index.IsQuarantinePath([]byte(w.path(domain.EntryID(side)))) {
					t.Errorf("duplicates row %d is a relation with the quarantined side %q", r.ID, w.path(domain.EntryID(side)))
				}
			}
		}
		if r.Copy.Entry != 0 && index.IsQuarantinePath([]byte(w.path(r.Copy.Entry))) {
			t.Errorf("duplicates row %d offers the quarantined %q", r.ID, w.path(r.Copy.Entry))
		}
	}
	if n := entryRows(ListUnpackedArchives, w.corpus.ID(zip)); n != 0 {
		t.Errorf("%s is %d unpacked archives rows, its folder quarantined; want none", zip, n)
	}
	if n := entryRows(ListRescue, rescued); n != 0 {
		t.Errorf("%s, inside a quarantined group, is %d rescue rows; want none", orcamento, n)
	}
	for _, l := range CardLists {
		for _, decided := range []bool{false, true} {
			for _, r := range w.all(l, "", decided, MaxLimit) {
				for _, id := range []domain.EntryID{r.Entry, r.Group} {
					if id != 0 && index.IsQuarantinePath([]byte(w.path(id))) {
						t.Errorf("%s row %d names the quarantined %q", l, r.ID, w.path(id))
					}
				}
			}
		}
	}
}
