package relations

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"

	"precious/internal/domain"
	"precious/internal/store"
)

// The snapshot (design D9): the present entries of every source and the
// members of complete archives, reduced to compact arrays for relating.
//
// Every non-empty regular file, file member, and symlink has a key, is
// unique, or is a gap:
//   - hashed: its content;
//   - unique_size or sampled: unique, which never matches, except that the
//     names of one multiply-linked file (same dev and ino on a source with
//     stable identity) match each other by inode and free nothing;
//   - pending, changed, unreadable, no content row yet, or a special file:
//     a gap;
//   - a symlink: its link text.
//
// In a provisional snapshot (Candidates, design D4) pending files and
// members, and hashed ones below largeFileBytes, are keyed by their size
// instead. Empty files are ignored, and an unreadable folder or a mount
// boundary is a gap of every folder holding it.
//
// A complete archive is no file entry: it is spliced into the tree as a
// folder named like the archive file, under the archive's directory after
// that directory's own subdirectories, holding its member folders (children
// by raw name). A tar hard-link member has its target's key and is one
// inode with it. Members count at their unpacked size and free nothing on
// their own: an archive frees its file's (packed) size when every member's
// key occurs under the partner as another inode.
//
// Folder A is inside folder B (neither A nor its ancestor or descendant)
// when A has no gap, holds a key, and every key under A occurs under B. Each
// key keeps the sorted directory indices of its occurrences, so "key under
// B" is a binary search in B's subtree range.

type keyKind uint8

const (
	keyContent keyKind = iota + 1
	keyLink
	keySize
	// keyInode keys the names of one multiply-linked file whose content is
	// unknown because no other file shares its size or samples.
	keyInode
)

// Raw keys carry their kind in the top three bits.
const (
	rawShift = 61
	rawMask  = 1<<rawShift - 1
)

// dirSums are per-directory totals; Snapshot keeps their prefix sums over
// pre order, so a subtree's totals are one subtraction.
type dirSums struct {
	bytes, files       int64 // non-empty regular files and file members
	gapFiles, gapBytes int64 // gap entries (a directory gap adds none); bytes of regular ones
	gapRegular         int64 // regular gap files and file members
	gaps               int64 // gap entries and gap directories
	keyed              int64 // entries with a key, and unique files
}

func (a dirSums) minus(b dirSums) dirSums {
	return dirSums{a.bytes - b.bytes, a.files - b.files, a.gapFiles - b.gapFiles, a.gapBytes - b.gapBytes,
		a.gapRegular - b.gapRegular, a.gaps - b.gaps, a.keyed - b.keyed}
}

func (a dirSums) plus(b dirSums) dirSums {
	return dirSums{a.bytes + b.bytes, a.files + b.files, a.gapFiles + b.gapFiles, a.gapBytes + b.gapBytes,
		a.gapRegular + b.gapRegular, a.gaps + b.gaps, a.keyed + b.keyed}
}

// Snapshot is the index reduced to compact arrays for relating (about 50
// bytes per matchable file). Directories are indexed in pre order, so a
// subtree is the index range d..end[d]; entries (files, members, and
// symlinks with a key) are indexed in directory order.
type Snapshot struct {
	provisional bool

	sources []domain.SourceID
	parent  []int32 // -1 for a source root
	depth   []int32 // components of the path; an archive's folders continue its file's path
	end     []int32
	id      []int64 // an entry directory's id, the archive file's for an archive, else the member's
	src     []int32 // index into sources
	arc     []int32 // the archive d is or lies in (an index of arcDir), else -1
	pathAt  []int   // directory d's path is paths[pathAt[d]:pathAt[d+1]]
	paths   []byte
	sums    []dirSums // prefix sums: the subtree of d is sums[end[d]+1] - sums[d]
	first   []int32   // directory d's own entries are first[d]..first[d+1]-1
	top     [][3]int32

	arcDir  []int32 // archive a's folder, ascending
	arcSize []int64 // archive a's file size (packed)
	// lowered maps each folder that holds nothing but one archive (directly
	// or through folders holding nothing else) to that archive's folder.
	lowered map[int32]int32

	eDir  []int32
	eKey  []int32
	eSize []int64
	eIno  []int32 // inode group among content entries, else -1

	kAt    []int32 // key k's occurrences are occ[kAt[k]:kAt[k+1]], by directory
	occ    []int32
	kKind  []keyKind
	kBytes []int64

	// dups holds the inputs of the folder duplication figures (final
	// snapshots only).
	dups *dupInputs
}

