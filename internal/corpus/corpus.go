// Package corpus is the regression corpus (precious-spec §15, design D18): a
// synthetic "messy disk" defined once, deterministically, and built through a
// Builder into either a synthfs tree (BuildSynth) or a real directory
// (WriteDir), together with its ground truth.
//
// Paths are relative to the tree's root, '/'-joined, and hold raw name bytes
// (some names are not valid UTF-8). The ground truth lists every entry a
// complete scan indexes: everything except the root itself and the contents of
// unreadable folders. Where the corpus asserts it, an entry also carries the
// category, triage, group, veto, and file kind the rules must give it (design
// D9, §6.6); the assertions are the table in expect.go.
package corpus

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"precious/internal/domain"
)

// Tree is an immutable corpus definition.
type Tree struct {
	items []item
	// rootMTime is the root folder's modification time.
	rootMTime time.Time
	expects   []expect
}

// item is one entry of a tree, in creation order: every folder comes before
// its contents.
type item struct {
	path       string
	kind       domain.EntryKind
	data       []byte // file content
	target     string // symlink text
	mtime      time.Time
	unreadable bool
}

// Builder creates a tree's entries. Build calls it in three passes: first
// Mkdir, WriteFile, and Symlink, every folder before its contents; then
// DirTime for every folder, contents before their folder, ending with the
// root (path ""); then Unreadable. Implementations must not modify data.
type Builder interface {
	Mkdir(path string) error
	WriteFile(path string, data []byte, mtime time.Time) error
	Symlink(path, target string) error
	DirTime(path string, mtime time.Time) error
	// Unreadable makes the folder at path impossible to list.
	Unreadable(path string) error
}

// Build creates the tree through b.
func (t *Tree) Build(b Builder) error {
	for _, it := range t.items {
		var err error
		switch it.kind {
		case domain.EntryDirectory:
			err = b.Mkdir(it.path)
		case domain.EntryFile:
			err = b.WriteFile(it.path, it.data, it.mtime)
		case domain.EntrySymlink:
			err = b.Symlink(it.path, it.target)
		}
		if err != nil {
			return err
		}
	}
	for i := len(t.items) - 1; i >= 0; i-- {
		if it := t.items[i]; it.kind == domain.EntryDirectory {
			if err := b.DirTime(it.path, it.mtime); err != nil {
				return err
			}
		}
	}
	if err := b.DirTime("", t.rootMTime); err != nil {
		return err
	}
	for _, it := range t.items {
		if it.unreadable {
			if err := b.Unreadable(it.path); err != nil {
				return err
			}
		}
	}
	return nil
}

// Size is the sum of the tree's file sizes, including files inside
// unreadable folders.
func (t *Tree) Size() int64 {
	var n int64
	for _, it := range t.items {
		n += int64(len(it.data))
	}
	return n
}

// GroundTruth is what a complete scan of the tree must find.
type GroundTruth struct {
	Entries []Entry `json:"entries"`
}

// Entry is one ground-truth entry. Size is set for files only. Category,
// Triage, Group, and Veto are set together, for entries whose classification
// the corpus asserts; FileKind is set for files whose kind it asserts.
// Category, Triage, and FileKind use the plain-string vocabulary of design D9.
type Entry struct {
	PathB64 string `json:"path_b64"`
	// Path is the display form (domain.DisplayName of each component).
	Path       string           `json:"path"`
	Kind       domain.EntryKind `json:"kind"`
	Size       *int64           `json:"size,omitempty"`
	Unreadable bool             `json:"unreadable,omitempty"`
	Category   string           `json:"category,omitempty"`
	Triage     string           `json:"triage,omitempty"`
	Group      *bool            `json:"group,omitempty"`
	Veto       *bool            `json:"veto,omitempty"`
	FileKind   string           `json:"file_kind,omitempty"`
}

// RawPath returns the entry's raw '/'-joined path bytes.
func (e Entry) RawPath() ([]byte, error) {
	return base64.StdEncoding.DecodeString(e.PathB64)
}

// GroundTruth returns the tree's ground truth, in creation order.
func (t *Tree) GroundTruth() GroundTruth {
	asserted := make(map[string]expect, len(t.expects))
	for _, x := range t.expects {
		asserted[x.path] = x
	}
	var hidden []string // unreadable folders, as path prefixes
	entries := make([]Entry, 0, len(t.items))
items:
	for _, it := range t.items {
		for _, h := range hidden {
			if strings.HasPrefix(it.path, h) {
				continue items
			}
		}
		e := Entry{
			PathB64:    base64.StdEncoding.EncodeToString([]byte(it.path)),
			Path:       displayPath(it.path),
			Kind:       it.kind,
			Unreadable: it.unreadable,
		}
		if it.kind == domain.EntryFile {
			size := int64(len(it.data))
			e.Size = &size
		}
		if x, ok := asserted[it.path]; ok {
			if x.category != "" {
				group, veto := x.group, x.veto
				e.Category, e.Triage, e.Group, e.Veto = x.category, x.triage, &group, &veto
			}
			e.FileKind = x.fileKind
		}
		if it.unreadable {
			hidden = append(hidden, it.path+"/")
		}
		entries = append(entries, e)
	}
	return GroundTruth{Entries: entries}
}

