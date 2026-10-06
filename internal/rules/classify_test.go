package rules

import (
	"slices"
	"strings"
	"testing"

	"precious/internal/domain"
)

// testMarkers is a small markers file for the grammar tests.
const testMarkers = `
version = "markers-test"

[[signals]]
id = "vcs"
names = [".git"]

[[signals]]
id = "exe"
kinds = ["file"]
names = ["*.exe"]

[[signals]]
id = "doc"
kinds = ["file"]
names = ["*.doc"]
indicator = true

[[signals]]
id = "compiled"
kinds = ["file"]
names = ["*.class"]

[file_kinds]
image = ["jpg"]
video = ["avi"]
document = ["doc"]
executable = ["exe", "dll"]
system = ["chk"]

[[file_kind_pairs]]
ext = "bin"
with = "cue"
kind = "archive"
`

func testPolicy(t *testing.T, rules string) *Policy {
	t.Helper()
	p, err := Load([]byte(testMarkers), []byte("version = \"rules-test\"\n"+rules))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func checkResult(t *testing.T, what string, got Result, category domain.Category, triage domain.Triage, reason string, rules ...string) {
	t.Helper()
	if got.Category != category || got.Triage != triage || got.Reason != reason || !slices.Equal(got.Rules, rules) {
		t.Errorf("%s = %s/%s/%s %v; want %s/%s/%s %v", what,
			got.Category, got.Triage, got.Reason, got.Rules, category, triage, reason, rules)
	}
	if got.Family != domain.FamilyOf(category) {
		t.Errorf("%s family = %s, want %s", what, got.Family, domain.FamilyOf(category))
	}
}

func folder(name string) FolderFacts { return FolderFacts{Name: []byte(name)} }

// Task 3.2: a name condition matches the entry's own name, for files and
// folders alike, and a rule of one target never classifies the other.
func TestConditionName(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "thumbs"
target = "file"
name = ["Thumbs.db"]
priority = 10
category = "system_junk"
explain = "Thumbnails."

[[rules]]
id = "modules"
target = "folder"
name = ["node_modules"]
priority = 10
category = "generated_artifacts"
explain = "Packages."
`)
	checkResult(t, "file THUMBS.DB", p.ClassifyFile(FileFacts{Name: []byte("THUMBS.DB")}),
		domain.CategorySystemJunk, domain.TriageDiscard, ReasonMatched, "thumbs")
	checkResult(t, "folder Thumbs.db", p.ClassifyFolder(folder("Thumbs.db")),
		domain.CategoryUnknown, domain.TriageReview, ReasonNoRuleMatched)
	checkResult(t, "folder node_modules", p.ClassifyFolder(folder("node_modules")),
		domain.CategoryGeneratedArtifacts, domain.TriageDiscard, ReasonMatched, "modules")
	checkResult(t, "file node_modules", p.ClassifyFile(FileFacts{Name: []byte("node_modules")}),
		domain.CategoryUnknown, domain.TriageReview, ReasonNoRuleMatched)
}

// Task 3.2: child_signals any, all, and none look at the immediate children
// only.
func TestConditionChildSignals(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "any"
target = "folder"
child_signals = { any = ["vcs", "exe"] }
priority = 10
traits = ["contains_vcs"]
explain = "Any."

[[rules]]
id = "all"
target = "folder"
child_signals = { all = ["vcs", "exe"] }
priority = 10
traits = ["possible_generated_content"]
explain = "All."

[[rules]]
id = "none"
target = "folder"
child_signals = { any = ["doc"], none = ["exe"] }
priority = 10
traits = ["contains_user_material"]
explain = "None."
`)
	cases := []struct {
		children map[SignalID]int
		subtree  map[SignalID]int
		want     []string
	}{
		{nil, nil, nil},
		{map[SignalID]int{"vcs": 1}, nil, []string{"any"}},
		{map[SignalID]int{"vcs": 1, "exe": 2}, nil, []string{"any", "all"}},
		{map[SignalID]int{"doc": 1}, nil, []string{"none"}},
		{map[SignalID]int{"doc": 1, "exe": 1}, nil, []string{"any"}},
		{map[SignalID]int{"vcs": 0}, map[SignalID]int{"vcs": 3, "exe": 3, "doc": 3}, nil}, // below is not a child
	}
	for _, c := range cases {
		got := p.ClassifyFolder(FolderFacts{Name: []byte("x"), ChildSignals: c.children, SubtreeSignals: c.subtree})
		if !slices.Equal(got.Rules, c.want) {
			t.Errorf("children %v, subtree %v: rules %v, want %v", c.children, c.subtree, got.Rules, c.want)
		}
	}
}

