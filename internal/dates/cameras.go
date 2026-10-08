package dates

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/media"
)

// cameraBasis is media_cameras.basis (design Interfaces).
type cameraBasis struct {
	Events []basisEvent `json:"events"`
	Others []string     `json:"others"`
}

type basisEvent struct {
	FolderID  string  `json:"folder_id"`
	Path      string  `json:"path"`
	PathB64   []byte  `json:"path_b64"`
	DeltaS    int64   `json:"delta_s"`
	Photos    int     `json:"photos"`
	SpanS     int64   `json:"span_s"`
	Reference *string `json:"reference"`
}

// detection is what pass 4 computed from its snapshot.
type detection struct {
	results []media.CameraResult
	folders map[int64][]byte // event folder ID → its path
	flagged map[int64]bool   // the photos that get camera_offset
}

// cameras is pass 4 (D8): detection over a snapshot of src's photos, then
// one write that replaces src's media_cameras rows, sets camera_offset on
// exactly the photos of offset cameras in their events and clears it
// elsewhere, adjusts the summary's camera_offset and cameras counts, and
// stamps detected_at. It writes at the end of every pass: a request during
// it makes the loop run again, and that run's write replaces this one.
func (s *Service) cameras(ctx context.Context, job jobs.Job) error {
	src := job.SourceID
	var det detection
	if err := s.st.Read(ctx, func(tx *sql.Tx) error {
		var err error
		det, err = s.detect(ctx, tx, src)
		return err
	}); err != nil {
		return fmt.Errorf("dates: detect the cameras of %q: %w", src, err)
	}
	if err := s.stage(ctx, job, stageSnapshot); err != nil {
		return err
	}
	if err := s.st.Write(ctx, func(tx *sql.Tx) error { return s.writeCameras(ctx, tx, src, det) }); err != nil {
		return fmt.Errorf("dates: write the cameras of %q: %w", src, err)
	}
	return nil
}

