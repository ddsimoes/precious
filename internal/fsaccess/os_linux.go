package fsaccess

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
)

// NewOS returns the Linux backend (design D3), built on os.Root, getdents, and
// O_PATH handles, with mount boundaries decided from /proc/self/mountinfo,
// volume identity from the mount table, udev's /dev/disk links, and
// /sys/class/block (design D4), and capabilities from the filesystem type.
//
// Traversal is strictly per component: names are validated as single
// components, symlinks are never resolved, and every directory opened below
// the root is verified against the identity the caller observed. Special files
// are recognised from the directory listing and lstat and are never opened.
// Outside its Writer methods (renameat2 with RENAME_NOREPLACE, mkdirat then
// fchmod, unlinkat of an empty folder, and fsync of a folder, r3 design D3)
// the backend issues no call that creates, writes, renames, removes, or
// changes metadata. It opens a regular file only in OpenFile, read-only and
// with O_NOATIME where the kernel allows it, after an identity check that
// opens nothing else (M4 design D5).
//
// A Dir from this backend may be used from several goroutines; ReadBatch and
// FSInfo calls on one Dir are serialised. A File may be used from the
// goroutine that opened it.
func NewOS() FS { return newOS("/proc", "/dev", "/sys") }

// newOS returns the Linux backend reading the mount table below procRoot,
// udev's links below devRoot, and block devices below sysRoot.
func newOS(procRoot, devRoot, sysRoot string) *osFS {
	return &osFS{procRoot: procRoot, devRoot: devRoot, sysRoot: sysRoot, statfs: unix.Statfs}
}

type osFS struct {
	procRoot string
	devRoot  string
	sysRoot  string
	statfs   func(path string, st *unix.Statfs_t) error
}

func (o *osFS) mountRows() ([]MountInfo, error) {
	data, err := os.ReadFile(filepath.Join(o.procRoot, "self", "mountinfo"))
	if err != nil {
		return nil, err
	}
	return parseMountinfo(data)
}

// Mounts maps every mountinfo row to a Mount with the volume identity of
// design D4.
func (o *osFS) Mounts() ([]Mount, error) {
	rows, err := o.mountRows()
	if err != nil {
		return nil, &Error{Op: "Mounts", Outcome: domain.OutcomeUnavailable, Err: fmt.Errorf("mount table: %w", err)}
	}
	idx := o.readDevIndex()
	mounts := make([]Mount, len(rows))
	for i := range rows {
		r := &rows[i]
		mounts[i] = Mount{Point: r.MountPoint, Root: []byte(r.Root), Volume: o.volume(r, idx), ReadOnly: r.ReadOnly}
	}
	return mounts, nil
}

// Capabilities returns the capabilities of the filesystem type of the mount
// holding path (the deepest mount point containing it in the current table),
// read-only when that mount is. path is matched as given; it must be
// canonical, like a Mount's Point.
func (o *osFS) Capabilities(path string) (Capabilities, error) {
	const op = "Capabilities"
	if !filepath.IsAbs(path) {
		return Capabilities{}, &Error{Op: op, Name: []byte(path), Err: errRelativePath}
	}
	rows, err := o.mountRows()
	if err != nil {
		return Capabilities{}, &Error{Op: op, Name: []byte(path), Outcome: domain.OutcomeUnavailable, Err: fmt.Errorf("mount table: %w", err)}
	}
	m := deepestMount(rows, filepath.Clean(path), nil)
	if m == nil {
		return Capabilities{}, &Error{Op: op, Name: []byte(path), Outcome: domain.OutcomeUnavailable, Err: errNoMount}
	}
	return o.capabilities(m), nil
}

