package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"precious/internal/archive"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/search"
)

type entryBody struct {
	Entry          entryRow       `json:"entry"`
	Ancestors      []ancestor     `json:"ancestors"`
	Classification classification `json:"classification"`
	Intent         intent         `json:"intent"`
	Stats          *folderStats   `json:"stats"`
	// The R2 fields (design D16): a file's or file member's content, the
	// relations of a folder, archive, or member folder, the archive of an
	// archive file Precious opened, and the coverage of every source
	// together, which every claim of no other copy carries (I7).
	Content   *contentJSON   `json:"content"`
	Relations []relationJSON `json:"relations"`
	Archive   *archiveJSON   `json:"archive"`
	Coverage  coverageJSON   `json:"coverage"`
	// The r2b fields (design D9, D10): the only child of a folder that
	// holds exactly one entry, a folder; and why an archive file has no
	// archive.
	OnlyFolder  *string      `json:"only_folder"`
	ArchiveNote *archiveNote `json:"archive_note"`
}

type ancestor struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	NameB64 []byte `json:"name_b64"`
	// OnlyChild is true when the ancestor holds nothing but the next
	// ancestor, or the entry itself for the last one (r2b design D9).
	OnlyChild bool `json:"only_child"`
}

type classification struct {
	Category   *domain.Category `json:"category"`
	Family     *domain.Family   `json:"family"`
	Traits     []domain.Trait   `json:"traits"`
	Triage     *domain.Triage   `json:"triage"`
	Group      bool             `json:"group"`
	Veto       bool             `json:"veto"`
	Rules      []ruleJSON       `json:"rules"`
	Indicators []indicator      `json:"indicators"`
}

type ruleJSON struct {
	ID      string `json:"id"`
	Explain string `json:"explain"`
}

// indicator is one element of dir_stats.indicators, read and sent as the
// scanner stores it.
type indicator struct {
	EntryID string `json:"entry_id"`
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
	Signal  string `json:"signal"`
}

// insideItem is one element of dir_stats.inside (design D21), read and sent
// as the scanner stores it: a notable entry below the folder.
type insideItem struct {
	EntryID  string           `json:"entry_id"`
	Path     string           `json:"path"`
	PathB64  []byte           `json:"path_b64"`
	Category *domain.Category `json:"category"`
	Family   *domain.Family   `json:"family"`
	Group    bool             `json:"group"`
	Bytes    int64            `json:"bytes"`
	Files    int64            `json:"files"`
}

type intent struct {
	Decision    *domain.Decision `json:"decision"`
	EffDecision domain.Decision  `json:"eff_decision"`
	From        *ref             `json:"from"`
	Tags        []tagRef         `json:"tags"`
}

type ref struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
}

type tagRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Own  bool   `json:"own"`
	From *ref   `json:"from"`
}

type folderStats struct {
	Dirs            int64        `json:"dirs"`
	Files           int64        `json:"files"`
	Unreadable      int64        `json:"unreadable"`
	MountBoundaries int64        `json:"mount_boundaries"`
	ByKind          []kindAmount `json:"by_kind"`
	ByYear          []yearAmount `json:"by_year"`
	Inside          []insideItem `json:"inside"`
}

// entry serves GET /api/entries/{id}: the entry's EntryRow, its ancestors
// from the root to its parent, its classification with each rule's
// explanation and the folder's indicators, its intent (decisions.Effective),
// and a folder's stats with its notable entries (null for any other kind;
// zero for a folder not scanned yet); and its content, relations, archive,
// and the global coverage; a folder's only folder, and an archive file's
// note. A member ("m<id>") reads as readMember says.
func (h *handler) entry(w http.ResponseWriter, r *http.Request) {
	ref, err := pathRef(r)
	if err == nil {
		err = checkParams(r.URL.Query())
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		var (
			body *entryBody
			err  error
		)
		if ref.IsMember() {
			body, err = h.readMember(ctx, tx, ref.Member)
		} else {
			body, err = h.readEntry(ctx, tx, ref.Entry)
		}
		if err != nil {
			return nil, err
		}
		if body.Content, err = readContent(ctx, tx, ref); err != nil {
			return nil, err
		}
		if body.Relations, err = h.readRelations(ctx, tx, ref); err != nil {
			return nil, err
		}
		if !ref.IsMember() {
			if body.Archive, err = readArchive(ctx, tx, ref.Entry); err != nil {
				return nil, err
			}
			if body.Entry.Kind == domain.EntryDirectory {
				if body.OnlyFolder, err = onlyFolder(ctx, tx, ref.Entry); err != nil {
					return nil, err
				}
			}
		}
		body.ArchiveNote = archiveNoteOf(ref, &body.Entry, body.Archive)
		if body.Coverage, err = readCoverage(ctx, tx, ""); err != nil {
			return nil, err
		}
		return body, nil
	})
}

