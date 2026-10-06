package review

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"precious/internal/domain"
	"precious/internal/store"
)

// insertBatch bounds the rows of one Refresh write transaction, and
// deleteBatch the rows one clean-up statement deletes.
const (
	insertBatch = 2000
	deleteBatch = 5000
)

// Refresh writes generation gen of review_rows and review_row_sources: the
// rows of the seven cards and the three Gems sections, computed from the
// index, the content state, and the relations of generation gen (D12, D14).
// It is the relate job's after hook, called before review_state.gen flips
// to gen, so readers never see a partial generation. Rows a previous,
// interrupted run left in gen are deleted first. Every insert re-checks
// that its entries, relation, content, and sources still exist
// (Transaction boundaries): a row whose entry a scan or remove-source
// deleted meanwhile is skipped. Refreshing the visible generation is an
// error.
func Refresh(ctx context.Context, st *store.Store, gen int64) error {
	var visible int64
	if err := st.Reader().QueryRowContext(ctx, `SELECT gen FROM review_state WHERE id = 1`).Scan(&visible); err != nil {
		return fmt.Errorf("review: refresh: %w", err)
	}
	if gen == visible {
		return fmt.Errorf("review: refresh: generation %d is the visible one", gen)
	}
	if err := deleteGen(ctx, st, gen); err != nil {
		return err
	}
	var rows []newRow
	err := st.Read(ctx, func(tx *sql.Tx) error {
		var err error
		rows, err = compute(ctx, tx, gen)
		return err
	})
	if err != nil {
		return fmt.Errorf("review: refresh: %w", err)
	}
	for rest := rows; len(rest) > 0; {
		batch := rest[:min(len(rest), insertBatch)]
		rest = rest[len(batch):]
		if err := st.Write(ctx, func(tx *sql.Tx) error { return insertRows(ctx, tx, gen, batch) }); err != nil {
			return fmt.Errorf("review: refresh: %w", err)
		}
	}
	return nil
}

// deleteGen deletes the rows of generation gen in batches.
func deleteGen(ctx context.Context, st *store.Store, gen int64) error {
	for {
		var n int64
		err := st.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `DELETE FROM review_rows WHERE id IN (
				SELECT id FROM review_rows WHERE gen = ? LIMIT ?)`, gen, deleteBatch)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		if err != nil {
			return fmt.Errorf("review: refresh: clear generation %d: %w", gen, err)
		}
		if n < deleteBatch {
			return nil
		}
	}
}

// newRow is one row to write.
type newRow struct {
	list                            List
	source                          string // "" for duplicates rows
	entry, relation, content, group int64  // 0 for none
	bytes, files, sortKey           int64
	sources                         []string // duplicates rows: review_row_sources
}

