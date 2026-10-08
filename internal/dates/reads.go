package dates

import (
	"bytes"
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
	"precious/internal/index"
	"precious/internal/media"
	"precious/internal/web/apierr"
)

// Page sizes of the list (Interfaces).
const (
	listDefaultLimit = 200
	listMaxLimit     = 1000
)

// Routes serves the dates read API on mux (D10, Interfaces); every read
// applies MediaCond, so quarantined, missing, and non-media entries are
// left out even before Rederive deletes their rows. Authentication is the
// caller's middleware.
//
//   - GET /api/dates/summary?source= is the stored summary of one source,
//     or the sum over every source, with the zone;
//   - GET /api/dates?source=&flag=&date_source=&camera=&within=&cursor=
//     &limit=&count= is the list of one source's media dates: by date
//     (none last), or by path within a folder; count=only counts it;
//   - GET /api/dates/cameras?source= is the cameras the last detection
//     found, offset first;
//   - GET /api/entries/{id}/dates is one entry's dates with its candidates,
//     null for anything that is not a media file.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dates/summary", s.summaryRead)
	mux.HandleFunc("GET /api/dates", s.list)
	mux.HandleFunc("GET /api/dates/cameras", s.cameras)
	mux.HandleFunc("GET /api/entries/{id}/dates", s.entryDates)
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
			s.log.Error("dates: internal error", "err", err)
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

// sourceParam reads the required source parameter, refusing an unknown
// source.
func sourceParam(ctx context.Context, tx *sql.Tx, q url.Values) (domain.SourceID, error) {
	src := domain.SourceID(q.Get("source"))
	if src == "" {
		return "", domain.Errorf(domain.CodeInvalidRequest, "source is required")
	}
	if err := knownSource(ctx, tx, src); err != nil {
		return "", err
	}
	return src, nil
}

