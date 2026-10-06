package api

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"precious/internal/clock"
	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/relations"
	"precious/internal/search"
)

// The R2 JSON shapes (design Interfaces): CopyJSON, RelationJSON,
// CoverageJSON, and the detail's content and archive.

type copyJSON struct {
	Ref         string          `json:"ref"`
	SourceID    domain.SourceID `json:"source_id"`
	Path        string          `json:"path"`
	PathB64     []byte          `json:"path_b64"`
	ArchiveID   *string         `json:"archive_id"`
	HardLink    bool            `json:"hard_link"`
	Offline     bool            `json:"offline"`
	EffDecision domain.Decision `json:"eff_decision"`
}

func copiesJSON(cs []content.Copy) []copyJSON {
	out := make([]copyJSON, len(cs))
	for i, c := range cs {
		out[i] = copyJSON{Ref: c.Ref.String(), SourceID: c.SourceID, Path: c.Path, PathB64: c.PathB64,
			HardLink: c.HardLink, Offline: c.Offline, EffDecision: c.EffDecision}
		if c.ArchiveID != nil {
			s := c.ArchiveID.String()
			out[i].ArchiveID = &s
		}
	}
	return out
}

type relationJSON struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Self           string   `json:"self"`
	Other          entryRow `json:"other"`
	MatchedBytes   int64    `json:"matched_bytes"`
	RedundantBytes int64    `json:"redundant_bytes"`
	OnlyHere       amount   `json:"only_here"`
	OnlyThere      amount   `json:"only_there"`
}

type coverageJSON struct {
	Candidate  amount `json:"candidate"`
	Checked    amount `json:"checked"`
	Unchecked  amount `json:"unchecked"`
	Unreadable amount `json:"unreadable"`
}

// readCoverage reads the coverage of src ("" for every source together).
func readCoverage(ctx context.Context, tx *sql.Tx, src domain.SourceID) (coverageJSON, error) {
	c, err := content.CoverageOf(ctx, tx, src)
	if err != nil {
		return coverageJSON{}, err
	}
	return coverageJSON{
		Candidate:  amount{Files: c.CandidateFiles, Bytes: c.CandidateBytes},
		Checked:    amount{Files: c.CheckedFiles, Bytes: c.CheckedBytes},
		Unchecked:  amount{Files: c.UncheckedFiles, Bytes: c.UncheckedBytes},
		Unreadable: amount{Files: c.UnreadableFiles, Bytes: c.UnreadableBytes},
	}, nil
}

// contentJSON is the detail's content of a file or file member.
type contentJSON struct {
	State       domain.ContentState `json:"state"`
	SHA256      *string             `json:"sha256"`
	CheckedAt   *time.Time          `json:"checked_at"`
	Copies      []copyJSON          `json:"copies"`
	CopiesCount int                 `json:"copies_count"`
}

// detailCopies is how many other copies the detail lists.
const detailCopies = 20

