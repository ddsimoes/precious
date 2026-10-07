package content

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"precious/internal/commands"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/jobs"
)

type response struct {
	status int
	raw    string
	body   map[string]any
}

func (r response) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// server serves the command API with the content commands over e's runner,
// which is never started, so jobs stay queued.
type server struct {
	e    *env
	url  string
	keys atomic.Int64
}

func (e *env) serve() *server {
	e.t.Helper()
	cmds := commands.New(commands.Options{Store: e.st, Jobs: e.r, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	RegisterCommands(cmds, e.svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", cmds)
	srv := httptest.NewServer(mux)
	e.t.Cleanup(srv.Close)
	return &server{e: e, url: srv.URL}
}

func (s *server) command(name string, body any) response {
	t := s.e.t
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, s.url+"/api/commands/"+name, strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("key-%d", s.keys.Add(1)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := response{status: resp.StatusCode, raw: string(raw)}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		t.Fatalf("%s: body %q is not JSON", name, raw)
	}
	return out
}

func want(t *testing.T, r response, status int, code domain.ErrorCode) {
	t.Helper()
	if r.status != status || (code != "" && r.errCode() != string(code)) {
		t.Fatalf("response %d %s, want %d %s", r.status, r.raw, status, code)
	}
}

// setOffline marks src offline.
func (e *env) setOffline(src domain.SourceID) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(`UPDATE sources SET state = 'offline', mount_point = NULL WHERE id = ?`,
		string(src)); err != nil {
		e.t.Fatal(err)
	}
}

// start-hash returns 202 with the job, coalesces into the active one, and
// refuses an unknown or offline source.
func TestStartHash(t *testing.T) {
	e := newEnv(t)
	e.disk("fotos", "/mnt/fotos", posix)
	e.disk("usb", "/mnt/usb", posix)
	e.setOffline("usb")
	s := e.serve()
	r := s.command(CommandStartHash, map[string]string{"source_id": "fotos"})
	want(t, r, http.StatusAccepted, "")
	if r.body["coalesced"] != false || r.body["state"] != "queued" {
		t.Fatalf("first start-hash %s", r.raw)
	}
	again := s.command(CommandStartHash, map[string]string{"source_id": "fotos"})
	want(t, again, http.StatusAccepted, "")
	if again.body["coalesced"] != true || again.body["job_id"] != r.body["job_id"] {
		t.Errorf("second start-hash %s, first %s", again.raw, r.raw)
	}
	if n := e.count(`SELECT count(*) FROM jobs WHERE kind = 'hash'`); n != 1 {
		t.Errorf("%d hash jobs, want 1", n)
	}
	want(t, s.command(CommandStartHash, map[string]string{"source_id": "usb"}), http.StatusConflict, domain.CodeSourceOffline)
	want(t, s.command(CommandStartHash, map[string]string{"source_id": "nada"}), http.StatusNotFound, domain.CodeUnknownSource)
	want(t, s.command(CommandStartHash, map[string]string{}), http.StatusBadRequest, domain.CodeInvalidRequest)
}

// check-now takes one or two folders, archives, or member folders, starts
// one hash_now job per source, coalesces the folders of a source into its
// active job, and refuses files, counts other than one or two, unknown
// refs, and offline sources.
func TestCheckNow(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	root.Dir("Fotos").File("a.jpg", 10, fileTime)
	root.Dir("Fotos - Copia").File("a.jpg", 10, fileTime)
	root.Dir("Outros")
	root.File("pacote.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "pasta/x.txt", data: []byte("x")}))
	usb := e.disk("usb", "/mnt/usb", posix)
	usb.Dir("Backup")
	off := e.disk("velho", "/mnt/velho", posix)
	off.Dir("Coisas")
	e.scan("fotos")
	e.scan("usb")
	e.scan("velho")
	e.hash("fotos")
	e.setOffline("velho")
	s := e.serve()
	ref := func(src domain.SourceID, p string) string { return e.id(src, p).String() }

	r := s.command(CommandCheckNow, map[string][]string{"entry_ids": {ref("fotos", "Fotos"), ref("usb", "Backup")}})
	want(t, r, http.StatusAccepted, "")
	if js, _ := r.body["jobs"].([]any); len(js) != 2 {
		t.Fatalf("check-now on two sources: %s", r.raw)
	}
	member := e.memberRef("fotos", "pacote.zip", "pasta").String()
	r = s.command(CommandCheckNow, map[string][]string{"entry_ids": {ref("fotos", "Fotos - Copia"), member}})
	want(t, r, http.StatusAccepted, "")
	js, _ := r.body["jobs"].([]any)
	if len(js) != 1 || js[0].(map[string]any)["coalesced"] != true {
		t.Fatalf("check-now coalescing: %s", r.raw)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM jobs WHERE kind = 'hash_now' AND source_id = 'fotos'`).
		Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p nowPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 3 {
		t.Errorf("payload %s; want Fotos, Fotos - Copia, and pacote.zip", payload)
	}
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {ref("fotos", "pacote.zip")}}), http.StatusAccepted, "")

	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {ref("fotos", "Fotos/a.jpg")}}),
		http.StatusBadRequest, domain.CodeInvalidRequest)
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {e.memberRef("fotos", "pacote.zip", "pasta/x.txt").String()}}),
		http.StatusBadRequest, domain.CodeInvalidRequest)
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {ref("fotos", "Fotos"), ref("fotos", "Outros"),
		ref("usb", "Backup")}}), http.StatusBadRequest, domain.CodeInvalidRequest)
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {}}), http.StatusBadRequest, domain.CodeInvalidRequest)
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {"x1"}}), http.StatusBadRequest, domain.CodeInvalidRequest)
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {"999999"}}), http.StatusNotFound, domain.CodeNotFound)
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {"m999999"}}), http.StatusNotFound, domain.CodeNotFound)
	want(t, s.command(CommandCheckNow, map[string][]string{"entry_ids": {ref("velho", "Coisas")}}),
		http.StatusConflict, domain.CodeSourceOffline)
}

// AfterScan and Startup enqueue a hash job for every online source, once.
func TestAfterScanEnqueuesOnlineSources(t *testing.T) {
	e := newEnv(t)
	e.disk("fotos", "/mnt/fotos", posix)
	e.disk("usb", "/mnt/usb", posix)
	e.disk("velho", "/mnt/velho", posix)
	e.setOffline("velho")
	e.svc.AfterScan(context.Background(), "fotos")
	e.svc.AfterScan(context.Background(), "usb")
	if err := e.svc.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Reader().Query(`SELECT source_id, scope_key, state FROM jobs WHERE kind = 'hash' ORDER BY source_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var src, scope, state string
		if err := rows.Scan(&src, &scope, &state); err != nil {
			t.Fatal(err)
		}
		got = append(got, src+" "+scope+" "+state)
	}
	if want := []string{"fotos hash:fotos queued", "usb hash:usb queued"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hash jobs %q, want %q", got, want)
	}
}

// Register makes the runner run hash and hash_now jobs: a started runner
// hashes a source to completion.
func TestRegisterRunsJobs(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	pairOf(root, 1234, "a", "b")
	e.scan("fotos")
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: e.src, Clock: fixedClock{testNow},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TickInterval: 5e6})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(e.st, e.src, fixedClock{testNow}, e.h, e.a, config.Defaults().Duplicates)
	svc.Register(r)
	svc.candidates = e.svc.candidates
	ctx := context.Background()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer r.Stop(ctx)
	if err := svc.Startup(ctx); err != nil {
		t.Fatal(err)
	}
	waitJobs(t, e, KindHash)
	if s := e.state("fotos", "a"); s != domain.ContentHashed {
		t.Errorf("a is %s after the runner's job", s)
	}
	var acc jobs.Accepted
	if err := r.Write(ctx, func(tx *jobs.Tx) error {
		op := &checkNowOp{refs: []domain.Ref{{Entry: e.id("fotos", "")}}}
		_, body, err := op.Apply(ctx, tx)
		if err == nil {
			acc = body.(checkNowJobs).Jobs[0]
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	waitJobs(t, e, KindHashNow)
	if acc.JobID == "" {
		t.Error("no hash_now job")
	}
}

// waitJobs waits until every job of kind is terminal and fails unless all
// succeeded.
func waitJobs(t *testing.T, e *env, kind jobs.Kind) {
	t.Helper()
	for range 2000 {
		if e.count(`SELECT count(*) FROM jobs WHERE kind = ? AND state IN ('queued', 'running', 'paused')`, string(kind)) == 0 {
			if n := e.count(`SELECT count(*) FROM jobs WHERE kind = ? AND state <> 'succeeded'`, string(kind)); n != 0 {
				var detail string
				e.st.Reader().QueryRow(`SELECT coalesce(terminal_detail, '') FROM jobs WHERE kind = ? AND state <> 'succeeded'`,
					string(kind)).Scan(&detail)
				t.Fatalf("%d %s jobs did not succeed: %s", n, kind, detail)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s jobs did not finish", kind)
}
