package index

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"precious/internal/domain"
	"precious/internal/store"
)

// The types below describe a step the organize executor has done on disk,
// for the functions that make the index follow it (r3 design D6). They are
// shared with internal/executor, which passes them through its Index
// interface.

// PostFacts are the lstat facts of an entry right after a step: renaming an
// entry changes its ctime, and changing a folder changes its mtime and ctime,
// so the index takes them from the disk instead of keeping stale ones.
type PostFacts struct {
	Dev, Ino         uint64
	MtimeNs, CtimeNs int64
}

// Move is a done rename: Entry, with everything below it, now sits in
// NewParent under NewName, inside the same source.
type Move struct {
	Source         domain.SourceID
	Entry          domain.EntryID
	NewParent      domain.EntryID
	NewName        []byte
	Facts          PostFacts // the moved entry
	OldParentFacts PostFacts // the folder it left
	NewParentFacts PostFacts // the folder it entered
}

// NewFolder is a done mkdir: an empty folder Name inside Parent.
type NewFolder struct {
	Source      domain.SourceID
	Parent      domain.EntryID
	Name        []byte
	Facts       PostFacts // the new folder
	ParentFacts PostFacts // the folder that holds it
}

// ErrMissingIntent refuses to delete a missing entry that holds the name a
// step needs while it, or an entry below it, carries the owner's intent: an
// own decision, a tag, a classification override, or a date correction (r3
// design D6 step 1, r5 design D11, I4). Nothing was changed.
var ErrMissingIntent = errors.New("index: a missing entry with the owner's decision, tags, classification, or date correction holds the name")

// place is what the functions below read of an entry.
type place struct {
	id      domain.EntryID
	source  domain.SourceID
	parent  sql.NullInt64
	name    []byte
	path    []byte
	kind    string
	state   string
	eff     string
	effFrom opt
	// The own facts a step may change, as stored.
	size                  int64
	mtime, ctime, dev, in opt
}

const placeColumns = `id, source_id, parent_id, name, path, kind, state, eff_decision, eff_from, size,
	mtime_ns, ctime_ns, dev, ino`

func scanPlace(r scanner) (*place, error) {
	var p place
	err := r.Scan(&p.id, &p.source, &p.parent, &p.name, &p.path, &p.kind, &p.state, &p.eff, &p.effFrom, &p.size,
		&p.mtime, &p.ctime, &p.dev, &p.in)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.path == nil {
		p.path = []byte{}
	}
	return &p, nil
}

// placeByID returns entry id, or nil when there is none.
func placeByID(ctx context.Context, q store.Queryer, id domain.EntryID) (*place, error) {
	p, err := scanPlace(q.QueryRowContext(ctx, `SELECT `+placeColumns+` FROM entries WHERE id = ?`, int64(id)))
	if err != nil {
		return nil, fmt.Errorf("index: read entry %d: %w", id, err)
	}
	return p, nil
}

// placeAt returns the entry at path in src, or nil when there is none.
func placeAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (*place, error) {
	p, err := scanPlace(q.QueryRowContext(ctx, `SELECT `+placeColumns+` FROM entries WHERE source_id = ? AND path = ?`,
		string(src), path))
	if err != nil {
		return nil, fmt.Errorf("index: read the entry at %q: %w", displayPath(path), err)
	}
	return p, nil
}

