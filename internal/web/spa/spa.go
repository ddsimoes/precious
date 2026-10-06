// Package spa serves the single-page app (design D13): its hashed static
// assets under /assets/ with immutable caching, and its index.html for every
// other path so client routes such as /map/12 deep-link. Both are public; the
// middleware chain adds the application CSP and the other security headers.
package spa

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// Cache policies: asset names carry a content hash, so they never change; the
// shell names the current assets, so it is never cached.
const (
	AssetCacheControl = "public, max-age=31536000, immutable"
	ShellCacheControl = "no-store"
)

// placeholderHTML is served as the shell when the UI was not built into the
// binary. It has no inline script or style, so the strict CSP holds.
//
//go:embed placeholder.html
var placeholderHTML []byte

type handler struct {
	dist  fs.FS
	index []byte
}

// New returns the handler for every non-/api path. dist is the built UI rooted
// at its output directory (web.Dist): index.html plus assets/*. When dist has
// no index.html, the shell is a short page saying to build the UI with
// `make ui`. Methods other than GET and HEAD get 405; route /api elsewhere.
func New(dist fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(dist, "index.html")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		index = placeholderHTML
	case err != nil:
		return nil, fmt.Errorf("spa: read index.html: %w", err)
	}
	return &handler{dist: dist, index: index}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		h.asset(w, r, strings.TrimPrefix(r.URL.Path, "/"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", ShellCacheControl)
	_, _ = w.Write(h.index)
}

// asset serves the file name of dist, or 404 (never the shell) when it is
// missing, a directory, or not a valid path.
func (h *handler) asset(w http.ResponseWriter, r *http.Request, name string) {
	f, err := h.dist.Open(name)
	if err != nil {
		notFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		notFound(w, r)
		return
	}
	// embed.FS, os.DirFS, and fstest.MapFS files all seek.
	content, ok := f.(io.ReadSeeker)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "asset cannot be served", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", AssetCacheControl)
	http.ServeContent(w, r, st.Name(), st.ModTime(), content)
}

func notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, r)
}
