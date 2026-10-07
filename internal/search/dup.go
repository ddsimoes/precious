package search

import (
	"strings"

	"precious/internal/domain"
)

// Copies and the duplicate filter (R2 design D8, D15). A copy of a content
// is a physical copy: a present file entry on any source, offline ones
// included, where a hard-link set (same dev and ino on a source with stable
// identity) is one copy; or a file member of a complete archive whose entry
// is present, where a tar hard link and its target are one copy.

// copyKey is the physical-copy identity of the file entry aliased x: its
// hard-link set, or the entry itself.
func copyKey(x string) string {
	return `CASE WHEN ` + x + `.nlink > 1 AND ` + x + `.ino IS NOT NULL AND ` + x + `.dev IS NOT NULL
		AND (SELECT json_extract(cs.capabilities, '$.stable_identity') FROM sources cs WHERE cs.id = ` + x + `.source_id) = 1
		THEN 'h' || ` + x + `.source_id || '/' || ` + x + `.dev || '/' || ` + x + `.ino ELSE ` + x + `.id END`
}

// CopiesSQL is the number of physical copies of the content whose ID is the
// SQL expression content, counting every present file entry and every file
// member of a complete archive with that content. A hard-link set counts by
// its lowest entry, a tar hard link by its target; neither count needs a
// temporary table, so a page row costs two indexed lookups by content.
func CopiesSQL(content string) string {
	return `((SELECT count(*) FROM file_content cf JOIN entries ce ON ce.id = cf.entry_id
			WHERE cf.content_id = ` + content + ` AND ce.state = 'present'
				AND NOT (ce.nlink > 1 AND ce.ino IS NOT NULL AND ce.dev IS NOT NULL
					AND (SELECT json_extract(cs.capabilities, '$.stable_identity') FROM sources cs WHERE cs.id = ce.source_id) = 1
					AND EXISTS (SELECT 1 FROM file_content cl JOIN entries cle ON cle.id = cl.entry_id
						WHERE cl.content_id = ` + content + ` AND cle.state = 'present' AND cle.id < ce.id
							AND cle.source_id = ce.source_id AND cle.dev = ce.dev AND cle.ino = ce.ino)))
		+ (SELECT count(*) FROM archive_members cm
			JOIN archives ca ON ca.entry_id = cm.archive_id JOIN entries cae ON cae.id = ca.entry_id
			WHERE cm.content_id = ` + content + ` AND cm.kind = 'file' AND ca.state = 'complete' AND cae.state = 'present'
				AND NOT EXISTS (SELECT 1 FROM archive_members ct WHERE ct.id = cm.link_member AND ct.content_id = ` + content + `)))`
}

// copiesColumn is the copies column of Columns: a hashed present file's
// physical copies, itself included; 1 for a file whose content is unique by
// its size or its sample; NULL otherwise.
var copiesColumn = `CASE WHEN e.state <> 'present' THEN NULL
	WHEN fc.state = 'hashed' THEN ` + CopiesSQL("fc.content_id") + `
	WHEN fc.state IN ('unique_size', 'sampled') THEN 1 END`

// otherCopy is an EXISTS over a physical copy of df's content other than
// the entry e: another present file entry outside e's hard-link set, or a
// file member of a complete archive whose entry is present. where, when
// not nil, further restricts the copy's entry (an entry, or a member's
// archive entry) by its alias, appending its arguments to args.
func otherCopy(where func(alias string, args *[]any) string, args *[]any) string {
	var b strings.Builder
	b.WriteString(`(EXISTS (SELECT 1 FROM file_content of JOIN entries oe ON oe.id = of.entry_id
		WHERE of.content_id = df.content_id AND oe.state = 'present' AND oe.id <> e.id
			AND (` + copyKey("oe") + `) IS NOT (` + copyKey("e") + `)`)
	if where != nil {
		b.WriteString(` AND `)
		b.WriteString(where("oe", args))
	}
	b.WriteString(`) OR EXISTS (SELECT 1 FROM archive_members om JOIN archives oa ON oa.entry_id = om.archive_id
		JOIN entries oae ON oae.id = oa.entry_id
		WHERE om.content_id = df.content_id AND om.kind = 'file' AND oa.state = 'complete' AND oae.state = 'present'`)
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
// Every value is tested per candidate row: the filter has no index of its
// own.
func dupFilter(b *filterBuilder, dups []domain.DupFilter, w *withinRange) {
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
	b.add(`+e.state = 'present' AND EXISTS (SELECT 1 FROM file_content df WHERE df.entry_id = e.id AND (`+
		strings.Join(conds, ` OR `)+`))`, args...)
}
