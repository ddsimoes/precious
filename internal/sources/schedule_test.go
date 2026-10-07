package sources

import (
	"net/http"
	"path/filepath"
	"testing"

	"precious/internal/domain"
)

// The coming Sunday 03:00 in São Paulo (UTC-3, no daylight saving) after
// testNow, Thursday 2026-10-01 12:00 UTC.
const nextSundaySaoPaulo = "2026-10-04T06:00:00Z"

func weeklySunday() map[string]any {
	return map[string]any{"every": "week", "at": "03:00", "weekday": 0, "zone": "America/Sao_Paulo"}
}

// Scenario "Scheduling a source": weekly on Sunday at 03:00 in
// America/Sao_Paulo is 200 with the source, its schedule, and its next scan
// at the coming Sunday 03:00 in that zone, the list shows the same, and one
// source_schedule_set event records the previous (none) and new schedules.
func TestSetSourceSchedule(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	e.serve()

	r := e.command(CommandSetSourceSchedule, map[string]any{"source_id": "fotos", "schedule": weeklySunday()})
	e.wantStatus(r, http.StatusOK, "")
	wantSchedule := func(what string, s map[string]any) {
		t.Helper()
		sch := wantKeys(t, what+" schedule", s["schedule"], "every", "at", "weekday", "zone")
		if sch["every"] != "week" || sch["at"] != "03:00" || sch["weekday"] != 0.0 || sch["zone"] != "America/Sao_Paulo" ||
			s["next_scan_at"] != nextSundaySaoPaulo || s["schedule_skipped"] != nil {
			t.Fatalf("%s = %v", what, s)
		}
	}
	wantSchedule("response", r.source(t))
	wantSchedule("listed source", e.getJSON("/api/sources").body["sources"].([]any)[0].(map[string]any))
	if n := e.count(`SELECT count(*) FROM audit_events WHERE kind = ? AND actor = 'admin'
		AND json_extract(detail, '$.source_id') = 'fotos' AND json_type(detail, '$.previous_schedule') = 'null'
		AND json_extract(detail, '$.schedule.every') = 'week' AND json_extract(detail, '$.schedule.weekday') = 0
		AND json_extract(detail, '$.schedule.zone') = 'America/Sao_Paulo'`, AuditSourceScheduleSet); n != 1 {
		t.Fatalf("%d source_schedule_set events, want 1", n)
	}

	// A daily schedule has no weekday, and its next scan is the next 03:00.
	r = e.command(CommandSetSourceSchedule, map[string]any{"source_id": "fotos",
		"schedule": map[string]any{"every": "day", "at": "03:00", "zone": "UTC"}})
	e.wantStatus(r, http.StatusOK, "")
	if s := r.source(t); s["next_scan_at"] != "2026-10-02T03:00:00Z" {
		t.Fatalf("daily = %v", s)
	} else {
		wantKeys(t, "daily schedule", s["schedule"], "every", "at", "zone")
	}
}

// Scenario "Turning a schedule off": null clears the schedule and the next
// scan, and the event records the schedule it replaced.
func TestSetSourceScheduleOff(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	e.serve()
	e.wantStatus(e.command(CommandSetSourceSchedule, map[string]any{"source_id": "fotos", "schedule": weeklySunday()}),
		http.StatusOK, "")

	r := e.command(CommandSetSourceSchedule, map[string]any{"source_id": "fotos", "schedule": nil})
	e.wantStatus(r, http.StatusOK, "")
	if s := r.source(t); s["schedule"] != nil || s["next_scan_at"] != nil {
		t.Fatalf("off = %v", s)
	}
	if src := e.get("fotos"); src.Schedule != nil || src.NextScanAt != nil {
		t.Fatalf("stored after off: %+v %v", src.Schedule, src.NextScanAt)
	}
	if n := e.count(`SELECT count(*) FROM sources WHERE scan_schedule IS NULL AND next_scan_at IS NULL`); n != 1 {
		t.Fatalf("%d sources without a schedule, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE kind = ? AND json_type(detail, '$.schedule') = 'null'
		AND json_extract(detail, '$.previous_schedule.every') = 'week'`, AuditSourceScheduleSet); n != 1 {
		t.Fatalf("%d source_schedule_set events turning it off, want 1", n)
	}
}

// Scenario "A bad time is refused": 25:00, and every other malformed
// schedule or request, is 400 invalid_request and leaves the schedule as it
// was; an unknown source is 404 unknown_source.
func TestSetSourceScheduleRefused(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	e.serve()
	e.wantStatus(e.command(CommandSetSourceSchedule, map[string]any{"source_id": "fotos", "schedule": weeklySunday()}),
		http.StatusOK, "")

	with := func(k string, v any) map[string]any {
		s := weeklySunday()
		if v == nil {
			delete(s, k)
		} else {
			s[k] = v
		}
		return map[string]any{"source_id": "fotos", "schedule": s}
	}
	for name, body := range map[string]any{
		"time 25:00":         with("at", "25:00"),
		"time 03:60":         with("at", "03:60"),
		"time 3:00":          with("at", "3:00"),
		"weekday 7":          with("weekday", 7),
		"weekly, no weekday": with("weekday", nil),
		"unknown zone":       with("zone", "Mars/Olympus_Mons"),
		"no zone":            with("zone", nil),
		"local zone":         with("zone", "Local"),
		"every month":        with("every", "month"),
		"unknown field":      with("cron", "0 3 * * 0"),
		"daily with weekday": map[string]any{"source_id": "fotos", "schedule": map[string]any{"every": "day", "at": "03:00", "weekday": 1, "zone": "UTC"}},
		"no schedule":        map[string]any{"source_id": "fotos"},
		"no source_id":       map[string]any{"schedule": nil},
		"schedule a string":  map[string]any{"source_id": "fotos", "schedule": "daily"},
	} {
		r := e.command(CommandSetSourceSchedule, body)
		if r.status != http.StatusBadRequest || r.errCode() != string(domain.CodeInvalidRequest) {
			t.Errorf("%s: %d %s, want 400 invalid_request", name, r.status, r.raw)
		}
	}
	if src := e.get("fotos"); src.Schedule == nil || src.Schedule.At != "03:00" || src.NextScanAt == nil ||
		src.NextScanAt.Format("2006-01-02T15:04:05Z07:00") != nextSundaySaoPaulo {
		t.Fatalf("schedule after refused requests: %+v %v", src.Schedule, src.NextScanAt)
	}
	e.wantStatus(e.command(CommandSetSourceSchedule, map[string]any{"source_id": "nope", "schedule": weeklySunday()}),
		http.StatusNotFound, domain.CodeUnknownSource)
	e.wantStatus(e.command(CommandSetSourceSchedule, map[string]any{"source_id": "nope", "schedule": nil}),
		http.StatusNotFound, domain.CodeUnknownSource)
	if n := e.count(`SELECT count(*) FROM audit_events WHERE kind = ?`, AuditSourceScheduleSet); n != 1 {
		t.Fatalf("%d source_schedule_set events, want only the accepted one", n)
	}
}
