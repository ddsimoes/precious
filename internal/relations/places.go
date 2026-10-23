package relations

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"precious/internal/domain"
	"precious/internal/jobs"
)

// The places of folders, complete archives, and member folders (r5c design
// D1, D3, D4, D5, D6, D14), computed on the final snapshot and stored in
// entry_places, archive_places, and member_places.
//
// Every non-empty regular file and file member outside the quarantine has
// one place, relative to its own source (a member's is its archive's):
//   - unique_size or sampled: only_copy;
//   - hashed: only_copy with one physical copy, this_source when all its
//     copies are on its source, else other_source (dupInputs.srcOf);
//   - pending, changed, or no content row yet: unchecked;
//   - unreadable: unreadable.
//
// A folder's row sums its subtree's files by place, apart from the
// disposable ones (domain.IsDisposable), which count only as disposable. A
// complete archive's entry_places row splits its file's size by its
// members' sums (domain.SplitArchive) after the archive file's own
// coverage; that split is what its folder adds. Its archive_places row,
// and each of its member folders' member_places rows, hold the unpacked
// member sums under that coverage.

// classifier gives a member's category, file kind, and triage from its name
// and size (D14).
type classifier = func(name []byte, size int64) (domain.Category, domain.FileKind, domain.Triage)

// multiSource is dupInputs.srcOf for a content with copies on two or more
// sources.
const multiSource int32 = -1

// sameSource merges source src into cur, the source of the copies so far.
func sameSource(cur, src int32) int32 {
	if cur != src {
		return multiSource
	}
	return cur
}

// slot is one of D1's places that hold files.
type slot uint8

const (
	slotOnlyCopy slot = iota
	slotThisSource
	slotOtherSource
	slotUnchecked
	slotUnreadable
)

func amountAt(p *domain.PlaceSums, s slot) *domain.PlaceAmount {
	switch s {
	case slotOnlyCopy:
		return &p.OnlyCopy
	case slotThisSource:
		return &p.ThisSource
	case slotOtherSource:
		return &p.OtherSource
	case slotUnchecked:
		return &p.Unchecked
	default:
		return &p.Unreadable
	}
}

func addAmount(a *domain.PlaceAmount, b domain.PlaceAmount) {
	a.Files += b.Files
	a.Bytes += b.Bytes
}

func addSums(a *domain.PlaceSums, b domain.PlaceSums) {
	addAmount(&a.OnlyCopy, b.OnlyCopy)
	addAmount(&a.ThisSource, b.ThisSource)
	addAmount(&a.OtherSource, b.OtherSource)
	addAmount(&a.Unchecked, b.Unchecked)
	addAmount(&a.Unreadable, b.Unreadable)
	addAmount(&a.Disposable, b.Disposable)
}

// statePlace is the place of a non-empty file or member that no key
// places, by its content state; hashed without a content is unchecked.
func statePlace(state domain.ContentState) slot {
	switch state {
	case domain.ContentUniqueSize, domain.ContentSampled:
		return slotOnlyCopy
	case domain.ContentUnreadable:
		return slotUnreadable
	default:
		return slotUnchecked
	}
}

// addOwnPlace adds one non-empty regular file or file member of size bytes
// to its directory's own sums p: as disposable when D14 counts it apart,
// else by its content state unless its key places it (keyed, entryPlaces).
func addOwnPlace(p *domain.PlaceSums, disposable, keyed bool, state domain.ContentState, size int64) {
	one := domain.PlaceAmount{Files: 1, Bytes: size}
	switch {
	case disposable:
		addAmount(&p.Disposable, one)
	case keyed:
	default:
		addAmount(amountAt(p, statePlace(state)), one)
	}
}

// disposableE is domain.IsDisposable as SQL on the entries row aliased e,
// generated from the domain's categories and file kinds (D14): 1 when the
// triage is not review, and the category's family is disposable, or the
// category is unknown or absent and the file kind's family is; else 0. It
// runs on every file of a pass, so it tests with plain comparisons, the
// rare cases last.
var disposableE = func() string {
	quote := func(s string) string { return "'" + s + "'" }
	var disposable, known, kinds []string
	for _, c := range domain.Categories {
		if c == domain.CategoryUnknown {
			continue
		}
		known = append(known, quote(string(c)))
		if domain.FamilyOf(c) == domain.FamilyDisposable {
			disposable = append(disposable, "e.category = "+quote(string(c)))
		}
	}
	for _, k := range domain.FileKinds {
		if domain.FileFamily(domain.CategoryUnknown, k) == domain.FamilyDisposable {
			kinds = append(kinds, "e.file_kind = "+quote(string(k)))
		}
	}
	return `CASE WHEN e.triage = ` + quote(string(domain.TriageReview)) + ` THEN 0` +
		` WHEN ` + strings.Join(disposable, " OR ") + ` THEN 1` +
		` WHEN ` + strings.Join(kinds, " OR ") + ` THEN e.category IS NULL OR e.category NOT IN (` +
		strings.Join(known, ", ") + `)` +
		` ELSE 0 END`
}()

