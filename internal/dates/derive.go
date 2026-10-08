package dates

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/media"
)

// rederiveChunk bounds the entry IDs of one read in Rederive.
const rederiveChunk = 500

// dateRow is the part of a media_dates row the summary counts.
type dateRow struct {
	source     string
	confidence string
	metaState  string
	flags      media.Flags
}

// inputsRow is what Rederive reads of one entry: the entry, its media_meta
// and date_corrections rows, and its media_dates row.
type inputsRow struct {
	id     domain.EntryID
	source domain.SourceID
	path   []byte
	mtime  sql.NullInt64
	media  bool

	metaState                         sql.NullString
	captureLocal                      sql.NullString
	captureOffset                     sql.NullInt64
	gpsNs, containerNs                sql.NullInt64
	mk, model, serial                 sql.NullString
	corrKind, corrLocal               sql.NullString
	corrOffset, corrShift             sql.NullInt64
	hasDate                           bool
	key                               sql.NullInt64
	old                               dateRow
	oldSource, oldConfidence, oldMeta sql.NullString
	oldFlags                          sql.NullInt64
}

const inputsQuery = `SELECT e.id, e.source_id, e.path, e.mtime_ns, %s,
	m.state, m.capture_local, m.capture_offset_min, m.gps_ns, m.container_ns, m.make, m.model, m.serial,
	c.kind, c.set_local, c.set_offset_min, c.shift_s,
	d.entry_id IS NOT NULL, d.inputs_key, d.source, d.confidence, d.meta_state, d.flags
	FROM entries e
	LEFT JOIN media_meta m ON m.entry_id = e.id
	LEFT JOIN date_corrections c ON c.entry_id = e.id
	LEFT JOIN media_dates d ON d.entry_id = e.id
	WHERE e.id IN (%s)`

func scanInputs(rows *sql.Rows) (inputsRow, error) {
	var r inputsRow
	err := rows.Scan(&r.id, &r.source, &r.path, &r.mtime, &r.media,
		&r.metaState, &r.captureLocal, &r.captureOffset, &r.gpsNs, &r.containerNs, &r.mk, &r.model, &r.serial,
		&r.corrKind, &r.corrLocal, &r.corrOffset, &r.corrShift,
		&r.hasDate, &r.key, &r.oldSource, &r.oldConfidence, &r.oldMeta, &r.oldFlags)
	r.old = dateRow{source: r.oldSource.String, confidence: r.oldConfidence.String, metaState: r.oldMeta.String,
		flags: media.Flags(r.oldFlags.Int64)}
	return r, err
}

