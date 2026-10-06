package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"precious/internal/domain"
	"precious/internal/web/apierr"
)

// Status is the JSON body of GET /api/jobs/{id}. Absent values are null.
type Status struct {
	ID              string            `json:"id"`
	Kind            Kind              `json:"kind"`
	SourceID        *domain.SourceID  `json:"source_id"`
	State           domain.JobState   `json:"state"` // displayed state, incl. cancel_requested
	CancelRequested bool              `json:"cancel_requested"`
	Attempts        int               `json:"attempts"`
	MaxAttempts     int               `json:"max_attempts"`
	Progress        map[string]int64  `json:"progress"`
	PauseReason     *string           `json:"pause_reason"`
	TerminalCode    *domain.ErrorCode `json:"terminal_code"`
	TerminalDetail  *string           `json:"terminal_detail"`
	CreatedAt       time.Time         `json:"created_at"`
	StartedAt       *time.Time        `json:"started_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	FinishedAt      *time.Time        `json:"finished_at"`
}

// Status is the API view of r.
func (r Record) Status() Status {
	progress := r.Progress
	if progress == nil {
		progress = map[string]int64{}
	}
	return Status{
		ID:              r.ID.String(),
		Kind:            r.Kind,
		SourceID:        ptrIf(r.SourceID),
		State:           r.DisplayState(),
		CancelRequested: r.CancelRequested,
		Attempts:        r.Attempts,
		MaxAttempts:     r.MaxAttempts,
		Progress:        progress,
		PauseReason:     ptrIf(r.PauseReason),
		TerminalCode:    ptrIf(r.TerminalCode),
		TerminalDetail:  ptrIf(r.TerminalDetail),
		CreatedAt:       r.CreatedAt,
		StartedAt:       r.StartedAt,
		UpdatedAt:       r.UpdatedAt,
		FinishedAt:      r.FinishedAt,
	}
}

func ptrIf[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// NewStatusHandler serves GET /api/jobs/{id}: the job's Status, or 404
// not_found. The route pattern must bind {id}.
func NewStatusHandler(r *Runner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id, err := domain.ParseJobID(req.PathValue("id"))
		if err != nil {
			apierr.FromError(w, err)
			return
		}
		rec, err := r.Get(req.Context(), id)
		if err != nil {
			apierr.FromError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(rec.Status())
	})
}

// sseBatch bounds the events read per query while streaming.
const sseBatch = 256

// heartbeat is the SSE keep-alive comment interval.
const heartbeat = 15 * time.Second

// NewEventsHandler serves GET /api/events as a server-sent event stream.
//
// Each job_events row is sent as `event: job` with `id: <job_events.id>` and
// `data: <Event JSON>`. A client resuming with Last-Event-ID (or the
// last_event_id query parameter, for a first connection that continues a
// rendered snapshot) receives every later event. When that ID is not a valid
// position in the retained history (older than the oldest retained event,
// newer than the newest, or malformed), the stream sends `event: reset` with
// `data: {}` and `id: <newest ID>` and continues from there; the client reloads
// its snapshot. A connection without an ID starts at the newest event and
// first sends a bare `id:` line so the browser reconnects from that position.
// A comment line is sent every 15 s, which also polls for events committed by
// other processes.
func NewEventsHandler(r *Runner) http.Handler {
	return &eventsHandler{r: r, heartbeat: heartbeat}
}

type eventsHandler struct {
	r         *Runner
	heartbeat time.Duration
}

func (h *eventsHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	db := h.r.store.Reader()
	rc := http.NewResponseController(w)
	// Streams outlive the server's write timeout.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return
	}

	lastRaw := req.Header.Get("Last-Event-ID")
	if lastRaw == "" {
		lastRaw = req.URL.Query().Get("last_event_id")
	}
	changed := h.r.hub.changed()
	oldest, head, err := eventWindow(ctx, db)
	if err != nil {
		apierr.FromError(w, err)
		return
	}

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	cursor := head
	switch {
	case lastRaw == "":
		_, err = fmt.Fprintf(w, "id: %d\n\n", head)
	default:
		last, perr := strconv.ParseInt(lastRaw, 10, 64)
		if perr == nil && last >= oldest-1 && last <= head {
			cursor = last
		} else {
			err = writeReset(w, head)
		}
	}
	if err != nil || rc.Flush() != nil {
		return
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		for {
			evs, err := eventsAfter(ctx, db, cursor, sseBatch)
			if err != nil {
				return
			}
			if len(evs) == 0 {
				break
			}
			if evs[0].ID != cursor+1 {
				// The next event may have been pruned while this client lagged.
				oldest, head, err := eventWindow(ctx, db)
				if err != nil {
					return
				}
				if cursor < oldest-1 {
					if writeReset(w, head) != nil || rc.Flush() != nil {
						return
					}
					cursor = head
					continue
				}
			}
			for _, e := range evs {
				if _, err := fmt.Fprintf(w, "id: %d\nevent: job\ndata: %s\n\n", e.ID, e.Payload); err != nil {
					return
				}
				cursor = e.ID
			}
			if rc.Flush() != nil {
				return
			}
			if len(evs) < sseBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
			changed = h.r.hub.changed()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

func writeReset(w http.ResponseWriter, head int64) error {
	_, err := fmt.Fprintf(w, "id: %d\nevent: reset\ndata: {}\n\n", head)
	return err
}
