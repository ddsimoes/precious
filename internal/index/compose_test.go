package index

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

const gb = 1_000_000_000

// localTree builds the owner's archive/local (design D21): a
// personal_media folder of 400 GB personal (three photo folders and clips,
// one video without a rule), and 6 GB of programs: downloads with a disk
// image, the installed program homeplanner (a group), and a loose program.
func localTree(e *env) *synthfs.Node {
	root := e.disk("disk", "/src/disk", posix)
	local := root.Dir("archive").Dir("local")
	for _, name := range []string{"fotos2019", "fotos2020", "fotos2021"} {
		d := local.Dir(name)
		d.File("IMG_0001.jpg", 66_600_000_000, mtime)
		d.File("IMG_0002.jpg", 66_600_000_000, mtime)
	}
	local.Dir("clips").File("clip.mp4", 400_000_000, mtime)
	local.Dir("downloads").File("ubuntu.iso", 4*gb, mtime)
	app := local.Dir("homeplanner")
	app.File("homeplanner.exe", 1_500_000_000, mtime)
	app.File("core.dll", 399_000_000, mtime)
	app.File("unins000.exe", 100_000_000, mtime)
	local.File("putty.exe", 1_000_000, mtime)
	return root
}

// family reads a dir_stats.by_family column.
func family(t *testing.T, r entry) map[domain.Family]counts {
	t.Helper()
	var cells map[domain.Family]struct{ Files, Bytes int64 }
	if err := json.Unmarshal([]byte(r.ByFamily.String), &cells); err != nil {
		t.Fatalf("%q by_family %q: %v", r.Path, r.ByFamily.String, err)
	}
	out := map[domain.Family]counts{}
	for f, c := range cells {
		if c.Bytes != 0 || c.Files != 0 {
			out[f] = counts{files: c.Files, bytes: c.Bytes}
		}
	}
	return out
}

// insideItem is one element of dir_stats.inside as tests read it.
type insideItem struct {
	EntryID  string  `json:"entry_id"`
	PathB64  []byte  `json:"path_b64"`
	Path     string  `json:"path"`
	Category *string `json:"category"`
	Family   *string `json:"family"`
	Group    bool    `json:"group"`
	Bytes    int64   `json:"bytes"`
	Files    int64   `json:"files"`
}

// insideOf reads a folder's inside list and checks that each item names the
// row at its path.
func insideOf(t *testing.T, rows map[string]entry, path string) []insideItem {
	t.Helper()
	r := get(t, rows, path)
	var items []insideItem
	if err := json.Unmarshal([]byte(r.Inside.String), &items); err != nil {
		t.Fatalf("%q inside %q: %v", path, r.Inside.String, err)
	}
	for _, it := range items {
		target := get(t, rows, string(it.PathB64))
		if it.EntryID != domain.EntryID(target.ID).String() || it.Path != string(it.PathB64) {
			t.Errorf("%q inside item %+v names entry %d", path, it, target.ID)
		}
	}
	return items
}

// wantItem is an expected inside item: path, category, family, group, bytes,
// and files.
type wantItem struct {
	path, category, family string
	group                  bool
	bytes, files           int64
}

