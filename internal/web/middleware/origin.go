package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Origin is the normalized configured external origin: the exact origin
// browsers use, which determines the expected Origin header and the cookie's
// security attributes. Request headers never change it.
type Origin struct {
	scheme string
	host   string // lowercase host, with the port only when it is not the default
}

// ParseOrigin parses an origin such as https://precious.example.net or
// http://127.0.0.1:8080. Paths other than "/", queries, fragments, and user
// info are refused.
func ParseOrigin(s string) (Origin, error) {
	o, err := parseOrigin(s)
	if err != nil {
		return Origin{}, fmt.Errorf("external origin %q: %w", s, err)
	}
	if o.scheme != "https" && o.scheme != "http" {
		return Origin{}, fmt.Errorf("external origin %q: scheme must be https or http", s)
	}
	return o, nil
}

var errNotOrigin = errors.New("not an origin")

func parseOrigin(s string) (Origin, error) {
	u, err := url.Parse(s)
	if err != nil {
		return Origin{}, err
	}
	if u.Scheme == "" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return Origin{}, errNotOrigin
	}
	return originOf(u)
}

// originOf returns the normalized origin of an absolute URL.
func originOf(u *url.URL) (Origin, error) {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return Origin{}, errNotOrigin
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return Origin{scheme: scheme, host: host}, nil
}

// String returns the serialized origin, e.g. https://precious.example.net.
func (o Origin) String() string { return o.scheme + "://" + o.host }

// HTTPS reports whether browsers reach precious over HTTPS.
func (o Origin) HTTPS() bool { return o.scheme == "https" }

// matchesHeader reports whether an Origin header value names o.
func (o Origin) matchesHeader(v string) bool {
	got, err := parseOrigin(v)
	return err == nil && got == o
}

// matchesReferer reports whether a Referer URL belongs to o.
func (o Origin) matchesReferer(v string) bool {
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return false
	}
	got, err := originOf(u)
	return err == nil && got == o
}

// Session cookie names. Browsers accept a __Host- cookie only when it is
// Secure, has Path=/, and has no Domain, which pins it to the exact host.
const (
	CookieNameHTTPS = "__Host-precious_session"
	CookieNameLocal = "precious_session"
)

// SessionCookie is the session cookie policy derived from the external origin.
type SessionCookie struct {
	Name   string
	Secure bool
}

// NewSessionCookie returns the policy for o: a Secure __Host- cookie for an
// HTTPS origin, and a plain cookie for explicit local HTTP mode.
func NewSessionCookie(o Origin) SessionCookie {
	if o.HTTPS() {
		return SessionCookie{Name: CookieNameHTTPS, Secure: true}
	}
	return SessionCookie{Name: CookieNameLocal}
}

// Set sends the session token. The cookie lasts for the browser session; the
// server enforces idle and absolute expiry.
func (c SessionCookie) Set(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.Name,
		Value:    token,
		Path:     "/",
		Secure:   c.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// Clear deletes the cookie in the browser.
func (c SessionCookie) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.Name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   c.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// Token returns the cookie value of r, or "" when absent.
func (c SessionCookie) Token(r *http.Request) string {
	ck, err := r.Cookie(c.Name)
	if err != nil {
		return ""
	}
	return ck.Value
}
