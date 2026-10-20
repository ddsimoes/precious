package cleanup

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
	"precious/internal/organize"
	"precious/internal/relations"
	"precious/internal/review"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
	"precious/internal/web/api"
)

// world is the server's whole cleanup stack as serve wires it, on a synthfs
// seen through an instrument recorder: the store, the source registry, a
// started job runner with the scan, hashing, relate, organize, and
// purge_check handlers, the executor over organize's index adapter, and
// every command and read route the cleanup tests drive (cleanup, organize,
// decisions, review, content, sources, index, and the read API).
type world struct {
	t     *testing.T
	st    *store.Store
	sfs   *synthfs.FS
	rec   *instrument.Recorder
	srcs  *sources.Service
	r     *jobs.Runner
	clk   *checkClock
	svc   *Service
	org   *organize.Service
	mux   *http.ServeMux
	roots map[domain.SourceID]string
	keys  int
}

// newWorld builds and starts the stack. Each world is independent (its own
// store, disks, clock, and runner), so the test calling it runs in
// parallel with the others; call it once per test or subtest.
func newWorld(t *testing.T) *world {
	t.Helper()
	t.Parallel()
	cfg := config.Defaults()
	w := &world{t: t, st: storetest.Open(t), sfs: synthfs.New(), clk: &checkClock{}, roots: map[domain.SourceID]string{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
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
	scanner := index.NewHandler(w.st, srcs, pol, w.clk, cfg.Scan)
	hashing := content.NewService(w.st, srcs, w.clk, cfg.Hashing, cfg.Archives, cfg.Duplicates)
	hashing.Register(r)
	relations.NewHandler(w.st, w.clk, cfg.Duplicates, func(ctx context.Context, gen int64) error {
		return review.Refresh(ctx, w.st, gen)
	}).Register(r)
	// The media job, as serve wires it: organizing's ActionDone requests it.
	mediaDates := dates.New(dates.Options{Store: w.st, Runner: r, Sources: srcs, Zone: time.UTC, Clock: w.clk,
		Logger: log})
	mediaDates.DeferWhile(executor.OrganizeActive)
	mediaDates.Register(r)
	scanner.OnScanDone(func(ctx context.Context, src domain.SourceID) {
		hashing.AfterScan(ctx, src)
		_ = r.Write(ctx, relations.RequestRefresh)
		mediaDates.AfterScan(ctx, src)
	})
	w.org = organize.New(organize.Options{Store: w.st, Policy: pol, AllowWrites: true, Clock: w.clk, Logger: log,
		Dates: mediaDates})
	ex := executor.New(executor.Options{Store: w.st, Sources: srcs, Index: w.org.Index(), AllowWrites: true,
		Clock: w.clk, Logger: log, Content: hashing})
	ex.Register(r)
	scanner.DeferWhile(executor.OrganizeActive)
	scanner.Register(r)
	w.svc = New(Options{Store: w.st, Runner: r, Sources: srcs, Content: hashing, Policy: pol, Executor: ex,
		AllowWrites: true, Clock: w.clk, Logger: log})
	w.svc.Register(r)

	h := commands.New(commands.Options{Store: w.st, Jobs: r, Logger: log})
	dec := decisions.New(w.clk, pol, index.StartScan)
	sources.RegisterCommands(h, srcs)
	index.RegisterCommands(h)
	decisions.RegisterCommands(h, dec)
	content.RegisterCommands(h, hashing)
	review.RegisterCommands(h, dec)
	w.org.RegisterCommands(h)
	w.svc.RegisterCommands(h)
	w.mux = http.NewServeMux()
	w.mux.Handle("POST /api/commands/{name}", h)
	sources.Register(w.mux, srcs, log)
	api.Register(w.mux, w.st, pol, log)
	w.org.Routes(w.mux)
	w.svc.Routes(w.mux)

	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ex.Startup(ctx, r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		if err := r.Stop(sctx); err != nil {
			t.Errorf("stop the runner: %v", err)
		}
		cancel()
	})
	return w
}

// disk builds the synthfs root at path on its own device, adds it as source
// id with writes on, and scans, hashes, and relates it.
func (w *world) disk(id domain.SourceID, path string, build func(root *synthfs.Node)) *synthfs.Node {
	w.t.Helper()
	root := w.sfs.Root(path)
	if build != nil {
		build(root)
	}
	w.add(id, path, root, "ext4")
	return root
}

// add adds the synthfs root built at path as source id, on its own volume
// of filesystem type fsType, with writes on, and scans it.
func (w *world) add(id domain.SourceID, path string, root *synthfs.Node, fsType string) {
	w.t.Helper()
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: fsType,
		DeviceKey: "dev:" + string(id), Strong: true}
	w.sfs.SetVolume(dev, vol)
	w.sfs.SetCapabilities(dev, checkPosix)
	caps, err := json.Marshal(checkPosix)
	if err != nil {
		w.t.Fatal(err)
	}
	w.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key, capabilities,
		state, mount_point, created_at, write_enabled) VALUES (?, ?, 'uuid', ?, ?, 1, X'', ?, ?, 'online', ?, 0, 1)`,
		string(id), string(id), vol.ID, fsType, vol.DeviceKey, string(caps), []byte(path))
	w.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id))
	w.roots[id] = path
	w.scan(id)
}

// scan scans src through start-scan and the runner, which then hashes and
// relates, and waits for every job to end.
func (w *world) scan(src domain.SourceID) {
	w.t.Helper()
	w.ok(http.StatusAccepted, "start-scan", fmt.Sprintf(`{"source_id":%q}`, src))
	w.idle()
}

// idle waits until no job is queued or running.
func (w *world) idle() {
	w.t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for w.count(`SELECT count(*) FROM jobs WHERE state IN ('queued', 'running')`) > 0 {
		if time.Now().After(deadline) {
			w.t.Fatalf("jobs still active: %d", w.count(`SELECT count(*) FROM jobs WHERE state IN ('queued', 'running')`))
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

func (w *world) count(query string, args ...any) int64 {
	w.t.Helper()
	var n int64
	if err := w.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// id is the entry ID of a path of src, as the API names it.
func (w *world) id(src domain.SourceID, path string) string {
	w.t.Helper()
	var id int64
	if err := w.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(src),
		[]byte(path)).Scan(&id); err != nil {
		w.t.Fatalf("entry %s:%q: %v", src, path, err)
	}
	return strconv.FormatInt(id, 10)
}

// pathOf is the index path of entry id, and its state.
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

// has reports whether src indexes an entry at path that is not missing.
func (w *world) has(src domain.SourceID, path string) bool {
	return w.count(`SELECT count(*) FROM entries WHERE source_id = ? AND path = ? AND state <> 'missing'`,
		string(src), []byte(path)) > 0
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

// refuse sends a command that must fail with status and code, and returns
// the error body.
func (w *world) refuse(status int, code domain.ErrorCode, name, body string) string {
	w.t.Helper()
	got, out := w.post(name, body)
	if got != status || !strings.Contains(out, `"code":"`+string(code)+`"`) {
		w.t.Fatalf("%s %s = %d %s, want %d %s", name, body, got, out, status, code)
	}
	return out
}

// get reads an endpoint, which must answer 200, into v.
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

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// ids is a JSON list of IDs.
func ids(list ...string) string {
	b, _ := json.Marshal(list)
	return string(b)
}

// The JSON the tests read, as the interface reads it.
type (
	actionView struct {
		ID           string           `json:"id"`
		Kind         string           `json:"kind"`
		State        string           `json:"state"`
		Counts       map[string]int64 `json:"counts"`
		Entries      map[string]int64 `json:"entries"`
		Bytes        int64            `json:"bytes"`
		Files        int64            `json:"files"`
		Ground       *string          `json:"ground"`
		List         *string          `json:"list"`
		CheckID      *string          `json:"check_id"`
		DeletedFiles int64            `json:"deleted_files"`
		DeletedBytes int64            `json:"deleted_bytes"`
		FreedBytes   int64            `json:"freed_bytes"`
		Undo         struct {
			Possible bool    `json:"possible"`
			Reason   *string `json:"reason"`
		} `json:"undo"`
	}
	pathView struct {
		Path string `json:"path"`
	}
	itemView struct {
		ID    string `json:"id"`
		Seq   int64  `json:"seq"`
		Op    string `json:"op"`
		Entry *struct {
			ID string `json:"id"`
		} `json:"entry"`
		From      *pathView `json:"from"`
		To        *pathView `json:"to"`
		State     string    `json:"state"`
		Reason    *string   `json:"reason"`
		Bytes     int64     `json:"bytes"`
		Files     int64     `json:"files"`
		KeptCount *int64    `json:"kept_count"`
	}
	// planView is a plan's answer; Summary is set for plan-cleanup.
	planView struct {
		Action     actionView   `json:"action"`
		Items      []itemView   `json:"items"`
		NextCursor *string      `json:"next_cursor"`
		Summary    *summaryJSON `json:"summary"`
	}
	quarantinedView struct {
		Entry struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"entry"`
		Original      *pathView `json:"original"`
		PlanID        *string   `json:"plan_id"`
		QuarantinedAt *string   `json:"quarantined_at"`
		Bytes         int64     `json:"bytes"`
		Files         int64     `json:"files"`
		Check         *checkRef `json:"check"`
	}
	quarantineView struct {
		Items      []quarantinedView `json:"items"`
		NextCursor *string           `json:"next_cursor"`
		Total      amount            `json:"total"`
	}
	checkFileView struct {
		ID      string  `json:"id"`
		EntryID *string `json:"entry_id"`
		Path    string  `json:"path"`
		Member  *string `json:"member"`
		Kind    string  `json:"kind"`
		Size    int64   `json:"size"`
		Verdict string  `json:"verdict"`
		Class   *string `json:"class"`
		Copy    *struct {
			SourceID string `json:"source_id"`
			Path     string `json:"path"`
			HardLink bool   `json:"hard_link"`
		} `json:"copy"`
		Confirmed    bool `json:"confirmed"`
		ItemReadable bool `json:"item_readable"`
	}
)

