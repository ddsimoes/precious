// Package search finds entries anywhere in the index by any combination of
// filters (§11.3, design D10/D11), pages the results by keyset cursor, and
// resolves a query to explicit entry IDs for selections.
//
// Filters, all optional and combined with AND; a list filter matches any of
// its values:
//
//   - Source: the entries of one source.
//   - Name: a case-insensitive substring of the display name
//     (domain.DisplayName). A name of three characters or more is looked up
//     in the trigram index entry_names. A shorter name cannot be: trigrams
//     need three characters. It is then tested on each row the other
//     filters select, as a byte substring of the raw name in any of its
//     Unicode simple case foldings, so a one- or two-character name with no
//     other filter reads every entry.
//   - Ext: the extension column (ASCII lower case, without the dot).
//   - FileKinds, Categories, Triages: the file_kind, category, and triage
//     columns.
//   - MinSize, MaxSize: total_bytes, inclusive (a file's size, a folder's
//     subtree sum).
//   - YearFrom, YearTo: the UTC year of mtime_ns, inclusive.
//   - Decisions: the effective decision (eff_decision), own or inherited.
//   - Tags: entries carrying a tag, own or inherited: each entry with an
//     own tag, plus its descendants as the range of paths below it on the
//     source's (source_id, path) index.
//   - Within: the descendants of an entry, the folder itself excluded, as the
//     same path range.
//   - Dup: the duplicate state of a present file (R2 design D15): copies
//     (another physical copy anywhere, archive members included),
//     elsewhere (one outside the Within folder; needs Within), unique, or
//     unchecked. It has no index and is tested on each candidate.
//
// The descendants of an entry at path p are the entries of its source whose
// path starts with p + "/" (every entry but the root, for the root): the
// byte range [p+"/", p+"0"), since '0' is the byte after '/'. A sibling
// such as "Fotos - Copia" is outside the range of "Fotos".
//
// One filter selects the candidate rows through an index, in this order of
// preference: a name of three characters or more (entry_names), a Within
// folder other than the root, tags (entry_tags_by_tag, then path ranges),
// and otherwise source and decision (entries_eff, or the (source_id, path)
// index). Every other filter is tested on those candidates. Extension, file
// kind, category, triage, size, year, and a short name have no index in the
// baseline schema, so a query made of them alone reads every entry of its
// source, or of the index without a source.
//
// Entries of every state (present, missing, unreadable) are found; each row
// carries its state.
//
// Results are ordered by Sort (bytes, files, newest, or name; default
// bytes) and Order (desc or asc; default desc, asc for name), ties broken
// by entry ID in the same direction. A cursor holds the last row's sort key
// and ID, so a page starts strictly after it: rows inserted while paging
// appear only when they sort after the cursor, and no earlier row is
// skipped or repeated. The match count is exact up to CountCap and reported
// as capped beyond.
package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"precious/internal/domain"
	"precious/internal/store"
)

// Query is a search: the filters above plus the result order. Its JSON form
// uses the URL parameter names of Parse, so a stored selection query reads
// like the search that made it.
type Query struct {
	Source     domain.SourceID   `json:"source,omitempty"`
	Name       string            `json:"name,omitempty"`
	Ext        []string          `json:"ext,omitempty"`
	FileKinds  []domain.FileKind `json:"file_kind,omitempty"`
	MinSize    *int64            `json:"min_size,omitempty"`
	MaxSize    *int64            `json:"max_size,omitempty"`
	YearFrom   *int              `json:"year_from,omitempty"`
	YearTo     *int              `json:"year_to,omitempty"`
	Categories []domain.Category `json:"category,omitempty"`
	Triages    []domain.Triage   `json:"triage,omitempty"`
	Tags       []int64           `json:"tag,omitempty"`
	Decisions  []domain.Decision `json:"decision,omitempty"`
	// Dup filters by duplicate state (R2 design D15, see dupFilter);
	// elsewhere requires Within.
	Dup    []domain.DupFilter `json:"dup,omitempty"`
	Within *domain.EntryID    `json:"within,omitempty,string"`
	Sort   string             `json:"sort,omitempty"`
	Order  string             `json:"order,omitempty"`
}

