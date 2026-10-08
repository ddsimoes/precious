// Package synthfs is an in-memory fsaccess.FS for tests (§13.1, design D3).
//
// Trees are built with a small builder API on *Node. Large subtrees are
// generated lazily from their index (Generate, Generated), so a 100,000-entry
// fixture costs nothing until it is listed, and a listing never materialises
// more entries than the batch it returns. The FS also simulates failures:
// unreadable directories and files, failing lstats, listings that fail after N
// entries, reads that fail at an offset, and a source root that vanishes.
//
// Each device can be given a volume identity (SetVolume) and capabilities
// (SetCapabilities), which then shape what it reports: names looked up
// without regard to letter case, modification times truncated to the time
// resolution and stored in local time, and new inode numbers when it is
// mounted again (Remount, Mount) without stable identity. Unmount and Mount
// change the mount table, moving the device's roots with its mount point.
//
// Regular files have content (M4 design D5): explicit bytes (Content), or
// bytes generated from a seed (Seed; by default the inode number, so distinct
// files differ). Generated content is computed per read from its offset, so a
// 1 GiB file costs nothing until it is read, and a read allocates nothing.
// Patch overwrites a range in place. Every entry has a change time (design
// D6) from a per-filesystem clock that starts at DefaultModTime and advances
// by one nanosecond on every change, so tests never depend on the wall clock.
//
// Semantics mirror the os.Root backend: names are validated with
// fsaccess.ValidateName, Lstat sets MountBoundary for directories and regular
// files on another device or marked as mount points, OpenDir refuses
// non-directories and boundaries and fails with changed_during_observation
// when the entry differs from the expected one, OpenFile does the same for
// regular files and also compares size, modification time, and change time,
// and every failure is an *fsaccess.Error with an outcome. Builder methods
// panic on misuse; they may be called while handles are in use (for example
// from an instrument hook), and an open file reads its content as it is at
// the time of each read.
//
// Directories implement fsaccess.Writer with the Linux backend's semantics
// and errors (see writer.go): a rename keeps the entry's node and identity
// and never replaces a taken name, Mkdir gives the new folder its parent's
// permission bits, Rmdir removes only empty folders, and Sync does nothing.
package synthfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// DefaultModTime is the modification time of nodes built without one.
// Generated entries use DefaultModTime plus their inode number in seconds.
var DefaultModTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// DefaultFSType is the statfs type reported for devices without SetFSInfo
// (EXT4_SUPER_MAGIC).
const DefaultFSType int64 = 0xEF53

// FS is the synthetic filesystem. The zero value is not usable; call New.
type FS struct {
	mu      sync.RWMutex
	roots   map[string]*rootState
	nextIno uint64
	nextDev uint64
	fsinfo  map[uint64]fsaccess.FSInfo
	devices map[uint64]*device
	// clock is the latest change time handed out; it only moves forward.
	clock time.Time

	maxBatch atomic.Int64
	listed   atomic.Int64
}

// device is what the volume knobs set for one device.
type device struct {
	// volume and caps replace the weak path volume and the unknown set.
	volume *fsaccess.Volume
	caps   *fsaccess.Capabilities
	// zone is the time zone a local-time device is mounted with (nil: UTC).
	zone *time.Location
	// point is the mount point set by Mount ("" derives it).
	point     string
	unmounted bool
	// inoShift is added to every inode number reported for the device; a
	// remount without stable identity moves it.
	inoShift uint64
}

type rootState struct {
	node     *Node
	vanished bool
}

var _ fsaccess.FS = (*FS)(nil)

// New returns an empty synthetic filesystem.
func New() *FS {
	return &FS{
		roots:   make(map[string]*rootState),
		nextIno: 2,
		nextDev: 100,
		fsinfo:  make(map[uint64]fsaccess.FSInfo),
		devices: make(map[uint64]*device),
		clock:   DefaultModTime,
	}
}

func (f *FS) allocIno(n uint64) uint64 {
	ino := f.nextIno
	f.nextIno += n
	return ino
}

// Root creates the source root directory at the absolute path, on a fresh
// device, and returns it. Calling Root again for the same path replaces the
// tree with a new one of a different identity (another disk at the same path);
// handles opened on the old tree keep working on it.
func (f *FS) Root(path string) *Node {
	if !filepath.IsAbs(path) {
		panic("synthfs: root path must be absolute: " + path)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := &Node{fs: f, name: []byte(filepath.Base(path)), kind: domain.EntryDirectory, dev: f.nextDev, ino: f.allocIno(1), failAfter: -1}
	n.touch()
	f.nextDev++
	f.roots[filepath.Clean(path)] = &rootState{node: n}
	return n
}

func (f *FS) rootState(path string) *rootState {
	rs, ok := f.roots[filepath.Clean(path)]
	if !ok {
		panic("synthfs: no root at " + path)
	}
	return rs
}

// Vanish makes the root at path disappear: OpenRoot fails with outcome
// unavailable, and every call on a handle opened beneath it (except Self and
// Close) fails with outcome unavailable.
func (f *FS) Vanish(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rootState(path).vanished = true
}

// Reattach undoes Vanish: the same tree, with the same identity, is back.
func (f *FS) Reattach(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rootState(path).vanished = false
}

// SetFSInfo sets the statfs and mount facts FSInfo reports for directories on
// dev (its Dev field is overwritten with dev). Without it, FSInfo reports
// DefaultFSType, FSID = dev, writable, and no mount row.
func (f *FS) SetFSInfo(dev uint64, info fsaccess.FSInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info.Mount != nil {
		m := *info.Mount
		info.Mount = &m
	}
	f.fsinfo[dev] = info
}

// device returns dev's knob state, creating it. Callers hold f.mu for writing.
func (f *FS) device(dev uint64) *device {
	d, ok := f.devices[dev]
	if !ok {
		d = &device{}
		f.devices[dev] = d
	}
	return d
}

// SetVolume makes Mounts report v as the identity of dev's mount, instead of
// a weak path volume known by its mount point.
func (f *FS) SetVolume(dev uint64, v fsaccess.Volume) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device(dev).volume = &v
}

