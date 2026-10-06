package decisions

import (
	"testing"

	"precious/internal/domain"
	"precious/internal/search"
)

// R1.11 Bulk discard skips kept entries: a selection holding an explicitly
// kept file and a file inside a kept folder skips both, listing them with
// their paths, and discards the rest; an individual request then changes
// the kept file.
func TestR1_11BulkDiscardSkipsKeptEntries(t *testing.T) {
	e := newEnv(t)
	s, _ := e.seedCorpus(t)
	keptFile := "Fotos/2006/Praia/Thumbs.db"
	keptFolder := "Fotos/2006/Casamento"
	inKeptFolder := keptFolder + "/Thumbs.db"
	e.decide(t, one(s.ID(keptFile), keep))
	e.decide(t, one(s.ID(keptFolder), keep))

	sel := e.createSelection(t, search.Query{Source: s.Source, Name: "Thumbs.db"})
	if sel.Count < 5 || sel.KeptCount != 2 {
		t.Fatalf("selection %+v, want several Thumbs.db files of which 2 kept", sel)
	}
	res := e.decide(t, SetDecision{SelectionID: sel.ID, Decision: discard})
	if res.Applied != int(sel.Count)-2 || res.SkippedCount != 2 || len(res.Skipped) != 2 {
		t.Fatalf("bulk discard = %+v, want %d applied and 2 skipped", res, sel.Count-2)
	}
	for i, want := range []string{inKeptFolder, keptFile} {
		if sk := res.Skipped[i]; sk.Path != want || string(sk.PathB64) != want || sk.EntryID != s.ID(want) {
			t.Errorf("skipped[%d] = %+v, want %q", i, sk, want)
		}
	}
	wantEffective(t, e, s, keptFile, keep, keep, ptr(keptFile))
	wantEffective(t, e, s, inKeptFolder, inherit, keep, ptr(keptFolder))

	members := selectionMembers(t, e, sel.ID)
	discarded := 0
	for _, id := range members {
		if id == s.ID(keptFile) || id == s.ID(inKeptFolder) {
			continue
		}
		in := e.intent(t, id)
		if in.Own != discard || in.Effective != discard {
			t.Errorf("selected entry %d reads own %q effective %q, want discard", id, in.Own, in.Effective)
		}
		discarded++
	}
	if discarded != res.Applied {
		t.Errorf("%d selected entries read discard, want %d", discarded, res.Applied)
	}

	res = e.decide(t, one(s.ID(keptFile), discard))
	if res.Applied != 1 || res.SkippedCount != 0 {
		t.Errorf("individual discard of the kept file = %+v", res)
	}
	wantEffective(t, e, s, keptFile, discard, discard, ptr(keptFile))
	checkConsistent(t, e.st)
}

func selectionMembers(t *testing.T, e *env, id string) []domain.EntryID {
	t.Helper()
	rs, err := e.st.Reader().Query(`SELECT entry_id FROM selection_entries WHERE selection_id = ? ORDER BY entry_id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []domain.EntryID
	for rs.Next() {
		var v int64
		if err := rs.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, domain.EntryID(v))
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
