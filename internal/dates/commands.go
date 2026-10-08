package dates

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/media"
	"precious/internal/web/clientip"
)

// Command names, the {name} segment of POST /api/commands/{name} (r5 design
// Interfaces).
const (
	CommandSetDateCorrection   = "set-date-correction"
	CommandClearDateCorrection = "clear-date-correction"
)

// Audit events of the correction commands (D11, owner-intent spec).
const (
	AuditDateCorrectionSet     = "date_correction_set"
	AuditDateCorrectionCleared = "date_correction_cleared"
)

// Limits and bounds of the correction commands (D11).
const (
	// maxCorrectionMedia is the most media files one correction names.
	maxCorrectionMedia = 50000
	// maxShiftS is ±50 years of 365.25 days, date_corrections' CHECK.
	maxShiftS = 1577880000
	// maxSkippedListed bounds the skipped list of an answer; skipped_count
	// counts them all.
	maxSkippedListed = 100
	// futureSlack is how far past now a corrected date may land.
	futureSlack = 24 * time.Hour
)

// Skip reasons of a bulk correction (D11), beside reasonNotMedia.
const (
	reasonNoNameDate   = "no_name_date"
	reasonNoFolderDate = "no_folder_date"
	reasonNoDate       = "no_date"
	reasonInFuture     = "in_future"
)

// RegisterCommands installs the correction commands on h (D11, Interfaces).
// Bodies are decoded strictly; IDs are strings.
//
//   - set-date-correction: Targets + {"correction":{"kind","local"?,
//     "offset_min"?,"shift_s"?}}: 200 {"applied","skipped_count",
//     "skipped":[{"entry_id","path","path_b64","reason"}],"batch_id"};
//   - clear-date-correction: Targets: 200 {"cleared","batch_id"}.
//
// Each writes its rows, re-derives its targets, requests the source's media
// job, and writes its audit event in the command's transaction.
func (s *Service) RegisterCommands(h *commands.Handler) {
	h.Register(CommandSetDateCorrection, s.decodeSetCorrection)
	h.Register(CommandClearDateCorrection, s.decodeClearCorrection)
}

// op is a decoded command: its canonical body and its effect.
type op struct {
	canonical []byte
	apply     func(ctx context.Context, tx *jobs.Tx) (int, any, error)
}

func (o *op) Canonical() []byte             { return o.canonical }
func (o *op) Prepare(context.Context) error { return nil }
func (o *op) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	return o.apply(ctx, tx)
}

// newOp decodes body strictly into req, checks its shape, and returns an
// operation whose canonical form is req re-encoded.
func newOp[T any](body []byte, req *T, check func() error, apply func(ctx context.Context, tx *jobs.Tx) (int, any, error)) (commands.Operation, error) {
	if err := commands.DecodeStrict(body, req); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return &op{canonical: canonical, apply: apply}, nil
}

// correctionJSON is a correction as the commands take it.
type correctionJSON struct {
	Kind      string `json:"kind"`
	Local     string `json:"local,omitempty"`
	OffsetMin *int   `json:"offset_min,omitempty"`
	ShiftS    *int64 `json:"shift_s,omitempty"`
}

type setCorrectionRequest struct {
	Targets
	Correction *correctionJSON `json:"correction"`
}

type clearCorrectionRequest struct {
	Targets
}

