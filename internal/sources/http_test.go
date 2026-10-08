package sources

import (
	"context"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
	"precious/internal/jobs"
)

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func wantKeys(t *testing.T, what string, v any, want ...string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s = %v, want an object", what, v)
	}
	slices.Sort(want)
	if got := keys(m); !slices.Equal(got, want) {
		t.Fatalf("%s keys = %v, want %v", what, got, want)
	}
	return m
}

var (
	sourceKeys = []string{"id", "label", "state", "state_reason", "mount_point", "path", "rel_root", "volume", "capabilities",
		"root_entry_id", "totals", "last_scan_at", "active_job", "schedule", "next_scan_at", "schedule_skipped", "writes",
		"quarantine"}
	volumeKeys = []string{"kind", "id", "label", "fs_type", "strong"}
	capsKeys   = []string{"known", "read_only", "case_sensitive", "normalization_sensitive", "stable_identity",
		"local_time", "hard_links", "time_resolution_ns", "no_replace_rename"}
	pickerItemKeys = []string{"handle", "name", "path", "volume_label", "fs_type", "is_source"}
)

// GET /api/sources returns SourceJSON for every source: a strong volume
// online with its totals and its active scan and no schedule, and a weak one
// offline whose last scheduled scan was skipped.
func TestSourcesEndpoint(t *testing.T) {
	e := newEnv(t)
	e.serve()
	vol := uuidVolume("u-1", "FOTOS")
	e.disk("usb", vol)
	e.insertSource("fotos", vol, "")
	seeded := indextest.Seed(t, e.st, indextest.Tree{Source: "fotos", Nodes: []indextest.Node{
		{Path: "2004/a.jpg", Size: 1000}, {Path: "b.txt", Size: 24},
	}})
	var job domain.JobID
	if err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		rec, _, err := tx.StartScan("fotos")
		job = rec.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`UPDATE jobs SET progress = '{"files":2,"phase":1}' WHERE id = ?`, int64(job)); err != nil {
		t.Fatal(err)
	}
	e.insertSource("share", fsaccess.Volume{Kind: fsaccess.VolumePath, ID: "/mnt/gone", FSType: "cifs", DeviceKey: "mount:/mnt/gone"}, "docs")
	if _, err := e.st.Writer().Exec(`UPDATE sources SET scan_schedule = '{"every":"day","at":"03:00","zone":"UTC"}',
		next_scan_at = ?, schedule_skipped_at = ?, schedule_skip_reason = 'offline' WHERE id = 'share'`,
		testNow.Add(15*time.Hour).UnixMilli(), testNow.Add(-9*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	r := e.getJSON("/api/sources")
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/sources = %d %s", r.status, r.raw)
	}
	wantKeys(t, "body", r.body, "sources")
	list := r.body["sources"].([]any)
	if len(list) != 2 {
		t.Fatalf("%d sources, want 2: %s", len(list), r.raw)
	}

	s := wantKeys(t, "fotos", list[0], sourceKeys...)
	if s["id"] != "fotos" || s["label"] != "fotos" || s["state"] != "online" || s["state_reason"] != nil ||
		s["mount_point"] != filepath.Join(e.base, "usb") || s["path"] != filepath.Join(e.base, "usb") || s["rel_root"] != "" ||
		s["root_entry_id"] != seeded.Root.String() ||
		s["last_scan_at"] != "2024-01-01T00:00:00Z" || s["schedule"] != nil || s["next_scan_at"] != nil ||
		s["schedule_skipped"] != nil {
		t.Fatalf("fotos = %v", s)
	}
	v := wantKeys(t, "fotos volume", s["volume"], volumeKeys...)
	if v["kind"] != "uuid" || v["id"] != "u-1" || v["label"] != "FOTOS" || v["fs_type"] != "ext4" || v["strong"] != true {
		t.Fatalf("fotos volume = %v", v)
	}
	c := wantKeys(t, "fotos capabilities", s["capabilities"], capsKeys...)
	if c["known"] != true || c["case_sensitive"] != true || c["time_resolution_ns"] != 1.0 {
		t.Fatalf("fotos capabilities = %v", c)
	}
	if w := wantKeys(t, "fotos writes", s["writes"], "enabled", "unavailable"); w["enabled"] != false || w["unavailable"] != nil {
		t.Fatalf("fotos writes = %v", w)
	}
	tot := wantKeys(t, "fotos totals", s["totals"], "bytes", "files", "dirs")
	if tot["bytes"] != 1024.0 || tot["files"] != 2.0 || tot["dirs"] != 1.0 {
		t.Fatalf("fotos totals = %v", tot)
	}
	if q := wantKeys(t, "fotos quarantine", s["quarantine"], "files", "bytes", "name_taken"); q["files"] != 0.0 ||
		q["bytes"] != 0.0 || q["name_taken"] != false {
		t.Fatalf("fotos quarantine = %v", q)
	}
	a := wantKeys(t, "fotos active_job", s["active_job"], "job_id", "state", "progress")
	p := a["progress"].(map[string]any)
	if a["job_id"] != job.String() || a["state"] != "queued" || p["files"] != 2.0 || p["phase"] != 1.0 {
		t.Fatalf("active_job = %v", a)
	}

	s = wantKeys(t, "share", list[1], sourceKeys...)
	if s["state"] != "offline" || s["state_reason"] != ReasonNotMounted || s["mount_point"] != nil ||
		s["path"] != nil || s["rel_root"] != "docs" ||
		s["active_job"] != nil || s["root_entry_id"] != nil || s["last_scan_at"] != nil ||
		s["next_scan_at"] != "2026-10-02T03:00:00Z" {
		t.Fatalf("share = %v", s)
	}
	if sch := wantKeys(t, "share schedule", s["schedule"], "every", "at", "zone"); sch["every"] != "day" ||
		sch["at"] != "03:00" || sch["zone"] != "UTC" {
		t.Fatalf("share schedule = %v", sch)
	}
	if skip := wantKeys(t, "share schedule_skipped", s["schedule_skipped"], "at", "reason"); skip["at"] != "2026-10-01T03:00:00Z" ||
		skip["reason"] != "offline" {
		t.Fatalf("share schedule_skipped = %v", skip)
	}
	v = wantKeys(t, "share volume", s["volume"], volumeKeys...)
	if v["kind"] != "path" || v["id"] != "/mnt/gone" || v["label"] != nil || v["strong"] != false {
		t.Fatalf("share volume = %v", v)
	}
	if tot := s["totals"].(map[string]any); tot["bytes"] != 0.0 || tot["files"] != 0.0 || tot["dirs"] != 0.0 {
		t.Fatalf("share totals = %v", tot)
	}

	// A cancel request shows on the active job.
	if err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		_, err := tx.Cancel(job)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if s := e.getJSON("/api/sources").body["sources"].([]any)[0].(map[string]any); s["active_job"] != nil {
		t.Fatalf("a cancelled scan is still active: %v", s["active_job"])
	}
}

