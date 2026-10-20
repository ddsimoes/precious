package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"precious/internal/clock"
	"precious/internal/config"
)

// serveStartLog starts serve with cfg prepared by prepare, stops it once it
// serves requests, and returns everything it logged.
func serveStartLog(t *testing.T, prepare func(*config.Config)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.Server.Listen = ln.Addr().String()
	cfg.Server.ExternalOrigin = "http://" + ln.Addr().String()
	cfg.Server.AllowInsecureHTTP = true
	prepare(&cfg)
	if err := config.Validate(&cfg); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- serve(ctx, cfg, slog.New(slog.NewTextHandler(&logged, nil)), serveDeps{
			Clock: clock.Real{}, UI: builtUI(), Listener: ln, Ready: func(string) { close(ready) },
		})
	}()
	select {
	case <-ready:
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("serve: %v", err)
		}
	case err := <-done:
		cancel()
		t.Fatalf("serve exited early: %v", err)
	}
	// serve has returned: nothing writes to logged any more.
	return logged.String()
}

// server-config "An unset zone", the server's half: serve starts and logs a
// warning naming dates.time_zone and the local zone in use; with a zone set
// it does not.
func TestServeWarnsUnsetTimeZone(t *testing.T) {
	logged := serveStartLog(t, func(*config.Config) {})
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "dates.time_zone is unset") ||
		!strings.Contains(logged, `zone="the server's local zone (`) {
		t.Errorf("start log lacks the unset-zone warning:\n%s", logged)
	}

	logged = serveStartLog(t, func(c *config.Config) { c.Dates.TimeZone = "America/Sao_Paulo" })
	if strings.Contains(logged, "dates.time_zone") {
		t.Errorf("start log warns although dates.time_zone is set:\n%s", logged)
	}
}
