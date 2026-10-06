package authhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"precious/internal/auth"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/store"
	"precious/internal/web/apierr"
	"precious/internal/web/clientip"
	"precious/internal/web/middleware"
)

const (
	httpsOrigin = "https://precious.example.net"
	password    = "correct horse battery staple"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// countingHasher counts password verifications.
type countingHasher struct {
	auth.PasswordHasher
	verifies atomic.Int64
}

func (c *countingHasher) Verify(ctx context.Context, enc string, pw []byte) (bool, bool, error) {
	c.verifies.Add(1)
	return c.PasswordHasher.Verify(ctx, enc, pw)
}

type harness struct {
	h      http.Handler
	svc    *auth.Service
	st     *store.Store
	clk    *fakeClock
	hasher *countingHasher
	origin string
	cookie middleware.SessionCookie
}

var defaultClient = netip.MustParseAddr("203.0.113.7")

// newHarness assembles the documented middleware order around the session
// endpoints and a protected API route.
func newHarness(t *testing.T, origin string) *harness {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	hasher := &countingHasher{PasswordHasher: auth.NewHasher(auth.Params{Memory: 64, Time: 1, Threads: 1})}
	svc := auth.New(st, clk, config.Defaults().Auth, auth.Options{Hasher: hasher})
	o, err := middleware.ParseOrigin(origin)
	if err != nil {
		t.Fatal(err)
	}
	cookie := middleware.NewSessionCookie(o)

	mux := http.NewServeMux()
	Register(mux, svc, cookie)
	mux.HandleFunc("GET /api/home", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"sources":[]}`)
	})

	var h http.Handler = mux
	h = middleware.CSRF(o)(h)
	h = middleware.RequireAuth(h)
	h = middleware.LoadSession(svc, cookie)(h)
	h = middleware.BodyLimit(1 << 20)(h)
	h = withClient(h) // stands in for the clientip middleware
	h = middleware.SecurityHeaders(h)
	return &harness{h: h, svc: svc, st: st, clk: clk, hasher: hasher, origin: o.String(), cookie: cookie}
}

// withClient derives the client address from a test-only header.
func withClient(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := defaultClient
		if v := r.Header.Get("X-Test-Client"); v != "" {
			addr = netip.MustParseAddr(v)
		}
		next.ServeHTTP(w, r.WithContext(clientip.With(r.Context(), clientip.Info{Addr: addr, Scheme: "https"})))
	})
}

func (h *harness) setPassword(t *testing.T) {
	t.Helper()
	if _, err := h.svc.SetPassword(context.Background(), []byte(password)); err != nil {
		t.Fatal(err)
	}
}

// browser plays the single-page app: it keeps the session cookie and the
// CSRF token of the last session or login response.
type browser struct {
	t     *testing.T
	h     *harness
	addr  string // client address; defaultClient when empty
	token string
	csrf  string
}

// send serves r with the browser's cookie and client address, and records a
// new session cookie.
func (b *browser) send(r *http.Request) *httptest.ResponseRecorder {
	b.t.Helper()
	if b.token != "" {
		r.AddCookie(&http.Cookie{Name: b.h.cookie.Name, Value: b.token})
	}
	if b.addr != "" {
		r.Header.Set("X-Test-Client", b.addr)
	}
	rec := httptest.NewRecorder()
	b.h.h.ServeHTTP(rec, r)
	for _, c := range rec.Result().Cookies() {
		if c.Name == b.h.cookie.Name {
			b.token = c.Value
		}
	}
	return rec
}

func (b *browser) get(target string) *httptest.ResponseRecorder {
	b.t.Helper()
	return b.send(httptest.NewRequest(http.MethodGet, b.h.origin+target, nil))
}

// post sends a same-origin JSON POST carrying the browser's CSRF token.
func (b *browser) post(target, body string) *httptest.ResponseRecorder {
	b.t.Helper()
	r := httptest.NewRequest(http.MethodPost, b.h.origin+target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", b.h.origin)
	r.Header.Set(middleware.CSRFHeader, b.csrf)
	return b.send(r)
}

// session calls GET /api/session and keeps its CSRF token.
func (b *browser) session() sessionResponse {
	b.t.Helper()
	rec := b.get("/api/session")
	if rec.Code != http.StatusOK {
		b.t.Fatalf("GET /api/session = %d %s", rec.Code, rec.Body)
	}
	assertJSON(b.t, "GET /api/session", rec)
	var s sessionResponse
	decode(b.t, rec, &s)
	if s.CSRFToken == "" {
		b.t.Fatal("GET /api/session returned no CSRF token")
	}
	b.csrf = s.CSRFToken
	return s
}

// login posts pw and, on success, keeps the new CSRF token.
func (b *browser) login(pw string) *httptest.ResponseRecorder {
	b.t.Helper()
	body, err := json.Marshal(loginRequest{Password: pw})
	if err != nil {
		b.t.Fatal(err)
	}
	rec := b.post("/api/session/login", string(body))
	if rec.Code == http.StatusOK {
		var lr loginResponse
		decode(b.t, rec, &lr)
		b.csrf = lr.CSRFToken
	}
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
}

func assertJSON(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("%s Content-Type = %q", name, got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s Cache-Control = %q", name, got)
	}
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) apierr.Detail {
	t.Helper()
	var body apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", rec.Body, err)
	}
	return body.Error
}

func countAudit(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	var n int
	if err := st.Reader().QueryRow(`SELECT count(*) FROM audit_events WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// "Session bootstrap is public" and "First visit gets a pre-login session":
// the first call sets a pre-login cookie; later calls reuse that session.
func TestSessionBootstrap(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	h.setPassword(t)
	b := &browser{t: t, h: h}
	first := b.session()
	if first.Authenticated || !first.AdminExists || b.token == "" {
		t.Fatalf("first GET /api/session = %+v, cookie %q", first, b.token)
	}
	pre := b.token
	rec := b.get("/api/session")
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("second GET /api/session set a cookie: %q", rec.Header().Get("Set-Cookie"))
	}
	var again sessionResponse
	decode(t, rec, &again)
	if again != first || b.token != pre {
		t.Fatalf("second GET /api/session = %+v, first %+v", again, first)
	}
	if rec := b.get("/api/home"); rec.Code != http.StatusUnauthorized || errorOf(t, rec).Code != domain.CodeUnauthenticated {
		t.Fatalf("GET /api/home with a pre-login session = %d %s", rec.Code, rec.Body)
	}
}

// An unauthenticated /api request gets 401 JSON with no data.
func TestUnauthenticatedAPI(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	rec := (&browser{t: t, h: h}).get("/api/home")
	if rec.Code != http.StatusUnauthorized || errorOf(t, rec).Code != domain.CodeUnauthenticated {
		t.Fatalf("GET /api/home = %d %s", rec.Code, rec.Body)
	}
	assertJSON(t, "GET /api/home", rec)
	if strings.Contains(rec.Body.String(), "sources") {
		t.Fatalf("401 leaked data: %s", rec.Body)
	}
}

// Task 8.4 "HTTPS origin cookie attributes", the plain cookie of local HTTP
// mode, and the login/logout round trip.
func TestLoginLogoutRoundTrip(t *testing.T) {
	for _, tc := range []struct{ origin, name, attrs string }{
		{httpsOrigin, "__Host-precious_session", "; Path=/; HttpOnly; Secure; SameSite=Strict"},
		{"http://127.0.0.1:8080", "precious_session", "; Path=/; HttpOnly; SameSite=Strict"},
	} {
		set := regexp.MustCompile(`^` + tc.name + `=[A-Za-z0-9_-]{43}` + regexp.QuoteMeta(tc.attrs) + `$`)
		h := newHarness(t, tc.origin)
		h.setPassword(t)
		b := &browser{t: t, h: h}
		pre := b.get("/api/session")
		if got := pre.Header().Get("Set-Cookie"); !set.MatchString(got) {
			t.Errorf("%s: pre-login Set-Cookie = %q", tc.origin, got)
		}
		var s sessionResponse
		decode(t, pre, &s)
		b.csrf = s.CSRFToken

		rec := b.login(password)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: login = %d %s", tc.origin, rec.Code, rec.Body)
		}
		assertJSON(t, "login", rec)
		if got := rec.Header().Get("Set-Cookie"); !set.MatchString(got) {
			t.Errorf("%s: login Set-Cookie = %q", tc.origin, got)
		}
		if b.csrf == s.CSRFToken {
			t.Errorf("%s: login kept the pre-login CSRF token", tc.origin)
		}
		if now := b.session(); !now.Authenticated || !now.AdminExists {
			t.Fatalf("%s: GET /api/session after login = %+v", tc.origin, now)
		}
		if rec := b.get("/api/home"); rec.Code != http.StatusOK {
			t.Fatalf("%s: GET /api/home after login = %d", tc.origin, rec.Code)
		}

		rec = b.post("/api/session/logout", "")
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("%s: logout = %d %s", tc.origin, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Set-Cookie"); got != tc.name+"=; Path=/; Max-Age=0"+strings.TrimPrefix(tc.attrs, "; Path=/") {
			t.Errorf("%s: logout Set-Cookie = %q", tc.origin, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: logout Cache-Control = %q", tc.origin, got)
		}
	}
}

// Task 8.3 "Session rotation on login" through HTTP.
func TestLoginRotatesSession(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	h.setPassword(t)
	b := &browser{t: t, h: h}
	b.session()
	preToken := b.token
	if rec := b.login(password); rec.Code != http.StatusOK {
		t.Fatalf("login = %d", rec.Code)
	}
	if b.token == preToken {
		t.Fatal("login kept the pre-login token")
	}
	old := &browser{t: t, h: h, token: preToken}
	if rec := old.get("/api/home"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("pre-login token after login: %d", rec.Code)
	}
	if s := old.session(); s.Authenticated || old.token == preToken {
		t.Fatalf("pre-login token after login: session %+v, cookie reused %v", s, old.token == preToken)
	}
	if rec := b.get("/api/home"); rec.Code != http.StatusOK {
		t.Fatalf("new token: %d", rec.Code)
	}
}

// Task 8.3 "Logout revokes": the replayed cookie is unauthenticated, and a
// logout without the CSRF header changes nothing.
func TestLogoutReplay(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	h.setPassword(t)
	b := &browser{t: t, h: h}
	b.session()
	b.login(password)
	token := b.token

	r := httptest.NewRequest(http.MethodPost, h.origin+"/api/session/logout", nil)
	r.Header.Set("Origin", h.origin)
	if rec := b.send(r); rec.Code != http.StatusForbidden || errorOf(t, rec).Code != domain.CodeForbidden {
		t.Fatalf("logout without CSRF header = %d %s", rec.Code, rec.Body)
	}
	if rec := b.post("/api/session/logout", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body)
	}
	replay := &browser{t: t, h: h, token: token}
	if rec := replay.get("/api/home"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed API request = %d", rec.Code)
	}
	replay.csrf = b.csrf
	if rec := replay.post("/api/session/logout", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed logout = %d", rec.Code)
	}
	if s := replay.session(); s.Authenticated {
		t.Fatalf("replayed GET /api/session = %+v", s)
	}
	if n := countAudit(t, h.st, auth.AuditLogout); n != 1 {
		t.Fatalf("logout audit rows = %d", n)
	}
}

