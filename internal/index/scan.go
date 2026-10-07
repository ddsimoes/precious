package index

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/rules"
)

// maxIndicators bounds a folder's dir_stats.indicators list (design D9).
const maxIndicators = 20

// Progress keys of a scan job (design D7).
const (
	ProgressPhase      = "phase" // 1 walking, 2 finishing
	ProgressDirs       = "dirs"
	ProgressFiles      = "files"
	ProgressBytes      = "bytes"
	ProgressWritten    = "written"
	ProgressUnreadable = "unreadable"
	ProgressMissing    = "missing"
)

// Scan phases, the values of ProgressPhase.
const (
	PhaseWalking   = 1
	PhaseFinishing = 2
)

// errSourceGone ends a walk whose source became unavailable under it.
var errSourceGone = errors.New("index: the source became unavailable during the scan")

// child is one listed entry of a folder, waiting to be processed.
type child struct {
	name  []byte
	info  fsaccess.EntryInfo
	old   *stored
	token uint64
}

// frame is one folder on the walk's stack: open, listed, and accumulating
// the totals of its finished children (post-order, design D7).
type frame struct {
	dir  fsaccess.Dir
	path []byte
	// name is what folder rules match: the raw name, or the root folder's
	// own name for the root.
	name  []byte
	info  fsaccess.EntryInfo
	id    domain.EntryID // 0: inserted by this scan, known by path
	old   *stored
	token uint64

	children map[string]*stored
	subdirs  []child
	next     int
	deferred []child
	stems    map[string]bool

	listed, unreadable, boundary bool

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
	indicators                   int
	refs                         []indicatorRef
	// inside holds the notable entries below the folder, largest first, for
	// each family that may turn out to be its dominant one (familyKeys
	// order), and at noDominant for no bytes (design D21).
	inside [noDominant + 1][]insideRef
}

func (f *frame) reset() {
	children, byKind, byYear := f.children, f.byKind, f.byYear
	childSignals, subtreeSignals, stems := f.childSignals, f.subtreeSignals, f.stems
	subdirs, deferred, refs, inside := f.subdirs, f.deferred, f.refs, f.inside
	clear(children)
	clear(byKind)
	clear(byYear)
	clear(childSignals)
	clear(subtreeSignals)
	clear(stems)
	clear(subdirs)
	clear(deferred)
	clear(refs)
	for i := range inside {
		clear(inside[i])
		inside[i] = inside[i][:0]
	}
	*f = frame{
		children: children, byKind: byKind, byYear: byYear, childSignals: childSignals,
		subtreeSignals: subtreeSignals, stems: stems, subdirs: subdirs[:0], deferred: deferred[:0],
		refs: refs[:0], inside: inside,
	}
}

