package organize

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
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

// r5 H3: a date organize's siblings look at each planned file's own stem
// only: 3,000 JPEGs planned out of one folder, beside a CR2 that stays,
// compare about one sibling per file, not every file of the folder per
// file; the JPEG whose stem the CR2 shares (in another letter case, on a
// case-insensitive disk) names it, and only it.
func TestR5ReviewSiblingsLookAtTheirOwnStem(t *testing.T) {
	t.Parallel()
	const n = 3000
	w := newWorld(t)
	w.disk("card", "/card", fat, func(root *synthfs.Node) {
		d := root.Dir("DCIM")
		for i := range n {
			d.File(fmt.Sprintf("IMG_%05d.JPG", i), 10, mtime)
		}
		d.File("img_00007.cr2", 10, mtime)
	})
	folder := w.id("card", "DCIM")
	w.readTx(func(tx *sql.Tx) error {
		ctx := context.Background()
		p, err := newPlan(ctx, tx, "card", true)
		if err != nil {
			return err
		}
		o := &organizer{p: p, destOf: map[int64]int64{}}
		rows, err := tx.Query(`SELECT id, parent_id, name FROM entries WHERE parent_id = ? ORDER BY name`, folder)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			it := &item{op: opRename, state: statePlanned}
			if err := rows.Scan(&it.entry, &it.fromParent, &it.fromName); err != nil {
				return err
			}
			if !strings.HasSuffix(string(it.fromName), ".JPG") {
				continue
			}
			p.items = append(p.items, it)
			o.destOf[it.entry] = 1
		}
		if err := rows.Err(); err != nil {
			return err
		}
		count, err := o.siblings()
		if err != nil {
			return err
		}
		if count != 1 || o.compared > 2*n {
			t.Errorf("%d files leave a sibling behind, after %d comparisons; want 1, at most %d", count, o.compared, 2*n)
		}
		for _, it := range p.items {
			want := ""
			if string(it.fromName) == "IMG_00007.JPG" {
				want = "img_00007.cr2"
			}
			if it.detail != want {
				t.Errorf("%s: detail %q, want %q", it.fromName, it.detail, want)
			}
		}
		return nil
	})
}

// r5 H4: a written time costs only its folders' time figures, not a refold
// reading and classifying every other child of each: the adapter's
// ApplyModTime of a photo succeeds beside a sibling folder whose stored
// figures cannot even be decoded (a refold of the photo's folder fails on
// them), and the folders' newest times and by-year figures follow.
func TestR5ReviewAWrittenTimeReadsNoSibling(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	t2004, t2006, t2010 := at(2004, 7, 1, 9, 0, 0), at(2006, 3, 4, 10, 0, 0), at(2010, 7, 17, 10, 0, 0)
	root := w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		fotos := root.Dir("Fotos")
		fotos.File("a.jpg", 1000, t2004)
		fotos.Dir("Outra").File("b.jpg", 2000, t2006)
	})
	w.exec(`UPDATE dir_stats SET signals = 'not json' WHERE entry_id = ?`, w.id("disk", "Fotos/Outra"))
	a := root.Child("Fotos").Child("a.jpg").ModTime(t2010)
	info := a.Info()
	id, err := strconv.ParseInt(w.id("disk", "Fotos/a.jpg"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	m := index.ModTime{Source: "disk", Entry: domain.EntryID(id), Facts: index.PostFacts{Dev: info.Dev, Ino: info.Ino,
		MtimeNs: info.ModTime.UnixNano(), CtimeNs: info.Ctime.UnixNano()}}
	if err := w.st.Write(context.Background(), func(tx *sql.Tx) error {
		return w.org.Index().ApplyModTime(context.Background(), tx, m)
	}); err != nil {
		t.Fatalf("ApplyModTime beside an unreadable sibling: %v", err)
	}
	for _, p := range []string{"Fotos", ""} {
		var (
			newest, oldest int64
			byYear         string
		)
		if err := w.st.Reader().QueryRow(`SELECT e.newest_ns, e.oldest_ns, d.by_year FROM entries e
			JOIN dir_stats d ON d.entry_id = e.id WHERE e.source_id = 'disk' AND e.path = ?`, []byte(p)).
			Scan(&newest, &oldest, &byYear); err != nil {
			t.Fatal(err)
		}
		want := `{"2006":{"files":1,"bytes":2000},"2010":{"files":1,"bytes":1000}}`
		if newest != t2010.UnixNano() || oldest != t2006.UnixNano() || byYear != want {
			t.Errorf("%q spans %v to %v, by year %s; want 2006 to 2010, %s", p, time.Unix(0, oldest).UTC(),
				time.Unix(0, newest).UTC(), byYear, want)
		}
	}
}