// disposableColumn selects whether D14 counts apart the file entry e; the
// provisional snapshot needs no place sums.
func (b *builder) disposableColumn() string {
	if b.s.provisional {
		return `0`
	}
	return disposableE
}

// placeMember adds a non-empty file member of archive a, member row's name
// with size bytes (a hard link's are its target's), to its folder's own
// place sums (addOwnPlace), and reports whether D14 counts it apart. Only
// a final snapshot has place sums, and without a classifier no member is
// disposable.
func (b *builder) placeMember(a *archiveRow, folder, row int32, size int64, state domain.ContentState, keyed bool) bool {
	if a.places == nil {
		return false
	}
	m := &a.members[row]
	disposable := false
	switch {
	case b.classify == nil:
	case a.classified != nil && m.kind == domain.MemberFile && m.size == size:
		disposable = a.classified[row]
	default:
		disposable = domain.IsDisposable(b.classify(m.name, size))
	}
	addOwnPlace(&a.places[folder], disposable, keyed, state, size)
	return disposable
}

// classifyMembers sets each archive's classified flags (D14), spread over
// the CPUs: run one by one, the file rules on millions of members add about
// 15% to the snapshot's load.
func (b *builder) classifyMembers() {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(b.arcs)) {
		wg.Go(func() {
			for i := int(next.Add(1) - 1); i < len(b.arcs); i = int(next.Add(1) - 1) {
				a := &b.arcs[i]
				a.classified = make([]bool, len(a.members))
				for j := range a.members {
					if m := &a.members[j]; m.kind == domain.MemberFile && m.size > 0 {
						a.classified[j] = domain.IsDisposable(b.classify(m.name, m.size))
					}
				}
			}
		})
	}
	wg.Wait()
}

// keyPlace is the place of a hashed file or member with content key k on
// source src (D3).
func (dd *dupInputs) keyPlace(k, src int32) slot {
	switch {
	case dd.copies[k] <= 1:
		return slotOnlyCopy
	case dd.srcOf[k] == src:
		return slotThisSource
	default:
		return slotOtherSource
	}
}

// placeTables are the rows of entry_places, archive_places, and
// member_places, keyed by entry ID, entry ID, and member ID.
type placeTables struct {
	entries, archives, members map[int64]domain.PlaceSums
}

// placeRows computes the places tables from the final snapshot: one
// entry_places row per entry folder and complete archive (D4, D5); one
// archive_places row per complete archive, its members' sums under the
// archive file's own coverage; and one member_places row per member
// folder, its members' sums under the same coverage. It consumes the
// snapshot's own place sums.
func (s *Snapshot) placeRows() placeTables {
	dd := s.dups
	sums := dd.places
	dd.places = nil
	for e, d := range s.eDir {
		k := s.eKey[e]
		if s.kKind[k] != keyContent || dd.disposable != nil && dd.disposable[e] {
			continue
		}
		addAmount(amountAt(&sums[d], dd.keyPlace(k, s.src[d])), domain.PlaceAmount{Files: 1, Bytes: s.eSize[e]})
	}
	folders := 0
	for _, a := range s.arc {
		if a < 0 {
			folders++
		}
	}
	out := placeTables{
		entries:  make(map[int64]domain.PlaceSums, folders+len(s.arcDir)),
		archives: make(map[int64]domain.PlaceSums, len(s.arcDir)),
		members:  make(map[int64]domain.PlaceSums, len(s.parent)-folders-len(s.arcDir)),
	}
	for d := len(s.parent) - 1; d >= 0; d-- {
		switch a := s.arc[d]; {
		case a < 0:
			out.entries[s.id[d]] = sums[d]
		case s.arcDir[a] == int32(d):
			m, own := s.covered(a, sums[d])
			out.archives[s.id[d]] = m
			sums[d] = s.archivePlaces(a, m, own)
			out.entries[s.id[d]] = sums[d]
		default:
			out.members[s.id[d]], _ = s.covered(a, sums[d])
		}
		if p := s.parent[d]; p >= 0 {
			addSums(&sums[p], sums[d])
		}
	}
	return out
}

