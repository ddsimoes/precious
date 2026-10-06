// Package indextest seeds a source's index for tests of the code that reads
// it (search, the read API, decisions). Seed writes the entries, dir_stats,
// and entry_names rows of a declarative tree exactly as a complete first scan
// writes them (design D6/D7):
//
//   - One entry per path. path is the raw names below the source root joined
//     by '/', empty for the root; name is the last raw name, empty for the
//     root; parent_id links each entry to its folder's row.
//   - kind is directory, file, symlink, or special; a special file keeps its
//     platform kind (fifo, socket, …) in special_kind.
//   - A file's total_bytes is its size and its total_files 1. A folder's are
//     the sums over the files of its subtree. Symlinks and special files count
//     0 bytes and 0 files.
//   - newest_ns and oldest_ns are a leaf's own mtime, and a folder's range over
//     the mtimes of the files in its subtree (NULL when it holds none).
//   - A file's ext is the ASCII-lowercased text after the last '.' of its
//     name, NULL when there is none or the only '.' is the first byte; its
//     file_kind defaults to other. Other kinds have neither.
//   - A folder's main_kind is the file kind with the most bytes in its
//     subtree, ties broken by more files, then by name; NULL when it holds
//     no files.
//   - category, family (FamilyOf(category)), and triage are written only when
//     the node names a category or triage; is_group is the node's Group.
//   - Every folder has a dir_stats row: dirs, files, symlinks, specials,
//     unreadable folders, and mount boundaries below it (itself excluded);
//     by_kind and by_year ({"<kind or UTC year>":{"files":n,"bytes":n}}) over
//     its subtree's files, with only non-empty keys; signals {} and
//     indicators [] (no rules run).
//   - by_family is the folder's composition with all four families (design
//     D21): a child group whose category's family is not containers counts
//     whole under that family, every other child folder adds its own
//     composition, and a file counts under domain.FileFamily(category,
//     file_kind).
//   - inside lists the folder's notable entries (design D21), at most 10,
//     by bytes descending, then raw path ascending. The folder's dominant
//     family holds the most of its by_family bytes, ties in the order
//     personal, programs, disposable, containers; a folder of 0 bytes has
//     none. Notable below a folder are its child groups; its other child
//     folders with less than half of their by_family bytes in its dominant
//     family; its files whose file family is not its dominant family; and,
//     below each other child folder, what is notable below that folder by
//     its own dominant family. Without a dominant family only groups are
//     notable. Each item is {"entry_id","path_b64","path","category",
//     "family","group","bytes","files"}: category null when the node has
//     none; family FamilyOf(category) for a folder (null without category)
//     and the file family for a file; bytes and files its totals.
//   - An unreadable folder has state unreadable, no children, and zero stats;
//     its ancestors are partial. A mount boundary has no children.
//   - Every entry but the root has an entry_names row: rowid = entries.id,
//     name = domain.DisplayName(name).
//   - Every entry is present, undecided (decision NULL, eff_decision
//     'undecided', eff_from NULL), first and last seen at Tree.Now, with the
//     source's new scan generation, and the source gets that generation and
//     last_scan_at = Tree.Now as a finished scan sets them.
//
// Platform facts a scan reads from Lstat (alloc, ctime_ns, dev, ino, nlink,
// mode) are NULL, unless the node carries the Lstat a scan would have read
// (Node.Lstat, for tests that open the files, such as the viewer's). Then
// size and mtime_ns come from it too, with the scanner's encodings:
//
//   - size = Size; mtime_ns = ModTime.UnixNano();
//   - ctime_ns = Ctime.UnixNano(), NULL when Ctime is the zero time;
//   - dev, ino, and nlink = the uint64 value bit-cast to int64;
//   - mode = int64(uint32(Mode)), Go's fs.FileMode bits (not st_mode);
//   - alloc = Blocks * 512.
package indextest

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/store"
)

