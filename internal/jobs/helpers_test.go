package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

// waitTimeout bounds every wait on a channel; tests never sleep.
const waitTimeout = 10 * time.Second

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type unresponsiveCall struct {
	Source domain.SourceID
	Since  time.Time // zero: the flag was cleared
}

// fakeRegistry serves the device keys recordDevice set and records
// SetUnresponsive calls.
type fakeRegistry struct {
	mu    sync.Mutex
	keys  map[domain.SourceID]string
	calls []unresponsiveCall
}

var _ Registry = (*fakeRegistry)(nil)

func (f *fakeRegistry) DeviceKey(_ context.Context, _ store.Queryer, id domain.SourceID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[id], nil
}

func (f *fakeRegistry) SetUnresponsive(_ context.Context, id domain.SourceID, since time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, unresponsiveCall{Source: id, Since: since})
	return nil
}

func (f *fakeRegistry) unresponsiveCalls() []unresponsiveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]unresponsiveCall(nil), f.calls...)
}

func openStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := storetest.Dir(t)
	st, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, dir
}

func addSource(t *testing.T, st *store.Store, id domain.SourceID) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong,
			rel_root, capabilities, state, created_at)
			VALUES (?, ?, 'path', ?, 'ext4', 0, X'', '{}', 'online', 0)`, string(id), string(id), "/srv/"+string(id))
		return err
	})
	if err != nil {
		t.Fatalf("add source %s: %v", id, err)
	}
}

// recordDevice makes reg report the source's device, as the sources service
// does once it knows the volume; jobs claimed afterwards use the key
// "dev:<dev>".
func recordDevice(reg *fakeRegistry, id domain.SourceID, dev int64) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.keys == nil {
		reg.keys = map[domain.SourceID]string{}
	}
	reg.keys[id] = "dev:" + strconv.FormatInt(dev, 10)
}

type handlerFunc func(ctx context.Context, job Job, rt Runtime) error

func (f handlerFunc) Run(ctx context.Context, job Job, rt Runtime) error { return f(ctx, job, rt) }

type env struct {
	st    *store.Store
	clock *fakeClock
	reg   *fakeRegistry
	cfg   config.Jobs
}

func newEnv(t *testing.T) *env {
	st, _ := openStore(t)
	return &env{st: st, clock: newFakeClock(), reg: &fakeRegistry{}, cfg: config.Defaults().Jobs}
}

// runner builds a runner whose periodic duties run only through tick.
func (e *env) runner(t *testing.T, handlers map[Kind]Handler, mod ...func(*Options)) *Runner {
	t.Helper()
	opts := Options{
		Store:    e.st,
		Registry: e.reg,
		Config:   e.cfg,
		Clock:    e.clock,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, m := range mod {
		m(&opts)
	}
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r.manualTick = make(chan chan struct{})
	for k, h := range handlers {
		r.Register(k, h)
	}
	return r
}

func start(t *testing.T, r *Runner) {
	t.Helper()
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if err := r.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
}

// tick runs one tick on the runner's loop and waits for it to complete.
func tick(t *testing.T, r *Runner) {
	t.Helper()
	done := make(chan struct{})
	select {
	case r.manualTick <- done:
	case <-time.After(waitTimeout):
		t.Fatal("tick: loop not receiving")
	}
	recv(t, done, "tick completion")
}

// abandon simulates the death of a runner's process: its loop stops, so it
// never renews a lease or claims a job again, while its handlers stay blocked.
func abandon(r *Runner) {
	r.loopCancel()
	<-r.loopDone
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitTimeout):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// waitJob waits until the job satisfies cond, re-checking after every
// committed event.
func waitJob(t *testing.T, r *Runner, id domain.JobID, what string, cond func(Record) bool) Record {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		changed := r.hub.changed()
		rec, err := r.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if cond(rec) {
			return rec
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("timed out waiting for job %s to be %s; last %+v", id, what, rec)
		}
	}
}

func inState(s domain.JobState) func(Record) bool {
	return func(r Record) bool { return r.State == s }
}

func enqueue(t *testing.T, r *Runner, spec Spec) Record {
	t.Helper()
	rec, err := r.Enqueue(context.Background(), spec)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return rec
}

func mustGet(t *testing.T, r *Runner, id domain.JobID) Record {
	t.Helper()
	rec, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return rec
}

type loggedEvent struct {
	ID    int64
	Type  string
	Event Event
}

func jobEvents(t *testing.T, st *store.Store, id domain.JobID) []loggedEvent {
	t.Helper()
	rows, err := st.Reader().Query(`SELECT id, type, payload FROM job_events WHERE job_id = ? ORDER BY id`, int64(id))
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	var out []loggedEvent
	for rows.Next() {
		var e loggedEvent
		var payload string
		if err := rows.Scan(&e.ID, &e.Type, &payload); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		if err := json.Unmarshal([]byte(payload), &e.Event); err != nil {
			t.Fatalf("decode event %d: %v", e.ID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("events: %v", err)
	}
	return out
}

func states(evs []loggedEvent) []domain.JobState {
	var out []domain.JobState
	for _, e := range evs {
		if e.Type == eventState {
			out = append(out, e.Event.State)
		}
	}
	return out
}

var _ clock.Clock = (*fakeClock)(nil)
