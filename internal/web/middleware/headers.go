// Package middleware holds the browser-protection layers that wrap every
// route (admin-auth spec, design D12/D13): security headers, the request body
// limit, session loading, authentication enforcement, and CSRF plus Origin
// validation.
//
// Intended order, outermost first:
//
//	SecurityHeaders → clientip (trusted-proxy derivation) → BodyLimit →
//	LoadSession → RequireAuth → CSRF → router
package middleware

import (
	"net/http"
	"path"
	"strings"

	"precious/internal/domain"
	"precious/internal/web/apierr"
)

// ContentSecurityPolicy is the application policy of design D12/D13:
// same-origin scripts, styles, images, media, frames, and connections only;
// no inline script or style; no plugins or base URIs; the application itself
// cannot be framed.
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"media-src 'self'; frame-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; " +
	"base-uri 'none'"

// ReferrerPolicy is same-origin rather than no-referrer: under no-referrer,
// browsers send "Origin: null" on same-origin POSTs, which the strict Origin
// check in CSRF must refuse. Other origins still receive no referrer.
const ReferrerPolicy = "same-origin"

// SecurityHeaders sets the CSP, nosniff, and referrer headers on every
// response. No Access-Control-Allow-* header is ever sent: the API is
// same-origin only.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", ReferrerPolicy)
		next.ServeHTTP(w, r)
	})
}

// BodyLimit rejects a request whose declared Content-Length exceeds max with
// 413 request_too_large before any handler runs, and caps every other body
// with http.MaxBytesReader so reading past max fails with *http.MaxBytesError.
// Handlers that read bodies map that error to 413 request_too_large.
func BodyLimit(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				// Close the connection rather than drain an oversized body.
				w.Header().Set("Connection", "close")
				deny(w, r, http.StatusRequestEntityTooLarge, domain.CodeRequestTooLarge, "request body too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

// isAPI reports whether p is part of the JSON API (including /api/events).
// The path is cleaned first, so "/assets/../api/sources" counts as API.
func isAPI(p string) bool {
	p = path.Clean(p)
	return p == "/api" || strings.HasPrefix(p, "/api/")
}

// deny writes an error: the JSON envelope for API paths, plain text for pages.
func deny(w http.ResponseWriter, r *http.Request, status int, code domain.ErrorCode, message string) {
	if isAPI(r.URL.Path) {
		apierr.Write(w, status, code, message)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, message, status)
}
