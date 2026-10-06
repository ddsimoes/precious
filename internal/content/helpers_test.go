package content

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// testNow is the tests' clock.
var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// posix is the capability set of a local POSIX filesystem.
var posix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond,
}

// env is a store, a synthfs seen through an instrument recorder, the source
// registry, a job runner that is never started (commits go through it), the
// scanner, and the hashing service.
type env struct {
	t   *testing.T
	st  *store.Store
	sfs *synthfs.FS
	rec *instrument.Recorder
	src *sources.Service
	r   *jobs.Runner
	svc *Service
	h   config.Hashing
	a   config.Archives
	// refreshes counts the relate passes requested.
	refreshes int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, st: storetest.Open(t), sfs: synthfs.New(), h: config.Defaults().Hashing,
		a: config.Defaults().Archives}
	e.rec = instrument.Wrap(e.sfs)
	svc, err := sources.New(e.st, e.rec, config.Sources{AllowedRoots: []string{t.TempDir()}}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	e.src = svc
	e.r, err = jobs.NewRunner(jobs.Options{Store: e.st, Registry: svc, Clock: fixedClock{testNow},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	e.service()
	return e
}

// service (re)builds the hashing service with e.h and e.a.
func (e *env) service() {
	e.svc = NewService(e.st, e.src, fixedClock{testNow}, e.h, e.a, config.Defaults().Duplicates)
	e.svc.runner = e.r // the tests run handlers directly; Register is tested with a runner of its own
	e.svc.candidates = func(context.Context, store.Queryer, domain.SourceID) ([][2]relations.Range, error) {
		return nil, nil
	}
	e.svc.requestRefresh = func(*jobs.Tx) error {
		e.refreshes++
		return nil
	}
}

// disk creates the synthfs root at path on its own device with caps and adds
// it as source id, root entry included, as add-source does.
func (e *env) disk(id domain.SourceID, path string, caps fsaccess.Capabilities) *synthfs.Node {
	e.t.Helper()
	root := e.sfs.Root(path)
	e.addSource(id, path, root, caps)
	return root
}

// addSource adds the synthfs root built at path as source id, on its own
// volume with caps, root entry included.
func (e *env) addSource(id domain.SourceID, path string, root *synthfs.Node, caps fsaccess.Capabilities) {
	e.t.Helper()
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4",
		DeviceKey: "dev:" + string(id), Strong: true}
	e.sfs.SetVolume(dev, vol)
	e.sfs.SetCapabilities(dev, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root,
			device_key, capabilities, state, mount_point, created_at)
			VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0)`,
			string(id), string(id), vol.ID, vol.DeviceKey, string(capsJSON), []byte(path)); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen,
			last_seen, scan_gen) VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id))
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
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

// runJob runs one attempt of a job of kind on src under a running job row,
// as the runner would, and returns the handler's error.
func (e *env) runJob(ctx context.Context, kind jobs.Kind, src domain.SourceID, payload string, h jobs.Handler,
	rt jobs.Runtime) error {
	e.t.Helper()
	if payload == "" {
		payload = "{}"
	}
	var id int64
	err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES (?, 1, ?, ?, 'running', 0, 3, 0, 0, 0)
		RETURNING id`, string(kind), payload, string(src)).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	job := jobs.Job{ID: domain.JobID(id), Kind: kind, PayloadVersion: 1, Payload: json.RawMessage(payload),
		SourceID: src, Attempt: 1}
	runErr := h.Run(ctx, job, rt)
	state := "succeeded"
	if runErr != nil {
		state = "failed"
	}
	if _, err := e.st.Writer().Exec(`UPDATE jobs SET state = ? WHERE id = ?`, state, id); err != nil {
		e.t.Fatal(err)
	}
	return runErr
}

// scan runs a complete scan of src.
func (e *env) scan(src domain.SourceID) {
	e.t.Helper()
	h := index.NewHandler(e.st, e.src, rules.Default(), fixedClock{testNow}, config.Defaults().Scan)
	if err := e.runJob(context.Background(), jobs.KindScan, src, "", h, &fakeRuntime{}); err != nil {
		e.t.Fatalf("scan %s: %v", src, err)
	}
}

// hashWith runs one hash attempt of src with rt.
func (e *env) hashWith(ctx context.Context, src domain.SourceID, rt jobs.Runtime) error {
	e.t.Helper()
	return e.runJob(ctx, KindHash, src, "", &handler{s: e.svc}, rt)
}

// hash runs a complete hash job of src and fails the test on any error.
func (e *env) hash(src domain.SourceID) *fakeRuntime {
	e.t.Helper()
	rt := &fakeRuntime{}
	if err := e.hashWith(context.Background(), src, rt); err != nil {
		e.t.Fatalf("hash %s: %v", src, err)
	}
	e.checkCoverage()
	return rt
}

// hashNow runs a complete hash_now job of src over the given entries.
func (e *env) hashNow(src domain.SourceID, ids ...domain.EntryID) {
	e.t.Helper()
	payload := string(marshal(nowPayload{Entries: ids}))
	if err := e.runJob(context.Background(), KindHashNow, src, payload, &handler{s: e.svc, now: true},
		&fakeRuntime{}); err != nil {
		e.t.Fatalf("hash_now %s: %v", src, err)
	}
}

// plan runs the planning pass alone.
func (e *env) plan() {
	e.t.Helper()
	if _, err := e.svc.plan(context.Background(), &fakeRuntime{}, ""); err != nil {
		e.t.Fatal(err)
	}
	e.checkCoverage()
}

