package clientip

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// ParsePrefix parses one trusted-proxy entry: a CIDR prefix such as
// "10.0.0.0/8", or a bare address meaning that single host (/32 or /128).
// Prefixes with host bits set, IPv6 zones, and IPv4-mapped IPv6 forms are
// rejected so every entry has exactly one spelling.
func ParsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a CIDR prefix or IP address", s)
		}
		if pfx.Addr().Is4In6() {
			return netip.Prefix{}, fmt.Errorf("%q is an IPv4-mapped IPv6 prefix; write the IPv4 form", s)
		}
		if masked := pfx.Masked(); masked != pfx {
			return netip.Prefix{}, fmt.Errorf("%q has host bits set; write %q", s, masked.String())
		}
		return pfx, nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a CIDR prefix or IP address", s)
	}
	if addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("%q has an IPv6 zone; zones are not supported", s)
	}
	if addr.Is4In6() {
		return netip.Prefix{}, fmt.Errorf("%q is an IPv4-mapped IPv6 address; write the IPv4 form", s)
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// ParseTrusted parses the configured trusted-proxy list (server.trusted_proxies)
// with ParsePrefix and reports every invalid entry by index.
func ParseTrusted(entries []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(entries))
	var errs []error
	for i, s := range entries {
		pfx, err := ParsePrefix(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("trusted_proxies[%d]: %w", i, err))
			continue
		}
		out = append(out, pfx)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}
