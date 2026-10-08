package cleanup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/organize"
	"precious/internal/store"
	"precious/internal/web/clientip"
)

// Conditions on a purge_check_files row f of the check c (D8, U10):
// gatedSQL holds for a file that needs a confirmation (its own, or its
// group's for likely junk), outside an item that is not readable, which is
// refused and never purged; confirmedSQL for one that has it.
const (
	gatedSQL = `((f.verdict IN ('unique', 'copy_offline', 'unreadable')
			OR (f.verdict = 'opaque_archive' AND f.copy_path IS NULL))
		AND NOT EXISTS (SELECT 1 FROM purge_check_items i WHERE i.check_id = f.check_id AND i.entry_id = f.item_id
			AND i.readable = 0))`
	confirmedSQL = `(f.confirmed_at IS NOT NULL
		OR (f.verdict = 'unique' AND f.class IS 'likely_junk' AND c.junk_confirmed_at IS NOT NULL))`
	// ownSQL holds for a file confirmed one by one: of a gated verdict and
	// not likely junk, which its group confirms. A file of an item that is
	// not readable is accepted too, though it gates nothing.
	ownSQL = `(f.verdict IN ('copy_offline', 'unreadable')
		OR (f.verdict = 'opaque_archive' AND f.copy_path IS NULL)
		OR (f.verdict = 'unique' AND f.class IS NOT 'likely_junk'))`
)

// checkStarted answers check-purge (202).
type checkStarted struct {
	CheckID string `json:"check_id"`
	JobID   string `json:"job_id"`
}

// checkPurge starts a pre-delete check of quarantined top items of one
// source (D7): it settles checks whose job ended without them, refuses
// while a check of the source runs, or while the source is offline, and
// inserts the check, running, with its set, then enqueues its job.
func (s *Service) checkPurge(ctx context.Context, tx *jobs.Tx, req checkPurgeRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	tops, err := topItems(ctx, q, "entry_ids", req.EntryIDs)
	if err != nil {
		return 0, nil, err
	}
	src := tops[0].n.source
	if err := settleChecks(ctx, q, now); err != nil {
		return 0, nil, err
	}
	if err := sourceOnline(ctx, q, src); err != nil {
		return 0, nil, err
	}
	var running int64
	err = q.QueryRowContext(ctx, `SELECT id FROM purge_checks WHERE source_id = ? AND state = 'running' LIMIT 1`,
		string(src)).Scan(&running)
	switch {
	case err == nil:
		return 0, nil, domain.Errorf(domain.CodeCheckRunning,
			"check %d of source %q is still running; wait for it to end, or cancel it", running, src)
	case !errors.Is(err, sql.ErrNoRows):
		return 0, nil, err
	}
	var check int64
	if err := q.QueryRowContext(ctx, `INSERT INTO purge_checks (source_id, state, created_at) VALUES (?, 'running', ?)
		RETURNING id`, string(src), clock.Millis(now)).Scan(&check); err != nil {
		return 0, nil, fmt.Errorf("cleanup: insert check: %w", err)
	}
	for _, t := range tops {
		if _, err := q.ExecContext(ctx, `INSERT INTO purge_check_items (check_id, entry_id, path, readable)
			VALUES (?, ?, ?, 1)`, check, t.n.id, t.n.path); err != nil {
			return 0, nil, fmt.Errorf("cleanup: insert check item: %w", err)
		}
	}
	rec, err := enqueueCheck(tx, src, check)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, checkStarted{CheckID: strconv.FormatInt(check, 10), JobID: rec.ID.String()}, nil
}

// confirmResponse answers confirm-purge.
type confirmResponse struct {
	Check checkJSON `json:"check"`
}

