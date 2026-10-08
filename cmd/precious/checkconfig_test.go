package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"precious/internal/config"
)

const shippedExampleConfig = "../../deploy/examples/precious.toml"

func runCheckConfigCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), env{stdout: &out, stderr: &errOut}, args)
	return code, out.String(), errOut.String()
}

func writeCheckConfigFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// checkEffective runs check-config on path, requires success, and checks
// that the printed settings contain every string of wantText and load back
// to the configuration path loads to. Standard error must be empty, or only
// the unset-zone warning when the file sets no dates.time_zone.
func checkEffective(t *testing.T, path string, wantText ...string) {
	t.Helper()
	want, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCheckConfigCmd(t, "check-config", "--config", path)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	wantStderr := ""
	if want.Dates.TimeZone == "" {
		wantStderr = "precious check-config: warning: " + unsetZoneWarning + "\n"
	}
	if stderr != wantStderr {
		t.Errorf("stderr:\n%s\nwant:\n%s", stderr, wantStderr)
	}
	for _, s := range wantText {
		if !strings.Contains(stdout, s) {
			t.Errorf("output lacks %q:\n%s", s, stdout)
		}
	}
	got, err := config.Load(writeCheckConfigFile(t, t.TempDir(), "effective.toml", stdout))
	if err != nil {
		t.Fatalf("effective output does not load: %v\n%s", err, stdout)
	}
	// Empty lists are printed explicitly; the defaults are nil.
	if want.Server.TrustedProxies == nil {
		want.Server.TrustedProxies = []string{}
	}
	if want.Sources.AllowedRoots == nil {
		want.Sources.AllowedRoots = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective output loads as %+v, want %+v", got, want)
	}
}

// server-config "Valid configuration accepted", "Scan defaults", "Hashing
// defaults printed", "Archive defaults printed", and "Writes can be forbidden
// by the configuration": check-config exits 0 on the shipped example and
// prints effective settings, the [scan], [hashing], [archives], and
// [duplicates] defaults, the empty allowed roots, allow_writes, and the
// example's time zone, without a warning, that load back to the same
// configuration.
func TestCheckConfigAcceptsShippedExample(t *testing.T) {
	checkEffective(t, shippedExampleConfig,
		`state_dir = "/var/lib/precious"`, "trusted_proxies = []",
		"[sources]\n  allowed_roots = []\n  allow_writes = true", "picker offers the platform default roots",
		"[scan]\n  batch_size = 1000\n  list_batch = 256",
		"[hashing]\n  read_chunk_bytes = 1048576\n  yield_bytes = 67108864",
		"[archives]\n  max_members = 1000000\n  max_unpacked_bytes = 1099511627776\n  max_ratio = 100\n"+
			"  max_time = \"4h0m0s\"\n  view_max_bytes = 67108864",
		"[duplicates]\n  refresh_interval = \"10m0s\"",
		"# Media dates are read in America/Sao_Paulo (-03, UTC-03:00).\n",
		"[dates]\n  time_zone = \"America/Sao_Paulo\"")
}

// server-config "An unset zone": with no [dates] section check-config
// succeeds, prints the server's local zone with its abbreviation and offset
// (time.Local, whatever the host's zone is), and warns on standard error that
// dates.time_zone is unset.
func TestCheckConfigUnsetTimeZone(t *testing.T) {
	base := t.TempDir()
	path := writeCheckConfigFile(t, base, "precious.toml", `state_dir = "`+base+`/state"
[server]
external_origin = "https://precious.example.net"
`)
	checkEffective(t, path, "# dates.time_zone is unset: media dates are read in the server's local zone (",
		"[dates]\n  time_zone = \"\"")
	if !strings.Contains(unsetZoneWarning, "dates.time_zone is unset") {
		t.Errorf("warning %q does not name dates.time_zone", unsetZoneWarning)
	}
	// The described zone is time.Local's, at the time of the call.
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if got, want := describeZone(time.Local, at), "the server's local zone ("+at.In(time.Local).Format("MST, UTC-07:00")+")"; got != want {
		t.Errorf("describeZone(Local) = %q, want %q", got, want)
	}
	if got := describeZone(time.UTC, at); got != "UTC (UTC, UTC+00:00)" {
		t.Errorf("describeZone(UTC) = %q", got)
	}
}

// server-config "An unknown zone": check-config fails naming dates.time_zone,
// and serve does not start, with the same message and no state directory.
func TestUnknownTimeZoneRefused(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	path := writeCheckConfigFile(t, base, "precious.toml", `state_dir = "`+stateDir+`"
[server]
external_origin = "https://precious.example.net"
[dates]
time_zone = "Mars/Olympus"
`)
	for _, cmd := range []string{"check-config", "serve"} {
		code, stdout, stderr := runCheckConfigCmd(t, cmd, "--config", path)
		if code != 1 || stdout != "" || !strings.Contains(stderr, `dates.time_zone: "Mars/Olympus" is not a known IANA time zone`) {
			t.Errorf("%s: exit %d, stdout %q, stderr:\n%s", cmd, code, stdout, stderr)
		}
	}
	if _, err := os.Lstat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("state directory exists after the refusal (err=%v)", err)
	}
}

