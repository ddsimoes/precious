package spa_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"precious/internal/web/spa"
)

const builtIndex = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Precious</title>` +
	`<script type="module" crossorigin src="/assets/x.js"></script>` +
	`<link rel="stylesheet" crossorigin href="/assets/x.css"></head><body><div id="root"></div></body></html>`

func builtDist() fstest.MapFS {
	return fstest.MapFS{
		".gitkeep":     {},
		"index.html":   {Data: []byte(builtIndex)},
		"assets/x.js":  {Data: []byte("console.log('precious')\n")},
		"assets/x.css": {Data: []byte("body{margin:0}\n")},
		"assets/sub/y": {Data: []byte("y")},
		"favicon.svg":  {Data: []byte("<svg/>")},
	}
}

func newHandler(t *testing.T, dist fstest.MapFS) http.Handler {
	t.Helper()
	h, err := spa.New(dist)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// assertShell checks that rec is the uncached HTML shell with body want.
func assertShell(t *testing.T, target string, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("GET %s = %d %q, want the shell", target, rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("GET %s Content-Type = %q", target, got)
	}
	if got := rec.Header().Get("Cache-Control"); got != spa.ShellCacheControl {
		t.Errorf("GET %s Cache-Control = %q, want %q", target, got, spa.ShellCacheControl)
	}
}

var (
	scriptTag     = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	srcAttr       = regexp.MustCompile(`(?i)\ssrc\s*=`)
	inlineAttr    = regexp.MustCompile(`(?i)<[a-z][^>]*?\s(style|on[a-z]+)\s*=`)
	inlineStyleEl = regexp.MustCompile(`(?i)<style\b`)
)

// inlineCode returns the first inline script, style element, style attribute,
// or event-handler attribute of html, or "" when it has none, so the strict
// CSP (script-src 'self'; style-src 'self') holds.
func inlineCode(html string) string {
	for _, m := range scriptTag.FindAllStringSubmatch(html, -1) {
		if !srcAttr.MatchString(m[1]) || strings.TrimSpace(m[2]) != "" {
			return m[0]
		}
	}
	if m := inlineStyleEl.FindString(html); m != "" {
		return m
	}
	return inlineAttr.FindString(html)
}

// Without a built index.html, every page is the "build the UI" page, which
// has no inline script or style.
func TestPlaceholderShell(t *testing.T) {
	h := newHandler(t, fstest.MapFS{".gitkeep": {}})
	page := get(h, http.MethodGet, "/").Body.String()
	if !strings.Contains(page, "make ui") {
		t.Fatalf("placeholder does not say how to build the UI: %s", page)
	}
	if inline := inlineCode(page); inline != "" {
		t.Errorf("placeholder has inline code: %s", inline)
	}
	for _, target := range []string{"/", "/map/12", "/login", "/search?q=x"} {
		assertShell(t, target, get(h, http.MethodGet, target), page)
	}
	if rec := get(h, http.MethodGet, "/assets/x.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("asset without a build = %d", rec.Code)
	}
}

// With a built dist, client routes deep-link to index.html.
func TestBuiltShell(t *testing.T) {
	h := newHandler(t, builtDist())
	for _, target := range []string{"/", "/map/12", "/login", "/assets", "/favicon.svg", "/index.html"} {
		assertShell(t, target, get(h, http.MethodGet, target), builtIndex)
	}
	head := get(h, http.MethodHead, "/map/12")
	if head.Code != http.StatusOK || head.Header().Get("Cache-Control") != spa.ShellCacheControl {
		t.Fatalf("HEAD /map/12 = %d %v", head.Code, head.Header())
	}
	for _, target := range []string{"/map/12", "/assets/x.js"} {
		rec := get(h, http.MethodPost, target)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("POST %s = %d, Allow %q", target, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

// Hashed assets are cached forever; a missing asset is 404, never the shell.
func TestAssets(t *testing.T) {
	h := newHandler(t, builtDist())
	for target, want := range map[string]struct{ body, ctype string }{
		"/assets/x.js":  {"console.log('precious')\n", "text/javascript; charset=utf-8"},
		"/assets/x.css": {"body{margin:0}\n", "text/css; charset=utf-8"},
	} {
		rec := get(h, http.MethodGet, target)
		if rec.Code != http.StatusOK || rec.Body.String() != want.body {
			t.Fatalf("GET %s = %d %q", target, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s Cache-Control = %q", target, got)
		}
		if got := rec.Header().Get("Content-Type"); got != want.ctype {
			t.Errorf("GET %s Content-Type = %q, want %q", target, got, want.ctype)
		}
	}
	for _, target := range []string{"/assets/missing.js", "/assets/", "/assets/sub", "/assets/sub/", "/assets/../index.html", "/assets//x.js"} {
		rec := get(h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s = %d %q, want 404 without the shell", target, rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s Cache-Control = %q", target, got)
		}
	}
}

var assetRef = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

// The index of the built test dist references its assets, and each resolves.
func TestBuiltIndexAssetsResolve(t *testing.T) {
	h := newHandler(t, builtDist())
	refs := assetRef.FindAllStringSubmatch(get(h, http.MethodGet, "/").Body.String(), -1)
	if len(refs) != 2 {
		t.Fatalf("asset references = %v", refs)
	}
	for _, m := range refs {
		if rec := get(h, http.MethodGet, m[1]); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", m[1], rec.Code)
		}
	}
}
