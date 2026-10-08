package organize

import (
	"bytes"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// sonyShift is the correction of the corpus Sony's clock: +1 year 3 hours
// (r5 D19).
const sonyShift = 31_546_800

// R5.5 (task 2.11): on the hashed corpus, its dates seeded and the Sony's
// shift corrected, "Organize by date" of Viagens and celular_2011 into Fotos
// with {year}/{month}:
//   - the preview lists each missing year and month folder once, then one
//     move per file into its truth's month;
//   - Sent/IMG-20110416-WA0003.jpg is refused identical_copy, copy_of the
//     other copy, and summary.files_with_copies counts the planned files
//     with a copy among the targets or below Fotos;
//   - Ana's IMG_0102.JPG is planned as IMG_0102 (1).JPG;
//
// then it runs through the R3 executor with a file created at one planned
// name after planning: that item ends conflict, both files are untouched,
// and the other items are done.
func TestR5_5OrganizingByYearAndMonth(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	root, truth := corpus.BuildSynth(w.sfs, "/corpus", corpus.Corpus())
	w.add("corpus", "/corpus", root, posix)
	w.seed("corpus")

	var sony corpus.CameraTruth
	for _, c := range truth.Cameras {
		if c.Key == "SONY|DSC-W55|" {
			sony = c
		}
	}
	if sony.ShiftS != sonyShift || len(sony.Folders) != 2 {
		t.Fatalf("the corpus Sony is %+v", sony)
	}

	// The targets, in path order, with the folder of their truth's month
	// (the Sony's corrected), and the folders Fotos already holds.
	type target struct {
		path, month string
		sha         string
		id          string
	}
	var (
		targets []target
		dirs    = map[string]bool{}
		shaOf   = map[string][]string{} // sha256 -> paths of files
	)
	for _, e := range truth.Entries {
		raw, err := e.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		p := string(raw)
		if e.Kind == domain.EntryDirectory && strings.HasPrefix(p, "Fotos/") {
			dirs[p] = true
		}
		if e.Kind == domain.EntryFile && e.SHA256 != "" {
			shaOf[e.SHA256] = append(shaOf[e.SHA256], p)
		}
		if e.Date == nil || !(strings.HasPrefix(p, "Viagens/") || strings.HasPrefix(p, "celular_2011/")) {
			continue
		}
		if e.Date.Effective == nil {
			t.Fatalf("%s has no truth date", p)
		}
		month := e.Date.Local[:7]
		if slices.Contains(sony.Folders, path.Dir(p)) && strings.HasPrefix(path.Base(p), "DSC0") {
			month = e.Date.Effective.Add(sonyShift * time.Second).UTC().Format("2006-01")
		}
		targets = append(targets, target{path: p, month: month, sha: e.SHA256})
	}
	slices.SortFunc(targets, func(a, b target) int { return strings.Compare(a.path, b.path) })
	if len(targets) < 30 {
		t.Fatalf("only %d targets in the corpus", len(targets))
	}
	shifted := 0
	for i := range targets {
		targets[i].id = w.id("corpus", targets[i].path)
		if slices.Contains(sony.Folders, path.Dir(targets[i].path)) && strings.HasPrefix(path.Base(targets[i].path), "DSC0") {
			w.shift(targets[i].id, sonyShift)
			shifted++
		}
	}
	if shifted != sony.Photos {
		t.Fatalf("shifted %d Sony photos, want %d", shifted, sony.Photos)
	}

	const (
		wa3  = "celular_2011/WhatsApp/Media/WhatsApp Images/IMG-20110416-WA0003.jpg"
		sent = "celular_2011/WhatsApp/Media/WhatsApp Images/Sent/IMG-20110416-WA0003.jpg"
		ana  = "Viagens/2010-07 Bahia/do celular da Ana/IMG_0102.JPG"
		shot = "celular_2011/Pictures/Screenshots/Screenshot_2011-05-02-21-14-07.png"
	)
	var want, mkdirs []string
	planned := map[string]string{} // from path -> to path
	for _, tg := range targets {
		year := tg.month[:4]
		for _, d := range []string{"Fotos/" + year, "Fotos/" + year + "/" + tg.month[5:7]} {
			if !dirs[d] {
				dirs[d] = true
				mkdirs = append(mkdirs, "mkdir planned -> "+d)
			}
		}
		name := path.Base(tg.path)
		if tg.path == ana {
			name = "IMG_0102 (1).JPG"
		}
		to := "Fotos/" + year + "/" + tg.month[5:7] + "/" + name
		if tg.path == sent {
			want = append(want, "rename refused identical_copy "+tg.path+" -> "+to)
			continue
		}
		want = append(want, "rename planned "+tg.path+" -> "+to)
		planned[tg.path] = to
	}
	want = append(mkdirs, want...)
	// The planned files with a copy among the targets or below Fotos.
	isTarget := map[string]bool{}
	for _, tg := range targets {
		isTarget[tg.path] = true
	}
	copies := 0
	for _, tg := range targets {
		if _, ok := planned[tg.path]; !ok {
			continue
		}
		for _, p := range shaOf[tg.sha] {
			if p != tg.path && (isTarget[p] || strings.HasPrefix(p, "Fotos/")) {
				copies++
				break
			}
		}
	}
	if copies < 1 {
		t.Fatal("the corpus has no copy among the targets")
	}

	body := folders(w.dest("corpus", "Fotos")+`,"template":"{year}/{month}"`,
		w.id("corpus", "Viagens"), w.id("corpus", "celular_2011"))
	var sum dateOrganizeSummary
	a, items := w.datePlan("plan-date-organize", body, &sum)
	wantItems(t, items, want...)
	if a.Kind != "date_organize" || !a.Bulk || a.Template == nil || *a.Template != "{year}/{month}" || a.Rename ||
		a.Destination == nil || a.Destination.Path != "Fotos" || a.State != "planned" {
		t.Fatalf("the action is %+v", a)
	}
	if sum.FilesWithCopies != int64(copies) || sum.SplitSiblings != 0 {
		t.Errorf("summary %+v, want %d files with copies and no split sibling", sum, copies)
	}
	refused := byPath(t, items, sent)
	if refused.CopyOf == nil || refused.CopyOf.Entry != w.id("corpus", wa3) || refused.CopyOf.Path != wa3 ||
		!bytes.Equal(refused.CopyOf.PathB64, []byte(wa3)) || refused.Entry == nil || refused.Entry.ID != w.id("corpus", sent) {
		t.Errorf("the identical copy reads copy_of %+v, entry %+v", refused.CopyOf, refused.Entry)
	}
	for _, it := range items {
		if it.Mtime != nil || it.Detail != nil || it.CopyOf != nil && (it.From == nil || it.From.Path != sent) {
			t.Errorf("item %s reads mtime %v, detail %v, copy_of %v", itemSummary(it), it.Mtime, it.Detail, it.CopyOf)
		}
	}
	// Planning moved nothing.
	for from := range planned {
		if !w.exists("/corpus", from) {
			t.Fatalf("%s moved at planning", from)
		}
	}

	// The run, with a file appearing at the screenshot's planned name just
	// before its move.
	racer := root.Child("celular_2011").Child("Pictures").Child("Screenshots").Child(path.Base(shot))
	before := racer.Info()
	raceTime := at(2026, 9, 30, 8, 0, 0)
	var once sync.Once
	w.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpRename && len(c.Path) > 0 && string(c.Path[len(c.Path)-1]) == path.Base(shot) {
			once.Do(func() { root.Child("Fotos").Child("2011").Child("05").File(path.Base(shot), 7, raceTime) })
		}
	})
	done := w.run(a.ID)
	w.rec.SetBeforeCall(nil)
	if done.State != "done" || done.Counts["conflict"] != 1 || done.Counts["done"] != int64(len(planned)-1+len(mkdirs)) ||
		done.Counts["refused"] != 1 {
		t.Fatalf("the run ends %s with %v", done.State, done.Counts)
	}
	raced := byPath(t, w.items(a.ID, "state=conflict"), shot)
	if raced.Op != opRename || raced.To == nil || raced.To.Path != planned[shot] {
		t.Errorf("the conflict is %s", itemSummary(raced))
	}
	if info := racer.Info(); info.Size != before.Size || !info.ModTime.Equal(before.ModTime) || !w.exists("/corpus", shot) {
		t.Errorf("the screenshot changed: %+v, was %+v", info, before)
	}
	if info := root.Child("Fotos").Child("2011").Child("05").Child(path.Base(shot)).Info(); info.Size != 7 ||
		!info.ModTime.Equal(raceTime) {
		t.Errorf("the file at the planned name changed: %+v", info)
	}
	for from, to := range planned {
		if from == shot {
			continue
		}
		if w.exists("/corpus", from) || !w.exists("/corpus", to) {
			t.Errorf("%s did not move to %s", from, to)
		}
	}
	if !w.exists("/corpus", sent) {
		t.Error("the identical copy moved")
	}
}

