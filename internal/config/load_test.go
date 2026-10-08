package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "precious.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// unknownKeys returns the keys a Load error reports as unknown.
func unknownKeys(t *testing.T, err error) []string {
	t.Helper()
	if err == nil {
		t.Fatal("Load accepted unknown keys")
	}
	_, list, ok := strings.Cut(err.Error(), "unknown keys: ")
	if !ok {
		t.Fatalf("Load error %v reports no unknown keys", err)
	}
	return strings.Split(list, ", ")
}

// server-config "Unknown key rejected".
func TestLoadUnknownKeyRejected(t *testing.T) {
	_, err := Load(writeConfig(t, `
state_dir = "/var/lib/precious"
sorces = []

[server]
external_origin = "https://precious.example.net"
lisen = "127.0.0.1:9000"
`))
	got := unknownKeys(t, err)
	if want := []string{"server.lisen", "sorces"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown keys = %q, want %q", got, want)
	}
}

// server-config "Removed settings fail loudly": a v0.2 configuration is
// refused, and the error names every key of every removed section, the
// [[sources]] tables included.
func TestLoadRemovedSettingsNamed(t *testing.T) {
	_, err := Load(writeConfig(t, `
state_dir = "/var/lib/precious"

[server]
external_origin = "https://precious.example.net"

[discovery]
node_budget = 50000

[inspection]
peek_entry_limit = 100

[reconciliation]
expanded_interval = "15m"

[inbox]
poll_interval = "5s"

[watch]
backend = "auto"

[copies]
large_file_bytes = 1048576

[classifier]
workers = 2

[[sources]]
id = "old-disk"
label = "Old disk"
root = "/srv/sources/old-disk"
mode = "read_only"

[[classifier_profiles]]
id = "jev"
adapter = "typesafe"

[[classifier_policies]]
id = "dirs"
primary = "jev"
`))
	got := unknownKeys(t, err)
	for _, key := range []string{
		"classifier", "classifier.workers",
		"classifier_policies", "classifier_policies.id", "classifier_policies.primary",
		"classifier_profiles", "classifier_profiles.adapter", "classifier_profiles.id",
		"copies", "copies.large_file_bytes",
		"discovery", "discovery.node_budget",
		"inbox", "inbox.poll_interval",
		"inspection", "inspection.peek_entry_limit",
		"reconciliation", "reconciliation.expanded_interval",
		"sources.id", "sources.label", "sources.mode", "sources.root",
		"watch", "watch.backend",
	} {
		found := false
		for _, k := range got {
			found = found || k == key
		}
		if !found {
			t.Errorf("unknown keys %q do not name %s", got, key)
		}
	}
}

// server-config "Write mode requested": a v0.2 write setting inside the new
// [sources] table is an unknown key.
func TestLoadSourcesModeUnknown(t *testing.T) {
	_, err := Load(writeConfig(t, `
state_dir = "/var/lib/precious"
[server]
external_origin = "https://precious.example.net"
[sources]
mode = "read_write"
`))
	if got := unknownKeys(t, err); !reflect.DeepEqual(got, []string{"sources.mode"}) {
		t.Fatalf("unknown keys = %q, want sources.mode", got)
	}
}

func TestLoadMalformedRejected(t *testing.T) {
	for name, body := range map[string]string{
		"syntax":         "state_dir = ",
		"wrong type":     "state_dir = 5",
		"bad duration":   "[auth]\nsession_idle = \"soon\"",
		"sources scalar": `sources = "old-disk"`,
		"roots scalar":   "[sources]\nallowed_roots = \"/mnt\"",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatal("Load accepted a malformed file")
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("Load accepted a missing file")
	}
}

func TestLoadAppliesDefaultsAndReportsProblems(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	cfg, err := Load(writeConfig(t, `
state_dir = "`+stateDir+`"

[server]
external_origin = "https://precious.example.net"

[jobs]
lease = "1m"

[sources]
allowed_roots = ["`+base+`/"]

[scan]
list_batch = 512
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.StateDir = stateDir
	want.Server.ExternalOrigin = "https://precious.example.net"
	want.Jobs.Lease = Duration{time.Minute}
	want.Sources.AllowedRoots = []string{base}
	want.Scan.ListBatch = 512
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load = %+v\nwant %+v", cfg, want)
	}

	_, err = Load(writeConfig(t, `
state_dir = "relative"

[scan]
batch_size = 0
`))
	p, ok := AsProblems(err)
	if !ok {
		t.Fatalf("Load error %v does not carry Problems", err)
	}
	keys := make([]string, len(p))
	for i, s := range p {
		keys[i], _, _ = strings.Cut(s, ":")
	}
	if got, want := strings.Join(keys, " "), "state_dir server.external_origin scan.batch_size"; got != want {
		t.Fatalf("problem keys = %q, want %q\n%v", got, want, err)
	}
}

// server-config "Scan defaults" and "Defaults when absent": with no [sources]
// and no [scan] section, the allowed roots are empty (the platform defaults),
// writes are allowed, and the batch sizes are 1000 and 256.
func TestLoadSourcesAndScanDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
state_dir = "/var/lib/precious"
[server]
external_origin = "https://precious.example.net"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources.AllowedRoots != nil || !cfg.Sources.AllowWrites || cfg.Scan != (Scan{BatchSize: 1000, ListBatch: 256}) {
		t.Fatalf("sources %+v scan %+v, want no roots, writes allowed, and batches 1000/256", cfg.Sources, cfg.Scan)
	}
}

// server-config "Writes can be forbidden by the configuration": allow_writes
// is a boolean, true unless the file says false, also when [sources] sets
// only other keys.
func TestLoadAllowWrites(t *testing.T) {
	const head = "state_dir = \"/var/lib/precious\"\n[server]\nexternal_origin = \"https://precious.example.net\"\n"
	for _, tc := range []struct {
		sources string
		want    bool
	}{
		{"[sources]\nallowed_roots = []\n", true},
		{"[sources]\nallow_writes = true\n", true},
		{"[sources]\nallow_writes = false\n", false},
	} {
		cfg, err := Load(writeConfig(t, head+tc.sources))
		if err != nil {
			t.Fatalf("%q: %v", tc.sources, err)
		}
		if cfg.Sources.AllowWrites != tc.want {
			t.Errorf("%q: allow_writes = %v, want %v", tc.sources, cfg.Sources.AllowWrites, tc.want)
		}
	}
	if _, err := Load(writeConfig(t, head+"[sources]\nallow_writes = \"no\"\n")); err == nil {
		t.Error("allow_writes = \"no\" loaded, want a type error")
	}
}
