package content

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"precious/internal/domain"
	"precious/internal/store"
)

// PreferredCopy is the one other copy D9 names for a file or member, with
// More, its content's physical copies (D1, the named one and the ref's own
// included) minus 2, at least 0. Copy.HardLink is always false, and the
// decision fields are not read (empty).
type PreferredCopy struct {
	Copy
	More int
}

// PreferredCopies returns D9's copy for each ref of want (an entry file, or
// a member "m<id>") whose place is this_source or other_source; refs with
// another place, without a digest, unknown, or with no other copy are
// absent.
//
//   - other_source: a copy on another source than the ref's (a member's is
//     its archive entry's), copies on online sources first;
//   - this_source: a copy on the ref's own source, loose files before
//     members.
//
// Ties go to the lowest reference: entries before members, each by ID, as
// Copies lists them. A copy is a present file outside the quarantine, or a
// file member of a complete archive whose present entry is outside it, as
// in Copies, but the ref's own physical copy is never named: not a name of
// its hard-link set, not its tar link group, and, for a member, not a
// member of another name of its archive's hard-link set. Physical copies
// follow the relate snapshot (design D1, D3): a hard-link set on a source
// with stable identity is one copy, a tar hard link and its target are one,
// and the members at one path of the names of one archive's hard-link set
// are one. One statement serves every ref.
func PreferredCopies(ctx context.Context, q store.Queryer, want map[domain.Ref]domain.Place) (map[domain.Ref]PreferredCopy, error) {
	out := map[domain.Ref]PreferredCopy{}
	type key struct {
		member bool
		id     int64
	}
	refsOf := map[key][]domain.Ref{}
	arg := make([]byte, 0, 16*len(want)+2)
	arg = append(arg, '[')
	for ref, place := range want {
		other := 0
		switch place {
		case domain.PlaceOtherSource:
			other = 1
		case domain.PlaceThisSource:
		default:
			continue
		}
		k := key{member: ref.IsMember(), id: int64(ref.Entry)}
		if k.member {
			k.id = int64(ref.Member)
		}
		if _, ok := refsOf[k]; !ok {
			if len(arg) > 1 {
				arg = append(arg, ',')
			}
			m := 0
			if k.member {
				m = 1
			}
			arg = append(arg, '[')
			arg = strconv.AppendInt(arg, int64(m), 10)
			arg = append(arg, ',')
			arg = strconv.AppendInt(arg, k.id, 10)
			arg = append(arg, ',')
			arg = strconv.AppendInt(arg, int64(other), 10)
			arg = append(arg, ']')
		}
		refsOf[k] = append(refsOf[k], ref)
	}
	if len(refsOf) == 0 {
		return out, nil
	}
	arg = append(arg, ']')
	rows, err := q.QueryContext(ctx, preferredSQL, string(arg))
	if err != nil {
		return nil, fmt.Errorf("content: preferred copies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			rmember, member bool
			rid, id         int64
			src             string
			path, mpath     []byte
			archiveID       sql.NullInt64
			zip, offline    bool
			n               int
		)
		if err := rows.Scan(&rmember, &rid, &member, &id, &src, &path, &mpath, &archiveID, &zip, &offline, &n); err != nil {
			return nil, fmt.Errorf("content: preferred copies: %w", err)
		}
		c := Copy{SourceID: domain.SourceID(src), PathB64: path, Offline: offline, Path: domain.DisplayName(path)}
		if member {
			a := domain.EntryID(archiveID.Int64)
			c.Ref = domain.Ref{Entry: a, Member: domain.MemberID(id)}
			c.ArchiveID = &a
			c.PathB64 = append(append(path, '!'), mpath...)
			c.Path += "!" + domain.MemberDisplayName(mpath, zip)
		} else {
			c.Ref = domain.Ref{Entry: domain.EntryID(id)}
		}
		p := PreferredCopy{Copy: c, More: max(n-2, 0)}
		for _, ref := range refsOf[key{member: rmember, id: rid}] {
			out[ref] = p
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: preferred copies: %w", err)
	}
	return out, nil
}

// physKey is the physical identity of the file entry aliased x on the
// source aliased s (the snapshot's physOf, with the source): its hard-link
// set when it may have other names, else the entry itself.
func physKey(x, s string) string {
	return `CASE WHEN ` + x + `.nlink > 1 AND ` + x + `.dev IS NOT NULL AND ` + x + `.ino IS NOT NULL
		AND coalesce(json_extract(` + s + `.capabilities, '$.stable_identity'), 0)
		THEN 'h' || ` + x + `.source_id || '/' || ` + x + `.dev || '/' || ` + x + `.ino ELSE 'e' || ` + x + `.id END`
}

// memberKey is the physical identity of the file member aliased m of the
// archive entry aliased ae on the source aliased s: its archive's identity
// and the path of its tar link target (its own path when it is no link),
// unique in an archive.
func memberKey(m, ae, s string) string {
	return physKey(ae, s) + ` || '!' || hex(coalesce((SELECT t.path FROM archive_members t WHERE t.id = ` + m +
		`.link_member), ` + m + `.path))`
}

// preferredSQL reads ?1, a JSON array of [member, id, other] (other: 1 for
// other_source, 0 for this_source), and returns, per ref, its preferred
// copy and the content's physical copies, the ref's own counted once. self
// is each ref's content, source, physical identity, and, for a member, its
// archive's identity and entry; copy is every copy of those contents; phys
// their number of distinct physical identities.
var preferredSQL = `WITH want AS (
		SELECT json_extract(value, '$[0]') AS member, json_extract(value, '$[1]') AS id,
			json_extract(value, '$[2]') AS other
		FROM json_each(?1)
	),
	self AS (
		SELECT w.member, w.id, w.other, f.content_id AS content, e.source_id AS src, ` + physKey("e", "s") + ` AS pkey,
			NULL AS akey, NULL AS arc
		FROM want w CROSS JOIN entries e ON e.id = w.id JOIN sources s ON s.id = e.source_id
			JOIN file_content f ON f.entry_id = e.id
		WHERE w.member = 0 AND f.content_id IS NOT NULL
		UNION ALL
		SELECT w.member, w.id, w.other, m.content_id, ae.source_id, ` + memberKey("m", "ae", "s") + `,
			` + physKey("ae", "s") + `, ae.id
		FROM want w CROSS JOIN archive_members m ON m.id = w.id JOIN entries ae ON ae.id = m.archive_id
			JOIN sources s ON s.id = ae.source_id
		WHERE w.member = 1 AND m.kind = 'file' AND m.content_id IS NOT NULL
	),
	contents AS (SELECT DISTINCT content FROM self),
	copy AS (
		SELECT f.content_id AS content, 0 AS member, e.id AS id, e.source_id AS src, s.state <> 'online' AS offline,
			e.path AS path, NULL AS mpath, NULL AS arc, 0 AS zip, ` + physKey("e", "s") + ` AS pkey, NULL AS akey
		FROM contents c CROSS JOIN file_content f ON f.content_id = c.content JOIN entries e ON e.id = f.entry_id
			JOIN sources s ON s.id = e.source_id
		WHERE e.state = 'present' AND ` + notQuarantinedE + `
		UNION ALL
		SELECT m.content_id, 1, m.id, e.source_id, s.state <> 'online', e.path, m.path, e.id, a.format = 'zip',
			` + memberKey("m", "e", "s") + `, ` + physKey("e", "s") + `
		FROM contents c CROSS JOIN archive_members m ON m.content_id = c.content
			JOIN archives a ON a.entry_id = m.archive_id JOIN entries e ON e.id = a.entry_id
			JOIN sources s ON s.id = e.source_id
		WHERE m.kind = 'file' AND a.state = 'complete' AND e.state = 'present' AND ` + notQuarantinedE + `
	),
	phys AS (SELECT content, count(DISTINCT pkey) AS n FROM copy GROUP BY content),
	pick AS (
		SELECT self.member AS rmember, self.id AS rid, self.pkey AS spkey, copy.*,
			row_number() OVER (PARTITION BY self.member, self.id
				ORDER BY CASE WHEN self.other THEN copy.offline ELSE 0 END, copy.member, copy.id) AS rn
		FROM self JOIN copy ON copy.content = self.content
		WHERE copy.pkey <> self.pkey AND NOT (copy.member AND copy.akey IS self.akey AND copy.arc <> self.arc)
			AND CASE WHEN self.other THEN copy.src <> self.src ELSE copy.src = self.src END
	)
	SELECT p.rmember, p.rid, p.member, p.id, p.src, p.path, p.mpath, p.arc, p.zip, p.offline,
		x.n + NOT EXISTS (SELECT 1 FROM copy y WHERE y.content = p.content AND y.pkey = p.spkey)
	FROM pick p JOIN phys x ON x.content = p.content WHERE p.rn = 1`
