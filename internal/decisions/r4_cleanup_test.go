package decisions

import (
	"database/sql"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// A quarantined entry is frozen (r4 D13): set-decision, individual or bulk,
// and set-tags refuse it with in_quarantine and change nothing, also when
// the other targets are outside the quarantine.
func TestQuarantinedTargetsAreRefused(t *testing.T) {
	e := newEnv(t)
	s := e.seed(t, indextest.Node{Path: "Fotos/a.jpg"}, indextest.Node{Path: ".precious-quarantine/7/1/b.jpg"})
	q, out := s.ID(".precious-quarantine/7/1/b.jpg"), s.ID("Fotos/a.jpg")

	_, err := e.setDecision(one(q, domain.DecisionKeep))
	wantCode(t, err, domain.CodeInQuarantine)
	_, err = e.setDecision(bulk(domain.DecisionLater, out, q))
	wantCode(t, err, domain.CodeInQuarantine)
	var tag int64
	if err := e.write(func(tx *sql.Tx) error {
		tg, err := e.svc.CreateTag(reqCtx(), tx, "férias")
		tag = tg.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = e.write(func(tx *sql.Tx) error {
		_, err := e.svc.SetTags(reqCtx(), tx, SetTags{EntryIDs: []domain.EntryID{out, q}, Add: []int64{tag}})
		return err
	})
	wantCode(t, err, domain.CodeInQuarantine)

	var decided, tagged int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM entries WHERE decision IS NOT NULL`).Scan(&decided); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM entry_tags`).Scan(&tagged); err != nil {
		t.Fatal(err)
	}
	if decided != 0 || tagged != 0 {
		t.Fatalf("a refused request changed %d decisions and %d tags", decided, tagged)
	}
	// Outside the quarantine, the same requests apply.
	e.decide(t, bulk(domain.DecisionLater, out))
}

// A decision on the folder of a copy a ready check relied on makes the
// check stale at once (r4 D10, F8), and leaves a check relying on nothing
// there alone; a bulk decision that skips a kept target does not touch the
// checks relying on that target.
func TestDecisionMarksChecksStale(t *testing.T) {
	e := newEnv(t)
	s := e.seed(t, indextest.Node{Path: "Docs/copia.doc"}, indextest.Node{Path: "Outros/x.doc"},
		indextest.Node{Path: "Guardado/y.doc"}, indextest.Node{Path: ".precious-quarantine/7/1/orig.doc"})
	check := func(copyPath string) int64 {
		t.Helper()
		var id int64
		if err := e.write(func(tx *sql.Tx) error {
			if err := tx.QueryRow(`INSERT INTO purge_checks (source_id, state, created_at) VALUES ('disco', 'ready', 0)
				RETURNING id`).Scan(&id); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO purge_check_files (check_id, item_id, entry_id, kind, path, size, verdict,
				copy_source, copy_path) VALUES (?, ?, ?, 'file', ?, 10, 'safe', 'disco', ?)`, id,
				int64(s.ID(".precious-quarantine/7/1/orig.doc")), int64(s.ID(".precious-quarantine/7/1/orig.doc")),
				[]byte(".precious-quarantine/7/1/orig.doc"), []byte(copyPath))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	state := func(id int64) (string, sql.NullString) {
		t.Helper()
		var (
			st     string
			reason sql.NullString
		)
		if err := e.st.Reader().QueryRow(`SELECT state, stale_reason FROM purge_checks WHERE id = ?`, id).
			Scan(&st, &reason); err != nil {
			t.Fatal(err)
		}
		return st, reason
	}
	relied, other, kept := check("Docs/copia.doc"), check("Outros/elsewhere.doc"), check("Guardado/y.doc")

	e.decide(t, one(s.ID("Docs"), domain.DecisionDiscard))
	if st, reason := state(relied); st != "stale" || reason.String != "index_changed" {
		t.Fatalf("the check relying on Docs/copia.doc is %s (%v)", st, reason)
	}
	if st, _ := state(other); st != "ready" {
		t.Fatalf("a check relying on nothing in Docs is %s", st)
	}

	e.decide(t, one(s.ID("Guardado/y.doc"), domain.DecisionKeep))
	if st, _ := state(kept); st != "stale" {
		t.Fatalf("keeping a relied-on copy left its check %s", st)
	}
	kept2 := check("Guardado/y.doc")
	// The bulk discard skips the kept copy: its check stays ready.
	e.decide(t, bulk(domain.DecisionDiscard, s.ID("Guardado/y.doc"), s.ID("Outros/x.doc")))
	if st, _ := state(kept2); st != "ready" {
		t.Fatalf("a bulk decision that skipped the kept copy made its check %s", st)
	}
	if st, _ := state(other); st != "ready" {
		t.Fatalf("a decision on Outros/x.doc made the check relying on Outros/elsewhere.doc %s", st)
	}
}