// SetCapabilities makes Capabilities report c for paths on dev's mount
// (read-only also when the mount is), and makes the entries on dev behave as
// c describes:
//
//   - without CaseSensitive, a name is looked up regardless of letter case
//     (an exact match first), and Lstat and OpenDir report the name asked for,
//     as the operating system does;
//   - reported modification times are truncated to TimeResolution;
//   - with LocalTime, a modification time is stored as its UTC wall-clock
//     reading and read back in the zone the device is mounted with
//     (SetTimeZone);
//   - without StableIdentity, Remount and Mount give every entry a new inode
//     number.
//
// A device without SetCapabilities reports the unknown set but keeps exact
// names and times as built; only Remount and Mount follow the unknown set's
// lack of stable identity.
func (f *FS) SetCapabilities(dev uint64, c fsaccess.Capabilities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device(dev).caps = &c
}

// SetTimeZone sets the zone a local-time device is mounted with (UTC until
// set). Moving it by an hour, as a daylight-saving change between mounts
// does, shifts every reported modification time on dev by that hour. It has
// no effect on a device whose capabilities lack LocalTime.
func (f *FS) SetTimeZone(dev uint64, loc *time.Location) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device(dev).zone = loc
}

// remountInoStride separates the inode numbers of successive mounts of a
// device without stable identity; built trees stay far below it.
const remountInoStride = 1 << 40

// Remount models dev being unmounted and mounted again at the same point.
// When its capabilities lack stable identity, every entry on it, generated
// ones included, gets a new inode number; names, kinds, sizes, times, and
// content stay. Handles opened before see the new numbers too.
func (f *FS) Remount(dev uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remount(dev)
}

// remount renumbers dev's entries unless it has stable identity. Callers hold
// f.mu for writing.
func (f *FS) remount(dev uint64) {
	if !f.capsOf(dev).StableIdentity {
		f.device(dev).inoShift += remountInoStride
	}
}

// Unmount removes dev from the mount table, as unplugging a disk does: Mounts
// no longer lists it, OpenRoot of a root on it fails with outcome
// unavailable, and so does every call on a handle opened beneath one (except
// Self and Close), until Mount brings it back.
func (f *FS) Unmount(dev uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.device(dev).unmounted = true
}

// Mount mounts dev at the absolute point: after Unmount, or to move it while
// mounted. Every root on dev moves with the mount point (a root at
// <old point>/rel is now at <point>/rel, and a root at the old point is now
// at point), Mounts lists dev at point, and its entries are renumbered as by
// Remount. A weak path volume is then known by the new point.
func (f *FS) Mount(dev uint64, point string) {
	if !filepath.IsAbs(point) {
		panic("synthfs: mount point must be absolute: " + point)
	}
	point = filepath.Clean(point)
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.pointOf(dev)
	moved := make(map[string]*rootState)
	for path, rs := range f.roots {
		if rs.node.devOf() != dev {
			continue
		}
		rel, err := filepath.Rel(old, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			panic("synthfs: root " + path + " is not under its mount point " + old)
		}
		moved[filepath.Join(point, rel)] = rs
		delete(f.roots, path)
	}
	for path, rs := range moved {
		if _, taken := f.roots[path]; taken {
			panic("synthfs: Mount would move a root onto another at " + path)
		}
		rs.node.name = []byte(filepath.Base(path))
		f.roots[path] = rs
	}
	d := f.device(dev)
	d.point, d.unmounted = point, false
	f.remount(dev)
}

// capsOf returns dev's capabilities: those set, else the unknown set.
// Callers hold f.mu.
func (f *FS) capsOf(dev uint64) fsaccess.Capabilities {
	if d := f.devices[dev]; d != nil && d.caps != nil {
		return *d.caps
	}
	return fsaccess.UnknownCapabilities(false)
}

// gone reports a root that vanished or whose device is unmounted. Callers
// hold f.mu.
func (f *FS) gone(rs *rootState) bool {
	d := f.devices[rs.node.devOf()]
	return rs.vanished || (d != nil && d.unmounted)
}

// MaxBatch returns the largest number of entries any single ReadBatch has
// produced: the most directory entries this FS ever held materialised at once.
func (f *FS) MaxBatch() int { return int(f.maxBatch.Load()) }

// EntriesListed returns the total number of entries returned by ReadBatch.
func (f *FS) EntriesListed() int64 { return f.listed.Load() }

func (f *FS) noteBatch(n int) {
	f.listed.Add(int64(n))
	for {
		cur := f.maxBatch.Load()
		if int64(n) <= cur || f.maxBatch.CompareAndSwap(cur, int64(n)) {
			return
		}
	}
}

