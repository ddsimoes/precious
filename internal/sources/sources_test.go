package sources

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
)

func wantCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if err == nil || domain.CodeOf(err) != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func (e *env) column(id domain.SourceID, col string) sql.NullString {
	e.t.Helper()
	var v sql.NullString
	if err := e.st.Reader().QueryRow(`SELECT CAST(`+col+` AS TEXT) FROM sources WHERE id = ?`, string(id)).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// An added source is online on its volume's mount, with its folder relative
// to the volume, and Open opens its root there.
func TestOpenOnline(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", "FOTOS"), "Fotos")
	point := filepath.Join(e.base, "usb")
	src := e.add(filepath.Join(point, "Fotos"), "")

	if src.ID != "fotos" || src.Label != "Fotos" || src.State != StateOnline || src.StateReason != "" ||
		src.MountPoint != point || string(src.RelRoot) != "Fotos" || src.Volume != uuidVolume("u-1", "FOTOS") ||
		src.Caps != ext4Caps || src.RootEntry == 0 || src.ScanGen != 0 || src.LastScanAt != nil {
		t.Fatalf("added source = %+v", src)
	}
	op, err := e.svc.Open(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Root.Close()
	if op.AbsRoot != filepath.Join(point, "Fotos") || string(op.Root.Self().Name) != "Fotos" || op.Source.State != StateOnline {
		t.Fatalf("Open = %+v", op)
	}
	if got := e.get(src.ID); got.State != StateOnline || got.MountPoint != point {
		t.Fatalf("after Open = %+v", got)
	}
	_, err = e.svc.Open(context.Background(), "nope")
	wantCode(t, err, domain.CodeUnknownSource)
}

// The capabilities column holds the Interfaces JSON, and a refresh rewrites
// it when the filesystem reports others.
func TestCapabilitiesJSONWritten(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", ""))
	src := e.add(filepath.Join(e.base, "usb"), "Card")
	if got := e.column(src.ID, "capabilities").String; got != ext4CapsJSON {
		t.Fatalf("capabilities = %s, want %s", got, ext4CapsJSON)
	}
	ro := fatCaps
	ro.ReadOnly = true
	e.fs.SetCapabilities(dev, ro)
	e.refresh()
	want := `{"known":true,"read_only":true,"case_sensitive":false,"normalization_sensitive":true,"stable_identity":false,"local_time":true,"hard_links":false,"time_resolution_ns":2000000000,"no_replace_rename":true}`
	if got := e.column(src.ID, "capabilities").String; got != want {
		t.Fatalf("capabilities after refresh = %s, want %s", got, want)
	}
	if got := e.get(src.ID).Caps; got != ro {
		t.Fatalf("Caps = %+v, want %+v", got, ro)
	}
}

// A source whose volume is no longer mounted is offline, keeps its last
// capabilities, has no mount point, and cannot be opened.
func TestOfflineWhenVolumeGone(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	src := e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	e.fs.Unmount(dev)
	e.refresh()

	got := e.get(src.ID)
	if got.State != StateOffline || got.StateReason != ReasonNotMounted || got.MountPoint != "" || got.Caps != ext4Caps {
		t.Fatalf("after unmount = %+v", got)
	}
	if mp := e.column(src.ID, "mount_point"); mp.Valid {
		t.Fatalf("mount_point = %q, want NULL", mp.String)
	}
	_, err := e.svc.Open(context.Background(), src.ID)
	wantCode(t, err, domain.CodeSourceOffline)
}

// A source whose volume is mounted but whose root cannot be opened is
// unavailable, with the reason, at the mount point.
func TestUnavailableWhenRootUnreadable(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", ""))
	root := e.folder(dev, filepath.Join(e.base, "usb", "Fotos"))
	src := e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	root.Unreadable()
	e.refresh()

	got := e.get(src.ID)
	if got.State != StateUnavailable || got.StateReason != ReasonRootUnreadable || got.MountPoint != filepath.Join(e.base, "usb") {
		t.Fatalf("unreadable root = %+v", got)
	}
	_, err := e.svc.Open(context.Background(), src.ID)
	wantCode(t, err, domain.CodeSourceOffline)
}

// A root folder that no longer exists on a mounted volume is unavailable.
func TestUnavailableWhenRootMissing(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""))
	e.insertSource("gone", uuidVolume("u-1", ""), "Gone")
	e.refresh()
	if got := e.get("gone"); got.State != StateUnavailable || got.StateReason != ReasonRootMissing {
		t.Fatalf("missing root = %+v", got)
	}
}

