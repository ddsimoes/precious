package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/jobs"
	"precious/internal/store"
)

const testPassword = "correct horse battery staple"

const builtIndex = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Precious</title>` +
	`<script type="module" crossorigin src="/assets/index-abc.js"></script></head>` +
	`<body><div id="root"></div></body></html>`

// builtUI stands in for a `make ui` build of web/dist.
func builtUI() fs.FS {
	return fstest.MapFS{
		".gitkeep":            {},
		"index.html":          {Data: []byte(builtIndex)},
		"assets/index-abc.js": {Data: []byte("console.log('precious')\n")},
	}
}

// TestServeWiring runs the server stack serve wires: the session endpoints,
// the single-page app, the browser-protection chain, the command dispatcher
// with cancel-job, the job status endpoint, and the event stream. A
// cross-site or token-less command changes nothing (A15).
func TestServeWiring(t *testing.T) {
	var job string
	origin := startServe(t, builtUI(), func(cfg *config.Config) { job = enqueueUnclaimedJob(t, *cfg) })

	c := newBrowser(t, origin)
	for _, path := range []string{"/api/jobs/" + job, "/api/home", "/api/no-such-endpoint"} {
		resp, body := c.fetch(http.MethodGet, path)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"code":"unauthenticated"`) {
			t.Fatalf("GET %s before login = %d %s, want 401 unauthenticated", path, resp.StatusCode, body)
		}
		assertAppHeaders(t, "401 "+path, resp)
	}

	// The shell and its assets are public; client routes deep-link.
	for _, path := range []string{"/", "/map/12", "/login"} {
		resp, body := c.fetch(http.MethodGet, path)
		if resp.StatusCode != http.StatusOK || body != builtIndex || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s = %d %q, Cache-Control %q", path, resp.StatusCode, body, resp.Header.Get("Cache-Control"))
		}
		assertAppHeaders(t, "shell "+path, resp)
	}
	resp, body := c.fetch(http.MethodGet, "/assets/index-abc.js")
	if resp.StatusCode != http.StatusOK || body != "console.log('precious')\n" ||
		resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("GET asset = %d %q, Cache-Control %q", resp.StatusCode, body, resp.Header.Get("Cache-Control"))
	}
	assertAppHeaders(t, "asset", resp)
	if resp, body := c.fetch(http.MethodGet, "/assets/missing.js"); resp.StatusCode != http.StatusNotFound || body == builtIndex {
		t.Fatalf("GET missing asset = %d %q", resp.StatusCode, body)
	}

	csrf := c.login()
	resp, body = c.fetch(http.MethodGet, "/api/no-such-endpoint")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("GET unknown API endpoint = %d %s, want 404 not_found", resp.StatusCode, body)
	}
	assertAppHeaders(t, "api", resp)

	cancelJob := `{"job_id":"` + job + `"}`
	if code, _ := c.command("cancel-job", cancelJob, "https://evil.example", csrf); code != http.StatusForbidden {
		t.Fatalf("cross-site cancel-job = %d, want 403", code)
	}
	if code, _ := c.command("cancel-job", cancelJob, origin, ""); code != http.StatusForbidden {
		t.Fatalf("token-less cancel-job = %d, want 403", code)
	}
	if state := c.jobState(job); state != "queued" {
		t.Fatalf("job after refused commands = %q, want queued", state)
	}

	if code, body := c.command("start-scan", `{"source_id":"old-disk"}`, origin, csrf); code != http.StatusNotFound || !strings.Contains(body, `"code":"unknown_source"`) {
		t.Fatalf("start-scan of an unknown source = %d %s, want 404 unknown_source", code, body)
	}
	if code, body := c.command("cancel-job", `{"job_id":"999"}`, origin, csrf); code != http.StatusNotFound || !strings.Contains(body, "not_found") {
		t.Fatalf("cancel-job of a missing job = %d %s, want 404 not_found", code, body)
	}
	if code, body := c.command("cancel-job", cancelJob, origin, csrf); code != http.StatusAccepted || !strings.Contains(body, `"state":"cancelled"`) {
		t.Fatalf("cancel-job = %d %s, want 202 cancelled", code, body)
	}
	if state := c.awaitJob(job); state != "cancelled" {
		t.Fatalf("event stream ended job %s %q, want cancelled", job, state)
	}
	if state := c.jobState(job); state != "cancelled" {
		t.Fatalf("GET /api/jobs/%s state = %q, want cancelled", job, state)
	}

	c.logout(csrf)
	if resp, _ := c.fetch(http.MethodGet, "/api/jobs/"+job); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/jobs/%s after logout = %d, want 401", job, resp.StatusCode)
	}
	if s := c.session(); s.Authenticated {
		t.Fatalf("GET /api/session after logout = %+v", s)
	}
}

