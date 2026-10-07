package rules

import (
	"slices"
	"testing"

	"precious/internal/domain"
)

// ownerRules classify a project by its .git, a program by an .exe below it,
// photos and documents by name (two at one priority conflict), recovered
// fragments for review, and add traits from lower-priority rules, one of
// which also names a category it never decides.
const ownerRules = `
[[rules]]
id = "project"
target = "folder"
child_signals = { any = ["vcs"] }
priority = 60
category = "source_project"
explain = "A project."

[[rules]]
id = "output_below"
target = "folder"
subtree_signals = [{ signal = "compiled" }]
priority = 5
category = "generated_artifacts"
traits = ["possible_generated_content"]
explain = "Output below."

[[rules]]
id = "docs_below"
target = "folder"
subtree_signals = [{ signal = "doc" }]
priority = 100
traits = ["contains_user_material"]
explain = "Documents below."

[[rules]]
id = "app"
target = "folder"
subtree_signals = [{ signal = "exe" }]
priority = 40
category = "application_installation"
explain = "A program."

[[rules]]
id = "media"
target = "folder"
name = ["*fotos*"]
priority = 50
category = "personal_media"
explain = "Photos."

[[rules]]
id = "docs"
target = "folder"
name = ["*docs*"]
priority = 50
category = "documents"
explain = "Documents."

[[rules]]
id = "found_folder"
target = "folder"
name = ["found.???"]
priority = 100
category = "system_junk"
triage = "review"
explain = "Recovered fragments."

[[rules]]
id = "installer"
target = "file"
name = ["*.exe"]
priority = 10
category = "installer_download"
explain = "An installer."
`

// r2b 2.1: an owner category replaces the rules' and sets family and
// triage as a rule category would, keeping the traits, rule IDs, and reason
// the rules observed. The folder's group flag follows the new category.
func TestApplyOwnerCategory(t *testing.T) {
	p := testPolicy(t, ownerRules)
	site := p.ClassifyFolder(FolderFacts{Name: []byte("site_antigo"), ChildSignals: map[SignalID]int{"vcs": 1},
		SubtreeSignals: map[SignalID]int{"vcs": 1, "compiled": 3, "doc": 1}, Indicators: 1})
	checkResult(t, "site_antigo", site, domain.CategorySourceProject, domain.TriageKeep, ReasonMatched,
		"docs_below", "project", "output_below")
	if !site.Group {
		t.Fatal("a source project is not a group")
	}

	got := ApplyOwner(site, domain.Override{Category: domain.CategoryDocuments})
	if got.Category != domain.CategoryDocuments || got.Family != domain.FamilyPersonal || got.Triage != domain.TriageKeep ||
		got.Group || got.Veto {
		t.Errorf("owner documents = %s/%s/%s group %v veto %v; want documents/personal/keep, no group, no veto",
			got.Category, got.Family, got.Triage, got.Group, got.Veto)
	}
	if !slices.Equal(got.Rules, site.Rules) || !slices.Equal(got.Traits, site.Traits) || got.Reason != site.Reason {
		t.Errorf("owner category changed what the rules observed: %v %v %s", got.Rules, got.Traits, got.Reason)
	}

	// A file takes the category's family and triage, and is never a group
	// or vetoed, whatever the category.
	file := p.ClassifyFile(FileFacts{Name: []byte("setup.exe"), Kind: domain.FileKindExecutable})
	got = ApplyOwner(file, domain.Override{Category: domain.CategoryCache, Group: ptr(true)})
	if got.Category != domain.CategoryCache || got.Family != domain.FamilyDisposable || got.Triage != domain.TriageDiscard ||
		got.Group || got.Veto {
		t.Errorf("owner cache on a file = %+v", got)
	}

	// The zero override follows the rules.
	if got := ApplyOwner(site, domain.Override{}); got.Category != site.Category || got.Triage != site.Triage ||
		got.Group != site.Group || got.Veto != site.Veto {
		t.Errorf("no override changed the result: %+v", got)
	}
}

// r2b 2.1: a folder whose triage would be discard with user material below
// it gets review and the veto, whoever set the category; a kept category
// lifts the rules' veto, and without user material discard stands.
func TestApplyOwnerVeto(t *testing.T) {
	p := testPolicy(t, ownerRules)
	office := p.ClassifyFolder(FolderFacts{Name: []byte("Microsoft Office"),
		SubtreeSignals: map[SignalID]int{"exe": 1, "doc": 1}, Indicators: 1})
	if office.Category != domain.CategoryApplicationInstallation || !office.Veto {
		t.Fatalf("Microsoft Office = %+v, want a vetoed application_installation", office)
	}
	cases := []struct {
		what      string
		res       Result
		category  domain.Category
		triage    domain.Triage
		veto      bool
		wantGroup bool
	}{
		{"cache over user material", office, domain.CategoryCache, domain.TriageReview, true, true},
		{"temporary over user material", office, domain.CategoryTemporaryData, domain.TriageReview, true, false},
		{"documents over user material", office, domain.CategoryDocuments, domain.TriageKeep, false, false},
		{"cache without user material", p.ClassifyFolder(FolderFacts{Name: []byte("Winamp"),
			SubtreeSignals: map[SignalID]int{"exe": 1}}), domain.CategoryCache, domain.TriageDiscard, false, true},
	}
	for _, c := range cases {
		got := ApplyOwner(c.res, domain.Override{Category: c.category})
		if got.Category != c.category || got.Triage != c.triage || got.Veto != c.veto || got.Group != c.wantGroup {
			t.Errorf("%s: %s/%s veto %v group %v; want %s/%s veto %v group %v", c.what,
				got.Category, got.Triage, got.Veto, got.Group, c.category, c.triage, c.veto, c.wantGroup)
		}
	}
}

