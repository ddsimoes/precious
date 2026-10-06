package api

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"fmt"
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
	return entryRow{
		ID: domain.Ref{Entry: r.ID, Member: r.Member}.String(), SourceID: r.Source,
		Name: domain.DisplayName(r.Name), NameB64: r.Name,
		Path: domain.DisplayName(r.Path), PathB64: r.Path,
		Kind:     r.Kind,
		FileKind: nonEmpty(&r.FileKind), MainKind: nonEmpty(&r.MainKind),
		Category: nonEmpty(&r.Category), Family: nonEmpty(&r.Family), Triage: nonEmpty(&r.Triage),
		Group: r.Group, Veto: r.Veto,
		Size: r.Size, TotalBytes: r.TotalBytes, TotalFiles: r.TotalFiles,
		MTime: nonZero(&r.MTime), Newest: nonZero(&r.Newest), Oldest: nonZero(&r.Oldest),
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

// nonZero is p, or nil for the zero time (unknown, null in JSON).
func nonZero(p *time.Time) *time.Time {
	if p.IsZero() {
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

type yearAmount struct {
	Year  int   `json:"year"`
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
}

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

// yearList lists the years ascending.
func (b *breakdowns) yearList() []yearAmount {
	out := make([]yearAmount, 0, len(b.years))
	for y, a := range b.years {
		out = append(out, yearAmount{Year: y, Bytes: a.Bytes, Files: a.Files})
	}
	slices.SortFunc(out, func(x, y yearAmount) int { return cmp.Compare(x.Year, y.Year) })
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