// Without a built UI, every page is the "build the UI" page.
func TestServePlaceholderShell(t *testing.T) {
	origin := startServe(t, fstest.MapFS{".gitkeep": {}}, func(*config.Config) {})
	c := newBrowser(t, origin)
	for _, path := range []string{"/", "/map/12"} {
		resp, body := c.fetch(http.MethodGet, path)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "make ui") || strings.Contains(body, "<script") {
			t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
		}
		assertAppHeaders(t, "placeholder "+path, resp)
	}
}

// assertAppHeaders checks the application security headers of D12/D13.
func assertAppHeaders(t *testing.T, name string, resp *http.Response) {
	t.Helper()
	const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; media-src 'self'; " +
		"frame-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"
	if got := resp.Header.Get("Content-Security-Policy"); got != csp {
		t.Errorf("%s: CSP = %q", name, got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options = %q", name, got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("%s: Referrer-Policy = %q", name, got)
	}
	for k := range resp.Header {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Errorf("%s: CORS header %s present", name, k)
		}
	}
}

// startServe runs serve on a loopback listener in local HTTP mode with ui as
// the embedded UI until the test ends, after calling prepare on the
// configuration and its state and setting the administrator password, and
// returns the external origin.
func startServe(t *testing.T, ui fs.FS, prepare func(*config.Config)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + ln.Addr().String()
	cfg := config.Defaults()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.Server.Listen = ln.Addr().String()
	cfg.Server.ExternalOrigin = origin
	cfg.Server.AllowInsecureHTTP = true
	prepare(&cfg)
	if err := config.Validate(&cfg); err != nil {
		t.Fatal(err)
	}
	setAdminPassword(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- serve(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), serveDeps{
			Clock: clock.Real{}, UI: ui, Listener: ln, Ready: func(string) { close(ready) },
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("serve exited early: %v", err)
	}
	return origin
}

func setAdminPassword(t *testing.T, cfg config.Config) {
	t.Helper()
	st := openTestStore(t, cfg)
	if _, err := auth.New(st, clock.Real{}, cfg.Auth, auth.Options{}).SetPassword(context.Background(), []byte(testPassword)); err != nil {
		t.Fatal(err)
	}
}

// enqueueUnclaimedJob stores a queued job of a kind nobody registers, so the
// server's runner never claims it, and returns its ID.
func enqueueUnclaimedJob(t *testing.T, cfg config.Config) string {
	t.Helper()
	r, err := jobs.NewRunner(jobs.Options{Store: openTestStore(t, cfg), Config: cfg.Jobs})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.Enqueue(context.Background(), jobs.Spec{Kind: "unregistered"})
	if err != nil {
		t.Fatal(err)
	}
	return rec.ID.String()
}

func openTestStore(t *testing.T, cfg config.Config) *store.Store {
	t.Helper()
	if err := config.EnsureStateDir(cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.StateDir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// browser is a minimal same-origin HTTP client with a cookie jar, used the
// way the single-page app uses the API.
type browser struct {
	t      *testing.T
	origin string
	hc     *http.Client
}

func newBrowser(t *testing.T, origin string) *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, origin: origin, hc: &http.Client{
		Jar:           jar,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// send runs req and returns the response, whose body is already read and
// closed, and that body.
func (b *browser) send(req *http.Request) (*http.Response, string) {
	b.t.Helper()
	resp, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp, string(body)
}

func (b *browser) fetch(method, path string) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequest(method, b.origin+path, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.send(req)
}

// post sends a same-origin JSON POST with the CSRF header.
func (b *browser) post(path, body, origin, csrf string) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.origin+path, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	return b.send(req)
}

func (b *browser) jobState(id string) string {
	b.t.Helper()
	resp, body := b.fetch(http.MethodGet, "/api/jobs/"+id)
	var rec struct {
		State string `json:"state"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &rec) != nil {
		b.t.Fatalf("GET /api/jobs/%s = %d: %s", id, resp.StatusCode, body)
	}
	return rec.State
}

type sessionState struct {
	Authenticated bool   `json:"authenticated"`
	CSRFToken     string `json:"csrf_token"`
	AdminExists   bool   `json:"admin_exists"`
}

// session calls GET /api/session, the app's bootstrap.
func (b *browser) session() sessionState {
	b.t.Helper()
	resp, body := b.fetch(http.MethodGet, "/api/session")
	var s sessionState
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &s) != nil || s.CSRFToken == "" {
		b.t.Fatalf("GET /api/session = %d: %s", resp.StatusCode, body)
	}
	return s
}

// login signs in with the pre-login session's CSRF token from GET
// /api/session and returns the authenticated session's token.
func (b *browser) login() string {
	b.t.Helper()
	pre := b.session()
	if pre.Authenticated || !pre.AdminExists {
		b.t.Fatalf("GET /api/session before login = %+v", pre)
	}
	resp, body := b.post("/api/session/login", `{"password":"`+testPassword+`"}`, b.origin, pre.CSRFToken)
	var lr struct {
		Authenticated bool   `json:"authenticated"`
		CSRFToken     string `json:"csrf_token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &lr) != nil || !lr.Authenticated {
		b.t.Fatalf("login = %d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Set-Cookie"); !strings.HasPrefix(got, "precious_session=") {
		b.t.Fatalf("login Set-Cookie = %q", got)
	}
	if now := b.session(); !now.Authenticated || now.CSRFToken != lr.CSRFToken {
		b.t.Fatalf("GET /api/session after login = %+v, login token %q", now, lr.CSRFToken)
	}
	return lr.CSRFToken
}

func (b *browser) logout(csrf string) {
	b.t.Helper()
	resp, body := b.post("/api/session/logout", "", b.origin, csrf)
	if resp.StatusCode != http.StatusNoContent {
		b.t.Fatalf("logout = %d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Set-Cookie"); !strings.HasPrefix(got, "precious_session=;") || !strings.Contains(got, "Max-Age=0") {
		b.t.Fatalf("logout Set-Cookie = %q", got)
	}
}

// commandSeq makes every command in a test carry a fresh idempotency key.
var commandSeq int

func (b *browser) command(name, body, origin, csrf string) (int, string) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.origin+"/api/commands/"+name, strings.NewReader(body))
	if err != nil {
		b.t.Fatal(err)
	}
	commandSeq++
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "serve-test-"+strconv.Itoa(commandSeq))
	req.Header.Set("Origin", origin)
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, out := b.send(req)
	return resp.StatusCode, out
}

// awaitJob follows the SSE stream until job id reaches a terminal or paused
// state and returns that state.
func (b *browser) awaitJob(id string) string {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, b.origin+"/api/events", nil)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", "0")
	resp, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("GET /api/events = %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			JobID string `json:"job_id"`
			State string `json:"state"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil || ev.JobID != id {
			continue
		}
		switch ev.State {
		case "succeeded", "failed", "cancelled", "paused":
			return ev.State
		}
	}
	b.t.Fatalf("event stream ended before job %s finished: %v", id, sc.Err())
	return ""
}
