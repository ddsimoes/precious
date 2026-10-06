// Package clientip derives the client address and scheme of a request, honoring
// forwarded headers only from configured trusted proxies (§12.3).
//
// This file is the shared contract: the middleware stores the derived values in
// the request context and other handlers read them with Addr.
package clientip

import (
	"context"
	"net/netip"
)

type ctxKey struct{}

// Info is the derived client address and scheme of one request.
type Info struct {
	Addr netip.Addr
	// Scheme is "https" or "http" as derived from the connection or a trusted
	// proxy's forwarded header.
	Scheme string
	// ViaTrustedProxy is true when forwarded headers were honored.
	ViaTrustedProxy bool
}

// With returns ctx carrying info.
func With(ctx context.Context, info Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, info)
}

// From returns the derived info, and false when no middleware ran.
func From(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(ctxKey{}).(Info)
	return info, ok
}
