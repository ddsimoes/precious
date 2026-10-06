package relations

import (
	"bytes"
	"cmp"
	"context"
	"slices"

	"precious/internal/domain"
	"precious/internal/store"
)

// The relation algorithm, restored from curator-m4b's relate.go (design
// D1, D9): partner, Relate, maximal, and lift, unchanged but for the
// overlap share (50%, no longer 10%), no reporting floor, no file results
// (duplicate groups cover single files, design D12), and freeable bytes
// renamed redundant bytes.

// The relation kinds, as stored in relations.kind.
const (
	KindSame    = "same"
	KindInside  = "inside"
	KindOverlap = "overlap"
)

// maxTopsPerKey bounds the partner subtrees one key proposes for a folder,
// and maxChains the chains searched below the best of them for the deepest
// partner. A key present in many places (a common library in every
// project) would otherwise make every folder holding it evaluate every
// other one.
const (
	maxTopsPerKey = 64
	maxChains     = 16
)

// match is what folder A has under folder B.
type match struct {
	bytes, files int64 // regular files and file members whose key occurs under B
	entries      int64 // files, members, and symlinks whose key occurs under B
	// redundant: regular files outside archives whose content occurs
	// under B as another inode, and the file size of each whole archive in
	// A whose members all do (archiveFreed)
	redundant int64
}

// compare orders partners: more matched bytes, then more matched entries.
func (m match) compare(o match) int {
	return cmp.Or(cmp.Compare(m.bytes, o.bytes), cmp.Compare(m.entries, o.entries))
}

func (s *Snapshot) sameInode(e, f int32) bool { return s.eIno[e] >= 0 && s.eIno[e] == s.eIno[f] }

