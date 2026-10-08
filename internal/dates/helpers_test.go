package dates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// testNow is the tests' starting time.
var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// testClock is a clock the test sets.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// posix is the capability set of a local POSIX filesystem.
var posix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond, NoReplaceRename: true,
}

// The corpus source every corpus test shares (newCorpusEnv).
const (
	corpusSource domain.SourceID = "corpus"
	corpusRoot                   = "/mnt/corpus"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// env is a store, a synthfs seen through an instrument recorder, the source
// registry, a job runner that is not started (commits go through it), and
// the dates service in the zone UTC, on a clock at testNow.
type env struct {
	t   *testing.T
	st  *store.Store
	sfs *synthfs.FS
	rec *instrument.Recorder
	clk *testClock
	src *sources.Service
	r   *jobs.Runner
	svc *Service
}

// newEnv returns an env on a new migrated store.
func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvOn(t, storetest.Open(t))
}

func newEnvOn(t *testing.T, st *store.Store) *env {
	t.Helper()
	e := &env{t: t, st: st, sfs: synthfs.New(), clk: &testClock{t: testNow}}
	e.rec = instrument.Wrap(e.sfs)
	svc, err := sources.New(e.st, e.rec, config.Sources{AllowedRoots: []string{t.TempDir()}, AllowWrites: true}, e.clk)
	if err != nil {
		t.Fatal(err)
	}
	e.src = svc
	e.r, err = jobs.NewRunner(jobs.Options{Store: e.st, Registry: svc, Clock: e.clk, Logger: discard()})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = New(Options{Store: e.st, Runner: e.r, Sources: e.src, Zone: time.UTC, Clock: e.clk, Logger: discard()})
	return e
}

// The corpus, built on synthfs, added as source "corpus", and scanned once
// per package: each corpus env starts from a copy of that database and a
// synthfs built the same way, which gives every file the same identity.
var corpusDB struct {
	sync.Mutex
	db []byte
}

// newCorpusEnv returns an env holding the scanned corpus as source
// "corpus" at /mnt/corpus (posix capabilities, writes on), its root, and
// its ground truth. Nothing is read or derived yet.
func newCorpusEnv(t *testing.T) (*env, *synthfs.Node, corpus.GroundTruth) {
	t.Helper()
	db := corpusTemplate(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, store.FileName), db, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := newEnvOn(t, st)
	root, truth := corpus.BuildSynth(e.sfs, corpusRoot, corpus.Corpus())
	e.mount(corpusSource, root, posix)
	return e, root, truth
}

// corpusTemplate returns the bytes of a closed database holding the
// scanned corpus, building it on first use.
func corpusTemplate(t *testing.T) []byte {
	t.Helper()
	corpusDB.Lock()
	defer corpusDB.Unlock()
	if corpusDB.db != nil {
		return corpusDB.db
	}
	dir := storetest.Dir(t)
	st, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvOn(t, st)
	root, _ := corpus.BuildSynth(e.sfs, corpusRoot, corpus.Corpus())
	e.addSource(corpusSource, corpusRoot, root, posix)
	e.scan(corpusSource)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, store.FileName+"-wal")); err == nil && fi.Size() > 0 {
		t.Fatal("the corpus database kept a non-empty write-ahead log after Close")
	}
	db, err := os.ReadFile(filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	corpusDB.db = db
	return db
}

// mount gives root's synthfs device the volume and capabilities of source
// id, as addSource records them.
func (e *env) mount(id domain.SourceID, root *synthfs.Node, caps fsaccess.Capabilities) fsaccess.Volume {
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4",
		DeviceKey: "dev:" + string(id), Strong: true}
	e.sfs.SetVolume(dev, vol)
	e.sfs.SetCapabilities(dev, caps)
	return vol
}

// disk creates the synthfs root at path on its own device with caps, runs
// build on it, adds it as source id, and scans it.
func (e *env) disk(id domain.SourceID, path string, caps fsaccess.Capabilities, build func(root *synthfs.Node)) *synthfs.Node {
	e.t.Helper()
	root := e.sfs.Root(path)
	if build != nil {
		build(root)
	}
	e.addSource(id, path, root, caps)
	e.scan(id)
	return root
}

