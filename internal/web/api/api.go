// Package api serves the index read endpoints of the single-page app
// (§11, design D11 and the Interfaces section): Home, an entry with its
// detail, a folder's children and treemap, search, and the tag list.
//
// Each request reads one snapshot: its statements run in one read
// transaction, so a page and its count, or an entry and its ancestors,
// always agree. Entry IDs are decimal strings; tag IDs are numbers. Names
// and paths are sent twice, as the escaped display form
// (domain.DisplayName) and as base64 (standard encoding) of the raw bytes.
// Times are RFC 3339 in UTC, or null.
//
// Errors use the apierr envelope: an unknown or malformed ID is 404
// not_found; an unknown, repeated, or bad query parameter, or a cursor the
// server did not give for that order, is 400 invalid_request.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"precious/internal/domain"
	"precious/internal/rules"
	"precious/internal/store"
	"precious/internal/web/apierr"
)

// Register serves the read endpoints on mux; authentication is the
// caller's middleware. pol explains the rule IDs stored on entries; it is
// the policy the scanner classifies with.
//
//   - GET /api/home[?source=ID]
//   - GET /api/entries/{id}
//   - GET /api/entries/{id}/children?sort=&order=&cursor=&limit=
//   - GET /api/entries/{id}/treemap
//   - GET /api/search?…
//   - GET /api/tags
func Register(mux *http.ServeMux, st *store.Store, pol *rules.Policy, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	h := &handler{st: st, pol: pol, log: log}
	mux.HandleFunc("GET /api/home", h.home)
	mux.HandleFunc("GET /api/entries/{id}", h.entry)
	mux.HandleFunc("GET /api/entries/{id}/children", h.children)
	mux.HandleFunc("GET /api/entries/{id}/treemap", h.treemap)
	mux.HandleFunc("GET /api/search", h.search)
	mux.HandleFunc("GET /api/tags", h.tags)
}

type handler struct {
	st  *store.Store
	pol *rules.Policy
	log *slog.Logger
}

// serve answers r with the body read returns, read in one read
// transaction, or with the error envelope.
func (h *handler) serve(w http.ResponseWriter, r *http.Request, read func(ctx context.Context, tx *sql.Tx) (any, error)) {
	var body any
	err := h.st.Read(r.Context(), func(tx *sql.Tx) error {
		var err error
		body, err = read(r.Context(), tx)
		return err
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	var de *domain.Error
	if !errors.As(err, &de) {
		h.log.Error("api: internal error", "err", err)
	}
	apierr.FromError(w, err)
}

// checkParams refuses a query with a parameter not in allowed, or with one
// given more than once.
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

// parseLimit reads a page size: 0 (the default) when s is empty, else a
// positive integer, which the pager caps.
func parseLimit(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, domain.Errorf(domain.CodeInvalidRequest, "limit must be a positive integer, not %q", s)
	}
	return n, nil
}

// pathID is the entry ID of the request path; a malformed one is not_found.
func pathID(r *http.Request) (domain.EntryID, error) {
	return domain.ParseEntryID(r.PathValue("id"))
}
