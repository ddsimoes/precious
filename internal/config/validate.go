package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Upper bounds of the [scan] settings (design D7): they keep one write
// transaction and one directory read bounded.
const (
	maxScanBatchSize = 10_000
	maxScanListBatch = 4096
)

// Validate checks every startup safety rule in the server-config spec and
// returns Problems naming each offending key. It cleans the allowed roots in
// place.
func Validate(c *Config) error {
	var p Problems
	validateStateDir(c.StateDir, &p)
	validateServer(&c.Server, &p)
	validateAuth(&c.Auth, &p)
	validateJobs(&c.Jobs, &p)
	validateSources(&c.Sources, &p)
	validateScan(&c.Scan, &p)
	return p.Err()
}

func (p *Problems) addf(key, format string, args ...any) {
	*p = append(*p, key+": "+fmt.Sprintf(format, args...))
}

// validateStateDir requires an absolute state directory.
func validateStateDir(dir string, p *Problems) {
	switch {
	case dir == "":
		p.addf("state_dir", "required; set an absolute path on local storage, such as \"/var/lib/precious\"")
	case !filepath.IsAbs(dir):
		p.addf("state_dir", "%q must be an absolute path", dir)
	}
}

// validateSources requires every allowed root to be an absolute path of an
// existing directory, and cleans each one.
func validateSources(s *Sources, p *Problems) {
	const key = "sources.allowed_roots"
	for i, root := range s.AllowedRoots {
		if !filepath.IsAbs(root) {
			p.addf(key, "%q must be an absolute directory path", root)
			continue
		}
		root = filepath.Clean(root)
		s.AllowedRoots[i] = root
		fi, err := os.Stat(root)
		switch {
		case err != nil:
			p.addf(key, "%q is not an existing directory: %v", root, err)
		case !fi.IsDir():
			p.addf(key, "%q exists but is not a directory", root)
		}
	}
}

func validateScan(s *Scan, p *Problems) {
	if s.BatchSize < 1 || s.BatchSize > maxScanBatchSize {
		p.addf("scan.batch_size", "must be between 1 and %d, got %d", maxScanBatchSize, s.BatchSize)
	}
	if s.ListBatch < 1 || s.ListBatch > maxScanListBatch {
		p.addf("scan.list_batch", "must be between 1 and %d, got %d", maxScanListBatch, s.ListBatch)
	}
}

func validateAuth(a *Auth, p *Problems) {
	if a.SessionIdle.Duration <= 0 {
		p.addf("auth.session_idle", "must be positive, got %s", a.SessionIdle.Duration)
	}
	if a.SessionAbsolute.Duration < a.SessionIdle.Duration || a.SessionAbsolute.Duration <= 0 {
		p.addf("auth.session_absolute", "must be positive and at least auth.session_idle (%s), got %s", a.SessionIdle.Duration, a.SessionAbsolute.Duration)
	}
	if a.LoginMaxBackoff.Duration <= 0 {
		p.addf("auth.login_max_backoff", "must be positive, got %s", a.LoginMaxBackoff.Duration)
	}
}

func validateJobs(j *Jobs, p *Problems) {
	positive := func(key string, v int) {
		if v < 1 {
			p.addf(key, "must be at least 1, got %d", v)
		}
	}
	positive("jobs.workers_per_device", j.WorkersPerDevice)
	positive("jobs.max_attempts", j.MaxAttempts)
	positive("jobs.event_retention_rows", j.EventRetentionRows)
	for _, d := range []struct {
		key string
		v   Duration
	}{
		{"jobs.lease", j.Lease},
		{"jobs.lease_renew", j.LeaseRenew},
		{"jobs.call_watchdog", j.CallWatchdog},
		{"jobs.event_retention_age", j.EventRetentionAge},
	} {
		if d.v.Duration <= 0 {
			p.addf(d.key, "must be positive, got %s", d.v.Duration)
		}
	}
	if j.LeaseRenew.Duration > 0 && j.LeaseRenew.Duration >= j.Lease.Duration {
		p.addf("jobs.lease_renew", "must be shorter than jobs.lease (%s), got %s", j.Lease.Duration, j.LeaseRenew.Duration)
	}
}
