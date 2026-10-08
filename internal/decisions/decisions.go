// Package decisions holds the owner's intent on indexed entries (§6.7, §6.9,
// design D10): own and effective decisions, tags, the owner's category
// overrides and group marks, and the selections that bulk requests name.
//
// # Decisions
//
// An entry's own decision (entries.decision) is NULL (inherit) or one of
// undecided, keep, discard, and later. Its effective decision
// (entries.eff_decision) is stored with eff_from, the entry whose own
// decision applies: the entry itself when it has one, otherwise its nearest
// ancestor with one, otherwise NULL with the default undecided.
//
// SetDecision changes own decisions and keeps every stored effective decision
// true in the same transaction. Below each target folder it rewrites the
// subtree as path ranges on the (source_id, path) index, the descendants of a
// folder at path p being the byte range [p+"/", p+"0"). The ranges skip the
// subtrees of descendant folders that have their own decision (the cut
// points), so a decision never reaches past a nearer one. The statements are
// set-based: no Go loop runs per entry of a subtree.
//
// An individual request (one entry_id) applies to that entry whatever its
// effective decision. A bulk request (entry_ids or a selection) with any value
// other than keep skips every target whose effective decision is keep,
// explicit or inherited, as read inside the writing transaction, and reports
// them (I5).
//
// # New entries
//
// A row a scan inserts takes its effective decision from its parent row, read
// in the scan's writing transaction: InheritFrom. The child's eff_decision and
// eff_from are the parent's eff_decision and eff_from (the parent's eff_from is
// the parent itself when it has its own decision). A decision that commits
// after the batch covers the inserted rows, because its subtree update reads
// the committed index.
//
// # Tags
//
// Own tags are entry_tags rows. Effective tags are an entry's own tags plus
// those of its ancestors, found by walking parent_id when an entry is read
// (Effective); they are never materialized. Removing a tag removes own tags
// only.
//
// # Category overrides and group marks
//
// SetCategory and SetGroup (r2b design D3, D5) write entry_overrides rows,
// one per overridden entry, deleted once both values are back to the rules.
// In the same transaction they write each target's own effective category,
// family, triage, group flag, and veto, as rules.ApplyOwner gives them over
// the rules' result rebuilt from the stored rule IDs, and start a scan of
// each online source of the targets (index.StartScan), which brings the
// compositions and inside lists above them up to date. When that source's
// scan is already running, it may have read the overrides before them:
// sources.rescan_requested makes it run once more. An offline source keeps
// the overrides for its next scan. Archive members, which are classified
// with their archive, are no targets.
//
// # Audit
//
// Each accepted request writes one audit event in its transaction:
// decision_set, tags_set, tag_created, tag_renamed, tag_deleted,
// category_set, or group_set, with the client address from the request
// context. A rejected request writes none.
package decisions

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/store"
	"precious/internal/web/clientip"
)

// Audit event kinds written by this package.
const (
	AuditDecisionSet = "decision_set"
	AuditTagsSet     = "tags_set"
	AuditTagCreated  = "tag_created"
	AuditTagRenamed  = "tag_renamed"
	AuditTagDeleted  = "tag_deleted"
)

// MaxEntryIDs bounds the entry_ids of one bulk request.
const MaxEntryIDs = 1000

// MaxSkippedListed bounds the skipped entries a bulk response lists; the
// count covers them all.
const MaxSkippedListed = 100

// Service applies the owner's decisions, tags, selections, and
// classification overrides. Its methods run inside the caller's writing
// transaction.
type Service struct {
	clk   clock.Clock
	pol   *rules.Policy
	scans ScanStarter
}

// ScanStarter starts a scan of a source in tx, or returns its active scan;
// a source that is not online is source_offline. serve passes
// index.StartScan, which this package cannot import: the scanner's tests
// use it.
type ScanStarter func(ctx context.Context, tx *jobs.Tx, src domain.SourceID) (jobs.Accepted, error)

// New returns the service. clk dates decisions, tags, selections,
// overrides, and audit events; pol is the policy the scans classify with,
// from which an override's write-through derives what the rules give; and
// scans starts the scan an override needs.
func New(clk clock.Clock, pol *rules.Policy, scans ScanStarter) *Service {
	return &Service{clk: clk, pol: pol, scans: scans}
}