// Statements over a subtree: each reads the (source_id, path) index over
// the range [path+'/', path+'0') (subtree), never every entry.
const (
	// intentSQL tells whether the entry at a path, or one below it, carries
	// the owner's intent: source, path, source, lo, hi.
	intentSQL = `SELECT EXISTS (SELECT 1 FROM entries e WHERE e.source_id = ? AND e.path = ? AND ` + intentCond + `)
		OR EXISTS (SELECT 1 FROM entries e WHERE e.source_id = ? AND e.path >= ? AND e.path < ? AND ` + intentCond + `)`
	intentCond = `(e.decision IS NOT NULL OR EXISTS (SELECT 1 FROM entry_tags t WHERE t.entry_id = e.id)
		OR EXISTS (SELECT 1 FROM entry_overrides o WHERE o.entry_id = e.id)
		OR EXISTS (SELECT 1 FROM date_corrections dc WHERE dc.entry_id = e.id))`
	// intentBelowSQL tells whether an entry below a path carries the
	// owner's intent: source, lo, hi.
	intentBelowSQL = `SELECT EXISTS (SELECT 1 FROM entries e WHERE e.source_id = ? AND e.path >= ? AND e.path < ?
		AND ` + intentCond + `)`
	// presentBelowSQL tells whether an entry below a path is not missing:
	// source, lo, hi.
	presentBelowSQL = `SELECT EXISTS (SELECT 1 FROM entries WHERE source_id = ? AND path >= ? AND path < ?
		AND state <> 'missing')`
	// deleteBelowSQL deletes the entries below a path: source, lo, hi. Their
	// entry_names rows go first (writerQueries[stDeleteSubNames]).
	deleteBelowSQL = `DELETE FROM entries WHERE source_id = ? AND path >= ? AND path < ?`
	// movePathsSQL rewrites the prefix of the paths below a folder: the new
	// path, the length of the old one plus one, source, lo, hi. The
	// concatenation is bytewise; the cast keeps the paths BLOBs.
	movePathsSQL = `UPDATE entries SET path = CAST(? || substr(path, ?) AS BLOB)
		WHERE source_id = ? AND path >= ? AND path < ?`
	// folderListsSQL reads the dir_stats lists of the folders below a path:
	// source, lo, hi.
	folderListsSQL = `SELECT d.entry_id, d.indicators, d.inside FROM entries e JOIN dir_stats d ON d.entry_id = e.id
		WHERE e.source_id = ? AND e.path >= ? AND e.path < ? AND e.kind = 'directory'`
)

// intentAt reports whether the entry at path in src, or one below it,
// carries the owner's intent.
func intentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	lo, hi := subtree(path)
	var yes bool
	if err := q.QueryRowContext(ctx, intentSQL, string(src), path, string(src), lo, hi).Scan(&yes); err != nil {
		return false, fmt.Errorf("index: look for the owner's intent at %q: %w", displayPath(path), err)
	}
	return yes, nil
}

// presentBelow reports whether an entry below path in src is not missing.
func presentBelow(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	lo, hi := subtree(path)
	var yes bool
	if err := q.QueryRowContext(ctx, presentBelowSQL, string(src), lo, hi).Scan(&yes); err != nil {
		return false, fmt.Errorf("index: look below %q: %w", displayPath(path), err)
	}
	return yes, nil
}

// MissingIntentAt reports whether a missing row at (src, path), or one below
// it, carries owner intent (r3 design D6 step 1): such a row keeps its name,
// and a step that needs the name is a conflict (name_taken_by_missing). It
// is false when no missing row is at path.
func MissingIntentAt(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	if len(path) == 0 {
		return false, nil // the root is never missing
	}
	p, err := placeAt(ctx, q, src, path)
	if err != nil || p == nil || p.state != "missing" {
		return false, err
	}
	return intentAt(ctx, q, src, path)
}

// IntentBelow reports whether an entry strictly below the folder at path in
// src carries owner intent (its own decision, a tag, an override, or a date
// correction), whatever its state. A missing one is not a child the disk
// shows, yet removing the folder above it would lose that intent (design
// V2); a present one is a child anyway. path is a folder's, never the
// source root's.
func IntentBelow(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	if len(path) == 0 {
		return false, errors.New("index: IntentBelow needs a folder below the source root")
	}
	lo, hi := subtree(path)
	var yes bool
	if err := q.QueryRowContext(ctx, intentBelowSQL, string(src), lo, hi).Scan(&yes); err != nil {
		return false, fmt.Errorf("index: look for the owner's intent below %q: %w", displayPath(path), err)
	}
	return yes, nil
}

