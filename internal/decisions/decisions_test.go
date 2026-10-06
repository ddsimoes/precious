package decisions

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/index/indextest"
)

const (
	keep      = domain.DecisionKeep
	discard   = domain.DecisionDiscard
	later     = domain.DecisionLater
	undecided = domain.DecisionUndecided
	inherit   = domain.Decision("")
)

func dir(p string) indextest.Node { return indextest.Node{Path: p, Kind: domain.EntryDirectory} }

func file(p string, size int64) indextest.Node { return indextest.Node{Path: p, Size: size} }

// disk is a small tree with the sibling traps of path ranges: "Fotos - Copia",
// "Fotos!", and "Fotos0" sort around "Fotos/".
func disk(t *testing.T, e *env) *indextest.Seeded {
	t.Helper()
	return e.seed(t,
		file("Fotos/2006/rejeitadas/IMG_0001.JPG", 10),
		file("Fotos/2006/rejeitadas/IMG_0002.JPG", 11),
		file("Fotos/2006/borrada.jpg", 12),
		file("Fotos/2006/natal/IMG_0001.JPG", 13),
		file("Fotos/IMG_0042.JPG", 14),
		file("Fotos - Copia/c.jpg", 15),
		file("Fotos!/d.jpg", 16),
		file("Fotos0/z.jpg", 17),
		file("HD antigo/Backup_PC_2004/Meus documentos/carta.doc", 20),
		file("HD antigo/Backup_PC_2004/novo.tmp", 21),
		file("HD antigo/leiame.txt", 22),
		file("Arquivos de programas/Microsoft Office/WINWORD.EXE", 30),
		file("Arquivos de programas/Microsoft Office/OFFICE11/EXCEL.EXE", 31),
		dir("vazio"),
	)
}

func TestNearestOwnDecisionWins(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	e.decide(t, one(s.ID("Fotos"), keep))
	e.decide(t, one(s.ID("Fotos/2006/rejeitadas"), discard))

	wantEffective(t, e, s, "Fotos", keep, keep, ptr("Fotos"))
	wantEffective(t, e, s, "Fotos/2006", inherit, keep, ptr("Fotos"))
	wantEffective(t, e, s, "Fotos/2006/borrada.jpg", inherit, keep, ptr("Fotos"))
	wantEffective(t, e, s, "Fotos/2006/rejeitadas", discard, discard, ptr("Fotos/2006/rejeitadas"))
	wantEffective(t, e, s, "Fotos/2006/rejeitadas/IMG_0001.JPG", inherit, discard, ptr("Fotos/2006/rejeitadas"))
	for _, p := range []string{"Fotos - Copia", "Fotos - Copia/c.jpg", "Fotos!/d.jpg", "Fotos0/z.jpg", "HD antigo", ""} {
		wantEffective(t, e, s, p, inherit, undecided, nil)
	}
	checkConsistent(t, e.st)
}

// Deciding a folder after its descendants stops at each descendant's own
// decision, including cut folders whose subtrees sort out of name order
// ("X/a!/" sorts before "X/a/"), nested cuts, and a decided file. Both
// assignments of keep and later to "X/a" and "X/a!" run, so the cut points
// come back in either order whatever index SQLite reads them from.
func TestDecisionStopsAtOwnDecisions(t *testing.T) {
	for _, pair := range [][2]domain.Decision{{keep, later}, {later, keep}} {
		a, bang := pair[0], pair[1]
		t.Run(fmt.Sprintf("%s-%s", a, bang), func(t *testing.T) {
			e := newEnv(t)
			s := e.seed(t,
				file("X/a/f", 1), file("X/a/b/g", 1), file("X/a/b/c/h", 1),
				file("X/a!/i", 1), file("X/a!b/j", 1), file("X/a0/k", 1), file("X/b", 1), file("X/c", 1),
			)
			e.decide(t, one(s.ID("X/a"), a))
			e.decide(t, one(s.ID("X/a/b/c"), undecided))
			e.decide(t, one(s.ID("X/a!"), bang))
			e.decide(t, one(s.ID("X/c"), keep))
			e.decide(t, one(s.ID("X"), discard))

			wantEffective(t, e, s, "X/a/f", inherit, a, ptr("X/a"))
			wantEffective(t, e, s, "X/a/b/g", inherit, a, ptr("X/a"))
			wantEffective(t, e, s, "X/a/b/c/h", inherit, undecided, ptr("X/a/b/c"))
			wantEffective(t, e, s, "X/a!/i", inherit, bang, ptr("X/a!"))
			wantEffective(t, e, s, "X/a!b/j", inherit, discard, ptr("X"))
			wantEffective(t, e, s, "X/a0/k", inherit, discard, ptr("X"))
			wantEffective(t, e, s, "X/b", inherit, discard, ptr("X"))
			wantEffective(t, e, s, "X/c", keep, keep, ptr("X/c"))
			checkConsistent(t, e.st)

			// Clearing a cut hands its subtree to the nearest decided ancestor.
			e.decide(t, one(s.ID("X/a"), inherit))
			wantEffective(t, e, s, "X/a/b/g", inherit, discard, ptr("X"))
			wantEffective(t, e, s, "X/a/b/c/h", inherit, undecided, ptr("X/a/b/c"))
			checkConsistent(t, e.st)
		})
	}
}