// dupInputs are what dir_dups needs beyond the relation arrays (design
// D10): each entry directory's own candidate and checked bytes, its
// regular content files (keyed entries outside archives), and the complete
// archives' own file contents, which are copies too (design D8).
type dupInputs struct {
	own      []dupOwn // per directory: its own files' candidate and checked bytes
	arcFiles []arcFile
	// copies counts each content key's physical copies: inode groups
	// among its occurrences plus complete archive files with that content.
	copies []int32
	// arcExtra counts, per content of a complete archive file, those files
	// (one per physical identity); arcKeys maps those contents to their
	// keys, -1 when no entry or member has the content.
	arcExtra map[int64]int32
	arcKeys  map[int64]int32
}

type dupOwn struct{ candidate, checked int64 }

// arcFile is a complete archive's file entry: a regular file of its
// directory, with its content when hashed (0 otherwise).
type arcFile struct {
	dir           int32
	size, content int64
	phys          [2]int64
}

// LoadSnapshot reads the final snapshot of every source in one read of q
// (design D9); q should be a read transaction.
func LoadSnapshot(ctx context.Context, q store.Queryer) (*Snapshot, error) {
	s, err := loadSnapshot(ctx, q, false)
	if err != nil {
		return nil, fmt.Errorf("relations: snapshot: %w", err)
	}
	return s, nil
}

// loadProvisional reads the provisional snapshot of Candidates (design D4).
func loadProvisional(ctx context.Context, q store.Queryer) (*Snapshot, error) {
	s, err := loadSnapshot(ctx, q, true)
	if err != nil {
		return nil, fmt.Errorf("relations: provisional snapshot: %w", err)
	}
	return s, nil
}

// dirRow is one directory entry while the snapshot loads.
type dirRow struct {
	id, parent int64 // parent 0: a source root
	src        int32
	path       []byte
	gap        bool
}

func (r *dirRow) name() []byte {
	if i := bytes.LastIndexByte(r.path, '/'); i >= 0 {
		return r.path[i+1:]
	}
	return r.path
}

// archiveRow is one complete archive while the snapshot loads.
type archiveRow struct {
	entry, parent int64
	path          []byte
	size          int64
	phys          [2]int64 // its file's physical identity: (dev, ino) when linkable, else (-1, entry)
	state         string   // its file's content state, "" without a row
	content       int64
	members       []memberRow
	folders       []arcFolder
	keyed         []record // its members' keyed records, dir = folder index until spliced
}

// memberRow is one archive_members row while its archive loads.
type memberRow struct {
	id, parent, link, content, size int64 // parent 0: the top level
	name, linkText                  []byte
	kind                            domain.MemberKind
	state                           string
}

type arcFolder struct {
	parent int32 // folder index; -1 for the archive itself
	id     int64 // member id; the archive's entry for the archive itself
	name   []byte
	sums   dirSums
}

// record is a keyed entry before the entries are put in directory order.
type record struct {
	dir  int32
	raw  uint64
	size int64
	// phys places a content entry in an inode group: a linkable file's
	// (dev, ino, 0); a member's archive identity and its ordinal (from 1).
	// phys[0] == 0 && phys[1] == 0: in no group.
	phys [3]int64
}

// builder turns the index rows into a Snapshot.
type builder struct {
	s      *Snapshot
	stable []bool
	dirs   []dirRow
	index  map[int64]int32 // directory entry id -> directory index
	arcs   []archiveRow
	arcAt  map[int64]int32 // archive entry id -> index in arcs
	direct []dirSums
	recs   []record
	links  map[string]uint64
	inodes map[[2]int64]uint64
}

func loadSnapshot(ctx context.Context, q store.Queryer, provisional bool) (*Snapshot, error) {
	b := &builder{s: &Snapshot{provisional: provisional, pathAt: []int{0}}, index: map[int64]int32{},
		arcAt: map[int64]int32{}, links: map[string]uint64{}, inodes: map[[2]int64]uint64{}}
	if !provisional {
		b.s.dups = &dupInputs{}
	}
	if err := b.loadSources(ctx, q); err != nil {
		return nil, err
	}
	if err := b.loadDirs(ctx, q); err != nil {
		return nil, err
	}
	if err := b.loadArchives(ctx, q); err != nil {
		return nil, err
	}
	b.order()
	if err := b.loadFiles(ctx, q); err != nil {
		return nil, err
	}
	return b.finish(), nil
}

