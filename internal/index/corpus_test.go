package index

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
)

// scanCorpus scans the synthfs regression corpus as source "corpus".
func scanCorpus(t *testing.T) (*env, corpus.GroundTruth, map[string]entry) {
	t.Helper()
	e := newEnv(t)
	root, gt := corpus.BuildSynth(e.sfs, "/corpus", corpus.Corpus())
	e.addSource("corpus", "/corpus", root, posix)
	e.scan("corpus")
	return e, gt, e.entries("corpus")
}

func rawPath(t *testing.T, g corpus.Entry) string {
	t.Helper()
	raw, err := g.RawPath()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// R1.1: every ground-truth path is indexed with its kind and size, nothing
// else is, and every folder's totals are the sums over the files below it.
func TestR1_1EveryEntryIndexedAndTotalsAddUp(t *testing.T) {
	e, gt, rows := scanCorpus(t)
	sums := map[string][2]int64{} // folder path -> files, bytes
	for _, g := range gt.Entries {
		p := rawPath(t, g)
		r := get(t, rows, p)
		kind, _ := kindColumn(g.Kind)
		if r.Kind != kind || r.State != map[bool]string{false: "present", true: "unreadable"}[g.Unreadable] {
			t.Errorf("%s: %s %s, ground truth %s unreadable %v", g.Path, r.Kind, r.State, g.Kind, g.Unreadable)
		}
		if g.Size == nil {
			continue
		}
		if r.Size != *g.Size || r.TotalBytes != *g.Size || r.TotalFiles != 1 {
			t.Errorf("%s: size %d total %d/%d, ground truth %d", g.Path, r.Size, r.TotalBytes, r.TotalFiles, *g.Size)
		}
		for dir := p; ; {
			i := strings.LastIndexByte(dir, '/')
			dir = dir[:max(i, 0)]
			s := sums[dir]
			sums[dir] = [2]int64{s[0] + 1, s[1] + *g.Size}
			if i < 0 {
				break
			}
		}
	}
	if len(rows) != len(gt.Entries)+1 {
		t.Errorf("indexed %d rows, ground truth %d entries plus the root", len(rows), len(gt.Entries))
	}
	folders := 0
	for p, r := range rows {
		if r.Kind != "directory" {
			continue
		}
		folders++
		if want := sums[p]; r.TotalFiles != want[0] || r.TotalBytes != want[1] {
			t.Errorf("folder %q totals %d files %d bytes, files below sum to %d/%d", p, r.TotalFiles, r.TotalBytes, want[0], want[1])
		}
		if r.Files.Int64 != r.TotalFiles {
			t.Errorf("folder %q dir_stats files %d, total_files %d", p, r.Files.Int64, r.TotalFiles)
		}
	}
	if folders < 50 || get(t, rows, "").TotalBytes != corpusReadableBytes(t, gt) {
		t.Errorf("%d folders; root %d bytes", folders, get(t, rows, "").TotalBytes)
	}
	compareWithSeed(t, e, "corpus")
}

func corpusReadableBytes(t *testing.T, gt corpus.GroundTruth) int64 {
	var n int64
	for _, g := range gt.Entries {
		if g.Size != nil {
			n += *g.Size
		}
	}
	return n
}

// R1.4: the scan classifies the corpus as its ground truth says: category,
// triage, group, and veto of every asserted entry, and the asserted file
// kinds.
func TestR1_4RulesClassifyTheCorpus(t *testing.T) {
	_, gt, rows := scanCorpus(t)
	asserted := 0
	for _, g := range gt.Entries {
		r := get(t, rows, rawPath(t, g))
		if g.Category != "" {
			asserted++
			if r.Category.String != g.Category || r.Triage.String != g.Triage || r.Group != *g.Group || r.Veto != *g.Veto {
				t.Errorf("%s = %s/%s group %v veto %v (rules %s); ground truth %s/%s group %v veto %v", g.Path,
					r.Category.String, r.Triage.String, r.Group, r.Veto, r.RuleIDs.String, g.Category, g.Triage,
					*g.Group, *g.Veto)
			}
			if r.Family.String != string(domain.FamilyOf(domain.Category(g.Category))) {
				t.Errorf("%s family %s", g.Path, r.Family.String)
			}
		}
		if g.FileKind != "" {
			asserted++
			if r.FileKind.String != g.FileKind {
				t.Errorf("%s file kind %s; ground truth %s", g.Path, r.FileKind.String, g.FileKind)
			}
		}
	}
	if asserted < 20 {
		t.Errorf("only %d assertions checked", asserted)
	}
}

// R1.5: the spreadsheet inside Microsoft Office vetoes discard for its group
// and is listed among the group's indicators.
func TestR1_5SpreadsheetVetoesDiscard(t *testing.T) {
	_, gt, rows := scanCorpus(t)
	var office string
	for _, g := range gt.Entries {
		if strings.HasSuffix(g.Path, "/Arquivos de programas/Microsoft Office") {
			office = rawPath(t, g)
		}
	}
	r := get(t, rows, office)
	if r.Triage.String != "review" || !r.Veto || !r.Group || r.Category.String != "application_installation" {
		t.Fatalf("Microsoft Office: %s/%s group %v veto %v", r.Category.String, r.Triage.String, r.Group, r.Veto)
	}
	sheet := get(t, rows, office+"/OFFICE11/Meu orcamento casamento.xls")
	var indicators []struct {
		EntryID string `json:"entry_id"`
		PathB64 []byte `json:"path_b64"`
		Path    string `json:"path"`
		Signal  string `json:"signal"`
	}
	if err := json.Unmarshal([]byte(r.Indicators.String), &indicators); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ind := range indicators {
		if ind.EntryID == domain.EntryID(sheet.ID).String() {
			found = true
			if ind.Path != sheet.Path || !bytes.Equal(ind.PathB64, []byte(sheet.Path)) || ind.Signal == "" {
				t.Errorf("indicator %+v", ind)
			}
		}
	}
	if !found || len(indicators) > maxIndicators {
		t.Errorf("Microsoft Office indicators %s do not list the spreadsheet %d", r.Indicators.String, sheet.ID)
	}
}
