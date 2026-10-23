package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
)

// r5c task 3.1, design Interfaces: each bad use of the place parameters is
// 400 invalid_request.
func TestPlacesParameterErrors(t *testing.T) {
	t.Parallel()
	w := newPWorld(t)
	w.file("Fotos/a.jpg", 10, "")
	w.file("Fotos/b.jpg", 20, "")
	w.file("Fotos/c.jpg", 30, "")
	w.seed()
	fotos := w.id("Fotos")
	cursorOf := func(query string) string {
		t.Helper()
		var p ppage
		w.get(t, "/api/entries/"+fotos+"/children?limit=1&"+query, 200, &p)
		if p.NextCursor == nil {
			t.Fatalf("%s: no next page", query)
		}
		return url.QueryEscape(*p.NextCursor)
	}
	plain := cursorOf("sort=bytes")
	onlyHere := cursorOf("places=1&sort=only_here")
	hidden := cursorOf("places=1&hide=other_source")
	for _, target := range []string{
		"/api/entries/" + fotos + "/children?places=0",
		"/api/entries/" + fotos + "/children?places=true",
		"/api/entries/" + fotos + "/children?places=1&hide=none",
		"/api/entries/" + fotos + "/children?places=1&hide=all",
		"/api/entries/" + fotos + "/children?hide=other_source",
		"/api/entries/" + fotos + "/children?sort=only_here",
		"/api/entries/" + fotos + "/children?places=1&places=1",
		// A cursor of another sort or filter.
		"/api/entries/" + fotos + "/children?places=1&sort=only_here&cursor=" + plain,
		"/api/entries/" + fotos + "/children?places=1&sort=bytes&cursor=" + onlyHere,
		"/api/entries/" + fotos + "/children?places=1&hide=other_source&cursor=" + plain,
		"/api/entries/" + fotos + "/children?places=1&cursor=" + hidden,
		"/api/entries/" + fotos + "/children?cursor=" + hidden,
		"/api/entries/" + fotos + "/treemap?places=2",
		"/api/entries/" + fotos + "/treemap?hide=other_source",
		"/api/entries/" + fotos + "?places=yes",
		"/api/entries/" + fotos + "?hide=other_source",
	} {
		w.fails(t, target, 400, "invalid_request")
	}
	// The same cursors continue their own listing.
	w.get(t, "/api/entries/"+fotos+"/children?places=1&sort=only_here&limit=1&cursor="+onlyHere, 200, nil)
	w.get(t, "/api/entries/"+fotos+"/children?places=1&hide=other_source&limit=1&cursor="+hidden, 200, nil)
	w.get(t, "/api/entries/"+fotos+"/children?places=1&sort=bytes&limit=1&cursor="+plain, 200, nil)
}

