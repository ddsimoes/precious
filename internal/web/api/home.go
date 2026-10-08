package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"precious/internal/content"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/review"
	"precious/internal/search"
)

type homeBody struct {
	Totals   homeTotals            `json:"totals"`
	ByFamily []search.FamilyAmount `json:"by_family"`
	ByKind   []kindAmount          `json:"by_kind"`
	ByYear   []yearAmount          `json:"by_year"`
	// Decisions are decisions.Totals summed by decision, with the
	// quarantine bucket beside them (r4 design D15).
	Decisions map[string]amount `json:"decisions"`
	Partial   bool              `json:"partial"`
	Scans     []homeScan        `json:"scans"`
	// The R2 fields: what hashing checked, the opportunity cards, and the
	// active hashing jobs.
	Coverage coverageJSON `json:"coverage"`
	Cards    []cardJSON   `json:"cards"`
	Hashing  []homeScan   `json:"hashing"`
}

type homeTotals struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
	Dirs  int64 `json:"dirs"`
}

// homeScan is an active job: a scan, or a hashing job, which names its
// kind (hash or hash_now).
type homeScan struct {
	SourceID domain.SourceID  `json:"source_id"`
	JobID    string           `json:"job_id"`
	Kind     jobs.Kind        `json:"kind,omitempty"`
	State    domain.JobState  `json:"state"`
	Progress map[string]int64 `json:"progress"`
}

// home serves GET /api/home[?source=ID]: the figures of one source, or of
// every source when source is absent or empty. Totals and breakdowns are
// the sums of the root entries and their dir_stats (a source not scanned
// yet adds nothing), which leave the quarantine out (r4 design D2);
// decisions are decisions.Totals summed, and their quarantine bucket the
// sum of each source's quarantine folder row (D15); partial is set when a
// root is partial or unreadable, and scans are the queued, running, and
// paused scans with their progress. Coverage is the source's (all
// sources' without one), cards are review.Cards for the source, and
// hashing lists the active hash and hash_now jobs like the scans. An
// unknown source is not_found.
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
		body    = homeBody{Decisions: make(map[string]amount, len(domain.Decisions)+1)}
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
		body.Decisions[string(d)] = amount{}
	}
	var quarantine amount
	for _, s := range sources {
		totals, err := decisions.Totals(ctx, tx, s)
		if err != nil {
			return homeBody{}, err
		}
		for d, t := range totals {
			a := body.Decisions[string(d)]
			a.add(amount{Files: t.Files, Bytes: t.Bytes})
			body.Decisions[string(d)] = a
		}
		q, err := quarantineAmount(ctx, tx, s)
		if err != nil {
			return homeBody{}, err
		}
		quarantine.add(q)
	}
	body.Decisions[quarantineBucket] = quarantine

	if body.Scans, err = activeJobs(ctx, tx, src, false, jobs.KindScan); err != nil {
		return homeBody{}, err
	}
	if body.Hashing, err = activeJobs(ctx, tx, src, true, content.KindHash, content.KindHashNow); err != nil {
		return homeBody{}, err
	}
	if body.Coverage, err = readCoverage(ctx, tx, src); err != nil {
		return homeBody{}, err
	}
	if body.Cards, err = readCards(ctx, tx, src); err != nil {
		return homeBody{}, err
	}
	return body, nil
}

// quarantineBucket is the key of Home's decisions that holds the bytes and
// files in quarantine.
const quarantineBucket = "quarantine"

// quarantineAmount reads the files and bytes in the quarantine of source
// src: its quarantine folder row's totals, which the scans and Refold fold
// while the top leaves them out (r4 design D2, D15); zero without one.
func quarantineAmount(ctx context.Context, tx *sql.Tx, src domain.SourceID) (amount, error) {
	var a amount
	err := tx.QueryRowContext(ctx, `SELECT ifnull(sum(q.total_files), 0), ifnull(sum(q.total_bytes), 0) FROM entries q
		WHERE q.source_id = ? AND q.path = ? AND q.kind = 'directory' AND q.state <> 'missing'`,
		string(src), []byte(index.QuarantineName)).Scan(&a.Files, &a.Bytes)
	if err != nil {
		return amount{}, fmt.Errorf("api: quarantine of %s: %w", src, err)
	}
	return a, nil
}

// activeJobs lists the queued, running, and paused jobs of the kinds for
// src (every source when ""), oldest first, naming their kind when named.
func activeJobs(ctx context.Context, tx *sql.Tx, src domain.SourceID, named bool, kinds ...jobs.Kind) ([]homeScan, error) {
	args := make([]any, 0, len(kinds)+2)
	for _, k := range kinds {
		args = append(args, string(k))
	}
	args = append(args, string(src), string(src))
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, source_id, state, cancel_requested, progress FROM jobs
		WHERE kind IN (`+marks(len(kinds))+`) AND state IN ('queued', 'running', 'paused') AND (? = '' OR source_id = ?)
		ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: active jobs: %w", err)
	}
	defer rows.Close()
	out := []homeScan{}
	for rows.Next() {
		var (
			job          int64
			kind, source string
			state        string
			cancelReq    bool
			progress     []byte
		)
		if err := rows.Scan(&job, &kind, &source, &state, &cancelReq, &progress); err != nil {
			return nil, fmt.Errorf("api: active jobs: %w", err)
		}
		s := homeScan{
			SourceID: domain.SourceID(source),
			JobID:    domain.JobID(job).String(),
			State:    jobs.DisplayState(domain.JobState(state), cancelReq),
			Progress: map[string]int64{},
		}
		if named {
			s.Kind = jobs.Kind(kind)
		}
		if err := json.Unmarshal(progress, &s.Progress); err != nil {
			return nil, fmt.Errorf("api: progress of job %d: %w", job, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: active jobs: %w", err)
	}
	return out, nil
}

// cardJSON is CardJSON: its open rows, and its rows no longer open (r2b
// D13).
type cardJSON struct {
	List         review.List `json:"list"`
	Bytes        int64       `json:"bytes"`
	Rows         int64       `json:"rows"`
	DecidedBytes int64       `json:"decided_bytes"`
	DecidedRows  int64       `json:"decided_rows"`
	Basis        string      `json:"basis"`
}

func toCardJSON(c review.Card) cardJSON {
	return cardJSON{List: c.List, Bytes: c.Bytes, Rows: c.Rows, DecidedBytes: c.DecidedBytes, DecidedRows: c.DecidedRows,
		Basis: c.Basis}
}

// readCards reads the eight cards of src ("" = every source) in
// review.Cards' order: the rescue card first while it has open rows, then
// largest first.
func readCards(ctx context.Context, tx *sql.Tx, src domain.SourceID) ([]cardJSON, error) {
	cards, err := review.Cards(ctx, tx, src)
	if err != nil {
		return nil, err
	}
	out := make([]cardJSON, len(cards))
	for i, c := range cards {
		out[i] = toCardJSON(c)
	}
	return out, nil
}