// detect reads src's detection input (D8): each media photo with a read
// plausible EXIF capture and a camera key gives its capture (moved by a
// shift correction), its folder, its GPS time, and its folder's own date
// (D6, read by media.FolderPathDate); a photo with any other correction is
// left out. It runs media.Detect over them.
func (s *Service) detect(ctx context.Context, tx *sql.Tx, src domain.SourceID) (detection, error) {
	caps, err := sourceCaps(ctx, tx, src)
	if err != nil {
		return detection{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.parent_id, p.path, e.path, e.mtime_ns,
		m.capture_local, m.capture_offset_min, m.gps_ns, m.container_ns, m.make, m.model, m.serial,
		c.kind, c.shift_s
		FROM media_meta m JOIN entries e ON e.id = m.entry_id JOIN entries p ON p.id = e.parent_id
		LEFT JOIN date_corrections c ON c.entry_id = e.id
		WHERE m.source_id = ? AND m.state = 'read' AND m.capture_local IS NOT NULL AND `+MediaCond("e")+`
		ORDER BY e.id`, string(src))
	if err != nil {
		return detection{}, err
	}
	defer rows.Close()
	now := s.clk.Now()
	paths := map[int64][]byte{}
	folderDates := map[int64]*media.Date{}
	var photos []media.Photo
	for rows.Next() {
		var (
			r          inputsRow
			parent     int64
			parentPath []byte
		)
		r.metaState.String, r.metaState.Valid = string(media.MetaRead), true
		if err := rows.Scan(&r.id, &parent, &parentPath, &r.path, &r.mtime,
			&r.captureLocal, &r.captureOffset, &r.gpsNs, &r.containerNs, &r.mk, &r.model, &r.serial,
			&r.corrKind, &r.corrShift); err != nil {
			return detection{}, err
		}
		key := media.CameraKey(r.mk.String, r.model.String, r.serial.String)
		if key == "" || (r.corrKind.Valid && r.corrKind.String != media.CorrectionShift) {
			continue
		}
		// The uncorrected derivation gives the capture's candidate and its
		// plausibility; a shift then moves it.
		in := s.inputs(r, caps, now)
		capture, ok := exifCapture(media.Derive(in))
		if !ok {
			continue
		}
		ph := media.Photo{Entry: int64(r.id), Folder: parent, Camera: key, Capture: capture.Instant,
			OffsetKnown: capture.OffsetMin != nil, GPS: in.Meta.GPS}
		if r.corrKind.String == media.CorrectionShift {
			ph.Capture = ph.Capture.Add(time.Duration(r.corrShift.Int64) * time.Second)
		}
		fd, seen := folderDates[parent]
		if !seen {
			if d, ok := media.FolderPathDate(parentPath, s.zone, now); ok {
				fd = &d
			}
			folderDates[parent] = fd
			paths[parent] = parentPath
		}
		ph.FolderDate = fd
		photos = append(photos, ph)
	}
	if err := rows.Err(); err != nil {
		return detection{}, err
	}
	det := detection{results: media.Detect(photos), folders: map[int64][]byte{}, flagged: map[int64]bool{}}
	for _, r := range det.results {
		for _, ev := range r.Events {
			det.folders[ev.Folder] = paths[ev.Folder]
		}
		if r.State != media.CameraOffset {
			continue
		}
		events := map[int64]bool{}
		for _, ev := range r.Events {
			events[ev.Folder] = true
		}
		for _, ph := range photos {
			if ph.Camera == r.Key && events[ph.Folder] {
				det.flagged[ph.Entry] = true
			}
		}
	}
	return det, nil
}

// exifCapture returns the EXIF capture candidate of eff when it is
// plausible.
func exifCapture(eff media.Effective) (media.Date, bool) {
	for _, c := range eff.Candidates {
		if c.Source == media.SourceEXIF {
			return c.Date, c.Plausible
		}
	}
	return media.Date{}, false
}

// writeCameras is pass 4's write transaction.
func (s *Service) writeCameras(ctx context.Context, tx *sql.Tx, src domain.SourceID, det detection) error {
	now := clock.Millis(s.clk.Now())
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_cameras WHERE source_id = ?`, string(src)); err != nil {
		return err
	}
	var d summary
	for _, r := range det.results {
		b := cameraBasis{Events: []basisEvent{}, Others: r.Others}
		if b.Others == nil {
			b.Others = []string{}
		}
		for _, ev := range r.Events {
			e := basisEvent{FolderID: domain.EntryID(ev.Folder).String(), DeltaS: ev.DeltaS, Photos: ev.Photos,
				SpanS: ev.SpanS}
			p := det.folders[ev.Folder]
			e.Path, e.PathB64 = domain.DisplayName(p), p
			if e.PathB64 == nil {
				e.PathB64 = []byte{}
			}
			if ev.Reference != "" {
				ref := ev.Reference
				e.Reference = &ref
			}
			b.Events = append(b.Events, e)
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		parts := strings.SplitN(r.Key, "|", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		var shift any
		if r.ShiftS != nil {
			shift = *r.ShiftS
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_cameras (source_id, camera_key, make, model, serial,
			photos, state, suggested_shift_s, basis, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(src), r.Key, nullText(parts[0]), nullText(parts[1]), nullText(parts[2]), r.Photos, r.State, shift,
			string(raw), now); err != nil {
			return err
		}
		switch r.State {
		case media.CameraOffset:
			d.Cameras.Offset++
		case media.CameraDisagrees:
			d.Cameras.Disagrees++
		}
	}

	old := map[int64]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT entry_id FROM media_dates WHERE source_id = ? AND flags & 4 <> 0`,
		string(src))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		old[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	update := func(q string, ids []int64) (int64, error) {
		var n int64
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, q, id)
			if err != nil {
				return 0, err
			}
			k, err := res.RowsAffected()
			if err != nil {
				return 0, err
			}
			n += k
		}
		return n, nil
	}
	var clear, set []int64
	for _, id := range slices.Sorted(maps.Keys(old)) {
		if !det.flagged[id] {
			clear = append(clear, id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(det.flagged)) {
		if !old[id] {
			set = append(set, id)
		}
	}
	cleared, err := update(`UPDATE media_dates SET flags = flags & ~4 WHERE entry_id = ? AND flags & 4 <> 0`, clear)
	if err != nil {
		return err
	}
	added, err := update(`UPDATE media_dates SET flags = flags | 4 WHERE entry_id = ? AND flags & 4 = 0`, set)
	if err != nil {
		return err
	}
	sum, err := readSummary(ctx, tx, src)
	if err != nil {
		return err
	}
	sum.Flags.CameraOffset += added - cleared
	sum.Cameras = d.Cameras
	if err := writeSummary(ctx, tx, src, sum); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE media_sources SET detected_at = ? WHERE source_id = ?`, now, string(src))
	return err
}
