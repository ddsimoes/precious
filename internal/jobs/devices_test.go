package jobs

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/store"
)

// gate parks every attempt in its handler until the test releases the job.
type gate struct {
	entered chan Job
	mu      sync.Mutex
	open    map[domain.JobID]chan struct{}
}

func newGate() *gate {
	return &gate{entered: make(chan Job, 8), open: map[domain.JobID]chan struct{}{}}
}

func (g *gate) ch(id domain.JobID) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.open[id]
	if !ok {
		c = make(chan struct{})
		g.open[id] = c
	}
	return c
}

func (g *gate) Run(ctx context.Context, job Job, _ Runtime) error {
	g.entered <- job
	select {
	case <-g.ch(job.ID):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gate) release(id domain.JobID) { close(g.ch(id)) }

// Spec scenario "Device limit before the first observation" (design D14),
// with one worker per device: a source's first job runs under the
// placeholder key and keeps the source to itself until it finishes, even
// after its first observation records the source's device.
func TestDeviceLimitBeforeFirstObservation(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	g := newGate()
	r := e.runner(t, map[Kind]Handler{KindScan: g, kindWalk: g})
	start(t, r)

	scan := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, g.entered, "first scan")
	if key := mustGet(t, r, scan.ID).DeviceKey; key != "source:a" {
		t.Fatalf("first scan claimed under %q, want the placeholder source:a", key)
	}
	recordDevice(e.reg, "a", 7) // the scan's first observation
	walk := enqueue(t, r, Spec{Kind: kindWalk, SourceID: "a", ScopeKey: "node:2"})
	tick(t, r) // every due job has been considered for dispatch
	if got := mustGet(t, r, walk.ID); got.State != domain.JobQueued {
		t.Fatalf("walk on the free key dev:7 is %s while the placeholder scan runs", got.State)
	}

	g.release(scan.ID)
	if job := recv(t, g.entered, "walk"); job.ID != walk.ID {
		t.Fatalf("next job = %s, want the walk %s", job.ID, walk.ID)
	}
	if key := mustGet(t, r, walk.ID).DeviceKey; key != "dev:7" {
		t.Fatalf("walk claimed under %q, want dev:7", key)
	}
	if got := mustGet(t, r, scan.ID); got.State != domain.JobSucceeded || got.DeviceKey != "source:a" {
		t.Fatalf("finished scan = %s under %q, want succeeded under source:a", got.State, got.DeviceKey)
	}
	g.release(walk.ID)
	waitJob(t, r, walk.ID, "succeeded", inState(domain.JobSucceeded))
}

// Design D14: placeholder keys are per source, so the first jobs of two
// never-observed sources run concurrently.
func TestPlaceholderJobsOfDifferentSourcesRunConcurrently(t *testing.T) {
	e := newEnv(t)
	g := newGate()
	r := e.runner(t, map[Kind]Handler{KindScan: g})
	start(t, r)
	ids := map[domain.SourceID]domain.JobID{}
	for _, src := range []domain.SourceID{"a", "b"} {
		addSource(t, e.st, src)
		ids[src] = enqueue(t, r, Spec{Kind: KindScan, SourceID: src}).ID
	}
	got := []domain.SourceID{recv(t, g.entered, "first scan").SourceID, recv(t, g.entered, "second scan").SourceID}
	slices.Sort(got)
	if !slices.Equal(got, []domain.SourceID{"a", "b"}) {
		t.Fatalf("running scans = %v, want a and b at once", got)
	}
	for src, id := range ids {
		if key := mustGet(t, r, id).DeviceKey; key != "source:"+string(src) {
			t.Errorf("%s's scan claimed under %q, want source:%s", src, key, src)
		}
	}
}

// failingRegistry fails every device lookup.
type failingRegistry struct{ fakeRegistry }

func (f *failingRegistry) DeviceKey(context.Context, store.Queryer, domain.SourceID) (string, error) {
	return "", errors.New("device lookup failed")
}