// OpenRoot opens the root built with Root.
func (f *FS) OpenRoot(path string) (fsaccess.Dir, error) {
	const op = "OpenRoot"
	if !filepath.IsAbs(path) {
		return nil, &fsaccess.Error{Op: op, Name: []byte(path), Err: errors.New("synthfs: root path is not absolute")}
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	rs, ok := f.roots[filepath.Clean(path)]
	if !ok || f.gone(rs) {
		return nil, &fsaccess.Error{Op: op, Name: []byte(path), Outcome: domain.OutcomeUnavailable, Err: syscall.ENOENT}
	}
	n := rs.node
	if n.unreadable {
		return nil, &fsaccess.Error{Op: op, Name: []byte(path), Outcome: domain.OutcomeUnreadable, Err: syscall.EACCES}
	}
	return &dir{fs: f, root: rs, node: n, gen: n.gen, dev: n.devOf(), self: n.info()}, nil
}

// Mounts returns one mount per device: the device of every root that has
// not vanished, every other device given SetFSInfo, and every device given a
// point by Mount, except unmounted ones. A device is mounted at the point
// Mount set; else at its SetFSInfo mount row's point; else, for a root's
// device, at the root's path (the shortest, when several roots share it);
// else at "/". The root and filesystem type come from the SetFSInfo mount row
// ("/" and none without one), and the mount is read-only when SetFSInfo says
// so. The volume is the one SetVolume gave, else a weak path volume known by
// the mount point. Mounts are ordered by device.
func (f *FS) Mounts() ([]fsaccess.Mount, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	rows := f.table()
	mounts := make([]fsaccess.Mount, len(rows))
	for i, r := range rows {
		mounts[i] = r.mount
	}
	return mounts, nil
}

// mounted is one row of the synthetic mount table.
type mounted struct {
	dev   uint64
	mount fsaccess.Mount
}

// table returns the current mount table, ordered by device. Callers hold
// f.mu.
func (f *FS) table() []mounted {
	listed := make(map[uint64]bool) // false: a root on the device vanished
	for _, rs := range f.roots {
		dev := rs.node.devOf()
		ok, seen := listed[dev]
		listed[dev] = (ok || !seen) && !rs.vanished
	}
	for dev := range f.fsinfo {
		if _, seen := listed[dev]; !seen {
			listed[dev] = true
		}
	}
	for dev, d := range f.devices {
		if _, seen := listed[dev]; !seen && d.point != "" {
			listed[dev] = true
		}
	}
	var rows []mounted
	for _, dev := range slices.Sorted(maps.Keys(listed)) {
		if d := f.devices[dev]; !listed[dev] || (d != nil && d.unmounted) {
			continue
		}
		rows = append(rows, mounted{dev: dev, mount: f.mountOf(dev)})
	}
	return rows
}

// pointOf returns where dev is mounted, by the rules of Mounts. Callers hold
// f.mu.
func (f *FS) pointOf(dev uint64) string {
	if d := f.devices[dev]; d != nil && d.point != "" {
		return d.point
	}
	if m := f.fsinfo[dev].Mount; m != nil {
		return m.MountPoint
	}
	point := ""
	for path, rs := range f.roots {
		if rs.node.devOf() == dev && (point == "" || len(path) < len(point) || (len(path) == len(point) && path < point)) {
			point = path
		}
	}
	if point == "" {
		return "/"
	}
	return point
}

// mountOf returns dev's mount. Callers hold f.mu.
func (f *FS) mountOf(dev uint64) fsaccess.Mount {
	info := f.fsinfo[dev]
	point, root, fsType, readOnly := f.pointOf(dev), "/", "", info.ReadOnly
	if m := info.Mount; m != nil {
		root, fsType, readOnly = m.Root, m.FSType, readOnly || m.ReadOnly
	}
	v := fsaccess.Volume{Kind: fsaccess.VolumePath, ID: point, FSType: fsType, DeviceKey: "mount:" + point}
	if d := f.devices[dev]; d != nil && d.volume != nil {
		v = *d.volume
	}
	return fsaccess.Mount{Point: point, Root: []byte(root), Volume: v, ReadOnly: readOnly}
}

// Capabilities returns the capabilities of the device mounted at the deepest
// mount point of Mounts containing path: those SetCapabilities gave, else the
// unknown set, read-only also when the mount is. A path on no mount gets the
// unknown set.
func (f *FS) Capabilities(path string) (fsaccess.Capabilities, error) {
	if !filepath.IsAbs(path) {
		return fsaccess.Capabilities{}, &fsaccess.Error{Op: "Capabilities", Name: []byte(path), Err: errors.New("synthfs: path is not absolute")}
	}
	path = filepath.Clean(path)
	f.mu.RLock()
	defer f.mu.RUnlock()
	var best *mounted
	rows := f.table()
	for i := range rows {
		r := &rows[i]
		if containsPath(r.mount.Point, path) && (best == nil || len(r.mount.Point) >= len(best.mount.Point)) {
			best = r
		}
	}
	if best == nil {
		return fsaccess.UnknownCapabilities(false), nil
	}
	caps := f.capsOf(best.dev)
	caps.ReadOnly = caps.ReadOnly || best.mount.ReadOnly
	return caps, nil
}

// containsPath reports whether the absolute path p is point or lies below it.
func containsPath(point, p string) bool {
	return point == "/" || p == point || strings.HasPrefix(p, point+"/")
}

// Node is an explicitly built entry. Methods that add children return the
// child; modifiers return the receiver for chaining.
type Node struct {
	fs      *FS
	parent  *Node
	name    []byte
	kind    domain.EntryKind
	size    int64
	mtime   time.Time
	perm    fs.FileMode
	hasPerm bool
	dev     uint64 // 0 inherits the parent's device
	ino     uint64
	link    []byte
	// blocks overrides the reported st_blocks when hasBlocks is set.
	blocks    int64
	hasBlocks bool
	// inode is the file this name is a hard link to (nil: its own inode).
	inode *Node
	// links counts the hard links added to this file with HardLink.
	links int
	// nlink overrides the reported link count when hasNlink is set.
	nlink    uint64
	hasNlink bool
	// ctime is the change time; touch advances it.
	ctime time.Time

	// Content of a regular file: explicit bytes (zero beyond them up to the
	// size) when hasContent, else bytes generated from seed (from the inode
	// number unless hasSeed); then patches, applied in order.
	content    []byte
	hasContent bool
	seed       uint64
	hasSeed    bool
	patches    []patch

	children []*Node
	index    map[string]*Node
	gen      *region

	unreadable bool
	mountPoint bool
	hideKind   bool
	lstatFail  domain.AccessOutcome
	failAfter  int // -1: the listing does not fail
	failWith   domain.AccessOutcome
	// readFail makes reads reaching offset readFailAt fail ("" never).
	readFail   domain.AccessOutcome
	readFailAt int64
}

// patch is an in-place overwrite of a file's content at off.
type patch struct {
	off int64
	b   []byte
}

// touch advances n's change time on the FS clock, past both the clock and n's
// own change time. Callers hold fs.mu.
func (n *Node) touch() {
	f := n.fs
	next := f.clock.Add(time.Nanosecond)
	if !n.ctime.Before(next) {
		next = n.ctime.Add(time.Nanosecond)
	}
	f.clock, n.ctime = next, next
}

func (n *Node) add(name string, kind domain.EntryKind) *Node {
	if err := fsaccess.ValidateName("synthfs", []byte(name)); err != nil {
		panic(err)
	}
	f := n.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if n.kind != domain.EntryDirectory {
		panic("synthfs: " + domain.DisplayName(n.name) + " is not a directory")
	}
	if _, dup := n.index[name]; dup {
		panic("synthfs: duplicate entry " + domain.DisplayName([]byte(name)))
	}
	if n.gen != nil && n.gen.has([]byte(name)) {
		panic("synthfs: entry " + domain.DisplayName([]byte(name)) + " collides with a generated name")
	}
	c := &Node{fs: f, name: []byte(name), kind: kind, ino: f.allocIno(1), failAfter: -1}
	c.touch()
	n.attach(c)
	return c
}

// Dir adds an empty child directory.
func (n *Node) Dir(name string) *Node { return n.add(name, domain.EntryDirectory) }

// File adds a regular file with the given size and modification time.
func (n *Node) File(name string, size int64, mtime time.Time) *Node {
	c := n.add(name, domain.EntryFile)
	c.size, c.mtime = size, mtime
	return c
}

// Symlink adds a symbolic link whose link text is target.
func (n *Node) Symlink(name, target string) *Node {
	c := n.add(name, domain.EntrySymlink)
	c.link = []byte(target)
	return c
}

// Special adds a FIFO, socket, character device, or block device.
func (n *Node) Special(name string, kind domain.EntryKind) *Node {
	switch kind {
	case domain.EntryFIFO, domain.EntrySocket, domain.EntryCharDevice, domain.EntryBlockDevice:
	default:
		panic("synthfs: not a special kind: " + string(kind))
	}
	return n.add(name, kind)
}

// HardLink adds name as another name of the regular file target, as link(2)
// does: Lstat of every name reports target's device, inode, size, blocks,
// modification time, and change time, Nlink counts the names, and every name
// opens the same content. It advances target's change time. Modify the
// target, not the link, to change what they report (content methods called on
// a link apply to its target). target must be a file built on this FS, on
// this directory's device.
func (n *Node) HardLink(name string, target *Node) *Node {
	c := n.add(name, domain.EntryFile)
	f := n.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if target.inode != nil {
		target = target.inode
	}
	if target.fs != f || target.kind != domain.EntryFile {
		panic("synthfs: HardLink target " + domain.DisplayName(target.name) + " is not a regular file of this FS")
	}
	if target.devOf() != c.devOf() {
		panic("synthfs: hard link " + domain.DisplayName(c.name) + " would cross devices")
	}
	c.inode = target
	target.links++
	target.touch()
	return c
}

// Generated adds a child directory holding exactly descendants lazily
// generated entries, with at most fanout entries per directory.
func (n *Node) Generated(name string, descendants, fanout int) *Node {
	return n.Dir(name).Generate(descendants, fanout)
}

// Generate gives this directory exactly descendants lazily generated
// descendants, listed after its explicit children. Each generated directory
// has at most fanout entries; generated entries are named dir-NNNNNNN and
// file-NNNNNNN.dat by their index, get unique inode numbers on this node's
// device, and are never mount boundaries. With fanout >= descendants the
// directory is flat.
func (n *Node) Generate(descendants, fanout int) *Node {
	if descendants < 0 || fanout <= 0 {
		panic(fmt.Sprintf("synthfs: Generate(%d, %d): need descendants >= 0 and fanout > 0", descendants, fanout))
	}
	f := n.fs
	f.mu.Lock()
	defer f.mu.Unlock()
	if n.kind != domain.EntryDirectory {
		panic("synthfs: " + domain.DisplayName(n.name) + " is not a directory")
	}
	if n.gen != nil {
		panic("synthfs: " + domain.DisplayName(n.name) + " already generates entries")
	}
	// The region's own number is virtual; descendants use the next ones.
	g := &region{base: f.allocIno(uint64(descendants) + 1), budget: descendants, fanout: fanout}
	for _, c := range n.children {
		if g.has(c.name) {
			panic("synthfs: entry " + domain.DisplayName(c.name) + " collides with a generated name")
		}
	}
	n.gen = g
	return n
}

// Child returns the explicit child called name, or nil.
func (n *Node) Child(name string) *Node {
	n.fs.mu.RLock()
	defer n.fs.mu.RUnlock()
	return n.index[name]
}

// Remove deletes the explicit child called name. Together with the add
// methods it simulates entries replaced while a scan runs.
func (n *Node) Remove(name string) {
	n.fs.mu.Lock()
	defer n.fs.mu.Unlock()
	c, ok := n.index[name]
	if !ok {
		panic("synthfs: no entry " + domain.DisplayName([]byte(name)))
	}
	n.detach(c)
}

func (n *Node) modify(fn func()) *Node {
	n.fs.mu.Lock()
	defer n.fs.mu.Unlock()
	fn()
	return n
}

// change is modify for a change that advances the change time.
func (n *Node) change(fn func()) *Node {
	return n.modify(func() {
		fn()
		n.touch()
	})
}

// changeContent applies fn to the regular file n names and advances that
// file's change time.
func (n *Node) changeContent(what string, fn func(file *Node)) *Node {
	return n.modify(func() {
		file := n.regular(what)
		fn(file)
		file.touch()
	})
}

// regular returns the regular file n names (its target, for a hard link). It
// panics on anything but a regular file. Callers hold fs.mu.
func (n *Node) regular(what string) *Node {
	file := n
	if file.inode != nil {
		file = file.inode
	}
	if file.kind != domain.EntryFile {
		panic("synthfs: " + what + " of " + domain.DisplayName(n.name) + ", which is not a regular file")
	}
	return file
}

// Ident sets the device and inode number and advances the change time.
func (n *Node) Ident(dev, ino uint64) *Node { return n.change(func() { n.dev, n.ino = dev, ino }) }

// Blocks sets the reported st_blocks (512-byte units), for example to model a
// sparse file. By default allocation is the size rounded up to 4 KiB.
func (n *Node) Blocks(blocks int64) *Node {
	return n.modify(func() { n.blocks, n.hasBlocks = blocks, true })
}

// Nlink sets the link count Lstat reports for this file, for example for a
// file whose other names lie outside the built tree. By default a file
// reports one link plus one per HardLink to it.
func (n *Node) Nlink(count uint64) *Node {
	return n.modify(func() { n.nlink, n.hasNlink = count, true })
}

// Dev puts the node on another device; descendants without their own device
// inherit it. A directory or regular file on a device other than its parent's
// is a mount boundary.
func (n *Node) Dev(dev uint64) *Node { return n.modify(func() { n.dev = dev }) }

// ModTime sets the modification time and advances the change time.
func (n *Node) ModTime(t time.Time) *Node { return n.change(func() { n.mtime = t }) }

// Ctime sets the change time, and moves the FS clock forward to it if it is
// later, so later changes still advance change times.
func (n *Node) Ctime(t time.Time) *Node {
	return n.modify(func() {
		n.ctime = t
		if t.After(n.fs.clock) {
			n.fs.clock = t
		}
	})
}

// Size sets a file's size in place: the same inode with new content metadata,
// as an in-place write would leave it. Shrinking discards content beyond the
// new size; growing exposes generated bytes for generated content and zero
// bytes for explicit content. It advances the change time.
func (n *Node) Size(size int64) *Node {
	if size < 0 {
		panic(fmt.Sprintf("synthfs: negative size %d", size))
	}
	return n.change(func() {
		n.size = size
		if int64(len(n.content)) > size {
			n.content = n.content[:size]
		}
		kept := n.patches[:0:0]
		for _, p := range n.patches {
			if p.off < size {
				p.b = p.b[:min(int64(len(p.b)), size-p.off)]
				kept = append(kept, p)
			}
		}
		n.patches = kept
	})
}

// Content gives the regular file the explicit content b (copied) and sets its
// size to len(b). It advances the change time.
func (n *Node) Content(b []byte) *Node {
	return n.changeContent("Content", func(file *Node) {
		file.content, file.hasContent, file.hasSeed, file.patches = bytes.Clone(b), true, false, nil
		file.size = int64(len(b))
	})
}

// Seed gives the regular file content generated from s, of its current size:
// equal seeds and sizes give equal bytes. It advances the change time.
func (n *Node) Seed(s uint64) *Node {
	return n.changeContent("Seed", func(file *Node) {
		file.content, file.hasContent, file.patches = nil, false, nil
		file.seed, file.hasSeed = s, true
	})
}

// Patch overwrites the regular file's content at off with b (copied), in
// place: the size and modification time stay, and the change time advances,
// as a write followed by restoring the modification time leaves a file.
// off+len(b) must not exceed the size.
func (n *Node) Patch(off int64, b []byte) *Node {
	return n.changeContent("Patch", func(file *Node) {
		if off < 0 || off+int64(len(b)) > file.size {
			panic(fmt.Sprintf("synthfs: Patch(%d, %d bytes) outside %s of size %d", off, len(b), domain.DisplayName(n.name), file.size))
		}
		file.patches = append(file.patches, patch{off: off, b: bytes.Clone(b)})
	})
}

// Perm sets the permission bits reported in Mode and advances the change time.
func (n *Node) Perm(perm fs.FileMode) *Node {
	return n.change(func() { n.perm, n.hasPerm = perm&fs.ModePerm, true })
}

// Unreadable makes opening this directory or regular file fail with outcome
// unreadable, as mode 0000 does: Lstat of it succeeds and reports no
// permission bits. It advances the change time.
func (n *Node) Unreadable() *Node { return n.change(func() { n.unreadable = true }) }

// MountPoint lists this directory or regular file in the mount table, as a
// same-device bind mount is.
func (n *Node) MountPoint() *Node { return n.modify(func() { n.mountPoint = true }) }

// HideKind makes listings report this entry as EntryUnknown, as filesystems
// without d_type do; Lstat still reports the real kind.
func (n *Node) HideKind() *Node { return n.modify(func() { n.hideKind = true }) }

// FailLstat makes Lstat of this entry fail with outcome.
func (n *Node) FailLstat(outcome domain.AccessOutcome) *Node {
	checkOutcome(outcome)
	return n.modify(func() { n.lstatFail = outcome })
}

// FailListingAfter makes listings of this directory fail with outcome after
// returning entries entries (or after the last entry, if there are fewer).
func (n *Node) FailListingAfter(entries int, outcome domain.AccessOutcome) *Node {
	if entries < 0 {
		panic("synthfs: FailListingAfter needs entries >= 0")
	}
	checkOutcome(outcome)
	return n.modify(func() { n.failAfter, n.failWith = entries, outcome })
}

// FailReadAfter makes reads of this regular file fail with outcome once they
// reach offset off: a read returns the bytes before off and the failure.
func (n *Node) FailReadAfter(off int64, outcome domain.AccessOutcome) *Node {
	if off < 0 {
		panic("synthfs: FailReadAfter needs off >= 0")
	}
	checkOutcome(outcome)
	return n.modify(func() {
		file := n.regular("FailReadAfter")
		file.readFail, file.readFailAt = outcome, off
	})
}

// Info returns the node's lstat-level metadata as its parent's Lstat reports
// it, for building OpenDir and OpenFile expectations in tests.
func (n *Node) Info() fsaccess.EntryInfo {
	n.fs.mu.RLock()
	defer n.fs.mu.RUnlock()
	info := n.info()
	info.MountBoundary = n.parent != nil && n.boundaryFrom(n.parent.devOf())
	return info
}

func checkOutcome(o domain.AccessOutcome) {
	switch o {
	case domain.OutcomeAbsent, domain.OutcomeUnreadable, domain.OutcomeUnavailable, domain.OutcomeChangedDuringObservation:
	default:
		panic("synthfs: unknown access outcome " + string(o))
	}
}

// errFor returns an error resembling what the OS reports for outcome.
func errFor(o domain.AccessOutcome) error {
	switch o {
	case domain.OutcomeAbsent:
		return syscall.ENOENT
	case domain.OutcomeUnreadable:
		return syscall.EACCES
	case domain.OutcomeChangedDuringObservation:
		return fsaccess.ErrIdentityChanged
	default:
		return syscall.EIO
	}
}

func (n *Node) devOf() uint64 {
	for x := n; x != nil; x = x.parent {
		if x.dev != 0 {
			return x.dev
		}
	}
	return 0
}

func (n *Node) boundaryFrom(parentDev uint64) bool {
	return (n.kind == domain.EntryDirectory || n.kind == domain.EntryFile) && (n.devOf() != parentDev || n.mountPoint)
}

func (n *Node) info() fsaccess.EntryInfo {
	if n.inode != nil {
		info := n.inode.info()
		info.Name = bytes.Clone(n.name)
		return info
	}
	info := fsaccess.EntryInfo{
		Name:    bytes.Clone(n.name),
		Kind:    n.kind,
		ModTime: n.mtime,
		Dev:     n.devOf(),
		Ino:     n.ino,
		Nlink:   1,
		Ctime:   n.ctime,
	}
	if info.ModTime.IsZero() {
		info.ModTime = DefaultModTime
	}
	var perm fs.FileMode
	switch n.kind {
	case domain.EntryDirectory:
		info.Mode, perm, info.Size, info.Nlink = fs.ModeDir, 0o755, 4096, 2
		if n.unreadable {
			perm = 0
		}
	case domain.EntryFile:
		perm, info.Size, info.Nlink = 0o644, n.size, 1+uint64(n.links)
		if n.unreadable {
			perm = 0
		}
	case domain.EntrySymlink:
		info.Mode, perm, info.Size = fs.ModeSymlink, 0o777, int64(len(n.link))
	case domain.EntryFIFO:
		info.Mode, perm = fs.ModeNamedPipe, 0o644
	case domain.EntrySocket:
		info.Mode, perm = fs.ModeSocket, 0o755
	case domain.EntryCharDevice:
		info.Mode, perm = fs.ModeDevice|fs.ModeCharDevice, 0o620
	case domain.EntryBlockDevice:
		info.Mode, perm = fs.ModeDevice, 0o660
	}
	if n.hasPerm {
		perm = n.perm
	}
	info.Mode |= perm
	if n.kind == domain.EntryFile || n.kind == domain.EntryDirectory {
		info.Blocks = allocBlocks(info.Size)
	}
	if n.hasBlocks {
		info.Blocks = n.blocks
	}
	if n.hasNlink {
		info.Nlink = n.nlink
	}
	n.fs.present(&info)
	return info
}

// present applies the device's knobs to metadata as built: the inode shift
// of its remounts and, for a device given capabilities, the time resolution
// and local-time storage. Callers hold f.mu.
func (f *FS) present(info *fsaccess.EntryInfo) {
	d := f.devices[info.Dev]
	if d == nil {
		return
	}
	info.Ino += d.inoShift
	if d.caps == nil {
		return
	}
	if r := d.caps.TimeResolution; r > 0 {
		info.ModTime = info.ModTime.Truncate(r)
	}
	if d.caps.LocalTime {
		w, loc := info.ModTime.UTC(), d.zone
		if loc == nil {
			loc = time.UTC
		}
		info.ModTime = time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), loc)
	}
}

