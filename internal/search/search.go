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
//     in the trigram index entry_names, whose tokenizer folds accents in
//     the indexed names and in the searched name alike (r2b design D7):
//     "confraternizacao" finds "Confraternização 2018". A shorter name
//     cannot be looked up: trigrams need three characters. It is then
//     tested on each row the other filters select, as a byte substring of
//     the raw name in any of its Unicode simple case foldings, without
//     folding accents ("çã" finds "AÇÃO", "ca" does not), so a one- or
//     two-character name with no other filter reads every entry.
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
//     unchecked.
//   - State: StateUnreadable for the folders and files that could not be
//     read, through the partial index entries_unreadable.
//
// The descendants of an entry at path p are the entries of its source whose
// path starts with p + "/" (every entry but the root, for the root): the
// byte range [p+"/", p+"0"), since '0' is the byte after '/'. A sibling
// such as "Fotos - Copia" is outside the range of "Fotos".
//
// One filter selects the candidate rows through an index, in this order of
// preference: State (entries_unreadable), a name of three characters or more
// (entry_names), a Within folder other than the root, tags
// (entry_tags_by_tag, then path ranges), Dup, and otherwise source and
// decision (entries_eff, or the (source_id, path) index).
// Every other filter is tested on those candidates. Dup selects the files
// of the contents with two copies or more (file_content_by_content, for
// copies and elsewhere), or the files in the content states its values need
// (file_content_by_source), and tests the exact duplicate state on those
// only (r2b design D8). Extension, file kind, category, triage, size, year,
// and a short name have no index, so a query made of them alone reads every
// entry of its source, or of the index without a source, in the order of
// the page when it is by bytes.
//
// Entries of every state (present, missing, unreadable) are found; each row
// carries its state. A source's quarantine folder and everything below it
// are never found (r4 design D2), unless InQuarantine asks for them alone;
// a quarantined copy is no copy (dupFilter, CopiesSQL).
//
// Results are ordered by Sort (bytes, files, newest, or name; default
// bytes) and Order (desc or asc; default desc, asc for name), ties broken
// by entry ID in the same direction. A page by bytes, the default, that the
// duplicate filter drives merges size-ordered ranges and stops after the
// page (Page); a page with no filter reads every entry once. A cursor
// holds the last row's sort key and ID, so a page starts strictly after it:
// rows inserted while paging appear only when they sort after the cursor,
// and no earlier row is skipped or repeated. The match count (Count) is a
// request of its own, exact up to CountCap and reported as capped beyond.
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
	Dup []domain.DupFilter `json:"dup,omitempty"`
	// State is StateUnreadable for the entries that could not be read, ""
	// for every state.
	State  string          `json:"state,omitempty"`
	Within *domain.EntryID `json:"within,omitempty,string"`
	Sort   string          `json:"sort,omitempty"`
	Order  string          `json:"order,omitempty"`
	// InQuarantine searches the quarantine folders and their contents
	// only, for the Cleanup screen; every other search leaves them out
	// (r4 design D2). It has no URL parameter, and a stored selection
	// never carries it.
	InQuarantine bool `json:"-"`
}

// StateUnreadable is the State of the entries that could not be read.
const StateUnreadable = "unreadable"

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
}

// Page returns the matches of query after cursor ("" for the first page),
// at most limit of them (DefaultLimit when limit ≤ 0, at most MaxLimit). A
// bad query value or cursor is invalid_request; an unknown Within entry is
// not_found. Pass a read transaction as q so that the page's two
// statements read one snapshot.
//
// The page is read in two stages (r2b design D8): the first selects the
// IDs of the page's rows with the filter, the order, and the limit; the
// second reads the full columns, copies included, for those rows only.
//
// A page by bytes that the duplicate filter drives reads the files of
// each source and content state it needs in size order, through
// file_content_by_source, and merges them (dupArms), so it stops after the
// page; other orders sort every match.
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

	f, err := buildFilter(ctx, q, query, s.key == SortBytes)
	if err != nil {
		return Result{}, err
	}
	res, err := page(ctx, q, f, s, after, limit)
	if err != nil {
		return Result{}, err
	}
	if res.Items == nil {
		res.Items = []Row{}
	}
	return res, nil
}