// readMember reads the detail of an archive member: its row, its ancestors
// (the archive's, the archive, and the member folders above it), no
// classification, the intent of its archive (the decision and tags it
// reads, all inherited), and a member folder's stats.
func (h *handler) readMember(ctx context.Context, tx *sql.Tx, id domain.MemberID) (*entryBody, error) {
	m, agg, err := memberByID(ctx, tx, h.pol, id)
	if err != nil {
		return nil, err
	}
	body := &entryBody{Entry: rowJSON(&m.row),
		Classification: classification{Traits: []domain.Trait{}, Rules: []ruleJSON{}, Indicators: []indicator{}}}
	if body.Ancestors, err = memberAncestors(ctx, tx, m); err != nil {
		return nil, err
	}
	in, err := decisions.Effective(ctx, tx, m.archive)
	if err != nil {
		return nil, err
	}
	// An own tag of the archive is inherited from the archive.
	archive := &ref{ID: m.archive.String()}
	if err := tx.QueryRowContext(ctx, `SELECT path FROM entries WHERE id = ?`, int64(m.archive)).Scan(&archive.PathB64); err != nil {
		return nil, fmt.Errorf("api: archive %s: %w", m.archive, err)
	}
	archive.Path = domain.DisplayName(archive.PathB64)
	body.Intent = intent{EffDecision: in.Effective, From: refJSON(in.From), Tags: make([]tagRef, len(in.Tags))}
	for i, t := range in.Tags {
		from := refJSON(t.From)
		if t.Own {
			from = archive
		}
		body.Intent.Tags[i] = tagRef{ID: t.ID, Name: t.Name, From: from}
	}
	if agg != nil {
		body.Stats = &folderStats{Dirs: agg.dirs, Files: agg.files, ByKind: agg.b.kindList(), ByYear: agg.b.yearList(),
			Inside: []insideItem{}}
	}
	return body, nil
}

