package cleanup

import (
	"archive/zip"
	"bytes"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"precious/internal/fsaccess/synthfs"
)

// photoGate is the R4.7 world: on the ext4 source casa, a cleanup plan has
// quarantined three discarded items, in this order:
//
//  1. the folder "Fotos 2005", holding the unique photo praia.jpg (2000
//     bytes) and the unique system junk Thumbs.db (512 bytes);
//  2. the folder Temp, holding the unique temporary files x.tmp (300 bytes)
//     and ~WRL0003.tmp (400 bytes);
//  3. the unique photo festa.jpg (3000 bytes), on its own.
//
// Salvas is a folder outside the quarantine to move photos into.
type photoGate struct {
	w *world
	// plan is the quarantine's plan folder, ".precious-quarantine/<id>".
	plan string
	// The entry IDs of the three items, of the files in them, and of Salvas.
	fotos, temp, festa, praia, thumbs, xtmp, wrl, salvas string
	// The quarantine paths of the items and of the files in them.
	fotosAt, tempAt, festaAt, praiaAt, thumbsAt, xtmpAt, wrlAt string
}

func newPhotoGate(t *testing.T) *photoGate {
	t.Helper()
	w := newWorld(t)
	at := time.Date(2005, 3, 4, 5, 6, 7, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		f := root.Dir("Fotos 2005")
		f.File("praia.jpg", 2000, at).Seed(21)
		f.File("Thumbs.db", 512, at).Seed(22)
		tmp := root.Dir("Temp")
		tmp.File("x.tmp", 300, at).Seed(23)
		tmp.File("~WRL0003.tmp", 400, at).Seed(24)
		root.File("festa.jpg", 3000, at).Seed(25)
		root.Dir("Salvas")
	})
	g := &photoGate{w: w, fotos: w.id("casa", "Fotos 2005"), temp: w.id("casa", "Temp"),
		festa: w.id("casa", "festa.jpg"), praia: w.id("casa", "Fotos 2005/praia.jpg"),
		thumbs: w.id("casa", "Fotos 2005/Thumbs.db"), xtmp: w.id("casa", "Temp/x.tmp"),
		wrl: w.id("casa", "Temp/~WRL0003.tmp"), salvas: w.id("casa", "Salvas")}
	w.decide("casa", "Fotos 2005", "discard")
	w.decide("casa", "Temp", "discard")
	w.decide("casa", "festa.jpg", "discard")
	p := w.plan("plan-cleanup", `{"source_id":"casa"}`)
	g.plan = ".precious-quarantine/" + p.Action.ID
	if a := w.run(p.Action.ID); a.State != "done" || a.Entries["done"] != 3 {
		t.Fatalf("cleanup %s, entries %v; want done with 3 items", a.State, a.Entries)
	}
	g.fotosAt = g.plan + "/1/Fotos 2005"
	g.praiaAt, g.thumbsAt = g.fotosAt+"/praia.jpg", g.fotosAt+"/Thumbs.db"
	g.tempAt = g.plan + "/2/Temp"
	g.xtmpAt, g.wrlAt = g.tempAt+"/x.tmp", g.tempAt+"/~WRL0003.tmp"
	g.festaAt = g.plan + "/3/festa.jpg"
	for id, want := range map[string]string{g.fotos: g.fotosAt, g.temp: g.tempAt, g.festa: g.festaAt,
		g.praia: g.praiaAt, g.thumbs: g.thumbsAt, g.xtmp: g.xtmpAt, g.wrl: g.wrlAt} {
		if path, state := w.pathOf(id); path != want || state == "missing" {
			t.Fatalf("entry %s at %q (%s), want %q", id, path, state, want)
		}
	}
	return g
}

// photoGateWant is a Check as R4.7 expects it: the non-empty buckets by
// verdict and class (every other bucket is zero), the confirmed and
// unconfirmed amounts, the junk group's confirmation, and the gate.
type photoGateWant struct {
	items                  int64
	verdict, class         map[string]amount
	confirmed, unconfirmed amount
	junk, allowed          bool
}

