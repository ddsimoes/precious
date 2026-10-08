// Package config defines precious's operator configuration (TOML, design D16),
// its defaults, loading, and startup safety validation.
//
// The struct shape and defaults below are a shared contract: other packages read
// these fields. Loading and validation live in this package's other files.
package config

import "time"

// Config is the whole configuration file.
type Config struct {
	// StateDir holds the database and other private state. Absolute path on
	// local storage.
	StateDir   string     `toml:"state_dir"`
	Server     Server     `toml:"server"`
	Auth       Auth       `toml:"auth"`
	Jobs       Jobs       `toml:"jobs"`
	Sources    Sources    `toml:"sources"`
	Scan       Scan       `toml:"scan"`
	Hashing    Hashing    `toml:"hashing"`
	Archives   Archives   `toml:"archives"`
	Duplicates Duplicates `toml:"duplicates"`
}

// Server is the HTTP listener and browser-origin policy (§12.3).
type Server struct {
	// Listen is the TCP listen address. Default 127.0.0.1:8080.
	Listen string `toml:"listen"`
	// ExternalOrigin is the exact origin browsers use, e.g. https://precious.example.net.
	// Required. It determines the expected Origin header and cookie security.
	ExternalOrigin string `toml:"external_origin"`
	// AllowNonLoopbackListen must be true to listen on a non-loopback address
	// (for example 0.0.0.0 inside a container).
	AllowNonLoopbackListen bool `toml:"allow_non_loopback_listen"`
	// AllowInsecureHTTP permits an http:// external origin, on any host, so
	// precious can be served over plain HTTP on a local network.
	AllowInsecureHTTP bool `toml:"allow_insecure_http"`
	// TrustedProxies lists CIDR prefixes whose forwarded headers are honored.
	TrustedProxies []string `toml:"trusted_proxies"`
	// MaxRequestBytes caps request bodies. Default 1 MiB.
	MaxRequestBytes int64 `toml:"max_request_bytes"`
}

// Auth holds session and login-throttling settings (§12.1).
type Auth struct {
	SessionIdle     Duration `toml:"session_idle"`      // default 1h
	SessionAbsolute Duration `toml:"session_absolute"`  // default 12h
	LoginMaxBackoff Duration `toml:"login_max_backoff"` // default 15m
}

// Jobs holds runner settings (§12, design D15).
type Jobs struct {
	WorkersPerDevice   int      `toml:"workers_per_device"`   // default 1
	MaxAttempts        int      `toml:"max_attempts"`         // default 3
	Lease              Duration `toml:"lease"`                // default 30s
	LeaseRenew         Duration `toml:"lease_renew"`          // default 10s
	CallWatchdog       Duration `toml:"call_watchdog"`        // default 30s
	EventRetentionRows int      `toml:"event_retention_rows"` // default 10,000
	EventRetentionAge  Duration `toml:"event_retention_age"`  // default 24h
}

// Sources holds where sources may be added (§6.1, design D5) and whether
// any of them may be changed (r3 design D1). Sources themselves, and each
// source's own write permission, live in the database.
type Sources struct {
	// AllowedRoots are the folders the picker offers and below which a source
	// may be added: absolute paths of existing directories, cleaned by
	// Validate. Empty selects the platform default roots.
	AllowedRoots []string `toml:"allowed_roots"`
	// AllowWrites false forbids writes on every source, whatever its own
	// write permission (reason forbidden_by_config). Default true: each
	// source still starts with writes off until the owner turns them on.
	AllowWrites bool `toml:"allow_writes"`
}

// Scan holds the scanner's batch sizes (§7, design D7).
type Scan struct {
	// BatchSize is the most row operations one write transaction commits.
	BatchSize int `toml:"batch_size"` // default 1000; 1–10,000
	// ListBatch is the most directory entries one read returns.
	ListBatch int `toml:"list_batch"` // default 256; 1–4096
}

// Hashing holds how content is read for digests (§7, R2 design D4).
type Hashing struct {
	// ReadChunkBytes is the size of one read call.
	ReadChunkBytes int64 `toml:"read_chunk_bytes"` // default 1 MiB; 64 KiB–16 MiB
	// YieldBytes is how many bytes a hashing job reads between yields to
	// scans and pages, even inside one file.
	YieldBytes int64 `toml:"yield_bytes"` // default 64 MiB; 1 MiB–1 GiB
}

// Archives holds the budgets for opening archives (§6.4, R2 design D7, D17).
// Reaching a budget leaves the archive partial.
type Archives struct {
	MaxMembers       int64    `toml:"max_members"`        // default 1,000,000; 1–5,000,000
	MaxUnpackedBytes int64    `toml:"max_unpacked_bytes"` // default 1 TiB; 1 MiB–16 TiB
	MaxRatio         int64    `toml:"max_ratio"`          // default 100; 2–100,000
	MaxTime          Duration `toml:"max_time"`           // default 4h; 1m–7 days
	// ViewMaxBytes is the largest compressed member the viewer inflates
	// into memory to serve with ranges.
	ViewMaxBytes int64 `toml:"view_max_bytes"` // default 64 MiB; 1 MiB–1 GiB
}

// Duplicates holds how often folder relations are recomputed while hashing
// runs (R2 design D5).
type Duplicates struct {
	RefreshInterval Duration `toml:"refresh_interval"` // default 10m; 1m–24h
}

// Duration is a time.Duration decoded from TOML strings such as "30s" or "1h".
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.Duration.String()), nil }

// Defaults returns a Config holding every default value. Load starts from this
// and overlays the file.
func Defaults() Config {
	return Config{
		Server: Server{
			Listen:          "127.0.0.1:8080",
			MaxRequestBytes: 1 << 20,
		},
		Auth: Auth{
			SessionIdle:     Duration{time.Hour},
			SessionAbsolute: Duration{12 * time.Hour},
			LoginMaxBackoff: Duration{15 * time.Minute},
		},
		Jobs: Jobs{
			WorkersPerDevice:   1,
			MaxAttempts:        3,
			Lease:              Duration{30 * time.Second},
			LeaseRenew:         Duration{10 * time.Second},
			CallWatchdog:       Duration{30 * time.Second},
			EventRetentionRows: 10_000,
			EventRetentionAge:  Duration{24 * time.Hour},
		},
		Sources: Sources{
			AllowWrites: true,
		},
		Scan: Scan{
			BatchSize: 1000,
			ListBatch: 256,
		},
		Hashing: Hashing{
			ReadChunkBytes: 1 << 20,
			YieldBytes:     64 << 20,
		},
		Archives: Archives{
			MaxMembers:       1_000_000,
			MaxUnpackedBytes: 1 << 40,
			MaxRatio:         100,
			MaxTime:          Duration{4 * time.Hour},
			ViewMaxBytes:     64 << 20,
		},
		Duplicates: Duplicates{
			RefreshInterval: Duration{10 * time.Minute},
		},
	}
}
