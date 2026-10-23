package api

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"precious/internal/domain"
	"precious/internal/search"
)

// entryRow is EntryRow of the Interfaces section: the row of children,
// treemap, and search, and the entry of the detail. Its pointers point into
// the search.Row it is made from, nil for null.
type entryRow struct {
	ID            string           `json:"id"`
	SourceID      domain.SourceID  `json:"source_id"`
	Name          string           `json:"name"`
	NameB64       []byte           `json:"name_b64"`
	Path          string           `json:"path"`
	PathB64       []byte           `json:"path_b64"`
	Kind          domain.EntryKind `json:"kind"`
	FileKind      *domain.FileKind `json:"file_kind"`
	MainKind      *domain.FileKind `json:"main_kind"`
	Category      *domain.Category `json:"category"`
	Family        *domain.Family   `json:"family"`
	Triage        *domain.Triage   `json:"triage"`
	Group         bool             `json:"group"`
	Veto          bool             `json:"veto"`
	Size          int64            `json:"size"`
	TotalBytes    int64            `json:"total_bytes"`
	TotalFiles    int64            `json:"total_files"`
	MTime         *time.Time       `json:"mtime"`
	Newest        *time.Time       `json:"newest"`
	Oldest        *time.Time       `json:"oldest"`
	State         string           `json:"state"`
	Partial       bool             `json:"partial"`
	MountBoundary bool             `json:"mount_boundary"`
	Decision      *domain.Decision `json:"decision"`
	EffDecision   domain.Decision  `json:"eff_decision"`
	TagIDs        []int64          `json:"tag_ids"`
	// Composition is the entry's bytes and files by family (design D21).
	Composition []search.FamilyAmount `json:"composition"`
	// The R2 content fields (R2 design D16), null where they do not apply.
	ContentState    *domain.ContentState `json:"content_state"`
	Copies          *int64               `json:"copies"`
	CandidateBytes  *int64               `json:"candidate_bytes"`
	CheckedBytes    *int64               `json:"checked_bytes"`
	DuplicatedBytes *int64               `json:"duplicated_bytes"`
	ArchiveState    *string              `json:"archive_state"`
	ArchiveID       *string              `json:"archive_id"`
	// The r5c place fields (r5c design Interfaces), sent only with
	// places=1: nil leaves every one of them out.
	*rowPlaces
}

// rowPlaces is the place fields of an EntryRow (r5c design D1, D5, D9,
// D11, D14, Interfaces). member_places goes on the entry of the entry
// response only, other_copy and covered_by on children rows only: each pair
// is left out where its pointer is nil.
type rowPlaces struct {
	Place        *domain.Place  `json:"place"`
	PlaceOffline bool           `json:"place_offline"`
	Disposable   bool           `json:"disposable"`
	Places       *placeSumsJSON `json:"places"`
	*memberPlacesJSON
	*rowCopyJSON
}

// memberPlacesJSON is a complete archive's unpacked member sums with its
// own coverage applied (D5, D6); null for anything else.
type memberPlacesJSON struct {
	MemberPlaces *placeSumsJSON `json:"member_places"`
}

// rowCopyJSON is what names a row's other copy (D9).
type rowCopyJSON struct {
	OtherCopy *otherCopyJSON `json:"other_copy"`
	CoveredBy *coveredByJSON `json:"covered_by"`
}

// placeSumsJSON is domain.PlaceSums as JSON.
type placeSumsJSON struct {
	OnlyCopy    amount `json:"only_copy"`
	ThisSource  amount `json:"this_source"`
	OtherSource amount `json:"other_source"`
	Unchecked   amount `json:"unchecked"`
	Unreadable  amount `json:"unreadable"`
	Disposable  amount `json:"disposable"`
}

// sumsJSON is p as JSON, nil for nil.
func sumsJSON(p *domain.PlaceSums) *placeSumsJSON {
	if p == nil {
		return nil
	}
	a := func(x domain.PlaceAmount) amount { return amount{Files: x.Files, Bytes: x.Bytes} }
	return &placeSumsJSON{OnlyCopy: a(p.OnlyCopy), ThisSource: a(p.ThisSource), OtherSource: a(p.OtherSource),
		Unchecked: a(p.Unchecked), Unreadable: a(p.Unreadable), Disposable: a(p.Disposable)}
}

