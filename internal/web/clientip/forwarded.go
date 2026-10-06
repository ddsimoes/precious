package clientip

import (
	"net/netip"
	"strings"
)

// parseForwarded parses RFC 7239 Forwarded header lines into hops, one per
// element, in header order. Empty list elements are skipped. A malformed
// element yields a hop without an address, which stops the client walk there.
func parseForwarded(values []string) []hop {
	var hops []hop
	for _, v := range values {
		for _, elem := range splitUnquoted(v, ',') {
			if strings.TrimSpace(elem) == "" {
				continue
			}
			hops = append(hops, parseForwardedElement(elem))
		}
	}
	return hops
}

func parseForwardedElement(elem string) hop {
	var h hop
	seenFor := false
	for _, pair := range splitUnquoted(elem, ';') {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, raw, ok := strings.Cut(pair, "=")
		if !ok {
			return hop{}
		}
		value, ok := unquote(strings.TrimSpace(raw))
		if !ok {
			return hop{}
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "for":
			if seenFor {
				return hop{}
			}
			seenFor = true
			h.addr = parseNode(value)
		case "proto":
			h.proto = normProto(value)
		}
	}
	return h
}

// splitUnquoted splits s at sep outside quoted strings. An unterminated
// quoted string runs to the end of s, so its piece fails to unquote.
func splitUnquoted(s string, sep byte) []string {
	var parts []string
	start, inQuote, escaped := 0, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case inQuote && c == '\\':
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == sep:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// unquote returns a token unchanged or the content of an RFC 7230
// quoted-string, and false for anything malformed.
func unquote(v string) (string, bool) {
	if !strings.HasPrefix(v, `"`) {
		return v, v != "" && !strings.ContainsAny(v, "\"\\ \t")
	}
	if len(v) < 2 || !strings.HasSuffix(v, `"`) {
		return "", false
	}
	var b strings.Builder
	inner := v[1 : len(v)-1]
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch c {
		case '\\':
			i++
			if i == len(inner) {
				return "", false
			}
			b.WriteByte(inner[i])
		case '"':
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// parseNode parses an RFC 7239 node: an IPv4 address or a bracketed IPv6
// address, each with an optional port. "unknown" and obfuscated identifiers
// yield an invalid address.
func parseNode(v string) netip.Addr {
	var host, port string
	var hasPort bool
	if rest, ok := strings.CutPrefix(v, "["); ok {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return netip.Addr{}
		}
		host = rest[:end]
		after := rest[end+1:]
		if after != "" {
			port, hasPort = strings.CutPrefix(after, ":")
			if !hasPort {
				return netip.Addr{}
			}
		}
		a, err := netip.ParseAddr(host)
		if err != nil || !a.Is6() || (hasPort && port == "") {
			return netip.Addr{}
		}
		return normalize(a)
	}
	host, port, hasPort = strings.Cut(v, ":")
	a, err := netip.ParseAddr(host)
	if err != nil || !a.Is4() || (hasPort && port == "") {
		return netip.Addr{}
	}
	return a
}