// Task 3.2: subtree_signals ask for a minimum count anywhere below, all of
// them at once.
func TestConditionSubtreeSignals(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "build_output"
target = "folder"
name = ["bin", "build"]
subtree_signals = [{ signal = "compiled", min_count = 3 }, { signal = "exe" }]
priority = 10
category = "generated_artifacts"
explain = "Output."
`)
	cases := []struct {
		name    string
		subtree map[SignalID]int
		want    domain.Category
	}{
		{"bin", map[SignalID]int{"compiled": 3, "exe": 1}, domain.CategoryGeneratedArtifacts},
		{"BUILD", map[SignalID]int{"compiled": 7, "exe": 4}, domain.CategoryGeneratedArtifacts},
		{"bin", map[SignalID]int{"compiled": 2, "exe": 1}, domain.CategoryUnknown}, // below min_count
		{"bin", map[SignalID]int{"compiled": 3}, domain.CategoryUnknown},           // min_count defaults to 1
		{"src", map[SignalID]int{"compiled": 3, "exe": 1}, domain.CategoryUnknown}, // name still applies
	}
	for _, c := range cases {
		got := p.ClassifyFolder(FolderFacts{Name: []byte(c.name), SubtreeSignals: c.subtree})
		if got.Category != c.want {
			t.Errorf("%s with %v: %s, want %s", c.name, c.subtree, got.Category, c.want)
		}
	}
}

// Task 3.2: kind_share sums the listed kinds over the subtree and asks for a
// file count and a share of the bytes; for a file, the subtree is the file.
func TestConditionKindShare(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "media"
target = "folder"
kind_share = { kinds = ["image", "video"], min_files = 3, min_bytes_share = 0.8 }
priority = 10
category = "personal_media"
explain = "Media."

[[rules]]
id = "image_file"
target = "file"
kind_share = { kinds = ["image"] }
priority = 10
category = "personal_media"
explain = "A picture."

[[rules]]
id = "disc"
target = "file"
name = ["*.bin"]
kind_share = { kinds = ["archive"] }
priority = 10
category = "installer_download"
explain = "A disc image."
`)
	byKind := func(img, imgBytes, vid, vidBytes int64) map[domain.FileKind]KindTotals {
		return map[domain.FileKind]KindTotals{
			domain.FileKindImage: {img, imgBytes}, domain.FileKindVideo: {vid, vidBytes},
		}
	}
	cases := []struct {
		name         string
		files, bytes int64
		kinds        map[domain.FileKind]KindTotals
		want         domain.Category
	}{
		{"photos and a video", 40, 1000, byKind(38, 400, 1, 560), domain.CategoryPersonalMedia},
		{"exactly the share", 5, 1000, byKind(5, 800, 0, 0), domain.CategoryPersonalMedia},
		{"below the share", 6, 1000, byKind(5, 799, 0, 0), domain.CategoryUnknown},
		{"too few files", 2, 1000, byKind(2, 1000, 0, 0), domain.CategoryUnknown},
		{"empty folder", 0, 0, nil, domain.CategoryUnknown},
		{"zero-byte files", 3, 0, byKind(3, 0, 0, 0), domain.CategoryUnknown}, // no bytes: only a zero share holds
	}
	for _, c := range cases {
		got := p.ClassifyFolder(FolderFacts{Name: []byte("x"), Files: c.files, Bytes: c.bytes, ByKind: c.kinds})
		if got.Category != c.want {
			t.Errorf("%s: %s, want %s", c.name, got.Category, c.want)
		}
	}

	checkResult(t, "a JPEG", p.ClassifyFile(FileFacts{Name: []byte("a.jpg"), Kind: domain.FileKindImage, Size: 10}),
		domain.CategoryPersonalMedia, domain.TriageKeep, ReasonMatched, "image_file")
	checkResult(t, "an empty JPEG", p.ClassifyFile(FileFacts{Name: []byte("a.jpg"), Kind: domain.FileKindImage}),
		domain.CategoryPersonalMedia, domain.TriageKeep, ReasonMatched, "image_file")
	checkResult(t, "a text file", p.ClassifyFile(FileFacts{Name: []byte("a.txt"), Kind: domain.FileKindOther, Size: 10}),
		domain.CategoryUnknown, domain.TriageReview, ReasonNoRuleMatched)

	// A .bin is an archive only beside its .cue: ClassifyFile pairs a file
	// given as other by its sibling stems.
	stems := map[string]bool{"jogo": true}
	checkResult(t, "jogo.bin beside jogo.cue", p.ClassifyFile(FileFacts{Name: []byte("JOGO.BIN"), Kind: domain.FileKindOther, Size: 9, SiblingStems: stems}),
		domain.CategoryInstallerDownload, domain.TriageDiscard, ReasonMatched, "disc")
	checkResult(t, "jogo.bin as FileKindNear gives it", p.ClassifyFile(FileFacts{Name: []byte("jogo.bin"), Kind: domain.FileKindArchive, Size: 9}),
		domain.CategoryInstallerDownload, domain.TriageDiscard, ReasonMatched, "disc")
	checkResult(t, "a lone dados.bin", p.ClassifyFile(FileFacts{Name: []byte("dados.bin"), Kind: domain.FileKindOther, Size: 9, SiblingStems: stems}),
		domain.CategoryUnknown, domain.TriageReview, ReasonNoRuleMatched)
}

