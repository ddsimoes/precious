package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// testNow is the tests' clock.
var testNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// posix is a local POSIX filesystem with the no-replace rename.
var posix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond, NoReplaceRename: true,
}

// fat is a case-insensitive filesystem without stable identity, with the
// no-replace rename (exfat, vfat).
var fat = fsaccess.Capabilities{
	Known: true, TimeResolution: 2 * time.Second, NoReplaceRename: true,
}

// srcID is the tests' source.
const srcID domain.SourceID = "disk"

// env is a store, a filesystem seen through an instrument recorder, the
// source registry, a job runner that is never started (commits go through
// it), a fake index, and the executor.
type env struct {
	t   *testing.T
	st  *store.Store
	sfs *synthfs.FS
	rec *instrument.Recorder
	src *sources.Service
	r   *jobs.Runner
	idx *fakeIndex
	ex  *Executor
	// allowWrites and hooks shape the next executor (executor()).
	allowWrites bool
	hooks       Hooks
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newEnv is an env over a synthfs.
func newEnv(t *testing.T) *env {
	t.Helper()
	sfs := synthfs.New()
	e := newEnvOn(t, sfs, t.TempDir())
	e.sfs = sfs
	return e
}

// newEnvOn is an env over fs, with root as the allowed root.
func newEnvOn(t *testing.T, fs fsaccess.FS, root string) *env {
	t.Helper()
	e := &env{t: t, st: storetest.Open(t), allowWrites: true}
	e.rec = instrument.Wrap(fs)
	svc, err := sources.New(e.st, e.rec, config.Sources{AllowedRoots: []string{root}}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	e.src = svc
	e.idx = &fakeIndex{}
	e.executor()
	return e
}

// executor builds a new executor and runner over the same database, as a
// restarted process would.
func (e *env) executor() *Executor {
	e.t.Helper()
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: e.src, Clock: fixedClock{testNow}, Logger: discard()})
	if err != nil {
		e.t.Fatal(err)
	}
	e.r = r
	e.ex = New(Options{Store: e.st, Sources: e.src, Index: e.idx, AllowWrites: e.allowWrites, Clock: fixedClock{testNow},
		Logger: discard(), Hooks: e.hooks})
	e.ex.Register(r)
	return e.ex
}

// disk creates the synthfs root at path on its own device with caps and adds
// it as srcID, writes allowed, root entry included, then scans it.
func (e *env) disk(path string, caps fsaccess.Capabilities, build func(root *synthfs.Node)) *synthfs.Node {
	e.t.Helper()
	root := e.sfs.Root(path)
	if build != nil {
		build(root)
	}
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-disk", FSType: "ext4", DeviceKey: "dev:disk", Strong: true}
	e.sfs.SetVolume(dev, vol)
	e.sfs.SetCapabilities(dev, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key, capabilities,
		state, mount_point, created_at, write_enabled) VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0, 1)`,
		string(srcID), string(srcID), vol.ID, vol.DeviceKey, string(capsJSON), []byte(path))
	e.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(srcID))
	e.scan()
	return root
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

// fakeRuntime records progress; Yield reports the context's end.
type fakeRuntime struct {
	mu       sync.Mutex
	progress map[string]int64
	calls    []string
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

func (r *fakeRuntime) FSCall(op string) func() {
	r.mu.Lock()
	r.calls = append(r.calls, op)
	r.mu.Unlock()
	return func() {}
}

func (r *fakeRuntime) Yield(ctx context.Context) error                  { return ctx.Err() }
func (r *fakeRuntime) UseSource(context.Context, domain.SourceID) error { return nil }

// scan runs a complete scan of srcID, as a running scan job.
func (e *env) scan() {
	e.t.Helper()
	h := index.NewHandler(e.st, e.src, rules.Default(), fixedClock{testNow}, config.Defaults().Scan)
	var id int64
	if err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)
		RETURNING id`, string(srcID)).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	job := jobs.Job{ID: domain.JobID(id), Kind: jobs.KindScan, PayloadVersion: 1, Payload: json.RawMessage("{}"),
		SourceID: srcID, Attempt: 1}
	if err := h.Run(context.Background(), job, &fakeRuntime{}); err != nil {
		e.t.Fatalf("scan: %v", err)
	}
	e.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, id)
}

// id returns the entry ID of a path of srcID.
func (e *env) id(path string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(srcID),
		[]byte(path)).Scan(&id); err != nil {
		e.t.Fatalf("entry %q: %v", path, err)
	}
	return id
}

// step describes one planned item: a rename of From into To/Name (To ""
// is the source root), a mkdir of Name in To (or in the folder of item
// ToSeq), or an rmdir of From.
type step struct {
	Op       string
	From     string
	To       string
	ToSeq    int
	Name     string
	Reverses int64
}

