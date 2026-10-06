package sources

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
)

func (r response) source(t *testing.T) map[string]any {
	t.Helper()
	s, ok := r.body["source"].(map[string]any)
	if !ok {
		t.Fatalf("response %d %s has no source", r.status, r.raw)
	}
	return s
}

func (e *env) wantStatus(r response, status int, code domain.ErrorCode) {
	e.t.Helper()
	if r.status != status || (code != "" && r.errCode() != string(code)) {
		e.t.Fatalf("response = %d %s, want %d %s", r.status, r.raw, status, code)
	}
}

// add-source with a handle for the folder Fotos and no label creates source
// fotos, online, with its root entry and an audit event, and queues no scan.
func TestAddSource(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", "FOTOS"), "Fotos")
	e.serve()
	r := e.command(CommandAddSource, map[string]string{"handle": e.svc.handle(filepath.Join(e.base, "usb", "Fotos"))})
	e.wantStatus(r, http.StatusCreated, "")
	s := r.source(t)
	if s["id"] != "fotos" || s["label"] != "Fotos" || s["state"] != "online" || s["active_job"] != nil {
		t.Fatalf("source = %v", s)
	}
	if n := e.count(`SELECT count(*) FROM jobs`); n != 0 {
		t.Fatalf("%d jobs queued, want none", n)
	}
	if n := e.count(`SELECT count(*) FROM entries WHERE source_id = 'fotos' AND path = X'' AND kind = 'directory' AND parent_id IS NULL`); n != 1 {
		t.Fatalf("%d root entries, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE kind = ? AND actor = 'admin'
		AND json_extract(detail, '$.source_id') = 'fotos' AND json_extract(detail, '$.volume_kind') = 'uuid'`, AuditSourceAdded); n != 1 {
		t.Fatalf("%d source_added events, want 1", n)
	}
	// A label given is used, trimmed.
	e.disk("disk2", uuidVolume("u-2", ""))
	r = e.command(CommandAddSource, map[string]string{"handle": e.svc.handle(filepath.Join(e.base, "disk2")), "label": "  Old disk "})
	e.wantStatus(r, http.StatusCreated, "")
	if s := r.source(t); s["id"] != "old-disk" || s["label"] != "Old disk" {
		t.Fatalf("labelled source = %v", s)
	}
}

// A second source labelled Fotos on another volume is fotos-2, and an ID is
// never reused after its source is removed.
func TestAddSourceUniqueIDs(t *testing.T) {
	e := newEnv(t)
	e.disk("a", uuidVolume("u-1", ""), "Fotos")
	e.disk("b", uuidVolume("u-2", ""), "Fotos")
	e.disk("c", uuidVolume("u-3", ""), "Fotos")
	e.serve()
	add := func(disk string) map[string]any {
		t.Helper()
		r := e.command(CommandAddSource, map[string]string{"handle": e.svc.handle(filepath.Join(e.base, disk, "Fotos"))})
		e.wantStatus(r, http.StatusCreated, "")
		return r.source(t)
	}
	if id := add("a")["id"]; id != "fotos" {
		t.Fatalf("first id = %v", id)
	}
	if id := add("b")["id"]; id != "fotos-2" {
		t.Fatalf("second id = %v", id)
	}
	if got := e.get("fotos"); got.Label != "Fotos" || got.Volume.ID != "u-1" {
		t.Fatalf("first source changed: %+v", got)
	}
	e.wantStatus(e.command(CommandRemoveSource, map[string]string{"source_id": "fotos-2"}), http.StatusOK, "")
	if id := add("c")["id"]; id != "fotos-3" {
		t.Fatalf("id after removing fotos-2 = %v, want fotos-3", id)
	}
	if got := slug("Músicas & Fotos: 2004/ÉTÉ"); got != "musicas-fotos-2004-ete" {
		t.Fatalf("slug = %q", got)
	}
	if got := slug("日本"); got != "source" {
		t.Fatalf("slug of a label without Latin letters = %q", got)
	}
}

// A folder equal to, inside, or around a source of the same volume is
// source_exists.
func TestAddSourceOverlap(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	e.disk("disk2", uuidVolume("u-2", ""), "Fotos", "Fotos/2004")
	e.serve()
	add := func(p string) response {
		return e.command(CommandAddSource, map[string]string{"handle": e.svc.handle(filepath.Join(e.base, p))})
	}
	e.wantStatus(add("usb"), http.StatusCreated, "")
	e.wantStatus(add("disk2/Fotos"), http.StatusCreated, "")
	for _, p := range []string{"usb", "usb/Fotos", "disk2", "disk2/Fotos", "disk2/Fotos/2004"} {
		e.wantStatus(add(p), http.StatusConflict, domain.CodeSourceExists)
	}
	if n := e.count(`SELECT count(*) FROM sources`); n != 2 {
		t.Fatalf("%d sources, want 2", n)
	}
}

// The overlap is checked again under the writer lock: of concurrent
// requests for one folder exactly one succeeds.
func TestAddSourceConcurrent(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""))
	e.serve()
	h := e.svc.handle(filepath.Join(e.base, "usb"))
	const n = 8
	var wg sync.WaitGroup
	results := make(chan response, n)
	for range n {
		wg.Go(func() {
			r, err := e.send(CommandAddSource, map[string]string{"handle": h})
			if err != nil {
				t.Error(err)
				return
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	created, exists := 0, 0
	for r := range results {
		switch {
		case r.status == http.StatusCreated:
			created++
		case r.status == http.StatusConflict && r.errCode() == string(domain.CodeSourceExists):
			exists++
		default:
			t.Errorf("response %d %s", r.status, r.raw)
		}
	}
	if created != 1 || exists != n-1 {
		t.Fatalf("%d created and %d source_exists, want 1 and %d", created, exists, n-1)
	}
	// Once the source exists, the check outside the transaction refuses too.
	_, err := e.svc.PrepareAdd(context.Background(), h, "")
	wantCode(t, err, domain.CodeSourceExists)
}

// Two folders that both passed the check outside the transaction: the second
// Add, for the same or a nested folder, finds the first under the writer lock.
func TestAddRechecksOverlapInTransaction(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	ctx := context.Background()
	prepare := func(p string) Candidate {
		t.Helper()
		c, err := e.svc.PrepareAdd(ctx, e.svc.handle(filepath.Join(e.base, p)), "")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first, same, nested := prepare("usb"), prepare("usb"), prepare("usb/Fotos")
	add := func(c Candidate) error {
		return e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := e.svc.Add(ctx, tx, c)
			return err
		})
	}
	if err := add(first); err != nil {
		t.Fatal(err)
	}
	wantCode(t, add(same), domain.CodeSourceExists)
	wantCode(t, add(nested), domain.CodeSourceExists)
	if n := e.count(`SELECT count(*) FROM sources`); n != 1 {
		t.Fatalf("%d sources, want 1", n)
	}
}

// The state directory, a folder holding it, and a folder inside it are
// refused with outside_allowed_roots.
func TestAddSourceRefusesStateDirectory(t *testing.T) {
	e := newEnv(t)
	stateDir, err := filepath.EvalSymlinks(filepath.Dir(e.st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(stateDir, "inside"))
	e.svc = e.service(filepath.Dir(stateDir))
	e.serve()
	for _, p := range []string{filepath.Dir(stateDir), stateDir, filepath.Join(stateDir, "inside")} {
		r := e.command(CommandAddSource, map[string]string{"handle": e.svc.handle(p)})
		e.wantStatus(r, http.StatusForbidden, domain.CodeOutsideAllowedRoots)
	}
	if n := e.count(`SELECT count(*) FROM sources`); n != 0 {
		t.Fatalf("%d sources, want none", n)
	}
}

// Malformed add-source requests are invalid_request.
func TestAddSourceInvalidRequests(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""))
	e.serve()
	h := e.svc.handle(filepath.Join(e.base, "usb"))
	for _, body := range []any{
		`{}`,
		`{"handle":""}`,
		`not json`,
		`{"handle":"` + h + `"} {}`,
		map[string]any{"handle": h, "label": strings.Repeat("x", MaxLabelLen+1)},
		map[string]any{"handle": h, "label": "tab\there"},
		map[string]any{"handle": h, "mount_point": "/mnt"},
	} {
		e.wantStatus(e.command(CommandAddSource, body), http.StatusBadRequest, domain.CodeInvalidRequest)
	}
}

// rename-source changes only the label.
func TestRenameSource(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	before := e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	e.serve()
	r := e.command(CommandRenameSource, map[string]string{"source_id": "fotos", "label": "Blue USB stick"})
	e.wantStatus(r, http.StatusOK, "")
	if s := r.source(t); s["id"] != "fotos" || s["label"] != "Blue USB stick" || s["root_entry_id"] != before.RootEntry.String() {
		t.Fatalf("renamed source = %v", s)
	}
	after := e.get("fotos")
	before.Label = "Blue USB stick"
	if after.ID != before.ID || after.Label != before.Label || string(after.RelRoot) != string(before.RelRoot) ||
		after.Volume != before.Volume || after.RootEntry != before.RootEntry || after.MountPoint != before.MountPoint {
		t.Fatalf("after rename = %+v, want %+v", after, before)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE kind = ? AND json_extract(detail, '$.previous_label') = 'Fotos'
		AND json_extract(detail, '$.label') = 'Blue USB stick'`, AuditSourceRenamed); n != 1 {
		t.Fatalf("%d source_renamed events, want 1", n)
	}
	e.wantStatus(e.command(CommandRenameSource, map[string]string{"source_id": "nope", "label": "x"}),
		http.StatusNotFound, domain.CodeUnknownSource)
	e.wantStatus(e.command(CommandRenameSource, map[string]string{"source_id": "fotos", "label": "  "}),
		http.StatusBadRequest, domain.CodeInvalidRequest)
	e.wantStatus(e.command(CommandRenameSource, map[string]string{"label": "x"}),
		http.StatusBadRequest, domain.CodeInvalidRequest)
}

// remove-source deletes the source with its entries, aggregates, decisions,
// tag assignments, name index rows, and finished jobs; tags themselves and
// other sources stay.
func TestRemoveSourceCascades(t *testing.T) {
	e := newEnv(t)
	e.serve()
	nodes := []indextest.Node{{Path: "2004/a.jpg", Size: 10}, {Path: "2004/b.jpg", Size: 20}, {Path: "c.txt", Size: 3}}
	e.insertSource("gone", uuidVolume("u-1", ""), "")
	gone := indextest.Seed(t, e.st, indextest.Tree{Source: "gone", Nodes: nodes})
	e.insertSource("kept", uuidVolume("u-2", ""), "")
	kept := indextest.Seed(t, e.st, indextest.Tree{Source: "kept", Nodes: nodes})
	e.tag(gone.ID("c.txt"), "trip")
	e.tag(kept.ID("c.txt"), "trip")
	if _, err := e.st.Writer().Exec(`UPDATE entries SET decision = 'keep', eff_decision = 'keep' WHERE source_id = 'gone'`); err != nil {
		t.Fatal(err)
	}
	if err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		rec, _, err := tx.StartScan("gone")
		if err != nil {
			return err
		}
		_, err = tx.Cancel(rec.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	keptSnapshot := e.entriesSnapshot("kept")

	e.wantStatus(e.command(CommandRemoveSource, map[string]string{"source_id": "gone"}), http.StatusOK, "")
	for q, want := range map[string]int{
		`SELECT count(*) FROM sources WHERE id = 'gone'`:                                                            0,
		`SELECT count(*) FROM entries WHERE source_id = 'gone'`:                                                     0,
		`SELECT count(*) FROM jobs WHERE source_id = 'gone'`:                                                        0,
		`SELECT count(*) FROM dir_stats`:                                                                            2, // kept's root and 2004
		`SELECT count(*) FROM entry_tags`:                                                                           1,
		`SELECT count(*) FROM tags WHERE name = 'trip'`:                                                             1,
		`SELECT count(*) FROM entry_names WHERE entry_names MATCH '"jpg"'`:                                          2,
		`SELECT count(*) FROM audit_events WHERE kind = 'source_removed' AND json_extract(detail, '$.entries') = 5`: 1,
	} {
		if n := e.count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	for _, p := range []string{"2004", "2004/a.jpg", "c.txt"} {
		if n := e.count(`SELECT count(*) FROM entry_names WHERE rowid = ?`, int64(gone.ID(p))); n != 0 {
			t.Errorf("name index row of removed %s remains", p)
		}
	}
	if got := e.entriesSnapshot("kept"); got != keptSnapshot {
		t.Fatalf("other source changed:\n%s\nwant:\n%s", got, keptSnapshot)
	}
	e.wantStatus(e.command(CommandRemoveSource, map[string]string{"source_id": "gone"}), http.StatusNotFound, domain.CodeUnknownSource)
}

// While a scan of the source is queued, running, or paused, remove-source is
// job_active and changes nothing.
func TestRemoveSourceWithActiveScan(t *testing.T) {
	e := newEnv(t)
	e.serve()
	e.insertSource("busy", uuidVolume("u-1", ""), "")
	indextest.Seed(t, e.st, indextest.Tree{Source: "busy", Nodes: []indextest.Node{{Path: "a", Size: 1}}})
	var job domain.JobID
	if err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		rec, _, err := tx.StartScan("busy")
		job = rec.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"queued", "running", "paused"} {
		if _, err := e.st.Writer().Exec(`UPDATE jobs SET state = ? WHERE id = ?`, state, int64(job)); err != nil {
			t.Fatal(err)
		}
		e.wantStatus(e.command(CommandRemoveSource, map[string]string{"source_id": "busy"}), http.StatusConflict, domain.CodeJobActive)
		if n := e.count(`SELECT count(*) FROM entries WHERE source_id = 'busy'`); n != 2 {
			t.Fatalf("with a %s scan: %d entries left, want 2", state, n)
		}
	}
	if _, err := e.st.Writer().Exec(`UPDATE jobs SET state = 'cancelled' WHERE id = ?`, int64(job)); err != nil {
		t.Fatal(err)
	}
	e.wantStatus(e.command(CommandRemoveSource, map[string]string{"source_id": "busy"}), http.StatusOK, "")
}

// R1.18: a source can be added only through the picker, inside an allowed
// root. A raw path or a location outside the allowed roots is refused, and
// nothing else in the API accepts a path.
func TestR1_18SourceAddedOnlyThroughPicker(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	outside := realDir(t)
	if err := os.Symlink("/etc", filepath.Join(e.base, "etc-link")); err != nil {
		t.Fatal(err)
	}
	e.serve()
	sources := func() int { return e.count(`SELECT count(*) FROM sources`) }

	// A valid handle, reached through the picker, adds the folder.
	roots := e.getJSON("/api/picker")
	root := roots.body["roots"].([]any)[0].(map[string]any)
	usb := e.getJSON("/api/picker?handle=" + root["handle"].(string))
	var usbHandle string
	for _, c := range usb.body["children"].([]any) {
		if c := c.(map[string]any); c["name"] == "usb" {
			usbHandle = c["handle"].(string)
		}
	}
	listing := e.getJSON("/api/picker?handle=" + usbHandle)
	fotos := listing.body["children"].([]any)[0].(map[string]any)
	e.wantStatus(e.command(CommandAddSource, map[string]any{"handle": fotos["handle"]}), http.StatusCreated, "")

	// A raw path in handle is refused, and no source is created.
	for _, raw := range []string{"/etc", `C:\Windows`, filepath.Join(e.base, "usb")} {
		e.wantStatus(e.command(CommandAddSource, map[string]string{"handle": raw}), http.StatusBadRequest, domain.CodeInvalidRequest)
		e.wantStatus(e.getJSON("/api/picker?handle="+url.QueryEscape(raw)), http.StatusBadRequest, domain.CodeInvalidRequest)
	}
	// A forged handle is refused.
	forged := usbHandle[:len(usbHandle)-4] + "AAAA"
	e.wantStatus(e.command(CommandAddSource, map[string]string{"handle": forged}), http.StatusBadRequest, domain.CodeInvalidRequest)
	// A correctly signed handle for a folder outside every allowed root, or
	// for a symlink inside one that points outside, is outside_allowed_roots.
	for _, p := range []string{outside, filepath.Join(e.base, "etc-link")} {
		h := e.svc.handle(p)
		e.wantStatus(e.command(CommandAddSource, map[string]string{"handle": h}), http.StatusForbidden, domain.CodeOutsideAllowedRoots)
		e.wantStatus(e.getJSON("/api/picker?handle="+h), http.StatusForbidden, domain.CodeOutsideAllowedRoots)
	}
	if n := sources(); n != 1 {
		t.Fatalf("%d sources, want 1", n)
	}

	// Nothing else accepts a path: every source command refuses a path field,
	// and the picker refuses a path parameter.
	for name, body := range map[string]map[string]any{
		CommandAddSource:    {"handle": usbHandle, "path": "/etc"},
		CommandRenameSource: {"source_id": "fotos", "label": "x", "path": "/etc"},
		CommandRemoveSource: {"source_id": "fotos", "path": "/etc"},
	} {
		e.wantStatus(e.command(name, body), http.StatusBadRequest, domain.CodeInvalidRequest)
	}
	e.wantStatus(e.getJSON("/api/picker?path=/etc"), http.StatusBadRequest, domain.CodeInvalidRequest)
	e.wantStatus(e.getJSON("/api/picker?handle="+usbHandle+"&path=/etc"), http.StatusBadRequest, domain.CodeInvalidRequest)
	if n := sources(); n != 1 || e.get("fotos").Label != "Fotos" {
		t.Fatalf("%d sources after the path requests, want fotos unchanged", n)
	}
}
