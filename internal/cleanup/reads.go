package cleanup

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/web/api"
	"precious/internal/web/apierr"
)

// Page sizes of the reads (Interfaces).
const (
	quarantineDefaultLimit = 100
	quarantineMaxLimit     = 500
	filesDefaultLimit      = 200
	filesMaxLimit          = 1000
)

// Routes serves the cleanup read API on mux (r4 design Interfaces);
// authentication is the caller's middleware.
//
//   - GET /api/quarantine?source=&cursor=&limit= lists a source's
//     quarantined top items, newest plan first: {"items":[Quarantined],
//     "next_cursor","total":{"files","bytes"}};
//   - GET /api/checks/{id} is one Check;
//   - GET /api/checks/{id}/files?verdict=&class=&confirmed=1|0&cursor=&limit=
//     lists what a check recorded: {"items":[CheckFile],"next_cursor"}.
//
// The history's kept and export.csv reads are organize's.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/quarantine", s.quarantine)
	mux.HandleFunc("GET /api/checks/{id}", s.check)
	mux.HandleFunc("GET /api/checks/{id}/files", s.checkFiles)
}

// serve answers r with what read returns, read in one read transaction, or
// with the error envelope.
func (s *Service) serve(w http.ResponseWriter, r *http.Request, read func(ctx context.Context, tx *sql.Tx) (any, error)) {
	var body any
	err := s.st.Read(r.Context(), func(tx *sql.Tx) error {
		var err error
		body, err = read(r.Context(), tx)
		return err
	})
	if err != nil {
		var de *domain.Error
		if !errors.As(err, &de) {
			s.log.Error("cleanup: internal error", "err", err)
		}
		apierr.FromError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// checkParams refuses a parameter not in allowed, and a repeated one.
func checkParams(q url.Values, allowed ...string) error {
	for k, vs := range q {
		if !slices.Contains(allowed, k) {
			return domain.Errorf(domain.CodeInvalidRequest, "unknown query parameter %q", k)
		}
		if len(vs) > 1 {
			return domain.Errorf(domain.CodeInvalidRequest, "query parameter %q is repeated", k)
		}
	}
	return nil
}

// limitOf reads a page size: def when s is empty, else a positive integer
// capped at most.
func limitOf(s string, def, most int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, domain.Errorf(domain.CodeInvalidRequest, "limit must be a positive integer, not %q", s)
	}
	return min(n, most), nil
}

// settleRead fails, in a write transaction, the running checks whose job
// ended without them, when there is one (C6): a check read never shows
// one running forever.
func (s *Service) settleRead(ctx context.Context) error {
	var unsettled bool
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM purge_checks WHERE state = 'running'
		AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id = purge_checks.job_id
			AND j.state IN ('queued', 'running', 'paused')))`).Scan(&unsettled); err != nil || !unsettled {
		return err
	}
	return s.st.Write(ctx, func(tx *sql.Tx) error { return settleChecks(ctx, tx, s.clk.Now()) })
}

// pathJSON is a path in its display form and its raw bytes.
type pathJSON struct {
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
}

func pathOf(p []byte) *pathJSON {
	if p == nil {
		p = []byte{}
	}
	return &pathJSON{Path: domain.DisplayName(p), PathB64: p}
}

// checkRef is a check of a quarantined item.
type checkRef struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// quarantinedJSON is Quarantined of the Interfaces section.
type quarantinedJSON struct {
	Entry         *api.EntryRow `json:"entry"`
	Original      *pathJSON     `json:"original"`
	PlanID        *string       `json:"plan_id"`
	QuarantinedAt *time.Time    `json:"quarantined_at"`
	Bytes         int64         `json:"bytes"`
	Files         int64         `json:"files"`
	Check         *checkRef     `json:"check"`
}

type quarantinePage struct {
	Items      []quarantinedJSON `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	Total      amount            `json:"total"`
}

// quarantineKey orders a source's top items, newest plan first: the plan
// folder's and the <seq> folder's names read as numbers, then the entry.
const quarantineKey = `CAST(p.name AS INTEGER), CAST(sq.name AS INTEGER), e.id`