// A source below a bind mount: rel_root is its folder inside the volume,
// including the part of the volume the mount shows, and path is where the
// folder is reached through the mount, in the add-source response and in
// GET /api/sources.
func TestSourcePathThroughBindMount(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", "BACKUP"), "Fotos")
	point := filepath.Join(e.base, "usb")
	e.fs.SetFSInfo(dev, fsaccess.FSInfo{Dev: dev, Mount: &fsaccess.MountInfo{Root: "/data", MountPoint: point, FSType: "ext4"}})
	e.serve()
	folder := filepath.Join(point, "Fotos")

	added := e.command(CommandAddSource, map[string]any{"handle": e.svc.handle(folder)})
	e.wantStatus(added, http.StatusCreated, "")
	s := wantKeys(t, "added source", added.body["source"], sourceKeys...)
	if s["path"] != folder || s["rel_root"] != "data/Fotos" || s["mount_point"] != point {
		t.Fatalf("added source = %s", added.raw)
	}
	s = wantKeys(t, "listed source", e.getJSON("/api/sources").body["sources"].([]any)[0], sourceKeys...)
	if s["state"] != "online" || s["path"] != folder || s["rel_root"] != "data/Fotos" {
		t.Fatalf("listed source = %v", s)
	}
}

// GET /api/picker lists the roots; with a handle it lists that folder, and
// refuses bad handles, outside folders, and other parameters.
func TestPickerEndpoint(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", "FOTOS"), "Fotos")
	e.serve()
	r := e.getJSON("/api/picker")
	wantKeys(t, "roots body", r.body, "roots")
	roots := r.body["roots"].([]any)
	if len(roots) != 1 {
		t.Fatalf("roots = %v", roots)
	}
	root := wantKeys(t, "root", roots[0], pickerItemKeys...)
	if root["path"] != e.base || root["is_source"] != false {
		t.Fatalf("root = %v", root)
	}
	l := e.getJSON("/api/picker?handle=" + e.svc.handle(filepath.Join(e.base, "usb")))
	if l.status != http.StatusOK {
		t.Fatalf("listing = %d %s", l.status, l.raw)
	}
	wantKeys(t, "listing", l.body, "entry", "children", "truncated")
	entry := wantKeys(t, "entry", l.body["entry"], pickerItemKeys...)
	child := wantKeys(t, "child", l.body["children"].([]any)[0], pickerItemKeys...)
	if entry["name"] != "usb" || entry["volume_label"] != "FOTOS" || entry["fs_type"] != "ext4" ||
		child["name"] != "Fotos" || child["path"] != filepath.Join(e.base, "usb", "Fotos") || l.body["truncated"] != false {
		t.Fatalf("listing = %s", l.raw)
	}
	h := e.svc.handle(filepath.Join(e.base, "usb"))
	e.wantStatus(e.getJSON("/api/picker?handle=nope"), http.StatusBadRequest, domain.CodeInvalidRequest)
	e.wantStatus(e.getJSON("/api/picker?handle="), http.StatusBadRequest, domain.CodeInvalidRequest)
	e.wantStatus(e.getJSON("/api/picker?handle="+h+"&handle="+h), http.StatusBadRequest, domain.CodeInvalidRequest)
	e.wantStatus(e.getJSON("/api/picker?path="+e.base), http.StatusBadRequest, domain.CodeInvalidRequest)
	e.wantStatus(e.getJSON("/api/picker?handle="+e.svc.handle(realDir(t))), http.StatusForbidden, domain.CodeOutsideAllowedRoots)
}

