package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"precious/internal/archive"
	"precious/internal/commands"
	"precious/internal/domain"
	"precious/internal/jobs"
	"precious/internal/sources"
)

// The command names (design Interfaces).
const (
	CommandStartHash = "start-hash"
	CommandCheckNow  = "check-now"
)

// RegisterCommands registers with h:
//
//   - start-hash {"source_id"}: 202 {"job_id","state","coalesced"}; 404
//     unknown_source; 409 source_offline.
//   - check-now {"entry_ids":["12","m45"]}: one or two folders, archive
//     files, or member folders; 202 {"jobs":[{"job_id","state","coalesced"}]},
//     one hash_now job per source; 400 for no ref, more than two, or a file;
//     404 not_found; 409 source_offline.
//
// Neither writes an audit event: neither changes an owner decision.
func RegisterCommands(h *commands.Handler, s *Service) {
	h.Register(CommandStartHash, func(body []byte) (commands.Operation, error) {
		var req startHashRequest
		if err := commands.DecodeStrict(body, &req); err != nil {
			return nil, err
		}
		if req.SourceID == "" {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "source_id is required")
		}
		return &startHashOp{req: req}, nil
	})
	h.Register(CommandCheckNow, func(body []byte) (commands.Operation, error) {
		var req checkNowRequest
		if err := commands.DecodeStrict(body, &req); err != nil {
			return nil, err
		}
		if len(req.EntryIDs) == 0 || len(req.EntryIDs) > 2 {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "entry_ids must name one or two folders")
		}
		refs := make([]domain.Ref, len(req.EntryIDs))
		for i, raw := range req.EntryIDs {
			ref, err := domain.ParseRef(raw)
			if err != nil {
				return nil, err
			}
			refs[i] = ref
		}
		return &checkNowOp{req: req, refs: refs}, nil
	})
}

func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // plain structs of strings always marshal
	}
	return b
}

type startHashRequest struct {
	SourceID domain.SourceID `json:"source_id"`
}

type startHashOp struct{ req startHashRequest }

func (o *startHashOp) Canonical() []byte             { return marshal(o.req) }
func (o *startHashOp) Prepare(context.Context) error { return nil }

func (o *startHashOp) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	if err := checkOnline(ctx, tx.SQL(), o.req.SourceID); err != nil {
		return 0, nil, err
	}
	rec, coalesced, err := tx.EnqueueOnce(hashSpec(o.req.SourceID))
	if err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, rec.Accepted(coalesced), nil
}

// checkOnline fails with unknown_source or source_offline unless src is
// online.
func checkOnline(ctx context.Context, tx *sql.Tx, src domain.SourceID) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM sources WHERE id = ?`, string(src)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeUnknownSource, "unknown source %q", src)
	}
	if err != nil {
		return err
	}
	if state != string(sources.StateOnline) {
		return domain.Errorf(domain.CodeSourceOffline, "source %q is %s", src, state)
	}
	return nil
}

type checkNowRequest struct {
	EntryIDs []string `json:"entry_ids"`
}

type checkNowOp struct {
	req  checkNowRequest
	refs []domain.Ref
}

func (o *checkNowOp) Canonical() []byte             { return marshal(o.req) }
func (o *checkNowOp) Prepare(context.Context) error { return nil }

// checkNowJobs is the check-now response.
type checkNowJobs struct {
	Jobs []jobs.Accepted `json:"jobs"`
}

func (o *checkNowOp) Apply(ctx context.Context, tx *jobs.Tx) (int, any, error) {
	bySource := map[domain.SourceID][]domain.EntryID{}
	var order []domain.SourceID
	for _, ref := range o.refs {
		src, id, err := resolveFolder(ctx, tx.SQL(), ref)
		if err != nil {
			return 0, nil, err
		}
		if _, ok := bySource[src]; !ok {
			order = append(order, src)
		}
		bySource[src] = append(bySource[src], id)
	}
	out := checkNowJobs{Jobs: []jobs.Accepted{}}
	for _, src := range order {
		if err := checkOnline(ctx, tx.SQL(), src); err != nil {
			return 0, nil, err
		}
		spec := jobs.Spec{Kind: KindHashNow, SourceID: src, ScopeKey: "hash_now:" + string(src),
			Payload: marshal(nowPayload{Entries: bySource[src]})}
		rec, coalesced, err := tx.EnqueueOnce(spec)
		if err != nil {
			return 0, nil, err
		}
		if coalesced {
			// The active job reads its payload again: add the folders.
			var p nowPayload
			if err := json.Unmarshal(rec.Payload, &p); err != nil {
				return 0, nil, err
			}
			for _, id := range bySource[src] {
				if !slices.Contains(p.Entries, id) {
					p.Entries = append(p.Entries, id)
				}
			}
			if _, err := tx.SQL().ExecContext(ctx, `UPDATE jobs SET payload = ? WHERE id = ?`,
				string(marshal(p)), int64(rec.ID)); err != nil {
				return 0, nil, err
			}
		}
		out.Jobs = append(out.Jobs, rec.Accepted(coalesced))
	}
	return http.StatusAccepted, out, nil
}

// resolveFolder returns the source and entry check-now reads for ref: a
// folder, an archive file, or a member folder (its archive). A file, a
// member file, or a missing entry is refused.
func resolveFolder(ctx context.Context, tx *sql.Tx, ref domain.Ref) (domain.SourceID, domain.EntryID, error) {
	id := ref.Entry
	if ref.IsMember() {
		var kind string
		var archiveID int64
		err := tx.QueryRowContext(ctx, `SELECT kind, archive_id FROM archive_members WHERE id = ?`, int64(ref.Member)).
			Scan(&kind, &archiveID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, domain.Errorf(domain.CodeNotFound, "member %s not found", ref)
		}
		if err != nil {
			return "", 0, err
		}
		if kind != string(domain.MemberDirectory) {
			return "", 0, domain.Errorf(domain.CodeInvalidRequest, "member %s is a %s, not a folder", ref, kind)
		}
		id = domain.EntryID(archiveID)
	}
	var (
		src, kind, state string
		name             []byte
	)
	err := tx.QueryRowContext(ctx, `SELECT source_id, kind, state, name FROM entries WHERE id = ?`, int64(id)).
		Scan(&src, &kind, &state, &name)
	if errors.Is(err, sql.ErrNoRows) || err == nil && state == "missing" {
		return "", 0, domain.Errorf(domain.CodeNotFound, "entry %s not found", ref)
	}
	if err != nil {
		return "", 0, err
	}
	switch {
	case kind == string(domain.EntryDirectory):
	case kind == string(domain.EntryFile) && isArchive(name):
	default:
		return "", 0, domain.Errorf(domain.CodeInvalidRequest, "entry %s is a %s, not a folder or an archive", ref, kind)
	}
	return domain.SourceID(src), id, nil
}

func isArchive(name []byte) bool {
	_, ok := archive.Classify(name)
	return ok
}