// Mode tells an individual request from a bulk one.
type Mode int

const (
	// Individual names one entry (entry_id) and applies to it whatever its
	// effective decision.
	Individual Mode = iota
	// Bulk names entries (entry_ids) or a selection and never changes a keep
	// unless the value is keep.
	Bulk
)

// SetDecision is one set-decision request. Exactly one of EntryID, EntryIDs,
// and SelectionID names the targets. Decision is the new own decision, or ""
// with Inherit to clear it.
type SetDecision struct {
	Decision    domain.Decision
	Inherit     bool
	EntryID     domain.EntryID
	EntryIDs    []domain.EntryID
	SelectionID string
}

// Mode is Individual for a request naming EntryID, Bulk otherwise.
func (r SetDecision) Mode() Mode {
	if r.EntryID != 0 {
		return Individual
	}
	return Bulk
}

// Validate checks the request's shape: one target form, at most MaxEntryIDs
// IDs, and either a known decision or Inherit. Failures are invalid_request.
func (r SetDecision) Validate() error {
	if err := validTargets(r.EntryID != 0, r.EntryIDs, r.SelectionID); err != nil {
		return err
	}
	if r.Inherit {
		if r.Decision != "" {
			return domain.Errorf(domain.CodeInvalidRequest, "a request either sets a decision or inherits")
		}
		return nil
	}
	_, err := domain.ParseDecision(string(r.Decision))
	return err
}

// validTargets checks that a request names its targets in exactly one form.
func validTargets(individual bool, ids []domain.EntryID, selection string) error {
	forms := 0
	for _, set := range []bool{individual, len(ids) > 0, selection != ""} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return domain.Errorf(domain.CodeInvalidRequest, "name the targets with exactly one of entry_id, entry_ids, and selection_id")
	}
	if len(ids) > MaxEntryIDs {
		return domain.Errorf(domain.CodeInvalidRequest, "at most %d entry_ids per request, got %d", MaxEntryIDs, len(ids))
	}
	return nil
}

// Skipped is a bulk target left unchanged because it is kept.
type Skipped struct {
	EntryID domain.EntryID
	Path    string // display form (domain.DisplayName)
	PathB64 []byte // raw path
}

// SetDecisionResult reports a SetDecision. Skipped lists at most
// MaxSkippedListed of the SkippedCount entries, in path order; it is empty
// for an individual request.
type SetDecisionResult struct {
	Applied      int
	SkippedCount int
	Skipped      []Skipped
}

