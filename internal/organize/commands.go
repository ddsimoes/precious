package organize

import (
	"context"
	"encoding/json"

	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/jobs"
)

// Command names, the {name} segment of POST /api/commands/{name}.
const (
	CommandPlanMove         = "plan-move"
	CommandPlanRename       = "plan-rename"
	CommandPlanCreateFolder = "plan-create-folder"
	CommandPlanRescue       = "plan-rescue"
	CommandPlanMerge        = "plan-merge"
	CommandPlanUndo         = "plan-undo"
	CommandRunAction        = "run-action"
	CommandCancelAction     = "cancel-action"
	CommandResolveRecovery  = "resolve-recovery"
)

// RegisterCommands installs the organize commands on h (r3 design
// Interfaces). Each body is decoded strictly; IDs are strings.
//
//   - plan-move {"entry_id"|"entry_ids"|"selection_id", "destination_id"},
//     plan-rename {"entry_id","name"}, plan-create-folder
//     {"parent_id","name"}, plan-rescue {"folder_id","destination_id"},
//     plan-merge {"left_id","right_id","from"}, plan-undo
//     {"action_id","destination_id"?}: 201 {"action","items","next_cursor"};
//   - run-action {"action_id"}: 202 {"action","job_id","state"};
//   - cancel-action {"action_id"}: 200 {"action"};
//   - resolve-recovery {"item_id"}: 200 {"action","scan":{"job_id","coalesced"}}.
func (s *Service) RegisterCommands(h *commands.Handler) {
	h.Register(CommandPlanMove, s.decodePlanMove)
	h.Register(CommandPlanRename, s.decodePlanRename)
	h.Register(CommandPlanCreateFolder, s.decodePlanCreateFolder)
	h.Register(CommandPlanRescue, s.decodePlanRescue)
	h.Register(CommandPlanMerge, s.decodePlanMerge)
	h.Register(CommandPlanUndo, s.decodePlanUndo)
	h.Register(CommandRunAction, s.decodeRunAction)
	h.Register(CommandCancelAction, s.decodeCancelAction)
	h.Register(CommandResolveRecovery, s.decodeResolveRecovery)
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

type planMoveRequest struct {
	EntryID       string   `json:"entry_id,omitempty"`
	EntryIDs      []string `json:"entry_ids,omitempty"`
	SelectionID   string   `json:"selection_id,omitempty"`
	DestinationID string   `json:"destination_id"`
}

func (s *Service) decodePlanMove(body []byte) (commands.Operation, error) {
	var req planMoveRequest
	check := func() error {
		given := 0
		for _, set := range []bool{req.EntryID != "", req.EntryIDs != nil, req.SelectionID != ""} {
			if set {
				given++
			}
		}
		if given != 1 {
			return domain.Errorf(domain.CodeInvalidRequest, "name exactly one of entry_id, entry_ids, and selection_id")
		}
		if req.EntryIDs != nil && (len(req.EntryIDs) < 1 || len(req.EntryIDs) > maxEntryIDs) {
			return domain.Errorf(domain.CodeInvalidRequest, "entry_ids holds 1 to %d IDs", maxEntryIDs)
		}
		return required("destination_id", req.DestinationID)
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planMove(ctx, tx, req)
	})
}

type planRenameRequest struct {
	EntryID string `json:"entry_id"`
	Name    string `json:"name"`
}

func (s *Service) decodePlanRename(body []byte) (commands.Operation, error) {
	var req planRenameRequest
	check := func() error {
		if err := required("entry_id", req.EntryID); err != nil {
			return err
		}
		return validName(req.Name)
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planRename(ctx, tx, req)
	})
}

type planCreateFolderRequest struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

func (s *Service) decodePlanCreateFolder(body []byte) (commands.Operation, error) {
	var req planCreateFolderRequest
	check := func() error {
		if err := required("parent_id", req.ParentID); err != nil {
			return err
		}
		return validName(req.Name)
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planCreateFolder(ctx, tx, req)
	})
}

type planRescueRequest struct {
	FolderID      string `json:"folder_id"`
	DestinationID string `json:"destination_id"`
}

func (s *Service) decodePlanRescue(body []byte) (commands.Operation, error) {
	var req planRescueRequest
	check := func() error {
		if err := required("folder_id", req.FolderID); err != nil {
			return err
		}
		return required("destination_id", req.DestinationID)
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planRescue(ctx, tx, req)
	})
}

type planMergeRequest struct {
	LeftID  string `json:"left_id"`
	RightID string `json:"right_id"`
	From    string `json:"from"`
}

func (s *Service) decodePlanMerge(body []byte) (commands.Operation, error) {
	var req planMergeRequest
	check := func() error {
		if err := required("left_id", req.LeftID); err != nil {
			return err
		}
		if err := required("right_id", req.RightID); err != nil {
			return err
		}
		if req.From != "left" && req.From != "right" {
			return domain.Errorf(domain.CodeInvalidRequest, `from is "left" or "right"`)
		}
		return nil
	}
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planMerge(ctx, tx, req)
	})
}

type planUndoRequest struct {
	ActionID      string `json:"action_id"`
	DestinationID string `json:"destination_id,omitempty"`
}

func (s *Service) decodePlanUndo(body []byte) (commands.Operation, error) {
	var req planUndoRequest
	check := func() error { return required("action_id", req.ActionID) }
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.planUndo(ctx, tx, req)
	})
}

type actionRequest struct {
	ActionID string `json:"action_id"`
}

func (s *Service) decodeRunAction(body []byte) (commands.Operation, error) {
	var req actionRequest
	check := func() error { return required("action_id", req.ActionID) }
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.runAction(ctx, tx, req.ActionID)
	})
}

func (s *Service) decodeCancelAction(body []byte) (commands.Operation, error) {
	var req actionRequest
	check := func() error { return required("action_id", req.ActionID) }
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.cancelAction(ctx, tx, req.ActionID)
	})
}

type resolveRecoveryRequest struct {
	ItemID string `json:"item_id"`
}

func (s *Service) decodeResolveRecovery(body []byte) (commands.Operation, error) {
	var req resolveRecoveryRequest
	check := func() error { return required("item_id", req.ItemID) }
	return newOp(body, &req, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		return s.resolveRecovery(ctx, tx, req.ItemID)
	})
}