// check refuses a correction of a bad shape (D11): an unknown kind, a set
// without a date or with a date ParseLocal refuses (an offset needs a
// time), a shift of 0 or beyond ±50 years, and a field its kind does not
// take. Whether a set date lies in the future is decided when it applies.
func (c *correctionJSON) check(zone *time.Location) error {
	if c == nil {
		return domain.Errorf(domain.CodeInvalidRequest, "correction is required")
	}
	switch c.Kind {
	case media.CorrectionSet:
		if c.ShiftS != nil {
			return domain.Errorf(domain.CodeInvalidRequest, "shift_s goes with a shift only")
		}
		if c.Local == "" {
			return domain.Errorf(domain.CodeInvalidRequest, "a set needs local: YYYY, YYYY-MM, YYYY-MM-DD, or YYYY-MM-DDTHH:MM:SS")
		}
		if _, err := media.ParseLocal(c.Local, zone, c.OffsetMin); err != nil {
			return domain.Errorf(domain.CodeInvalidRequest,
				"%q is not a date to set: give YYYY, YYYY-MM, YYYY-MM-DD, or YYYY-MM-DDTHH:MM:SS (years 1700 to 2200), and offset_min (±840) only with a time", c.Local)
		}
	case media.CorrectionShift:
		if c.Local != "" || c.OffsetMin != nil {
			return domain.Errorf(domain.CodeInvalidRequest, "local and offset_min go with a set only")
		}
		if c.ShiftS == nil || *c.ShiftS == 0 || *c.ShiftS < -maxShiftS || *c.ShiftS > maxShiftS {
			return domain.Errorf(domain.CodeInvalidRequest, "a shift needs shift_s, a non-zero number of seconds within ±%d (50 years)", maxShiftS)
		}
	case media.CorrectionUseName, media.CorrectionUseFolder:
		if c.Local != "" || c.OffsetMin != nil || c.ShiftS != nil {
			return domain.Errorf(domain.CodeInvalidRequest, "%s takes no local, offset_min, or shift_s", c.Kind)
		}
	default:
		return domain.Errorf(domain.CodeInvalidRequest, "correction kind must be set, shift, use_name, or use_folder")
	}
	return nil
}

// media returns c as media.Derive takes it.
func (c *correctionJSON) media() *media.Correction {
	m := &media.Correction{Kind: c.Kind, SetLocal: c.Local}
	if c.OffsetMin != nil {
		off := *c.OffsetMin
		m.SetOffsetMin = &off
	}
	if c.ShiftS != nil {
		m.ShiftS = *c.ShiftS
	}
	return m
}

func (s *Service) decodeSetCorrection(body []byte) (commands.Operation, error) {
	var req setCorrectionRequest
	check := func() error {
		if err := req.Targets.Validate(true); err != nil {
			return err
		}
		return req.Correction.check(s.zone)
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.setCorrection(ctx, tx, req)
	})
}

func (s *Service) decodeClearCorrection(body []byte) (commands.Operation, error) {
	var req clearCorrectionRequest
	check := func() error { return req.Targets.Validate(true) }
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.clearCorrection(ctx, tx, req)
	})
}

// skippedJSON is a target a bulk correction left out.
type skippedJSON struct {
	EntryID string `json:"entry_id"`
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
	Reason  string `json:"reason"`
}

type setCorrectionResponse struct {
	Applied      int           `json:"applied"`
	SkippedCount int           `json:"skipped_count"`
	Skipped      []skippedJSON `json:"skipped"`
	BatchID      string        `json:"batch_id"`
}

type clearCorrectionResponse struct {
	Cleared int    `json:"cleared"`
	BatchID string `json:"batch_id"`
}

