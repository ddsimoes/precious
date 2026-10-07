package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/store/storetest"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// day0 is Thursday 2026-10-01; times below are UTC unless a zone is named.
func at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, 1+day, hour, minute, 0, 0, time.UTC)
}

var daily3 = domain.Schedule{Every: domain.EveryDay, At: "03:00", Zone: "UTC"}

type env struct {
	t      *testing.T
	st     *store.Store
	fs     *synthfs.FS
	src    *sources.Service
	runner *jobs.Runner
	sched  *Scheduler
	devs   map[domain.SourceID]uint64
}

// newEnv is a store, a synthfs, the registry, a job runner that is not
// started (so scans stay queued), and the scheduler.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, st: storetest.Open(t), fs: synthfs.New(), devs: map[domain.SourceID]uint64{}}
	var err error
	if e.src, err = sources.New(e.st, e.fs, config.Sources{AllowedRoots: []string{t.TempDir()}}, fixedClock{at(0, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if e.runner, err = jobs.NewRunner(jobs.Options{Store: e.st, Registry: e.src, Config: config.Defaults().Jobs,
		Logger: logger, TickInterval: 10 * time.Millisecond, ProgressInterval: 10 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	e.sched = New(e.st, e.runner, e.src, nil, logger)
	return e
}

// disk adds an online source on its own synthfs device mounted at
// /mnt/<id>, with one file, root entry included, as add-source does, and
// returns its root folder.
func (e *env) disk(id domain.SourceID) *synthfs.Node {
	e.t.Helper()
	point := "/mnt/" + string(id)
	root := e.fs.Root(point)
	root.Dir("2004").File("a.jpg", 10, at(-1000, 0, 0))
	dev := root.Info().Dev
	e.devs[id] = dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4", DeviceKey: "dev:" + string(id), Strong: true}
	caps := fsaccess.Capabilities{Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true,
		TimeResolution: time.Nanosecond}
	e.fs.SetVolume(dev, vol)
	e.fs.SetCapabilities(dev, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key,
		capabilities, state, mount_point, created_at) VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0)`,
		string(id), string(id), vol.ID, vol.DeviceKey, string(capsJSON), []byte(point))
	e.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(id))
	return root
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.Writer().Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// schedule sets id's schedule at the time set through the registry, as
// set-source-schedule does; nil turns it off.
func (e *env) schedule(id domain.SourceID, sch *domain.Schedule, set time.Time) {
	e.t.Helper()
	svc, err := sources.New(e.st, e.fs, config.Sources{AllowedRoots: []string{e.t.TempDir()}}, fixedClock{set})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return svc.SetSchedule(context.Background(), tx, id, sch)
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) runDue(now time.Time) {
	e.t.Helper()
	if err := e.sched.RunDue(context.Background(), now); err != nil {
		e.t.Fatalf("RunDue(%s): %v", now, err)
	}
}

// scans is the number of scan jobs of id, finished ones included.
func (e *env) scans(id domain.SourceID) int {
	e.t.Helper()
	var n int
	if err := e.st.Reader().QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'scan' AND source_id = ?`, string(id)).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// finishScans ends id's active scans as succeeded.
func (e *env) finishScans(id domain.SourceID) {
	e.t.Helper()
	e.exec(`UPDATE jobs SET state = 'succeeded' WHERE source_id = ? AND state IN ('queued', 'running', 'paused')`, string(id))
}

func (e *env) get(id domain.SourceID) sources.Source {
	e.t.Helper()
	src, err := e.src.Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return src
}

// wantNext checks id's next scan, and that it is after now.
func (e *env) wantNext(id domain.SourceID, now, want time.Time) {
	e.t.Helper()
	src := e.get(id)
	if src.NextScanAt == nil || !src.NextScanAt.Equal(want) {
		e.t.Fatalf("%s next scan = %v, want %s", id, src.NextScanAt, want)
	}
	if !src.NextScanAt.After(now) {
		e.t.Fatalf("%s next scan %s is not after now %s", id, src.NextScanAt, now)
	}
}

func (e *env) wantNoSkip(id domain.SourceID) {
	e.t.Helper()
	if src := e.get(id); src.SkippedAt != nil || src.SkipReason != "" {
		e.t.Fatalf("%s skip = %v %q, want none", id, src.SkippedAt, src.SkipReason)
	}
}

// Scenario "A daily scan runs at its time": nothing starts before 03:00; the
// first look after it starts a scan, which runs as any scan does, its
// after-scan hook (hashing and relations) included, and the next scan is
// the next day at 03:00.
func TestDailyScanRunsAtItsTime(t *testing.T) {
	e := newEnv(t)
	e.disk("fotos")
	var mu sync.Mutex
	var done []domain.SourceID
	h := index.NewHandler(e.st, e.src, rules.Default(), nil, config.Scan{})
	h.OnScanDone(func(_ context.Context, src domain.SourceID) {
		mu.Lock()
		defer mu.Unlock()
		done = append(done, src)
	})
	h.Register(e.runner)
	e.schedule("fotos", &daily3, at(0, 1, 0))
	e.wantNext("fotos", at(0, 1, 0), at(0, 3, 0))

	e.runDue(at(0, 2, 59))
	if n := e.scans("fotos"); n != 0 {
		t.Fatalf("%d scans before 03:00", n)
	}
	now := at(0, 3, 0).Add(30 * time.Second)
	e.runDue(now)
	if n := e.scans("fotos"); n != 1 {
		t.Fatalf("%d scans after 03:00, want 1", n)
	}
	e.wantNext("fotos", now, at(1, 3, 0))
	e.wantNoSkip("fotos")

	if err := e.runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.runner.Stop(context.Background())
	deadline := time.Now().Add(20 * time.Second)
	for {
		var state string
		if err := e.st.Reader().QueryRow(`SELECT state FROM jobs WHERE kind = 'scan' AND source_id = 'fotos'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if domain.JobState(state).Terminal() {
			if state != string(domain.JobSucceeded) {
				t.Fatalf("scheduled scan ended %s", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled scan still %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(done) != 1 || done[0] != "fotos" {
		t.Fatalf("after-scan hook ran for %v, want [fotos]", done)
	}
}

// Scenario "Offline at the due time": a disk unplugged since the last
// availability refresh starts no scan; the source records that its 03:00
// scan was skipped because it was offline, and its next scan is the next
// day at 03:00. The next due time with the disk back runs and clears the
// skip.
func TestOfflineAtDueTimeRecordsSkip(t *testing.T) {
	e := newEnv(t)
	e.disk("fotos")
	e.schedule("fotos", &daily3, at(0, 1, 0))
	e.fs.Unmount(e.devs["fotos"])

	now := at(0, 3, 0).Add(20 * time.Second)
	e.runDue(now)
	if n := e.scans("fotos"); n != 0 {
		t.Fatalf("%d scans of an offline source", n)
	}
	src := e.get("fotos")
	if src.State != sources.StateOffline || src.SkippedAt == nil || !src.SkippedAt.Equal(at(0, 3, 0)) ||
		src.SkipReason != string(sources.StateOffline) {
		t.Fatalf("after the offline due time: state %s, skip %v %q", src.State, src.SkippedAt, src.SkipReason)
	}
	e.wantNext("fotos", now, at(1, 3, 0))
	e.runDue(now.Add(time.Minute))
	if src := e.get("fotos"); !src.SkippedAt.Equal(at(0, 3, 0)) {
		t.Fatalf("skip moved to %s", src.SkippedAt)
	}

	e.fs.Mount(e.devs["fotos"], "/mnt/fotos")
	now = at(1, 3, 0).Add(20 * time.Second)
	e.runDue(now)
	if n := e.scans("fotos"); n != 1 {
		t.Fatalf("%d scans with the disk back, want 1", n)
	}
	e.wantNoSkip("fotos")
	e.wantNext("fotos", now, at(2, 3, 0))
}

// An unavailable source (its disk mounted, its root folder unreadable) is
// skipped with that state as the reason.
func TestUnavailableAtDueTimeRecordsSkip(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos")
	e.schedule("fotos", &daily3, at(0, 1, 0))
	root.Unreadable()
	now := at(0, 3, 0).Add(20 * time.Second)
	e.runDue(now)
	if n := e.scans("fotos"); n != 0 {
		t.Fatalf("%d scans of an unavailable source", n)
	}
	if src := e.get("fotos"); src.State != sources.StateUnavailable || src.SkippedAt == nil ||
		!src.SkippedAt.Equal(at(0, 3, 0)) || src.SkipReason != string(sources.StateUnavailable) {
		t.Fatalf("after the unavailable due time: state %s, skip %v %q", src.State, src.SkippedAt, src.SkipReason)
	}
	e.wantNext("fotos", now, at(1, 3, 0))
}

// Scenario "The server was down": stopped from 02:00 to 05:00 on two
// consecutive days, each start runs one scan, and the next scan is the next
// day at 03:00; looking again at once starts nothing. Several missed days
// also give one scan.
func TestServerDownCatchesUpOnce(t *testing.T) {
	e := newEnv(t)
	e.disk("fotos")
	e.schedule("fotos", &daily3, at(0, 1, 0))

	for day := 0; day < 2; day++ {
		now := at(day, 5, 0)
		e.runDue(now)
		e.runDue(now.Add(time.Second))
		e.runDue(now.Add(time.Minute))
		if n := e.scans("fotos"); n != day+1 {
			t.Fatalf("day %d: %d scans, want %d", day, n, day+1)
		}
		e.wantNext("fotos", now, at(day+1, 3, 0))
		e.finishScans("fotos")
	}

	// Down from day 2 at 02:00 to day 5 at 05:00: three due times, one scan.
	now := at(5, 5, 0)
	e.runDue(now)
	if n := e.scans("fotos"); n != 3 {
		t.Fatalf("%d scans after three missed days, want 3", n)
	}
	e.wantNext("fotos", now, at(6, 3, 0))
}

// Scenario "A due scan joins a running scan": the owner's scan of the source
// is queued, running, or paused when the schedule comes due; no second scan
// starts, and the due time is still consumed.
func TestDueScanJoinsActiveScan(t *testing.T) {
	for _, state := range []string{"queued", "running", "paused"} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			e.disk("fotos")
			e.schedule("fotos", &daily3, at(0, 1, 0))
			if err := e.runner.Write(context.Background(), func(tx *jobs.Tx) error {
				_, err := index.StartScan(context.Background(), tx, "fotos")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE jobs SET state = ? WHERE kind = 'scan'`, state)
			now := at(0, 3, 1)
			e.runDue(now)
			if n := e.scans("fotos"); n != 1 {
				t.Fatalf("%d scans, want the owner's only", n)
			}
			e.wantNext("fotos", now, at(1, 3, 0))
			e.wantNoSkip("fotos")
		})
	}
}

// A source without a schedule, and one whose schedule was turned off, never
// scan by schedule and record no skip.
func TestOffNeverRuns(t *testing.T) {
	e := newEnv(t)
	e.disk("never")
	e.disk("was")
	e.schedule("was", &daily3, at(0, 1, 0))
	e.schedule("was", nil, at(0, 2, 0))
	for day := 0; day < 3; day++ {
		e.runDue(at(day, 3, 1))
	}
	for _, id := range []domain.SourceID{"never", "was"} {
		if n := e.scans(id); n != 0 {
			t.Errorf("%s: %d scans", id, n)
		}
		if src := e.get(id); src.Schedule != nil || src.NextScanAt != nil {
			t.Errorf("%s: schedule %+v next %v", id, src.Schedule, src.NextScanAt)
		}
		e.wantNoSkip(id)
	}
}

// The next scan always advances past now: a weekly schedule in its zone
// moves to the next Sunday 03:00 in São Paulo, whatever the time of the
// look.
func TestNextScanAdvancesPastNow(t *testing.T) {
	e := newEnv(t)
	e.disk("fotos")
	sunday := time.Sunday
	weekly := domain.Schedule{Every: domain.EveryWeek, At: "03:00", Weekday: &sunday, Zone: "America/Sao_Paulo"}
	e.schedule("fotos", &weekly, at(0, 12, 0))
	// Sunday 2026-10-04 03:00 in São Paulo (UTC-3) is 06:00 UTC.
	e.wantNext("fotos", at(0, 12, 0), at(3, 6, 0))
	now := at(3, 6, 0)
	e.runDue(now)
	e.wantNext("fotos", now, at(10, 6, 0))
	e.finishScans("fotos")
	now = at(12, 0, 0)
	e.runDue(now)
	e.wantNext("fotos", now, at(17, 6, 0))
	if n := e.scans("fotos"); n != 2 {
		t.Fatalf("%d scans, want 2", n)
	}
}