// Task 3.2: the highest priority decides; two categories at the top priority
// conflict, while the same category from two rules does not; a lower rule
// never conflicts.
func TestPriority(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "low_media"
target = "folder"
name = ["Fotos*"]
priority = 10
category = "personal_media"
explain = "Low."

[[rules]]
id = "high_docs"
target = "folder"
name = ["*docs*"]
priority = 50
category = "documents"
explain = "Docs."

[[rules]]
id = "high_media"
target = "folder"
name = ["*fotos*"]
priority = 50
category = "personal_media"
explain = "Photos."

[[rules]]
id = "high_media_again"
target = "folder"
name = ["*album*"]
priority = 50
category = "personal_media"
explain = "Album."
`)
	checkResult(t, "Fotos", p.ClassifyFolder(folder("Fotos")),
		domain.CategoryPersonalMedia, domain.TriageKeep, ReasonMatched, "high_media")
	checkResult(t, "Fotos-docs", p.ClassifyFolder(folder("Fotos-docs")),
		domain.CategoryUnknown, domain.TriageReview, ReasonConflictingRules, "high_docs", "high_media")
	checkResult(t, "fotos album", p.ClassifyFolder(folder("fotos album")),
		domain.CategoryPersonalMedia, domain.TriageKeep, ReasonMatched, "high_media", "high_media_again")
	checkResult(t, "my docs", p.ClassifyFolder(folder("my docs")),
		domain.CategoryDocuments, domain.TriageKeep, ReasonMatched, "high_docs")
	if r := p.ClassifyFolder(folder("Fotos-docs")); r.Group || r.Veto {
		t.Errorf("a conflict is a group or vetoed: %+v", r)
	}
}

// Task 3.2: traits are the union over every matching rule, whatever its
// priority, in the domain order, and a trait-only rule never decides the
// category.
func TestTraitsUnion(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "project"
target = "folder"
child_signals = { any = ["vcs"] }
priority = 60
category = "source_project"
explain = "A project."

[[rules]]
id = "vcs_below"
target = "folder"
subtree_signals = [{ signal = "vcs" }]
priority = 0
traits = ["contains_vcs"]
explain = "Version control below."

[[rules]]
id = "output_below"
target = "folder"
subtree_signals = [{ signal = "compiled" }]
priority = 5
category = "generated_artifacts"
traits = ["possible_generated_content", "contains_vcs"]
explain = "Output below."

[[rules]]
id = "docs_below"
target = "folder"
subtree_signals = [{ signal = "doc" }]
priority = 100
traits = ["contains_user_material"]
explain = "Documents below."
`)
	got := p.ClassifyFolder(FolderFacts{
		Name:           []byte("site"),
		ChildSignals:   map[SignalID]int{"vcs": 1},
		SubtreeSignals: map[SignalID]int{"vcs": 1, "compiled": 4, "doc": 1},
	})
	checkResult(t, "site", got, domain.CategorySourceProject, domain.TriageKeep, ReasonMatched,
		"docs_below", "project", "output_below", "vcs_below")
	want := []domain.Trait{domain.TraitContainsUserMaterial, domain.TraitContainsVCS, domain.TraitPossibleGeneratedContent}
	if !slices.Equal(got.Traits, want) {
		t.Errorf("traits = %v, want %v", got.Traits, want)
	}

	got = p.ClassifyFolder(FolderFacts{Name: []byte("letters"), SubtreeSignals: map[SignalID]int{"doc": 2}})
	checkResult(t, "letters", got, domain.CategoryUnknown, domain.TriageReview, ReasonNoRuleMatched, "docs_below")
	if !slices.Equal(got.Traits, []domain.Trait{domain.TraitContainsUserMaterial}) {
		t.Errorf("traits = %v, want contains_user_material", got.Traits)
	}
}