// quarantine lists a source's quarantined top items, newest plan first,
// with their origin (null when unknown, D4) and their newest check. The
// cursor is the last item's key, "<plan>.<seq>.<entry>" in unpadded
// base64url. total is what the quarantine folder holds, records included.
func (s *Service) quarantine(w http.ResponseWriter, r *http.Request) {
	if err := s.settleRead(r.Context()); err != nil {
		apierr.FromError(w, err)
		return
	}
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		qv := r.URL.Query()
		if err := checkParams(qv, "source", "cursor", "limit"); err != nil {
			return nil, err
		}
		src := domain.SourceID(qv.Get("source"))
		if src == "" {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "source is required")
		}
		limit, err := limitOf(qv.Get("limit"), quarantineDefaultLimit, quarantineMaxLimit)
		if err != nil {
			return nil, err
		}
		root, _, err := quarantineOf(ctx, tx, src)
		if err != nil {
			return nil, err
		}
		page := quarantinePage{Items: []quarantinedJSON{}}
		if root == 0 {
			return page, nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT total_files, total_bytes FROM entries WHERE id = ?`, root).
			Scan(&page.Total.Files, &page.Total.Bytes); err != nil {
			return nil, err
		}
		query := `SELECT e.id, ` + quarantineKey + ` FROM entries p
			JOIN entries sq ON sq.parent_id = p.id AND sq.kind = 'directory'
			JOIN entries e ON e.parent_id = sq.id AND e.state <> 'missing'
			WHERE p.parent_id = ? AND p.kind = 'directory'`
		args := []any{root}
		if c := qv.Get("cursor"); c != "" {
			plan, seq, entry, err := decodeKey(c)
			if err != nil {
				return nil, err
			}
			query += ` AND (` + quarantineKey + `) < (?, ?, ?)`
			args = append(args, plan, seq, entry)
		}
		query += ` ORDER BY 2 DESC, 3 DESC, 4 DESC LIMIT ?`
		args = append(args, limit+1)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("cleanup: quarantine of %q: %w", src, err)
		}
		type key struct{ id, plan, seq int64 }
		var keys []key
		for rows.Next() {
			var k key
			var entry int64
			if err := rows.Scan(&k.id, &k.plan, &k.seq, &entry); err != nil {
				rows.Close()
				return nil, err
			}
			keys = append(keys, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(keys) > limit {
			keys = keys[:limit]
			last := keys[limit-1]
			c := encodeKey(last.plan, last.seq, last.id)
			page.NextCursor = &c
		}
		ids := make([]domain.EntryID, len(keys))
		for i, k := range keys {
			ids[i] = domain.EntryID(k.id)
		}
		entries, err := api.EntryRows(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			n, err := loadNode(ctx, tx, k.id)
			if err != nil {
				return nil, err
			}
			it := quarantinedJSON{Entry: entries[domain.EntryID(k.id)], Bytes: n.bytes, Files: n.files}
			o, err := originOf(ctx, tx, n)
			if err != nil {
				return nil, err
			}
			if o != nil {
				it.Original = pathOf(o.fromPath)
				plan := strconv.FormatInt(o.plan, 10)
				it.PlanID = &plan
				if o.at.Valid {
					t := clock.FromMillis(o.at.Int64).UTC()
					it.QuarantinedAt = &t
				}
			}
			var (
				check int64
				state string
			)
			err = tx.QueryRowContext(ctx, `SELECT c.id, c.state FROM purge_check_items i
				JOIN purge_checks c ON c.id = i.check_id WHERE i.entry_id = ? ORDER BY c.id DESC LIMIT 1`, k.id).
				Scan(&check, &state)
			switch {
			case err == nil:
				it.Check = &checkRef{ID: strconv.FormatInt(check, 10), State: state}
			case !errors.Is(err, sql.ErrNoRows):
				return nil, err
			}
			page.Items = append(page.Items, it)
		}
		return page, nil
	})
}

func encodeKey(parts ...int64) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = strconv.FormatInt(p, 10)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(s, ".")))
}

func decodeKey(c string) (plan, seq, entry int64, err error) {
	bad := domain.Errorf(domain.CodeInvalidRequest, "malformed cursor")
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, 0, 0, bad
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 {
		return 0, 0, 0, bad
	}
	var out [3]int64
	for i, p := range parts {
		if out[i], err = strconv.ParseInt(p, 10, 64); err != nil {
			return 0, 0, 0, bad
		}
	}
	return out[0], out[1], out[2], nil
}

// check answers one Check.
func (s *Service) check(w http.ResponseWriter, r *http.Request) {
	if err := s.settleRead(r.Context()); err != nil {
		apierr.FromError(w, err)
		return
	}
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		if err := checkParams(r.URL.Query()); err != nil {
			return nil, err
		}
		id, err := parseID("check", r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		return readCheck(ctx, tx, id)
	})
}

// copyJSON is a check file's verified copy.
type copyJSON struct {
	SourceID string `json:"source_id"`
	pathJSON
	HardLink bool `json:"hard_link"`
}

// checkFileJSON is CheckFile of the Interfaces section.
type checkFileJSON struct {
	ID        string        `json:"id"`
	Item      *api.EntryRow `json:"item"`
	EntryID   *string       `json:"entry_id"`
	Path      string        `json:"path"`
	PathB64   []byte        `json:"path_b64"`
	Member    *string       `json:"member"`
	Kind      string        `json:"kind"`
	Size      int64         `json:"size"`
	Verdict   string        `json:"verdict"`
	Class     *string       `json:"class"`
	Copy      *copyJSON     `json:"copy"`
	Confirmed bool          `json:"confirmed"`
	// ItemReadable is false for a file of an item that holds what the
	// check could not read: that item is never deleted, so its files need
	// no confirmation (D11, G15).
	ItemReadable bool `json:"item_readable"`
}

// checkFiles lists what a check recorded, in record order, filtered by
// verdict, class, and confirmed (1 or 0). The cursor is the last file's
// ID.
func (s *Service) checkFiles(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		qv := r.URL.Query()
		if err := checkParams(qv, "verdict", "class", "confirmed", "cursor", "limit"); err != nil {
			return nil, err
		}
		id, err := parseID("check", r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		var known bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM purge_checks WHERE id = ?)`, id).
			Scan(&known); err != nil {
			return nil, err
		}
		if !known {
			return nil, notFound("check", r.PathValue("id"))
		}
		limit, err := limitOf(qv.Get("limit"), filesDefaultLimit, filesMaxLimit)
		if err != nil {
			return nil, err
		}
		query := `SELECT f.id, f.item_id, f.entry_id, f.member_id, m.path, f.path, f.kind, f.size, f.verdict, f.class,
				f.copy_source, f.copy_path, f.copy_hard_link, ` + confirmedSQL + `, coalesce(i.readable, 1)
			FROM purge_check_files f JOIN purge_checks c ON c.id = f.check_id
			LEFT JOIN archive_members m ON m.id = f.member_id
			LEFT JOIN purge_check_items i ON i.check_id = f.check_id AND i.entry_id = f.item_id
			WHERE f.check_id = ?`
		args := []any{id}
		if v := qv.Get("verdict"); v != "" {
			if !slices.Contains(verdicts, v) {
				return nil, domain.Errorf(domain.CodeInvalidRequest, "unknown verdict %q", v)
			}
			query += ` AND f.verdict = ?`
			args = append(args, v)
		}
		if k := qv.Get("class"); k != "" {
			if !slices.Contains(classes, k) {
				return nil, domain.Errorf(domain.CodeInvalidRequest, "unknown class %q", k)
			}
			query += ` AND f.class = ?`
			args = append(args, k)
		}
		switch qv.Get("confirmed") {
		case "":
		case "1":
			query += ` AND ` + confirmedSQL
		case "0":
			query += ` AND NOT ` + confirmedSQL
		default:
			return nil, domain.Errorf(domain.CodeInvalidRequest, "confirmed is 1 or 0, not %q", qv.Get("confirmed"))
		}
		if c := qv.Get("cursor"); c != "" {
			after, err := strconv.ParseInt(c, 10, 64)
			if err != nil || after < 1 {
				return nil, domain.Errorf(domain.CodeInvalidRequest, "invalid cursor %q", c)
			}
			query += ` AND f.id > ?`
			args = append(args, after)
		}
		query += ` ORDER BY f.id LIMIT ?`
		args = append(args, limit+1)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("cleanup: files of check %d: %w", id, err)
		}
		var (
			files []checkFileJSON
			items []domain.EntryID
		)
		for rows.Next() {
			var (
				f                   checkFileJSON
				fid, item           int64
				entry, member       sql.NullInt64
				memberPath          sql.Null[[]byte]
				path                []byte
				class, copySrc      sql.NullString
				copyPath            sql.Null[[]byte]
				hardLink, confirmed bool
			)
			if err := rows.Scan(&fid, &item, &entry, &member, &memberPath, &path, &f.Kind, &f.Size, &f.Verdict, &class,
				&copySrc, &copyPath, &hardLink, &confirmed, &f.ItemReadable); err != nil {
				rows.Close()
				return nil, err
			}
			f.ID, f.Confirmed = strconv.FormatInt(fid, 10), confirmed
			f.Path, f.PathB64 = domain.DisplayName(path), path
			if f.PathB64 == nil {
				f.PathB64 = []byte{}
			}
			if member.Valid {
				if memberPath.Valid {
					m := domain.DisplayName(memberPath.V)
					f.Member = &m
				}
			} else if entry.Valid {
				e := strconv.FormatInt(entry.Int64, 10)
				f.EntryID = &e
			}
			if class.Valid {
				f.Class = &class.String
			}
			if copyPath.Valid {
				f.Copy = &copyJSON{SourceID: copySrc.String, pathJSON: *pathOf(copyPath.V), HardLink: hardLink}
			}
			files = append(files, f)
			items = append(items, domain.EntryID(item))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		var next *string
		if len(files) > limit {
			files, items = files[:limit], items[:limit]
			c := files[limit-1].ID
			next = &c
		}
		rowsOf, err := api.EntryRows(ctx, tx, items)
		if err != nil {
			return nil, err
		}
		for i := range files {
			files[i].Item = rowsOf[items[i]]
		}
		if files == nil {
			files = []checkFileJSON{}
		}
		return struct {
			Items      []checkFileJSON `json:"items"`
			NextCursor *string         `json:"next_cursor"`
		}{files, next}, nil
	})
}
