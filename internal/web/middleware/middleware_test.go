package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"precious/internal/domain"
	"precious/internal/web/apierr"
	"precious/internal/web/session"
)

const (
	externalOrigin = "https://precious.example.net"
	authToken      = "auth-token"
	preToken       = "pre-token"
	authCSRF       = "csrf-of-authenticated-session"
	preCSRF        = "csrf-of-pre-login-session"
	maxBody        = 1024
)

type fakeLookup map[string]session.Info

func (f fakeLookup) Lookup(_ context.Context, token string) (session.Info, bool, error) {
	info, ok := f[token]
	return info, ok, nil
}

// protected records whether the wrapped router ran.
type protected struct{ calls int }

func (p *protected) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.calls++
	if r.Body != nil {
		if _, err := io.ReadAll(r.Body); err != nil {
			apierr.Write(w, http.StatusRequestEntityTooLarge, domain.CodeRequestTooLarge, err.Error())
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sources":[{"id":"archive"}]}`)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><title>page</title>")
}

// stack composes the documented order (minus clientip, which does not affect
// these layers) around a recording router.
func stack(t *testing.T) (http.Handler, *protected) {
	t.Helper()
	o, err := ParseOrigin(externalOrigin)
	if err != nil {
		t.Fatal(err)
	}
	lookup := fakeLookup{
		authToken: {ID: 1, Authenticated: true, CSRFToken: authCSRF},
		preToken:  {ID: 2, CSRFToken: preCSRF},
	}
	p := &protected{}
	var h http.Handler = p
	h = CSRF(o)(h)
	h = RequireAuth(h)
	h = LoadSession(lookup, NewSessionCookie(o))(h)
	h = BodyLimit(maxBody)(h)
	h = SecurityHeaders(h)
	return h, p
}

func request(method, target, cookie string, body io.Reader, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, body)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieNameHTTPS, Value: cookie})
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) domain.ErrorCode {
	t.Helper()
	var b apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", rec.Body.String(), err)
	}
	return b.Error.Code
}

