package decisions

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/search"
)

// Command names, the {name} segment of POST /api/commands/{name}.
const (
	CommandSetDecision     = "set-decision"
	CommandSetTags         = "set-tags"
	CommandCreateTag       = "create-tag"
	CommandRenameTag       = "rename-tag"
	CommandDeleteTag       = "delete-tag"
	CommandCreateSelection = "create-selection"
)

// RegisterCommands installs this package's commands on h, applied by s.
func RegisterCommands(h *commands.Handler, s *Service) {
	h.Register(CommandSetDecision, func(body []byte) (commands.Operation, error) { return decodeSetDecision(s, body) })
	h.Register(CommandSetTags, func(body []byte) (commands.Operation, error) { return decodeSetTags(s, body) })
	h.Register(CommandCreateTag, func(body []byte) (commands.Operation, error) { return decodeCreateTag(s, body) })
	h.Register(CommandRenameTag, func(body []byte) (commands.Operation, error) { return decodeRenameTag(s, body) })
	h.Register(CommandDeleteTag, func(body []byte) (commands.Operation, error) { return decodeDeleteTag(s, body) })
	h.Register(CommandCreateSelection, func(body []byte) (commands.Operation, error) { return decodeCreateSelection(s, body) })
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

// newOp decodes body strictly into req and returns an operation whose
// canonical form is req re-encoded.
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

// entryIDs parses the opaque IDs of a request. A malformed ID is not_found,
// reported after the request's shape is checked, so a malformed request is
// invalid_request whatever its IDs. An archive member's ref ("m45") is
// invalid_request: a member has no decision or tags of its own, it reads
// its archive's (R2 design D7).
type entryIDs struct{ err error }

func (p *entryIDs) one(s string) domain.EntryID {
	if s == "" {
		return 0
	}
	id, err := domain.ParseEntryID(s)
	if err != nil {
		if ref, rerr := domain.ParseRef(s); rerr == nil && ref.IsMember() {
			err = domain.Errorf(domain.CodeInvalidRequest,
				"%s is a member of an archive, which is decided and tagged with its archive", s)
		}
		if p.err == nil {
			p.err = err
		}
		return -1 // named but unknown; never reaches the service
	}
	return id
}

func (p *entryIDs) all(ss []string) []domain.EntryID {
	if len(ss) == 0 {
		return nil
	}
	out := make([]domain.EntryID, len(ss))
	for i, s := range ss {
		out[i] = p.one(s)
	}
	return out
}

// set-decision: {"entry_id":"12","decision":"keep"}, or
// {"entry_ids":["12",…],"decision":"discard"}, or
// {"selection_id":"…","decision":"later"}; "inherit" clears.

type setDecisionRequest struct {
	EntryID     string   `json:"entry_id,omitempty"`
	EntryIDs    []string `json:"entry_ids,omitempty"`
	SelectionID string   `json:"selection_id,omitempty"`
	Decision    string   `json:"decision"`
}

type skippedJSON struct {
	EntryID string `json:"entry_id"`
	Path    string `json:"path"`
	PathB64 []byte `json:"path_b64"`
}

type setDecisionResponse struct {
	Applied      int           `json:"applied"`
	SkippedCount int           `json:"skipped_count"`
	Skipped      []skippedJSON `json:"skipped"`
}

func decodeSetDecision(s *Service, body []byte) (commands.Operation, error) {
	var (
		w   setDecisionRequest
		req SetDecision
	)
	check := func() error {
		var ids entryIDs
		req = SetDecision{EntryID: ids.one(w.EntryID), EntryIDs: ids.all(w.EntryIDs), SelectionID: w.SelectionID}
		if w.Decision == "inherit" {
			req.Inherit = true
		} else {
			req.Decision = domain.Decision(w.Decision)
		}
		if err := req.Validate(); err != nil {
			return err
		}
		return ids.err
	}
	return newOp(body, &w, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		res, err := s.SetDecision(ctx, tx.SQL(), req)
		if err != nil {
			return 0, nil, err
		}
		out := setDecisionResponse{Applied: res.Applied, SkippedCount: res.SkippedCount, Skipped: make([]skippedJSON, len(res.Skipped))}
		for i, sk := range res.Skipped {
			out.Skipped[i] = skippedJSON{EntryID: sk.EntryID.String(), Path: sk.Path, PathB64: sk.PathB64}
		}
		return http.StatusOK, out, nil
	})
}