// freePath makes the path of a step's new entry free in the index: a
// missing row there goes, with its subtree and their entry_names rows, when
// none of them carries the owner's intent (ErrMissingIntent otherwise); a
// row that is not missing is an error. Nothing is changed on error.
func freePath(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte) error {
	p, err := placeAt(ctx, tx, src, path)
	if err != nil || p == nil {
		return err
	}
	if p.state != "missing" {
		return fmt.Errorf("index: %q is already indexed as present", displayPath(path))
	}
	if below, err := presentBelow(ctx, tx, src, path); err != nil || below {
		if err == nil {
			err = fmt.Errorf("index: the missing entry %q holds entries that are not missing", displayPath(path))
		}
		return err
	}
	intent, err := intentAt(ctx, tx, src, path)
	if err != nil {
		return err
	}
	if intent {
		return fmt.Errorf("%w: %q", ErrMissingIntent, displayPath(path))
	}
	return deleteTree(ctx, tx, p)
}

// deleteTree deletes the entry p, its subtree, and their entry_names rows.
func deleteTree(ctx context.Context, tx *sql.Tx, p *place) error {
	lo, hi := subtree(p.path)
	for _, s := range []struct {
		query string
		args  []any
	}{
		{writerQueries[stDeleteSubNames], []any{string(p.source), lo, hi}},
		{writerQueries[stDeleteNames], []any{int64(p.id)}},
		{deleteBelowSQL, []any{string(p.source), lo, hi}},
		{writerQueries[stDeleteSelf], []any{int64(p.id)}},
	} {
		if _, err := tx.ExecContext(ctx, s.query, s.args...); err != nil {
			return fmt.Errorf("index: delete %q: %w", displayPath(p.path), err)
		}
	}
	return nil
}

// validName refuses a name no folder entry can have.
func validName(name []byte) error {
	if len(name) == 0 || bytes.Equal(name, []byte(".")) || bytes.Equal(name, []byte("..")) ||
		bytes.ContainsAny(name, "/\x00") {
		return fmt.Errorf("index: %q is not a valid name", domain.DisplayName(name))
	}
	return nil
}

// folderIn returns the folder id of src that a step puts an entry into: a
// folder of the same source that is not missing.
func folderIn(ctx context.Context, tx *sql.Tx, src domain.SourceID, id domain.EntryID) (*place, error) {
	p, err := placeByID(ctx, tx, id)
	switch {
	case err != nil:
		return nil, err
	case p == nil:
		return nil, fmt.Errorf("index: folder %d is not indexed", id)
	case p.source != src:
		return nil, fmt.Errorf("index: folder %d is on source %q, not %q", id, p.source, src)
	case p.kind != string(domain.EntryDirectory) || p.state == "missing":
		return nil, fmt.Errorf("index: %q is not a present folder", displayPath(p.path))
	}
	return p, nil
}

