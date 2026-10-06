// Package session is the shared contract between the authentication middleware
// (which populates it) and the API handlers (which read it, for example to
// return the CSRF token from GET /api/session).
package session

import "context"

type ctxKey struct{}

// Info describes the session attached to the current request.
type Info struct {
	ID int64
	// Authenticated is false for a pre-login session.
	Authenticated bool
	// CSRFToken reaches the app through GET /api/session and the login
	// response, and comes back as the X-CSRF-Token header.
	CSRFToken string
}

// With returns ctx carrying info.
func With(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

// From returns the session of the request, and false when none is attached.
func From(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(ctxKey{}).(Info)
	return info, ok
}
