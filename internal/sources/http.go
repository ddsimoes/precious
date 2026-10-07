package sources

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/store"
	"precious/internal/web/apierr"
)

// listRefreshWait bounds how long GET /api/sources waits for the availability
// refresh before answering with the states last recorded.
const listRefreshWait = 10 * time.Second

// Register serves the read endpoints on mux; authentication is the caller's
// middleware.
//
//   - GET /api/sources refreshes availability and returns
//     {"sources": [SourceJSON]}.
//   - GET /api/picker returns {"roots": [PickerItem]}, and
//     GET /api/picker?handle=H returns PickerListing. A bad handle, or any
//     query parameter but handle, is 400 invalid_request; a folder outside the
//     allowed roots is 403 outside_allowed_roots.
func Register(mux *http.ServeMux, s *Service, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	mux.Handle("GET /api/sources", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), listRefreshWait)
		err := s.Refresh(ctx)
		cancel()
		if err != nil && r.Context().Err() == nil {
			log.Warn("sources: availability refresh for the sources list failed", "err", err)
		}
		mounts := s.displayMounts()
		var out sourcesBody
		err = s.st.Read(r.Context(), func(tx *sql.Tx) error {
			srcs, err := listSources(r.Context(), tx)
			if err != nil {
				return err
			}
			out.Sources = make([]sourceJSON, len(srcs))
			for i, src := range srcs {
				if out.Sources[i], err = describe(r.Context(), tx, src, mounts); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			fail(w, log, err)
			return
		}
		writeJSON(w, out)
	}))
	mux.Handle("GET /api/picker", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for k := range q {
			if k != "handle" {
				apierr.Write(w, http.StatusBadRequest, domain.CodeInvalidRequest, "unknown query parameter "+k)
				return
			}
		}
		if !q.Has("handle") {
			roots, err := s.PickerRoots(r.Context())
			if err != nil {
				fail(w, log, err)
				return
			}
			writeJSON(w, pickerRootsBody{Roots: roots})
			return
		}
		if len(q["handle"]) != 1 {
			apierr.Write(w, http.StatusBadRequest, domain.CodeInvalidRequest, "one handle is required")
			return
		}
		listing, err := s.PickerChildren(r.Context(), q.Get("handle"))
		if err != nil {
			fail(w, log, err)
			return
		}
		writeJSON(w, listing)
	}))
}

func fail(w http.ResponseWriter, log *slog.Logger, err error) {
	var de *domain.Error
	if !errors.As(err, &de) {
		log.Error("sources: internal error", "err", err)
	}
	apierr.FromError(w, err)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

type sourcesBody struct {
	Sources []sourceJSON `json:"sources"`
}

type sourceBody struct {
	Source sourceJSON `json:"source"`
}

type pickerRootsBody struct {
	Roots []PickerItem `json:"roots"`
}

// sourceJSON is SourceJSON of the Interfaces section. Path and RelRoot are
// for display: Path is the absolute root folder where the source's volume is
// mounted now, RelRoot the root folder inside the volume.
type sourceJSON struct {
	ID           domain.SourceID       `json:"id"`
	Label        string                `json:"label"`
	State        State                 `json:"state"`
	StateReason  *string               `json:"state_reason"`
	MountPoint   *string               `json:"mount_point"`
	Path         *string               `json:"path"`
	RelRoot      string                `json:"rel_root"`
	Volume       volumeJSON            `json:"volume"`
	Capabilities fsaccess.Capabilities `json:"capabilities"`
	RootEntryID  *string               `json:"root_entry_id"`
	Totals       totalsJSON            `json:"totals"`
	LastScanAt   *time.Time            `json:"last_scan_at"`
	ActiveJob    *activeJobJSON        `json:"active_job"`
	// Schedule is null when off, and NextScanAt then too (r2b design D6).
	Schedule   *domain.Schedule `json:"schedule"`
	NextScanAt *time.Time       `json:"next_scan_at"`
	// ScheduleSkipped is the last due time when it was skipped, null when it
	// ran.
	ScheduleSkipped *scheduleSkipJSON `json:"schedule_skipped"`
}

type scheduleSkipJSON struct {
	At time.Time `json:"at"`
	// Reason is the source's state at that time: offline or unavailable.
	Reason string `json:"reason"`
}

type volumeJSON struct {
	Kind   fsaccess.VolumeKind `json:"kind"`
	ID     string              `json:"id"`
	Label  *string             `json:"label"`
	FSType string              `json:"fs_type"`
	Strong bool                `json:"strong"`
}

type totalsJSON struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
	Dirs  int64 `json:"dirs"`
}

