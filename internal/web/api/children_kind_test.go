package api

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"precious/internal/domain"
)

// folderChildren returns, from the ground truth, the raw names of the folder
// children of the folder at raw path parent ("" for the root) in name order,
// and the number of its other children and of those that are archives.
func (w *contentWorld) folderChildren(t *testing.T, parent string) (folders [][]byte, others, archives int) {
	t.Helper()
	prefix := []byte(parent + "/")
	if parent == "" {
		prefix = nil
	}
	for _, g := range w.gt.Entries {
		raw, err := g.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		name, ok := bytes.CutPrefix(raw, prefix)
		if !ok || len(name) == 0 || bytes.IndexByte(name, '/') >= 0 {
			continue
		}
		switch {
		case g.Kind == domain.EntryDirectory:
			folders = append(folders, name)
		case g.FileKind == string(domain.FileKindArchive):
			archives++
			others++
		default:
			others++
		}
	}
	slices.SortFunc(folders, bytes.Compare)
	return folders, others, archives
}

// wantFoldersOnly lists ref with kind=directory under every sort and order,
// in one page and in pages of two, and checks that each listing holds
// exactly the folders want, every row a folder, in the order asked.
func (w *contentWorld) wantFoldersOnly(t *testing.T, ref string, want [][]byte) {
	t.Helper()
	byName := w.allChildren(t, ref, "&kind=directory&sort=name", 1000)
	names := make([][]byte, len(byName))
	for i, r := range byName {
		names[i] = r.NameB64
	}
	if !slices.EqualFunc(names, want, bytes.Equal) {
		t.Fatalf("folders of %s by name = %q, want %q", ref, names, want)
	}
	for _, sort := range []string{"name", "bytes", "files", "newest"} {
		for _, order := range []string{"asc", "desc"} {
			q := "&kind=directory&sort=" + sort + "&order=" + order
			one := w.allChildren(t, ref, q, 1000)
			paged := w.allChildren(t, ref, q, 2)
			if !slices.Equal(contentIDs(one), contentIDs(paged)) || len(one) != len(want) {
				t.Errorf("%s %s: %q, paged %q, want %d folders", ref, q, contentIDs(one), contentIDs(paged), len(want))
			}
			for i, r := range one {
				if r.Kind != string(domain.EntryDirectory) || r.ArchiveState != nil {
					t.Errorf("%s %s: row %s (%s) is not a folder", ref, q, r.Path, r.Kind)
				}
				if i > 0 {
					c := compareRows(t, sort, one[i-1].row, r.row)
					if (order == "asc" && c > 0) || (order == "desc" && c < 0) {
						t.Errorf("%s %s: %s before %s", ref, q, one[i-1].Name, r.Name)
					}
				}
			}
		}
	}
}

// Scenario "Folders only" (r3 design D16): the children of the corpus root
// with kind=directory&sort=name are every folder child of the root, in name
// order, and nothing else; the same holds under every sort and order, in
// pages, for a folder holding files and archives (the corpus root holds only
// folders), and inside an archive. A cursor continues only the kind filter
// it was made for, and any other kind is 400 invalid_request.
func TestFoldersOnly(t *testing.T) {
	w := newContentWorld(t)
	root := w.s.Root.String()

	folders, others, _ := w.folderChildren(t, "")
	if len(folders) < 2 {
		t.Fatalf("the corpus root has %d folders; the scenario needs several", len(folders))
	}
	w.wantFoldersOnly(t, root, folders)
	if all := w.allChildren(t, root, "", 1000); len(all) != len(folders)+others {
		t.Fatalf("the unfiltered root lists %d children, want %d", len(all), len(folders)+others)
	}

	const downloads = "Downloads"
	folders, others, archives := w.folderChildren(t, downloads)
	if len(folders) == 0 || archives == 0 || others == archives {
		t.Fatalf("%s has %d folders, %d archives, and %d other children; the scenario needs folders, archives, and other files",
			downloads, len(folders), archives, others)
	}
	w.wantFoldersOnly(t, w.id(downloads), folders)

	// Inside an archive, the member folders of a member folder.
	dir := w.member(t, "Downloads/eMule0.47c-Installer.zip", "emule-0.47c")
	var memberFolders [][]byte
	for _, r := range w.allChildren(t, dir, "&sort=name", 1000) {
		if r.Kind == string(domain.EntryDirectory) {
			memberFolders = append(memberFolders, r.NameB64)
		}
	}
	if len(memberFolders) == 0 {
		t.Fatal("the member folder holds no folder")
	}
	w.wantFoldersOnly(t, dir, memberFolders)

	// A cursor continues only the kind filter it was made for.
	var p page
	w.get(t, "/api/entries/"+root+"/children?kind=directory&sort=name&limit=1", 200, &p)
	if p.NextCursor == nil {
		t.Fatal("no second page of the root's folders")
	}
	w.fails(t, "/api/entries/"+root+"/children?sort=name&limit=1&cursor="+*p.NextCursor, 400, "invalid_request")
	w.get(t, "/api/entries/"+root+"/children?sort=name&limit=1", 200, &p)
	if p.NextCursor == nil {
		t.Fatal("no second page of the root's children")
	}
	w.fails(t, "/api/entries/"+root+"/children?kind=directory&sort=name&limit=1&cursor="+*p.NextCursor, 400, "invalid_request")

	for _, q := range []string{"kind=file", "kind=Directory", "kind=archive", "kind=symlink", "kind=directory,file",
		"kind=directory&kind=directory"} {
		w.fails(t, fmt.Sprintf("/api/entries/%s/children?%s", root, q), 400, "invalid_request")
	}
}