func TestInheritClears(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	backup := "HD antigo/Backup_PC_2004"
	e.decide(t, one(s.ID("HD antigo"), later))
	e.decide(t, one(s.ID(backup), discard))
	e.decide(t, one(s.ID(backup+"/Meus documentos/carta.doc"), keep))
	wantEffective(t, e, s, backup+"/novo.tmp", inherit, discard, ptr(backup))

	res := e.decide(t, one(s.ID(backup), inherit))
	if res.Applied != 1 || res.SkippedCount != 0 || len(res.Skipped) != 0 {
		t.Errorf("inherit = %+v, want 1 applied", res)
	}
	wantEffective(t, e, s, backup, inherit, later, ptr("HD antigo"))
	wantEffective(t, e, s, backup+"/novo.tmp", inherit, later, ptr("HD antigo"))
	wantEffective(t, e, s, backup+"/Meus documentos", inherit, later, ptr("HD antigo"))
	wantEffective(t, e, s, backup+"/Meus documentos/carta.doc", keep, keep, ptr(backup+"/Meus documentos/carta.doc"))
	checkConsistent(t, e.st)

	// With no decided ancestor, inherit gives the default.
	office := "Arquivos de programas/Microsoft Office"
	e.decide(t, one(s.ID(office), discard))
	e.decide(t, one(s.ID(office), inherit))
	wantEffective(t, e, s, office, inherit, undecided, nil)
	wantEffective(t, e, s, office+"/OFFICE11/EXCEL.EXE", inherit, undecided, nil)

	// Inheriting a file takes its folder's decision.
	e.decide(t, one(s.ID(backup+"/Meus documentos/carta.doc"), inherit))
	wantEffective(t, e, s, backup+"/Meus documentos/carta.doc", inherit, later, ptr("HD antigo"))
	checkConsistent(t, e.st)

	var at sql.NullInt64
	if err := e.st.Reader().QueryRow(`SELECT decision_at FROM entries WHERE id = ?`, int64(s.ID(backup))).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at.Valid {
		t.Errorf("decision_at = %d after inherit, want NULL", at.Int64)
	}
	if err := e.st.Reader().QueryRow(`SELECT decision_at FROM entries WHERE id = ?`, int64(s.ID("HD antigo"))).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at.Int64 != clock.Millis(start) {
		t.Errorf("decision_at = %v, want %d", at, clock.Millis(start))
	}
}

func TestRootDecision(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	e.decide(t, one(s.ID("Fotos"), keep))
	e.decide(t, one(s.Root, later))
	wantEffective(t, e, s, "", later, later, ptr(""))
	wantEffective(t, e, s, "Fotos0/z.jpg", inherit, later, ptr(""))
	wantEffective(t, e, s, "Fotos/2006", inherit, keep, ptr("Fotos"))
	checkConsistent(t, e.st)
	e.decide(t, one(s.Root, inherit))
	wantEffective(t, e, s, "", inherit, undecided, nil)
	wantEffective(t, e, s, "vazio", inherit, undecided, nil)
	checkConsistent(t, e.st)
}

func TestIndividualChangesKeep(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	e.decide(t, one(s.ID("Fotos"), keep))
	res := e.decide(t, one(s.ID("Fotos/2006/borrada.jpg"), discard))
	if res.Applied != 1 || res.SkippedCount != 0 || res.Skipped == nil || len(res.Skipped) != 0 {
		t.Errorf("individual discard of an inherited keep = %+v, want {1 0 []}", res)
	}
	wantEffective(t, e, s, "Fotos/2006/borrada.jpg", discard, discard, ptr("Fotos/2006/borrada.jpg"))
	wantEffective(t, e, s, "Fotos", keep, keep, ptr("Fotos"))
	wantEffective(t, e, s, "Fotos/IMG_0042.JPG", inherit, keep, ptr("Fotos"))

	office := "Arquivos de programas/Microsoft Office"
	e.decide(t, one(s.ID(office), keep))
	e.decide(t, one(s.ID(office), discard))
	wantEffective(t, e, s, office+"/OFFICE11/EXCEL.EXE", inherit, discard, ptr(office))
	checkConsistent(t, e.st)
}