// Count returns the number of matches of query, at most CountCap; capped
// reports more than CountCap. It is a request of its own (r2b design D8),
// so a page never waits for its count. When the duplicate filter drives,
// copies and elsewhere count the files of the contents with copies; the
// other values read their content states' ranges in turn, those that need
// no copy test first (dupStates), so a capped count stops early.
func Count(ctx context.Context, q store.Queryer, query Query) (n int, capped bool, err error) {
	if err := query.validate(); err != nil {
		return 0, false, err
	}
	f, err := buildFilter(ctx, q, query, !copiesOnly(query.Dup))
	if err != nil {
		return 0, false, err
	}
	if f.arms != nil && len(f.arms) == 0 {
		return 0, false, nil // no source
	}
	return count(ctx, q, f)
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
	f, err := buildFilter(ctx, q, query, false)
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

// idsSQL is the first stage of a page (r2b design D8): the sort key and ID
// of the rows of f in order s, starting after a position when after is
// set, one row past limit. Only driveColumns may read entries in its sort
// index's order, and f.arms are each read in size order and merged; for the
// other drivers a unary + keeps the order from an index, so their rows are
// selected first and then sorted.
func idsSQL(f filter, s sortSpec, after *position, limit int) (string, []any) {
	cmp, dir := ">", " ASC"
	if s.desc {
		cmp, dir = "<", " DESC"
	}
	var (
		b    strings.Builder
		args []any
	)
	if f.arms != nil {
		// A compound SELECT whose arms are each in its ORDER BY's order is
		// a merge (MERGE (UNION ALL) in the plan), which stops at the limit.
		for i, a := range f.arms {
			if i > 0 {
				b.WriteString(` UNION ALL `)
			}
			fmt.Fprintf(&b, `SELECT df.size, df.entry_id FROM %s WHERE df.source_id = ? AND df.state = ? AND %s`,
				f.from, f.where)
			args = append(append(args, a.source, a.state), f.args...)
			if after != nil {
				fmt.Fprintf(&b, ` AND (df.size, df.entry_id) %s (?, ?)`, cmp)
				args = append(args, after.key(s), int64(after.ID))
			}
		}
		fmt.Fprintf(&b, ` ORDER BY 1%s, 2%s LIMIT ?`, dir, dir)
		return b.String(), append(args, limit+1)
	}
	expr := s.expr
	if !f.ordered() {
		expr = `+` + expr
	}
	fmt.Fprintf(&b, `SELECT %s, e.id FROM %s WHERE %s`, expr, f.from, f.where)
	args = append(args, f.args...)
	if after != nil {
		fmt.Fprintf(&b, ` AND (%s, e.id) %s (?, ?)`, expr, cmp)
		args = append(args, after.key(s), int64(after.ID))
	}
	fmt.Fprintf(&b, ` ORDER BY %s%s, e.id%s LIMIT ?`, expr, dir, dir)
	return b.String(), append(args, limit+1)
}

// rowsSQL is the second stage of a page: the Columns of n entries by ID.
func rowsSQL(n int) string {
	var b strings.Builder
	b.WriteString(`SELECT `)
	b.WriteString(Columns)
	b.WriteString(` FROM `)
	b.WriteString(From)
	b.WriteString(` WHERE e.id IN (`)
	writeMarks(&b, n)
	b.WriteByte(')')
	return b.String()
}

func page(ctx context.Context, q store.Queryer, f filter, s sortSpec, after *position, limit int) (Result, error) {
	if f.arms != nil && len(f.arms) == 0 {
		return Result{}, nil // no source
	}
	stmt, args := idsSQL(f, s, after, limit)
	keys, err := pageKeys(ctx, q, stmt, args, s)
	if err != nil {
		return Result{}, fmt.Errorf("search: page: %w", err)
	}
	var res Result
	if len(keys) > limit {
		keys = keys[:limit]
		res.NextCursor = encodeCursor(s, keys[limit-1])
	}
	if len(keys) == 0 {
		return res, nil
	}
	ids := make([]any, len(keys))
	at := make(map[domain.EntryID]int, len(keys))
	for i, k := range keys {
		ids[i] = int64(k.ID)
		at[k.ID] = i
	}
	rows, err := q.QueryContext(ctx, rowsSQL(len(ids)), ids...)
	if err != nil {
		return Result{}, fmt.Errorf("search: page rows: %w", err)
	}
	defer rows.Close()
	items := make([]Row, len(keys))
	found := make([]bool, len(keys))
	for rows.Next() {
		r, err := ScanRow(rows)
		if err != nil {
			return Result{}, fmt.Errorf("search: page rows: %w", err)
		}
		i := at[r.ID]
		items[i], found[i] = r, true
	}
	if err := rows.Err(); err != nil {
		return Result{}, fmt.Errorf("search: page rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return Result{}, fmt.Errorf("search: page rows: %w", err)
	}
	// Outside a read transaction an entry may go between the stages.
	res.Items = make([]Row, 0, len(items))
	for i, r := range items {
		if found[i] {
			res.Items = append(res.Items, r)
		}
	}
	if err := LoadTags(ctx, q, res.Items); err != nil {
		return Result{}, err
	}
	return res, nil
}

// pageKeys reads the positions the first stage selects.
func pageKeys(ctx context.Context, q store.Queryer, stmt string, args []any, s sortSpec) ([]position, error) {
	rows, err := q.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []position
	for rows.Next() {
		var (
			p  position
			id int64
		)
		keyDest := any(&p.N)
		if s.key == SortName {
			keyDest = &p.B
		}
		if err := rows.Scan(keyDest, &id); err != nil {
			return nil, err
		}
		p.ID = domain.EntryID(id)
		keys = append(keys, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, rows.Close()
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

// countSQL counts the matches of f up to one past CountCap. f.arms are read
// one after the other (a compound SELECT without ORDER BY), so the count
// stops in the first ranges when they hold enough matches.
func countSQL(f filter) (string, []any) {
	if f.arms == nil {
		return `SELECT count(*) FROM (SELECT 1 FROM ` + f.from + ` WHERE ` + f.where + ` LIMIT ?)`,
			append(f.args[:len(f.args):len(f.args)], CountCap+1)
	}
	var (
		b    strings.Builder
		args []any
	)
	b.WriteString(`SELECT count(*) FROM (`)
	for i, a := range f.arms {
		if i > 0 {
			b.WriteString(` UNION ALL `)
		}
		fmt.Fprintf(&b, `SELECT 1 FROM %s WHERE df.source_id = ? AND df.state = ? AND %s`, f.from, f.where)
		args = append(append(args, a.source, a.state), f.args...)
	}
	b.WriteString(` LIMIT ?)`)
	return b.String(), append(args, CountCap+1)
}

// resolveSQL lists the IDs of up to limit matches of f, ascending.
func resolveSQL(f filter, limit int) (string, []any) {
	return `SELECT e.id FROM ` + f.from + ` WHERE ` + f.where + ` ORDER BY e.id LIMIT ?`,
		append(f.args[:len(f.args):len(f.args)], limit)
}
