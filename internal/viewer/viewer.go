// Package viewer serves indexed files to the interface (design D12,
// §11.12): GET /api/entries/{id}/content streams a file with a type from
// Precious's own table and a sandboxing policy, and GET
// /api/entries/{id}/text returns its first MiB decoded.
//
// Every read starts from the source's root as sources.Open resolves it and
// walks the entry's raw path through fsaccess, one rooted component at a
// time, so nothing is opened by a typed path, no symlink is followed, and no
// mount is crossed. The file is opened read-only (with O_NOATIME where the
// backend can) and only when what is on disk still matches the entry, by
// the same test a rescan uses to leave a row unchanged (design D7, D8):
//
//   - a regular file, not a mount boundary;
//   - the row's size;
//   - the row's mtime_ns within the filesystem's time resolution, or an hour
//     off on a local-time filesystem (FAT);
//   - the row's ino, when the filesystem has stable identity.
//
// The open itself then re-checks that the object opened is the one that
// lstat described (device, inode, size, and modification and change times).
// Anything else is 409 invalid_entry_state, which a rescan clears.
package viewer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/web/apierr"
)

// Register adds the content and text endpoints to mux.
func Register(mux *http.ServeMux, st *store.Store, src *sources.Service, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	h := &handler{q: st.Reader(), src: src, log: log}
	mux.HandleFunc("GET /api/entries/{id}/content", h.content)
	mux.HandleFunc("GET /api/entries/{id}/text", h.text)
}

type handler struct {
	q   store.Queryer
	src *sources.Service
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
	defer f.close()
	t := typeOf(f.name)
	hd := w.Header()
	hd.Set("Content-Type", t.mime)
	hd.Set("Content-Security-Policy", t.csp)
	hd.Set("Cache-Control", "private, no-cache")
	if t.download {
		hd.Set("Content-Disposition", attachment(f.name))
	}
	// An empty name keeps ServeContent from choosing a type; the one set
	// above is never replaced by sniffing.
	http.ServeContent(w, r, "", f.info.ModTime, io.NewSectionReader(f.file, 0, f.info.Size))
}

func (h *handler) text(w http.ResponseWriter, r *http.Request) {
	protect(w.Header())
	f, err := h.open(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer f.close()
	buf := make([]byte, min(f.info.Size, textLimit))
	n, err := f.file.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		h.fail(w, r, readError(err, f.path))
		return
	}
	truncated := f.info.Size > textLimit
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
func attachment(name []byte) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": domain.DisplayName(name)}); v != "" {
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

// entry is the part of an entries row the viewer needs.
type entry struct {
	source      domain.SourceID
	path, name  []byte
	kind, state string
	size        int64
	mtime, ino  sql.NullInt64
}

func (h *handler) lookup(ctx context.Context, raw string) (entry, error) {
	id, err := domain.ParseEntryID(raw)
	if err != nil {
		return entry{}, err
	}
	var e entry
	var source string
	err = h.q.QueryRowContext(ctx, `SELECT source_id, path, name, kind, state, size, mtime_ns, ino
		FROM entries WHERE id = ?`, int64(id)).
		Scan(&source, &e.path, &e.name, &e.kind, &e.state, &e.size, &e.mtime, &e.ino)
	if errors.Is(err, sql.ErrNoRows) {
		return entry{}, domain.Errorf(domain.CodeNotFound, "entry %s not found", id)
	}
	e.source = domain.SourceID(source)
	return e, err
}

// openFile is an entry's file, open for reading.
type openFile struct {
	file fsaccess.File
	root fsaccess.Dir
	info fsaccess.EntryInfo
	name []byte
	path []byte
}

func (f *openFile) close() {
	f.file.Close()
	f.root.Close()
}

// open resolves the entry's source and opens its file, refusing anything
// that is not a present file matching its row.
func (h *handler) open(ctx context.Context, rawID string) (*openFile, error) {
	e, err := h.lookup(ctx, rawID)
	if err != nil {
		return nil, err
	}
	if e.kind != "file" {
		return nil, domain.Errorf(domain.CodeInvalidEntryState, "entry %s is a %s, not a file", rawID, e.kind)
	}
	if e.state != "present" {
		return nil, domain.Errorf(domain.CodeInvalidEntryState, "entry %s is %s", rawID, e.state)
	}
	opened, err := h.src.Open(ctx, e.source)
	if err != nil {
		return nil, err
	}
	file, info, err := openAt(opened.Root, e, opened.Source.Caps)
	if err != nil {
		opened.Root.Close()
		return nil, err
	}
	return &openFile{file: file, root: opened.Root, info: info, name: e.name, path: e.path}, nil
}

// openAt walks e's path below root and opens the file when it matches e.
func openAt(root fsaccess.Dir, e entry, caps fsaccess.Capabilities) (fsaccess.File, fsaccess.EntryInfo, error) {
	dir, rest := root, e.path
	defer func() {
		if dir != root {
			dir.Close()
		}
	}()
	for {
		i := bytes.IndexByte(rest, '/')
		if i < 0 {
			break
		}
		name := rest[:i]
		info, err := dir.Lstat(name)
		if err != nil {
			return nil, fsaccess.EntryInfo{}, readError(err, e.path)
		}
		if info.Kind != domain.EntryDirectory || info.MountBoundary {
			return nil, fsaccess.EntryInfo{}, changed(e.path)
		}
		next, err := dir.OpenDir(name, info)
		if err != nil {
			return nil, fsaccess.EntryInfo{}, readError(err, e.path)
		}
		if dir != root {
			dir.Close()
		}
		dir, rest = next, rest[i+1:]
	}
	info, err := dir.Lstat(rest)
	if err != nil {
		return nil, fsaccess.EntryInfo{}, readError(err, e.path)
	}
	if !matches(e, info, caps) {
		return nil, fsaccess.EntryInfo{}, changed(e.path)
	}
	f, err := dir.OpenFile(rest, info)
	if err != nil {
		return nil, fsaccess.EntryInfo{}, readError(err, e.path)
	}
	return f, info, nil
}

// matches reports whether info, an lstat of the entry's path, is the file
// its row describes (see the package doc).
func matches(e entry, info fsaccess.EntryInfo, caps fsaccess.Capabilities) bool {
	if info.Kind != domain.EntryFile || info.MountBoundary || info.Size != e.size || !e.mtime.Valid {
		return false
	}
	if caps.StableIdentity && (!e.ino.Valid || uint64(e.ino.Int64) != info.Ino) {
		return false
	}
	return sameMTime(e.mtime.Int64, info.ModTime.UnixNano(), caps)
}

// sameMTime is the unchanged-time test of design D8.
func sameMTime(stored, observed int64, caps fsaccess.Capabilities) bool {
	res := int64(caps.TimeResolution)
	d := observed - stored
	if abs(d) <= res {
		return true
	}
	h := int64(time.Hour)
	return caps.LocalTime && (abs(d-h) <= res || abs(d+h) <= res)
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func changed(path []byte) error {
	return domain.Errorf(domain.CodeInvalidEntryState,
		"%s no longer matches its entry; a rescan updates it", domain.DisplayName(path))
}

// readError maps a filesystem failure on the entry's path: a path that is
// gone, replaced, or unreadable is invalid_entry_state; anything else (an I/O
// error) stays an internal error.
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
