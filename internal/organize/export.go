package organize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"precious/internal/domain"
	"precious/internal/web/api"
	"precious/internal/web/apierr"
)

// keptPage is how many kept entries one page of the kept read lists.
const keptPage = 100

// keptWhere selects the entries of a source at or below a path that have
// the owner's own keep, which block a cleanup item (r4 D3): source, path,
// lo, hi. An item's subtree is the range [path/, path0) and the item.
const keptWhere = `source_id = ?1 AND decision = 'keep' AND (path = ?2 OR (path >= ?3 AND path < ?4))`

// keptBounds returns the arguments of keptWhere for path of src; the empty
// path (a source's top folder) holds every path.
func keptBounds(src domain.SourceID, path []byte) []any {
	if len(path) == 0 {
		return []any{string(src), []byte{}, []byte{}, []byte{0xff, 0xff, 0xff, 0xff}}
	}
	lo := append(bytes.Clone(path), '/')
	hi := append(bytes.Clone(path), '0')
	return []any{string(src), path, lo, hi}
}

// countKept counts the entries of src at or below path that the owner
// keeps.
func countKept(ctx context.Context, tx *sql.Tx, src domain.SourceID, path []byte) (int64, error) {
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE `+keptWhere, keptBounds(src, path)...).
		Scan(&n); err != nil {
		return 0, fmt.Errorf("organize: count kept entries: %w", err)
	}
	return n, nil
}

// keptResponse answers the kept read.
type keptResponse struct {
	Count      int64           `json:"count"`
	Items      []*api.EntryRow `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

// kept lists, by path, the entries at or below an item's entry (its
// from_path) that the owner keeps: those that block a cleanup item. The
// cursor is the last path given, in unpadded base64url.
func (s *Service) kept(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		q := r.URL.Query()
		if err := checkParams(q, []string{"cursor"}, nil); err != nil {
			return nil, err
		}
		action, err := pathID(r)
		if err != nil {
			return nil, err
		}
		itemID, err := parseID("item", r.PathValue("item"))
		if err != nil {
			return nil, err
		}
		var after []byte
		if c := q.Get("cursor"); c != "" {
			if after, err = base64.RawURLEncoding.DecodeString(c); err != nil {
				return nil, domain.Errorf(domain.CodeInvalidRequest, "invalid cursor %q", c)
			}
		}
		var (
			src  domain.SourceID
			from sql.Null[[]byte]
		)
		err = tx.QueryRowContext(ctx, `SELECT a.source_id, i.from_path FROM action_items i JOIN actions a ON a.id = i.action_id
			WHERE i.id = ? AND i.action_id = ?`, itemID, action).Scan(&src, &from)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("item", r.PathValue("item"))
		}
		if err != nil {
			return nil, err
		}
		res := keptResponse{Items: []*api.EntryRow{}}
		if !from.Valid {
			return res, nil
		}
		bounds := keptBounds(src, from.V)
		if res.Count, err = countKept(ctx, tx, src, from.V); err != nil {
			return nil, err
		}
		query := `SELECT id, path FROM entries WHERE ` + keptWhere
		args := bounds
		if after != nil {
			query += ` AND path > ?5`
			args = append(args, after)
		}
		rows, err := tx.QueryContext(ctx, query+` ORDER BY path LIMIT `+strconv.Itoa(keptPage+1), args...)
		if err != nil {
			return nil, fmt.Errorf("organize: list kept entries: %w", err)
		}
		var (
			ids  []domain.EntryID
			last [][]byte
		)
		for rows.Next() {
			var (
				id   int64
				path []byte
			)
			if err := rows.Scan(&id, &path); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, domain.EntryID(id))
			last = append(last, path)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(ids) > keptPage {
			ids = ids[:keptPage]
			c := base64.RawURLEncoding.EncodeToString(last[keptPage-1])
			res.NextCursor = &c
		}
		entries, err := api.EntryRows(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if e, ok := entries[id]; ok {
				res.Items = append(res.Items, e)
			}
		}
		return res, nil
	})
}

// exportHeader is the CSV header of an action's export (r4 D16).
const exportHeader = "path,size,operation,state,reason"

// operation names an item's op in the export: a cleanup's rename
// quarantines, a restore's restores, a rename action's renames, and every
// other rename, a date organize's included, moves (r5 Interfaces).
func operation(kind, op string) string {
	switch op {
	case opRename:
		switch kind {
		case "cleanup":
			return "quarantine"
		case "restore":
			return "restore"
		case "rename":
			return "rename"
		}
		return "move"
	case opMkdir:
		return "create_folder"
	case opRmdir:
		return "remove_folder"
	case opRecord:
		return "write_record"
	case opUnlink:
		return "remove_record"
	}
	return op // purge, verify, set_mtime
}

// csvCell quotes a cell, doubling its quotes, after a leading ' on a cell a
// spreadsheet would read as a formula (r4 D16).
func csvCell(b *strings.Builder, s string) {
	b.WriteByte('"')
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		b.WriteByte('\'')
	}
	b.WriteString(strings.ReplaceAll(s, `"`, `""`))
	b.WriteByte('"')
}

// export answers every item of an action as CSV, in seq order (r4 D16):
// its path (the from path, or the to path of a mkdir or a record, in
// display form), its bytes, its operation, its state, and its reason.
func (s *Service) export(w http.ResponseWriter, r *http.Request) {
	var (
		out  strings.Builder
		kind string
		id   int64
	)
	err := s.st.Read(r.Context(), func(tx *sql.Tx) error {
		ctx := r.Context()
		out.Reset()
		if err := checkParams(r.URL.Query(), nil, nil); err != nil {
			return err
		}
		var err error
		if id, err = pathID(r); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT kind FROM actions WHERE id = ?`, id).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("action", r.PathValue("id"))
		}
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT op, CASE WHEN op IN ('mkdir', 'record') THEN to_path ELSE from_path END,
			bytes, state, coalesce(reason, '') FROM action_items WHERE action_id = ? ORDER BY seq`, id)
		if err != nil {
			return fmt.Errorf("organize: export items: %w", err)
		}
		defer rows.Close()
		out.WriteString(exportHeader + "\r\n")
		for rows.Next() {
			var (
				op, state, reason string
				path              sql.Null[[]byte]
				size              int64
			)
			if err := rows.Scan(&op, &path, &size, &state, &reason); err != nil {
				return err
			}
			p := ""
			if path.Valid {
				p = domain.DisplayName(path.V)
			}
			csvCell(&out, p)
			out.WriteByte(',')
			csvCell(&out, strconv.FormatInt(size, 10))
			out.WriteByte(',')
			csvCell(&out, operation(kind, op))
			out.WriteByte(',')
			csvCell(&out, state)
			out.WriteByte(',')
			csvCell(&out, reason)
			out.WriteString("\r\n")
		}
		return rows.Err()
	})
	if err != nil {
		var de *domain.Error
		if !errors.As(err, &de) {
			s.log.Error("organize: internal error", "err", err)
		}
		apierr.FromError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="precious-%s-%d.csv"`, kind, id))
	_, _ = w.Write([]byte(out.String()))
}