// MoveEntry makes the index follow a done rename (r3 design D6 steps 1–5),
// inside the outcome's transaction, and returns the entry's old and new
// paths:
//  1. a missing row at the new path goes, with its subtree and their
//     entry_names rows, unless one of them carries the owner's intent
//     (ErrMissingIntent, and nothing changes);
//  2. the entry takes its new parent, name, and path, and the paths of its
//     subtree are rewritten by range; IDs never change, so decisions,
//     tags, overrides, digests, listings, relations, and selections follow;
//  3. the paths in the dir_stats indicators and inside lists of the entry
//     (a folder) and of every folder below it are rewritten alike;
//  4. its entry_names row changes with its name;
//  5. the post-step facts of the entry and of both parents are written, and
//     a file's file_content, archives, and media_meta rows take its new
//     change time, when they describe the file as stored before the step.
//
// It refuses a move to another source, a move of a folder into itself or
// below itself, a source's root, and an entry that is missing. The caller
// then runs decisions.Reinherit and Refolder.Refold in the same transaction.
func MoveEntry(ctx context.Context, tx *sql.Tx, m Move) (old, new []byte, err error) {
	if err := validName(m.NewName); err != nil {
		return nil, nil, err
	}
	e, err := placeByID(ctx, tx, m.Entry)
	switch {
	case err != nil:
		return nil, nil, err
	case e == nil:
		return nil, nil, fmt.Errorf("index: entry %d is not indexed", m.Entry)
	case e.source != m.Source:
		return nil, nil, fmt.Errorf("index: entry %d is on source %q, not %q", m.Entry, e.source, m.Source)
	case !e.parent.Valid:
		return nil, nil, fmt.Errorf("index: the root of source %q cannot move", m.Source)
	case e.state == "missing":
		return nil, nil, fmt.Errorf("index: %q is missing", displayPath(e.path))
	}
	np, err := folderIn(ctx, tx, m.Source, m.NewParent)
	if err != nil {
		return nil, nil, err
	}
	if e.kind == string(domain.EntryDirectory) && (bytes.Equal(np.path, e.path) || isBelow(np.path, e.path)) {
		return nil, nil, fmt.Errorf("index: %q cannot move into itself", displayPath(e.path))
	}
	old, new = e.path, joinPath(np.path, m.NewName)
	if bytes.Equal(old, new) {
		return nil, nil, fmt.Errorf("index: %q is already at that path", displayPath(old))
	}
	if err := freePath(ctx, tx, m.Source, new); err != nil {
		return nil, nil, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE entries SET parent_id = ?, name = ?, path = ? WHERE id = ?`,
		int64(np.id), m.NewName, new, int64(e.id)); err != nil {
		return nil, nil, fmt.Errorf("index: move %q: %w", displayPath(old), err)
	}
	if e.kind == string(domain.EntryDirectory) {
		lo, hi := subtree(old)
		if _, err := tx.ExecContext(ctx, movePathsSQL, new, len(old)+1, string(m.Source), lo, hi); err != nil {
			return nil, nil, fmt.Errorf("index: move the entries below %q: %w", displayPath(old), err)
		}
		if err := movePathLists(ctx, tx, m.Source, e.id, old, new); err != nil {
			return nil, nil, err
		}
	}
	if !bytes.Equal(e.name, m.NewName) {
		if err := rename(ctx, tx, e.id, m.NewName); err != nil {
			return nil, nil, err
		}
	}
	if err := writeFacts(ctx, tx, e.id, m.Facts); err != nil {
		return nil, nil, err
	}
	if e.kind == string(domain.EntryFile) {
		if err := keepContent(ctx, tx, e, m.Facts); err != nil {
			return nil, nil, err
		}
	}
	if np.id != domain.EntryID(e.parent.Int64) {
		if err := writeFacts(ctx, tx, domain.EntryID(e.parent.Int64), m.OldParentFacts); err != nil {
			return nil, nil, err
		}
	}
	if err := writeFacts(ctx, tx, np.id, m.NewParentFacts); err != nil {
		return nil, nil, err
	}
	return old, new, nil
}

// isBelow reports whether path p lies strictly below the folder at dir.
func isBelow(p, dir []byte) bool {
	if len(dir) == 0 {
		return len(p) > 0
	}
	return len(p) > len(dir) && p[len(dir)] == '/' && bytes.HasPrefix(p, dir)
}

// rename replaces the entry_names row of id.
func rename(ctx context.Context, tx *sql.Tx, id domain.EntryID, name []byte) error {
	if _, err := tx.ExecContext(ctx, writerQueries[stDeleteNames], int64(id)); err != nil {
		return fmt.Errorf("index: rename entry %d in the name index: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entry_names (rowid, name) VALUES (?, ?)`,
		int64(id), domain.DisplayName(name)); err != nil {
		return fmt.Errorf("index: rename entry %d in the name index: %w", id, err)
	}
	return nil
}

// factsArgs are the bind values of post-step facts, encoded as setFacts
// encodes an Lstat: identity as the bit pattern of the unsigned values, and
// a change time of 0 (unknown) as NULL.
func factsArgs(f PostFacts) []any {
	var ctime any
	if f.CtimeNs != 0 {
		ctime = f.CtimeNs
	}
	return []any{f.MtimeNs, ctime, int64(f.Dev), int64(f.Ino)}
}

// writeFacts writes post-step facts to entry id.
func writeFacts(ctx context.Context, tx *sql.Tx, id domain.EntryID, f PostFacts) error {
	if _, err := tx.ExecContext(ctx, `UPDATE entries SET mtime_ns = ?, ctime_ns = ?, dev = ?, ino = ? WHERE id = ?`,
		append(factsArgs(f), int64(id))...); err != nil {
		return fmt.Errorf("index: write the facts of entry %d: %w", id, err)
	}
	return nil
}

