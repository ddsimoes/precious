package index

import (
	"bytes"

	"precious/internal/domain"
)

// maxInside bounds a folder's dir_stats.inside list (design D21).
const maxInside = 10

// noDominant indexes the inside candidates of a folder without bytes, which
// has no dominant family: nothing in it is notable by family, so only groups
// are listed. The four families index the others, in familyKeys order.
const noDominant = 4

// dominantOrder is the family indexes in tie order: personal, programs,
// disposable, containers (design D21).
var dominantOrder = func() (o [4]int) {
	for i, f := range domain.Families {
		o[i] = familyIndex(f)
	}
	return o
}()

// dominant is the index of the family with the most bytes of a composition
// totalling total bytes, or noDominant when total is 0.
func dominant(comp *[4]counts, total int64) int {
	if total == 0 {
		return noDominant
	}
	best := dominantOrder[0]
	for _, i := range dominantOrder[1:] {
		if comp[i].bytes > comp[best].bytes {
			best = i
		}
	}
	return best
}

// insideRef is one entry of a folder's inside list: a stored entry by ID, or
// one the scan inserted by token.
type insideRef struct {
	id               domain.EntryID
	token            uint64
	path             []byte
	category, family text
	group            bool
	bytes, files     int64
}

// insideBefore reports whether an entry of n bytes at the path dir/name is
// listed before r: more bytes first, then the smaller raw path.
func insideBefore(n int64, dir, name []byte, r *insideRef) bool {
	if n != r.bytes {
		return n > r.bytes
	}
	return comparePath(dir, name, r.path) < 0
}

// comparePath compares the path of name inside the folder at dir with p,
// as bytes.Compare would compare joinPath(dir, name) with p.
func comparePath(dir, name, p []byte) int {
	if len(dir) == 0 {
		return bytes.Compare(name, p)
	}
	n := min(len(dir), len(p))
	if c := bytes.Compare(dir, p[:n]); c != 0 {
		return c // they differ within p, or p is a proper prefix of dir
	}
	p = p[n:]
	if len(p) == 0 {
		return 1
	}
	if p[0] != '/' {
		if '/' < p[0] {
			return -1
		}
		return 1
	}
	return bytes.Compare(name, p[1:])
}

// accepts reports whether list l would take an entry of n bytes at dir/name.
func accepts(l []insideRef, n int64, dir, name []byte) bool {
	return len(l) < maxInside || insideBefore(n, dir, name, &l[len(l)-1])
}

// offer adds r to the list l, largest first, when it is among the
// maxInside first, and holds its token for the list; the entry it evicts is
// dropped.
func (t *tokens) offer(l *[]insideRef, r *insideRef) {
	list := *l
	if len(list) == maxInside {
		if !insideBefore(r.bytes, nil, r.path, &list[maxInside-1]) {
			return
		}
		t.drop(list[maxInside-1].token)
		list = list[:maxInside-1]
	}
	i := len(list)
	for i > 0 && insideBefore(r.bytes, nil, r.path, &list[i-1]) {
		i--
	}
	list = append(list, insideRef{})
	copy(list[i+1:], list[i:])
	list[i] = *r
	*l = list
	t.hold(r.token)
}
