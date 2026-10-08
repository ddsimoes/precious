package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/commands"
	"precious/internal/config"
	"precious/internal/content"
	"precious/internal/dates"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/executor"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/review"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// posix is a local POSIX filesystem with the no-replace rename (ext4).
var posix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond, NoReplaceRename: true,
}

// fat is a case-insensitive filesystem without stable identity, with the
// no-replace rename (exfat).
var fat = fsaccess.Capabilities{Known: true, TimeResolution: 2 * time.Second, NoReplaceRename: true}

// mtime is the modification time of the fixtures' files.
var mtime = time.Date(2006, 3, 4, 10, 0, 0, 0, time.UTC)

// testClock is the real time shifted by an offset the test moves.
type testClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// world is the server's organizing stack as serve wires it, on a synthfs:
// the store, the source registry, a job runner with the scan, hashing,
// relate, and organize handlers, the executor over this package's index
// adapter, and the command and history routes. The runner starts with
// start (most tests) or never (tests that drive state by hand).
type world struct {
	t       *testing.T
	st      *store.Store
	sfs     *synthfs.FS
	srcs    *sources.Service
	r       *jobs.Runner
	clk     *testClock
	org     *Service
	dates   *dates.Service
	scanner *index.Handler
	hashing *content.Service
	mux     *http.ServeMux
	keys    int
	rec     *instrument.Recorder
	started bool
	startup func(ctx context.Context) error
	stop    func()
}

func newWorld(t *testing.T) *world {
	t.Helper()
	cfg := config.Defaults()
	w := &world{t: t, st: storetest.Open(t), sfs: synthfs.New(), clk: &testClock{}}
	log := discard()
	w.rec = instrument.Wrap(w.sfs)
	srcs, err := sources.New(w.st, w.rec, config.Sources{AllowedRoots: []string{t.TempDir()}, AllowWrites: true}, w.clk)
	if err != nil {
		t.Fatal(err)
	}
	w.srcs = srcs
	r, err := jobs.NewRunner(jobs.Options{Store: w.st, Registry: srcs, Config: cfg.Jobs, Clock: w.clk, Logger: log,
		TickInterval: 20 * time.Millisecond, ProgressInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	w.r = r
	pol := rules.Default()
	w.scanner = index.NewHandler(w.st, srcs, pol, w.clk, cfg.Scan)
	w.hashing = content.NewService(w.st, srcs, w.clk, cfg.Hashing, cfg.Archives, cfg.Duplicates)
	w.hashing.Register(r)
	relate := relations.NewHandler(w.st, w.clk, cfg.Duplicates, func(ctx context.Context, gen int64) error {
		return review.Refresh(ctx, w.st, gen)
	})
	relate.Register(r)
	w.dates = dates.New(dates.Options{Store: w.st, Runner: r, Sources: srcs, Zone: time.UTC, Clock: w.clk, Logger: log})
	w.dates.DeferWhile(executor.OrganizeActive)
	w.dates.Register(r)
	w.scanner.OnScanDone(func(ctx context.Context, src domain.SourceID) {
		w.hashing.AfterScan(ctx, src)
		_ = r.Write(ctx, relations.RequestRefresh)
		w.dates.AfterScan(ctx, src)
	})
	w.org = New(Options{Store: w.st, Policy: pol, AllowWrites: true, Clock: w.clk, Logger: log, Dates: w.dates})
	ex := executor.New(executor.Options{Store: w.st, Sources: srcs, Index: w.org.Index(), AllowWrites: true,
		Clock: w.clk, Logger: log, Content: w.hashing})
	ex.Register(r)
	w.scanner.DeferWhile(executor.OrganizeActive)
	w.scanner.Register(r)

	h := commands.New(commands.Options{Store: w.st, Jobs: r, Logger: log})
	w.org.RegisterCommands(h)
	decisions.RegisterCommands(h, decisions.New(w.clk, pol, index.StartScan))
	index.RegisterCommands(h)
	w.mux = http.NewServeMux()
	w.mux.Handle("POST /api/commands/{name}", h)
	w.org.Routes(w.mux)
	w.stop = func() {}
	t.Cleanup(func() { w.stop() })
	w.startup = func(ctx context.Context) error { return ex.Startup(ctx, r) }
	return w
}

// start starts the runner, as serve does, with the executor's Startup.
func (w *world) start() *world {
	w.t.Helper()
	if w.started {
		return w
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.r.Start(ctx); err != nil {
		w.t.Fatal(err)
	}
	if err := w.startup(ctx); err != nil {
		w.t.Fatal(err)
	}
	w.started = true
	w.stop = func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		if err := w.r.Stop(sctx); err != nil {
			w.t.Errorf("stop the runner: %v", err)
		}
		cancel()
	}
	return w
}

// disk builds the synthfs root at path on its own device with caps, adds it
// as source id with writes on, as add-source and set-source-writes leave
// it, and scans it.
func (w *world) disk(id domain.SourceID, path string, caps fsaccess.Capabilities, build func(root *synthfs.Node)) *synthfs.Node {
	w.t.Helper()
	root := w.sfs.Root(path)
	if build != nil {
		build(root)
	}
	w.add(id, path, root, caps)
	return root
}

// add adds the synthfs root built at path as source id, as disk does, and
// scans it.
func (w *world) add(id domain.SourceID, path string, root *synthfs.Node, caps fsaccess.Capabilities) {
	w.t.Helper()
	w.addAs(id, path, root, caps, "ext4")
}

// addAs is add with the volume's filesystem type.
func (w *world) addAs(id domain.SourceID, path string, root *synthfs.Node, caps fsaccess.Capabilities, fsType string) {
	w.t.Helper()
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: fsType,
		DeviceKey: "dev:" + string(id), Strong: true}
	w.sfs.SetVolume(dev, vol)
	w.sfs.SetCapabilities(dev, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		w.t.Fatal(err)
	}
	w.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key, capabilities,
		state, mount_point, created_at, write_enabled) VALUES (?, ?, 'uuid', ?, ?, 1, X'', ?, ?, 'online', ?, 0, 1)`,
		string(id), string(id), vol.ID, fsType, vol.DeviceKey, string(capsJSON), []byte(path))
	w.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id))
	w.scan(id)
}

// scan scans src completely: through start-scan and the runner, which then
// hashes and relates, when it runs, else by running the scan handler
// directly as a running scan job.
func (w *world) scan(src domain.SourceID) {
	w.t.Helper()
	if w.started {
		w.ok(http.StatusAccepted, "start-scan", fmt.Sprintf(`{"source_id":%q}`, src))
		w.idle()
		return
	}
	var job int64
	if err := w.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)
		RETURNING id`, string(src)).Scan(&job); err != nil {
		w.t.Fatal(err)
	}
	j := jobs.Job{ID: domain.JobID(job), Kind: jobs.KindScan, PayloadVersion: 1, Payload: json.RawMessage("{}"),
		SourceID: src, Attempt: 1}
	if err := w.scanner.Run(context.Background(), j, scanRuntime{}); err != nil {
		w.t.Fatalf("scan %s: %v", src, err)
	}
	w.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, job)
}

