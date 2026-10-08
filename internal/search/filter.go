package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"precious/internal/domain"
	"precious/internal/store"
)

// minTrigram is the shortest name the trigram index can look up.
const minTrigram = 3

// filter is the FROM and WHERE clauses of a query: from reads `entries e`,
// alone or after its driver's rows; where and its arguments, in order,
// select the matches.
type filter struct {
	from  string
	where string
	args  []any
	drive driver
	// arms, set for a page by bytes the duplicate filter drives, are the
	// size-ordered ranges each of whose statements reads from with where;
	// the page merges them (idsSQL).
	arms []arm
}

// ordered reports that a statement over f may read entries e in its sort
// index's order (driveColumns): the other drivers select their rows first,
// and their statements sort them.
func (f filter) ordered() bool { return f.drive == driveColumns }

type filterBuilder struct {
	conds []string
	args  []any
}

func (b *filterBuilder) add(cond string, args ...any) {
	b.conds = append(b.conds, cond)
	b.args = append(b.args, args...)
}

// in adds `col IN (?, …)` over values.
func in[T ~string | ~int64](b *filterBuilder, col string, values []T) {
	var s strings.Builder
	s.WriteString(col)
	s.WriteString(" IN (")
	writeMarks(&s, len(values))
	s.WriteByte(')')
	b.conds = append(b.conds, s.String())
	for _, v := range values {
		b.args = append(b.args, v)
	}
}

func writeMarks(s *strings.Builder, n int) {
	for i := range n {
		if i > 0 {
			s.WriteByte(',')
		}
		s.WriteByte('?')
	}
}

// driver is the filter whose index selects the candidate rows; the other
// filters test each candidate. SQLite has no statistics to choose by (the
// store runs no ANALYZE), and without them it prefers the source_id
// equality of the (source_id, …) indexes, which reads a whole source.
type driver int

const (
	// driveColumns lets SQLite use the source and decision
	// indexes: entries_eff (source_id, eff_decision), or the (source_id,
	// path) index for a source or the root's Within.
	driveColumns    driver = iota
	driveUnreadable        // the partial index of unreadable entries
	driveName              // the trigram index
	driveWithin            // the path range of a folder
	driveTags              // the tagged entries and their path ranges
	driveDup               // the content states, or the contents with copies
)

func (q Query) driver(withinRoot bool) driver {
	switch {
	case q.State == StateUnreadable:
		return driveUnreadable
	case utf8.RuneCountInString(q.Name) >= minTrigram:
		return driveName
	case q.Within != nil && !withinRoot:
		return driveWithin
	case len(q.Tags) > 0:
		return driveTags
	case len(q.Dup) > 0:
		return driveDup
	}
	return driveColumns
}

// buildFilter turns a validated query into SQL. It reads the Within entry,
// which must exist. ranged makes a query the duplicate filter drives read
// its files by ranges of file_content_by_source, one per source and content
// state (dupArms): a page by bytes merges them, a count reads them in turn.
func buildFilter(ctx context.Context, q store.Queryer, query Query, ranged bool) (filter, error) {
	var (
		withinSource string
		withinPath   []byte
	)
	if query.Within != nil {
		err := q.QueryRowContext(ctx, `SELECT source_id, path FROM entries WHERE id = ?`, int64(*query.Within)).
			Scan(&withinSource, &withinPath)
		if errors.Is(err, sql.ErrNoRows) {
			return filter{}, domain.Errorf(domain.CodeNotFound, "entry %s not found", *query.Within)
		}
		if err != nil {
			return filter{}, fmt.Errorf("search: within: %w", err)
		}
	}
	drive := query.driver(query.Within != nil && len(withinPath) == 0)
	// A unary + on a column keeps SQLite from using an index for that term,
	// so only the driver's index selects rows.
	source, decision, within, tags := "+", "+", "+", "+"
	switch drive {
	case driveColumns:
		source, decision = "", ""
	case driveUnreadable:
		source = ""
	case driveWithin:
		within = ""
	case driveTags:
		tags = ""
	}

	b := filterBuilder{}
	f := filter{from: `entries e`, drive: drive}
	switch drive {
	case driveDup:
		// The candidates' source: the query's, or the root Within's.
		src := string(query.Source)
		if src == "" {
			src = withinSource
		}
		if !ranged {
			f.from = dupDriver(&b, query.Dup, src)
			break
		}
		f.from = byContentState
		var err error
		if f.arms, err = dupArms(ctx, q, query.Dup, src); err != nil {
			return filter{}, err
		}
	case driveUnreadable:
		// Without statistics SQLite may prefer another (source_id, …)
		// index, which reads the whole source for its few unreadable entries.
		f.from = `entries e INDEXED BY entries_unreadable`
	}
	// The quarantine is left out of every search (r4 design D2), as a
	// residual on the row each shape already fetches; InQuarantine searches
	// it alone instead.
	if query.InQuarantine {
		b.add(inQuarantine("e"))
	} else {
		b.add(notQuarantined("e"))
	}
	if query.Source != "" {
		b.add(source+`e.source_id = ?`, string(query.Source))
	} else if (drive == driveColumns && len(query.Decisions) > 0) || drive == driveUnreadable {
		// entries_eff and entries_unreadable start with source_id: name
		// every source so the decision or state filter can use them.
		b.add(`e.source_id IN (SELECT id FROM sources)`)
	}
	if query.State == StateUnreadable {
		if drive == driveUnreadable {
			b.add(`e.state = 'unreadable'`)
		} else {
			b.add(`+e.state = 'unreadable'`)
		}
	}
	if query.Within != nil {
		if len(withinPath) == 0 {
			// The root: every other entry of the source, which only its
			// source selects.
			b.add(source+`e.source_id = ? AND +e.path > ?`, withinSource, []byte{})
		} else {
			b.add(within+`e.source_id = ? AND `+within+`e.path >= ? AND `+within+`e.path < ?`,
				withinSource, descendantsFrom(withinPath), descendantsTo(withinPath))
		}
	}
	if query.Name != "" {
		if drive == driveName {
			b.add(`e.id IN (SELECT rowid FROM entry_names WHERE entry_names MATCH ?)`, ftsPhrase(query.Name))
		} else if utf8.RuneCountInString(query.Name) >= minTrigram {
			b.add(`+e.id IN (SELECT rowid FROM entry_names WHERE entry_names MATCH ?)`, ftsPhrase(query.Name))
		} else {
			nameScan(&b, query.Name)
		}
	}
	if len(query.Ext) > 0 {
		exts := make([]string, len(query.Ext))
		for i, x := range query.Ext {
			exts[i] = normExt(x)
		}
		in(&b, `e.ext`, exts)
	}
	if len(query.FileKinds) > 0 {
		in(&b, `e.file_kind`, query.FileKinds)
	}
	if len(query.Categories) > 0 {
		in(&b, `e.category`, query.Categories)
	}
	if len(query.Triages) > 0 {
		in(&b, `e.triage`, query.Triages)
	}
	if len(query.Decisions) > 0 {
		in(&b, decision+`e.eff_decision`, query.Decisions)
	}
	if query.MinSize != nil {
		b.add(`e.total_bytes >= ?`, *query.MinSize)
	}
	if query.MaxSize != nil {
		b.add(`e.total_bytes <= ?`, *query.MaxSize)
	}
	if query.YearFrom != nil {
		b.add(`e.mtime_ns >= ?`, yearStart(*query.YearFrom))
	}
	if query.YearTo != nil {
		b.add(`e.mtime_ns < ?`, yearStart(*query.YearTo+1))
	}
	if len(query.Tags) > 0 {
		tagFilter(&b, tags, query.Tags)
	}
	if len(query.Dup) > 0 {
		dupFilter(&b, query.Dup, &withinRange{source: withinSource, path: withinPath}, drive == driveDup)
	}
	f.where, f.args = `1`, b.args
	if len(b.conds) > 0 {
		f.where = strings.Join(b.conds, ` AND `)
	}
	return f, nil
}

