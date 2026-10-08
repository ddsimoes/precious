package relations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/store"
)

// Task 4.2: the relate job (design D5, D6, D10).

type fakeRT struct{ yields atomic.Int64 }

func (*fakeRT) Progress(map[string]int64)                        {}
func (*fakeRT) FSCall(string) func()                             { return func() {} }
func (f *fakeRT) Yield(ctx context.Context) error                { f.yields.Add(1); return ctx.Err() }
func (*fakeRT) UseSource(context.Context, domain.SourceID) error { return nil }

func configDuplicates() config.Duplicates { return config.Defaults().Duplicates }

func (w *world) handler(after func(ctx context.Context, gen int64) error) *Handler {
	w.seed()
	return NewHandler(w.st, nil, configDuplicates(), after)
}

func runJob(t testing.TB, h *Handler) {
	t.Helper()
	if err := h.Run(context.Background(), jobs.Job{Kind: KindRelate}, &fakeRT{}); err != nil {
		t.Fatal(err)
	}
}

func reviewState(t testing.TB, st *store.Store) (gen int64, dirty bool, computed sql.NullInt64) {
	t.Helper()
	if err := st.Reader().QueryRow(`SELECT gen, dirty, computed_at FROM review_state WHERE id = 1`).Scan(&gen, &dirty, &computed); err != nil {
		t.Fatal(err)
	}
	return gen, dirty, computed
}

