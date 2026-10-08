package cleanup

import (
	"context"
	"encoding/json"

	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/jobs"
)

// Command names, the {name} segment of POST /api/commands/{name} (r4 design
// Interfaces).
const (
	CommandPlanCleanup  = "plan-cleanup"
	CommandPlanRestore  = "plan-restore"
	CommandCheckPurge   = "check-purge"
	CommandConfirmPurge = "confirm-purge"
	CommandPlanPurge    = "plan-purge"
)

// AuditPurgeConfirmed is the audit event of confirm-purge: the owner
// agreed to delete files that have no verified copy (r4 D8).
const AuditPurgeConfirmed = "purge_confirmed"

// Limits of the commands (r4 design D3, Interfaces).
const (
	// maxSteps is the most items one cleanup action holds: 9,999 entries
	// of three steps each, and the two folders of the plan.
	maxSteps = 30000
	// maxRestoreIDs and maxCheckIDs bound the entry_ids of plan-restore and
	// check-purge; maxFileIDs the file_ids of confirm-purge.
	maxRestoreIDs = 1000
	maxCheckIDs   = 10000
	maxFileIDs    = 1000
)

// RegisterCommands installs the cleanup commands on h (r4 design
// Interfaces). Each body is decoded strictly; IDs are strings.
//
//   - plan-cleanup {"source_id","list"?}: 201 {"action","items",
//     "next_cursor","summary"};
//   - plan-restore {"entry_ids"|"plan_id","destination_id"?}: 201
//     {"action","items","next_cursor"};
//   - check-purge {"entry_ids"|"check_id"}: 202 {"check_id","job_id"};
//   - confirm-purge {"check_id","file_ids"|"group":"likely_junk"}: 200
//     {"check"};
//   - plan-purge {"check_id"}: 201 {"action","items","next_cursor"}.
//
// Running a plan is organize's run-action, which gates a purge on its check
// (organize.GatePurge).
func (s *Service) RegisterCommands(h *commands.Handler) {
	h.Register(CommandPlanCleanup, s.decodePlanCleanup)
	h.Register(CommandPlanRestore, s.decodePlanRestore)
	h.Register(CommandCheckPurge, s.decodeCheckPurge)
	h.Register(CommandConfirmPurge, s.decodeConfirmPurge)
	h.Register(CommandPlanPurge, s.decodePlanPurge)
}

// op is a decoded command: its canonical body and its effect.
type op struct {
	canonical []byte
	apply     func(ctx context.Context, tx *jobs.Tx) (int, any, error)
}

func (o *op) Canonical() []byte             { return o.canonical }
func (o *op) Prepare(context.Context) error { return nil }
func (o *op) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	return o.apply(ctx, tx)
}

// newOp decodes body strictly into req, checks its shape, and returns an
// operation whose canonical form is req re-encoded.
func newOp[T any](body []byte, req *T, check func() error, apply func(ctx context.Context, tx *jobs.Tx) (int, any, error)) (commands.Operation, error) {
	if err := commands.DecodeStrict(body, req); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return &op{canonical: canonical, apply: apply}, nil
}

// required refuses an empty field.
func required(field, v string) error {
	if v == "" {
		return domain.Errorf(domain.CodeInvalidRequest, "%s is required", field)
	}
	return nil
}

// idCount refuses a list of IDs outside 1 to most.
func idCount(field string, ids []string, most int) error {
	if len(ids) < 1 || len(ids) > most {
		return domain.Errorf(domain.CodeInvalidRequest, "%s holds 1 to %d IDs", field, most)
	}
	return nil
}

type planCleanupRequest struct {
	SourceID string `json:"source_id"`
	List     string `json:"list,omitempty"`
}

func (s *Service) decodePlanCleanup(body []byte) (commands.Operation, error) {
	var req planCleanupRequest
	check := func() error { return required("source_id", req.SourceID) }
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planCleanup(ctx, tx, req)
	})
}

type planRestoreRequest struct {
	EntryIDs      []string `json:"entry_ids,omitempty"`
	PlanID        string   `json:"plan_id,omitempty"`
	DestinationID string   `json:"destination_id,omitempty"`
}

func (s *Service) decodePlanRestore(body []byte) (commands.Operation, error) {
	var req planRestoreRequest
	check := func() error {
		if (req.EntryIDs != nil) == (req.PlanID != "") {
			return domain.Errorf(domain.CodeInvalidRequest, "name exactly one of entry_ids and plan_id")
		}
		if req.EntryIDs != nil {
			return idCount("entry_ids", req.EntryIDs, maxRestoreIDs)
		}
		return nil
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planRestore(ctx, tx, req)
	})
}

type checkPurgeRequest struct {
	EntryIDs []string `json:"entry_ids,omitempty"`
	// CheckID checks again what is left in the quarantine of an earlier
	// check's set (G11).
	CheckID string `json:"check_id,omitempty"`
}

func (s *Service) decodeCheckPurge(body []byte) (commands.Operation, error) {
	var req checkPurgeRequest
	check := func() error {
		if (req.EntryIDs != nil) == (req.CheckID != "") {
			return domain.Errorf(domain.CodeInvalidRequest, "name exactly one of entry_ids and check_id")
		}
		if req.EntryIDs != nil {
			return idCount("entry_ids", req.EntryIDs, maxCheckIDs)
		}
		return nil
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.checkPurge(ctx, tx, req)
	})
}

// groupLikelyJunk is the one group confirm-purge confirms at once (r4 D8).
const groupLikelyJunk = "likely_junk"

type confirmPurgeRequest struct {
	CheckID string   `json:"check_id"`
	FileIDs []string `json:"file_ids,omitempty"`
	Group   string   `json:"group,omitempty"`
}

func (s *Service) decodeConfirmPurge(body []byte) (commands.Operation, error) {
	var req confirmPurgeRequest
	check := func() error {
		if err := required("check_id", req.CheckID); err != nil {
			return err
		}
		if (req.FileIDs != nil) == (req.Group != "") {
			return domain.Errorf(domain.CodeInvalidRequest, "name exactly one of file_ids and group")
		}
		if req.Group != "" && req.Group != groupLikelyJunk {
			return domain.Errorf(domain.CodeInvalidRequest, "group is %q", groupLikelyJunk)
		}
		if req.FileIDs != nil {
			return idCount("file_ids", req.FileIDs, maxFileIDs)
		}
		return nil
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.confirmPurge(ctx, tx, req)
	})
}

type planPurgeRequest struct {
	CheckID string `json:"check_id"`
}

func (s *Service) decodePlanPurge(body []byte) (commands.Operation, error) {
	var req planPurgeRequest
	check := func() error { return required("check_id", req.CheckID) }
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planPurge(ctx, tx, req)
	})
}
