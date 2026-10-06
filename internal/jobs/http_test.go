package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
)

type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// sseStream reads frames of one event-stream response.
type sseStream struct {
	frames chan sseFrame
	cancel context.CancelFunc
	resp   *http.Response
}

func openStream(t *testing.T, url, lastEventID string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		cancel()
		t.Fatalf("events: status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	s := &sseStream{frames: make(chan sseFrame, 64), cancel: cancel, resp: resp}
	go func() {
		defer close(s.frames)
		br := bufio.NewReader(resp.Body)
		var f sseFrame
		var fields int
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(line, "\n")
			switch {
			case line == "":
				if fields > 0 {
					s.frames <- f
				}
				f, fields = sseFrame{}, 0
			case strings.HasPrefix(line, ":"):
			default:
				name, value, _ := strings.Cut(line, ": ")
				fields++
				switch name {
				case "id":
					f.ID = value
				case "event":
					f.Event = value
				case "data":
					f.Data = value
				}
			}
		}
	}()
	t.Cleanup(s.close)
	return s
}

func (s *sseStream) next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			t.Fatal("event stream ended")
		}
		return f
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for an event")
		panic("unreachable")
	}
}

// close disconnects like a closing browser tab.
func (s *sseStream) close() {
	s.cancel()
	s.resp.Body.Close()
}

func eventServer(t *testing.T, r *Runner) string {
	t.Helper()
	srv := httptest.NewServer(NewEventsHandler(r))
	t.Cleanup(srv.Close)
	return srv.URL
}

func decodeEvent(t *testing.T, f sseFrame) Event {
	t.Helper()
	if f.Event != "job" {
		t.Fatalf("frame %+v is not a job event", f)
	}
	var ev Event
	if err := json.Unmarshal([]byte(f.Data), &ev); err != nil {
		t.Fatalf("decode %q: %v", f.Data, err)
	}
	return ev
}

// TestA16BrowserDisconnects: the browser disconnects during a scan and
// reconnects with its last event ID; the scan kept running and the client
// receives every later event.
func TestA16BrowserDisconnects(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	step := make(chan struct{})
	h := handlerFunc(func(ctx context.Context, _ Job, rt Runtime) error {
		for i := int64(1); i <= 4; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-step:
			}
			rt.Progress(map[string]int64{"dirs_probed": i})
		}
		return nil
	})
	r := e.runner(t, map[Kind]Handler{KindScan: h}, func(o *Options) { o.ProgressBatch = 1 })
	start(t, r)
	url := eventServer(t, r)

	browser := openStream(t, url, "")
	if pos := browser.next(t); pos.ID != "0" || pos.Event != "" {
		t.Fatalf("first frame = %+v, want the stream position id: 0", pos)
	}
	rec := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	for _, want := range []domain.JobState{domain.JobQueued, domain.JobRunning} {
		if ev := decodeEvent(t, browser.next(t)); ev.State != want || ev.JobID != rec.ID.String() {
			t.Fatalf("event = %+v, want %s of job %s", ev, want, rec.ID)
		}
	}
	step <- struct{}{}
	last := browser.next(t)
	if ev := decodeEvent(t, last); ev.Progress["dirs_probed"] != 1 {
		t.Fatalf("progress event = %+v", ev)
	}
	browser.close()

	for range 3 {
		step <- struct{}{}
	}
	waitJob(t, r, rec.ID, "succeeded", inState(domain.JobSucceeded))

	lastID, _ := strconv.ParseInt(last.ID, 10, 64)
	var want []loggedEvent
	for _, ev := range jobEvents(t, e.st, rec.ID) {
		if ev.ID > lastID {
			want = append(want, ev)
		}
	}
	if len(want) != 4 {
		t.Fatalf("events after %d = %d, want 3 progress + succeeded", lastID, len(want))
	}
	reconnected := openStream(t, url, last.ID)
	for i, w := range want {
		f := reconnected.next(t)
		if f.ID != strconv.FormatInt(w.ID, 10) {
			t.Fatalf("event %d id = %s, want %d", i, f.ID, w.ID)
		}
		if got := decodeEvent(t, f); got.State != w.Event.State || got.Progress["dirs_probed"] != w.Event.Progress["dirs_probed"] {
			t.Fatalf("event %d = %+v, want %+v", i, got, w.Event)
		}
	}
	if final := want[len(want)-1].Event; final.State != domain.JobSucceeded {
		t.Fatalf("last replayed event = %+v, want succeeded", final)
	}
}

