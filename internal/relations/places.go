package relations

import (
	"context"
	"strings"

	"precious/internal/domain"
	"precious/internal/jobs"
)

// The places of folders and complete archives (r5c design D1, D3, D4, D5,
// D14), computed on the final snapshot and stored in entry_places.
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
// complete archive's row splits its file's size by its members' sums
// (domain.SplitArchive) after the archive file's own coverage; that split
// is what its folder adds. Member folders get no row.

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

// placeMember adds a non-empty file member of archive a, named name with
// size bytes, to its folder's own place sums (addOwnPlace), and reports
// whether D14 counts it apart. Only a final snapshot has place sums, and
// without a classifier no member is disposable.
func (b *builder) placeMember(a *archiveRow, folder int32, name []byte, size int64, state domain.ContentState, keyed bool) bool {
	if a.places == nil {
		return false
	}
	disposable := b.classify != nil && domain.IsDisposable(b.classify(name, size))
	addOwnPlace(&a.places[folder], disposable, keyed, state, size)
	return disposable
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

// entryPlaces computes the rows of entry_places (D4) from the final
// snapshot: one per entry folder and one per complete archive, keyed by
// their entry IDs. It consumes the snapshot's own place sums.
func (s *Snapshot) entryPlaces() map[int64]domain.PlaceSums {
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
	rows := len(s.arcDir)
	for _, a := range s.arc {
		if a < 0 {
			rows++
		}
	}
	out := make(map[int64]domain.PlaceSums, rows)
	for d := len(s.parent) - 1; d >= 0; d-- {
		switch a := s.arc[d]; {
		case a < 0:
			out[s.id[d]] = sums[d]
		case s.arcDir[a] == int32(d):
			sums[d] = s.archivePlaces(a, sums[d])
			out[s.id[d]] = sums[d]
		}
		if p := s.parent[d]; p >= 0 {
			addSums(&sums[p], sums[d])
		}
	}
	return out
}

// archivePlaces is complete archive a's row from its members' sums m (D5):
//   - a disposable archive (D14) counts whole as disposable;
//   - the archive file's own place by its content covers its members: when
//     it is other_source, every member byte counts as other_source; when
//     it is this_source, the only_copy member bytes count as this_source;
//     an unhashed archive file covers nothing;
//   - the file's size is then split by the member bytes, or, with no member
//     bytes, placed whole by the file's own place.
func (s *Snapshot) archivePlaces(a int32, m domain.PlaceSums) domain.PlaceSums {
	f := s.dups.arcFiles[a]
	if f.disposable {
		return domain.PlaceSums{Disposable: domain.PlaceAmount{Files: 1, Bytes: f.size}}
	}
	own := statePlace(f.state)
	if f.content > 0 {
		own = s.arcOwnPlace(f)
		switch own {
		case slotOtherSource:
			var all domain.PlaceAmount
			for _, x := range []domain.PlaceAmount{m.OnlyCopy, m.ThisSource, m.OtherSource, m.Unchecked, m.Unreadable} {
				addAmount(&all, x)
			}
			m = domain.PlaceSums{OtherSource: all}
		case slotThisSource:
			addAmount(&m.ThisSource, m.OnlyCopy)
			m.OnlyCopy = domain.PlaceAmount{}
		}
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

// entryPlacesTable is entry_places (design Interfaces) for writeFigures.
var entryPlacesTable = figureTable[domain.PlaceSums]{
	name: "entry_places",
	cols: []string{"only_copy_files", "only_copy_bytes", "this_source_files", "this_source_bytes",
		"other_source_files", "other_source_bytes", "unchecked_files", "unchecked_bytes",
		"unreadable_files", "unreadable_bytes", "disposable_files", "disposable_bytes"},
	fields: func(p *domain.PlaceSums) []*int64 {
		return []*int64{&p.OnlyCopy.Files, &p.OnlyCopy.Bytes, &p.ThisSource.Files, &p.ThisSource.Bytes,
			&p.OtherSource.Files, &p.OtherSource.Bytes, &p.Unchecked.Files, &p.Unchecked.Bytes,
			&p.Unreadable.Files, &p.Unreadable.Bytes, &p.Disposable.Files, &p.Disposable.Bytes}
	},
}

// writeEntryPlaces upserts the rows of want that differ from the stored
// ones, each only while its entry exists, and deletes the rows of entries
// no longer in the snapshot (D4).
func (h *Handler) writeEntryPlaces(ctx context.Context, rt jobs.Runtime, want map[int64]domain.PlaceSums) error {
	return writeFigures(ctx, h, rt, entryPlacesTable, want)
}
