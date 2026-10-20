package dates

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/store"
)

// The media job (design D4). One job of kind KindMedia per source, ClassBulk:
//
//   - The start, one write transaction: while media_sources.passes_job names
//     another media job that is running, it defers 3 s (DeferMediaRunning);
//     a first attempt with if_dirty ends at once when dirty is clear; while
//     organizing holds the source (DeferWhile), it defers 3 s
//     (index.DeferOrganizing); then it takes passes_job.
//   - The loop: each iteration clears dirty, runs passes 1–4, and reads
//     dirty again in one write transaction, which releases passes_job when
//     dirty is clear; set, the loop runs again.
//   - Failure: on any error, a cancel included, one write transaction sets
//     dirty and releases passes_job, so no request is lost.
//
// The passes: 1 plan (plan.go), 2 read (read.go), 3 dates (deriveAll), 4
// cameras (cameras.go). A source that cannot be opened runs 3 and 4 only.

// DeferMediaRunning: another media job of the source runs its passes. A
// job waiting for organizing defers with index.DeferOrganizing, as a scan.
const DeferMediaRunning = "media_running"

// startDelay is how long a media job's start waits before it checks again.
const startDelay = 3 * time.Second

// Progress keys of the media job (design D4, Interfaces).
const (
	progPhase      = "phase"
	progFiles      = "files"
	progOfFiles    = "of_files"
	progBytes      = "bytes"
	progChanged    = "changed"
	progUnreadable = "unreadable"
	progMedia      = "media"
	progOfMedia    = "of_media"
)

// The phases a media job reports: its passes.
const (
	phasePlan int64 = iota + 1
	phaseRead
	phaseDates
	phaseCameras
)

// Test stages: jobWiring.hook is called at each, and an error it returns
// fails the job there.
const (
	stageStarted    = "started"     // passes_job taken, before the first loop
	stagePlanned    = "planned"     // after pass 1
	stageReadCommit = "read_commit" // in pass 2, before each commit of read results
	stageRead       = "read"        // after pass 2
	stageDerived    = "derived"     // after pass 3
	stageSnapshot   = "snapshot"    // in pass 4, between its snapshot and its write
	stageDetected   = "detected"    // after pass 4
	stageEnded      = "ended"       // after the end read released passes_job, before Run returns
)

// jobWiring is what Register, DeferWhile, and the tests give the media job.
type jobWiring struct {
	// active, when set (DeferWhile), tells the start to wait.
	active func(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error)
	// hook, when set (tests), runs at each stage.
	hook func(ctx context.Context, job jobs.Job, stage string) error
}

// mediaPayload is a media job's payload: {"if_dirty":true} from
// EnqueueMedia, or {}.
type mediaPayload struct {
	IfDirty bool `json:"if_dirty"`
}

// handler runs media jobs.
type handler struct{ s *Service }

var _ jobs.Handler = (*handler)(nil)

// Register installs the media job (ClassBulk). Call DeferWhile first.
func (s *Service) Register(r *jobs.Runner) {
	r.RegisterClass(KindMedia, &handler{s: s}, jobs.ClassBulk)
}

// DeferWhile sets active, which the media job's start calls inside its
// write transaction before it takes its source's passes: while active
// reports true (serve wires executor.OrganizeActive), the job returns a
// jobs.Defer for 3 s with reason index.DeferOrganizing (design D4). Call it
// before Register.
func (s *Service) DeferWhile(active func(ctx context.Context, q store.Queryer, src domain.SourceID) (bool, error)) {
	s.job.active = active
}

// AfterScan is the index's after-scan hook: it requests src's media job
// (D4). A failure is logged; the next scan or start requests it again.
func (s *Service) AfterScan(ctx context.Context, src domain.SourceID) {
	err := s.runner.Write(ctx, func(tx *jobs.Tx) error { return EnqueueMedia(ctx, tx, src) })
	if err != nil && ctx.Err() == nil {
		s.log.Error("dates: request the media job after a scan", "source", src, "err", err)
	}
}

