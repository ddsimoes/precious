package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index/indextest"
)

// insideJSON is one stats.inside item of a seeded entry: category and
// family "" are null.
func insideJSON(s *indextest.Seeded, path, category, family string, group bool, bytes, files int64) string {
	null := func(v string) string {
		if v == "" {
			return "null"
		}
		return `"` + v + `"`
	}
	display, _ := json.Marshal(domain.DisplayName([]byte(path)))
	raw, _ := json.Marshal([]byte(path))
	return fmt.Sprintf(`{"entry_id":"%s","path":%s,"path_b64":%s,"category":%s,"family":%s,"group":%t,"bytes":%d,"files":%d}`,
		s.ID(path), display, raw, null(category), null(family), group, bytes, files)
}

// rowComposition is the composition of an EntryRow, as JSON.
type rowComposition struct {
	ID          string          `json:"id"`
	Path        string          `json:"path"`
	Composition json.RawMessage `json:"composition"`
}

// Every EntryRow carries its composition (design D21): a folder's
// by_family without empty families, in family order; a file's size under its
// file family; nothing for an unreadable folder. Children, treemap, search,
// and the detail's entry serve the same.
func TestEntryRowComposition(t *testing.T) {
	e := newEnv(t)
	s := detailTree(t, e)
	want := map[string]string{
		"":                             `[{"family":"personal","bytes":246,"files":4},{"family":"programs","bytes":5060,"files":2}]`,
		"a":                            `[{"family":"personal","bytes":240,"files":3}]`,
		"Arquivos de programas":        `[{"family":"programs","bytes":5060,"files":2}]`,
		"Arquivos de programas/Office": `[{"family":"personal","bytes":60,"files":1},{"family":"programs","bytes":5000,"files":1}]`,
		"f\xe9.txt":                    `[{"family":"personal","bytes":6,"files":1}]`,
		"trancada":                     `[]`,
		"Arquivos de programas/Office/WINWORD.EXE": `[{"family":"programs","bytes":5000,"files":1}]`,
	}
	check := func(where string, rows []rowComposition) {
		t.Helper()
		if len(rows) == 0 {
			t.Fatalf("%s: no rows", where)
		}
		for _, r := range rows {
			w, ok := want[r.Path]
			if !ok {
				continue
			}
			assertJSON(t, where+" "+r.Path, r.Composition, w)
		}
	}
	var children struct {
		Items []rowComposition `json:"items"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/children", s.Root), 200, &children)
	check("children", children.Items)
	e.get(t, fmt.Sprintf("/api/entries/%s/children?sort=name", s.ID("Arquivos de programas/Office")), 200, &children)
	check("office children", children.Items)

	var tm struct {
		Entry rowComposition   `json:"entry"`
		Items []rowComposition `json:"items"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s/treemap", s.Root), 200, &tm)
	check("treemap", append(tm.Items, tm.Entry))

	var res struct {
		Items []rowComposition `json:"items"`
	}
	e.get(t, "/api/search?source=disco&sort=name&limit=1000", 200, &res)
	check("search", res.Items)

	var detail struct {
		Entry rowComposition `json:"entry"`
	}
	for _, path := range []string{"", "Arquivos de programas/Office", "f\xe9.txt"} {
		e.get(t, fmt.Sprintf("/api/entries/%s", s.ID(path)), 200, &detail)
		check("detail", []rowComposition{detail.Entry})
	}
}

// GET /api/entries/{id} lists a folder's notable entries in stats.inside,
// as the scan stored them (design D21).
func TestEntryInside(t *testing.T) {
	e := newEnv(t)
	s := detailTree(t, e)
	var got struct {
		Stats struct {
			Inside json.RawMessage `json:"inside"`
		} `json:"stats"`
	}
	// The root is mostly programs: the group, the personal folder a, and the
	// loose text are notable; the unreadable folder is not.
	e.get(t, fmt.Sprintf("/api/entries/%s", s.Root), 200, &got)
	assertJSON(t, "root inside", got.Stats.Inside, "["+strings.Join([]string{
		insideJSON(s, "Arquivos de programas/Office", "application_installation", "programs", true, 5060, 2),
		insideJSON(s, "a", "", "", false, 240, 3),
		insideJSON(s, "f\xe9.txt", "", "personal", false, 6, 1),
	}, ",")+"]")
	e.get(t, fmt.Sprintf("/api/entries/%s", s.ID("a")), 200, &got)
	assertJSON(t, "a inside", got.Stats.Inside, `[]`)
}

// The owner's archive through the API (design D21): Home splits local's
// 406 GB into 400 GB personal and 6 GB programs, summing to the total; the
// Map row of local shows the same composition; and local's detail lists
// downloads, homeplanner, and the loose program.
func TestLocalCompositionThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	sfs := synthfs.New()
	root := sfs.Root("/src/archive")
	local := root.Dir("local")
	mtime := time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"fotos2019", "fotos2020", "fotos2021"} {
		d := local.Dir(name)
		d.File("IMG_0001.jpg", 66_600_000_000, mtime)
		d.File("IMG_0002.jpg", 66_600_000_000, mtime)
	}
	local.Dir("clips").File("clip.mp4", 400_000_000, mtime)
	local.Dir("downloads").File("ubuntu.iso", 4_000_000_000, mtime)
	app := local.Dir("homeplanner")
	app.File("homeplanner.exe", 1_500_000_000, mtime)
	app.File("core.dll", 399_000_000, mtime)
	app.File("unins000.exe", 100_000_000, mtime)
	local.File("putty.exe", 1_000_000, mtime)
	e.scanSynth(t, sfs, "archive", "/src/archive", root)

	var home struct {
		Totals struct {
			Bytes int64 `json:"bytes"`
			Files int64 `json:"files"`
		} `json:"totals"`
		ByFamily json.RawMessage `json:"by_family"`
	}
	e.get(t, "/api/home", 200, &home)
	assertJSON(t, "home by_family", home.ByFamily, `[{"family":"personal","bytes":400000000000,"files":7},`+
		`{"family":"programs","bytes":6000000000,"files":5},{"family":"disposable","bytes":0,"files":0},`+
		`{"family":"containers","bytes":0,"files":0}]`)
	if home.Totals.Bytes != 406_000_000_000 || home.Totals.Files != 12 {
		t.Errorf("home totals %+v", home.Totals)
	}

	var id string
	if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = 'archive' AND path = ?`,
		[]byte("local")).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	rows, err := e.st.Reader().Query(`SELECT id, path FROM entries WHERE source_id = 'archive'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var (
			i    int64
			path []byte
		)
		if err := rows.Scan(&i, &path); err != nil {
			t.Fatal(err)
		}
		ids[string(path)] = domain.EntryID(i).String()
	}
	rows.Close()

	var detail struct {
		Entry struct {
			Category    string          `json:"category"`
			Composition json.RawMessage `json:"composition"`
		} `json:"entry"`
		Stats struct {
			Inside json.RawMessage `json:"inside"`
		} `json:"stats"`
	}
	e.get(t, "/api/entries/"+id, 200, &detail)
	if detail.Entry.Category != "personal_media" {
		t.Errorf("local category %s, want personal_media", detail.Entry.Category)
	}
	assertJSON(t, "local composition", detail.Entry.Composition,
		`[{"family":"personal","bytes":400000000000,"files":7},{"family":"programs","bytes":6000000000,"files":5}]`)
	item := func(path, category, family string, group bool, bytes, files int64) string {
		raw, _ := json.Marshal([]byte(path))
		return fmt.Sprintf(`{"entry_id":"%s","path":%q,"path_b64":%s,"category":"%s","family":"%s","group":%t,"bytes":%d,"files":%d}`,
			ids[path], path, raw, category, family, group, bytes, files)
	}
	assertJSON(t, "local inside", detail.Stats.Inside, "["+strings.Join([]string{
		item("local/downloads", "download_collection", "containers", false, 4_000_000_000, 1),
		item("local/homeplanner", "application_installation", "programs", true, 1_999_000_000, 3),
		item("local/putty.exe", "installer_download", "programs", false, 1_000_000, 1),
	}, ",")+"]")
	t.Logf("local composition %s\nlocal inside %s", detail.Entry.Composition, detail.Stats.Inside)
}
