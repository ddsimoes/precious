package organize

import (
	"testing"

	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// r5 H1: the owner's 1975-06-01T12:00:00 on a scanned photo on a FAT card
// is a time FAT cannot hold (Linux would silently store 1980-01-01), so
// plan-set-mtime refuses it date_out_of_range, and running the action never
// sets its time; the card's other photo is planned and set. On ext4 the same
// date is planned, and a date in the epoch's first day, which the index
// reads as unknown, is refused.
func TestR5ReviewSetFileDatesRefusesATimeTheDiskCannotHold(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	card := w.sfs.Root("/card")
	dcim := card.Dir("DCIM")
	dcim.File("IMG_20100717_100000.jpg", 100, at(2012, 2, 1, 9, 0, 0))
	dcim.File("scan.jpg", 100, at(2012, 2, 1, 9, 0, 0))
	w.addAs("card", "/card", card, fat, "vfat")
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.File("scan.jpg", 100, at(2012, 2, 1, 9, 0, 0))
		root.File("lost.jpg", 100, at(2012, 2, 1, 9, 0, 0))
	})
	w.seed("card")
	w.seed("disk")
	w.setDate(w.id("card", "DCIM/scan.jpg"), "1975-06-01T12:00:00")
	w.setDate(w.id("disk", "scan.jpg"), "1975-06-01T12:00:00")
	w.setDate(w.id("disk", "lost.jpg"), "1970-01-01T12:00:00")

	var sum setMtimeSummary
	a, items := w.datePlan("plan-set-mtime", folders("", w.id("card", "DCIM")), &sum)
	wantItems(t, items,
		"set_mtime planned DCIM/IMG_20100717_100000.jpg",
		"set_mtime refused date_out_of_range DCIM/scan.jpg")
	w.rec.Reset()
	if got := w.run(a.ID); got.State != "done" {
		t.Fatalf("the action %+v", got)
	}
	var set []string
	for _, c := range w.rec.Calls() {
		if c.Op == instrument.OpSetModTime {
			set = append(set, c.FullPath())
		}
	}
	if len(set) != 1 || set[0] != "/card/DCIM/IMG_20100717_100000.jpg" {
		t.Errorf("SetModTime calls %q, want the dated photo's only", set)
	}

	_, items = w.datePlan("plan-set-mtime", entries("", w.id("disk", "scan.jpg"), w.id("disk", "lost.jpg")), &sum)
	wantItems(t, items,
		"set_mtime refused date_out_of_range lost.jpg",
		"set_mtime planned scan.jpg")
	if got := byPath(t, items, "scan.jpg").Mtime; got == nil || !got.To.Equal(at(1975, 6, 1, 12, 0, 0)) {
		t.Errorf("on ext4 the time is %+v, want 1975-06-01 12:00", got)
	}
}
