package search

import (
	"slices"
	"strings"
	"sync"

	"precious/internal/domain"
)

// Places (r5c design D1, D2, D6, D11, D14). A present file outside the
// quarantine has one place, relative to its own source; a member's source is
// its archive entry's. Physical copies are those of CopiesSQL and the relate
// snapshot: a hard-link set is one copy, a tar hard link and its target are
// one copy, the members of complete archives count (the members of
// hard-linked names of one archive once), archive files count by their own
// content, offline sources count, and the quarantine does not.
//
// Each place answers two questions about the content's other copies: is one
// on another source, and is there one at all. Each question has an entry arm
// and a member arm (copyArms), and each arm starts with one indexed probe by
// content ID. The copy aliases of copyArms (of, oe, om, oa, oae) and os must
// not be the caller's.

// PlaceSQL is the place of the file entry aliased e with its LEFT JOINed
// file_content row fc: one of D1's values, or NULL for a missing or
// quarantined entry. Complete archives are the caller's (entry_places).
//
// A folder is NULL too (its place is stored); a symlink, a special file, and
// an empty file are none; a file of size > 0 without a content row is
// unchecked. A complete archive's entry gets the place of its file by its
// own content, which D6 applies to its members.
func PlaceSQL(e, fc string) string {
	return `(CASE WHEN ` + e + `.state <> 'present' OR ` + inQuarantine(e) + ` OR ` + e + `.kind = 'directory' THEN NULL
	WHEN ` + e + `.kind <> 'file' OR ` + e + `.size <= 0 THEN '` + string(domain.PlaceNone) + `'
	` + placeByState(fc+".state",
		copyArms(fc+".content_id", "", "", onOtherSource(e), nil),
		otherCopy(fc+".content_id", e, nil, nil)) + ` END)`
}

// MemberPlaceSQL is the place of the file member aliased m of the archive
// entry aliased ae, before D5's archive coverage (D6).
//
// Another copy is never m itself, a member of its tar link group, or a
// member of another name of its archive's hard-link set (the members of
// hard-linked archive names are one copy, member by member); another member
// of m's own archive with the same content is a copy. Another source is one
// other than ae's. A member of a missing or quarantined archive entry is
// NULL, as is a folder member; a symlink, a special, and an empty member are
// none.
func MemberPlaceSQL(m, ae string) string {
	return `(CASE WHEN ` + ae + `.state <> 'present' OR ` + inQuarantine(ae) + ` OR ` + m + `.kind = 'directory' THEN NULL
	WHEN ` + m + `.kind <> 'file' OR ` + m + `.size <= 0 THEN '` + string(domain.PlaceNone) + `'
	` + placeByState(m+".state",
		copyArms(m+".content_id", "", "", onOtherSource(ae), nil),
		copyArms(m+".content_id", "", notMemberSelf(m, ae), nil, nil)) + ` END)`
}

// OfflineOnlySQL is D11's test for the entry aliased e, with its LEFT
// JOINed file_content row fc: a present hashed file outside the quarantine
// with a physical copy on another source, none of them on a source whose
// state is online. It is 0 or 1, never NULL.
func OfflineOnlySQL(e, fc string) string {
	content := fc + ".content_id"
	return `(CASE WHEN ` + e + `.state = 'present' AND ` + notQuarantined(e) + ` AND ` + fc + `.state = 'hashed'
		AND ` + copyArms(content, "", "", onOtherSource(e), nil) + `
		AND NOT ` + copyArms(content, "", "", onOnlineOtherSource(e), nil) + ` THEN 1 ELSE 0 END)`
}

// MemberOfflineOnlySQL is OfflineOnlySQL for the file member aliased m of
// the archive entry aliased ae, whose source is the member's.
func MemberOfflineOnlySQL(m, ae string) string {
	content := m + ".content_id"
	return `(CASE WHEN ` + ae + `.state = 'present' AND ` + notQuarantined(ae) + ` AND ` + m + `.kind = 'file'
		AND ` + m + `.state = 'hashed'
		AND ` + copyArms(content, "", "", onOtherSource(ae), nil) + `
		AND NOT ` + copyArms(content, "", "", onOnlineOtherSource(ae), nil) + ` THEN 1 ELSE 0 END)`
}

// placeByState is the WHEN arms of D1 for a regular file of size > 0 whose
// content state is the SQL expression state (NULL: no content row yet).
// otherSource and another are the two questions about a hashed file's other
// physical copies.
func placeByState(state, otherSource, another string) string {
	return `WHEN ` + state + ` IS NULL THEN '` + string(domain.PlaceUnchecked) + `'
	WHEN ` + state + ` IN ('` + string(domain.ContentUniqueSize) + `', '` + string(domain.ContentSampled) + `') THEN '` +
		string(domain.PlaceOnlyCopy) + `'
	WHEN ` + state + ` IN ('` + string(domain.ContentPending) + `', '` + string(domain.ContentChanged) + `') THEN '` +
		string(domain.PlaceUnchecked) + `'
	WHEN ` + state + ` = '` + string(domain.ContentUnreadable) + `' THEN '` + string(domain.PlaceUnreadable) + `'
	WHEN ` + state + ` = '` + string(domain.ContentHashed) + `' THEN CASE
		WHEN ` + otherSource + ` THEN '` + string(domain.PlaceOtherSource) + `'
		WHEN ` + another + ` THEN '` + string(domain.PlaceThisSource) + `'
		ELSE '` + string(domain.PlaceOnlyCopy) + `' END`
}