// readContent reads the content of the file or file member ref: its state,
// its digest once hashed, when hashing last read it, and up to 20 of its
// other copies with their count. It is nil for anything without a content
// state (a folder, an empty file, a file hashing has not planned yet).
func readContent(ctx context.Context, tx *sql.Tx, ref domain.Ref) (*contentJSON, error) {
	var (
		state     sql.NullString
		sum       []byte
		checkedAt sql.NullInt64
		err       error
	)
	if ref.IsMember() {
		err = tx.QueryRowContext(ctx, `SELECT m.state, c.sha256, NULL FROM archive_members m
			LEFT JOIN contents c ON c.id = m.content_id WHERE m.id = ? AND m.kind = 'file'`, int64(ref.Member)).
			Scan(&state, &sum, &checkedAt)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT fc.state, c.sha256, fc.checked_at FROM file_content fc
			LEFT JOIN contents c ON c.id = fc.content_id WHERE fc.entry_id = ?`, int64(ref.Entry)).
			Scan(&state, &sum, &checkedAt)
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !state.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("api: content of %s: %w", ref, err)
	}
	c := &contentJSON{State: domain.ContentState(state.String)}
	if sum != nil {
		s := hex.EncodeToString(sum)
		c.SHA256 = &s
	}
	if checkedAt.Valid {
		t := clock.FromMillis(checkedAt.Int64)
		c.CheckedAt = &t
	}
	copies, n, _, err := content.Copies(ctx, tx, ref, "", detailCopies)
	if err != nil {
		return nil, err
	}
	c.Copies, c.CopiesCount = copiesJSON(copies), n
	return c, nil
}

// archiveJSON is the detail's archive of an archive file Precious opened.
type archiveJSON struct {
	Format        domain.ArchiveFormat `json:"format"`
	State         domain.ArchiveState  `json:"state"`
	Detail        *string              `json:"detail"`
	Members       int64                `json:"members"`
	UnpackedBytes int64                `json:"unpacked_bytes"`
}

// readArchive reads the archives row of entry id; nil when it has none or
// is still being listed.
func readArchive(ctx context.Context, tx *sql.Tx, id domain.EntryID) (*archiveJSON, error) {
	var (
		a      archiveJSON
		detail sql.NullString
	)
	err := tx.QueryRowContext(ctx, `SELECT format, state, detail, members, unpacked_bytes FROM archives
		WHERE entry_id = ? AND state <> 'listing'`, int64(id)).Scan(&a.Format, &a.State, &detail, &a.Members, &a.UnpackedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("api: archive %s: %w", id, err)
	}
	if detail.Valid {
		a.Detail = &detail.String
	}
	return &a, nil
}

// rowKey is the key of a ref in a rowSet: an entry by its ID, a member by
// its member ID alone.
func rowKey(r domain.Ref) domain.Ref {
	if r.IsMember() {
		return domain.Ref{Member: r.Member}
	}
	return domain.Ref{Entry: r.Entry}
}

// rowSet holds the EntryRows of refs, by rowKey.
type rowSet map[domain.Ref]*search.Row

// get returns the row of ref; ok is false when ref names nothing.
func (s rowSet) get(ref domain.Ref) (*search.Row, bool) {
	r, ok := s[rowKey(ref)]
	return r, ok
}

// loadRows reads the EntryRows of refs, entries with their own tags and
// member folders with their figures. A ref naming nothing is left out.
func (h *handler) loadRows(ctx context.Context, tx *sql.Tx, refs []domain.Ref) (rowSet, error) {
	set := rowSet{}
	var entries, members []any
	for _, r := range refs {
		k := rowKey(r)
		if _, ok := set[k]; ok || (r.Entry == 0 && r.Member == 0) {
			continue
		}
		set[k] = nil
		if r.IsMember() {
			members = append(members, int64(r.Member))
		} else {
			entries = append(entries, int64(r.Entry))
		}
	}
	const chunk = 500
	for len(entries) > 0 {
		n := min(len(entries), chunk)
		rows, err := appendRows(ctx, tx, nil, `SELECT `+search.Columns+` FROM `+search.From+
			` WHERE e.id IN (`+marks(n)+`)`, entries[:n])
		if err != nil {
			return nil, fmt.Errorf("api: rows: %w", err)
		}
		if err := search.LoadTags(ctx, tx, rows); err != nil {
			return nil, err
		}
		for i := range rows {
			set[domain.Ref{Entry: rows[i].ID}] = &rows[i]
		}
		entries = entries[n:]
	}
	for len(members) > 0 {
		n := min(len(members), chunk)
		ms, err := memberRows(ctx, tx, h.pol, `m.id IN (`+marks(n)+`)`, members[:n]...)
		if err != nil {
			return nil, err
		}
		for i := range ms {
			if ms[i].row.Kind == domain.EntryDirectory {
				if _, err := fillFolder(ctx, tx, h.pol, &ms[i]); err != nil {
					return nil, err
				}
			}
			set[domain.Ref{Member: ms[i].row.Member}] = &ms[i].row
		}
		members = members[n:]
	}
	for k, r := range set {
		if r == nil {
			delete(set, k)
		}
	}
	return set, nil
}

// marks is n comma-separated placeholders.
func marks(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// sameSide reports whether a relation side is ref: a member folder by its
// member, an entry (a folder or a whole archive) by itself.
func sameSide(side, ref domain.Ref) bool {
	if ref.IsMember() {
		return side.Member == ref.Member
	}
	return !side.IsMember() && side.Entry == ref.Entry
}

// relationsJSON renders rels as seen from the side self returns ("a" or
// "b") for each, with the other side's row. A relation whose other side no
// longer exists is left out.
func (h *handler) relationsJSON(ctx context.Context, tx *sql.Tx, rels []relations.Relation,
	self func(relations.Relation) string) ([]relationJSON, error) {
	refs := make([]domain.Ref, 0, len(rels))
	for _, r := range rels {
		if self(r) == "a" {
			refs = append(refs, r.B)
		} else {
			refs = append(refs, r.A)
		}
	}
	rows, err := h.loadRows(ctx, tx, refs)
	if err != nil {
		return nil, err
	}
	out := make([]relationJSON, 0, len(rels))
	for i, r := range rels {
		other, ok := rows.get(refs[i])
		if !ok {
			continue
		}
		j := relationJSON{ID: strconv.FormatInt(r.ID, 10), Kind: r.Kind, Self: self(r), Other: rowJSON(other),
			MatchedBytes: r.MatchedBytes, RedundantBytes: r.RedundantBytes,
			OnlyHere:  amount{Files: r.AOnlyFiles, Bytes: r.AOnlyBytes},
			OnlyThere: amount{Files: r.BOnlyFiles, Bytes: r.BOnlyBytes}}
		if j.Self == "b" {
			j.OnlyHere, j.OnlyThere = j.OnlyThere, j.OnlyHere
		}
		out = append(out, j)
	}
	return out, nil
}

// relationsByID reads relations of the visible generation by ID.
func relationsByID(ctx context.Context, tx *sql.Tx, ids []int64) (map[int64]relations.Relation, error) {
	out := make(map[int64]relations.Relation, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, a_entry, ifnull(a_member, 0), b_entry, ifnull(b_member, 0),
			matched_bytes, redundant_bytes, a_only_files, a_only_bytes, b_only_files, b_only_bytes
		FROM relations WHERE id IN (`+marks(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: relations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r      relations.Relation
			ae, am int64
			be, bm int64
		)
		if err := rows.Scan(&r.ID, &r.Kind, &ae, &am, &be, &bm, &r.MatchedBytes, &r.RedundantBytes,
			&r.AOnlyFiles, &r.AOnlyBytes, &r.BOnlyFiles, &r.BOnlyBytes); err != nil {
			return nil, fmt.Errorf("api: relations: %w", err)
		}
		r.A = domain.Ref{Entry: domain.EntryID(ae), Member: domain.MemberID(am)}
		r.B = domain.Ref{Entry: domain.EntryID(be), Member: domain.MemberID(bm)}
		out[r.ID] = r
	}
	return out, rows.Err()
}