// keepContent gives the file e's file_content, archives, and media_meta
// rows its change time after a rename, so that hashing, listing, and header
// reading do not take the rename for a change. Only rows that describe the
// file as e stored it before the step (size, times, inode) are updated, and
// only when the step left its modification time and inode as stored:
// anything else is a change since the last read, which those jobs must see.
func keepContent(ctx context.Context, tx *sql.Tx, e *place, f PostFacts) error {
	if !e.mtime.ok || e.mtime.v != f.MtimeNs || !e.in.ok || uint64(e.in.v) != f.Ino {
		return nil
	}
	var ctime any
	if f.CtimeNs != 0 {
		ctime = f.CtimeNs
	}
	for _, table := range contentTables {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET ctime_ns = ? WHERE entry_id = ? AND size = ?
			AND mtime_ns IS ? AND ctime_ns IS ? AND ino IS ?`,
			ctime, int64(e.id), e.size, e.mtime.arg(), e.ctime.arg(), e.in.arg()); err != nil {
			return fmt.Errorf("index: keep the content rows of %q: %w", displayPath(e.path), err)
		}
	}
	return nil
}

// movePathLists rewrites the old path prefix to the new one in the
// dir_stats lists of the moved folder id, now at new, and of every folder
// below it. Each list keeps its order: the paths it holds all share the
// prefix.
func movePathLists(ctx context.Context, tx *sql.Tx, src domain.SourceID, id domain.EntryID, old, new []byte) error {
	type lists struct {
		id                 int64
		indicators, inside string
	}
	var all []lists
	read := func(rows *sql.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l lists
			if err := rows.Scan(&l.id, &l.indicators, &l.inside); err != nil {
				return err
			}
			all = append(all, l)
		}
		return rows.Err()
	}
	lo, hi := subtree(new)
	if err := read(tx.QueryContext(ctx, `SELECT entry_id, indicators, inside FROM dir_stats WHERE entry_id = ?`,
		int64(id))); err != nil {
		return fmt.Errorf("index: read the lists of %q: %w", displayPath(new), err)
	}
	if err := read(tx.QueryContext(ctx, folderListsSQL, string(src), lo, hi)); err != nil {
		return fmt.Errorf("index: read the lists below %q: %w", displayPath(new), err)
	}
	for _, l := range all {
		ind, err := rewriteList[indicatorJSON](l.indicators, old, new,
			func(j *indicatorJSON) *[]byte { return &j.PathB64 }, func(j *indicatorJSON, p string) { j.Path = p })
		if err != nil {
			return fmt.Errorf("index: rewrite the indicators of entry %d: %w", l.id, err)
		}
		ins, err := rewriteList[insideJSON](l.inside, old, new,
			func(j *insideJSON) *[]byte { return &j.PathB64 }, func(j *insideJSON, p string) { j.Path = p })
		if err != nil {
			return fmt.Errorf("index: rewrite the inside list of entry %d: %w", l.id, err)
		}
		if ind == l.indicators && ins == l.inside {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE dir_stats SET indicators = ?, inside = ? WHERE entry_id = ?`,
			ind, ins, l.id); err != nil {
			return fmt.Errorf("index: rewrite the lists of entry %d: %w", l.id, err)
		}
	}
	return nil
}

// rewriteList rewrites, in the JSON list doc, every path below old to the
// same path below new, as the writer renders them (path_b64 raw, path for
// display).
func rewriteList[T any](doc string, old, new []byte, raw func(*T) *[]byte, display func(*T, string)) (string, error) {
	var list []T
	if err := json.Unmarshal([]byte(doc), &list); err != nil {
		return "", err
	}
	changed := false
	for i := range list {
		p := raw(&list[i])
		if !isBelow(*p, old) {
			continue
		}
		*p = append(append([]byte(nil), new...), (*p)[len(old):]...)
		display(&list[i], displayPath(*p))
		changed = true
	}
	if !changed {
		return doc, nil
	}
	out, err := json.Marshal(list)
	return string(out), err
}

