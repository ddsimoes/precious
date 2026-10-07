package api

import (
	"fmt"
	"reflect"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// pathDetail is the part of GET /api/entries/{ref} that the Map's path and
// start read (r2b design D9), and the archive note (D10).
type pathDetail struct {
	Ancestors []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		OnlyChild bool   `json:"only_child"`
	} `json:"ancestors"`
	OnlyFolder *string `json:"only_folder"`
	Archive    *struct {
		State string `json:"state"`
	} `json:"archive"`
	ArchiveNote *string `json:"archive_note"`
}

func (e *env) pathDetail(t *testing.T, ref string) pathDetail {
	t.Helper()
	var d pathDetail
	e.get(t, fmt.Sprintf("/api/entries/%s", ref), 200, &d)
	return d
}

// onlyChildren lists each ancestor as name:only_child.
func (d pathDetail) onlyChildren() []string {
	out := []string{}
	for _, a := range d.Ancestors {
		out = append(out, fmt.Sprintf("%s:%v", a.Name, a.OnlyChild))
	}
	return out
}

// The inventory-explorer scenario "A source whose top holds one folder": the
// top holds only old-disk, which holds only home.
func TestEntryPathCollapse(t *testing.T) {
	e := newEnv(t)
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "velho", CreateSource: true, MountPoint: "/mnt/velho", Nodes: []indextest.Node{
		{Path: "old-disk/home/docs/carta.txt", Size: 10, MTime: year(2004)},
		{Path: "old-disk/home/notas.txt", Size: 20, MTime: year(2005)},
		{Path: "old-disk/home/vazia", Kind: domain.EntryDirectory},
	}})

	for _, c := range []struct {
		path string
		want *string
	}{
		{"", new(s.ID("old-disk").String())},
		{"old-disk", new(s.ID("old-disk/home").String())},
		// Three entries, one file, an empty folder: nothing to follow.
		{"old-disk/home", nil},
		{"old-disk/home/docs", nil},
		{"old-disk/home/vazia", nil},
		{"old-disk/home/notas.txt", nil},
	} {
		id := s.Root
		if c.path != "" {
			id = s.ID(c.path)
		}
		if got := e.pathDetail(t, id.String()).OnlyFolder; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: only_folder %v, want %v", c.path, str(got), str(c.want))
		}
	}

	got := e.pathDetail(t, s.ID("old-disk/home/docs/carta.txt").String()).onlyChildren()
	want := []string{":true", "old-disk:true", "home:false", "docs:true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ancestors %v, want %v", got, want)
	}

	// A missing entry still shows in the folder, so it still counts.
	e.exec(t, `UPDATE entries SET state = 'missing' WHERE id = ?`, int64(s.ID("old-disk/home/docs/carta.txt")))
	e.exec(t, `INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		SELECT source_id, id, 'velha.txt', 'old-disk/velha.txt', 'file', 'missing', first_seen, last_seen, scan_gen
		FROM entries WHERE id = ?`, int64(s.ID("old-disk")))
	if got := e.pathDetail(t, s.ID("old-disk").String()).OnlyFolder; got != nil {
		t.Errorf("old-disk with a missing file: only_folder %s", *got)
	}
	got = e.pathDetail(t, s.ID("old-disk/home/docs/carta.txt").String()).onlyChildren()
	want = []string{":true", "old-disk:false", "home:false", "docs:true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ancestors %v, want %v", got, want)
	}
}

// The archive-contents scenario "A 7z file", and the other notes of
// archives that were not opened (r2b design D10).
func TestArchiveNoteUnopened(t *testing.T) {
	e := newEnv(t)
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "misc", CreateSource: true, MountPoint: "/mnt/misc", Nodes: []indextest.Node{
		{Path: "backup.7z", Size: 100, MTime: year(2010)},
		{Path: "FOTOS.RAR", Size: 100, MTime: year(2010)},
		{Path: "pacote.zip", Size: 100, MTime: year(2010)},
		{Path: "sumida.tar.gz", Size: 100, MTime: year(2010)},
		{Path: "notas.txt", Size: 10, MTime: year(2010)},
		{Path: "pasta.7z", Kind: domain.EntryDirectory},
	}})
	e.exec(t, `UPDATE entries SET state = 'missing' WHERE id = ?`, int64(s.ID("sumida.tar.gz")))
	for path, want := range map[string]string{
		"backup.7z":     "unsupported",
		"FOTOS.RAR":     "unsupported",
		"pacote.zip":    "not_listed",
		"sumida.tar.gz": "<null>",
		"notas.txt":     "<null>",
		"pasta.7z":      "<null>",
	} {
		d := e.pathDetail(t, s.ID(path).String())
		if str(d.ArchiveNote) != want || d.Archive != nil {
			t.Errorf("%s: archive_note %q, archive %v; want %q", path, str(d.ArchiveNote), d.Archive, want)
		}
	}
}

// A listed zip has its archive and no note, and its members' paths
// collapse like folders; a member that is an archive is not opened.
func TestArchiveNoteListed(t *testing.T) {
	w := newContentWorld(t)
	const zip = "Downloads/eMule0.47c-Installer.zip"
	d := w.pathDetail(t, w.id(zip))
	if d.Archive == nil || d.Archive.State != "complete" || d.ArchiveNote != nil {
		t.Errorf("%s: archive %v, archive_note %q", zip, d.Archive, str(d.ArchiveNote))
	}
	if d.OnlyFolder != nil {
		t.Errorf("%s: only_folder %s for a file", zip, *d.OnlyFolder)
	}

	dll := w.member(t, zip, "emule-0.47c/lang/pt_BR.dll")
	got := w.pathDetail(t, dll)
	want := []string{":false", "Downloads:false", "eMule0.47c-Installer.zip:true", "emule-0.47c:false", "lang:true"}
	if !reflect.DeepEqual(got.onlyChildren(), want) || got.ArchiveNote != nil {
		t.Errorf("%s: ancestors %v, archive_note %q; want %v", dll, got.onlyChildren(), str(got.ArchiveNote), want)
	}

	// A zip and a 7z inside the zip.
	archive := w.s.ID(zip)
	folder := w.arcs[zip].Member("emule-0.47c")
	for _, name := range []string{"plugins.zip", "skins.7z"} {
		w.exec(t, `INSERT INTO archive_members (archive_id, parent_id, name, path, kind, size, state)
			VALUES (?, ?, ?, ?, 'file', 10, 'unique_size')`, int64(archive), int64(folder), name, "emule-0.47c/"+name)
		var id int64
		if err := w.st.Reader().QueryRow(`SELECT id FROM archive_members WHERE archive_id = ? AND name = ?`, int64(archive), name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ref := domain.Ref{Member: domain.MemberID(id)}.String()
		if d := w.pathDetail(t, ref); str(d.ArchiveNote) != "nested" || d.Archive != nil {
			t.Errorf("%s: archive_note %q, archive %v", name, str(d.ArchiveNote), d.Archive)
		}
	}
	if d := w.pathDetail(t, w.member(t, zip, "emule-0.47c/license.txt")); d.ArchiveNote != nil {
		t.Errorf("license.txt: archive_note %q", *d.ArchiveNote)
	}
	if d := w.pathDetail(t, w.member(t, zip, "emule-0.47c")); d.ArchiveNote != nil || d.OnlyFolder != nil {
		t.Errorf("member folder: archive_note %q, only_folder %v", str(d.ArchiveNote), str(d.OnlyFolder))
	}
}
