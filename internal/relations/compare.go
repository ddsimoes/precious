package relations

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"precious/internal/domain"
	"precious/internal/store"
)

// Compare (design D11, spec "Two folders can be compared"): the files of
// two sides, each a folder, a complete archive, or a folder inside one,
// split into five buckets, computed per request in one read of q.

// Bucket is one of Compare's five groups.
type Bucket string

const (
	BucketOnlyLeft  Bucket = "only_left"
	BucketOnlyRight Bucket = "only_right"
	BucketIdentical Bucket = "identical"
	BucketDifferent Bucket = "different"
	BucketUnchecked Bucket = "unchecked"
)

// Buckets lists the buckets in summary order.
var Buckets = []Bucket{BucketOnlyLeft, BucketOnlyRight, BucketIdentical, BucketDifferent, BucketUnchecked}

// openingOrder is the order in which Compare without a bucket looks for
// the first bucket holding files (R2 B22): what sets the sides apart
// first, so a pair with nothing apart opens on its identical files.
var openingOrder = []Bucket{BucketOnlyLeft, BucketOnlyRight, BucketDifferent, BucketUnchecked, BucketIdentical}

// Valid reports whether b is one of the five buckets.
func (b Bucket) Valid() bool { return slices.Contains(Buckets, b) }

// Count is a bucket's size: its items, and their bytes (a paired item
// counts its left file's size, or its right file's when it has no left).
type Count struct{ Files, Bytes int64 }

// CompareItem is one item of a bucket: a pair (identical, different) or
// one file. LeftPath and RightPath are each file's path relative to its
// side (wrapper dropped, see Compare), nil for a side without a file; Path
// is LeftPath, else RightPath. An identical item with one file is an extra
// copy: Twin is the file on the other side holding the same content (its
// first in path order), and TwinPath its path in that side. A file is an
// entry, or a member with its archive's entry.
type CompareItem struct {
	Path                []byte
	LeftPath, RightPath []byte
	Left, Right         *domain.Ref
	Twin                *domain.Ref
	TwinPath            []byte
}

// CompareResult is a page of one bucket and the summary of all five.
// Bucket is the bucket listed: the one asked for, else the opening one.
type CompareResult struct {
	Summary    map[Bucket]Count
	Bucket     Bucket
	Items      []CompareItem
	NextCursor string
}

// Paging of Compare's items.
const (
	compareDefaultLimit = 100
	compareMaxLimit     = 1000
)

// Compare compares the files of left and right (design D11):
//   - identical: a content occurring on both sides, paired by path order
//     (an extra copy on one side is an item with one file);
//   - different: the same relative path on both sides, neither identical,
//     with content proven different (other sizes, other digests, or a
//     sampled file);
//   - only on one side: a file in neither of those whose content is proven
//     absent from the other side: its size does not occur there (no read
//     needed), or every file of that size there is checked;
//   - unchecked: any other file: a pending, changed, or unreadable file
//     (or one hashing has not planned yet) whose size occurs on the other
//     side, and a checked file whose size occurs there only among such
//     files.
//
// Only non-empty regular files and file members take part; empty files
// are identical to each other. When exactly one side holds a single top
// folder and nothing else, and dropping it lines up more relative paths
// with the other side, it is dropped (m4b lift; emule-0.47c/ inside the zip
// against the unpacked folder).
//
// A side must be a folder, a complete archive, or a folder inside one;
// another file is 400 invalid_request, and so are two sides of which one
// contains the other. An unknown side is 404 not_found. bucket selects
// the items listed; "" lists the first bucket holding files in the order
// only on the left, only on the right, different, unchecked, identical
// (R2 B22), so opening a comparison computes it once. cursor continues a
// previous page.
func Compare(ctx context.Context, q store.Queryer, left, right domain.Ref, bucket Bucket, cursor string, limit int) (CompareResult, error) {
	if bucket != "" && !bucket.Valid() {
		return CompareResult{}, domain.Errorf(domain.CodeInvalidRequest, "unknown bucket %q", bucket)
	}
	offset := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			return CompareResult{}, domain.Errorf(domain.CodeInvalidRequest, "invalid cursor %q", cursor)
		}
		offset = n
	}
	if limit <= 0 {
		limit = compareDefaultLimit
	}
	limit = min(limit, compareMaxLimit)

	ls, err := resolveSide(ctx, q, left)
	if err != nil {
		return CompareResult{}, err
	}
	rs, err := resolveSide(ctx, q, right)
	if err != nil {
		return CompareResult{}, err
	}
	if ls.contains(rs) || rs.contains(ls) {
		return CompareResult{}, domain.Errorf(domain.CodeInvalidRequest, "%s and %s: one side contains the other", left, right)
	}
	lf, err := ls.files(ctx, q)
	if err != nil {
		return CompareResult{}, err
	}
	rf, err := rs.files(ctx, q)
	if err != nil {
		return CompareResult{}, err
	}
	dropWrapper(lf, rf)

	all := bucketize(lf, rf)
	res := CompareResult{Summary: make(map[Bucket]Count, len(Buckets))}
	for _, b := range Buckets {
		var c Count
		for _, it := range all[b] {
			c.Files++
			c.Bytes += it.bytes
		}
		res.Summary[b] = c
	}
	if bucket == "" {
		bucket = BucketOnlyLeft // two empty sides
		for _, b := range openingOrder {
			if res.Summary[b].Files > 0 {
				bucket = b
				break
			}
		}
	}
	res.Bucket = bucket
	items := all[bucket]
	if offset > len(items) {
		offset = len(items)
	}
	end := min(offset+limit, len(items))
	for _, it := range items[offset:end] {
		res.Items = append(res.Items, it.CompareItem)
	}
	if end < len(items) {
		res.NextCursor = strconv.Itoa(end)
	}
	return res, nil
}