// SetDecision applies req in tx. An unknown target fails the whole request
// with not_found and an expired selection with selection_expired, before
// anything changes.
func (s *Service) SetDecision(ctx context.Context, tx *sql.Tx, req SetDecision) (SetDecisionResult, error) {
	if err := req.Validate(); err != nil {
		return SetDecisionResult{}, err
	}
	now := s.clk.Now()
	ids := req.EntryIDs
	if req.EntryID != 0 {
		ids = []domain.EntryID{req.EntryID}
	}
	t, err := resolveTargets(ctx, tx, now, ids, req.SelectionID)
	if err != nil {
		return SetDecisionResult{}, err
	}
	if err := frozen(ctx, tx, t); err != nil {
		return SetDecisionResult{}, err
	}

	// guard selects the targets the request applies to. Every statement
	// below that reads it runs before any effective decision of a target
	// can become keep, so it selects the same entries each time.
	guard := ""
	if req.Mode() == Bulk && req.Decision != domain.DecisionKeep {
		guard = ` AND eff_decision <> 'keep'`
	}
	res := SetDecisionResult{Skipped: []Skipped{}}
	if guard != "" {
		if res.SkippedCount, res.Skipped, err = skippedTargets(ctx, tx, t); err != nil {
			return SetDecisionResult{}, err
		}
	}
	res.Applied = t.n - res.SkippedCount
	old, err := ownCounts(ctx, tx, t, guard)
	if err != nil {
		return SetDecisionResult{}, err
	}
	if err := markStale(ctx, tx, t, guard); err != nil {
		return SetDecisionResult{}, err
	}

	if err := applyOwn(ctx, tx, t, guard, req.Decision, now); err != nil {
		return SetDecisionResult{}, err
	}
	dirs, err := targetFolders(ctx, tx, t, guard)
	if err != nil {
		return SetDecisionResult{}, err
	}
	for _, d := range dirs {
		eff, from := req.Decision, &d.id
		if req.Inherit {
			if eff, from, err = parentEffective(ctx, tx, d.parent); err != nil {
				return SetDecisionResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE entries SET eff_decision = ?, eff_from = ? WHERE id = ?`,
				string(eff), nullID(from), int64(d.id)); err != nil {
				return SetDecisionResult{}, fmt.Errorf("decisions: set effective decision: %w", err)
			}
		}
		if err := propagate(ctx, tx, d.source, d.path, eff, from); err != nil {
			return SetDecisionResult{}, err
		}
	}

	var detail map[string]any
	if req.Mode() == Individual {
		var path []byte
		if err := tx.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ?`, int64(req.EntryID)).Scan(&path); err != nil {
			return SetDecisionResult{}, fmt.Errorf("decisions: read entry: %w", err)
		}
		detail = map[string]any{"entry_id": req.EntryID.String(), "path": domain.DisplayName(path)}
		for k := range old {
			detail["old"] = k
		}
	} else {
		detail = t.audit()
		detail["old"] = old
	}
	detail["new"] = decisionName(req.Decision)
	detail["applied"] = res.Applied
	detail["skipped"] = res.SkippedCount
	if err := s.audit(ctx, tx, now, AuditDecisionSet, detail); err != nil {
		return SetDecisionResult{}, err
	}
	return res, nil
}

// decisionName is the request form of an own decision: "inherit" for none.
func decisionName(d domain.Decision) string {
	if d == "" {
		return "inherit"
	}
	return string(d)
}

// skippedTargets counts the kept targets and lists the first
// MaxSkippedListed of them in path order.
func skippedTargets(ctx context.Context, tx *sql.Tx, t targets) (int, []Skipped, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE id IN (`+t.sub+`) AND eff_decision = 'keep'`,
		t.args...).Scan(&n); err != nil {
		return 0, nil, fmt.Errorf("decisions: count kept targets: %w", err)
	}
	list := []Skipped{}
	if n == 0 {
		return 0, list, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, path FROM entries WHERE id IN (`+t.sub+`) AND eff_decision = 'keep'
		ORDER BY source_id, path LIMIT `+strconv.Itoa(MaxSkippedListed), t.args...)
	if err != nil {
		return 0, nil, fmt.Errorf("decisions: list kept targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			path []byte
		)
		if err := rows.Scan(&id, &path); err != nil {
			return 0, nil, fmt.Errorf("decisions: list kept targets: %w", err)
		}
		list = append(list, Skipped{EntryID: domain.EntryID(id), Path: domain.DisplayName(path), PathB64: nonNil(path)})
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("decisions: list kept targets: %w", err)
	}
	return n, list, nil
}

// ownCounts counts the applied targets by their own decision before the
// change ("inherit" for none), for the audit event.
func ownCounts(ctx context.Context, tx *sql.Tx, t targets, guard string) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT coalesce(decision, 'inherit'), count(*) FROM entries
		WHERE id IN (`+t.sub+`)`+guard+` GROUP BY 1`, t.args...)
	if err != nil {
		return nil, fmt.Errorf("decisions: read own decisions: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var (
			d string
			n int
		)
		if err := rows.Scan(&d, &n); err != nil {
			return nil, fmt.Errorf("decisions: read own decisions: %w", err)
		}
		out[d] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions: read own decisions: %w", err)
	}
	return out, nil
}

// applyOwn writes the own decision of the applied targets. A decision is
// also each target's effective decision. Inherit clears it, and a target
// that is not a folder then takes its parent's effective decision; a folder
// target is finished by the caller, ancestors first.
//
// A leaf's parent may itself be below an inherit folder target the caller
// has not finished yet; that folder's subtree update then rewrites the leaf,
// whose own decision is now NULL.
func applyOwn(ctx context.Context, tx *sql.Tx, t targets, guard string, d domain.Decision, now time.Time) error {
	var err error
	if d == "" {
		_, err = tx.ExecContext(ctx, `UPDATE entries SET decision = NULL, decision_at = NULL
			WHERE id IN (`+t.sub+`)`+guard, t.args...)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE entries SET (eff_decision, eff_from) =
				(SELECT p.eff_decision, p.eff_from FROM entries p WHERE p.id = entries.parent_id)
				WHERE id IN (`+t.sub+`) AND kind <> 'directory'`+guard, t.args...)
		}
	} else {
		args := append([]any{string(d), clock.Millis(now), string(d)}, t.args...)
		_, err = tx.ExecContext(ctx, `UPDATE entries SET decision = ?, decision_at = ?, eff_decision = ?, eff_from = id
			WHERE id IN (`+t.sub+`)`+guard, args...)
	}
	if err != nil {
		return fmt.Errorf("decisions: set own decisions: %w", err)
	}
	return nil
}

