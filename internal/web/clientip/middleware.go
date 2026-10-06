package clientip

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Middleware derives each request's client address and scheme and stores them
// with With. The immediate peer comes from RemoteAddr and the scheme from the
// connection (r.TLS). Only when the peer is inside a trusted prefix are the
// forwarded headers honored: RFC 7239 Forwarded (for=, proto=) when present,
// otherwise X-Forwarded-For and X-Forwarded-Proto. From any other peer they are
// ignored. Request headers are not modified.
func Middleware(trusted []netip.Prefix) func(http.Handler) http.Handler {
	d := deriver{trusted: slices.Clone(trusted)}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(With(r.Context(), d.derive(r))))
		})
	}
}

type deriver struct {
	trusted []netip.Prefix
}

func (d deriver) isTrusted(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range d.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (d deriver) derive(r *http.Request) Info {
	info := Info{Addr: peerAddr(r.RemoteAddr), Scheme: "http"}
	if r.TLS != nil {
		info.Scheme = "https"
	}
	if !d.isTrusted(info.Addr) {
		return info
	}

	fwd := r.Header.Values("Forwarded")
	xff := r.Header.Values("X-Forwarded-For")
	xfp := r.Header.Values("X-Forwarded-Proto")
	if len(fwd) == 0 && len(xff) == 0 && len(xfp) == 0 {
		return info
	}
	info.ViaTrustedProxy = true

	if hops := parseForwarded(fwd); len(hops) > 0 {
		addr, proto := d.client(info.Addr, hops)
		info.Addr = addr
		if proto != "" {
			info.Scheme = proto
		}
		return info
	}

	info.Addr, _ = d.client(info.Addr, parseXForwardedFor(xff))
	if proto := lastProto(xfp); proto != "" {
		info.Scheme = proto
	}
	return info
}

// hop is one forwarded-for entry, ordered client first.
type hop struct {
	addr  netip.Addr // invalid when the node is unknown, obfuscated, or malformed
	proto string     // "http", "https", or "" when absent or unrecognized
}

// client walks hops from the right (the entry the trusted peer appended),
// skipping trusted proxies, and returns the first untrusted address with the
// proto recorded alongside it. An unidentifiable hop ends the walk at the
// nearest identified hop, since nothing to its left can be attributed. When
// every hop is trusted, the leftmost one is the client.
func (d deriver) client(peer netip.Addr, hops []hop) (netip.Addr, string) {
	client, proto := peer, ""
	for i := len(hops) - 1; i >= 0; i-- {
		h := hops[i]
		proto = h.proto
		if !h.addr.IsValid() {
			break
		}
		client = h.addr
		if !d.isTrusted(client) {
			break
		}
	}
	return client, proto
}

func peerAddr(remote string) netip.Addr {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return normalize(ap.Addr())
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return normalize(a)
	}
	return netip.Addr{}
}

// normalize unmaps IPv4-mapped IPv6 addresses and drops zones so one client
// has one address for throttling and audit.
func normalize(a netip.Addr) netip.Addr {
	return a.Unmap().WithZone("")
}

func parseXForwardedFor(values []string) []hop {
	var hops []hop
	for _, v := range values {
		for _, e := range strings.Split(v, ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			var h hop
			if a, err := netip.ParseAddr(e); err == nil {
				h.addr = normalize(a)
			} else if ap, err := netip.ParseAddrPort(e); err == nil {
				h.addr = normalize(ap.Addr())
			}
			hops = append(hops, h)
		}
	}
	return hops
}

// lastProto returns the rightmost recognized X-Forwarded-Proto value, the one
// set by the trusted peer itself.
func lastProto(values []string) string {
	var parts []string
	for _, v := range values {
		parts = append(parts, strings.Split(v, ",")...)
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			return normProto(p)
		}
	}
	return ""
}

func normProto(p string) string {
	switch strings.ToLower(p) {
	case "https":
		return "https"
	case "http":
		return "http"
	}
	return ""
}