// allocBlocks is the default allocation: size rounded up to 4 KiB, in 512-byte units.
func allocBlocks(size int64) int64 {
	return (size + 4095) / 4096 * 8
}

// readAt fills p with the regular file's content at off, as it is now, and
// returns the bytes filled: fewer than len(p) only at the end of the file or
// at an injected read failure. Callers hold fs.mu.
func (n *Node) readAt(p []byte, off int64) (int, error) {
	if off >= n.size {
		return 0, io.EOF
	}
	p = p[:min(int64(len(p)), n.size-off)]
	if n.hasContent {
		k := 0
		if off < int64(len(n.content)) {
			k = copy(p, n.content[off:])
		}
		clear(p[k:])
	} else {
		seed := n.ino
		if n.hasSeed {
			seed = n.seed
		}
		generate(p, seed, off)
	}
	for _, pt := range n.patches {
		lo, hi := max(off, pt.off), min(off+int64(len(p)), pt.off+int64(len(pt.b)))
		if lo < hi {
			copy(p[lo-off:hi-off], pt.b[lo-pt.off:])
		}
	}
	if n.readFail != "" && off+int64(len(p)) > n.readFailAt {
		return int(max(0, n.readFailAt-off)), errFor(n.readFail)
	}
	return len(p), nil
}

