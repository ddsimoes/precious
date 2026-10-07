package corpus

import (
	"cmp"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"precious/internal/domain"
)

// What a complete hashing run must find in a tree (R2 design D8, D9, D19),
// and the rows of the rescue card (r2c design D6). Everything here is
// computed from the bytes the tree writes, except the relations, which are
// declared and checked against those bytes, and the rescue rows, which need
// the rules' classification and are declared in rescueDeclarations.
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

// Rescue is one row of the rescue card: a user-material indicator (a file
// or a folder) inside a programs or disposable group, and the outermost
// such group holding it. Both are display paths.
type Rescue struct {
	Path  string `json:"path"`
	Group string `json:"group"`
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

// rescueTruth returns the rescue card's rows: the declared indicators that
// lie inside no other declared indicator, so that no byte counts twice in
// the card, in its order: largest first (a file's size, a folder's total
// bytes), then by raw path.
func (t *Tree) rescueTruth() []Rescue {
	var decls []rescueDecl
	for _, r := range t.rescue {
		if !slices.ContainsFunc(t.rescue, func(o rescueDecl) bool { return strings.HasPrefix(r.path, o.path+"/") }) {
			decls = append(decls, r)
		}
	}
	bytes := make(map[string]int64, len(decls))
	for _, r := range decls {
		for _, it := range t.visible() {
			if it.kind == domain.EntryFile && (it.path == r.path || strings.HasPrefix(it.path, r.path+"/")) {
				bytes[r.path] += int64(len(it.data))
			}
		}
	}
	slices.SortFunc(decls, func(a, b rescueDecl) int {
		return cmp.Or(cmp.Compare(bytes[b.path], bytes[a.path]), strings.Compare(a.path, b.path))
	})
	out := make([]Rescue, len(decls))
	for i, r := range decls {
		out[i] = Rescue{Path: displayPath(r.path), Group: displayPath(r.group)}
	}
	return out
}

// rescueDecl is one indicator of a group: a file or a folder below it.
type rescueDecl struct{ group, path string }

// checkRescue checks that every declared group is a folder holding its
// indicator, a file or a folder, and returns decls.
func (t *Tree) checkRescue(decls []rescueDecl) []rescueDecl {
	kinds := make(map[string]domain.EntryKind)
	for _, it := range t.visible() {
		kinds[it.path] = it.kind
	}
	for _, r := range decls {
		if kinds[r.group] != domain.EntryDirectory {
			panic("corpus: rescue group is no folder: " + displayPath(r.group))
		}
		if k := kinds[r.path]; k != domain.EntryFile && k != domain.EntryDirectory || !strings.HasPrefix(r.path, r.group+"/") {
			panic("corpus: rescue row is no entry of its group: " + displayPath(r.path))
		}
	}
	return decls
}
