package index

import (
	"bytes"
	"slices"
	"time"

	"precious/internal/domain"
	"precious/internal/rules"
)

// agg is a folder's fold: the facts of its subtree, accumulated child by
// child (design D7). A scan accumulates it while it walks the folder (the
// frame embeds one); a refold accumulates it from the stored rows of the
// folder's children (r3 design D6, refold.go). Both take children in with
// the same functions and finish the folder with finishFolder, so a refold
// writes exactly what a scan of the same tree writes.
type agg struct {
	// The subtree's facts. newest and oldest span the files with a known
	// time (dated, domain.KnownModTime).
	files, bytes                               int64
	newest, oldest                             int64
	dated                                      bool
	dirs, symlinks, specials, unreadableN, mts int64
	partial                                    bool
	byKind                                     map[domain.FileKind]rules.KindTotals
	byYear                                     map[int]counts
	// comp is the composition: the bytes and files of the content by
	// family, bottom-up (design D21).
	comp                         [4]counts
	childSignals, subtreeSignals map[rules.SignalID]int
	// indicators counts the entries below that raise an indicator signal;
	// refs holds the first maxIndicators of them by path (r3 design D6).
	indicators int
	refs       []indicatorRef
	// inside holds the notable entries below the folder, largest first, for
	// each family that may turn out to be its dominant one (familyKeys
	// order), and at noDominant for no bytes (design D21).
	inside [noDominant + 1][]insideRef
}

func newAgg() agg {
	return agg{
		byKind: map[domain.FileKind]rules.KindTotals{}, byYear: map[int]counts{},
		childSignals: map[rules.SignalID]int{}, subtreeSignals: map[rules.SignalID]int{},
	}
}

// reset empties the fold, keeping its maps and lists for reuse.
func (a *agg) reset() {
	byKind, byYear, childSignals, subtreeSignals := a.byKind, a.byYear, a.childSignals, a.subtreeSignals
	refs, inside := a.refs, a.inside
	clear(byKind)
	clear(byYear)
	clear(childSignals)
	clear(subtreeSignals)
	clear(refs)
	for i := range inside {
		clear(inside[i])
		inside[i] = inside[i][:0]
	}
	*a = agg{byKind: byKind, byYear: byYear, childSignals: childSignals, subtreeSignals: subtreeSignals,
		refs: refs[:0], inside: inside}
}

func (a *agg) addRange(oldest, newest int64) {
	if !a.dated {
		a.oldest, a.newest, a.dated = oldest, newest, true
		return
	}
	a.oldest = min(a.oldest, oldest)
	a.newest = max(a.newest, newest)
}

// unknownYear is the by_year key of the files without a known time.
const unknownYear = 0

// ownRange is a file's or leaf's own newest and oldest time: its
// modification time when known, else none.
func ownRange(mtime opt) (newest, oldest opt) {
	if !mtime.ok || !domain.KnownModTime(mtime.v) {
		return opt{}, opt{}
	}
	return mtime, mtime
}

// addFile counts one regular file of the subtree under its file family.
func (a *agg) addFile(kind domain.FileKind, size, mtime int64, family domain.Family) {
	a.files++
	a.bytes += size
	y := unknownYear
	if domain.KnownModTime(mtime) {
		a.addRange(mtime, mtime)
		y = time.Unix(0, mtime).UTC().Year()
	}
	kt := a.byKind[kind]
	a.byKind[kind] = rules.KindTotals{Files: kt.Files + 1, Bytes: kt.Bytes + size}
	c := a.byYear[y]
	c.add(counts{files: 1, bytes: size})
	a.byYear[y] = c
	a.comp[familyIndex(family)].add(counts{files: 1, bytes: size})
}

// nameSignals counts the signals the name of a child of kind raises, and
// returns the first indicator among them, or "".
func (a *agg) nameSignals(pol *rules.Policy, sigs []rules.SignalID, name []byte, kind domain.EntryKind) ([]rules.SignalID, rules.SignalID) {
	sigs = pol.AppendNameSignals(sigs[:0], name, kind)
	var indicator rules.SignalID
	for _, sig := range sigs {
		a.childSignals[sig]++
		a.subtreeSignals[sig]++
		if indicator == "" && pol.IsIndicator(sig) {
			indicator = sig
		}
	}
	if indicator != "" {
		a.indicators++
	}
	return sigs, indicator
}

// refFits reports whether an indicator at the path of name inside the folder
// at dir enters the fold's list: the list keeps the maxIndicators smallest
// paths (r3 design D6), so it does not depend on the order children come in.
func (a *agg) refFits(dir, name []byte) bool {
	return len(a.refs) < maxIndicators || comparePath(dir, name, a.refs[maxIndicators-1].path) < 0
}