// lowerBound returns the first index j >= from of occ whose directory is
// at least d.
func (s *Snapshot) lowerBound(occ []int32, from int, d int32) int {
	lo, hi := from, len(occ)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if s.eDir[occ[m]] < d {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

// occursUnder reports whether key k occurs in directory b's subtree.
func (s *Snapshot) occursUnder(k, b int32) bool {
	occ := s.occOf(k)
	j := s.lowerBound(occ, 0, b)
	return j < len(occ) && s.eDir[occ[j]] <= s.end[b]
}

// redundantUnder reports whether content entry e has a copy in b's subtree
// that is another inode.
func (s *Snapshot) redundantUnder(e, b int32) bool {
	occ := s.occOf(s.eKey[e])
	for j := s.lowerBound(occ, 0, b); j < len(occ) && s.eDir[occ[j]] <= s.end[b]; j++ {
		if !s.sameInode(e, occ[j]) {
			return true
		}
	}
	return false
}

func (s *Snapshot) match(a, b int32) match {
	var m match
	lo, hi := s.entries(a)
	end := s.end[b]
	// The members of one archive are consecutive entries: cur is the
	// archive being read, got its members matched so far, and free whether
	// each matched as another inode.
	cur, got, free := int32(-1), int64(0), true
	for e := lo; e < hi; e++ {
		if arc := s.arc[s.eDir[e]]; arc != cur {
			m.redundant += s.archiveFreed(a, cur, got, free)
			cur, got, free = arc, 0, true
		}
		k := s.eKey[e]
		occ := s.occOf(k)
		if len(occ) < 2 {
			continue
		}
		j := s.lowerBound(occ, 0, b)
		if j == len(occ) || s.eDir[occ[j]] > end {
			continue
		}
		m.entries++
		got++
		kind := s.kKind[k]
		if kind == keyLink {
			continue
		}
		m.files++
		m.bytes += s.eSize[e]
		if kind != keyContent {
			free = false
			continue
		}
		other := false
		for ; j < len(occ) && s.eDir[occ[j]] <= end; j++ {
			if !s.sameInode(e, occ[j]) {
				other = true
				break
			}
		}
		switch {
		case !other:
			free = false
		case cur < 0:
			m.redundant += s.eSize[e]
		}
	}
	m.redundant += s.archiveFreed(a, cur, got, free)
	return m
}

// archiveFreed is the file size of archive arc when folder a holds it whole
// and got, its members matched, covers every key it has, each as another
// inode (free); else 0. A side inside an archive frees nothing.
func (s *Snapshot) archiveFreed(a, arc int32, got int64, free bool) int64 {
	if arc < 0 || !free || s.arcDir[arc] < a {
		return 0
	}
	if t := s.subtree(s.arcDir[arc]); t.gaps != 0 || t.keyed == 0 || got != t.keyed {
		return 0
	}
	return s.arcSize[arc]
}

// inside reports whether folder a, with match m against some folder, is
// inside it: no gap, a key, and every key matched.
func (s *Snapshot) inside(a int32, m match) bool {
	t := s.subtree(a)
	return t.gaps == 0 && t.keyed > 0 && m.entries == t.keyed
}

// relScratch is reused across the folders of one Relate or Candidates call.
type relScratch struct {
	stamp []int32 // stamp[d] == a+1 when d is already a partner subtree of folder a
	tops  []int32
	best  []int32
	chain []int32
	ends  []int32 // the deepest folder of each descended chain
	memo  map[int32]match
}

func newRelScratch(n int) *relScratch {
	return &relScratch{stamp: make([]int32, n), memo: map[int32]match{}}
}

func (s *Snapshot) memoMatch(a, b int32, sc *relScratch) match {
	if m, ok := sc.memo[b]; ok {
		return m
	}
	m := s.match(a, b)
	sc.memo[b] = m
	return m
}

// partnerTop returns the highest folder on directory d's ancestor chain that
// is not an ancestor of folder a: the child of their common ancestor, or d's
// source root.
func (s *Snapshot) partnerTop(d, a int32) int32 {
	for {
		p := s.parent[d]
		if p < 0 || s.contains(p, a) {
			return d
		}
		d = p
	}
}

// partner finds folder a's best partner among the folders on the ancestor
// chains of the other occurrences of a's three largest shared keys, minus
// a's ancestors and descendants: the most matched bytes, then the most
// matched entries, then the deepest, then the path-first. It returns -1
// when no candidate matches anything.
//
// Matches only grow toward the root, so the subtrees just below the common
// ancestor (the tops) bound every chain: the best match is found among the
// tops, and the deepest folder keeping it by a binary search down each
// chain.
func (s *Snapshot) partner(a int32, sc *relScratch) (int32, match) {
	clear(sc.memo)
	sc.tops = sc.tops[:0]
	tag := a + 1
	for _, k := range s.top[a] {
		if k < 0 {
			break
		}
		occ := s.occOf(k)
		for j, n := 0, 0; j < len(occ) && n < maxTopsPerKey; {
			d := s.eDir[occ[j]]
			if s.contains(a, d) {
				j = s.lowerBound(occ, j, s.end[a]+1) // a's own occurrences
				continue
			}
			t := s.partnerTop(d, a)
			if sc.stamp[t] != tag {
				sc.stamp[t] = tag
				sc.tops = append(sc.tops, t)
			}
			n++
			j = s.lowerBound(occ, j, s.end[t]+1)
		}
	}
	var best match
	sc.best = sc.best[:0]
	for _, t := range sc.tops {
		m := s.memoMatch(a, t, sc)
		switch c := m.compare(best); {
		case c > 0:
			best = m
			sc.best = append(sc.best[:0], t)
		case c == 0 && m.entries > 0:
			sc.best = append(sc.best, t)
		}
	}
	if best.entries == 0 {
		return -1, match{}
	}
	// Descend at most maxChains chains in all, spread over the tied tops,
	// keeping the deepest folder of each chain that keeps the best match.
	sc.ends = sc.ends[:0]
	perTop, chains := max(1, maxChains/len(sc.best)), 0
	for _, t := range sc.best {
		if chains == maxChains {
			break
		}
		var key int32 = -1
		for _, k := range s.top[a] {
			if k >= 0 && s.occursUnder(k, t) {
				key = k
				break
			}
		}
		if key < 0 {
			continue // t was proposed by one of these keys
		}
		occ := s.occOf(key)
		j := s.lowerBound(occ, 0, t)
		for n := 0; n < perTop && chains < maxChains && j < len(occ) && s.eDir[occ[j]] <= s.end[t]; n++ {
			chains++
			d := s.eDir[occ[j]]
			sc.chain = sc.chain[:0]
			for x := d; ; x = s.parent[x] {
				sc.chain = append(sc.chain, x)
				if x == t {
					break
				}
			}
			lo, hi := 0, len(sc.chain)-1
			for lo < hi {
				mid := int(uint(lo+hi) >> 1)
				if s.memoMatch(a, sc.chain[mid], sc).compare(best) == 0 {
					hi = mid
				} else {
					lo = mid + 1
				}
			}
			if c := sc.chain[lo]; !slices.Contains(sc.ends, c) {
				sc.ends = append(sc.ends, c)
			}
			j = s.lowerBound(occ, j, d+1)
		}
	}
	return s.pickPartner(a, best, sc.ends), best
}

// pickPartner chooses among the folders keeping folder a's best match.
// When a is inside them, a folder holding more than a comes before one
// that is the same as a: a folder kept in two places, both inside a larger
// one, is reported inside the larger folder, not paired with its twin.
// Then the deepest, then the path-first. It returns -1 for no folder.
func (s *Snapshot) pickPartner(a int32, best match, ends []int32) int32 {
	if len(ends) == 0 {
		return -1
	}
	whole := len(ends) > 1 && s.inside(a, best)
	chosen, chosenSame := ends[0], whole && s.coveredBy(ends[0], a)
	for _, c := range ends[1:] {
		cSame := whole && s.coveredBy(c, a)
		if cSame != chosenSame {
			if chosenSame {
				chosen, chosenSame = c, cSame
			}
			continue
		}
		if s.preferDeeper(c, chosen) {
			chosen = c
		}
	}
	return chosen
}

// coveredBy reports whether folder c is inside folder a: no gap, a key,
// and every keyed entry's key occurring under a. It stops at the first
// entry that does not.
func (s *Snapshot) coveredBy(c, a int32) bool {
	t := s.subtree(c)
	if t.gaps != 0 || t.keyed == 0 {
		return false
	}
	lo, hi := s.entries(c)
	if int64(hi-lo) != t.keyed {
		return false // a file of a unique size or sample matches nothing
	}
	for e := lo; e < hi; e++ {
		occ := s.occOf(s.eKey[e])
		if j := s.lowerBound(occ, 0, a); j == len(occ) || s.eDir[occ[j]] > s.end[a] {
			return false
		}
	}
	return true
}

// preferDeeper reports whether folder x beats folder y as a partner of equal
// match: deeper, then path-first (then source-first).
func (s *Snapshot) preferDeeper(x, y int32) bool {
	if s.depth[x] != s.depth[y] {
		return s.depth[x] > s.depth[y]
	}
	return s.comparePaths(x, y) < 0
}

// comparePaths orders two directories by path, then by source.
func (s *Snapshot) comparePaths(x, y int32) int {
	return cmp.Or(bytes.Compare(s.dirPath(x), s.dirPath(y)), cmp.Compare(s.src[x], s.src[y]))
}

// Candidates returns the folder pairs that hashing should check first
// (design D4): on a provisional snapshot of every source, keyed by size
// where content is not known yet, each folder A whose matched bytes under
// its best partner B reach candidatePercent of A's bytes, with A or B on
// src. A pair is listed once, without the pairs nested in a listed one,
// lower side first and in directory order (sources by ID, folders depth
// first by name). q should be a read transaction.
func Candidates(ctx context.Context, q store.Queryer, src domain.SourceID) ([][2]Range, error) {
	s, err := loadProvisional(ctx, q)
	if err != nil {
		return nil, err
	}
	return s.candidates(src), nil
}

func (s *Snapshot) candidates(src domain.SourceID) [][2]Range {
	si, ok := slices.BinarySearch(s.sources, src)
	if !ok {
		return nil
	}
	sc := newRelScratch(len(s.parent))
	var pairs [][2]int32
	for a := range int32(len(s.parent)) {
		t := s.subtree(a)
		if t.bytes == 0 {
			continue
		}
		b, m := s.partner(a, sc)
		if b < 0 || m.bytes*100 < candidatePercent*t.bytes {
			continue
		}
		if s.src[a] != int32(si) && s.src[b] != int32(si) {
			continue
		}
		// Each side by its highest equivalent folder, as Relate names it.
		a, b = s.lift(a), s.lift(b)
		pairs = append(pairs, [2]int32{min(a, b), max(a, b)})
	}
	slices.SortFunc(pairs, func(x, y [2]int32) int { return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1])) })
	pairs = slices.Compact(pairs)
	// Outer pairs first; a pair nested in a kept pair is dropped.
	slices.SortFunc(pairs, func(x, y [2]int32) int {
		return cmp.Or(cmp.Compare(s.depth[x[0]]+s.depth[x[1]], s.depth[y[0]]+s.depth[y[1]]),
			cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1]))
	})
	var kept [][2]int32
	partners := map[int32][]int32{}
	for _, p := range pairs {
		if s.heldPair(p[0], p[1], partners) {
			continue
		}
		kept = append(kept, p)
		partners[p[0]] = append(partners[p[0]], p[1])
		partners[p[1]] = append(partners[p[1]], p[0])
	}
	slices.SortFunc(kept, func(x, y [2]int32) int { return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1])) })
	out := make([][2]Range, 0, len(kept))
	for _, p := range kept {
		ra, rb := s.rangeOf(p[0]), s.rangeOf(p[1])
		if ra.Source == rb.Source && bytes.Equal(ra.From, rb.From) && bytes.Equal(ra.To, rb.To) {
			continue // two folders of one archive
		}
		out = append(out, [2]Range{ra, rb})
	}
	return out
}