// notMemberSelf leaves out of copyArms' member arm the members that are not
// another copy of the member m of the archive entry ae: m's tar link group
// (a hard link and the member it names), and every member of another name of
// ae's hard-link set, whose members are those of ae again.
func notMemberSelf(m, ae string) string {
	return `coalesce(om.link_member, om.id) <> coalesce(` + m + `.link_member, ` + m + `.id)
			AND (oae.id = ` + ae + `.id OR (` + copyKey("oae") + `) IS NOT (` + copyKey(ae) + `))`
}

// onOtherSource restricts a copy's entry to a source other than self's.
func onOtherSource(self string) func(alias string, args *[]any) string {
	return func(alias string, _ *[]any) string { return alias + `.source_id <> ` + self + `.source_id` }
}

// onOnlineOtherSource restricts a copy's entry to an online source other
// than self's.
func onOnlineOtherSource(self string) func(alias string, args *[]any) string {
	return func(alias string, _ *[]any) string {
		return alias + `.source_id <> ` + self + `.source_id AND EXISTS (SELECT 1 FROM sources os WHERE os.id = ` +
			alias + `.source_id AND os.state = 'online')`
	}
}

// DisposableSQL is true for a file entry aliased e that D14 counts apart.
// It is 0 or 1, never NULL, and 0 for every entry that is not a file.
func DisposableSQL(e string) string {
	return strings.ReplaceAll(disposableTemplate(), disposableAlias, e)
}

// disposableAlias stands for the entry alias in disposableTemplate.
const disposableAlias = "\x00"

// disposableTemplate is DisposableSQL's text, generated once from
// domain.IsDisposable over every category, file kind, and triage, so that
// the SQL cannot disagree with Go. A NULL column, an empty one, and any value
// outside the domain's lists are the empty value, as Go reads them: each
// list gains "" as the stand-in for all of them, and a group that holds ""
// is written as NOT IN the listed values outside it.
var disposableTemplate = sync.OnceValue(func() string {
	col := func(name string) string { return `coalesce(` + disposableAlias + `.` + name + `, '')` }
	categories := append(slices.Clone(domain.Categories), "")
	kinds := append(slices.Clone(domain.FileKinds), "")
	triages := append(slices.Clone(domain.Triages), "")

	// byTriage is the condition over category and kind for one triage.
	byTriage := func(tr domain.Triage) string {
		var terms []string
		groups := groupBy(categories, func(c domain.Category) string {
			var sig []byte
			for _, k := range kinds {
				if domain.IsDisposable(c, k, tr) {
					sig = append(sig, '1')
				} else {
					sig = append(sig, '0')
				}
			}
			return string(sig)
		})
		for _, g := range groups {
			var in []domain.FileKind
			for i, k := range kinds {
				if g.key[i] == '1' {
					in = append(in, k)
				}
			}
			if len(in) == 0 {
				continue
			}
			term := inList(col("category"), g.members, categories)
			if len(in) < len(kinds) {
				term += ` AND ` + inList(col("file_kind"), in, kinds)
			}
			terms = append(terms, `(`+term+`)`)
		}
		return strings.Join(terms, ` OR `)
	}
	var terms []string
	for _, g := range groupBy(triages, byTriage) {
		if g.key == "" {
			continue
		}
		terms = append(terms, `(`+inList(col("triage"), g.members, triages)+` AND (`+g.key+`))`)
	}
	cond := "0"
	if len(terms) > 0 {
		cond = strings.Join(terms, ` OR `)
	}
	return `(CASE WHEN ` + disposableAlias + `.kind = 'file' AND (` + cond + `) THEN 1 ELSE 0 END)`
})

// group is the values of a list that share a key, in list order.
type group[T any] struct {
	key     string
	members []T
}

// groupBy groups values by key, in the order each key first appears.
func groupBy[T any](values []T, key func(T) string) []group[T] {
	var out []group[T]
	for _, v := range values {
		k := key(v)
		i := slices.IndexFunc(out, func(g group[T]) bool { return g.key == k })
		if i < 0 {
			out = append(out, group[T]{key: k})
			i = len(out) - 1
		}
		out[i].members = append(out[i].members, v)
	}
	return out
}

// inList is the test that the SQL expression col is one of members, all of
// them values of all, where "" stands for every value outside all's other
// values: with "" among members it is NOT IN the values of all outside
// members, else IN members.
func inList[T ~string](col string, members, all []T) string {
	if slices.Contains(members, "") {
		var out []T
		for _, v := range all {
			if !slices.Contains(members, v) {
				out = append(out, v)
			}
		}
		if len(out) == 0 {
			return `1`
		}
		return col + ` NOT IN (` + quoteList(out) + `)`
	}
	return col + ` IN (` + quoteList(members) + `)`
}

// quoteList is values as SQL string literals separated by commas.
func quoteList[T ~string](values []T) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = `'` + strings.ReplaceAll(string(v), `'`, `''`) + `'`
	}
	return strings.Join(quoted, `, `)
}
