package index

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/policies"
)

// Corpus folders the owner tests override.
const (
	officePath  = "Backup_PC_2004/C/Arquivos de programas/Microsoft Office"
	winampPath  = "Backup_PC_2004/C/Arquivos de programas/Winamp"
	programsDir = "Backup_PC_2004/C/Arquivos de programas"
	orcamento   = officePath + "/OFFICE11/Meu orcamento casamento.xls"
)

// override writes the owner's override of the entry at path of src, as
// set-category and set-group store it.
func (e *env) override(src domain.SourceID, path string, o domain.Override) {
	e.t.Helper()
	var category, mark any
	if o.Category != "" {
		category = string(o.Category)
	}
	if o.Group != nil {
		mark = *o.Group
	}
	res, err := e.st.Writer().Exec(`INSERT INTO entry_overrides (entry_id, category, group_mark, updated_at)
		SELECT id, ?, ?, 1 FROM entries WHERE source_id = ? AND path = ?
		ON CONFLICT (entry_id) DO UPDATE SET category = excluded.category, group_mark = excluded.group_mark`,
		category, mark, string(src), []byte(path))
	if err != nil {
		e.t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		e.t.Fatalf("no entry at %q to override", path)
	}
}

// byFamily decodes a dir_stats.by_family column.
func byFamily(t *testing.T, r entry) map[domain.Family]struct{ Files, Bytes int64 } {
	t.Helper()
	out := map[domain.Family]struct{ Files, Bytes int64 }{}
	if err := json.Unmarshal([]byte(r.ByFamily.String), &out); err != nil {
		t.Fatalf("%s by_family %q: %v", r.Path, r.ByFamily.String, err)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// r2b 2.2 (classification: owner overrides take precedence): an owner
// category gives the scan exactly what a rule giving that category gives,
// in every row, composition, and inside list of the tree. Source "owner" is
// scanned with the default rules and the overrides; source "rule" with
// rules that classify the same entries the same way.
func TestOwnerCategoryScansAsARuleResult(t *testing.T) {
	e := newEnv(t)
	for _, src := range []domain.SourceID{"owner", "rule"} {
		root, _ := corpus.BuildSynth(e.sfs, "/"+string(src), corpus.Corpus())
		e.addSource(src, "/"+string(src), root, posix)
		e.scan(src)
	}
	e.override("owner", "Projetos/site_antigo", domain.Override{Category: domain.CategoryDocuments})
	e.override("owner", officePath, domain.Override{Category: domain.CategoryCache})
	e.override("owner", orcamento, domain.Override{Category: domain.CategoryCache})
	e.scan("owner")

	ruled := append(bytes.Clone(policies.Rules), []byte(`
[[rules]]
id = "test_site_is_documents"
target = "folder"
name = ["site_antigo"]
priority = 1000
category = "documents"
explain = "A test rule."

[[rules]]
id = "test_office_is_cache"
target = "folder"
name = ["Microsoft Office"]
priority = 1000
category = "cache"
explain = "A test rule."

[[rules]]
id = "test_budget_is_cache"
target = "file"
name = ["Meu orcamento casamento.xls"]
priority = 1000
category = "cache"
explain = "A test rule."
`)...)
	pol, err := rules.Load(policies.Markers, ruled)
	if err != nil {
		t.Fatal(err)
	}
	e.pol = pol
	e.scan("rule")
	e.pol = nil

	owner, rule := e.entries("owner"), e.entries("rule")
	if len(owner) != len(rule) {
		t.Fatalf("%d rows against %d", len(owner), len(rule))
	}
	ownerIDs, ruleIDs := pathsByID(owner), pathsByID(rule)
	for path, o := range owner {
		r := get(t, rule, path)
		if o.Category != r.Category || o.Family != r.Family || o.Triage != r.Triage || o.Group != r.Group ||
			o.Veto != r.Veto || o.ByFamily != r.ByFamily || o.TotalBytes != r.TotalBytes {
			t.Errorf("%q: owner %v/%v/%v group %v veto %v %s\n rule %v/%v/%v group %v veto %v %s", path,
				o.Category.String, o.Family.String, o.Triage.String, o.Group, o.Veto, o.ByFamily.String,
				r.Category.String, r.Family.String, r.Triage.String, r.Group, r.Veto, r.ByFamily.String)
		}
		if a, b := insideByPath(t, ownerIDs, o.Inside), insideByPath(t, ruleIDs, r.Inside); a != b {
			t.Errorf("%q: inside\n owner %s\n rule  %s", path, a, b)
		}
	}

	// The scenarios' values, and the rule IDs and traits as the rules
	// observed them.
	site := get(t, owner, "Projetos/site_antigo")
	if site.Category.String != "documents" || site.Family.String != "personal" || site.Triage.String != "keep" ||
		site.Group || site.RuleIDs.String == "" {
		t.Errorf("site_antigo %+v", site)
	}
	office := get(t, owner, officePath)
	if office.Category.String != "cache" || office.Triage.String != "review" || !office.Veto || !office.Group {
		t.Errorf("Microsoft Office %s/%s veto %v group %v; want cache, review, vetoed, a group",
			office.Category.String, office.Triage.String, office.Veto, office.Group)
	}
	if want := byFamily(t, get(t, owner, "Projetos/site_antigo")); byFamily(t, get(t, owner, "Projetos"))[domain.FamilyPersonal].Bytes < want[domain.FamilyPersonal].Bytes {
		t.Error("Projetos does not count site_antigo's personal bytes")
	}

	// A second scan with the same overrides writes nothing.
	writes := e.writes()
	e.scan("owner")
	if n := writes(); n != 0 {
		t.Errorf("an unchanged rescan with overrides wrote %d rows", n)
	}
}

// r2b 2.2 (classification: groups never hide their contents): a group mark
// counts the folder whole under its family in the composition of every
// folder above it, an unmark counts a rule group's files one by one, the
// folder's own figures and listing are unchanged, and indextest's mirror
// agrees with the scan.
func TestOwnerGroupMarks(t *testing.T) {
	e, _, before := scanCorpus(t)
	e.override("corpus", "Fotos/2006/Praia", domain.Override{Group: ptr(true)})
	e.override("corpus", winampPath, domain.Override{Group: ptr(false)})
	e.override("corpus", "Projetos/site_antigo", domain.Override{Category: domain.CategoryDocuments, Group: ptr(true)})
	e.scan("corpus")
	after := e.entries("corpus")

	praia := get(t, after, "Fotos/2006/Praia")
	if !praia.Group || praia.Category.String != "personal_media" || praia.ByFamily != get(t, before, "Fotos/2006/Praia").ByFamily {
		t.Fatalf("Praia group %v category %s by_family %s", praia.Group, praia.Category.String, praia.ByFamily.String)
	}
	thumbs := get(t, after, "Fotos/2006/Praia/Thumbs.db")
	for _, p := range []string{"Fotos/2006", "Fotos"} {
		was, now := byFamily(t, get(t, before, p)), byFamily(t, get(t, after, p))
		if now[domain.FamilyDisposable].Bytes != was[domain.FamilyDisposable].Bytes-thumbs.Size ||
			now[domain.FamilyPersonal].Bytes != was[domain.FamilyPersonal].Bytes+thumbs.Size {
			t.Errorf("%s does not count Praia whole as personal: %v -> %v", p, was, now)
		}
	}
	var inside []struct {
		Path  string `json:"path"`
		Group bool   `json:"group"`
	}
	if err := json.Unmarshal([]byte(get(t, after, "Fotos/2006").Inside.String), &inside); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range inside {
		found = found || (it.Path == "Fotos/2006/Praia" && it.Group)
	}
	if !found {
		t.Errorf("Fotos/2006 does not list the group Praia: %s", get(t, after, "Fotos/2006").Inside.String)
	}

	winamp := get(t, after, winampPath)
	if winamp.Group || winamp.Category.String != "application_installation" {
		t.Errorf("Winamp group %v category %s; want an application installation that is no group", winamp.Group, winamp.Category.String)
	}
	was, now := byFamily(t, get(t, before, programsDir)), byFamily(t, get(t, after, programsDir))
	own := byFamily(t, winamp)
	for _, f := range domain.Families {
		want := was[f].Bytes + own[f].Bytes
		if f == domain.FamilyPrograms {
			want -= winamp.TotalBytes
		}
		if now[f].Bytes != want {
			t.Errorf("%s %s: %d bytes, want %d (Winamp's files one by one)", programsDir, f, now[f].Bytes, want)
		}
	}
	if site := get(t, after, "Projetos/site_antigo"); !site.Group || site.Category.String != "documents" {
		t.Errorf("site_antigo group %v category %s", site.Group, site.Category.String)
	}

	// Every entry is still indexed: the marks change no row count.
	if len(after) != len(before) {
		t.Errorf("%d rows after the marks, %d before", len(after), len(before))
	}
	compareWithSeed(t, e, "corpus")
}

// r2b 2.1: on the scanned corpus, what the rules would set, rebuilt from
// each row's stored rule IDs, is what the scan stored for every entry the
// owner did not override.
func TestRecallMatchesTheScan(t *testing.T) {
	e, _, rows := scanCorpus(t)
	pol := rules.Default()
	checked := 0
	for path, r := range rows {
		if r.Kind != "file" && r.Kind != "directory" {
			continue
		}
		var ids []string
		if r.RuleIDs.Valid {
			if err := json.Unmarshal([]byte(r.RuleIDs.String), &ids); err != nil {
				t.Fatal(err)
			}
		}
		res := pol.Recall(ids, r.Kind == "directory", r.Indicators.Valid && r.Indicators.String != "[]")
		if string(res.Category) != r.Category.String || string(res.Family) != r.Family.String ||
			string(res.Triage) != r.Triage.String || res.Group != r.Group || res.Veto != r.Veto {
			t.Errorf("%q: Recall %s/%s/%s group %v veto %v; scanned %s/%s/%s group %v veto %v", path,
				res.Category, res.Family, res.Triage, res.Group, res.Veto,
				r.Category.String, r.Family.String, r.Triage.String, r.Group, r.Veto)
		}
		checked++
	}
	if checked < 100 {
		t.Fatalf("checked %d entries", checked)
	}
	_ = e
}

// r2b 2.3 (owner intent: survival): a rescan, a rules change, and a return
// from missing never change an override, and the entry keeps reading it.
func TestOverrideSurvivesRescansAndRulesChanges(t *testing.T) {
	e, _, _ := scanCorpus(t)
	emule := "Downloads/emule-0.47c"
	e.override("corpus", emule, domain.Override{Category: domain.CategoryApplicationInstallation, Group: ptr(false)})
	check := func(when string) {
		t.Helper()
		r := get(t, e.entries("corpus"), emule)
		if r.Category.String != "application_installation" || r.Group || r.State != "present" {
			t.Errorf("%s: emule %s group %v state %s", when, r.Category.String, r.Group, r.State)
		}
		var category string
		var mark bool
		if err := e.st.Reader().QueryRow(`SELECT category, group_mark FROM entry_overrides WHERE entry_id = ?`, r.ID).
			Scan(&category, &mark); err != nil || category != "application_installation" || mark {
			t.Errorf("%s: override row %s %v (%v)", when, category, mark, err)
		}
	}
	e.scan("corpus")
	check("rescan")

	bumped := bytes.Replace(policies.Rules, []byte(`version = "rules-v2"`), []byte(`version = "rules-v2-test"`), 1)
	pol, err := rules.Load(policies.Markers, bumped)
	if err != nil {
		t.Fatal(err)
	}
	e.pol = pol
	e.scan("corpus")
	check("rules change")

	if _, err := e.st.Writer().Exec(`UPDATE entries SET state = 'missing', missing_since = 1
		WHERE source_id = 'corpus' AND (path = ? OR path >= ? AND path < ?)`,
		[]byte(emule), []byte(emule+"/"), []byte(emule+"0")); err != nil {
		t.Fatal(err)
	}
	e.scan("corpus")
	check("return from missing")
}

// r2b 2.3 (owner intent: effect): an override set while a scan runs sets
// rescan_requested. The scan that ends with it set clears it and runs its
// job once more instead of finishing, with no after-scan hook; the next
// pass reads the override, and only it runs the hook.
func TestRescanRequestedRunsOneMorePass(t *testing.T) {
	e, _, _ := scanCorpus(t)
	var done int
	e.onDone = func(context.Context, domain.SourceID) { done++ }
	rt := &fakeRuntime{onYield: func(n int) {
		if n == 1 {
			e.override("corpus", "Fotos/2006/Praia", domain.Override{Group: ptr(true)})
			if _, err := e.st.Writer().Exec(`UPDATE sources SET rescan_requested = 1 WHERE id = 'corpus'`); err != nil {
				t.Error(err)
			}
		}
	}}
	err := e.scanWith(context.Background(), "corpus", rt)
	var d *jobs.Defer
	if !errors.As(err, &d) || d.Reason != DeferRescanRequested || !d.Until.Equal(testNow) {
		t.Fatalf("the scan that ended with a rescan requested returned %v", err)
	}
	if done != 0 {
		t.Error("the after-scan hook ran before the last pass")
	}
	if get(t, e.entries("corpus"), "Fotos/2006/Praia").Group {
		t.Fatal("the first pass read an override set after it started")
	}
	var requested bool
	if err := e.st.Reader().QueryRow(`SELECT rescan_requested FROM sources WHERE id = 'corpus'`).Scan(&requested); err != nil || requested {
		t.Fatalf("rescan_requested %v after the pass (%v)", requested, err)
	}

	e.scan("corpus")
	if !get(t, e.entries("corpus"), "Fotos/2006/Praia").Group || done != 1 {
		t.Errorf("the next pass: Praia group %v, hook ran %d times", get(t, e.entries("corpus"), "Fotos/2006/Praia").Group, done)
	}
}
