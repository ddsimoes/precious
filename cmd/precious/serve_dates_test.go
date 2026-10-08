package main

import (
	"net/http"
	"strings"
	"testing"

	"precious/internal/config"
)

// TestServeDates checks that serve wires the dates commands and reads
// (r5 task 2.6): each correction command reaches its own decoder through
// the real router and middleware, the summary answers in the configured
// zone, and the list, cameras, and entry reads answer their own errors,
// never the unknown endpoint's.
func TestServeDates(t *testing.T) {
	origin := startServe(t, builtUI(), func(c *config.Config) { c.Dates.TimeZone = "America/Sao_Paulo" })
	c := newBrowser(t, origin)
	csrf := c.login()
	for _, name := range []string{"set-date-correction", "clear-date-correction"} {
		code, body := c.command(name, `{}`, origin, csrf)
		if code != http.StatusBadRequest || !strings.Contains(body, `"code":"invalid_request"`) || strings.Contains(body, "unknown command") {
			t.Errorf("%s {} = %d %s, want its own 400 invalid_request", name, code, body)
		}
	}
	var sum struct {
		Media       *int64 `json:"media"`
		TimeZone    string `json:"time_zone"`
		TimeZoneSet bool   `json:"time_zone_set"`
	}
	c.getJSON("/api/dates/summary", &sum)
	if sum.Media == nil || *sum.Media != 0 || sum.TimeZone != "America/Sao_Paulo" || !sum.TimeZoneSet {
		t.Errorf("GET /api/dates/summary = %+v", sum)
	}
	for path, want := range map[string]string{
		"/api/dates":                    `"code":"invalid_request"`,
		"/api/dates/cameras?source=x":   `"code":"unknown_source"`,
		"/api/entries/99999/dates":      `"code":"not_found"`,
		"/api/dates?source=x&count=all": `"code":"unknown_source"`,
	} {
		resp, body := c.fetch(http.MethodGet, path)
		if resp.StatusCode < 400 || !strings.Contains(body, want) || strings.Contains(body, "unknown API endpoint") {
			t.Errorf("GET %s = %d %s, want the dates read's own %s", path, resp.StatusCode, body, want)
		}
	}
}
