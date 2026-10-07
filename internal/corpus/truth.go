package corpus

import (
	"cmp"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"precious/internal/domain"
)

// What a complete hashing run must find in a tree (R2 design D8, D9, D14,
// D19). Everything here is computed from the bytes the tree writes, except
// the relations, which are declared and checked against those bytes, and
// the parts of Gems that need the rules' classification, which are declared
// in gemDeclarations.
//
// A copy is an indexed file (not one inside an unreadable folder) or a file
// member of an archive the archive readers open completely; the corpus has
// no hard links. Copies are named by their raw path; a member's path is its
// archive file's path, '!', and its path inside the archive, as in
// "Downloads/fotos_2005_do_pendrive.zip!Carnaval/DSC01001.JPG". Empty files
// have no content (contents.size > 0) and are never copies.

// Path names an entry or a member: its display form and its raw bytes in
// base64, as Entry does.
type Path struct {
	Path    string `json:"path"`
	PathB64 string `json:"path_b64"`
}

func pathOf(raw string) Path {
	return Path{Path: displayCopy(raw), PathB64: base64.StdEncoding.EncodeToString([]byte(raw))}
}

// displayCopy renders a copy's raw path, keeping the '!' between an archive
// and its member.
func displayCopy(raw string) string {
	archive, member, ok := strings.Cut(raw, "!")
	if !ok {
		return displayPath(raw)
	}
	return displayPath(archive) + "!" + displayPath(member)
}

// Duplicate is one duplicate group: a content with at least two copies.
type Duplicate struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Copies are sorted by raw path.
	Copies []Path `json:"copies"`
}

// Archive is the complete listing of one archive file.
type Archive struct {
	Path
	// Format is the archives.format value: zip, tar_gzip, gzip, or bzip2.
	Format  string   `json:"format"`
	Members []Member `json:"members"`
}

// Member is one member of an archive. Path is relative to the archive, so
// the copy is Archive.Path + "!" + Member.Path. Kind is directory or file;
// directories include the folders implied by deeper paths. Size and SHA256
// are set for files; Stored marks a zip member stored without compression.
// Locator is a zip file member's central-directory index (0-based). MTime is
// the time the archive records for the member: unset for implied folders and
// for a bzip2 member (bzip2 records none); a gzip member has the header's.
type Member struct {
	Path
	Kind    string     `json:"kind"`
	Size    *int64     `json:"size,omitempty"`
	SHA256  string     `json:"sha256,omitempty"`
	Stored  bool       `json:"stored,omitempty"`
	Locator *int       `json:"locator,omitempty"`
	MTime   *time.Time `json:"mtime,omitempty"`
}

// The relation kinds of design D9.
const (
	relSame    = "same"
	relInside  = "inside"
	relOverlap = "overlap"
)

// Relation is a declared relation between two folders or archives, checked
// against their content: same when each side's content all occurs on the
// other, inside when side A's content all occurs on side B, and overlap when
// at least half of one side's bytes have their content on the other side.
// The sides follow design D9: for inside, A is the contained side; for same,
// A is the archive side against a folder, otherwise the later raw path; for
// overlap, A has the larger matched share. AOnly and BOnly are the copies
// on each side whose content does not occur on the other, by raw path.
type Relation struct {
	Kind  string `json:"kind"`
	A     Path   `json:"a"`
	B     Path   `json:"b"`
	AOnly []Path `json:"a_only"`
	BOnly []Path `json:"b_only"`
}

// Gems are the Gems sections (design D14), each in its order.
type Gems struct {
	// Unique are the personal images, videos, audio files, and documents
	// outside every programs or disposable group that have no other copy,
	// oldest first (then by raw path).
	Unique []Gem `json:"unique"`
	// Rescue are the indicators of the programs and disposable groups (files
	// or folders), each under its outermost such group, in declaration
	// order.
	Rescue []Gem `json:"rescue"`
	// OnlyInCopy are the files on one side of a declared overlap relation
	// that have no other copy, by relation then raw path.
	OnlyInCopy []Gem `json:"only_in_copy"`
}

