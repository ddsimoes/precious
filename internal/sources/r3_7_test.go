package sources

import (
	"net/http"
	"path/filepath"
	"testing"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index/indextest"
)

// listedWrites returns the writes object of source id in GET /api/sources,
// with the source's no_replace_rename capability.
func (e *env) listedWrites(id domain.SourceID) (writes map[string]any, noReplace any) {
	e.t.Helper()
	r := e.getJSON("/api/sources")
	if r.status != http.StatusOK {
		e.t.Fatalf("GET /api/sources = %d %s", r.status, r.raw)
	}
	for _, v := range r.body["sources"].([]any) {
		s := wantKeys(e.t, "source", v, sourceKeys...)
		if s["id"] == string(id) {
			caps := wantKeys(e.t, "capabilities", s["capabilities"], capsKeys...)
			return wantKeys(e.t, "writes", s["writes"], "enabled", "unavailable"), caps["no_replace_rename"]
		}
	}
	e.t.Fatalf("source %s is not listed: %s", id, r.raw)
	return nil, nil
}

// wantWrites checks the listed writes of source id.
func (e *env) wantWrites(id domain.SourceID, enabled bool, unavailable any) {
	e.t.Helper()
	if w, _ := e.listedWrites(id); w["enabled"] != enabled || w["unavailable"] != unavailable {
		e.t.Fatalf("writes of %s = %v, want enabled %v, unavailable %v", id, w, enabled, unavailable)
	}
}

func (e *env) setWrites(id domain.SourceID, enabled bool) response {
	e.t.Helper()
	return e.command(CommandSetSourceWrites, map[string]any{"source_id": id, "enabled": enabled})
}