// generate fills p with the content generated from seed at offset off: word i
// (bytes 8i to 8i+7, little-endian) is a splitmix64 output for (seed, i), so
// any range is computed in O(1) per word without materialising the file.
func generate(p []byte, seed uint64, off int64) {
	key := mix64(seed)
	pos := uint64(off)
	for len(p) > 0 {
		w := mix64(key + (pos/8+1)*0x9E3779B97F4A7C15)
		if r := pos % 8; r != 0 || len(p) < 8 {
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], w)
			k := copy(p, buf[r:])
			p, pos = p[k:], pos+uint64(k)
			continue
		}
		binary.LittleEndian.PutUint64(p, w)
		p, pos = p[8:], pos+8
	}
}

// mix64 is the splitmix64 finaliser.
func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// region describes a lazily generated subtree: budget descendants numbered
// base+1 … base+budget in pre-order, at most fanout per directory. Child i of
// a region is itself a region; it is a directory when it has descendants.
type region struct {
	base   uint64
	budget int
	fanout int
}

func (r *region) width() int { return min(r.budget, r.fanout) }

func (r *region) child(i int) (sub region, isDir bool) {
	k := r.width()
	rest := r.budget - k
	q, rem := rest/k, rest%k
	size := q
	if i < rem {
		size++
	}
	ino := r.base + 1 + uint64(i)*uint64(1+q) + uint64(min(i, rem))
	return region{base: ino, budget: size, fanout: r.fanout}, size > 0
}

