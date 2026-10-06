package api

import (
	"fmt"
	"strings"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
)

// corpusPrograms is the corpus's copied programs folder, whose programs are
// groups.
const corpusPrograms = "Backup_PC_2004/C/Arquivos de programas"

// truth is the ground truth of the scanned corpus by raw path, the root
// ("") included.
type truth struct {
	entries map[string]corpus.Entry
	// totals are each folder's file count and bytes: the sums over the
	// files the ground truth lists below it.
	totals map[string][2]int64
}

// scanCorpus builds the regression corpus in a synthfs, adds it as source
// "corpus", and scans it to completion.
func scanCorpus(t *testing.T) (*env, domain.EntryID, *synthfs.Node, truth) {
	t.Helper()
	e := newEnv(t)
	sfs := synthfs.New()
	root, gt := corpus.BuildSynth(sfs, "/corpus", corpus.Corpus())
	rootID := e.scanSynth(t, sfs, "corpus", "/corpus", root)
	tr := truth{
		entries: map[string]corpus.Entry{"": {Kind: domain.EntryDirectory}},
		totals:  map[string][2]int64{"": {}},
	}
	for _, g := range gt.Entries {
		raw, err := g.RawPath()
		if err != nil {
			t.Fatal(err)
		}
		p := string(raw)
		tr.entries[p] = g
		if g.Kind == domain.EntryDirectory {
			tr.totals[p] = tr.totals[p]
		}
		if g.Size == nil {
			continue
		}
		for dir := p; dir != ""; {
			dir = dir[:max(strings.LastIndexByte(dir, '/'), 0)]
			s := tr.totals[dir]
			tr.totals[dir] = [2]int64{s[0] + 1, s[1] + *g.Size}
		}
	}
	return e, rootID, root, tr
}

// TestR1_2EveryFolderShowsItsSize walks the scanned corpus through the Map
// endpoints, from the root down through the children of every folder: each
// folder row, the groups under Arquivos de programas included, carries the
// total bytes and file count of the files below it, each file its size, and
// each folder's treemap level accounts for exactly those bytes (R1.2).
func TestR1_2EveryFolderShowsItsSize(t *testing.T) {
	e, rootID, _, tr := scanCorpus(t)

	check := func(where string, r row) {
		t.Helper()
		g, ok := tr.entries[string(r.PathB64)]
		if !ok {
			t.Errorf("%s: %q is not in the ground truth", where, r.Path)
			return
		}
		switch {
		case r.Kind != string(g.Kind):
			t.Errorf("%s: %q is a %s, ground truth %s", where, r.Path, r.Kind, g.Kind)
		case g.Kind == domain.EntryDirectory:
			if want := tr.totals[string(r.PathB64)]; r.TotalFiles != want[0] || r.TotalBytes != want[1] {
				t.Errorf("%s: folder %q shows %d files and %d bytes, ground truth %d and %d",
					where, r.Path, r.TotalFiles, r.TotalBytes, want[0], want[1])
			}
		case g.Size != nil:
			if r.Size != *g.Size || r.TotalBytes != *g.Size || r.TotalFiles != 1 {
				t.Errorf("%s: file %q shows size %d, %d bytes, %d files; ground truth %d",
					where, r.Path, r.Size, r.TotalBytes, r.TotalFiles, *g.Size)
			}
		}
	}

	var detail struct {
		Entry row `json:"entry"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", rootID), 200, &detail)
	check("root", detail.Entry)

	seen := map[string]row{}
	folders := []row{detail.Entry}
	for len(folders) > 0 {
		f := folders[0]
		folders = folders[1:]
		children, _ := e.pages(t, fmt.Sprintf("/api/entries/%s/children?sort=name", f.ID), 1000)
		var childBytes int64
		for _, r := range children {
			if _, dup := seen[string(r.PathB64)]; dup {
				t.Errorf("%q listed twice", r.Path)
			}
			seen[string(r.PathB64)] = r
			check("children of "+f.Path, r)
			childBytes += r.TotalBytes
			if r.Kind == "directory" {
				folders = append(folders, r)
			}
		}
		if childBytes != f.TotalBytes && f.State != "unreadable" {
			t.Errorf("folder %q: its children total %d bytes, the folder %d", f.Path, childBytes, f.TotalBytes)
		}

		var tm struct {
			Entry row   `json:"entry"`
			Items []row `json:"items"`
			Other struct {
				Count int64 `json:"count"`
				Bytes int64 `json:"bytes"`
			} `json:"other"`
		}
		e.get(t, fmt.Sprintf("/api/entries/%s/treemap", f.ID), 200, &tm)
		check("treemap of "+f.Path, tm.Entry)
		areas := tm.Other.Bytes
		for _, r := range tm.Items {
			check("treemap of "+f.Path, r)
			areas += r.TotalBytes
		}
		if areas != tm.Entry.TotalBytes || int64(len(tm.Items))+tm.Other.Count != int64(len(children)) {
			t.Errorf("treemap of %q: %d areas of %d bytes for %d children of %d bytes",
				f.Path, int64(len(tm.Items))+tm.Other.Count, areas, len(children), tm.Entry.TotalBytes)
		}
	}

	if len(seen) != len(tr.entries)-1 {
		t.Errorf("the Map lists %d entries below the root, ground truth %d", len(seen), len(tr.entries)-1)
	}
	groups := 0
	for p, g := range tr.entries {
		if p == "" {
			continue
		}
		r, ok := seen[p]
		if !ok {
			t.Errorf("%q is not listed in the Map", g.Path)
			continue
		}
		if strings.HasPrefix(p, corpusPrograms+"/") && g.Group != nil && *g.Group {
			groups++
			if !r.Group {
				t.Errorf("%q is listed as no group", g.Path)
			}
		}
	}
	if groups < 5 {
		t.Errorf("%d groups under %s, want the corpus's programs", groups, corpusPrograms)
	}
}
