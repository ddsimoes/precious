package middleware

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"precious/internal/domain"
	"precious/internal/web/session"
)

// SessionLookup resolves a cookie token to a live session. *auth.Service
// implements it.
type SessionLookup interface {
	Lookup(ctx context.Context, token string) (session.Info, bool, error)
}

// LoadSession attaches the live session named by the cookie, if any, to the
// request context (session.With). It never rejects a request by itself.
func LoadSession(lookup SessionLookup, cookie SessionCookie) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := cookie.Token(r)
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			info, ok, err := lookup.Lookup(r.Context(), token)
			if err != nil {
				slog.ErrorContext(r.Context(), "session lookup failed", "err", err)
				deny(w, r, http.StatusInternalServerError, domain.CodeInternal, "internal error")
				return
			}
			if ok {
				r = r.WithContext(session.With(r.Context(), info))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// public reports whether a request is allowed without an authenticated
// session (design D13): GET /api/session, POST /api/session/login (still
// CSRF-checked against the pre-login session), and every GET or HEAD outside
// /api, which serves the application shell or its static assets.
func public(r *http.Request) bool {
	p := r.URL.Path
	switch r.Method {
	case http.MethodGet:
		return p == "/api/session" || !isAPI(p)
	case http.MethodHead:
		return !isAPI(p)
	case http.MethodPost:
		return p == "/api/session/login"
	}
	return false
}

// RequireAuth lets public requests and requests with an authenticated session
// through. Every other request gets 401 unauthenticated with no data: the JSON
// envelope for /api paths (including /api/events), plain text otherwise.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, ok := session.From(r.Context()); (ok && info.Authenticated) || public(r) {
			next.ServeHTTP(w, r)
			return
		}
		deny(w, r, http.StatusUnauthorized, domain.CodeUnauthenticated, "authentication required")
	})
}

// CSRFHeader carries the session's CSRF token.
const CSRFHeader = "X-CSRF-Token"

// CSRF guards every request whose method is not GET, HEAD, or OPTIONS, login
// and logout included. It requires the Origin header to equal the external
// origin (or, only when Origin is absent, the Referer to be on it), and the
// session's or pre-login session's CSRF token in the X-CSRF-Token header. A
// failure answers 403 forbidden without invoking next.
func CSRF(origin Origin) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if !sameOrigin(r, origin) {
				deny(w, r, http.StatusForbidden, domain.CodeForbidden, "cross-origin request refused")
				return
			}
			info, ok := session.From(r.Context())
			if !ok || info.CSRFToken == "" {
				deny(w, r, http.StatusForbidden, domain.CodeForbidden,
					"request could not be verified; reload the page and try again")
				return
			}
			token := r.Header.Get(CSRFHeader)
			if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(info.CSRFToken)) != 1 {
				deny(w, r, http.StatusForbidden, domain.CodeForbidden,
					"request could not be verified; reload the page and try again")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// sameOrigin applies the Origin check, falling back to Referer only when no
// Origin header is present. "Origin: null" never matches.
func sameOrigin(r *http.Request, o Origin) bool {
	if vs := r.Header.Values("Origin"); len(vs) > 0 {
		return len(vs) == 1 && o.matchesHeader(vs[0])
	}
	ref := r.Header.Get("Referer")
	return ref != "" && o.matchesReferer(ref)
}