// summary is an item as "op state reason from -> to".
func (it itemView) summary() string {
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

// wantItems fails unless items read exactly want, in order.
func wantItems(t *testing.T, items []itemView, want ...string) {
	t.Helper()
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = it.summary()
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("items:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// plan sends a plan command, which must answer 201, and returns the plan
// with every item (following the items' pages).
func (w *world) plan(name, body string) planView {
	w.t.Helper()
	var p planView
	decode(w.t, w.ok(http.StatusCreated, name, body), &p)
	if p.NextCursor != nil {
		p.Items = append(p.Items, w.items(p.Action.ID, "cursor="+*p.NextCursor)...)
	}
	return p
}

// items reads every page of an action's items, with query appended.
func (w *world) items(id, query string) []itemView {
	w.t.Helper()
	var out []itemView
	for {
		var p struct {
			Items      []itemView `json:"items"`
			NextCursor *string    `json:"next_cursor"`
		}
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

// action reads an action from the history.
func (w *world) action(id string) actionView {
	w.t.Helper()
	var a actionView
	w.get("/api/history/"+id, &a)
	return a
}

// run runs a planned action, waits for every job to end, and returns the
// action as the history reads it then.
func (w *world) run(action string) actionView {
	w.t.Helper()
	w.ok(http.StatusAccepted, "run-action", fmt.Sprintf(`{"action_id":%q}`, action))
	w.idle()
	return w.action(action)
}

// decide sets the own decision of the entry at path of src.
func (w *world) decide(src domain.SourceID, path, decision string) {
	w.t.Helper()
	w.ok(http.StatusOK, "set-decision", fmt.Sprintf(`{"entry_id":%q,"decision":%q}`, w.id(src, path), decision))
}

// quarantined reads every page of a source's quarantine.
func (w *world) quarantined(src domain.SourceID) quarantineView {
	w.t.Helper()
	var all quarantineView
	cursor := ""
	for {
		var p quarantineView
		path := "/api/quarantine?source=" + string(src)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w.get(path, &p)
		all.Items = append(all.Items, p.Items...)
		all.Total = p.Total
		if p.NextCursor == nil {
			return all
		}
		cursor = *p.NextCursor
	}
}

// checkPurge starts a check of entry IDs, waits for it to end, and returns
// its ID and Check.
func (w *world) checkPurge(entries ...string) (string, checkJSON) {
	w.t.Helper()
	var started checkStarted
	decode(w.t, w.ok(http.StatusAccepted, "check-purge", `{"entry_ids":`+ids(entries...)+`}`), &started)
	w.idle()
	return started.CheckID, w.check(started.CheckID)
}

// check reads a Check.
func (w *world) check(id string) checkJSON {
	w.t.Helper()
	var c checkJSON
	w.get("/api/checks/"+id, &c)
	return c
}

// checkFiles reads every page of a check's files, with query appended.
func (w *world) checkFiles(id, query string) []checkFileView {
	w.t.Helper()
	var out []checkFileView
	cursor := ""
	for {
		var p struct {
			Items      []checkFileView `json:"items"`
			NextCursor *string         `json:"next_cursor"`
		}
		path := "/api/checks/" + id + "/files?" + query
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w.get(path, &p)
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out
		}
		cursor = *p.NextCursor
	}
}

// exists reports whether rel exists below the synthfs root of src.
func (w *world) exists(src domain.SourceID, rel string) bool {
	w.t.Helper()
	d, err := w.sfs.OpenRoot(w.roots[src])
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

// readTx runs fn in a read transaction.
func (w *world) readTx(fn func(tx *sql.Tx) error) {
	w.t.Helper()
	if err := w.st.Read(context.Background(), fn); err != nil {
		w.t.Fatal(err)
	}
}
