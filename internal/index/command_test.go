package index

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"precious/internal/commands"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/rules"
)

// startScan posts start-scan for src and decodes the response.
func startScan(t *testing.T, url string, n int, src string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"source_id": src})
	req, err := http.NewRequest(http.MethodPost, url+"/api/commands/start-scan", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("key-%d", n))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%d %s: %v", resp.StatusCode, raw, err)
	}
	return resp.StatusCode, out
}

// start-scan queues one scan per source and coalesces a second request; the
// runner runs it as the scan kind and it ends succeeded with its progress;
// an unknown source is 404 and an offline one 409.
func TestStartScanCommand(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/src/disk", posix)
	root.Dir("a").File("x.jpg", 10, mtime)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := jobs.NewRunner(jobs.Options{Store: e.st, Registry: e.src, Config: config.Defaults().Jobs,
		Logger: logger, TickInterval: 10 * time.Millisecond, ProgressInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	NewHandler(e.st, e.src, rules.Default(), nil, config.Scan{}).Register(r)
	cmds := commands.New(commands.Options{Store: e.st, Jobs: r, Logger: logger})
	RegisterCommands(cmds)
	mux := http.NewServeMux()
	mux.Handle("POST /api/commands/{name}", cmds)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, first := startScan(t, srv.URL, 1, "disk")
	if status != http.StatusAccepted || first["state"] != "queued" || first["coalesced"] != false {
		t.Fatalf("start-scan = %d %v", status, first)
	}
	if status, again := startScan(t, srv.URL, 2, "disk"); status != http.StatusAccepted ||
		again["job_id"] != first["job_id"] || again["coalesced"] != true {
		t.Fatalf("second start-scan = %d %v, want the same job coalesced", status, again)
	}
	if status, body := startScan(t, srv.URL, 3, "nope"); status != http.StatusNotFound ||
		body["error"].(map[string]any)["code"] != string(domain.CodeUnknownSource) {
		t.Errorf("unknown source: %d %v", status, body)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong,
		rel_root, capabilities, state, created_at) VALUES ('gone', 'gone', 'path', '/gone', 'ext4', 0, X'', '{}',
		'offline', 0)`); err != nil {
		t.Fatal(err)
	}
	if status, body := startScan(t, srv.URL, 4, "gone"); status != http.StatusConflict ||
		body["error"].(map[string]any)["code"] != string(domain.CodeSourceOffline) {
		t.Errorf("offline source: %d %v", status, body)
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.Stop(context.Background())
	id, err := domain.ParseJobID(first["job_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var rec jobs.Record
	for {
		if rec, err = r.Get(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if rec.State.Terminal() || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.State != domain.JobSucceeded {
		t.Fatalf("scan job %s: %s %s", rec.State, rec.TerminalCode, rec.TerminalDetail)
	}
	if rec.Progress[ProgressPhase] != PhaseFinishing || rec.Progress[ProgressFiles] != 1 || rec.Progress[ProgressDirs] != 2 {
		t.Errorf("progress %v", rec.Progress)
	}
	if got := get(t, e.entries("disk"), "a/x.jpg"); got.TotalBytes != 10 {
		t.Errorf("a/x.jpg %+v", got)
	}
}
