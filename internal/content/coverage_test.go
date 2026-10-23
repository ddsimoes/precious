package content

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
)

// storedCoverage returns the rows of content_coverage with any figure set.
func (e *env) storedCoverage() map[string][8]int64 {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT source_id, candidate_files, candidate_bytes, checked_files, checked_bytes,
		unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes FROM content_coverage`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][8]int64{}
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
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return got
}

// exec runs one statement through the store's writer.
func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

// coverageWorld is a source fotos with two pending files of one size and a
// unique_size file, planned and with exact coverage.
func coverageWorld(t *testing.T) *env {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	root.File("a.jpg", 7000, fileTime)
	root.Dir("b").File("a.jpg", 7000, fileTime)
	root.File("solo.jpg", 123, fileTime)
	e.scan("fotos")
	e.plan()
	if c := e.coverage("fotos"); c.CandidateFiles != 2 || c.UncheckedFiles != 2 {
		t.Fatalf("coverage %+v; want two candidates not checked", c)
	}
	return e
}

// Scenario "Signing in while coverage is recomputed": while the plan's
// coverage aggregate is open, a session write commits without waiting.
func TestSigningInWhileCoverageIsRecomputed(t *testing.T) {
	e := coverageWorld(t)
	var (
		held    bool
		took    time.Duration
		signErr error
	)
	e.svc.coverageRead = func() {
		held = true
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		signErr = e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO sessions (token_hash, kind, csrf_token, created_at,
				last_seen_at, idle_expires_at, absolute_expires_at) VALUES (randomblob(32), 'pre_login', 'c', 1, 1, 2, 3)`)
			return err
		})
		took = time.Since(start)
	}
	e.plan()
	if !held {
		t.Fatal("the plan never held the coverage aggregate open")
	}
	if signErr != nil || took >= time.Second {
		t.Fatalf("the sign-in write took %v: %v; want a commit within 1 s", took, signErr)
	}
	if n := e.count(`SELECT count(*) FROM sessions`); n != 1 {
		t.Errorf("%d sessions, want the one committed", n)
	}
}

// Scenario "A hashing commit during the recomputation is kept": a file
// checked after the aggregate was read and before its result is written
// counts as checked, and coverage equals a full recomputation.
func TestHashingCommitDuringRecomputationIsKept(t *testing.T) {
	e := coverageWorld(t)
	id := e.id("fotos", "a.jpg")
	e.svc.coverageRead = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // fails rather than hangs on a held writer
		defer cancel()
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO contents (sha256, size) VALUES (randomblob(32), 7000)`)
			if err != nil {
				return err
			}
			cid, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE file_content SET state = 'hashed', content_id = ?, checked_at = 1
				WHERE entry_id = ? AND state = 'pending'`, cid, int64(id)); err != nil {
				return err
			}
			d := deltas{}
			d.move("fotos", domain.ContentPending, domain.ContentHashed, 7000)
			return d.apply(context.Background(), tx, 1)
		})
		if err != nil {
			t.Errorf("hashing commit: %v", err)
		}
	}
	e.plan() // checks the result against a direct GROUP BY of the rows
	c := e.coverage("fotos")
	if c.CheckedFiles != 1 || c.CheckedBytes != 7000 || c.UncheckedFiles != 1 || c.CandidateFiles != 2 {
		t.Errorf("coverage %+v; want the file hashed meanwhile checked", c)
	}
	got := e.storedCoverage()
	indextest.RecomputeCoverage(t, e.st)
	if want := e.storedCoverage(); fmt.Sprint(sortedCov(got)) != fmt.Sprint(sortedCov(want)) {
		t.Errorf("coverage %v; a full recomputation gives %v", sortedCov(got), sortedCov(want))
	}
}