type activeJobJSON struct {
	JobID    string           `json:"job_id"`
	State    domain.JobState  `json:"state"`
	Progress map[string]int64 `json:"progress"`
}

// describe returns src's SourceJSON: its row, its root folder's path through
// mounts, the totals of its root entry, and its queued, running, or paused
// scan.
func describe(ctx context.Context, q store.Queryer, src Source, mounts []fsaccess.Mount) (sourceJSON, error) {
	j := sourceJSON{
		ID: src.ID, Label: src.Label, State: src.State,
		StateReason: optString(src.StateReason),
		MountPoint:  optString(domain.DisplayName([]byte(src.MountPoint))),
		RelRoot:     domain.DisplayName(src.RelRoot),
		Volume: volumeJSON{
			Kind: src.Volume.Kind, ID: src.Volume.ID, Label: optString(src.Volume.Label),
			FSType: src.Volume.FSType, Strong: src.Volume.Strong,
		},
		Capabilities: src.Caps,
		LastScanAt:   src.LastScanAt,
		Schedule:     src.Schedule,
		NextScanAt:   src.NextScanAt,
	}
	if src.SkippedAt != nil {
		j.ScheduleSkipped = &scheduleSkipJSON{At: *src.SkippedAt, Reason: src.SkipReason}
	}
	// A source with a mount point is reached through a mount of its volume
	// that holds its root folder; for a bind mount that is not simply the
	// mount point joined with rel_root.
	if src.MountPoint != "" {
		if _, abs, ok := locate(mounts, src); ok {
			j.Path = optString(domain.DisplayName([]byte(abs)))
		}
	}
	if src.RootEntry != 0 {
		id := src.RootEntry.String()
		j.RootEntryID = &id
		err := q.QueryRowContext(ctx, `SELECT e.total_bytes, e.total_files, COALESCE(d.dirs, 0)
			FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id WHERE e.id = ?`, int64(src.RootEntry)).
			Scan(&j.Totals.Bytes, &j.Totals.Files, &j.Totals.Dirs)
		if err != nil {
			return sourceJSON{}, fmt.Errorf("sources: totals of %s: %w", src.ID, err)
		}
	}
	var (
		job       int64
		state     string
		cancelReq int64
		progress  string
	)
	err := q.QueryRowContext(ctx, `SELECT id, state, cancel_requested, progress FROM jobs
		WHERE source_id = ? AND kind = ? AND state IN ('queued', 'running', 'paused')`,
		string(src.ID), string(jobs.KindScan)).Scan(&job, &state, &cancelReq, &progress)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return j, nil
	case err != nil:
		return sourceJSON{}, err
	}
	a := &activeJobJSON{
		JobID:    domain.JobID(job).String(),
		State:    jobs.DisplayState(domain.JobState(state), cancelReq != 0),
		Progress: map[string]int64{},
	}
	if err := json.Unmarshal([]byte(progress), &a.Progress); err != nil {
		return sourceJSON{}, fmt.Errorf("sources: progress of job %d: %w", job, err)
	}
	j.ActiveJob = a
	return j, nil
}

// displayMounts reads the mount table for SourceJSON's path. A table that
// cannot be read leaves path null rather than failing the request; the
// availability refresh reports that failure.
func (s *Service) displayMounts() []fsaccess.Mount {
	m, err := s.fs.Mounts()
	if err != nil {
		return nil
	}
	return m
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