// detailRelations is how many relations the detail lists.
const detailRelations = 20

// readRelations lists up to 20 relations of ref, seen from ref.
func (h *handler) readRelations(ctx context.Context, tx *sql.Tx, ref domain.Ref) ([]relationJSON, error) {
	rels, err := relations.RelationsOf(ctx, tx, ref, detailRelations)
	if err != nil {
		return nil, err
	}
	return h.relationsJSON(ctx, tx, rels, func(r relations.Relation) string {
		if sameSide(r.A, ref) {
			return "a"
		}
		return "b"
	})
}

// copiesBody is the /copies response.
type copiesBody struct {
	Items      []copyJSON `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	Count      int        `json:"count"`
}

const (
	copiesDefaultLimit = 100
	copiesMaxLimit     = 1000
)

// copies serves GET /api/entries/{ref}/copies?cursor=&limit=: a page of
// the other copies of a file or file member (content.Copies), entries
// first, with their count. Anything without a digest has none.
func (h *handler) copies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref, err := pathRef(r)
	if err == nil {
		err = checkParams(q, "cursor", "limit")
	}
	var limit int
	if err == nil {
		limit, err = parseLimit(q.Get("limit"))
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	if limit == 0 {
		limit = copiesDefaultLimit
	}
	limit = min(limit, copiesMaxLimit)
	h.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		items, n, next, err := content.Copies(ctx, tx, ref, q.Get("cursor"), limit)
		if err != nil {
			return nil, err
		}
		body := copiesBody{Items: copiesJSON(items), Count: n}
		if next != "" {
			body.NextCursor = &next
		}
		return body, nil
	})
}