// WriteFile writes the ground truth as indented JSON.
func (g GroundTruth) WriteFile(name string) error {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, append(b, '\n'), 0o644)
}

// displayPath renders a raw '/'-joined path component by component.
func displayPath(p string) string {
	parts := strings.Split(p, "/")
	for i, c := range parts {
		parts[i] = domain.DisplayName([]byte(c))
	}
	return strings.Join(parts, "/")
}

// def accumulates a tree definition.
type def struct {
	items []item
	index map[string]int
}

func newDef() *def { return &def{index: make(map[string]int)} }

func (d *def) add(it item) {
	if _, dup := d.index[it.path]; dup {
		panic("corpus: duplicate path " + displayPath(it.path))
	}
	if dir := path.Dir(it.path); dir != "." {
		d.mkdirs(dir)
	}
	d.index[it.path] = len(d.items)
	d.items = append(d.items, it)
}

// mkdirs adds the folder p and its missing ancestors. Their modification
// times are computed by finish.
func (d *def) mkdirs(p string) {
	if i, ok := d.index[p]; ok {
		if d.items[i].kind != domain.EntryDirectory {
			panic("corpus: not a folder: " + displayPath(p))
		}
		return
	}
	d.add(item{path: p, kind: domain.EntryDirectory})
}

// file adds a file and returns its content, for byte-identical copies.
func (d *def) file(p string, mtime time.Time, data []byte) []byte {
	d.add(item{path: p, kind: domain.EntryFile, data: data, mtime: mtime})
	return data
}

// emptyDir adds a folder that stays empty.
func (d *def) emptyDir(p string, mtime time.Time) {
	d.add(item{path: p, kind: domain.EntryDirectory, mtime: mtime})
}

// symlink adds a symbolic link. Its time only dates its folder: builders
// leave a link's own time as creating it sets it.
func (d *def) symlink(p, target string, mtime time.Time) {
	d.add(item{path: p, kind: domain.EntrySymlink, target: target, mtime: mtime})
}

// unreadable marks an existing folder as impossible to list.
func (d *def) unreadable(p string) {
	i, ok := d.index[p]
	if !ok || d.items[i].kind != domain.EntryDirectory {
		panic("corpus: no folder " + displayPath(p))
	}
	d.items[i].unreadable = true
}

// copyTree copies every entry below src to the same relative path below dst,
// with the same content and modification time, except the relative paths in
// skip.
func (d *def) copyTree(src, dst string, skip ...string) {
	skipped := make(map[string]bool, len(skip))
	for _, s := range skip {
		if _, ok := d.index[src+"/"+s]; !ok {
			panic("corpus: copyTree skips a missing path: " + displayPath(src+"/"+s))
		}
		skipped[s] = true
	}
	n := len(d.items)
	for _, it := range d.items[:n] {
		rel, ok := strings.CutPrefix(it.path, src+"/")
		if !ok || skipped[rel] {
			continue
		}
		it.path = dst + "/" + rel
		if it.kind == domain.EntryDirectory {
			d.mkdirs(it.path)
			continue
		}
		d.add(it)
	}
}

// data returns the content of the file at p.
func (d *def) data(p string) []byte {
	i, ok := d.index[p]
	if !ok || d.items[i].kind != domain.EntryFile {
		panic("corpus: no file " + displayPath(p))
	}
	return d.items[i].data
}

// finish gives every folder without its own modification time the newest
// modification time among its contents, checks the assertions against the
// entries, and returns the tree.
func (d *def) finish(expects []expect) *Tree {
	t := &Tree{items: d.items, expects: expects}
	explicit := make([]bool, len(d.items))
	for i, it := range d.items {
		explicit[i] = !it.mtime.IsZero()
	}
	for i := len(d.items) - 1; i >= 0; i-- {
		it := d.items[i]
		if it.kind == domain.EntryDirectory && it.mtime.IsZero() {
			panic("corpus: folder without contents or time: " + displayPath(it.path))
		}
		parent := &t.rootMTime
		if dir := path.Dir(it.path); dir != "." {
			p := d.index[dir]
			if explicit[p] {
				panic("corpus: folder with its own time has contents: " + displayPath(dir))
			}
			parent = &d.items[p].mtime
		}
		if it.mtime.After(*parent) {
			*parent = it.mtime
		}
	}
	for _, x := range expects {
		i, ok := d.index[x.path]
		if !ok {
			panic("corpus: assertion for a missing path: " + displayPath(x.path))
		}
		if x.fileKind != "" && d.items[i].kind != domain.EntryFile {
			panic(fmt.Sprintf("corpus: file kind asserted for %s, which is not a file", displayPath(x.path)))
		}
	}
	return t
}