func genName(i int, isDir bool) []byte {
	if isDir {
		return fmt.Appendf(nil, "dir-%07d", i)
	}
	return fmt.Appendf(nil, "file-%07d.dat", i)
}

// lookup resolves a generated name to its index.
func (r *region) lookup(name []byte) (i int, sub region, isDir bool, ok bool) {
	var digits []byte
	switch {
	case bytes.HasPrefix(name, []byte("dir-")):
		digits = name[len("dir-"):]
	case bytes.HasPrefix(name, []byte("file-")) && bytes.HasSuffix(name, []byte(".dat")):
		digits = name[len("file-") : len(name)-len(".dat")]
	default:
		return 0, region{}, false, false
	}
	i, err := strconv.Atoi(string(digits))
	if err != nil || i < 0 || i >= r.width() {
		return 0, region{}, false, false
	}
	sub, isDir = r.child(i)
	if !bytes.Equal(genName(i, isDir), name) {
		return 0, region{}, false, false
	}
	return i, sub, isDir, true
}

func (r *region) has(name []byte) bool {
	_, _, _, ok := r.lookup(name)
	return ok
}

// genInfo describes a generated entry as the device presents it.
func (f *FS) genInfo(name []byte, r region, isDir bool, dev uint64) fsaccess.EntryInfo {
	info := rawGenInfo(name, r, isDir, dev)
	f.present(&info)
	return info
}

// rawGenInfo describes a generated entry as built. Its change time equals its
// modification time; a generated file's content is generated from its inode.
func rawGenInfo(name []byte, r region, isDir bool, dev uint64) fsaccess.EntryInfo {
	info := fsaccess.EntryInfo{
		Name:    name,
		ModTime: DefaultModTime.Add(time.Duration(r.base) * time.Second),
		Dev:     dev,
		Ino:     r.base,
	}
	info.Ctime = info.ModTime
	if isDir {
		info.Kind, info.Mode, info.Size, info.Nlink = domain.EntryDirectory, fs.ModeDir|0o755, 4096, 2
	} else {
		info.Kind, info.Mode, info.Size, info.Nlink = domain.EntryFile, 0o644, int64(r.base*7919%(1<<20)), 1
	}
	info.Blocks = allocBlocks(info.Size)
	return info
}