// side is a resolved Compare side.
type side struct {
	ref     domain.Ref
	source  domain.SourceID
	path    []byte // the folder's path, or the archive file's
	archive int64  // the archive's entry, else 0
	member  []byte // a member folder's path inside its archive, else nil
}

func resolveSide(ctx context.Context, q store.Queryer, ref domain.Ref) (side, error) {
	s := side{ref: ref}
	if ref.Member != 0 {
		var arc int64
		var kind, astate, estate string
		err := q.QueryRowContext(ctx, `SELECT m.archive_id, m.path, m.kind, a.state, e.source_id, e.path, e.state
			FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = m.archive_id
			WHERE m.id = ?`, int64(ref.Member)).Scan(&arc, &s.member, &kind, &astate, &s.source, &s.path, &estate)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (ref.Entry != 0 && int64(ref.Entry) != arc ||
			astate != string(domain.ArchiveComplete) || estate == "missing") {
			return side{}, domain.Errorf(domain.CodeNotFound, "%s not found", ref)
		}
		if err != nil {
			return side{}, fmt.Errorf("relations: compare side %s: %w", ref, err)
		}
		if kind != string(domain.MemberDirectory) {
			return side{}, domain.Errorf(domain.CodeInvalidRequest, "%s is not a folder", ref)
		}
		s.archive = arc
		return s, nil
	}
	var kind, estate string
	var astate sql.NullString
	err := q.QueryRowContext(ctx, `SELECT e.source_id, e.path, e.kind, e.state, a.state
		FROM entries e LEFT JOIN archives a ON a.entry_id = e.id WHERE e.id = ?`, int64(ref.Entry)).
		Scan(&s.source, &s.path, &kind, &estate, &astate)
	if errors.Is(err, sql.ErrNoRows) || err == nil && estate == "missing" {
		return side{}, domain.Errorf(domain.CodeNotFound, "%s not found", ref)
	}
	if err != nil {
		return side{}, fmt.Errorf("relations: compare side %s: %w", ref, err)
	}
	switch {
	case kind == string(domain.EntryDirectory):
	case kind == string(domain.EntryFile) && astate.String == string(domain.ArchiveComplete):
		s.archive = int64(ref.Entry)
	default:
		return side{}, domain.Errorf(domain.CodeInvalidRequest, "%s is a file, not a folder or an opened archive", ref)
	}
	return s, nil
}

// under reports whether path p is d or lies below it ('/'-joined; the
// empty path is the root).
func underPath(p, d []byte) bool {
	return len(d) == 0 || bytes.Equal(p, d) || len(p) > len(d) && bytes.HasPrefix(p, d) && p[len(d)] == '/'
}

// contains reports whether side s contains side o or is it.
func (s side) contains(o side) bool {
	if s.source != o.source {
		return false
	}
	switch {
	case s.archive == 0:
		// A folder holds o when o's folder or archive file lies below it.
		return underPath(o.path, s.path)
	case o.archive != s.archive:
		return false
	case s.member == nil:
		return true // the whole archive
	default:
		return o.member != nil && underPath(o.member, s.member)
	}
}

// cfile is one file of a Compare side.
type cfile struct {
	rel     []byte
	size    int64
	state   domain.ContentState // "" when hashing has no row for it
	content int64
	ref     domain.Ref
}

// known reports whether the file's content is known: hashed, or unique
// (unique_size, sampled); empty files are known too.
func (f *cfile) known() bool {
	switch f.state {
	case domain.ContentHashed, domain.ContentUniqueSize, domain.ContentSampled:
		return true
	}
	return f.size == 0
}

