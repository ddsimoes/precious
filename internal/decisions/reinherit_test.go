package decisions

import (
	"context"
	"database/sql"
	"testing"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/index/indextest"
)

// Reinherit after a move (r3 design D6 step 6): the moved folder takes the
// effective decision of each new parent; inside it, the folders with their
// own decision keep it and pass it on, and the rest follow the new parent.
// A moved entry with its own decision keeps it. Own decisions never change.
func TestReinheritAfterMove(t *testing.T) {
	e := newEnv(t)
	s := e.seed(t,
		indextest.Node{Path: "Keep/a.txt", Size: 1},
		indextest.Node{Path: "Trash/b.txt", Size: 1},
		indextest.Node{Path: "Plain", Kind: domain.EntryDirectory},
		indextest.Node{Path: "M/x.txt", Size: 1},
		indextest.Node{Path: "M/Later/y.txt", Size: 1},
		indextest.Node{Path: "M/N/z.txt", Size: 1},
		indextest.Node{Path: "M/N/Kept/w.txt", Size: 1},
		indextest.Node{Path: "M/N/Kept/Deep/v.txt", Size: 1},
		indextest.Node{Path: "Own.txt", Size: 1},
	)
	e.decide(t, one(s.ID("Keep"), domain.DecisionKeep))
	e.decide(t, one(s.ID("Trash"), domain.DecisionDiscard))
	e.decide(t, one(s.ID("M/Later"), domain.DecisionLater))
	e.decide(t, one(s.ID("M/N/Kept"), domain.DecisionKeep))
	e.decide(t, one(s.ID("Own.txt"), domain.DecisionKeep))
	ownBefore := map[int64]string{}
	for id, r := range rows(t, e.st) {
		ownBefore[id] = r.own
	}

	move := func(entry, parent domain.EntryID) {
		t.Helper()
		if err := e.write(func(tx *sql.Tx) error {
			var name []byte
			if err := tx.QueryRow(`SELECT name FROM entries WHERE id = ?`, int64(entry)).Scan(&name); err != nil {
				return err
			}
			if _, _, err := index.MoveEntry(context.Background(), tx, index.Move{Source: s.Source, Entry: entry,
				NewParent: parent, NewName: name}); err != nil {
				return err
			}
			return Reinherit(context.Background(), tx, entry)
		}); err != nil {
			t.Fatal(err)
		}
		checkConsistent(t, e.st)
		for id, r := range rows(t, e.st) {
			if r.own != ownBefore[id] {
				t.Errorf("%q own decision %q, was %q", r.path, r.own, ownBefore[id])
			}
		}
	}

	m := s.ID("M")
	move(m, s.ID("Trash"))
	wantEffective(t, e, s, "M", "", domain.DecisionDiscard, ptr("Trash"))
	wantFrom := func(id domain.EntryID, eff domain.Decision, from domain.EntryID) {
		t.Helper()
		in := e.intent(t, id)
		if in.Effective != eff || in.From == nil || in.From.ID != from {
			t.Errorf("entry %d effective %q from %+v, want %q from %d", id, in.Effective, in.From, eff, from)
		}
	}
	wantFrom(s.ID("M/x.txt"), domain.DecisionDiscard, s.ID("Trash"))
	wantFrom(s.ID("M/N/z.txt"), domain.DecisionDiscard, s.ID("Trash"))
	wantFrom(s.ID("M/Later/y.txt"), domain.DecisionLater, s.ID("M/Later"))
	wantFrom(s.ID("M/N/Kept/Deep/v.txt"), domain.DecisionKeep, s.ID("M/N/Kept"))

	move(m, s.ID("Keep"))
	wantFrom(m, domain.DecisionKeep, s.ID("Keep"))
	wantFrom(s.ID("M/N/z.txt"), domain.DecisionKeep, s.ID("Keep"))
	wantFrom(s.ID("M/Later/y.txt"), domain.DecisionLater, s.ID("M/Later"))

	move(m, s.ID("Plain"))
	if in := e.intent(t, s.ID("M/N/z.txt")); in.Effective != domain.DecisionUndecided || in.From != nil {
		t.Errorf("M/N/z.txt under Plain reads %q from %+v, want the default", in.Effective, in.From)
	}

	// A file and a folder with their own decision keep it wherever they go.
	move(s.ID("Own.txt"), s.ID("Trash"))
	wantFrom(s.ID("Own.txt"), domain.DecisionKeep, s.ID("Own.txt"))
	move(s.ID("M/N/Kept"), s.ID("Trash"))
	wantFrom(s.ID("M/N/Kept"), domain.DecisionKeep, s.ID("M/N/Kept"))
	wantFrom(s.ID("M/N/Kept/w.txt"), domain.DecisionKeep, s.ID("M/N/Kept"))
	// A file without one follows its new folder.
	move(s.ID("M/x.txt"), s.ID("Keep"))
	wantFrom(s.ID("M/x.txt"), domain.DecisionKeep, s.ID("Keep"))

	err := e.write(func(tx *sql.Tx) error { return Reinherit(context.Background(), tx, 999999) })
	wantCode(t, err, domain.CodeNotFound)
}