// heldPair reports whether an ancestor-or-self of a is paired with an
// ancestor-or-self of b.
func (s *Snapshot) heldPair(a, b int32, partners map[int32][]int32) bool {
	for x := a; x >= 0; x = s.parent[x] {
		for _, y := range partners[x] {
			if s.contains(y, b) {
				return true
			}
		}
	}
	return false
}

// folderResult is a folder relation: a is the stored side a (the contained
// side of inside, the archive or path-later side of same, the side with the
// larger matched share of overlap), m its match under b.
type folderResult struct {
	kind string
	a, b int32
	m    match
}

// result is one relation as the relate job stores it.
type result struct {
	Kind                    string
	A, B                    domain.Ref
	MatchedBytes, Redundant int64
	ABytes, AFiles          int64
	BBytes, BFiles          int64
	AOnlyFiles, AOnlyBytes  int64
	BOnlyFiles, BOnlyBytes  int64
	aDir, bDir              int32
}

// Relate returns the relations of a final snapshot (design D9), ranked by
// redundant bytes, then matched bytes, descending:
//   - each folder with a key relates to its best partner: inside, same
//     (reported once: an archive or a part of one is side a against a
//     folder, otherwise the path-later side), or overlap when the matched
//     share of one side reaches overlapPercent;
//   - one line per copy: a folder pair already held by an emitted pair of
//     ancestors-or-selves is skipped (maximal).
func (s *Snapshot) Relate() []result {
	n := int32(len(s.parent))
	sc := newRelScratch(int(n))

	var found []folderResult
	for a := range n {
		t := s.subtree(a)
		if t.keyed == 0 {
			continue
		}
		b, m := s.partner(a, sc)
		if b < 0 {
			continue
		}
		r := folderResult{a: a, b: b, m: m}
		switch {
		case s.inside(a, m):
			r.kind = KindInside
			if back := s.match(b, a); s.inside(b, back) {
				r.kind = KindSame
				// Either side can go. An archive, or a part of one, is
				// side a against a folder (M4b design D10, the owner's
				// choice); otherwise the path-later side, the path-first
				// one being the reference.
				la, lb := s.lift(a), s.lift(b)
				var swap bool
				if inA, inB := s.arc[la] >= 0, s.arc[lb] >= 0; inA != inB {
					swap = inB
				} else {
					swap = s.comparePaths(la, lb) < 0
				}
				if swap {
					r.a, r.b, r.m = b, a, back
				}
			}
		case t.bytes > 0 && m.bytes*100 >= overlapPercent*t.bytes:
			r.kind = KindOverlap
			// Side a has the larger matched share.
			back := s.match(b, a)
			if tb := s.subtree(b); tb.bytes > 0 && float64(back.bytes)/float64(tb.bytes) > float64(m.bytes)/float64(t.bytes) {
				r.a, r.b, r.m = b, a, back
			}
		default:
			continue
		}
		// Name each side by its highest equivalent folder: a folder whose
		// parents up to it hold nothing else, or the archive a folder holds
		// and nothing else. Both hold the same files, so the counts stand,
		// except that a part of an archive named by the whole archive frees
		// its size.
		c, o := s.lift(r.a), s.lift(r.b)
		if c != r.a && s.arc[r.a] >= 0 {
			r.m = s.match(c, o)
		}
		r.a, r.b = c, o
		found = append(found, r)
	}
	emitted := s.maximal(found)

	out := make([]result, 0, len(emitted))
	for _, r := range emitted {
		ta, tb := s.subtree(r.a), s.subtree(r.b)
		back := s.match(r.b, r.a)
		out = append(out, result{Kind: r.kind, A: s.ref(r.a), B: s.ref(r.b),
			MatchedBytes: r.m.bytes, Redundant: r.m.redundant,
			ABytes: ta.bytes, AFiles: ta.files, BBytes: tb.bytes, BFiles: tb.files,
			AOnlyFiles: ta.files - r.m.files - ta.gapRegular, AOnlyBytes: ta.bytes - r.m.bytes - ta.gapBytes,
			BOnlyFiles: tb.files - back.files - tb.gapRegular, BOnlyBytes: tb.bytes - back.bytes - tb.gapBytes,
			aDir: r.a, bDir: r.b})
	}
	slices.SortFunc(out, func(x, y result) int {
		return cmp.Or(cmp.Compare(y.Redundant, x.Redundant), cmp.Compare(y.MatchedBytes, x.MatchedBytes),
			cmp.Compare(strength(y.Kind), strength(x.Kind)), s.comparePaths(x.aDir, y.aDir), s.comparePaths(x.bDir, y.bDir),
			cmp.Compare(x.Kind, y.Kind))
	})
	return out
}

