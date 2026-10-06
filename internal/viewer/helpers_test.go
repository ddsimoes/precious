package viewer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index/indextest"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
	"precious/internal/viewer"
)

var ext4Caps = fsaccess.Capabilities{Known: true, CaseSensitive: true, NormalizationSensitive: true,
	StableIdentity: true, HardLinks: true, TimeResolution: time.Nanosecond}

// env is a source "disk" on a synthetic filesystem mounted at diskPoint,
// with the viewer registered on a mux.
type env struct {
	t   *testing.T
	st  *store.Store
	fs  *synthfs.FS
	dev uint64
	mux *http.ServeMux
}

const (
	diskPoint = "/srv/disk"
	diskID    = domain.SourceID("disk")
)

// newEnv returns an env over a new empty root at diskPoint, with caps.
func newEnv(t *testing.T, caps fsaccess.Capabilities) (*env, *synthfs.Node) {
	t.Helper()
	fsys := synthfs.New()
	root := fsys.Root(diskPoint)
	return newEnvOn(t, fsys, root, caps), root
}

// newEnvOn returns an env over root, a root of fsys at diskPoint, with caps.
func newEnvOn(t *testing.T, fsys *synthfs.FS, root *synthfs.Node, caps fsaccess.Capabilities) *env {
	t.Helper()
	e := &env{t: t, st: storetest.Open(t), fs: fsys, dev: root.Info().Dev}
	e.fs.SetCapabilities(e.dev, caps)
	svc, err := sources.New(e.st, e.fs, config.Sources{AllowedRoots: []string{t.TempDir()}}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	// A weak path volume, known by its mount point, which is how synthfs
	// lists a device without SetVolume. It is offline until opened.
	if _, err := e.st.Writer().Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong,
		rel_root, capabilities, state, state_reason, created_at)
		VALUES (?, 'Disk', 'path', ?, 'ext4', 0, X'', ?, 'offline', 'volume_not_mounted', 0)`,
		string(diskID), diskPoint, indextest.Capabilities); err != nil {
		t.Fatal(err)
	}
	e.mux = http.NewServeMux()
	viewer.Register(e.mux, e.st, svc, nil)
	return e
}

// seed indexes the given paths of the disk, plus their folders, with the
// identity facts a scan reads from Lstat.
func (e *env) seed(paths ...string) *indextest.Seeded {
	e.t.Helper()
	return indextest.Seed(e.t, e.st, indextest.Tree{Source: diskID, Nodes: lstatNodes(e.t, e.fs, diskPoint, paths...)})
}

// lstatNodes returns an indextest node per path, carrying the lstat of the
// entry at that path below the root at abs, as a scan reads it.
func lstatNodes(t *testing.T, fsys fsaccess.FS, abs string, paths ...string) []indextest.Node {
	t.Helper()
	root, err := fsys.OpenRoot(abs)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	nodes := make([]indextest.Node, 0, len(paths))
	for _, p := range paths {
		info := lstatPath(t, root, p)
		nodes = append(nodes, indextest.Node{Path: p, Kind: info.Kind, Lstat: &info})
	}
	return nodes
}

func lstatPath(t *testing.T, root fsaccess.Dir, p string) fsaccess.EntryInfo {
	t.Helper()
	names := strings.Split(p, "/")
	dir := root
	for _, name := range names[:len(names)-1] {
		info, err := dir.Lstat([]byte(name))
		if err != nil {
			t.Fatal(err)
		}
		next, err := dir.OpenDir([]byte(name), info)
		if err != nil {
			t.Fatal(err)
		}
		if dir != root {
			dir.Close()
		}
		dir = next
	}
	if dir != root {
		defer dir.Close()
	}
	info, err := dir.Lstat([]byte(names[len(names)-1]))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// get requests /api/entries/{id}/{what} with the optional header pairs.
func (e *env) get(id domain.EntryID, what string, header ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.getRaw(id.String(), what, header...)
}

func (e *env) getRaw(id, what string, header ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/entries/"+id+"/"+what, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// textResponse is the /text body.
type textResponse struct {
	Encoding  string  `json:"encoding"`
	Text      string  `json:"text"`
	Truncated bool    `json:"truncated"`
	Language  *string `json:"language"`
	Markdown  bool    `json:"markdown"`
}

func decodeText(t *testing.T, rec *httptest.ResponseRecorder) textResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("text: %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("text Content-Type %q", ct)
	}
	var body textResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// checkProtected fails unless rec carries the headers of every viewer
// response.
func checkProtected(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if v := rec.Header().Get("X-Content-Type-Options"); v != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options %q, want nosniff", what, v)
	}
	if v := rec.Header().Get("Cross-Origin-Resource-Policy"); v != "same-origin" {
		t.Errorf("%s: Cross-Origin-Resource-Policy %q, want same-origin", what, v)
	}
}

// checkError fails unless rec is the error envelope with status and code.
func checkError(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, code domain.ErrorCode) {
	t.Helper()
	checkProtected(t, what, rec)
	var body struct {
		Error struct {
			Code domain.ErrorCode `json:"code"`
		} `json:"error"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != code {
		t.Errorf("%s: %d %s, want %d %s", what, rec.Code, rec.Body, status, code)
	}
}
