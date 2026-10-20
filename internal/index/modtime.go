package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"precious/internal/domain"
)

// ModTime is a done set_mtime (r5 design D13, D15): the file Entry of Source
// now has the post-step facts Facts, its new modification time among them.
type ModTime struct {
	Source domain.SourceID
	Entry  domain.EntryID
	Facts  PostFacts
}

// contentTables are the tables that cache what a read of a file found, each
// keyed by entry_id with the identity the read started from (size,
// mtime_ns, ctime_ns, ino): hashing's digests, archive listings, and media
// headers. A step that changes a file's facts but not its bytes carries
// their matching rows to the new facts, so nothing is read again.
var contentTables = [...]string{"file_content", "archives", "media_meta"}

// ApplyModTime makes the index follow a done set_mtime (r5 design D15),
// inside the outcome's transaction:
//   - the entry's mtime_ns, ctime_ns, dev, and ino become the post-step
//     facts, and its own newest_ns and oldest_ns the new time, NULL when it
//     is unknown (domain.KnownModTime);
//   - its file_content, archives, and media_meta rows that describe the file
//     as stored before the step (size, mtime_ns, ctime_ns, and ino equal)
//     take the new times, when the step left its inode as stored. Any other
//     row is from a read of something else, which those jobs must see;
//   - its folders' newest_ns, oldest_ns, and dir_stats.by_year follow, as a
//     refold would leave them (foldTime); a folder's own times do not
//     change.
//
// The carry is safe only because the step matched the disk's change time to
// the index's before it set the time (D13): a file edited in place since the
// scan is changed, and never reaches this. It refuses an entry that is not a
// file (a folder included), one of another source, and a missing entry.
func ApplyModTime(ctx context.Context, tx *sql.Tx, m ModTime) error {
	e, err := placeByID(ctx, tx, m.Entry)
	switch {
	case err != nil:
		return err
	case e == nil:
		return fmt.Errorf("index: entry %d is not indexed", m.Entry)
	case e.source != m.Source:
		return fmt.Errorf("index: entry %d is on source %q, not %q", m.Entry, e.source, m.Source)
	case e.state == "missing":
		return fmt.Errorf("index: %q is missing", displayPath(e.path))
	case e.kind != string(domain.EntryFile):
		return fmt.Errorf("index: %q is not a file", displayPath(e.path))
	}
	newest, oldest := ownRange(opt{v: m.Facts.MtimeNs, ok: true})
	if _, err := tx.ExecContext(ctx, `UPDATE entries SET mtime_ns = ?, ctime_ns = ?, dev = ?, ino = ?,
		newest_ns = ?, oldest_ns = ? WHERE id = ?`,
		append(factsArgs(m.Facts), newest.arg(), oldest.arg(), int64(e.id))...); err != nil {
		return fmt.Errorf("index: write the time of %q: %w", displayPath(e.path), err)
	}
	if err := foldTime(ctx, tx, e, m.Facts.MtimeNs); err != nil {
		return err
	}
	if !e.in.ok || uint64(e.in.v) != m.Facts.Ino {
		return nil
	}
	var ctime any
	if m.Facts.CtimeNs != 0 {
		ctime = m.Facts.CtimeNs
	}
	for _, table := range contentTables {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET mtime_ns = ?, ctime_ns = ? WHERE entry_id = ? AND size = ?
			AND mtime_ns IS ? AND ctime_ns IS ? AND ino IS ?`,
			m.Facts.MtimeNs, ctime, int64(e.id), e.size, e.mtime.arg(), e.ctime.arg(), e.in.arg()); err != nil {
			return fmt.Errorf("index: carry the content rows of %q: %w", displayPath(e.path), err)
		}
	}
	return nil
}

// timeSpan is a fold's oldest and newest known time; ok is false for none.
type timeSpan struct {
	oldest, newest int64
	ok             bool
}

// ownSpan is a file's own span: its modification time when known.
func ownSpan(mtime opt) timeSpan {
	if !mtime.ok || !domain.KnownModTime(mtime.v) {
		return timeSpan{}
	}
	return timeSpan{oldest: mtime.v, newest: mtime.v, ok: true}
}

// yearOf is the by_year key of a file's modification time.
func yearOf(mtime opt) int {
	if !mtime.ok || !domain.KnownModTime(mtime.v) {
		return unknownYear
	}
	return time.Unix(0, mtime.v).UTC().Year()
}

// Statements of foldTime.
const (
	// foldTimeFolderSQL reads what foldTime needs of a folder.
	foldTimeFolderSQL = `SELECT e.parent_id, e.name, e.state, e.mount_boundary, e.newest_ns, e.oldest_ns,
		d.entry_id IS NOT NULL, d.dirs, d.by_year FROM entries e LEFT JOIN dir_stats d ON d.entry_id = e.id
		WHERE e.id = ?`
	// The newest and the oldest file's time below a folder, read from the
	// end of entries_by_newest (a file's oldest_ns is its newest_ns), and
	// the oldest time of its folders: folder, top, quarantine name.
	foldNewestSQL = `SELECT newest_ns FROM entries INDEXED BY entries_by_newest WHERE parent_id = ?
		AND newest_ns IS NOT NULL AND state <> 'missing' AND kind IN ('file', 'directory') AND (? = 0 OR name <> ?)
		ORDER BY newest_ns DESC LIMIT 1`
	foldOldestFileSQL = `SELECT newest_ns FROM entries INDEXED BY entries_by_newest WHERE parent_id = ?
		AND newest_ns IS NOT NULL AND state <> 'missing' AND kind = 'file' AND (? = 0 OR name <> ?)
		ORDER BY newest_ns LIMIT 1`
	foldOldestDirSQL = `SELECT min(oldest_ns) FROM entries WHERE parent_id = ? AND state <> 'missing'
		AND kind = 'directory' AND (? = 0 OR name <> ?)`
)

// foldTime carries the change of file e's modification time to newNs up its
// folders (r5 H4), leaving each with the newest_ns, oldest_ns, and by_year
// a refold of its stored children would give, without reading or
// classifying those children: a written time changes no name, kind, or
// size, so nothing else of a fold can change. Each folder moves the file
// from its old year's by_year cell to its new one (dropping a cell left
// empty), and its span follows the changed child's: widened by the new
// span, or, when the old one held an end the new one gives up, that end
// read again from its children's stored rows by entries_by_newest (and,
// for the oldest end of a folder with folders inside, a read of those
// folders). As a refold does, a folder that is not present or is a mount
// boundary keeps what it stores, and the root leaves the quarantine out;
// either ends the climb, as does a folder whose span and years no longer
// change.
func foldTime(ctx context.Context, tx *sql.Tx, e *place, newNs int64) error {
	fromYear, toYear := yearOf(e.mtime), yearOf(some(newNs))
	was, now := ownSpan(e.mtime), ownSpan(some(newNs))
	child, parent := e.name, e.parent
	var c *codec
	for parent.Valid && (was != now || fromYear != toYear) {
		var (
			grand          sql.NullInt64
			name           []byte
			state          string
			boundary       bool
			newest, oldest opt
			hasStats       bool
			dirs           sql.NullInt64
			byYear         sql.NullString
			id             = parent.Int64
		)
		if err := tx.QueryRowContext(ctx, foldTimeFolderSQL, id).Scan(&grand, &name, &state, &boundary, &newest,
			&oldest, &hasStats, &dirs, &byYear); err != nil {
			return fmt.Errorf("index: read the folder above %q: %w", displayPath(e.path), err)
		}
		top := !grand.Valid
		if (top && isQuarantineName(child)) || state != "present" || boundary {
			return nil
		}
		if !hasStats {
			return fmt.Errorf("index: folder %d above %q has no dir_stats", id, displayPath(e.path))
		}
		if fromYear != toYear {
			var cells map[string]counts
			if err := decodeCells(byYear.String, &cells); err != nil {
				return fmt.Errorf("index: read the by_year of folder %d: %w", id, err)
			}
			years := make(map[int]counts, len(cells)+1)
			for k, n := range cells {
				y, err := strconv.Atoi(k)
				if err != nil {
					return fmt.Errorf("index: by_year key %q of folder %d: %w", k, id, err)
				}
				years[y] = n
			}
			cell := years[fromYear]
			cell.add(counts{files: -1, bytes: -e.size})
			if cell.files <= 0 {
				delete(years, fromYear)
			} else {
				years[fromYear] = cell
			}
			cell = years[toYear]
			cell.add(counts{files: 1, bytes: e.size})
			years[toYear] = cell
			if c == nil {
				c = newCodec()
			}
			if _, err := tx.ExecContext(ctx, `UPDATE dir_stats SET by_year = ? WHERE entry_id = ?`, c.byYear(years),
				id); err != nil {
				return fmt.Errorf("index: write the by_year of folder %d: %w", id, err)
			}
		}
		before := timeSpan{}
		if newest.ok && oldest.ok {
			before = timeSpan{oldest: oldest.v, newest: newest.v, ok: true}
		}
		after, err := refoldSpan(ctx, tx, id, top, dirs.Int64 > 0, before, was, now)
		if err != nil {
			return err
		}
		if after != before {
			n, o := opt{}, opt{}
			if after.ok {
				n, o = some(after.newest), some(after.oldest)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE entries SET newest_ns = ?, oldest_ns = ? WHERE id = ?`,
				n.arg(), o.arg(), id); err != nil {
				return fmt.Errorf("index: write the times of folder %d: %w", id, err)
			}
		}
		was, now = before, after
		child, parent = name, grand
	}
	return nil
}