// maximal keeps one line per copy. Strong relations (inside, same) are
// decided before overlaps, each from the shallowest pair down; candidate
// (C, P), C side a, is skipped when an emitted result (X, Y), X its side a,
// holds it:
//   - X is C or an ancestor of C, and Y is P or an ancestor of P (an
//     ancestor's result names the partner or one of its ancestors). An
//     overlap holds only overlaps: a part of a folder that merely overlaps
//     P can still be entirely inside P, which is the more useful line;
//   - or, for an inside or same result, Y holds C and X holds P: a part of
//     the folder kept in X's favour is not reported as a copy of a part of
//     X; the exact reverse pair (Y = C, X = P) is still reported.
//
// Sides arrive lifted (their highest equivalent folder), so a pair found
// from several equivalent folders, or from both folders of a same or
// overlap pair, is kept once. A same result holds both ways, and a same
// candidate is skipped when held in either orientation.
//
// Strong results also join their two folders into a group, transitively:
// with fotos/2013 same fotos-b and fotos-reorg2 same fotos-b, all three are
// one group. A candidate is held when an ancestor-or-self of each side
// belongs to the same group, so parts of two folders related only through
// a third are not reported one subfolder pair at a time. The overlap of the
// two exact folders of a group is still reported.
func (s *Snapshot) maximal(found []folderResult) []folderResult {
	slices.SortFunc(found, func(x, y folderResult) int {
		return cmp.Or(cmp.Compare(strength(y.kind), strength(x.kind)),
			cmp.Compare(s.depth[x.a]+s.depth[x.b], s.depth[y.a]+s.depth[y.b]),
			cmp.Compare(x.a, y.a), cmp.Compare(x.b, y.b), cmp.Compare(x.kind, y.kind))
	})
	// holds[x] lists the folders y such that a pair (C under x, P under y)
	// is held, and whether a strong relation holds it.
	holds := map[int32][]heldBy{}
	groups := folderGroups{}
	seen := map[[2]int32]bool{}
	var emitted []folderResult
	for _, r := range found {
		strong := strength(r.kind) > 1
		same := r.kind == KindSame
		if seen[[2]int32{r.a, r.b}] || r.kind != KindInside && seen[[2]int32{r.b, r.a}] {
			continue // the same pair found again
		}
		if s.held(r.a, r.b, strong, holds) || same && s.held(r.b, r.a, strong, holds) ||
			s.grouped(r.a, r.b, strong, groups) {
			continue
		}
		emitted = append(emitted, r)
		seen[[2]int32{r.a, r.b}] = true
		holds[r.a] = append(holds[r.a], heldBy{other: r.b, strong: strong})
		if strong {
			holds[r.b] = append(holds[r.b], heldBy{other: r.a, strong: true})
			groups.union(r.a, r.b)
		}
	}
	return emitted
}

