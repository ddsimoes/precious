package dates

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/dates/datestest"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
)

// Corpus folders the tests name (internal/corpus, design D19).
const (
	bahia     = "Viagens/2010-07 Bahia"
	natal     = "Viagens/2010-12 Natal"
	ouroPreto = "Viagens/2008-03 Ouro Preto"
	sonyKey   = "SONY|DSC-W55|"
	canonKey  = "Canon|Canon PowerShot SX230 HS|"
)

// r5 task 1.6, D11: Validate accepts exactly one form, entry_id only where
// the command allows it, within the bounds, with string IDs; a member ref
// is refused as entry_id and as a folder.
func TestValidate(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strconv.Itoa(i + 1)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		t      Targets
		single bool
		ok     bool
	}{
		{"nothing", Targets{}, true, false},
		{"two forms", Targets{EntryID: "1", EntryIDs: []string{"2"}}, true, false},
		{"entry_id", Targets{EntryID: "1"}, true, true},
		{"entry_id where only bulk is taken", Targets{EntryID: "1"}, false, false},
		{"entry_id of a member", Targets{EntryID: "m3"}, true, false},
		{"entry_id not an ID", Targets{EntryID: "abc"}, true, false},
		{"empty entry_ids", Targets{EntryIDs: []string{}}, false, false},
		{"1,000 entry_ids", Targets{EntryIDs: ids(1000)}, false, true},
		{"1,001 entry_ids", Targets{EntryIDs: ids(1001)}, false, false},
		{"entry_ids with a member", Targets{EntryIDs: []string{"1", "m2"}}, false, true},
		{"entry_ids with a bad ID", Targets{EntryIDs: []string{"1", "-2"}}, false, false},
		{"entry_ids with a camera", Targets{EntryIDs: []string{"1"}, CameraKey: sonyKey}, false, false},
		{"empty folder_ids", Targets{FolderIDs: []string{}}, false, false},
		{"100 folder_ids", Targets{FolderIDs: ids(100)}, false, true},
		{"101 folder_ids", Targets{FolderIDs: ids(101)}, false, false},
		{"folder_ids of a member", Targets{FolderIDs: []string{"m1"}}, false, false},
		{"folder_ids with a camera", Targets{FolderIDs: []string{"1"}, CameraKey: sonyKey}, false, true},
		{"a camera alone", Targets{CameraKey: sonyKey}, true, false},
	} {
		err := tc.t.Validate(tc.single)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && domain.CodeOf(err) != domain.CodeInvalidRequest {
			t.Errorf("%s: %v, want invalid_request", tc.name, err)
		}
	}
}

