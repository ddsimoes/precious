// Package organize plans the changes the owner asks for on a source and
// hands them to the executor (r3 design D7–D9, D11–D14, D17, Interfaces):
//
//   - the plan-* commands (plan-move, plan-rename, plan-create-folder,
//     plan-rescue, plan-merge, plan-undo) read the index only, never a disk
//     (D8), and write an action in state planned with its items: planned,
//     refused with a reason, or in conflict. A plan expires after an hour;
//   - run-action queues a planned action for its organize job, cancel-action
//     stops a queued or running one, and resolve-recovery marks an item the
//     owner checked by hand as resolved and scans its source again;
//   - the history read API (GET /api/history…) lists actions and items;
//   - Index adapts the index, decisions, and relations packages to the
//     executor's Index, so a done step updates the index in its outcome's
//     transaction (D6).
//
// Every command runs in the command's write transaction and does no disk
// I/O. The executor owns every later state change of an action.
package organize

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/rules"
	"precious/internal/store"
	"precious/internal/web/clientip"
)

// Limits of plans and pages (r3 design D9, Interfaces).
const (
	// actionTTL is how long a planned action can be run.
	actionTTL = time.Hour
	// plannedRetention is how long an expired plan is kept before the next
	// plan deletes it.
	plannedRetention = 24 * time.Hour
	// maxItems is the most items one action holds.
	maxItems = 10000
	// maxEntryIDs is the most entry_ids one plan-move names.
	maxEntryIDs = 1000
	// maxNameBytes bounds a new name.
	maxNameBytes = 255
	// Pages of items (the first page answers a plan) and of the history.
	itemsDefaultLimit   = 200
	itemsMaxLimit       = 1000
	historyDefaultLimit = 50
	historyMaxLimit     = 200
)

// Audit events of the commands that change an action's state.
const (
	AuditActionRun        = "action_run"
	AuditActionCancelled  = "action_cancelled"
	AuditRecoveryResolved = "recovery_resolved"
)

// Options configures New.
type Options struct {
	Store *store.Store
	// Policy is the policy the scans classify with; the index adapter's
	// refold classifies moved entries with it.
	Policy *rules.Policy
	// AllowWrites is [sources] allow_writes, checked by every plan and run.
	AllowWrites bool
	// Clock (default clock.Real) says when a planned action reads expired in
	// the history; commands use their transaction's time.
	Clock  clock.Clock
	Logger *slog.Logger
}

// Service serves the organize commands and the history.
type Service struct {
	st          *store.Store
	rf          *index.Refolder
	allowWrites bool
	clk         clock.Clock
	log         *slog.Logger
}

// New returns the organize service.
func New(o Options) *Service {
	s := &Service{st: o.Store, rf: index.NewRefolder(o.Policy), allowWrites: o.AllowWrites, clk: o.Clock, log: o.Logger}
	if s.clk == nil {
		s.clk = clock.Real{}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// audit writes one audit event of an organize command, with the client's
// address.
func audit(ctx context.Context, tx *sql.Tx, now time.Time, kind string, detail map[string]any) error {
	ev := auth.AuditEvent{At: now, Kind: kind, Actor: auth.ActorAdmin, Detail: detail}
	if info, ok := clientip.From(ctx); ok {
		ev.ClientAddr = info.Addr
	}
	return auth.WriteAudit(ctx, tx, ev)
}

// notFound is the error of an ID that names nothing.
func notFound(what, id string) error {
	return domain.Errorf(domain.CodeNotFound, "%s %q not found", what, id)
}
