package search

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"precious/internal/domain"
	"precious/internal/store"
)

// Copies and the duplicate filter (R2 design D8, D15). A copy of a content
// is a physical copy: a present file entry on any source, offline ones
// included, where a hard-link set (same dev and ino on a source with stable
// identity) is one copy; or a file member of a complete archive whose entry
// is present, where a tar hard link and its target are one copy. A file in
// a source's quarantine, or a member of an archive there, is no copy (r4
// design D2): each copy's entry carries notQuarantined.

// copyKey is the physical-copy identity of the file entry aliased x: its
// hard-link set, or the entry itself.
func copyKey(x string) string {
	return `CASE WHEN ` + x + `.nlink > 1 AND ` + x + `.ino IS NOT NULL AND ` + x + `.dev IS NOT NULL
		AND (SELECT json_extract(cs.capabilities, '$.stable_identity') FROM sources cs WHERE cs.id = ` + x + `.source_id) = 1
		THEN 'h' || ` + x + `.source_id || '/' || ` + x + `.dev || '/' || ` + x + `.ino ELSE ` + x + `.id END`
}

// CopiesSQL is the number of physical copies of the content whose ID is the
// SQL expression content, counting every present file entry and every file
// member of a complete archive with that content, outside the quarantine. A
// hard-link set counts by its lowest entry outside the quarantine, a tar
// hard link by its target; neither count needs a temporary table, so a page
// row costs two indexed lookups by content.
func CopiesSQL(content string) string {
	return `((SELECT count(*) FROM file_content cf JOIN entries ce ON ce.id = cf.entry_id
			WHERE cf.content_id = ` + content + ` AND ce.state = 'present' AND ` + notQuarantined("ce") + `
				AND NOT (ce.nlink > 1 AND ce.ino IS NOT NULL AND ce.dev IS NOT NULL
					AND (SELECT json_extract(cs.capabilities, '$.stable_identity') FROM sources cs WHERE cs.id = ce.source_id) = 1
					AND EXISTS (SELECT 1 FROM file_content cl JOIN entries cle ON cle.id = cl.entry_id
						WHERE cl.content_id = ` + content + ` AND cle.state = 'present' AND cle.id < ce.id
							AND cle.source_id = ce.source_id AND cle.dev = ce.dev AND cle.ino = ce.ino
							AND ` + notQuarantined("cle") + `)))
		+ (SELECT count(*) FROM archive_members cm
			JOIN archives ca ON ca.entry_id = cm.archive_id JOIN entries cae ON cae.id = ca.entry_id
			WHERE cm.content_id = ` + content + ` AND cm.kind = 'file' AND ca.state = 'complete' AND cae.state = 'present'
				AND ` + notQuarantined("cae") + `
				AND NOT EXISTS (SELECT 1 FROM archive_members ct WHERE ct.id = cm.link_member AND ct.content_id = ` + content + `)))`
}

// copiesColumn is the copies column of Columns: a hashed present file's
// physical copies, itself included; 1 for a file whose content is unique by
// its size or its sample; NULL otherwise, and for a file in the quarantine,
// which is no copy itself (shown on the Cleanup screen only).
var copiesColumn = `CASE WHEN e.state <> 'present' OR ` + inQuarantine("e") + ` THEN NULL
	WHEN fc.state = 'hashed' THEN ` + CopiesSQL("fc.content_id") + `
	WHEN fc.state IN ('unique_size', 'sampled') THEN 1 END`

// otherCopy is an EXISTS over a physical copy of df's content other than
// the entry e: another present file entry outside e's hard-link set, or a
// file member of a complete archive whose entry is present, neither in the
// quarantine. where, when not nil, further restricts the copy's entry (an
// entry, or a member's archive entry) by its alias, appending its arguments
// to args.
func otherCopy(where func(alias string, args *[]any) string, args *[]any) string {
	var b strings.Builder
	b.WriteString(`(EXISTS (SELECT 1 FROM file_content of JOIN entries oe ON oe.id = of.entry_id
		WHERE of.content_id = df.content_id AND oe.state = 'present' AND oe.id <> e.id
			AND ` + notQuarantined("oe") + `
			AND (` + copyKey("oe") + `) IS NOT (` + copyKey("e") + `)`)
	if where != nil {
		b.WriteString(` AND `)
		b.WriteString(where("oe", args))
	}
	b.WriteString(`) OR EXISTS (SELECT 1 FROM archive_members om JOIN archives oa ON oa.entry_id = om.archive_id
		JOIN entries oae ON oae.id = oa.entry_id
		WHERE om.content_id = df.content_id AND om.kind = 'file' AND oa.state = 'complete' AND oae.state = 'present'
			AND ` + notQuarantined("oae"))
	if where != nil {
		b.WriteString(` AND `)
		b.WriteString(where("oae", args))
	}
	b.WriteString(`))`)
	return b.String()
}

// withinRange is the Within folder of a query: its source and path (empty
// for the root).
type withinRange struct {
	source string
	path   []byte
}

// outside restricts an entry aliased x to the entries of another source or
// outside w's path range.
func (w withinRange) outside(x string, args *[]any) string {
	if len(w.path) == 0 {
		*args = append(*args, w.source)
		return x + `.source_id <> ?`
	}
	*args = append(*args, w.source, descendantsFrom(w.path), descendantsTo(w.path))
	return `(` + x + `.source_id <> ? OR ` + x + `.path < ? OR ` + x + `.path >= ?)`
}