// r5 task 1.6, D11: ExpandTargets for each form, on the corpus: files keep
// their media and skip the rest as not_media (member refs included);
// folders expand to the media files below them in path order, outside the
// quarantine; a camera key takes that camera's photos directly in the
// folders; a quarantined target is in_quarantine; two sources, a file as a
// folder, and more than max are invalid_request; an unknown ID is
// not_found.
func TestExpandTargets(t *testing.T) {
	e, root, truth := newCorpusEnv(t)
	mtime := time.Date(2010, 7, 17, 10, 0, 0, 0, time.UTC)
	root.Dir(index.QuarantineName).Dir("7").Dir("1").File("IMG_0901.JPG", 100, mtime)
	e.scan(corpusSource)
	e.disk("outro", "/mnt/outro", posix, func(r *synthfs.Node) { r.Dir("Fotos").File("IMG_0001.JPG", 100, mtime) })
	datestest.Seed(t, e.st, e.src, e.svc, corpusSource)
	const max = 50000

	// The truth's media below a folder, in path order.
	below := func(dir string) []domain.EntryID {
		var paths [][]byte
		for _, en := range truth.Entries {
			raw, err := en.RawPath()
			if err != nil {
				t.Fatal(err)
			}
			if en.Date != nil && (dir == "" || strings.HasPrefix(string(raw), dir+"/")) {
				paths = append(paths, raw)
			}
		}
		slices.SortFunc(paths, bytes.Compare)
		out := make([]domain.EntryID, len(paths))
		for i, p := range paths {
			out[i] = e.id(corpusSource, string(p))
		}
		return out
	}
	expand := func(name string, tg Targets) Expanded {
		t.Helper()
		out, err := e.expand(tg, max)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.Source != corpusSource {
			t.Errorf("%s: source %q", name, out.Source)
		}
		return out
	}
	refuse := func(name string, tg Targets, max int, code domain.ErrorCode) {
		t.Helper()
		if _, err := e.expand(tg, max); domain.CodeOf(err) != code {
			t.Errorf("%s: %v, want %s", name, err, code)
		}
	}
	photo := bahia + "/IMG_0101.JPG"

	// Files: the media kept, the rest skipped in path order, then members.
	got := expand("files", Targets{EntryIDs: []string{"m7", e.ref(corpusSource, "Documentos/curriculo.doc"),
		e.ref(corpusSource, photo), e.ref(corpusSource, "Viagens"), e.ref(corpusSource, photo)}})
	wantSkips := []Skip{{Entry: domain.Ref{Entry: e.id(corpusSource, "Documentos/curriculo.doc")}, Reason: "not_media"},
		{Entry: domain.Ref{Entry: e.id(corpusSource, "Viagens")}, Reason: "not_media"},
		{Entry: domain.Ref{Member: 7}, Reason: "not_media"}}
	if !slices.Equal(got.Media, []domain.EntryID{e.id(corpusSource, photo)}) || !slices.Equal(got.Skipped, wantSkips) {
		t.Errorf("files: media %v, skipped %v; want %v, %v", got.Media, got.Skipped, e.id(corpusSource, photo), wantSkips)
	}
	if got := expand("one file", Targets{EntryID: e.ref(corpusSource, "Documentos/curriculo.doc")}); len(got.Media) != 0 ||
		len(got.Skipped) != 1 || got.Skipped[0].Reason != "not_media" {
		t.Errorf("one file that is not media: %+v", got)
	}

	// Folders: the media below them, in path order, nested folders once.
	viagens := below("Viagens")
	if len(viagens) != 25 {
		t.Fatalf("the truth has %d media below Viagens", len(viagens))
	}
	for name, tg := range map[string]Targets{
		"a folder": {FolderIDs: []string{e.ref(corpusSource, "Viagens")}},
		"nested folders": {FolderIDs: []string{e.ref(corpusSource, bahia), e.ref(corpusSource, "Viagens"),
			e.ref(corpusSource, natal)}},
	} {
		if got := expand(name, tg); !slices.Equal(got.Media, viagens) || len(got.Skipped) != 0 {
			t.Errorf("%s: %v, want %v", name, got.Media, viagens)
		}
	}
	// The top folder: every media file but the quarantined one.
	if got, want := expand("the top", Targets{FolderIDs: []string{e.ref(corpusSource, "")}}).Media, below(""); !slices.Equal(got, want) {
		t.Errorf("the top: %d media, want the truth's %d", len(got), len(want))
	}
	// A sibling whose name extends a target's sorts between the target and
	// its descendants (`Fotos` < `Fotos - Copia` < `Fotos/2006`): the
	// nested target still collapses, and the media come once, in path order.
	fotos := append(below("Fotos - Copia"), below("Fotos")...)
	if got := expand("a sibling between nested folders", Targets{FolderIDs: []string{e.ref(corpusSource, "Fotos"),
		e.ref(corpusSource, "Fotos - Copia"), e.ref(corpusSource, "Fotos/2006"),
		e.ref(corpusSource, "Fotos/2006/Praia")}}); !slices.Equal(got.Media, fotos) {
		t.Errorf("a sibling between nested folders: %d media %v, want %d %v", len(got.Media), got.Media, len(fotos), fotos)
	}

	// A camera in two events: its photos directly in them, in path order.
	var sony, canon []domain.EntryID
	for _, dir := range []string{bahia, natal} {
		for _, en := range truth.Entries {
			raw, _ := en.RawPath()
			if p := string(raw); strings.HasPrefix(p, dir+"/DSC") {
				sony = append(sony, e.id(corpusSource, p))
			} else if strings.HasPrefix(p, dir+"/IMG_") {
				canon = append(canon, e.id(corpusSource, p))
			}
		}
	}
	events := []string{e.ref(corpusSource, natal), e.ref(corpusSource, bahia)}
	for key, want := range map[string][]domain.EntryID{sonyKey: sony, canonKey: canon} {
		got := expand(key, Targets{FolderIDs: events, CameraKey: key})
		if len(want) != map[string]int{sonyKey: 12, canonKey: 8}[key] || !slices.Equal(got.Media, want) {
			t.Errorf("camera %s: %v, want %v", key, got.Media, want)
		}
	}
	if got := expand("a camera elsewhere", Targets{FolderIDs: []string{e.ref(corpusSource, ouroPreto)},
		CameraKey: sonyKey}); len(got.Media) != 0 {
		t.Errorf("the Sony in Ouro Preto: %v", got.Media)
	}

	// Refusals.
	quarantined := index.QuarantineName + "/7/1/IMG_0901.JPG"
	refuse("a quarantined file", Targets{EntryIDs: []string{e.ref(corpusSource, photo), e.ref(corpusSource, quarantined)}},
		max, domain.CodeInQuarantine)
	refuse("a quarantined single file", Targets{EntryID: e.ref(corpusSource, quarantined)}, max, domain.CodeInQuarantine)
	refuse("the quarantine", Targets{FolderIDs: []string{e.ref(corpusSource, index.QuarantineName)}}, max,
		domain.CodeInQuarantine)
	refuse("files on two sources", Targets{EntryIDs: []string{e.ref(corpusSource, photo),
		e.ref("outro", "Fotos/IMG_0001.JPG")}}, max, domain.CodeInvalidRequest)
	refuse("folders on two sources", Targets{FolderIDs: []string{e.ref(corpusSource, "Viagens"),
		e.ref("outro", "Fotos")}}, max, domain.CodeInvalidRequest)
	refuse("a file as a folder", Targets{FolderIDs: []string{e.ref(corpusSource, photo)}}, max,
		domain.CodeInvalidRequest)
	refuse("more than max", Targets{FolderIDs: []string{e.ref(corpusSource, "Viagens")}}, len(viagens)-1,
		domain.CodeInvalidRequest)
	refuse("more than max files", Targets{EntryIDs: []string{e.ref(corpusSource, photo),
		e.ref(corpusSource, bahia+"/IMG_0102.JPG")}}, 1, domain.CodeInvalidRequest)
	refuse("an unknown file", Targets{EntryIDs: []string{e.ref(corpusSource, photo), "99999999"}}, max,
		domain.CodeNotFound)
	refuse("an unknown folder", Targets{FolderIDs: []string{"99999999"}}, max, domain.CodeNotFound)
	refuse("a bad shape", Targets{}, max, domain.CodeInvalidRequest)
	if got, err := e.expand(Targets{FolderIDs: []string{e.ref(corpusSource, "Viagens")}}, len(viagens)); err != nil ||
		len(got.Media) != len(viagens) {
		t.Errorf("exactly max: %d media, %v", len(got.Media), err)
	}
}
