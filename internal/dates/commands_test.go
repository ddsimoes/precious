package dates

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/media"
)

// apiB serves the dates commands and reads of an env through the real
// command handler (Idempotency-Key, one write transaction) and router.
type apiB struct {
	e    *env
	mux  *http.ServeMux
	keys int
}

func (e *env) apiB() *apiB {
	h := commands.New(commands.Options{Store: e.st, Jobs: e.r, Logger: discard()})
	e.svc.RegisterCommands(h)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", h)
	e.svc.Routes(mux)
	return &apiB{e: e, mux: mux}
}

// post sends a command with a fresh Idempotency-Key.
func (a *apiB) post(name, body string) (int, string) {
	a.keys++
	req := httptest.NewRequest(http.MethodPost, "/api/commands/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "key-"+strconv.Itoa(a.keys))
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// ok sends a command that must answer 200 and decodes its body into v.
func (a *apiB) ok(name, body string, v any) {
	a.e.t.Helper()
	code, out := a.post(name, body)
	if code != http.StatusOK {
		a.e.t.Fatalf("%s %s = %d %s, want 200", name, body, code, out)
	}
	decodeB(a.e.t, out, v)
}

// refuse sends a command that must fail with status and code.
func (a *apiB) refuse(status int, code domain.ErrorCode, name, body string) {
	a.e.t.Helper()
	got, out := a.post(name, body)
	if got != status || !strings.Contains(out, `"code":"`+string(code)+`"`) {
		a.e.t.Errorf("%s %s = %d %s, want %d %s", name, body, got, out, status, code)
	}
}

func (a *apiB) getRaw(path string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// get reads path, which must answer 200, into v.
func (a *apiB) get(path string, v any) {
	a.e.t.Helper()
	code, body := a.getRaw(path)
	if code != http.StatusOK {
		a.e.t.Fatalf("GET %s = %d %s", path, code, body)
	}
	decodeB(a.e.t, body, v)
}

// getFails reads path, which must fail with status and code.
func (a *apiB) getFails(status int, code domain.ErrorCode, path string) {
	a.e.t.Helper()
	got, body := a.getRaw(path)
	if got != status || !strings.Contains(body, `"code":"`+string(code)+`"`) {
		a.e.t.Errorf("GET %s = %d %s, want %d %s", path, got, body, status, code)
	}
}

func decodeB(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// jsonB renders v as JSON for a request body.
func jsonB(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// The JSON the tests read, as the interface reads it.
type (
	setAnswerB struct {
		Applied      int `json:"applied"`
		SkippedCount int `json:"skipped_count"`
		Skipped      []struct {
			EntryID string `json:"entry_id"`
			Path    string `json:"path"`
			PathB64 []byte `json:"path_b64"`
			Reason  string `json:"reason"`
		} `json:"skipped"`
		BatchID string `json:"batch_id"`
	}
	clearAnswerB struct {
		Cleared int    `json:"cleared"`
		BatchID string `json:"batch_id"`
	}
)

// correctionB is a correction row as the tests compare it.
type correctionB struct {
	kind     string
	setLocal sql.NullString
	shiftS   sql.NullInt64
	batch    string
}

// correctionOfB returns the date_corrections row of id, if any.
func (e *env) correctionOfB(id domain.EntryID) (correctionB, bool) {
	e.t.Helper()
	var c correctionB
	err := e.st.Reader().QueryRow(`SELECT kind, set_local, shift_s, batch_id FROM date_corrections WHERE entry_id = ?`,
		int64(id)).Scan(&c.kind, &c.setLocal, &c.shiftS, &c.batch)
	if err == sql.ErrNoRows {
		return correctionB{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return c, true
}

// setCameraBitsB sets camera_offset on ids as the cameras pass would, and
// stores the recounted summary, as that pass keeps it.
func (e *env) setCameraBitsB(src domain.SourceID, ids []domain.EntryID) {
	e.t.Helper()
	for _, id := range ids {
		e.exec(`UPDATE media_dates SET flags = flags | ? WHERE entry_id = ?`, int64(media.FlagCameraOffset), int64(id))
	}
	e.storeRecountB(src)
}

// storeRecountB stores src's recounted summary.
func (e *env) storeRecountB(src domain.SourceID) {
	e.t.Helper()
	s := e.recounted(src)
	raw, err := json.Marshal(s)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`UPDATE media_sources SET summary = ? WHERE source_id = ?`, string(raw), string(src))
}

// auditsB returns the details of the audit events of kind, oldest first.
func (e *env) auditsB(kind string) []map[string]any {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT detail FROM audit_events WHERE kind = ? ORDER BY id`, kind)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}
