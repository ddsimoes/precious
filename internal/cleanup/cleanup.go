// Package cleanup removes discarded items from a disk, never in one step
// (r4 design D1–D16, Interfaces): a cleanup plan moves them to their
// source's quarantine, a restore brings them back, the pre-delete check
// reads a chosen set of quarantined items and looks for a verified copy of
// every file, and a purge deletes a checked set for good. Plans run on the
// executor; the check is a read-only job of this package (D7).
package cleanup

import (
	"log/slog"

	"precious/internal/clock"
	"precious/internal/content"
	"precious/internal/executor"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
	"precious/internal/store"
)

// Options configures New.
type Options struct {
	Store   *store.Store
	Runner  *jobs.Runner
	Sources *sources.Service
	// Content hashes the files of a checked set and their copies (D9).
	Content *content.Service
	// Policy classifies the archive members a check finds with no copy
	// (D8).
	Policy   *rules.Policy
	Executor *executor.Executor
	// AllowWrites is [sources] allow_writes.
	AllowWrites bool
	// Clock defaults to clock.Real; Logger to slog.Default.
	Clock  clock.Clock
	Logger *slog.Logger
}

// Service serves cleanup: the purge_check job (Register), and the commands
// and reads.
type Service struct {
	st          *store.Store
	runner      *jobs.Runner
	src         *sources.Service
	content     *content.Service
	pol         *rules.Policy
	ex          *executor.Executor
	allowWrites bool
	clk         clock.Clock
	log         *slog.Logger
}

// New returns the cleanup service. Call Register before the runner starts.
func New(o Options) *Service {
	s := &Service{st: o.Store, runner: o.Runner, src: o.Sources, content: o.Content, pol: o.Policy, ex: o.Executor,
		allowWrites: o.AllowWrites, clk: o.Clock, log: o.Logger}
	if s.clk == nil {
		s.clk = clock.Real{}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.pol == nil {
		s.pol = rules.Default()
	}
	return s
}