// r2b 2.1: the group flag has three states: the owner's mark, the owner's
// unmark, and the rules' (the effective category's). Only folders are
// groups.
func TestApplyOwnerGroupStates(t *testing.T) {
	p := testPolicy(t, ownerRules)
	app := p.ClassifyFolder(FolderFacts{Name: []byte("WinZip"), SubtreeSignals: map[SignalID]int{"exe": 1}})
	praia := p.ClassifyFolder(FolderFacts{Name: []byte("fotos praia")})
	cases := []struct {
		what string
		res  Result
		o    domain.Override
		want bool
	}{
		{"rule group, rules", app, domain.Override{}, true},
		{"rule group, unmarked", app, domain.Override{Group: ptr(false)}, false},
		{"rule group, marked", app, domain.Override{Group: ptr(true)}, true},
		{"photos, rules", praia, domain.Override{}, false},
		{"photos, marked", praia, domain.Override{Group: ptr(true)}, true},
		{"photos, unmarked", praia, domain.Override{Group: ptr(false)}, false},
		{"owner cache, rules", praia, domain.Override{Category: domain.CategoryCache}, true},
		{"owner cache, unmarked", praia, domain.Override{Category: domain.CategoryCache, Group: ptr(false)}, false},
		{"owner documents on a program, rules", app, domain.Override{Category: domain.CategoryDocuments}, false},
		{"owner documents on a program, marked", app, domain.Override{Category: domain.CategoryDocuments, Group: ptr(true)}, true},
	}
	for _, c := range cases {
		if got := ApplyOwner(c.res, c.o); got.Group != c.want {
			t.Errorf("%s: group %v, want %v", c.what, got.Group, c.want)
		}
	}
	if got := ApplyOwner(praia, domain.Override{Group: ptr(true)}); got.Category != praia.Category ||
		got.Triage != praia.Triage || got.Family != praia.Family {
		t.Errorf("a group mark changed the classification: %+v", got)
	}
}

// r2b 2.1: what the rules would set is derived from the stored rule IDs by
// the rules' own priority resolution: Recall gives back every result the
// rules gave, a conflict and no match included, and CategoryOf its category.
func TestRecallAndCategoryOf(t *testing.T) {
	p := testPolicy(t, ownerRules)
	folders := []FolderFacts{
		{Name: []byte("site"), ChildSignals: map[SignalID]int{"vcs": 1}, SubtreeSignals: map[SignalID]int{"compiled": 2, "doc": 1}, Indicators: 1},
		{Name: []byte("build"), SubtreeSignals: map[SignalID]int{"compiled": 2}},
		{Name: []byte("Microsoft Office"), SubtreeSignals: map[SignalID]int{"exe": 1}, Indicators: 2},
		{Name: []byte("fotos docs")}, // two categories at one priority conflict
		{Name: []byte("letters"), SubtreeSignals: map[SignalID]int{"doc": 1}, Indicators: 1},
		{Name: []byte("nothing")},
		{Name: []byte("found.000"), Indicators: 1},
	}
	for _, f := range folders {
		want := p.ClassifyFolder(f)
		got := p.Recall(want.Rules, true, f.Indicators > 0)
		if got.Category != want.Category || got.Family != want.Family || got.Triage != want.Triage ||
			got.Group != want.Group || got.Veto != want.Veto || got.Reason != want.Reason ||
			!slices.Equal(got.Traits, want.Traits) || !slices.Equal(got.Rules, want.Rules) {
			t.Errorf("%s: Recall = %+v, want %+v", f.Name, got, want)
		}
		if c := p.CategoryOf(want.Rules); c != want.Category {
			t.Errorf("%s: CategoryOf = %s, want %s", f.Name, c, want.Category)
		}
	}
	file := p.ClassifyFile(FileFacts{Name: []byte("setup.exe"), Kind: domain.FileKindExecutable})
	if got := p.Recall(file.Rules, false, false); got.Category != domain.CategoryInstallerDownload ||
		got.Triage != domain.TriageDiscard || got.Group || got.Veto {
		t.Errorf("file Recall = %+v", got)
	}

	for _, c := range []struct {
		ids  []string
		want domain.Category
	}{
		{[]string{"docs_below", "project", "output_below"}, domain.CategorySourceProject},
		{[]string{"output_below"}, domain.CategoryGeneratedArtifacts},
		{[]string{"media", "docs"}, domain.CategoryUnknown},
		{nil, domain.CategoryUnknown},
		{[]string{"docs_below"}, domain.CategoryUnknown},
		// A rule of earlier rules is ignored.
		{[]string{"gone_rule", "app"}, domain.CategoryApplicationInstallation},
	} {
		if got := p.CategoryOf(c.ids); got != c.want {
			t.Errorf("CategoryOf(%v) = %s, want %s", c.ids, got, c.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }
