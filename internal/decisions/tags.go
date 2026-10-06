package decisions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/store"
)

// MaxTagName bounds a tag name, in characters.
const MaxTagName = 64

// Tag is one owner tag.
type Tag struct {
	ID   int64
	Name string
}

// SetTags is one set-tags request: exactly one of EntryIDs and SelectionID
// names the targets; Add and Remove are tag IDs, at least one in all, none
// in both.
type SetTags struct {
	EntryIDs    []domain.EntryID
	SelectionID string
	Add, Remove []int64
}

// Validate checks the request's shape; failures are invalid_request.
func (r SetTags) Validate() error {
	if err := validTargets(false, r.EntryIDs, r.SelectionID); err != nil {
		return err
	}
	if len(r.Add) == 0 && len(r.Remove) == 0 {
		return domain.Errorf(domain.CodeInvalidRequest, "add or remove at least one tag")
	}
	for _, id := range slices.Concat(r.Add, r.Remove) {
		if id <= 0 {
			return domain.Errorf(domain.CodeInvalidRequest, "invalid tag id %d", id)
		}
	}
	for _, id := range r.Add {
		if slices.Contains(r.Remove, id) {
			return domain.Errorf(domain.CodeInvalidRequest, "tag %d is both added and removed", id)
		}
	}
	return nil
}

// SetTags adds and removes own tags on the targets of req and returns how
// many entries it targeted. Removing a tag removes only the targets' own
// rows: a tag an entry inherits stays until it is removed from the folder
// carrying it. An unknown entry or tag is not_found and an expired selection
// selection_expired, before anything changes.
func (s *Service) SetTags(ctx context.Context, tx *sql.Tx, req SetTags) (applied int, err error) {
	if err := req.Validate(); err != nil {
		return 0, err
	}
	now := s.clk.Now()
	add := slices.Compact(slices.Sorted(slices.Values(req.Add)))
	remove := slices.Compact(slices.Sorted(slices.Values(req.Remove)))
	if err := tagsExist(ctx, tx, slices.Concat(add, remove)); err != nil {
		return 0, err
	}
	t, err := resolveTargets(ctx, tx, now, req.EntryIDs, req.SelectionID)
	if err != nil {
		return 0, err
	}
	var added, removed int64
	if len(add) > 0 {
		args := append([]any{clock.Millis(now)}, t.args...)
		r, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entry_tags (entry_id, tag_id, added_at)
			SELECT x.id, g.value, ? FROM (`+t.sub+`) x CROSS JOIN json_each(?) g`, append(args, idArray(add))...)
		if err != nil {
			return 0, fmt.Errorf("decisions: add tags: %w", err)
		}
		if added, err = r.RowsAffected(); err != nil {
			return 0, fmt.Errorf("decisions: add tags: %w", err)
		}
	}
	if len(remove) > 0 {
		r, err := tx.ExecContext(ctx, `DELETE FROM entry_tags WHERE entry_id IN (`+t.sub+`)
			AND tag_id IN (SELECT value FROM json_each(?))`, append(slices.Clone(t.args), idArray(remove))...)
		if err != nil {
			return 0, fmt.Errorf("decisions: remove tags: %w", err)
		}
		if removed, err = r.RowsAffected(); err != nil {
			return 0, fmt.Errorf("decisions: remove tags: %w", err)
		}
	}
	detail := t.audit()
	detail["add"], detail["remove"] = add, remove
	detail["applied"], detail["added"], detail["removed"] = t.n, added, removed
	if err := s.audit(ctx, tx, now, AuditTagsSet, detail); err != nil {
		return 0, err
	}
	return t.n, nil
}

// tagsExist fails with not_found naming the first of ids (sorted, distinct)
// that is no tag.
func tagsExist(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var missing int64
	err := tx.QueryRowContext(ctx, `SELECT value FROM json_each(?) WHERE value NOT IN (SELECT id FROM tags) LIMIT 1`,
		idArray(ids)).Scan(&missing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decisions: check tags: %w", err)
	}
	return domain.Errorf(domain.CodeNotFound, "tag %d not found", missing)
}

// CreateTag creates the tag name: 1 to MaxTagName characters once spaces
// around it are trimmed (invalid_request otherwise), and unique ignoring case
// (tag_exists otherwise).
func (s *Service) CreateTag(ctx context.Context, tx *sql.Tx, name string) (Tag, error) {
	name, err := tagName(name)
	if err != nil {
		return Tag{}, err
	}
	if err := nameFree(ctx, tx, name, 0); err != nil {
		return Tag{}, err
	}
	now := s.clk.Now()
	r, err := tx.ExecContext(ctx, `INSERT INTO tags (name, created_at) VALUES (?, ?)`, name, clock.Millis(now))
	if err != nil {
		return Tag{}, fmt.Errorf("decisions: create tag: %w", err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return Tag{}, fmt.Errorf("decisions: create tag: %w", err)
	}
	tag := Tag{ID: id, Name: name}
	if err := s.audit(ctx, tx, now, AuditTagCreated, map[string]any{"tag_id": id, "name": name}); err != nil {
		return Tag{}, err
	}
	return tag, nil
}

// RenameTag renames tag id, under the rules of CreateTag; its assignments
// stay. An unknown tag is not_found.
func (s *Service) RenameTag(ctx context.Context, tx *sql.Tx, id int64, name string) (Tag, error) {
	name, err := tagName(name)
	if err != nil {
		return Tag{}, err
	}
	old, err := tagByID(ctx, tx, id)
	if err != nil {
		return Tag{}, err
	}
	if err := nameFree(ctx, tx, name, id); err != nil {
		return Tag{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tags SET name = ? WHERE id = ?`, name, id); err != nil {
		return Tag{}, fmt.Errorf("decisions: rename tag: %w", err)
	}
	if err := s.audit(ctx, tx, s.clk.Now(), AuditTagRenamed,
		map[string]any{"tag_id": id, "old": old.Name, "new": name}); err != nil {
		return Tag{}, err
	}
	return Tag{ID: id, Name: name}, nil
}

