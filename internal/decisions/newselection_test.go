package decisions

import (
	"database/sql"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/search"
)

func (e *env) newSelection(query json.RawMessage, ids []domain.EntryID) (Selection, error) {
	var sel Selection
	err := e.write(func(tx *sql.Tx) error {
		var err error
		sel, err = e.svc.NewSelection(reqCtx(), tx, query, ids)
		return err
	})
	return sel, err
}

func (e *env) selectionEntries(t *testing.T, id string) []domain.EntryID {
	t.Helper()
	rows, err := e.st.Reader().Query(`SELECT entry_id FROM selection_entries WHERE selection_id = ? ORDER BY entry_id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []domain.EntryID
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, domain.EntryID(n))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// A selection made from explicit IDs (R2 design D13, select-list) has the
// count, bytes, kept figures, and expiry of create-selection over the same
// entries, and stores the given query JSON.
func TestNewSelectionMatchesCreateSelection(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	e.decide(t, one(s.ID("Fotos/2006/natal"), keep))
	e.decide(t, one(s.ID("Fotos/IMG_0042.JPG"), keep))
	q := search.Query{Within: ptr(s.ID("Fotos"))}
	byQuery := e.createSelection(t, q)

	var ids []domain.EntryID
	if err := e.write(func(tx *sql.Tx) error {
		var err error
		ids, err = search.Resolve(reqCtx(), tx, q, search.MaxResolve)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Out of order and with a repeat: the resolver's order does not matter.
	given := append(slices.Clone(ids), ids[0])
	slices.Reverse(given)
	listQuery := json.RawMessage(`{"list":"system_junk"}`)
	byIDs, err := e.newSelection(listQuery, given)
	if err != nil {
		t.Fatal(err)
	}
	if byIDs.ID == "" || byIDs.ID == byQuery.ID {
		t.Fatalf("selection id %q", byIDs.ID)
	}
	a, b := byQuery, byIDs
	a.ID, b.ID = "", ""
	if a != b {
		t.Errorf("from IDs %+v, from the query %+v", b, a)
	}
	if b.KeptCount == 0 || b.Count != int64(len(ids)) || b.ExpiresAt != start.Add(SelectionTTL) {
		t.Errorf("from IDs %+v, want %d entries, some kept, expiring after the TTL", b, len(ids))
	}
	if got := e.selectionEntries(t, byIDs.ID); !slices.Equal(got, ids) {
		t.Errorf("stored entries %v, want %v", got, ids)
	}
	var stored string
	var count, bytes, kept int64
	if err := e.st.Reader().QueryRow(`SELECT query, count, bytes, kept FROM selections WHERE id = ?`, byIDs.ID).
		Scan(&stored, &count, &bytes, &kept); err != nil {
		t.Fatal(err)
	}
	if stored != string(listQuery) || count != b.Count || bytes != b.Bytes || kept != b.KeptCount {
		t.Errorf("stored %s %d %d %d", stored, count, bytes, kept)
	}

	// Usable until it expires, like any selection.
	e.clk.Advance(SelectionTTL - time.Millisecond)
	if _, err := e.setDecision(SetDecision{SelectionID: byIDs.ID, Decision: later}); err != nil {
		t.Fatalf("a selection just before its expiry: %v", err)
	}
	e.clk.Advance(time.Millisecond)
	_, err = e.setDecision(SetDecision{SelectionID: byIDs.ID, Decision: discard})
	wantCode(t, err, domain.CodeSelectionExpired)
}

// No entries is an empty selection; an unknown entry is not_found, and
// nothing is stored for it; a query that is not JSON is refused.
func TestNewSelectionRefusals(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	empty, err := e.newSelection(json.RawMessage(`{}`), nil)
	if err != nil || empty.Count != 0 || empty.Bytes != 0 {
		t.Fatalf("empty selection %+v, %v", empty, err)
	}
	before := e.tagCount(t, `SELECT count(*) FROM selections`)
	_, err = e.newSelection(json.RawMessage(`{}`), []domain.EntryID{s.ID("Fotos"), 987654})
	wantCode(t, err, domain.CodeNotFound)
	if _, err := e.newSelection(json.RawMessage(`{"list":`), []domain.EntryID{s.ID("Fotos")}); err == nil {
		t.Error("a query that is not JSON was stored")
	}
	if n := e.tagCount(t, `SELECT count(*) FROM selections`); n != before {
		t.Errorf("refused selections stored %d rows", n-before)
	}
}