// Task 3.2: recovered *.CHK fragments and found.000 are system junk held for
// review, never suggested for discard, through the rule's triage.
func TestReviewException(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "thumbs"
target = "file"
name = ["Thumbs.db"]
priority = 100
category = "system_junk"
explain = "Thumbnails."

[[rules]]
id = "recovered_fragment"
target = "file"
name = ["*.CHK"]
priority = 100
category = "system_junk"
triage = "review"
explain = "A recovered fragment."

[[rules]]
id = "found_folder"
target = "folder"
name = ["found.???"]
priority = 100
category = "system_junk"
triage = "review"
explain = "Recovered fragments."
`)
	checkResult(t, "Thumbs.db", p.ClassifyFile(FileFacts{Name: []byte("Thumbs.db"), Kind: domain.FileKindSystem}),
		domain.CategorySystemJunk, domain.TriageDiscard, ReasonMatched, "thumbs")
	checkResult(t, "FILE0000.CHK", p.ClassifyFile(FileFacts{Name: []byte("FILE0000.CHK"), Kind: domain.FileKindSystem}),
		domain.CategorySystemJunk, domain.TriageReview, ReasonMatched, "recovered_fragment")
	got := p.ClassifyFolder(FolderFacts{Name: []byte("found.000"), Indicators: 3})
	checkResult(t, "found.000", got, domain.CategorySystemJunk, domain.TriageReview, ReasonMatched, "found_folder")
	if got.Veto || got.Group {
		t.Errorf("found.000 veto %v group %v, want neither: review is not a vetoed discard", got.Veto, got.Group)
	}
}

// Task 3.2: an indicator below a folder the rules would discard turns the
// suggestion into review with the veto; without one, discard stands; a kept
// or reviewed folder is never vetoed. Groups follow the category.
func TestVetoAndGroups(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "app"
target = "folder"
subtree_signals = [{ signal = "exe" }]
priority = 40
category = "application_installation"
explain = "A program."

[[rules]]
id = "temp"
target = "folder"
name = ["Temp"]
priority = 80
category = "temporary_data"
explain = "Temporary files."

[[rules]]
id = "media"
target = "folder"
name = ["Fotos"]
priority = 50
category = "personal_media"
explain = "Photos."

[[rules]]
id = "windows"
target = "folder"
name = ["WINDOWS"]
priority = 90
category = "os_installation"
explain = "Windows."
`)
	cases := []struct {
		name         string
		indicators   int
		triage       domain.Triage
		group, vetoe bool
	}{
		{"Microsoft Office", 1, domain.TriageReview, true, true},
		{"Winamp", 0, domain.TriageDiscard, true, false},
		{"Temp", 2, domain.TriageReview, false, true},
		{"Temp", 0, domain.TriageDiscard, false, false},
		{"Fotos", 9, domain.TriageKeep, false, false},
		{"WINDOWS", 1, domain.TriageReview, true, false},
	}
	for _, c := range cases {
		got := p.ClassifyFolder(FolderFacts{Name: []byte(c.name), SubtreeSignals: map[SignalID]int{"exe": 1}, Indicators: c.indicators})
		if got.Triage != c.triage || got.Group != c.group || got.Veto != c.vetoe {
			t.Errorf("%s with %d indicators: triage %s group %v veto %v; want %s %v %v",
				c.name, c.indicators, got.Triage, got.Group, got.Veto, c.triage, c.group, c.vetoe)
		}
	}
	if got := p.ClassifyFile(FileFacts{Name: []byte("x.exe")}); got.Group || got.Veto {
		t.Errorf("a file is a group or vetoed: %+v", got)
	}
}

