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
	StateDir string  `toml:"state_dir"`
	Server   Server  `toml:"server"`
	Auth     Auth    `toml:"auth"`
	Jobs     Jobs    `toml:"jobs"`
	Sources  Sources `toml:"sources"`
	Scan     Scan    `toml:"scan"`
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

// Sources holds where sources may be added (§6.1, design D5). Sources
// themselves live in the database.
type Sources struct {
	// AllowedRoots are the folders the picker offers and below which a source
	// may be added: absolute paths of existing directories, cleaned by
	// Validate. Empty selects the platform default roots.
	AllowedRoots []string `toml:"allowed_roots"`
}

// Scan holds the scanner's batch sizes (§7, design D7).
type Scan struct {
	// BatchSize is the most row operations one write transaction commits.
	BatchSize int `toml:"batch_size"` // default 1000; 1–10,000
	// ListBatch is the most directory entries one read returns.
	ListBatch int `toml:"list_batch"` // default 256; 1–4096
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
		Scan: Scan{
			BatchSize: 1000,
			ListBatch: 256,
		},
	}
}
