package index

import (
	"context"
	"database/sql"
	"fmt"

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
//     row is from a read of something else, which those jobs must see.
//
// The carry is safe only because the step matched the disk's change time to
// the index's before it set the time (D13): a file edited in place since the
// scan is changed, and never reaches this. It refuses an entry that is not a
// file (a folder included), one of another source, and a missing entry. The
// caller then runs Refolder.Refold on the entry, so its folders' newest,
// oldest, and by-year figures follow; a folder's own times do not change.
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
