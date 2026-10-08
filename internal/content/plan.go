package content

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/store"
)

// Coverage is what hashing knows, per source or in total (design D8): the
// files and bytes that could have a copy (every row but unique_size), those
// checked (hashed, or sampled with a sample distinct within its size),
// those not checked yet (pending, changed), and those that could not be
// read. File members of complete archives count under their archive's
// source.
type Coverage struct {
	CandidateFiles, CandidateBytes   int64
	CheckedFiles, CheckedBytes       int64
	UncheckedFiles, UncheckedBytes   int64
	UnreadableFiles, UnreadableBytes int64
}

// CoverageOf returns the published coverage of src, or of every source
// together when src is "". A source without a row has zero coverage.
func CoverageOf(ctx context.Context, q store.Queryer, src domain.SourceID) (Coverage, error) {
	var c Coverage
	where, args := "", []any{}
	if src != "" {
		where, args = ` WHERE source_id = ?`, []any{string(src)}
	}
	err := q.QueryRowContext(ctx, `SELECT coalesce(sum(candidate_files), 0), coalesce(sum(candidate_bytes), 0),
			coalesce(sum(checked_files), 0), coalesce(sum(checked_bytes), 0),
			coalesce(sum(unchecked_files), 0), coalesce(sum(unchecked_bytes), 0),
			coalesce(sum(unreadable_files), 0), coalesce(sum(unreadable_bytes), 0)
		FROM content_coverage`+where, args...).
		Scan(&c.CandidateFiles, &c.CandidateBytes, &c.CheckedFiles, &c.CheckedBytes, &c.UncheckedFiles,
			&c.UncheckedBytes, &c.UnreadableFiles, &c.UnreadableBytes)
	if err != nil {
		return Coverage{}, fmt.Errorf("content: read coverage: %w", err)
	}
	return c, nil
}

// coverageRows are the rows coverage counts: present files and the file
// members of complete archives whose file is present, by source, none of
// them in a quarantine (r4 design D2).
var coverageRows = `SELECT f.source_id AS source_id, f.state AS state, f.size AS size FROM file_content f
		JOIN entries e ON e.id = f.entry_id WHERE e.state = 'present' AND ` + notQuarantinedE + `
	UNION ALL
	SELECT e.source_id, m.state, m.size FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
		JOIN entries e ON e.id = a.entry_id
		WHERE a.state = 'complete' AND m.kind = 'file' AND e.state = 'present' AND ` + notQuarantinedE

// recomputeCoverage rewrites content_coverage for every source from its
// rows, which repairs any drift (design D8).
func recomputeCoverage(ctx context.Context, tx *sql.Tx, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_coverage`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO content_coverage (source_id, candidate_files, candidate_bytes,
			checked_files, checked_bytes, unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes, updated_at)
		SELECT s.id,
			count(r.state) FILTER (WHERE r.state <> 'unique_size'), coalesce(sum(r.size) FILTER (WHERE r.state <> 'unique_size'), 0),
			count(r.state) FILTER (WHERE r.state IN ('hashed', 'sampled')), coalesce(sum(r.size) FILTER (WHERE r.state IN ('hashed', 'sampled')), 0),
			count(r.state) FILTER (WHERE r.state IN ('pending', 'changed')), coalesce(sum(r.size) FILTER (WHERE r.state IN ('pending', 'changed')), 0),
			count(r.state) FILTER (WHERE r.state = 'unreadable'), coalesce(sum(r.size) FILTER (WHERE r.state = 'unreadable'), 0),
			?
		FROM sources s LEFT JOIN (`+coverageRows+`) r ON r.source_id = s.id GROUP BY s.id`, now)
	if err != nil {
		return fmt.Errorf("content: recompute coverage: %w", err)
	}
	return nil
}