// Design D15: without a registry, or when the registry fails, a source's jobs
// are claimed under its placeholder key, so they run one at a time.
func TestDeviceKeyFallsBackToPlaceholder(t *testing.T) {
	for name, reg := range map[string]Registry{"no registry": nil, "registry error": &failingRegistry{}} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			addSource(t, e.st, "a")
			g := newGate()
			r := e.runner(t, map[Kind]Handler{KindScan: g, kindWalk: g}, func(o *Options) { o.Registry = reg })
			start(t, r)

			scan := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
			recv(t, g.entered, "scan")
			walk := enqueue(t, r, Spec{Kind: kindWalk, SourceID: "a", ScopeKey: "node:2"})
			tick(t, r) // every due job has been considered for dispatch
			if got := mustGet(t, r, walk.ID); got.State != domain.JobQueued {
				t.Fatalf("walk is %s while the placeholder scan of its source runs", got.State)
			}
			g.release(scan.ID)
			if job := recv(t, g.entered, "walk"); job.ID != walk.ID {
				t.Fatalf("next job = %s, want the walk %s", job.ID, walk.ID)
			}
			g.release(walk.ID)
			for _, id := range []domain.JobID{scan.ID, walk.ID} {
				if got := waitJob(t, r, id, "succeeded", inState(domain.JobSucceeded)); got.DeviceKey != "source:a" {
					t.Errorf("job %s claimed under %q, want source:a", id, got.DeviceKey)
				}
			}
		})
	}
}

// Design D15: a runner without a registry still lets a cancel request show
// on a job blocked in a filesystem call past the watchdog; it flags nothing.
func TestBlockedFilesystemCallWithoutRegistry(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	entered := make(chan struct{})
	release := make(chan struct{})
	h := handlerFunc(func(ctx context.Context, _ Job, rt Runtime) error {
		done := rt.FSCall("readdir")
		close(entered)
		<-release // a filesystem call that does not observe cancellation
		done()
		return ctx.Err()
	})
	r := e.runner(t, map[Kind]Handler{KindScan: h}, func(o *Options) { o.Registry = nil })
	start(t, r)
	rec := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, entered, "blocked call")
	if _, err := r.Cancel(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(e.cfg.CallWatchdog.Duration + time.Second)
	tick(t, r)
	if got := mustGet(t, r, rec.ID); got.State != domain.JobRunning || got.DisplayState() != StateCancelRequested {
		t.Fatalf("blocked job shows %s/%s, want running/cancel_requested", got.State, got.DisplayState())
	}
	close(release)
	waitJob(t, r, rec.ID, "cancelled", inState(domain.JobCancelled))
	if calls := e.reg.unresponsiveCalls(); len(calls) != 0 {
		t.Fatalf("the env's registry, not given to the runner, got %+v", calls)
	}
}

// Design D14 keeps the meaning of workers_per_device: with two workers per
// device, two jobs of one observed source run concurrently, and a third job
// on that device waits for a free slot.
func TestTwoWorkersPerDevice(t *testing.T) {
	e := newEnv(t)
	e.cfg.WorkersPerDevice = 2
	for _, src := range []domain.SourceID{"a", "b"} {
		addSource(t, e.st, src)
		recordDevice(e.reg, src, 1)
	}
	g := newGate()
	r := e.runner(t, map[Kind]Handler{KindScan: g, kindWalk: g})
	start(t, r)

	scan := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	walk := enqueue(t, r, Spec{Kind: kindWalk, SourceID: "a", ScopeKey: "node:2"})
	got := []domain.JobID{recv(t, g.entered, "first job").ID, recv(t, g.entered, "second job").ID}
	slices.Sort(got)
	if !slices.Equal(got, []domain.JobID{scan.ID, walk.ID}) {
		t.Fatalf("running jobs = %v, want the scan %s and the walk %s at once", got, scan.ID, walk.ID)
	}
	third := enqueue(t, r, Spec{Kind: KindScan, SourceID: "b"})
	tick(t, r)
	if got := mustGet(t, r, third.ID); got.State != domain.JobQueued {
		t.Fatalf("third dev:1 job is %s while two run", got.State)
	}

	g.release(scan.ID)
	if job := recv(t, g.entered, "third job"); job.ID != third.ID {
		t.Fatalf("next job = %s, want %s", job.ID, third.ID)
	}
	for _, id := range []domain.JobID{walk.ID, third.ID} {
		if key := mustGet(t, r, id).DeviceKey; key != "dev:1" {
			t.Errorf("job %s claimed under %q, want dev:1", id, key)
		}
	}
}