// Sort keys.
const (
	SortBytes  = "bytes"
	SortFiles  = "files"
	SortNewest = "newest"
	SortName   = "name"
)

// Orders.
const (
	OrderDesc = "desc"
	OrderAsc  = "asc"
)

const (
	// DefaultLimit is the page size when Page is given none.
	DefaultLimit = 200
	// MaxLimit caps a page.
	MaxLimit = 1000
	// CountCap is the largest exact count; beyond it a page reports
	// CountCapped (shown as "10000+").
	CountCap = 10000
	// MaxResolve is the largest max Resolve accepts.
	MaxResolve = 1_000_000
)

// Row is one result: the columns of the API's EntryRow.
type Row struct {
	ID     domain.EntryID
	Source domain.SourceID
	// Name and Path are raw bytes; Path is '/'-joined below the source root.
	Name, Path []byte
	// Kind is the entry kind; a special file has its platform kind (fifo,
	// socket, …).
	Kind     domain.EntryKind
	FileKind domain.FileKind // files only
	MainKind domain.FileKind // folders holding files only
	Category domain.Category
	Family   domain.Family
	Triage   domain.Triage
	Group    bool
	Veto     bool
	Size     int64
	// TotalBytes and TotalFiles are a file's size and 1, a folder's subtree
	// sums.
	TotalBytes, TotalFiles int64
	// MTime, Newest, and Oldest are zero when NULL; one at or before the
	// epoch is kept as stored but is not known either (KnownTime).
	MTime, Newest, Oldest time.Time
	State                 string // present, missing, unreadable
	Partial               bool
	MountBoundary         bool
	// Decision is the own decision, "" when inherited.
	Decision    domain.Decision
	EffDecision domain.Decision
	// TagIDs are the own tags, ascending.
	TagIDs []int64
	// Composition is the entry's bytes and files by family (design D21): a
	// folder's dir_stats.by_family, a file's size under its file family
	// (domain.FileFamily). Families without bytes or files are left out;
	// the others follow domain.Families order. Empty for other kinds and
	// for a folder not scanned yet.
	Composition []FamilyAmount

	// The R2 content fields (design D16), each null (zero, or not Valid)
	// where it does not apply.

	// ContentState is a file's or file member's content state.
	ContentState domain.ContentState
	// Copies is how many copies of the content exist, this one included.
	Copies sql.NullInt64
	// CandidateBytes, CheckedBytes, and DuplicatedBytes are a folder's
	// figures from dir_dups.
	CandidateBytes, CheckedBytes, DuplicatedBytes sql.NullInt64
	// ArchiveState is an archive file's listing state (a domain.ArchiveState
	// value), "" when it was not listed.
	ArchiveState string
	// Member is set, and ID is 0, on the row of an archive member; ArchiveID
	// is then the archive's entry, MemberPath the member's path inside the
	// archive (the end of Path, after '!'), and Zip whether the archive is a
	// zip, whose names display through domain.MemberDisplayName.
	Member     domain.MemberID
	ArchiveID  domain.EntryID
	MemberPath []byte
	Zip        bool
}

// FamilyAmount is one family's share of a composition.
type FamilyAmount struct {
	Family domain.Family `json:"family"`
	Bytes  int64         `json:"bytes"`
	Files  int64         `json:"files"`
}

// Result is one page.
type Result struct {
	Items []Row
	// NextCursor continues after the last item; "" on the last page.
	NextCursor string
	// Count is the number of matches, at most CountCap. CountCapped
	// reports more than CountCap.
	Count       int
	CountCapped bool
}

