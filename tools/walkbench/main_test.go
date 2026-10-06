package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"precious/internal/corpus"
)

// walkbench runs on the generated corpus and prints both times and a ratio;
// every listed entry is indexed.
func TestWalkbenchOnGeneratedCorpus(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "corpus")
	gt, err := corpus.WriteDir(dir, corpus.Corpus())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(dir, "privado"), 0o755); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Error(err)
		}
	})
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"-root", dir, "-state", state}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`entries  (\d+) walked, (\d+) indexed\nwalk     \S+\nscan     \S+\nratio    (\d+\.\d\d)\n$`).
		FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("output:\n%s", out.String())
	}
	walked, _ := strconv.Atoi(m[1])
	indexed, _ := strconv.Atoi(m[2])
	if os.Geteuid() != 0 && (walked != len(gt.Entries) || indexed != walked) {
		t.Errorf("walked %d, indexed %d, ground truth %d", walked, indexed, len(gt.Entries))
	}
	if err := run([]string{"-root", dir, "-state", state}, io.Discard, io.Discard); err == nil {
		t.Error("a second run reused the state directory's database")
	}
}
