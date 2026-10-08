package decisions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/rules"
)

// Audit event kinds of the owner's classification (r2b design D5).
const (
	AuditCategorySet = "category_set"
	AuditGroupSet    = "group_set"
)

// SetCategory is one set-category request: exactly one of EntryID,
// EntryIDs, and SelectionID names the targets, files or folders. Category is
// the owner's category, or "" to return the targets' category to the rules.
type SetCategory struct {
	EntryID     domain.EntryID
	EntryIDs    []domain.EntryID
	SelectionID string
	Category    domain.Category
}

// Validate checks the request's shape: one target form, at most MaxEntryIDs
// IDs, and a category of the fixed list or "". Failures are invalid_request.
func (r SetCategory) Validate() error {
	if err := validTargets(r.EntryID != 0, r.EntryIDs, r.SelectionID); err != nil {
		return err
	}
	if r.Category == "" {
		return nil
	}
	_, err := domain.ParseCategory(string(r.Category))
	return err
}

// SetGroup is one set-group request: targets as for SetCategory, folders
// only. Group marks (true) or unmarks (false) them as groups; nil returns
// their group flag to the rules.
type SetGroup struct {
	EntryID     domain.EntryID
	EntryIDs    []domain.EntryID
	SelectionID string
	Group       *bool
}

// Validate checks the request's targets; failures are invalid_request.
func (r SetGroup) Validate() error {
	return validTargets(r.EntryID != 0, r.EntryIDs, r.SelectionID)
}

// OwnerResult reports a SetCategory or SetGroup: the targets it applied to,
// and the scan of their source that it started or joined so that folder
// figures follow, nil when every target's source is offline.
type OwnerResult struct {
	Applied int
	Scan    *jobs.Accepted
}

// ownerField is the entry_overrides column one command sets.
type ownerField struct {
	column, other string
	// kinds lists the entry kinds a target may have; what words the others.
	kinds string
	what  string
	// oldName is the SQL expression naming a target's override o before the
	// change, as its audit event records it.
	oldName string
	audit   string
}

var (
	categoryField = ownerField{column: "category", other: "group_mark", kinds: `'file', 'directory'`,
		what: "only files and folders have a category", oldName: `coalesce(o.category, 'rules')`, audit: AuditCategorySet}
	groupField = ownerField{column: "group_mark", other: "category", kinds: `'directory'`,
		what:    "only a folder can be a group",
		oldName: `CASE o.group_mark WHEN 1 THEN 'true' WHEN 0 THEN 'false' ELSE 'rules' END`, audit: AuditGroupSet}
)

// SetCategory sets or clears the owner's category of the targets of req
// (r2b design D3, D5), in tx: it writes their overrides and their own
// effective classification, and starts a scan of their sources, which
// brings the folders above them up to date. An unknown target is not_found,
// an expired selection selection_expired, and a target that is neither a
// file nor a folder invalid_request, before anything changes.
func (s *Service) SetCategory(ctx context.Context, tx *jobs.Tx, req SetCategory) (OwnerResult, error) {
	if err := req.Validate(); err != nil {
		return OwnerResult{}, err
	}
	var value, name any = nil, "rules"
	if req.Category != "" {
		value, name = string(req.Category), string(req.Category)
	}
	return s.setOwner(ctx, tx, req.EntryID, req.EntryIDs, req.SelectionID, categoryField, value, name)
}

// SetGroup marks, unmarks, or returns to the rules the group flag of the
// targets of req, as SetCategory does for the category. A target that is not
// a folder is invalid_request.
func (s *Service) SetGroup(ctx context.Context, tx *jobs.Tx, req SetGroup) (OwnerResult, error) {
	if err := req.Validate(); err != nil {
		return OwnerResult{}, err
	}
	var value, name any = nil, "rules"
	if req.Group != nil {
		value, name = boolInt(*req.Group), *req.Group
	}
	return s.setOwner(ctx, tx, req.EntryID, req.EntryIDs, req.SelectionID, groupField, value, name)
}

