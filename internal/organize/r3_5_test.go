package organize

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"precious/internal/fsaccess/synthfs"
)

// row is what a test compares of an entry and of its dir_stats.
type row struct {
	path, state, kind, decision, eff, category string
	size, totalBytes, totalFiles               int64
	stats                                      string
}

// snapshot reads every entry of src by ID.
func (w *world) snapshot(src string) map[int64]row {
	w.t.Helper()
	rows, err := w.st.Reader().Query(`SELECT e.id, e.path, e.state, e.kind, COALESCE(e.decision, ''), e.eff_decision,
			COALESCE(e.category, ''), e.size, e.total_bytes, e.total_files,
			COALESCE(d.dirs || ' ' || d.files || ' ' || d.unreadable || ' ' || d.mount_boundaries || ' ' || d.by_kind || ' ' ||
				d.by_year || ' ' || d.by_family || ' ' || d.signals || ' ' || d.indicators || ' ' || d.inside, '')
		FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id WHERE e.source_id = ?`, src)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]row{}
	for rows.Next() {
		var (
			id   int64
			r    row
			path []byte
		)
		if err := rows.Scan(&id, &path, &r.state, &r.kind, &r.decision, &r.eff, &r.category, &r.size, &r.totalBytes,
			&r.totalFiles, &r.stats); err != nil {
			w.t.Fatal(err)
		}
		r.path = string(path)
		out[id] = r
	}
	return out
}

// owner reads the owner's intent and the content of src's entries: own
// decision, tags, override, and file_content state with content.
func (w *world) owner(src string) map[int64]string {
	w.t.Helper()
	rows, err := w.st.Reader().Query(`SELECT e.id, COALESCE(e.decision, '-'),
			COALESCE((SELECT group_concat(t.name) FROM entry_tags et JOIN tags t ON t.id = et.tag_id WHERE et.entry_id = e.id), '-'),
			COALESCE((SELECT o.category FROM entry_overrides o WHERE o.entry_id = e.id), '-'),
			COALESCE(fc.state || ':' || fc.content_id, '-')
		FROM entries e LEFT JOIN file_content fc ON fc.entry_id = e.id WHERE e.source_id = ?`, src)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var (
			id                       int64
			dec, tags, over, content string
		)
		if err := rows.Scan(&id, &dec, &tags, &over, &content); err != nil {
			w.t.Fatal(err)
		}
		out[id] = strings.Join([]string{dec, tags, over, content}, " ")
	}
	return out
}

// R3.5: the owner moves a folder carrying its own keep, a tag, and an
// override, with hashed files inside, into another folder through the
// commands and the real executor, then rescans. The folder and its contents
// keep their IDs, decisions, tags, override, and digests; the rescan adds no
// entry, marks none missing, and changes no folder's totals or dir_stats.
func TestR3_5DecisionsAndTagsFollowAMove(t *testing.T) {
	t.Parallel()
	w := newWorld(t).start()
	same := []byte("the same bytes in two files, so hashing reads both")
	w.disk("disk", "/disk", posix, func(root *synthfs.Node) {
		site := root.Dir("Projetos").Dir("Site")
		site.File("index.html", 0, mtime).Content(same)
		site.Dir("img").File("logo.png", 2048, mtime)
		site.Dir("docs").File("leia.txt", 300, mtime)
		root.Dir("Backup").File("index.html", 0, mtime).Content(same)
		root.Dir("Arquivo").Dir("2010").File("velho.doc", 500, mtime)
	})
	id := func(p string) string { return w.id("disk", p) }
	site := id("Projetos/Site")
	w.decide(site, "keep")
	w.tag("site", site, id("Projetos/Site/docs/leia.txt"))
	w.ok(http.StatusOK, "set-category", fmt.Sprintf(`{"entry_id":%q,"category":"documents"}`, site))
	w.idle()
	if n := w.count(`SELECT count(*) FROM file_content WHERE state = 'hashed'`); n != 2 {
		t.Fatalf("%d files hashed, want the two copies", n)
	}
	ids := w.subtreeIDs("disk", "Projetos/Site")
	intent := w.owner("disk")
	root := w.snapshot("disk")[mustID(t, id(""))]

	move, _ := w.plan("plan-move", fmt.Sprintf(`{"entry_id":%q,"destination_id":%q}`, site, id("Arquivo")))
	if got := w.run(move.ID); got.State != "done" || got.Counts["done"] != 1 {
		t.Fatalf("move %+v", got)
	}
	w.idle()
	if moved := w.subtreeIDs("disk", "Arquivo/Site"); !maps.Equal(moved, ids) {
		t.Fatalf("after the move %v, want %v", moved, ids)
	}
	if got := w.owner("disk"); !maps.Equal(got, intent) {
		t.Errorf("the owner's intent or digests changed:\n got %v\nwant %v", got, intent)
	}
	for p, want := range map[string]string{"Arquivo/Site": "keep", "Arquivo/Site/img/logo.png": "keep",
		"Arquivo/2010/velho.doc": "undecided"} {
		if r := w.snapshot("disk")[mustID(t, id(p))]; r.eff != want {
			t.Errorf("%s reads %s, want %s", p, r.eff, want)
		}
	}
	if r := w.snapshot("disk")[mustID(t, id(""))]; r.totalBytes != root.totalBytes || r.totalFiles != root.totalFiles {
		t.Errorf("the root's totals went from %d/%d to %d/%d", root.totalBytes, root.totalFiles, r.totalBytes, r.totalFiles)
	}

	afterMove := w.snapshot("disk")
	w.scan("disk")
	afterScan := w.snapshot("disk")
	if !maps.Equal(afterScan, afterMove) {
		var diff []string
		for id, r := range afterScan {
			if before, ok := afterMove[id]; !ok {
				diff = append(diff, fmt.Sprintf("new %d %+v", id, r))
			} else if before != r {
				diff = append(diff, fmt.Sprintf("changed %d\n   %+v\n-> %+v", id, before, r))
			}
		}
		for id, r := range afterMove {
			if _, ok := afterScan[id]; !ok {
				diff = append(diff, fmt.Sprintf("gone %d %+v", id, r))
			}
		}
		slices.Sort(diff)
		t.Errorf("the rescan changed the index:\n%s", strings.Join(diff, "\n"))
	}
	if n := w.count(`SELECT count(*) FROM entries WHERE source_id = 'disk' AND state = 'missing'`); n != 0 {
		t.Errorf("%d entries missing after the rescan", n)
	}
	if got := w.owner("disk"); !maps.Equal(got, intent) {
		t.Errorf("the rescan changed the owner's intent or digests:\n got %v\nwant %v", got, intent)
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	var id int64
	if _, err := fmt.Sscan(s, &id); err != nil {
		t.Fatal(err)
	}
	return id
}