// Gem is one entry of a Gems section. Group is set in rescue, Relation (an
// index into GroundTruth.Relations) in only_in_copy. Copies counts a file's
// copies, itself included; it is 0 for a folder.
type Gem struct {
	Path
	Group    *Path `json:"group,omitempty"`
	Relation *int  `json:"relation,omitempty"`
	Copies   int   `json:"copies"`
}

// copyOf is one copy of a content.
type copyOf struct {
	path string // raw; "archive!member" for a member
	size int64
	sum  [32]byte
}

// visible returns the items a complete scan indexes, in creation order.
func (t *Tree) visible() []item {
	var hidden []string
	out := make([]item, 0, len(t.items))
items:
	for _, it := range t.items {
		for _, h := range hidden {
			if strings.HasPrefix(it.path, h) {
				continue items
			}
		}
		if it.unreadable {
			hidden = append(hidden, it.path+"/")
		}
		out = append(out, it)
	}
	return out
}

// copies returns the copies of the tree: with side "", every file and every
// member; else the copies the folder or archive at side holds, where an
// archive counts as its members and not as a file, as relate splices
// archives in as folders (design D9).
func (t *Tree) copies(side string) []copyOf {
	var out []copyOf
	for _, it := range t.visible() {
		if it.kind != domain.EntryFile || side != "" && it.path != side && !strings.HasPrefix(it.path, side+"/") {
			continue
		}
		if len(it.data) > 0 && (side == "" || it.archive == nil) {
			out = append(out, copyOf{path: it.path, size: int64(len(it.data)), sum: it.sum})
		}
		if it.archive != nil {
			for _, m := range it.archive.members {
				if m.kind == memberFile && len(m.data) > 0 {
					out = append(out, copyOf{path: it.path + "!" + m.path, size: int64(len(m.data)), sum: m.sum})
				}
			}
		}
	}
	return out
}

// counts returns how many copies the tree holds of each content.
func (t *Tree) counts() map[[32]byte]int {
	n := make(map[[32]byte]int)
	for _, c := range t.copies("") {
		n[c.sum]++
	}
	return n
}

// duplicates returns the duplicate groups, sorted by their first copy.
func (t *Tree) duplicates() []Duplicate {
	type group struct {
		sum   [32]byte
		size  int64
		paths []string
	}
	by := make(map[[32]byte]*group)
	for _, c := range t.copies("") {
		g := by[c.sum]
		if g == nil {
			g = &group{sum: c.sum, size: c.size}
			by[c.sum] = g
		}
		g.paths = append(g.paths, c.path)
	}
	var groups []*group
	for _, g := range by {
		if len(g.paths) > 1 {
			slices.Sort(g.paths)
			groups = append(groups, g)
		}
	}
	slices.SortFunc(groups, func(a, b *group) int { return strings.Compare(a.paths[0], b.paths[0]) })
	out := make([]Duplicate, 0, len(groups))
	for _, g := range groups {
		d := Duplicate{SHA256: hex.EncodeToString(g.sum[:]), Size: g.size}
		for _, p := range g.paths {
			d.Copies = append(d.Copies, pathOf(p))
		}
		out = append(out, d)
	}
	return out
}

// members returns every visible archive's listing, by archive path.
func (t *Tree) members() []Archive {
	var archives []item
	for _, it := range t.visible() {
		if it.archive != nil {
			archives = append(archives, it)
		}
	}
	slices.SortFunc(archives, func(a, b item) int { return strings.Compare(a.path, b.path) })
	out := make([]Archive, 0, len(archives))
	for _, it := range archives {
		a := Archive{Path: pathOf(it.path), Format: it.archive.format}
		for _, m := range it.archive.members {
			x := Member{Path: pathOf(m.path), Kind: m.kind, Stored: m.stored, Locator: m.locator}
			if !m.mtime.IsZero() {
				mtime := m.mtime
				x.MTime = &mtime
			}
			if m.kind == memberFile {
				size := int64(len(m.data))
				x.Size, x.SHA256 = &size, hex.EncodeToString(m.sum[:])
			}
			a.Members = append(a.Members, x)
		}
		out = append(out, a)
	}
	return out
}

// relationDecl declares that the folders or archives x and y are related
// with kind; the order of x and y does not matter.
type relationDecl struct {
	kind string
	x, y string
}