// photoGateCheck fails unless c is a ready check of casa reading want.
func photoGateCheck(t *testing.T, what string, c checkJSON, want photoGateWant) {
	t.Helper()
	full := func(keys []string, nonzero map[string]amount) map[string]amount {
		m := make(map[string]amount, len(keys))
		for _, k := range keys {
			m[k] = nonzero[k]
		}
		return m
	}
	if c.State != checkReady || c.SourceID != "casa" || c.StaleReason != nil || c.Items != want.items {
		t.Fatalf("%s: check %s of %s, stale reason %v, %d items; want ready of casa with %d items", what, c.State,
			c.SourceID, c.StaleReason, c.Items, want.items)
	}
	if v := full(verdicts, want.verdict); !maps.Equal(c.Counts.Verdict, v) {
		t.Fatalf("%s: verdicts %v, want %v", what, c.Counts.Verdict, v)
	}
	if k := full(classes, want.class); !maps.Equal(c.Counts.Class, k) {
		t.Fatalf("%s: classes %v, want %v", what, c.Counts.Class, k)
	}
	if c.Confirmed != want.confirmed || c.Unconfirmed != want.unconfirmed || c.JunkConfirmed != want.junk ||
		c.Allowed != want.allowed {
		t.Fatalf("%s: confirmed %+v, unconfirmed %+v, junk confirmed %v, allowed %v; want %+v, %+v, %v, %v", what,
			c.Confirmed, c.Unconfirmed, c.JunkConfirmed, c.Allowed, want.confirmed, want.unconfirmed, want.junk,
			want.allowed)
	}
}

// photoGateFiles fails unless the check's files, read with query, are
// want, each as "path verdict class size", with " confirmed" when it is.
// Each is the record of the entry at its path, with no copy.
func photoGateFiles(t *testing.T, w *world, check, query string, want ...string) {
	t.Helper()
	var got []string
	for _, f := range w.checkFiles(check, query) {
		if f.EntryID == nil || *f.EntryID != w.id("casa", f.Path) || f.Member != nil || f.Copy != nil {
			t.Fatalf("file %s of check %s: entry %v, member %v, copy %v; want the entry at %q, no member, no copy",
				f.ID, check, f.EntryID, f.Member, f.Copy, f.Path)
		}
		s := fmt.Sprintf("%s %s", f.Path, f.Verdict)
		if f.Class != nil {
			s += " " + *f.Class
		}
		s += fmt.Sprintf(" %d", f.Size)
		if f.Confirmed {
			s += " confirmed"
		}
		got = append(got, s)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("files of check %s?%s:\n  %s\nwant:\n  %s", check, query, strings.Join(got, "\n  "),
			strings.Join(want, "\n  "))
	}
}

// photoGateFileID is the ID of the check's file at path.
func photoGateFileID(t *testing.T, w *world, check, path string) string {
	t.Helper()
	for _, f := range w.checkFiles(check, "") {
		if f.Path == path {
			return f.ID
		}
	}
	t.Fatalf("check %s records no %q", check, path)
	return ""
}

// photoGateRefused fails unless plan-purge of check answers 409
// purge_not_allowed naming exactly the files at names, in that order.
func photoGateRefused(t *testing.T, w *world, check string, names ...string) {
	t.Helper()
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decode(t, w.refuse(http.StatusConflict, "purge_not_allowed", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, check)),
		&e)
	want := "these files have no verified copy and are not confirmed yet: " + strings.Join(names, ", ") +
		"; confirm them, or take them out of the set and check it again"
	if e.Error.Message != want {
		t.Fatalf("purge_not_allowed message\n  %s\nwant\n  %s", e.Error.Message, want)
	}
}

// photoGateConfirm sends confirm-purge with body (the check_id added),
// which must answer 200, and returns the Check it answers.
func photoGateConfirm(t *testing.T, w *world, check, body string) checkJSON {
	t.Helper()
	var r confirmResponse
	decode(t, w.ok(http.StatusOK, "confirm-purge", fmt.Sprintf(`{"check_id":%q,%s}`, check, body)), &r)
	if r.Check.ID != check {
		t.Fatalf("confirm-purge answered check %s, want %s", r.Check.ID, check)
	}
	return r.Check
}