// Startup requests the media job of every source, in any state, at server
// start after the runner started (D4): a request a stopped server never
// served is served, and offline sources derive from the index.
func (s *Service) Startup(ctx context.Context) error {
	return s.runner.Write(ctx, func(tx *jobs.Tx) error {
		rows, err := tx.SQL().QueryContext(ctx, `SELECT id FROM sources ORDER BY id`)
		if err != nil {
			return fmt.Errorf("dates: list the sources: %w", err)
		}
		var ids []domain.SourceID
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("dates: list the sources: %w", err)
			}
			ids = append(ids, domain.SourceID(id))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("dates: list the sources: %w", err)
		}
		for _, id := range ids {
			if err := EnqueueMedia(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Run runs one attempt of a media job (D4).
func (h *handler) Run(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	s := h.s
	var p mediaPayload
	dec := json.NewDecoder(bytes.NewReader(job.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return domain.Wrap(domain.CodeInvalidRequest, err, "media job %d: bad payload", job.ID)
	}
	ok, err := s.start(ctx, job, p)
	if err != nil {
		var d *jobs.Defer
		if errors.As(err, &d) {
			return err
		}
		return s.fail(ctx, job, err)
	}
	if !ok {
		return nil
	}
	if err := s.stage(ctx, job, stageStarted); err != nil {
		return s.fail(ctx, job, err)
	}
	for {
		if err := s.clearDirty(ctx, job.SourceID); err != nil {
			return s.fail(ctx, job, err)
		}
		if err := s.passes(ctx, job, rt); err != nil {
			return s.fail(ctx, job, err)
		}
		again, err := s.end(ctx, job)
		if err != nil {
			return s.fail(ctx, job, err)
		}
		if !again {
			// passes_job is released: a failure from here would only set
			// dirty, which a hook's request has set already.
			if err := s.stage(ctx, job, stageEnded); err != nil {
				return s.fail(ctx, job, err)
			}
			return nil
		}
	}
}

// stage calls the test hook.
func (s *Service) stage(ctx context.Context, job jobs.Job, name string) error {
	if s.job.hook == nil {
		return nil
	}
	return s.job.hook(ctx, job, name)
}

// start is D4's start transaction. It returns a *jobs.Defer as its error
// when the job must wait, and false when a first attempt with if_dirty
// finds nothing requested.
func (s *Service) start(ctx context.Context, job jobs.Job, p mediaPayload) (bool, error) {
	src := job.SourceID
	var (
		run  bool
		wait *jobs.Defer
	)
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		run, wait = false, nil
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_sources (source_id) VALUES (?)
			ON CONFLICT (source_id) DO NOTHING`, string(src)); err != nil {
			return fmt.Errorf("dates: media state of %q: %w", src, err)
		}
		var (
			dirty  bool
			passes sql.NullInt64
		)
		if err := tx.QueryRowContext(ctx, `SELECT dirty, passes_job FROM media_sources WHERE source_id = ?`,
			string(src)).Scan(&dirty, &passes); err != nil {
			return fmt.Errorf("dates: media state of %q: %w", src, err)
		}
		if passes.Valid && passes.Int64 != int64(job.ID) {
			var state string
			err := tx.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id = ?`, passes.Int64).Scan(&state)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("dates: read media job %d: %w", passes.Int64, err)
			}
			if state == string(domain.JobRunning) {
				wait = &jobs.Defer{Until: s.clk.Now().Add(startDelay), Reason: DeferMediaRunning}
				return nil
			}
		}
		if job.Attempt <= 1 && p.IfDirty && !dirty {
			return nil
		}
		if s.job.active != nil {
			busy, err := s.job.active(ctx, tx, src)
			if err != nil {
				return fmt.Errorf("dates: is %q being organized: %w", src, err)
			}
			if busy {
				wait = &jobs.Defer{Until: s.clk.Now().Add(startDelay), Reason: index.DeferOrganizing}
				return nil
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_sources SET passes_job = ? WHERE source_id = ?`,
			int64(job.ID), string(src)); err != nil {
			return fmt.Errorf("dates: take the passes of %q: %w", src, err)
		}
		run = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if wait != nil {
		return false, wait
	}
	return run, nil
}

// clearDirty starts a loop iteration: requests from here on make it run
// again.
func (s *Service) clearDirty(ctx context.Context, src domain.SourceID) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE media_sources SET dirty = 0 WHERE source_id = ?`, string(src)); err != nil {
			return fmt.Errorf("dates: clear the request of %q: %w", src, err)
		}
		return nil
	})
}

// end reads dirty after the passes: set, the loop runs again; clear, it
// releases passes_job in the same transaction.
func (s *Service) end(ctx context.Context, job jobs.Job) (bool, error) {
	var again bool
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT dirty FROM media_sources WHERE source_id = ?`,
			string(job.SourceID)).Scan(&again); err != nil {
			return fmt.Errorf("dates: read the requests of %q: %w", job.SourceID, err)
		}
		if again {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_sources SET passes_job = NULL
			WHERE source_id = ? AND passes_job = ?`, string(job.SourceID), int64(job.ID)); err != nil {
			return fmt.Errorf("dates: release the passes of %q: %w", job.SourceID, err)
		}
		return nil
	})
	return again, err
}