// setCorrection applies req in tx (D11): the targets are expanded, a
// camera target to the photos detection flagged (G2; a quarantined target
// fails the request), each media file is tried with the
// correction, and those it cannot apply to are skipped (a single target
// fails 409 invalid_entry_state instead); the rest get their correction row,
// replacing any earlier one, and are re-derived; the source's media job is
// requested when anything changed; one audit event records it all.
func (s *Service) setCorrection(ctx context.Context, tx *jobs.Tx, req setCorrectionRequest) (int, any, error) {
	q := tx.SQL()
	now := s.clk.Now()
	corr := req.Correction
	if corr.Kind == media.CorrectionSet {
		d, err := media.ParseLocal(corr.Local, s.zone, corr.OffsetMin)
		if err != nil {
			return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "%q is not a date to set", corr.Local)
		}
		if d.Instant.After(now.Add(futureSlack)) {
			return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "%s is after tomorrow; a date can be set up to now plus one day", corr.Local)
		}
	}
	single := req.EntryID != ""
	exp, err := expandTargets(ctx, q, req.Targets, maxCorrectionMedia, true)
	if err != nil {
		return 0, nil, err
	}
	if single && len(exp.Skipped) > 0 {
		return 0, nil, domain.Errorf(domain.CodeInvalidEntryState, "entry %s is not a photo or video of the index", req.EntryID)
	}
	refused, err := s.tryCorrection(ctx, q, exp.Media, corr.media(), now)
	if err != nil {
		return 0, nil, err
	}
	if single && len(refused) > 0 {
		return 0, nil, domain.Errorf(domain.CodeInvalidEntryState, "this correction cannot apply to entry %s: %s",
			req.EntryID, refusalText(refused[exp.Media[0]]))
	}
	applied := make([]domain.EntryID, 0, len(exp.Media))
	for _, id := range exp.Media {
		if _, ok := refused[id]; !ok {
			applied = append(applied, id)
		}
	}
	batch := rand.Text()
	var setOffset, shift any
	setLocal := any(nil)
	if corr.Kind == media.CorrectionSet {
		setLocal = corr.Local
		if corr.OffsetMin != nil {
			setOffset = *corr.OffsetMin
		}
	}
	if corr.ShiftS != nil {
		shift = *corr.ShiftS
	}
	for _, id := range applied {
		if _, err := q.ExecContext(ctx, `INSERT INTO date_corrections (entry_id, kind, set_local, set_offset_min, shift_s,
			batch_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (entry_id) DO UPDATE SET kind = excluded.kind, set_local = excluded.set_local,
				set_offset_min = excluded.set_offset_min, shift_s = excluded.shift_s, batch_id = excluded.batch_id,
				created_at = excluded.created_at`,
			int64(id), corr.Kind, setLocal, setOffset, shift, batch, clock.Millis(now)); err != nil {
			return 0, nil, fmt.Errorf("dates: write the correction of entry %d: %w", id, err)
		}
	}
	if len(applied) > 0 {
		if err := s.Rederive(ctx, q, applied); err != nil {
			return 0, nil, err
		}
		if err := EnqueueMedia(ctx, tx, exp.Source); err != nil {
			return 0, nil, err
		}
	}
	skipped, count, err := skippedList(ctx, q, exp, refused)
	if err != nil {
		return 0, nil, err
	}
	if err := audit(ctx, q, now, AuditDateCorrectionSet, map[string]any{"targets": req.Targets, "correction": corr,
		"applied": len(applied), "skipped": count, "batch_id": batch}); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, setCorrectionResponse{Applied: len(applied), SkippedCount: count, Skipped: skipped,
		BatchID: batch}, nil
}

// clearCorrection removes the corrections of req's media files in tx,
// re-derives them, requests the media job when any was removed, and audits
// the request (D11).
func (s *Service) clearCorrection(ctx context.Context, tx *jobs.Tx, req clearCorrectionRequest) (int, any, error) {
	q := tx.SQL()
	now := s.clk.Now()
	exp, err := ExpandTargets(ctx, q, req.Targets, maxCorrectionMedia)
	if err != nil {
		return 0, nil, err
	}
	var cleared []domain.EntryID
	for _, id := range exp.Media {
		res, err := q.ExecContext(ctx, `DELETE FROM date_corrections WHERE entry_id = ?`, int64(id))
		if err != nil {
			return 0, nil, fmt.Errorf("dates: clear the correction of entry %d: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return 0, nil, err
		} else if n > 0 {
			cleared = append(cleared, id)
		}
	}
	batch := rand.Text()
	if len(cleared) > 0 {
		if err := s.Rederive(ctx, q, cleared); err != nil {
			return 0, nil, err
		}
		if err := EnqueueMedia(ctx, tx, exp.Source); err != nil {
			return 0, nil, err
		}
	}
	if err := audit(ctx, q, now, AuditDateCorrectionCleared, map[string]any{"targets": req.Targets,
		"cleared": len(cleared), "batch_id": batch}); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, clearCorrectionResponse{Cleared: len(cleared), BatchID: batch}, nil
}

// tryCorrection derives each media file of ids with corr in place of its
// own correction, and returns the reason of each it cannot apply to: a
// use_name or use_folder without that date, a shift with nothing to shift,
// and a shift landing after now plus one day (D11, P9).
func (s *Service) tryCorrection(ctx context.Context, tx *sql.Tx, ids []domain.EntryID, corr *media.Correction,
	now time.Time) (map[domain.EntryID]string, error) {
	refused := map[domain.EntryID]string{}
	caps := map[domain.SourceID]fsaccess.Capabilities{}
	for chunk := range slices.Chunk(ids, rederiveChunk) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = int64(id)
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(inputsQuery, MediaCond("e"), placeholders(len(chunk))), args...)
		if err != nil {
			return nil, fmt.Errorf("dates: read the inputs of the targets: %w", err)
		}
		var read []inputsRow
		for rows.Next() {
			r, err := scanInputs(rows)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("dates: read the inputs of the targets: %w", err)
			}
			read = append(read, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("dates: read the inputs of the targets: %w", err)
		}
		for _, r := range read {
			c, ok := caps[r.source]
			if !ok {
				if c, err = sourceCaps(ctx, tx, r.source); err != nil {
					return nil, err
				}
				caps[r.source] = c
			}
			in := s.inputs(r, c, now)
			in.Correction = corr
			eff := media.Derive(in)
			switch {
			case eff.Corrected == "" && corr.Kind == media.CorrectionUseName:
				refused[r.id] = reasonNoNameDate
			case eff.Corrected == "" && corr.Kind == media.CorrectionUseFolder:
				refused[r.id] = reasonNoFolderDate
			case eff.Corrected == "" || eff.Date == nil:
				refused[r.id] = reasonNoDate
			case corr.Kind == media.CorrectionShift && eff.Date.Instant.After(now.Add(futureSlack)):
				refused[r.id] = reasonInFuture
			}
		}
	}
	return refused, nil
}

