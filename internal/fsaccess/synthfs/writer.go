package synthfs

import (
	"bytes"
	"errors"
	"fmt"
	"syscall"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

var _ fsaccess.Writer = (*dir)(nil)

var (
	errForeignDir = errors.New("synthfs: destination folder is not from this FS")
	errGenerated  = errors.New("synthfs: generated entries and folders cannot be changed")
)

// The Writer methods follow the Linux backend (renameat2 with
// RENAME_NOREPLACE, mkdirat then fchmod, unlinkat with AT_REMOVEDIR, fsync)
// and fail with the same errors, built by fsaccess.WriteError from the errno
// Linux would give, checked in the kernel's order:
//
//   - a folder whose root vanished, or a closed handle: unavailable;
//   - a folder no longer in its tree (removed or replaced): absent;
//   - two devices: ErrCrossDevice; a read-only device (its capabilities, its
//     FSInfo, or its mount row): ErrReadOnly;
//   - a missing name: absent; a mount boundary: unavailable (EBUSY);
//   - a folder moved into itself or below itself: fsaccess.ErrIntoItself;
//   - a taken name, compared as Lstat compares (regardless of letter case on
//     a case-insensitive device): ErrExist;
//   - a device given capabilities without NoReplaceRename: the rename fails
//     with ErrNoReplaceUnsupported after the checks above, as an old driver
//     does. A device without capabilities renames.
//
// A renamed entry keeps its node, so its device, inode, size, content, and
// times stay; its change time and both folders' modification and change
// times advance. Mkdir creates an empty folder with its parent's permission
// bits. Rmdir removes only an empty folder. Sync checks the handle and does
// nothing else. Generated entries and folders cannot be changed: the call
// fails with outcome unavailable.

// readOnly reports whether d's device is read-only. Callers hold fs.mu.
func (d *dir) readOnly() bool {
	if dev := d.fs.devices[d.dev]; dev != nil && dev.caps != nil && dev.caps.ReadOnly {
		return true
	}
	info := d.fs.fsinfo[d.dev]
	return info.ReadOnly || (info.Mount != nil && info.Mount.ReadOnly)
}

// attached reports whether n is still reachable from its tree's root.
// Callers hold fs.mu.
func attached(n *Node) bool {
	for ; n.parent != nil; n = n.parent {
		if n.parent.index[string(n.name)] != n {
			return false
		}
	}
	return true
}

// writable runs the checks every write on d shares and returns the error to
// report, or nil. Callers hold fs.mu.
func (d *dir) writable(op string, name []byte) error {
	if err := d.check(op); err != nil {
		return err
	}
	if d.node == nil {
		return &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: errGenerated}
	}
	if !attached(d.node) {
		return fsaccess.WriteError(op, name, syscall.ENOENT)
	}
	return nil
}

// detach removes the explicit child c from n. Callers hold fs.mu.
func (n *Node) detach(c *Node) {
	delete(n.index, string(c.name))
	for i, x := range n.children {
		if x == c {
			n.children = append(n.children[:i], n.children[i+1:]...)
			break
		}
	}
}

// attach adds c as an explicit child of n. Callers hold fs.mu.
func (n *Node) attach(c *Node) {
	if n.index == nil {
		n.index = make(map[string]*Node)
	}
	c.parent = n
	n.index[string(c.name)] = c
	n.children = append(n.children, c)
}

// changed advances a folder's change time and sets its modification time to
// it, as adding or removing a name does. Callers hold fs.mu.
func (n *Node) changed() {
	n.touch()
	n.mtime = n.ctime
}

func (d *dir) RenameNoReplace(name []byte, to fsaccess.Dir, newName []byte) error {
	const op = "RenameNoReplace"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return err
	}
	if err := fsaccess.ValidateName(op, newName); err != nil {
		return err
	}
	t, ok := to.(*dir)
	if !ok || t.fs != d.fs {
		return &fsaccess.Error{Op: op, Name: bytes.Clone(name), Err: errForeignDir}
	}
	f := d.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := d.writable(op, name); err != nil {
		return err
	}
	if err := t.writable(op, name); err != nil {
		return err
	}
	fail := func(errno error) error { return fsaccess.WriteError(op, name, errno) }
	if d.dev != t.dev {
		return fail(syscall.EXDEV)
	}
	if d.readOnly() {
		return fail(syscall.EROFS)
	}
	c, _, _, found := d.child(name)
	switch {
	case !found:
		return fail(syscall.ENOENT)
	case c == nil:
		return &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: errGenerated}
	case c.boundaryFrom(d.dev):
		return fail(syscall.EBUSY)
	}
	if c.kind == domain.EntryDirectory {
		for x := t.node; x != nil; x = x.parent {
			if x == c {
				return &fsaccess.Error{Op: op, Name: bytes.Clone(name),
					Err: fmt.Errorf("%w (%w)", fsaccess.ErrIntoItself, syscall.EINVAL)}
			}
		}
	}
	if _, _, _, taken := t.child(newName); taken {
		return fail(syscall.EEXIST)
	}
	if dev := f.devices[d.dev]; dev != nil && dev.caps != nil && !dev.caps.NoReplaceRename {
		return fail(syscall.EINVAL)
	}
	d.node.detach(c)
	c.name = bytes.Clone(newName)
	t.node.attach(c)
	if c.inode != nil {
		c.inode.touch()
	} else {
		c.touch()
	}
	d.node.changed()
	if t.node != d.node {
		t.node.changed()
	}
	return nil
}

func (d *dir) Mkdir(name []byte) error {
	const op = "Mkdir"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return err
	}
	f := d.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := d.writable(op, name); err != nil {
		return err
	}
	if d.readOnly() {
		return fsaccess.WriteError(op, name, syscall.EROFS)
	}
	if _, _, _, taken := d.child(name); taken {
		return fsaccess.WriteError(op, name, syscall.EEXIST)
	}
	perm := d.node.info().Mode.Perm()
	c := &Node{fs: f, name: bytes.Clone(name), kind: domain.EntryDirectory, ino: f.allocIno(1), failAfter: -1,
		perm: perm, hasPerm: true}
	c.changed()
	d.node.attach(c)
	d.node.changed()
	return nil
}

func (d *dir) Rmdir(name []byte) error {
	const op = "Rmdir"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return err
	}
	f := d.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := d.writable(op, name); err != nil {
		return err
	}
	if d.readOnly() {
		return fsaccess.WriteError(op, name, syscall.EROFS)
	}
	c, _, _, found := d.child(name)
	switch {
	case !found:
		return fsaccess.WriteError(op, name, syscall.ENOENT)
	case c == nil:
		return &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: errGenerated}
	case c.kind != domain.EntryDirectory:
		return fsaccess.WriteError(op, name, syscall.ENOTDIR)
	case c.boundaryFrom(d.dev):
		return fsaccess.WriteError(op, name, syscall.EBUSY)
	case len(c.children) > 0 || (c.gen != nil && c.gen.width() > 0):
		return fsaccess.WriteError(op, name, syscall.ENOTEMPTY)
	}
	d.node.detach(c)
	d.node.changed()
	return nil
}

func (d *dir) Sync() error {
	d.fs.mu.RLock()
	defer d.fs.mu.RUnlock()
	return d.check("Sync")
}