func TestUnknownIDFailsWholeRequest(t *testing.T) {
	e := newEnv(t)
	s := e.seed(t, func() []indextest.Node {
		nodes := make([]indextest.Node, 50)
		for i := range nodes {
			nodes[i] = file(fmt.Sprintf("d/f%02d", i), 1)
		}
		return nodes
	}()...)
	ids := make([]domain.EntryID, 0, 51)
	for i := range 50 {
		ids = append(ids, s.ID(fmt.Sprintf("d/f%02d", i)))
	}
	before := rows(t, e.st)
	_, err := e.setDecision(bulk(discard, append(ids, 999999)...))
	wantCode(t, err, domain.CodeNotFound)
	_, err = e.setDecision(one(999999, keep))
	wantCode(t, err, domain.CodeNotFound)
	if after := rows(t, e.st); !mapsEqual(before, after) {
		t.Error("a request with an unknown ID changed entries")
	}
	if ev := audits(t, e.st); len(ev) != 0 {
		t.Errorf("rejected requests wrote audit events %+v", ev)
	}
}

func mapsEqual(a, b map[int64]row) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRequestShape(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	id := s.ID("Fotos")
	many := make([]domain.EntryID, MaxEntryIDs+1)
	for i := range many {
		many[i] = id
	}
	for name, req := range map[string]SetDecision{
		"both":               {EntryID: id, EntryIDs: []domain.EntryID{id}, Decision: keep},
		"id and selection":   {EntryID: id, SelectionID: "x", Decision: keep},
		"ids and selection":  {EntryIDs: []domain.EntryID{id}, SelectionID: "x", Decision: keep},
		"neither":            {Decision: keep},
		"too many":           {EntryIDs: many, Decision: discard},
		"no decision":        {EntryID: id},
		"unknown decision":   {EntryID: id, Decision: "maybe"},
		"inherit and keep":   {EntryID: id, Decision: keep, Inherit: true},
		"inherit as a value": {EntryID: id, Decision: "inherit"},
	} {
		_, err := e.setDecision(req)
		if domain.CodeOf(err) != domain.CodeInvalidRequest {
			t.Errorf("%s: error %v, want invalid_request", name, err)
		}
	}
	if _, err := e.setDecision(SetDecision{EntryIDs: many[:MaxEntryIDs], Decision: discard}); err != nil {
		t.Errorf("%d entry_ids: %v", MaxEntryIDs, err)
	}
	checkConsistent(t, e.st)
}