// Another filesystem mounted over the source's folder is not the source:
// the source is unavailable rather than showing that filesystem.
func TestUnavailableWhenRootCovered(t *testing.T) {
	e := newEnv(t)
	e.disk("usb", uuidVolume("u-1", ""), "Fotos")
	src := e.add(filepath.Join(e.base, "usb", "Fotos"), "")
	e.fs.Root(filepath.Join(e.base, "usb", "Fotos")) // a new device mounted over it
	e.refresh()
	if got := e.get(src.ID); got.State != StateUnavailable || got.StateReason != ReasonRootCovered {
		t.Fatalf("covered root = %+v", got)
	}
}

// A different filesystem mounted where an offline source's volume was does
// not bring the source back.
func TestDifferentDiskAtOldMountPoint(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", ""))
	src := e.add(filepath.Join(e.base, "usb"), "Old disk")
	e.fs.Unmount(dev)
	e.refresh()
	other := e.fs.Root(filepath.Join(e.base, "usb")).Info().Dev
	e.fs.SetVolume(other, uuidVolume("u-2", ""))
	e.refresh()
	if got := e.get(src.ID); got.State != StateOffline || got.MountPoint != "" {
		t.Fatalf("with another disk at the old mount point = %+v", got)
	}
}

// A path volume is known by its mount point only: mounted elsewhere it is
// offline, and back at its mount point it is online again.
func TestPathVolumeOnlyAtItsMountPoint(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("share", fsaccess.Volume{})
	point := filepath.Join(e.base, "share")
	src := e.add(point, "")
	if src.Volume.Kind != fsaccess.VolumePath || src.Volume.ID != point || src.Volume.Strong || len(src.RelRoot) != 0 {
		t.Fatalf("path source = %+v", src)
	}
	e.fs.Mount(dev, filepath.Join(e.base, "elsewhere"))
	e.refresh()
	if got := e.get(src.ID); got.State != StateOffline {
		t.Fatalf("path volume mounted elsewhere = %+v, want offline", got)
	}
	e.fs.Mount(dev, point)
	e.refresh()
	if got := e.get(src.ID); got.State != StateOnline || got.MountPoint != point {
		t.Fatalf("path volume back at its mount point = %+v, want online", got)
	}
}

