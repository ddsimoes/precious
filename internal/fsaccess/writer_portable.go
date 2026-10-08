package fsaccess

import (
	"bytes"
	"time"
)

var _ Writer = (*portableDir)(nil)

// The portable backend has no rename that refuses to replace (r3 design D2:
// macOS and Windows get theirs in R8), so it writes nothing: every Writer
// method validates its names and fails with ErrNoReplaceUnsupported.

func (d *portableDir) RenameNoReplace(name []byte, _ Dir, newName []byte) error {
	if err := checkName(opRename, name); err != nil {
		return err
	}
	if err := checkName(opRename, newName); err != nil {
		return err
	}
	return &Error{Op: opRename, Name: bytes.Clone(name), Err: ErrNoReplaceUnsupported}
}

func (d *portableDir) Mkdir(name []byte) error {
	if err := checkName(opMkdir, name); err != nil {
		return err
	}
	return &Error{Op: opMkdir, Name: bytes.Clone(name), Err: ErrNoReplaceUnsupported}
}

func (d *portableDir) Rmdir(name []byte) error {
	if err := checkName(opRmdir, name); err != nil {
		return err
	}
	return &Error{Op: opRmdir, Name: bytes.Clone(name), Err: ErrNoReplaceUnsupported}
}

func (d *portableDir) Sync() error {
	return &Error{Op: opSync, Name: d.self.Name, Err: ErrNoReplaceUnsupported}
}

func (d *portableDir) CreateExclusive(name, _ []byte) error {
	if err := checkName(opCreate, name); err != nil {
		return err
	}
	return &Error{Op: opCreate, Name: bytes.Clone(name), Err: ErrNoReplaceUnsupported}
}

func (d *portableDir) Unlink(name []byte) error {
	if err := checkName(opUnlink, name); err != nil {
		return err
	}
	return &Error{Op: opUnlink, Name: bytes.Clone(name), Err: ErrNoReplaceUnsupported}
}

func (d *portableDir) SetModTime(name []byte, _ time.Time) error {
	if err := checkName(opSetModTime, name); err != nil {
		return err
	}
	return &Error{Op: opSetModTime, Name: bytes.Clone(name), Err: ErrNoReplaceUnsupported}
}
