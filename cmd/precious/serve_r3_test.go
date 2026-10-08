package main

import (
	"net/http"
	"strings"
	"testing"

	"precious/internal/config"
)

// TestServeOrganizing checks that serve wires organizing: each organize
// command reaches its own decoder through the real router and middleware
// (an empty body is that command's invalid_request, not an unknown
// command), and the history routes answer JSON.
func TestServeOrganizing(t *testing.T) {
	origin := startServe(t, builtUI(), func(*config.Config) {})
	c := newBrowser(t, origin)
	csrf := c.login()
	for _, name := range []string{"plan-move", "plan-rename", "plan-create-folder", "plan-rescue", "plan-merge",
		"plan-undo", "run-action", "cancel-action", "resolve-recovery"} {
		code, body := c.command(name, `{}`, origin, csrf)
		if code != http.StatusBadRequest || !strings.Contains(body, `"code":"invalid_request"`) || strings.Contains(body, "unknown command") {
			t.Errorf("%s {} = %d %s, want its own 400 invalid_request", name, code, body)
		}
	}
	var history struct {
		Items      []any   `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	c.getJSON("/api/history", &history)
	if history.Items == nil || len(history.Items) != 0 || history.NextCursor != nil {
		t.Errorf("GET /api/history = %+v, want an empty page", history)
	}
	for _, path := range []string{"/api/history/1", "/api/history/1/items"} {
		resp, body := c.fetch(http.MethodGet, path)
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, `"code":"not_found"`) ||
			strings.Contains(body, "unknown API endpoint") {
			t.Errorf("GET %s = %d %s, want the history's 404 not_found", path, resp.StatusCode, body)
		}
	}
}
