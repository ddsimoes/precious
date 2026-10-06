package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/search"
)

type homeBody struct {
	Totals    homeTotals                 `json:"totals"`
	ByFamily  []search.FamilyAmount      `json:"by_family"`
	ByKind    []kindAmount               `json:"by_kind"`
	ByYear    []yearAmount               `json:"by_year"`
	Decisions map[domain.Decision]amount `json:"decisions"`
	Partial   bool                       `json:"partial"`
	Scans     []homeScan                 `json:"scans"`
}

type homeTotals struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
	Dirs  int64 `json:"dirs"`
}

type homeScan struct {
	SourceID domain.SourceID  `json:"source_id"`
	JobID    string           `json:"job_id"`
	State    domain.JobState  `json:"state"`
	Progress map[string]int64 `json:"progress"`
}

// home serves GET /api/home[?source=ID]: the figures of one source, or of
// every source when source is absent or empty. Totals and breakdowns are
// the sums of the root entries and their dir_stats (a source not scanned
// yet adds nothing), decisions are decisions.Totals summed, partial is set
// when a root is partial or unreadable, and scans are the queued, running,
// and paused scans with their progress. An unknown source is not_found.
func (h *handler) home(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := checkParams(q, "source"); err != nil {
		h.fail(w, err)
		return
	}
	src := domain.SourceID(q.Get("source"))
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		return readHome(ctx, tx, src)
	})
}

func readHome(ctx context.Context, tx *sql.Tx, src domain.SourceID) (homeBody, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.id, ifnull(e.total_bytes, 0), ifnull(e.total_files, 0),
		ifnull(e.partial, 0) OR ifnull(e.state = 'unreadable', 0),
		ifnull(d.dirs, 0), ifnull(d.by_kind, '{}'), ifnull(d.by_year, '{}'), ifnull(d.by_family, '{}')
		FROM sources s
		LEFT JOIN entries e ON e.source_id = s.id AND e.path = X''
		LEFT JOIN dir_stats d ON d.entry_id = e.id
		WHERE ?1 = '' OR s.id = ?1
		ORDER BY s.id`, string(src))
	if err != nil {
		return homeBody{}, fmt.Errorf("api: home: %w", err)
	}
	defer rows.Close()
	var (
		body    = homeBody{Decisions: make(map[domain.Decision]amount, len(domain.Decisions))}
		b       = newBreakdowns()
		sources []domain.SourceID
	)
	for rows.Next() {
		var (
			id                    string
			bytes, files, dirs    int64
			partial               bool
			byKind, byYear, byFam []byte
		)
		if err := rows.Scan(&id, &bytes, &files, &partial, &dirs, &byKind, &byYear, &byFam); err != nil {
			return homeBody{}, fmt.Errorf("api: home: %w", err)
		}
		sources = append(sources, domain.SourceID(id))
		body.Totals.Bytes += bytes
		body.Totals.Files += files
		body.Totals.Dirs += dirs
		body.Partial = body.Partial || partial
		if err := b.addKinds(byKind); err != nil {
			return homeBody{}, err
		}
		if err := b.addYears(byYear); err != nil {
			return homeBody{}, err
		}
		if err := b.addFamilies(byFam); err != nil {
			return homeBody{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return homeBody{}, fmt.Errorf("api: home: %w", err)
	}
	if err := rows.Close(); err != nil {
		return homeBody{}, fmt.Errorf("api: home: %w", err)
	}
	if src != "" && len(sources) == 0 {
		return homeBody{}, domain.Errorf(domain.CodeNotFound, "source %q not found", src)
	}
	body.ByFamily, body.ByKind, body.ByYear = b.familyList(), b.kindList(), b.yearList()

	for _, d := range domain.Decisions {
		body.Decisions[d] = amount{}
	}
	for _, s := range sources {
		totals, err := decisions.Totals(ctx, tx, s)
		if err != nil {
			return homeBody{}, err
		}
		for d, t := range totals {
			a := body.Decisions[d]
			a.add(amount{Files: t.Files, Bytes: t.Bytes})
			body.Decisions[d] = a
		}
	}

	if body.Scans, err = activeScans(ctx, tx, src); err != nil {
		return homeBody{}, err
	}
	return body, nil
}

// activeScans lists the queued, running, and paused scans of src (every
// source when ""), oldest first.
func activeScans(ctx context.Context, tx *sql.Tx, src domain.SourceID) ([]homeScan, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, source_id, state, cancel_requested, progress FROM jobs
		WHERE kind = ?1 AND state IN ('queued', 'running', 'paused') AND (?2 = '' OR source_id = ?2)
		ORDER BY id`, string(jobs.KindScan), string(src))
	if err != nil {
		return nil, fmt.Errorf("api: active scans: %w", err)
	}
	defer rows.Close()
	scans := []homeScan{}
	for rows.Next() {
		var (
			job       int64
			source    string
			state     string
			cancelReq bool
			progress  []byte
		)
		if err := rows.Scan(&job, &source, &state, &cancelReq, &progress); err != nil {
			return nil, fmt.Errorf("api: active scans: %w", err)
		}
		s := homeScan{
			SourceID: domain.SourceID(source),
			JobID:    domain.JobID(job).String(),
			State:    jobs.DisplayState(domain.JobState(state), cancelReq),
			Progress: map[string]int64{},
		}
		if err := json.Unmarshal(progress, &s.Progress); err != nil {
			return nil, fmt.Errorf("api: progress of job %d: %w", job, err)
		}
		scans = append(scans, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: active scans: %w", err)
	}
	return scans, nil
}
