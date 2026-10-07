// Package viewer serves indexed files and archive members to the interface
// (design D12, §11.12; R2 design D17): GET /api/entries/{ref}/content
// streams a file or a file member with a type from Precious's own table and
// a sandboxing policy, and GET /api/entries/{ref}/text returns its first MiB
// decoded.
//
// A file is opened by content.OpenAt, the identity-checked open hashing
// uses: from the source's root as sources.Open resolves it, its raw path is
// walked through fsaccess one rooted component at a time, so nothing is
// opened by a typed path, no symlink is followed, and no mount is crossed.
// The file is opened read-only (with O_NOATIME where the backend can) and
// only when what is on disk still matches the entry, by the same test a
// rescan uses to leave a row unchanged (R1 design D7, D8; R2 design D4): a
// regular file, not a mount boundary, with the row's size, its mtime_ns and
// ctime_ns within the filesystem's time resolution (or an hour off on a
// local-time filesystem), and its inode where identity is stable. The open
// then re-checks that the object opened is the one lstat described.
// Anything else is 409 invalid_entry_state, which a rescan clears.
//
// A member ("m<id>") of a complete archive is opened by
// content.(*Service).OpenMember, in memory only (ADR 0007, R2.8): the
// archive file passes the same checks and must still be what its listing
// read, else 409 invalid_entry_state. A stored zip member, and a deflated
// one of at most archives.view_max_bytes, are served with ranges; a larger
// one, and a member of a streamed archive (the tar family, gzip, bzip2),
// is streamed whole without Accept-Ranges. Types, headers, and the text
// decoding are those of files, by the member's own name.
package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/web/apierr"
)

// Register adds the content and text endpoints to mux. cs opens archive
// members; it is the hashing service serve runs.
func Register(mux *http.ServeMux, st *store.Store, src *sources.Service, cs *content.Service, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	h := &handler{q: st.Reader(), src: src, cs: cs, log: log}
	mux.HandleFunc("GET /api/entries/{id}/content", h.content)
	mux.HandleFunc("GET /api/entries/{id}/text", h.text)
}

type handler struct {
	q   store.Queryer
	src *sources.Service
	cs  *content.Service
	log *slog.Logger
}

// textBody is the /text response.
type textBody struct {
	Encoding  string  `json:"encoding"`
	Text      string  `json:"text"`
	Truncated bool    `json:"truncated"`
	Language  *string `json:"language"`
	Markdown  bool    `json:"markdown"`
}