// R1.15 (the Linux half of 2.4): the server over the portable backend adds a
// folder through a picker handle, and GET /api/sources reports it on a weak
// path volume with capabilities known false. Nested folders are still
// refused although every root is its own path volume there.
func TestR1_15PortableBackendReportsUnknownCapabilities(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the acceptance suite runs on Linux in R1")
	}
	e := newEnv(t)
	mkdir(t, filepath.Join(e.base, "Fotos", "2004"))
	svc, err := New(e.st, fsaccess.NewPortable(), config.Sources{AllowedRoots: []string{e.base}}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.serve()

	root := e.getJSON("/api/picker").body["roots"].([]any)[0].(map[string]any)
	l := e.getJSON("/api/picker?handle=" + root["handle"].(string))
	fotos := l.body["children"].([]any)[0].(map[string]any)
	if fotos["name"] != "Fotos" {
		t.Fatalf("picker children = %s", l.raw)
	}
	e.wantStatus(e.command(CommandAddSource, map[string]any{"handle": fotos["handle"]}), http.StatusCreated, "")

	r := e.getJSON("/api/sources")
	s := wantKeys(t, "source", r.body["sources"].([]any)[0], sourceKeys...)
	v := s["volume"].(map[string]any)
	c := s["capabilities"].(map[string]any)
	point := filepath.Join(e.base, "Fotos")
	if s["state"] != "online" || s["mount_point"] != point || s["path"] != point || s["rel_root"] != "" ||
		v["kind"] != "path" || v["id"] != point || v["strong"] != false ||
		c["known"] != false || c["case_sensitive"] != false || c["stable_identity"] != false || c["time_resolution_ns"] != 2e9 {
		t.Fatalf("portable source = %s", r.raw)
	}

	for _, p := range []string{e.base, filepath.Join(e.base, "Fotos", "2004")} {
		e.wantStatus(e.command(CommandAddSource, map[string]string{"handle": e.svc.handle(p)}), http.StatusConflict, domain.CodeSourceExists)
	}

	// After a restart nothing has been opened yet; a refresh finds the
	// source at its folder again.
	restarted, err := New(e.st, fsaccess.NewPortable(), config.Sources{AllowedRoots: []string{e.base}}, fixedClock{testNow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`UPDATE sources SET state = 'offline', mount_point = NULL`); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.Get(context.Background(), "fotos"); err != nil || got.State != StateOnline || got.MountPoint != point {
		t.Fatalf("after restart = %+v, %v", got, err)
	}
}