// Spec scenario "Expired event history".
func TestExpiredEventHistory(t *testing.T) {
	e := newEnv(t)
	e.cfg.EventRetentionRows = 3
	r := e.runner(t, nil)
	url := eventServer(t, r)
	ctx := context.Background()
	for range 3 {
		rec := enqueue(t, r, Spec{Kind: "test"})
		if _, err := r.Cancel(ctx, rec.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.pruneEvents(ctx, e.clock.Now()); err != nil { // keeps events 4..6
		t.Fatal(err)
	}

	for _, last := range []string{"2", "99", "not-a-number"} {
		s := openStream(t, url, last)
		if f := s.next(t); f.Event != "reset" || f.Data != "{}" || f.ID != "6" {
			t.Fatalf("Last-Event-ID %s: first frame %+v, want reset with id 6", last, f)
		}
		s.close()
	}

	s := openStream(t, url, "3")
	for _, id := range []string{"4", "5", "6"} {
		if f := s.next(t); f.ID != id || f.Event != "job" {
			t.Fatalf("frame %+v, want job event %s", f, id)
		}
	}
	s.close()

	// Age-based retention empties the table; positions stay meaningful.
	e.clock.Advance(e.cfg.EventRetentionAge.Duration + time.Minute)
	if err := r.pruneEvents(ctx, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if f := openStream(t, url, "5").next(t); f.Event != "reset" || f.ID != "6" {
		t.Fatalf("pruned position: frame %+v, want reset", f)
	}
	current := openStream(t, url, "6")
	enqueue(t, r, Spec{Kind: "test"})
	if f := current.next(t); f.ID != "7" || decodeEvent(t, f).State != domain.JobQueued {
		t.Fatalf("frame after resume at head = %+v, want job event 7", f)
	}
}

func TestStatusHandler(t *testing.T) {
	e := newEnv(t)
	addSource(t, e.st, "a")
	stuck := newBlocker()
	r := e.runner(t, map[Kind]Handler{KindScan: stuck})
	start(t, r)
	mux := http.NewServeMux()
	mux.Handle("GET /api/jobs/{id}", NewStatusHandler(r))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	get := func(path string) (int, map[string]any) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}

	rec := enqueue(t, r, Spec{Kind: KindScan, SourceID: "a"})
	recv(t, stuck.entered, "attempt")
	if _, err := r.Cancel(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	code, body := get("/api/jobs/" + rec.ID.String())
	if code != http.StatusOK || body["id"] != rec.ID.String() || body["kind"] != "scan" ||
		body["source_id"] != "a" || body["state"] != "cancel_requested" || body["cancel_requested"] != true ||
		body["attempts"] != float64(0) || body["max_attempts"] != float64(3) ||
		body["pause_reason"] != nil || body["terminal_code"] != nil || body["finished_at"] != nil {
		t.Fatalf("running job: %d %v", code, body)
	}
	if _, ok := body["progress"].(map[string]any); !ok {
		t.Fatalf("progress = %v, want an object", body["progress"])
	}
	created, err := time.Parse(time.RFC3339Nano, body["created_at"].(string))
	if err != nil || !created.Equal(e.clock.Now()) {
		t.Fatalf("created_at = %v (%v)", body["created_at"], err)
	}

	close(stuck.release)
	waitJob(t, r, rec.ID, "cancelled", inState(domain.JobCancelled))
	if _, body := get("/api/jobs/" + rec.ID.String()); body["state"] != "cancelled" ||
		body["terminal_detail"] != "cancelled on request" || body["finished_at"] == nil {
		t.Fatalf("cancelled job: %v", body)
	}

	for _, path := range []string{"/api/jobs/999", "/api/jobs/abc", "/api/jobs/-1"} {
		code, body := get(path)
		errBody, _ := body["error"].(map[string]any)
		if code != http.StatusNotFound || errBody["code"] != "not_found" {
			t.Fatalf("GET %s = %d %v, want 404 not_found", path, code, body)
		}
	}
}