func (h *handler) content(w http.ResponseWriter, r *http.Request) {
	protect(w.Header())
	f, err := h.open(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer f.content.Close()
	t := typeOf(f.name)
	hd := w.Header()
	hd.Set("Content-Type", t.mime)
	hd.Set("Content-Security-Policy", t.csp)
	hd.Set("Cache-Control", "private, no-cache")
	if t.download {
		hd.Set("Content-Disposition", attachment(f.display))
	}
	if f.seeker != nil {
		// An empty name keeps ServeContent from choosing a type; the one
		// set above is never replaced by sniffing.
		http.ServeContent(w, r, "", f.modTime, f.seeker)
		return
	}
	// A streamed member: no ranges, the whole member once.
	hd.Set("Content-Length", strconv.FormatInt(f.size, 10))
	hd.Set("Last-Modified", f.modTime.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f.content); err != nil && r.Context().Err() == nil {
		h.log.Warn("viewer: streaming a member failed", "entry", r.PathValue("id"), "err", err)
	}
}

func (h *handler) text(w http.ResponseWriter, r *http.Request) {
	protect(w.Header())
	f, err := h.open(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer f.content.Close()
	buf := make([]byte, min(f.size, textLimit))
	n, err := io.ReadFull(f.content, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		h.fail(w, r, readError(err, f.path))
		return
	}
	truncated := f.size > textLimit
	body := textBody{Truncated: truncated, Language: languageOf(f.name), Markdown: isMarkdown(f.name)}
	body.Encoding, body.Text = decode(buf[:n], truncated)
	hd := w.Header()
	hd.Set("Content-Type", "application/json; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(body); err != nil && r.Context().Err() == nil {
		h.log.Warn("viewer: writing text failed", "entry", r.PathValue("id"), "err", err)
	}
}

// protect sets the headers every viewer response carries, errors included.
func protect(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

// attachment is the Content-Disposition of a download, naming the file by
// its display name.
func attachment(display string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": display}); v != "" {
		return v
	}
	return "attachment"
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if !errors.As(err, &de) && r.Context().Err() == nil {
		h.log.Error("viewer: reading an entry failed", "entry", r.PathValue("id"), "err", err)
	}
	apierr.FromError(w, err)
}

// opened is a file or a file member open for reading.
type opened struct {
	// name is the raw name the type table reads; path the raw path errors
	// name; display the name a download is given.
	name, path []byte
	display    string
	size       int64
	modTime    time.Time
	// content yields the bytes; closing it releases everything.
	content io.ReadCloser
	// seeker is content with ranges; nil for a streamed member.
	seeker io.ReadSeeker
}

// open opens the file or file member a path ref names. A malformed ref
// names nothing: not_found.
func (h *handler) open(ctx context.Context, raw string) (*opened, error) {
	ref, err := domain.ParseRef(raw)
	if err != nil {
		return nil, domain.Errorf(domain.CodeNotFound, "invalid entry id %q", raw)
	}
	if ref.IsMember() {
		return h.openMember(ctx, ref)
	}
	return h.openFile(ctx, ref.Entry)
}

// openFile resolves the entry's source and opens its file, refusing
// anything that is not a present file matching its row.
func (h *handler) openFile(ctx context.Context, id domain.EntryID) (*opened, error) {
	var (
		source, kind, state string
		name                []byte
		row                 content.Row
	)
	err := h.q.QueryRowContext(ctx, `SELECT source_id, path, name, kind, state, size, mtime_ns, ctime_ns, ino
		FROM entries WHERE id = ?`, int64(id)).
		Scan(&source, &row.Path, &name, &kind, &state, &row.Size, &row.MtimeNs, &row.CtimeNs, &row.Ino)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.Errorf(domain.CodeNotFound, "entry %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	if kind != "file" {
		return nil, domain.Errorf(domain.CodeInvalidEntryState, "entry %s is a %s, not a file", id, kind)
	}
	if state != "present" {
		return nil, domain.Errorf(domain.CodeInvalidEntryState, "entry %s is %s", id, state)
	}
	src, err := h.src.Open(ctx, domain.SourceID(source))
	if err != nil {
		return nil, err
	}
	file, info, err := content.OpenAt(src.Root, row, src.Source.Caps)
	if err != nil {
		src.Root.Close()
		return nil, err
	}
	sec := io.NewSectionReader(file, 0, info.Size)
	return &opened{name: name, path: row.Path, display: domain.DisplayName(name), size: info.Size, modTime: info.ModTime, seeker: sec,
		content: &closer{Reader: sec, close: func() error { return errors.Join(file.Close(), src.Root.Close()) }}}, nil
}

// openMember opens a file member of a complete archive through OpenMember.
func (h *handler) openMember(ctx context.Context, ref domain.Ref) (*opened, error) {
	var (
		name, path []byte
		zip        bool
	)
	err := h.q.QueryRowContext(ctx, `SELECT m.name, m.path, a.format = 'zip'
		FROM archive_members m JOIN archives a ON a.entry_id = m.archive_id WHERE m.id = ?`, int64(ref.Member)).
		Scan(&name, &path, &zip)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.Errorf(domain.CodeNotFound, "member %s not found", ref)
	}
	if err != nil {
		return nil, err
	}
	m, err := h.cs.OpenMember(ctx, h.q, ref)
	if err != nil {
		return nil, err
	}
	return &opened{name: name, path: path, display: domain.MemberDisplayName(name, zip), size: m.Size, modTime: m.ModTime,
		content: m.Content, seeker: m.Seeker}, nil
}

// closer is a reader with its own Close.
type closer struct {
	io.Reader
	close func() error
}

func (c *closer) Close() error { return c.close() }

// readError maps a read failure: a path that is gone, replaced, or
// unreadable is invalid_entry_state; a domain error (a member whose archive
// no longer matches its listing) stays as it is; anything else (an I/O
// error) is an internal error.
func readError(err error, path []byte) error {
	switch o, _ := fsaccess.OutcomeOf(err); o {
	case domain.OutcomeAbsent, domain.OutcomeChangedDuringObservation:
		return domain.Wrap(domain.CodeInvalidEntryState, err,
			"%s no longer matches its entry; a rescan updates it", domain.DisplayName(path))
	case domain.OutcomeUnreadable:
		return domain.Wrap(domain.CodeInvalidEntryState, err, "%s cannot be read", domain.DisplayName(path))
	}
	return err
}