func count(t testing.TB, st *store.Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := st.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestRelateJobShowsAGeneration(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("x/a.txt", 100, "a")
	w.file("x/sub/b.txt", 200, "b")
	w.file("y/renamed.txt", 100, "a")
	w.file("y/other/name.txt", 200, "b")
	w.archive(wf{path: "z/bkp.zip", size: 250}, mem("a.txt", 100, "a"), mem("b.txt", 200, "b"))
	var hooked []int64
	h := w.handler(func(ctx context.Context, gen int64) error {
		if g, _, _ := reviewState(t, w.st); g != gen-1 {
			t.Errorf("after hook with generation %d sees generation %d visible", gen, g)
		}
		hooked = append(hooked, gen)
		return nil
	})
	runJob(t, h)
	if gen, dirty, at := reviewState(t, w.st); gen != 1 || dirty || !at.Valid {
		t.Fatalf("review_state = %d %v %v, want generation 1, clean, computed", gen, dirty, at)
	}
	if !slices.Equal(hooked, []int64{1}) {
		t.Errorf("after hook calls %v", hooked)
	}
	rels := visible(t, w.st)
	// The archive is the same as x and as y; x and y, related through the
	// archive, are one group and get no line of their own.
	if len(rels) != 2 {
		t.Fatalf("relations %+v", rels)
	}
	got, err := RelationsOf(context.Background(), w.st.Reader(), w.ref("z/bkp.zip"), 20)
	if err != nil || len(got) != 2 {
		t.Fatalf("RelationsOf(zip) = %+v, %v", got, err)
	}
	for _, r := range got {
		if r.Kind != KindSame || r.A != w.ref("z/bkp.zip") || r.RedundantBytes != 250 || r.MatchedBytes != 300 {
			t.Errorf("zip relation %+v", r)
		}
	}
	if got, _ := RelationsOf(context.Background(), w.st.Reader(), w.ref("x/sub"), 20); len(got) != 0 {
		t.Errorf("x/sub has relations %+v", got)
	}

	// A second run replaces the generation.
	runJob(t, h)
	if gen, _, _ := reviewState(t, w.st); gen != 2 {
		t.Fatalf("generation %d after two runs", gen)
	}
	if n := count(t, w.st, `SELECT count(*) FROM relations WHERE gen <> 2`); n != 0 {
		t.Errorf("%d rows of an old generation remain", n)
	}
	if n := len(visible(t, w.st)); n != 2 {
		t.Errorf("%d relations in generation 2", n)
	}
}

// Member-folder refs: RelationsOf by "m<id>" and the stored a_member.
func TestRelationsOfAMemberFolder(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("fotos/a.jpg", 100, "a")
	w.file("fotos/b.jpg", 200, "b")
	w.file("fotos/c.jpg", 50, "")
	w.archive(wf{path: "bkp.zip", size: 700},
		mem("home/fotos/a.jpg", 100, "a"), mem("home/fotos/b.jpg", 200, "b"), mem("home/docs/x.doc", 500, ""))
	runJob(t, w.handler(nil))
	ref := w.ref("bkp.zip!home/fotos")
	got, err := RelationsOf(context.Background(), w.st.Reader(), domain.Ref{Member: ref.Member}, 20)
	if err != nil || len(got) != 1 || got[0].A != ref || got[0].B != w.ref("fotos") || got[0].Kind != KindInside {
		t.Fatalf("RelationsOf(%s) = %+v, %v", ref, got, err)
	}
	if got, _ := RelationsOf(context.Background(), w.st.Reader(), w.ref("bkp.zip"), 20); len(got) != 0 {
		t.Errorf("the whole archive has relations %+v", got)
	}
}

// Readers never see two generations: while generation 2 is written, the
// visible relations are generation 1's, whole; after the flip, generation
// 2's; a concurrent reader only ever sees one of the two sets.
func TestReadersNeverSeeTwoGenerations(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	for i := range 30 {
		w.file(fmt.Sprintf("p%02d/a.txt", i), int64(100+i), fmt.Sprint("a", i))
		w.file(fmt.Sprintf("q%02d/a.txt", i), int64(100+i), fmt.Sprint("a", i))
	}
	h := w.handler(nil)
	runJob(t, h)
	old := visible(t, w.st)
	if len(old) != 30 {
		t.Fatalf("generation 1 has %d relations", len(old))
	}
	// Generation 2 relates one more pair.
	if _, err := w.st.Writer().Exec(`UPDATE file_content SET state = 'unreadable', content_id = NULL
		WHERE entry_id = ?`, int64(w.id("q00/a.txt"))); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var seen sync.Map // relation count -> true
	var wg sync.WaitGroup
	wg.Go(func() {
		for !stop.Load() {
			n := int64(len(visible(t, w.st)))
			seen.Store(n, true)
		}
	})
	h.stage = func(ctx context.Context, name string, gen int64) {
		switch name {
		case "written":
			if n := count(t, w.st, `SELECT count(*) FROM relations WHERE gen = 2`); n != 29 {
				t.Errorf("generation 2 has %d rows before the flip", n)
			}
			if got := visible(t, w.st); !slices.Equal(got, old) {
				t.Errorf("before the flip, visible relations changed: %d", len(got))
			}
		case "flipped":
			if n := len(visible(t, w.st)); n != 29 {
				t.Errorf("after the flip, %d visible relations", n)
			}
		}
	}
	runJob(t, h)
	stop.Store(true)
	wg.Wait()
	seen.Range(func(k, _ any) bool {
		if n := k.(int64); n != 30 && n != 29 {
			t.Errorf("a reader saw %d relations: two generations, or neither", n)
		}
		return true
	})
}

// refreshDuringFirstPass runs the relate job through a real runner, from
// Startup on a new (dirty) database, and requests a refresh at the given
// stage of the first pass. It waits until every job succeeded and returns
// the store, the number of passes, and a runner-bound Startup.
func refreshDuringFirstPass(t *testing.T, stage string) (st *store.Store, passes int64, startup func()) {
	t.Helper()
	w := newWorld(t)
	w.file("x/a.txt", 100, "a")
	w.file("y/a.txt", 100, "a")
	h := w.handler(nil)
	r, err := jobs.NewRunner(jobs.Options{Store: w.st, Logger: slog.New(slog.DiscardHandler), TickInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h.Register(r)
	var begun, refreshed atomic.Int64
	h.stage = func(ctx context.Context, name string, gen int64) {
		if name == "begin" {
			begun.Add(1)
		}
		if name != stage || refreshed.Add(1) != 1 {
			return
		}
		if err := r.Write(ctx, RequestRefresh); err != nil {
			t.Error(err)
		}
	}
	ctx := context.Background()
	startup = func() {
		t.Helper()
		if err := h.Startup(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// Startup does nothing until the relations are dirty (new databases are).
	startup()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	waitJobs(t, w.st)
	return w.st, begun.Load(), startup
}

// A refresh requested during a run, before the flip reads dirty, makes the
// running job run another pass. The follow-up job the refresh also enqueued
// (the "relate" job was running) finds the relations clean and runs none.
func TestRefreshDuringARunRunsAgain(t *testing.T) {
	t.Parallel()
	st, passes, startup := refreshDuringFirstPass(t, "loaded")
	if passes != 2 {
		t.Errorf("%d passes, want 2 (the follow-up runs none)", passes)
	}
	if gen, dirty, _ := reviewState(t, st); gen != 2 || dirty {
		t.Errorf("review_state %d %v, want generation 2, clean", gen, dirty)
	}
	if n := count(t, st, `SELECT count(*) FROM jobs WHERE kind = 'relate' AND scope_key = 'relate:next'`); n != 1 {
		t.Errorf("%d follow-up jobs, want 1", n)
	}

	// Clean: Startup enqueues nothing.
	jobsBefore := count(t, st, `SELECT count(*) FROM jobs WHERE kind = 'relate'`)
	startup()
	if n := count(t, st, `SELECT count(*) FROM jobs WHERE kind = 'relate'`); n != jobsBefore {
		t.Errorf("Startup enqueued while clean")
	}
}

// A refresh requested after the flip read dirty (while the job prunes or
// ends) is not lost: the running job ends without another pass, and the
// follow-up job the refresh enqueued runs one that shows a later
// generation (design Addendum G1).
func TestRefreshAfterTheFlipRunsAgain(t *testing.T) {
	t.Parallel()
	st, passes, _ := refreshDuringFirstPass(t, "flipped")
	if passes != 2 {
		t.Errorf("%d passes, want 2", passes)
	}
	if gen, dirty, _ := reviewState(t, st); gen != 2 || dirty {
		t.Errorf("review_state %d %v, want generation 2, clean", gen, dirty)
	}
}

// A job enqueued with if_dirty runs no pass on its first attempt while the
// relations are clean; a retry, a dirty state, or a job without the flag
// runs one.
func TestIfDirtyJobRunsOnlyWhenDirty(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("x/a.txt", 100, "a")
	w.file("y/a.txt", 100, "a")
	h := w.handler(nil)
	run := func(attempt int, payload json.RawMessage) int64 {
		t.Helper()
		if err := h.Run(context.Background(), jobs.Job{Kind: KindRelate, Attempt: attempt, Payload: payload}, &fakeRT{}); err != nil {
			t.Fatal(err)
		}
		gen, dirty, _ := reviewState(t, w.st)
		if dirty {
			t.Fatalf("dirty after a run")
		}
		return gen
	}
	if gen := run(1, ifDirtyPayload); gen != 1 {
		t.Fatalf("generation %d, want 1 (new databases are dirty)", gen)
	}
	if gen := run(1, ifDirtyPayload); gen != 1 {
		t.Errorf("generation %d, want 1: a clean if_dirty job ran a pass", gen)
	}
	if gen := run(2, ifDirtyPayload); gen != 2 {
		t.Errorf("generation %d, want 2: a retry runs a pass", gen)
	}
	if gen := run(1, json.RawMessage(`{}`)); gen != 3 {
		t.Errorf("generation %d, want 3: a job without if_dirty runs a pass", gen)
	}
	if _, err := w.st.Writer().Exec(`UPDATE review_state SET dirty = 1`); err != nil {
		t.Fatal(err)
	}
	if gen := run(1, ifDirtyPayload); gen != 4 {
		t.Errorf("generation %d, want 4: a dirty if_dirty job runs a pass", gen)
	}
}

// waitJobs waits until every job is terminal and succeeded.
func waitJobs(t testing.TB, st *store.Store) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if count(t, st, `SELECT count(*) FROM jobs WHERE state NOT IN ('succeeded','failed','cancelled')`) == 0 {
			if n := count(t, st, `SELECT count(*) FROM jobs WHERE state <> 'succeeded'`); n != 0 {
				t.Fatalf("%d jobs did not succeed", n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("jobs did not finish")
}

// A source removed mid-run leaves no orphan rows: at each stage, the job
// succeeds and nothing refers to the removed source's entries.
func TestSourceRemovedMidRunLeavesNoOrphans(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"loaded", "written", "flipped"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.file("docs/a.txt", 100, "a")
			w.file("docs/b.txt", 200, "b")
			w.file("music/x.mp3", 5000, "")
			w.file("usb:backup/docs/a.txt", 100, "a")
			w.file("usb:backup/docs/b.txt", 200, "b")
			w.file("usb:other/y.bin", 7000, "")
			w.archive(wf{path: "usb:docs.zip", size: 250}, mem("a.txt", 100, "a"), mem("b.txt", 200, "b"))
			var after []int64
			h := w.handler(func(ctx context.Context, gen int64) error {
				after = append(after, count(t, w.st, `SELECT count(*) FROM relations WHERE gen = ?`, gen))
				return nil
			})
			h.stage = func(ctx context.Context, name string, gen int64) {
				if name != stage {
					return
				}
				if _, err := w.st.Writer().Exec(`DELETE FROM sources WHERE id = 'usb'`); err != nil {
					t.Fatal(err)
				}
			}
			runJob(t, h)
			for _, q := range []string{
				`SELECT count(*) FROM relations r WHERE NOT EXISTS (SELECT 1 FROM entries e WHERE e.id = r.a_entry)
					OR NOT EXISTS (SELECT 1 FROM entries e WHERE e.id = r.b_entry)`,
				`SELECT count(*) FROM relations r WHERE r.a_entry IN (SELECT id FROM entries WHERE source_id = 'usb')
					OR r.b_entry IN (SELECT id FROM entries WHERE source_id = 'usb')`,
				`SELECT count(*) FROM dir_dups d WHERE NOT EXISTS (SELECT 1 FROM entries e WHERE e.id = d.entry_id)`,
				`SELECT count(*) FROM archive_members`,
				`SELECT count(*) FROM relations WHERE gen <> (SELECT gen FROM review_state)`,
			} {
				if n := count(t, w.st, q); n != 0 {
					t.Errorf("%d orphan rows: %s", n, q)
				}
			}
			if stage == "loaded" && (len(after) != 1 || after[0] != 0) {
				t.Errorf("relations written for a removed source: %v", after)
			}
			if n := count(t, w.st, `SELECT count(*) FROM dir_dups`); n != 3 { // disk's root, docs, music
				t.Errorf("%d dir_dups rows, want disk's 3", n)
			}
			// The next run sees one source.
			runJob(t, h)
			if n := len(visible(t, w.st)); n != 0 {
				t.Errorf("%d relations without usb", n)
			}
		})
	}
}

// A failing after hook fails the pass: the generation is never shown, the
// relations are dirty again, and the next run replaces its rows.
func TestFailingAfterHookShowsNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("x/a.txt", 100, "a")
	w.file("y/a.txt", 100, "a")
	fail := errors.New("review failed")
	h := w.handler(func(context.Context, int64) error { return fail })
	if err := h.Run(context.Background(), jobs.Job{Kind: KindRelate}, &fakeRT{}); !errors.Is(err, fail) {
		t.Fatalf("Run = %v, want the hook's error", err)
	}
	if gen, dirty, _ := reviewState(t, w.st); gen != 0 || !dirty {
		t.Fatalf("review_state %d %v after a failed pass, want generation 0, dirty", gen, dirty)
	}
	h.after = nil
	runJob(t, h)
	if gen, _, _ := reviewState(t, w.st); gen != 1 {
		t.Fatalf("generation %d after a retry", gen)
	}
	if n := count(t, w.st, `SELECT count(*) FROM relations`); n != 1 {
		t.Errorf("%d relation rows, want the one of generation 1", n)
	}
}

// Contents no file, member, or review row references are pruned; others
// stay.
func TestUnreferencedContentsArePruned(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.file("x/a.txt", 100, "a")
	w.file("y/a.txt", 100, "a")
	w.archive(wf{path: "z.zip", size: 90}, mem("m.txt", 300, "m"))
	w.seed()
	for i := range 7 {
		if _, err := w.st.Writer().Exec(`INSERT INTO contents (sha256, size) VALUES (?, ?)`, digest(fmt.Sprint("orphan", i)), 10+i); err != nil {
			t.Fatal(err)
		}
	}
	before := count(t, w.st, `SELECT count(*) FROM contents`)
	runJob(t, w.handler(nil))
	if n := count(t, w.st, `SELECT count(*) FROM contents`); n != before-7 || n != 2 {
		t.Errorf("%d contents after pruning, want 2 (a, m)", n)
	}
}