func insertRows(ctx context.Context, tx *sql.Tx, gen int64, rows []newRow) error {
	ins, err := tx.PrepareContext(ctx, `INSERT INTO review_rows
		(gen, list, source_id, entry_id, relation_id, content_id, group_id, bytes, files, sort_key)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10
		WHERE (?3 IS NULL OR EXISTS (SELECT 1 FROM sources WHERE id = ?3))
			AND (?4 IS NULL OR EXISTS (SELECT 1 FROM entries WHERE id = ?4 AND state <> 'missing'))
			AND (?5 IS NULL OR EXISTS (SELECT 1 FROM relations WHERE id = ?5))
			AND (?6 IS NULL OR EXISTS (SELECT 1 FROM contents WHERE id = ?6))
			AND (?7 IS NULL OR EXISTS (SELECT 1 FROM entries WHERE id = ?7 AND state <> 'missing'))
		RETURNING id`)
	if err != nil {
		return err
	}
	defer ins.Close()
	src, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO review_row_sources (row_id, source_id)
		SELECT ?1, ?2 WHERE EXISTS (SELECT 1 FROM sources WHERE id = ?2)`)
	if err != nil {
		return err
	}
	defer src.Close()
	for _, r := range rows {
		var id int64
		err := ins.QueryRowContext(ctx, gen, string(r.list), nullString(r.source), nullID(r.entry), nullID(r.relation),
			nullID(r.content), nullID(r.group), r.bytes, r.files, r.sortKey).Scan(&id)
		if err == sql.ErrNoRows {
			continue // its entry, relation, content, or source is gone
		}
		if err != nil {
			return err
		}
		for _, s := range r.sources {
			if _, err := src.ExecContext(ctx, id, s); err != nil {
				return err
			}
		}
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// key names an entry index-wide: its source and raw path.
func key(src, path string) string { return src + "\x00" + path }

// underAny reports whether a proper ancestor of path in src is in set.
func underAny(set map[string]bool, src, path string) bool {
	if len(set) == 0 {
		return false
	}
	for i := len(path) - 1; i > 0; i-- {
		if path[i] == '/' && set[key(src, path[:i])] {
			return true
		}
	}
	return false
}

// outermostAncestor returns the shallowest proper ancestor of path in set,
// or "" with false.
func outermostAncestor(set map[string]bool, src, path string) (string, bool) {
	for i := 0; i < len(path); i++ {
		if path[i] == '/' && set[key(src, path[:i])] {
			return path[:i], true
		}
	}
	return "", false
}

// compute builds the rows of generation gen inside one read transaction.
func compute(ctx context.Context, tx *sql.Tx, gen int64) ([]newRow, error) {
	var out []newRow
	rules, err := ruleRows(ctx, tx)
	if err != nil {
		return nil, err
	}
	out = append(out, rules...)

	rels, err := loadRelations(ctx, tx, gen)
	if err != nil {
		return nil, err
	}
	copies, err := loadCopies(ctx, tx)
	if err != nil {
		return nil, err
	}
	out = append(out, duplicateRows(rels, copies)...)
	out = append(out, unpackedRows(rels)...)

	groups, err := loadGroups(ctx, tx)
	if err != nil {
		return nil, err
	}
	unique, err := uniqueGems(ctx, tx, copies, groups)
	if err != nil {
		return nil, err
	}
	out = append(out, unique...)
	rescue, err := rescueGems(ctx, tx, groups)
	if err != nil {
		return nil, err
	}
	out = append(out, rescue...)
	only, err := onlyInCopyGems(ctx, tx, rels, copies)
	if err != nil {
		return nil, err
	}
	return append(out, only...), nil
}

// ruleLists maps a category to its rule card (D12).
var ruleLists = map[string]List{
	string(domain.CategorySystemJunk):              ListSystemJunk,
	string(domain.CategoryInstallerDownload):       ListInstallers,
	string(domain.CategoryDownloadCollection):      ListInstallers,
	string(domain.CategoryApplicationInstallation): ListPrograms,
	string(domain.CategoryOSInstallation):          ListPrograms,
	string(domain.CategoryCache):                   ListCaches,
	string(domain.CategoryTemporaryData):           ListCaches,
	string(domain.CategoryGeneratedArtifacts):      ListCaches,
}

// partialDownloadRule is the rule whose files are leftovers, not caches.
const partialDownloadRule = "partial_download"

// candidate is an entry that may be a row of a rule card.
type candidate struct {
	id           int64
	src, path    string
	bytes, files int64
}

// ruleRows returns the rows of the five rule cards: per card, the
// outermost entries that match it, so that no byte counts twice in a card
// (D12). Missing entries and source roots are never rows.
func ruleRows(ctx context.Context, tx *sql.Tx) ([]newRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.source_id, e.path, e.kind, coalesce(e.category, ''),
			e.total_bytes, e.total_files,
			e.rule_ids IS NOT NULL AND EXISTS (SELECT 1 FROM json_each(e.rule_ids) WHERE value = ?),
			e.kind = 'directory' AND e.total_files = 0 AND e.state = 'present' AND e.mount_boundary = 0
				AND coalesce((SELECT d.unreadable = 0 AND d.mount_boundaries = 0 FROM dir_stats d WHERE d.entry_id = e.id), 1)
		FROM entries e
		WHERE e.state <> 'missing' AND e.parent_id IS NOT NULL AND (
			e.category IN ('system_junk', 'installer_download', 'download_collection', 'application_installation',
				'os_installation', 'cache', 'temporary_data', 'generated_artifacts')
			OR (e.kind = 'file' AND e.size = 0)
			OR (e.kind = 'directory' AND e.total_files = 0))`, partialDownloadRule)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byList := make(map[List][]candidate)
	for rows.Next() {
		var (
			c                    candidate
			path                 []byte
			kind, category       string
			partial, emptyFolder bool
		)
		if err := rows.Scan(&c.id, &c.src, &path, &kind, &category, &c.bytes, &c.files, &partial, &emptyFolder); err != nil {
			return nil, err
		}
		c.path = string(path)
		if l, ok := ruleLists[category]; ok {
			if partial && l == ListCaches {
				l = ListLeftovers
			}
			byList[l] = append(byList[l], c)
		}
		if emptyFolder || kind == string(domain.EntryFile) && c.bytes == 0 {
			if !partial || ruleLists[category] != ListCaches {
				byList[ListLeftovers] = append(byList[ListLeftovers], c)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []newRow
	for _, l := range []List{ListSystemJunk, ListInstallers, ListPrograms, ListCaches, ListLeftovers} {
		cs := byList[l]
		set := make(map[string]bool, len(cs))
		for _, c := range cs {
			set[key(c.src, c.path)] = true
		}
		for _, c := range cs {
			if underAny(set, c.src, c.path) {
				continue
			}
			out = append(out, newRow{list: l, source: c.src, entry: c.id, bytes: c.bytes, files: c.files, sortKey: c.bytes})
		}
	}
	return out, nil
}

// side is one side of a relation.
type side struct {
	entry, member int64
	src, path     string // the entry's
	memberPath    string // a member folder's path inside its archive
	kind          string // the entry's kind
	bytes         int64  // the entry's total_bytes
	archive       bool   // the entry is a listed archive file
}

// relation is one relation of the generation.
type relation struct {
	id                int64
	kind              string
	a, b              side
	redundant, aFiles int64
}

func loadRelations(ctx context.Context, tx *sql.Tx, gen int64) ([]relation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.id, r.kind, r.redundant_bytes, r.a_files,
			r.a_entry, coalesce(r.a_member, 0), ea.source_id, ea.path, ea.kind, ea.total_bytes,
			coalesce((SELECT m.path FROM archive_members m WHERE m.id = r.a_member), X''),
			EXISTS (SELECT 1 FROM archives x WHERE x.entry_id = r.a_entry),
			r.b_entry, coalesce(r.b_member, 0), eb.source_id, eb.path, eb.kind, eb.total_bytes,
			coalesce((SELECT m.path FROM archive_members m WHERE m.id = r.b_member), X''),
			EXISTS (SELECT 1 FROM archives x WHERE x.entry_id = r.b_entry)
		FROM relations r JOIN entries ea ON ea.id = r.a_entry JOIN entries eb ON eb.id = r.b_entry
		WHERE r.gen = ? AND ea.state = 'present' AND eb.state = 'present'
		ORDER BY r.id`, gen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []relation
	for rows.Next() {
		var (
			r              relation
			ap, am, bp, bm []byte
		)
		if err := rows.Scan(&r.id, &r.kind, &r.redundant, &r.aFiles,
			&r.a.entry, &r.a.member, &r.a.src, &ap, &r.a.kind, &r.a.bytes, &am, &r.a.archive,
			&r.b.entry, &r.b.member, &r.b.src, &bp, &r.b.kind, &r.b.bytes, &bm, &r.b.archive); err != nil {
			return nil, err
		}
		r.a.path, r.a.memberPath, r.b.path, r.b.memberPath = string(ap), string(am), string(bp), string(bm)
		out = append(out, r)
	}
	return out, rows.Err()
}

// aCopy is one copy of a content with at least two copies.
type aCopy struct {
	content, size int64
	key           string // the physical copy: a hard-link set, an entry, or a member
	src, path     string // the file's, or the member's archive file's
	archive       int64  // a member's archive entry; 0 for a file
	memberPath    string
}

// contentCopies are the copies of every content with at least two physical
// copies among present files and members of complete archives, on any
// source (D8), by content.
type contentCopies map[int64][]aCopy

func loadCopies(ctx context.Context, tx *sql.Tx) (contentCopies, error) {
	rows, err := tx.QueryContext(ctx, `WITH copies(content_id, k, src, path, archive, member_path) AS (
			SELECT fc.content_id, `+copyKeySQL+`, e.source_id, e.path, 0, X''
				FROM file_content fc JOIN entries e ON e.id = fc.entry_id
				WHERE fc.content_id IS NOT NULL AND e.state = 'present'
			UNION ALL
			SELECT m.content_id, 'm' || m.id, ae.source_id, ae.path, ae.id, m.path
				FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id AND a.state = 'complete'
				JOIN entries ae ON ae.id = a.entry_id
				WHERE m.content_id IS NOT NULL AND ae.state = 'present'),
		multi AS (SELECT content_id FROM copies GROUP BY content_id HAVING count(DISTINCT k) >= 2)
		SELECT c.content_id, ct.size, c.k, c.src, c.path, c.archive, c.member_path
		FROM copies c JOIN multi USING (content_id) JOIN contents ct ON ct.id = c.content_id
		ORDER BY c.content_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(contentCopies)
	for rows.Next() {
		var (
			c     aCopy
			p, mp []byte
		)
		if err := rows.Scan(&c.content, &c.size, &c.key, &c.src, &p, &c.archive, &mp); err != nil {
			return nil, err
		}
		c.path, c.memberPath = string(p), string(mp)
		out[c.content] = append(out[c.content], c)
	}
	return out, rows.Err()
}

// listedSides are the sides of the duplicates card's relations.
type listedSides struct {
	entries map[string]bool           // folders and archive files, by key
	members map[int64]map[string]bool // member folders, by archive entry
}

// listedRelation reports whether r is a row of the duplicates card.
func listedRelation(r relation) bool { return r.kind == "same" || r.kind == "inside" }

func sidesOf(rels []relation) listedSides {
	s := listedSides{entries: map[string]bool{}, members: map[int64]map[string]bool{}}
	for _, r := range rels {
		if !listedRelation(r) {
			continue
		}
		for _, x := range []side{r.a, r.b} {
			if x.member == 0 {
				s.entries[key(x.src, x.path)] = true
				continue
			}
			if s.members[x.entry] == nil {
				s.members[x.entry] = map[string]bool{}
			}
			s.members[x.entry][x.memberPath] = true
		}
	}
	return s
}

// inside reports whether c lies within a listed side: a file at or below
// a side entry, or a member of an archive at or below one, or below a
// member folder side.
func (s listedSides) inside(c aCopy) bool {
	if s.entries[key(c.src, c.path)] || underAny(s.entries, c.src, c.path) {
		return true
	}
	if c.archive == 0 {
		return false
	}
	folders := s.members[c.archive]
	for i := len(c.memberPath) - 1; i > 0 && folders != nil; i-- {
		if c.memberPath[i] == '/' && folders[c.memberPath[:i]] {
			return true
		}
	}
	return false
}

// duplicateRows returns the duplicates card's rows (D12): every same or
// inside relation, and every duplicate group with a physical copy outside
// all of those relations. A group's bytes are its redundant bytes not
// already counted by a relation: size × (copies outside − 1) when no copy
// lies inside a listed relation, else size × copies outside, since the
// relation's rows already count the inside copies' redundancy and leave one
// of them standing.
func duplicateRows(rels []relation, copies contentCopies) []newRow {
	var out []newRow
	for _, r := range rels {
		if !listedRelation(r) {
			continue
		}
		srcs := []string{r.a.src}
		if r.b.src != r.a.src {
			srcs = append(srcs, r.b.src)
		}
		out = append(out, newRow{list: ListDuplicates, relation: r.id, bytes: r.redundant, files: r.aFiles,
			sortKey: r.redundant, sources: srcs})
	}
	sides := sidesOf(rels)
	ids := make([]int64, 0, len(copies))
	for id := range copies {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		cs := copies[id]
		in := map[string]bool{}
		for _, c := range cs {
			if sides.inside(c) {
				in[c.key] = true
			}
		}
		keys := map[string]bool{}
		var srcs []string
		for _, c := range cs {
			keys[c.key] = true
			if !slices.Contains(srcs, c.src) {
				srcs = append(srcs, c.src)
			}
		}
		outside := int64(len(keys) - len(in))
		if outside == 0 {
			continue
		}
		n := outside
		if len(in) == 0 {
			n--
		}
		slices.Sort(srcs)
		b := cs[0].size * n
		out = append(out, newRow{list: ListDuplicates, content: id, bytes: b, files: n, sortKey: b, sources: srcs})
	}
	return out
}

// unpackedRows returns the unpacked_archives card's rows: each archive file
// that is the a side of a same or inside relation with a folder, once.
func unpackedRows(rels []relation) []newRow {
	var out []newRow
	seen := map[int64]bool{}
	for _, r := range rels {
		if !listedRelation(r) || r.a.member != 0 || !r.a.archive || r.b.member != 0 ||
			r.b.kind != string(domain.EntryDirectory) || seen[r.a.entry] {
			continue
		}
		seen[r.a.entry] = true
		out = append(out, newRow{list: ListUnpackedArchives, source: r.a.src, entry: r.a.entry,
			bytes: r.a.bytes, files: 1, sortKey: r.a.bytes})
	}
	return out
}

// group is a group of family programs or disposable.
type group struct {
	id         int64
	src, path  string
	bytes      int64
	indicators string
}

// groupSet are the programs and disposable groups, by key.
type groupSet struct {
	all []group
	set map[string]bool
}

func loadGroups(ctx context.Context, tx *sql.Tx) (groupSet, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.source_id, e.path, e.total_bytes, coalesce(d.indicators, '[]')
		FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id
		WHERE e.is_group = 1 AND e.family IN ('programs', 'disposable') AND e.state <> 'missing'`)
	if err != nil {
		return groupSet{}, err
	}
	defer rows.Close()
	g := groupSet{set: map[string]bool{}}
	for rows.Next() {
		var (
			x group
			p []byte
		)
		if err := rows.Scan(&x.id, &x.src, &p, &x.bytes, &x.indicators); err != nil {
			return groupSet{}, err
		}
		x.path = string(p)
		g.all = append(g.all, x)
		g.set[key(x.src, x.path)] = true
	}
	return g, rows.Err()
}

// gemKinds are the file kinds Gems lists as unique (D14).
var gemKinds = map[domain.FileKind]bool{
	domain.FileKindImage: true, domain.FileKindVideo: true, domain.FileKindAudio: true, domain.FileKindDocument: true,
}

// unique reports whether a file's content state proves it has no other
// copy (D8): unique by size, a distinct sample, or hashed with one copy.
func unique(state string, content int64, copies contentCopies) bool {
	switch domain.ContentState(state) {
	case domain.ContentUniqueSize, domain.ContentSampled:
		return true
	case domain.ContentHashed:
		return len(copies[content]) == 0
	}
	return false
}

// uniqueGems returns gems_unique: the personal images, videos, audio files,
// and documents with no other copy, outside every programs or disposable
// group, oldest first (D14). A file not checked yet is never listed (I7).
func uniqueGems(ctx context.Context, tx *sql.Tx, copies contentCopies, groups groupSet) ([]newRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.source_id, e.path, coalesce(e.category, ''), e.file_kind,
			e.size, coalesce(e.mtime_ns, 0), fc.state, coalesce(fc.content_id, 0)
		FROM entries e JOIN file_content fc ON fc.entry_id = e.id
		WHERE e.kind = 'file' AND e.state = 'present' AND e.file_kind IN ('image', 'video', 'audio', 'document')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type gem struct {
		row  newRow
		path string
	}
	var gems []gem
	for rows.Next() {
		var (
			id, size, mtime, content int64
			src, category, kind      string
			state                    string
			p                        []byte
		)
		if err := rows.Scan(&id, &src, &p, &category, &kind, &size, &mtime, &state, &content); err != nil {
			return nil, err
		}
		fk := domain.FileKind(kind)
		if !gemKinds[fk] || domain.FileFamily(domain.Category(category), fk) != domain.FamilyPersonal ||
			!unique(state, content, copies) || underAny(groups.set, src, string(p)) {
			continue
		}
		gems = append(gems, gem{row: newRow{list: ListGemsUnique, source: src, entry: id, bytes: size, files: 1, sortKey: mtime},
			path: key(src, string(p))})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(gems, func(a, b gem) int {
		return cmp.Or(cmp.Compare(a.row.sortKey, b.row.sortKey), strings.Compare(a.path, b.path))
	})
	out := make([]newRow, len(gems))
	for i, g := range gems {
		out[i] = g.row
	}
	return out, nil
}

// rescueGems returns gems_rescue: the indicators of the programs and
// disposable groups (dir_stats.indicators), each under its outermost such
// group, once; groups largest first, indicators by path (D14).
func rescueGems(ctx context.Context, tx *sql.Tx, groups groupSet) ([]newRow, error) {
	type indicator struct {
		id    int64
		outer string // the outermost group's key
	}
	byID := map[string]group{}
	for _, g := range groups.all {
		byID[key(g.src, g.path)] = g
	}
	var inds []indicator
	seen := map[int64]bool{}
	for _, g := range groups.all {
		outer := key(g.src, g.path)
		if p, ok := outermostAncestor(groups.set, g.src, g.path); ok {
			outer = key(g.src, p)
		}
		var list []struct {
			EntryID string `json:"entry_id"`
		}
		if err := json.Unmarshal([]byte(g.indicators), &list); err != nil {
			return nil, fmt.Errorf("indicators of entry %d: %w", g.id, err)
		}
		for _, x := range list {
			id, err := domain.ParseEntryID(x.EntryID)
			if err != nil || seen[int64(id)] {
				continue
			}
			seen[int64(id)] = true
			inds = append(inds, indicator{id: int64(id), outer: outer})
		}
	}
	if len(inds) == 0 {
		return nil, nil
	}
	ids := make([]int64, len(inds))
	for i, x := range inds {
		ids[i] = x.id
	}
	idJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id, e.path, e.total_bytes, e.total_files FROM entries e
		WHERE e.id IN (SELECT value FROM json_each(?)) AND e.state <> 'missing'`, string(idJSON))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type fact struct {
		path         string
		bytes, files int64
	}
	facts := map[int64]fact{}
	for rows.Next() {
		var (
			id int64
			f  fact
			p  []byte
		)
		if err := rows.Scan(&id, &p, &f.bytes, &f.files); err != nil {
			return nil, err
		}
		f.path = string(p)
		facts[id] = f
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var outers []group
	members := map[string][]indicator{}
	for _, x := range inds {
		if _, ok := facts[x.id]; !ok {
			continue
		}
		if len(members[x.outer]) == 0 {
			outers = append(outers, byID[x.outer])
		}
		members[x.outer] = append(members[x.outer], x)
	}
	slices.SortFunc(outers, func(a, b group) int {
		return cmp.Or(cmp.Compare(b.bytes, a.bytes), strings.Compare(a.src, b.src), strings.Compare(a.path, b.path))
	})
	var out []newRow
	for rank, g := range outers {
		list := members[key(g.src, g.path)]
		slices.SortFunc(list, func(a, b indicator) int { return strings.Compare(facts[a.id].path, facts[b.id].path) })
		for _, x := range list {
			f := facts[x.id]
			out = append(out, newRow{list: ListGemsRescue, source: g.src, entry: x.id, group: g.id,
				bytes: f.bytes, files: f.files, sortKey: int64(rank)})
		}
	}
	return out, nil
}

// onlyInCopyGems returns gems_only_in_copy: the files with no other copy
// below a folder side of an overlap relation (so found only on that side),
// grouped by relation, by raw path within one, each file once (D14). Files
// inside archives are members, which review rows cannot name, so archive
// sides add none.
func onlyInCopyGems(ctx context.Context, tx *sql.Tx, rels []relation, copies contentCopies) ([]newRow, error) {
	const files = `SELECT e.id, e.path, e.size, fc.state, coalesce(fc.content_id, 0)
		FROM entries e JOIN file_content fc ON fc.entry_id = e.id
		WHERE e.source_id = ?1 AND e.kind = 'file' AND e.state = 'present' AND `
	below, err := tx.PrepareContext(ctx, files+`e.path >= ?2 AND e.path < ?3 ORDER BY e.path`)
	if err != nil {
		return nil, err
	}
	defer below.Close()
	all, err := tx.PrepareContext(ctx, files+`e.path > X'' ORDER BY e.path`)
	if err != nil {
		return nil, err
	}
	defer all.Close()
	var out []newRow
	seen := map[int64]bool{}
	type found struct {
		row  newRow
		path string
	}
	for _, r := range rels {
		if r.kind != "overlap" {
			continue
		}
		var rel []found
		for _, s := range []side{r.a, r.b} {
			if s.member != 0 || s.kind != string(domain.EntryDirectory) {
				continue
			}
			var (
				rows *sql.Rows
				err  error
			)
			if s.path == "" { // the source root
				rows, err = all.QueryContext(ctx, s.src)
			} else {
				rows, err = below.QueryContext(ctx, s.src, []byte(s.path+"/"), []byte(s.path+"0"))
			}
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var (
					id, size, content int64
					p                 []byte
					state             string
				)
				if err := rows.Scan(&id, &p, &size, &state, &content); err != nil {
					rows.Close()
					return nil, err
				}
				if seen[id] || !unique(state, content, copies) {
					continue
				}
				seen[id] = true
				rel = append(rel, found{path: key(s.src, string(p)), row: newRow{list: ListGemsOnlyInCopy, source: s.src,
					entry: id, group: s.entry, bytes: size, files: 1, sortKey: r.id}})
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
		slices.SortFunc(rel, func(a, b found) int { return strings.Compare(a.path, b.path) })
		for _, f := range rel {
			out = append(out, f.row)
		}
	}
	return out, nil
}