func checkInside(t *testing.T, rows map[string]entry, path string, want ...wantItem) {
	t.Helper()
	got := insideOf(t, rows, path)
	if len(got) != len(want) {
		t.Errorf("%q inside has %d items %+v, want %d", path, len(got), got, len(want))
		return
	}
	for i, w := range want {
		g := got[i]
		if g.Path != w.path || deref(g.Category) != w.category || deref(g.Family) != w.family ||
			g.Group != w.group || g.Bytes != w.bytes || g.Files != w.files {
			t.Errorf("%q inside[%d] = %s %s/%s group %v %d bytes %d files; want %+v", path, i, g.Path,
				deref(g.Category), deref(g.Family), g.Group, g.Bytes, g.Files, w)
		}
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Classification "A photo folder keeps its other contents visible", "Clues
// from the top", and "A loose photo counts as personal" (design D21).
func TestCompositionAndInsideOfAPhotoFolder(t *testing.T) {
	e := newEnv(t)
	localTree(e)
	e.scan("disk")
	rows := e.entries("disk")

	for path, want := range map[string]domain.Category{
		"archive/local":                      domain.CategoryPersonalMedia,
		"archive/local/homeplanner":          domain.CategoryApplicationInstallation,
		"archive/local/clips":                domain.CategoryUnknown,
		"archive/local/clips/clip.mp4":       domain.CategoryUnknown,
		"archive/local/downloads/ubuntu.iso": domain.CategoryInstallerDownload,
	} {
		if r := get(t, rows, path); r.Category.String != string(want) {
			t.Errorf("%q category %s, want %s", path, r.Category.String, want)
		}
	}
	if !get(t, rows, "archive/local/homeplanner").Group {
		t.Fatal("homeplanner is not a group")
	}

	// 400 GB personal (the unclassified video included) and 6 GB programs,
	// the same at every folder above local.
	want := map[domain.Family]counts{
		domain.FamilyPersonal: {files: 7, bytes: 400 * gb},
		domain.FamilyPrograms: {files: 5, bytes: 6 * gb},
	}
	for _, path := range []string{"archive/local", "archive", ""} {
		if got := family(t, get(t, rows, path)); !equalFamilies(got, want) {
			t.Errorf("%q by_family %v, want %v", path, got, want)
		}
	}
	if got := family(t, get(t, rows, "archive/local/clips")); !equalFamilies(got,
		map[domain.Family]counts{domain.FamilyPersonal: {files: 1, bytes: 400_000_000}}) {
		t.Errorf("clips by_family %v: a video without a rule is personal", got)
	}
	// A group's own composition is its content's.
	if got := family(t, get(t, rows, "archive/local/homeplanner")); !equalFamilies(got,
		map[domain.Family]counts{domain.FamilyPrograms: {files: 3, bytes: 1_999_000_000}}) {
		t.Errorf("homeplanner by_family %v", got)
	}

	// Every folder from local up lists downloads, homeplanner, and the loose
	// program, largest first; the photo folders and clips are personal like
	// local, so nothing in them is notable.
	// An item's category and family are the entry's own.
	clues := []wantItem{
		{"archive/local/downloads", "download_collection", "containers", false, 4 * gb, 1},
		{"archive/local/homeplanner", "application_installation", "programs", true, 1_999_000_000, 3},
		{"archive/local/putty.exe", "installer_download", "programs", false, 1_000_000, 1},
	}
	for _, path := range []string{"archive/local", "archive", ""} {
		checkInside(t, rows, path, clues...)
	}
	checkInside(t, rows, "archive/local/fotos2019")
	checkInside(t, rows, "archive/local/downloads")
	compareWithSeed(t, e, "disk")
}

func equalFamilies(a, b map[domain.Family]counts) bool {
	if len(a) != len(b) {
		return false
	}
	for f, c := range a {
		if b[f] != c {
			return false
		}
	}
	return true
}

// Classification "A mixed folder splits by family", "A loose photo counts
// as personal", and "A nested group appears once": a group inside a group is
// not listed, and a folder's own category does not override its
// composition.
func TestCompositionOfAMixedFolderAndNestedGroups(t *testing.T) {
	e := newEnv(t)
	root := e.disk("disk", "/src/disk", posix)
	mixed := root.Dir("mixed")
	media := mixed.Dir("media")
	media.File("a.jpg", 5_000_000, mtime)
	media.File("b.jpg", 5_000_000, mtime)
	mixed.Dir("Cache").File("data_1", 2_000_000, mtime)
	mixed.File("tool.exe", 1_000_000, mtime)
	mixed.File("loose.jpg", 500_000, mtime)
	// More notable files than a list keeps: the 10 largest stay, exactly.
	album := mixed.Dir("album")
	album.File("c.jpg", 9_000_000, mtime)
	for i := range 25 {
		album.File(fmt.Sprintf("~WRL%04d.tmp", i), int64(1000*(i%13+1)), mtime)
	}
	proj := root.Dir("proj")
	proj.File("package.json", 900, mtime)
	proj.File("index.js", 100_000, mtime)
	nm := proj.Dir("node_modules").Dir("lodash")
	nm.File("package.json", 700, mtime)
	nm.File("lodash.js", 140_000, mtime)
	e.scan("disk")
	rows := e.entries("disk")

	// A JPEG no rule matches is unknown, and personal in every folder above.
	if c := get(t, rows, "mixed/loose.jpg").Category.String; c != string(domain.CategoryUnknown) {
		t.Errorf("loose.jpg category %s, want unknown", c)
	}
	if got := family(t, get(t, rows, ""))[domain.FamilyPersonal]; got.files != 8 {
		t.Errorf("root personal %+v, want the photos, loose.jpg, and the project's 4 files", got)
	}
	if got, want := family(t, get(t, rows, "mixed")), map[domain.Family]counts{
		domain.FamilyPersonal:   {files: 4, bytes: 19_500_000},
		domain.FamilyDisposable: {files: 26, bytes: 2_169_000},
		domain.FamilyPrograms:   {files: 1, bytes: 1_000_000},
	}; !equalFamilies(got, want) {
		t.Errorf("mixed by_family %v, want %v", got, want)
	}
	mixedInside := insideOf(t, rows, "mixed")
	var gotPaths []string
	for _, it := range mixedInside {
		gotPaths = append(gotPaths, it.Path)
	}
	wantPaths := []string{"mixed/Cache", "mixed/tool.exe"}
	for _, n := range []string{"0012", "0011", "0024", "0010", "0023", "0009", "0022", "0008"} {
		wantPaths = append(wantPaths, "mixed/album/~WRL"+n+".tmp")
	}
	if !slices.Equal(gotPaths, wantPaths) {
		t.Errorf("mixed inside %v, want %v", gotPaths, wantPaths)
	}
	if r := get(t, rows, "proj/node_modules"); !r.Group || !get(t, rows, "proj").Group {
		t.Fatalf("proj and its node_modules must be groups: %v %v", get(t, rows, "proj").Group, r.Group)
	}
	// The root holds the source project whole: only proj is listed, not the
	// node_modules group inside it.
	items := insideOf(t, rows, "")
	var paths []string
	for _, it := range items {
		paths = append(paths, it.Path)
	}
	for _, it := range items {
		if it.Path == "proj/node_modules" {
			t.Errorf("root inside lists the nested group: %v", paths)
		}
	}
	found := false
	for _, it := range items {
		found = found || it.Path == "proj"
	}
	if !found {
		t.Errorf("root inside %v misses the group proj", paths)
	}
	// proj itself lists its own nested group.
	if items := insideOf(t, rows, "proj"); len(items) == 0 || items[0].Path != "proj/node_modules" {
		t.Errorf("proj inside %+v, want node_modules first", items)
	}
	compareWithSeed(t, e, "disk")
}

// Home's by_family is the roots' composition: it sums to the source's total
// bytes and files, for the regression corpus too.
func TestRootCompositionSumsToTheTotal(t *testing.T) {
	_, _, corpusRows := scanCorpus(t)
	e := newEnv(t)
	localTree(e)
	e.scan("disk")
	for name, rows := range map[string]map[string]entry{"corpus": corpusRows, "local": e.entries("disk")} {
		root := get(t, rows, "")
		var sum counts
		for _, c := range family(t, root) {
			sum.add(c)
		}
		if sum != (counts{files: root.TotalFiles, bytes: root.TotalBytes}) {
			t.Errorf("%s: root by_family sums to %+v, total %d files %d bytes", name, sum, root.TotalFiles, root.TotalBytes)
		}
	}
}

// A rescan that changes one deep file updates the composition and the
// inside list of every folder above it, and rewrites nothing else.
func TestRescanUpdatesCompositionAndInside(t *testing.T) {
	e := newEnv(t)
	root := localTree(e)
	e.scan("disk")
	before := e.entries("disk")

	fotos := root.Child("archive").Child("local").Child("fotos2021")
	fotos.File("backup.zip", 50*gb, mtime)
	e.scan("disk")
	rows := e.entries("disk")

	zip := wantItem{"archive/local/fotos2021/backup.zip", get(t, rows, "archive/local/fotos2021/backup.zip").Category.String,
		"containers", false, 50 * gb, 1}
	want := map[domain.Family]counts{
		domain.FamilyPersonal:   {files: 7, bytes: 400 * gb},
		domain.FamilyPrograms:   {files: 5, bytes: 6 * gb},
		domain.FamilyContainers: {files: 1, bytes: 50 * gb},
	}
	for _, path := range []string{"archive/local/fotos2021", "archive/local", "archive", ""} {
		got := insideOf(t, rows, path)
		if len(got) == 0 || got[0].Path != zip.path || got[0].Bytes != zip.bytes || deref(got[0].Family) != zip.family {
			t.Errorf("%q inside %+v, want the new archive first", path, got)
		}
		if path == "archive/local/fotos2021" {
			continue
		}
		if got := family(t, get(t, rows, path)); !equalFamilies(got, want) {
			t.Errorf("%q by_family %v, want %v", path, got, want)
		}
	}
	// The other photo folders' rows and stats are as stored.
	for _, path := range []string{"archive/local/fotos2019", "archive/local/homeplanner", "archive/local/downloads"} {
		b, a := before[path], get(t, rows, path)
		if a.ScanGen != b.ScanGen || a.Inside != b.Inside || a.ByFamily != b.ByFamily {
			t.Errorf("%q rewritten: gen %d → %d", path, b.ScanGen, a.ScanGen)
		}
	}
	compareWithSeed(t, e, "disk")
}

// comparePath orders dir/name as bytes.Compare orders the joined path.
func TestComparePath(t *testing.T) {
	paths := []string{"", "a", "a/b", "a/b/c", "a.b", "a0", "a-", "ab", "a/a", "a/c", "b", "a/b0", "a/b/"}
	for _, dir := range paths {
		for _, name := range []string{"b", "a", "c", "b0", "zz"} {
			joined := joinPath([]byte(dir), []byte(name))
			for _, p := range paths {
				if got, want := comparePath([]byte(dir), []byte(name), []byte(p)), bytes.Compare(joined, []byte(p)); got != want {
					t.Errorf("comparePath(%q, %q, %q) = %d, want %d", dir, name, p, got, want)
				}
			}
		}
	}
}