// InsertFolder makes the index follow a done mkdir (r3 design D6): it
// inserts the folder, empty, under its parent, as a scan inserts a folder,
// with its post-step facts, the effective decision its parent passes on, its
// entry_names row, and the dir_stats of an empty folder, and writes the
// parent's post-step facts. A missing row at the path goes first, as for a
// move (ErrMissingIntent when it carries the owner's intent). The caller
// then runs Refolder.Refold on the new folder, which classifies it.
func InsertFolder(ctx context.Context, tx *sql.Tx, f NewFolder) (domain.EntryID, error) {
	if err := validName(f.Name); err != nil {
		return 0, err
	}
	p, err := folderIn(ctx, tx, f.Source, f.Parent)
	if err != nil {
		return 0, err
	}
	path := joinPath(p.path, f.Name)
	if err := freePath(ctx, tx, f.Source, path); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO entries (source_id, parent_id, name, path, kind, state,
		mtime_ns, ctime_ns, dev, ino, first_seen, last_seen, scan_gen, eff_decision, eff_from)
		VALUES (?, ?, ?, ?, 'directory', 'present', ?, ?, ?, ?, `+nowMillis+`, `+nowMillis+`,
		(SELECT scan_gen FROM sources WHERE id = ?), ?, ?) RETURNING id`,
		append(append([]any{string(f.Source), int64(p.id), f.Name, path}, factsArgs(f.Facts)...),
			string(f.Source), p.eff, p.effFrom.arg())...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("index: insert the folder %q: %w", displayPath(path), err)
	}
	cols := emptyStats(newCodec())
	if _, err := tx.ExecContext(ctx, writerQueries[stStats], id, cols.dirs, cols.files, cols.symlinks,
		cols.specials, cols.unreadable, cols.mounts, cols.byKind, cols.byYear, cols.byFamily, cols.signals,
		cols.indicators, cols.inside); err != nil {
		return 0, fmt.Errorf("index: insert the folder %q: %w", displayPath(path), err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entry_names (rowid, name) VALUES (?, ?)`,
		id, domain.DisplayName(f.Name)); err != nil {
		return 0, fmt.Errorf("index: insert the folder %q: %w", displayPath(path), err)
	}
	if err := writeFacts(ctx, tx, p.id, f.ParentFacts); err != nil {
		return 0, err
	}
	return domain.EntryID(id), nil
}

// nowMillis is the current time in Unix milliseconds, as first_seen and
// last_seen record it.
const nowMillis = `CAST(unixepoch('subsec') * 1000 AS INTEGER)`

// emptyStats is the dir_stats of a folder with nothing below it, as a scan
// writes it.
func emptyStats(c *codec) statsCols {
	a := newAgg()
	return statsCols{byKind: c.byKind(a.byKind), byYear: c.byYear(a.byYear), byFamily: c.byFamily(&a.comp),
		signals: c.signals(a.subtreeSignals), indicators: "[]", inside: "[]"}
}

// RemoveFolder makes the index follow a done rmdir (r3 design D6): it
// deletes the folder id, the missing entries below it, and their
// entry_names rows, and writes the parent's post-step facts. An entry below
// it that is not missing is an error, and a missing one carrying the
// owner's intent is ErrMissingIntent; nothing changes then. The caller then
// runs Refolder.Refold on the parent.
func RemoveFolder(ctx context.Context, tx *sql.Tx, id domain.EntryID, parentFacts PostFacts) error {
	p, err := placeByID(ctx, tx, id)
	switch {
	case err != nil:
		return err
	case p == nil:
		return fmt.Errorf("index: folder %d is not indexed", id)
	case p.kind != string(domain.EntryDirectory):
		return fmt.Errorf("index: %q is not a folder", displayPath(p.path))
	case !p.parent.Valid:
		return fmt.Errorf("index: the root of source %q cannot be removed", p.source)
	}
	if below, err := presentBelow(ctx, tx, p.source, p.path); err != nil || below {
		if err == nil {
			err = fmt.Errorf("index: %q still holds entries", displayPath(p.path))
		}
		return err
	}
	intent, err := IntentBelow(ctx, tx, p.source, p.path)
	if err != nil {
		return err
	}
	if intent {
		return fmt.Errorf("%w: below %q", ErrMissingIntent, displayPath(p.path))
	}
	if err := deleteTree(ctx, tx, p); err != nil {
		return err
	}
	return writeFacts(ctx, tx, domain.EntryID(p.parent.Int64), parentFacts)
}