func (h *handler) readEntry(ctx context.Context, tx *sql.Tx, id domain.EntryID) (*entryBody, error) {
	var (
		traits, ruleIDs, byKind, byYear, indicators, inside []byte
		hasStats                                            bool
		dirs, files, unreadable, mounts                     int64
	)
	rows, err := tx.QueryContext(ctx, `SELECT e.traits, e.rule_ids, ds.entry_id IS NOT NULL,
		ifnull(ds.dirs, 0), ifnull(ds.files, 0), ifnull(ds.unreadable, 0), ifnull(ds.mount_boundaries, 0),
		ifnull(ds.by_kind, '{}'), ifnull(ds.by_year, '{}'), ifnull(ds.indicators, '[]'), ifnull(ds.inside, '[]'),
		`+search.Columns+` FROM `+search.From+` WHERE e.id = ?`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("api: entry %s: %w", id, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("api: entry %s: %w", id, err)
		}
		return nil, notFound(id)
	}
	row, err := search.ScanRow(rows, &traits, &ruleIDs, &hasStats, &dirs, &files, &unreadable, &mounts,
		&byKind, &byYear, &indicators, &inside)
	if err != nil {
		return nil, fmt.Errorf("api: entry %s: %w", id, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("api: entry %s: %w", id, err)
	}
	items := []search.Row{row}
	if err := search.LoadTags(ctx, tx, items); err != nil {
		return nil, err
	}
	e := &items[0]
	body := &entryBody{Entry: rowJSON(e)}

	if body.Ancestors, err = ancestors(ctx, tx, id); err != nil {
		return nil, err
	}

	c := classification{
		Category: body.Entry.Category, Family: body.Entry.Family, Triage: body.Entry.Triage,
		Group: e.Group, Veto: e.Veto,
		Traits: []domain.Trait{}, Rules: []ruleJSON{}, Indicators: []indicator{},
	}
	if err := unmarshalOpt(traits, &c.Traits); err != nil {
		return nil, fmt.Errorf("api: traits of entry %s: %w", id, err)
	}
	var ids []string
	if err := unmarshalOpt(ruleIDs, &ids); err != nil {
		return nil, fmt.Errorf("api: rule_ids of entry %s: %w", id, err)
	}
	for i, explain := range h.pol.Explain(ids) {
		c.Rules = append(c.Rules, ruleJSON{ID: ids[i], Explain: explain})
	}
	if err := json.Unmarshal(indicators, &c.Indicators); err != nil {
		return nil, fmt.Errorf("api: indicators of entry %s: %w", id, err)
	}
	body.Classification = c

	in, err := decisions.Effective(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	body.Intent = intent{
		Decision: nonEmpty(&in.Own), EffDecision: in.Effective,
		From: refJSON(in.From), Tags: make([]tagRef, len(in.Tags)),
	}
	for i, t := range in.Tags {
		body.Intent.Tags[i] = tagRef{ID: t.ID, Name: t.Name, Own: t.Own, From: refJSON(t.From)}
	}

	if e.Kind == domain.EntryDirectory {
		b := newBreakdowns()
		if err := b.addKinds(byKind); err != nil {
			return nil, err
		}
		if err := b.addYears(byYear); err != nil {
			return nil, err
		}
		stats := &folderStats{
			Dirs: dirs, Files: files, Unreadable: unreadable, MountBoundaries: mounts,
			ByKind: b.kindList(), ByYear: b.yearList(), Inside: []insideItem{},
		}
		if err := json.Unmarshal(inside, &stats.Inside); err != nil {
			return nil, fmt.Errorf("api: inside of entry %s: %w", id, err)
		}
		body.Stats = stats
	}
	return body, nil
}

// ancestors lists the folders above id, from the root to its parent. Each
// one's only_child is one probe of the children index for a child other
// than the next ancestor (the entry itself for the parent), in every state,
// as the children listing shows them.
func ancestors(ctx context.Context, tx *sql.Tx, id domain.EntryID) ([]ancestor, error) {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE up(id, child, depth) AS (
			SELECT parent_id, id, 1 FROM entries WHERE id = ? AND parent_id IS NOT NULL
			UNION ALL
			SELECT e.parent_id, e.id, u.depth + 1 FROM up u JOIN entries e ON e.id = u.id WHERE e.parent_id IS NOT NULL
		)
		SELECT e.id, e.name, NOT EXISTS (SELECT 1 FROM entries c WHERE c.parent_id = u.id AND c.id <> u.child)
		FROM up u JOIN entries e ON e.id = u.id ORDER BY u.depth DESC`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("api: ancestors of %s: %w", id, err)
	}
	defer rows.Close()
	out := []ancestor{}
	for rows.Next() {
		var (
			a    int64
			name []byte
			only bool
		)
		if err := rows.Scan(&a, &name, &only); err != nil {
			return nil, fmt.Errorf("api: ancestors of %s: %w", id, err)
		}
		if name == nil {
			name = []byte{}
		}
		out = append(out, ancestor{ID: domain.EntryID(a).String(), Name: domain.DisplayName(name), NameB64: name, OnlyChild: only})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: ancestors of %s: %w", id, err)
	}
	return out, nil
}

// onlyFolder returns the ID of the only child of folder id when it holds
// exactly one entry, in any state, and that entry is a folder; nil
// otherwise (r2b design D9). It reads at most two children by the
// children index.
func onlyFolder(ctx context.Context, tx *sql.Tx, id domain.EntryID) (*string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, kind FROM entries WHERE parent_id = ? LIMIT 2`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("api: children of %s: %w", id, err)
	}
	defer rows.Close()
	var (
		n     int
		child int64
		kind  domain.EntryKind
	)
	for rows.Next() {
		if err := rows.Scan(&child, &kind); err != nil {
			return nil, fmt.Errorf("api: children of %s: %w", id, err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: children of %s: %w", id, err)
	}
	if n != 1 || kind != domain.EntryDirectory {
		return nil, nil
	}
	s := domain.EntryID(child).String()
	return &s, nil
}

// archiveNote says why an archive file has no archive (r2b design D10).
type archiveNote string

const (
	// noteUnsupported: a known archive format Precious does not open.
	noteUnsupported archiveNote = "unsupported"
	// noteNested: a member that is an archive; archives inside archives
	// are not opened.
	noteNested archiveNote = "nested"
	// noteNotListed: a format Precious opens, not listed yet.
	noteNotListed archiveNote = "not_listed"
)

// archiveNoteOf returns the note of the file e that ref names, whose
// archive is a; nil for anything that is no archive file, or that has an
// archive (its state says the rest).
func archiveNoteOf(ref domain.Ref, e *entryRow, a *archiveJSON) *archiveNote {
	if e.Kind != domain.EntryFile || a != nil {
		return nil
	}
	_, opened := archive.Classify(e.NameB64)
	unsupported := domain.UnsupportedArchive(e.NameB64)
	var n archiveNote
	switch {
	case ref.IsMember():
		if !opened && !unsupported {
			return nil
		}
		n = noteNested
	case unsupported:
		n = noteUnsupported
	// A missing or unreadable file is not waiting for its listing: its
	// state says why.
	case opened && e.State == "present":
		n = noteNotListed
	default:
		return nil
	}
	return &n
}

func refJSON(r *decisions.Ref) *ref {
	if r == nil {
		return nil
	}
	return &ref{ID: r.ID.String(), Path: r.Path, PathB64: r.PathB64}
}

// unmarshalOpt decodes a nullable JSON column into v, leaving v as it is
// for NULL.
func unmarshalOpt(raw []byte, v any) error {
	if raw == nil {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func notFound(id domain.EntryID) error {
	return domain.Errorf(domain.CodeNotFound, "entry %s not found", id)
}

// entryExists fails with not_found when id names no entry.
func entryExists(ctx context.Context, tx *sql.Tx, id domain.EntryID) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM entries WHERE id = ?`, int64(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound(id)
	}
	if err != nil {
		return fmt.Errorf("api: entry %s: %w", id, err)
	}
	return nil
}