func (e *env) writesEvents(id domain.SourceID) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM audit_events WHERE kind = ? AND json_extract(detail, '$.source_id') = ?`,
		AuditSourceWritesSet, string(id))
}

// R3.7: with [sources] allow_writes = false every source reports
// forbidden_by_config, whatever its filesystem, and turning writes on fails
// with 409 writes_unavailable and changes nothing; turning them off still
// succeeds.
func TestR3_7WritesForbiddenByConfig(t *testing.T) {
	e := newEnv(t)
	svc, err := New(e.st, e.fs, config.Sources{AllowedRoots: []string{e.base}, AllowWrites: false}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.disk("usb", uuidVolume("u-1", ""))
	ro := e.disk("dvd", uuidVolume("u-2", ""))
	roCaps := ext4Caps
	roCaps.ReadOnly = true
	e.fs.SetCapabilities(ro, roCaps)
	usb := e.add(filepath.Join(e.base, "usb"), "Fotos")
	dvd := e.add(filepath.Join(e.base, "dvd"), "Disco")
	e.serve()

	for _, id := range []domain.SourceID{usb.ID, dvd.ID} {
		e.wantWrites(id, false, WritesForbiddenByConfig)
		e.wantStatus(e.setWrites(id, true), http.StatusConflict, domain.CodeWritesUnavailable)
		if e.get(id).WriteEnabled {
			t.Fatalf("%s: writes on after a refused set-source-writes", id)
		}
		r := e.setWrites(id, false)
		e.wantStatus(r, http.StatusOK, "")
		if w := wantKeys(t, "writes", wantKeys(t, "source", r.body["source"], sourceKeys...)["writes"], "enabled", "unavailable"); w["enabled"] != false || w["unavailable"] != WritesForbiddenByConfig {
			t.Fatalf("%s: turning writes off answered %s", id, r.raw)
		}
		if n := e.writesEvents(id); n != 0 {
			t.Fatalf("%s: %d audit events, want none: nothing changed", id, n)
		}
	}

	// A source whose permission is on (from before the configuration
	// changed) can still have it turned off, with its event.
	if _, err := e.st.Writer().Exec(`UPDATE sources SET write_enabled = 1 WHERE id = ?`, string(usb.ID)); err != nil {
		t.Fatal(err)
	}
	e.wantWrites(usb.ID, true, WritesForbiddenByConfig)
	e.wantStatus(e.setWrites(usb.ID, false), http.StatusOK, "")
	e.wantWrites(usb.ID, false, WritesForbiddenByConfig)
	if n := e.writesEvents(usb.ID); n != 1 {
		t.Fatalf("turning writes off wrote %d audit events, want 1", n)
	}
}

// R3.7: a source on a read-only mount reports read_only, one on a filesystem
// of unknown capabilities no_replace_rename, and turning writes on fails with
// 409 writes_unavailable on both.
func TestR3_7WritesUnavailableByFilesystem(t *testing.T) {
	e := newEnv(t)
	ro := e.disk("dvd", uuidVolume("u-1", ""))
	e.fs.SetFSInfo(ro, fsaccess.FSInfo{Type: synthfs.DefaultFSType, FSID: ro, ReadOnly: true})
	odd := e.disk("odd", uuidVolume("u-2", ""))
	e.fs.SetCapabilities(odd, fsaccess.UnknownCapabilities(false))
	e.disk("usb", uuidVolume("u-3", ""))
	dvd := e.add(filepath.Join(e.base, "dvd"), "Disco")
	unknown := e.add(filepath.Join(e.base, "odd"), "Raro")
	usb := e.add(filepath.Join(e.base, "usb"), "Fotos")
	e.serve()

	for _, tc := range []struct {
		id        domain.SourceID
		reason    any
		noReplace bool
	}{
		{dvd.ID, WritesReadOnly, true},
		{unknown.ID, WritesNoReplaceRename, false},
		{usb.ID, nil, true},
	} {
		w, noReplace := e.listedWrites(tc.id)
		if w["enabled"] != false || w["unavailable"] != tc.reason || noReplace != tc.noReplace {
			t.Fatalf("%s: writes %v, no_replace_rename %v; want unavailable %v, no_replace_rename %v",
				tc.id, w, noReplace, tc.reason, tc.noReplace)
		}
		if tc.reason == nil {
			continue
		}
		e.wantStatus(e.setWrites(tc.id, true), http.StatusConflict, domain.CodeWritesUnavailable)
		if e.get(tc.id).WriteEnabled || e.writesEvents(tc.id) != 0 {
			t.Fatalf("%s: a refused set-source-writes changed the source or wrote an event", tc.id)
		}
	}
}

// R3.7: turning writes on and off answers the source with the new value,
// writes one audit event per change naming the source and the value, and
// GET /api/sources reflects it; setting the value a source already has
// changes nothing. Requests are decoded strictly.
func TestR3_7TurningWritesOnAndOff(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""))
	src := e.add(filepath.Join(e.base, "usb"), "Fotos")
	e.serve()
	e.wantWrites(src.ID, false, nil)

	answered := func(r response, want bool) {
		t.Helper()
		e.wantStatus(r, http.StatusOK, "")
		s := wantKeys(t, "source", r.body["source"], sourceKeys...)
		if w := s["writes"].(map[string]any); s["id"] != string(src.ID) || w["enabled"] != want || w["unavailable"] != nil {
			t.Fatalf("set-source-writes %v answered %s", want, r.raw)
		}
	}
	event := func(enabled, previous bool) int {
		t.Helper()
		return e.count(`SELECT count(*) FROM audit_events WHERE kind = ? AND actor = 'admin'
			AND json_extract(detail, '$.source_id') = ? AND json_extract(detail, '$.enabled') = ?
			AND json_extract(detail, '$.previous_enabled') = ?`, AuditSourceWritesSet, string(src.ID), enabled, previous)
	}

	answered(e.setWrites(src.ID, true), true)
	e.wantWrites(src.ID, true, nil)
	if !e.get(src.ID).WriteEnabled || e.writesEvents(src.ID) != 1 || event(true, false) != 1 {
		t.Fatalf("turning writes on: enabled %v, %d events", e.get(src.ID).WriteEnabled, e.writesEvents(src.ID))
	}
	answered(e.setWrites(src.ID, true), true)
	if n := e.writesEvents(src.ID); n != 1 {
		t.Fatalf("setting writes on again wrote %d events in all, want 1", n)
	}

	answered(e.setWrites(src.ID, false), false)
	e.wantWrites(src.ID, false, nil)
	if e.get(src.ID).WriteEnabled || e.writesEvents(src.ID) != 2 || event(false, true) != 1 {
		t.Fatalf("turning writes off: enabled %v, %d events", e.get(src.ID).WriteEnabled, e.writesEvents(src.ID))
	}

	for _, body := range []string{
		`{"source_id":"` + string(src.ID) + `"}`,
		`{"source_id":"` + string(src.ID) + `","enabled":null}`,
		`{"source_id":"` + string(src.ID) + `","enabled":"yes"}`,
		`{"source_id":"` + string(src.ID) + `","enabled":true,"path":"/"}`,
		`{"enabled":true}`,
	} {
		e.wantStatus(e.command(CommandSetSourceWrites, body), http.StatusBadRequest, domain.CodeInvalidRequest)
	}
	e.wantStatus(e.setWrites("nada", true), http.StatusNotFound, domain.CodeUnknownSource)
	e.wantStatus(e.setWrites("nada", false), http.StatusNotFound, domain.CodeUnknownSource)
	if e.get(src.ID).WriteEnabled || e.writesEvents(src.ID) != 2 {
		t.Fatal("a refused request changed the source")
	}
}

// Scenario "A move in progress blocks removal" (r3 design D15): while an
// action of the source is queued or running, remove-source is 409
// job_active; while one of its items has its intent recorded or awaits
// manual recovery, 409 recovery_needed; either way the source, its index,
// and its history are unchanged. Once the action ended and the item was
// resolved, the source is removed with its history.
func TestR3_7MoveInProgressBlocksRemoval(t *testing.T) {
	e := newEnv(t)
	e.serve()
	e.insertSource("busy", uuidVolume("u-1", ""), "")
	tree := indextest.Seed(t, e.st, indextest.Tree{Source: "busy", Nodes: []indextest.Node{
		{Path: "2004/a.jpg", Size: 10}, {Path: "Fotos", Kind: domain.EntryDirectory},
	}})
	if _, err := e.st.Writer().Exec(`INSERT INTO actions (id, kind, source_id, state, bulk, destination_id, created_at)
		VALUES (1, 'move', 'busy', 'queued', 0, ?, ?)`, int64(tree.ID("Fotos")), testNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO action_items (id, action_id, seq, op, entry_id, from_parent, from_name,
		to_parent, to_name, state) VALUES (1, 1, 1, 'rename', ?, ?, ?, ?, ?, 'planned')`,
		int64(tree.ID("2004/a.jpg")), int64(tree.ID("2004")), []byte("a.jpg"), int64(tree.ID("Fotos")), []byte("a.jpg")); err != nil {
		t.Fatal(err)
	}
	snapshot := e.entriesSnapshot("busy")
	history := func() string {
		t.Helper()
		var s string
		if err := e.st.Reader().QueryRow(`SELECT (SELECT group_concat(id || ':' || state) FROM actions) || '|' ||
			(SELECT group_concat(id || ':' || state) FROM action_items)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	set := func(table, state string) {
		t.Helper()
		if _, err := e.st.Writer().Exec(`UPDATE `+table+` SET state = ? WHERE id = 1`, state); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(what string, code domain.ErrorCode) {
		t.Helper()
		before := history()
		e.wantStatus(e.command(CommandRemoveSource, map[string]string{"source_id": "busy"}), http.StatusConflict, code)
		if e.count(`SELECT count(*) FROM sources WHERE id = 'busy'`) != 1 || e.entriesSnapshot("busy") != snapshot ||
			history() != before {
			t.Fatalf("%s: a refused remove-source changed the source, its index, or its history", what)
		}
	}

	refused("queued action", domain.CodeJobActive)
	set("actions", "running")
	refused("running action", domain.CodeJobActive)
	set("action_items", "intent")
	refused("running action with an intent", domain.CodeJobActive)
	set("actions", "stopped")
	refused("intent item", domain.CodeRecoveryNeeded)
	set("action_items", "manual_recovery")
	refused("manual_recovery item", domain.CodeRecoveryNeeded)
	if n := e.count(`SELECT count(*) FROM audit_events WHERE kind = ?`, AuditSourceRemoved); n != 0 {
		t.Fatalf("%d source_removed events after refusals", n)
	}

	// A stopped action with a resolved item, or a planned one, never blocks
	// removal.
	set("action_items", "resolved")
	if _, err := e.st.Writer().Exec(`INSERT INTO actions (id, kind, source_id, state, bulk, created_at)
		VALUES (2, 'rename', 'busy', 'planned', 0, ?)`, testNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(e.command(CommandRemoveSource, map[string]string{"source_id": "busy"}), http.StatusOK, "")
	for q, want := range map[string]int{
		`SELECT count(*) FROM sources WHERE id = 'busy'`:        0,
		`SELECT count(*) FROM entries WHERE source_id = 'busy'`: 0,
		`SELECT count(*) FROM actions`:                          0,
		`SELECT count(*) FROM action_items`:                     0,
	} {
		if n := e.count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
}