// otherCopyJSON is the one copy D9 names for a file or member placed
// this_source or other_source. ViaArchive marks the copy of a member's
// archive, named because the member is placed through the archive's own
// coverage (D6); More is the content's physical copies minus 2. A member
// copy also carries its archive entry's display path and its own path
// inside the archive, which a '!' in either keeps Path from telling apart;
// both are left out for an entry copy.
type otherCopyJSON struct {
	Ref         string          `json:"ref"`
	SourceID    domain.SourceID `json:"source_id"`
	Path        string          `json:"path"`
	PathB64     []byte          `json:"path_b64"`
	ArchiveID   *string         `json:"archive_id"`
	ArchivePath *string         `json:"archive_path,omitempty"`
	MemberPath  *string         `json:"member_path,omitempty"`
	Offline     bool            `json:"offline"`
	ViaArchive  bool            `json:"via_archive"`
	More        int             `json:"more"`
}

// coveredByJSON is the relation D9 names for a folder, complete archive,
// or member folder: its kind, and the other side.
type coveredByJSON struct {
	Kind  string   `json:"kind"`
	Other sideJSON `json:"other"`
}

// sideJSON names a relation's side: an entry, or a member folder ("m<id>",
// its path the archive's, '!', and its path inside the archive).
type sideJSON struct {
	ID       string          `json:"id"`
	SourceID domain.SourceID `json:"source_id"`
	Path     string          `json:"path"`
	PathB64  []byte          `json:"path_b64"`
}

// placeFacts are what a folder's or complete archive's completeness reads
// (D1): its state, partial, and mount_boundary, and the unreadable folders
// and mount boundaries below it (dir_stats; 0 for an archive).
type placeFacts struct {
	State                        string
	Partial, MountBoundary       bool
	UnreadableBelow, MountsBelow int64
}

// complete reports whether a folder or complete archive was read
// completely (D1): not unreadable, partial, or a mount boundary itself,
// with no unreadable folder or mount boundary below it. This is the only
// place that decides it; domain.PlaceSums.Place takes the answer.
func complete(f placeFacts) bool {
	return f.State != "unreadable" && !f.Partial && !f.MountBoundary && f.UnreadableBelow == 0 && f.MountsBelow == 0
}

// placed is a row's place (D1, D2, D5, D6, D11, D14): what rowPlaces
// shows, and D10's sort key and hide test.
type placed struct {
	// place is "" for null: a missing or quarantined entry.
	place      domain.Place
	offline    bool
	disposable bool
	// level is set for a folder, a complete archive, and a member folder:
	// a row that sums its files, and that covered_by may name.
	level bool
	// sums are a level's figures: stored for a folder or complete archive
	// (nil while not computed), unpacked for a member folder.
	sums *domain.PlaceSums
	// size is a file's or member's size, its key.
	size int64
	// via is set for a member placed through its archive's coverage (D6).
	via bool
	// copy and covered are D9's names, read for children rows.
	copy    *otherCopyJSON
	covered *coveredByJSON
}

// key is D10's only_here key: the bytes only on this source, and the
// bytes not checked or unreadable. A level reads its sums, and
// math.MaxInt64 while not computed; a file its size when it is placed
// only_copy or this_source (first key) or unchecked or unreadable (second
// key) and is not disposable; null is 0.
func (p *placed) key() (n, u int64) {
	switch {
	case p.place == "":
		return 0, 0
	case p.place == domain.PlaceNotComputed:
		return math.MaxInt64, 0
	case p.level:
		if p.sums == nil {
			return 0, 0
		}
		return p.sums.OnlyHereBytes(), p.sums.Unchecked.Bytes + p.sums.Unreadable.Bytes
	case p.disposable:
		return 0, 0
	}
	switch p.place {
	case domain.PlaceOnlyCopy, domain.PlaceThisSource:
		return p.size, 0
	case domain.PlaceUnchecked, domain.PlaceUnreadable:
		return 0, p.size
	}
	return 0, 0
}

