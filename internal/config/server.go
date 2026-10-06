package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"precious/internal/web/clientip"
)

func validateServer(s *Server, p *Problems) {
	validateListen(s, p)
	validateExternalOrigin(s, p)
	for i, raw := range s.TrustedProxies {
		if _, err := clientip.ParsePrefix(raw); err != nil {
			p.addf(fmt.Sprintf("server.trusted_proxies[%d]", i), "%v", err)
		}
	}
	if s.MaxRequestBytes <= 0 {
		p.addf("server.max_request_bytes", "must be positive, got %d", s.MaxRequestBytes)
	}
}

func validateListen(s *Server, p *Problems) {
	host, port, err := net.SplitHostPort(s.Listen)
	if err != nil {
		p.addf("server.listen", "%q is not host:port, such as \"127.0.0.1:8080\"", s.Listen)
		return
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		p.addf("server.listen", "%q has an invalid port; use a number from 1 to 65535", s.Listen)
	}
	if isLoopbackHost(host) || s.AllowNonLoopbackListen {
		return
	}
	where := fmt.Sprintf("host %q", host)
	if host == "" {
		where = "all interfaces"
	}
	p.addf("server.listen", "%q listens on %s, which is not a loopback address. precious listens on loopback by default. To listen on a non-loopback address, for example 0.0.0.0 inside a container or to serve a local network, opt in with server.allow_non_loopback_listen = true", s.Listen, where)
}

func validateExternalOrigin(s *Server, p *Problems) {
	const key = "server.external_origin"
	if s.ExternalOrigin == "" {
		p.addf(key, "required; set the exact origin browsers use, such as \"https://precious.example.net\" (or \"http://127.0.0.1:8080\" with server.allow_insecure_http = true)")
		return
	}
	o, err := parseOrigin(s.ExternalOrigin)
	if err != nil {
		p.addf(key, "%q is not a valid origin: %v", s.ExternalOrigin, err)
		return
	}
	if o.scheme == "http" && !s.AllowInsecureHTTP {
		p.addf(key, "%q uses plain HTTP, which needs server.allow_insecure_http = true; otherwise put precious behind an HTTPS reverse proxy and use an https:// origin", s.ExternalOrigin)
	}
}

// isLoopbackHost reports whether host is "localhost" or a loopback address
// (127.0.0.0/8, ::1, or their IPv4-mapped forms).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}

// origin is a parsed scheme://host[:port] with the port omitted when it is
// the scheme's default.
type origin struct {
	scheme, host, port string
}

func (o origin) String() string {
	if o.port != "" {
		return o.scheme + "://" + net.JoinHostPort(o.host, o.port)
	}
	if strings.Contains(o.host, ":") {
		return o.scheme + "://[" + o.host + "]"
	}
	return o.scheme + "://" + o.host
}

func parseOrigin(raw string) (origin, error) {
	u, err := url.Parse(raw)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return origin{}, err
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return origin{}, errors.New("the scheme must be https (or http in explicit local mode)")
	case u.Opaque != "" || u.Host == "":
		return origin{}, errors.New("it must have the form scheme://host[:port]")
	case u.User != nil:
		return origin{}, errors.New("it must not contain user information")
	case u.Path != "" && u.Path != "/":
		return origin{}, errors.New("it must not have a path")
	case u.RawQuery != "" || u.ForceQuery:
		return origin{}, errors.New("it must not have a query")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return origin{}, errors.New("it must not have a fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return origin{}, errors.New("it must name a host")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return origin{}, fmt.Errorf("port %q is invalid; use a number from 1 to 65535", port)
		}
		port = strconv.FormatUint(n, 10)
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	return origin{scheme: u.Scheme, host: host, port: port}, nil
}