// dir is an open directory handle: an explicit node (possibly with generated
// children) or a generated directory (node == nil).
type dir struct {
	fs   *FS
	root *rootState
	node *Node
	gen  *region
	dev  uint64
	self fsaccess.EntryInfo

	closed atomic.Bool
	mu     sync.Mutex
	cursor int
}

var (
	errBatchSize      = errors.New("synthfs: batch size must be positive")
	errNegativeOffset = errors.New("synthfs: negative read offset")
)

func (d *dir) Self() fsaccess.EntryInfo { return d.self }

// check reports a vanished root, an unmounted device, or a closed handle.
// Callers hold fs.mu.
func (d *dir) check(op string) error {
	if d.fs.gone(d.root) {
		return &fsaccess.Error{Op: op, Name: d.self.Name, Outcome: domain.OutcomeUnavailable, Err: syscall.EIO}
	}
	if d.closed.Load() {
		return &fsaccess.Error{Op: op, Name: d.self.Name, Outcome: domain.OutcomeUnavailable, Err: os.ErrClosed}
	}
	return nil
}

func (d *dir) explicitCount() int {
	if d.node == nil {
		return 0
	}
	return len(d.node.children)
}

func (d *dir) count() int {
	n := d.explicitCount()
	if d.gen != nil {
		n += d.gen.width()
	}
	return n
}

func (d *dir) entryAt(i int) fsaccess.DirEntry {
	if i < d.explicitCount() {
		c := d.node.children[i]
		kind := c.kind
		if c.hideKind {
			kind = domain.EntryUnknown
		}
		return fsaccess.DirEntry{Name: bytes.Clone(c.name), Kind: kind}
	}
	j := i - d.explicitCount()
	_, isDir := d.gen.child(j)
	kind := domain.EntryFile
	if isDir {
		kind = domain.EntryDirectory
	}
	return fsaccess.DirEntry{Name: genName(j, isDir), Kind: kind}
}

// ReadBatch returns either entries with a nil error, or no entries with io.EOF
// or an *fsaccess.Error, like the os.Root backend.
func (d *dir) ReadBatch(n int) ([]fsaccess.DirEntry, error) {
	const op = "ReadBatch"
	d.fs.mu.RLock()
	defer d.fs.mu.RUnlock()
	if err := d.check(op); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, &fsaccess.Error{Op: op, Name: d.self.Name, Err: errBatchSize}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	limit, failing := d.count(), false
	if d.node != nil && d.node.failAfter >= 0 {
		limit, failing = min(limit, d.node.failAfter), true
	}
	if d.cursor >= limit {
		if failing {
			o := d.node.failWith
			return nil, &fsaccess.Error{Op: op, Name: d.self.Name, Outcome: o, Err: errFor(o)}
		}
		return nil, io.EOF
	}
	end := min(d.cursor+n, limit)
	out := make([]fsaccess.DirEntry, 0, end-d.cursor)
	for i := d.cursor; i < end; i++ {
		out = append(out, d.entryAt(i))
	}
	d.cursor = end
	d.fs.noteBatch(len(out))
	return out, nil
}

// child resolves name to an explicit node or a generated entry: exactly, or
// else, on a device whose capabilities are case-insensitive, regardless of
// letter case (explicit children in listing order first).
func (d *dir) child(name []byte) (node *Node, sub region, isDir, ok bool) {
	if d.node != nil {
		if c, found := d.node.index[string(name)]; found {
			return c, region{}, false, true
		}
	}
	if d.gen != nil {
		if _, sub, isDir, found := d.gen.lookup(name); found {
			return nil, sub, isDir, true
		}
	}
	if dev := d.fs.devices[d.dev]; dev == nil || dev.caps == nil || dev.caps.CaseSensitive {
		return nil, region{}, false, false
	}
	if d.node != nil {
		for _, c := range d.node.children {
			if bytes.EqualFold(c.name, name) {
				return c, region{}, false, true
			}
		}
	}
	if d.gen != nil {
		// Generated names are lower-case ASCII.
		if _, sub, isDir, found := d.gen.lookup(bytes.ToLower(name)); found {
			return nil, sub, isDir, true
		}
	}
	return nil, region{}, false, false
}

func (d *dir) Lstat(name []byte) (fsaccess.EntryInfo, error) {
	const op = "Lstat"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return fsaccess.EntryInfo{}, err
	}
	d.fs.mu.RLock()
	defer d.fs.mu.RUnlock()
	if err := d.check(op); err != nil {
		return fsaccess.EntryInfo{}, err
	}
	c, sub, isDir, ok := d.child(name)
	if !ok {
		return fsaccess.EntryInfo{}, &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeAbsent, Err: syscall.ENOENT}
	}
	if c == nil {
		return d.fs.genInfo(bytes.Clone(name), sub, isDir, d.dev), nil
	}
	if c.lstatFail != "" {
		return fsaccess.EntryInfo{}, &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: c.lstatFail, Err: errFor(c.lstatFail)}
	}
	info := c.info()
	info.Name = bytes.Clone(name)
	info.MountBoundary = c.boundaryFrom(d.dev)
	return info, nil
}