// descendantsFrom and descendantsTo bound the paths below a non-root path p:
// [p + "/", p + "0"), '0' being the byte after '/'.
func descendantsFrom(p []byte) []byte { return append(p[:len(p):len(p)], '/') }
func descendantsTo(p []byte) []byte   { return append(p[:len(p):len(p)], '0') }

// tagFilter matches the entries with one of tags as an own tag, and the
// descendants of each such entry by path range (design D10). UNION removes
// the repeats of nested tagged folders. prefix is "+" when the tags do not
// drive the query.
func tagFilter(b *filterBuilder, prefix string, tags []int64) {
	var s strings.Builder
	s.WriteString(prefix)
	s.WriteString(`e.id IN (WITH tagged AS (SELECT t.id, t.source_id, t.path FROM entry_tags et
		JOIN entries t ON t.id = et.entry_id WHERE et.tag_id IN (`)
	writeMarks(&s, len(tags))
	s.WriteString(`))
		SELECT id FROM tagged
		UNION SELECT d.id FROM tagged g JOIN entries d ON d.source_id = g.source_id
			AND d.path >= CAST(g.path || '/' AS BLOB) AND d.path < CAST(g.path || '0' AS BLOB)
			WHERE g.path <> X''
		UNION SELECT d.id FROM tagged g JOIN entries d ON d.source_id = g.source_id
			WHERE g.path = X'')`)
	b.conds = append(b.conds, s.String())
	for _, t := range tags {
		b.args = append(b.args, t)
	}
}

// ftsPhrase quotes name as one FTS5 phrase. The trigram tokenizer matches a
// phrase as a substring, case- and accent-insensitively.
func ftsPhrase(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// nameScan adds the per-row test of a name too short for trigrams: the raw
// name contains one of the name's case variants. It folds case as the
// trigram index does (Unicode simple case folding), but not accents: a
// short search finds only its own accented letters.
func nameScan(b *filterBuilder, name string) {
	variants := [][]byte{nil}
	for _, r := range name {
		var next [][]byte
		for _, v := range variants {
			f := r
			for {
				next = append(next, utf8.AppendRune(v[:len(v):len(v)], f))
				if f = unicode.SimpleFold(f); f == r {
					break
				}
			}
		}
		variants = next
	}
	var s strings.Builder
	s.WriteByte('(')
	for i, v := range variants {
		if i > 0 {
			s.WriteString(` OR `)
		}
		s.WriteString(`instr(e.name, ?) > 0`)
		b.args = append(b.args, v)
	}
	s.WriteByte(')')
	b.conds = append(b.conds, s.String())
}

// normExt is an extension as the index stores it: no leading dot, ASCII
// lower case.
func normExt(x string) string {
	x = strings.TrimPrefix(x, ".")
	var lower strings.Builder
	lower.Grow(len(x))
	for i := range len(x) {
		c := x[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		lower.WriteByte(c)
	}
	return lower.String()
}

const (
	minYear = 1
	maxYear = 9999
)

// yearStart is the first nanosecond of a UTC year, clamped to the int64
// nanosecond range (years 1678 to 2262).
func yearStart(y int) int64 {
	t := time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC)
	switch {
	case t.Before(time.Unix(0, math.MinInt64)):
		return math.MinInt64
	case t.After(time.Unix(0, math.MaxInt64)):
		return math.MaxInt64
	}
	return t.UnixNano()
}