// addSource adds the synthfs root built at path as source id, online with
// writes on, root entry included, as add-source and set-source-writes leave
// it.
func (e *env) addSource(id domain.SourceID, path string, root *synthfs.Node, caps fsaccess.Capabilities) {
	e.t.Helper()
	vol := e.mount(id, root, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key, capabilities,
		state, mount_point, created_at, write_enabled) VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0, 1)`,
		string(id), string(id), vol.ID, vol.DeviceKey, string(capsJSON), []byte(path))
	e.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id))
}

// fakeRuntime records progress; Yield runs onYield and reports the
// context's end.
type fakeRuntime struct {
	mu       sync.Mutex
	progress map[string]int64
	yields   int
	onYield  func(n int)
}

func (r *fakeRuntime) Progress(c map[string]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.progress == nil {
		r.progress = map[string]int64{}
	}
	for k, v := range c {
		r.progress[k] = v
	}
}

func (r *fakeRuntime) FSCall(string) func() { return func() {} }

func (r *fakeRuntime) Yield(ctx context.Context) error {
	r.mu.Lock()
	r.yields++
	n, hook := r.yields, r.onYield
	r.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	return ctx.Err()
}

func (r *fakeRuntime) UseSource(context.Context, domain.SourceID) error { return nil }

// runJob runs one attempt (attempt, 1-based) of a job of kind on src under a
// running job row, as the runner would, and returns the handler's error;
// the row ends succeeded, or failed on an error.
func (e *env) runJob(ctx context.Context, kind jobs.Kind, src domain.SourceID, payload string, attempt int,
	h jobs.Handler, rt jobs.Runtime) error {
	e.t.Helper()
	if payload == "" {
		payload = "{}"
	}
	var id int64
	err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES (?, 1, ?, ?, 'running', ?, 3, 0, 0, 0)
		RETURNING id`, string(kind), payload, string(src), attempt-1).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	job := jobs.Job{ID: domain.JobID(id), Kind: kind, PayloadVersion: 1, Payload: json.RawMessage(payload),
		SourceID: src, Attempt: attempt}
	runErr := h.Run(ctx, job, rt)
	state := "succeeded"
	if runErr != nil {
		state = "failed"
	}
	e.exec(`UPDATE jobs SET state = ? WHERE id = ?`, state, id)
	return runErr
}

// scan runs a complete scan of src.
func (e *env) scan(src domain.SourceID) {
	e.t.Helper()
	h := index.NewHandler(e.st, e.src, rules.Default(), e.clk, config.Defaults().Scan)
	if err := e.runJob(context.Background(), jobs.KindScan, src, "", 1, h, &fakeRuntime{}); err != nil {
		e.t.Fatalf("scan %s: %v", src, err)
	}
}

// open opens the folder at the raw path p of src's synthfs root.
func (e *env) open(root, p string) fsaccess.Dir {
	e.t.Helper()
	dir, err := e.sfs.OpenRoot(root)
	if err != nil {
		e.t.Fatal(err)
	}
	if p == "" {
		return dir
	}
	for _, name := range strings.Split(p, "/") {
		info, err := dir.Lstat([]byte(name))
		if err != nil {
			e.t.Fatalf("lstat %q in %q: %v", name, p, err)
		}
		next, err := dir.OpenDir([]byte(name), info)
		dir.Close()
		if err != nil {
			e.t.Fatalf("open %q in %q: %v", name, p, err)
		}
		dir = next
	}
	return dir
}

// facts lstats the entry at the raw path p below root, as the executor
// confirms a step.
func (e *env) facts(root, p string) index.PostFacts {
	e.t.Helper()
	var info fsaccess.EntryInfo
	if p == "" {
		dir := e.open(root, "")
		info = dir.Self()
		dir.Close()
	} else {
		parent, name := splitPath(p)
		dir := e.open(root, parent)
		var err error
		info, err = dir.Lstat([]byte(name))
		dir.Close()
		if err != nil {
			e.t.Fatalf("lstat %q: %v", p, err)
		}
	}
	f := index.PostFacts{Dev: info.Dev, Ino: info.Ino, MtimeNs: info.ModTime.UnixNano()}
	if !info.Ctime.IsZero() {
		f.CtimeNs = info.Ctime.UnixNano()
	}
	return f
}

