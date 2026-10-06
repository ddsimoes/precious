package sources

import (
	"context"
	"encoding/json"
	"net/http"

	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/jobs"
)

// Command names, the {name} segment of POST /api/commands/{name}.
const (
	CommandAddSource    = "add-source"
	CommandRenameSource = "rename-source"
	CommandRemoveSource = "remove-source"
)

// RegisterCommands registers add-source, rename-source, and remove-source
// (design D5) with h. Each request is decoded strictly: a field other than
// those listed, such as a path, is invalid_request.
//
//   - add-source {"handle", "label"?}: 201 {"source": SourceJSON}.
//   - rename-source {"source_id", "label"}: 200 {"source": SourceJSON}.
//   - remove-source {"source_id"}: 200 {}.
func RegisterCommands(h *commands.Handler, s *Service) {
	h.Register(CommandAddSource, func(body []byte) (commands.Operation, error) {
		var req addSourceRequest
		if err := commands.DecodeStrict(body, &req); err != nil {
			return nil, err
		}
		if req.Handle == "" {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "handle is required")
		}
		return &addSourceOp{s: s, req: req}, nil
	})
	h.Register(CommandRenameSource, func(body []byte) (commands.Operation, error) {
		var req renameSourceRequest
		if err := commands.DecodeStrict(body, &req); err != nil {
			return nil, err
		}
		if req.SourceID == "" {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "source_id is required")
		}
		label, err := cleanLabel(req.Label)
		if err != nil {
			return nil, err
		}
		req.Label = label
		return &renameSourceOp{s: s, req: req}, nil
	})
	h.Register(CommandRemoveSource, func(body []byte) (commands.Operation, error) {
		var req removeSourceRequest
		if err := commands.DecodeStrict(body, &req); err != nil {
			return nil, err
		}
		if req.SourceID == "" {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "source_id is required")
		}
		return &removeSourceOp{s: s, req: req}, nil
	})
}

// canonical is the JSON of a decoded request, which always marshals.
func canonical(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type addSourceRequest struct {
	Handle string `json:"handle"`
	// Label defaults to the folder's name.
	Label string `json:"label,omitempty"`
}

type addSourceOp struct {
	s    *Service
	req  addSourceRequest
	cand Candidate
}

func (o *addSourceOp) Canonical() []byte { return canonical(o.req) }

func (o *addSourceOp) Prepare(ctx context.Context) error {
	c, err := o.s.PrepareAdd(ctx, o.req.Handle, o.req.Label)
	o.cand = c
	return err
}

func (o *addSourceOp) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	src, err := o.s.Add(ctx, tx.SQL(), o.cand)
	if err != nil {
		return 0, nil, err
	}
	j, err := describe(ctx, tx.SQL(), src, o.s.displayMounts())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, sourceBody{Source: j}, nil
}

type renameSourceRequest struct {
	SourceID domain.SourceID `json:"source_id"`
	Label    string          `json:"label"`
}

type renameSourceOp struct {
	s   *Service
	req renameSourceRequest
}

func (o *renameSourceOp) Canonical() []byte { return canonical(o.req) }

func (o *renameSourceOp) Prepare(context.Context) error { return nil }

func (o *renameSourceOp) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	if err := o.s.Rename(ctx, tx.SQL(), o.req.SourceID, o.req.Label); err != nil {
		return 0, nil, err
	}
	src, err := getSource(ctx, tx.SQL(), o.req.SourceID)
	if err != nil {
		return 0, nil, err
	}
	j, err := describe(ctx, tx.SQL(), src, o.s.displayMounts())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, sourceBody{Source: j}, nil
}

type removeSourceRequest struct {
	SourceID domain.SourceID `json:"source_id"`
}

type removeSourceOp struct {
	s   *Service
	req removeSourceRequest
}

func (o *removeSourceOp) Canonical() []byte { return canonical(o.req) }

func (o *removeSourceOp) Prepare(context.Context) error { return nil }

func (o *removeSourceOp) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	if err := o.s.Remove(ctx, tx.SQL(), o.req.SourceID); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, struct{}{}, nil
}