func (o *osFS) OpenRoot(path string) (Dir, error) {
	const op = "OpenRoot"
	fail := func(outcome domain.AccessOutcome, err error) (Dir, error) {
		return nil, &Error{Op: op, Name: []byte(path), Outcome: outcome, Err: err}
	}
	if !filepath.IsAbs(path) {
		return fail("", errRelativePath)
	}
	root, err := os.OpenRoot(dirOnly(path))
	if err != nil {
		return fail(rootOutcome(err), err)
	}
	fi, err := root.Stat(".")
	if err != nil {
		root.Close()
		return fail(rootOutcome(err), err)
	}
	d := &osDir{root: root, self: entryInfo([]byte(filepath.Base(path)), fi)}
	rc, err := d.dirConn()
	if err != nil {
		d.Close()
		return fail(rootOutcome(err), err)
	}
	// Mount points in the snapshot are canonical paths in this mount
	// namespace; the configured path may contain symlinks.
	if d.abs, err = fdPath(rc); err != nil {
		d.Close()
		return fail(domain.OutcomeUnavailable, err)
	}
	rows, err := o.mountRows()
	if err != nil {
		d.Close()
		return fail(domain.OutcomeUnavailable, fmt.Errorf("mount table snapshot: %w", err))
	}
	d.mounts = newMountTable(rows)
	return d, nil
}

// fdPath returns the kernel's path for an open directory.
func fdPath(rc syscall.RawConn) (string, error) {
	var p string
	var perr error
	if err := rc.Control(func(fd uintptr) {
		p, perr = os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(fd), 10))
	}); err != nil {
		return "", err
	}
	if perr != nil {
		return "", perr
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("fsaccess: directory has no path in this mount namespace: %q", p)
	}
	return p, nil
}

var direntBufPool = sync.Pool{New: func() any { b := make([]byte, direntBufSize); return &b }}

type osDir struct {
	root   *os.Root
	abs    string // canonical absolute path, for mount-table lookups
	self   EntryInfo
	mounts *mountTable

	mu     sync.Mutex
	closed bool
	f      *os.File // the directory itself, opened for listing and fstatfs
	rc     syscall.RawConn
	buf    *[]byte // getdents64 buffer while a listing is in progress
	bufp   int
	nbuf   int
	eof    bool
	err    error // sticky listing failure
}

func (d *osDir) Self() EntryInfo { return d.self }

// dirConn opens the directory itself (a directory, never a file) on first use.
func (d *osDir) dirConn() (syscall.RawConn, error) {
	if d.rc != nil {
		return d.rc, nil
	}
	f, err := d.root.Open(".")
	if err != nil {
		return nil, err
	}
	rc, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return nil, err
	}
	d.f, d.rc = f, rc
	return rc, nil
}

func (d *osDir) failure(op string, outcome domain.AccessOutcome, err error) *Error {
	return &Error{Op: op, Name: d.self.Name, Outcome: outcome, Err: err}
}

// ReadBatch returns up to n entries. It returns either entries with a nil
// error, or no entries with io.EOF or an *Error; entries read before a failure
// are returned first and the failure is reported by the next call.
func (d *osDir) ReadBatch(n int) ([]DirEntry, error) {
	const op = "ReadBatch"
	if n <= 0 {
		return nil, d.failure(op, "", errBatchSize)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, d.failure(op, domain.OutcomeUnavailable, os.ErrClosed)
	}
	if d.err != nil {
		return nil, d.err
	}
	if d.eof {
		return nil, io.EOF
	}
	rc, err := d.dirConn()
	if err != nil {
		d.err = d.failure(op, outcomeFor(err), err)
		return nil, d.err
	}
	if d.buf == nil {
		d.buf = direntBufPool.Get().(*[]byte)
		d.bufp, d.nbuf = 0, 0
	}
	out := make([]DirEntry, 0, min(n, 1024))
	for len(out) < n {
		if d.bufp >= d.nbuf {
			nr, err := getdents(rc, *d.buf)
			if err != nil {
				d.err = d.failure(op, outcomeFor(err), err)
				break
			}
			if nr == 0 {
				d.eof = true
				break
			}
			d.bufp, d.nbuf = 0, nr
		}
		e, skip, reclen, err := nextDirent((*d.buf)[d.bufp:d.nbuf])
		if err != nil {
			d.err = d.failure(op, domain.OutcomeUnavailable, err)
			break
		}
		d.bufp += reclen
		if !skip {
			out = append(out, e)
		}
	}
	if d.eof || d.err != nil {
		d.releaseBuf()
	}
	if len(out) > 0 {
		return out, nil
	}
	if d.err != nil {
		return nil, d.err
	}
	return nil, io.EOF
}