// folder is a folder target.
type folder struct {
	id     domain.EntryID
	source domain.SourceID
	path   []byte
	parent sql.NullInt64
}

// targetFolders returns the applied folder targets, ancestors before their
// descendants.
func targetFolders(ctx context.Context, tx *sql.Tx, t targets, guard string) ([]folder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, source_id, path, parent_id FROM entries
		WHERE id IN (`+t.sub+`) AND kind = 'directory'`+guard+` ORDER BY source_id, path`, t.args...)
	if err != nil {
		return nil, fmt.Errorf("decisions: read folder targets: %w", err)
	}
	defer rows.Close()
	var out []folder
	for rows.Next() {
		var (
			f   folder
			id  int64
			src string
		)
		if err := rows.Scan(&id, &src, &f.path, &f.parent); err != nil {
			return nil, fmt.Errorf("decisions: read folder targets: %w", err)
		}
		f.id, f.source, f.path = domain.EntryID(id), domain.SourceID(src), nonNil(f.path)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions: read folder targets: %w", err)
	}
	return out, nil
}

// parentEffective is what an entry without an own decision inherits from
// parent: the parent's effective decision and its origin, or the default for
// the root (no parent).
func parentEffective(ctx context.Context, q store.Queryer, parent sql.NullInt64) (domain.Decision, *domain.EntryID, error) {
	if !parent.Valid {
		return domain.DecisionUndecided, nil, nil
	}
	var (
		eff  string
		from sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT eff_decision, eff_from FROM entries WHERE id = ?`, parent.Int64).Scan(&eff, &from)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, domain.Errorf(domain.CodeNotFound, "entry %d not found", parent.Int64)
	}
	if err != nil {
		return "", nil, fmt.Errorf("decisions: read parent decision: %w", err)
	}
	return domain.Decision(eff), idPtr(from), nil
}

// InheritFrom returns the effective decision and its origin that a new child
// of parent takes: the parent's eff_decision and eff_from. A scan calls it in
// its writing transaction, once per folder whose children it inserts, and
// writes the pair into each new row (design D10, transaction boundaries). An
// unknown parent is not_found.
func InheritFrom(ctx context.Context, tx *sql.Tx, parent domain.EntryID) (eff domain.Decision, from *domain.EntryID, err error) {
	return parentEffective(ctx, tx, sql.NullInt64{Int64: int64(parent), Valid: true})
}

// pathRange is a half-open byte range of paths; a nil hi is unbounded.
type pathRange struct{ lo, hi []byte }

// descendants is the range of the paths below path p; for the root (p
// empty) every other path of the source.
func descendants(p []byte) pathRange {
	if len(p) == 0 {
		return pathRange{lo: []byte{0}} // every non-empty path
	}
	return pathRange{lo: append(p[:len(p):len(p)], '/'), hi: append(p[:len(p):len(p)], '0')}
}

// propagate sets eff and from on every descendant of the folder at path that
// has no own decision and is not below a descendant folder with one.
//
// The cut points are the descendant folders with their own decision. Their
// subtrees are ranges that are either nested or disjoint; sorted by start,
// the outermost ones leave gaps, and each gap is one range update.
func propagate(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte, eff domain.Decision, from *domain.EntryID) error {
	all := descendants(path)
	cuts, err := cutPoints(ctx, tx, src, all)
	if err != nil {
		return err
	}
	cursor := all.lo
	for _, c := range cuts {
		if bytes.Compare(c.lo, cursor) < 0 {
			continue // inside a cut already skipped
		}
		if err := setRange(ctx, tx, src, pathRange{lo: cursor, hi: c.lo}, eff, from); err != nil {
			return err
		}
		cursor = c.hi
	}
	return setRange(ctx, tx, src, pathRange{lo: cursor, hi: all.hi}, eff, from)
}

