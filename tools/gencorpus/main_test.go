package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"precious/internal/corpus"
)

// generate runs gencorpus into a fresh directory and returns the tree's
// directory and the ground truth it wrote.
func generate(t *testing.T, args ...string) (string, corpus.GroundTruth) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "corpus")
	if err := run(append([]string{"-out", dir}, args...), io.Discard); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// t.TempDir's removal needs the unreadable folder listable again.
		if err := os.Chmod(filepath.Join(dir, "privado"), 0o755); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Error(err)
		}
	})
	b, err := os.ReadFile(filepath.Join(base, "ground_truth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g corpus.GroundTruth
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return dir, g
}

// countWalk counts the entries below dir with filepath.WalkDir, checking each
// against the ground truth. Folders the ground truth calls unreadable are
// counted and not entered, so that the count is the same as root.
func countWalk(t *testing.T, dir string, g corpus.GroundTruth) {
	t.Helper()
	want := make(map[string]corpus.Entry, len(g.Entries))
	for _, e := range g.Entries {
		raw, err := e.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		want[string(raw)] = e
	}
	count := 0
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || name == dir {
			return err
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		count++
		e, ok := want[filepath.ToSlash(rel)]
		if !ok {
			t.Errorf("%q is not in the ground truth", rel)
			return nil
		}
		if e.Size != nil {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() != *e.Size {
				t.Errorf("%s: %d bytes, ground truth says %d", e.Path, info.Size(), *e.Size)
			}
			b, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != e.SHA256 {
				t.Errorf("%s: sha256 %x, ground truth says %s", e.Path, sum, e.SHA256)
			}
		}
		if e.Unreadable {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != len(g.Entries) {
		t.Errorf("walk found %d entries, ground truth lists %d", count, len(g.Entries))
	}
}

func TestCorpusMatchesGroundTruth(t *testing.T) {
	dir, g := generate(t)
	want := corpus.Corpus().GroundTruth()
	if len(g.Entries) != len(want.Entries) {
		t.Fatalf("ground_truth.json lists %d entries, the corpus %d", len(g.Entries), len(want.Entries))
	}
	countWalk(t, dir, g)
	// The R2 sections (design D19) and the rescue rows (r2c design D6)
	// round-trip through the file.
	if len(g.Duplicates) == 0 || len(g.Members) == 0 || len(g.Relations) == 0 || len(g.Rescue) == 0 {
		t.Errorf("ground_truth.json lacks R2 sections: %d duplicates, %d archives, %d relations, %d rescue rows",
			len(g.Duplicates), len(g.Members), len(g.Relations), len(g.Rescue))
	}
	if !reflect.DeepEqual(g, want) {
		t.Error("ground_truth.json differs from the corpus's ground truth")
	}

	assertedVeto := false
	for _, e := range g.Entries {
		if e.Veto != nil && *e.Veto {
			assertedVeto = true
		}
	}
	if !assertedVeto {
		t.Error("ground_truth.json asserts no veto")
	}

	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can list a mode-000 folder")
		}
		if _, err := os.ReadDir(filepath.Join(dir, "privado")); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("listing privado: %v", err)
		}
	})
}

func TestFATFixture(t *testing.T) {
	dir, g := generate(t, "-fat")
	countWalk(t, dir, g)
	if len(g.Entries) != len(corpus.FATFixture().GroundTruth().Entries) {
		t.Fatal("-fat did not write the FAT fixture")
	}
}

func TestRefusesBadUse(t *testing.T) {
	if err := run(nil, io.Discard); err == nil {
		t.Error("no -out accepted")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-out", dir}, io.Discard); err == nil {
		t.Error("non-empty -out accepted")
	}
}

func TestTruthFlag(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fat")
	truth := filepath.Join(t.TempDir(), "gt.json")
	if err := run([]string{"-out", dir, "-fat", "-truth", truth}, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(truth)
	if err != nil {
		t.Fatal(err)
	}
	var g corpus.GroundTruth
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Entries) == 0 || g.Entries[0].PathB64 != base64.StdEncoding.EncodeToString([]byte(filepath.ToSlash(g.Entries[0].Path))) {
		t.Errorf("unexpected ground truth %+v", g.Entries[:min(1, len(g.Entries))])
	}
}
