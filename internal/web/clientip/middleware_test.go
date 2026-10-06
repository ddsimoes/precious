package clientip

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// serve runs one request from peer through Middleware and returns the Info the
// handler saw.
func serve(t *testing.T, trusted []string, peer string, header http.Header, useTLS bool) Info {
	t.Helper()
	prefixes, err := ParseTrusted(trusted)
	if err != nil {
		t.Fatal(err)
	}
	var got Info
	var ok bool
	h := Middleware(prefixes)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = From(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if useTLS {
		req.TLS = &tls.ConnectionState{}
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !ok {
		t.Fatal("handler saw no clientip.Info")
	}
	return got
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// TestA15ForgedForwardedHeadersIgnored covers server-config "A15 forged
// forwarded headers ignored": a peer outside the trusted-proxy list claims to
// be 127.0.0.1 over HTTPS. Throttling and audit read Info.Addr, so they see the
// real peer; the scheme stays the connection's. The expected origin comes only
// from configuration (config.Server.Origin) and no request input reaches it.
func TestA15ForgedForwardedHeadersIgnored(t *testing.T) {
	forged := http.Header{
		"X-Forwarded-For":   {"127.0.0.1"},
		"X-Forwarded-Proto": {"https"},
		"Forwarded":         {"for=127.0.0.1;proto=https"},
	}
	for _, tc := range []struct {
		name    string
		trusted []string
	}{
		{"no trusted proxies", nil},
		{"peer outside the trusted list", []string{"10.0.0.0/8", "127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := serve(t, tc.trusted, "198.51.100.9:40000", forged, false)
			want := Info{Addr: addr("198.51.100.9"), Scheme: "http"}
			if got != want {
				t.Fatalf("Info = %+v, want %+v", got, want)
			}
		})
	}
}

// TestTrustedProxyHonored covers server-config "Trusted proxy honored": a
// request from a trusted proxy with X-Forwarded-For: 203.0.113.7 is attributed
// to 203.0.113.7.
func TestTrustedProxyHonored(t *testing.T) {
	got := serve(t, []string{"10.0.0.0/8"}, "10.0.0.2:51000", http.Header{
		"X-Forwarded-For":   {"203.0.113.7"},
		"X-Forwarded-Proto": {"https"},
	}, false)
	want := Info{Addr: addr("203.0.113.7"), Scheme: "https", ViaTrustedProxy: true}
	if got != want {
		t.Fatalf("Info = %+v, want %+v", got, want)
	}
}

func TestDerive(t *testing.T) {
	proxies := []string{"10.0.0.0/8", "::1", "127.0.0.1"}
	tests := []struct {
		name   string
		peer   string
		header http.Header
		tls    bool
		want   Info
	}{
		{name: "direct client over plain HTTP", peer: "192.0.2.1:1234",
			want: Info{Addr: addr("192.0.2.1"), Scheme: "http"}},
		{name: "direct client over TLS", peer: "192.0.2.1:1234", tls: true,
			want: Info{Addr: addr("192.0.2.1"), Scheme: "https"}},
		{name: "trusted peer without forwarded headers", peer: "10.1.2.3:80",
			want: Info{Addr: addr("10.1.2.3"), Scheme: "http"}},
		{name: "ipv4-mapped peer matches an IPv4 prefix", peer: "[::ffff:10.0.0.2]:80",
			header: http.Header{"X-Forwarded-For": {"203.0.113.7"}},
			want:   Info{Addr: addr("203.0.113.7"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "ipv6 loopback proxy", peer: "[::1]:5555",
			header: http.Header{"X-Forwarded-For": {"2001:db8::7"}},
			want:   Info{Addr: addr("2001:db8::7"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "unparseable peer is never trusted", peer: "@",
			header: http.Header{"X-Forwarded-For": {"203.0.113.7"}},
			want:   Info{Scheme: "http"}},

		{name: "client-injected entries left of the client are ignored", peer: "10.0.0.2:1",
			header: http.Header{"X-Forwarded-For": {"127.0.0.1, 198.51.100.1, 203.0.113.7, 10.0.0.3"}},
			want:   Info{Addr: addr("203.0.113.7"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "X-Forwarded-For across header lines", peer: "10.0.0.2:1",
			header: http.Header{"X-Forwarded-For": {"198.51.100.1", "203.0.113.7, 10.0.0.3"}},
			want:   Info{Addr: addr("203.0.113.7"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "every hop trusted gives the leftmost", peer: "10.0.0.2:1",
			header: http.Header{"X-Forwarded-For": {"10.9.9.9, 10.0.0.3"}},
			want:   Info{Addr: addr("10.9.9.9"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "garbage hop stops at the nearest proxy", peer: "10.0.0.2:1",
			header: http.Header{"X-Forwarded-For": {"203.0.113.7, not-an-ip, 10.0.0.3"}},
			want:   Info{Addr: addr("10.0.0.3"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "X-Forwarded-For with port", peer: "10.0.0.2:1",
			header: http.Header{"X-Forwarded-For": {"203.0.113.7:4711"}},
			want:   Info{Addr: addr("203.0.113.7"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "empty X-Forwarded-For keeps the peer", peer: "10.0.0.2:1",
			header: http.Header{"X-Forwarded-For": {""}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "X-Forwarded-Proto uses the value the proxy appended", peer: "10.0.0.2:1",
			header: http.Header{"X-Forwarded-Proto": {"https, http"}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "unknown X-Forwarded-Proto ignored", peer: "10.0.0.2:1", tls: true,
			header: http.Header{"X-Forwarded-Proto": {"gopher"}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "https", ViaTrustedProxy: true}},

		{name: "Forwarded preferred over X-Forwarded-For", peer: "10.0.0.2:1",
			header: http.Header{
				"Forwarded":         {`for=198.51.100.20;proto=https`},
				"X-Forwarded-For":   {"203.0.113.7"},
				"X-Forwarded-Proto": {"http"},
			},
			want: Info{Addr: addr("198.51.100.20"), Scheme: "https", ViaTrustedProxy: true}},
		{name: "Forwarded quoted IPv6 with port", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`For="[2001:db8:cafe::17]:4711";Proto=HTTPS`}},
			want:   Info{Addr: addr("2001:db8:cafe::17"), Scheme: "https", ViaTrustedProxy: true}},
		{name: "Forwarded chain uses the client's element", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`for=127.0.0.1;proto=https, for=203.0.113.7;proto=https`, `for=10.0.0.3;proto=http`}},
			want:   Info{Addr: addr("203.0.113.7"), Scheme: "https", ViaTrustedProxy: true}},
		{name: "Forwarded unknown node stays with the proxy", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`for=unknown;proto=https`}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "https", ViaTrustedProxy: true}},
		{name: "Forwarded obfuscated node stays with the proxy", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`for=203.0.113.7, for=_hidden`}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "Forwarded quoted comma does not split elements", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`for=198.51.100.1, for=10.0.0.3;by="a,for=203.0.113.9"`}},
			want:   Info{Addr: addr("198.51.100.1"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "Forwarded malformed element stops the walk", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`for=203.0.113.7, for="198.51.100.1`}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "Forwarded duplicate for is malformed", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`for=203.0.113.7;for=198.51.100.1`}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "Forwarded unbracketed IPv6 is malformed", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`for="2001:db8::17"`}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "Forwarded with only proto", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {`proto=https`}},
			want:   Info{Addr: addr("10.0.0.2"), Scheme: "https", ViaTrustedProxy: true}},
		{name: "empty Forwarded falls back to X-Forwarded-For", peer: "10.0.0.2:1",
			header: http.Header{"Forwarded": {" , "}, "X-Forwarded-For": {"203.0.113.7"}},
			want:   Info{Addr: addr("203.0.113.7"), Scheme: "http", ViaTrustedProxy: true}},
		{name: "Forwarded from an untrusted peer ignored", peer: "192.0.2.1:1",
			header: http.Header{"Forwarded": {`for=10.0.0.3;proto=https`}},
			want:   Info{Addr: addr("192.0.2.1"), Scheme: "http"}},
		{name: "a single trusted host does not trust its neighbours", peer: "127.0.0.2:1",
			header: http.Header{"X-Forwarded-For": {"203.0.113.7"}},
			want:   Info{Addr: addr("127.0.0.2"), Scheme: "http"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := serve(t, proxies, tc.peer, tc.header, tc.tls); got != tc.want {
				t.Fatalf("Info = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMiddlewareCopiesTrustedList(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	h := Middleware(prefixes)
	prefixes[0] = netip.MustParsePrefix("192.0.2.0/24")
	var got Info
	srv := h(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, _ = From(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	srv.ServeHTTP(httptest.NewRecorder(), req)
	if got.Addr != addr("192.0.2.1") || got.ViaTrustedProxy {
		t.Fatalf("later change to the caller's slice took effect: %+v", got)
	}
}

func TestParseTrusted(t *testing.T) {
	got, err := ParseTrusted([]string{"127.0.0.1", "::1", "10.0.0.0/8", "fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("fd00::/8"),
	}
	if len(got) != len(want) {
		t.Fatalf("ParseTrusted = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
		}
	}

	_, err = ParseTrusted([]string{"10.0.0.0/8", "proxy.lan", "10.0.0.1/8", "fe80::1%eth0", "::ffff:10.0.0.1", "::ffff:10.0.0.0/104", "10.0.0.0/33"})
	if err == nil {
		t.Fatal("ParseTrusted accepted invalid entries")
	}
	for _, s := range []string{
		`trusted_proxies[1]: "proxy.lan" is not a CIDR prefix or IP address`,
		`trusted_proxies[2]: "10.0.0.1/8" has host bits set; write "10.0.0.0/8"`,
		`trusted_proxies[3]: "fe80::1%eth0" has an IPv6 zone`,
		`trusted_proxies[4]: "::ffff:10.0.0.1" is an IPv4-mapped IPv6 address`,
		`trusted_proxies[5]: "::ffff:10.0.0.0/104" is an IPv4-mapped IPv6 prefix`,
		`trusted_proxies[6]: "10.0.0.0/33" is not a CIDR prefix`,
	} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error does not contain %q:\n%v", s, err)
		}
	}
	if strings.Contains(err.Error(), "trusted_proxies[0]") {
		t.Errorf("valid entry reported: %v", err)
	}
}
