package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServeRunsAMissedScheduleAtStart: a source whose scheduled scan came
// due while the server was stopped is scanned once when serve starts
// (file-index "The server was down"), and its next scan moves past now.
func TestServeRunsAMissedScheduleAtStart(t *testing.T) {
	disk := t.TempDir()
	folder := filepath.Join(disk, "corpus")
	if err := os.MkdirAll(filepath.Join(folder, "2004"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "2004", "a.jpg"), []byte("synthetic photo"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := r2Config(t, disk)
	setAdminPassword(t, cfg)
	origin, stop := serveAt(t, cfg)
	c := newBrowser(t, origin)
	csrf := c.login()
	src := addAndScan(t, c, origin, csrf)
	code, body := c.command("set-source-schedule",
		`{"source_id":"`+src+`","schedule":{"every":"day","at":"03:00","zone":"America/Sao_Paulo"}}`, origin, csrf)
	if code != http.StatusOK || !strings.Contains(body, `"next_scan_at":"`) {
		t.Fatalf("set-source-schedule = %d %s", code, body)
	}
	stop()

	// The due time passed while the server was down.
	st := openStore(t, cfg)
	defer st.Close()
	if _, err := st.Writer().Exec(`UPDATE sources SET next_scan_at = 1 WHERE id = ?`, src); err != nil {
		t.Fatal(err)
	}
	serveAt(t, cfg)
	var scans int
	var next int64
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if err := st.Reader().QueryRow(`SELECT (SELECT count(*) FROM jobs WHERE kind = 'scan' AND source_id = ?1
			AND state = 'succeeded'), next_scan_at FROM sources WHERE id = ?1`, src).Scan(&scans, &next); err != nil {
			t.Fatal(err)
		}
		if scans >= 2 || time.Now().After(deadline) {
			break
		}
	}
	awaitQuiet(t, st, "scan")
	if err := st.Reader().QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'scan' AND source_id = ?`, src).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 2 {
		t.Fatalf("%d scans, want the owner's and one catch-up", scans)
	}
	if now := time.Now().UnixMilli(); next <= now || next > now+25*time.Hour.Milliseconds() {
		t.Fatalf("next scan %d, want within a day after now %d", next, now)
	}
}