// fileRow classifies the regular file name of kind, whose folder's files
// give the pair stems, and sets the derived columns of its row r, which
// holds its own facts. owner, when not nil, is the owner's override. It
// returns the file's family.
func fileRow(pol *rules.Policy, c *codec, name []byte, kind domain.FileKind, stems map[string]bool,
	owner *domain.Override, r *row) domain.Family {
	res := pol.ClassifyFile(rules.FileFacts{Name: name, Kind: kind, Size: r.size, SiblingStems: stems})
	if owner != nil {
		res = rules.ApplyOwner(res, *owner)
	}
	r.classify(&res, c)
	r.fileKind = text(kind)
	r.ext = c.ext(name)
	r.totalBytes, r.totalFiles = r.size, 1
	r.newest, r.oldest = ownRange(r.mtime)
	return domain.FileFamily(res.Category, kind)
}

// finishFolder classifies the folder of fold a, called name for the rules,
// and sets its row r, which holds its kind, state, and own facts: partial,
// classification, totals, time range, and main kind. owner, when not nil,
// is the owner's override, which takes precedence before anything reads
// the result (r2b design D3). It returns what the folder adds to its
// parent's composition, its inside list, and its dir_stats columns but the
// indicators and inside JSON, which need the IDs of the entries listed.
func (a *agg) finishFolder(pol *rules.Policy, c *codec, name []byte, owner *domain.Override, r *row) (contribution [4]counts, inside []insideRef, cols statsCols) {
	res := pol.ClassifyFolder(rules.FolderFacts{
		Name: name, ChildSignals: a.childSignals, SubtreeSignals: a.subtreeSignals,
		Files: a.files, Bytes: a.bytes, ByKind: a.byKind, Indicators: a.indicators,
	})
	if owner != nil {
		res = rules.ApplyOwner(res, *owner)
	}
	r.partial = a.partial
	r.classify(&res, c)
	r.totalBytes, r.totalFiles = a.bytes, a.files
	r.newest, r.oldest = opt{}, opt{}
	if a.dated {
		r.newest, r.oldest = some(a.newest), some(a.oldest)
	}
	r.mainKind = ""
	if a.files > 0 {
		r.mainKind = mainKind(a.byKind)
	}
	cols = statsCols{
		dirs: a.dirs, files: a.files, symlinks: a.symlinks, specials: a.specials,
		unreadable: a.unreadableN, mounts: a.mts,
		byKind: c.byKind(a.byKind), byYear: c.byYear(a.byYear),
		byFamily: c.byFamily(&a.comp), signals: c.signals(a.subtreeSignals),
	}
	return contributionOf(r, &a.comp, a.files, a.bytes), a.inside[dominant(&a.comp, a.bytes)], cols
}

// contributionOf is what a finished folder with row r and composition comp
// adds to its parent's composition (design D21): its composition, unless it
// is a group outside Containers, which counts whole under its own family.
func contributionOf(r *row, comp *[4]counts, files, bytes int64) [4]counts {
	if fam := domain.Family(r.family); r.group && fam != "" && fam != domain.FamilyContainers {
		var c [4]counts
		c[familyIndex(fam)] = counts{files: files, bytes: bytes}
		return c
	}
	return *comp
}

// finished is a finished folder as its parent's fold takes it in.
type finished struct {
	a *agg // its fold
	r *row // its row: classification, state, partial, mount boundary
	// id is its entry, or 0 for one the scan inserted, known by token.
	id     domain.EntryID
	token  uint64
	path   []byte
	inside []insideRef // its own inside list
	// contribution is what it adds to the parent's composition.
	contribution [4]counts
}

// absorb adds the finished folder c to its parent's fold p.
func (t *tokens) absorb(p *agg, c *finished) {
	f, unreadable := c.a, c.r.state == "unreadable"
	p.dirs += 1 + f.dirs
	p.symlinks += f.symlinks
	p.specials += f.specials
	p.unreadableN += f.unreadableN + boolInt(unreadable)
	p.mts += f.mts + boolInt(c.r.boundary)
	p.partial = p.partial || c.r.partial || unreadable
	p.files += f.files
	p.bytes += f.bytes
	if f.dated {
		p.addRange(f.oldest, f.newest)
	}
	for k, kt := range f.byKind {
		pt := p.byKind[k]
		p.byKind[k] = rules.KindTotals{Files: pt.Files + kt.Files, Bytes: pt.Bytes + kt.Bytes}
	}
	for y, n := range f.byYear {
		pc := p.byYear[y]
		pc.add(n)
		p.byYear[y] = pc
	}
	for i := range c.contribution {
		p.comp[i].add(c.contribution[i])
	}
	for sig, n := range f.subtreeSignals {
		p.subtreeSignals[sig] += n
	}
	p.indicators += f.indicators
	for _, ref := range f.refs {
		t.addRef(p, ref)
	}
	t.notableFolder(p, c)
}