func TestBulkSkipsKeep(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	carta := "HD antigo/Backup_PC_2004/Meus documentos/carta.doc"

	// Bulk inherit does not clear a keep.
	e.decide(t, one(s.ID(carta), keep))
	res := e.decide(t, bulk(inherit, s.ID(carta), s.ID("HD antigo/leiame.txt")))
	if res.Applied != 1 || res.SkippedCount != 1 || len(res.Skipped) != 1 || res.Skipped[0].EntryID != s.ID(carta) ||
		res.Skipped[0].Path != carta || string(res.Skipped[0].PathB64) != carta {
		t.Errorf("bulk inherit = %+v, want carta.doc skipped", res)
	}
	wantEffective(t, e, s, carta, keep, keep, ptr(carta))

	// Bulk keep applies to every target, kept or not.
	e.decide(t, one(s.ID("Fotos0/z.jpg"), discard))
	res = e.decide(t, bulk(keep, s.ID("Fotos0/z.jpg"), s.ID("Fotos!/d.jpg"), s.ID(carta)))
	if res.Applied != 3 || res.SkippedCount != 0 || len(res.Skipped) != 0 {
		t.Errorf("bulk keep = %+v, want 3 applied", res)
	}
	for _, p := range []string{"Fotos0/z.jpg", "Fotos!/d.jpg", carta} {
		wantEffective(t, e, s, p, keep, keep, ptr(p))
	}

	// A keep committed just before is honored, inherited or explicit, and a
	// folder target is skipped whole.
	e.decide(t, one(s.ID("Fotos"), keep))
	res = e.decide(t, bulk(discard, s.ID("Fotos/2006/rejeitadas/IMG_0001.JPG"), s.ID("Fotos/2006"), s.ID("Fotos - Copia")))
	if res.Applied != 1 || res.SkippedCount != 2 {
		t.Errorf("bulk discard = %+v, want 1 applied and 2 skipped", res)
	}
	if got := []string{res.Skipped[0].Path, res.Skipped[1].Path}; !slices.Equal(got, []string{"Fotos/2006", "Fotos/2006/rejeitadas/IMG_0001.JPG"}) {
		t.Errorf("skipped %v, want in path order", got)
	}
	wantEffective(t, e, s, "Fotos/2006/rejeitadas/IMG_0001.JPG", inherit, keep, ptr("Fotos"))
	wantEffective(t, e, s, "Fotos - Copia/c.jpg", inherit, discard, ptr("Fotos - Copia"))

	// A bulk discard of a folder holding a kept entry discards the folder
	// and stops at the keep.
	res = e.decide(t, bulk(discard, s.ID("HD antigo")))
	if res.Applied != 1 || res.SkippedCount != 0 {
		t.Errorf("bulk discard of HD antigo = %+v", res)
	}
	wantEffective(t, e, s, "HD antigo/Backup_PC_2004/novo.tmp", inherit, discard, ptr("HD antigo"))
	wantEffective(t, e, s, carta, keep, keep, ptr(carta))
	checkConsistent(t, e.st)
}

func TestSkippedListCapped(t *testing.T) {
	e := newEnv(t)
	nodes := make([]indextest.Node, 250)
	for i := range nodes {
		nodes[i] = file(fmt.Sprintf("k/f%03d", i), 1)
	}
	s := e.seed(t, append(nodes, file("free", 1))...)
	e.decide(t, one(s.ID("k"), keep))
	ids := []domain.EntryID{s.ID("free")}
	for i := range nodes {
		ids = append(ids, s.ID(nodes[i].Path))
	}
	res := e.decide(t, bulk(discard, ids...))
	if res.Applied != 1 || res.SkippedCount != 250 || len(res.Skipped) != MaxSkippedListed {
		t.Fatalf("bulk discard = applied %d, skipped %d listing %d; want 1, 250, %d",
			res.Applied, res.SkippedCount, len(res.Skipped), MaxSkippedListed)
	}
	if res.Skipped[0].Path != "k/f000" || res.Skipped[99].Path != "k/f099" {
		t.Errorf("skipped list runs %q..%q, want k/f000..k/f099", res.Skipped[0].Path, res.Skipped[99].Path)
	}
	checkConsistent(t, e.st)
}

func TestDecisionAudit(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	e.decide(t, one(s.ID("Fotos"), keep))
	e.clk.Advance(time.Millisecond)
	e.decide(t, one(s.ID("Fotos"), discard))
	e.decide(t, one(s.ID("Fotos/IMG_0042.JPG"), keep))
	e.decide(t, bulk(later, s.ID("Fotos/IMG_0042.JPG"), s.ID("Fotos/2006"), s.ID("vazio")))

	ev := audits(t, e.st)
	if len(ev) != 4 {
		t.Fatalf("%d audit events, want one per request: %+v", len(ev), ev)
	}
	for _, x := range ev {
		if x.kind != AuditDecisionSet || x.actor != "admin" || x.addr.String != clientAddr.String() {
			t.Errorf("audit event %+v, want decision_set by admin from %s", x, clientAddr)
		}
	}
	change := ev[1]
	if change.at != clock.Millis(start)+1 {
		t.Errorf("audit time %d, want %d", change.at, clock.Millis(start)+1)
	}
	want := map[string]any{"entry_id": s.ID("Fotos").String(), "path": "Fotos", "old": "keep", "new": "discard",
		"applied": float64(1), "skipped": float64(0)}
	if fmt.Sprint(change.detail) != fmt.Sprint(want) {
		t.Errorf("protection change audited as %v, want %v", change.detail, want)
	}
	b := ev[3].detail
	if b["new"] != "later" || b["applied"] != float64(2) || b["skipped"] != float64(1) ||
		fmt.Sprint(b["old"]) != "map[inherit:2]" || len(b["entry_ids"].([]any)) != 3 {
		t.Errorf("bulk audited as %v", b)
	}
}