// folderGroups is a union-find over the folders of emitted strong results.
type folderGroups map[int32]int32

// find returns d's group representative, or -1 when d is in no group.
func (g folderGroups) find(d int32) int32 {
	p, ok := g[d]
	if !ok {
		return -1
	}
	if p == d {
		return d
	}
	r := g.find(p)
	g[d] = r
	return r
}

func (g folderGroups) union(a, b int32) {
	for _, d := range [2]int32{a, b} {
		if _, ok := g[d]; !ok {
			g[d] = d
		}
	}
	if ra, rb := g.find(a), g.find(b); ra != rb {
		g[rb] = ra
	}
}

// grouped reports whether an ancestor-or-self x of c and a different
// ancestor-or-self y of p belong to one group. For a weak candidate, the
// two exact folders (x = c, y = p) do not count: their overlap is the line
// that says how much of the larger folder the group covers.
func (s *Snapshot) grouped(c, p int32, strong bool, g folderGroups) bool {
	if len(g) == 0 {
		return false
	}
	for x := c; x >= 0; x = s.parent[x] {
		rx := g.find(x)
		if rx < 0 {
			continue
		}
		for y := p; y >= 0; y = s.parent[y] {
			if y == x || g.find(y) != rx || !strong && x == c && y == p {
				continue
			}
			return true
		}
	}
	return false
}