// kept reports whether hide=other_source keeps the row (D10): placed
// only_copy, this_source, unchecked, unreadable, or not_computed, and not a
// disposable file.
func (p *placed) kept() bool {
	if p.disposable {
		return false
	}
	switch p.place {
	case domain.PlaceOnlyCopy, domain.PlaceThisSource, domain.PlaceUnchecked, domain.PlaceUnreadable,
		domain.PlaceNotComputed:
		return true
	}
	return false
}

// json is p's fields, with other_copy and covered_by when copies is set.
func (p *placed) json(copies bool) *rowPlaces {
	out := &rowPlaces{PlaceOffline: p.offline, Disposable: p.disposable, Places: sumsJSON(p.sums)}
	if p.place != "" {
		place := p.place
		out.Place = &place
	}
	if copies {
		out.rowCopyJSON = &rowCopyJSON{OtherCopy: p.copy, CoveredBy: p.covered}
	}
	return out
}

// placedJSON returns the EntryRows of rows with their places pl, with
// other_copy and covered_by when copies is set.
func placedJSON(rows []search.Row, pl []*placed, copies bool) []entryRow {
	out := rowsJSON(rows)
	for i := range out {
		out[i].rowPlaces = pl[i].json(copies)
	}
	return out
}

// noTags is the tag_ids of an entry without own tags: [] rather than null.
var noTags = []int64{}

// rowJSON returns the EntryRow of r, which must outlive it.
func rowJSON(r *search.Row) entryRow {
	tags := r.TagIDs
	if tags == nil {
		tags = noTags
	}
	var archiveID *string
	if r.ArchiveID != 0 {
		s := r.ArchiveID.String()
		archiveID = &s
	}
	name, path := domain.DisplayName(r.Name), domain.DisplayName(r.Path)
	if r.Member != 0 && r.Zip {
		name = domain.MemberDisplayName(r.Name, true)
		path = domain.DisplayName(r.Path[:len(r.Path)-len(r.MemberPath)]) + domain.MemberDisplayName(r.MemberPath, true)
	}
	return entryRow{
		ID: domain.Ref{Entry: r.ID, Member: r.Member}.String(), SourceID: r.Source,
		Name: name, NameB64: r.Name,
		Path: path, PathB64: r.Path,
		Kind:     r.Kind,
		FileKind: nonEmpty(&r.FileKind), MainKind: nonEmpty(&r.MainKind),
		Category: nonEmpty(&r.Category), Family: nonEmpty(&r.Family), Triage: nonEmpty(&r.Triage),
		Group: r.Group, Veto: r.Veto,
		Size: r.Size, TotalBytes: r.TotalBytes, TotalFiles: r.TotalFiles,
		MTime: known(&r.MTime), Newest: known(&r.Newest), Oldest: known(&r.Oldest),
		State: r.State, Partial: r.Partial, MountBoundary: r.MountBoundary,
		Decision: nonEmpty(&r.Decision), EffDecision: r.EffDecision,
		TagIDs:       tags,
		Composition:  r.Composition,
		ContentState: nonEmpty(&r.ContentState),
		Copies:       nullInt(&r.Copies), CandidateBytes: nullInt(&r.CandidateBytes),
		CheckedBytes: nullInt(&r.CheckedBytes), DuplicatedBytes: nullInt(&r.DuplicatedBytes),
		ArchiveState: nonEmpty(&r.ArchiveState),
		ArchiveID:    archiveID,
	}
}

// rowsJSON returns the EntryRows of rows, which must outlive them.
func rowsJSON(rows []search.Row) []entryRow {
	out := make([]entryRow, len(rows))
	for i := range rows {
		out[i] = rowJSON(&rows[i])
	}
	return out
}

// nonEmpty is p, or nil for the empty value (null in JSON).
func nonEmpty[T ~string](p *T) *T {
	if *p == "" {
		return nil
	}
	return p
}

// known is p, or nil for a time that is not known (search.KnownTime): NULL,
// or a placeholder for a lost time (null in JSON).
func known(p *time.Time) *time.Time {
	if !search.KnownTime(*p) {
		return nil
	}
	return p
}

// nullInt is p's value, or nil when it is NULL.
func nullInt(p *sql.NullInt64) *int64 {
	if !p.Valid {
		return nil
	}
	return &p.Int64
}

// amount is one cell of a breakdown, as dir_stats stores it.
type amount struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

