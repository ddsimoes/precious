package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// server-config "Hashing settings are validated", "Archive settings are
// validated", and "Duplicates settings are validated": each bound is
// accepted, one past it is refused naming its key.
func TestContentSettingsBounds(t *testing.T) {
	type bound struct {
		key    string
		set    func(c *Config, v int64)
		lo, hi int64
	}
	dur := func(set func(c *Config, d Duration)) func(c *Config, v int64) {
		return func(c *Config, v int64) { set(c, Duration{time.Duration(v)}) }
	}
	const mib, gib, tib = int64(1) << 20, int64(1) << 30, int64(1) << 40
	bounds := []bound{
		{"hashing.read_chunk_bytes", func(c *Config, v int64) { c.Hashing.ReadChunkBytes = v }, 64 << 10, 16 * mib},
		{"hashing.yield_bytes", func(c *Config, v int64) { c.Hashing.YieldBytes = v }, mib, gib},
		{"archives.max_members", func(c *Config, v int64) { c.Archives.MaxMembers = v }, 1, 5_000_000},
		{"archives.max_unpacked_bytes", func(c *Config, v int64) { c.Archives.MaxUnpackedBytes = v }, mib, 16 * tib},
		{"archives.max_ratio", func(c *Config, v int64) { c.Archives.MaxRatio = v }, 2, 100_000},
		{"archives.max_time", dur(func(c *Config, d Duration) { c.Archives.MaxTime = d }),
			int64(time.Minute), int64(7 * 24 * time.Hour)},
		{"archives.view_max_bytes", func(c *Config, v int64) { c.Archives.ViewMaxBytes = v }, mib, gib},
		{"duplicates.refresh_interval", dur(func(c *Config, d Duration) { c.Duplicates.RefreshInterval = d }),
			int64(time.Minute), int64(24 * time.Hour)},
	}
	for _, b := range bounds {
		for _, tc := range []struct {
			v  int64
			ok bool
		}{{b.lo, true}, {b.hi, true}, {b.lo - 1, false}, {b.hi + 1, false}} {
			t.Run(fmt.Sprintf("%s=%d", b.key, tc.v), func(t *testing.T) {
				c, _ := validConfig(t)
				b.set(&c, tc.v)
				var wants []want
				if !tc.ok {
					wants = []want{{b.key, "must be between"}}
				}
				checkProblems(t, Validate(&c), wants)
			})
		}
	}
}

// server-config "Hashing defaults printed" and "Archive defaults printed"
// (the values; check-config's output is tested in cmd/precious).
func TestContentSettingsDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
state_dir = "/var/lib/precious"
[server]
external_origin = "https://precious.example.net"
`))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Hashing{ReadChunkBytes: 1048576, YieldBytes: 67108864}); cfg.Hashing != want {
		t.Errorf("hashing = %+v, want %+v", cfg.Hashing, want)
	}
	if want := (Archives{MaxMembers: 1_000_000, MaxUnpackedBytes: 1 << 40, MaxRatio: 100,
		MaxTime: Duration{4 * time.Hour}, ViewMaxBytes: 64 << 20}); cfg.Archives != want {
		t.Errorf("archives = %+v, want %+v", cfg.Archives, want)
	}
	if want := (Duplicates{RefreshInterval: Duration{10 * time.Minute}}); cfg.Duplicates != want {
		t.Errorf("duplicates = %+v, want %+v", cfg.Duplicates, want)
	}
}

// server-config "Chunk out of range", "Ratio out of range", and "Interval
// out of range": loading fails naming the key.
func TestLoadContentSettingOutOfRange(t *testing.T) {
	for key, body := range map[string]string{
		"hashing.read_chunk_bytes":    "[hashing]\nread_chunk_bytes = 4096\n",
		"archives.max_ratio":          "[archives]\nmax_ratio = 1\n",
		"duplicates.refresh_interval": "[duplicates]\nrefresh_interval = \"10s\"\n",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := Load(writeConfig(t, `
state_dir = "/var/lib/precious"
[server]
external_origin = "https://precious.example.net"
`+body))
			p, ok := AsProblems(err)
			if !ok || len(p) != 1 || !strings.HasPrefix(p[0], key+": ") {
				t.Fatalf("Load = %v, want one problem naming %s", err, key)
			}
		})
	}
}

// server-config "v0.2 copies section still refused".
func TestLoadCopiesSectionStillRefused(t *testing.T) {
	_, err := Load(writeConfig(t, `
state_dir = "/var/lib/precious"
[server]
external_origin = "https://precious.example.net"
[copies]
large_file_bytes = 1048576
`))
	got := unknownKeys(t, err)
	if strings.Join(got, " ") != "copies copies.large_file_bytes" {
		t.Fatalf("unknown keys = %q, want copies and copies.large_file_bytes", got)
	}
}
