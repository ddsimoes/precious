package organize

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

// R5.4 at planning (task 2.12): on the corpus, its dates seeded,
// plan-set-mtime over Viagens/2008-03 Ouro Preto plans DSCN0001–0003 to
// their capture instants, from the index's time, and refuses DSCN0004.JPG
// (dated to the month) date_too_coarse; a file already at its date counts
// in summary.unchanged; the items read with op=set_mtime; the export names
// set_mtime. Then plan-undo of the action, its items set done by hand as
// the step leaves them, plans set_mtime items back to each prev_mtime_ns,
// and refuses one whose index time changed since identity_changed.
func TestR5_4SetFileDatesPlansTheCaptureInstants(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	root, truth := corpus.BuildSynth(w.sfs, "/corpus", corpus.Corpus())
	w.add("corpus", "/corpus", root, posix)
	w.seed("corpus")
	const ouro = "Viagens/2008-03 Ouro Preto"
	capture := map[string]time.Time{}
	for _, e := range truth.Entries {
		raw, _ := e.RawPath()
		if e.Date != nil && e.Date.Effective != nil && strings.HasPrefix(string(raw), ouro+"/") {
			capture[string(raw)] = *e.Date.Effective
		}
	}
	if len(capture) != 4 {
		t.Fatalf("Ouro Preto holds %d dated photos", len(capture))
	}
	copied := at(2011, 1, 15, 10, 0, 0)

	var sum setMtimeSummary
	a, items := w.datePlan("plan-set-mtime", folders("", w.id("corpus", ouro)), &sum)
	wantItems(t, items,
		"set_mtime planned "+ouro+"/DSCN0001.JPG",
		"set_mtime planned "+ouro+"/DSCN0002.JPG",
		"set_mtime planned "+ouro+"/DSCN0003.JPG",
		"set_mtime refused date_too_coarse "+ouro+"/DSCN0004.JPG")
	if a.Kind != "set_mtime" || !a.Bulk || a.Template != nil || a.Rename || a.Destination != nil || sum.Unchanged != 0 {
		t.Fatalf("the action is %+v, summary %+v", a, sum)
	}
	for _, it := range items[:3] {
		want := capture[it.From.Path]
		if it.Mtime == nil || !it.Mtime.To.Equal(want) || it.Mtime.From == nil || !it.Mtime.From.Equal(copied) {
			t.Errorf("%s: mtime %+v, want %s from %s", it.From.Path, it.Mtime, want, copied)
		}
		if want.Nanosecond() == 0 {
			t.Errorf("%s: the capture %s has no subseconds", it.From.Path, want)
		}
		if it.Entry == nil || it.To != nil || it.Files != 1 {
			t.Errorf("%s: entry %v, to %v, files %d", it.From.Path, it.Entry, it.To, it.Files)
		}
	}
	if a.Counts["planned"] != 3 || a.Counts["refused"] != 1 || a.Files != 3 {
		t.Errorf("counts %v, files %d", a.Counts, a.Files)
	}
	if got := w.items(a.ID, "op=set_mtime"); len(got) != 4 {
		t.Errorf("op=set_mtime lists %d items", len(got))
	}
	if got := w.items(a.ID, "op=rename"); len(got) != 0 {
		t.Errorf("op=rename lists %d items", len(got))
	}
	code, csv := w.getRaw("/api/history/" + a.ID + "/export.csv")
	if code != http.StatusOK || !strings.Contains(csv, `"`+ouro+`/DSCN0001.JPG","`) ||
		strings.Count(csv, `,"set_mtime","planned",""`) != 3 || !strings.Contains(csv, `"set_mtime","refused","date_too_coarse"`) {
		t.Errorf("export %d:\n%s", code, csv)
	}

	// A file already at its date: the Canon's modification time is its
	// capture.
	canon := "Viagens/2010-07 Bahia/IMG_0101.JPG"
	_, unchanged := w.datePlan("plan-set-mtime", entries("", w.id("corpus", ouro+"/DSCN0001.JPG"), w.id("corpus", canon)), &sum)
	wantItems(t, unchanged, "set_mtime planned "+ouro+"/DSCN0001.JPG")
	if sum.Unchanged != 1 {
		t.Errorf("unchanged %d, want 1", sum.Unchanged)
	}

	// Undo, as the step leaves done items: prev_mtime_ns journaled and the
	// index at the written time. DSCN0002 was edited since.
	w.exec(`UPDATE actions SET state = 'done', started_at = 1, finished_at = 2 WHERE id = ?`, a.ID)
	w.exec(`UPDATE action_items SET state = 'done', prev_mtime_ns = ?, finished_at = 2
		WHERE action_id = ? AND state = 'planned'`, copied.UnixNano(), a.ID)
	w.exec(`UPDATE entries SET mtime_ns = (SELECT i.new_mtime_ns FROM action_items i WHERE i.action_id = ?
		AND i.entry_id = entries.id AND i.state = 'done') WHERE id IN (SELECT entry_id FROM action_items
		WHERE action_id = ? AND state = 'done')`, a.ID, a.ID)
	edited := w.id("corpus", ouro+"/DSCN0002.JPG")
	w.exec(`UPDATE entries SET mtime_ns = ? WHERE id = ?`, at(2026, 9, 1, 8, 0, 0).UnixNano(), edited)
	if got := w.action(a.ID); !got.Undo.Possible {
		t.Fatalf("the done action reads undo %+v", got.Undo)
	}
	undo, back := w.plan("plan-undo", fmt.Sprintf(`{"action_id":%q}`, a.ID))
	wantItems(t, back,
		"set_mtime planned "+ouro+"/DSCN0003.JPG",
		"set_mtime refused identity_changed "+ouro+"/DSCN0002.JPG",
		"set_mtime planned "+ouro+"/DSCN0001.JPG")
	if undo.Kind != "undo" || undo.UndoOf == nil || *undo.UndoOf != a.ID || undo.Bulk {
		t.Errorf("the undo is %+v", undo)
	}
	for _, it := range []itemJSON{back[0], back[2]} {
		want := capture[it.From.Path]
		if it.Mtime == nil || !it.Mtime.To.Equal(copied) || it.Mtime.From == nil || !it.Mtime.From.Equal(want) {
			t.Errorf("%s: undo mtime %+v, want %s from %s", it.From.Path, it.Mtime, copied, want)
		}
	}
	var reverses int
	if err := w.st.Reader().QueryRow(`SELECT count(*) FROM action_items u JOIN action_items o ON o.id = u.reverses
		WHERE u.action_id = ? AND o.action_id = ? AND o.entry_id = u.entry_id AND u.new_mtime_ns = o.prev_mtime_ns`,
		undo.ID, a.ID).Scan(&reverses); err != nil || reverses != 3 {
		t.Errorf("%d undo items reverse their originals (%v)", reverses, err)
	}
}