// TestA15UnauthenticatedAPIRejected covers "A15 unauthenticated API request
// rejected": 401 unauthenticated and no inventory data, also for the event
// stream and for a pre-login session.
func TestA15UnauthenticatedAPIRejected(t *testing.T) {
	h, p := stack(t)
	for _, tc := range []struct{ target, cookie string }{
		{"/api/sources", ""},
		{"/api/home", ""},
		{"/api/events", ""},
		{"/api/nodes/1/children", "unknown-token"},
		{"/api/sources", preToken},
		{"/assets/../api/sources", ""},
		{"//api/sources", ""},
	} {
		rec := serve(h, request(http.MethodGet, tc.target, tc.cookie, nil, nil))
		if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != domain.CodeUnauthenticated {
			t.Fatalf("%s (cookie %q): %d %s", tc.target, tc.cookie, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "archive") {
			t.Fatalf("%s leaked inventory data: %s", tc.target, rec.Body)
		}
	}
	if p.calls != 0 {
		t.Fatalf("protected handler ran %d times", p.calls)
	}
	if rec := serve(h, request(http.MethodGet, "/api/sources", authToken, nil, nil)); rec.Code != http.StatusOK || p.calls != 1 {
		t.Fatalf("authenticated GET /api/sources = %d, calls %d", rec.Code, p.calls)
	}
}

// "Unauthenticated page redirected": without a session, any page address such
// as /map/12 and the static assets reach the router, which serves the
// application shell (it shows the login screen) and no inventory data. Other
// methods outside /api still need a session.
func TestShellAndAssetsPublic(t *testing.T) {
	h, p := stack(t)
	for _, tc := range []struct{ method, target, cookie string }{
		{http.MethodGet, "/", ""},
		{http.MethodGet, "/map/12", ""},
		{http.MethodGet, "/login", "unknown-token"},
		{http.MethodGet, "/assets/index-abc.js", ""},
		{http.MethodHead, "/map/12", preToken},
	} {
		before := p.calls
		if rec := serve(h, request(tc.method, tc.target, tc.cookie, nil, nil)); rec.Code != http.StatusOK || p.calls != before+1 {
			t.Fatalf("%s %s (cookie %q): %d, router calls %d", tc.method, tc.target, tc.cookie, rec.Code, p.calls-before)
		}
	}
	calls := p.calls
	rec := serve(h, request(http.MethodPost, "/map/12", "", nil, map[string]string{"Origin": externalOrigin}))
	if rec.Code != http.StatusUnauthorized || p.calls != calls {
		t.Fatalf("unauthenticated POST /map/12: %d, router calls %d", rec.Code, p.calls-calls)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("unauthenticated POST /map/12 Content-Type = %q", ct)
	}
}

func TestPublicAllowlist(t *testing.T) {
	for _, tc := range []struct {
		method, target string
		public         bool
	}{
		{http.MethodGet, "/api/session", true},
		{http.MethodPost, "/api/session/login", true},
		{http.MethodGet, "/", true},
		{http.MethodGet, "/map/12", true},
		{http.MethodGet, "/login", true},
		{http.MethodGet, "/assets/index-abc.js", true},
		{http.MethodHead, "/map/12", true},
		{http.MethodHead, "/api/session", false},
		{http.MethodPut, "/api/session/login", false},
		{http.MethodGet, "/api/session/login", false},
		{http.MethodPost, "/api/session", false},
		{http.MethodPost, "/api/session/logout", false},
		{http.MethodGet, "/api/session/", false},
		{http.MethodPost, "/api/session/login/", false},
		{http.MethodGet, "/api", false},
		{http.MethodGet, "/api/", false},
		{http.MethodGet, "/api/home", false},
		{http.MethodGet, "/api/events", false},
		{http.MethodGet, "/assets/../api/sources", false},
		{http.MethodGet, "//api/sources", false},
		{http.MethodPost, "/assets/index-abc.js", false},
		{http.MethodPost, "/login", false},
		{http.MethodDelete, "/", false},
	} {
		r := httptest.NewRequest(tc.method, "/", nil)
		r.URL.Path = tc.target
		if got := public(r); got != tc.public {
			t.Errorf("public(%s %s) = %v, want %v", tc.method, tc.target, got, tc.public)
		}
	}
}

func startScan(origin, referer, token string) *http.Request {
	hdr := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "k1"}
	if origin != "" {
		hdr["Origin"] = origin
	}
	if referer != "" {
		hdr["Referer"] = referer
	}
	if token != "" {
		hdr[CSRFHeader] = token
	}
	return request(http.MethodPost, "/api/commands/start-scan", authToken, strings.NewReader(`{"source_id":"archive"}`), hdr)
}