// Run refreshes on its own: an unmount is recorded without any request.
func TestRefreshLoop(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", ""))
	src := e.add(filepath.Join(e.base, "usb"), "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.svc.run(ctx, nil, 5*time.Millisecond)
	}()
	defer func() {
		cancel()
		<-done
	}()
	e.fs.Unmount(dev)
	deadline := time.Now().Add(10 * time.Second)
	for e.get(src.ID).State != StateOffline {
		if time.Now().After(deadline) {
			t.Fatal("the refresh loop never recorded the unmount")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Concurrent refreshes share work and all report success.
func TestConcurrentRefresh(t *testing.T) {
	e := newEnv(t)
	dev := e.disk("usb", uuidVolume("u-1", ""))
	src := e.add(filepath.Join(e.base, "usb"), "")
	e.fs.Unmount(dev)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() { errs <- e.svc.Refresh(context.Background()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := e.get(src.ID); got.State != StateOffline {
		t.Fatalf("after concurrent refreshes = %+v", got)
	}
}

// The jobs.Registry methods: the device key follows the mount, and the
// unresponsive flag keeps its earliest start until cleared.
func TestRegistry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dev := e.disk("usb", uuidVolume("u-1", ""))
	src := e.add(filepath.Join(e.base, "usb"), "")
	key, err := e.svc.DeviceKey(ctx, e.st.Reader(), src.ID)
	if err != nil || key != "dev:u-1" {
		t.Fatalf("DeviceKey = %q, %v", key, err)
	}
	moved := uuidVolume("u-1", "")
	moved.DeviceKey = "dev:8:33"
	e.fs.SetVolume(dev, moved)
	e.refresh()
	if key, err := e.svc.DeviceKey(ctx, e.st.Reader(), src.ID); err != nil || key != "dev:8:33" {
		t.Fatalf("DeviceKey after replug = %q, %v", key, err)
	}
	_, err = e.svc.DeviceKey(ctx, e.st.Reader(), "nope")
	wantCode(t, err, domain.CodeUnknownSource)

	first := testNow.Add(-time.Minute)
	for _, since := range []time.Time{first, testNow} {
		if err := e.svc.SetUnresponsive(ctx, src.ID, since); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.column(src.ID, "unresponsive_since"); got.String != fmt.Sprint(first.UnixMilli()) {
		t.Fatalf("unresponsive_since = %v, want %d", got, first.UnixMilli())
	}
	if err := e.svc.SetUnresponsive(ctx, src.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := e.column(src.ID, "unresponsive_since"); got.Valid {
		t.Fatalf("unresponsive_since = %v after clearing", got)
	}
}

// entriesSnapshot renders every entry of src with what a refresh must never
// change.
func (e *env) entriesSnapshot(src domain.SourceID) string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT e.id, e.path, e.size, e.total_bytes, e.total_files, e.mtime_ns, e.state,
		COALESCE(e.decision, ''), e.eff_decision, (SELECT count(*) FROM entry_tags t WHERE t.entry_id = e.id)
		FROM entries e WHERE e.source_id = ? ORDER BY e.id`, string(src))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var (
			id, size, bytes, files, tags int64
			mtime                        sql.NullInt64
			path                         []byte
			state, decision, eff         string
		)
		if err := rows.Scan(&id, &path, &size, &bytes, &files, &mtime, &state, &decision, &eff, &tags); err != nil {
			e.t.Fatal(err)
		}
		fmt.Fprintf(&b, "%d %q %d %d %d %d %s %s %s %d\n", id, path, size, bytes, files, mtime.Int64, state, decision, eff, tags)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return b.String()
}

// R1.16: on Linux, a source whose volume is unmounted stays browsable as
// offline, and is recognized when the volume is mounted again at a different
// path.
func TestR1_16UnmountedSourceStaysBrowsableAndIsFoundAtNewMountPoint(t *testing.T) {
	e := newEnv(t)
	e.serve()
	ctx := context.Background()
	vol := uuidVolume("66cdfab2-e862-4156-a1a3-10f060de7fa3", "FOTOS")
	dev := e.disk("usb", vol)
	oldPoint, newPoint := filepath.Join(e.base, "usb"), filepath.Join(e.base, "run-media", "FOTOS")

	// 1. Seed entries for a source, with a decision and a tag.
	e.insertSource("fotos", vol, "")
	seeded := indextest.Seed(t, e.st, indextest.Tree{Source: "fotos", Nodes: []indextest.Node{
		{Path: "2004/a.jpg", Size: 1000, FileKind: domain.FileKindImage},
		{Path: "2004/b.jpg", Size: 2000, FileKind: domain.FileKindImage},
		{Path: "notes.txt", Size: 30},
	}})
	if _, err := e.st.Writer().Exec(`UPDATE entries SET decision = 'keep', eff_decision = 'keep' WHERE id = ?`,
		int64(seeded.ID("2004/a.jpg"))); err != nil {
		t.Fatal(err)
	}
	e.tag(seeded.ID("notes.txt"), "trip")
	e.refresh()
	if got := e.get("fotos"); got.State != StateOnline || got.MountPoint != oldPoint || got.RootEntry != seeded.Root {
		t.Fatalf("seeded source = %+v", got)
	}
	before := e.entriesSnapshot("fotos")
	listed := func() map[string]any {
		t.Helper()
		r := e.getJSON("/api/sources")
		list, _ := r.body["sources"].([]any)
		if r.status != 200 || len(list) != 1 {
			t.Fatalf("GET /api/sources = %d %s", r.status, r.raw)
		}
		return list[0].(map[string]any)
	}
	wantTotals := map[string]any{"bytes": 3030.0, "files": 3.0, "dirs": 1.0}

	// 2. Vanish the volume: offline, and the entries are still served.
	e.fs.Unmount(dev)
	s := listed()
	if s["id"] != "fotos" || s["state"] != "offline" || s["state_reason"] != ReasonNotMounted || s["mount_point"] != nil ||
		s["path"] != nil || s["rel_root"] != "" ||
		s["root_entry_id"] != seeded.Root.String() || fmt.Sprint(s["totals"]) != fmt.Sprint(wantTotals) {
		t.Fatalf("offline source = %v", s)
	}
	if got := e.entriesSnapshot("fotos"); got != before {
		t.Fatalf("entries changed while offline:\n%s\nwant:\n%s", got, before)
	}
	_, err := e.svc.Open(ctx, "fotos")
	wantCode(t, err, domain.CodeSourceOffline)

	// 3. Reattach at a new mount point: the same source, online there, with
	// the same entries.
	e.fs.Mount(dev, newPoint)
	s = listed()
	if s["id"] != "fotos" || s["state"] != "online" || s["mount_point"] != newPoint || s["path"] != newPoint ||
		s["root_entry_id"] != seeded.Root.String() || fmt.Sprint(s["totals"]) != fmt.Sprint(wantTotals) {
		t.Fatalf("remounted source = %v", s)
	}
	if got := e.entriesSnapshot("fotos"); got != before {
		t.Fatalf("entries changed after remount:\n%s\nwant:\n%s", got, before)
	}
	op, err := e.svc.Open(ctx, "fotos")
	if err != nil {
		t.Fatal(err)
	}
	defer op.Root.Close()
	if op.AbsRoot != newPoint || op.Source.ID != "fotos" || op.Source.MountPoint != newPoint {
		t.Fatalf("Open after remount = %+v", op)
	}
	if n := e.count(`SELECT count(*) FROM sources`); n != 1 {
		t.Fatalf("%d sources, want 1", n)
	}
}