// FAT truncation and hard links (r5 D14): on FAT (2 s resolution) a date
// of 10:00:01 is planned to 10:00:00; a photo with two names is refused
// hard_link; "A date known only to the year" is refused date_too_coarse and
// the others planned; "A photo not read yet" is refused not_dated_yet,
// while a PNG, a format the media job does not read, is dated by its name.
func TestR5_4TruncationLinksAndRefusals(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.disk("fat", "/fat", fat, func(root *synthfs.Node) {
		root.Dir("DCIM").File("IMG_20100717_100001.jpg", 100, at(2012, 2, 1, 9, 0, 0))
	})
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		c := root.Dir("Cartao")
		c.File("IMG_20100717_100000.jpg", 100, at(2012, 2, 1, 9, 0, 0))
		c.File("IMG_20100718_100000.jpg", 100, at(2012, 2, 1, 9, 0, 0)).Nlink(2)
		c.File("IMG_20100719_100000.jpg", 100, at(2012, 2, 1, 9, 0, 0))
		c.File("Screenshot_2011-05-02-21-14-07.png", 100, at(2012, 2, 1, 9, 0, 0))
		c.Dir("2006").File("ano.jpg", 100, at(2012, 2, 1, 9, 0, 0))
		c.File("notes.txt", 10, at(2012, 2, 1, 9, 0, 0))
	})
	w.seed("fat")
	w.seed("disk")

	var sum setMtimeSummary
	_, items := w.datePlan("plan-set-mtime", folders("", w.id("fat", "DCIM")), &sum)
	wantItems(t, items, "set_mtime planned DCIM/IMG_20100717_100001.jpg")
	if got := items[0].Mtime; got == nil || !got.To.Equal(at(2010, 7, 17, 10, 0, 0)) {
		t.Errorf("on FAT the time is %+v, want 10:00:00", got)
	}

	// Not read yet: no media_meta row since the last media job.
	unread := w.id("disk", "Cartao/IMG_20100719_100000.jpg")
	w.exec(`DELETE FROM media_meta WHERE entry_id = ?`, unread)
	_, items = w.datePlan("plan-set-mtime", entries("", w.id("disk", "Cartao/2006/ano.jpg"),
		w.id("disk", "Cartao/IMG_20100717_100000.jpg"), w.id("disk", "Cartao/IMG_20100718_100000.jpg"), unread,
		w.id("disk", "Cartao/Screenshot_2011-05-02-21-14-07.png"), w.id("disk", "Cartao/notes.txt"), w.id("disk", "Cartao")), &sum)
	wantItems(t, items,
		"set_mtime refused date_too_coarse Cartao/2006/ano.jpg",
		"set_mtime planned Cartao/IMG_20100717_100000.jpg",
		"set_mtime refused hard_link Cartao/IMG_20100718_100000.jpg",
		"set_mtime refused not_dated_yet Cartao/IMG_20100719_100000.jpg",
		"set_mtime planned Cartao/Screenshot_2011-05-02-21-14-07.png",
		"set_mtime refused not_media Cartao",
		"set_mtime refused not_media Cartao/notes.txt")
	if got := byPath(t, items, "Cartao/Screenshot_2011-05-02-21-14-07.png").Mtime; got == nil ||
		!got.To.Equal(at(2011, 5, 2, 21, 14, 7)) {
		t.Errorf("the screenshot's time is %+v", got)
	}
	if n := w.count(`SELECT count(*) FROM media_dates WHERE entry_id = ? AND meta_state = 'pending'`, unread); n != 1 {
		t.Errorf("the unread photo's date is not pending (%d)", n)
	}
	if n := w.count(`SELECT count(*) FROM media_meta WHERE entry_id = ?`, unread); n != 0 {
		t.Errorf("planning read the unread photo (%d rows)", n)
	}
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-set-mtime",
		fmt.Sprintf(`{"entry_id":%q}`, unread))
	w.refuse(http.StatusBadRequest, domain.CodeInvalidRequest, "plan-set-mtime",
		entries("", w.id("fat", "DCIM/IMG_20100717_100001.jpg"), unread))
}

