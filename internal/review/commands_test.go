package review

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"precious/internal/clock"
	"precious/internal/commands"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/jobs"
)

// api serves the command handler with select-list and the decisions
// commands registered.
type api struct {
	url  string
	keys int
}

func newAPI(t *testing.T, w *world) *api {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := jobs.NewRunner(jobs.Options{Store: w.st, Clock: clock.Real{}, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	h := commands.New(commands.Options{Store: w.st, Jobs: r, Logger: logger})
	svc := decisions.New(clock.Real{})
	decisions.RegisterCommands(h, svc)
	RegisterCommands(h, svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &api{url: srv.URL}
}

// post sends one command with a fresh Idempotency-Key and decodes the
// response body.
func (a *api) post(t *testing.T, name, body string) (int, map[string]any) {
	t.Helper()
	a.keys++
	req, err := http.NewRequest(http.MethodPost, a.url+"/api/commands/"+name, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "key-"+strconv.Itoa(a.keys))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// selected returns a selection's entry IDs.
func (w *world) selected(id string) []domain.EntryID {
	w.t.Helper()
	rows, err := w.st.Reader().Query(`SELECT entry_id FROM selection_entries WHERE selection_id = ? ORDER BY entry_id`, id)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []domain.EntryID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, domain.EntryID(id))
	}
	return out
}

// 5.3: select-list gives an R1 selection of the open rows' entries, with
// create-selection's response; a bulk discard on it skips the entries kept
// meanwhile and reports them.
func TestSelectListSelectsTheOpenRows(t *testing.T) {
	w := newCorpusWorld(t)
	a := newAPI(t, w.world)
	w.decide(w.all(ListSystemJunk, "", false, 1)[0].Entry, domain.DecisionDiscard) // no longer open

	var want []domain.EntryID
	var bytes int64
	for _, r := range w.all(ListSystemJunk, "corpus", false, 100) {
		want = append(want, r.Entry)
		bytes += r.Bytes
	}
	slices.Sort(want)
	status, body := a.post(t, CommandSelectList, `{"list":"system_junk","source_id":"corpus"}`)
	if status != http.StatusCreated {
		t.Fatalf("select-list: %d %v", status, body)
	}
	id, _ := body["selection_id"].(string)
	if got := w.selected(id); !slices.Equal(got, want) {
		t.Fatalf("selection %v, want the open rows' entries %v", got, want)
	}
	if body["count"] != float64(len(want)) || body["bytes"] != float64(bytes) {
		t.Errorf("response %v, want count %d and bytes %d", body, len(want), bytes)
	}
	if _, ok := body["kept"].(map[string]any); !ok || body["expires_at"] == nil {
		t.Errorf("response %v lacks create-selection's kept and expires_at", body)
	}
	var query string
	if err := w.st.Reader().QueryRow(`SELECT query FROM selections WHERE id = ?`, id).Scan(&query); err != nil {
		t.Fatal(err)
	}
	if query != `{"list":"system_junk","source_id":"corpus"}` {
		t.Errorf("stored query %s", query)
	}

	// The owner keeps a folder holding one selected row, then discards the
	// selection: that row is skipped and reported.
	const praia = "Fotos/2006/Praia"
	kept := w.corpus.ID(praia + "/Thumbs.db")
	if !slices.Contains(want, kept) {
		t.Fatalf("%s/Thumbs.db is not selected", praia)
	}
	w.decide(w.corpus.ID(praia), domain.DecisionKeep)
	status, body = a.post(t, decisions.CommandSetDecision, `{"selection_id":"`+id+`","decision":"discard"}`)
	if status != http.StatusOK {
		t.Fatalf("set-decision: %d %v", status, body)
	}
	if body["applied"] != float64(len(want)-1) || body["skipped_count"] != float64(1) {
		t.Fatalf("bulk discard %v, want %d applied and 1 skipped", body, len(want)-1)
	}
	skipped, _ := body["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["entry_id"] != kept.String() {
		t.Fatalf("skipped %v, want %s/Thumbs.db", skipped, praia)
	}
	if c := w.card(ListSystemJunk, "corpus"); c.Rows != 0 {
		t.Errorf("corpus's system junk card %+v after the bulk discard and the keep, want no open row", c)
	}
}

// select-list on every source and on a Gems section.
func TestSelectListAllSourcesAndGems(t *testing.T) {
	w := newCorpusWorld(t)
	a := newAPI(t, w.world)
	ids, err := Resolve(context.Background(), w.st.Reader(), ListCaches, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	status, body := a.post(t, CommandSelectList, `{"list":"caches"}`)
	if status != http.StatusCreated {
		t.Fatalf("select-list: %d %v", status, body)
	}
	if got := w.selected(body["selection_id"].(string)); !slices.Equal(got, ids) || len(ids) == 0 {
		t.Fatalf("selection %v, want %v", got, ids)
	}
	status, body = a.post(t, CommandSelectList, `{"list":"gems_unique"}`)
	if status != http.StatusCreated || body["count"] != float64(len(w.gt.Gems.Unique)) {
		t.Fatalf("select-list gems_unique: %d %v, want %d entries", status, body, len(w.gt.Gems.Unique))
	}
}

// 5.3: duplicates and unknown lists are 400, an unknown source 404.
func TestSelectListRefusals(t *testing.T) {
	w := newCopiesWorld(t)
	a := newAPI(t, w.world)
	for _, c := range []struct {
		body   string
		status int
		code   domain.ErrorCode
	}{
		{`{"list":"duplicates"}`, http.StatusBadRequest, domain.CodeInvalidRequest},
		{`{"list":"junk"}`, http.StatusBadRequest, domain.CodeInvalidRequest},
		{`{}`, http.StatusBadRequest, domain.CodeInvalidRequest},
		{`{"list":"caches","extra":1}`, http.StatusBadRequest, domain.CodeInvalidRequest},
		{`{"list":"caches","source_id":"nowhere"}`, http.StatusNotFound, domain.CodeUnknownSource},
	} {
		status, body := a.post(t, CommandSelectList, c.body)
		if status != c.status || errCode(body) != string(c.code) {
			t.Errorf("%s: %d %v, want %d %s", c.body, status, body, c.status, c.code)
		}
	}
	var n int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM selections`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d selections after refused requests", n)
	}
}