// rawRows decodes the rows at path in a body (a list, or one object) as
// raw field maps.
func rawRows(t *testing.T, body []byte, path ...string) []map[string]json.RawMessage {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	for _, p := range path {
		v = v.(map[string]any)[p]
	}
	b, _ := json.Marshal(v)
	var rows []map[string]json.RawMessage
	if len(b) > 0 && b[0] == '{' {
		var one map[string]json.RawMessage
		if err := json.Unmarshal(b, &one); err != nil {
			t.Fatal(err)
		}
		return []map[string]json.RawMessage{one}
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// r5c Interfaces: the place fields come only with places=1; other_copy and
// covered_by only on children rows; member_places only on the entry of the
// entry response; places_updating only on the entry response.
func TestPlaceFieldsOnlyWhereAsked(t *testing.T) {
	t.Parallel()
	w := newPWorld(t)
	w.file("Fotos/a.jpg", 10, "")
	w.archive(pf{path: "Fotos/z.zip", size: 50}, pmem("in.txt", 70, ""))
	w.seed()
	fotos, zip := w.id("Fotos"), w.id("Fotos/z.zip")
	placeFields := []string{"place", "place_offline", "disposable", "places"}
	has := func(row map[string]json.RawMessage, keys ...string) bool {
		for _, k := range keys {
			if _, ok := row[k]; !ok {
				return false
			}
		}
		return true
	}
	none := func(row map[string]json.RawMessage, keys ...string) bool {
		for _, k := range keys {
			if _, ok := row[k]; ok {
				return false
			}
		}
		return true
	}
	all := append(slices.Clone(placeFields), "member_places", "other_copy", "covered_by")
	for _, ref := range []string{fotos, zip} {
		for _, row := range rawRows(t, w.bodyOf(t, "/api/entries/"+ref+"/children"), "items") {
			if !none(row, all...) {
				t.Errorf("children of %s without places: %v", ref, row)
			}
		}
		for _, row := range rawRows(t, w.bodyOf(t, "/api/entries/"+ref+"/children?places=1"), "items") {
			if !has(row, append(placeFields, "other_copy", "covered_by")...) || !none(row, "member_places") {
				t.Errorf("children of %s with places: %v", ref, row)
			}
		}
		tm := w.bodyOf(t, "/api/entries/"+ref+"/treemap?places=1")
		for _, row := range rawRows(t, tm, "items") {
			if !has(row, placeFields...) || !none(row, "member_places", "other_copy", "covered_by") {
				t.Errorf("treemap items of %s: %v", ref, row)
			}
		}
		if e := rawRows(t, tm, "entry")[0]; !has(e, placeFields...) || !none(e, "member_places", "other_copy", "covered_by") {
			t.Errorf("treemap entry of %s: %v", ref, e)
		}
		if e := rawRows(t, w.bodyOf(t, "/api/entries/"+ref+"/treemap"), "entry")[0]; !none(e, all...) {
			t.Errorf("treemap entry of %s without places: %v", ref, e)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(w.bodyOf(t, "/api/entries/"+ref), &top); err != nil {
			t.Fatal(err)
		}
		if _, ok := top["places_updating"]; ok {
			t.Errorf("entry %s without places has places_updating", ref)
		}
		body := w.bodyOf(t, "/api/entries/"+ref+"?places=1")
		if err := json.Unmarshal(body, &top); err != nil {
			t.Fatal(err)
		}
		if _, ok := top["places_updating"]; !ok {
			t.Errorf("entry %s with places has no places_updating", ref)
		}
		if e := rawRows(t, body, "entry")[0]; !has(e, append(placeFields, "member_places")...) || !none(e, "other_copy", "covered_by") {
			t.Errorf("entry %s: %v", ref, e)
		}
	}
	// member_places: the archive's unpacked members, null for a folder.
	if d := w.detail(zip); d.Entry.MemberPlaces == nil || d.Entry.MemberPlaces.OnlyCopy != (pamount{1, 70}) ||
		d.Entry.Places == nil || d.Entry.Places.OnlyCopy != (pamount{1, 50}) {
		t.Errorf("zip: places %+v, member_places %+v", d.Entry.Places, d.Entry.MemberPlaces)
	}
	if d := w.detail(fotos); d.Entry.MemberPlaces != nil {
		t.Errorf("folder member_places %+v", d.Entry.MemberPlaces)
	}
	if m := w.treemap(zip); m.Entry.Places == nil || m.Entry.Places.OnlyCopy != (pamount{1, 50}) {
		t.Errorf("treemap of the zip: places %+v", m.Entry.Places)
	}
}

// A member copy names its archive and its path inside it apart, since a '!'
// in either makes the joined path ambiguous; an entry copy carries neither.
func TestOtherCopyNamesItsArchiveApart(t *testing.T) {
	t.Parallel()
	w := newPWorld(t)
	w.file("a/nota2.txt", 30, "nota")
	w.file("a/leiame.txt", 20, "leiame")
	w.file("b:copy/leiame.txt", 20, "leiame")
	w.archive(pf{path: "b:Importante!/backup.zip", size: 25}, pmem("docs/nota2.txt", 30, "nota"))
	// inner.zip's members are placed through its copy, a member of Backup.zip.
	w.archive(pf{path: "a/inner.zip", size: 60, content: "inner"}, pmem("x.txt", 70, ""))
	w.archive(pf{path: "b:Fotos!/Backup.zip", size: 65}, pmem("pack!/inner.zip", 60, "inner"))
	w.seed()
	str := func(p *string) string {
		if p == nil {
			return "<absent>"
		}
		return *p
	}
	rows, _ := w.children(w.id("a"), "", 100)
	got := byName(rows)
	backup := w.seeded["b"].ID("Importante!/backup.zip").String()
	if c := got["nota2.txt"].OtherCopy; c == nil || str(c.ArchiveID) != backup ||
		str(c.ArchivePath) != "Importante!/backup.zip" || str(c.MemberPath) != "docs/nota2.txt" {
		t.Errorf("nota2.txt: copy %+v", c)
	}
	inner, _ := w.children(w.id("a/inner.zip"), "", 100)
	fotos := w.seeded["b"].ID("Fotos!/Backup.zip").String()
	if len(inner) != 1 || inner[0].OtherCopy == nil || !inner[0].OtherCopy.ViaArchive {
		t.Fatalf("inner.zip: %+v", inner)
	}
	if c := inner[0].OtherCopy; str(c.ArchiveID) != fotos || str(c.ArchivePath) != "Fotos!/Backup.zip" ||
		str(c.MemberPath) != "pack!/inner.zip" {
		t.Errorf("x.txt: copy %+v", c)
	}
	for _, row := range rawRows(t, w.bodyOf(t, "/api/entries/"+w.id("a")+"/children?places=1"), "items") {
		var c map[string]json.RawMessage
		if err := json.Unmarshal(row["other_copy"], &c); err != nil {
			t.Fatal(err)
		}
		if string(c["archive_id"]) != "null" {
			continue
		}
		if _, ok := c["archive_path"]; ok {
			t.Errorf("entry copy with archive_path: %s", row["other_copy"])
		}
		if _, ok := c["member_path"]; ok {
			t.Errorf("entry copy with member_path: %s", row["other_copy"])
		}
	}
	if got["leiame.txt"].OtherCopy == nil || got["leiame.txt"].OtherCopy.ArchiveID != nil {
		t.Errorf("leiame.txt: copy %+v", got["leiame.txt"].OtherCopy)
	}
}

// onlyHereKey is D10's key of a row, computed from its JSON alone.
func onlyHereKey(r prow) (int64, int64) {
	switch {
	case r.Place == nil:
		return 0, 0
	case *r.Place == "not_computed":
		return math.MaxInt64, 0
	case r.Places != nil:
		return r.Places.OnlyCopy.Bytes + r.Places.ThisSource.Bytes, r.Places.Unchecked.Bytes + r.Places.Unreadable.Bytes
	case r.Disposable:
		return 0, 0
	case *r.Place == "only_copy" || *r.Place == "this_source":
		return r.Size, 0
	case *r.Place == "unchecked" || *r.Place == "unreadable":
		return 0, r.Size
	}
	return 0, 0
}

// sortWorld is a folder "big" of 250 children with every kind of only_here
// key, apart from zero: only copies, copies on the same source, files not
// checked yet, folders with equal only-here bytes and different unchecked
// bytes, folders not computed yet, complete archives, and a missing folder
// and a missing archive.
func sortWorld(t *testing.T) (*pworld, []string) {
	w := newPWorld(t)
	var notComputed []string
	n := 0
	for i := range 143 {
		w.file(fmt.Sprintf("big/only%03d.bin", i), int64(1000+i%50), "")
		n++
	}
	for i := range 30 {
		c := fmt.Sprintf("same%d", i)
		w.file(fmt.Sprintf("big/same%02d.bin", i), int64(700+i), c)
		w.file(fmt.Sprintf("elsewhere/same%02d.bin", i), int64(700+i), c)
		n++
	}
	for i := range 40 {
		w.add(pf{path: fmt.Sprintf("big/pending%02d.bin", i), size: int64(300 + i), state: domain.ContentPending})
		n++
	}
	for i := range 20 {
		w.file(fmt.Sprintf("big/tie%02d/unique.bin", i), 500, "")
		w.add(pf{path: fmt.Sprintf("big/tie%02d/pending.bin", i), size: int64(100 + i), state: domain.ContentPending})
		n++
	}
	for i := range 5 {
		w.file(fmt.Sprintf("big/fresh%d/a.bin", i), int64(10+i), "")
		notComputed = append(notComputed, fmt.Sprintf("big/fresh%d", i))
		n++
	}
	for i := range 10 {
		w.archive(pf{path: fmt.Sprintf("big/arc%d.zip", i), size: int64(2000 + i)}, pmem("m.txt", 5000, ""))
		n++
	}
	w.file("big/gone/x.bin", 77, "")
	w.archive(pf{path: "big/gone.zip", size: 88}, pmem("m.txt", 99, ""))
	n += 2
	if n != 250 {
		t.Fatalf("%d children", n)
	}
	w.seed()
	for _, p := range notComputed {
		w.exec(t, `DELETE FROM entry_places WHERE entry_id = ?`, w.seeded["disk"].ID(p))
	}
	w.exec(t, `UPDATE entries SET state = 'missing' WHERE id IN (?, ?)`,
		w.seeded["disk"].ID("big/gone"), w.seeded["disk"].ID("big/gone.zip"))
	return w, notComputed
}

// r5c task 3.1, design D10: sort=only_here pages 250 children as 200 and 50
// with no duplicate or omission, in key order; not computed first when
// descending and last when ascending; ties by unchecked bytes; the missing
// folder and archive null and last.
func TestSortOnlyHere(t *testing.T) {
	t.Parallel()
	w, notComputed := sortWorld(t)
	big := w.id("big")
	var everyone page
	w.get(t, "/api/entries/"+big+"/children?limit=1000", 200, &everyone)
	if len(everyone.Items) != 250 {
		t.Fatalf("%d children", len(everyone.Items))
	}
	fresh := map[string]bool{}
	for _, p := range notComputed {
		fresh[w.id(p)] = true
	}
	for _, order := range []string{"desc", "asc"} {
		t.Run(order, func(t *testing.T) {
			var (
				rows  []prow
				sizes []int
			)
			base := "/api/entries/" + big + "/children?places=1&sort=only_here&order=" + order
			target := base
			for {
				var p ppage
				w.get(t, target, 200, &p)
				rows = append(rows, p.Items...)
				sizes = append(sizes, len(p.Items))
				if p.NextCursor == nil {
					break
				}
				target = base + "&cursor=" + url.QueryEscape(*p.NextCursor)
			}
			if !slices.Equal(sizes, []int{200, 50}) {
				t.Fatalf("pages of %v rows", sizes)
			}
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.ID
			}
			want := ids(everyone.Items)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("the pages do not hold each child once")
			}
			checkOnlyHereOrder(t, rows, order == "desc")
			edge := rows[:5]
			if order == "asc" {
				edge = rows[245:]
			}
			for _, r := range edge {
				if !fresh[r.ID] || r.place() != "not_computed" || r.Places != nil {
					t.Errorf("%s at the not computed end: %s, places %+v", r.Name, r.place(), r.Places)
				}
			}
			// Missing entries key 0: last when descending, first when
			// ascending.
			last := rows[248:]
			if order == "asc" {
				last = rows[:2]
			}
			names := []string{last[0].Name, last[1].Name}
			slices.Sort(names)
			if !slices.Equal(names, []string{"gone", "gone.zip"}) || last[0].Place != nil || last[1].Place != nil {
				t.Errorf("the missing entries: %v at the other end, places %s %s", names, last[0].place(), last[1].place())
			}
			// The tie folders share their only-here bytes; their unchecked
			// bytes order them.
			var ties []string
			for _, r := range rows {
				if strings.HasPrefix(r.Name, "tie") {
					ties = append(ties, r.Name)
				}
			}
			wantTies := slices.Clone(ties)
			slices.Sort(wantTies)
			if order == "desc" {
				slices.Reverse(wantTies)
			}
			if !slices.Equal(ties, wantTies) {
				t.Errorf("tie folders in order %v", ties)
			}
		})
	}
}

// hideWorld holds one child of each place in "mix".
func hideWorld(t *testing.T) *pworld {
	w := newPWorld(t)
	w.file("mix/only.jpg", 10, "")
	w.file("mix/same.jpg", 11, "same")
	w.file("keep/same.jpg", 11, "same")
	w.file("mix/other.jpg", 12, "other")
	w.file("b:copy/other.jpg", 12, "other")
	w.add(pf{path: "mix/pending.jpg", size: 13, state: domain.ContentPending})
	w.add(pf{path: "mix/unreadable.jpg", size: 14, state: domain.ContentUnreadable})
	w.add(pf{path: "mix/new.jpg", size: 15, state: "none"})
	w.file("mix/empty.txt", 0, "")
	w.add(pf{path: "mix/link", kind: domain.EntrySymlink, link: "only.jpg"})
	w.add(pf{path: "mix/Thumbs.db", size: 16, category: domain.CategoryCache, fileKind: domain.FileKindOther})
	w.file("mix/gone.jpg", 17, "")
	w.file("mix/elsewhere/a.jpg", 18, "elsewhere")
	w.file("b:copy/a.jpg", 18, "elsewhere")
	w.file("mix/mine/a.jpg", 19, "")
	w.file("mix/fresh/a.jpg", 20, "")
	w.file("mix/gonedir/a.jpg", 21, "")
	w.archive(pf{path: "mix/backup.zip", size: 22, content: "zip"}, pmem("a.txt", 30, ""))
	w.file("b:copy/backup.zip", 22, "zip")
	w.archive(pf{path: "mix/gone.zip", size: 23}, pmem("a.txt", 31, ""))
	w.seed()
	w.exec(t, `DELETE FROM entry_places WHERE entry_id = ?`, w.seeded["disk"].ID("mix/fresh"))
	w.exec(t, `UPDATE entries SET state = 'missing' WHERE id IN (?, ?, ?)`, w.seeded["disk"].ID("mix/gone.jpg"),
		w.seeded["disk"].ID("mix/gonedir"), w.seeded["disk"].ID("mix/gone.zip"))
	return w
}

// r5c task 3.1, design D1, D10, D14: each child's place, and hide keeping
// exactly the rows placed only_copy, this_source, unchecked, unreadable, or
// not_computed, apart from disposable files, through every page.
func TestHideKeepsWhatNeedsKeeping(t *testing.T) {
	t.Parallel()
	w := hideWorld(t)
	mix := w.id("mix")
	all, _ := w.children(mix, "", 200)
	got := map[string]string{}
	for _, r := range all {
		got[r.Name] = r.place()
		if r.Name == "Thumbs.db" && !r.Disposable || r.Name != "Thumbs.db" && r.Disposable {
			t.Errorf("%s disposable %v", r.Name, r.Disposable)
		}
	}
	want := map[string]string{
		"only.jpg": "only_copy", "same.jpg": "this_source", "other.jpg": "other_source", "pending.jpg": "unchecked",
		"unreadable.jpg": "unreadable", "new.jpg": "unchecked", "empty.txt": "none", "link": "none",
		"Thumbs.db": "only_copy", "gone.jpg": "<null>", "elsewhere": "other_source", "mine": "only_copy",
		"fresh": "not_computed", "gonedir": "<null>", "backup.zip": "other_source", "gone.zip": "<null>",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("places\n got %v\nwant %v", got, want)
	}
	kept := []string{"fresh", "mine", "new.jpg", "only.jpg", "pending.jpg", "same.jpg", "unreadable.jpg"}
	for _, sort := range []string{"bytes", "name", "files", "newest", "only_here"} {
		for _, order := range []string{"desc", "asc"} {
			rows, pages := w.children(mix, "hide=other_source&sort="+sort+"&order="+order, 3)
			var names []string
			for _, r := range rows {
				names = append(names, r.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, kept) || pages != 3 {
				t.Errorf("hide, sort %s %s: %v in %d pages", sort, order, names, pages)
			}
		}
	}
	// Hide and sort=bytes order as sort=bytes does.
	rows, _ := w.children(mix, "hide=other_source&sort=bytes", 3)
	for i := 1; i < len(rows); i++ {
		if rows[i-1].TotalBytes < rows[i].TotalBytes {
			t.Errorf("hide and bytes: %s (%d) before %s (%d)", rows[i-1].Name, rows[i-1].TotalBytes, rows[i].Name,
				rows[i].TotalBytes)
		}
	}
	// The treemap is never filtered.
	if m := w.treemap(mix); len(m.Items) != 13 {
		t.Errorf("treemap of %d items", len(m.Items))
	}
}

// r5c task 3.1, design D1 (I7): a folder not read completely reads
// unreadable where its files would make it other_source or none, in
// children, entry, and treemap, and hide keeps it. One folder per trigger,
// each with every readable file copied to another source.
func TestFolderNotReadCompletelyIsUnreadable(t *testing.T) {
	t.Parallel()
	w := newPWorld(t)
	w.add(pf{path: "top/broken", kind: domain.EntryDirectory, unreadable: true})
	w.file("top/partial/a.jpg", 10, "a")
	w.add(pf{path: "top/mount", kind: domain.EntryDirectory, mount: true})
	w.file("top/below/a.jpg", 11, "b")
	w.add(pf{path: "top/below/sub", kind: domain.EntryDirectory, unreadable: true})
	w.file("top/mounted/a.jpg", 12, "c")
	w.add(pf{path: "top/mounted/sub", kind: domain.EntryDirectory, mount: true})
	w.file("top/whole/a.jpg", 13, "d")
	for i, c := range []string{"a", "b", "c", "d"} {
		w.file("b:copies/"+c+".jpg", int64(10+i), c)
	}
	w.seed()
	disk := w.seeded["disk"]
	// Each folder has its own trigger only: the seeding marks the folders
	// above an unreadable one partial.
	w.exec(t, `UPDATE entries SET partial = 0`)
	w.exec(t, `UPDATE entries SET partial = 1 WHERE id = ?`, disk.ID("top/partial"))
	triggers := []string{"broken", "partial", "mount", "below", "mounted"}
	top := w.id("top")
	rows, _ := w.children(top, "", 100)
	got := byName(rows)
	for _, name := range triggers {
		if p := got[name].place(); p != "unreadable" {
			t.Errorf("children: %s reads %s", name, p)
		}
		if p := w.detail(w.id("top/" + name)).Entry.place(); p != "unreadable" {
			t.Errorf("entry: %s reads %s", name, p)
		}
		if p := w.treemap(w.id("top/" + name)).Entry.place(); p != "unreadable" {
			t.Errorf("treemap entry: %s reads %s", name, p)
		}
	}
	if p := got["whole"].place(); p != "other_source" {
		t.Errorf("the folder read completely reads %s", p)
	}
	for _, r := range w.treemap(top).Items {
		if want := "unreadable"; r.Name != "whole" && r.place() != want {
			t.Errorf("treemap item %s reads %s", r.Name, r.place())
		}
	}
	hidden, _ := w.children(top, "hide=other_source", 100)
	var names []string
	for _, r := range hidden {
		names = append(names, r.Name)
	}
	slices.Sort(names)
	wantKept := slices.Clone(triggers)
	slices.Sort(wantKept)
	if !slices.Equal(names, wantKept) {
		t.Errorf("hide keeps %v", names)
	}
}

// r5c task 3.1, design D5, D6, D9, D11, D14: members are placed under their
// archive's own coverage.
func TestMemberPlaces(t *testing.T) {
	t.Parallel()
	w := newPWorld(t, "off")
	// backup.zip has a copy on another source: its members are placed
	// through it, naming it, apart from the one with a copy of its own.
	w.archive(pf{path: "a/backup.zip", size: 100, content: "backup"},
		pmem("photos/x.jpg", 40, ""), pmem("photos/Thumbs.db", 9, ""), pmem("own.jpg", 50, "own"),
		pmem("photos/sub/y.jpg", 30, ""))
	w.file("b:copy/backup.zip", 100, "backup")
	w.file("b:copy/own.jpg", 50, "own")
	// mirror.zip is unique, every member copied to another source.
	w.archive(pf{path: "a/mirror.zip", size: 60, content: "mirror"},
		pmem("p.jpg", 41, "p"), pmem("q.jpg", 42, "q"))
	w.file("b:copy/p.jpg", 41, "p")
	w.file("b:copy/q.jpg", 42, "q")
	// away.zip has a copy on an offline source only.
	w.archive(pf{path: "a/away.zip", size: 70, content: "away"}, pmem("r.jpg", 43, ""))
	w.file("off:copy/away.zip", 70, "away")
	// twin.zip has a copy on its own source.
	w.archive(pf{path: "a/twin.zip", size: 80, content: "twin"}, pmem("s.jpg", 44, ""))
	w.file("a/twin-copy.zip", 80, "twin")
	w.seed()

	backup := w.id("a/backup.zip")
	rows, _ := w.children(backup, "", 100)
	got := byName(rows)
	if r := got["own.jpg"]; r.place() != "other_source" || r.OtherCopy == nil || r.OtherCopy.ViaArchive ||
		r.OtherCopy.Path != "copy/own.jpg" {
		t.Errorf("own.jpg: %s, copy %+v", r.place(), r.OtherCopy)
	}
	photos := got["photos"]
	if photos.place() != "other_source" || photos.Places == nil ||
		photos.Places.OtherSource != (pamount{2, 70}) || photos.Places.Disposable != (pamount{1, 9}) ||
		photos.OtherCopy != nil {
		t.Errorf("photos: %s, places %+v", photos.place(), photos.Places)
	}
	inner, _ := w.children(photos.ID, "", 100)
	for _, r := range inner {
		switch r.Name {
		case "x.jpg", "Thumbs.db":
			if r.place() != "other_source" || r.OtherCopy == nil || !r.OtherCopy.ViaArchive ||
				r.OtherCopy.Path != "copy/backup.zip" || r.OtherCopy.Ref != w.id("b:copy/backup.zip") {
				t.Errorf("%s: %s, copy %+v", r.Name, r.place(), r.OtherCopy)
			}
			if r.Disposable != (r.Name == "Thumbs.db") {
				t.Errorf("%s: disposable %v", r.Name, r.Disposable)
			}
		case "sub":
			if r.Places == nil || r.Places.OtherSource != (pamount{1, 30}) || r.Disposable {
				t.Errorf("sub: places %+v", r.Places)
			}
		default:
			t.Errorf("unexpected member %s", r.Name)
		}
	}
	// Hide keeps nothing of backup.zip but nothing it holds needs keeping;
	// the member folder's only-here key is 0.
	if hidden, _ := w.children(backup, "hide=other_source", 100); len(hidden) != 0 {
		t.Errorf("hide keeps %d members of backup.zip", len(hidden))
	}
	d := w.detail(backup)
	if d.Entry.MemberPlaces == nil || d.Entry.MemberPlaces.OtherSource != (pamount{3, 120}) ||
		d.Entry.MemberPlaces.Disposable != (pamount{1, 9}) {
		t.Errorf("backup.zip member_places %+v", d.Entry.MemberPlaces)
	}
	if d := w.detail(w.id("a/backup.zip!photos/x.jpg")); d.Entry.place() != "other_source" || d.Entry.Places != nil {
		t.Errorf("entry of x.jpg: %s", d.Entry.place())
	}
	if d := w.detail(photos.ID); d.Entry.Places == nil || d.Entry.Places.OtherSource != (pamount{2, 70}) {
		t.Errorf("entry of photos: %+v", d.Entry.Places)
	}

	mirror, _ := w.children(w.id("a/mirror.zip"), "", 100)
	for _, r := range mirror {
		if r.place() != "other_source" || r.OtherCopy == nil || r.OtherCopy.ViaArchive {
			t.Errorf("mirror.zip %s: %s, copy %+v", r.Name, r.place(), r.OtherCopy)
		}
	}
	if p := w.detail(w.id("a/mirror.zip")); p.Entry.place() != "other_source" {
		t.Errorf("mirror.zip reads %s", p.Entry.place())
	}

	away, _ := w.children(w.id("a/away.zip"), "", 100)
	if len(away) != 1 || away[0].place() != "other_source" || !away[0].PlaceOffline || away[0].OtherCopy == nil ||
		!away[0].OtherCopy.Offline || !away[0].OtherCopy.ViaArchive {
		t.Errorf("away.zip: %+v", away)
	}

	twin, _ := w.children(w.id("a/twin.zip"), "", 100)
	if len(twin) != 1 || twin[0].place() != "this_source" || twin[0].OtherCopy == nil ||
		!twin[0].OtherCopy.ViaArchive || twin[0].OtherCopy.Path != "a/twin-copy.zip" || twin[0].OtherCopy.More != 0 {
		t.Errorf("twin.zip: %+v", twin)
	}
	// Sorting and hiding run inside an archive too.
	if rows, _ := w.children(w.id("a/twin.zip"), "hide=other_source&sort=only_here", 1); len(rows) != 1 {
		t.Errorf("hide in twin.zip keeps %d", len(rows))
	}
	if tm := w.treemap(photos.ID); tm.Entry.Places == nil || tm.Entry.place() != "other_source" || len(tm.Items) != 3 {
		t.Errorf("treemap of photos: %+v", tm.Entry)
	}

	// Until a relate pass stores them, a member folder reads not_computed,
	// hide keeps it, and the archive's member_places is null; its files are
	// still placed live.
	w.exec(t, `DELETE FROM member_places WHERE member_id = ?`, strings.TrimPrefix(photos.ID, "m"))
	w.exec(t, `DELETE FROM archive_places WHERE entry_id = ?`, backup)
	rows, _ = w.children(backup, "", 100)
	if r := byName(rows)["photos"]; r.place() != "not_computed" || r.Places != nil {
		t.Errorf("photos without a row: %s, places %+v", r.place(), r.Places)
	}
	if r := byName(rows)["own.jpg"]; r.place() != "other_source" {
		t.Errorf("own.jpg without its folder rows: %s", r.place())
	}
	if hidden, _ := w.children(backup, "hide=other_source", 100); len(hidden) != 1 || hidden[0].Name != "photos" {
		t.Errorf("hide keeps %+v, want photos alone", hidden)
	}
	if d := w.detail(photos.ID); d.Entry.place() != "not_computed" {
		t.Errorf("entry of photos without a row: %s", d.Entry.place())
	}
	if d := w.detail(backup); d.Entry.MemberPlaces != nil {
		t.Errorf("backup.zip without its row: member_places %+v", d.Entry.MemberPlaces)
	}
}

// r5c task 3.1, design D11, D14: the not-connected mark follows the copy's
// source; a Thumbs.db is disposable.
func TestPlaceOfflineMarkAndDisposable(t *testing.T) {
	t.Parallel()
	w := newPWorld(t, "off")
	w.file("a/away.jpg", 10, "away")
	w.file("off:copy/away.jpg", 10, "away")
	w.file("a/both.jpg", 11, "both")
	w.file("off:copy/both.jpg", 11, "both")
	w.file("b:copy/both.jpg", 11, "both")
	w.file("a/here.jpg", 12, "here")
	w.file("a/here2.jpg", 12, "here")
	w.add(pf{path: "a/Thumbs.db", size: 13, category: domain.CategoryCache, fileKind: domain.FileKindOther})
	w.seed()
	a := w.id("a")
	check := func(when string, away bool) {
		t.Helper()
		rows, _ := w.children(a, "", 100)
		got := byName(rows)
		if r := got["away.jpg"]; r.place() != "other_source" || r.PlaceOffline != away || r.OtherCopy == nil ||
			r.OtherCopy.Offline != away {
			t.Errorf("%s: away.jpg %s, mark %v, copy %+v", when, r.place(), r.PlaceOffline, r.OtherCopy)
		}
		if r := got["both.jpg"]; r.PlaceOffline || r.OtherCopy == nil || r.OtherCopy.SourceID != "b" {
			t.Errorf("%s: both.jpg mark %v, copy %+v", when, r.PlaceOffline, r.OtherCopy)
		}
		if r := got["here.jpg"]; r.place() != "this_source" || r.PlaceOffline || r.OtherCopy == nil ||
			r.OtherCopy.Path != "a/here2.jpg" {
			t.Errorf("%s: here.jpg %s, mark %v, copy %+v", when, r.place(), r.PlaceOffline, r.OtherCopy)
		}
		if r := got["Thumbs.db"]; !r.Disposable || r.place() != "only_copy" {
			t.Errorf("%s: Thumbs.db %s, disposable %v", when, r.place(), r.Disposable)
		}
		if d := w.detail(w.id("a/away.jpg")); d.Entry.PlaceOffline != away {
			t.Errorf("%s: entry of away.jpg mark %v", when, d.Entry.PlaceOffline)
		}
	}
	check("offline", true)
	w.exec(t, `UPDATE sources SET state = 'online' WHERE id = 'off'`)
	check("online", false)
}

// r5c task 3.1, design D8: places_updating is true while the relations are
// dirty or a relate job is queued or running, and false after the pass.
func TestPlacesUpdating(t *testing.T) {
	t.Parallel()
	w := newPWorld(t)
	w.file("a/x.jpg", 10, "")
	w.seed()
	a := w.id("a")
	updating := func() bool {
		t.Helper()
		d := w.detail(a)
		if d.PlacesUpdating == nil {
			t.Fatal("no places_updating")
		}
		return *d.PlacesUpdating
	}
	if updating() {
		t.Errorf("updating after the pass")
	}
	w.exec(t, `UPDATE review_state SET dirty = 1`)
	if !updating() {
		t.Errorf("not updating while dirty")
	}
	w.exec(t, `UPDATE review_state SET dirty = 0`)
	w.exec(t, `INSERT INTO jobs (kind, payload_version, payload, state, max_attempts, available_at, created_at, updated_at)
		VALUES ('relate', 1, '{}', 'queued', 3, 0, 0, 0)`)
	if !updating() {
		t.Errorf("not updating while a relate job is queued")
	}
	w.exec(t, `UPDATE jobs SET state = 'running'`)
	if !updating() {
		t.Errorf("not updating while a relate job runs")
	}
	w.exec(t, `UPDATE jobs SET state = 'succeeded'`)
	w.exec(t, `INSERT INTO jobs (kind, payload_version, payload, state, max_attempts, available_at, created_at, updated_at)
		VALUES ('hash', 1, '{}', 'running', 3, 0, 0, 0)`)
	w.exec(t, `UPDATE review_state SET dirty = 1`)
	w.relate()
	if updating() {
		t.Errorf("updating after the pass ended")
	}
}

// threeSourceWorld is a world of three sources, one offline, with every
// case of D1, D5, and D14: hard links, copies on the same and other
// sources, archives with and without their own copy, disposable files,
// files not checked yet, empty files, links, and a missing file.
func threeSourceWorld(t *testing.T) *pworld {
	w := newPWorld(t, "off")
	w.file("home/fotos/a.jpg", 100, "a")
	w.file("home/fotos/b.jpg", 200, "b")
	w.file("home/fotos/c.jpg", 300, "")
	w.add(pf{path: "home/fotos/d.jpg", size: 400, state: domain.ContentPending})
	w.add(pf{path: "home/fotos/e.jpg", size: 410, state: "none"})
	w.add(pf{path: "home/fotos/f.jpg", size: 420, state: domain.ContentUnreadable})
	w.add(pf{path: "home/fotos/Thumbs.db", size: 7, category: domain.CategoryCache, fileKind: domain.FileKindOther})
	w.add(pf{path: "home/fotos/link", kind: domain.EntrySymlink, link: "a.jpg"})
	w.file("home/fotos/empty", 0, "")
	w.file("home/fotos/gone.jpg", 55, "")
	w.add(pf{path: "home/fotos/hard1.jpg", size: 600, content: "hard", ino: 9})
	w.add(pf{path: "home/fotos/hard2.jpg", size: 600, content: "hard", ino: 9})
	w.file("home/docs/b-again.jpg", 200, "b")
	w.file("home/docs/deep/note.txt", 33, "note")
	w.archive(pf{path: "home/docs/backup.zip", size: 150, content: "backup"},
		pmem("a.jpg", 100, "a"), pmem("new.txt", 20, ""), pmem("Thumbs.db", 5, ""))
	w.archive(pf{path: "home/docs/solo.zip", size: 90}, pmem("x.txt", 60, ""), pmem("y/z.txt", 70, "z"))
	w.file("home/docs/z.txt", 70, "z")
	w.file("b:copies/a.jpg", 100, "a")
	w.file("off:old/note.txt", 33, "note")
	w.file("off:old/backup.zip", 150, "backup")
	w.missing = []string{"home/fotos/gone.jpg"}
	w.seed()
	return w
}

// r5c task 3.1: on the seeded three-source world after a relate pass, for
// every folder, its child files' live places plus its child folders' and
// archives' stored places sum to its own stored places; inside every
// complete archive and member folder, its member files' live places plus
// its member folders' stored places sum to the archive's stored
// member_places and the member folder's stored places.
func TestFolderPlacesAgreeWithTheirChildren(t *testing.T) {
	t.Parallel()
	w := threeSourceWorld(t)
	rows, err := w.st.Reader().Query(`SELECT e.id FROM entries e JOIN entry_places p ON p.entry_id = e.id
		WHERE e.kind = 'directory' ORDER BY e.id`)
	if err != nil {
		t.Fatal(err)
	}
	var folders []string
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		folders = append(folders, fmt.Sprint(id))
	}
	rows.Close()
	if len(folders) < 8 {
		t.Fatalf("only %d folders", len(folders))
	}
	// agree checks the level ref, whose stored places are stored, against
	// its children, and returns them.
	agree := func(ref, name string, stored psums) []prow {
		t.Helper()
		var sum psums
		kids, _ := w.children(ref, "", 1000)
		for _, k := range kids {
			switch {
			case k.Places != nil:
				sum.add(*k.Places)
			case k.Kind != "file" || k.Place == nil || k.place() == "none":
			case k.Disposable:
				sum.Disposable.Files++
				sum.Disposable.Bytes += k.Size
			default:
				a := sum.at(k.place())
				a.Files++
				a.Bytes += k.Size
			}
		}
		if sum != stored {
			t.Errorf("%q: children sum to %+v, stored %+v", name, sum, stored)
		}
		// The set-based sort and hide place every child as its row reads.
		for _, order := range []string{"desc", "asc"} {
			sorted, _ := w.children(ref, "sort=only_here&order="+order, 1000)
			if len(sorted) != len(kids) {
				t.Errorf("%q: %d rows sorted, %d listed", name, len(sorted), len(kids))
			}
			checkOnlyHereOrder(t, sorted, order == "desc")
		}
		var want, got []string
		for _, k := range kids {
			if keptRow(k) {
				want = append(want, k.ID)
			}
		}
		hidden, _ := w.children(ref, "hide=other_source", 1000)
		for _, k := range hidden {
			got = append(got, k.ID)
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%q: hide keeps %v, its rows %v", name, got, want)
		}
		return kids
	}
	for _, f := range folders {
		d := w.detail(f)
		if d.Entry.Places == nil {
			t.Fatalf("folder %s has no places", d.Entry.Path)
		}
		agree(f, d.Entry.Path, *d.Entry.Places)
	}
	// Member folders, at any depth, as their rows and entries read them.
	var inside func(ref, name string, stored psums) int
	inside = func(ref, name string, stored psums) int {
		n := 0
		for _, k := range agree(ref, name, stored) {
			if k.Kind != "directory" {
				continue
			}
			if k.Places == nil {
				t.Fatalf("member folder %s has no places", k.Path)
			}
			if d := w.detail(k.ID); d.Entry.Places == nil || *d.Entry.Places != *k.Places {
				t.Errorf("member folder %s: entry places %+v, row %+v", k.Path, d.Entry.Places, *k.Places)
			}
			n += 1 + inside(k.ID, k.Path, *k.Places)
		}
		return n
	}
	memberFolders := 0
	for _, arc := range []string{"home/docs/backup.zip", "home/docs/solo.zip"} {
		d := w.detail(w.id(arc))
		if d.Entry.MemberPlaces == nil {
			t.Fatalf("%s has no member_places", arc)
		}
		memberFolders += inside(w.id(arc), arc, *d.Entry.MemberPlaces)
	}
	if memberFolders == 0 {
		t.Errorf("no member folder checked")
	}
	// The world holds every place.
	root := w.detail(w.id("home"))
	p := root.Entry.Places
	for _, a := range []pamount{p.OnlyCopy, p.ThisSource, p.OtherSource, p.Unchecked, p.Unreadable, p.Disposable} {
		if a.Files == 0 {
			t.Errorf("home's places %+v miss a place", *p)
		}
	}
}

// keptRow is D10's hide test on a row's JSON.
func keptRow(r prow) bool {
	switch r.place() {
	case "only_copy", "this_source", "unchecked", "unreadable", "not_computed":
		return !r.Disposable
	}
	return false
}

// checkOnlyHereOrder checks that rows follow D10's key, computed from
// their JSON, then their decimal IDs, in the given direction.
func checkOnlyHereOrder(t *testing.T, rows []prow, desc bool) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		a1, a2 := onlyHereKey(rows[i-1])
		b1, b2 := onlyHereKey(rows[i])
		c := cmp.Or(cmp.Compare(a1, b1), cmp.Compare(a2, b2), cmp.Compare(len(rows[i-1].ID), len(rows[i].ID)),
			strings.Compare(rows[i-1].ID, rows[i].ID))
		if desc && c <= 0 || !desc && c >= 0 {
			t.Fatalf("rows %d and %d out of order: %s %s (%d,%d) then %s %s (%d,%d)", i-1, i,
				rows[i-1].Name, rows[i-1].place(), a1, a2, rows[i].Name, rows[i].place(), b1, b2)
		}
	}
}