// key is a content key comparable across sides: the content, or "empty";
// 0 when unknown or unique.
func (f *cfile) key() int64 {
	if f.size == 0 {
		return -1
	}
	if f.state == domain.ContentHashed {
		return f.content
	}
	return 0
}

func (s side) files(ctx context.Context, q store.Queryer) ([]cfile, error) {
	var out []cfile
	var err error
	if s.archive == 0 {
		from, to := descendants(s.source, s.path).From, descendants(s.source, s.path).To
		query := `SELECT e.id, e.path, e.size, COALESCE(fc.state, ''), COALESCE(fc.content_id, 0)
			FROM entries e LEFT JOIN file_content fc ON fc.entry_id = e.id
			WHERE e.source_id = ? AND e.path >= ? AND e.kind = 'file' AND e.state = 'present'`
		args := []any{string(s.source), from}
		if to != nil {
			query += ` AND e.path < ?`
			args = append(args, to)
		}
		cut := len(from)
		if len(s.path) == 0 {
			cut = 0
		}
		err = scanAll(ctx, q, query, args, func(r *sql.Rows) error {
			var f cfile
			var id int64
			var p []byte
			if err := r.Scan(&id, &p, &f.size, &f.state, &f.content); err != nil {
				return err
			}
			f.rel, f.ref = p[cut:], domain.Ref{Entry: domain.EntryID(id)}
			out = append(out, f)
			return nil
		})
	} else {
		out, err = s.memberFiles(ctx, q)
	}
	if err != nil {
		return nil, fmt.Errorf("relations: compare files of %s: %w", s.ref, err)
	}
	return out, nil
}

