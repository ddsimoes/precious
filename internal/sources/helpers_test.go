package sources

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"precious/internal/commands"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/jobs"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

var (
	ext4Caps = fsaccess.Capabilities{Known: true, CaseSensitive: true, NormalizationSensitive: true,
		StableIdentity: true, HardLinks: true, TimeResolution: time.Nanosecond, NoReplaceRename: true}
	ext4CapsJSON = `{"known":true,"read_only":false,"case_sensitive":true,"normalization_sensitive":true,"stable_identity":true,"local_time":false,"hard_links":true,"time_resolution_ns":1,"no_replace_rename":true}`
	fatCaps      = fsaccess.Capabilities{Known: true, NormalizationSensitive: true, LocalTime: true, TimeResolution: 2 * time.Second, NoReplaceRename: true}
)

func uuidVolume(id, label string) fsaccess.Volume {
	return fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: id, Label: label, FSType: "ext4", DeviceKey: "dev:" + id, Strong: true}
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// env is a registry over a synthetic filesystem whose folders also exist on
// disk below base, the only allowed root, so the picker (which reads the real
// filesystem) and the registry (which reads synthfs) see the same folders.
type env struct {
	t    *testing.T
	st   *store.Store
	fs   *synthfs.FS
	svc  *Service
	base string
	url  string
	r    *jobs.Runner
	keys atomic.Int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, st: storetest.Open(t), fs: synthfs.New(), base: realDir(t)}
	e.svc = e.service(e.base)
	return e
}

// service returns a new registry over e's store and filesystem with the
// given allowed roots (a restart: it has its own handle key).
func (e *env) service(roots ...string) *Service {
	e.t.Helper()
	svc, err := New(e.st, e.fs, config.Sources{AllowedRoots: roots}, fixedClock{testNow})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

// realDir is a new temporary directory by its canonical path.
func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// disk creates the folder base/name on a new synthetic device mounted there,
// with the given volume (a weak path volume when vol is zero) and ext4
// capabilities, and the folders below it (each a root of the same device in
// synthfs, so it can be opened as a source root). It returns the device.
func (e *env) disk(name string, vol fsaccess.Volume, folders ...string) uint64 {
	e.t.Helper()
	point := filepath.Join(e.base, name)
	mkdir(e.t, point)
	dev := e.fs.Root(point).Info().Dev
	if vol != (fsaccess.Volume{}) {
		e.fs.SetVolume(dev, vol)
	}
	e.fs.SetCapabilities(dev, ext4Caps)
	for _, f := range folders {
		e.folder(dev, filepath.Join(point, f))
	}
	return dev
}

// folder creates p on disk and as a root on dev in synthfs.
func (e *env) folder(dev uint64, p string) *synthfs.Node {
	e.t.Helper()
	mkdir(e.t, p)
	return e.fs.Root(p).Dev(dev)
}

// insertSource inserts a source row with no entries, offline until a
// refresh, for tests that seed its index with indextest.
func (e *env) insertSource(id string, vol fsaccess.Volume, rel string) {
	e.t.Helper()
	strong := 0
	if vol.Strong {
		strong = 1
	}
	_, err := e.st.Writer().Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, volume_label, fs_type, strong,
		rel_root, device_key, capabilities, state, state_reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'offline', ?, ?)`,
		id, id, string(vol.Kind), vol.ID, nullString(vol.Label), vol.FSType, strong, []byte(rel), nullString(vol.DeviceKey),
		ext4CapsJSON, ReasonNotMounted, testNow.UnixMilli())
	if err != nil {
		e.t.Fatal(err)
	}
}

// tag gives the entry the tag name, creating the tag when needed.
func (e *env) tag(entry domain.EntryID, name string) {
	e.t.Helper()
	var id int64
	err := e.st.Writer().QueryRow(`INSERT INTO tags (name, created_at) VALUES (?, 0)
		ON CONFLICT (name) DO UPDATE SET name = excluded.name RETURNING id`, name).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO entry_tags (entry_id, tag_id, added_at) VALUES (?, ?, 0)`, int64(entry), id); err != nil {
		e.t.Fatal(err)
	}
}

// add adds the folder p as a source through the registry, as add-source does.
func (e *env) add(p, label string) Source {
	e.t.Helper()
	c, err := e.svc.PrepareAdd(context.Background(), e.svc.handle(p), label)
	if err != nil {
		e.t.Fatalf("PrepareAdd(%s): %v", p, err)
	}
	var src Source
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		src, err = e.svc.Add(context.Background(), tx, c)
		return err
	})
	if err != nil {
		e.t.Fatalf("Add(%s): %v", p, err)
	}
	return src
}

func (e *env) get(id domain.SourceID) Source {
	e.t.Helper()
	src, err := e.svc.Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return src
}

func (e *env) refresh() {
	e.t.Helper()
	if err := e.svc.Refresh(context.Background()); err != nil {
		e.t.Fatalf("Refresh: %v", err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// serve starts the command API with the source commands, the sources and
// picker endpoints, and a job runner that is never started, so jobs stay
// queued.
func (e *env) serve() {
	e.t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: e.svc, Config: config.Defaults().Jobs,
		Clock: fixedClock{testNow}, Logger: logger})
	if err != nil {
		e.t.Fatal(err)
	}
	e.r = r
	cmds := commands.New(commands.Options{Store: e.st, Jobs: r, Logger: logger})
	RegisterCommands(cmds, e.svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", cmds)
	Register(mux, e.svc, logger)
	srv := httptest.NewServer(mux)
	e.t.Cleanup(srv.Close)
	e.url = srv.URL
}

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

// command posts a command with a fresh idempotency key.
func (e *env) command(name string, body any) response {
	e.t.Helper()
	r, err := e.send(name, body)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// send is command without failing the test, for other goroutines.
func (e *env) send(name string, body any) (response, error) {
	raw, ok := body.(string)
	if !ok {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		raw = string(b)
	}
	req, err := http.NewRequest(http.MethodPost, e.url+"/api/commands/"+name, strings.NewReader(raw))
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("key-%d", e.keys.Add(1)))
	return do(req)
}

func (e *env) getJSON(path string) response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.url+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := do(req)
	if err != nil {
		e.t.Fatal(err)
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
		return response{}, fmt.Errorf("%s %s: body %q is not JSON: %v", req.Method, req.URL, raw, err)
	}
	return out, nil
}