// refusalText explains a skip reason in a single request's error.
func refusalText(reason string) string {
	switch reason {
	case reasonNoNameDate:
		return "its name holds no date"
	case reasonNoFolderDate:
		return "no folder above it is named with a date"
	case reasonInFuture:
		return "the shift would move it past tomorrow"
	}
	return "it has no date to shift"
}

// skippedList lists what a correction skipped, the first
// maxSkippedListed in path order (member refs last), with the total: the
// targets ExpandTargets skipped and the media files refused.
func skippedList(ctx context.Context, q *sql.Tx, exp Expanded, refused map[domain.EntryID]string) ([]skippedJSON, int, error) {
	type skip struct {
		id     domain.EntryID
		path   []byte
		reason string
	}
	var entries []skip
	var members []Skip
	for _, sk := range exp.Skipped {
		if sk.Entry.IsMember() {
			members = append(members, sk)
			continue
		}
		entries = append(entries, skip{id: sk.Entry.Entry, reason: sk.Reason})
	}
	for _, id := range exp.Media {
		if reason, ok := refused[id]; ok {
			entries = append(entries, skip{id: id, reason: reason})
		}
	}
	count := len(entries) + len(members)
	for i := range entries {
		if err := q.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ?`, int64(entries[i].id)).
			Scan(&entries[i].path); err != nil {
			return nil, 0, fmt.Errorf("dates: read the path of entry %d: %w", entries[i].id, err)
		}
	}
	slices.SortStableFunc(entries, func(a, b skip) int { return bytes.Compare(a.path, b.path) })
	out := []skippedJSON{}
	for _, sk := range entries {
		if len(out) == maxSkippedListed {
			break
		}
		p := pathOf(sk.path)
		out = append(out, skippedJSON{EntryID: domain.Ref{Entry: sk.id}.String(), Path: p.Path, PathB64: p.PathB64,
			Reason: sk.reason})
	}
	for _, sk := range members {
		if len(out) == maxSkippedListed {
			break
		}
		path, err := memberPath(ctx, q, sk.Entry.Member)
		if err != nil {
			return nil, 0, err
		}
		p := pathOf(path)
		out = append(out, skippedJSON{EntryID: sk.Entry.String(), Path: p.Path, PathB64: p.PathB64, Reason: sk.Reason})
	}
	return out, count, nil
}

// memberPath is an archive member's path below its source's top (the
// archive's path, then the member's), or empty for an unknown member.
func memberPath(ctx context.Context, q *sql.Tx, id domain.MemberID) ([]byte, error) {
	var archive, member []byte
	err := q.QueryRowContext(ctx, `SELECT e.path, m.path FROM archive_members m JOIN entries e ON e.id = m.archive_id
		WHERE m.id = ?`, int64(id)).Scan(&archive, &member)
	if errors.Is(err, sql.ErrNoRows) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dates: read the path of member %d: %w", id, err)
	}
	return append(append(archive, '/'), member...), nil
}

// audit writes one audit event of the administrator at now, with the
// request's client address.
func audit(ctx context.Context, tx *sql.Tx, now time.Time, kind string, detail map[string]any) error {
	ev := auth.AuditEvent{At: now, Kind: kind, Actor: auth.ActorAdmin, Detail: detail}
	if info, ok := clientip.From(ctx); ok {
		ev.ClientAddr = info.Addr
	}
	return auth.WriteAudit(ctx, tx, ev)
}