// TestA15CrossSiteCommandRejected covers "A15 cross-site command rejected":
// a foreign or null Origin (or, without Origin, a foreign or missing Referer)
// gets 403 forbidden even with a valid token, and the command handler never
// runs, so no job can be created.
func TestA15CrossSiteCommandRejected(t *testing.T) {
	h, p := stack(t)
	for name, r := range map[string]*http.Request{
		"evil origin":              startScan("https://evil.example", "", authCSRF),
		"null origin":              startScan("null", "", authCSRF),
		"origin other port":        startScan("https://precious.example.net:8443", "", authCSRF),
		"origin plain http":        startScan("http://precious.example.net", "", authCSRF),
		"evil origin, own referer": startScan("https://evil.example", externalOrigin+"/nodes/1", authCSRF),
		"no origin, evil referer":  startScan("", "https://evil.example/x", authCSRF),
		"no origin, no referer":    startScan("", "", authCSRF),
	} {
		rec := serve(h, r)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != domain.CodeForbidden {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if p.calls != 0 {
		t.Fatalf("command handler ran %d times", p.calls)
	}
}

// TestA15MissingCSRFTokenRejected covers "A15 missing CSRF token rejected": a
// same-origin authenticated command without the session's token gets 403
// forbidden and the command handler never runs.
func TestA15MissingCSRFTokenRejected(t *testing.T) {
	h, p := stack(t)
	query := startScan(externalOrigin, "", "")
	query.URL.RawQuery = url.Values{"csrf_token": {authCSRF}}.Encode()
	form := request(http.MethodPost, "/api/commands/start-scan", authToken,
		strings.NewReader(url.Values{"csrf_token": {authCSRF}}.Encode()),
		map[string]string{"Origin": externalOrigin, "Content-Type": "application/x-www-form-urlencoded", "Idempotency-Key": "k1"})
	for name, r := range map[string]*http.Request{
		"no token":                  startScan(externalOrigin, "", ""),
		"wrong token":               startScan(externalOrigin, "", authCSRF+"x"),
		"pre-login session's token": startScan(externalOrigin, "", preCSRF),
		"token in query string":     query,
		"token in form field":       form,
	} {
		rec := serve(h, r)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != domain.CodeForbidden {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if p.calls != 0 {
		t.Fatalf("command handler ran %d times", p.calls)
	}
}

// Same-origin requests carrying the token in the X-CSRF-Token header pass: a
// JSON command, the login with the pre-login session's token, the logout with
// the session's token, and the Referer fallback when Origin is absent.
func TestSameOriginRequestsAccepted(t *testing.T) {
	h, p := stack(t)
	post := func(target, cookie, token string, hdr map[string]string) *http.Request {
		hdr["Content-Type"] = "application/json"
		if token != "" {
			hdr[CSRFHeader] = token
		}
		return request(http.MethodPost, target, cookie, strings.NewReader(`{"password":"x"}`), hdr)
	}
	for name, r := range map[string]*http.Request{
		"json command":      startScan(externalOrigin, "", authCSRF),
		"origin with 443":   startScan("https://PRECIOUS.example.net:443", "", authCSRF),
		"referer fallback":  startScan("", externalOrigin+"/nodes/1?x=1", authCSRF),
		"login":             post("/api/session/login", preToken, preCSRF, map[string]string{"Origin": externalOrigin}),
		"login, signed in":  post("/api/session/login", authToken, authCSRF, map[string]string{"Origin": externalOrigin}),
		"logout":            post("/api/session/logout", authToken, authCSRF, map[string]string{"Origin": externalOrigin}),
		"logout, referrer":  post("/api/session/logout", authToken, authCSRF, map[string]string{"Referer": externalOrigin + "/"}),
		"login, referrer":   post("/api/session/login", preToken, preCSRF, map[string]string{"Referer": externalOrigin + "/login"}),
		"session bootstrap": request(http.MethodGet, "/api/session", "", nil, nil),
	} {
		before := p.calls
		if rec := serve(h, r); rec.Code != http.StatusOK || p.calls != before+1 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	calls := p.calls
	for name, r := range map[string]*http.Request{
		// What browsers send for POSTs from a no-referrer page.
		"login, Origin: null": post("/api/session/login", preToken, preCSRF, map[string]string{"Origin": "null"}),
		"login, evil origin":  post("/api/session/login", preToken, preCSRF, map[string]string{"Origin": "https://evil.example"}),
		"login without token": post("/api/session/login", preToken, "", map[string]string{"Origin": externalOrigin}),
		// A login post without any session has no token to match.
		"session-less login": post("/api/session/login", "", preCSRF, map[string]string{"Origin": externalOrigin}),
	} {
		if rec := serve(h, r); rec.Code != http.StatusForbidden || errorCode(t, rec) != domain.CodeForbidden {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Logout needs an authenticated session, not just a pre-login one.
	if rec := serve(h, post("/api/session/logout", preToken, preCSRF, map[string]string{"Origin": externalOrigin})); rec.Code != http.StatusUnauthorized {
		t.Errorf("pre-login logout: %d %s", rec.Code, rec.Body)
	}
	if p.calls != calls {
		t.Fatalf("router ran %d times for refused posts", p.calls-calls)
	}
}

func assertSecurityHeaders(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if got := h.Get("Content-Security-Policy"); got != "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; "+
		"media-src 'self'; frame-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'" {
		t.Errorf("%s: CSP = %q", name, got)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options = %q", name, got)
	}
	if got := h.Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("%s: Referrer-Policy = %q", name, got)
	}
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "Access-Control-Allow-") {
			t.Errorf("%s: CORS header %s present", name, k)
		}
	}
}

// Task 8.8 "Headers on every response" (shell, asset, API, 401) and
// "Oversized body" (413 before the command runs).
func TestSecurityHeadersAndBodyLimit(t *testing.T) {
	h, p := stack(t)
	page := serve(h, request(http.MethodGet, "/nodes/1", authToken, nil, map[string]string{"Origin": "https://evil.example"}))
	if page.Code != http.StatusOK {
		t.Fatalf("page: %d", page.Code)
	}
	assertSecurityHeaders(t, "page", page)
	api := serve(h, request(http.MethodGet, "/api/sources", authToken, nil, nil))
	if api.Code != http.StatusOK {
		t.Fatalf("api: %d", api.Code)
	}
	assertSecurityHeaders(t, "api", api)
	assertSecurityHeaders(t, "unauthenticated shell", serve(h, request(http.MethodGet, "/", "", nil, nil)))
	assertSecurityHeaders(t, "asset", serve(h, request(http.MethodGet, "/assets/index-abc.js", "", nil, nil)))
	assertSecurityHeaders(t, "401", serve(h, request(http.MethodGet, "/api/sources", "", nil, nil)))

	calls := p.calls
	big := startScan(externalOrigin, "", authCSRF)
	big.Body = io.NopCloser(strings.NewReader(`{"source_id":"` + strings.Repeat("a", maxBody) + `"}`))
	big.ContentLength = maxBody + 20
	rec := serve(h, big)
	if rec.Code != http.StatusRequestEntityTooLarge || errorCode(t, rec) != domain.CodeRequestTooLarge {
		t.Fatalf("oversized declared body: %d %s", rec.Code, rec.Body)
	}
	assertSecurityHeaders(t, "413", rec)
	if p.calls != calls {
		t.Fatal("command handler ran for an oversized declared body")
	}

	// Without a Content-Length the body is capped while being read.
	chunked := startScan(externalOrigin, "", authCSRF)
	chunked.Body = io.NopCloser(strings.NewReader(strings.Repeat("a", maxBody+1)))
	chunked.ContentLength = -1
	rec = serve(h, chunked)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized streamed body: %d %s", rec.Code, rec.Body)
	}
}

// Task 8.4: cookie attributes for both origin modes.
func TestSessionCookieAttributes(t *testing.T) {
	for _, tc := range []struct {
		origin, name string
		secure       bool
	}{
		{"https://precious.example.net", "__Host-precious_session", true},
		{"http://127.0.0.1:8080", "precious_session", false},
	} {
		o, err := ParseOrigin(tc.origin)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		NewSessionCookie(o).Set(rec, "tok")
		got := rec.Header().Get("Set-Cookie")
		want := tc.name + "=tok; Path=/; HttpOnly; SameSite=Strict"
		if tc.secure {
			want = tc.name + "=tok; Path=/; HttpOnly; Secure; SameSite=Strict"
		}
		if got != want {
			t.Errorf("%s: Set-Cookie = %q, want %q", tc.origin, got, want)
		}
	}
}

func TestParseOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://precious.example.net":      "https://precious.example.net",
		"https://Precious.Example.NET:443/": "https://precious.example.net",
		"http://127.0.0.1:8080":             "http://127.0.0.1:8080",
		"http://localhost:80":               "http://localhost",
		"http://[::1]:8080":                 "http://[::1]:8080",
		"https://[::1]":                     "https://[::1]",
	} {
		o, err := ParseOrigin(in)
		if err != nil || o.String() != want {
			t.Errorf("ParseOrigin(%q) = %q, %v; want %q", in, o.String(), err, want)
		}
	}
	for _, in := range []string{"", "precious.example.net", "ftp://x", "https://x/app", "https://x?q", "https://u@x", "https://x#f", "null"} {
		if _, err := ParseOrigin(in); err == nil {
			t.Errorf("ParseOrigin(%q) accepted", in)
		}
	}
}
