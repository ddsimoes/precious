package decisions

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/commands"
	"precious/internal/jobs"
	"precious/internal/web/clientip"
)

// api serves the command handler with this package's commands registered,
// behind a stand-in for the client address middleware.
type api struct {
	*env
	url  string
	keys int
}

func newAPI(t *testing.T) *api {
	t.Helper()
	e := newEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Clock: e.clk, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	h := commands.New(commands.Options{Store: e.st, Jobs: r, Logger: logger})
	RegisterCommands(h, e.svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", h)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(clientip.With(r.Context(), clientip.Info{Addr: clientAddr, Scheme: "https"})))
	}))
	t.Cleanup(srv.Close)
	return &api{env: e, url: srv.URL}
}

type reply struct {
	status int
	raw    string
	body   map[string]any
}

func (r reply) code() string {
	e, _ := r.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// keys returns the sorted keys of a JSON object.
func keys(v any) []string {
	m, _ := v.(map[string]any)
	return slices.Sorted(maps.Keys(m))
}

// post sends one command with a fresh Idempotency-Key.
func (a *api) post(t *testing.T, name, body string) reply {
	t.Helper()
	a.keys++
	return a.postKey(t, name, "key-"+strconv.Itoa(a.keys), body)
}

func (a *api) postKey(t *testing.T, name, key, body string) reply {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, a.url+"/api/commands/"+name, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := reply{status: resp.StatusCode, raw: string(raw)}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		t.Fatalf("%s: body %q is not JSON: %v", name, raw, err)
	}
	return out
}

func (a *api) want(t *testing.T, name, body string, status int, code string) reply {
	t.Helper()
	r := a.post(t, name, body)
	if r.status != status || r.code() != code {
		t.Errorf("%s %s = %d %s, want %d %s", name, body, r.status, r.raw, status, code)
	}
	return r
}

func TestSetDecisionCommand(t *testing.T) {
	a := newAPI(t)
	s := disk(t, a.env)
	id := func(p string) string { return strconv.Quote(s.ID(p).String()) }
	carta := "HD antigo/Backup_PC_2004/Meus documentos/carta.doc"

	r := a.want(t, CommandSetDecision, `{"entry_id":`+id(carta)+`,"decision":"keep"}`, http.StatusOK, "")
	if r.raw != `{"applied":1,"skipped_count":0,"skipped":[]}` {
		t.Errorf("individual response %s", r.raw)
	}
	r = a.want(t, CommandSetDecision, `{"entry_ids":[`+id(carta)+`,`+id("HD antigo/leiame.txt")+`],"decision":"discard"}`, http.StatusOK, "")
	if keys(r.body)[0] != "applied" || r.body["applied"] != float64(1) || r.body["skipped_count"] != float64(1) {
		t.Errorf("bulk response %s", r.raw)
	}
	sk := r.body["skipped"].([]any)
	if len(sk) != 1 || fmt.Sprint(keys(sk[0])) != "[entry_id path path_b64]" {
		t.Fatalf("skipped %s", r.raw)
	}
	if got := sk[0].(map[string]any); got["entry_id"] != s.ID(carta).String() || got["path"] != carta ||
		got["path_b64"] != base64.StdEncoding.EncodeToString([]byte(carta)) {
		t.Errorf("skipped entry %v", got)
	}
	a.want(t, CommandSetDecision, `{"entry_id":`+id("Fotos")+`,"decision":"inherit"}`, http.StatusOK, "")

	sel := a.want(t, CommandCreateSelection, `{"query":{"within":`+id("Fotos")+`}}`, http.StatusCreated, "")
	r = a.want(t, CommandSetDecision, `{"selection_id":"`+sel.body["selection_id"].(string)+`","decision":"later"}`, http.StatusOK, "")
	if r.body["applied"] != sel.body["count"] {
		t.Errorf("selection response %s, want %v applied", r.raw, sel.body["count"])
	}

	ids := make([]string, MaxEntryIDs+1)
	for i := range ids {
		ids[i] = id("Fotos")
	}
	for _, c := range []struct{ body, code string }{
		{`{"entry_id":` + id("Fotos") + `,"entry_ids":[` + id("Fotos") + `],"decision":"keep"}`, "invalid_request"},
		{`{"decision":"keep"}`, "invalid_request"},
		{`{"entry_ids":[],"decision":"keep"}`, "invalid_request"},
		{`{"entry_ids":[` + strings.Join(ids, ",") + `],"decision":"discard"}`, "invalid_request"},
		{`{"entry_id":` + id("Fotos") + `,"decision":"maybe"}`, "invalid_request"},
		{`{"entry_id":` + id("Fotos") + `}`, "invalid_request"},
		{`{"entry_id":` + id("Fotos") + `,"decision":"keep","extra":1}`, "invalid_request"},
		{`{"entry_id":"abc","entry_ids":["1"],"decision":"keep"}`, "invalid_request"},
		{`{"entry_id":"abc","decision":"keep"}`, "not_found"},
		{`{"entry_ids":[` + id("Fotos") + `,"999999"],"decision":"keep"}`, "not_found"},
		{`{"selection_id":"nope","decision":"keep"}`, "not_found"},
	} {
		want := http.StatusBadRequest
		if c.code == "not_found" {
			want = http.StatusNotFound
		}
		a.want(t, CommandSetDecision, c.body, want, c.code)
	}

	a.clk.Advance(SelectionTTL)
	a.want(t, CommandSetDecision, `{"selection_id":"`+sel.body["selection_id"].(string)+`","decision":"discard"}`,
		http.StatusConflict, "selection_expired")
	checkConsistent(t, a.st)
}