// dupFilter adds the duplicate filter (design D15): a present file entry
// whose content state is one of dups, each value being
//
//   - copies: hashed, with another physical copy anywhere;
//   - elsewhere: hashed, with another physical copy outside the Within
//     folder (on another source, or outside its path range; a member by its
//     archive's path);
//   - unique: unique by its size, by its sample, or hashed with no other
//     physical copy;
//   - unchecked: pending, changed, or unreadable.
//
// When the filter drives (r2b design D8), dupDriver has put the file's
// content row df in the FROM clause; otherwise each candidate row looks it
// up by its entry ID. Either way, the exact test runs on the candidates
// only. Another copy is never in the quarantine (otherCopy); e itself is
// kept out by buildFilter's residual on the same row, or, under
// Query.InQuarantine, is a quarantined file whose copies are those outside
// the quarantine.
func dupFilter(b *filterBuilder, dups []domain.DupFilter, w *withinRange, driving bool) {
	var (
		conds []string
		args  []any
		seen  = map[domain.DupFilter]bool{}
	)
	for _, d := range dups {
		if seen[d] {
			continue
		}
		seen[d] = true
		switch d {
		case domain.DupCopies:
			conds = append(conds, `(df.state = 'hashed' AND `+otherCopy(nil, &args)+`)`)
		case domain.DupElsewhere:
			conds = append(conds, `(df.state = 'hashed' AND `+otherCopy(w.outside, &args)+`)`)
		case domain.DupUnique:
			conds = append(conds, `(df.state IN ('unique_size', 'sampled') OR (df.state = 'hashed' AND NOT `+
				otherCopy(nil, &args)+`))`)
		case domain.DupUnchecked:
			conds = append(conds, `df.state IN ('pending', 'changed', 'unreadable')`)
		}
	}
	// A unary + keeps SQLite from choosing an index for the state test.
	if driving {
		b.add(`+e.state = 'present' AND (`+strings.Join(conds, ` OR `)+`)`, args...)
		return
	}
	b.add(`+e.state = 'present' AND EXISTS (SELECT 1 FROM file_content df WHERE df.entry_id = e.id AND (`+
		strings.Join(conds, ` OR `)+`))`, args...)
}

// copiesOnly reports that every value of dups needs another copy (copies,
// elsewhere).
func copiesOnly(dups []domain.DupFilter) bool {
	for _, d := range dups {
		if d != domain.DupCopies && d != domain.DupElsewhere {
			return false
		}
	}
	return true
}

// dupStates are the content states each duplicate state needs.
var dupStates = map[domain.DupFilter][]string{
	domain.DupCopies:    {"hashed"},
	domain.DupElsewhere: {"hashed"},
	domain.DupUnique:    {"unique_size", "sampled", "hashed"},
	domain.DupUnchecked: {"pending", "changed", "unreadable"},
}

// statesOf are the content states dups need, without repeats, hashed last:
// only hashed files need the copy test.
func statesOf(dups []domain.DupFilter) []string {
	var states []string
	hashed := false
	for _, d := range dups {
		for _, s := range dupStates[d] {
			if s == "hashed" {
				hashed = true
			} else if !slices.Contains(states, s) {
				states = append(states, s)
			}
		}
	}
	if hashed {
		states = append(states, "hashed")
	}
	return states
}

// withCopies are the contents with two or more file rows, or with an
// archive member: the only ones another copy can hold. Grouping streams
// over file_content_by_content, so a statement that stops early (a capped
// count) stops reading it too.
const withCopies = `(SELECT content_id FROM file_content WHERE content_id IS NOT NULL GROUP BY content_id
	HAVING count(*) > 1 OR EXISTS (SELECT 1 FROM archive_members m
		WHERE m.content_id = file_content.content_id AND m.kind = 'file')) dc
	CROSS JOIN file_content df ON df.content_id = dc.content_id
	CROSS JOIN entries e ON e.id = df.entry_id`

// byContentState reads the files through their content row df first.
const byContentState = `file_content df CROSS JOIN entries e ON e.id = df.entry_id`

// dupDriver returns the FROM clause of a query the duplicate filter
// drives (r2b design D8), adding its index conditions to b: the files of
// the contents with copies (withCopies) when every value needs a copy,
// otherwise the files of source (every source when "") in the content
// states the values need, through file_content_by_source.
func dupDriver(b *filterBuilder, dups []domain.DupFilter, source string) string {
	if source != "" {
		b.add(`df.source_id = ?`, source)
	}
	if copiesOnly(dups) {
		return withCopies
	}
	if source == "" {
		b.add(`df.source_id IN (SELECT id FROM sources)`)
	}
	in(b, `df.state`, statesOf(dups))
	return byContentState
}

// arm is one (source, content state) range of file_content_by_source,
// which holds its files in size order.
type arm struct {
	source, state string
}

// dupArms returns the ranges of file_content_by_source a query the
// duplicate filter drives reads (r2b design D8): one per source (source,
// or every source when "") and content state dups need, by state in
// dupStates order, those that need no copy test first. A file's size is its
// total_bytes, and file_content.size is its entry's size (hashing copies
// it, and the scan drops the row when the file changes), so each range is
// in the order of a page by bytes, and SQLite merges them without sorting.
func dupArms(ctx context.Context, q store.Queryer, dups []domain.DupFilter, source string) ([]arm, error) {
	sources := []string{source}
	if source == "" {
		var err error
		if sources, err = sourceIDs(ctx, q); err != nil {
			return nil, err
		}
	}
	arms := []arm{} // never nil: no source has no files
	for _, state := range statesOf(dups) {
		for _, s := range sources {
			arms = append(arms, arm{source: s, state: state})
		}
	}
	return arms, nil
}

// sourceIDs lists the IDs of every source.
func sourceIDs(ctx context.Context, q store.Queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM sources ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("search: sources: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("search: sources: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: sources: %w", err)
	}
	return ids, nil
}
