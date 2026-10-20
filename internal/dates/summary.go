package dates

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"precious/internal/domain"
	"precious/internal/media"
	"precious/internal/store"
)

// summary is media_sources.summary (D10): the summary read's body for one
// source, without time_zone, time_zone_set, summary_at, and detected_at,
// which the read adds. Every key is always present.
type summary struct {
	Media    int64 `json:"media"`
	Metadata struct {
		Pending    int64 `json:"pending"`
		Read       int64 `json:"read"`
		None       int64 `json:"none"`
		Unreadable int64 `json:"unreadable"`
	} `json:"metadata"`
	BySource struct {
		Owner      int64 `json:"owner"`
		EXIF       int64 `json:"exif"`
		GPS        int64 `json:"gps"`
		Container  int64 `json:"container"`
		FileName   int64 `json:"file_name"`
		FolderName int64 `json:"folder_name"`
		Mtime      int64 `json:"mtime"`
		None       int64 `json:"none"`
	} `json:"by_source"`
	ByConfidence struct {
		High   int64 `json:"high"`
		Medium int64 `json:"medium"`
		Low    int64 `json:"low"`
		Lowest int64 `json:"lowest"`
		None   int64 `json:"none"`
	} `json:"by_confidence"`
	Flags struct {
		MtimeDisagrees int64 `json:"mtime_disagrees"`
		Implausible    int64 `json:"implausible"`
		CameraOffset   int64 `json:"camera_offset"`
		NoDateMetadata int64 `json:"no_date_metadata"`
	} `json:"flags"`
	Cameras struct {
		Offset    int64 `json:"offset"`
		Disagrees int64 `json:"disagrees"`
	} `json:"cameras"`
}

// count adds n media_dates rows like r (n is negative to remove them).
func (s *summary) count(r dateRow, n int64) {
	s.Media += n
	switch media.MetaState(r.metaState) {
	case media.MetaPending:
		s.Metadata.Pending += n
	case media.MetaRead:
		s.Metadata.Read += n
	case media.MetaNone:
		s.Metadata.None += n
	case media.MetaUnreadable:
		s.Metadata.Unreadable += n
	}
	switch media.Source(r.source) {
	case media.SourceOwner:
		s.BySource.Owner += n
	case media.SourceEXIF:
		s.BySource.EXIF += n
	case media.SourceGPS:
		s.BySource.GPS += n
	case media.SourceContainer:
		s.BySource.Container += n
	case media.SourceFileName:
		s.BySource.FileName += n
	case media.SourceFolderName:
		s.BySource.FolderName += n
	case media.SourceMtime:
		s.BySource.Mtime += n
	case media.SourceNone:
		s.BySource.None += n
	}
	switch media.Confidence(r.confidence) {
	case media.ConfidenceHigh:
		s.ByConfidence.High += n
	case media.ConfidenceMedium:
		s.ByConfidence.Medium += n
	case media.ConfidenceLow:
		s.ByConfidence.Low += n
	case media.ConfidenceLowest:
		s.ByConfidence.Lowest += n
	case media.ConfidenceNone:
		s.ByConfidence.None += n
	}
	if r.flags&media.FlagMtimeDisagrees != 0 {
		s.Flags.MtimeDisagrees += n
	}
	if r.flags&media.FlagImplausible != 0 {
		s.Flags.Implausible += n
	}
	if r.flags&media.FlagCameraOffset != 0 {
		s.Flags.CameraOffset += n
	}
	if r.flags&media.FlagNoDateMetadata != 0 {
		s.Flags.NoDateMetadata += n
	}
}

// add adds every count of d to s.
func (s *summary) add(d *summary) {
	s.Media += d.Media
	s.Metadata.Pending += d.Metadata.Pending
	s.Metadata.Read += d.Metadata.Read
	s.Metadata.None += d.Metadata.None
	s.Metadata.Unreadable += d.Metadata.Unreadable
	s.BySource.Owner += d.BySource.Owner
	s.BySource.EXIF += d.BySource.EXIF
	s.BySource.GPS += d.BySource.GPS
	s.BySource.Container += d.BySource.Container
	s.BySource.FileName += d.BySource.FileName
	s.BySource.FolderName += d.BySource.FolderName
	s.BySource.Mtime += d.BySource.Mtime
	s.BySource.None += d.BySource.None
	s.ByConfidence.High += d.ByConfidence.High
	s.ByConfidence.Medium += d.ByConfidence.Medium
	s.ByConfidence.Low += d.ByConfidence.Low
	s.ByConfidence.Lowest += d.ByConfidence.Lowest
	s.ByConfidence.None += d.ByConfidence.None
	s.Flags.MtimeDisagrees += d.Flags.MtimeDisagrees
	s.Flags.Implausible += d.Flags.Implausible
	s.Flags.CameraOffset += d.Flags.CameraOffset
	s.Flags.NoDateMetadata += d.Flags.NoDateMetadata
	s.Cameras.Offset += d.Cameras.Offset
	s.Cameras.Disagrees += d.Cameras.Disagrees
}