// photoGateStale fails unless check is stale since after, its index
// changed, and plan-purge and confirm-purge refuse it with check_stale.
func photoGateStale(t *testing.T, w *world, check, after string) {
	t.Helper()
	if c := w.check(check); c.State != checkStale || c.StaleReason == nil || *c.StaleReason != "index_changed" ||
		c.Allowed {
		t.Fatalf("after %s the check is %s (reason %v, allowed %v), want stale with reason index_changed", after,
			c.State, c.StaleReason, c.Allowed)
	}
	w.refuse(http.StatusConflict, "check_stale", "plan-purge", fmt.Sprintf(`{"check_id":%q}`, check))
	w.refuse(http.StatusConflict, "check_stale", "confirm-purge",
		fmt.Sprintf(`{"check_id":%q,"group":"likely_junk"}`, check))
}

// photoGatePurge plans and runs the purge of check, which must hold want
// as its items, and fails unless it ends done having deleted files and
// bytes, with the plan folder gone from the disk and the index.
func photoGatePurge(t *testing.T, g *photoGate, check string, files, bytes int64, want ...string) {
	t.Helper()
	w := g.w
	p := w.plan("plan-purge", fmt.Sprintf(`{"check_id":%q}`, check))
	if p.Action.Kind != "purge" || p.Action.CheckID == nil || *p.Action.CheckID != check {
		t.Fatalf("plan-purge planned %s of check %v, want a purge of check %s", p.Action.Kind, p.Action.CheckID, check)
	}
	planned := make([]string, len(want))
	done := make([]string, len(want))
	for i, s := range want {
		op, rest, _ := strings.Cut(s, " ")
		planned[i], done[i] = strings.TrimSpace(op+" planned "+rest), strings.TrimSpace(op+" done "+rest)
	}
	wantItems(t, p.Items, planned...)
	a := w.run(p.Action.ID)
	if a.State != "done" || a.DeletedFiles != files || a.DeletedBytes != bytes {
		t.Fatalf("purge %s deleted %d files, %d bytes; want done, %d files, %d bytes", a.State, a.DeletedFiles,
			a.DeletedBytes, files, bytes)
	}
	wantItems(t, w.items(p.Action.ID, ""), done...)
	if w.exists("casa", g.plan) || w.has("casa", g.plan) {
		t.Fatalf("plan folder %s is still there after the purge", g.plan)
	}
	if !w.exists("casa", ".precious-quarantine") {
		t.Fatalf("the quarantine folder is gone after the purge")
	}
}