// DeleteTag deletes tag id and its assignments everywhere, and returns the
// deleted tag. An unknown tag is not_found.
func (s *Service) DeleteTag(ctx context.Context, tx *sql.Tx, id int64) (Tag, error) {
	tag, err := tagByID(ctx, tx, id)
	if err != nil {
		return Tag{}, err
	}
	var entries int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM entry_tags WHERE tag_id = ?`, id).Scan(&entries); err != nil {
		return Tag{}, fmt.Errorf("decisions: delete tag: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, id); err != nil {
		return Tag{}, fmt.Errorf("decisions: delete tag: %w", err)
	}
	if err := s.audit(ctx, tx, s.clk.Now(), AuditTagDeleted,
		map[string]any{"tag_id": id, "name": tag.Name, "entries": entries}); err != nil {
		return Tag{}, err
	}
	return tag, nil
}

// tagName trims name and checks its length.
func tagName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch n := utf8.RuneCountInString(name); {
	case !utf8.ValidString(name):
		return "", domain.Errorf(domain.CodeInvalidRequest, "a tag name must be valid UTF-8")
	case n == 0:
		return "", domain.Errorf(domain.CodeInvalidRequest, "a tag name must not be empty")
	case n > MaxTagName:
		return "", domain.Errorf(domain.CodeInvalidRequest, "a tag name has at most %d characters, got %d", MaxTagName, n)
	}
	return name, nil
}

// nameFree fails with tag_exists when a tag other than except carries name
// ignoring case. Unicode case folding is compared here: the column's
// NOCASE collation folds ASCII letters only, so "Família" and "FAMÍLIA" would
// both pass it.
func nameFree(ctx context.Context, tx *sql.Tx, name string, except int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM tags WHERE id <> ?`, except)
	if err != nil {
		return fmt.Errorf("decisions: check tag names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return fmt.Errorf("decisions: check tag names: %w", err)
		}
		if strings.EqualFold(t.Name, name) {
			return domain.Errorf(domain.CodeTagExists, "the tag %q already exists", t.Name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("decisions: check tag names: %w", err)
	}
	return nil
}

func tagByID(ctx context.Context, q store.Queryer, id int64) (Tag, error) {
	t := Tag{ID: id}
	err := q.QueryRowContext(ctx, `SELECT name FROM tags WHERE id = ?`, id).Scan(&t.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, domain.Errorf(domain.CodeNotFound, "tag %d not found", id)
	}
	if err != nil {
		return Tag{}, fmt.Errorf("decisions: read tag: %w", err)
	}
	return t, nil
}

// effectiveTags returns the tags of entry id and its ancestors, by name,
// each once: own when id carries it, otherwise from the nearest ancestor
// carrying it.
func effectiveTags(ctx context.Context, q store.Queryer, id domain.EntryID) ([]TagRef, error) {
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE anc(id, depth) AS (
			SELECT ?, 0
			UNION ALL
			SELECT e.parent_id, a.depth + 1 FROM anc a JOIN entries e ON e.id = a.id WHERE e.parent_id IS NOT NULL
		)
		SELECT t.id, t.name, a.depth, a.id, e.path
		FROM anc a JOIN entry_tags et ON et.entry_id = a.id JOIN tags t ON t.id = et.tag_id JOIN entries e ON e.id = a.id
		ORDER BY t.name, t.id, a.depth`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("decisions: read tags: %w", err)
	}
	defer rows.Close()
	out := []TagRef{}
	for rows.Next() {
		var (
			ref   TagRef
			depth int
			from  int64
			path  []byte
		)
		if err := rows.Scan(&ref.ID, &ref.Name, &depth, &from, &path); err != nil {
			return nil, fmt.Errorf("decisions: read tags: %w", err)
		}
		if n := len(out); n > 0 && out[n-1].ID == ref.ID {
			continue // a farther carrier of the same tag
		}
		if depth == 0 {
			ref.Own = true
		} else {
			ref.From = &Ref{ID: domain.EntryID(from), Path: domain.DisplayName(path), PathB64: nonNil(path)}
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions: read tags: %w", err)
	}
	return out, nil
}
