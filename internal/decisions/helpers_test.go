package decisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/index/indextest"
	"precious/internal/rules"
	"precious/internal/search"
	"precious/internal/store"
	"precious/internal/store/storetest"
	"precious/internal/web/clientip"
)

// start is the time of every test clock.
var start = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// clientAddr is the client address of every test request.
var clientAddr = netip.MustParseAddr("192.0.2.7")

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	st  *store.Store
	clk *testClock
	svc *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := &testClock{t: start}
	return &env{st: storetest.Open(t), clk: clk, svc: New(clk, rules.Default(), index.StartScan)}
}

// reqCtx is a request context carrying the client address.
func reqCtx() context.Context {
	return clientip.With(context.Background(), clientip.Info{Addr: clientAddr, Scheme: "https"})
}

// seed seeds source "disco" with nodes.
func (e *env) seed(t *testing.T, nodes ...indextest.Node) *indextest.Seeded {
	t.Helper()
	return indextest.Seed(t, e.st, indextest.Tree{Source: "disco", CreateSource: true, MountPoint: "/mnt/disco", Nodes: nodes})
}

// seedCorpus seeds source "corpus" with the regression corpus's ground
// truth: every path with its kind and size, as a complete scan finds them.
func (e *env) seedCorpus(t *testing.T) (*indextest.Seeded, corpus.GroundTruth) {
	t.Helper()
	gt := corpus.Corpus().GroundTruth()
	nodes := make([]indextest.Node, 0, len(gt.Entries))
	for _, en := range gt.Entries {
		raw, err := en.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		n := indextest.Node{Path: string(raw), Kind: en.Kind, Unreadable: en.Unreadable}
		if en.Size != nil {
			n.Size = *en.Size
		}
		if en.Kind == domain.EntrySymlink {
			n.LinkText = "target"
		}
		nodes = append(nodes, n)
	}
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "corpus", CreateSource: true, MountPoint: "/mnt/corpus", Nodes: nodes})
	return s, gt
}

// write runs fn in one write transaction and returns its error.
func (e *env) write(fn func(tx *sql.Tx) error) error {
	return e.st.Write(reqCtx(), fn)
}

func (e *env) setDecision(req SetDecision) (SetDecisionResult, error) {
	var res SetDecisionResult
	err := e.write(func(tx *sql.Tx) error {
		var err error
		res, err = e.svc.SetDecision(reqCtx(), tx, req)
		return err
	})
	return res, err
}

func (e *env) decide(t *testing.T, req SetDecision) SetDecisionResult {
	t.Helper()
	res, err := e.setDecision(req)
	if err != nil {
		t.Fatalf("SetDecision(%+v): %v", req, err)
	}
	return res
}

// one is an individual request setting d (or inherit for "") on id.
func one(id domain.EntryID, d domain.Decision) SetDecision {
	return SetDecision{EntryID: id, Decision: d, Inherit: d == ""}
}

// bulk is a bulk request setting d (or inherit for "") on ids.
func bulk(d domain.Decision, ids ...domain.EntryID) SetDecision {
	return SetDecision{EntryIDs: ids, Decision: d, Inherit: d == ""}
}

func (e *env) createSelection(t *testing.T, q search.Query) Selection {
	t.Helper()
	var sel Selection
	if err := e.write(func(tx *sql.Tx) error {
		var err error
		sel, err = e.svc.CreateSelection(reqCtx(), tx, q)
		return err
	}); err != nil {
		t.Fatalf("CreateSelection(%+v): %v", q, err)
	}
	return sel
}
func (e *env) intent(t *testing.T, id domain.EntryID) Intent {
	t.Helper()
	in, err := Effective(context.Background(), e.st.Reader(), id)
	if err != nil {
		t.Fatalf("Effective(%d): %v", id, err)
	}
	return in
}

// wantEffective checks the own and effective decision of path and the entry
// it comes from (nil for the default).
func wantEffective(t *testing.T, e *env, s *indextest.Seeded, path string, own, eff domain.Decision, from *string) {
	t.Helper()
	in := e.intent(t, s.ID(path))
	if in.Own != own || in.Effective != eff {
		t.Errorf("%q reads own %q effective %q, want own %q effective %q", path, in.Own, in.Effective, own, eff)
	}
	switch {
	case from == nil && in.From != nil:
		t.Errorf("%q effective decision comes from %q, want the default", path, in.From.Path)
	case from != nil && in.From == nil:
		t.Errorf("%q effective decision comes from nothing, want %q", path, *from)
	case from != nil && (in.From.ID != s.ID(*from) || string(in.From.PathB64) != *from || in.From.Path != domain.DisplayName([]byte(*from))):
		t.Errorf("%q effective decision comes from %+v, want %q", path, *in.From, *from)
	}
}

func ptr[T any](v T) *T { return &v }

// row is one entry's stored intent columns.
type row struct {
	id, parent int64
	path       string
	own, eff   string
	from       int64 // 0 for NULL
}

func rows(t *testing.T, st *store.Store) map[int64]row {
	t.Helper()
	rs, err := st.Reader().Query(`SELECT id, coalesce(parent_id, 0), path, coalesce(decision, ''), eff_decision, coalesce(eff_from, 0) FROM entries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	out := map[int64]row{}
	for rs.Next() {
		var r row
		var path []byte
		if err := rs.Scan(&r.id, &r.parent, &path, &r.own, &r.eff, &r.from); err != nil {
			t.Fatal(err)
		}
		r.path = string(path)
		out[r.id] = r
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkConsistent recomputes every entry's effective decision from the own
// decisions by walking parents, and fails on any stored value that differs.
func checkConsistent(t *testing.T, st *store.Store) {
	t.Helper()
	all := rows(t, st)
	for _, r := range all {
		wantEff, wantFrom := string(domain.DecisionUndecided), int64(0)
		for a := r; ; a = all[a.parent] {
			if a.own != "" {
				wantEff, wantFrom = a.own, a.id
				break
			}
			if a.parent == 0 {
				break
			}
		}
		if r.eff != wantEff || r.from != wantFrom {
			t.Errorf("%q stores effective %q from %d, want %q from %d", r.path, r.eff, r.from, wantEff, wantFrom)
		}
	}
}

// auditEvent is one audit_events row.
type auditEvent struct {
	at     int64
	kind   string
	actor  string
	addr   sql.NullString
	detail map[string]any
}

func audits(t *testing.T, st *store.Store) []auditEvent {
	t.Helper()
	rs, err := st.Reader().Query(`SELECT occurred_at, kind, actor, client_addr, detail FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []auditEvent
	for rs.Next() {
		var (
			ev     auditEvent
			detail string
		)
		if err := rs.Scan(&ev.at, &ev.kind, &ev.actor, &ev.addr, &detail); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(detail), &ev.detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func wantCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("error %v, want %s", err, code)
	}
}

// below reports whether path is a descendant of folder.
func below(path, folder string) bool { return strings.HasPrefix(path, folder+"/") }

// rawPaths returns the raw paths of the ground truth's entries.
func rawPaths(t *testing.T, gt corpus.GroundTruth) []corpus.Entry {
	t.Helper()
	out := make([]corpus.Entry, len(gt.Entries))
	for i, en := range gt.Entries {
		raw, err := en.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		en.Path = string(raw)
		out[i] = en
	}
	return out
}