// relationDecls are the relations the corpus asserts (duplicates delta
// spec): the zips and the tar.gz are the same as their unpacked folders,
// the copied Winamp is the same as the original, Fotos - Copia overlaps
// Fotos while their 2004 folders are the same, and the site's copy with one
// changed file overlaps the site. Relate may find more, such as the parent
// folders of the partial 2004 backup copy.
var relationDecls = []relationDecl{
	{relSame, "Downloads/fotos_2005_do_pendrive.zip", "Downloads/fotos_2005_do_pendrive"},
	{relSame, "Downloads/eMule0.47c-Installer.zip", "Downloads/emule-0.47c"},
	{relSame, "Projetos/site_antigo_2006.tar.gz", "Projetos/site_antigo"},
	{relSame, oldCopy + "/Arquivos de programas/Winamp", programs + "/Winamp"},
	{relOverlap, "Fotos - Copia", "Fotos"},
	{relSame, "Fotos - Copia/2004", "Fotos/2004"},
	{relOverlap, "Projetos/site_antigo_copia", "Projetos/site_antigo"},
}

// relation is a resolved relation, sides ordered by design D9.
type relation struct {
	kind         string
	a, b         string
	aOnly, bOnly []string
}

// resolve checks each declared relation against the content of its sides,
// orders its sides, and lists what is only on each side.
func (t *Tree) resolve(decls []relationDecl) []relation {
	isArchive := make(map[string]bool)
	exists := make(map[string]bool)
	for _, it := range t.visible() {
		exists[it.path] = it.kind == domain.EntryDirectory || it.archive != nil
		isArchive[it.path] = it.archive != nil
	}
	out := make([]relation, 0, len(decls))
	for _, d := range decls {
		for _, p := range []string{d.x, d.y} {
			if !exists[p] {
				panic("corpus: relation side is no folder or archive: " + displayPath(p))
			}
		}
		if strings.HasPrefix(d.x, d.y+"/") || strings.HasPrefix(d.y, d.x+"/") {
			panic("corpus: relation side contains the other: " + displayPath(d.x))
		}
		x, y := t.copies(d.x), t.copies(d.y)
		xOnly, xShare := only(x, y)
		yOnly, yShare := only(y, x)
		var kind string
		switch {
		case len(x) == 0 || len(y) == 0:
		case len(xOnly) == 0 && len(yOnly) == 0:
			kind = relSame
		case len(xOnly) == 0 || len(yOnly) == 0:
			kind = relInside
		case xShare >= 0.5 || yShare >= 0.5:
			kind = relOverlap
		}
		if kind != d.kind {
			panic(fmt.Sprintf("corpus: %s and %s are %q by content, declared %q", displayPath(d.x), displayPath(d.y), kind, d.kind))
		}
		// Side A: the contained side; the archive against a folder, else the
		// later path; the larger matched share.
		var swap bool
		switch kind {
		case relInside:
			swap = len(yOnly) == 0
		case relSame:
			if isArchive[d.x] != isArchive[d.y] {
				swap = isArchive[d.y]
			} else {
				swap = d.y > d.x
			}
		case relOverlap:
			if xShare == yShare {
				panic("corpus: overlap with equal shares: " + displayPath(d.x))
			}
			swap = yShare > xShare
		}
		r := relation{kind: kind, a: d.x, b: d.y, aOnly: xOnly, bOnly: yOnly}
		if swap {
			r.a, r.b, r.aOnly, r.bOnly = r.b, r.a, r.bOnly, r.aOnly
		}
		out = append(out, r)
	}
	return out
}

// only returns the copies of side whose content does not occur in other, by
// raw path, and the share of side's bytes whose content does.
func only(side, other []copyOf) ([]string, float64) {
	in := make(map[[32]byte]bool, len(other))
	for _, c := range other {
		in[c.sum] = true
	}
	var paths []string
	var total, matched int64
	for _, c := range side {
		total += c.size
		if in[c.sum] {
			matched += c.size
		} else {
			paths = append(paths, c.path)
		}
	}
	slices.Sort(paths)
	if total == 0 {
		return paths, 0
	}
	return paths, float64(matched) / float64(total)
}