// cutPoints returns the subtree ranges of the folders inside r that have
// their own decision, sorted by start.
func cutPoints(ctx context.Context, tx *sql.Tx, src domain.SourceID, r pathRange) ([]pathRange, error) {
	query, args := cutPointsSQL(src, r)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("decisions: find own decisions below a folder: %w", err)
	}
	defer rows.Close()
	var cuts []pathRange
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("decisions: find own decisions below a folder: %w", err)
		}
		cuts = append(cuts, descendants(p))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions: find own decisions below a folder: %w", err)
	}
	slices.SortFunc(cuts, func(a, b pathRange) int { return bytes.Compare(a.lo, b.lo) })
	return cuts, nil
}

func cutPointsSQL(src domain.SourceID, r pathRange) (string, []any) {
	where, args := rangeWhere(src, r)
	return `SELECT path FROM entries WHERE ` + where + ` AND decision IS NOT NULL AND kind = 'directory'`, args
}

// setRange sets eff and from on the entries of r without an own decision,
// writing only rows that change.
func setRange(ctx context.Context, tx *sql.Tx, src domain.SourceID, r pathRange, eff domain.Decision, from *domain.EntryID) error {
	if r.hi != nil && bytes.Compare(r.lo, r.hi) >= 0 {
		return nil
	}
	query, args := setRangeSQL(src, r, eff, from)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("decisions: update a subtree: %w", err)
	}
	return nil
}

func setRangeSQL(src domain.SourceID, r pathRange, eff domain.Decision, from *domain.EntryID) (string, []any) {
	where, args := rangeWhere(src, r)
	args = append([]any{string(eff), nullID(from)}, args...)
	return `UPDATE entries SET eff_decision = ?, eff_from = ? WHERE ` + where +
		` AND decision IS NULL AND (eff_decision <> ? OR eff_from IS NOT ?)`, append(args, string(eff), nullID(from))
}

// rangeWhere is the condition selecting the entries of source src in r, on
// the (source_id, path) index.
func rangeWhere(src domain.SourceID, r pathRange) (string, []any) {
	if r.hi == nil {
		return `source_id = ? AND path >= ?`, []any{string(src), r.lo}
	}
	return `source_id = ? AND path >= ? AND path < ?`, []any{string(src), r.lo, r.hi}
}

// Ref names an entry for display.
type Ref struct {
	ID      domain.EntryID
	Path    string // display form (domain.DisplayName)
	PathB64 []byte // raw path
}

// TagRef is one effective tag of an entry: Own, or inherited From the
// nearest ancestor carrying it.
type TagRef struct {
	ID   int64
	Name string
	Own  bool
	From *Ref
}

// Intent is an entry's own decision ("" for inherit), its effective decision,
// the entry that decision comes from (nil for the default undecided), and its
// effective tags by name.
type Intent struct {
	Own       domain.Decision
	Effective domain.Decision
	From      *Ref
	Tags      []TagRef
}

// Effective reads the intent of entry id; an unknown entry is not_found.
func Effective(ctx context.Context, q store.Queryer, id domain.EntryID) (Intent, error) {
	var (
		own      sql.NullString
		eff      string
		from     sql.NullInt64
		fromPath []byte
	)
	err := q.QueryRowContext(ctx, `SELECT e.decision, e.eff_decision, e.eff_from, f.path
		FROM entries e LEFT JOIN entries f ON f.id = e.eff_from WHERE e.id = ?`, int64(id)).Scan(&own, &eff, &from, &fromPath)
	if errors.Is(err, sql.ErrNoRows) {
		return Intent{}, domain.Errorf(domain.CodeNotFound, "entry %s not found", id)
	}
	if err != nil {
		return Intent{}, fmt.Errorf("decisions: read intent: %w", err)
	}
	in := Intent{Own: domain.Decision(own.String), Effective: domain.Decision(eff)}
	if from.Valid {
		in.From = &Ref{ID: domain.EntryID(from.Int64), Path: domain.DisplayName(fromPath), PathB64: nonNil(fromPath)}
	}
	if in.Tags, err = effectiveTags(ctx, q, id); err != nil {
		return Intent{}, err
	}
	return in, nil
}

// Total is the files and bytes of one effective decision.
type Total struct{ Files, Bytes int64 }

