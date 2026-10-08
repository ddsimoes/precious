package synthfs

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"syscall"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

var _ fsaccess.Writer = (*dir)(nil)

var (
	errForeignDir = errors.New("synthfs: destination folder is not from this FS")
	errGenerated  = errors.New("synthfs: generated entries and folders cannot be changed")
)

// The Writer methods follow the Linux backend (renameat2 with
// RENAME_NOREPLACE, mkdirat then fchmod, unlinkat with AT_REMOVEDIR, fsync,
// openat with O_CREAT|O_EXCL, unlinkat without flags, utimensat) and fail
// with the same errors, built by fsaccess.WriteError from the errno Linux
// would give, checked in the kernel's order:
//
//   - a folder whose root vanished, or a closed handle: unavailable;
//   - a folder no longer in its tree (removed or replaced): absent;
//   - two devices: ErrCrossDevice; a read-only device (its capabilities, its
//     FSInfo, or its mount row): ErrReadOnly;
//   - a missing name: absent; a mount boundary: unavailable (EBUSY);
//   - a folder moved into itself or below itself: fsaccess.ErrIntoItself;
//   - a taken name, compared as Lstat compares (regardless of letter case on
//     a case-insensitive device): ErrExist;
//   - a folder to unlink: fsaccess.ErrIsDir;
//   - a device given capabilities without NoReplaceRename: the rename fails
//     with ErrNoReplaceUnsupported after the checks above, as an old driver
//     does. A device without capabilities renames.
//
// SetModTime looks the name up first, as utimensat does: a missing name is
// absent, then a read-only device is ErrReadOnly, then a file marked
// Foreign is ErrPermission (EPERM).
//
// A renamed entry keeps its node, so its device, inode, size, content, and
// times stay; its change time and both folders' modification and change
// times advance. Mkdir creates an empty folder with its parent's permission
// bits. Rmdir removes only an empty folder. Sync checks the handle and does
// nothing else. CreateExclusive adds a regular file with explicit content
// and its parent's permission bits & 0666, never over a taken name. Unlink
// removes any explicit entry but a folder (fsaccess.ErrIsDir), and lowers
// the link count of a hard-linked file's other names. SetModTime sets the
// entry's own modification time (a symlink's, never its target's; a
// hard-linked file's, under every name) as its device stores it (see
// storedModTime) and advances its change time; nothing else changes.
// Generated entries and folders cannot be changed: the call fails with
// outcome unavailable.

// readOnly reports whether d's device is read-only. Callers hold fs.mu.
func (d *dir) readOnly() bool { return d.fs.readOnly(d.dev) }

// readOnly reports whether dev is read-only: by its capabilities, its
// FSInfo, or its mount row. Callers hold f.mu.
func (f *FS) readOnly(dev uint64) bool {
	if d := f.devices[dev]; d != nil && d.caps != nil && d.caps.ReadOnly {
		return true
	}
	info := f.fsinfo[dev]
	return info.ReadOnly || (info.Mount != nil && info.Mount.ReadOnly)
}

// storedModTime is the modification time dev stores when t is set, which
// Lstat reports back as it is (see FS.present): first clamped to its
// filesystem type's range, as Linux's timestamp_truncate does without an
// error (see clampModTime); then, for a device given capabilities, with
// LocalTime, the wall time of t in the zone dev is mounted with, written as
// a UTC reading, and truncated to its TimeResolution. A device without
// capabilities stores t exactly, but for the clamp. Callers hold f.mu.
func (f *FS) storedModTime(dev uint64, t time.Time) time.Time {
	t = t.Round(0)
	d := f.devices[dev]
	loc := time.UTC
	if d != nil && d.zone != nil {
		loc = d.zone
	}
	t = clampModTime(f.fsTypeOf(dev), loc, t)
	if d == nil || d.caps == nil {
		return t
	}
	if d.caps.LocalTime {
		w := t.In(loc)
		t = time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), time.UTC)
	}
	if r := d.caps.TimeResolution; r > 0 {
		t = t.Truncate(r)
	}
	return t
}