// set-tags: {"entry_ids":[…]} or {"selection_id":"…"}, with "add" and
// "remove" tag IDs.

type setTagsRequest struct {
	EntryIDs    []string `json:"entry_ids,omitempty"`
	SelectionID string   `json:"selection_id,omitempty"`
	Add         []int64  `json:"add,omitempty"`
	Remove      []int64  `json:"remove,omitempty"`
}

type appliedResponse struct {
	Applied int `json:"applied"`
}

func decodeSetTags(s *Service, body []byte) (commands.Operation, error) {
	var (
		w   setTagsRequest
		req SetTags
	)
	check := func() error {
		var ids entryIDs
		req = SetTags{EntryIDs: ids.all(w.EntryIDs), SelectionID: w.SelectionID, Add: w.Add, Remove: w.Remove}
		if err := req.Validate(); err != nil {
			return err
		}
		return ids.err
	}
	return newOp(body, &w, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		n, err := s.SetTags(ctx, tx.SQL(), req)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, appliedResponse{Applied: n}, nil
	})
}

// create-tag {"name"}, rename-tag {"tag_id","name"}, delete-tag {"tag_id"}:
// each answers {"tag":{"id","name"}}.

type tagJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type tagResponse struct {
	Tag tagJSON `json:"tag"`
}

func tagResult(status int, t Tag, err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	return status, tagResponse{Tag: tagJSON{ID: t.ID, Name: t.Name}}, nil
}

type createTagRequest struct {
	Name string `json:"name"`
}

func decodeCreateTag(s *Service, body []byte) (commands.Operation, error) {
	var w createTagRequest
	check := func() error {
		_, err := tagName(w.Name)
		return err
	}
	return newOp(body, &w, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		t, err := s.CreateTag(ctx, tx.SQL(), w.Name)
		return tagResult(http.StatusCreated, t, err)
	})
}

type renameTagRequest struct {
	TagID int64  `json:"tag_id"`
	Name  string `json:"name"`
}

func decodeRenameTag(s *Service, body []byte) (commands.Operation, error) {
	var w renameTagRequest
	check := func() error {
		if err := validTagID(w.TagID); err != nil {
			return err
		}
		_, err := tagName(w.Name)
		return err
	}
	return newOp(body, &w, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		t, err := s.RenameTag(ctx, tx.SQL(), w.TagID, w.Name)
		return tagResult(http.StatusOK, t, err)
	})
}

type deleteTagRequest struct {
	TagID int64 `json:"tag_id"`
}

func decodeDeleteTag(s *Service, body []byte) (commands.Operation, error) {
	var w deleteTagRequest
	return newOp(body, &w, func() error { return validTagID(w.TagID) }, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		t, err := s.DeleteTag(ctx, tx.SQL(), w.TagID)
		return tagResult(http.StatusOK, t, err)
	})
}

func validTagID(id int64) error {
	if id <= 0 {
		return domain.Errorf(domain.CodeInvalidRequest, "tag_id is required")
	}
	return nil
}

// create-selection: {"query":{…search query fields…}}.

type createSelectionRequest struct {
	Query *search.Query `json:"query"`
}

type keptJSON struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

type selectionResponse struct {
	SelectionID string    `json:"selection_id"`
	Count       int64     `json:"count"`
	Bytes       int64     `json:"bytes"`
	Kept        keptJSON  `json:"kept"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func decodeCreateSelection(s *Service, body []byte) (commands.Operation, error) {
	var w createSelectionRequest
	check := func() error {
		if w.Query == nil {
			return domain.Errorf(domain.CodeInvalidRequest, "query is required")
		}
		return nil
	}
	return newOp(body, &w, check, func(ctx context.Context, tx *jobs.Tx) (int, any, error) {
		sel, err := s.CreateSelection(ctx, tx.SQL(), *w.Query)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, selectionResponse{
			SelectionID: sel.ID, Count: sel.Count, Bytes: sel.Bytes,
			Kept:      keptJSON{Count: sel.KeptCount, Bytes: sel.KeptBytes},
			ExpiresAt: sel.ExpiresAt.UTC(),
		}, nil
	})
}