// Totals sums the present files of source src by effective decision, with
// every decision present (design D10, Home). The quarantine is left out
// (r4 design D2, D15): Home shows its bytes apart.
func Totals(ctx context.Context, q store.Queryer, src domain.SourceID) (map[domain.Decision]Total, error) {
	rows, err := q.QueryContext(ctx, totalsSQL, string(src))
	if err != nil {
		return nil, fmt.Errorf("decisions: totals: %w", err)
	}
	defer rows.Close()
	out := make(map[domain.Decision]Total, len(domain.Decisions))
	for _, d := range domain.Decisions {
		out[d] = Total{}
	}
	for rows.Next() {
		var (
			d string
			t Total
		)
		if err := rows.Scan(&d, &t.Files, &t.Bytes); err != nil {
			return nil, fmt.Errorf("decisions: totals: %w", err)
		}
		out[domain.Decision(d)] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions: totals: %w", err)
	}
	return out, nil
}

var totalsSQL = `SELECT e.eff_decision, count(*), coalesce(sum(e.size), 0) FROM entries e
	WHERE e.source_id = ? AND e.kind = 'file' AND e.state = 'present' AND ` + notQuarantined("e") + `
	GROUP BY e.eff_decision`

// targets is the set of entries a request names: sub is a subquery with one
// column, id, over args, naming n distinct existing entries.
type targets struct {
	sub       string
	args      []any
	n         int
	ids       []domain.EntryID // sorted, for entry_ids requests
	selection string
}

// resolveTargets checks that every named entry exists (not_found otherwise)
// and that a selection has not expired (selection_expired), and returns the
// targets. ids may repeat.
func resolveTargets(ctx context.Context, tx *sql.Tx, now time.Time, ids []domain.EntryID, selection string) (targets, error) {
	if selection != "" {
		return selectionTargets(ctx, tx, now, selection)
	}
	sorted := slices.Compact(slices.Sorted(slices.Values(ids)))
	t := targets{sub: `SELECT value AS id FROM json_each(?)`, args: []any{idArray(sorted)}, n: len(sorted), ids: sorted}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE id IN (`+t.sub+`)`, t.args...).Scan(&n); err != nil {
		return targets{}, fmt.Errorf("decisions: check entries: %w", err)
	}
	if n != t.n {
		var missing int64
		if err := tx.QueryRowContext(ctx, `SELECT value FROM json_each(?) WHERE value NOT IN (SELECT id FROM entries) LIMIT 1`,
			t.args...).Scan(&missing); err != nil {
			return targets{}, fmt.Errorf("decisions: check entries: %w", err)
		}
		return targets{}, domain.Errorf(domain.CodeNotFound, "entry %d not found", missing)
	}
	return t, nil
}

// audit is the target part of an audit event's detail.
func (t targets) audit() map[string]any {
	if t.selection != "" {
		return map[string]any{"selection_id": t.selection, "count": t.n}
	}
	ids := make([]string, len(t.ids))
	for i, id := range t.ids {
		ids[i] = id.String()
	}
	return map[string]any{"entry_ids": ids}
}

// idArray renders ids as a JSON array for json_each.
func idArray[T ~int64](ids []T) string {
	b := make([]byte, 0, 2+len(ids)*8)
	b = append(b, '[')
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, int64(id), 10)
	}
	return string(append(b, ']'))
}

// audit writes one audit event of the administrator at now, with the
// request's client address.
func (s *Service) audit(ctx context.Context, tx *sql.Tx, now time.Time, kind string, detail map[string]any) error {
	var addr netip.Addr
	if info, ok := clientip.From(ctx); ok {
		addr = info.Addr
	}
	return auth.WriteAudit(ctx, tx, auth.AuditEvent{At: now, Kind: kind, Actor: auth.ActorAdmin, ClientAddr: addr, Detail: detail})
}

func nullID(id *domain.EntryID) any {
	if id == nil {
		return nil
	}
	return int64(*id)
}

func idPtr(v sql.NullInt64) *domain.EntryID {
	if !v.Valid {
		return nil
	}
	id := domain.EntryID(v.Int64)
	return &id
}

// nonNil keeps the root's empty path an empty slice rather than nil.
func nonNil(p []byte) []byte {
	if p == nil {
		return []byte{}
	}
	return p
}