// refoldSpan is the span of folder id once one child's span went from was
// to now (its row already holding now), the folder's having been before.
// top says the folder is a source's root, whose fold leaves the quarantine
// out; hasDirs that folders are inside it.
func refoldSpan(ctx context.Context, tx *sql.Tx, id int64, top, hasDirs bool, before, was, now timeSpan) (timeSpan, error) {
	if !before.ok {
		return now, nil // no child had a time: now is the only one
	}
	after := before
	quarantine := []byte(QuarantineName)
	switch {
	case now.ok && now.newest >= before.newest:
		after.newest = now.newest
	case was.ok && was.newest == before.newest:
		var v sql.NullInt64
		err := tx.QueryRowContext(ctx, foldNewestSQL, id, top, quarantine).Scan(&v)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return timeSpan{}, fmt.Errorf("index: read the newest time in folder %d: %w", id, err)
		}
		if !v.Valid {
			return timeSpan{}, nil // no child has a time any more
		}
		after.newest = v.Int64
	}
	switch {
	case now.ok && now.oldest <= before.oldest:
		after.oldest = now.oldest
	case was.ok && was.oldest == before.oldest:
		var file, dir sql.NullInt64
		err := tx.QueryRowContext(ctx, foldOldestFileSQL, id, top, quarantine).Scan(&file)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return timeSpan{}, fmt.Errorf("index: read the oldest time in folder %d: %w", id, err)
		}
		if hasDirs {
			if err := tx.QueryRowContext(ctx, foldOldestDirSQL, id, top, quarantine).Scan(&dir); err != nil {
				return timeSpan{}, fmt.Errorf("index: read the oldest time in folder %d: %w", id, err)
			}
		}
		switch {
		case file.Valid && dir.Valid:
			after.oldest = min(file.Int64, dir.Int64)
		case file.Valid:
			after.oldest = file.Int64
		case dir.Valid:
			after.oldest = dir.Int64
		default:
			return timeSpan{}, nil
		}
	}
	return after, nil
}