// Triage follows the category's row of the D9 table, and only the listed
// categories make groups.
func TestTriageAndGroupTables(t *testing.T) {
	keep := []domain.Category{domain.CategoryPersonalMedia, domain.CategoryDocuments, domain.CategorySourceProject, domain.CategoryApplicationUserData}
	discard := []domain.Category{domain.CategorySystemJunk, domain.CategoryCache, domain.CategoryTemporaryData,
		domain.CategoryGeneratedArtifacts, domain.CategoryInstallerDownload, domain.CategoryApplicationInstallation}
	groups := []domain.Category{domain.CategoryApplicationInstallation, domain.CategoryOSInstallation, domain.CategorySourceProject,
		domain.CategoryApplicationUserData, domain.CategoryCache, domain.CategoryGeneratedArtifacts, domain.CategoryBackup}
	for _, c := range domain.Categories {
		want := domain.TriageReview
		switch {
		case slices.Contains(keep, c):
			want = domain.TriageKeep
		case slices.Contains(discard, c):
			want = domain.TriageDiscard
		}
		if got := triageOf(c); got != want {
			t.Errorf("triageOf(%s) = %s, want %s", c, got, want)
		}
		if got := groupCategories[c]; got != slices.Contains(groups, c) {
			t.Errorf("group(%s) = %v", c, got)
		}
	}
}

// Explain gives each rule's sentence in the order asked, and "" for a rule
// these rules do not define.
func TestExplain(t *testing.T) {
	p := testPolicy(t, `
[[rules]]
id = "thumbs"
target = "file"
name = ["Thumbs.db"]
priority = 10
category = "system_junk"
explain = "Windows keeps small previews of a folder's pictures in this file."

[[rules]]
id = "modules"
target = "folder"
name = ["node_modules"]
priority = 10
category = "generated_artifacts"
explain = "Packages a build tool downloaded."
`)
	got := p.Explain([]string{"modules", "gone_rule", "thumbs"})
	want := []string{"Packages a build tool downloaded.", "", "Windows keeps small previews of a folder's pictures in this file."}
	if !slices.Equal(got, want) {
		t.Errorf("Explain = %q, want %q", got, want)
	}
	if p.Version() != "rules-test+markers-test" {
		t.Errorf("Version = %q", p.Version())
	}
}