// Task 8.7 "Repeated failures throttled": ten quick wrong passwords run
// Argon2 once; the other nine are refused before verification with the same
// generic response, and every attempt is audited without the password.
func TestLoginThrottledSkipsVerification(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	h.setPassword(t)
	b := &browser{t: t, h: h}
	b.session()

	const wrong = "wrong-password-0123456789"
	var first string
	for i := range 10 {
		rec := b.login(wrong)
		if rec.Code != http.StatusUnauthorized || errorOf(t, rec) != (apierr.Detail{Code: domain.CodeLoginFailed, Message: LoginFailedMessage}) {
			t.Fatalf("attempt %d: %d %s", i+1, rec.Code, rec.Body)
		}
		if i == 0 {
			first = rec.Body.String()
		} else if rec.Body.String() != first {
			t.Fatalf("attempt %d response differs from a plain wrong-password response", i+1)
		}
	}
	if n := h.hasher.verifies.Load(); n != 1 {
		t.Fatalf("password verifications = %d, want 1 (the rest throttled before Argon2)", n)
	}
	// The account key throttles other addresses too, even with the right password.
	other := &browser{t: t, h: h, addr: "198.51.100.9"}
	other.session()
	if rec := other.login(password); rec.Code != http.StatusUnauthorized || rec.Body.String() != first {
		t.Fatalf("other address during the account backoff: %d %s", rec.Code, rec.Body)
	}
	if n := h.hasher.verifies.Load(); n != 1 {
		t.Fatalf("password verifications = %d after a throttled attempt from another address", n)
	}

	if n := countAudit(t, h.st, auth.AuditLoginFailed); n != 1 {
		t.Fatalf("login_failed rows = %d", n)
	}
	if n := countAudit(t, h.st, auth.AuditLoginThrottled); n != 10 {
		t.Fatalf("login_throttled rows = %d", n)
	}
	rows, err := h.st.Reader().Query(`SELECT occurred_at, kind, actor, coalesce(client_addr, ''), detail FROM audit_events WHERE kind LIKE 'login_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	perAddr := map[string]int{}
	for rows.Next() {
		var at int64
		var kind, actor, addr, detail string
		if err := rows.Scan(&at, &kind, &actor, &addr, &detail); err != nil {
			t.Fatal(err)
		}
		perAddr[addr]++
		if at != h.clk.Now().UnixMilli() || actor != auth.ActorAnonymous {
			t.Errorf("%s row: at %d actor %q", kind, at, actor)
		}
		for _, secret := range []string{wrong, password, b.token, b.csrf, other.token, other.csrf} {
			if strings.Contains(kind+actor+addr+detail, secret) {
				t.Errorf("%s row leaks a secret: %s", kind, detail)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if perAddr[defaultClient.String()] != 10 || perAddr["198.51.100.9"] != 1 {
		t.Fatalf("audit rows per client address = %v", perAddr)
	}

	// Once the backoff window elapses the correct password works again.
	h.clk.Advance(time.Second)
	if rec := b.login(password); rec.Code != http.StatusOK {
		t.Fatalf("login after the window = %d", rec.Code)
	}
	if n := h.hasher.verifies.Load(); n != 2 {
		t.Fatalf("verifications = %d, want 2", n)
	}
}

// "Fresh install has no usable login": GET /api/session reports no
// administrator, and every attempt fails with the same generic login_failed.
func TestFreshInstallLoginFails(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	b := &browser{t: t, h: h}
	if s := b.session(); s.AdminExists || s.Authenticated {
		t.Fatalf("GET /api/session without administrator = %+v", s)
	}
	rec := b.login(password)
	if rec.Code != http.StatusUnauthorized || errorOf(t, rec) != (apierr.Detail{Code: domain.CodeLoginFailed, Message: LoginFailedMessage}) {
		t.Fatalf("login without administrator: %d %s", rec.Code, rec.Body)
	}
	assertJSON(t, "failed login", rec)
	if rec := b.get("/api/home"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("API after failed login: %d", rec.Code)
	}
	if n := h.hasher.verifies.Load(); n != 0 {
		t.Fatalf("verifications = %d without a stored credential", n)
	}
}

// "Login without CSRF token": login is a state-changing request; it needs the
// pre-login session's token in the X-CSRF-Token header and a same-origin
// Origin, and a refused post neither verifies nor audits.
func TestLoginRequiresCSRF(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	h.setPassword(t)
	b := &browser{t: t, h: h}
	b.session()
	pre := b.token
	body := `{"password":"` + password + `"}`

	noHeader := httptest.NewRequest(http.MethodPost, h.origin+"/api/session/login", strings.NewReader(body))
	noHeader.Header.Set("Content-Type", "application/json")
	noHeader.Header.Set("Origin", h.origin)
	form := httptest.NewRequest(http.MethodPost, h.origin+"/api/session/login",
		strings.NewReader(url.Values{"csrf_token": {b.csrf}, "password": {password}}.Encode()))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.Header.Set("Origin", h.origin)
	evil := httptest.NewRequest(http.MethodPost, h.origin+"/api/session/login", strings.NewReader(body))
	evil.Header.Set("Content-Type", "application/json")
	evil.Header.Set("Origin", "https://evil.example")
	evil.Header.Set(middleware.CSRFHeader, b.csrf)
	noSession := httptest.NewRequest(http.MethodPost, h.origin+"/api/session/login", strings.NewReader(body))
	noSession.Header.Set("Content-Type", "application/json")
	noSession.Header.Set("Origin", h.origin)
	noSession.Header.Set(middleware.CSRFHeader, b.csrf)

	for name, r := range map[string]*http.Request{
		"no CSRF header":        noHeader,
		"token in a form field": form,
		"cross-site origin":     evil,
	} {
		if rec := b.send(r); rec.Code != http.StatusForbidden || errorOf(t, rec).Code != domain.CodeForbidden {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := (&browser{t: t, h: h}).send(noSession); rec.Code != http.StatusForbidden {
		t.Errorf("session-less login: %d %s", rec.Code, rec.Body)
	}
	if b.token != pre || h.hasher.verifies.Load() != 0 || countAudit(t, h.st, auth.AuditLoginSucceeded)+countAudit(t, h.st, auth.AuditLoginFailed) != 0 {
		t.Fatal("a refused login post reached the login handler")
	}
}

// A malformed login body is a 400 invalid_request, not a login attempt.
func TestLoginRejectsMalformedBody(t *testing.T) {
	h := newHarness(t, httpsOrigin)
	h.setPassword(t)
	b := &browser{t: t, h: h}
	b.session()
	for _, body := range []string{"", "password", `{"password":1}`, `{"password":"x","user":"admin"}`, `{"password":"x"} {}`} {
		rec := b.post("/api/session/login", body)
		if rec.Code != http.StatusBadRequest || errorOf(t, rec).Code != domain.CodeInvalidRequest {
			t.Errorf("body %q: %d %s", body, rec.Code, rec.Body)
		}
	}
	if h.hasher.verifies.Load() != 0 || countAudit(t, h.st, auth.AuditLoginFailed) != 0 {
		t.Fatal("a malformed login body was treated as an attempt")
	}
}