func TestTagCommands(t *testing.T) {
	a := newAPI(t)
	s := disk(t, a.env)
	r := a.want(t, CommandCreateTag, `{"name":"familia"}`, http.StatusCreated, "")
	tagID := r.body["tag"].(map[string]any)["id"].(float64)
	if fmt.Sprint(keys(r.body)) != "[tag]" || r.raw != fmt.Sprintf(`{"tag":{"id":%d,"name":"familia"}}`, int64(tagID)) {
		t.Errorf("create-tag response %s", r.raw)
	}
	a.want(t, CommandCreateTag, `{"name":"Familia"}`, http.StatusConflict, "tag_exists")
	a.want(t, CommandCreateTag, `{"name":""}`, http.StatusBadRequest, "invalid_request")
	a.want(t, CommandCreateTag, `{"name":"`+strings.Repeat("x", MaxTagName+1)+`"}`, http.StatusBadRequest, "invalid_request")
	a.want(t, CommandCreateTag, `{}`, http.StatusBadRequest, "invalid_request")

	tag := strconv.FormatInt(int64(tagID), 10)
	fotos := strconv.Quote(s.ID("Fotos").String())
	r = a.want(t, CommandSetTags, `{"entry_ids":[`+fotos+`],"add":[`+tag+`]}`, http.StatusOK, "")
	if r.raw != `{"applied":1}` {
		t.Errorf("set-tags response %s", r.raw)
	}
	a.want(t, CommandSetTags, `{"entry_ids":[`+fotos+`],"add":[999]}`, http.StatusNotFound, "not_found")
	a.want(t, CommandSetTags, `{"entry_ids":["999999"],"remove":[`+tag+`]}`, http.StatusNotFound, "not_found")
	a.want(t, CommandSetTags, `{"entry_ids":[`+fotos+`]}`, http.StatusBadRequest, "invalid_request")
	a.want(t, CommandSetTags, `{"add":[`+tag+`]}`, http.StatusBadRequest, "invalid_request")
	sel := a.want(t, CommandCreateSelection, `{"query":{"within":`+fotos+`}}`, http.StatusCreated, "")
	r = a.want(t, CommandSetTags, `{"selection_id":"`+sel.body["selection_id"].(string)+`","remove":[`+tag+`]}`, http.StatusOK, "")
	if r.body["applied"] != sel.body["count"] {
		t.Errorf("set-tags on a selection %s, want %v applied", r.raw, sel.body["count"])
	}

	r = a.want(t, CommandRenameTag, `{"tag_id":`+tag+`,"name":"família"}`, http.StatusOK, "")
	if r.raw != `{"tag":{"id":`+tag+`,"name":"família"}}` {
		t.Errorf("rename-tag response %s", r.raw)
	}
	a.want(t, CommandRenameTag, `{"tag_id":999,"name":"x"}`, http.StatusNotFound, "not_found")
	a.want(t, CommandRenameTag, `{"name":"x"}`, http.StatusBadRequest, "invalid_request")
	r = a.want(t, CommandDeleteTag, `{"tag_id":`+tag+`}`, http.StatusOK, "")
	if r.raw != `{"tag":{"id":`+tag+`,"name":"família"}}` {
		t.Errorf("delete-tag response %s", r.raw)
	}
	a.want(t, CommandDeleteTag, `{"tag_id":`+tag+`}`, http.StatusNotFound, "not_found")
	a.want(t, CommandSetTags, `{"entry_ids":[`+fotos+`],"add":[`+tag+`]}`, http.StatusNotFound, "not_found")
}

func TestCreateSelectionCommand(t *testing.T) {
	a := newAPI(t)
	s := disk(t, a.env)
	a.decide(t, one(s.ID("Fotos/IMG_0042.JPG"), keep))
	body := `{"query":{"within":"` + s.ID("Fotos").String() + `","ext":["jpg"]}}`
	r := a.postKey(t, CommandCreateSelection, "sel-1", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create-selection = %d %s", r.status, r.raw)
	}
	if got := fmt.Sprint(keys(r.body)); got != "[bytes count expires_at kept selection_id]" {
		t.Errorf("create-selection keys %s", got)
	}
	if fmt.Sprint(keys(r.body["kept"])) != "[bytes count]" {
		t.Errorf("kept %v", r.body["kept"])
	}
	want := fmt.Sprintf(`{"selection_id":%q,"count":5,"bytes":60,"kept":{"count":1,"bytes":14},"expires_at":%q}`,
		r.body["selection_id"], start.Add(time.Hour).Format(time.RFC3339))
	if r.raw != want {
		t.Errorf("create-selection response %s, want %s", r.raw, want)
	}
	// The dispatcher replays a repeated key without a second selection.
	if again := a.postKey(t, CommandCreateSelection, "sel-1", body); again.raw != r.raw {
		t.Errorf("replay %s, want %s", again.raw, r.raw)
	}
	if n := a.tagCount(t, `SELECT count(*) FROM selections`); n != 1 {
		t.Errorf("%d selections after a replay, want 1", n)
	}

	a.want(t, CommandCreateSelection, `{}`, http.StatusBadRequest, "invalid_request")
	a.want(t, CommandCreateSelection, `{"query":{"file_kind":["pictures"]}}`, http.StatusBadRequest, "invalid_request")
	a.want(t, CommandCreateSelection, `{"query":{"colour":"red"}}`, http.StatusBadRequest, "invalid_request")
}