// bucket is a state's coverage bucket: 0 not a candidate, 1 checked, 2 not
// checked, 3 unreadable. The empty state (a row that did not count) is 0.
func bucket(s domain.ContentState) int {
	switch s {
	case domain.ContentHashed, domain.ContentSampled:
		return 1
	case domain.ContentPending, domain.ContentChanged:
		return 2
	case domain.ContentUnreadable:
		return 3
	}
	return 0
}

// deltas are coverage changes by source, applied in the transaction that
// made them.
type deltas map[string]*[8]int64

// move records that a row of size on src went from state from to state to;
// "" stands for a row that does not count.
func (d deltas) move(src string, from, to domain.ContentState, size int64) {
	bf, bt := bucket(from), bucket(to)
	if bf == bt {
		return
	}
	c := d[src]
	if c == nil {
		c = new([8]int64)
		d[src] = c
	}
	if bf != 0 {
		c[0]--
		c[1] -= size
		c[2*bf]--
		c[2*bf+1] -= size
	}
	if bt != 0 {
		c[0]++
		c[1] += size
		c[2*bt]++
		c[2*bt+1] += size
	}
}

// apply adds the deltas to content_coverage, creating a zero row first
// where a source has none, and clears them.
func (d deltas) apply(ctx context.Context, tx *sql.Tx, now int64) error {
	for src, c := range d {
		if _, err := tx.ExecContext(ctx, `INSERT INTO content_coverage (source_id, candidate_files, candidate_bytes,
				checked_files, checked_bytes, unchecked_files, unchecked_bytes, unreadable_files, unreadable_bytes,
				updated_at) SELECT id, 0, 0, 0, 0, 0, 0, 0, 0, ? FROM sources WHERE id = ?
			ON CONFLICT (source_id) DO NOTHING`, now, src); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE content_coverage SET candidate_files = candidate_files + ?,
				candidate_bytes = candidate_bytes + ?, checked_files = checked_files + ?, checked_bytes = checked_bytes + ?,
				unchecked_files = unchecked_files + ?, unchecked_bytes = unchecked_bytes + ?,
				unreadable_files = unreadable_files + ?, unreadable_bytes = unreadable_bytes + ?, updated_at = ?
			WHERE source_id = ?`, c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7], now, src); err != nil {
			return fmt.Errorf("content: update coverage of %s: %w", src, err)
		}
	}
	clear(d)
	return nil
}

// ident is one physical copy: an entry, an inode on a volume with stable
// identity (hard links count once), or a member with its hard-link target.
type ident struct {
	kind byte // 'e' entry, 'i' inode, 'm' member
	vol  string
	a, b int64
}

// copyRow is one file or file member of a size group.
type copyRow struct {
	id     int64
	member bool
	source string
	state  domain.ContentState
	sample []byte
	size   int64
	key    ident
}

// groupColumns select the copies of size groups: size, id, member, source,
// state, sample, dev, ino, nlink, volume, stable identity, link target.
// filesWhere and membersWhere narrow them. A quarantined file, or a member
// of a quarantined archive, is in no group (r4 design D2).
func groupQuery(filesWhere, membersWhere, order string) string {
	return `SELECT f.size, f.entry_id, 0, f.source_id, f.state, f.sample, e.dev, e.ino, e.nlink, s.volume_id,
			coalesce(json_extract(s.capabilities, '$.stable_identity'), 0), 0
		FROM file_content f JOIN entries e ON e.id = f.entry_id JOIN sources s ON s.id = f.source_id
		WHERE e.state = 'present' AND ` + notQuarantinedE + filesWhere + `
	UNION ALL
	SELECT m.size, m.id, 1, e.source_id, m.state, NULL, NULL, NULL, NULL, NULL, 0, coalesce(m.link_member, m.id)
		FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = a.entry_id
		WHERE m.kind = 'file' AND m.size > 0 AND a.state = 'complete' AND e.state = 'present' AND ` + notQuarantinedE +
		membersWhere + order
}

// notQuarantinedE is index.NotQuarantined("e"), rendered once: the residual
// every content reader adds on its entries row e (r4 design D2).
var notQuarantinedE = index.NotQuarantined("e")

func scanCopy(rows *sql.Rows) (copyRow, error) {
	var (
		r               copyRow
		member, stable  bool
		dev, ino, nlink sql.NullInt64
		vol             sql.NullString
		link            int64
	)
	err := rows.Scan(&r.size, &r.id, &member, &r.source, &r.state, &r.sample, &dev, &ino, &nlink, &vol, &stable, &link)
	r.member = member
	switch {
	case member:
		r.key = ident{kind: 'm', a: link}
	case stable && nlink.Int64 > 1 && dev.Valid && ino.Valid:
		r.key = ident{kind: 'i', vol: vol.String, a: dev.Int64, b: ino.Int64}
	default:
		r.key = ident{kind: 'e', a: r.id}
	}
	return r, err
}

// change is a decided state change of one row.
type change struct {
	row copyRow
	to  domain.ContentState
}

// decide appends the state changes one size group needs (design D3): a
// size with fewer than two physical copies is unique_size, except for
// hashed rows, which keep their digest; a shared size makes unique_size
// rows pending; a file of at least SampleFromBytes with a sample is sampled
// exactly when every other copy of its size has a different sample (a
// member or an unsampled copy may be equal), and pending otherwise.
func decide(g []copyRow, out []change) []change {
	if len(g) == 0 {
		return out
	}
	seen := make(map[ident]struct{}, 2)
	for i := range g {
		seen[g[i].key] = struct{}{}
		if len(seen) > 1 {
			break
		}
	}
	shared := len(seen) > 1
	for i := range g {
		r := &g[i]
		to := r.state
		switch {
		case r.state == domain.ContentHashed:
		case !shared:
			to = domain.ContentUniqueSize
		case r.state == domain.ContentUniqueSize:
			to = domain.ContentPending
			if !r.member && distinctSample(g, r) {
				to = domain.ContentSampled
			}
		case r.member:
		case r.state == domain.ContentPending || r.state == domain.ContentSampled:
			to = domain.ContentPending
			if distinctSample(g, r) {
				to = domain.ContentSampled
			}
		}
		if to != r.state {
			out = append(out, change{row: *r, to: to})
		}
	}
	return out
}

// distinctSample reports whether r's sample differs from every other
// copy's of its size.
func distinctSample(g []copyRow, r *copyRow) bool {
	if r.sample == nil || r.size < SampleFromBytes {
		return false
	}
	for i := range g {
		o := &g[i]
		if o.key == r.key {
			continue
		}
		if o.member || o.sample == nil || bytes.Equal(o.sample, r.sample) {
			return false
		}
	}
	return true
}

// plan is the planning pass of design D3, serialized across jobs: it
// creates the missing file_content rows, decides every size group's
// states, enqueues the hashing of other online sources whose rows became
// pending, and recomputes coverage. It reports whether it changed any
// state.
func (s *Service) plan(ctx context.Context, rt jobs.Runtime, self domain.SourceID) (bool, error) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if err := s.insertRows(ctx, rt); err != nil {
		return false, err
	}
	var changes []change
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, groupQuery("", "", ` ORDER BY 1`))
		if err != nil {
			return err
		}
		defer rows.Close()
		var group []copyRow
		for rows.Next() {
			r, err := scanCopy(rows)
			if err != nil {
				return err
			}
			if len(group) > 0 && group[0].size != r.size {
				changes = decide(group, changes)
				group = group[:0]
			}
			group = append(group, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		changes = decide(group, changes)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("content: plan the size groups: %w", err)
	}
	changed := len(changes) > 0
	for {
		batch := changes[:min(len(changes), planBatch)]
		changes = changes[len(batch):]
		last := len(changes) == 0
		err = s.runner.Write(ctx, func(tx *jobs.Tx) error {
			wake := map[string]bool{}
			if err := applyChanges(ctx, tx.SQL(), batch, nil, wake); err != nil {
				return err
			}
			if err := enqueueOthers(ctx, tx, wake, self); err != nil {
				return err
			}
			if last {
				return recomputeCoverage(ctx, tx.SQL(), s.now())
			}
			return nil
		})
		if err != nil {
			return false, fmt.Errorf("content: write the plan: %w", err)
		}
		if last {
			break
		}
		if err := rt.Yield(ctx); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// insertRows creates a pending file_content row for every present
// non-empty file without one, planSpan entry IDs per transaction. A file in
// a quarantine is never enrolled (r4 design D2).
func (s *Service) insertRows(ctx context.Context, rt jobs.Runtime) error {
	var top int64
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM entries`).Scan(&top); err != nil {
		return err
	}
	for from := int64(0); from < top; from += planSpan {
		var n int64
		err := s.st.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `INSERT INTO file_content (entry_id, source_id, state, size)
				SELECT e.id, e.source_id, 'pending', e.size FROM entries e
				WHERE e.id > ? AND e.id <= ? AND e.kind = 'file' AND e.state = 'present' AND e.size > 0
					AND `+notQuarantinedE+`
					AND NOT EXISTS (SELECT 1 FROM file_content f WHERE f.entry_id = e.id)`, from, from+planSpan)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		if err != nil {
			return fmt.Errorf("content: create file_content rows: %w", err)
		}
		if n > 0 {
			if err := rt.Yield(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyChanges writes state changes, each guarded by the state it was
// decided from; with d set it records their coverage deltas. wake collects
// the sources with a row turned pending.
func applyChanges(ctx context.Context, tx *sql.Tx, cs []change, d deltas, wake map[string]bool) error {
	for _, c := range cs {
		table, key := "file_content", "entry_id"
		if c.row.member {
			table, key = "archive_members", "id"
		}
		res, err := tx.ExecContext(ctx, `UPDATE `+table+` SET state = ? WHERE `+key+` = ? AND state = ?`,
			string(c.to), c.row.id, string(c.row.state))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if d != nil {
			d.move(c.row.source, c.row.state, c.to, c.row.size)
		}
		if c.to == domain.ContentPending {
			wake[c.row.source] = true
		}
	}
	return nil
}

// enqueueOthers enqueues the hashing of every online source in wake but
// self.
func enqueueOthers(ctx context.Context, tx *jobs.Tx, wake map[string]bool, self domain.SourceID) error {
	for src := range wake {
		if src == string(self) {
			continue
		}
		var state string
		err := tx.SQL().QueryRowContext(ctx, `SELECT state FROM sources WHERE id = ?`, src).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) || state != "online" {
			continue
		}
		if err != nil {
			return err
		}
		if _, _, err := tx.EnqueueOnce(hashSpec(domain.SourceID(src))); err != nil {
			return err
		}
	}
	return nil
}

// settle decides the size group of size again inside a commit (design D3),
// after samples were stored: it records coverage deltas in d and enqueues
// other sources whose rows went back to pending.
func settle(ctx context.Context, tx *jobs.Tx, size int64, d deltas, self domain.SourceID) error {
	rows, err := tx.SQL().QueryContext(ctx, groupQuery(` AND f.source_id IN (SELECT id FROM sources)
		AND f.state IN ('pending', 'sampled', 'hashed', 'changed', 'unreadable', 'unique_size') AND f.size = ?1`,
		` AND m.size = ?1`, ""), size)
	if err != nil {
		return err
	}
	var g []copyRow
	for rows.Next() {
		r, err := scanCopy(rows)
		if err != nil {
			rows.Close()
			return err
		}
		g = append(g, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	wake := map[string]bool{}
	if err := applyChanges(ctx, tx.SQL(), decide(g, nil), d, wake); err != nil {
		return err
	}
	return enqueueOthers(ctx, tx, wake, self)
}