// smallDisk builds disk "disk" (posix) from build, scans it, and seeds its
// dates.
func smallDisk(t *testing.T, build func(root *synthfs.Node)) (*world, *synthfs.Node) {
	t.Helper()
	w := newWorld(t)
	root := w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		root.Dir("Fotos")
		build(root)
	})
	w.seed("disk")
	return w, root
}

// "The event token keeps the event name": with {year}/{month} {event}, a
// photo of 2010-07 in "2010-07 Bahia" goes into 2010/07 Bahia, and one
// whose folder name is only a date into 2010/07.
func TestR5_5EventTokenKeepsTheEventName(t *testing.T) {
	t.Parallel()
	w, _ := smallDisk(t, func(root *synthfs.Node) {
		v := root.Dir("Viagens")
		v.Dir("2010-07 Bahia").File("a.jpg", 100, at(2010, 7, 17, 10, 0, 0))
		v.Dir("2010-07").File("b.jpg", 100, at(2010, 7, 18, 10, 0, 0))
	})
	var sum dateOrganizeSummary
	a, items := w.datePlan("plan-date-organize",
		folders(w.dest("disk", "Fotos")+`,"template":"{year}/{month} {event}"`, w.id("disk", "Viagens")), &sum)
	wantItems(t, items,
		"mkdir planned -> Fotos/2010",
		"mkdir planned -> Fotos/2010/07 Bahia",
		"mkdir planned -> Fotos/2010/07",
		"rename planned Viagens/2010-07 Bahia/a.jpg -> Fotos/2010/07 Bahia/a.jpg",
		"rename planned Viagens/2010-07/b.jpg -> Fotos/2010/07/b.jpg")
	if a.Template == nil || *a.Template != "{year}/{month} {event}" {
		t.Errorf("template %v", a.Template)
	}
}