// setOwner sets column f of the targets' overrides to value, nil returning
// it to the rules: a row with neither value set is deleted (design D1).
func (s *Service) setOwner(ctx context.Context, jtx *jobs.Tx, one domain.EntryID, ids []domain.EntryID, selection string,
	f ownerField, value, name any) (OwnerResult, error) {
	tx := jtx.SQL()
	now := s.clk.Now()
	if one != 0 {
		ids = []domain.EntryID{one}
	}
	t, err := resolveTargets(ctx, tx, now, ids, selection)
	if err != nil {
		return OwnerResult{}, err
	}
	var (
		bad  domain.EntryID
		kind string
	)
	err = tx.QueryRowContext(ctx, `SELECT id, CASE kind WHEN 'special' THEN special_kind ELSE kind END FROM entries
		WHERE id IN (`+t.sub+`) AND kind NOT IN (`+f.kinds+`) ORDER BY id LIMIT 1`, t.args...).Scan(&bad, &kind)
	switch {
	case err == nil:
		return OwnerResult{}, domain.Errorf(domain.CodeInvalidRequest, "entry %s is a %s: %s", bad, kind, f.what)
	case !errors.Is(err, sql.ErrNoRows):
		return OwnerResult{}, fmt.Errorf("decisions: check target kinds: %w", err)
	}
	if err := frozen(ctx, tx, t); err != nil {
		return OwnerResult{}, err
	}
	old, err := overrideCounts(ctx, tx, t, f)
	if err != nil {
		return OwnerResult{}, err
	}
	if err := markStale(ctx, tx, t, ""); err != nil {
		return OwnerResult{}, err
	}

	at := clock.Millis(now)
	if value == nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM entry_overrides WHERE entry_id IN (`+t.sub+`) AND `+f.other+` IS NULL`,
			t.args...); err != nil {
			return OwnerResult{}, fmt.Errorf("decisions: delete overrides: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entry_overrides SET `+f.column+` = NULL, updated_at = ?
			WHERE entry_id IN (`+t.sub+`) AND `+f.column+` IS NOT NULL`, append([]any{at}, t.args...)...); err != nil {
			return OwnerResult{}, fmt.Errorf("decisions: clear overrides: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `INSERT INTO entry_overrides (entry_id, `+f.column+`, updated_at)
			SELECT id, ?, ? FROM (`+t.sub+`) WHERE true
			ON CONFLICT (entry_id) DO UPDATE SET `+f.column+` = excluded.`+f.column+`, updated_at = excluded.updated_at
			WHERE `+f.column+` IS NOT excluded.`+f.column, append([]any{value, at}, t.args...)...); err != nil {
			return OwnerResult{}, fmt.Errorf("decisions: write overrides: %w", err)
		}
	}
	sources, err := s.writeThrough(ctx, tx, t)
	if err != nil {
		return OwnerResult{}, err
	}
	res := OwnerResult{Applied: t.n}
	if res.Scan, err = s.startScans(ctx, jtx, sources); err != nil {
		return OwnerResult{}, err
	}

	var detail map[string]any
	if one != 0 {
		var path []byte
		if err := tx.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ?`, int64(one)).Scan(&path); err != nil {
			return OwnerResult{}, fmt.Errorf("decisions: read entry: %w", err)
		}
		detail = map[string]any{"entry_id": one.String(), "path": domain.DisplayName(path)}
		for k := range old {
			detail["old"] = requestForm(k)
		}
	} else {
		detail = t.audit()
		detail["old"] = old
	}
	detail["new"] = name
	detail["applied"] = t.n
	if err := s.audit(ctx, tx, now, f.audit, detail); err != nil {
		return OwnerResult{}, err
	}
	return res, nil
}

// requestForm is an audit counts key as the request names the value: a
// group mark as a JSON boolean, anything else as its name.
func requestForm(k string) any {
	switch k {
	case "true":
		return true
	case "false":
		return false
	}
	return k
}

// overrideCounts counts the targets by the value of f before the change,
// for the audit event.
func overrideCounts(ctx context.Context, tx *sql.Tx, t targets, f ownerField) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+f.oldName+`, count(*) FROM entries e
		LEFT JOIN entry_overrides o ON o.entry_id = e.id WHERE e.id IN (`+t.sub+`) GROUP BY 1`, t.args...)
	if err != nil {
		return nil, fmt.Errorf("decisions: read overrides: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var (
			v string
			n int
		)
		if err := rows.Scan(&v, &n); err != nil {
			return nil, fmt.Errorf("decisions: read overrides: %w", err)
		}
		out[v] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions: read overrides: %w", err)
	}
	return out, nil
}

