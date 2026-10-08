package cleanup

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/fsaccess/instrument"
)

// zfsSourceView is what the interface reads of a source in GET /api/sources
// to add the snapshot note to a purge's report: its filesystem type, and
// its quarantine's amount.
type zfsSourceView struct {
	ID     string `json:"id"`
	Volume struct {
		FSType string `json:"fs_type"`
	} `json:"volume"`
	Quarantine struct {
		Files int64 `json:"files"`
		Bytes int64 `json:"bytes"`
	} `json:"quarantine"`
}

// zfsSource reads source id from GET /api/sources.
func zfsSource(t *testing.T, w *world, id string) zfsSourceView {
	t.Helper()
	var body struct {
		Sources []zfsSourceView `json:"sources"`
	}
	w.get("/api/sources", &body)
	for _, s := range body.Sources {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("GET /api/sources lists no source %q", id)
	return zfsSourceView{}
}

// zfsHoldCheckReads makes the first read of a file in the quarantine wait
// until the returned release is called, and returns a channel closed once
// a read waits: a check that has reached it is running.
func zfsHoldCheckReads(t *testing.T, w *world) (held <-chan struct{}, release func()) {
	t.Helper()
	reached, gate := make(chan struct{}), make(chan struct{})
	var first, open sync.Once
	release = func() {
		open.Do(func() {
			close(gate)
			w.rec.SetBeforeCall(nil)
		})
	}
	t.Cleanup(release)
	w.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpReadAt && len(c.Path) > 0 && string(c.Path[0]) == ".precious-quarantine" {
			first.Do(func() {
				close(reached)
				<-gate
			})
		}
	})
	return reached, release
}