// scanRuntime is a jobs.Runtime that never pauses.
type scanRuntime struct{}

func (scanRuntime) Progress(map[string]int64)                        {}
func (scanRuntime) FSCall(string) func()                             { return func() {} }
func (scanRuntime) Yield(ctx context.Context) error                  { return ctx.Err() }
func (scanRuntime) UseSource(context.Context, domain.SourceID) error { return nil }

// idle waits until no job is queued or running.
func (w *world) idle() {
	w.t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		if w.count(`SELECT count(*) FROM jobs WHERE state IN ('queued', 'running')`) == 0 {
			return
		}
		if time.Now().After(deadline) {
			rows, _ := w.st.Reader().Query(`SELECT id, kind, state, COALESCE(terminal_detail, '') FROM jobs
				WHERE state IN ('queued', 'running')`)
			var jobs []string
			for rows != nil && rows.Next() {
				var id int64
				var kind, state, detail string
				_ = rows.Scan(&id, &kind, &state, &detail)
				jobs = append(jobs, fmt.Sprintf("%d %s %s %s", id, kind, state, detail))
			}
			if rows != nil {
				rows.Close()
			}
			w.t.Fatalf("jobs still active: %v", jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	if _, err := w.st.Writer().Exec(query, args...); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// id is the entry ID of a path of src.
func (w *world) id(src domain.SourceID, path string) string {
	w.t.Helper()
	var id int64
	if err := w.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(src),
		[]byte(path)).Scan(&id); err != nil {
		w.t.Fatalf("entry %s:%q: %v", src, path, err)
	}
	return strconv.FormatInt(id, 10)
}

// pathOf is the index path of an entry, and its state.
func (w *world) pathOf(id string) (string, string) {
	w.t.Helper()
	var (
		path  []byte
		state string
	)
	if err := w.st.Reader().QueryRow(`SELECT path, state FROM entries WHERE id = ?`, id).Scan(&path, &state); err != nil {
		w.t.Fatalf("entry %s: %v", id, err)
	}
	return string(path), state
}

// post sends a command with a fresh Idempotency-Key.
func (w *world) post(name, body string) (int, string) {
	w.t.Helper()
	w.keys++
	req := httptest.NewRequest(http.MethodPost, "/api/commands/"+name, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "key-"+strconv.Itoa(w.keys))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// ok sends a command that must answer status, and returns its body.
func (w *world) ok(status int, name, body string) string {
	w.t.Helper()
	code, out := w.post(name, body)
	if code != status {
		w.t.Fatalf("%s %s = %d %s, want %d", name, body, code, out, status)
	}
	return out
}

// refuse sends a command that must fail with status and code.
func (w *world) refuse(status int, code domain.ErrorCode, name, body string) {
	w.t.Helper()
	got, out := w.post(name, body)
	if got != status || !strings.Contains(out, `"code":"`+string(code)+`"`) {
		w.t.Fatalf("%s %s = %d %s, want %d %s", name, body, got, out, status, code)
	}
}

// plan sends a plan-* command, which must answer 201, and returns its
// action and every item (following the items' pages).
func (w *world) plan(name, body string) (actionJSON, []itemJSON) {
	w.t.Helper()
	var res PlanResponse
	decode(w.t, w.ok(http.StatusCreated, name, body), &res)
	items := res.Items
	if res.NextCursor != nil {
		items = append(items, w.items(res.Action.ID, "cursor="+*res.NextCursor)...)
	}
	return res.Action, items
}

// run runs a planned action and waits for every job to end; it returns
// the action as the history reads it then.
func (w *world) run(action string) actionJSON {
	w.t.Helper()
	var res runResponse
	decode(w.t, w.ok(http.StatusAccepted, "run-action", fmt.Sprintf(`{"action_id":%q}`, action)), &res)
	if res.Action.State != "queued" || res.JobID == "" {
		w.t.Fatalf("run-action answered %+v", res)
	}
	w.idle()
	return w.action(action)
}

// get reads a history endpoint, which must answer 200.
func (w *world) get(path string, v any) {
	w.t.Helper()
	code, body := w.getRaw(path)
	if code != http.StatusOK {
		w.t.Fatalf("GET %s = %d %s", path, code, body)
	}
	decode(w.t, body, v)
}

func (w *world) getRaw(path string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (w *world) action(id string) actionJSON {
	w.t.Helper()
	var a actionJSON
	w.get("/api/history/"+id, &a)
	return a
}

// items reads every page of an action's items, with query appended.
func (w *world) items(id, query string) []itemJSON {
	w.t.Helper()
	var out []itemJSON
	for {
		var p page[itemJSON]
		path := "/api/history/" + id + "/items"
		if query != "" {
			path += "?" + query
		}
		w.get(path, &p)
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out
		}
		q := []string{"cursor=" + *p.NextCursor}
		for _, part := range strings.Split(query, "&") {
			if part != "" && !strings.HasPrefix(part, "cursor=") {
				q = append(q, part)
			}
		}
		query = strings.Join(q, "&")
	}
}

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// exists reports whether rel exists below the synthfs root at root.
func (w *world) exists(root, rel string) bool {
	w.t.Helper()
	d, err := w.sfs.OpenRoot(root)
	if err != nil {
		w.t.Fatal(err)
	}
	defer d.Close()
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		info, err := d.Lstat([]byte(p))
		if err != nil {
			return false
		}
		next, err := d.OpenDir([]byte(p), info)
		if err != nil {
			return false
		}
		defer next.Close()
		d = next
	}
	_, err = d.Lstat([]byte(parts[len(parts)-1]))
	return err == nil
}

// itemSummary is an item as "state reason from -> to".
func itemSummary(it itemJSON) string {
	s := it.Op + " " + it.State
	if it.Reason != nil {
		s += " " + *it.Reason
	}
	if it.From != nil {
		s += " " + it.From.Path
	}
	if it.To != nil {
		s += " -> " + it.To.Path
	}
	return s
}

func summaries(items []itemJSON) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = itemSummary(it)
	}
	return out
}

// wantItems fails unless items read exactly want, in order.
func wantItems(t *testing.T, items []itemJSON, want ...string) {
	t.Helper()
	got := summaries(items)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("items:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// ids is a JSON list of entry IDs.
func ids(list ...string) string {
	b, _ := json.Marshal(list)
	return string(b)
}

// readTx runs fn in a read transaction.
func (w *world) readTx(fn func(tx *sql.Tx) error) {
	w.t.Helper()
	if err := w.st.Read(context.Background(), fn); err != nil {
		w.t.Fatal(err)
	}
}