// r5c task 3.1, design D9: covered_by names the relation of most matched
// bytes, same from either side or inside as the contained side, with the
// other side's decimal ID and path; other_copy.more counts the physical
// copies beyond two.
func TestCoveredByAndMore(t *testing.T) {
	t.Parallel()
	w := newPWorld(t)
	w.file("a/album/x.jpg", 1000, "x")
	w.file("a/album/y.jpg", 2000, "y")
	w.file("b:album/x.jpg", 1000, "x")
	w.file("b:album/y.jpg", 2000, "y")
	w.file("a/small/p.jpg", 3000, "p")
	w.file("b:big/p.jpg", 3000, "p")
	w.file("b:big/q.jpg", 4000, "q")
	w.archive(pf{path: "a/pack.zip", size: 900}, pmem("photos/r.jpg", 5000, "r"), pmem("photos/s.jpg", 6000, "s"),
		pmem("readme.txt", 77777, ""))
	w.file("b:photos/r.jpg", 5000, "r")
	w.file("b:photos/s.jpg", 6000, "s")
	w.file("a/three.jpg", 70, "three")
	w.file("b:three1.jpg", 70, "three")
	w.file("b:three2.jpg", 70, "three")
	// Unique files keep the tops from relating as a whole.
	w.file("a/mine.bin", 90000, "")
	w.file("b:theirs.bin", 80000, "")
	w.seed()
	rows, _ := w.children(w.id("a"), "", 100)
	got := byName(rows)
	if c := got["album"].CoveredBy; c == nil || c.Kind != "same" || c.Other.ID != w.id("b:album") ||
		c.Other.SourceID != "b" || c.Other.Path != "album" {
		t.Errorf("album covered by %+v", c)
	}
	if c := got["small"].CoveredBy; c == nil || c.Kind != "inside" || c.Other.ID != w.id("b:big") {
		t.Errorf("small covered by %+v", c)
	}
	if c := got["three.jpg"]; c.CoveredBy != nil || c.OtherCopy == nil || c.OtherCopy.More != 1 {
		t.Errorf("three.jpg: copy %+v, covered by %+v", c.OtherCopy, c.CoveredBy)
	}
	members, _ := w.children(w.id("a/pack.zip"), "", 100)
	if c := byName(members)["photos"].CoveredBy; c == nil || c.Kind != "same" || c.Other.ID != w.id("b:photos") {
		t.Errorf("pack.zip!photos covered by %+v", c)
	}
	if c := byName(members)["readme.txt"].CoveredBy; c != nil {
		t.Errorf("a file member covered by %+v", c)
	}
	// From b, the same relation names the member folder inside the zip.
	bRows, _ := w.children(w.seeded["b"].Root.String(), "", 100)
	if c := byName(bRows)["photos"].CoveredBy; c == nil || c.Kind != "same" || c.Other.ID != w.id("a/pack.zip!photos") ||
		c.Other.Path != "a/pack.zip!photos" || c.Other.SourceID != "disk" {
		t.Errorf("b:photos covered by %+v", c)
	}
	// b's big holds small: the containing side names nothing.
	if c := byName(bRows)["big"].CoveredBy; c != nil {
		t.Errorf("big covered by %+v", c)
	}
}
