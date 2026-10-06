package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

const waitTimeout = 10 * time.Second

// enqueueTest is a command the tests register through Handler.Register, as a
// slice package registers its own: {"key": "<scope key>"} returns the active
// job of kind testKind with that scope key, or enqueues one.
const enqueueTest = "enqueue-test"

const testKind jobs.Kind = "test"

type enqueueRequest struct {
	Key string `json:"key"`
}

type enqueueOp struct{ req enqueueRequest }

func decodeEnqueue(body []byte) (Operation, error) {
	var req enqueueRequest
	if err := DecodeStrict(body, &req); err != nil {
		return nil, err
	}
	if req.Key == "" {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "key is required")
	}
	return &enqueueOp{req: req}, nil
}

func (o *enqueueOp) Canonical() []byte {
	b, err := json.Marshal(o.req)
	if err != nil {
		panic(err)
	}
	return b
}

func (o *enqueueOp) Prepare(context.Context) error { return nil }

func (o *enqueueOp) Apply(_ context.Context, tx *jobs.Tx) (int, any, error) {
	rec, coalesced, err := tx.EnqueueOnce(jobs.Spec{Kind: testKind, ScopeKey: o.req.Key})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, rec.Accepted(coalesced), nil
}

type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

type handlerFunc func(ctx context.Context, job jobs.Job, rt jobs.Runtime) error

func (f handlerFunc) Run(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	return f(ctx, job, rt)
}

type env struct {
	st  *store.Store
	r   *jobs.Runner
	url string
}

// newEnv serves the command API, with enqueue-test registered, and the job
// status API. With a handler of testKind the runner is started; without
// one, jobs stay queued.
func newEnv(t *testing.T, h jobs.Handler) *env {
	t.Helper()
	st := storetest.Open(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := jobs.NewRunner(jobs.Options{
		Store:  st,
		Config: config.Defaults().Jobs,
		Clock:  fakeClock{time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)},
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		r.Register(testKind, h)
		if err := r.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
			defer cancel()
			if err := r.Stop(ctx); err != nil {
				t.Errorf("Stop: %v", err)
			}
		})
	}
	cmds := New(Options{Store: st, Jobs: r, MaxBodyBytes: 1024, Logger: logger})
	cmds.Register(enqueueTest, decodeEnqueue)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", cmds)
	mux.Handle("GET /api/jobs/{id}", jobs.NewStatusHandler(r))
	mux.Handle("GET /api/events", jobs.NewEventsHandler(r))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{st: st, r: r, url: srv.URL}
}

type response struct {
	status int
	raw    string
	body   map[string]any
}

func (e *env) post(t *testing.T, name, key, body string) response {
	t.Helper()
	r, err := e.send(name, key, body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// send is post without *testing.T, for use from other goroutines.
func (e *env) send(name, key, body string) (response, error) {
	req, err := http.NewRequest(http.MethodPost, e.url+"/api/commands/"+name, strings.NewReader(body))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return do(req)
}

func (e *env) get(t *testing.T, path string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := do(req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func do(req *http.Request) (response, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return response{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, err
	}
	out := response{status: resp.StatusCode, raw: string(raw)}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		return response{}, fmt.Errorf("%s %s: body %q is not JSON: %v", req.Method, req.URL.Path, raw, err)
	}
	return out, nil
}

func (r response) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitState follows the event stream from its start until the job reaches want.
func (e *env) waitState(t *testing.T, id string, want domain.JobState) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev jobs.Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("event %q: %v", data, err)
		}
		if ev.JobID == id && ev.State == want {
			return
		}
	}
	t.Fatalf("job %s never reached %s: %v", id, want, sc.Err())
}

// A registered command runs through the dispatcher: 202 with the job it
// created, visible through the job status API.
func TestRegisteredCommandAccepted(t *testing.T) {
	e := newEnv(t, nil)
	resp := e.post(t, enqueueTest, "k1", `{"key":"a"}`)
	if resp.status != http.StatusAccepted || resp.body["state"] != "queued" || resp.body["coalesced"] != false {
		t.Fatalf("enqueue-test = %d %s", resp.status, resp.raw)
	}
	id, _ := resp.body["job_id"].(string)
	job := e.get(t, "/api/jobs/"+id)
	if job.status != http.StatusOK || job.body["id"] != id || job.body["kind"] != string(testKind) || job.body["state"] != "queued" {
		t.Fatalf("GET /api/jobs/%s = %d %s", id, job.status, job.raw)
	}
}