func knownSource(ctx context.Context, tx *sql.Tx, src domain.SourceID) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM sources WHERE id = ?`, string(src)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeUnknownSource, "unknown source %q", src)
	}
	return err
}

// pathJSON is a path in its display form and its raw bytes.
type pathJSON struct {
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
}

func pathOf(p []byte) pathJSON {
	if p == nil {
		p = []byte{}
	}
	return pathJSON{Path: domain.DisplayName(p), PathB64: p}
}

func millisTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := clock.FromMillis(v.Int64).UTC()
	return &t
}

// ---- summary ----

// summaryJSON is the summary read's body (Interfaces).
type summaryJSON struct {
	summary
	TimeZone    string     `json:"time_zone"`
	TimeZoneSet bool       `json:"time_zone_set"`
	SummaryAt   *time.Time `json:"summary_at"`
	DetectedAt  *time.Time `json:"detected_at"`
}

// zoneName names the zone dates are read in: its IANA name when
// [dates] time_zone is set, else the server's local zone by its
// abbreviation and offset now, as check-config shows it.
func (s *Service) zoneName() (string, bool) {
	if s.zone == time.Local {
		return s.clk.Now().In(time.Local).Format("MST (UTC-07:00)"), false
	}
	return s.zone.String(), true
}

// summaryRead answers the stored summary of ?source=, or, without it, the
// sum of every source's; summary_at and detected_at are then the oldest
// source's, null while any source has none.
func (s *Service) summaryRead(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		qv := r.URL.Query()
		if err := checkParams(qv, "source"); err != nil {
			return nil, err
		}
		query := `SELECT s.id, m.summary, m.summary_at, m.detected_at FROM sources s
			LEFT JOIN media_sources m ON m.source_id = s.id`
		var args []any
		if qv.Has("source") {
			src, err := sourceParam(ctx, tx, qv)
			if err != nil {
				return nil, err
			}
			query += ` WHERE s.id = ?`
			args = append(args, string(src))
		}
		rows, err := tx.QueryContext(ctx, query+` ORDER BY s.id`, args...)
		if err != nil {
			return nil, fmt.Errorf("dates: read the summaries: %w", err)
		}
		defer rows.Close()
		var (
			out           summaryJSON
			summaryAt     *time.Time
			detectedAt    *time.Time
			missingAt     bool
			missingDetect bool
		)
		for rows.Next() {
			var (
				src          string
				raw          sql.NullString
				sumAt, detAt sql.NullInt64
			)
			if err := rows.Scan(&src, &raw, &sumAt, &detAt); err != nil {
				return nil, fmt.Errorf("dates: read the summaries: %w", err)
			}
			if raw.Valid {
				one, err := decodeSummary(raw.String)
				if err != nil {
					return nil, fmt.Errorf("dates: decode the summary of %q: %w", src, err)
				}
				out.add(&one)
			}
			summaryAt, missingAt = oldest(summaryAt, millisTime(sumAt), missingAt)
			detectedAt, missingDetect = oldest(detectedAt, millisTime(detAt), missingDetect)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("dates: read the summaries: %w", err)
		}
		if !missingAt {
			out.SummaryAt = summaryAt
		}
		if !missingDetect {
			out.DetectedAt = detectedAt
		}
		out.TimeZone, out.TimeZoneSet = s.zoneName()
		return out, nil
	})
}

// oldest folds t into acc, the oldest time so far; missing records a nil.
func oldest(acc, t *time.Time, missing bool) (*time.Time, bool) {
	if t == nil {
		return acc, true
	}
	if acc == nil || t.Before(*acc) {
		return t, missing
	}
	return acc, missing
}

// ---- the list ----

// flagName is a flag as the API names it, with its partial index over
// (source_id, effective_ns, entry_id) WHERE flags & bit <> 0.
type flagName struct {
	name  string
	bit   media.Flags
	index string
}

// apiFlags are the flags in bit order.
var apiFlags = []flagName{
	{"mtime_disagrees", media.FlagMtimeDisagrees, "media_dates_mtime_disagrees"},
	{"implausible", media.FlagImplausible, "media_dates_implausible"},
	{"camera_offset", media.FlagCameraOffset, "media_dates_camera_offset"},
	{"no_date_metadata", media.FlagNoDateMetadata, "media_dates_no_date_metadata"},
}

// names lists the flags of f by name, in bit order; never nil.
func names(f media.Flags) []string {
	out := []string{}
	for _, fl := range apiFlags {
		if f&fl.bit != 0 {
			out = append(out, fl.name)
		}
	}
	return out
}

var dateSources = []media.Source{media.SourceOwner, media.SourceEXIF, media.SourceGPS, media.SourceContainer,
	media.SourceFileName, media.SourceFolderName, media.SourceMtime, media.SourceNone}

// entryJSON is MediaDate's entry.
type entryJSON struct {
	ID       string          `json:"id"`
	SourceID domain.SourceID `json:"source_id"`
	Name     string          `json:"name"`
	pathJSON
	Size  int64      `json:"size"`
	MTime *time.Time `json:"mtime"`
}

// dateJSON is DateJSON of the Interfaces.
type dateJSON struct {
	Instant    *time.Time `json:"instant"`
	Local      *string    `json:"local"`
	OffsetMin  *int64     `json:"offset_min"`
	Precision  *string    `json:"precision"`
	Source     string     `json:"source"`
	Confidence string     `json:"confidence"`
	Refined    bool       `json:"refined"`
	Corrected  *string    `json:"corrected"`
}

// cameraJSON is a MediaDate's camera.
type cameraJSON struct {
	Key    string `json:"key"`
	Make   string `json:"make"`
	Model  string `json:"model"`
	Serial string `json:"serial"`
}

// correctionOut is CorrectionJSON of the Interfaces.
type correctionOut struct {
	Kind      string    `json:"kind"`
	Local     string    `json:"local,omitempty"`
	OffsetMin *int64    `json:"offset_min,omitempty"`
	ShiftS    *int64    `json:"shift_s,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// datesJSON is MediaDate without its entry.
type datesJSON struct {
	Date       dateJSON       `json:"date"`
	Metadata   string         `json:"metadata"`
	Flags      []string       `json:"flags"`
	Camera     *cameraJSON    `json:"camera"`
	Correction *correctionOut `json:"correction"`
}

// mediaDateJSON is MediaDate of the Interfaces.
type mediaDateJSON struct {
	Entry entryJSON `json:"entry"`
	datesJSON
}

