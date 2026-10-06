// Package jobs is the durable SQLite job runner (§8.4, design D11): persistence,
// leases, crash recovery, per-device worker limits, cancellation, batched
// progress, and the resumable event stream.
//
// This file is the shared contract between the runner and job handlers
// (internal/index implements the "scan" handler) and the runner's view of
// sources (Registry).
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"precious/internal/domain"
	"precious/internal/store"
)

// Registry is the runner's view of sources (design D15), implemented by
// sources.Service. A runner works without one: every source is then claimed
// under its placeholder key and the watchdog only logs.
type Registry interface {
	// DeviceKey returns the claim key of source's device (design D4): jobs of
	// sources with one key share that device's worker slots. q is the
	// runner's read view, a write transaction when the claim is decided in
	// one. An error or an empty key means the device is unknown: the runner
	// then claims under the placeholder "source:<id>", which runs the job
	// alone in its source. A key never starts with "workers:", the prefix of
	// RegisterPool keys.
	DeviceKey(ctx context.Context, q store.Queryer, source domain.SourceID) (string, error)
	// SetUnresponsive records that a filesystem call on source has been in
	// flight since since for longer than the call watchdog; a zero since
	// clears the flag once no flagged call is left.
	SetUnresponsive(ctx context.Context, source domain.SourceID, since time.Time) error
}

// Kind names a job handler.
type Kind string

// KindScan is the full scan of one source (registered by internal/index).
const KindScan Kind = "scan"

// Class is the priority class of a device-key job kind, which decides its
// share of a device when jobs compete for it (design D13). A job's class
// follows from its kind (RegisterClass); pool kinds have none.
type Class int

const (
	// ClassReconciliation is the class of scans, and the class of every kind
	// registered with Register.
	ClassReconciliation Class = iota
	// ClassInteractive is the class of work someone is waiting for, and the
	// rank of every job that has waited for its device longer than agingAfter.
	ClassInteractive
	// ClassBulk is the class of long background work.
	ClassBulk
)

// Job is the runner's view of one attempt, passed to its handler.
type Job struct {
	ID             domain.JobID
	Kind           Kind
	PayloadVersion int
	Payload        json.RawMessage
	SourceID       domain.SourceID // empty for jobs not bound to a source
	Attempt        int             // 1-based
}

// Handler runs one job attempt.
//
// ctx is cancelled when cancellation is requested or the lease is lost; the
// handler must check ctx between filesystem operations and must keep already
// committed observations. Return values:
//
//   - nil: the job succeeded.
//   - *Pause: the job pauses with that reason and can be resumed later.
//   - *Defer: the job is queued again, not to be claimed before Defer.Until,
//     without using an attempt.
//   - an error after ctx was cancelled by a cancel request: the job is cancelled.
//   - a *domain.Error: the job fails with that code (no automatic retry).
//   - any other error: the job fails with domain.CodeInternal.
//
// A cancel request made during the attempt wins over every outcome but nil.
// Automatic retries happen only when a worker is lost (crash or lease expiry);
// a handler that wants to run its job again later returns *Defer.
type Handler interface {
	Run(ctx context.Context, job Job, rt Runtime) error
}

// Runtime is supplied by the runner to a running handler.
type Runtime interface {
	// Progress records running totals: each key overwrites the stored value,
	// keys not passed are kept, and progress survives across attempts. The
	// runner persists and publishes progress in batches, so handlers may call
	// this per entry.
	Progress(counters map[string]int64)
	// FSCall marks the start of one filesystem operation for the watchdog. The
	// handler calls the returned func when the operation returns. A call that
	// exceeds the configured watchdog marks the source unresponsive and, after a
	// cancel request, shows the job as cancel_requested.
	FSCall(op string) (done func())
	// Yield offers the job's device at a work-unit boundary, with no
	// transaction open (design D13). It returns ctx.Err() when the job's
	// context is done, otherwise nil once the job may continue.
	Yield(ctx context.Context) error
	// UseSource moves the job to source's device (design D13). Call it only at a
	// work-unit boundary with no transaction open.
	//
	// It returns nil at once when source's device key is the job's current
	// one. Otherwise the job gives its slot up and waits for a slot of the
	// new key as a waiting job of its class does, and the watchdog watches
	// later filesystem calls against source. When ctx (or the job's context)
	// ends first, it returns ctx.Err() holding no slot, and the handler only
	// winds down. A source that does not exist is a domain unknown_source
	// error; a pool kind's job cannot move.
	UseSource(ctx context.Context, source domain.SourceID) error
}

// Pause is returned by a handler to pause its job.
type Pause struct {
	Reason string // stable identifier, shown as the job's pause_reason
	Detail string
}

func (p *Pause) Error() string { return fmt.Sprintf("paused: %s: %s", p.Reason, p.Detail) }

// Defer is returned by a handler to run its job again later (design D11): the
// job is stored queued with available_at = Until, or the time it returned if
// Until is earlier, with its attempt count unchanged and its lease cleared. It
// is not claimed before then, across restarts too, unless Tx.WakeOnce wakes
// it. Until it is claimed or woken, the job's TerminalDetail shows the
// deferral, as Error formats it with the stored time. A deferral returned
// while the runner stops is kept.
type Defer struct {
	Until  time.Time
	Reason string // e.g. "rate_limited"
}

func (d *Defer) Error() string { return deferDetail(d.Until, d.Reason) }

// deferDetail is the TerminalDetail of a job deferred until until.
func deferDetail(until time.Time, reason string) string {
	detail := "deferred until " + until.UTC().Format(time.RFC3339)
	if reason == "" {
		return detail
	}
	return detail + ": " + reason
}

// Event is the JSON `data` of an SSE `job` event and of job_events.payload.
// SSE events carry `id: <job_events.id>`; an expired Last-Event-ID yields an SSE
// event named `reset` with data `{}`.
type Event struct {
	JobID           string           `json:"job_id"`
	Kind            Kind             `json:"kind"`
	SourceID        domain.SourceID  `json:"source_id,omitempty"`
	State           domain.JobState  `json:"state"`
	CancelRequested bool             `json:"cancel_requested"`
	PauseReason     string           `json:"pause_reason,omitempty"`
	TerminalCode    domain.ErrorCode `json:"terminal_code,omitempty"`
	Progress        map[string]int64 `json:"progress"`
	Attempts        int              `json:"attempts"`
}