// Node is one entry of a seeded tree. Only Path is required.
type Node struct {
	// Path is the raw names below the source root joined by '/'; "" is the
	// root. Folders above a listed path that are not listed themselves are
	// created as plain directories.
	Path string
	// Kind defaults to domain.EntryFile. Special kinds (fifo, socket, …) are
	// stored as kind 'special'.
	Kind domain.EntryKind
	Size int64
	// MTime defaults to Tree.Now.
	MTime time.Time
	// FileKind applies to files and defaults to domain.FileKindOther.
	FileKind domain.FileKind
	Category domain.Category
	Triage   domain.Triage
	// Group marks a folder that is one item for review (is_group).
	Group bool
	// LinkText is a symlink's target.
	LinkText string
	// Unreadable marks a folder whose listing failed; it has no children.
	Unreadable bool
	// MountBoundary marks a folder where another filesystem is mounted; it
	// has no children.
	MountBoundary bool
	// Lstat is the entry's lstat as a scan read it. When set, Size and MTime
	// must be zero: they come from Lstat, and alloc, ctime_ns, dev, ino,
	// nlink, and mode are written from it (see the package doc). Its Kind
	// must equal the node's.
	Lstat *fsaccess.EntryInfo
}

// Tree is the input of Seed.
type Tree struct {
	// Source is the source to seed. It must exist unless CreateSource is set,
	// and it must have no entries yet.
	Source domain.SourceID
	// CreateSource inserts the source row first: label = ID, volume kind
	// "path", online at MountPoint, or offline when MountPoint is "".
	CreateSource bool
	MountPoint   string
	// Now is the scan time, and the default mtime. Default 2024-01-01 UTC.
	Now   time.Time
	Nodes []Node
}

// DefaultNow is the scan time of a Tree without Now.
var DefaultNow = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// Capabilities is the capabilities JSON of a created source (a local
// POSIX filesystem).
const Capabilities = `{"known":true,"read_only":false,"case_sensitive":true,"normalization_sensitive":true,"stable_identity":true,"local_time":false,"hard_links":true,"time_resolution_ns":1}`

// Seeded is the result of Seed.
type Seeded struct {
	Source domain.SourceID
	Root   domain.EntryID
	ids    map[string]domain.EntryID
	now    int64
	t      testing.TB
}

// ID returns the entry ID of a seeded path and fails the test for any other.
func (s *Seeded) ID(path string) domain.EntryID {
	s.t.Helper()
	id, ok := s.ids[path]
	if !ok {
		s.t.Fatalf("indextest: %q was not seeded", path)
	}
	return id
}

// Tag gives each seeded path the own tag name, as the create-tag and
// set-tags commands write them: the tags row is created when no tag has
// that name (case-insensitive), and each path gets one entry_tags row added
// at Tree.Now. Descendants get no rows; their tags are inherited (design
// D10). It returns the tag's ID.
func (s *Seeded) Tag(st *store.Store, name string, paths ...string) int64 {
	s.t.Helper()
	ids := make([]domain.EntryID, len(paths))
	for i, p := range paths {
		ids[i] = s.ID(p)
	}
	var tag int64
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO tags (name, created_at) VALUES (?, ?) ON CONFLICT (name) DO NOTHING`,
			name, s.now); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT id FROM tags WHERE name = ?`, name).Scan(&tag); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO entry_tags (entry_id, tag_id, added_at) VALUES (?, ?, ?)`,
				int64(id), tag, s.now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.t.Fatalf("indextest: tag %q: %v", name, err)
	}
	return tag
}

// Counts is one breakdown cell of dir_stats.
type Counts struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

func (c *Counts) add(o Counts) {
	c.Files += o.Files
	c.Bytes += o.Bytes
}

type node struct {
	Node
	name     string
	children []*node
	id       domain.EntryID

	totalBytes, totalFiles                              int64
	newest, oldest                                      int64
	hasFiles                                            bool
	dirs, files, symlinks, specials, unreadable, mounts int64
	partial                                             bool
	byKind                                              map[domain.FileKind]Counts
	byYear                                              map[string]Counts
	byFamily                                            map[domain.Family]Counts
	// notable is every notable entry below the folder (design D21), before
	// the inside list keeps the 10 largest.
	notable []*node
}

// Seed writes tree into st in one transaction and fails the test on any
// invalid node or database error.
func Seed(t testing.TB, st *store.Store, tree Tree) *Seeded {
	t.Helper()
	if tree.Source == "" {
		t.Fatal("indextest: Tree.Source is required")
	}
	if tree.Now.IsZero() {
		tree.Now = DefaultNow
	}
	root, err := build(tree)
	if err != nil {
		t.Fatalf("indextest: %v", err)
	}
	aggregate(root)

	s := &Seeded{Source: tree.Source, ids: map[string]domain.EntryID{}, now: tree.Now.UnixMilli(), t: t}
	err = st.Write(context.Background(), func(tx *sql.Tx) error {
		if tree.CreateSource {
			if err := createSource(tx, tree); err != nil {
				return err
			}
		}
		var gen int64
		if err := tx.QueryRow(`SELECT scan_gen + 1 FROM sources WHERE id = ?`, string(tree.Source)).Scan(&gen); err != nil {
			return err
		}
		w := writer{tx: tx, source: tree.Source, gen: gen, seen: tree.Now.UnixMilli(), ids: s.ids}
		if err := w.write(root, "", nil); err != nil {
			return err
		}
		if err := w.writeStats(root); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE sources SET scan_gen = ?, last_scan_at = ? WHERE id = ?`,
			gen, tree.Now.UnixMilli(), string(tree.Source))
		return err
	})
	if err != nil {
		t.Fatalf("indextest: seed %s: %v", tree.Source, err)
	}
	s.Root = root.id
	return s
}