// classified is a target's stored classification and what it is derived
// from.
type classified struct {
	id                       domain.EntryID
	folder, indicated        bool
	ruleIDs                  sql.NullString
	category, family, triage sql.NullString
	group, veto              bool
	owner                    domain.Override
}

// writeThrough writes the targets' own effective classification (category,
// family, triage, group flag, veto) as a scan with their overrides would:
// the rules' result rebuilt from the stored rule IDs and the indicators
// below each folder, with the overrides applied (r2b design D2). It returns
// the targets' sources, sorted. The folders above the targets follow at the
// scan the caller starts.
func (s *Service) writeThrough(ctx context.Context, tx *sql.Tx, t targets) ([]domain.SourceID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.source_id, e.kind = 'directory',
		coalesce(d.indicators, '[]') <> '[]', e.rule_ids, e.category, e.family, e.triage, e.is_group, e.veto,
		o.category, o.group_mark
		FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id LEFT JOIN entry_overrides o ON o.entry_id = e.id
		WHERE e.id IN (`+t.sub+`)`, t.args...)
	if err != nil {
		return nil, fmt.Errorf("decisions: read classifications: %w", err)
	}
	var (
		list    []classified
		sources []domain.SourceID
	)
	for rows.Next() {
		var (
			c        classified
			src      domain.SourceID
			category sql.NullString
			mark     sql.NullBool
		)
		if err := rows.Scan(&c.id, &src, &c.folder, &c.indicated, &c.ruleIDs, &c.category, &c.family, &c.triage,
			&c.group, &c.veto, &category, &mark); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decisions: read classifications: %w", err)
		}
		c.owner.Category = domain.Category(category.String)
		if mark.Valid {
			c.owner.Group = &mark.Bool
		}
		list = append(list, c)
		if !slices.Contains(sources, src) {
			sources = append(sources, src)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions: read classifications: %w", err)
	}
	for _, c := range list {
		var ids []string
		if c.ruleIDs.Valid {
			if err := json.Unmarshal([]byte(c.ruleIDs.String), &ids); err != nil {
				return nil, fmt.Errorf("decisions: entry %s rule IDs: %w", c.id, err)
			}
		}
		res := rules.ApplyOwner(s.pol.Recall(ids, c.folder, c.indicated), c.owner)
		if c.category.String == string(res.Category) && c.family.String == string(res.Family) &&
			c.triage.String == string(res.Triage) && c.group == res.Group && c.veto == res.Veto {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entries SET category = ?, family = ?, triage = ?, is_group = ?, veto = ?
			WHERE id = ?`, nullText(string(res.Category)), nullText(string(res.Family)), nullText(string(res.Triage)),
			boolInt(res.Group), boolInt(res.Veto), int64(c.id)); err != nil {
			return nil, fmt.Errorf("decisions: write classification: %w", err)
		}
	}
	slices.Sort(sources)
	return sources, nil
}

// startScans starts or joins a scan of each online source, so that its
// folder figures follow the overrides (design D3), and returns the first
// one, or nil when every source is offline: those keep the overrides, which
// their next scan applies. A scan already running may have read the
// overrides before them: rescan_requested makes it run once more.
func (s *Service) startScans(ctx context.Context, tx *jobs.Tx, sources []domain.SourceID) (*jobs.Accepted, error) {
	var first *jobs.Accepted
	for _, src := range sources {
		acc, err := s.scans(ctx, tx, src)
		if domain.CodeOf(err) == domain.CodeSourceOffline {
			continue
		}
		if err != nil {
			return nil, err
		}
		if acc.Coalesced && acc.State != domain.JobQueued {
			if _, err := tx.SQL().ExecContext(ctx, `UPDATE sources SET rescan_requested = 1 WHERE id = ?`, string(src)); err != nil {
				return nil, fmt.Errorf("decisions: request a rescan: %w", err)
			}
		}
		if first == nil {
			first = &acc
		}
	}
	return first, nil
}

// nullText is a nullable text column's value: "" is NULL.
func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