// TestA16RetriedCommandAfterLostResponse covers A16 duplicate idempotency keys
// (spec scenario "Retried command after lost response").
func TestA16RetriedCommandAfterLostResponse(t *testing.T) {
	e := newEnv(t, nil)
	first := e.post(t, enqueueTest, "retry-key", `{"key":"a"}`)
	if first.status != http.StatusAccepted {
		t.Fatalf("first = %d %s", first.status, first.raw)
	}
	id := first.body["job_id"].(string)
	jobID, _ := domain.ParseJobID(id)
	if _, err := e.r.Cancel(context.Background(), jobID); err != nil {
		t.Fatal(err)
	}

	// Same key and payload, reformatted: the stored outcome is replayed even
	// though the job has changed since, and nothing new happens.
	again := e.post(t, enqueueTest, "retry-key", "{ \"key\" : \"a\" }\n")
	if again.status != first.status || again.raw != first.raw {
		t.Fatalf("replay = %d %s, want %d %s", again.status, again.raw, first.status, first.raw)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM jobs`); n != 1 {
		t.Fatalf("jobs = %d, want 1", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM command_requests`); n != 1 {
		t.Fatalf("command_requests = %d, want 1", n)
	}

	cancel := e.post(t, CancelJob, "cancel-key", `{"job_id":"`+id+`"}`)
	cancelAgain := e.post(t, CancelJob, "cancel-key", `{"job_id":"`+id+`"}`)
	if cancel.status != http.StatusAccepted || cancel.body["state"] != "cancelled" || cancelAgain.raw != cancel.raw {
		t.Fatalf("cancel = %d %s, replay %s", cancel.status, cancel.raw, cancelAgain.raw)
	}
}

