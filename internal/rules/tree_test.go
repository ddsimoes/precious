package rules

import (
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"precious/internal/domain"
)

// node is one entry of a test tree: a raw '/'-joined path below the root, its
// kind, and a file's size.
type node struct {
	path string
	kind domain.EntryKind
	size int64
}

// classified is what classifyTree gives one entry.
type classified struct {
	Result
	FileKind   domain.FileKind // files only
	Indicators int             // folders only: FolderFacts.Indicators
}

// subtree accumulates the facts of a folder's whole subtree.
type subtree struct {
	files, bytes int64
	byKind       map[domain.FileKind]KindTotals
	signals      map[SignalID]int
	indicators   int
}

// classifyTree classifies every file and folder of a tree, root ("")
// included, the way the scanner does (design D7, D9): it lists each folder,
// derives each child's signals and each file's kind with the folder's
// sibling stems, classifies files as they are listed, and classifies each
// folder once its subtree is complete, from its children's signals and its
// subtree's signals, kinds, totals, and indicator count.
func classifyTree(p *Policy, nodes []node) map[string]classified {
	children := map[string][]node{}
	for _, n := range nodes {
		dir := path.Dir(n.path)
		if dir == "." {
			dir = ""
		}
		children[dir] = append(children[dir], n)
	}
	out := make(map[string]classified, len(nodes)+1)
	var walk func(dir string) subtree
	walk = func(dir string) subtree {
		agg := subtree{byKind: map[domain.FileKind]KindTotals{}, signals: map[SignalID]int{}}
		childSignals := map[SignalID]int{}
		stems := map[string]bool{}
		for _, c := range children[dir] {
			if c.kind == domain.EntryFile {
				if s, ok := p.PairStem([]byte(path.Base(c.path))); ok {
					stems[s] = true
				}
			}
		}
		var sigs []SignalID
		for _, c := range children[dir] {
			name := []byte(path.Base(c.path))
			sigs = p.AppendNameSignals(sigs[:0], name, c.kind)
			indicator := false
			for _, s := range sigs {
				childSignals[s]++
				agg.signals[s]++
				indicator = indicator || p.IsIndicator(s)
			}
			if indicator {
				agg.indicators++
			}
			switch c.kind {
			case domain.EntryFile:
				kind := p.FileKindNear(name, stems)
				agg.files++
				agg.bytes += c.size
				kt := agg.byKind[kind]
				agg.byKind[kind] = KindTotals{Files: kt.Files + 1, Bytes: kt.Bytes + c.size}
				res := p.ClassifyFile(FileFacts{Name: name, Kind: kind, Size: c.size, SiblingStems: stems})
				out[c.path] = classified{Result: res, FileKind: kind}
			case domain.EntryDirectory:
				sub := walk(c.path)
				agg.files += sub.files
				agg.bytes += sub.bytes
				agg.indicators += sub.indicators
				for k, kt := range sub.byKind {
					t := agg.byKind[k]
					agg.byKind[k] = KindTotals{Files: t.Files + kt.Files, Bytes: t.Bytes + kt.Bytes}
				}
				for s, n := range sub.signals {
					agg.signals[s] += n
				}
			}
		}
		res := p.ClassifyFolder(FolderFacts{
			Name:           []byte(path.Base("/" + dir)),
			ChildSignals:   childSignals,
			SubtreeSignals: maps.Clone(agg.signals),
			Files:          agg.files,
			Bytes:          agg.bytes,
			ByKind:         maps.Clone(agg.byKind),
			Indicators:     agg.indicators,
		})
		out[dir] = classified{Result: res, Indicators: agg.indicators}
		return agg
	}
	walk("")
	return out
}

// files builds a tree from "path" or "path:size" specs of files (1000 bytes
// unless given); their folders are implied.
func files(specs ...string) []node {
	seen := map[string]bool{}
	var out []node
	for _, s := range specs {
		p, size := s, int64(1000)
		if i := strings.LastIndexByte(s, ':'); i >= 0 {
			n, err := strconv.ParseInt(s[i+1:], 10, 64)
			if err != nil {
				panic(err)
			}
			p, size = s[:i], n
		}
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			if !seen[dir] {
				seen[dir] = true
				out = append(out, node{path: dir, kind: domain.EntryDirectory})
			}
		}
		out = append(out, node{path: p, kind: domain.EntryFile, size: size})
	}
	slices.SortStableFunc(out, func(a, b node) int { return strings.Compare(a.path, b.path) })
	return out
}