func (d *osDir) releaseBuf() {
	if d.buf != nil {
		direntBufPool.Put(d.buf)
		d.buf = nil
	}
}

func getdents(rc syscall.RawConn, buf []byte) (int, error) {
	var n int
	var gerr error
	if err := rc.Control(func(fd uintptr) {
		for {
			n, gerr = unix.Getdents(int(fd), buf)
			if gerr != unix.EINTR {
				return
			}
		}
	}); err != nil {
		return 0, err
	}
	return n, gerr
}

func (d *osDir) Lstat(name []byte) (EntryInfo, error) {
	const op = "Lstat"
	if err := ValidateName(op, name); err != nil {
		return EntryInfo{}, err
	}
	fi, err := d.root.Lstat(string(name))
	if err != nil {
		return EntryInfo{}, &Error{Op: op, Name: bytes.Clone(name), Outcome: outcomeFor(err), Err: err}
	}
	info := entryInfo(bytes.Clone(name), fi)
	if (info.Kind == domain.EntryDirectory || info.Kind == domain.EntryFile) &&
		(info.Dev != d.self.Dev || d.mounts.isChildMountPoint(d.abs, name)) {
		info.MountBoundary = true
	}
	return info, nil
}

func (d *osDir) OpenDir(name []byte, expect EntryInfo) (Dir, error) {
	const op = "OpenDir"
	if err := ValidateName(op, name); err != nil {
		return nil, err
	}
	fail := func(outcome domain.AccessOutcome, err error) (Dir, error) {
		return nil, &Error{Op: op, Name: bytes.Clone(name), Outcome: outcome, Err: err}
	}
	if expect.Kind != domain.EntryDirectory {
		return fail("", ErrNotDirectory)
	}
	if expect.MountBoundary {
		return fail("", ErrMountBoundary)
	}
	child, err := d.root.OpenRoot(dirOnly(string(name)))
	if err != nil {
		return fail(openDirOutcome(err), err)
	}
	fi, err := child.Stat(".")
	if err != nil {
		child.Close()
		return fail(outcomeFor(err), err)
	}
	got := entryInfo(bytes.Clone(name), fi)
	if !sameObject(got, expect) {
		child.Close()
		return fail(domain.OutcomeChangedDuringObservation, ErrIdentityChanged)
	}
	// os.Root follows symlinks that stay inside the root. Require that the name
	// itself still denotes the opened directory, not a link to it.
	now, err := d.root.Lstat(string(name))
	if err != nil {
		child.Close()
		return fail(outcomeFor(err), err)
	}
	if !sameObject(entryInfo(nil, now), expect) {
		child.Close()
		return fail(domain.OutcomeChangedDuringObservation, ErrIdentityChanged)
	}
	abs := childPath(d.abs, name)
	if got.Dev != d.self.Dev || d.mounts.isMountPoint(abs) {
		// The caller's observation claimed no boundary; the snapshot disagrees.
		child.Close()
		return fail(domain.OutcomeChangedDuringObservation, ErrMountBoundary)
	}
	return &osDir{root: child, abs: abs, self: got, mounts: d.mounts}, nil
}