// heldBy is one entry of maximal's holds: the partner folder of an emitted
// result, and whether that result is strong (inside or same).
type heldBy struct {
	other  int32
	strong bool
}

// strength ranks relations for maximal: inside and same before overlap.
func strength(kind string) int {
	if kind == KindOverlap {
		return 1
	}
	return 2
}

// held reports whether an emitted pair holds side c with partner p and is
// not that pair itself. A weak (overlap) pair holds only weak candidates.
// Sides compare by their highest equivalent ancestor (lift), so single-child
// chains neither repeat a pair nor hide its exact reverse.
func (s *Snapshot) held(c, p int32, strong bool, holds map[int32][]heldBy) bool {
	for x := c; x >= 0; x = s.parent[x] {
		for _, h := range holds[x] {
			if strong && !h.strong {
				continue
			}
			if y := s.lift(h.other); s.contains(y, p) && (x != c || y != s.lift(p)) {
				return true
			}
		}
	}
	return false
}

// lift returns d's highest ancestor-or-self holding exactly what d holds:
// the folders above d up to it hold nothing else. It does not leave an
// archive, and a folder holding nothing but one archive lifts to that
// archive (lowered).
func (s *Snapshot) lift(d int32) int32 {
	t := s.subtree(d)
	for p := s.parent[d]; p >= 0 && !s.isArchive(d) && s.subtree(p) == t; p = s.parent[p] {
		d = p
	}
	if z, ok := s.lowered[d]; ok {
		return z
	}
	return d
}

// isArchive reports whether d is an archive's own folder.
func (s *Snapshot) isArchive(d int32) bool { return s.arc[d] >= 0 && s.arcDir[s.arc[d]] == d }
