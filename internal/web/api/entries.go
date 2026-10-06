package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
}

type ancestor struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	NameB64 []byte `json:"name_b64"`
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
// zero for a folder not scanned yet).
func (h *handler) entry(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = checkParams(r.URL.Query())
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		return h.readEntry(ctx, tx, id)
	})
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

// ancestors lists the folders above id, from the root to its parent.
func ancestors(ctx context.Context, tx *sql.Tx, id domain.EntryID) ([]ancestor, error) {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE up(id, depth) AS (
			SELECT parent_id, 1 FROM entries WHERE id = ? AND parent_id IS NOT NULL
			UNION ALL
			SELECT e.parent_id, u.depth + 1 FROM up u JOIN entries e ON e.id = u.id WHERE e.parent_id IS NOT NULL
		)
		SELECT e.id, e.name FROM up u JOIN entries e ON e.id = u.id ORDER BY u.depth DESC`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("api: ancestors of %s: %w", id, err)
	}
	defer rows.Close()
	out := []ancestor{}
	for rows.Next() {
		var (
			a    int64
			name []byte
		)
		if err := rows.Scan(&a, &name); err != nil {
			return nil, fmt.Errorf("api: ancestors of %s: %w", id, err)
		}
		if name == nil {
			name = []byte{}
		}
		out = append(out, ancestor{ID: domain.EntryID(a).String(), Name: domain.DisplayName(name), NameB64: name})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: ancestors of %s: %w", id, err)
	}
	return out, nil
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
