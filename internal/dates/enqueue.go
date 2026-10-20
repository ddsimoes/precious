package dates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
)

// MediaCond returns the condition, for the entries row aliased alias, that
// holds for a media file (D2): a present regular file, outside the
// quarantine (index.NotQuarantined), whose file_kind is image or video.
// media.IsMediaKind is its Go twin. Archive members are not entries, so
// never media. alias must be a plain SQL identifier; any other panics.
func MediaCond(alias string) string {
	notQuarantined := index.NotQuarantined(alias) // panics on a bad alias
	return "(" + alias + ".kind = 'file' AND " + alias + ".state = 'present' AND " + alias +
		".file_kind IN ('image', 'video') AND " + notQuarantined + ")"
}

// ifDirtyPayload is the payload of every media job a request enqueues: on a
// first attempt the job runs its passes only while media_sources.dirty is
// set (D4).
var ifDirtyPayload = json.RawMessage(`{"if_dirty":true}`)

// mediaScope is the EnqueueOnce scope of a source's media job, and
// mediaNextScope that of its follow-up, enqueued while the first one runs.
func mediaScope(src domain.SourceID) string     { return "media:" + string(src) }
func mediaNextScope(src domain.SourceID) string { return "media-next:" + string(src) }

// EnqueueMedia requests src's media job inside the caller's transaction
// (D4): it sets media_sources.dirty, enqueues the job once by scope
// "media:<src>", and, when that job is already running, its follow-up once
// by scope "media-next:<src>", both with payload {"if_dirty":true}. A job
// paused by hand is resumed (jobs.Tx.EnqueueOnce). Sources in any state are
// requested: an offline source's job derives from the index alone. An
// unknown source is a domain unknown_source error.
func EnqueueMedia(ctx context.Context, tx *jobs.Tx, src domain.SourceID) error {
	q := tx.SQL()
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM sources WHERE id = ?`, string(src)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeUnknownSource, "unknown source %q", src)
	}
	if err != nil {
		return fmt.Errorf("dates: read source %q: %w", src, err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO media_sources (source_id, dirty) VALUES (?, 1)
		ON CONFLICT (source_id) DO UPDATE SET dirty = 1`, string(src)); err != nil {
		return fmt.Errorf("dates: mark source %q dirty: %w", src, err)
	}
	rec, _, err := tx.EnqueueOnce(jobs.Spec{Kind: KindMedia, SourceID: src, ScopeKey: mediaScope(src),
		Payload: ifDirtyPayload})
	if err != nil {
		return fmt.Errorf("dates: enqueue the media job of %q: %w", src, err)
	}
	if rec.State != domain.JobRunning {
		return nil
	}
	if _, _, err := tx.EnqueueOnce(jobs.Spec{Kind: KindMedia, SourceID: src, ScopeKey: mediaNextScope(src),
		Payload: ifDirtyPayload}); err != nil {
		return fmt.Errorf("dates: enqueue the media follow-up of %q: %w", src, err)
	}
	return nil
}