func createSource(tx *sql.Tx, tree Tree) error {
	state, mount := "offline", any(nil)
	if tree.MountPoint != "" {
		state, mount = "online", blob(tree.MountPoint)
	}
	_, err := tx.Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root,
		capabilities, state, mount_point, created_at)
		VALUES (?, ?, 'path', ?, 'ext4', 0, X'', ?, ?, ?, ?)`,
		string(tree.Source), string(tree.Source), "indextest:"+string(tree.Source),
		Capabilities, state, mount, tree.Now.UnixMilli())
	return err
}

// build turns the node list into a tree, creating unlisted ancestors.
func build(tree Tree) (*node, error) {
	byPath := map[string]*node{"": {Node: Node{Kind: domain.EntryDirectory, MTime: tree.Now}}}
	listed := map[string]bool{}
	for _, n := range tree.Nodes {
		if listed[n.Path] {
			return nil, fmt.Errorf("path %q listed twice", n.Path)
		}
		listed[n.Path] = true
		if n.Kind == "" {
			n.Kind = domain.EntryFile
		}
		if n.Lstat != nil {
			if n.Size != 0 || !n.MTime.IsZero() {
				return nil, fmt.Errorf("%q sets Size or MTime besides Lstat", n.Path)
			}
			if n.Lstat.Kind != n.Kind {
				return nil, fmt.Errorf("%q is a %s but its Lstat is a %s", n.Path, n.Kind, n.Lstat.Kind)
			}
			n.Size, n.MTime = n.Lstat.Size, n.Lstat.ModTime
		}
		if n.MTime.IsZero() {
			n.MTime = tree.Now
		}
		if n.Path == "" {
			if n.Kind != domain.EntryDirectory {
				return nil, fmt.Errorf("the root must be a directory, not %s", n.Kind)
			}
			byPath[""].Node = n
			continue
		}
		for _, name := range strings.Split(n.Path, "/") {
			if name == "" || name == "." || name == ".." {
				return nil, fmt.Errorf("path %q has an empty, '.', or '..' name", n.Path)
			}
		}
		if n.Group && n.Kind != domain.EntryDirectory {
			return nil, fmt.Errorf("%q is a group but a %s", n.Path, n.Kind)
		}
		if existing, ok := byPath[n.Path]; ok {
			// Created earlier as an implicit ancestor.
			if n.Kind != domain.EntryDirectory {
				return nil, fmt.Errorf("%q has children but is a %s", n.Path, n.Kind)
			}
			existing.Node = n
			continue
		}
		cur := &node{Node: n, name: n.Path[strings.LastIndexByte(n.Path, '/')+1:]}
		byPath[n.Path] = cur
		for {
			parentPath := ""
			if i := strings.LastIndexByte(cur.Path, '/'); i >= 0 {
				parentPath = cur.Path[:i]
			}
			parent, ok := byPath[parentPath]
			if ok {
				parent.children = append(parent.children, cur)
				break
			}
			parent = &node{
				Node: Node{Path: parentPath, Kind: domain.EntryDirectory, MTime: tree.Now},
				name: parentPath[strings.LastIndexByte(parentPath, '/')+1:],
			}
			byPath[parentPath] = parent
			parent.children = append(parent.children, cur)
			cur = parent
		}
	}
	for path, n := range byPath {
		if len(n.children) == 0 {
			continue
		}
		switch {
		case n.Kind != domain.EntryDirectory:
			return nil, fmt.Errorf("%q has children but is a %s", path, n.Kind)
		case n.Unreadable:
			return nil, fmt.Errorf("unreadable folder %q has children", path)
		case n.MountBoundary:
			return nil, fmt.Errorf("mount boundary %q has children", path)
		}
		slices.SortFunc(n.children, func(a, b *node) int { return strings.Compare(a.name, b.name) })
	}
	return byPath[""], nil
}

// fileFamily is the D21 family of a file.
func fileFamily(n *node) domain.Family { return domain.FileFamily(n.Category, n.fileKind()) }

// contribution is what a child folder adds to its parent's composition: all
// of it under its family when it is a group outside containers, else its own
// composition.
func (n *node) contribution() map[domain.Family]Counts {
	if f := domain.FamilyOf(n.Category); n.Group && f != "" && f != domain.FamilyContainers {
		return map[domain.Family]Counts{f: {Files: n.totalFiles, Bytes: n.totalBytes}}
	}
	return n.byFamily
}

// dominant is the family holding the most of the folder's composition
// bytes, ties in domain.Families order, or "" when it holds no bytes.
func (n *node) dominant() domain.Family {
	if n.totalBytes == 0 {
		return ""
	}
	best := domain.Families[0]
	for _, f := range domain.Families[1:] {
		if n.byFamily[f].Bytes > n.byFamily[best].Bytes {
			best = f
		}
	}
	return best
}

// findNotable sets n.notable from its children's (design D21).
func (n *node) findNotable() {
	dom := n.dominant()
	for _, c := range n.children {
		switch {
		case c.Kind == domain.EntryDirectory:
			if c.Group || (dom != "" && 2*c.byFamily[dom].Bytes < c.totalBytes) {
				n.notable = append(n.notable, c)
			} else {
				n.notable = append(n.notable, c.notable...)
			}
		case c.Kind == domain.EntryFile:
			if dom != "" && fileFamily(c) != dom {
				n.notable = append(n.notable, c)
			}
		}
	}
}

// inside is the folder's dir_stats.inside list: its 10 largest notable
// entries.
func (n *node) inside() []insideItem {
	sorted := slices.SortedFunc(slices.Values(n.notable), func(a, b *node) int {
		return cmp.Or(cmp.Compare(b.totalBytes, a.totalBytes), strings.Compare(a.Path, b.Path))
	})
	out := []insideItem{}
	for _, c := range sorted[:min(len(sorted), 10)] {
		it := insideItem{EntryID: c.id.String(), PathB64: []byte(c.Path), Path: displayPath(c.Path),
			Group: c.Group, Bytes: c.totalBytes, Files: c.totalFiles}
		if c.Category != "" {
			cat := string(c.Category)
			it.Category = &cat
		}
		fam := string(domain.FamilyOf(c.Category))
		if c.Kind == domain.EntryFile {
			fam = string(fileFamily(c))
		}
		if fam != "" {
			it.Family = &fam
		}
		out = append(out, it)
	}
	return out
}

// insideItem is one element of dir_stats.inside.
type insideItem struct {
	EntryID  string  `json:"entry_id"`
	PathB64  []byte  `json:"path_b64"`
	Path     string  `json:"path"`
	Category *string `json:"category"`
	Family   *string `json:"family"`
	Group    bool    `json:"group"`
	Bytes    int64   `json:"bytes"`
	Files    int64   `json:"files"`
}

// displayPath renders a raw '/'-joined path name by name.
func displayPath(p string) string {
	names := strings.Split(p, "/")
	for i, name := range names {
		names[i] = domain.DisplayName([]byte(name))
	}
	return strings.Join(names, "/")
}

func (n *node) fileKind() domain.FileKind {
	if n.FileKind == "" {
		return domain.FileKindOther
	}
	return n.FileKind
}

// aggregate computes n's subtree totals post-order, as the scan does when a
// folder's last child is done.
func aggregate(n *node) {
	n.byKind = map[domain.FileKind]Counts{}
	n.byYear = map[string]Counts{}
	n.byFamily = map[domain.Family]Counts{}
	for _, f := range domain.Families {
		n.byFamily[f] = Counts{}
	}
	for _, c := range n.children {
		switch {
		case c.Kind == domain.EntryDirectory:
			aggregate(c)
			n.dirs += 1 + c.dirs
			n.files += c.files
			n.symlinks += c.symlinks
			n.specials += c.specials
			n.unreadable += c.unreadable
			n.mounts += c.mounts
			if c.Unreadable {
				n.unreadable++
			}
			if c.MountBoundary {
				n.mounts++
			}
			n.partial = n.partial || c.partial || c.Unreadable
			n.totalBytes += c.totalBytes
			n.totalFiles += c.totalFiles
			if c.hasFiles {
				n.addRange(c.oldest, c.newest)
			}
			mergeInto(n.byKind, c.byKind)
			mergeInto(n.byYear, c.byYear)
			mergeInto(n.byFamily, c.contribution())
		case c.Kind == domain.EntryFile:
			c.totalBytes, c.totalFiles = c.Size, 1
			one := Counts{Files: 1, Bytes: c.Size}
			n.files++
			n.totalBytes += c.Size
			n.totalFiles++
			mt := c.MTime.UnixNano()
			n.addRange(mt, mt)
			addTo(n.byKind, c.fileKind(), one)
			addTo(n.byYear, strconv.Itoa(c.MTime.UTC().Year()), one)
			addTo(n.byFamily, fileFamily(c), one)
		case c.Kind == domain.EntrySymlink:
			n.symlinks++
		default:
			n.specials++
		}
	}
	n.findNotable()
}

func (n *node) addRange(oldest, newest int64) {
	if !n.hasFiles {
		n.oldest, n.newest, n.hasFiles = oldest, newest, true
		return
	}
	n.oldest = min(n.oldest, oldest)
	n.newest = max(n.newest, newest)
}

func addTo[K comparable](m map[K]Counts, k K, c Counts) {
	v := m[k]
	v.add(c)
	m[k] = v
}

func mergeInto[K comparable](dst, src map[K]Counts) {
	for k, c := range src {
		addTo(dst, k, c)
	}
}

// mainKind is the file kind with the most bytes, then the most files, then
// the smallest name.
func mainKind(byKind map[domain.FileKind]Counts) any {
	kinds := slices.SortedFunc(maps.Keys(byKind), func(a, b domain.FileKind) int {
		x, y := byKind[a], byKind[b]
		return cmp.Or(cmp.Compare(y.Bytes, x.Bytes), cmp.Compare(y.Files, x.Files), strings.Compare(string(a), string(b)))
	})
	if len(kinds) == 0 {
		return nil
	}
	return string(kinds[0])
}

// ext is the ASCII-lowercased extension of a file name, or nil.
func ext(name string) any {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return nil
	}
	b := []byte(name[i+1:])
	for j, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[j] = c + ('a' - 'A')
		}
	}
	return string(b)
}

type writer struct {
	tx     *sql.Tx
	source domain.SourceID
	gen    int64
	seen   int64
	ids    map[string]domain.EntryID
}

// write inserts n and its subtree pre-order, so each parent row exists
// before its children reference it. The dir_stats rows follow (writeStats),
// once every ID their inside lists name is known.
func (w writer) write(n *node, path string, parent any) error {
	kind, special := string(n.Kind), any(nil)
	if n.Kind.IsSpecial() {
		kind, special = "special", string(n.Kind)
	}
	var (
		extV, fileKind, mainK, linkText any
		newest, oldest                  any
		category, family, triage        any
		totalBytes, totalFiles          int64
	)
	switch n.Kind {
	case domain.EntryDirectory:
		mainK = mainKind(n.byKind)
		totalBytes, totalFiles = n.totalBytes, n.totalFiles
		if n.hasFiles {
			newest, oldest = n.newest, n.oldest
		}
	case domain.EntryFile:
		extV, fileKind = ext(n.name), string(n.fileKind())
		totalBytes, totalFiles = n.Size, 1
		newest, oldest = n.MTime.UnixNano(), n.MTime.UnixNano()
	default:
		newest, oldest = n.MTime.UnixNano(), n.MTime.UnixNano()
	}
	if n.Kind == domain.EntrySymlink {
		linkText = blob(n.LinkText)
	}
	if n.Category != "" {
		category, family = string(n.Category), string(domain.FamilyOf(n.Category))
	}
	if n.Triage != "" {
		triage = string(n.Triage)
	}
	state := "present"
	if n.Unreadable {
		state = "unreadable"
	}
	var alloc, ctime, dev, ino, nlink, mode any
	if st := n.Lstat; st != nil {
		alloc, dev, ino, nlink, mode = st.Blocks*512, int64(st.Dev), int64(st.Ino), int64(st.Nlink), int64(uint32(st.Mode))
		if !st.Ctime.IsZero() {
			ctime = st.Ctime.UnixNano()
		}
	}
	res, err := w.tx.Exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, special_kind,
		size, alloc, total_bytes, total_files, mtime_ns, ctime_ns, newest_ns, oldest_ns, dev, ino, nlink, mode,
		link_text, ext, file_kind, main_kind, category, family, triage, is_group, state, partial, mount_boundary,
		first_seen, last_seen, scan_gen, eff_decision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'undecided')`,
		string(w.source), parent, blob(n.name), blob(path), kind, special,
		n.Size, alloc, totalBytes, totalFiles, n.MTime.UnixNano(), ctime, newest, oldest, dev, ino, nlink, mode,
		linkText, extV, fileKind, mainK, category, family, triage, boolInt(n.Group), state, boolInt(n.partial),
		boolInt(n.MountBoundary), w.seen, w.seen, w.gen)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	n.id = domain.EntryID(id)
	w.ids[path] = n.id
	if path != "" {
		if _, err := w.tx.Exec(`INSERT INTO entry_names (rowid, name) VALUES (?, ?)`,
			id, domain.DisplayName([]byte(n.name))); err != nil {
			return err
		}
	}
	if n.Kind != domain.EntryDirectory {
		return nil
	}
	for _, c := range n.children {
		if err := w.write(c, c.Path, id); err != nil {
			return err
		}
	}
	return nil
}

// writeStats inserts the dir_stats rows of n's subtree.
func (w writer) writeStats(n *node) error {
	if n.Kind != domain.EntryDirectory {
		return nil
	}
	byKind, err := json.Marshal(n.byKind)
	if err != nil {
		return err
	}
	byYear, err := json.Marshal(n.byYear)
	if err != nil {
		return err
	}
	byFamily, err := json.Marshal(n.byFamily)
	if err != nil {
		return err
	}
	inside, err := json.Marshal(n.inside())
	if err != nil {
		return err
	}
	if _, err = w.tx.Exec(`INSERT INTO dir_stats (entry_id, dirs, files, symlinks, specials, unreadable,
		mount_boundaries, by_kind, by_year, by_family, signals, indicators, inside)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', '[]', ?)`,
		int64(n.id), n.dirs, n.files, n.symlinks, n.specials, n.unreadable, n.mounts,
		string(byKind), string(byYear), string(byFamily), string(inside)); err != nil {
		return err
	}
	for _, c := range n.children {
		if err := w.writeStats(c); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// blob returns s as a non-nil byte slice, so that "" binds as an empty BLOB
// rather than NULL.
func blob(s string) []byte {
	b := make([]byte, len(s))
	copy(b, s)
	return b
}