func (f *frame) addRange(oldest, newest int64) {
	if !f.dated {
		f.oldest, f.newest, f.dated = oldest, newest, true
		return
	}
	f.oldest = min(f.oldest, oldest)
	f.newest = max(f.newest, newest)
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
func (f *frame) addFile(kind domain.FileKind, size, mtime int64, family domain.Family) {
	f.files++
	f.bytes += size
	y := unknownYear
	if domain.KnownModTime(mtime) {
		f.addRange(mtime, mtime)
		y = time.Unix(0, mtime).UTC().Year()
	}
	kt := f.byKind[kind]
	f.byKind[kind] = rules.KindTotals{Files: kt.Files + 1, Bytes: kt.Bytes + size}
	c := f.byYear[y]
	c.add(counts{files: 1, bytes: size})
	f.byYear[y] = c
	f.comp[familyIndex(family)].add(counts{files: 1, bytes: size})
}

// walk is one scan attempt's walk of its source.
type walk struct {
	ctx      context.Context
	rt       jobs.Runtime
	pol      *rules.Policy
	caps     fsaccess.Capabilities
	w        *writer
	children *sql.Stmt
	listN    int
	batchN   int
	codec    *codec

	b      *batch
	frames []*frame
	depth  int
	sigs   []rules.SignalID
	// token numbers the entries the scan inserts that a list refers to;
	// held counts the lists referring to each, and release collects the
	// ones no list refers to any more, for the next folder finish.
	token   uint64
	held    map[uint64]int32
	release []uint64

	progress                        map[string]int64
	dirs, files, bytes, unreadableN int64
}

func (s *walk) top() *frame { return s.frames[s.depth-1] }

func (s *walk) push() *frame {
	if s.depth == len(s.frames) {
		s.frames = append(s.frames, &frame{
			children: map[string]*stored{}, byKind: map[domain.FileKind]rules.KindTotals{},
			byYear: map[int]counts{}, childSignals: map[rules.SignalID]int{},
			subtreeSignals: map[rules.SignalID]int{}, stems: map[string]bool{},
		})
	}
	f := s.frames[s.depth]
	f.reset()
	s.depth++
	return f
}

// batch returns the batch being filled.
func (s *walk) batch() *batch {
	if s.b == nil {
		select {
		case s.b = <-s.w.free:
		default:
			s.b = &batch{}
		}
	}
	return s.b
}

// emit queues o, which refers to bytes of the current batch, and hands the
// batch to the writer once it is full. It blocks while the writer's queue is
// full (backpressure) and fails once the writer has failed.
func (s *walk) emit(o *op) error {
	b := s.batch()
	b.ops = append(b.ops, *o)
	if len(b.ops) < s.batchN {
		return nil
	}
	return s.flush()
}

func (s *walk) flush() error {
	if s.b == nil || len(s.b.ops) == 0 {
		return nil
	}
	select {
	case s.w.in <- s.b:
		s.b = nil
		return nil
	case <-s.w.dead:
		return s.w.err
	}
}

// fs runs one filesystem call under the job's watchdog.
func fs[T any](s *walk, op string, call func() (T, error)) (T, error) {
	done := s.rt.FSCall(op)
	defer done()
	return call()
}

// run walks the tree below the root folder, which is open as dir.
func (s *walk) run(dir fsaccess.Dir, rootName []byte, old *stored) error {
	f := s.push()
	f.dir, f.path, f.name, f.info, f.old = dir, []byte{}, rootName, dir.Self(), old
	if old == nil {
		o := op{kind: opInsert, root: true}
		o.row = s.openRow(&f.info)
		if err := s.emit(&o); err != nil {
			return err
		}
	} else {
		f.id = old.id
	}
	s.dirs++
	if err := s.list(f); err != nil {
		return err
	}
	for s.depth > 0 {
		f := s.top()
		if f.next < len(f.subdirs) {
			c := f.subdirs[f.next]
			f.subdirs[f.next] = child{}
			f.next++
			if err := s.descend(f, c); err != nil {
				return err
			}
			continue
		}
		if err := s.finish(f); err != nil {
			return err
		}
	}
	return nil
}

// openRow is the row a folder is inserted with when it is listed, before its
// totals are known.
func (s *walk) openRow(info *fsaccess.EntryInfo) row {
	r := row{kind: string(domain.EntryDirectory), state: "present"}
	r.setFacts(info)
	return r
}

// descend opens the listed folder c of p and pushes it.
func (s *walk) descend(p *frame, c child) error {
	var dir fsaccess.Dir
	unreadable := false
	if !c.info.MountBoundary {
		d, err := fs(s, "OpenDir", func() (fsaccess.Dir, error) { return p.dir.OpenDir(c.name, c.info) })
		switch outcome, _ := fsaccess.OutcomeOf(err); {
		case err == nil:
			dir = d
		case outcome == domain.OutcomeAbsent:
			// Gone since it was listed.
			if c.old != nil && c.old.row.state != "missing" {
				return s.missing(p, c.name, c.old, true, true)
			}
			return nil
		case outcome == domain.OutcomeUnavailable:
			return errSourceGone
		default:
			unreadable = true
		}
	}
	f := s.push()
	f.dir, f.path, f.name, f.info, f.old, f.token = dir, joinPath(p.path, c.name), c.name, c.info, c.old, c.token
	f.boundary, f.unreadable = c.info.MountBoundary, unreadable
	s.dirs++
	if f.old != nil {
		f.id = f.old.id
		if f.boundary && !f.old.row.boundary {
			// A folder that became a mount point: what was stored below it is
			// on the covered filesystem, no longer reachable.
			b := s.batch()
			o := op{kind: opMissing, subtree: true, path: b.add(f.path)}
			if err := s.emit(&o); err != nil {
				return err
			}
		}
	} else {
		// A new folder may enter its parent's inside lists when it finishes:
		// its frame holds a token until then.
		if f.token == 0 {
			f.token = s.newToken()
		}
		s.hold(f.token)
		b := s.batch()
		o := op{kind: opInsert, id: p.id, path: b.add(f.path), name: b.add(c.name), token: f.token}
		if p.id == 0 {
			o.parent = b.add(p.path)
		}
		o.row = s.openRow(&f.info)
		if err := s.emit(&o); err != nil {
			return err
		}
	}
	if f.dir == nil {
		f.listed = true
		if f.unreadable {
			s.unreadableN++
		}
		s.report()
		return nil
	}
	return s.list(f)
}

func joinPath(dir, name []byte) []byte {
	if len(dir) == 0 {
		return append([]byte(nil), name...)
	}
	p := make([]byte, 0, len(dir)+1+len(name))
	return append(append(append(p, dir...), '/'), name...)
}

// list reads the folder's stored children, then lists it, processing every
// entry but its subfolders, which the walk descends into next.
func (s *walk) list(f *frame) error {
	f.listed = true
	if f.old != nil {
		if err := readChildren(s.ctx, s.children, f.id, f.children); err != nil {
			return err
		}
	}
	complete, err := s.read(f)
	if err != nil {
		return err
	}
	for i := range f.deferred {
		if err := s.file(f, &f.deferred[i], s.pol.FileKindNear(f.deferred[i].name, f.stems)); err != nil {
			return err
		}
	}
	if complete {
		for name, old := range f.children {
			if old.seen || old.row.state == "missing" {
				continue
			}
			dir := old.row.kind == string(domain.EntryDirectory)
			if err := s.missing(f, []byte(name), old, true, dir); err != nil {
				return err
			}
		}
	} else {
		// Its stored children not listed stay as they were; the subfolders
		// listed before the failure are still walked.
		f.unreadable = true
		s.unreadableN++
	}
	s.report()
	return s.rt.Yield(s.ctx)
}

// read lists the folder in batches and Lstats every entry. complete is false
// when the listing failed; the entries seen before stay processed.
func (s *walk) read(f *frame) (complete bool, err error) {
	for {
		if err := s.ctx.Err(); err != nil {
			return false, err
		}
		entries, err := fs(s, "ReadBatch", func() ([]fsaccess.DirEntry, error) { return f.dir.ReadBatch(s.listN) })
		if err == io.EOF {
			return true, nil
		}
		if err != nil {
			if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeUnavailable {
				return false, errSourceGone
			}
			return false, nil
		}
		for i := range entries {
			name := entries[i].Name
			info, err := fs(s, "Lstat", func() (fsaccess.EntryInfo, error) { return f.dir.Lstat(name) })
			if err != nil {
				switch o, _ := fsaccess.OutcomeOf(err); o {
				case domain.OutcomeAbsent:
					continue // removed since listed
				case domain.OutcomeUnavailable:
					return false, errSourceGone
				default:
					return false, nil
				}
			}
			if err := s.entry(f, name, &info); err != nil {
				return false, err
			}
		}
	}
}

// entry processes one listed entry of f.
func (s *walk) entry(f *frame, name []byte, info *fsaccess.EntryInfo) error {
	kind, _ := kindColumn(info.Kind)
	old := f.children[string(name)]
	if old != nil {
		old.seen = true
		if old.row.kind != kind {
			// A different entry at the same path (design D6).
			b := s.batch()
			o := op{kind: opDelete, id: old.id, subtree: old.row.kind == string(domain.EntryDirectory),
				path: b.join(f.path, name)}
			if err := s.emit(&o); err != nil {
				return err
			}
			old = nil
		}
	}
	c := child{name: name, info: *info, old: old}

	s.sigs = s.pol.AppendNameSignals(s.sigs[:0], name, info.Kind)
	var indicator rules.SignalID
	for _, sig := range s.sigs {
		f.childSignals[sig]++
		f.subtreeSignals[sig]++
		if indicator == "" && s.pol.IsIndicator(sig) {
			indicator = sig
		}
	}
	if indicator != "" {
		f.indicators++
		if len(f.refs) < maxIndicators {
			ref := indicatorRef{path: joinPath(f.path, name), signal: indicator}
			if old != nil {
				ref.id = old.id
			} else {
				c.token = s.newToken()
				ref.token = c.token
			}
			s.hold(ref.token)
			f.refs = append(f.refs, ref)
		}
	}

	switch info.Kind {
	case domain.EntryDirectory:
		f.subdirs = append(f.subdirs, c)
		return nil
	case domain.EntryFile:
		if stem, ok := s.pol.PairStem(name); ok {
			f.stems[stem] = true
		}
		if k := s.pol.FileKind(name); k != domain.FileKindOther || !s.pol.Paired(name) {
			return s.file(f, &c, k)
		}
		// Its kind depends on a sibling that may be listed later (a .bin
		// next to its .cue): processed once the listing is complete.
		f.deferred = append(f.deferred, c)
		return nil
	default:
		return s.leaf(f, &c)
	}
}

// unchanged reports whether a stored entry's own facts match an Lstat under
// the source's capabilities (design D8): same size, a modification time
// within the tolerance, a change time within the same tolerance when both
// are known (R2 design D4: a restored modification time does not hide a
// change), and, where identity is stable, the same object.
func (s *walk) unchanged(old *stored, info *fsaccess.EntryInfo) bool {
	r := &old.row
	if r.size != info.Size || r.boundary != info.MountBoundary || !sameTime(&s.caps, r.mtime, info.ModTime) {
		return false
	}
	if r.ctime.ok && r.ctime.v != 0 && !info.Ctime.IsZero() && !sameTime(&s.caps, r.ctime, info.Ctime) {
		return false
	}
	return !s.caps.StableIdentity ||
		(r.dev == some(int64(info.Dev)) && r.ino == some(int64(info.Ino)))
}

// facts sets r's own facts: the stored ones when the entry is unchanged,
// else the observed ones. It reports whether they are the stored ones.
func (s *walk) facts(r *row, c *child) bool {
	if c.old != nil && s.unchanged(c.old, &c.info) {
		r.copyFacts(&c.old.row)
		r.boundary = c.info.MountBoundary
		return true
	}
	r.setFacts(&c.info)
	return false
}

// file processes a regular file of kind.
func (s *walk) file(f *frame, c *child, kind domain.FileKind) error {
	r := row{kind: string(domain.EntryFile), state: "present"}
	same := s.facts(&r, c)
	res := s.pol.ClassifyFile(rules.FileFacts{Name: c.name, Kind: kind, Size: r.size, SiblingStems: f.stems})
	r.classify(&res, s.codec)
	r.fileKind = text(kind)
	r.ext = s.codec.ext(c.name)
	r.totalBytes, r.totalFiles = r.size, 1
	r.newest, r.oldest = ownRange(r.mtime)
	family := domain.FileFamily(res.Category, kind)
	f.addFile(kind, r.size, r.mtime.v, family)
	s.notableFile(f, c, &r, family)
	s.files++
	s.bytes += r.size
	return s.write(f, c, &r, nil, false, !same)
}

// notableFile offers the file c of f, with row r, to the inside lists of the
// families it differs from (design D21). A new file that any of them takes
// gets a token, so that the writer can resolve its ID.
func (s *walk) notableFile(f *frame, c *child, r *row, family domain.Family) {
	own := familyIndex(family)
	var ref insideRef
	for i := range familyKeys {
		if i == own || !accepts(f.inside[i], r.size, f.path, c.name) {
			continue
		}
		if ref.path == nil {
			ref = insideRef{path: joinPath(f.path, c.name), category: r.category, family: text(family),
				bytes: r.size, files: 1}
			if c.old != nil {
				ref.id = c.old.id
			} else {
				if c.token == 0 {
					c.token = s.newToken()
				}
				ref.token = c.token
			}
		}
		s.offer(&f.inside[i], &ref)
	}
}

// leaf processes a symlink or a special file: recorded, never followed or
// opened.
func (s *walk) leaf(f *frame, c *child) error {
	var r row
	r.kind, r.special = kindColumn(c.info.Kind)
	r.state = "present"
	same := s.facts(&r, c)
	r.newest, r.oldest = ownRange(r.mtime)
	var link []byte
	hasLink := false
	if c.info.Kind == domain.EntrySymlink {
		f.symlinks++
		hasLink = true
		if same {
			link = c.old.link
		} else {
			l, err := fs(s, "Readlink", func() ([]byte, error) { return f.dir.Readlink(c.name) })
			if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeUnavailable {
				return errSourceGone
			}
			link, hasLink = l, err == nil
		}
	} else {
		f.specials++
	}
	return s.write(f, c, &r, link, hasLink, false)
}

// write inserts a new leaf, or rewrites a stored one whose row differs.
// refacts reports that a stored file's own facts changed: the update then
// drops the file's content rows (R2 design D4).
func (s *walk) write(f *frame, c *child, r *row, link []byte, hasLink, refacts bool) error {
	if c.old != nil && c.old.row == *r && (!hasLink || string(c.old.link) == string(link)) {
		return nil
	}
	b := s.batch()
	o := op{kind: opUpdate, row: *r, hasLink: hasLink}
	if hasLink {
		o.link = b.add(link)
	}
	if c.old != nil {
		o.id = c.old.id
		o.dropContent = refacts
	} else {
		o.kind, o.id, o.token = opInsert, f.id, c.token
		o.path, o.name = b.join(f.path, c.name), b.add(c.name)
		if f.id == 0 {
			o.parent = b.add(f.path)
		}
	}
	return s.emit(&o)
}

// missing marks a stored child of f, its stored subtree, or both missing.
func (s *walk) missing(f *frame, name []byte, old *stored, self, subtree bool) error {
	b := s.batch()
	o := op{kind: opMissing, id: old.id, self: self, subtree: subtree}
	if subtree {
		o.path = b.join(f.path, name)
	}
	return s.emit(&o)
}

// finish writes the folder on top of the stack, which has no child left, and
// adds its totals to its parent's.
func (s *walk) finish(f *frame) error {
	if f.dir != nil && s.depth > 1 {
		f.dir.Close()
		f.dir = nil
	}
	res := s.pol.ClassifyFolder(rules.FolderFacts{
		Name: f.name, ChildSignals: f.childSignals, SubtreeSignals: f.subtreeSignals,
		Files: f.files, Bytes: f.bytes, ByKind: f.byKind, Indicators: f.indicators,
	})
	r := row{kind: string(domain.EntryDirectory), state: "present", partial: f.partial}
	if f.unreadable {
		r.state = "unreadable"
	}
	c := child{info: f.info, old: f.old}
	s.facts(&r, &c)
	r.classify(&res, s.codec)
	r.totalBytes, r.totalFiles = f.bytes, f.files
	if f.dated {
		r.newest, r.oldest = some(f.newest), some(f.oldest)
	}
	if f.files > 0 {
		r.mainKind = mainKind(f.byKind)
	}
	// Its composition is its content's (design D21); what it adds to its
	// parent's is the same, unless it is a group outside Containers, which
	// counts whole under its own family.
	contribution := f.comp
	if fam := res.Family; res.Group && fam != "" && fam != domain.FamilyContainers {
		contribution = [4]counts{}
		contribution[familyIndex(fam)] = counts{files: f.files, bytes: f.bytes}
	}
	inside := f.inside[dominant(&f.comp, f.bytes)]
	stats := &dirStats{
		cols: statsCols{
			dirs: f.dirs, files: f.files, symlinks: f.symlinks, specials: f.specials,
			unreadable: f.unreadableN, mounts: f.mts,
			byKind: s.codec.byKind(f.byKind), byYear: s.codec.byYear(f.byYear),
			byFamily: s.codec.byFamily(&f.comp), signals: s.codec.signals(f.subtreeSignals),
		},
		refs:   append([]indicatorRef(nil), f.refs...),
		inside: append([]insideRef(nil), inside...),
	}

	s.depth--
	if s.depth > 0 {
		p := s.top()
		p.dirs += 1 + f.dirs
		p.symlinks += f.symlinks
		p.specials += f.specials
		p.unreadableN += f.unreadableN + boolInt(f.unreadable)
		p.mts += f.mts + boolInt(f.boundary)
		p.partial = p.partial || f.partial || f.unreadable
		p.files += f.files
		p.bytes += f.bytes
		if f.dated {
			p.addRange(f.oldest, f.newest)
		}
		for k, t := range f.byKind {
			pt := p.byKind[k]
			p.byKind[k] = rules.KindTotals{Files: pt.Files + t.Files, Bytes: pt.Bytes + t.Bytes}
		}
		for y, n := range f.byYear {
			pc := p.byYear[y]
			pc.add(n)
			p.byYear[y] = pc
		}
		for i := range contribution {
			p.comp[i].add(contribution[i])
		}
		for sig, n := range f.subtreeSignals {
			p.subtreeSignals[sig] += n
		}
		p.indicators += f.indicators
		for _, ref := range f.refs {
			if len(p.refs) < maxIndicators {
				p.refs = append(p.refs, ref)
			} else {
				s.drop(ref.token)
			}
		}
		s.notableFolder(p, f, &r, inside)
	} else {
		// The root: nothing refers to an indicator after it.
		for _, ref := range f.refs {
			s.drop(ref.token)
		}
	}
	// The folder's lists are done with; so is its own token.
	for i := range f.inside {
		for j := range f.inside[i] {
			s.drop(f.inside[i][j].token)
		}
	}
	s.drop(f.token)
	stats.release = append([]uint64(nil), s.release...)
	s.release = s.release[:0]

	b := s.batch()
	o := op{kind: opFinish, id: f.id, row: r, stats: stats, old: f.old}
	if f.id == 0 {
		o.path = b.add(f.path)
	}
	return s.emit(&o)
}

// mainKind is the file kind with the most bytes, then the most files, then
// the smallest name.
func mainKind(m map[domain.FileKind]rules.KindTotals) text {
	var (
		best domain.FileKind
		bt   rules.KindTotals
	)
	for k, t := range m {
		if t.Files == 0 {
			continue
		}
		if best == "" || cmp.Or(cmp.Compare(t.Bytes, bt.Bytes), cmp.Compare(t.Files, bt.Files), cmp.Compare(best, k)) > 0 {
			best, bt = k, t
		}
	}
	return text(best)
}

// abort marks every folder still on the stack partial: their totals are not
// final, so they keep the ones stored before (design D7).
func (s *walk) abort() error {
	for _, f := range s.frames[:s.depth] {
		b := s.batch()
		o := op{kind: opPartial, id: f.id}
		if f.id == 0 {
			o.path = b.add(f.path)
		}
		if err := s.emit(&o); err != nil {
			return err
		}
	}
	return nil
}

// close closes the handles of the folders still on the stack, but the root's,
// which the caller opened.
func (s *walk) close() {
	for i := 1; i < s.depth; i++ {
		if f := s.frames[i]; f.dir != nil {
			f.dir.Close()
			f.dir = nil
		}
	}
}

// report publishes the progress counters.
func (s *walk) report() {
	p := s.progress
	p[ProgressPhase] = PhaseWalking
	p[ProgressDirs] = s.dirs
	p[ProgressFiles] = s.files
	p[ProgressBytes] = s.bytes
	p[ProgressWritten] = s.w.written.Load()
	p[ProgressUnreadable] = s.unreadableN
	p[ProgressMissing] = s.w.missing.Load()
	s.rt.Progress(p)
}
