package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// validConfig returns a configuration that passes Validate, with a state
// directory and one allowed root under a fresh temporary directory, which it
// returns too.
func validConfig(t *testing.T) (Config, string) {
	t.Helper()
	base := t.TempDir()
	c := Defaults()
	c.StateDir = filepath.Join(base, "state")
	c.Server.ExternalOrigin = "https://precious.example.net"
	c.Sources.AllowedRoots = []string{filepath.Join(base, "media")}
	if err := os.Mkdir(c.Sources.AllowedRoots[0], 0o755); err != nil {
		t.Fatal(err)
	}
	return c, base
}

// want is one expected problem: the key it must name and text it must contain.
type want struct{ key, text string }

func checkProblems(t *testing.T, err error, wants []want) {
	t.Helper()
	if len(wants) == 0 {
		if err != nil {
			t.Fatalf("Validate() = %v, want no problems", err)
		}
		return
	}
	p, ok := AsProblems(err)
	if !ok {
		t.Fatalf("Validate() = %v, want Problems", err)
	}
	if len(p) != len(wants) {
		t.Fatalf("got %d problems, want %d:\n%v", len(p), len(wants), err)
	}
	for i, w := range wants {
		if !strings.HasPrefix(p[i], w.key+": ") {
			t.Errorf("problem %d = %q, want key %q", i, p[i], w.key)
		}
		if !strings.Contains(p[i], w.text) {
			t.Errorf("problem %d = %q, want text %q", i, p[i], w.text)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, c *Config, base string)
		want   []want
	}{
		// server-config "Valid configuration accepted" (validation half; the
		// check-config output is tested in cmd/precious).
		{"valid", func(*testing.T, *Config, string) {}, nil},

		{"state_dir required", func(_ *testing.T, c *Config, _ string) { c.StateDir = "" },
			[]want{{"state_dir", "required"}}},
		{"state_dir relative", func(_ *testing.T, c *Config, _ string) { c.StateDir = "var/lib/precious" },
			[]want{{"state_dir", "must be an absolute path"}}},

		{"jobs zero", func(_ *testing.T, c *Config, _ string) { c.Jobs = Jobs{} }, []want{
			{"jobs.workers_per_device", "at least 1"},
			{"jobs.max_attempts", "at least 1"},
			{"jobs.event_retention_rows", "at least 1"},
			{"jobs.lease", "must be positive"},
			{"jobs.lease_renew", "must be positive"},
			{"jobs.call_watchdog", "must be positive"},
			{"jobs.event_retention_age", "must be positive"},
		}},
		{"lease renew equals lease", func(_ *testing.T, c *Config, _ string) { c.Jobs.LeaseRenew = c.Jobs.Lease },
			[]want{{"jobs.lease_renew", "must be shorter than jobs.lease (30s)"}}},

		// server-config "Scan settings are validated".
		{"batch size out of range", func(_ *testing.T, c *Config, _ string) { c.Scan.BatchSize = 0 },
			[]want{{"scan.batch_size", "must be between 1 and 10000, got 0"}}},
		{"batch size above bound", func(_ *testing.T, c *Config, _ string) { c.Scan.BatchSize = 10_001 },
			[]want{{"scan.batch_size", "got 10001"}}},
		{"list batch out of range", func(_ *testing.T, c *Config, _ string) { c.Scan.ListBatch = 5000 },
			[]want{{"scan.list_batch", "must be between 1 and 4096, got 5000"}}},
		{"list batch zero", func(_ *testing.T, c *Config, _ string) { c.Scan.ListBatch = 0 },
			[]want{{"scan.list_batch", "got 0"}}},
		{"scan at lower bounds", func(_ *testing.T, c *Config, _ string) { c.Scan = Scan{BatchSize: 1, ListBatch: 1} }, nil},
		{"scan at upper bounds", func(_ *testing.T, c *Config, _ string) { c.Scan = Scan{BatchSize: 10_000, ListBatch: 4096} }, nil},

		{"auth zero", func(_ *testing.T, c *Config, _ string) { c.Auth = Auth{} }, []want{
			{"auth.session_idle", "must be positive"},
			{"auth.session_absolute", "must be positive"},
			{"auth.login_max_backoff", "must be positive"},
		}},
		{"absolute shorter than idle", func(_ *testing.T, c *Config, _ string) {
			c.Auth.SessionAbsolute = Duration{30 * time.Minute}
		}, []want{{"auth.session_absolute", "at least auth.session_idle (1h0m0s)"}}},
		{"absolute equals idle", func(_ *testing.T, c *Config, _ string) { c.Auth.SessionAbsolute = c.Auth.SessionIdle }, nil},

		{"max request bytes", func(_ *testing.T, c *Config, _ string) { c.Server.MaxRequestBytes = 0 },
			[]want{{"server.max_request_bytes", "must be positive"}}},

		{"every problem reported at once", func(_ *testing.T, c *Config, _ string) {
			c.StateDir = ""
			c.Server.Listen = "0.0.0.0:8080"
			c.Sources.AllowedRoots = []string{"mnt/disks"}
			c.Scan.BatchSize = 0
		}, []want{
			{"state_dir", "required"},
			{"server.listen", "allow_non_loopback_listen"},
			{"sources.allowed_roots", "mnt/disks"},
			{"scan.batch_size", "between 1 and 10000"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, base := validConfig(t)
			tc.mutate(t, &c, base)
			checkProblems(t, Validate(&c), tc.want)
		})
	}
}

// TestAllowedRoots covers the server-config "Allowed roots are validated"
// requirement: every entry is an absolute path of an existing directory, each
// problem names the entry, and accepted entries are cleaned.
func TestAllowedRoots(t *testing.T) {
	tests := []struct {
		name  string
		roots func(t *testing.T, base string) []string
		want  []want
		clean func(base string) []string // the cleaned roots of a valid list
	}{
		{"defaults when absent", func(*testing.T, string) []string { return nil }, nil,
			func(string) []string { return nil }},
		{"existing directories cleaned", func(t *testing.T, base string) []string {
			mkdirs(t, base, "media", "srv/disks")
			return []string{base + "/media/", base + "/srv/x/../disks"}
		}, nil, func(base string) []string { return []string{base + "/media", base + "/srv/disks"} }},
		{"relative root rejected", func(*testing.T, string) []string { return []string{"mnt/disks"} },
			[]want{{"sources.allowed_roots", `"mnt/disks" must be an absolute directory path`}}, nil},
		{"missing root rejected", func(_ *testing.T, base string) []string { return []string{base + "/tank"} },
			[]want{{"sources.allowed_roots", "/tank\" is not an existing directory"}}, nil},
		{"root that is a file rejected", func(t *testing.T, base string) []string {
			if err := os.WriteFile(base+"/file", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{base + "/file"}
		}, []want{{"sources.allowed_roots", "/file\" exists but is not a directory"}}, nil},
		{"every bad entry named", func(t *testing.T, base string) []string {
			mkdirs(t, base, "media")
			return []string{"rel", base + "/media", base + "/gone"}
		}, []want{
			{"sources.allowed_roots", `"rel"`},
			{"sources.allowed_roots", "/gone\""},
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, base := validConfig(t)
			c.Sources.AllowedRoots = tc.roots(t, base)
			checkProblems(t, Validate(&c), tc.want)
			if tc.clean != nil {
				if got, want := c.Sources.AllowedRoots, tc.clean(base); !slices.Equal(got, want) {
					t.Errorf("allowed roots = %q, want %q", got, want)
				}
			}
		})
	}
}

// TestListenerAndOriginPolicy covers the server-config "Loopback-by-default
// listener" requirement, including the "Non-loopback bind without opt-in",
// "Remote plain HTTP rejected", and "Plain HTTP on a local network"
// scenarios, and trusted-proxy entry parsing.
func TestListenerAndOriginPolicy(t *testing.T) {
	const plainHTTP = "uses plain HTTP, which needs server.allow_insecure_http = true"
	tests := []struct {
		name   string
		server func(s *Server)
		want   []want
	}{
		{"default listen is loopback", func(s *Server) {}, nil},
		{"ipv6 loopback", func(s *Server) { s.Listen = "[::1]:8080" }, nil},
		{"other 127/8 address", func(s *Server) { s.Listen = "127.0.0.2:9000" }, nil},
		{"localhost", func(s *Server) { s.Listen = "localhost:8080" }, nil},
		{"non-loopback bind without opt-in", func(s *Server) { s.Listen = "0.0.0.0:8080" },
			[]want{{"server.listen", "opt in with server.allow_non_loopback_listen = true"}}},
		{"all interfaces without opt-in", func(s *Server) { s.Listen = ":8080" },
			[]want{{"server.listen", "listens on all interfaces"}}},
		{"lan address without opt-in", func(s *Server) { s.Listen = "192.168.1.5:8080" },
			[]want{{"server.listen", "not a loopback address"}}},
		{"hostname without opt-in", func(s *Server) { s.Listen = "precious.lan:8080" },
			[]want{{"server.listen", "not a loopback address"}}},
		{"non-loopback bind with opt-in", func(s *Server) {
			s.Listen = "0.0.0.0:8080"
			s.AllowNonLoopbackListen = true
		}, nil},
		{"missing port", func(s *Server) { s.Listen = "127.0.0.1" },
			[]want{{"server.listen", "is not host:port"}}},
		{"port zero", func(s *Server) { s.Listen = "127.0.0.1:0" },
			[]want{{"server.listen", "invalid port"}}},
		{"port too large", func(s *Server) { s.Listen = "127.0.0.1:65536" },
			[]want{{"server.listen", "invalid port"}}},

		{"origin required", func(s *Server) { s.ExternalOrigin = "" },
			[]want{{"server.external_origin", "required"}}},
		{"origin trailing slash", func(s *Server) { s.ExternalOrigin = "https://precious.example.net/" }, nil},
		{"origin with port", func(s *Server) { s.ExternalOrigin = "https://precious.example.net:8443" }, nil},
		{"origin with path", func(s *Server) { s.ExternalOrigin = "https://precious.example.net/precious" },
			[]want{{"server.external_origin", "must not have a path"}}},
		{"origin with query", func(s *Server) { s.ExternalOrigin = "https://precious.example.net/?a=b" },
			[]want{{"server.external_origin", "must not have a query"}}},
		{"origin with empty query", func(s *Server) { s.ExternalOrigin = "https://precious.example.net?" },
			[]want{{"server.external_origin", "must not have a query"}}},
		{"origin with fragment", func(s *Server) { s.ExternalOrigin = "https://precious.example.net#x" },
			[]want{{"server.external_origin", "must not have a fragment"}}},
		{"origin with userinfo", func(s *Server) { s.ExternalOrigin = "https://admin@precious.example.net" },
			[]want{{"server.external_origin", "must not contain user information"}}},
		{"origin other scheme", func(s *Server) { s.ExternalOrigin = "ftp://precious.example.net" },
			[]want{{"server.external_origin", "the scheme must be https"}}},
		{"origin without host", func(s *Server) { s.ExternalOrigin = "https://" },
			[]want{{"server.external_origin", "scheme://host[:port]"}}},
		{"origin bare host", func(s *Server) { s.ExternalOrigin = "precious.example.net" },
			[]want{{"server.external_origin", "the scheme must be https"}}},
		{"origin bad port", func(s *Server) { s.ExternalOrigin = "https://precious.example.net:99999" },
			[]want{{"server.external_origin", "is not a valid origin"}}},

		{"remote plain HTTP rejected", func(s *Server) { s.ExternalOrigin = "http://precious.lan:8080" },
			[]want{{"server.external_origin", plainHTTP}}},
		{"loopback plain HTTP without opt-in", func(s *Server) { s.ExternalOrigin = "http://127.0.0.1:8080" },
			[]want{{"server.external_origin", plainHTTP}}},
		{"loopback plain HTTP with opt-in", func(s *Server) {
			s.ExternalOrigin = "http://127.0.0.1:8080"
			s.AllowInsecureHTTP = true
		}, nil},
		{"remote plain HTTP with opt-in", func(s *Server) {
			s.ExternalOrigin = "http://precious.lan:8080"
			s.AllowInsecureHTTP = true
		}, nil},
		{"plain HTTP on a local network", func(s *Server) {
			s.Listen = "0.0.0.0:8080"
			s.AllowNonLoopbackListen = true
			s.ExternalOrigin = "http://192.168.1.10:8080"
			s.AllowInsecureHTTP = true
		}, nil},
		{"insecure HTTP opt-in does not open the listener", func(s *Server) {
			s.Listen = "0.0.0.0:8080"
			s.ExternalOrigin = "http://192.168.1.10:8080"
			s.AllowInsecureHTTP = true
		}, []want{{"server.listen", "allow_non_loopback_listen"}}},

		{"trusted proxies valid", func(s *Server) {
			s.TrustedProxies = []string{"127.0.0.1", "::1", "10.0.0.0/8", "fd00::/8"}
		}, nil},
		{"trusted proxies invalid", func(s *Server) {
			s.TrustedProxies = []string{"10.0.0.0/8", "proxy.lan", "10.0.0.1/8"}
		}, []want{
			{"server.trusted_proxies[1]", `"proxy.lan" is not a CIDR prefix or IP address`},
			{"server.trusted_proxies[2]", `has host bits set; write "10.0.0.0/8"`},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := validConfig(t)
			tc.server(&c.Server)
			checkProblems(t, Validate(&c), tc.want)
		})
	}
}

func mkdirs(t *testing.T, base string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		if err := os.MkdirAll(filepath.Join(base, r), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