// OpenFile opens a regular file for reading without ever opening anything
// else (design D5):
//
//  1. invalid names, expected kinds other than a regular file, and expected
//     mount boundaries are refused before any system call;
//  2. openat(O_PATH|O_NOFOLLOW) on this directory's own descriptor resolves
//     the single name without opening the object: a symlink yields a handle
//     to the link itself, and a FIFO or device is not opened;
//  3. fstat of that handle must show the observed regular file (device,
//     inode, size, modification and change time) on this directory's device
//     and not a mount point;
//  4. the inode the handle holds is reopened for reading through
//     /proc/self/fd, which resolves no name, so only that regular file can be
//     opened, without blocking;
//  5. fstat of the read handle must show the same device and inode.
func (d *osDir) OpenFile(name []byte, expect EntryInfo) (File, error) {
	const op = "OpenFile"
	if err := ValidateName(op, name); err != nil {
		return nil, err
	}
	fail := func(outcome domain.AccessOutcome, err error) (File, error) {
		return nil, &Error{Op: op, Name: bytes.Clone(name), Outcome: outcome, Err: err}
	}
	if expect.Kind != domain.EntryFile {
		return fail("", ErrNotRegular)
	}
	if expect.MountBoundary {
		return fail("", ErrMountBoundary)
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return fail(domain.OutcomeUnavailable, os.ErrClosed)
	}
	rc, err := d.dirConn()
	d.mu.Unlock()
	if err != nil {
		return fail(outcomeFor(err), err)
	}

	pathFD, oerr := -1, error(nil)
	if err := rc.Control(func(dirfd uintptr) {
		for {
			pathFD, oerr = unix.Openat(int(dirfd), string(name), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if oerr != unix.EINTR {
				return
			}
		}
	}); err != nil {
		oerr = err
	}
	if oerr != nil {
		return fail(openFileOutcome(oerr), oerr)
	}
	defer unix.Close(pathFD)
	var st unix.Stat_t
	if err := fstat(pathFD, &st); err != nil {
		return fail(outcomeFor(err), err)
	}
	got := statInfo(nil, &st)
	if !sameFile(got, expect) {
		return fail(domain.OutcomeChangedDuringObservation, ErrIdentityChanged)
	}
	if got.Dev != d.self.Dev || d.mounts.isChildMountPoint(d.abs, name) {
		// The caller's observation claimed no boundary; the snapshot disagrees.
		return fail(domain.OutcomeChangedDuringObservation, ErrMountBoundary)
	}

	proc := "/proc/self/fd/" + strconv.Itoa(pathFD)
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOCTTY | unix.O_NONBLOCK
	fd, err := openRetrying(proc, flags|unix.O_NOATIME)
	if err == unix.EPERM {
		// O_NOATIME needs ownership of the file or CAP_FOWNER.
		fd, err = openRetrying(proc, flags)
	}
	if err != nil {
		return fail(reopenOutcome(err), err)
	}
	if err := fstat(fd, &st); err != nil {
		unix.Close(fd)
		return fail(outcomeFor(err), err)
	}
	if uint64(st.Dev) != got.Dev || uint64(st.Ino) != got.Ino {
		unix.Close(fd)
		return fail(domain.OutcomeChangedDuringObservation, ErrIdentityChanged)
	}
	return &osFile{fd: fd, name: bytes.Clone(name)}, nil
}

func fstat(fd int, st *unix.Stat_t) error {
	for {
		err := unix.Fstat(fd, st)
		if err != unix.EINTR {
			return err
		}
	}
}

func openRetrying(path string, flags int) (int, error) {
	for {
		fd, err := unix.Open(path, flags, 0)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// osFile is a regular file opened by OpenFile. It reads with pread and never
// maps the file: a truncation under a mapping would raise SIGBUS (design D5).
type osFile struct {
	fd   int // -1 once closed
	name []byte
}

func (f *osFile) failure(op string, outcome domain.AccessOutcome, err error) *Error {
	return &Error{Op: op, Name: bytes.Clone(f.name), Outcome: outcome, Err: err}
}

func (f *osFile) Stat() (EntryInfo, error) {
	const op = "FileStat"
	if f.fd < 0 {
		return EntryInfo{}, f.failure(op, domain.OutcomeUnavailable, os.ErrClosed)
	}
	var st unix.Stat_t
	if err := fstat(f.fd, &st); err != nil {
		return EntryInfo{}, f.failure(op, outcomeFor(err), err)
	}
	return statInfo(bytes.Clone(f.name), &st), nil
}

func (f *osFile) ReadAt(p []byte, off int64) (int, error) {
	const op = "ReadAt"
	if f.fd < 0 {
		return 0, f.failure(op, domain.OutcomeUnavailable, os.ErrClosed)
	}
	if off < 0 {
		return 0, f.failure(op, "", errNegativeOffset)
	}
	n := 0
	for n < len(p) {
		m, err := unix.Pread(f.fd, p[n:], off+int64(n))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return n, f.failure(op, outcomeFor(err), err)
		}
		if m == 0 {
			return n, io.EOF
		}
		n += m
	}
	return n, nil
}

func (f *osFile) Close() error {
	if f.fd < 0 {
		return nil
	}
	err := unix.Close(f.fd)
	f.fd = -1
	if err != nil {
		return f.failure("Close", outcomeFor(err), err)
	}
	return nil
}

func (d *osDir) Readlink(name []byte) ([]byte, error) {
	const op = "Readlink"
	if err := ValidateName(op, name); err != nil {
		return nil, err
	}
	target, err := d.root.Readlink(string(name))
	if err != nil {
		return nil, &Error{Op: op, Name: bytes.Clone(name), Outcome: readlinkOutcome(err), Err: err}
	}
	return []byte(target), nil
}

func (d *osDir) FSInfo() (FSInfo, error) {
	const op = "FSInfo"
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return FSInfo{}, d.failure(op, domain.OutcomeUnavailable, os.ErrClosed)
	}
	rc, err := d.dirConn()
	if err != nil {
		return FSInfo{}, d.failure(op, outcomeFor(err), err)
	}
	var st unix.Statfs_t
	var serr error
	if err := rc.Control(func(fd uintptr) {
		for {
			serr = unix.Fstatfs(int(fd), &st)
			if serr != unix.EINTR {
				return
			}
		}
	}); err != nil {
		serr = err
	}
	if serr != nil {
		return FSInfo{}, d.failure(op, outcomeFor(serr), serr)
	}
	return FSInfo{
		Type:     int64(st.Type),
		FSID:     uint64(uint32(st.Fsid.Val[0])) | uint64(uint32(st.Fsid.Val[1]))<<32,
		Dev:      d.self.Dev,
		ReadOnly: st.Flags&unix.ST_RDONLY != 0,
		Mount:    d.mounts.lookup(d.abs, d.self.Dev),
	}, nil
}

func (d *osDir) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	f := d.f
	d.f, d.rc = nil, nil
	d.releaseBuf()
	d.mu.Unlock()
	var errs []error
	if f != nil {
		errs = append(errs, f.Close())
	}
	errs = append(errs, d.root.Close())
	if err := errors.Join(errs...); err != nil {
		return d.failure("Close", outcomeFor(err), err)
	}
	return nil
}

func childPath(dir string, name []byte) string {
	if dir == "/" {
		return "/" + string(name)
	}
	return dir + "/" + string(name)
}

// statInfo is entryInfo for an fstat result. The mode is decoded as os.Lstat
// decodes it, so both describe one object identically.
func statInfo(name []byte, st *unix.Stat_t) EntryInfo {
	mode := fileMode(st.Mode)
	return EntryInfo{
		Name:    name,
		Kind:    kindOfMode(mode),
		Size:    st.Size,
		ModTime: time.Unix(st.Mtim.Unix()),
		Mode:    mode,
		Dev:     uint64(st.Dev),
		Ino:     uint64(st.Ino),
		Nlink:   uint64(st.Nlink),
		Blocks:  int64(st.Blocks),
		Ctime:   time.Unix(st.Ctim.Unix()),
	}
}

func fileMode(m uint32) fs.FileMode {
	mode := fs.FileMode(m & 0o777)
	switch m & unix.S_IFMT {
	case unix.S_IFBLK:
		mode |= fs.ModeDevice
	case unix.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFREG:
	case unix.S_IFSOCK:
		mode |= fs.ModeSocket
	default:
		mode |= fs.ModeIrregular
	}
	if m&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if m&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if m&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}