// Spec scenario "Key reused with different payload".
func TestKeyReusedWithDifferentPayload(t *testing.T) {
	e := newEnv(t, nil)
	if r := e.post(t, enqueueTest, "k", `{"key":"a"}`); r.status != http.StatusAccepted {
		t.Fatalf("first = %d %s", r.status, r.raw)
	}
	for _, tc := range []struct{ name, body string }{
		{enqueueTest, `{"key":"b"}`},
		{CancelJob, `{"job_id":"1"}`},
	} {
		r := e.post(t, tc.name, "k", tc.body)
		if r.status != http.StatusConflict || r.errCode() != "idempotency_key_reused" {
			t.Fatalf("%s with reused key = %d %s, want 409 idempotency_key_reused", tc.name, r.status, r.raw)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM jobs WHERE scope_key = 'b'`); n != 0 {
		t.Fatalf("reused key created %d jobs for b", n)
	}
	if got := e.get(t, "/api/jobs/1").body["state"]; got != "queued" {
		t.Fatalf("reused key changed job 1 to %v", got)
	}
}

func TestInvalidRequests(t *testing.T) {
	e := newEnv(t, nil)
	for _, tc := range []struct {
		desc, name, key, body string
		status                int
		code                  string
	}{
		{"missing key", enqueueTest, "", `{"key":"a"}`, 400, "invalid_request"},
		{"key with spaces", enqueueTest, "a b", `{"key":"a"}`, 400, "invalid_request"},
		{"key too long", enqueueTest, strings.Repeat("k", maxKeyLen+1), `{"key":"a"}`, 400, "invalid_request"},
		{"malformed JSON", enqueueTest, "k1", `{"key":`, 400, "invalid_request"},
		{"unknown field", enqueueTest, "k2", `{"key":"a","path":"/etc"}`, 400, "invalid_request"},
		{"decoder refusal", enqueueTest, "k3", `{"key":""}`, 400, "invalid_request"},
		{"trailing data", enqueueTest, "k4", `{"key":"a"} {}`, 400, "invalid_request"},
		{"too large", enqueueTest, "k5", `{"key":"` + strings.Repeat("a", 2048) + `"}`, 413, "request_too_large"},
		{"missing job", CancelJob, "k6", `{"job_id":""}`, 400, "invalid_request"},
		{"cancel unknown field", CancelJob, "k8", `{"job_id":"1","force":true}`, 400, "invalid_request"},
		{"unknown command", "delete-everything", "k7", `{}`, 404, "not_found"},
	} {
		r := e.post(t, tc.name, tc.key, tc.body)
		if r.status != tc.status || r.errCode() != tc.code {
			t.Errorf("%s: %d %s, want %d %s", tc.desc, r.status, r.raw, tc.status, tc.code)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM jobs`) + e.count(t, `SELECT COUNT(*) FROM command_requests`); n != 0 {
		t.Fatalf("rejected requests left %d rows", n)
	}
}

// Concurrent requests with distinct keys go through one write transaction
// each: a single-flight command creates one job, and every response names it.
func TestConcurrentRequests(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e := newEnv(t, handlerFunc(func(context.Context, jobs.Job, jobs.Runtime) error {
		entered <- struct{}{}
		<-release
		return nil
	}))
	defer close(release)

	first := e.post(t, enqueueTest, "first", `{"key":"a"}`)
	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("job did not start")
	}
	second := e.post(t, enqueueTest, "second", `{"key":"a"}`)
	if second.status != http.StatusAccepted || second.body["job_id"] != first.body["job_id"] ||
		second.body["state"] != "running" || second.body["coalesced"] != true {
		t.Fatalf("enqueue-test while running = %d %s, want job %v coalesced", second.status, second.raw, first.body["job_id"])
	}

	const n = 16
	var wg sync.WaitGroup
	results := make([]response, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			results[i], errs[i] = e.send(enqueueTest, fmt.Sprintf("burst-%d", i), `{"key":"b"}`)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, r := range results {
		if r.status != http.StatusAccepted || r.body["job_id"] != results[0].body["job_id"] {
			t.Fatalf("burst response %d %s, want job %v", r.status, r.raw, results[0].body["job_id"])
		}
		if r.body["coalesced"] == false {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("%d responses report a new job, want 1", created)
	}
	if got := e.count(t, `SELECT COUNT(*) FROM jobs`); got != 2 {
		t.Fatalf("jobs = %d, want one per key", got)
	}
	if got := e.count(t, `SELECT COUNT(*) FROM command_requests`); got != n+2 {
		t.Fatalf("command_requests = %d, want %d", got, n+2)
	}
}

func TestCancelJob(t *testing.T) {
	entered := make(chan struct{}, 1)
	e := newEnv(t, handlerFunc(func(ctx context.Context, _ jobs.Job, _ jobs.Runtime) error {
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}))
	id := e.post(t, enqueueTest, "enqueue", `{"key":"a"}`).body["job_id"].(string)
	select {
	case <-entered:
	case <-time.After(waitTimeout):
		t.Fatal("job did not start")
	}
	r := e.post(t, CancelJob, "cancel", `{"job_id":"`+id+`"}`)
	if r.status != http.StatusAccepted || r.body["job_id"] != id || r.body["state"] != "cancel_requested" {
		t.Fatalf("cancel running = %d %s", r.status, r.raw)
	}
	e.waitState(t, id, domain.JobCancelled)

	again := e.post(t, CancelJob, "cancel-terminal", `{"job_id":"`+id+`"}`)
	if again.status != http.StatusAccepted || again.body["state"] != "cancelled" {
		t.Fatalf("cancel of a cancelled job = %d %s, want 202 cancelled", again.status, again.raw)
	}

	for _, body := range []string{`{"job_id":"999"}`, `{"job_id":"abc"}`} {
		r := e.post(t, CancelJob, "cancel-"+body, body)
		if r.status != http.StatusNotFound || r.errCode() != "not_found" {
			t.Fatalf("cancel %s = %d %s, want 404 not_found", body, r.status, r.raw)
		}
	}
}