// memberFiles reads the file members of an archive side; a tar hard link
// has its target's state and content.
func (s side) memberFiles(ctx context.Context, q store.Queryer) ([]cfile, error) {
	query := `SELECT id, path, size, COALESCE(state, ''), COALESCE(content_id, 0), COALESCE(link_member, 0)
		FROM archive_members WHERE archive_id = ? AND kind = 'file'`
	args := []any{s.archive}
	cut := 0
	if s.member != nil {
		r := descendants("", s.member)
		query += ` AND path >= ? AND path < ?`
		args = append(args, r.From, r.To)
		cut = len(r.From)
	}
	var out []cfile
	links := map[int]int64{} // index in out -> link target
	err := scanAll(ctx, q, query, args, func(r *sql.Rows) error {
		var f cfile
		var id, link int64
		var p []byte
		if err := r.Scan(&id, &p, &f.size, &f.state, &f.content, &link); err != nil {
			return err
		}
		f.rel, f.ref = p[cut:], domain.Ref{Entry: domain.EntryID(s.archive), Member: domain.MemberID(id)}
		if link != 0 {
			links[len(out)] = link
		}
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, target := range links {
		f := &out[i]
		for steps := 0; target != 0 && steps < 64; steps++ {
			var next int64
			err := q.QueryRowContext(ctx, `SELECT size, COALESCE(state, ''), COALESCE(content_id, 0), COALESCE(link_member, 0)
				FROM archive_members WHERE id = ?`, target).Scan(&f.size, &f.state, &f.content, &next)
			if errors.Is(err, sql.ErrNoRows) {
				f.state, f.content = "", 0 // a link to nothing: unknown
				break
			}
			if err != nil {
				return nil, err
			}
			target = next
		}
	}
	return out, nil
}

// dropWrapper drops the single top folder of one side (see Compare).
func dropWrapper(l, r []cfile) {
	lt, lok := topFolder(l)
	rt, rok := topFolder(r)
	switch {
	case lok == rok:
	case lok && aligned(l, r, len(lt)+1) > aligned(l, r, 0):
		strip(l, len(lt)+1)
	case rok && aligned(r, l, len(rt)+1) > aligned(r, l, 0):
		strip(r, len(rt)+1)
	}
}

// topFolder returns the single top folder all of fs lie in.
func topFolder(fs []cfile) ([]byte, bool) {
	var top []byte
	for i, f := range fs {
		j := bytes.IndexByte(f.rel, '/')
		if j < 0 {
			return nil, false
		}
		if i == 0 {
			top = f.rel[:j]
		} else if !bytes.Equal(f.rel[:j], top) {
			return nil, false
		}
	}
	return top, len(fs) > 0
}

// aligned counts the files of x whose relative path, cut bytes dropped,
// is a relative path of y.
func aligned(x, y []cfile, cut int) int {
	paths := make(map[string]bool, len(y))
	for _, f := range y {
		paths[string(f.rel)] = true
	}
	n := 0
	for _, f := range x {
		if paths[string(f.rel[cut:])] {
			n++
		}
	}
	return n
}

func strip(fs []cfile, cut int) {
	for i := range fs {
		fs[i].rel = fs[i].rel[cut:]
	}
}

// bucketItem is an item with its counted bytes.
type bucketItem struct {
	CompareItem
	bytes int64
}

// bucketize splits the files of both sides into the buckets, each sorted
// by path (then right path).
func bucketize(l, r []cfile) map[Bucket][]bucketItem {
	byPath := func(fs []cfile) {
		slices.SortFunc(fs, func(x, y cfile) int { return bytes.Compare(x.rel, y.rel) })
	}
	byPath(l)
	byPath(r)
	out := map[Bucket][]bucketItem{}
	usedL, usedR := make([]bool, len(l)), make([]bool, len(r))
	pair := func(b Bucket, i, j int) *bucketItem {
		var it bucketItem
		if i >= 0 {
			usedL[i] = true
			it.Path, it.LeftPath, it.Left, it.bytes = l[i].rel, l[i].rel, &l[i].ref, l[i].size
		}
		if j >= 0 {
			usedR[j] = true
			it.RightPath, it.Right = r[j].rel, &r[j].ref
			if i < 0 {
				it.Path, it.bytes = r[j].rel, r[j].size
			}
		}
		out[b] = append(out[b], it)
		return &out[b][len(out[b])-1]
	}

	// Identical: contents on both sides, paired in path order; an extra
	// copy names the other side's first file of its content as its twin.
	keysL, keysR := map[int64][]int{}, map[int64][]int{}
	for i := range l {
		if k := l[i].key(); k != 0 {
			keysL[k] = append(keysL[k], i)
		}
	}
	for j := range r {
		if k := r[j].key(); k != 0 {
			keysR[k] = append(keysR[k], j)
		}
	}
	for k, li := range keysL {
		ri, ok := keysR[k]
		if !ok {
			continue
		}
		for n := range max(len(li), len(ri)) {
			i, j := -1, -1
			if n < len(li) {
				i = li[n]
			}
			if n < len(ri) {
				j = ri[n]
			}
			it := pair(BucketIdentical, i, j)
			switch {
			case i < 0:
				it.Twin, it.TwinPath = &l[li[0]].ref, l[li[0]].rel
			case j < 0:
				it.Twin, it.TwinPath = &r[ri[0]].ref, r[ri[0]].rel
			}
		}
	}

	// Different: one path, neither identical, content proven different.
	pathR := make(map[string]int, len(r))
	for j := range r {
		pathR[string(r[j].rel)] = j
	}
	for i := range l {
		j, ok := pathR[string(l[i].rel)]
		if !ok || usedL[i] || usedR[j] {
			continue
		}
		if a, b := &l[i], &r[j]; a.size != b.size || a.known() && b.known() {
			pair(BucketDifferent, i, j)
		}
	}

	// The rest: only when proven by size or by checked files, else
	// unchecked.
	type sizes struct{ all, gaps int }
	count := func(fs []cfile) map[int64]sizes {
		m := map[int64]sizes{}
		for i := range fs {
			s := m[fs[i].size]
			s.all++
			if !fs[i].known() {
				s.gaps++
			}
			m[fs[i].size] = s
		}
		return m
	}
	sizesL, sizesR := count(l), count(r)
	rest := func(fs []cfile, used []bool, other map[int64]sizes, only Bucket, isLeft bool) {
		for i := range fs {
			if used[i] {
				continue
			}
			f := &fs[i]
			o := other[f.size]
			b := only
			if f.known() && o.gaps > 0 || !f.known() && o.all > 0 {
				b = BucketUnchecked
			}
			if isLeft {
				pair(b, i, -1)
			} else {
				pair(b, -1, i)
			}
		}
	}
	rest(l, usedL, sizesR, BucketOnlyLeft, true)
	rest(r, usedR, sizesL, BucketOnlyRight, false)

	for _, items := range out {
		slices.SortFunc(items, func(x, y bucketItem) int {
			return cmp.Or(bytes.Compare(x.Path, y.Path), compareRefs(x.Left, y.Left), compareRefs(x.Right, y.Right))
		})
	}
	return out
}

func compareRefs(x, y *domain.Ref) int {
	switch {
	case x == nil || y == nil:
		return cmp.Compare(boolInt(x != nil), boolInt(y != nil))
	default:
		return cmp.Or(cmp.Compare(x.Entry, y.Entry), cmp.Compare(x.Member, y.Member))
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