// A quarantined target is answered 409 in_quarantine by both plans (r4
// D13, r5 D14, D16); writes off is writes_disabled.
func TestR5_4QuarantinedTargetsAndWrites(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.Dir("Fotos")
		root.Dir(".precious-quarantine").Dir("1").File("q.jpg", 100, at(2010, 7, 17, 10, 0, 0))
		root.Dir("Cartao").File("a.jpg", 100, at(2010, 7, 17, 10, 0, 0))
	})
	w.seed("disk")
	q := w.id("disk", ".precious-quarantine/1/q.jpg")
	w.refuse(http.StatusConflict, domain.CodeInQuarantine, "plan-set-mtime", entries("", q))
	w.refuse(http.StatusConflict, domain.CodeInQuarantine, "plan-date-organize", entries(w.dest("disk", "Fotos"), q))
	w.refuse(http.StatusConflict, domain.CodeInQuarantine, "plan-set-mtime", folders("", w.id("disk", ".precious-quarantine")))
	w.exec(`UPDATE sources SET write_enabled = 0 WHERE id = 'disk'`)
	a := w.id("disk", "Cartao/a.jpg")
	w.refuse(http.StatusConflict, domain.CodeWritesDisabled, "plan-set-mtime", entries("", a))
	w.refuse(http.StatusConflict, domain.CodeWritesDisabled, "plan-date-organize", entries(w.dest("disk", "Fotos"), a))
}

// A photo renamed by Precious since the last media job is planned from its
// new name's date: the plan re-derives its targets first.
func TestR5_4ARenamedPhotoIsPlannedFromItsNewName(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.Dir("Cartao").File("IMG_20100717_100000.jpg", 100, at(2012, 2, 1, 9, 0, 0))
	})
	w.seed("disk")
	id := w.id("disk", "Cartao/IMG_20100717_100000.jpg")
	rename, _ := w.plan("plan-rename", fmt.Sprintf(`{"entry_id":%q,"name":"IMG_20110101_120000.jpg"}`, id))
	if got := w.run(rename.ID); got.State != "done" {
		t.Fatalf("the rename %+v", got)
	}
	var sum setMtimeSummary
	_, items := w.datePlan("plan-set-mtime", entries("", id), &sum)
	wantItems(t, items, "set_mtime planned Cartao/IMG_20110101_120000.jpg")
	if got := items[0].Mtime; got == nil || !got.To.Equal(at(2011, 1, 1, 12, 0, 0)) {
		t.Errorf("the planned time is %+v", got)
	}
}