func TestInheritFrom(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	e.decide(t, one(s.ID("Fotos"), discard))
	inherited := func(parent domain.EntryID) (domain.Decision, *domain.EntryID) {
		var (
			eff  domain.Decision
			from *domain.EntryID
		)
		if err := e.write(func(tx *sql.Tx) error {
			var err error
			eff, from, err = InheritFrom(context.Background(), tx, parent)
			return err
		}); err != nil {
			t.Fatalf("InheritFrom(%d): %v", parent, err)
		}
		return eff, from
	}
	if eff, from := inherited(s.ID("Fotos")); eff != discard || from == nil || *from != s.ID("Fotos") {
		t.Errorf("child of Fotos inherits %q from %v, want discard from Fotos", eff, from)
	}
	if eff, from := inherited(s.ID("Fotos/2006")); eff != discard || from == nil || *from != s.ID("Fotos") {
		t.Errorf("child of Fotos/2006 inherits %q from %v, want discard from Fotos", eff, from)
	}
	if eff, from := inherited(s.ID("vazio")); eff != undecided || from != nil {
		t.Errorf("child of vazio inherits %q from %v, want the default", eff, from)
	}
	err := e.write(func(tx *sql.Tx) error {
		_, _, err := InheritFrom(context.Background(), tx, 999999)
		return err
	})
	wantCode(t, err, domain.CodeNotFound)
}

func TestTotals(t *testing.T) {
	e := newEnv(t)
	s := disk(t, e)
	totals := func() map[domain.Decision]Total {
		got, err := Totals(context.Background(), e.st.Reader(), s.Source)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	all := Total{Files: 13, Bytes: 10 + 11 + 12 + 13 + 14 + 15 + 16 + 17 + 20 + 21 + 22 + 30 + 31}
	if got := totals(); got[undecided] != all || got[keep] != (Total{}) || got[discard] != (Total{}) || got[later] != (Total{}) || len(got) != 4 {
		t.Errorf("totals before any decision = %v", got)
	}
	e.decide(t, one(s.ID("Fotos"), discard))
	e.decide(t, one(s.ID("Fotos/2006/natal"), keep))
	// A missing file counts nowhere.
	if _, err := e.st.Writer().Exec(`UPDATE entries SET state = 'missing' WHERE id = ?`, int64(s.ID("Fotos/IMG_0042.JPG"))); err != nil {
		t.Fatal(err)
	}
	got := totals()
	if want := (Total{Files: 3, Bytes: 10 + 11 + 12}); got[discard] != want {
		t.Errorf("discard totals %v, want %v", got[discard], want)
	}
	if want := (Total{Files: 1, Bytes: 13}); got[keep] != want {
		t.Errorf("keep totals %v, want %v", got[keep], want)
	}
	if want := (Total{Files: all.Files - 5, Bytes: all.Bytes - 10 - 11 - 12 - 13 - 14}); got[undecided] != want {
		t.Errorf("undecided totals %v, want %v", got[undecided], want)
	}
}

// Random sequences of individual and bulk requests, nested and overlapping,
// keep every stored effective decision true.
func TestRandomRequestsStayConsistent(t *testing.T) {
	e := newEnv(t)
	var nodes []indextest.Node
	for _, a := range []string{"a", "a!", "a0", "b"} {
		for _, b := range []string{"x", "x!", "y"} {
			for _, c := range []string{"1", "2"} {
				nodes = append(nodes, file(a+"/"+b+"/"+c, 1))
			}
			nodes = append(nodes, file(a+"/"+b+"/z/"+"3", 1))
		}
	}
	e.seed(t, nodes...)
	var ids []domain.EntryID
	for _, r := range rows(t, e.st) {
		ids = append(ids, domain.EntryID(r.id))
	}
	slices.Sort(ids)
	rng := rand.New(rand.NewPCG(1, 2))
	values := []domain.Decision{inherit, undecided, keep, discard, later}
	for i := range 300 {
		d := values[rng.IntN(len(values))]
		var req SetDecision
		if rng.IntN(2) == 0 {
			req = one(ids[rng.IntN(len(ids))], d)
		} else {
			n := 1 + rng.IntN(8)
			targets := make([]domain.EntryID, n)
			for j := range targets {
				targets[j] = ids[rng.IntN(len(ids))]
			}
			req = bulk(d, targets...)
		}
		e.decide(t, req)
		checkConsistent(t, e.st)
		if t.Failed() {
			t.Fatalf("inconsistent after request %d: %+v", i, req)
		}
	}
}