// action inserts a queued action of kind with its planned items, as
// run-action leaves it, and enqueues its job. It returns the action ID.
func (e *env) action(kind string, bulk bool, steps ...step) int64 {
	e.t.Helper()
	var action int64
	err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		q := tx.SQL()
		if err := q.QueryRow(`INSERT INTO actions (kind, source_id, state, bulk, created_at)
			VALUES (?, ?, 'queued', ?, 0) RETURNING id`, kind, string(srcID), bulk).Scan(&action); err != nil {
			return err
		}
		for i, s := range steps {
			var (
				entry, fromParent, toParent, toSeq, reverses any
				fromName, fromPath, toName                   any
			)
			if s.From != "" {
				var parent sql.NullInt64
				var name, path []byte
				if err := q.QueryRow(`SELECT id, parent_id, name, path FROM entries WHERE source_id = ? AND path = ?`,
					string(srcID), []byte(s.From)).Scan(&entry, &parent, &name, &path); err != nil {
					return err
				}
				fromParent, fromName, fromPath = parent.Int64, name, path
			}
			switch {
			case s.ToSeq != 0:
				toSeq = s.ToSeq
			case s.Op != opRmdir:
				var id int64
				if err := q.QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(srcID),
					[]byte(s.To)).Scan(&id); err != nil {
					return err
				}
				toParent = id
			}
			if s.Op != opRmdir {
				toName = []byte(s.Name)
			}
			if s.Reverses != 0 {
				reverses = s.Reverses
			}
			if _, err := q.Exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name,
				from_path, to_parent, to_dir_seq, to_name, state, reverses) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'planned', ?)`,
				action, i+1, s.Op, entry, fromParent, fromName, fromPath, toParent, toSeq, toName, reverses); err != nil {
				return err
			}
		}
		_, err := Enqueue(tx, srcID, action)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return action
}

// jobOf returns the job of an action.
func (e *env) jobOf(action int64) domain.JobID {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT job_id FROM actions WHERE id = ?`, action).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return domain.JobID(id)
}

// attempt runs one attempt of job with ex, as the runner would after
// claiming it: the job row is running meanwhile. It returns the handler's
// error; a nil error leaves the job succeeded, a deferral queued, and any
// other error (a simulated crash) running, as a dead process leaves it.
func (e *env) attempt(job domain.JobID) error {
	e.t.Helper()
	return e.attemptWith(context.Background(), job, &fakeRuntime{})
}

func (e *env) attemptWith(ctx context.Context, job domain.JobID, rt jobs.Runtime) error {
	e.t.Helper()
	rec, err := e.r.Get(context.Background(), job)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`UPDATE jobs SET state = 'running' WHERE id = ?`, int64(job))
	runErr := (&handler{e: e.ex}).Run(ctx, jobs.Job{ID: rec.ID, Kind: rec.Kind, PayloadVersion: 1,
		Payload: rec.Payload, SourceID: rec.SourceID, Attempt: 1}, rt)
	var d *jobs.Defer
	switch {
	case runErr == nil:
		e.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, int64(job))
	case errors.As(runErr, &d):
		e.exec(`UPDATE jobs SET state = 'queued' WHERE id = ?`, int64(job))
	}
	return runErr
}

// run runs the action's job to its end and fails the test on any error.
func (e *env) run(action int64) {
	e.t.Helper()
	if err := e.attempt(e.jobOf(action)); err != nil {
		e.t.Fatalf("action %d: %v", action, err)
	}
}

// itemRow is an item's outcome.
type itemRow struct {
	State, Reason, Detail string
	ReversedBy            int64
	ID, Entry             int64
	Created               bool
}

func (e *env) item(action int64, seq int) itemRow {
	e.t.Helper()
	var (
		r                 itemRow
		reason, detail    sql.NullString
		reversedBy, entry sql.NullInt64
	)
	if err := e.st.Reader().QueryRow(`SELECT id, state, reason, detail, reversed_by, entry_id, created FROM action_items
		WHERE action_id = ? AND seq = ?`, action, seq).Scan(&r.ID, &r.State, &reason, &detail, &reversedBy, &entry,
		&r.Created); err != nil {
		e.t.Fatal(err)
	}
	r.Reason, r.Detail, r.ReversedBy, r.Entry = reason.String, detail.String, reversedBy.Int64, entry.Int64
	return r
}

// states returns the action's state and its items' states in seq order.
func (e *env) states(action int64) (string, []string) {
	e.t.Helper()
	var state string
	if err := e.st.Reader().QueryRow(`SELECT state FROM actions WHERE id = ?`, action).Scan(&state); err != nil {
		e.t.Fatal(err)
	}
	rows, err := e.st.Reader().Query(`SELECT state FROM action_items WHERE action_id = ? ORDER BY seq`, action)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		items = append(items, s)
	}
	return state, items
}

func (e *env) wantStates(action int64, wantAction string, wantItems ...string) {
	e.t.Helper()
	got, items := e.states(action)
	if got != wantAction || strings.Join(items, ",") != strings.Join(wantItems, ",") {
		e.t.Fatalf("action %d is %s with items %v, want %s with %v", action, got, items, wantAction, wantItems)
	}
}