func (a *amount) add(o amount) {
	a.Files += o.Files
	a.Bytes += o.Bytes
}

type kindAmount struct {
	Kind  domain.FileKind `json:"kind"`
	Bytes int64           `json:"bytes"`
	Files int64           `json:"files"`
}

// yearAmount is one year of a by_year breakdown; Year is null for the
// files without a known modification time.
type yearAmount struct {
	Year  *int  `json:"year"`
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
}

// unknownYear is the by_year key of the files without a known time, as
// dir_stats stores it.
const unknownYear = 0

// breakdowns sums dir_stats breakdowns: by_kind, by_year, and by_family.
type breakdowns struct {
	kinds    map[domain.FileKind]amount
	years    map[int]amount
	families map[domain.Family]amount
}

func newBreakdowns() *breakdowns {
	b := &breakdowns{
		kinds:    map[domain.FileKind]amount{},
		years:    map[int]amount{},
		families: make(map[domain.Family]amount, len(domain.Families)),
	}
	for _, f := range domain.Families {
		b.families[f] = amount{}
	}
	return b
}

func (b *breakdowns) addKinds(raw []byte) error {
	return addCells(b.kinds, raw, func(k string) (domain.FileKind, error) { return domain.FileKind(k), nil })
}

func (b *breakdowns) addYears(raw []byte) error {
	return addCells(b.years, raw, strconv.Atoi)
}

func (b *breakdowns) addFamilies(raw []byte) error {
	return addCells(b.families, raw, func(k string) (domain.Family, error) { return domain.Family(k), nil })
}

// addCells adds the cells of a breakdown object ({"key":{"files","bytes"}})
// to dst.
func addCells[K comparable](dst map[K]amount, raw []byte, key func(string) (K, error)) error {
	var cells map[string]amount
	if err := json.Unmarshal(raw, &cells); err != nil {
		return fmt.Errorf("api: breakdown %s: %w", raw, err)
	}
	for k, c := range cells {
		kk, err := key(k)
		if err != nil {
			return fmt.Errorf("api: breakdown key %q: %w", k, err)
		}
		a := dst[kk]
		a.add(c)
		dst[kk] = a
	}
	return nil
}

// kindList lists the kinds in §6.2 order, any other after them by name.
func (b *breakdowns) kindList() []kindAmount {
	out := make([]kindAmount, 0, len(b.kinds))
	for k, a := range b.kinds {
		out = append(out, kindAmount{Kind: k, Bytes: a.Bytes, Files: a.Files})
	}
	slices.SortFunc(out, func(x, y kindAmount) int {
		return cmp.Or(cmp.Compare(rank(domain.FileKinds, x.Kind), rank(domain.FileKinds, y.Kind)),
			strings.Compare(string(x.Kind), string(y.Kind)))
	})
	return out
}

// yearList lists the years ascending, then the unknown year.
func (b *breakdowns) yearList() []yearAmount {
	years := slices.Sorted(maps.Keys(b.years))
	out := make([]yearAmount, 0, len(years))
	for _, y := range years {
		if y != unknownYear {
			a := b.years[y]
			out = append(out, yearAmount{Year: &y, Bytes: a.Bytes, Files: a.Files})
		}
	}
	if a, ok := b.years[unknownYear]; ok {
		out = append(out, yearAmount{Bytes: a.Bytes, Files: a.Files})
	}
	return out
}

// familyList lists every family in §6.6 order, any other after them by
// name.
func (b *breakdowns) familyList() []search.FamilyAmount {
	out := make([]search.FamilyAmount, 0, len(b.families))
	for f, a := range b.families {
		out = append(out, search.FamilyAmount{Family: f, Bytes: a.Bytes, Files: a.Files})
	}
	slices.SortFunc(out, func(x, y search.FamilyAmount) int {
		return cmp.Or(cmp.Compare(rank(domain.Families, x.Family), rank(domain.Families, y.Family)),
			strings.Compare(string(x.Family), string(y.Family)))
	})
	return out
}

// rank is the position of v in order, or len(order) when absent.
func rank[T comparable](order []T, v T) int {
	if i := slices.Index(order, v); i >= 0 {
		return i
	}
	return len(order)
}
