// Package dates gives every photo and video of the index its effective date
// (precious-spec §10.7.1–§10.7.3; r5 design D2–D11):
//
//   - MediaCond is the one SQL condition that says which entries are media
//     (D2); every reader, pass, and command of this package applies it.
//   - EnqueueMedia requests a source's `media` job (D4) inside the caller's
//     transaction, without ever losing a request.
//   - Rederive derives the effective dates of entries inside the writing
//     transaction, with media.Derive, into media_dates, and keeps the
//     source's summary in media_sources (D9, D10).
//   - Targets, Validate, and ExpandTargets resolve the targets of the
//     correction commands and of organize's date plans (D11, D14, D16).
//
// The media job (Register, DeferWhile, AfterScan, Startup), the correction
// commands (RegisterCommands), and the reads (Routes) build on these.
//
// Imports go one way: dates reads index, content, and sources and is read
// by organize and cmd/precious; it never imports organize or executor.
package dates

import (
	"log/slog"
	"time"

	"precious/internal/clock"
	"precious/internal/jobs"
	"precious/internal/sources"
	"precious/internal/store"
)

// KindMedia is the media job's kind (D4): ClassBulk, bound to its source,
// single flight by scope "media:<source>", plus at most one follow-up by
// scope "media-next:<source>" enqueued while the first one runs.
const KindMedia jobs.Kind = "media"

// Options configures New.
type Options struct {
	Store   *store.Store
	Runner  *jobs.Runner
	Sources *sources.Service
	// Zone is [dates] time_zone resolved (config.Dates.Location); nil is
	// time.Local, as an unset time_zone is.
	Zone *time.Location
	// Clock defaults to clock.Real; Logger to slog.Default.
	Clock  clock.Clock
	Logger *slog.Logger
}

// Service derives, stores, and serves media dates.
type Service struct {
	st     *store.Store
	runner *jobs.Runner
	src    *sources.Service
	zone   *time.Location
	clk    clock.Clock
	log    *slog.Logger
	job    jobWiring // the media job's wiring (job.go)
}

// New returns the dates service.
func New(o Options) *Service {
	s := &Service{st: o.Store, runner: o.Runner, src: o.Sources, zone: o.Zone, clk: o.Clock, log: o.Logger}
	if s.zone == nil {
		s.zone = time.Local
	}
	if s.clk == nil {
		s.clk = clock.Real{}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}