type listPage struct {
	Items      []mediaDateJSON `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

type countBody struct {
	Count int64 `json:"count"`
}

// rowColumns are the columns every list row reads, scanned by scanRow:
// the entry, its media_dates row (alias d), its read metadata's camera
// (m), and its correction (c).
const rowColumns = `e.id, e.source_id, e.name, e.path, e.size, e.mtime_ns,
	d.effective_ns, d.local, d.offset_min, d.precision, d.source, d.confidence, d.refined, d.corrected, d.flags,
	d.meta_state, d.camera_key, m.make, m.model, m.serial,
	c.kind, c.set_local, c.set_offset_min, c.shift_s, c.created_at`

// rowJoins reads a row's metadata and correction by their primary keys.
const rowJoins = ` LEFT JOIN media_meta m ON m.entry_id = e.id
	LEFT JOIN date_corrections c ON c.entry_id = e.id`

// listRow is one scanned list row, with its sort keys.
type listRow struct {
	item      mediaDateJSON
	id        int64
	path      []byte
	effective sql.NullInt64
}

func scanRow(rows *sql.Rows) (listRow, error) {
	var (
		r                         listRow
		name                      []byte
		mtime                     sql.NullInt64
		local, precision          sql.NullString
		offset                    sql.NullInt64
		corrected                 sql.NullString
		flags                     int64
		cameraKey                 sql.NullString
		mk, model, serial         sql.NullString
		cKind, cLocal             sql.NullString
		cOffset, cShift, cCreated sql.NullInt64
		it                        = &r.item
	)
	if err := rows.Scan(&r.id, &it.Entry.SourceID, &name, &r.path, &it.Entry.Size, &mtime,
		&r.effective, &local, &offset, &precision, &it.Date.Source, &it.Date.Confidence, &it.Date.Refined, &corrected,
		&flags, &it.Metadata, &cameraKey, &mk, &model, &serial,
		&cKind, &cLocal, &cOffset, &cShift, &cCreated); err != nil {
		return listRow{}, err
	}
	it.Entry.ID = domain.EntryID(r.id).String()
	it.Entry.Name = domain.DisplayName(name)
	it.Entry.pathJSON = pathOf(r.path)
	if mtime.Valid && domain.KnownModTime(mtime.Int64) {
		t := time.Unix(0, mtime.Int64).UTC()
		it.Entry.MTime = &t
	}
	it.datesJSON = datesOf(r.effective, local, offset, precision, corrected, media.Flags(flags), cameraKey, mk, model,
		serial, cKind, cLocal, cOffset, cShift, cCreated, it.Date.Source, it.Date.Confidence, it.Date.Refined,
		it.Metadata)
	return r, nil
}

// datesOf assembles a datesJSON from a media_dates row and its joins.
func datesOf(effective sql.NullInt64, local sql.NullString, offset sql.NullInt64, precision, corrected sql.NullString,
	flags media.Flags, cameraKey, mk, model, serial, cKind, cLocal sql.NullString, cOffset, cShift, cCreated sql.NullInt64,
	source, confidence string, refined bool, metadata string) datesJSON {
	d := datesJSON{Metadata: metadata, Flags: names(flags)}
	d.Date = dateJSON{Source: source, Confidence: confidence, Refined: refined}
	if effective.Valid {
		t := time.Unix(0, effective.Int64).UTC()
		d.Date.Instant = &t
	}
	d.Date.Local = nullString(local)
	d.Date.Precision = nullString(precision)
	d.Date.Corrected = nullString(corrected)
	d.Date.OffsetMin = nullInt(offset)
	if cameraKey.Valid {
		d.Camera = &cameraJSON{Key: cameraKey.String, Make: mk.String, Model: model.String, Serial: serial.String}
	}
	if cKind.Valid {
		d.Correction = &correctionOut{Kind: cKind.String, Local: cLocal.String, OffsetMin: nullInt(cOffset),
			ShiftS: nullInt(cShift), CreatedAt: clock.FromMillis(cCreated.Int64).UTC()}
	}
	return d
}

func nullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// listCursor is the list's position: the last row's effective_ns and
// entry ID in date order (None once in the dateless rows, which come
// last), or its path in path order (within).
type listCursor struct {
	Ns   *int64 `json:"t,omitempty"`
	None bool   `json:"n,omitempty"`
	ID   int64  `json:"id,omitempty"`
	Path []byte `json:"p,omitempty"`
}

func (c listCursor) encode() *string {
	b, _ := json.Marshal(c)
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func decodeCursor(s string, within bool) (*listCursor, error) {
	bad := domain.Errorf(domain.CodeInvalidRequest, "invalid cursor")
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, bad
	}
	var c listCursor
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, bad
	}
	switch {
	case within && c.Path != nil && c.Ns == nil && !c.None && c.ID == 0:
	case !within && c.Path == nil && c.ID > 0 && (c.Ns != nil) != c.None:
	default:
		return nil, bad
	}
	return &c, nil
}

// listQuery is a parsed list request.
type listQuery struct {
	src       domain.SourceID
	flag      int // index into apiFlags, or -1
	source    string
	camera    string
	within    *withinFolder
	limit     int
	cursor    *listCursor
	countOnly bool
}

// withinFolder is the folder ?within= names: its subtree is the half-open
// path range [lo, hi), or the whole source for its top.
type withinFolder struct {
	lo, hi []byte
	top    bool
}

func (s *Service) parseList(ctx context.Context, tx *sql.Tx, qv url.Values) (listQuery, error) {
	q := listQuery{flag: -1}
	if err := checkParams(qv, "source", "flag", "date_source", "camera", "within", "cursor", "limit", "count"); err != nil {
		return q, err
	}
	src, err := sourceParam(ctx, tx, qv)
	if err != nil {
		return q, err
	}
	q.src = src
	if qv.Has("flag") {
		f := qv.Get("flag")
		q.flag = slices.IndexFunc(apiFlags, func(fl flagName) bool { return fl.name == f })
		if q.flag < 0 {
			return q, domain.Errorf(domain.CodeInvalidRequest, "flag must be mtime_disagrees, implausible, camera_offset, or no_date_metadata")
		}
	}
	if qv.Has("date_source") {
		q.source = qv.Get("date_source")
		if !slices.Contains(dateSources, media.Source(q.source)) {
			return q, domain.Errorf(domain.CodeInvalidRequest, "date_source must be owner, exif, gps, container, file_name, folder_name, mtime, or none")
		}
	}
	if qv.Has("camera") {
		q.camera = qv.Get("camera")
		if q.camera == "" {
			return q, domain.Errorf(domain.CodeInvalidRequest, "camera must not be empty")
		}
	}
	if qv.Has("within") {
		if q.within, err = withinOf(ctx, tx, src, qv.Get("within")); err != nil {
			return q, err
		}
	}
	if qv.Has("count") {
		if qv.Get("count") != "only" {
			return q, domain.Errorf(domain.CodeInvalidRequest, "count takes only the value only")
		}
		if qv.Has("cursor") || qv.Has("limit") {
			return q, domain.Errorf(domain.CodeInvalidRequest, "count=only takes no cursor or limit")
		}
		q.countOnly = true
	}
	q.limit = listDefaultLimit
	if qv.Has("limit") {
		n, err := strconv.Atoi(qv.Get("limit"))
		if err != nil || n < 1 {
			return q, domain.Errorf(domain.CodeInvalidRequest, "limit must be a positive integer, not %q", qv.Get("limit"))
		}
		q.limit = min(n, listMaxLimit)
	}
	if qv.Has("cursor") {
		if q.cursor, err = decodeCursor(qv.Get("cursor"), q.within != nil); err != nil {
			return q, err
		}
	}
	return q, nil
}

// withinOf resolves ?within= to a present folder of src.
func withinOf(ctx context.Context, tx *sql.Tx, src domain.SourceID, raw string) (*withinFolder, error) {
	ref, err := domain.ParseRef(raw)
	if err != nil {
		return nil, err
	}
	if ref.IsMember() {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "within must be a folder, not an archive member")
	}
	var (
		path       []byte
		kind, st   string
		folderFrom string
	)
	err = tx.QueryRowContext(ctx, `SELECT path, kind, state, source_id FROM entries WHERE id = ?`, int64(ref.Entry)).
		Scan(&path, &kind, &st, &folderFrom)
	if errors.Is(err, sql.ErrNoRows) || err == nil && st == "missing" {
		return nil, domain.Errorf(domain.CodeNotFound, "folder %s not found", raw)
	}
	if err != nil {
		return nil, fmt.Errorf("dates: read folder %s: %w", raw, err)
	}
	if kind != string(domain.EntryDirectory) || domain.SourceID(folderFrom) != src {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "within must be a folder of source %q", src)
	}
	if len(path) == 0 {
		return &withinFolder{top: true}, nil
	}
	return &withinFolder{lo: append(append([]byte{}, path...), '/'), hi: append(append([]byte{}, path...), '0')}, nil
}

// list answers GET /api/dates (D10). The driving index is within's path
// range, else camera's, else flag's, else date_source's, else the date
// order's; the other filters are residual. In date order, the dated rows
// come first by (effective_ns, entry_id), then the dateless ones by
// entry_id, each phase one range of the driving index.
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		q, err := s.parseList(ctx, tx, r.URL.Query())
		if err != nil {
			return nil, err
		}
		if q.countOnly {
			stmt, args := q.countSQL()
			var n int64
			if err := tx.QueryRowContext(ctx, stmt, args...).Scan(&n); err != nil {
				return nil, fmt.Errorf("dates: count the dates of %q: %w", q.src, err)
			}
			return countBody{Count: n}, nil
		}
		page := listPage{Items: []mediaDateJSON{}}
		var got []listRow
		for _, seg := range q.segments() {
			stmt, args := q.pageSQL(seg, q.limit+1-len(got))
			rows, err := tx.QueryContext(ctx, stmt, args...)
			if err != nil {
				return nil, fmt.Errorf("dates: list the dates of %q: %w", q.src, err)
			}
			for rows.Next() {
				row, err := scanRow(rows)
				if err != nil {
					rows.Close()
					return nil, fmt.Errorf("dates: list the dates of %q: %w", q.src, err)
				}
				got = append(got, row)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("dates: list the dates of %q: %w", q.src, err)
			}
			if len(got) > q.limit {
				break
			}
		}
		if len(got) > q.limit {
			got = got[:q.limit]
			last := got[len(got)-1]
			var c listCursor
			switch {
			case q.within != nil:
				c.Path = last.path
			case last.effective.Valid:
				ns := last.effective.Int64
				c.Ns, c.ID = &ns, last.id
			default:
				c.None, c.ID = true, last.id
			}
			page.NextCursor = c.encode()
		}
		for _, row := range got {
			page.Items = append(page.Items, row.item)
		}
		return page, nil
	})
}

// segment is one ordered range a page reads: the path range (within), or
// the dated or the dateless rows of the date order.
type segment int

const (
	segPath segment = iota
	segDated
	segDateless
)

func (q listQuery) segments() []segment {
	switch {
	case q.within != nil:
		return []segment{segPath}
	case q.cursor != nil && q.cursor.None:
		return []segment{segDateless}
	}
	return []segment{segDated, segDateless}
}

// driver returns the FROM clause, its WHERE terms, and their arguments for
// the driving index: d INDEXED BY the index D10 names, joined to e by
// primary key (or e's (source_id, path) range for within), with
// MediaCond and the residual filters.
func (q listQuery) driver() (from string, where []string, args []any) {
	residual := func(skip string) {
		if q.flag >= 0 && skip != "flag" {
			where = append(where, `d.flags & ? <> 0`)
			args = append(args, int64(apiFlags[q.flag].bit))
		}
		if q.source != "" && skip != "source" {
			where = append(where, `d.source = ?`)
			args = append(args, q.source)
		}
		if q.camera != "" && skip != "camera" {
			where = append(where, `d.camera_key = ?`)
			args = append(args, q.camera)
		}
	}
	if q.within != nil {
		from = `entries e INDEXED BY sqlite_autoindex_entries_1 CROSS JOIN media_dates d NOT INDEXED ON d.entry_id = e.id`
		where = append(where, `e.source_id = ?`)
		args = append(args, string(q.src))
		if !q.within.top {
			where = append(where, `e.path >= ?`, `e.path < ?`)
			args = append(args, q.within.lo, q.within.hi)
		}
		where = append(where, MediaCond("e"))
		residual("")
		return from, where, args
	}
	where = append(where, `d.source_id = ?`)
	args = append(args, string(q.src))
	var skip string
	switch {
	case q.camera != "":
		from, skip = `media_dates d INDEXED BY media_dates_by_camera`, "camera"
		where = append(where, `d.camera_key = ?`, `d.camera_key IS NOT NULL`)
		args = append(args, q.camera)
	case q.flag >= 0:
		fl := apiFlags[q.flag]
		from, skip = `media_dates d INDEXED BY `+fl.index, "flag"
		// The literal term matches the partial index's WHERE.
		where = append(where, fmt.Sprintf(`d.flags & %d <> 0`, fl.bit))
	case q.source != "":
		from, skip = `media_dates d INDEXED BY media_dates_by_source`, "source"
		where = append(where, `d.source = ?`)
		args = append(args, q.source)
	default:
		from = `media_dates d INDEXED BY media_dates_by_time`
	}
	from += ` CROSS JOIN entries e ON e.id = d.entry_id`
	where = append(where, MediaCond("e"))
	residual(skip)
	return from, where, args
}

func (q listQuery) pageSQL(seg segment, limit int) (string, []any) {
	from, where, args := q.driver()
	var order string
	switch seg {
	case segPath:
		if q.cursor != nil {
			where = append(where, `e.path > ?`)
			args = append(args, q.cursor.Path)
		}
		order = `e.path`
	case segDated:
		where = append(where, `d.effective_ns IS NOT NULL`)
		if q.cursor != nil {
			// A range from the cursor's time; its ties up to its entry
			// are a residual.
			where = append(where, `d.effective_ns >= ?`, `(d.effective_ns > ? OR d.entry_id > ?)`)
			args = append(args, *q.cursor.Ns, *q.cursor.Ns, q.cursor.ID)
		}
		order = `d.effective_ns, d.entry_id`
	case segDateless:
		where = append(where, `d.effective_ns IS NULL`)
		if q.cursor != nil && q.cursor.None {
			where = append(where, `d.entry_id > ?`)
			args = append(args, q.cursor.ID)
		}
		order = `d.entry_id`
	}
	args = append(args, limit)
	return `SELECT ` + rowColumns + ` FROM ` + from + rowJoins + ` WHERE ` + strings.Join(where, ` AND `) +
		` ORDER BY ` + order + ` LIMIT ?`, args
}

func (q listQuery) countSQL() (string, []any) {
	from, where, args := q.driver()
	return `SELECT count(*) FROM ` + from + ` WHERE ` + strings.Join(where, ` AND `), args
}

// ---- cameras ----

type cameraEventJSON struct {
	Folder    folderJSON `json:"folder"`
	DeltaS    int64      `json:"delta_s"`
	Photos    int64      `json:"photos"`
	Reference *string    `json:"reference"`
}

type folderJSON struct {
	ID string `json:"id"`
	pathJSON
}

type cameraItemJSON struct {
	Key             string            `json:"key"`
	Make            string            `json:"make"`
	Model           string            `json:"model"`
	Serial          string            `json:"serial"`
	SourceID        domain.SourceID   `json:"source_id"`
	Photos          int64             `json:"photos"`
	State           string            `json:"state"`
	SuggestedShiftS *int64            `json:"suggested_shift_s"`
	Events          []cameraEventJSON `json:"events"`
	ComputedAt      time.Time         `json:"computed_at"`
}

type camerasBody struct {
	Items []cameraItemJSON `json:"items"`
}

// basisJSON is the part of media_cameras.basis the read shows (Interfaces);
// other keys are the detection's own.
type basisJSON struct {
	Events []struct {
		FolderID  string  `json:"folder_id"`
		DeltaS    int64   `json:"delta_s"`
		Photos    int64   `json:"photos"`
		Reference *string `json:"reference"`
	} `json:"events"`
}

// camerasSQL reads a source's cameras in their order, each with at least
// one media file left (media_dates_by_camera). A source has few cameras,
// so they are sorted after reading.
var camerasSQL = `SELECT c.camera_key, c.make, c.model, c.serial, c.photos, c.state, c.suggested_shift_s, c.basis,
	c.computed_at FROM media_cameras c
	WHERE c.source_id = ? AND EXISTS (SELECT 1 FROM media_dates d INDEXED BY media_dates_by_camera
		CROSS JOIN entries e ON e.id = d.entry_id
		WHERE d.source_id = c.source_id AND d.camera_key = c.camera_key AND ` + MediaCond("e") + `)
	ORDER BY CASE c.state WHEN 'offset' THEN 0 WHEN 'disagrees' THEN 1 ELSE 2 END, c.photos DESC, c.camera_key`

// cameras answers GET /api/dates/cameras: the source's media_cameras rows,
// offset, then disagrees, then ok, each by photos (most first), then key.
// Applying MediaCond, an event whose folder is no longer a present folder
// of the source outside the quarantine is left out, its path is the
// folder's current one, and a camera none of whose photos is still a media
// file of the source is left out.
func (s *Service) cameras(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		qv := r.URL.Query()
		if err := checkParams(qv, "source"); err != nil {
			return nil, err
		}
		src, err := sourceParam(ctx, tx, qv)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, camerasSQL, string(src))
		if err != nil {
			return nil, fmt.Errorf("dates: read the cameras of %q: %w", src, err)
		}
		type cam struct {
			item  cameraItemJSON
			basis string
		}
		var cams []cam
		for rows.Next() {
			var (
				c                 cam
				mk, model, serial sql.NullString
				shift             sql.NullInt64
				computed          int64
			)
			if err := rows.Scan(&c.item.Key, &mk, &model, &serial, &c.item.Photos, &c.item.State, &shift, &c.basis,
				&computed); err != nil {
				rows.Close()
				return nil, fmt.Errorf("dates: read the cameras of %q: %w", src, err)
			}
			c.item.Make, c.item.Model, c.item.Serial = mk.String, model.String, serial.String
			c.item.SourceID, c.item.SuggestedShiftS = src, nullInt(shift)
			c.item.ComputedAt = clock.FromMillis(computed).UTC()
			cams = append(cams, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("dates: read the cameras of %q: %w", src, err)
		}
		body := camerasBody{Items: make([]cameraItemJSON, 0, len(cams))}
		for _, c := range cams {
			c.item.Events = []cameraEventJSON{}
			var b basisJSON
			if err := json.Unmarshal([]byte(c.basis), &b); err != nil {
				return nil, fmt.Errorf("dates: decode the basis of camera %q: %w", c.item.Key, err)
			}
			for _, ev := range b.Events {
				folder, ok, err := eventFolder(ctx, tx, src, ev.FolderID)
				if err != nil {
					return nil, err
				}
				if ok {
					c.item.Events = append(c.item.Events, cameraEventJSON{Folder: folder, DeltaS: ev.DeltaS,
						Photos: ev.Photos, Reference: ev.Reference})
				}
			}
			body.Items = append(body.Items, c.item)
		}
		return body, nil
	})
}

// eventFolder reads an event's folder: false when it is gone (not a present
// folder of src, or in the quarantine).
func eventFolder(ctx context.Context, tx *sql.Tx, src domain.SourceID, raw string) (folderJSON, bool, error) {
	ref, err := domain.ParseRef(raw)
	if err != nil || ref.IsMember() {
		return folderJSON{}, false, nil
	}
	var path []byte
	err = tx.QueryRowContext(ctx, `SELECT f.path FROM entries f WHERE f.id = ? AND f.source_id = ?
		AND f.kind = 'directory' AND f.state = 'present' AND `+index.NotQuarantined("f"), int64(ref.Entry), string(src)).
		Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return folderJSON{}, false, nil
	}
	if err != nil {
		return folderJSON{}, false, fmt.Errorf("dates: read the event folder %s: %w", raw, err)
	}
	return folderJSON{ID: ref.String(), pathJSON: pathOf(path)}, true, nil
}

// ---- one entry ----

// candidateJSON is one candidate of an entry's dates, derived on read.
type candidateJSON struct {
	Source    string    `json:"source"`
	Local     string    `json:"local"`
	OffsetMin *int      `json:"offset_min"`
	Instant   time.Time `json:"instant"`
	Precision string    `json:"precision"`
	Plausible bool      `json:"plausible"`
}

type entryDatesJSON struct {
	datesJSON
	Candidates []candidateJSON `json:"candidates"`
}

type entryDatesBody struct {
	Dates *entryDatesJSON `json:"dates"`
}

// entryDates answers GET /api/entries/{id}/dates: null for anything
// MediaCond leaves out (not media, a member, missing, quarantined), 404 for
// an unknown ID. The date, flags, and state are the stored row's; the
// candidates, and the date of a media file not derived yet, are derived on
// read from the same inputs (D9).
func (s *Service) entryDates(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, func(ctx context.Context, tx *sql.Tx) (any, error) {
		if err := checkParams(r.URL.Query()); err != nil {
			return nil, err
		}
		raw := r.PathValue("id")
		ref, err := domain.ParseRef(raw)
		if err != nil {
			return nil, err
		}
		if ref.IsMember() {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM archive_members WHERE id = ?`, int64(ref.Member)).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, domain.Errorf(domain.CodeNotFound, "entry %s not found", raw)
			}
			if err != nil {
				return nil, err
			}
			return entryDatesBody{}, nil
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(inputsQuery, MediaCond("e"), "?"), int64(ref.Entry))
		if err != nil {
			return nil, fmt.Errorf("dates: read the inputs of entry %s: %w", raw, err)
		}
		var (
			in    inputsRow
			found bool
		)
		for rows.Next() {
			if in, err = scanInputs(rows); err != nil {
				rows.Close()
				return nil, fmt.Errorf("dates: read the inputs of entry %s: %w", raw, err)
			}
			found = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("dates: read the inputs of entry %s: %w", raw, err)
		}
		if !found {
			return nil, domain.Errorf(domain.CodeNotFound, "entry %s not found", raw)
		}
		if !in.media {
			return entryDatesBody{}, nil
		}
		caps, err := sourceCaps(ctx, tx, in.source)
		if err != nil {
			return nil, err
		}
		inputs := s.inputs(in, caps, s.clk.Now())
		eff := media.Derive(inputs)
		out := &entryDatesJSON{Candidates: make([]candidateJSON, 0, len(eff.Candidates))}
		for _, c := range eff.Candidates {
			out.Candidates = append(out.Candidates, candidateJSON{Source: string(c.Source), Local: c.Date.Local,
				OffsetMin: c.Date.OffsetMin, Instant: c.Date.Instant.UTC(), Precision: string(c.Date.Precision),
				Plausible: c.Plausible})
		}
		if in.hasDate {
			out.datesJSON, err = storedDates(ctx, tx, in.id)
			if err != nil {
				return nil, err
			}
		} else {
			out.datesJSON = derivedDates(eff, inputs)
			if err := correctionOf(ctx, tx, in.id, &out.datesJSON); err != nil {
				return nil, err
			}
		}
		return entryDatesBody{Dates: out}, nil
	})
}

