package content

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"precious/internal/domain"
	"precious/internal/store"
)

// Copy is one other copy of a file's or member's content (design D8,
// Interfaces): a present file on any source, offline ones included, or a
// file member of a complete archive. Path is the copy's raw source-relative
// path in display form, a member's as its archive's path, '!', and its
// path inside the archive (domain.MemberDisplayName); PathB64 holds the raw bytes. ArchiveID is set
// for a member, HardLink for another name of the same physical file, and a
// member's EffDecision is its archive's.
type Copy struct {
	Ref         domain.Ref
	SourceID    domain.SourceID
	Path        string
	PathB64     []byte
	ArchiveID   *domain.EntryID
	HardLink    bool
	Offline     bool
	EffDecision domain.Decision
}

// Copies returns a page of at most limit other copies of ref's content,
// entries first, then members, each by ID, after cursor (a value it
// returned as the next cursor, "" for the first page), with the total
// count and the next cursor ("" after the last page). A ref without a
// digest has no copies. An unknown ref is not_found; a malformed cursor is
// invalid_request.
func Copies(ctx context.Context, q store.Queryer, ref domain.Ref, cursor string, limit int) ([]Copy, int, string, error) {
	if limit <= 0 {
		return nil, 0, "", domain.Errorf(domain.CodeInvalidRequest, "limit must be positive")
	}
	afterMember, afterID, err := parseCopyCursor(cursor)
	if err != nil {
		return nil, 0, "", err
	}
	var (
		content        sql.NullInt64
		dev, ino, link sql.NullInt64
		stable         bool
		vol            string
	)
	if ref.IsMember() {
		err = q.QueryRowContext(ctx, `SELECT content_id, coalesce(link_member, id) FROM archive_members WHERE id = ?`,
			int64(ref.Member)).Scan(&content, &link)
	} else {
		err = q.QueryRowContext(ctx, `SELECT f.content_id, e.dev, e.ino,
				e.nlink > 1 AND coalesce(json_extract(s.capabilities, '$.stable_identity'), 0), s.volume_id
			FROM entries e JOIN sources s ON s.id = e.source_id LEFT JOIN file_content f ON f.entry_id = e.id
			WHERE e.id = ?`, int64(ref.Entry)).Scan(&content, &dev, &ino, &stable, &vol)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, "", domain.Errorf(domain.CodeNotFound, "%s not found", ref)
	}
	if err != nil {
		return nil, 0, "", fmt.Errorf("content: read %s: %w", ref, err)
	}
	if !content.Valid {
		return []Copy{}, 0, "", nil
	}
	const entries = `SELECT 0 AS member, e.id AS id, e.source_id, e.path, NULL, NULL, 0, s.state <> 'online', e.eff_decision,
			e.nlink > 1 AND coalesce(json_extract(s.capabilities, '$.stable_identity'), 0) AND s.volume_id = ?2
				AND e.dev IS ?3 AND e.ino IS ?4 AND ?5
		FROM file_content f JOIN entries e ON e.id = f.entry_id JOIN sources s ON s.id = e.source_id
		WHERE f.content_id = ?1 AND e.state = 'present' AND e.id <> ?6`
	const members = `SELECT 1, m.id, e.source_id, e.path, m.path, e.id, a.format = 'zip', s.state <> 'online', e.eff_decision,
			coalesce(m.link_member, m.id) = ?7
		FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = a.entry_id
			JOIN sources s ON s.id = e.source_id
		WHERE m.content_id = ?1 AND m.kind = 'file' AND a.state = 'complete' AND e.state = 'present' AND m.id <> ?8`
	self := []any{content.Int64, vol, dev, ino, stable, int64(ref.Entry), link.Int64, int64(ref.Member)}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM (`+entries+` UNION ALL `+members+`)`, self...).
		Scan(&total); err != nil {
		return nil, 0, "", fmt.Errorf("content: count the copies of %s: %w", ref, err)
	}
	rows, err := q.QueryContext(ctx, `SELECT * FROM (`+entries+` UNION ALL `+members+`)
		WHERE (member, id) > (?9, ?10) ORDER BY member, id LIMIT ?11`, append(self, afterMember, afterID, limit+1)...)
	if err != nil {
		return nil, 0, "", fmt.Errorf("content: list the copies of %s: %w", ref, err)
	}
	defer rows.Close()
	out := []Copy{}
	next := ""
	for rows.Next() {
		var (
			member       bool
			id           int64
			src, eff     string
			path, mpath  []byte
			archiveID    sql.NullInt64
			offline, hdl bool
			zip          bool
		)
		if err := rows.Scan(&member, &id, &src, &path, &mpath, &archiveID, &zip, &offline, &eff, &hdl); err != nil {
			return nil, 0, "", err
		}
		if len(out) == limit {
			last := out[len(out)-1]
			next = copyCursor(last.Ref)
			break
		}
		c := Copy{SourceID: domain.SourceID(src), PathB64: path, Offline: offline, HardLink: hdl,
			EffDecision: domain.Decision(eff), Path: domain.DisplayName(path)}
		if member {
			c.Ref = domain.Ref{Entry: domain.EntryID(archiveID.Int64), Member: domain.MemberID(id)}
			a := domain.EntryID(archiveID.Int64)
			c.ArchiveID = &a
			c.PathB64 = append(append(path, '!'), mpath...)
			c.Path += "!" + domain.MemberDisplayName(mpath, zip)
		} else {
			c.Ref = domain.Ref{Entry: domain.EntryID(id)}
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, "", err
	}
	return out, total, next, nil
}

// copyCursor is the opaque cursor after ref: "e<id>" or "m<id>".
func copyCursor(ref domain.Ref) string {
	if ref.IsMember() {
		return "m" + strconv.FormatInt(int64(ref.Member), 10)
	}
	return "e" + strconv.FormatInt(int64(ref.Entry), 10)
}

func parseCopyCursor(s string) (member, id int64, err error) {
	if s == "" {
		return 0, 0, nil
	}
	n, perr := strconv.ParseInt(s[1:], 10, 64)
	switch {
	case perr != nil || n <= 0:
	case s[0] == 'e':
		return 0, n, nil
	case s[0] == 'm':
		return 1, n, nil
	}
	return 0, 0, domain.Errorf(domain.CodeInvalidRequest, "invalid cursor %q", s)
}