// check-config prints configured allowed roots, cleaned, forbidden writes,
// and changed scan settings.
func TestCheckConfigPrintsSourcesAndScan(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeCheckConfigFile(t, base, "precious.toml", `state_dir = "`+base+`/state"
[server]
external_origin = "https://precious.example.net"
[sources]
allowed_roots = ["`+base+`/media/"]
allow_writes = false
[scan]
batch_size = 500
`)
	checkEffective(t, path, `allowed_roots = ["`+base+`/media"]`, "allow_writes = false", "batch_size = 500", "list_batch = 256")
	if _, stdout, _ := runCheckConfigCmd(t, "check-config", "--config", path); strings.Contains(stdout, "platform default roots") {
		t.Errorf("output mentions the default roots although roots are configured:\n%s", stdout)
	}
}

func TestCheckConfigRejects(t *testing.T) {
	tests := []struct {
		name  string
		body  func(base string) string
		names []string // every one must appear on stderr
	}{
		// server-config "Unknown key rejected".
		{"unknown key", func(base string) string {
			return `state_dir = "` + base + `/state"
sorces = []
[server]
external_origin = "https://precious.example.net"
`
		}, []string{"sorces"}},
		// server-config "v0.2 configuration refused".
		{"v0.2 configuration", func(base string) string {
			return `state_dir = "` + base + `/state"
[server]
external_origin = "https://precious.example.net"
[inbox]
poll_interval = "5s"
[copies]
large_file_bytes = 1048576
[[sources]]
id = "old-disk"
label = "Old disk"
root = "/srv/sources/old-disk"
mode = "read_only"
`
		}, []string{"unknown keys", "copies.large_file_bytes", "inbox.poll_interval", "sources.id", "sources.root"}},
		// server-config "Missing root rejected".
		{"missing allowed root", func(base string) string {
			return `state_dir = "` + base + `/state"
[server]
external_origin = "https://precious.example.net"
[sources]
allowed_roots = ["` + base + `/tank"]
`
		}, []string{"sources.allowed_roots", "/tank"}},
		// server-config "Batch size out of range".
		{"scan batch size", func(base string) string {
			return `state_dir = "` + base + `/state"
[server]
external_origin = "https://precious.example.net"
[scan]
batch_size = 0
`
		}, []string{"scan.batch_size"}},
		// server-config "Chunk out of range" and "Ratio out of range".
		{"hashing chunk and archive ratio", func(base string) string {
			return `state_dir = "` + base + `/state"
[server]
external_origin = "https://precious.example.net"
[hashing]
read_chunk_bytes = 4096
[archives]
max_ratio = 1
`
		}, []string{"hashing.read_chunk_bytes", "archives.max_ratio"}},
		// Every problem is printed, not only the first.
		{"several problems", func(string) string {
			return `state_dir = "relative"
[server]
listen = "0.0.0.0:8080"
external_origin = "http://precious.example.net"
[scan]
list_batch = 5000
`
		}, []string{"state_dir", "server.listen", "server.external_origin", "scan.list_batch"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			code, stdout, stderr := runCheckConfigCmd(t, "check-config", "--config", writeCheckConfigFile(t, base, "precious.toml", tc.body(base)))
			if code != 1 {
				t.Fatalf("exit %d, want 1; stderr:\n%s", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout not empty:\n%s", stdout)
			}
			for _, s := range tc.names {
				if !strings.Contains(stderr, s) {
					t.Errorf("stderr does not name %s:\n%s", s, stderr)
				}
			}
		})
	}
}

// An invalid configuration creates nothing: check-config and the startup
// path share loadConfig, which runs before openState.
func TestInvalidConfigCreatesNothing(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	path := writeCheckConfigFile(t, base, "precious.toml", `state_dir = "`+stateDir+`"
[server]
external_origin = "https://precious.example.net"
[sources]
allowed_roots = ["relative"]
`)
	code, _, stderr := runCheckConfigCmd(t, "check-config", "--config", path)
	if code != 1 || !strings.Contains(stderr, "sources.allowed_roots") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	var errOut bytes.Buffer
	if _, ok := loadConfig(&errOut, path); ok {
		t.Fatal("loadConfig accepted the configuration")
	}
	if _, err := os.Lstat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("state directory exists after the refusal (err=%v)", err)
	}
}

func TestCheckConfigUsageErrors(t *testing.T) {
	if code, _, stderr := runCheckConfigCmd(t, "check-config"); code != 1 || !strings.Contains(stderr, "--config is required") {
		t.Errorf("missing --config: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCheckConfigCmd(t, "check-config", "--config", shippedExampleConfig, "extra"); code != 2 || !strings.Contains(stderr, `unexpected argument "extra"`) {
		t.Errorf("extra argument: exit %d, stderr %q", code, stderr)
	}
	if code, _, _ := runCheckConfigCmd(t, "check-config", "--bogus"); code != 2 {
		t.Errorf("unknown flag: exit %d, want 2", code)
	}
	if code, _, _ := runCheckConfigCmd(t, "check-config", "--config", filepath.Join(t.TempDir(), "missing.toml")); code != 1 {
		t.Errorf("missing file: exit %d, want 1", code)
	}
}