// storedDates reads an entry's media_dates row with its camera and
// correction, as a list row shows them.
func storedDates(ctx context.Context, tx *sql.Tx, id domain.EntryID) (datesJSON, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+rowColumns+` FROM media_dates d CROSS JOIN entries e ON e.id = d.entry_id`+
		rowJoins+` WHERE d.entry_id = ?`, int64(id))
	if err != nil {
		return datesJSON{}, fmt.Errorf("dates: read the dates of entry %d: %w", id, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return datesJSON{}, err
		}
		return datesJSON{}, fmt.Errorf("dates: the dates of entry %d vanished", id)
	}
	row, err := scanRow(rows)
	if err != nil {
		return datesJSON{}, fmt.Errorf("dates: read the dates of entry %d: %w", id, err)
	}
	return row.item.datesJSON, nil
}

// derivedDates is a datesJSON of a date derived on read.
func derivedDates(eff media.Effective, in media.Inputs) datesJSON {
	d := datesJSON{Metadata: string(in.MetaState), Flags: names(eff.Flags)}
	d.Date = dateJSON{Source: string(eff.Source), Confidence: string(eff.Confidence), Refined: eff.Refined}
	if eff.Date != nil {
		t, local, p := eff.Date.Instant.UTC(), eff.Date.Local, string(eff.Date.Precision)
		d.Date.Instant, d.Date.Local, d.Date.Precision = &t, &local, &p
		if eff.Date.OffsetMin != nil {
			off := int64(*eff.Date.OffsetMin)
			d.Date.OffsetMin = &off
		}
	}
	if eff.Corrected != "" {
		c := eff.Corrected
		d.Date.Corrected = &c
	}
	if m := in.Meta; m != nil {
		if k := media.CameraKey(m.Make, m.Model, m.Serial); k != "" {
			d.Camera = &cameraJSON{Key: k, Make: m.Make, Model: m.Model, Serial: m.Serial}
		}
	}
	return d
}

// correctionOf sets d's correction from the entry's row, if any.
func correctionOf(ctx context.Context, tx *sql.Tx, id domain.EntryID, d *datesJSON) error {
	var (
		kind          string
		local         sql.NullString
		offset, shift sql.NullInt64
		createdAt     int64
	)
	err := tx.QueryRowContext(ctx, `SELECT kind, set_local, set_offset_min, shift_s, created_at FROM date_corrections
		WHERE entry_id = ?`, int64(id)).Scan(&kind, &local, &offset, &shift, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("dates: read the correction of entry %d: %w", id, err)
	}
	d.Correction = &correctionOut{Kind: kind, Local: local.String, OffsetMin: nullInt(offset), ShiftS: nullInt(shift),
		CreatedAt: clock.FromMillis(createdAt).UTC()}
	return nil
}