// addRef puts ref in the fold's indicator list, in path order, when it is
// among the maxIndicators smallest paths; the reference it pushes out, or
// ref itself when it does not fit, is dropped. A list takes over the hold
// of the references it is given.
func (t *tokens) addRef(a *agg, ref indicatorRef) {
	i, _ := slices.BinarySearchFunc(a.refs, ref.path, func(r indicatorRef, p []byte) int {
		return bytes.Compare(r.path, p)
	})
	if i >= maxIndicators {
		t.drop(ref.token)
		return
	}
	if len(a.refs) == maxIndicators {
		t.drop(a.refs[maxIndicators-1].token)
		a.refs = a.refs[:maxIndicators-1]
	}
	a.refs = slices.Insert(a.refs, i, ref)
}

// notableFile offers the file name of the folder at dir, with row r and
// family, to the inside lists of the families it differs from (design D21).
// A file without an ID (one the scan inserts) that any list takes gets a
// token in *token, so that the writer can resolve its ID.
func (t *tokens) notableFile(a *agg, dir, name []byte, id domain.EntryID, token *uint64, r *row, family domain.Family) {
	own := familyIndex(family)
	var ref insideRef
	for i := range familyKeys {
		if i == own || !accepts(a.inside[i], r.size, dir, name) {
			continue
		}
		if ref.path == nil {
			ref = insideRef{path: joinPath(dir, name), category: r.category, family: text(family),
				bytes: r.size, files: 1, id: id}
			if id == 0 {
				if *token == 0 {
					*token = t.newToken()
				}
				ref.token = *token
			}
		}
		t.offer(&a.inside[i], &ref)
	}
}

// notableFolder offers the finished folder c to its parent's inside lists
// (design D21). For each family that may be the parent's dominant one, c
// itself is notable when it is a group or holds less than half of its bytes
// in that family; otherwise the entries of its own list are offered. With no
// dominant family, only groups are notable.
func (t *tokens) notableFolder(p *agg, c *finished) {
	self := insideRef{id: c.id, token: c.token, path: c.path, category: c.r.category, family: c.r.family,
		group: c.r.group, bytes: c.a.bytes, files: c.a.files}
	for i := range p.inside {
		if c.r.group || (i != noDominant && 2*c.a.comp[i].bytes < c.a.bytes) {
			t.offer(&p.inside[i], &self)
			continue
		}
		for j := range c.inside {
			t.offer(&p.inside[i], &c.inside[j])
		}
	}
}

// tokens numbers the entries a scan inserts that a list refers to before
// the writer assigns their IDs, and counts the lists referring to each (a
// refold refers to stored entries by ID only, so every token it meets is 0).
type tokens struct {
	token uint64
	// held counts the lists referring to each token; -1 marks a token in
	// release, which no list refers to any more.
	held map[uint64]int32
	// release collects the tokens no list refers to, for the next folder
	// finish the writer applies, after that folder's lists are resolved.
	release []uint64
}

// hold counts one more list referring to an inserted entry's token. A token
// released since, but not yet handed to the writer, is taken back.
func (t *tokens) hold(token uint64) {
	if token == 0 {
		return
	}
	n := t.held[token]
	if n < 0 {
		t.release = slices.DeleteFunc(t.release, func(x uint64) bool { return x == token })
		n = 0
	}
	t.held[token] = n + 1
}

// drop counts one list less referring to token. Once none does, the token
// is released with the next folder finish the writer applies.
func (t *tokens) drop(token uint64) {
	if token == 0 || t.held[token] <= 0 {
		return
	}
	if t.held[token]--; t.held[token] == 0 {
		t.held[token] = -1
		t.release = append(t.release, token)
	}
}

// released hands the tokens released so far to a folder finish.
func (t *tokens) released() []uint64 {
	out := slices.Clone(t.release)
	for _, tok := range t.release {
		delete(t.held, tok)
	}
	t.release = t.release[:0]
	return out
}

// newToken returns a fresh token for an entry the scan inserts.
func (t *tokens) newToken() uint64 {
	t.token++
	return t.token
}
