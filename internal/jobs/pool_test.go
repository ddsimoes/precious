package jobs

import (
	"context"
	"testing"

	"precious/internal/domain"
)

const kindClassify Kind = "classify"

// Spec scenario "Slow provider, running scan" (design D11), with one worker
// per device: while pool jobs of a source block on their provider, a scan of
// the same source takes the device's only slot and completes, and no more
// pool jobs run at once than the pool's capacity.
func TestSlowProviderRunningScan(t *testing.T) {
	e := newEnv(t)
	e.cfg.WorkersPerDevice = 1
	addSource(t, e.st, "s")
	recordDevice(e.reg, "s", 7)
	provider, fs := newGate(), newGate()
	r := e.runner(t, map[Kind]Handler{KindScan: fs})
	r.RegisterPool(kindClassify, provider, "classifier", 2)
	start(t, r)

	var pool [3]domain.JobID
	for i := range pool {
		pool[i] = enqueue(t, r, Spec{Kind: kindClassify, SourceID: "s"}).ID
	}
	for i := range 2 {
		if job := recv(t, provider.entered, "pool job"); job.ID != pool[i] {
			t.Fatalf("pool job %d running = %s, want %s", i, job.ID, pool[i])
		}
		if key := mustGet(t, r, pool[i]).DeviceKey; key != "workers:classifier" {
			t.Fatalf("pool job claimed under %q, want workers:classifier", key)
		}
	}

	scan := enqueue(t, r, Spec{Kind: KindScan, SourceID: "s"})
	if job := recv(t, fs.entered, "scan"); job.ID != scan.ID {
		t.Fatalf("next filesystem job = %s, want the scan %s", job.ID, scan.ID)
	}
	if key := mustGet(t, r, scan.ID).DeviceKey; key != "dev:7" {
		t.Fatalf("scan claimed under %q, want dev:7", key)
	}
	fs.release(scan.ID)
	waitJob(t, r, scan.ID, "succeeded", inState(domain.JobSucceeded))
	tick(t, r) // every due job has been considered for dispatch
	for i, want := range []domain.JobState{domain.JobRunning, domain.JobRunning, domain.JobQueued} {
		if got := mustGet(t, r, pool[i]).State; got != want {
			t.Fatalf("after the scan, pool job %d is %s, want %s", i, got, want)
		}
	}

	provider.release(pool[0])
	if job := recv(t, provider.entered, "third pool job"); job.ID != pool[2] {
		t.Fatalf("pool job running after a release = %s, want %s", job.ID, pool[2])
	}
	for _, id := range pool[1:] {
		provider.release(id)
		waitJob(t, r, id, "succeeded", inState(domain.JobSucceeded))
	}
}

// Design D11: pool jobs never take part in placeholder exclusivity. Before
// the source's device is recorded, its scan is claimed under the placeholder
// key although a pool job of the source runs, and while that scan holds the
// source exclusively, another pool job of the source is claimed.
func TestPoolJobsIgnorePlaceholderExclusivity(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "s")
	provider, fs := newGate(), newGate()
	r := e.runner(t, map[Kind]Handler{KindScan: fs})
	r.RegisterPool(kindClassify, provider, "classifier", 2)
	start(t, r)

	first := enqueue(t, r, Spec{Kind: kindClassify, SourceID: "s"})
	recv(t, provider.entered, "first pool job")
	scan := enqueue(t, r, Spec{Kind: KindScan, SourceID: "s"})
	recv(t, fs.entered, "placeholder scan")
	if key := mustGet(t, r, scan.ID).DeviceKey; key != "source:s" {
		t.Fatalf("scan claimed under %q, want the placeholder source:s", key)
	}
	second := enqueue(t, r, Spec{Kind: kindClassify, SourceID: "s"})
	if job := recv(t, provider.entered, "second pool job"); job.ID != second.ID {
		t.Fatalf("pool job running = %s, want %s", job.ID, second.ID)
	}
	for _, id := range []domain.JobID{scan.ID, first.ID, second.ID} {
		if got := mustGet(t, r, id).State; got != domain.JobRunning {
			t.Fatalf("job %s is %s, want all three running", id, got)
		}
	}
	fs.release(scan.ID)
	provider.release(first.ID)
	provider.release(second.ID)
	for _, id := range []domain.JobID{scan.ID, first.ID, second.ID} {
		waitJob(t, r, id, "succeeded", inState(domain.JobSucceeded))
	}
}

// RegisterPool panics, like Register, on a kind registered twice or a call
// after Start, and on a capacity below 1 or a second capacity for one pool.
func TestRegisterPoolPanics(t *testing.T) {
	e := newEnv(t)
	h := handlerFunc(func(context.Context, Job, Runtime) error { return nil })
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Runner) // must not panic
		call  func(*Runner)             // must panic
	}{
		{"duplicate pool kind",
			func(_ *testing.T, r *Runner) { r.RegisterPool("a", h, "p", 1) },
			func(r *Runner) { r.RegisterPool("a", h, "p", 1) }},
		{"kind already registered by Register",
			func(_ *testing.T, r *Runner) { r.Register("a", h) },
			func(r *Runner) { r.RegisterPool("a", h, "p", 1) }},
		{"pool kind registered by Register",
			func(_ *testing.T, r *Runner) { r.RegisterPool("a", h, "p", 1) },
			func(r *Runner) { r.Register("a", h) }},
		{"capacity 0",
			func(*testing.T, *Runner) {},
			func(r *Runner) { r.RegisterPool("a", h, "p", 0) }},
		{"second capacity for one pool",
			func(_ *testing.T, r *Runner) { r.RegisterPool("a", h, "p", 2) },
			func(r *Runner) { r.RegisterPool("b", h, "p", 3) }},
		{"after Start",
			func(t *testing.T, r *Runner) { start(t, r) },
			func(r *Runner) { r.RegisterPool("a", h, "p", 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.runner(t, nil)
			tc.setup(t, r)
			defer func() {
				if recover() == nil {
					t.Fatal("registration did not panic")
				}
			}()
			tc.call(r)
		})
	}
}
