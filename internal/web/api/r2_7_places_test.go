package api

import (
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// r27World is a world where hashing has checked 80% of the content that
// could have a copy, over every source: in "photos", two files read in
// full (one with a copy on source b, one without), one waiting to be read,
// one the content plan has not reached yet, and an archive with one member
// read and one waiting.
//
//	candidate: a 400 + b 400 + c 300 + m1 400 + m2 100 + b:a 400 = 2000
//	checked:   a 400 + b 400 + m1 400 + b:a 400                 = 1600
func r27World(t *testing.T) *pworld {
	w := newPWorld(t)
	w.file("photos/a.jpg", 400, "a")
	w.file("b:copies/a.jpg", 400, "a")
	w.file("photos/b.jpg", 400, "b")
	w.add(pf{path: "photos/c.jpg", size: 300, state: domain.ContentPending})
	w.add(pf{path: "photos/d.jpg", size: 50, state: "none"})
	w.archive(pf{path: "photos/pack.zip", size: 50}, pmem("m1.jpg", 400, "m1"),
		indextest.Member{Path: "m2.jpg", Size: 100, Content: indextest.Content{State: domain.ContentPending}})
	w.seed()
	return w
}

// R2.7 Not checked is not an only copy (r5c task 3.2): no file waiting to
// be read, or not planned yet, reads only_copy in children, treemap, or
// entry, archive members included; each reads unchecked.
func TestR2_7NotCheckedIsNotAnOnlyCopy(t *testing.T) {
	t.Parallel()
	w := r27World(t)
	photos, pack := w.id("photos"), w.id("photos/pack.zip")
	unchecked := map[string]bool{"c.jpg": true, "d.jpg": true, "m2.jpg": true}
	check := func(where string, rows []prow) {
		t.Helper()
		seen := 0
		for _, r := range rows {
			if !unchecked[r.Name] {
				continue
			}
			seen++
			if r.place() != "unchecked" {
				t.Errorf("%s: %s reads %s", where, r.Name, r.place())
			}
		}
		if seen == 0 {
			t.Errorf("%s: no unchecked row", where)
		}
	}
	rows, _ := w.children(photos, "", 100)
	check("children of photos", rows)
	check("treemap of photos", w.treemap(photos).Items)
	members, _ := w.children(pack, "", 100)
	check("children of pack.zip", members)
	check("treemap of pack.zip", w.treemap(pack).Items)
	for _, ref := range []string{w.id("photos/c.jpg"), w.id("photos/d.jpg"), w.id("photos/pack.zip!m2.jpg")} {
		d := w.detail(ref)
		check("entry", []prow{d.Entry})
	}
	// The files read in full are placed by their copies.
	got := byName(rows)
	if got["a.jpg"].place() != "other_source" || got["b.jpg"].place() != "only_copy" {
		t.Errorf("a.jpg %s, b.jpg %s", got["a.jpg"].place(), got["b.jpg"].place())
	}
}

// R2.7 Only-copy figures name the checked share (r5c task 3.2): the entry
// carries the coverage of every source together, 80%, beside its places;
// the folder's unchecked figure holds its unchecked files, the archive's
// members included through the archive's share, and the archive's unpacked
// figures name its unchecked member.
func TestR2_7OnlyCopyFiguresNameTheCheckedShare(t *testing.T) {
	t.Parallel()
	w := r27World(t)
	d := w.detail(w.id("photos"))
	c := d.Coverage
	if c.Candidate.Bytes != 2000 || c.Checked.Bytes != 1600 {
		t.Fatalf("coverage: candidate %+v, checked %+v", c.Candidate, c.Checked)
	}
	if share := float64(c.Checked.Bytes) / float64(c.Candidate.Bytes); share != 0.8 {
		t.Errorf("checked share %v, want 0.8", share)
	}
	if d.Entry.Places == nil {
		t.Fatal("photos has no places")
	}
	pack := w.detail(w.id("photos/pack.zip"))
	if pack.Entry.Places == nil || pack.Entry.MemberPlaces == nil {
		t.Fatalf("pack.zip: places %+v, member_places %+v", pack.Entry.Places, pack.Entry.MemberPlaces)
	}
	if got := pack.Entry.MemberPlaces.Unchecked; got != (pamount{1, 100}) {
		t.Errorf("pack.zip member_places unchecked %+v, want its waiting member", got)
	}
	share := pack.Entry.Places.Unchecked
	if share.Bytes == 0 {
		t.Errorf("pack.zip's split puts nothing under unchecked: %+v", *pack.Entry.Places)
	}
	want := pamount{Files: 2 + share.Files, Bytes: 300 + 50 + share.Bytes}
	if got := d.Entry.Places.Unchecked; got != want {
		t.Errorf("photos unchecked %+v, want %+v (c.jpg, d.jpg, and pack.zip's share)", got, want)
	}
	if d.Entry.place() != "only_copy" {
		t.Errorf("photos reads %s", d.Entry.place())
	}
}