// R4.5: a purge on a ZFS source, through commands and the real executor
// on synthfs. A discarded folder (with a file in a subfolder, and a hard
// link to a photo kept outside it) and a discarded file are quarantined by
// a cleanup plan, checked, confirmed, and purged. The files are gone from
// the disk and the index, with their origin records, item folders, and
// plan folder. The action reports the files and bytes deleted, and frees
// the allocated bytes (st_blocks × 512) of the files whose last link it
// removed only: the hard-linked photo's space stays with its other name.
// The action's source and that source's fs_type are what the interface
// needs to add the snapshot note.
func TestR4_5PurgeOnZFS(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2009, 6, 7, 8, 9, 10, 0, time.UTC)
	root := w.sfs.Root("/pool")
	foto := root.Dir("Album").File("foto.jpg", 3000, at).Seed(41).Blocks(8)
	velho := root.Dir("Velho")
	velho.File("a.txt", 100, at).Seed(11).Blocks(8)
	velho.File("b.doc", 5000, at).Seed(12).Blocks(16)
	velho.Dir("sub").File("c.bin", 1000000, at).Seed(13).Blocks(24)
	velho.HardLink("link.jpg", foto)
	root.File("solto.log", 700, at).Seed(14).Blocks(8)
	root.Dir("Fica").File("x.txt", 10, at).Seed(15)
	w.add("pool", "/pool", root, "zfs")
	if n := foto.Info().Nlink; n != 2 {
		t.Fatalf("foto.jpg has %d links before the purge, want 2", n)
	}
	velhoID, soltoID, fotoID := w.id("pool", "Velho"), w.id("pool", "solto.log"), w.id("pool", "Album/foto.jpg")
	inside := map[string]string{}
	for _, p := range []string{"Velho/a.txt", "Velho/b.doc", "Velho/sub", "Velho/sub/c.bin", "Velho/link.jpg"} {
		inside[p] = w.id("pool", p)
	}
	w.decide("pool", "Velho", "discard")
	w.decide("pool", "solto.log", "discard")

	// Quarantine both items.
	p := w.plan("plan-cleanup", `{"source_id":"pool"}`)
	plan := ".precious-quarantine/" + p.Action.ID
	wantItems(t, p.Items,
		"mkdir planned -> .precious-quarantine",
		"mkdir planned -> "+plan,
		"mkdir planned -> "+plan+"/1",
		"rename planned Velho -> "+plan+"/1/Velho",
		"record planned -> "+plan+"/1.json",
		"mkdir planned -> "+plan+"/2",
		"rename planned solto.log -> "+plan+"/2/solto.log",
		"record planned -> "+plan+"/2.json")
	if a := w.run(p.Action.ID); a.State != "done" || a.Entries["done"] != 2 {
		t.Fatalf("cleanup %s, entries %v; want done with 2 items", a.State, a.Entries)
	}
	velhoAt, soltoAt := plan+"/1/Velho", plan+"/2/solto.log"
	if s := zfsSource(t, w, "pool"); s.Quarantine.Files != 5 || s.Quarantine.Bytes != 1008800 {
		t.Fatalf("quarantine of the source %+v, want 5 files, 1008800 bytes", s.Quarantine)
	}

	// check-purge takes only quarantined top items.
	for _, id := range []string{w.id("pool", "Fica/x.txt"), w.id("pool", "Fica"), inside["Velho/a.txt"], fotoID} {
		w.refuse(http.StatusBadRequest, "invalid_request", "check-purge", `{"entry_ids":`+ids(id)+`}`)
	}

	// While a check of the source runs, a second one, its confirmations,
	// and its purge are refused with check_running.
	held, release := zfsHoldCheckReads(t, w)
	var started checkStarted
	decode(t, w.ok(http.StatusAccepted, "check-purge", `{"entry_ids":`+ids(velhoID, soltoID)+`}`), &started)
	check := started.CheckID
	select {
	case <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("the check never read a quarantined file")
	}
	if c := w.check(check); c.State != "running" || c.Allowed {
		t.Fatalf("check %s, allowed %v while it reads; want running, not allowed", c.State, c.Allowed)
	}
	w.refuse(http.StatusConflict, "check_running", "check-purge", `{"entry_ids":`+ids(soltoID)+`}`)
	w.refuse(http.StatusConflict, "check_running", "confirm-purge",
		fmt.Sprintf(`{"check_id":%q,"group":"likely_junk"}`, check))
	w.refuse(http.StatusConflict, "check_running", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, check))
	release()
	w.idle()

	c := w.check(check)
	if c.State != checkReady || c.Items != 2 || c.Allowed {
		t.Fatalf("check %s with %d items, allowed %v; want ready with 2, not allowed", c.State, c.Items, c.Allowed)
	}
	if got, want := c.Counts.Verdict, map[string]amount{"safe": {1, 3000}, "copy_offline": {}, "unique": {4, 1005800},
		"unreadable": {}, "opaque_archive": {}, "no_content": {2, 8192}}; !maps.Equal(got, want) {
		t.Fatalf("verdicts %v, want %v", got, want)
	}
	if got, want := c.Counts.Class, map[string]amount{"possibly_valuable": {2, 5100}, "likely_junk": {},
		"uncertain": {2, 1000700}}; !maps.Equal(got, want) {
		t.Fatalf("classes %v, want %v", got, want)
	}
	if c.Confirmed != (amount{}) || c.Unconfirmed != (amount{4, 1005800}) || c.JunkConfirmed {
		t.Fatalf("confirmed %+v, unconfirmed %+v, junk confirmed %v; want none, 4 files and 1005800 bytes, false",
			c.Confirmed, c.Unconfirmed, c.JunkConfirmed)
	}
	var (
		files  []string
		unique []string
	)
	for _, f := range w.checkFiles(check, "") {
		s := fmt.Sprintf("%s %s %d", f.Path, f.Verdict, f.Size)
		if f.Copy != nil {
			s += fmt.Sprintf(" copy %s:%s hard_link=%v", f.Copy.SourceID, f.Copy.Path, f.Copy.HardLink)
		}
		files = append(files, s)
		if f.Verdict == "unique" {
			unique = append(unique, f.ID)
		}
	}
	if got, want := strings.Join(files, "\n"), strings.Join([]string{
		velhoAt + " no_content 4096",
		velhoAt + "/a.txt unique 100",
		velhoAt + "/b.doc unique 5000",
		velhoAt + "/link.jpg safe 3000 copy pool:Album/foto.jpg hard_link=true",
		velhoAt + "/sub no_content 4096",
		velhoAt + "/sub/c.bin unique 1000000",
		soltoAt + " unique 700",
	}, "\n"); got != want {
		t.Fatalf("check files:\n%s\nwant:\n%s", got, want)
	}
	var confirmed confirmResponse
	decode(t, w.ok(http.StatusOK, "confirm-purge", fmt.Sprintf(`{"check_id":%q,"file_ids":%s}`, check,
		ids(unique...))), &confirmed)
	if c := confirmed.Check; !c.Allowed || c.Confirmed != (amount{4, 1005800}) || c.Unconfirmed != (amount{}) {
		t.Fatalf("after confirming the unique files: allowed %v, confirmed %+v, unconfirmed %+v; want allowed, 4 files and 1005800 bytes, none",
			c.Allowed, c.Confirmed, c.Unconfirmed)
	}

	// The purge.
	pp := w.plan("plan-purge", fmt.Sprintf(`{"check_id":%q}`, check))
	wantItems(t, pp.Items,
		"verify planned",
		"purge planned "+velhoAt,
		"purge planned "+soltoAt,
		"rmdir planned "+plan)
	if pp.Action.Kind != "purge" || pp.Action.CheckID == nil || *pp.Action.CheckID != check ||
		pp.Action.Bytes != 1008800 || pp.Action.Files != 5 || pp.Action.Entries["planned"] != 2 {
		t.Fatalf("planned purge %+v; want a purge of check %s, 5 files, 1008800 bytes, 2 entries planned",
			pp.Action, check)
	}
	a := w.run(pp.Action.ID)
	wantItems(t, w.items(pp.Action.ID, ""),
		"verify done",
		"purge done "+velhoAt,
		"purge done "+soltoAt,
		"rmdir done "+plan)
	// Freed: a.txt 8, b.doc 16, c.bin 24, and solto.log 8 blocks of 512
	// bytes; link.jpg frees nothing, foto.jpg still holds its blocks.
	const freed = (8 + 16 + 24 + 8) * 512
	if a.State != "done" || a.DeletedFiles != 5 || a.DeletedBytes != 100+5000+1000000+3000+700 ||
		a.FreedBytes != freed {
		t.Fatalf("purge %s: deleted %d files, %d bytes, freed %d; want done, 5 files, %d bytes, %d freed", a.State,
			a.DeletedFiles, a.DeletedBytes, a.FreedBytes, 100+5000+1000000+3000+700, freed)
	}
	if a.Entries["done"] != 2 || a.Counts["done"] != 4 || len(a.Entries) == 0 {
		t.Fatalf("purge entries %v, counts %v; want 2 entries done, 4 items done", a.Entries, a.Counts)
	}
	for k, n := range a.Entries {
		if k != "done" && n != 0 {
			t.Fatalf("purge entries %v; want only done", a.Entries)
		}
	}

	// What the interface needs for the ZFS snapshot note: the action's
	// source, and that source's filesystem type.
	var report struct {
		SourceID string `json:"source_id"`
	}
	w.get("/api/history/"+pp.Action.ID, &report)
	s := zfsSource(t, w, report.SourceID)
	if report.SourceID != "pool" || s.Volume.FSType != "zfs" {
		t.Fatalf("purge of source %q, whose fs_type is %q; want pool, zfs", report.SourceID, s.Volume.FSType)
	}
	if s.Quarantine.Files != 0 || s.Quarantine.Bytes != 0 {
		t.Fatalf("quarantine of the source after the purge %+v, want empty", s.Quarantine)
	}

	// Gone from the disk and the index: the items, their records and item
	// folders, and the plan folder. The quarantine folder stays.
	for _, rel := range []string{velhoAt + "/a.txt", velhoAt + "/sub/c.bin", velhoAt + "/link.jpg", velhoAt, soltoAt,
		plan + "/1.json", plan + "/1", plan + "/2.json", plan + "/2", plan} {
		if w.exists("pool", rel) || w.has("pool", rel) {
			t.Errorf("%s is still there after the purge", rel)
		}
	}
	for _, id := range append([]string{velhoID, soltoID}, slices.Collect(maps.Values(inside))...) {
		if n := w.count(`SELECT count(*) FROM entries WHERE id = ?`, id); n != 0 {
			t.Errorf("entry %s is still indexed after the purge", id)
		}
	}
	if n := w.count(`SELECT count(*) FROM entries WHERE source_id = 'pool' AND path >= ? AND path < ?`,
		[]byte(plan), []byte(plan+"\xff")); n != 0 {
		t.Errorf("%d entries still indexed at or below %s", n, plan)
	}
	if !w.exists("pool", ".precious-quarantine") || !w.has("pool", ".precious-quarantine") {
		t.Error("the quarantine folder is gone after the purge")
	}
	// The photo outside keeps its data, now through its one name.
	if !w.exists("pool", "Album/foto.jpg") || !w.has("pool", "Album/foto.jpg") {
		t.Fatal("Album/foto.jpg is gone after the purge")
	}
	if n := foto.Info().Nlink; n != 1 {
		t.Fatalf("foto.jpg has %d links after the purge, want 1", n)
	}
	if path, state := w.pathOf(fotoID); path != "Album/foto.jpg" || state == "missing" {
		t.Fatalf("foto.jpg indexed at %q (%s) after the purge", path, state)
	}
}
