package organize

import (
	"bytes"
	"context"
	"unicode/utf8"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
)

// validName refuses a name no entry can take: empty, "." or "..", holding
// "/" or NUL, not UTF-8, or over 255 bytes.
func validName(name string) error {
	b := []byte(name)
	switch {
	case fsaccess.ValidateName("name", b) != nil:
		return domain.Errorf(domain.CodeInvalidRequest, `a name cannot be empty, "." or "..", or hold "/" or a NUL`)
	case !utf8.Valid(b):
		return domain.Errorf(domain.CodeInvalidRequest, "a name must be valid UTF-8")
	case len(b) > maxNameBytes:
		return domain.Errorf(domain.CodeInvalidRequest, "a name holds at most %d bytes, not %d", maxNameBytes, len(b))
	}
	return nil
}

// nameTaken refuses name in folder d when an entry other than except holds
// it there, present, or missing with the owner's intent (D9: 409
// name_taken, and no action).
func (p *plan) nameTaken(d dest, name []byte, except int64) error {
	children, err := p.childNames(d)
	if err != nil {
		return err
	}
	_, taken := children.holder(name, except)
	if !taken {
		if taken, err = index.MissingIntentAt(p.ctx, p.tx, p.src, joinPath(d.path, name)); err != nil {
			return err
		}
	}
	if taken {
		return domain.Errorf(domain.CodeNameTaken, "%q is already taken in %q", domain.DisplayName(name), domain.DisplayName(d.path))
	}
	return nil
}

// planRename plans plan-rename: one rename inside the entry's folder.
func (s *Service) planRename(ctx context.Context, tx *jobs.Tx, req planRenameRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	id, err := parseEntryID("entry_id", req.EntryID)
	if err != nil {
		return 0, nil, err
	}
	n, err := loadNode(ctx, q, id)
	if err != nil {
		return 0, nil, err
	}
	if n == nil {
		return 0, nil, notFound("entry", req.EntryID)
	}
	if n.parent == 0 {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "a source's top folder cannot be renamed here")
	}
	name := []byte(req.Name)
	if bytes.Equal(name, n.name) {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest, "the new name is the current one")
	}
	if err := s.checkSource(ctx, q, n.source); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, n.source, false)
	if err != nil {
		return 0, nil, err
	}
	if !p.sensitive && bytes.EqualFold(name, n.name) {
		return 0, nil, domain.Errorf(domain.CodeInvalidRequest,
			"only the letter case differs, and this disk does not tell letter case apart, so it cannot rename that in one step")
	}
	parent, err := loadNode(ctx, q, n.parent)
	if err != nil {
		return 0, nil, err
	}
	d := folderDest(parent)
	if err := p.nameTaken(d, name, n.id); err != nil {
		return 0, nil, err
	}
	if err := prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	if _, err := p.move(n, d, name, 0); err != nil {
		return 0, nil, err
	}
	return s.finish(ctx, tx, p, "rename", 0, 0)
}

// planCreateFolder plans plan-create-folder: one mkdir in a present folder.
func (s *Service) planCreateFolder(ctx context.Context, tx *jobs.Tx, req planCreateFolderRequest) (int, any, error) {
	q, now := tx.SQL(), tx.Now()
	parent, err := folderArg(ctx, q, "parent_id", req.ParentID)
	if err != nil {
		return 0, nil, err
	}
	if err := s.checkSource(ctx, q, parent.source); err != nil {
		return 0, nil, err
	}
	p, err := newPlan(ctx, q, parent.source, false)
	if err != nil {
		return 0, nil, err
	}
	d, name := folderDest(parent), []byte(req.Name)
	if err := p.nameTaken(d, name, 0); err != nil {
		return 0, nil, err
	}
	if err := prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	if _, err := p.mkdir(d, name); err != nil {
		return 0, nil, err
	}
	return s.finish(ctx, tx, p, "create_folder", parent.id, 0)
}