// Page returns the matches of query after cursor ("" for the first page),
// at most limit of them (DefaultLimit when limit ≤ 0, at most MaxLimit). A
// bad query value or cursor is invalid_request; an unknown Within entry is
// not_found. Pass a read transaction as q for a page and count from one
// snapshot.
func Page(ctx context.Context, q store.Queryer, query Query, cursor string, limit int) (Result, error) {
	if err := query.validate(); err != nil {
		return Result{}, err
	}
	s := query.sortSpec()
	var after *position
	if cursor != "" {
		p, err := decodeCursor(cursor, s)
		if err != nil {
			return Result{}, err
		}
		after = &p
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	f, err := buildFilter(ctx, q, query)
	if err != nil {
		return Result{}, err
	}
	res, err := page(ctx, q, f, s, after, limit)
	if err != nil {
		return Result{}, err
	}
	if res.Count, res.CountCapped, err = count(ctx, q, f); err != nil {
		return Result{}, err
	}
	if res.Items == nil {
		res.Items = []Row{}
	}
	return res, nil
}

// Resolve returns the IDs of every match of query, ascending, for a
// selection (design D10). The sort fields are ignored. A query matching
// more than max entries is invalid_request, so a selection is never a silent
// subset; max must be in 1..MaxResolve. Pass the command's transaction as q
// so the IDs are those of the committed index it writes against.
func Resolve(ctx context.Context, q store.Queryer, query Query, max int) ([]domain.EntryID, error) {
	if max < 1 || max > MaxResolve {
		return nil, fmt.Errorf("search: resolve max %d outside 1..%d", max, MaxResolve)
	}
	if err := query.validate(); err != nil {
		return nil, err
	}
	f, err := buildFilter(ctx, q, query)
	if err != nil {
		return nil, err
	}
	ids := []domain.EntryID{}
	stmt, args := resolveSQL(f, max+1)
	rows, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("search: resolve: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("search: resolve: %w", err)
		}
		ids = append(ids, domain.EntryID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: resolve: %w", err)
	}
	if len(ids) > max {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "the search matches more than %d entries; narrow it", max)
	}
	return ids, nil
}

// Columns are the columns of a Row, in the order ScanRow reads them, for a
// statement over From. The R2 fields: a file's content state, its copies
// (copiesColumn), a folder's dir_dups figures, and an archive's listing
// state (NULL while it is being listed, which readers ignore).
var Columns = `e.id, e.source_id, e.name, e.path, e.kind, e.special_kind, e.file_kind, e.main_kind,
	e.category, e.family, e.triage, e.is_group, e.veto, e.size, e.total_bytes, e.total_files,
	e.mtime_ns, e.newest_ns, e.oldest_ns, e.state, e.partial, e.mount_boundary, e.decision, e.eff_decision,
	ds.by_family,
	fc.state AS content_state, ` + copiesColumn + ` AS copies,
	dd.candidate_bytes, dd.checked_bytes, dd.duplicated_bytes,
	CASE WHEN ar.state <> 'listing' THEN ar.state END AS archive_state, NULL AS archive_id`

// From is what Columns read: entries aliased e, with a folder's dir_stats
// (ds) and dir_dups (dd), a file's file_content (fc), and an archive's
// archives row (ar), each joined on its primary key.
const From = `entries e LEFT JOIN dir_stats ds ON ds.entry_id = e.id
	LEFT JOIN file_content fc ON fc.entry_id = e.id
	LEFT JOIN dir_dups dd ON dd.entry_id = e.id
	LEFT JOIN archives ar ON ar.entry_id = e.id`