// "A RAW file beside its JPEG": IMG_0001.JPG and IMG_0001.CR2 share a
// folder and only the JPEG's date is fine enough for {year}/{month}: the
// JPEG is planned, naming the CR2 in its detail, the CR2 is refused
// date_too_coarse, and split_siblings is 1. A pair planned into the same
// folder leaves nothing behind.
func TestR5_5ARawFileBesideItsJPEG(t *testing.T) {
	t.Parallel()
	w, _ := smallDisk(t, func(root *synthfs.Node) {
		c := root.Dir("Cartao").Dir("2010 Viagem")
		c.File("IMG_0001.JPG", 100, at(2010, 7, 17, 10, 0, 0))
		c.File("IMG_0001.CR2", 100, at(2015, 3, 1, 9, 0, 0)) // outside 2010: the folder's year only
		c.File("IMG_0002.JPG", 100, at(2010, 7, 17, 10, 5, 0))
		c.File("IMG_0002.CR2", 100, at(2010, 7, 17, 10, 5, 0))
	})
	var sum dateOrganizeSummary
	_, items := w.datePlan("plan-date-organize", folders(w.dest("disk", "Fotos"), w.id("disk", "Cartao")), &sum)
	wantItems(t, items,
		"mkdir planned -> Fotos/2010",
		"mkdir planned -> Fotos/2010/07",
		"rename refused date_too_coarse Cartao/2010 Viagem/IMG_0001.CR2",
		"rename planned Cartao/2010 Viagem/IMG_0001.JPG -> Fotos/2010/07/IMG_0001.JPG",
		"rename planned Cartao/2010 Viagem/IMG_0002.CR2 -> Fotos/2010/07/IMG_0002.CR2",
		"rename planned Cartao/2010 Viagem/IMG_0002.JPG -> Fotos/2010/07/IMG_0002.JPG")
	if sum.SplitSiblings != 1 || sum.FilesWithCopies != 0 {
		t.Errorf("summary %+v", sum)
	}
	for _, it := range items {
		want := ""
		if it.From != nil && it.From.Path == "Cartao/2010 Viagem/IMG_0001.JPG" {
			want = "IMG_0001.CR2"
		}
		if got := deref(it.Detail); got != want {
			t.Errorf("%s: detail %q, want %q", itemSummary(it), got, want)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// "A missing corrected photo keeps its name": a photo with a date
// correction goes missing, and a date organize plans another file into its
// path: that item is a conflict, name_taken_by_missing, and the correction
// stays.
func TestR5_5AMissingCorrectedPhotoKeepsItsName(t *testing.T) {
	t.Parallel()
	var july *synthfs.Node
	w, _ := smallDisk(t, func(root *synthfs.Node) {
		july = root.Child("Fotos").Dir("2010").Dir("07")
		july.File("c.jpg", 100, at(2010, 7, 1, 9, 0, 0))
		root.Dir("Cartao").Dir("2010-07").File("c.jpg", 200, at(2010, 7, 17, 10, 0, 0))
	})
	corrected := w.id("disk", "Fotos/2010/07/c.jpg")
	w.setDate(corrected, "2010-07-01")
	july.Remove("c.jpg")
	w.scan("disk")
	if _, state := w.pathOf(corrected); state != "missing" {
		t.Fatalf("the corrected photo is %s", state)
	}
	var sum dateOrganizeSummary
	_, items := w.datePlan("plan-date-organize", folders(w.dest("disk", "Fotos"), w.id("disk", "Cartao")), &sum)
	wantItems(t, items, "rename conflict name_taken_by_missing Cartao/2010-07/c.jpg -> Fotos/2010/07/c.jpg")
	if n := w.count(`SELECT count(*) FROM date_corrections WHERE entry_id = ? AND set_local = '2010-07-01'`, corrected); n != 1 {
		t.Errorf("the correction is gone (%d rows)", n)
	}
}

// {day} needs a day: a photo dated only to its folder's month is refused
// date_too_coarse, and goes into its month with {year}/{month}.
func TestR5_5DayOfAMonthPhotoIsTooCoarse(t *testing.T) {
	t.Parallel()
	w, _ := smallDisk(t, func(root *synthfs.Node) {
		root.Dir("Cartao").Dir("2010-07").File("d.jpg", 100, at(2012, 1, 1, 9, 0, 0))
	})
	var sum dateOrganizeSummary
	_, items := w.datePlan("plan-date-organize",
		folders(w.dest("disk", "Fotos")+`,"template":"{year}/{month}/{day}"`, w.id("disk", "Cartao")), &sum)
	wantItems(t, items, "rename refused date_too_coarse Cartao/2010-07/d.jpg")
	_, items = w.datePlan("plan-date-organize", folders(w.dest("disk", "Fotos"), w.id("disk", "Cartao")), &sum)
	wantItems(t, items, "mkdir planned -> Fotos/2010", "mkdir planned -> Fotos/2010/07",
		"rename planned Cartao/2010-07/d.jpg -> Fotos/2010/07/d.jpg")
}

// rename gives each file its date and time once: a name already starting
// with them is kept; a file dated coarser than a second is refused
// date_too_coarse.
func TestR5_5RenameAppliedOnce(t *testing.T) {
	t.Parallel()
	w, _ := smallDisk(t, func(root *synthfs.Node) {
		c := root.Dir("Cartao").Dir("2010-07")
		c.File("20100717_100500_b.jpg", 100, at(2010, 7, 17, 10, 5, 0))
		c.File("a.jpg", 100, at(2010, 7, 17, 10, 0, 0))
		c.File("m.jpg", 100, at(2012, 1, 1, 9, 0, 0)) // the folder's month only
	})
	var sum dateOrganizeSummary
	a, items := w.datePlan("plan-date-organize",
		folders(w.dest("disk", "Fotos")+`,"rename":true`, w.id("disk", "Cartao")), &sum)
	wantItems(t, items,
		"mkdir planned -> Fotos/2010",
		"mkdir planned -> Fotos/2010/07",
		"rename planned Cartao/2010-07/20100717_100500_b.jpg -> Fotos/2010/07/20100717_100500_b.jpg",
		"rename planned Cartao/2010-07/a.jpg -> Fotos/2010/07/20100717_100000_a.jpg",
		"rename refused date_too_coarse Cartao/2010-07/m.jpg")
	if !a.Rename || a.Template == nil || *a.Template != "{year}/{month}" {
		t.Errorf("the action reads rename %v, template %v", a.Rename, a.Template)
	}
}

// Name collisions (r5 D17): a file whose name a present file of other
// content takes in its folder gets "stem (k)ext", the first k free among
// the folder's files and the plan's items; one already where it belongs is
// refused already_there; a name the source cannot hold is invalid_name.
func TestR5_5SuffixesAndRefusals(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.disk("disk", "/disk", fat, func(root *synthfs.Node) {
		july := root.Dir("Fotos").Dir("2010").Dir("07")
		july.File("x.jpg", 100, at(2010, 7, 2, 9, 0, 0))
		july.File("X (1).jpg", 100, at(2010, 7, 3, 9, 0, 0))
		c := root.Dir("Cartao")
		c.Dir("2010-07 a").File("x.jpg", 200, at(2010, 7, 17, 10, 0, 0))
		c.Dir("2010-07 b").File("X.JPG", 300, at(2010, 7, 18, 10, 0, 0))
		c.Dir("2010-07 c").File("ok.jpg", 300, at(2010, 7, 18, 10, 0, 0))
	})
	w.seed("disk")
	var sum dateOrganizeSummary
	_, items := w.datePlan("plan-date-organize",
		folders(w.dest("disk", "Fotos"), w.id("disk", "Cartao"), w.id("disk", "Fotos/2010/07")), &sum)
	wantItems(t, items,
		"rename planned Cartao/2010-07 a/x.jpg -> Fotos/2010/07/x (2).jpg",
		"rename planned Cartao/2010-07 b/X.JPG -> Fotos/2010/07/X (3).JPG",
		"rename planned Cartao/2010-07 c/ok.jpg -> Fotos/2010/07/ok.jpg",
		"rename refused already_there Fotos/2010/07/X (1).jpg -> Fotos/2010/07/X (1).jpg",
		"rename refused already_there Fotos/2010/07/x.jpg -> Fotos/2010/07/x.jpg")

	// A template literal a FAT disk cannot hold refuses its files.
	w.exec(`UPDATE sources SET fs_type = 'vfat' WHERE id = 'disk'`)
	_, items = w.datePlan("plan-date-organize",
		folders(w.dest("disk", "Fotos")+`,"template":"{year}/a:b"`, w.id("disk", "Cartao/2010-07 c")), &sum)
	wantItems(t, items, "rename refused invalid_name Cartao/2010-07 c/ok.jpg -> Fotos/2010/a:b/ok.jpg")
}

// The plan's own refusals: a bad template, camera_key, entry_id, a missing
// destination, a destination on another source or in the quarantine.
func TestR5_5DateOrganizeRequests(t *testing.T) {
	t.Parallel()
	w, _ := smallDisk(t, func(root *synthfs.Node) {
		root.Dir("Cartao").File("a.jpg", 100, at(2010, 7, 17, 10, 0, 0))
		root.Dir(".precious-quarantine").Dir("1")
	})
	w.disk("other", "/other", posix, func(root *synthfs.Node) { root.Dir("Fotos") })
	cartao, fotos := w.id("disk", "Cartao"), w.dest("disk", "Fotos")
	bad := func(code domain.ErrorCode, body string) {
		t.Helper()
		status := http.StatusBadRequest
		w.refuse(status, code, "plan-date-organize", body)
	}
	for _, tmpl := range []string{"{year}//{month}", "{year}/..", "{hour}", "a/b/c/d/e", "{year", "x}"} {
		bad(domain.CodeInvalidRequest, folders(fotos+fmt.Sprintf(`,"template":%q`, tmpl), cartao))
	}
	bad(domain.CodeInvalidRequest, folders(fotos+`,"camera_key":"SONY|DSC-W55|"`, cartao))
	bad(domain.CodeInvalidRequest, fmt.Sprintf(`{"entry_id":%q,%s}`, w.id("disk", "Cartao/a.jpg"), fotos))
	bad(domain.CodeInvalidRequest, folders("", cartao))
	bad(domain.CodeInvalidRequest, folders(w.dest("other", "Fotos"), cartao))
	bad(domain.CodeInvalidRequest, folders(w.dest("disk", ".precious-quarantine/1"), cartao))
	bad(domain.CodeInvalidRequest, folders(fmt.Sprintf(`"destination_id":%q`, w.id("disk", "Cartao/a.jpg")), cartao))
	bad(domain.CodeInvalidRequest, folders(fotos+`,"unknown":1`, cartao))
	w.refuse(http.StatusNotFound, domain.CodeNotFound, "plan-date-organize", folders(fotos, "999999"))
}

// The date plans' queries read by indexes (plan guard): the targets by
// primary key, a folder's files by entries_by_name in name order, and a
// digest's copies by file_content_by_content; none scans a table or sorts.
func TestDatePlanQueryPlans(t *testing.T) {
	t.Parallel()
	w, _ := smallDisk(t, func(root *synthfs.Node) {
		root.Dir("Cartao").File("a.jpg", 100, at(2010, 7, 17, 10, 0, 0))
	})
	w.exec(`ANALYZE`)
	for _, c := range []struct {
		name  string
		query string
		args  []any
		uses  []string
	}{
		{"media files", mediaFilesSQL + `(?, ?)`, []any{1, 2}, []string{"USING INTEGER PRIMARY KEY"}},
		{"siblings", siblingsSQL, []any{1}, []string{"entries_by_name"}},
		{"copies", copiesSQL, []any{1, "disk", 1}, []string{"file_content_by_content", "USING INTEGER PRIMARY KEY"}},
	} {
		rows, err := w.st.Reader().Query(`EXPLAIN QUERY PLAN `+c.query, c.args...)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		all := strings.Join(plan, "\n")
		for _, idx := range c.uses {
			if !strings.Contains(all, idx) {
				t.Errorf("%s: the plan does not use %s:\n%s", c.name, idx, all)
			}
		}
		for _, line := range plan {
			if strings.HasPrefix(line, "SCAN ") || strings.Contains(line, "TEMP B-TREE") {
				t.Errorf("%s: the plan scans or sorts:\n%s", c.name, all)
			}
		}
	}
}