// fail is D4's failure transaction: dirty is set and passes_job released
// (when this job holds it), also after a cancel, so the request being
// served is served by the follow-up, the next request, or the next start.
// It returns err, joined with its own failure.
func (s *Service) fail(ctx context.Context, job jobs.Job, err error) error {
	ctx = context.WithoutCancel(ctx)
	ferr := s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE media_sources SET dirty = 1,
			passes_job = CASE WHEN passes_job = ? THEN NULL ELSE passes_job END WHERE source_id = ?`,
			int64(job.ID), string(job.SourceID))
		return err
	})
	if ferr != nil {
		return errors.Join(err, fmt.Errorf("dates: keep the request of %q: %w", job.SourceID, ferr))
	}
	return err
}

// passes runs passes 1–4 over the job's source (D4). A source that cannot
// be opened, or that goes away during the read pass, runs passes 3 and 4
// only, which need the index alone.
func (s *Service) passes(ctx context.Context, job jobs.Job, rt jobs.Runtime) error {
	p := &progress{rt: rt}
	src := job.SourceID
	opened, err := s.openSource(ctx, rt, src)
	if err != nil {
		return err
	}
	if opened != nil {
		p.phase(phasePlan)
		err := s.plan(ctx, rt, src)
		if err == nil {
			err = s.stage(ctx, job, stagePlanned)
		}
		if err == nil {
			p.phase(phaseRead)
			err = s.readPass(ctx, job, rt, opened, p)
		}
		opened.close(rt)
		if err != nil {
			return err
		}
		if err := s.stage(ctx, job, stageRead); err != nil {
			return err
		}
	}
	p.phase(phaseDates)
	if err := s.deriveAll(ctx, src, p); err != nil {
		return err
	}
	if err := s.stage(ctx, job, stageDerived); err != nil {
		return err
	}
	p.phase(phaseCameras)
	if err := s.cameras(ctx, job); err != nil {
		return err
	}
	return s.stage(ctx, job, stageDetected)
}

// progress holds the counters of one loop iteration and publishes them.
type progress struct {
	rt jobs.Runtime
	c  map[string]int64
}

func (p *progress) phase(n int64) {
	if p.c == nil {
		p.c = map[string]int64{progFiles: 0, progOfFiles: 0, progBytes: 0, progChanged: 0, progUnreadable: 0,
			progMedia: 0, progOfMedia: 0}
	}
	p.c[progPhase] = n
	p.publish()
}

func (p *progress) set(key string, v int64) { p.c[key] = v }
func (p *progress) add(key string, v int64) { p.c[key] += v }
func (p *progress) publish()                { p.rt.Progress(p.c) }

// deriveWindow is the number of entries pass 3 derives per write.
const deriveWindow = 256

// deriveWindowSQL reads pass 3's next window inside one span of entry IDs.
// entries is read NOT INDEXED, by its rowid range, because the store never
// runs ANALYZE: without statistics SQLite rates source_id = ? on an index of
// entries as highly selective and would walk the whole source in it, then
// sort, per window (Addendum G1). The span's upper bound keeps a window
// that finds fewer than deriveWindow rows from reading every entry after it
// (Addendum K1).
var deriveWindowSQL = `SELECT e.id FROM entries e NOT INDEXED WHERE e.id > ? AND e.id <= ? AND e.source_id = ?
		AND (` + MediaCond("e") + ` OR EXISTS (SELECT 1 FROM media_dates d WHERE d.entry_id = e.id))
		ORDER BY e.id LIMIT ?`

// deriveAll is pass 3 (D4, D9): Rederive over every media entry of src,
// and every entry of src that still holds a media_dates row though
// MediaCond no longer holds it (Addendum C6), in windows of at most
// deriveWindow entry IDs, each derived inside its write transaction. The
// windows walk src's rowid range, from its first entry to its last, in
// spans of at most idSpan IDs: a window that fills up continues after its
// last ID, one that does not after its span (Addendum K1). The window of
// the last span rewrites src's summary with a recount and stamps
// summary_at.
func (s *Service) deriveAll(ctx context.Context, src domain.SourceID, p *progress) error {
	var total int64
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM entries e
		WHERE e.source_id = ? AND `+MediaCond("e"), string(src)).Scan(&total); err != nil {
		return fmt.Errorf("dates: count the media of %q: %w", src, err)
	}
	p.set(progOfMedia, total)
	p.publish()
	after, last, err := s.sourceIDs(ctx, src)
	if err != nil {
		return err
	}
	for {
		var (
			n    int
			next int64
		)
		end := min(after+idSpan, last)
		err := s.st.Write(ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, deriveWindowSQL, after, end, string(src), deriveWindow)
			if err != nil {
				return err
			}
			var ids []domain.EntryID
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, domain.EntryID(id))
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			n = len(ids)
			if n > 0 {
				if err := s.Rederive(ctx, tx, ids); err != nil {
					return err
				}
			}
			if n == deriveWindow {
				next = int64(ids[n-1])
				return nil
			}
			next = end
			if end < last {
				return nil
			}
			sum, err := recount(ctx, tx, src)
			if err != nil {
				return err
			}
			if err := writeSummary(ctx, tx, src, sum); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE media_sources SET summary_at = ? WHERE source_id = ?`,
				clock.Millis(s.clk.Now()), string(src))
			return err
		})
		if err != nil {
			return fmt.Errorf("dates: derive the dates of %q: %w", src, err)
		}
		p.set(progMedia, min(p.c[progMedia]+int64(n), total))
		p.publish()
		if n < deriveWindow && end >= last {
			return nil
		}
		after = next
	}
}