func scanAll(ctx context.Context, q store.Queryer, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (b *builder) loadSources(ctx context.Context, q store.Queryer) error {
	return scanAll(ctx, q, `SELECT id, COALESCE(json_extract(capabilities, '$.stable_identity'), 0) FROM sources ORDER BY id`,
		nil, func(r *sql.Rows) error {
			var id domain.SourceID
			var stable bool
			if err := r.Scan(&id, &stable); err != nil {
				return err
			}
			b.s.sources = append(b.s.sources, id)
			b.stable = append(b.stable, stable)
			return nil
		})
}

func (b *builder) sourceIndex(id domain.SourceID) (int32, bool) {
	i, ok := slices.BinarySearch(b.s.sources, id)
	return int32(i), ok
}

// loadDirs reads the folders that are not missing. The quarantine folder of
// each source, and every folder below it, is left out (r4 design D2), so
// whatever lies there falls away as below a missing folder.
func (b *builder) loadDirs(ctx context.Context, q store.Queryer) error {
	return scanAll(ctx, q, `SELECT id, COALESCE(parent_id, 0), source_id, path, state <> 'present' OR mount_boundary <> 0
		FROM entries e WHERE kind = 'directory' AND state <> 'missing' AND `+notQuarantinedE, nil, func(r *sql.Rows) error {
		var d dirRow
		var src domain.SourceID
		if err := r.Scan(&d.id, &d.parent, &src, &d.path, &d.gap); err != nil {
			return err
		}
		i, ok := b.sourceIndex(src)
		if !ok {
			return fmt.Errorf("directory %d of unknown source %q", d.id, src)
		}
		d.src = i
		b.dirs = append(b.dirs, d)
		return nil
	})
}

// physOf is a file's physical identity: (dev, ino) when it may share it
// with another name (more than one link on a source with stable identity),
// else (-1, its entry id).
func (b *builder) physOf(src int32, entry int64, dev, ino, nlink sql.NullInt64) [2]int64 {
	if b.stable[src] && nlink.Int64 > 1 && dev.Valid && ino.Valid {
		return [2]int64{dev.Int64, ino.Int64}
	}
	return [2]int64{-1, entry}
}

// loadArchives reads the complete archives of present files outside the
// quarantine and their members (archive_members_children order), and
// orders each one's folders depth first.
func (b *builder) loadArchives(ctx context.Context, q store.Queryer) error {
	err := scanAll(ctx, q, `SELECT a.entry_id, e.parent_id, e.source_id, e.path, e.size, e.dev, e.ino, e.nlink,
			COALESCE(fc.state, ''), COALESCE(fc.content_id, 0)
		FROM archives a JOIN entries e ON e.id = a.entry_id LEFT JOIN file_content fc ON fc.entry_id = a.entry_id
		WHERE a.state = 'complete' AND e.state = 'present' AND e.parent_id IS NOT NULL AND `+notQuarantinedE+`
		ORDER BY a.entry_id`,
		nil, func(r *sql.Rows) error {
			var a archiveRow
			var src domain.SourceID
			var dev, ino, nlink sql.NullInt64
			if err := r.Scan(&a.entry, &a.parent, &src, &a.path, &a.size, &dev, &ino, &nlink, &a.state, &a.content); err != nil {
				return err
			}
			i, ok := b.sourceIndex(src)
			if !ok {
				return fmt.Errorf("archive %d of unknown source %q", a.entry, src)
			}
			a.phys = b.physOf(i, a.entry, dev, ino, nlink)
			b.arcAt[a.entry] = int32(len(b.arcs))
			b.arcs = append(b.arcs, a)
			return nil
		})
	if err != nil || len(b.arcs) == 0 {
		return err
	}
	err = scanAll(ctx, q, `SELECT m.archive_id, m.id, COALESCE(m.parent_id, 0), m.name, m.kind, m.size, m.link_text,
			COALESCE(m.link_member, 0), COALESCE(m.state, ''), COALESCE(m.content_id, 0)
		FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id
		WHERE a.state = 'complete' ORDER BY m.archive_id, m.parent_id, m.name`, nil, func(r *sql.Rows) error {
		var arc int64
		var m memberRow
		if err := r.Scan(&arc, &m.id, &m.parent, &m.name, &m.kind, &m.size, &m.linkText, &m.link, &m.state, &m.content); err != nil {
			return err
		}
		if i, ok := b.arcAt[arc]; ok {
			b.arcs[i].members = append(b.arcs[i].members, m)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := range b.arcs {
		if err := b.keyMembers(&b.arcs[i]); err != nil {
			return fmt.Errorf("members of archive %d: %w", b.arcs[i].entry, err)
		}
		b.arcs[i].members = nil
	}
	return nil
}

// keyMembers orders archive a's folders depth first (children by raw
// name) and keys its members.
func (b *builder) keyMembers(a *archiveRow) error {
	rows := a.members
	byID := make(map[int64]int32, len(rows))
	children := map[int64][2]int32{} // parent id -> its children's rows, by name
	for i := 0; i < len(rows); {
		j := i + 1
		for j < len(rows) && rows[j].parent == rows[i].parent {
			j++
		}
		children[rows[i].parent] = [2]int32{int32(i), int32(j)}
		for k := i; k < j; k++ {
			byID[rows[k].id] = int32(k)
		}
		i = j
	}
	name := a.path
	if i := bytes.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	type frame struct {
		parent int32 // folder index
		row    int32 // -1: the archive itself
	}
	type placed struct{ row, folder int32 }
	var order []placed
	stack := []frame{{parent: -1, row: -1}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		folder := int32(len(a.folders))
		parentID, fname, id := int64(0), name, a.entry
		if f.row >= 0 {
			parentID, fname, id = rows[f.row].id, rows[f.row].name, rows[f.row].id
		}
		a.folders = append(a.folders, arcFolder{parent: f.parent, id: id, name: fname})
		run := children[parentID]
		for i := run[0]; i < run[1]; i++ {
			if rows[i].kind != domain.MemberDirectory {
				order = append(order, placed{row: i, folder: folder})
			}
		}
		for i := run[1] - 1; i >= run[0]; i-- {
			if rows[i].kind == domain.MemberDirectory {
				stack = append(stack, frame{parent: folder, row: i})
			}
		}
	}
	if got := len(a.folders) - 1 + len(order); got != len(rows) {
		return fmt.Errorf("%d of %d members are not below the top level", len(rows)-got, len(rows))
	}

	for _, p := range order {
		own := &a.folders[p.folder].sums
		// A hard link has its target's key; a chain is followed to its end.
		t := p.row
		for steps := 0; t >= 0 && rows[t].link != 0 && steps <= len(rows); steps++ {
			next, ok := byID[rows[t].link]
			if !ok {
				next = -1
			}
			t = next
		}
		if t >= 0 && rows[t].link != 0 {
			t = -1 // a cycle
		}
		var m *memberRow
		if t >= 0 {
			m = &rows[t]
		}
		switch {
		case m == nil || m.kind == domain.MemberSpecial || m.kind == domain.MemberDirectory:
			// A gap: a special member, or a hard link to a folder or to
			// no member.
			own.gaps++
			own.gapFiles++
			if rows[p.row].kind == domain.MemberFile && rows[p.row].size > 0 {
				own.bytes += rows[p.row].size
				own.files++
				own.gapBytes += rows[p.row].size
				own.gapRegular++
			}
		case m.kind == domain.MemberSymlink:
			if m.linkText == nil {
				own.gaps++
				own.gapFiles++
				continue
			}
			own.keyed++
			a.keyed = append(a.keyed, record{dir: p.folder, raw: uint64(keyLink)<<rawShift | b.linkKey(m.linkText)})
		case m.size == 0:
			// An empty member holds no content.
		default:
			own.bytes += m.size
			own.files++
			raw, keyed, gap := b.memberKey(m)
			switch {
			case gap:
				own.gaps++
				own.gapFiles++
				own.gapBytes += m.size
				own.gapRegular++
			case !keyed:
				own.keyed++ // unique: never matches
			default:
				own.keyed++
				r := record{dir: p.folder, raw: raw, size: m.size}
				if raw>>rawShift == uint64(keyContent) {
					r.phys = [3]int64{a.phys[0], a.phys[1], int64(t) + 1}
				}
				a.keyed = append(a.keyed, r)
			}
		}
	}
	return nil
}

// memberKey keys a non-empty file member by its state.
func (b *builder) memberKey(m *memberRow) (raw uint64, keyed, gap bool) {
	switch domain.ContentState(m.state) {
	case domain.ContentHashed:
		if m.content == 0 {
			return 0, false, true
		}
		if b.s.provisional && m.size < largeFileBytes {
			return sizeKey(m.size), true, false
		}
		return uint64(keyContent)<<rawShift | uint64(m.content)&rawMask, true, false
	case domain.ContentUniqueSize, domain.ContentSampled:
		return 0, false, false
	case domain.ContentPending:
		if b.s.provisional {
			return sizeKey(m.size), true, false
		}
	}
	return 0, false, true
}

func sizeKey(size int64) uint64 { return uint64(keySize)<<rawShift | uint64(size)&rawMask }

func (b *builder) linkKey(text []byte) uint64 {
	id, ok := b.links[string(text)]
	if !ok {
		id = uint64(len(b.links))
		b.links[string(text)] = id
	}
	return id
}

// order numbers the directories in pre order: each source root by source,
// children by raw name, and each directory's complete archives (by name)
// after its own subdirectories, each with its member folders depth first.
func (b *builder) order() {
	s := b.s
	slices.SortFunc(b.dirs, func(x, y dirRow) int {
		return cmp.Or(cmp.Compare(x.parent, y.parent), bytes.Compare(x.name(), y.name()), cmp.Compare(x.src, y.src))
	})
	children := make(map[int64][2]int32, len(b.dirs)/2+1)
	for i := 0; i < len(b.dirs); {
		j := i + 1
		for j < len(b.dirs) && b.dirs[j].parent == b.dirs[i].parent {
			j++
		}
		children[b.dirs[i].parent] = [2]int32{int32(i), int32(j)}
		i = j
	}
	slices.SortFunc(b.arcs, func(x, y archiveRow) int {
		return cmp.Or(cmp.Compare(x.parent, y.parent), bytes.Compare(x.path, y.path))
	})
	arcsOf := map[int64][2]int32{}
	for i := 0; i < len(b.arcs); {
		j := i + 1
		for j < len(b.arcs) && b.arcs[j].parent == b.arcs[i].parent {
			j++
		}
		arcsOf[b.arcs[i].parent] = [2]int32{int32(i), int32(j)}
		i = j
	}
	for i, a := range b.arcs {
		b.arcAt[a.entry] = int32(i)
	}

	n := len(b.dirs)
	s.parent = make([]int32, 0, n)
	s.depth = make([]int32, 0, n)
	s.id = make([]int64, 0, n)
	s.src = make([]int32, 0, n)
	s.arc = make([]int32, 0, n)
	b.direct = make([]dirSums, 0, n)

	add := func(parent int32, id int64, src, arc int32, path []byte, own dirSums) int32 {
		d := int32(len(s.parent))
		depth := int32(0)
		if parent >= 0 {
			depth = s.depth[parent] + 1
		} else if len(path) > 0 {
			depth = int32(bytes.Count(path, []byte{'/'})) + 1
		}
		s.parent = append(s.parent, parent)
		s.depth = append(s.depth, depth)
		s.id = append(s.id, id)
		s.src = append(s.src, src)
		s.arc = append(s.arc, arc)
		s.paths = append(s.paths, path...)
		s.pathAt = append(s.pathAt, len(s.paths))
		b.direct = append(b.direct, own)
		return d
	}
	type frame struct {
		row    int32 // a dirs index
		parent int32
		done   bool // its subdirectories are visited: splice its archives
		d      int32
	}
	var stack []frame
	roots := children[0]
	for i := roots[1] - 1; i >= roots[0]; i-- {
		stack = append(stack, frame{row: i, parent: -1})
	}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		r := &b.dirs[f.row]
		if f.done {
			b.splice(f.d, arcsOf[r.id], r.src)
			continue
		}
		var own dirSums
		if r.gap {
			own.gaps = 1
		}
		d := add(f.parent, r.id, r.src, -1, r.path, own)
		b.index[r.id] = d
		stack = append(stack, frame{row: f.row, done: true, d: d})
		run := children[r.id]
		for i := run[1] - 1; i >= run[0]; i-- {
			stack = append(stack, frame{row: i, parent: d})
		}
	}
	b.dirs = nil
}

// splice adds the folders of directory d's archives arcs[run[0]:run[1]],
// each with its member folders depth first, and queues their keyed members.
func (b *builder) splice(d int32, run [2]int32, src int32) {
	s := b.s
	for i := run[0]; i < run[1]; i++ {
		a := &b.arcs[i]
		ord, base := int32(len(s.arcDir)), int32(len(s.parent))
		for fi, f := range a.folders {
			parent := d
			if fi > 0 {
				parent = base + f.parent
			}
			pp := s.dirPath(parent)
			if fi == 0 {
				s.paths = append(s.paths, a.path...)
			} else {
				s.paths = append(append(s.paths, pp...), '/')
				s.paths = append(s.paths, f.name...)
			}
			s.parent = append(s.parent, parent)
			s.depth = append(s.depth, s.depth[parent]+1)
			s.id = append(s.id, f.id)
			s.src = append(s.src, src)
			s.arc = append(s.arc, ord)
			s.pathAt = append(s.pathAt, len(s.paths))
			b.direct = append(b.direct, f.sums)
		}
		s.arcDir = append(s.arcDir, base)
		s.arcSize = append(s.arcSize, a.size)
		for _, r := range a.keyed {
			r.dir += base
			b.recs = append(b.recs, r)
		}
		if dd := s.dups; dd != nil {
			c := int64(0)
			if domain.ContentState(a.state) == domain.ContentHashed {
				c = a.content
			}
			dd.arcFiles = append(dd.arcFiles, arcFile{dir: d, size: a.size, content: c, phys: a.phys})
		}
		a.folders, a.keyed = nil, nil
	}
}

// loadFiles reads the present non-directory entries outside the quarantine
// and keys them.
func (b *builder) loadFiles(ctx context.Context, q store.Queryer) error {
	s := b.s
	if s.dups != nil {
		s.dups.own = make([]dupOwn, len(s.parent))
	}
	return scanAll(ctx, q, `SELECT e.id, e.parent_id, e.kind, e.size, e.dev, e.ino, e.nlink, e.link_text,
			COALESCE(fc.state, ''), COALESCE(fc.content_id, 0)
		FROM entries e LEFT JOIN file_content fc ON fc.entry_id = e.id
		WHERE e.kind <> 'directory' AND e.state = 'present' AND e.parent_id IS NOT NULL
			AND `+notQuarantinedE, nil, func(r *sql.Rows) error {
		var id, parent, size, content int64
		var kind, state string
		var dev, ino, nlink sql.NullInt64
		var linkText []byte
		if err := r.Scan(&id, &parent, &kind, &size, &dev, &ino, &nlink, &linkText, &state, &content); err != nil {
			return err
		}
		d, ok := b.index[parent]
		if !ok {
			return nil // below a missing folder
		}
		b.addFile(d, id, kind, size, dev, ino, nlink, linkText, domain.ContentState(state), content)
		return nil
	})
}

func (b *builder) addFile(d int32, id int64, kind string, size int64, dev, ino, nlink sql.NullInt64, linkText []byte,
	state domain.ContentState, content int64) {
	s := b.s
	regular := kind == string(domain.EntryFile)
	if regular && size > 0 && s.dups != nil && state != "" {
		own := &s.dups.own[d]
		if state != domain.ContentUniqueSize && state != domain.ContentUnreadable {
			own.candidate += size
		}
		if state == domain.ContentHashed || state == domain.ContentSampled {
			own.checked += size
		}
	}
	if _, ok := b.arcAt[id]; ok && regular {
		return // a folder of its members (spliced), not a file
	}
	if regular && size == 0 {
		return // an empty file holds no content
	}
	own := &b.direct[d]
	if regular {
		own.bytes += size
		own.files++
	}
	rec := record{dir: d, size: size}
	switch {
	case kind == string(domain.EntrySymlink) && linkText != nil:
		rec.raw, rec.size = uint64(keyLink)<<rawShift|b.linkKey(linkText), 0
	case regular && state == domain.ContentHashed && content > 0 && !(s.provisional && size < largeFileBytes):
		rec.raw = uint64(keyContent)<<rawShift | uint64(content)&rawMask
		if p := b.physOf(s.src[d], id, dev, ino, nlink); p[0] >= 0 {
			rec.phys = [3]int64{p[0], p[1], 0}
		}
	case regular && (state == domain.ContentUniqueSize || state == domain.ContentSampled):
		if p := b.physOf(s.src[d], id, dev, ino, nlink); p[0] >= 0 {
			// The names of one multiply-linked file match each other
			// without a read; being one file, they free nothing.
			key, ok := b.inodes[p]
			if !ok {
				key = uint64(len(b.inodes))
				b.inodes[p] = key
			}
			rec.raw = uint64(keyInode)<<rawShift | key
			break
		}
		own.keyed++ // unique: never matches
		return
	case regular && s.provisional && (state == domain.ContentPending || state == domain.ContentHashed):
		rec.raw = sizeKey(size)
	default:
		// A gap: pending, changed, unreadable, not planned yet, a special
		// file, or a symlink without text.
		own.gaps++
		own.gapFiles++
		if regular {
			own.gapBytes += size
			own.gapRegular++
		}
		return
	}
	own.keyed++
	b.recs = append(b.recs, rec)
}

func (b *builder) finish() *Snapshot {
	s := b.s
	n := len(s.parent)

	// Entries in directory order (a stable counting sort).
	s.first = make([]int32, n+1)
	for _, r := range b.recs {
		s.first[r.dir+1]++
	}
	for d := range n {
		s.first[d+1] += s.first[d]
	}
	at := slices.Clone(s.first[:n])
	ne := len(b.recs)
	s.eDir = make([]int32, ne)
	s.eSize = make([]int64, ne)
	raws := make([]uint64, ne)
	type inodeRow struct {
		phys [3]int64
		e    int32
	}
	var inodes []inodeRow
	for _, r := range b.recs {
		e := at[r.dir]
		at[r.dir]++
		s.eDir[e], s.eSize[e], raws[e] = r.dir, r.size, r.raw
		if r.phys != ([3]int64{}) {
			inodes = append(inodes, inodeRow{r.phys, e})
		}
	}
	b.recs = nil

	s.end = make([]int32, n)
	for d := range s.end {
		s.end[d] = int32(d)
	}
	for d := n - 1; d >= 0; d-- {
		if p := s.parent[d]; p >= 0 {
			s.end[p] = max(s.end[p], s.end[d])
		}
	}
	s.sums = make([]dirSums, n+1)
	for d := range n {
		s.sums[d+1] = s.sums[d].plus(b.direct[d])
	}
	b.direct = nil

	// A folder holding nothing but one archive is named by the archive.
	for _, z := range s.arcDir {
		t := s.subtree(z)
		if t.keyed == 0 {
			continue
		}
		for p := s.parent[z]; p >= 0 && s.subtree(p) == t; p = s.parent[p] {
			if s.lowered == nil {
				s.lowered = map[int32]int32{}
			}
			s.lowered[p] = z
		}
	}

	// Inode groups: entries sharing a physical identity free nothing for
	// each other.
	s.eIno = make([]int32, ne)
	for e := range s.eIno {
		s.eIno[e] = -1
	}
	slices.SortFunc(inodes, func(x, y inodeRow) int {
		return cmp.Or(cmp.Compare(x.phys[0], y.phys[0]), cmp.Compare(x.phys[1], y.phys[1]), cmp.Compare(x.phys[2], y.phys[2]))
	})
	groups := int32(0)
	for i := 0; i < len(inodes); {
		j := i + 1
		for j < len(inodes) && inodes[j].phys == inodes[i].phys {
			j++
		}
		if j-i > 1 {
			for _, r := range inodes[i:j] {
				s.eIno[r.e] = groups
			}
			groups++
		}
		i = j
	}

	// Keys: group entries by raw key; within a key, entries (and so
	// directories) stay in pre order.
	type keyed struct {
		raw uint64
		e   int32
	}
	byKey := make([]keyed, ne)
	for e, raw := range raws {
		byKey[e] = keyed{raw, int32(e)}
	}
	raws = nil
	slices.SortFunc(byKey, func(x, y keyed) int { return cmp.Or(cmp.Compare(x.raw, y.raw), cmp.Compare(x.e, y.e)) })
	s.eKey = make([]int32, ne)
	s.occ = make([]int32, ne)
	var arcKeys map[int64]int32 // content -> key, for the complete archives' own contents
	if s.dups != nil && len(s.dups.arcFiles) > 0 {
		arcKeys = map[int64]int32{}
		for _, f := range s.dups.arcFiles {
			if f.content > 0 {
				arcKeys[f.content] = -1
			}
		}
	}
	for i, r := range byKey {
		if i == 0 || r.raw != byKey[i-1].raw {
			s.kAt = append(s.kAt, int32(i))
			s.kKind = append(s.kKind, keyKind(r.raw>>rawShift))
			s.kBytes = append(s.kBytes, s.eSize[r.e])
			if c := int64(r.raw & rawMask); arcKeys != nil && keyKind(r.raw>>rawShift) == keyContent {
				if _, ok := arcKeys[c]; ok {
					arcKeys[c] = int32(len(s.kKind) - 1)
				}
			}
		}
		s.eKey[r.e] = int32(len(s.kKind) - 1)
		s.occ[i] = r.e
	}
	s.kAt = append(s.kAt, int32(ne))
	byKey = nil

	if s.dups != nil {
		s.countCopies(arcKeys, groups)
	}

	// Each folder's three largest shared keys (keys found elsewhere too).
	s.top = make([][3]int32, n)
	for d := range s.top {
		s.top[d] = [3]int32{-1, -1, -1}
	}
	for d := n - 1; d >= 0; d-- {
		for e := s.first[d]; e < s.first[d+1]; e++ {
			if k := s.eKey[e]; s.kAt[k+1]-s.kAt[k] > 1 {
				s.addTop(int32(d), k)
			}
		}
		if p := s.parent[d]; p >= 0 {
			for _, k := range s.top[d] {
				if k >= 0 {
					s.addTop(p, k)
				}
			}
		}
	}
	return s
}

// countCopies sets each content key's physical copies (design D8): one per
// inode group among its occurrences, plus each complete archive file of
// that content (one per physical identity). arcKeys maps the archives'
// contents to their keys (-1: no entry or member has it).
func (s *Snapshot) countCopies(arcKeys map[int64]int32, groups int32) {
	dd := s.dups
	dd.copies = make([]int32, len(s.kKind))
	seen := make([]int32, groups) // seen[g] == k+1: group g counted for key k
	for k := range int32(len(s.kKind)) {
		if s.kKind[k] != keyContent {
			continue
		}
		n := int32(0)
		for _, e := range s.occOf(k) {
			if g := s.eIno[e]; g >= 0 {
				if seen[g] == k+1 {
					continue
				}
				seen[g] = k + 1
			}
			n++
		}
		dd.copies[k] = n
	}
	if len(arcKeys) == 0 {
		return
	}
	extra := map[int64]int32{} // content -> archive file copies
	phys := map[[2]int64]bool{}
	for _, f := range dd.arcFiles {
		if f.content == 0 || phys[f.phys] {
			continue
		}
		phys[f.phys] = true
		extra[f.content]++
	}
	for c, n := range extra {
		if k := arcKeys[c]; k >= 0 {
			dd.copies[k] += n
		}
	}
	dd.arcExtra = extra
	dd.arcKeys = arcKeys
}

// addTop inserts key k into directory d's three largest keys (by bytes,
// then key order).
func (s *Snapshot) addTop(d, k int32) {
	t := &s.top[d]
	for _, x := range t {
		if x == k {
			return
		}
	}
	less := func(x, y int32) bool { // x ranks before y
		return y < 0 || s.kBytes[x] > s.kBytes[y] || s.kBytes[x] == s.kBytes[y] && x < y
	}
	if !less(k, t[2]) {
		return
	}
	t[2] = k
	for i := 2; i > 0 && less(t[i], t[i-1]); i-- {
		t[i], t[i-1] = t[i-1], t[i]
	}
}

func (s *Snapshot) dirPath(d int32) []byte { return s.paths[s.pathAt[d]:s.pathAt[d+1]] }

func (s *Snapshot) subtree(d int32) dirSums { return s.sums[s.end[d]+1].minus(s.sums[d]) }

func (s *Snapshot) entries(d int32) (lo, hi int32) { return s.first[d], s.first[s.end[d]+1] }

func (s *Snapshot) occOf(k int32) []int32 { return s.occ[s.kAt[k]:s.kAt[k+1]] }

// contains reports whether directory x is d or one of d's descendants.
func (s *Snapshot) contains(d, x int32) bool { return x >= d && x <= s.end[d] }

// ref is directory d's side: an entry folder, an archive (its file's
// entry), or a folder inside an archive (the archive's entry and the
// member).
func (s *Snapshot) ref(d int32) domain.Ref {
	a := s.arc[d]
	switch {
	case a < 0:
		return domain.Ref{Entry: domain.EntryID(s.id[d])}
	case s.arcDir[a] == d:
		return domain.Ref{Entry: domain.EntryID(s.id[d])}
	default:
		return domain.Ref{Entry: domain.EntryID(s.id[s.arcDir[a]]), Member: domain.MemberID(s.id[d])}
	}
}

// rangeOf is directory d's Range (see Range).
func (s *Snapshot) rangeOf(d int32) Range {
	src := s.sources[s.src[d]]
	if a := s.arc[d]; a >= 0 {
		return itself(src, s.dirPath(s.arcDir[a]))
	}
	return descendants(src, s.dirPath(d))
}