// move moves src's entry at the raw path from into the folder to (root
// path at root) under the same name, on disk and in the index, as the
// executor's outcome transaction does (r3 design D6).
func (e *env) move(src domain.SourceID, root, from, to string) {
	e.t.Helper()
	parent, name := splitPath(from)
	sd, dd := e.open(root, parent), e.open(root, to)
	w, ok := fsaccess.AsWriter(sd)
	if !ok {
		e.t.Fatal("synthfs folders write")
	}
	err := w.RenameNoReplace([]byte(name), dd, []byte(name))
	sd.Close()
	dd.Close()
	if err != nil {
		e.t.Fatalf("rename %q into %q: %v", from, to, err)
	}
	m := index.Move{Source: src, Entry: e.id(src, from), NewParent: e.id(src, to), NewName: []byte(name),
		Facts: e.facts(root, join(to, name)), OldParentFacts: e.facts(root, parent), NewParentFacts: e.facts(root, to)}
	oldParent := e.id(src, parent)
	rf := index.NewRefolder(rules.Default())
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, _, err := index.MoveEntry(context.Background(), tx, m); err != nil {
			return err
		}
		return rf.Refold(context.Background(), tx, src, []domain.EntryID{m.Entry, oldParent})
	})
	if err != nil {
		e.t.Fatalf("move %q into %q: %v", from, to, err)
	}
}

func splitPath(p string) (parent, name string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// write runs fn in one write transaction of the jobs runner.
func (e *env) write(fn func(tx *jobs.Tx) error) {
	e.t.Helper()
	if err := e.r.Write(context.Background(), fn); err != nil {
		e.t.Fatal(err)
	}
}

// rederive runs Rederive over ids in one write transaction.
func (e *env) rederive(ids ...domain.EntryID) {
	e.t.Helper()
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return e.svc.Rederive(context.Background(), tx, ids)
	}); err != nil {
		e.t.Fatal(err)
	}
}

// expand runs ExpandTargets in a transaction that is rolled back.
func (e *env) expand(t Targets, max int) (Expanded, error) {
	e.t.Helper()
	return e.expandAs(t, max, false)
}

// expandAs runs expandTargets, with flagged as set-date-correction passes
// it, in a transaction that is rolled back.
func (e *env) expandAs(t Targets, max int, flagged bool) (Expanded, error) {
	e.t.Helper()
	var out Expanded
	errDone := errors.New("done")
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = expandTargets(context.Background(), tx, t, max, flagged)
		if err != nil {
			return err
		}
		return errDone
	})
	if errors.Is(err, errDone) {
		err = nil
	}
	return out, err
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
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

// id returns the ID of src's entry at the raw path p.
func (e *env) id(src domain.SourceID, p string) domain.EntryID {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(src),
		[]byte(p)).Scan(&id); err != nil {
		e.t.Fatalf("entry %s:%q: %v", src, p, err)
	}
	return domain.EntryID(id)
}

// ref is the API's string ID of src's entry at p.
func (e *env) ref(src domain.SourceID, p string) string {
	e.t.Helper()
	return domain.Ref{Entry: e.id(src, p)}.String()
}

// mediaIDs returns src's media entries (MediaCond), in path order.
func (e *env) mediaIDs(src domain.SourceID) []domain.EntryID {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT e.id FROM entries e WHERE e.source_id = ? AND `+MediaCond("e")+`
		ORDER BY e.path`, string(src))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []domain.EntryID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, domain.EntryID(id))
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// storedSummary returns src's media_sources.summary.
func (e *env) storedSummary(src domain.SourceID) summary {
	e.t.Helper()
	s, err := readSummary(context.Background(), e.st.Reader(), src)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// recounted returns src's summary as a full recount gives it.
func (e *env) recounted(src domain.SourceID) summary {
	e.t.Helper()
	s, err := recount(context.Background(), e.st.Reader(), src)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}