func (t *Tree) relationTruth() []Relation {
	out := make([]Relation, 0, len(t.relations))
	for _, r := range t.relations {
		x := Relation{Kind: r.kind, A: pathOf(r.a), B: pathOf(r.b), AOnly: []Path{}, BOnly: []Path{}}
		for _, p := range r.aOnly {
			x.AOnly = append(x.AOnly, pathOf(p))
		}
		for _, p := range r.bOnly {
			x.BOnly = append(x.BOnly, pathOf(p))
		}
		out = append(out, x)
	}
	return out
}

// gemTruth computes the Gems sections from the declarations and the copy
// counts.
func (t *Tree) gemTruth() Gems {
	counts := t.counts()
	g := Gems{Unique: []Gem{}, Rescue: []Gem{}, OnlyInCopy: []Gem{}}
	byPath := make(map[string]item)
	for _, it := range t.visible() {
		byPath[it.path] = it
	}

	var unique []item
	for _, it := range t.visible() {
		if it.kind == domain.EntryFile && len(it.data) > 0 && t.gems.personal(it.path) && counts[it.sum] == 1 {
			unique = append(unique, it)
		}
	}
	slices.SortFunc(unique, func(a, b item) int {
		return cmp.Or(a.mtime.Compare(b.mtime), strings.Compare(a.path, b.path))
	})
	for _, it := range unique {
		g.Unique = append(g.Unique, Gem{Path: pathOf(it.path), Copies: 1})
	}

	for _, r := range t.gems.rescue {
		group := pathOf(r.group)
		x := Gem{Path: pathOf(r.path), Group: &group}
		if it := byPath[r.path]; it.kind == domain.EntryFile {
			x.Copies = counts[it.sum]
		}
		g.Rescue = append(g.Rescue, x)
	}

	for i, r := range t.relations {
		if r.kind != relOverlap {
			continue
		}
		var paths []string
		for _, p := range append(slices.Clone(r.aOnly), r.bOnly...) {
			if it, ok := byPath[p]; ok && counts[it.sum] == 1 {
				paths = append(paths, p)
			}
		}
		slices.Sort(paths)
		for _, p := range paths {
			g.OnlyInCopy = append(g.OnlyInCopy, Gem{Path: pathOf(p), Relation: &i, Copies: 1})
		}
	}
	return g
}

// gemDecls declare what Gems needs from the rules' classification (design
// D14), which the corpus does not run: which files are personal images,
// videos, audio files, and documents outside every programs or disposable
// group, and which indicators those groups raise.
type gemDecls struct {
	// kinds maps the lower-case extensions of the corpus's files of kind
	// image, video, audio, or document to that kind.
	kinds map[string]string
	// outside are the outermost groups of family programs or disposable.
	outside []string
	// notPersonal are the files of those kinds that rules classify outside
	// the personal family.
	notPersonal []string
	// rescue are the indicators of the programs and disposable groups.
	rescue []rescueDecl
}

// rescueDecl is one indicator of a group: a file or a folder below it.
type rescueDecl struct{ group, path string }

// personal reports whether the file at p is a personal image, video, audio
// file, or document outside every programs or disposable group.
func (g gemDecls) personal(p string) bool {
	if g.kinds[strings.ToLower(path.Ext(p))] == "" || slices.Contains(g.notPersonal, p) {
		return false
	}
	for _, o := range g.outside {
		if strings.HasPrefix(p, o+"/") {
			return false
		}
	}
	return true
}

// checkGems checks that every declared path exists with its kind, and
// returns g.
func (t *Tree) checkGems(g gemDecls) gemDecls {
	kinds := make(map[string]domain.EntryKind)
	for _, it := range t.visible() {
		kinds[it.path] = it.kind
	}
	want := func(p string, k domain.EntryKind) {
		if kinds[p] != k {
			panic(fmt.Sprintf("corpus: Gems declares %s, which is no %s", displayPath(p), k))
		}
	}
	for _, p := range g.outside {
		want(p, domain.EntryDirectory)
	}
	for _, p := range g.notPersonal {
		want(p, domain.EntryFile)
	}
	for _, r := range g.rescue {
		want(r.group, domain.EntryDirectory)
		if k := kinds[r.path]; k != domain.EntryFile && k != domain.EntryDirectory || !strings.HasPrefix(r.path, r.group+"/") {
			panic("corpus: Gems rescue is no entry of its group: " + displayPath(r.path))
		}
	}
	return g
}
