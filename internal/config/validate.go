package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
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
	validateHashing(&c.Hashing, &p)
	validateArchives(&c.Archives, &p)
	validateDuplicates(&c.Duplicates, &p)
	validateDates(&c.Dates, &p)
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

// Bounds of the R2 [hashing], [archives], and [duplicates] settings
// (server-config).
const (
	kib = int64(1) << 10
	mib = int64(1) << 20
	gib = int64(1) << 30
	tib = int64(1) << 40
)

// inRange reports a problem unless lo <= v <= hi.
func (p *Problems) inRange(key string, v, lo, hi int64) {
	if v < lo || v > hi {
		p.addf(key, "must be between %d and %d, got %d", lo, hi, v)
	}
}

// durationInRange reports a problem unless lo <= d <= hi.
func (p *Problems) durationInRange(key string, d Duration, lo, hi time.Duration) {
	if d.Duration < lo || d.Duration > hi {
		p.addf(key, "must be between %s and %s, got %s", lo, hi, d.Duration)
	}
}

func validateHashing(h *Hashing, p *Problems) {
	p.inRange("hashing.read_chunk_bytes", h.ReadChunkBytes, 64*kib, 16*mib)
	p.inRange("hashing.yield_bytes", h.YieldBytes, mib, gib)
}

func validateArchives(a *Archives, p *Problems) {
	p.inRange("archives.max_members", a.MaxMembers, 1, 5_000_000)
	p.inRange("archives.max_unpacked_bytes", a.MaxUnpackedBytes, mib, 16*tib)
	p.inRange("archives.max_ratio", a.MaxRatio, 2, 100_000)
	p.durationInRange("archives.max_time", a.MaxTime, time.Minute, 7*24*time.Hour)
	p.inRange("archives.view_max_bytes", a.ViewMaxBytes, mib, gib)
}

func validateDuplicates(d *Duplicates, p *Problems) {
	p.durationInRange("duplicates.refresh_interval", d.RefreshInterval, time.Minute, 24*time.Hour)
}

// validateDates requires a time zone the binary can resolve, when one is set.
func validateDates(d *Dates, p *Problems) {
	if _, err := d.Location(); err != nil {
		p.addf("dates.time_zone", "%v", err)
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