func (d *dir) OpenDir(name []byte, expect fsaccess.EntryInfo) (fsaccess.Dir, error) {
	const op = "OpenDir"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return nil, err
	}
	fail := func(outcome domain.AccessOutcome, err error) (fsaccess.Dir, error) {
		return nil, &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: outcome, Err: err}
	}
	if expect.Kind != domain.EntryDirectory {
		return fail("", fsaccess.ErrNotDirectory)
	}
	if expect.MountBoundary {
		return fail("", fsaccess.ErrMountBoundary)
	}
	d.fs.mu.RLock()
	defer d.fs.mu.RUnlock()
	if err := d.check(op); err != nil {
		return nil, err
	}
	c, sub, isDir, ok := d.child(name)
	if !ok {
		return fail(domain.OutcomeAbsent, syscall.ENOENT)
	}
	same := func(info fsaccess.EntryInfo) bool {
		return info.Kind == expect.Kind && info.Dev == expect.Dev && info.Ino == expect.Ino
	}
	if c == nil {
		info := d.fs.genInfo(bytes.Clone(name), sub, isDir, d.dev)
		if !same(info) {
			return fail(domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
		}
		return &dir{fs: d.fs, root: d.root, gen: &sub, dev: d.dev, self: info}, nil
	}
	if c.kind != domain.EntryDirectory {
		return fail(domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
	}
	if c.unreadable {
		return fail(domain.OutcomeUnreadable, syscall.EACCES)
	}
	info := c.info()
	info.Name = bytes.Clone(name)
	if !same(info) {
		return fail(domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
	}
	if c.boundaryFrom(d.dev) {
		return fail(domain.OutcomeChangedDuringObservation, fsaccess.ErrMountBoundary)
	}
	return &dir{fs: d.fs, root: d.root, node: c, gen: c.gen, dev: c.devOf(), self: info}, nil
}

// OpenFile opens a regular file with the os.Root backend's contract: refusals
// before touching anything, then an identity check of kind, device, inode,
// size, modification time, and change time, then the boundary check, and
// only then the permission check.
func (d *dir) OpenFile(name []byte, expect fsaccess.EntryInfo) (fsaccess.File, error) {
	const op = "OpenFile"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return nil, err
	}
	fail := func(outcome domain.AccessOutcome, err error) (fsaccess.File, error) {
		return nil, &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: outcome, Err: err}
	}
	if expect.Kind != domain.EntryFile {
		return fail("", fsaccess.ErrNotRegular)
	}
	if expect.MountBoundary {
		return fail("", fsaccess.ErrMountBoundary)
	}
	d.fs.mu.RLock()
	defer d.fs.mu.RUnlock()
	if err := d.check(op); err != nil {
		return nil, err
	}
	c, sub, isDir, ok := d.child(name)
	if !ok {
		return fail(domain.OutcomeAbsent, syscall.ENOENT)
	}
	same := func(info fsaccess.EntryInfo) bool {
		return info.Kind == domain.EntryFile && info.Dev == expect.Dev && info.Ino == expect.Ino &&
			info.Size == expect.Size && info.ModTime.Equal(expect.ModTime) && info.Ctime.Equal(expect.Ctime)
	}
	if c == nil {
		info := d.fs.genInfo(bytes.Clone(name), sub, isDir, d.dev)
		if !same(info) {
			return fail(domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
		}
		raw := rawGenInfo(info.Name, sub, isDir, d.dev)
		gen := &Node{fs: d.fs, name: raw.Name, kind: domain.EntryFile, size: raw.Size, mtime: raw.ModTime,
			ctime: raw.Ctime, dev: raw.Dev, ino: raw.Ino, failAfter: -1}
		return &file{fs: d.fs, root: d.root, node: gen, name: bytes.Clone(name)}, nil
	}
	if c.kind != domain.EntryFile || !same(c.info()) {
		return fail(domain.OutcomeChangedDuringObservation, fsaccess.ErrIdentityChanged)
	}
	if c.boundaryFrom(d.dev) {
		return fail(domain.OutcomeChangedDuringObservation, fsaccess.ErrMountBoundary)
	}
	target := c.regular(op)
	if target.unreadable {
		return fail(domain.OutcomeUnreadable, syscall.EACCES)
	}
	return &file{fs: d.fs, root: d.root, node: target, name: bytes.Clone(name)}, nil
}

// file is an open regular file: the inode node it was opened on, read as it
// is at each call.
type file struct {
	fs     *FS
	root   *rootState
	node   *Node
	name   []byte
	closed atomic.Bool
}

// check reports a vanished root, an unmounted device, or a closed handle.
// Callers hold fs.mu.
func (f *file) check(op string) error {
	if f.fs.gone(f.root) {
		return &fsaccess.Error{Op: op, Name: bytes.Clone(f.name), Outcome: domain.OutcomeUnavailable, Err: syscall.EIO}
	}
	if f.closed.Load() {
		return &fsaccess.Error{Op: op, Name: bytes.Clone(f.name), Outcome: domain.OutcomeUnavailable, Err: os.ErrClosed}
	}
	return nil
}

// Stat reports the file's current metadata under the name it was opened by.
func (f *file) Stat() (fsaccess.EntryInfo, error) {
	const op = "FileStat"
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()
	if err := f.check(op); err != nil {
		return fsaccess.EntryInfo{}, err
	}
	info := f.node.info()
	info.Name = bytes.Clone(f.name)
	return info, nil
}

// ReadAt serves the content as it is at the time of the call.
func (f *file) ReadAt(p []byte, off int64) (int, error) {
	const op = "ReadAt"
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()
	if err := f.check(op); err != nil {
		return 0, err
	}
	if off < 0 {
		return 0, &fsaccess.Error{Op: op, Name: bytes.Clone(f.name), Err: errNegativeOffset}
	}
	if len(p) == 0 {
		return 0, nil
	}
	n, err := f.node.readAt(p, off)
	switch {
	case err == io.EOF || (err == nil && n < len(p)):
		return n, io.EOF
	case err != nil:
		o := f.node.readFail
		return n, &fsaccess.Error{Op: op, Name: bytes.Clone(f.name), Outcome: o, Err: err}
	}
	return n, nil
}

func (f *file) Close() error {
	f.closed.Store(true)
	return nil
}

func (d *dir) Readlink(name []byte) ([]byte, error) {
	const op = "Readlink"
	if err := fsaccess.ValidateName(op, name); err != nil {
		return nil, err
	}
	d.fs.mu.RLock()
	defer d.fs.mu.RUnlock()
	if err := d.check(op); err != nil {
		return nil, err
	}
	c, _, _, ok := d.child(name)
	if !ok {
		return nil, &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeAbsent, Err: syscall.ENOENT}
	}
	if c == nil || c.kind != domain.EntrySymlink {
		return nil, &fsaccess.Error{Op: op, Name: bytes.Clone(name), Outcome: domain.OutcomeChangedDuringObservation, Err: syscall.EINVAL}
	}
	return bytes.Clone(c.link), nil
}

func (d *dir) FSInfo() (fsaccess.FSInfo, error) {
	d.fs.mu.RLock()
	defer d.fs.mu.RUnlock()
	if err := d.check("FSInfo"); err != nil {
		return fsaccess.FSInfo{}, err
	}
	info, ok := d.fs.fsinfo[d.dev]
	if !ok {
		info = fsaccess.FSInfo{Type: DefaultFSType, FSID: d.dev}
	}
	info.Dev = d.dev
	if info.Mount != nil {
		m := *info.Mount
		if dev := d.fs.devices[d.dev]; dev != nil && dev.point != "" {
			m.MountPoint = dev.point
		}
		info.Mount = &m
	}
	return info, nil
}

func (d *dir) Close() error {
	d.closed.Store(true)
	return nil
}