// id returns the entry ID of a path of src.
func (e *env) id(src domain.SourceID, path string) domain.EntryID {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(src),
		[]byte(path)).Scan(&id); err != nil {
		e.t.Fatalf("entry %s:%q: %v", src, path, err)
	}
	return domain.EntryID(id)
}

// state returns the content state of a path of src, "" without a row.
func (e *env) state(src domain.SourceID, path string) domain.ContentState {
	e.t.Helper()
	var s string
	err := e.st.Reader().QueryRow(`SELECT f.state FROM file_content f JOIN entries e ON e.id = f.entry_id
		WHERE e.source_id = ? AND e.path = ?`, string(src), []byte(path)).Scan(&s)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return domain.ContentState(s)
}

// digest returns the hex SHA-256 recorded for a path of src, "" without.
func (e *env) digest(src domain.SourceID, path string) string {
	e.t.Helper()
	var sum []byte
	err := e.st.Reader().QueryRow(`SELECT c.sha256 FROM file_content f JOIN entries e ON e.id = f.entry_id
		JOIN contents c ON c.id = f.content_id WHERE e.source_id = ? AND e.path = ?`, string(src), []byte(path)).Scan(&sum)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return hex.EncodeToString(sum)
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// checkCoverage compares content_coverage with a direct GROUP BY of the
// rows it counts.
func (e *env) checkCoverage() {
	e.t.Helper()
	direct := map[string][8]int64{}
	rows, err := e.st.Reader().Query(`SELECT source_id, state, count(*), sum(size) FROM (` + coverageRows + `)
		GROUP BY source_id, state`)
	if err != nil {
		e.t.Fatal(err)
	}
	for rows.Next() {
		var src string
		var state domain.ContentState
		var n, size int64
		if err := rows.Scan(&src, &state, &n, &size); err != nil {
			e.t.Fatal(err)
		}
		c := direct[src]
		if b := bucket(state); b != 0 {
			c[0] += n
			c[1] += size
			c[2*b] += n
			c[2*b+1] += size
		}
		direct[src] = c
	}
	rows.Close()
	got := map[string][8]int64{}
	rows, err = e.st.Reader().Query(`SELECT source_id, candidate_files, candidate_bytes, checked_files, checked_bytes,
		unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes FROM content_coverage`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var src string
		var c [8]int64
		if err := rows.Scan(&src, &c[0], &c[1], &c[2], &c[3], &c[4], &c[5], &c[6], &c[7]); err != nil {
			e.t.Fatal(err)
		}
		if c != [8]int64{} {
			got[src] = c
		}
	}
	for src, c := range direct {
		if c == [8]int64{} {
			delete(direct, src)
		}
	}
	if fmt.Sprint(sortedCov(got)) != fmt.Sprint(sortedCov(direct)) {
		e.t.Errorf("content_coverage %v; direct GROUP BY %v", sortedCov(got), sortedCov(direct))
	}
}

func sortedCov(m map[string][8]int64) []string {
	var out []string
	for k, v := range m {
		out = append(out, fmt.Sprint(k, v))
	}
	sort.Strings(out)
	return out
}

// opened returns the paths below root, '/'-joined, that OpenFile opened
// since the recorder was last reset, in order.
func (e *env) opened() []string {
	var out []string
	for _, c := range e.rec.Calls() {
		if c.Op == instrument.OpOpenFile && c.Err == nil {
			out = append(out, string(joinPath(c.Path)))
		}
	}
	return out
}

// readOf returns the content bytes ReadAt returned for the file at path
// below its root since the recorder was last reset.
func (e *env) readOf(path string) int64 {
	var n int64
	for _, c := range e.rec.Calls() {
		if c.Op == instrument.OpReadAt && string(joinPath(c.Path)) == path {
			n += int64(c.Bytes)
		}
	}
	return n
}

// groups returns the duplicate groups as sorted lists of copies: a file's
// source-relative path, a member's archive path, '!', and its member path;
// files on other sources than first are prefixed with their source and ':'.
func (e *env) groups() [][]string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT content_id, source_id, path FROM (
			SELECT f.content_id, e.source_id, e.path FROM file_content f JOIN entries e ON e.id = f.entry_id
				WHERE f.content_id IS NOT NULL AND e.state = 'present'
			UNION ALL
			SELECT m.content_id, e.source_id, e.path || X'21' || m.path FROM archive_members m
				JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = a.entry_id
				WHERE m.content_id IS NOT NULL AND m.kind = 'file' AND a.state = 'complete' AND e.state = 'present')`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	by := map[int64][]string{}
	for rows.Next() {
		var id int64
		var src string
		var path []byte
		if err := rows.Scan(&id, &src, &path); err != nil {
			e.t.Fatal(err)
		}
		by[id] = append(by[id], src+":"+string(path))
	}
	var out [][]string
	for _, g := range by {
		if len(g) > 1 {
			slices.Sort(g)
			out = append(out, g)
		}
	}
	slices.SortFunc(out, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	return out
}

// diffGroups reports the groups only in got and only in want.
func diffGroups(t *testing.T, got, want [][]string) {
	t.Helper()
	key := func(g []string) string { return strings.Join(g, " | ") }
	in := func(gs [][]string) map[string]bool {
		m := map[string]bool{}
		for _, g := range gs {
			m[key(g)] = true
		}
		return m
	}
	g, w := in(got), in(want)
	for k := range g {
		if !w[k] {
			t.Errorf("unexpected group: %q", k)
		}
	}
	for k := range w {
		if !g[k] {
			t.Errorf("missing group: %q", k)
		}
	}
}
