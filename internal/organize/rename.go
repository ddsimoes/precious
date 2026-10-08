package organize

import (
	"bytes"
	"context"
	"strings"
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

// windowsNames reports whether a filesystem type holds only names Windows
// allows: FAT and exFAT, and NTFS through any of its Linux drivers
// (ntfs-3g is fuseblk). Their drivers refuse the rest, or silently drop a
// trailing dot or space, which would leave the index naming an entry the
// disk does not hold.
func windowsNames(fsType string) bool {
	switch fsType {
	case "vfat", "exfat", "ntfs3", "ntfs", "fuseblk":
		return true
	}
	return strings.HasPrefix(fsType, "fuseblk.")
}

// holdable refuses a new name the source's filesystem cannot hold (design
// V3): on a FAT-family or NTFS disk, any of " * : < > ? \ |, a control
// character, or a trailing space or dot.
func (p *plan) holdable(name []byte) error {
	if !windowsNames(p.fsType) {
		return nil
	}
	reserved := func(r rune) bool { return r < 0x20 || r == 0x7f || strings.ContainsRune(`"*:<>?\|`, r) }
	if bytes.IndexFunc(name, reserved) >= 0 {
		return domain.Errorf(domain.CodeInvalidRequest,
			`this disk (%s) cannot hold a name with any of " * : < > ? \ | or a control character; choose another name`,
			p.fsType)
	}
	if last := name[len(name)-1]; last == ' ' || last == '.' {
		return domain.Errorf(domain.CodeInvalidRequest,
			"this disk (%s) cannot hold a name ending in a space or a dot; choose another name", p.fsType)
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
	if err := frozen("entry_id", req.EntryID, n); err != nil {
		return 0, nil, err
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
	if err := p.holdable(name); err != nil {
		return 0, nil, err
	}
	parent, err := loadNode(ctx, q, n.parent)
	if err != nil {
		return 0, nil, err
	}
	d := folderDest(parent)
	if err := reserved(d, name); err != nil {
		return 0, nil, err
	}
	if err := p.nameTaken(d, name, n.id); err != nil {
		return 0, nil, err
	}
	if err := Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	if _, err := p.move(n, d, name, 0); err != nil {
		return 0, nil, err
	}
	return s.finish(ctx, tx, p, "rename", 0, 0)
}

// reserved refuses the quarantine's name at a source's top folder (r4 D1,
// D13): only a cleanup plan makes that folder.
func reserved(d dest, name []byte) error {
	if len(d.path) == 0 && string(name) == index.QuarantineName {
		return domain.Errorf(domain.CodeInvalidRequest,
			"%q at the top of a source is reserved for Precious's quarantine; choose another name", index.QuarantineName)
	}
	return nil
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
	if err := p.holdable(name); err != nil {
		return 0, nil, err
	}
	if err := reserved(d, name); err != nil {
		return 0, nil, err
	}
	if err := p.nameTaken(d, name, 0); err != nil {
		return 0, nil, err
	}
	if err := Prune(ctx, q, now); err != nil {
		return 0, nil, err
	}
	if _, err := p.mkdir(d, name); err != nil {
		return 0, nil, err
	}
	return s.finish(ctx, tx, p, "create_folder", parent.id, 0)
}