// fsTypeOf is dev's filesystem type: its volume's, else its mount row's, else
// "". Callers hold f.mu.
func (f *FS) fsTypeOf(dev uint64) string {
	if d := f.devices[dev]; d != nil && d.volume != nil && d.volume.FSType != "" {
		return d.volume.FSType
	}
	if m := f.fsinfo[dev].Mount; m != nil {
		return m.FSType
	}
	return ""
}

// clampModTime is t as Linux stores it on a filesystem of fsType mounted in
// zone loc: clamped to its superblock's s_time_min and s_time_max. vfat's
// range is 1980-01-01 00:00:00 to 2107-12-31 23:59:58 local time, exFAT's
// 1980-01-01 00:00:00 to 2107-12-31 23:59:59 UTC, and ext2/3/4's and XFS's
// starts at 1901-12-13T20:45:52Z (their large-inode ends lie past what a
// nanosecond time holds). Any other type, or none, stores t as given.
func clampModTime(fsType string, loc *time.Location, t time.Time) time.Time {
	var lo, hi time.Time
	switch fsType {
	case "vfat":
		lo, hi = time.Date(1980, 1, 1, 0, 0, 0, 0, loc), time.Date(2107, 12, 31, 23, 59, 58, 0, loc)
	case "exfat":
		lo, hi = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2107, 12, 31, 23, 59, 59, 0, time.UTC)
	case "ext2", "ext3", "ext4", "xfs":
		return maxTime(t, time.Unix(math.MinInt32, 0))
	default:
		return t
	}
	return minTime(maxTime(t, lo), hi)
}

func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func minTime(a, b time.Time) time.Time {
	if a.After(b) {
		return b
	}
	return a
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

func (d *dir) CreateExclusive(name, data []byte) error {
	const op = "CreateExclusive"
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
	c := &Node{fs: f, name: bytes.Clone(name), kind: domain.EntryFile, ino: f.allocIno(1), failAfter: -1,
		perm: d.node.info().Mode.Perm() & 0o666, hasPerm: true,
		content: bytes.Clone(data), hasContent: true, size: int64(len(data))}
	c.changed()
	d.node.attach(c)
	d.node.changed()
	return nil
}

func (d *dir) Unlink(name []byte) error {
	const op = "Unlink"
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
	c, _, isDir, found := d.child(name)
	switch {
	case !found:
		return fsaccess.WriteError(op, name, syscall.ENOENT)
	case c == nil && isDir, c != nil && c.kind == domain.EntryDirectory:
		return fsaccess.WriteError(op, name, syscall.EISDIR)
	case c == nil:
		return &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: errGenerated}
	case c.boundaryFrom(d.dev):
		return fsaccess.WriteError(op, name, syscall.EBUSY)
	}
	d.node.detach(c)
	// 1+links counts a file's names; its other names now report one fewer.
	file := c
	if c.inode != nil {
		file = c.inode
	}
	if file.links > 0 {
		file.links--
		file.touch()
	}
	d.node.changed()
	return nil
}

func (d *dir) SetModTime(name []byte, t time.Time) error {
	const op = "SetModTime"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return err
	}
	f := d.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := d.writable(op, name); err != nil {
		return err
	}
	c, _, _, found := d.child(name)
	switch {
	case !found:
		return fsaccess.WriteError(op, name, syscall.ENOENT)
	case c == nil:
		return &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeUnavailable, Err: errGenerated}
	case f.readOnly(c.devOf()):
		return fsaccess.WriteError(op, name, syscall.EROFS)
	}
	file := c
	if c.inode != nil {
		file = c.inode
	}
	if file.foreign {
		return fsaccess.WriteError(op, name, syscall.EPERM)
	}
	file.mtime = f.storedModTime(file.devOf(), t)
	file.touch()
	return nil
}
