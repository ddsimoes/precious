package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"precious/internal/rules"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

type env struct {
	st  *store.Store
	mux *http.ServeMux
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.Open(t)
	mux := http.NewServeMux()
	Register(mux, st, rules.Default(), nil)
	return &env{st: st, mux: mux}
}

// get requests target, checks the status, and decodes the JSON body into
// out unless it is nil.
func (e *env) get(t *testing.T, target string, status int, out any) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != status {
		t.Fatalf("GET %s: status %d, want %d; body %s", target, rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("GET %s: Content-Type %q", target, ct)
	}
	if out == nil {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("GET %s: decode %s: %v", target, rec.Body, err)
	}
}

// fails requests target and checks the error status and code.
func (e *env) fails(t *testing.T, target string, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	e.get(t, target, status, &body)
	if body.Error.Code != code {
		t.Fatalf("GET %s: code %q, want %q", target, body.Error.Code, code)
	}
}

// exec runs one write statement.
func (e *env) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func year(y int) time.Time { return time.Date(y, 6, 1, 12, 0, 0, 0, time.UTC) }

// row is the decoded EntryRow.
type row struct {
	ID            string  `json:"id"`
	SourceID      string  `json:"source_id"`
	Name          string  `json:"name"`
	NameB64       []byte  `json:"name_b64"`
	Path          string  `json:"path"`
	PathB64       []byte  `json:"path_b64"`
	Kind          string  `json:"kind"`
	FileKind      *string `json:"file_kind"`
	MainKind      *string `json:"main_kind"`
	Category      *string `json:"category"`
	Family        *string `json:"family"`
	Triage        *string `json:"triage"`
	Group         bool    `json:"group"`
	Veto          bool    `json:"veto"`
	Size          int64   `json:"size"`
	TotalBytes    int64   `json:"total_bytes"`
	TotalFiles    int64   `json:"total_files"`
	MTime         *string `json:"mtime"`
	Newest        *string `json:"newest"`
	Oldest        *string `json:"oldest"`
	State         string  `json:"state"`
	Partial       bool    `json:"partial"`
	MountBoundary bool    `json:"mount_boundary"`
	Decision      *string `json:"decision"`
	EffDecision   string  `json:"eff_decision"`
	TagIDs        []int64 `json:"tag_ids"`
}

type page struct {
	Items      []row   `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func str(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

func ids(rows []row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