// confirmPurge records the owner's confirmations of a ready check (D8):
// files one by one (each recorded in the check and needing its own
// confirmation, else invalid_request), or the likely junk group.
func (s *Service) confirmPurge(ctx context.Context, tx *jobs.Tx, req confirmPurgeRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	id, err := parseID("check", req.CheckID)
	if err != nil {
		return 0, nil, err
	}
	if err := settleChecks(ctx, q, now); err != nil {
		return 0, nil, err
	}
	var src, state string
	err = q.QueryRowContext(ctx, `SELECT source_id, state FROM purge_checks WHERE id = ?`, id).Scan(&src, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, notFound("check", req.CheckID)
	}
	if err != nil {
		return 0, nil, err
	}
	switch state {
	case checkRunning:
		return 0, nil, domain.Errorf(domain.CodeCheckRunning, "check %d is still running; wait for it to end", id)
	case checkStale, checkFailed:
		return 0, nil, domain.Errorf(domain.CodeCheckStale,
			"check %d is %s; check the items again before confirming anything", id, state)
	}
	detail := map[string]any{"check_id": req.CheckID, "source_id": src}
	if req.Group != "" {
		if _, err := q.ExecContext(ctx, `UPDATE purge_checks SET junk_confirmed_at = ? WHERE id = ?
			AND junk_confirmed_at IS NULL`, clock.Millis(now), id); err != nil {
			return 0, nil, err
		}
		var files, bytes int64
		if err := q.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(size), 0) FROM purge_check_files
			WHERE check_id = ? AND verdict = 'unique' AND class = 'likely_junk'`, id).Scan(&files, &bytes); err != nil {
			return 0, nil, err
		}
		detail["group"], detail["files"], detail["bytes"] = req.Group, files, bytes
	} else {
		ids := make([]int64, 0, len(req.FileIDs))
		for _, f := range req.FileIDs {
			n, err := strconv.ParseInt(f, 10, 64)
			if err != nil || n < 1 {
				return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "file %q is not in check %d", f, id)
			}
			ids = append(ids, n)
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		var files, bytes int64
		for _, f := range ids {
			var own bool
			var size int64
			err := q.QueryRowContext(ctx, `SELECT `+ownSQL+`, f.size FROM purge_check_files f
				WHERE f.id = ? AND f.check_id = ?`, f, id).Scan(&own, &size)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "file %d is not in check %d", f, id)
			}
			if err != nil {
				return 0, nil, err
			}
			if !own {
				return 0, nil, domain.Errorf(domain.CodeInvalidRequest,
					"file %d needs no confirmation of its own: it has a verified copy, holds nothing, or is likely junk, which its group confirms", f)
			}
			if _, err := q.ExecContext(ctx, `UPDATE purge_check_files SET confirmed_at = ? WHERE id = ?
				AND confirmed_at IS NULL`, clock.Millis(now), f); err != nil {
				return 0, nil, err
			}
			files++
			bytes += size
		}
		fileIDs := make([]string, len(ids))
		for i, f := range ids {
			fileIDs[i] = strconv.FormatInt(f, 10)
		}
		detail["file_ids"], detail["files"], detail["bytes"] = fileIDs, files, bytes
	}
	ev := auth.AuditEvent{At: now, Kind: AuditPurgeConfirmed, Actor: auth.ActorAdmin, Detail: detail}
	if info, ok := clientip.From(ctx); ok {
		ev.ClientAddr = info.Addr
	}
	if err := auth.WriteAudit(ctx, q, ev); err != nil {
		return 0, nil, err
	}
	c, err := readCheck(ctx, q, id)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, confirmResponse{Check: c}, nil
}

// planPurge plans plan-purge (D11, E8): the check must be ready with every
// file safe or confirmed (organize.GatePurge), and its source writable. The
// purge holds one verify item, one purge item per readable set item (an
// unreadable one is refused), then the sweep of each plan folder the purge
// empties.
func (s *Service) planPurge(ctx context.Context, tx *jobs.Tx, req planPurgeRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	id, err := parseID("check", req.CheckID)
	if err != nil {
		return 0, nil, err
	}
	if err := settleChecks(ctx, q, now); err != nil {
		return 0, nil, err
	}
	var src domain.SourceID
	err = q.QueryRowContext(ctx, `SELECT source_id FROM purge_checks WHERE id = ?`, id).Scan(&src)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, notFound("check", req.CheckID)
	}
	if err != nil {
		return 0, nil, err
	}
	if err := organize.GatePurge(ctx, q, src, id); err != nil {
		return 0, nil, err
	}
	if err := organize.CheckSource(ctx, q, src, s.allowWrites); err != nil {
		return 0, nil, err
	}
	if err := organize.Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT entry_id, readable FROM purge_check_items WHERE check_id = ? ORDER BY path`, id)
	if err != nil {
		return 0, nil, fmt.Errorf("cleanup: items of check %d: %w", id, err)
	}
	type setItem struct {
		entry    int64
		readable bool
	}
	var set []setItem
	for rows.Next() {
		var it setItem
		if err := rows.Scan(&it.entry, &it.readable); err != nil {
			rows.Close()
			return 0, nil, err
		}
		set = append(set, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	steps := []*step{{op: opVerify, state: statePlanned}}
	var tops []*topItem
	leaving := map[int64]map[int64]bool{}
	purged := 0
	for _, it := range set {
		t, err := loadTop(ctx, q, it.entry)
		if err != nil {
			return 0, nil, err
		}
		if t == nil {
			// Out of place since the check was ready: the check is stale
			// (MarkStale runs in every move's transaction).
			return 0, nil, domain.Errorf(domain.CodeCheckStale,
				"an item of check %d is no longer in the quarantine; check the items again", id)
		}
		n := t.n
		p := &step{op: opPurge, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path, bytes: n.bytes,
			files: n.files, state: statePlanned}
		if !it.readable {
			p.state, p.reason = stateRefused, reasonUnreadable
			steps = append(steps, p)
			continue
		}
		steps = append(steps, p)
		purged++
		tops = append(tops, t)
		if leaving[t.plan.id] == nil {
			leaving[t.plan.id] = map[int64]bool{}
		}
		leaving[t.plan.id][t.seq.id] = true
	}
	if purged == 0 {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest,
			"nothing in check %d can be deleted: every item holds a folder that could not be read", id)
	}
	sweeps, err := sweepAll(ctx, q, tops, leaving)
	if err != nil {
		return 0, nil, err
	}
	steps = append(steps, sweeps...)
	if len(steps) > maxSteps {
		return 0, nil, tooMany(len(steps))
	}
	action, err := insertAction(ctx, tx, action{kind: kindPurge, src: src, bulk: true, check: id, ttl: purgeTTL})
	if err != nil {
		return 0, nil, err
	}
	if err := insertSteps(ctx, q, action, steps); err != nil {
		return 0, nil, err
	}
	res, err := organize.ReadPlan(ctx, q, action, now)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, res, nil
}