// pageSQL is the page statement of f in order s, starting after a position
// when after is set.
func pageSQL(f filter, s sortSpec, after *position, limit int) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT `)
	b.WriteString(s.expr)
	b.WriteString(`, `)
	b.WriteString(Columns)
	b.WriteString(` FROM `)
	b.WriteString(From)
	b.WriteString(` WHERE `)
	b.WriteString(f.where)
	args := append([]any(nil), f.args...)
	cmp := ">"
	if s.desc {
		cmp = "<"
	}
	if after != nil {
		fmt.Fprintf(&b, ` AND (%s, e.id) %s (?, ?)`, s.expr, cmp)
		args = append(args, after.key(s), int64(after.ID))
	}
	dir := " ASC"
	if s.desc {
		dir = " DESC"
	}
	fmt.Fprintf(&b, ` ORDER BY %s%s, e.id%s LIMIT ?`, s.expr, dir, dir)
	args = append(args, limit+1)
	return b.String(), args
}

func page(ctx context.Context, q store.Queryer, f filter, s sortSpec, after *position, limit int) (Result, error) {
	query, args := pageSQL(f, s, after, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return Result{}, fmt.Errorf("search: page: %w", err)
	}
	defer rows.Close()
	var (
		res  Result
		last position
	)
	for rows.Next() {
		if len(res.Items) == limit {
			res.NextCursor = encodeCursor(s, last)
			break
		}
		r, key, err := scanRow(rows, s)
		if err != nil {
			return Result{}, fmt.Errorf("search: page: %w", err)
		}
		res.Items = append(res.Items, r)
		last = key
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("search: page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Result{}, fmt.Errorf("search: page: %w", err)
	}
	if err := LoadTags(ctx, q, res.Items); err != nil {
		return Result{}, err
	}
	return res, nil
}

// scanRow reads one page row: the sort key, then Columns.
func scanRow(rows *sql.Rows, s sortSpec) (Row, position, error) {
	var p position
	keyDest := any(&p.N)
	if s.key == SortName {
		keyDest = &p.B
	}
	r, err := ScanRow(rows, keyDest)
	if err != nil {
		return Row{}, position{}, err
	}
	p.ID = r.ID
	return r, p, nil
}

// ScanRow reads the current row of rows: the lead destinations, then
// Columns. TagIDs are left for LoadTags.
func ScanRow(rows *sql.Rows, lead ...any) (Row, error) {
	var (
		r                                Row
		id                               int64
		source, kind, state, eff         string
		special, fileKind, mainKind      sql.NullString
		category, family, triage, decide sql.NullString
		group, veto, partial, boundary   bool
		mtime, newest, oldest            sql.NullInt64
		byFamily                         []byte
		contentState, archiveState       sql.NullString
		archiveID                        sql.NullInt64
	)
	dest := append(lead[:len(lead):len(lead)], &id, &source, &r.Name, &r.Path, &kind, &special, &fileKind, &mainKind,
		&category, &family, &triage, &group, &veto, &r.Size, &r.TotalBytes, &r.TotalFiles,
		&mtime, &newest, &oldest, &state, &partial, &boundary, &decide, &eff, &byFamily,
		&contentState, &r.Copies, &r.CandidateBytes, &r.CheckedBytes, &r.DuplicatedBytes, &archiveState, &archiveID)
	if err := rows.Scan(dest...); err != nil {
		return Row{}, err
	}
	r.ID = domain.EntryID(id)
	r.Source = domain.SourceID(source)
	if r.Name == nil {
		// The root's empty name and path are empty, not absent.
		r.Name, r.Path = []byte{}, []byte{}
	}
	r.Kind = domain.EntryKind(kind)
	if kind == "special" {
		r.Kind = domain.EntryKind(special.String)
	}
	r.FileKind = domain.FileKind(fileKind.String)
	r.MainKind = domain.FileKind(mainKind.String)
	r.Category = domain.Category(category.String)
	r.Family = domain.Family(family.String)
	r.Triage = domain.Triage(triage.String)
	r.Group, r.Veto, r.Partial, r.MountBoundary = group, veto, partial, boundary
	r.MTime, r.Newest, r.Oldest = NsTime(mtime), NsTime(newest), NsTime(oldest)
	r.State = state
	r.Decision = domain.Decision(decide.String)
	r.EffDecision = domain.Decision(eff)
	r.ContentState = domain.ContentState(contentState.String)
	r.ArchiveState = archiveState.String
	r.ArchiveID = domain.EntryID(archiveID.Int64)
	switch r.Kind {
	case domain.EntryFile:
		r.Composition = []FamilyAmount{{Family: domain.FileFamily(r.Category, r.FileKind), Bytes: r.Size, Files: 1}}
	case domain.EntryDirectory:
		var err error
		if r.Composition, err = composition(byFamily); err != nil {
			return Row{}, fmt.Errorf("entry %s: %w", r.ID, err)
		}
	default:
		r.Composition = []FamilyAmount{}
	}
	return r, nil
}

// familyCells is dir_stats.by_family as the scanner writes it.
type familyCells struct {
	Personal   familyCell `json:"personal"`
	Programs   familyCell `json:"programs"`
	Disposable familyCell `json:"disposable"`
	Containers familyCell `json:"containers"`
}

type familyCell struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// composition lists a by_family column's non-empty families in
// domain.Families order; NULL (a folder not scanned yet) has none.
func composition(raw []byte) ([]FamilyAmount, error) {
	if raw == nil {
		return []FamilyAmount{}, nil
	}
	var c familyCells
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("by_family %s: %w", raw, err)
	}
	out := make([]FamilyAmount, 0, len(domain.Families))
	for _, a := range [...]FamilyAmount{
		{Family: domain.FamilyPersonal, Bytes: c.Personal.Bytes, Files: c.Personal.Files},
		{Family: domain.FamilyPrograms, Bytes: c.Programs.Bytes, Files: c.Programs.Files},
		{Family: domain.FamilyDisposable, Bytes: c.Disposable.Bytes, Files: c.Disposable.Files},
		{Family: domain.FamilyContainers, Bytes: c.Containers.Bytes, Files: c.Containers.Files},
	} {
		if a.Bytes != 0 || a.Files != 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

// NsTime is a stored time, the zero time when NULL. A time on the epoch's
// first day is kept as stored, since orders and cursors read the stored
// column, but it is not known (KnownTime).
func NsTime(ns sql.NullInt64) time.Time {
	if !ns.Valid {
		return time.Time{}
	}
	return time.Unix(0, ns.Int64).UTC()
}

// KnownTime reports whether t is a known modification time: neither zero
// (NULL) nor a placeholder for a lost time (domain.KnownModTime).
func KnownTime(t time.Time) bool { return !t.IsZero() && domain.KnownModTime(t.UnixNano()) }

// LoadTags fills the own tag IDs of items, ascending, with one indexed
// read.
func LoadTags(ctx context.Context, q store.Queryer, items []Row) error {
	if len(items) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`SELECT entry_id, tag_id FROM entry_tags WHERE entry_id IN (`)
	args := make([]any, len(items))
	at := make(map[domain.EntryID]int, len(items))
	for i := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args[i] = int64(items[i].ID)
		at[items[i].ID] = i
	}
	b.WriteString(`) ORDER BY entry_id, tag_id`)
	rows, err := q.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("search: tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry, tag int64
		if err := rows.Scan(&entry, &tag); err != nil {
			return fmt.Errorf("search: tags: %w", err)
		}
		i := at[domain.EntryID(entry)]
		items[i].TagIDs = append(items[i].TagIDs, tag)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("search: tags: %w", err)
	}
	return nil
}

// count counts the matches of f, stopping one past CountCap.
func count(ctx context.Context, q store.Queryer, f filter) (int, bool, error) {
	var n int
	stmt, args := countSQL(f)
	err := q.QueryRowContext(ctx, stmt, args...).Scan(&n)
	if err != nil {
		return 0, false, fmt.Errorf("search: count: %w", err)
	}
	if n > CountCap {
		return CountCap, true, nil
	}
	return n, false, nil
}

// countSQL counts the matches of f up to one past CountCap.
func countSQL(f filter) (string, []any) {
	return `SELECT count(*) FROM (SELECT 1 FROM entries e WHERE ` + f.where + ` LIMIT ?)`,
		append(f.args[:len(f.args):len(f.args)], CountCap+1)
}

// resolveSQL lists the IDs of up to limit matches of f, ascending.
func resolveSQL(f filter, limit int) (string, []any) {
	return `SELECT e.id FROM entries e WHERE ` + f.where + ` ORDER BY e.id LIMIT ?`,
		append(f.args[:len(f.args):len(f.args)], limit)
}