// decodeSummary reads a stored summary strictly; "{}" is all zeros.
func decodeSummary(raw string) (summary, error) {
	var s summary
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return summary{}, err
	}
	return s, nil
}

// readSummary returns src's stored summary, zeros when it has none.
func readSummary(ctx context.Context, q store.Queryer, src domain.SourceID) (summary, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT summary FROM media_sources WHERE source_id = ?`, string(src)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return summary{}, nil
	}
	if err != nil {
		return summary{}, fmt.Errorf("dates: read the summary of %q: %w", src, err)
	}
	s, err := decodeSummary(raw)
	if err != nil {
		return summary{}, fmt.Errorf("dates: decode the summary of %q: %w", src, err)
	}
	return s, nil
}

// writeSummary stores s as src's summary, creating its media_sources row.
func writeSummary(ctx context.Context, tx *sql.Tx, src domain.SourceID, s summary) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO media_sources (source_id, summary) VALUES (?, ?)
		ON CONFLICT (source_id) DO UPDATE SET summary = excluded.summary`, string(src), string(raw)); err != nil {
		return fmt.Errorf("dates: write the summary of %q: %w", src, err)
	}
	return nil
}

// adjustSummary adds d to src's stored summary (D10).
func adjustSummary(ctx context.Context, tx *sql.Tx, src domain.SourceID, d *summary) error {
	s, err := readSummary(ctx, tx, src)
	if err != nil {
		return err
	}
	s.add(d)
	return writeSummary(ctx, tx, src, s)
}

// recount computes src's summary from its media_dates rows of media files
// (MediaCond) and its media_cameras rows: what the stored summary holds
// once every entry with a row has been derived since its last change. The
// dates pass rewrites the summary with it (D10).
func recount(ctx context.Context, q store.Queryer, src domain.SourceID) (summary, error) {
	var s summary
	rows, err := q.QueryContext(ctx, `SELECT d.source, d.confidence, d.meta_state, d.flags, count(*)
		FROM media_dates d JOIN entries e ON e.id = d.entry_id
		WHERE d.source_id = ? AND `+MediaCond("e")+`
		GROUP BY d.source, d.confidence, d.meta_state, d.flags`, string(src))
	if err != nil {
		return summary{}, fmt.Errorf("dates: count the dates of %q: %w", src, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r     dateRow
			flags int64
			n     int64
		)
		if err := rows.Scan(&r.source, &r.confidence, &r.metaState, &flags, &n); err != nil {
			return summary{}, fmt.Errorf("dates: count the dates of %q: %w", src, err)
		}
		r.flags = media.Flags(flags)
		s.count(r, n)
	}
	if err := rows.Err(); err != nil {
		return summary{}, fmt.Errorf("dates: count the dates of %q: %w", src, err)
	}
	rows.Close()
	cams, err := q.QueryContext(ctx, `SELECT state, count(*) FROM media_cameras WHERE source_id = ? GROUP BY state`,
		string(src))
	if err != nil {
		return summary{}, fmt.Errorf("dates: count the cameras of %q: %w", src, err)
	}
	defer cams.Close()
	for cams.Next() {
		var (
			state string
			n     int64
		)
		if err := cams.Scan(&state, &n); err != nil {
			return summary{}, fmt.Errorf("dates: count the cameras of %q: %w", src, err)
		}
		switch state {
		case media.CameraOffset:
			s.Cameras.Offset = n
		case media.CameraDisagrees:
			s.Cameras.Disagrees = n
		}
	}
	if err := cams.Err(); err != nil {
		return summary{}, fmt.Errorf("dates: count the cameras of %q: %w", src, err)
	}
	return s, nil
}