// amount is a number of files and their bytes.
type amount struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// countsJSON holds a check's buckets by verdict, and by class over its
// unique files.
type countsJSON struct {
	Verdict map[string]amount `json:"verdict"`
	Class   map[string]amount `json:"class"`
}

// checkJSON is Check of the Interfaces section.
type checkJSON struct {
	ID            string          `json:"id"`
	SourceID      domain.SourceID `json:"source_id"`
	State         string          `json:"state"`
	JobID         *string         `json:"job_id"`
	CreatedAt     time.Time       `json:"created_at"`
	FinishedAt    *time.Time      `json:"finished_at"`
	StaleReason   *string         `json:"stale_reason"`
	Items         int64           `json:"items"`
	Counts        countsJSON      `json:"counts"`
	Confirmed     amount          `json:"confirmed"`
	Unconfirmed   amount          `json:"unconfirmed"`
	JunkConfirmed bool            `json:"junk_confirmed"`
	Allowed       bool            `json:"allowed"`
}

var (
	verdicts = []string{verdictSafe, verdictCopyOffline, verdictUnique, verdictUnreadable, verdictOpaque,
		verdictNoContent}
	classes = []string{classValuable, classJunk, classUncertain}
)

// readCheck reads Check id; not_found when there is none.
func readCheck(ctx context.Context, q store.Queryer, id int64) (checkJSON, error) {
	var (
		c                   checkJSON
		created             int64
		job, finished, junk sql.NullInt64
		reason              sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT source_id, state, job_id, created_at, finished_at, stale_reason,
		junk_confirmed_at, (SELECT count(*) FROM purge_check_items WHERE check_id = purge_checks.id)
		FROM purge_checks WHERE id = ?`, id).Scan(&c.SourceID, &c.State, &job, &created, &finished, &reason, &junk,
		&c.Items)
	if errors.Is(err, sql.ErrNoRows) {
		return checkJSON{}, notFound("check", strconv.FormatInt(id, 10))
	}
	if err != nil {
		return checkJSON{}, fmt.Errorf("cleanup: read check %d: %w", id, err)
	}
	c.ID, c.CreatedAt, c.JunkConfirmed = strconv.FormatInt(id, 10), clock.FromMillis(created).UTC(), junk.Valid
	if job.Valid {
		j := strconv.FormatInt(job.Int64, 10)
		c.JobID = &j
	}
	if finished.Valid {
		t := clock.FromMillis(finished.Int64).UTC()
		c.FinishedAt = &t
	}
	if reason.Valid {
		c.StaleReason = &reason.String
	}
	c.Counts = countsJSON{Verdict: make(map[string]amount, len(verdicts)), Class: make(map[string]amount, len(classes))}
	for _, v := range verdicts {
		c.Counts.Verdict[v] = amount{}
	}
	for _, k := range classes {
		c.Counts.Class[k] = amount{}
	}
	rows, err := q.QueryContext(ctx, `SELECT f.verdict, coalesce(f.class, ''), `+gatedSQL+`, `+confirmedSQL+`,
			count(*), coalesce(sum(f.size), 0)
		FROM purge_check_files f JOIN purge_checks c ON c.id = f.check_id WHERE f.check_id = ?
		GROUP BY 1, 2, 3, 4`, id)
	if err != nil {
		return checkJSON{}, fmt.Errorf("cleanup: count check %d: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			verdict, class   string
			gated, confirmed bool
			a                amount
		)
		if err := rows.Scan(&verdict, &class, &gated, &confirmed, &a.Files, &a.Bytes); err != nil {
			return checkJSON{}, err
		}
		v := c.Counts.Verdict[verdict]
		v.Files, v.Bytes = v.Files+a.Files, v.Bytes+a.Bytes
		c.Counts.Verdict[verdict] = v
		if verdict == verdictUnique && class != "" {
			k := c.Counts.Class[class]
			k.Files, k.Bytes = k.Files+a.Files, k.Bytes+a.Bytes
			c.Counts.Class[class] = k
		}
		switch {
		case gated && confirmed:
			c.Confirmed.Files += a.Files
			c.Confirmed.Bytes += a.Bytes
		case gated:
			c.Unconfirmed.Files += a.Files
			c.Unconfirmed.Bytes += a.Bytes
		}
	}
	if err := rows.Err(); err != nil {
		return checkJSON{}, err
	}
	c.Allowed = c.State == checkReady && c.Unconfirmed.Files == 0
	return c, nil
}