// Stored figures that drifted, too high, too low, or missing, are restored
// exactly by the next plan; a source with no rows of its own ends at zero,
// and a source with no rows and no stored row stays at zero.
func TestPlanRepairsCoverageDrift(t *testing.T) {
	e := coverageWorld(t)
	usb := e.disk("usb", "/mnt/usb", posix)
	usb.File("x.mov", 7000, fileTime)
	usb.File("y.mov", 900, fileTime)
	e.disk("empty", "/mnt/empty", posix)
	e.disk("blank", "/mnt/blank", posix)
	e.scan("usb")
	e.scan("empty")
	e.scan("blank")
	e.plan()
	want := e.storedCoverage()
	if len(want) != 2 {
		t.Fatalf("coverage %v; want fotos and usb", sortedCov(want))
	}
	e.exec(`UPDATE content_coverage SET candidate_files = candidate_files + 40, checked_bytes = 12345,
		unreadable_files = 3, unreadable_bytes = -9 WHERE source_id = 'fotos'`)
	e.exec(`DELETE FROM content_coverage WHERE source_id = 'usb'`) // a source new to coverage
	e.exec(`INSERT OR REPLACE INTO content_coverage VALUES ('empty', 5, 50, 1, 10, 4, 40, 0, 0, 0)`)
	e.plan()
	if got := e.storedCoverage(); fmt.Sprint(sortedCov(got)) != fmt.Sprint(sortedCov(want)) {
		t.Errorf("coverage %v after the plan; want %v", sortedCov(got), sortedCov(want))
	}
	if n := e.count(`SELECT count(*) FROM content_coverage WHERE source_id = 'usb'`); n != 1 {
		t.Errorf("%d coverage rows for usb, want the one the plan created", n)
	}
	for _, src := range []domain.SourceID{"empty", "blank"} {
		if c := e.coverage(src); c != (Coverage{}) {
			t.Errorf("%s: coverage %+v, want zero", src, c)
		}
	}
	if n := e.count(`SELECT count(*) FROM content_coverage WHERE source_id = 'usb' AND updated_at = ?`,
		testNow.UnixMilli()); n != 1 {
		t.Error("the corrected row of usb does not carry the plan's time")
	}
}

// A source removed after the aggregate was read is skipped: its drift
// resurrects no row and the plan ends without an error.
func TestCoverageSkipsSourceRemovedMeanwhile(t *testing.T) {
	e := coverageWorld(t)
	e.disk("gone", "/mnt/gone", posix).File("g.jpg", 7000, fileTime)
	e.scan("gone")
	e.plan()
	want := e.storedCoverage()
	delete(want, "gone")
	e.exec(`DELETE FROM content_coverage`) // every source drifts
	e.svc.coverageRead = func() {
		if err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
			return e.src.Remove(context.Background(), tx, "gone")
		}); err != nil {
			t.Errorf("remove gone: %v", err)
		}
	}
	e.plan()
	if n := e.count(`SELECT count(*) FROM content_coverage WHERE source_id = 'gone'`); n != 0 {
		t.Errorf("%d coverage rows for the removed source", n)
	}
	if got := e.storedCoverage(); fmt.Sprint(sortedCov(got)) != fmt.Sprint(sortedCov(want)) {
		t.Errorf("coverage %v; want %v", sortedCov(got), sortedCov(want))
	}
}

// The aggregate is the read snapshot's: a change without deltas committed
// after it, a file marked missing, stays out of the figures until the next
// plan repairs them (r5d design D1).
func TestCoverageDriftIsTheSnapshots(t *testing.T) {
	e := coverageWorld(t)
	id := e.id("fotos", "b/a.jpg")
	e.svc.coverageRead = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE entries SET state = 'missing' WHERE id = ?`, int64(id))
			return err
		}); err != nil {
			t.Errorf("mark b/a.jpg missing: %v", err)
		}
	}
	if _, err := e.svc.plan(context.Background(), &fakeRuntime{}, ""); err != nil {
		t.Fatal(err)
	}
	if c := e.coverage("fotos"); c.CandidateFiles != 2 || c.CandidateBytes != 14000 || c.UncheckedFiles != 2 ||
		c.UncheckedBytes != 14000 {
		t.Errorf("coverage %+v; want the snapshot's two unchecked candidates", c)
	}
	e.svc.coverageRead = nil
	e.plan() // checks the figures against a direct GROUP BY of the rows
	if c := e.coverage("fotos"); c.CandidateFiles == 2 {
		t.Errorf("coverage %+v; the next plan did not drop the missing file", c)
	}
}