// Rederive derives the effective dates of ids inside tx, the caller's
// write transaction (D9): it reads each entry's inputs (path, modification
// time, media_meta row, correction, the source's capabilities, and the
// zone), and writes the media_dates row of each media file whose inputs
// key changed. A written row keeps its camera_offset bit unless the entry
// has a set or shift correction, which clears it. The rows of entries
// MediaCond no longer holds (missing, quarantined, not media) are deleted.
// Each source's summary (media_sources.summary) is adjusted by the rows
// written and deleted (D10). IDs no longer indexed are skipped.
func (s *Service) Rederive(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) error {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	now := s.clk.Now()
	caps := map[domain.SourceID]fsaccess.Capabilities{}
	deltas := map[domain.SourceID]*summary{}
	delta := func(src domain.SourceID) *summary {
		d := deltas[src]
		if d == nil {
			d = &summary{}
			deltas[src] = d
		}
		return d
	}
	for chunk := range slices.Chunk(ids, rederiveChunk) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = int64(id)
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(inputsQuery, MediaCond("e"), placeholders(len(chunk))), args...)
		if err != nil {
			return fmt.Errorf("dates: read the inputs of the dates: %w", err)
		}
		var read []inputsRow
		for rows.Next() {
			r, err := scanInputs(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("dates: read the inputs of the dates: %w", err)
			}
			read = append(read, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("dates: read the inputs of the dates: %w", err)
		}
		for _, r := range read {
			if !r.media {
				if !r.hasDate {
					continue
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM media_dates WHERE entry_id = ?`, int64(r.id)); err != nil {
					return fmt.Errorf("dates: delete the date of entry %d: %w", r.id, err)
				}
				delta(r.source).count(r.old, -1)
				continue
			}
			c, ok := caps[r.source]
			if !ok {
				if c, err = sourceCaps(ctx, tx, r.source); err != nil {
					return err
				}
				caps[r.source] = c
			}
			in := s.inputs(r, c, now)
			key := int64(media.InputsKey(in))
			if r.hasDate && r.key.Valid && r.key.Int64 == key {
				continue
			}
			row, err := writeDate(ctx, tx, r, in, key, now)
			if err != nil {
				return err
			}
			d := delta(r.source)
			if r.hasDate {
				d.count(r.old, -1)
			}
			d.count(row, 1)
		}
	}
	for _, src := range slices.Sorted(maps.Keys(deltas)) {
		if err := adjustSummary(ctx, tx, src, deltas[src]); err != nil {
			return err
		}
	}
	return nil
}

// inputs assembles media.Inputs for r (D9).
func (s *Service) inputs(r inputsRow, c fsaccess.Capabilities, now time.Time) media.Inputs {
	in := media.Inputs{Path: r.path, MetaState: media.MetaPending, Zone: s.zone, Now: now}
	in.Caps.LocalTime, in.Caps.Resolution = c.LocalTime, c.TimeResolution
	if r.mtime.Valid && domain.KnownModTime(r.mtime.Int64) {
		t := time.Unix(0, r.mtime.Int64).UTC()
		in.Mtime = &t
	}
	if r.metaState.Valid {
		in.MetaState = media.MetaState(r.metaState.String)
	}
	if in.MetaState == media.MetaRead {
		m := &media.Meta{CaptureLocal: r.captureLocal.String, Make: r.mk.String, Model: r.model.String,
			Serial: r.serial.String}
		if r.captureOffset.Valid {
			off := int(r.captureOffset.Int64)
			m.CaptureOffsetMin = &off
		}
		m.GPS, m.Container = nsTime(r.gpsNs), nsTime(r.containerNs)
		in.Meta = m
	}
	if r.corrKind.Valid {
		c := &media.Correction{Kind: r.corrKind.String, SetLocal: r.corrLocal.String, ShiftS: r.corrShift.Int64}
		if r.corrOffset.Valid {
			off := int(r.corrOffset.Int64)
			c.SetOffsetMin = &off
		}
		in.Correction = c
	}
	return in
}

func nsTime(ns sql.NullInt64) *time.Time {
	if !ns.Valid {
		return nil
	}
	t := time.Unix(0, ns.Int64).UTC()
	return &t
}

// writeDate derives r's effective date from in and writes its media_dates
// row with key; it returns the row as the summary counts it.
func writeDate(ctx context.Context, tx *sql.Tx, r inputsRow, in media.Inputs, key int64, now time.Time) (dateRow, error) {
	eff := media.Derive(in)
	flags := eff.Flags
	switch r.corrKind.String {
	case media.CorrectionSet, media.CorrectionShift:
	default:
		if r.hasDate {
			flags |= r.old.flags & media.FlagCameraOffset
		}
	}
	var (
		effective, offset   any
		local, precision    any
		corrected, cameraID any
	)
	if d := eff.Date; d != nil {
		effective, local, precision = d.Instant.UnixNano(), d.Local, string(d.Precision)
		if d.OffsetMin != nil {
			offset = *d.OffsetMin
		}
	}
	if eff.Corrected != "" {
		corrected = eff.Corrected
	}
	if m := in.Meta; m != nil {
		if k := media.CameraKey(m.Make, m.Model, m.Serial); k != "" {
			cameraID = k
		}
	}
	refined := 0
	if eff.Refined {
		refined = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_dates (entry_id, source_id, effective_ns, local, offset_min,
		precision, source, confidence, refined, corrected, flags, meta_state, camera_key, inputs_key, computed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id) DO UPDATE SET source_id = excluded.source_id, effective_ns = excluded.effective_ns,
			local = excluded.local, offset_min = excluded.offset_min, precision = excluded.precision,
			source = excluded.source, confidence = excluded.confidence, refined = excluded.refined,
			corrected = excluded.corrected, flags = excluded.flags, meta_state = excluded.meta_state,
			camera_key = excluded.camera_key, inputs_key = excluded.inputs_key, computed_at = excluded.computed_at`,
		int64(r.id), string(r.source), effective, local, offset, precision, string(eff.Source),
		string(eff.Confidence), refined, corrected, int64(flags), string(in.MetaState), cameraID, key,
		clock.Millis(now)); err != nil {
		return dateRow{}, fmt.Errorf("dates: write the date of entry %d: %w", r.id, err)
	}
	return dateRow{source: string(eff.Source), confidence: string(eff.Confidence), metaState: string(in.MetaState),
		flags: flags}, nil
}

// sourceCaps reads a source's recorded capabilities.
func sourceCaps(ctx context.Context, tx *sql.Tx, src domain.SourceID) (fsaccess.Capabilities, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT capabilities FROM sources WHERE id = ?`, string(src)).Scan(&raw); err != nil {
		return fsaccess.Capabilities{}, fmt.Errorf("dates: read the capabilities of %q: %w", src, err)
	}
	var c fsaccess.Capabilities
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return fsaccess.Capabilities{}, fmt.Errorf("dates: capabilities of %q: %w", src, err)
	}
	return c, nil
}
