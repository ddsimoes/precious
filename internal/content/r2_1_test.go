package content

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"precious/internal/corpus"
	"precious/internal/fsaccess/synthfs"
)

// buildCorpus builds the regression corpus as the synthfs root of source
// "corpus", scans it, and returns its root and ground truth.
func buildCorpus(t *testing.T, e *env) (*synthfs.Node, corpus.GroundTruth) {
	t.Helper()
	root, truth := corpus.BuildSynth(e.sfs, "/mnt/corpus", corpus.Corpus())
	e.addSource("corpus", "/mnt/corpus", root, posix)
	e.scan("corpus")
	return root, truth
}

// truthGroups are the ground truth's duplicate groups in the form of
// env.groups.
func truthGroups(t *testing.T, truth corpus.GroundTruth) [][]string {
	t.Helper()
	var out [][]string
	for _, d := range truth.Duplicates {
		var g []string
		for _, c := range d.Copies {
			raw, err := base64.StdEncoding.DecodeString(c.PathB64)
			if err != nil {
				t.Fatal(err)
			}
			g = append(g, "corpus:"+string(raw))
		}
		slices.Sort(g)
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	return out
}

// R2.1: hashing the corpus gives exactly the ground truth's duplicate
// groups, members included, and the published coverage grows while the job
// runs and ends at 100% with nothing unreadable.
func TestR2_1CorpusDuplicateGroups(t *testing.T) {
	e := newEnv(t)
	e.h.ReadChunkBytes = 64 << 10
	e.h.YieldBytes = 1 << 20
	e.service()
	_, truth := buildCorpus(t, e)
	var checked []int64
	rt := &fakeRuntime{onYield: func(int) {
		c, err := CoverageOf(context.Background(), e.st.Reader(), "")
		if err != nil {
			t.Error(err)
			return
		}
		checked = append(checked, c.CheckedBytes)
	}}
	if err := e.hashWith(context.Background(), "corpus", rt); err != nil {
		t.Fatal(err)
	}
	e.checkCoverage()
	got, want := e.groups(), truthGroups(t, truth)
	if len(want) == 0 {
		t.Fatal("the ground truth has no duplicate groups")
	}
	diffGroups(t, got, want)
	for i := 1; i < len(checked); i++ {
		if checked[i] < checked[i-1] {
			t.Fatalf("checked bytes went down while hashing: %v", checked)
		}
	}
	distinct := slices.Compact(slices.Clone(checked))
	if len(distinct) < 3 {
		t.Errorf("checked bytes seen while hashing: %v; want it to grow over several steps", distinct)
	}
	c, err := CoverageOf(context.Background(), e.st.Reader(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.CandidateBytes == 0 || c.CheckedBytes != c.CandidateBytes || c.CheckedFiles != c.CandidateFiles ||
		c.UnreadableFiles != 0 || c.UncheckedFiles != 0 {
		t.Errorf("coverage after hashing = %+v; want everything checked and nothing unreadable", c)
	}
	if n := e.count(`SELECT count(*) FROM archives WHERE state <> 'complete'`); n != 0 {
		t.Errorf("%d archives were not listed completely", n)
	}
	if n, want := e.count(`SELECT count(*) FROM archives`), len(truth.Members); n != want {
		t.Errorf("%d archives listed; ground truth %d", n, want)
	}
	if e.refreshes == 0 {
		t.Error("the hashing job requested no relate pass")
	}
}