// Task 3.2: the rules loader rejects unknown keys and every malformed rule,
// naming the file and the key.
func TestRulesRejectMalformed(t *testing.T) {
	const ok = "target = \"folder\"\nname = [\"x\"]\npriority = 1\ncategory = \"cache\"\nexplain = \"E.\"\n"
	cases := map[string]struct{ rules, want string }{
		"unknown key":          {"[[rules]]\nid = \"a\"\n" + ok + "sibling_signals = [\"vcs\"]\n", `unknown key "rules.sibling_signals"`},
		"unknown nested key":   {"[[rules]]\nid = \"a\"\n" + ok + "kind_share = { kind = \"image\", kinds = [\"image\"] }\n", `unknown key "rules.kind_share.kind"`},
		"unknown top key":      {"rule = 1\n", `unknown key "rule"`},
		"bad id":               {"[[rules]]\nid = \"A b\"\n" + ok, "rules[0].id"},
		"duplicate id":         {"[[rules]]\nid = \"a\"\n" + ok + "[[rules]]\nid = \"a\"\n" + ok, "used by another rule"},
		"bad target":           {"[[rules]]\nid = \"a\"\ntarget = \"dir\"\nname = [\"x\"]\npriority = 1\ncategory = \"cache\"\nexplain = \"E.\"\n", "must be \"file\" or \"folder\""},
		"file child signals":   {"[[rules]]\nid = \"a\"\ntarget = \"file\"\nchild_signals = { any = [\"vcs\"] }\npriority = 1\ncategory = \"cache\"\nexplain = \"E.\"\n", "a file rule cannot"},
		"file subtree signals": {"[[rules]]\nid = \"a\"\ntarget = \"file\"\nsubtree_signals = [{ signal = \"vcs\" }]\npriority = 1\ncategory = \"cache\"\nexplain = \"E.\"\n", "a file rule cannot"},
		"no condition":         {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\npriority = 1\ncategory = \"cache\"\nexplain = \"E.\"\n", "must have a condition"},
		"empty child signals":  {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nchild_signals = {}\npriority = 1\ncategory = \"cache\"\nexplain = \"E.\"\n", "must list a signal"},
		"unknown child signal": {"[[rules]]\nid = \"a\"\n" + ok + "child_signals = { none = [\"nope\"] }\n", `child_signals.none[0] "nope" is not a signal`},
		"unknown subtree sig":  {"[[rules]]\nid = \"a\"\n" + ok + "subtree_signals = [{ signal = \"nope\" }]\n", `subtree_signals[0].signal "nope"`},
		"zero min_count":       {"[[rules]]\nid = \"a\"\n" + ok + "subtree_signals = [{ signal = \"vcs\", min_count = 0 }]\n", "at least 1"},
		"bad pattern":          {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"a/b\"]\npriority = 1\ncategory = \"cache\"\nexplain = \"E.\"\n", "rules[0].name[0]"},
		"no kinds":             {"[[rules]]\nid = \"a\"\n" + ok + "kind_share = { min_files = 2 }\n", "at least one file kind"},
		"bad kind":             {"[[rules]]\nid = \"a\"\n" + ok + "kind_share = { kinds = [\"photo\"] }\n", `"photo" is not a file kind`},
		"zero min_files":       {"[[rules]]\nid = \"a\"\n" + ok + "kind_share = { kinds = [\"image\"], min_files = 0 }\n", "min_files 0"},
		"share above one":      {"[[rules]]\nid = \"a\"\n" + ok + "kind_share = { kinds = [\"image\"], min_bytes_share = 1.5 }\n", "between 0 and 1"},
		"no priority":          {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"x\"]\ncategory = \"cache\"\nexplain = \"E.\"\n", "priority is missing"},
		"negative priority":    {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"x\"]\npriority = -1\ncategory = \"cache\"\nexplain = \"E.\"\n", "must not be negative"},
		"bad category":         {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"x\"]\npriority = 1\ncategory = \"junk\"\nexplain = \"E.\"\n", `category "junk"`},
		"unknown category":     {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"x\"]\npriority = 1\ncategory = \"unknown\"\nexplain = \"E.\"\n", "other than unknown"},
		"discard triage":       {"[[rules]]\nid = \"a\"\n" + ok + "triage = \"discard\"\n", "can only be review"},
		"triage alone":         {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"x\"]\npriority = 1\ntriage = \"review\"\ntraits = [\"contains_vcs\"]\nexplain = \"E.\"\n", "needs a category"},
		"bad trait":            {"[[rules]]\nid = \"a\"\n" + ok + "traits = [\"precious\"]\n", `"precious" is not a trait`},
		"duplicate trait":      {"[[rules]]\nid = \"a\"\n" + ok + "traits = [\"contains_vcs\", \"contains_vcs\"]\n", "listed twice"},
		"no result":            {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"x\"]\npriority = 1\nexplain = \"E.\"\n", "category, traits, or both"},
		"no explain":           {"[[rules]]\nid = \"a\"\ntarget = \"folder\"\nname = [\"x\"]\npriority = 1\ncategory = \"cache\"\n", "explain is missing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load([]byte(testMarkers), []byte("version = \"rules-test\"\n"+c.rules))
			if err == nil || !strings.Contains(err.Error(), "rules: ") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
	if _, err := Load([]byte(testMarkers), []byte("[[rules]]\nid = \"a\"\n"+ok)); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("a rules file without a version: err = %v", err)
	}
}