// R4.7: a checked set holding unique photos and unique system junk. The
// purge is refused, naming every unconfirmed file, then, once the junk
// group is confirmed, naming only the photos; a junk file cannot be
// confirmed on its own; after each photo is confirmed the purge is
// allowed, and deletes the set.
func TestR4_7UniquePhotosBlockThePurge(t *testing.T) {
	g := newPhotoGate(t)
	w := g.w
	check, c := w.checkPurge(g.fotos, g.temp, g.festa)
	photoGateCheck(t, "the check", c, photoGateWant{items: 3,
		verdict:   map[string]amount{"no_content": {2, 8192}, "unique": {5, 6212}},
		class:     map[string]amount{"likely_junk": {3, 1212}, "possibly_valuable": {2, 5000}},
		confirmed: amount{}, unconfirmed: amount{5, 6212}})
	photoGateFiles(t, w, check, "",
		g.fotosAt+" no_content 4096",
		g.thumbsAt+" unique likely_junk 512",
		g.praiaAt+" unique possibly_valuable 2000",
		g.tempAt+" no_content 4096",
		g.xtmpAt+" unique likely_junk 300",
		g.wrlAt+" unique likely_junk 400",
		g.festaAt+" unique possibly_valuable 3000")
	photoGateFiles(t, w, check, "class=likely_junk",
		g.thumbsAt+" unique likely_junk 512",
		g.xtmpAt+" unique likely_junk 300",
		g.wrlAt+" unique likely_junk 400")
	photoGateRefused(t, w, check, g.thumbsAt, g.praiaAt, g.xtmpAt, g.wrlAt, g.festaAt)

	// The junk group: the photos still block the purge, and only they are
	// named.
	c = photoGateConfirm(t, w, check, `"group":"likely_junk"`)
	afterJunk := photoGateWant{items: 3,
		verdict:   map[string]amount{"no_content": {2, 8192}, "unique": {5, 6212}},
		class:     map[string]amount{"likely_junk": {3, 1212}, "possibly_valuable": {2, 5000}},
		confirmed: amount{3, 1212}, unconfirmed: amount{2, 5000}, junk: true}
	photoGateCheck(t, "after the junk group", c, afterJunk)
	photoGateCheck(t, "read after the junk group", w.check(check), afterJunk)
	photoGateFiles(t, w, check, "confirmed=1",
		g.thumbsAt+" unique likely_junk 512 confirmed",
		g.xtmpAt+" unique likely_junk 300 confirmed",
		g.wrlAt+" unique likely_junk 400 confirmed")
	photoGateFiles(t, w, check, "verdict=unique&confirmed=0",
		g.praiaAt+" unique possibly_valuable 2000",
		g.festaAt+" unique possibly_valuable 3000")
	photoGateRefused(t, w, check, g.praiaAt, g.festaAt)

	// A junk file, a folder, and a file of no check need no confirmation of
	// their own; a refusal confirms nothing, not even the photo sent with
	// it.
	thumbs, folder := photoGateFileID(t, w, check, g.thumbsAt), photoGateFileID(t, w, check, g.fotosAt)
	praia, festa := photoGateFileID(t, w, check, g.praiaAt), photoGateFileID(t, w, check, g.festaAt)
	xtmp := photoGateFileID(t, w, check, g.xtmpAt)
	for _, list := range [][]string{{thumbs}, {folder}, {"999999"}, {praia, xtmp}} {
		w.refuse(http.StatusBadRequest, "invalid_request", "confirm-purge",
			fmt.Sprintf(`{"check_id":%q,"file_ids":%s}`, check, ids(list...)))
	}
	photoGateCheck(t, "after the refused confirmations", w.check(check), afterJunk)

	// Each photo, one by one.
	c = photoGateConfirm(t, w, check, `"file_ids":`+ids(praia))
	photoGateCheck(t, "after praia.jpg", c, photoGateWant{items: 3,
		verdict:   map[string]amount{"no_content": {2, 8192}, "unique": {5, 6212}},
		class:     map[string]amount{"likely_junk": {3, 1212}, "possibly_valuable": {2, 5000}},
		confirmed: amount{4, 3212}, unconfirmed: amount{1, 3000}, junk: true})
	photoGateRefused(t, w, check, g.festaAt)
	c = photoGateConfirm(t, w, check, `"file_ids":`+ids(festa))
	allowed := photoGateWant{items: 3,
		verdict:   map[string]amount{"no_content": {2, 8192}, "unique": {5, 6212}},
		class:     map[string]amount{"likely_junk": {3, 1212}, "possibly_valuable": {2, 5000}},
		confirmed: amount{5, 6212}, junk: true, allowed: true}
	photoGateCheck(t, "after festa.jpg", c, allowed)
	photoGateCheck(t, "read after festa.jpg", w.check(check), allowed)
	photoGateFiles(t, w, check, "confirmed=0",
		g.fotosAt+" no_content 4096",
		g.tempAt+" no_content 4096")

	photoGatePurge(t, g, check, 5, 6212,
		"verify",
		"purge "+g.fotosAt,
		"purge "+g.tempAt,
		"purge "+g.festaAt,
		"rmdir "+g.plan)
	for _, id := range []string{g.fotos, g.temp, g.festa, g.praia, g.thumbs, g.xtmp, g.wrl} {
		if n := w.count(`SELECT count(*) FROM entries WHERE id = ?`, id); n != 0 {
			t.Errorf("entry %s still indexed after the purge", id)
		}
	}
}

// R4.7: restoring the items that hold the photos takes them out of the set.
// The check becomes stale, and the smaller set, checked again, needs only
// its junk group confirmed.
func TestR4_7RestoringThePhotosAllowsThePurge(t *testing.T) {
	g := newPhotoGate(t)
	w := g.w
	check, _ := w.checkPurge(g.fotos, g.temp, g.festa)
	photoGateConfirm(t, w, check, `"group":"likely_junk"`)
	photoGateRefused(t, w, check, g.praiaAt, g.festaAt)

	r := w.plan("plan-restore", `{"entry_ids":`+ids(g.fotos, g.festa)+`}`)
	wantItems(t, r.Items,
		"rename planned "+g.fotosAt+" -> Fotos 2005",
		"unlink planned "+g.plan+"/1.json",
		"rmdir planned "+g.plan+"/1",
		"rename planned "+g.festaAt+" -> festa.jpg",
		"unlink planned "+g.plan+"/3.json",
		"rmdir planned "+g.plan+"/3")
	if a := w.run(r.Action.ID); a.State != "done" || a.Entries["done"] != 2 {
		t.Fatalf("restore %s, entries %v; want done with 2 items", a.State, a.Entries)
	}
	for id, want := range map[string]string{g.fotos: "Fotos 2005", g.praia: "Fotos 2005/praia.jpg",
		g.festa: "festa.jpg"} {
		if path, _ := w.pathOf(id); path != want {
			t.Fatalf("entry %s at %q after the restore, want %q", id, path, want)
		}
	}
	photoGateStale(t, w, check, "the restore")

	again, c := w.checkPurge(g.temp)
	if again == check {
		t.Fatalf("check-purge answered the stale check %s", check)
	}
	junkOnly := photoGateWant{items: 1,
		verdict:     map[string]amount{"no_content": {1, 4096}, "unique": {2, 700}},
		class:       map[string]amount{"likely_junk": {2, 700}},
		unconfirmed: amount{2, 700}}
	photoGateCheck(t, "the smaller set", c, junkOnly)
	photoGateRefused(t, w, again, g.xtmpAt, g.wrlAt)
	c = photoGateConfirm(t, w, again, `"group":"likely_junk"`)
	junkOnly.confirmed, junkOnly.unconfirmed, junkOnly.junk, junkOnly.allowed = amount{2, 700}, amount{}, true, true
	photoGateCheck(t, "the smaller set after its junk group", c, junkOnly)

	photoGatePurge(t, g, again, 2, 700,
		"verify",
		"purge "+g.tempAt,
		"rmdir "+g.plan)
	for _, p := range []string{"Fotos 2005/praia.jpg", "Fotos 2005/Thumbs.db", "festa.jpg"} {
		if !w.exists("casa", p) || !w.has("casa", p) {
			t.Errorf("%s is gone after the purge of the smaller set", p)
		}
	}
}

// R4.7: moving the photos out of the quarantine, each by an individual
// move, takes them out of the set: the check becomes stale, and the set
// checked again holds only junk, which its group confirms. The purge then
// also sweeps the empty item folder and the record festa.jpg left.
func TestR4_7MovingThePhotosOutAllowsThePurge(t *testing.T) {
	g := newPhotoGate(t)
	w := g.w
	check, _ := w.checkPurge(g.fotos, g.temp, g.festa)
	photoGateConfirm(t, w, check, `"group":"likely_junk"`)
	photoGateRefused(t, w, check, g.praiaAt, g.festaAt)

	for _, m := range []struct{ id, from, to string }{
		{g.praia, g.praiaAt, "Salvas/praia.jpg"},
		{g.festa, g.festaAt, "Salvas/festa.jpg"},
	} {
		p := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, m.id, g.salvas))
		wantItems(t, p.Items, "rename planned "+m.from+" -> "+m.to)
		if a := w.run(p.Action.ID); a.State != "done" {
			t.Fatalf("moving %s out ended %s", m.from, a.State)
		}
		if path, _ := w.pathOf(m.id); path != m.to {
			t.Fatalf("entry %s at %q after its move, want %q", m.id, path, m.to)
		}
	}
	photoGateStale(t, w, check, "the moves")

	again, c := w.checkPurge(g.fotos, g.temp)
	junkOnly := photoGateWant{items: 2,
		verdict:     map[string]amount{"no_content": {2, 8192}, "unique": {3, 1212}},
		class:       map[string]amount{"likely_junk": {3, 1212}},
		unconfirmed: amount{3, 1212}}
	photoGateCheck(t, "the set without the photos", c, junkOnly)
	photoGateRefused(t, w, again, g.thumbsAt, g.xtmpAt, g.wrlAt)
	c = photoGateConfirm(t, w, again, `"group":"likely_junk"`)
	junkOnly.confirmed, junkOnly.unconfirmed, junkOnly.junk, junkOnly.allowed = amount{3, 1212}, amount{}, true, true
	photoGateCheck(t, "the set without the photos after its junk group", c, junkOnly)

	photoGatePurge(t, g, again, 3, 1212,
		"verify",
		"purge "+g.fotosAt,
		"purge "+g.tempAt,
		"rmdir "+g.plan+"/3",
		"unlink "+g.plan+"/3.json",
		"rmdir "+g.plan)
	for _, p := range []string{"Salvas/praia.jpg", "Salvas/festa.jpg"} {
		if !w.exists("casa", p) || !w.has("casa", p) {
			t.Errorf("%s is gone after the purge", p)
		}
	}
}

// G12: the check's counts add up to what is in the set. A complete
// archive is its members (C2): its own record stays no_content, listed
// with the folders, but counts no bytes, so the zip's bytes are not counted
// on top of its members'.
func TestR4_7CountsOfASetHoldingAZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"a.txt": strings.Repeat("alfa ", 40), "b.txt": strings.Repeat("beta ", 60)} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t)
	at := time.Date(2011, 2, 3, 4, 5, 6, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		root.Dir("Velho").File("antigo.zip", 0, at).Content(buf.Bytes())
	})
	w.decide("casa", "Velho", "discard")
	velho := staleQuarantineVelho(w)
	_, c := w.checkPurge(velho)
	if c.State != "ready" {
		t.Fatalf("check %+v", c)
	}
	files := w.checkFiles(c.ID, "verdict=no_content")
	var folder int64
	for _, f := range files {
		if f.Kind == "directory" {
			folder += f.Size
		}
	}
	if len(files) != 2 || folder == 0 {
		t.Fatalf("no_content files %+v; want the folder Velho and the zip", files)
	}
	if got, want := c.Counts.Verdict, map[string]amount{"safe": {}, "copy_offline": {}, "unreadable": {},
		"opaque_archive": {}, "unique": {2, 200 + 300}, "no_content": {2, folder}}; !maps.Equal(got, want) {
		t.Errorf("counts by verdict %v; want %v", got, want)
	}
}

// G15: each file of a check says whether its set item could be read. An
// item holding a folder the scan could not read is never deleted (D11), so
// the interface offers no confirmation for its files.
func TestR4_7FilesOfAnUnreadableItem(t *testing.T) {
	w := newWorld(t)
	at := time.Date(2011, 2, 3, 4, 5, 6, 0, time.UTC)
	w.disk("casa", "/casa", func(root *synthfs.Node) {
		v := root.Dir("Velho")
		v.File("carta.txt", 300, at).Seed(1)
		v.Dir("privado").Unreadable()
		root.File("solto.bin", 200, at).Seed(2)
	})
	w.decide("casa", "Velho", "discard")
	w.decide("casa", "solto.bin", "discard")
	velho := staleQuarantineVelho(w)
	solto := w.id("casa", ".precious-quarantine/1/2/solto.bin")
	_, c := w.checkPurge(velho, solto)
	if c.State != "ready" || c.Items != 2 {
		t.Fatalf("check %+v", c)
	}
	got := map[string]bool{}
	for _, f := range w.checkFiles(c.ID, "") {
		got[f.Path] = f.ItemReadable
	}
	want := map[string]bool{
		".precious-quarantine/1/1/Velho":           false,
		".precious-quarantine/1/1/Velho/carta.txt": false,
		".precious-quarantine/1/1/Velho/privado":   false,
		".precious-quarantine/1/2/solto.bin":       true,
	}
	if !maps.Equal(got, want) {
		t.Errorf("item_readable by path %v; want %v", got, want)
	}
}
