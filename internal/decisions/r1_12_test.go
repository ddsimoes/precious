package decisions

import (
	"context"
	"slices"
	"testing"

	"precious/internal/domain"
	"precious/internal/search"
)

// R1.12 A folder tag applies to everything inside: familia on Fotos/2006
// shows on every entry below it as inherited from Fotos/2006, beside an own
// tag, and a search by familia finds the folder and everything inside it.
func TestR1_12AFolderTagAppliesToEverythingInside(t *testing.T) {
	e := newEnv(t)
	s, gt := e.seedCorpus(t)
	folder := "Fotos/2006"
	photo := folder + "/Casamento/DSC02101.JPG"
	familia := e.createTag(t, "familia")
	dudu := e.createTag(t, "dudu")
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID(folder)}, Add: []int64{familia.ID}})
	e.setTags(t, SetTags{EntryIDs: []domain.EntryID{s.ID(photo)}, Add: []int64{dudu.ID}})

	var want []string
	for _, en := range rawPaths(t, gt) {
		if en.Path == folder || below(en.Path, folder) {
			want = append(want, en.Path)
		}
		if !below(en.Path, folder) {
			continue
		}
		tags := e.intent(t, s.ID(en.Path)).Tags
		wantTags := []TagRef{{ID: familia.ID, Name: "familia", From: &Ref{ID: s.ID(folder), Path: folder, PathB64: []byte(folder)}}}
		if en.Path == photo {
			wantTags = []TagRef{{ID: dudu.ID, Name: "dudu", Own: true}, wantTags[0]}
		}
		if !tagsEqual(tags, wantTags) {
			t.Errorf("%q tags %s, want %s", en.Path, fmtTags(tags), fmtTags(wantTags))
		}
	}
	if len(want) < 10 {
		t.Fatalf("the corpus has %d entries in %s; the test needs more", len(want), folder)
	}
	if got := e.intent(t, s.ID(folder)).Tags; !tagsEqual(got, []TagRef{{ID: familia.ID, Name: "familia", Own: true}}) {
		t.Errorf("%q tags %s, want familia own", folder, fmtTags(got))
	}
	if got := e.intent(t, s.ID("Fotos - Copia/2006")).Tags; len(got) != 0 {
		t.Errorf("Fotos - Copia/2006 tags %s, want none", fmtTags(got))
	}

	ids, err := search.Resolve(context.Background(), e.st.Reader(), search.Query{Tags: []int64{familia.ID}}, search.MaxResolve)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, id := range ids {
		var p []byte
		if err := e.st.Reader().QueryRow(`SELECT path FROM entries WHERE id = ?`, int64(id)).Scan(&p); err != nil {
			t.Fatal(err)
		}
		found = append(found, string(p))
	}
	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("search by familia found %d entries %v, want %d %v", len(found), found, len(want), want)
	}
}
