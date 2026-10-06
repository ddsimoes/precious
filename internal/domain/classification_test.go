package domain

import (
	"slices"
	"testing"
)

// roundTrip checks that every value of an enum parses back to itself, that
// the list has want distinct values, and that bad names are invalid_request.
func roundTrip[T ~string](t *testing.T, what string, values []T, want int, parse func(string) (T, error), bad ...string) {
	t.Helper()
	if len(values) != want {
		t.Errorf("%s: %d values, want %d", what, len(values), want)
	}
	seen := map[T]bool{}
	for _, v := range values {
		if seen[v] {
			t.Errorf("%s: %q listed twice", what, v)
		}
		seen[v] = true
		if got, err := parse(string(v)); err != nil || got != v {
			t.Errorf("%s: parse(%q) = %q, %v", what, v, got, err)
		}
	}
	for _, s := range append([]string{"", " "}, bad...) {
		if got, err := parse(s); CodeOf(err) != CodeInvalidRequest || got != "" {
			t.Errorf("%s: parse(%q) = %q, %v; want invalid_request", what, s, got, err)
		}
	}
}

func TestVocabularyRoundTrips(t *testing.T) {
	roundTrip(t, "file kind", FileKinds, 10, ParseFileKind, "Image", "source_file", "unknown")
	roundTrip(t, "category", Categories, 16, ParseCategory, "Cache", "photos", "unknown ")
	roundTrip(t, "family", Families, 4, ParseFamily, "Personal", "system")
	roundTrip(t, "trait", Traits, 5, ParseTrait, "backup_container", "contains_photos")
	roundTrip(t, "triage", Triages, 3, ParseTriage, "preserve", "cleanup_candidate", "expand")
	roundTrip(t, "decision", Decisions, 4, ParseDecision, "inherit", "preserve", "Keep")
}

// precious-spec §6.6: the 16 categories fall into four families of four, in
// table order.
func TestFamilyOfCoversEveryCategory(t *testing.T) {
	want := map[Category]Family{
		CategoryPersonalMedia:            FamilyPersonal,
		CategoryDocuments:                FamilyPersonal,
		CategorySourceProject:            FamilyPersonal,
		CategoryApplicationUserData:      FamilyPersonal,
		CategoryApplicationInstallation:  FamilyPrograms,
		CategoryApplicationConfiguration: FamilyPrograms,
		CategoryOSInstallation:           FamilyPrograms,
		CategoryInstallerDownload:        FamilyPrograms,
		CategorySystemJunk:               FamilyDisposable,
		CategoryCache:                    FamilyDisposable,
		CategoryTemporaryData:            FamilyDisposable,
		CategoryGeneratedArtifacts:       FamilyDisposable,
		CategoryDownloadCollection:       FamilyContainers,
		CategoryBackup:                   FamilyContainers,
		CategoryMixed:                    FamilyContainers,
		CategoryUnknown:                  FamilyContainers,
	}
	if len(want) != len(Categories) {
		t.Fatalf("table has %d categories, Categories %d", len(want), len(Categories))
	}
	perFamily := map[Family]int{}
	for _, c := range Categories {
		got := FamilyOf(c)
		if got != want[c] {
			t.Errorf("FamilyOf(%s) = %q, want %q", c, got, want[c])
		}
		if !slices.Contains(Families, got) {
			t.Errorf("FamilyOf(%s) = %q, not a family", c, got)
		}
		perFamily[got]++
	}
	for _, f := range Families {
		if perFamily[f] != 4 {
			t.Errorf("family %s has %d categories, want 4", f, perFamily[f])
		}
	}
	if got := FamilyOf("photos"); got != "" {
		t.Errorf(`FamilyOf("photos") = %q, want ""`, got)
	}
}

// Design D21: a file counts under its category's family; an unknown or
// absent category falls back to the file kind's family.
func TestFileFamily(t *testing.T) {
	byKind := map[FileKind]Family{
		FileKindImage: FamilyPersonal, FileKindVideo: FamilyPersonal, FileKindAudio: FamilyPersonal,
		FileKindDocument: FamilyPersonal, FileKindSource: FamilyPersonal,
		FileKindInstaller: FamilyPrograms, FileKindExecutable: FamilyPrograms,
		FileKindSystem: FamilyDisposable, FileKindArchive: FamilyContainers, FileKindOther: FamilyContainers,
	}
	if len(byKind) != len(FileKinds) {
		t.Fatalf("table has %d kinds, FileKinds %d", len(byKind), len(FileKinds))
	}
	for k, want := range byKind {
		for _, c := range []Category{CategoryUnknown, ""} {
			if got := FileFamily(c, k); got != want {
				t.Errorf("FileFamily(%q, %s) = %s, want %s", c, k, got, want)
			}
		}
	}
	for _, c := range Categories {
		if c == CategoryUnknown {
			continue
		}
		if got := FileFamily(c, FileKindImage); got != FamilyOf(c) {
			t.Errorf("FileFamily(%s, image) = %s, want %s", c, got, FamilyOf(c))
		}
	}
}