// lstat looks at the absolute path through the recorder's FS, bypassing the
// log: it opens the source root's filesystem directly.
func (e *env) lstat(root, rel string) (fsaccess.EntryInfo, bool) {
	e.t.Helper()
	var fs fsaccess.FS = e.sfs
	d, err := fs.OpenRoot(root)
	if err != nil {
		e.t.Fatal(err)
	}
	defer d.Close()
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		info, err := d.Lstat([]byte(p))
		if err != nil {
			return fsaccess.EntryInfo{}, false
		}
		next, err := d.OpenDir([]byte(p), info)
		if err != nil {
			e.t.Fatal(err)
		}
		defer next.Close()
		d = next
	}
	info, err := d.Lstat([]byte(parts[len(parts)-1]))
	if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeAbsent {
		return fsaccess.EntryInfo{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return info, true
}

// count runs a count query.
func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// writes counts the Writer calls that change something (not Sync).
func (e *env) writes() int {
	return e.rec.Count(instrument.OpRename) + e.rec.Count(instrument.OpMkdir) + e.rec.Count(instrument.OpRmdir)
}

// fakeIndex applies the minimal row updates of a done step and records the
// calls.
type fakeIndex struct {
	mu         sync.Mutex
	renames    []index.Move
	mkdirs     []index.NewFolder
	rmdirs     []domain.EntryID
	actionDone int
	// fail makes every Apply* fail.
	fail error
	// onApply runs at the start of every Apply* call.
	onApply func()
}

func (f *fakeIndex) begin() error {
	f.mu.Lock()
	hook, fail := f.onApply, f.fail
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return fail
}

func (f *fakeIndex) ApplyRename(ctx context.Context, tx *sql.Tx, m index.Move) error {
	f.mu.Lock()
	f.renames = append(f.renames, m)
	f.mu.Unlock()
	if err := f.begin(); err != nil {
		return err
	}
	var old, parent []byte
	if err := tx.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ?`, int64(m.Entry)).Scan(&old); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ?`, int64(m.NewParent)).Scan(&parent); err != nil {
		return err
	}
	neu := childPath(parent, m.NewName)
	lo, hi := append(append([]byte{}, old...), '/'), append(append([]byte{}, old...), '0')
	if _, err := tx.ExecContext(ctx, `UPDATE entries SET path = CAST(? || substr(path, ?) AS BLOB)
		WHERE source_id = ? AND path >= ? AND path < ?`, neu, len(old)+1, string(m.Source), lo, hi); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE entries SET parent_id = ?, name = ?, path = ?, mtime_ns = ?, ctime_ns = ?
		WHERE id = ?`, int64(m.NewParent), m.NewName, neu, m.Facts.MtimeNs, m.Facts.CtimeNs, int64(m.Entry))
	return err
}

func (f *fakeIndex) ApplyMkdir(ctx context.Context, tx *sql.Tx, nf index.NewFolder) (domain.EntryID, error) {
	f.mu.Lock()
	f.mkdirs = append(f.mkdirs, nf)
	f.mu.Unlock()
	if err := f.begin(); err != nil {
		return 0, err
	}
	var parent []byte
	if err := tx.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ?`, int64(nf.Parent)).Scan(&parent); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen,
		last_seen, scan_gen, dev, ino, mtime_ns, ctime_ns) VALUES (?, ?, ?, ?, 'directory', 'present', 0, 0, 0, ?, ?, ?, ?)
		RETURNING id`, string(nf.Source), int64(nf.Parent), nf.Name, childPath(parent, nf.Name), int64(nf.Facts.Dev),
		int64(nf.Facts.Ino), nf.Facts.MtimeNs, nf.Facts.CtimeNs).Scan(&id)
	return domain.EntryID(id), err
}

func (f *fakeIndex) ApplyRmdir(ctx context.Context, tx *sql.Tx, src domain.SourceID, id domain.EntryID,
	_ index.PostFacts) error {
	f.mu.Lock()
	f.rmdirs = append(f.rmdirs, id)
	f.mu.Unlock()
	if err := f.begin(); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE id = ? AND source_id = ?`, int64(id), string(src))
	return err
}

func (f *fakeIndex) ActionDone(context.Context, *jobs.Tx, domain.SourceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionDone++
	return nil
}

func (f *fakeIndex) MissingIntentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	var found bool
	lo, hi := append(append([]byte{}, path...), '/'), append(append([]byte{}, path...), '0')
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE source_id = ? AND state = 'missing'
		AND decision IS NOT NULL AND (path = ? OR (path >= ? AND path < ?)))`, string(src), path, lo, hi).Scan(&found)
	return found, err
}

func (f *fakeIndex) counts() (renames, mkdirs, rmdirs, done int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.renames), len(f.mkdirs), len(f.rmdirs), f.actionDone
}