// covered applies complete archive a's own coverage (D5) to member sums m,
// and gives the archive file's own place: when the file is other_source,
// every member byte counts as other_source; when it is this_source, the
// only_copy member bytes count as this_source; an unhashed archive file
// covers nothing and is placed by its content state.
func (s *Snapshot) covered(a int32, m domain.PlaceSums) (domain.PlaceSums, slot) {
	f := s.dups.arcFiles[a]
	if f.content == 0 {
		return m, statePlace(f.state)
	}
	own := s.arcOwnPlace(f)
	switch own {
	case slotOtherSource:
		var all domain.PlaceAmount
		for _, x := range []domain.PlaceAmount{m.OnlyCopy, m.ThisSource, m.OtherSource, m.Unchecked, m.Unreadable} {
			addAmount(&all, x)
		}
		m = domain.PlaceSums{OtherSource: all, Disposable: m.Disposable}
	case slotThisSource:
		addAmount(&m.ThisSource, m.OnlyCopy)
		m.OnlyCopy = domain.PlaceAmount{}
	}
	return m, own
}

// archivePlaces is complete archive a's entry_places row from its members'
// sums m under its coverage and its file's own place (covered), by D5: a
// disposable archive (D14) counts whole as disposable; else the file's
// size is split by the member bytes, or, with no member bytes, placed
// whole by the file's own place.
func (s *Snapshot) archivePlaces(a int32, m domain.PlaceSums, own slot) domain.PlaceSums {
	f := s.dups.arcFiles[a]
	if f.disposable {
		return domain.PlaceSums{Disposable: domain.PlaceAmount{Files: 1, Bytes: f.size}}
	}
	if m.OnlyCopy.Bytes+m.ThisSource.Bytes+m.OtherSource.Bytes+m.Unchecked.Bytes+m.Unreadable.Bytes > 0 {
		return domain.SplitArchive(f.size, m)
	}
	var row domain.PlaceSums
	if f.size > 0 {
		*amountAt(&row, own) = domain.PlaceAmount{Files: 1, Bytes: f.size}
	}
	return row
}

// arcOwnPlace is the place of hashed complete archive file f by its own
// content (D3): through its key when an entry or member has the content,
// else through the archive files alone.
func (s *Snapshot) arcOwnPlace(f arcFile) slot {
	dd := s.dups
	copies, src := dd.arcExtra[f.content], dd.arcSrc[f.content]
	if k := dd.arcKeys[f.content]; k >= 0 {
		copies, src = dd.copies[k], dd.srcOf[k]
	}
	switch {
	case copies <= 1:
		return slotOnlyCopy
	case src == s.src[f.dir]:
		return slotThisSource
	default:
		return slotOtherSource
	}
}

// placeColumns are the figure columns of the places tables (design
// Interfaces), in placeFields' order.
var placeColumns = []string{"only_copy_files", "only_copy_bytes", "this_source_files", "this_source_bytes",
	"other_source_files", "other_source_bytes", "unchecked_files", "unchecked_bytes",
	"unreadable_files", "unreadable_bytes", "disposable_files", "disposable_bytes"}

func placeFields(p *domain.PlaceSums) []*int64 {
	return []*int64{&p.OnlyCopy.Files, &p.OnlyCopy.Bytes, &p.ThisSource.Files, &p.ThisSource.Bytes,
		&p.OtherSource.Files, &p.OtherSource.Bytes, &p.Unchecked.Files, &p.Unchecked.Bytes,
		&p.Unreadable.Files, &p.Unreadable.Bytes, &p.Disposable.Files, &p.Disposable.Bytes}
}

// The places tables for writeFigures.
var (
	entryPlacesTable = figureTable[domain.PlaceSums]{name: "entry_places", key: "entry_id", owner: "entries",
		cols: placeColumns, fields: placeFields}
	archivePlacesTable = figureTable[domain.PlaceSums]{name: "archive_places", key: "entry_id", owner: "entries",
		cols: placeColumns, fields: placeFields}
	memberPlacesTable = figureTable[domain.PlaceSums]{name: "member_places", key: "member_id", owner: "archive_members",
		cols: placeColumns, fields: placeFields}
)

// writePlaces upserts the rows of each places table that differ from the
// stored ones, each only while its entry or member exists, and deletes the
// rows of entries and members no longer in the snapshot (D4). Each table's
// rows are dropped once written.
func (h *Handler) writePlaces(ctx context.Context, rt jobs.Runtime, want placeTables) error {
	if err := writeFigures(ctx, h, rt, entryPlacesTable, want.entries); err != nil {
		return err
	}
	want.entries = nil
	if err := writeFigures(ctx, h, rt, archivePlacesTable, want.archives); err != nil {
		return err
	}
	want.archives = nil
	return writeFigures(ctx, h, rt, memberPlacesTable, want.members)
}
