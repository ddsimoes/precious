// Package authhttp serves the session endpoints of the single-page app
// (admin-auth spec, design D13): GET /api/session, POST /api/session/login,
// and POST /api/session/logout. It relies on the middleware chain documented
// in package middleware: LoadSession attaches the (pre-login) session,
// RequireAuth lets only GET /api/session and the login through without an
// authenticated session, and CSRF has already validated Origin and the
// X-CSRF-Token header of every POST before these handlers run.
package authhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"

	"precious/internal/auth"
	"precious/internal/domain"
	"precious/internal/web/apierr"
	"precious/internal/web/clientip"
	"precious/internal/web/middleware"
	"precious/internal/web/session"
)

// LoginFailedMessage is the single generic message for every refused login:
// wrong password, throttled attempt, or no administrator.
const LoginFailedMessage = "Login failed."

// sessionResponse is the body of GET /api/session.
type sessionResponse struct {
	Authenticated bool   `json:"authenticated"`
	CSRFToken     string `json:"csrf_token"`
	AdminExists   bool   `json:"admin_exists"`
}

// loginRequest is the body of POST /api/session/login.
type loginRequest struct {
	Password string `json:"password"`
}

// loginResponse is the body of a successful POST /api/session/login.
type loginResponse struct {
	Authenticated bool   `json:"authenticated"`
	CSRFToken     string `json:"csrf_token"`
}

type handler struct {
	svc    *auth.Service
	cookie middleware.SessionCookie
}

// Register adds GET /api/session, POST /api/session/login, and
// POST /api/session/logout to mux.
func Register(mux *http.ServeMux, svc *auth.Service, cookie middleware.SessionCookie) {
	h := &handler{svc: svc, cookie: cookie}
	mux.HandleFunc("GET /api/session", h.session)
	mux.HandleFunc("POST /api/session/login", h.login)
	mux.HandleFunc("POST /api/session/logout", h.logout)
}

// session reports the request's session, first creating a pre-login session
// (and its cookie) when the request has none, so the app always has a CSRF
// token for its next POST.
func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	info, ok := session.From(r.Context())
	if !ok {
		addr, ok := clientAddr(w, r)
		if !ok {
			return
		}
		iss, err := h.svc.StartPreLogin(r.Context(), addr)
		if err != nil {
			internalError(w, r, "start pre-login session", err)
			return
		}
		h.cookie.Set(w, iss.Token)
		info = iss.Info
	}
	exists, err := h.svc.AdminExists(r.Context())
	if err != nil {
		internalError(w, r, "read administrator", err)
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{
		Authenticated: info.Authenticated,
		CSRFToken:     info.CSRFToken,
		AdminExists:   exists,
	})
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	info, ok := session.From(r.Context())
	if !ok {
		// CSRF middleware refuses session-less posts; never proceed without one.
		apierr.Write(w, http.StatusForbidden, domain.CodeForbidden,
			"request could not be verified; reload the page and try again")
		return
	}
	addr, ok := clientAddr(w, r)
	if !ok {
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	iss, err := h.svc.Login(r.Context(), auth.LoginRequest{
		Password:   []byte(req.Password),
		ClientAddr: addr,
		PreviousID: info.ID,
	})
	if errors.Is(err, auth.ErrLoginFailed) {
		apierr.Write(w, http.StatusUnauthorized, domain.CodeLoginFailed, LoginFailedMessage)
		return
	}
	if err != nil {
		internalError(w, r, "login", err)
		return
	}
	h.cookie.Set(w, iss.Token)
	writeJSON(w, http.StatusOK, loginResponse{Authenticated: true, CSRFToken: iss.Info.CSRFToken})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	info, ok := session.From(r.Context())
	if !ok || !info.Authenticated {
		// RequireAuth refuses these; never revoke without an authenticated session.
		apierr.Write(w, http.StatusUnauthorized, domain.CodeUnauthenticated, "authentication required")
		return
	}
	addr, ok := clientAddr(w, r)
	if !ok {
		return
	}
	if err := h.svc.Logout(r.Context(), info.ID, addr); err != nil {
		internalError(w, r, "logout", err)
		return
	}
	h.cookie.Clear(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// decodeJSON reads exactly one JSON object with only known fields into v. The
// body is already capped by the BodyLimit middleware; a body over the cap
// answers 413 request_too_large, any other failure 400 invalid_request.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if mbe := new(http.MaxBytesError); errors.As(err, &mbe) {
			apierr.Write(w, http.StatusRequestEntityTooLarge, domain.CodeRequestTooLarge, "request body too large")
			return false
		}
		apierr.Write(w, http.StatusBadRequest, domain.CodeInvalidRequest, "unreadable request body")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		apierr.Write(w, http.StatusBadRequest, domain.CodeInvalidRequest, "invalid JSON body")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		apierr.Write(w, http.StatusBadRequest, domain.CodeInvalidRequest, "invalid JSON body: trailing data")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// clientAddr returns the address derived by the clientip middleware. Without
// it, throttling and audit would key on the wrong address, so the request
// fails instead.
func clientAddr(w http.ResponseWriter, r *http.Request) (netip.Addr, bool) {
	info, ok := clientip.From(r.Context())
	if !ok || !info.Addr.IsValid() {
		internalError(w, r, "client address", errors.New("clientip middleware did not run"))
		return netip.Addr{}, false
	}
	return info.Addr, true
}

func internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), "auth handler failed", "op", what, "err", err)
	apierr.Write(w, http.StatusInternalServerError, domain.CodeInternal, "internal error")
}
