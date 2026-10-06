package fsaccess

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"precious/internal/domain"
)

// NewPortable returns the portable backend (design D3), built only on os.Root,
// (*os.File).ReadDir, and Root.Lstat. It is the default (NewOS) wherever no
// native backend exists, and it builds and runs everywhere, Linux included.
//
// Its rules are the Linux backend's: names are single components, symlinks
// are never followed (kinds come from lstat, and OpenDir and OpenFile check
// with lstat that the name still denotes the observed object), and a special
// file is never opened on purpose. It is less precise:
//
//   - identity (device, inode, link count, allocation, change time) comes from
//     FileInfo.Sys() where the platform exposes it, and is zero elsewhere, so
//     OpenDir and OpenFile then compare kinds (and a file's size and
//     modification time) only;
//   - mount boundaries are recognised by a device change alone: there is no
//     mount table, so a same-device bind mount is not detected;
//   - OpenFile opens the name after an lstat confirmed a regular file and then
//     verifies the open handle, so an entry swapped for a FIFO or device in
//     between is opened (non-blocking, without becoming a controlling
//     terminal) before it is refused; access times may be updated;
//   - FSInfo reports only the directory's device: there is no statfs;
//   - Mounts lists one weak path volume per root opened, and Capabilities is
//     always the unknown set.
//
// A Dir may be used from several goroutines; ReadBatch and FSInfo calls on one
// Dir are serialised. A File may be used from the goroutine that opened it.
func NewPortable() FS { return &portableFS{roots: make(map[string]struct{})} }

type portableFS struct {
	mu    sync.Mutex
	roots map[string]struct{} // cleaned paths of the roots opened
}

// Mounts returns one weak path volume per root opened so far, by path.
func (p *portableFS) Mounts() ([]Mount, error) {
	p.mu.Lock()
	points := make([]string, 0, len(p.roots))
	for point := range p.roots {
		points = append(points, point)
	}
	p.mu.Unlock()
	slices.Sort(points)
	mounts := make([]Mount, len(points))
	for i, point := range points {
		mounts[i] = pathMount(point, []byte("/"), "", false)
	}
	return mounts, nil
}

// Capabilities returns the unknown set: this backend cannot classify a
// filesystem, nor tell whether it is mounted read-only.
func (p *portableFS) Capabilities(path string) (Capabilities, error) {
	if !filepath.IsAbs(path) {
		return Capabilities{}, &Error{Op: "Capabilities", Name: []byte(path), Err: errRelativePath}
	}
	return UnknownCapabilities(false), nil
}

func (p *portableFS) OpenRoot(path string) (Dir, error) {
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
	p.mu.Lock()
	p.roots[filepath.Clean(path)] = struct{}{}
	p.mu.Unlock()
	return &portableDir{root: root, self: entryInfo([]byte(filepath.Base(path)), fi)}, nil
}

// portableBatch bounds the entries requested from ReadDir at once, which
// allocates for the whole request up front.
const portableBatch = 1024

type portableDir struct {
	root *os.Root
	self EntryInfo

	mu     sync.Mutex
	closed bool
	f      *os.File // the directory itself while a listing is in progress
	eof    bool
	err    error // sticky listing failure
}

func (d *portableDir) Self() EntryInfo { return d.self }

func (d *portableDir) failure(op string, outcome domain.AccessOutcome, err error) *Error {
	return &Error{Op: op, Name: d.self.Name, Outcome: outcome, Err: err}
}

// checkName is ValidateName plus the platform's own path syntax: a name must
// not contain the OS separator (Windows' '\') or be anything but a plain local
// name (a volume name, a reserved device name).
func checkName(op string, name []byte) error {
	if err := ValidateName(op, name); err != nil {
		return err
	}
	if s := string(name); strings.ContainsRune(s, filepath.Separator) || !filepath.IsLocal(s) {
		return &Error{Op: op, Name: bytes.Clone(name), Err: ErrInvalidName}
	}
	return nil
}

// ReadBatch returns up to n entries. It returns either entries with a nil
// error, or no entries with io.EOF or an *Error; entries read before a failure
// are returned first and the failure is reported by the next call.
func (d *portableDir) ReadBatch(n int) ([]DirEntry, error) {
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
	if d.f == nil {
		f, err := d.root.Open(".")
		if err != nil {
			d.err = d.failure(op, outcomeFor(err), err)
			return nil, d.err
		}
		d.f = f
	}
	out := make([]DirEntry, 0, min(n, portableBatch))
	for len(out) < n {
		ents, err := d.f.ReadDir(min(n-len(out), portableBatch))
		for _, e := range ents {
			out = append(out, DirEntry{Name: []byte(e.Name()), Kind: kindOfMode(e.Type())})
		}
		if err == io.EOF {
			d.eof = true
			break
		}
		if err != nil {
			d.err = d.failure(op, outcomeFor(err), err)
			break
		}
	}
	if d.eof || d.err != nil {
		d.f.Close()
		d.f = nil
	}
	if len(out) > 0 {
		return out, nil
	}
	if d.err != nil {
		return nil, d.err
	}
	return nil, io.EOF
}

func (d *portableDir) Lstat(name []byte) (EntryInfo, error) {
	const op = "Lstat"
	if err := checkName(op, name); err != nil {
		return EntryInfo{}, err
	}
	fi, err := d.root.Lstat(string(name))
	if err != nil {
		return EntryInfo{}, &Error{Op: op, Name: bytes.Clone(name), Outcome: outcomeFor(err), Err: err}
	}
	info := entryInfo(bytes.Clone(name), fi)
	if (info.Kind == domain.EntryDirectory || info.Kind == domain.EntryFile) && info.Dev != d.self.Dev {
		info.MountBoundary = true
	}
	return info, nil
}

func (d *portableDir) OpenDir(name []byte, expect EntryInfo) (Dir, error) {
	const op = "OpenDir"
	if err := checkName(op, name); err != nil {
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
	// itself still denotes a directory (with the opened identity, where there
	// is one), not a link to it.
	now, err := d.root.Lstat(string(name))
	if err != nil {
		child.Close()
		return fail(outcomeFor(err), err)
	}
	if !sameObject(entryInfo(nil, now), expect) {
		child.Close()
		return fail(domain.OutcomeChangedDuringObservation, ErrIdentityChanged)
	}
	if got.Dev != d.self.Dev {
		// The caller's observation claimed no boundary; the device disagrees.
		child.Close()
		return fail(domain.OutcomeChangedDuringObservation, ErrMountBoundary)
	}
	return &portableDir{root: child, self: got}, nil
}

// OpenFile opens a regular file for reading: lstat must show the observed
// regular file on this directory's device, the name is then opened read-only
// with openReadFlags, and the open handle must still show that file.
func (d *portableDir) OpenFile(name []byte, expect EntryInfo) (File, error) {
	const op = "OpenFile"
	if err := checkName(op, name); err != nil {
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
	fi, err := d.root.Lstat(string(name))
	if err != nil {
		return fail(outcomeFor(err), err)
	}
	got := entryInfo(nil, fi)
	if !sameFile(got, expect) {
		return fail(domain.OutcomeChangedDuringObservation, ErrIdentityChanged)
	}
	if got.Dev != d.self.Dev {
		return fail(domain.OutcomeChangedDuringObservation, ErrMountBoundary)
	}
	f, err := d.root.OpenFile(string(name), openReadFlags, 0)
	if err != nil {
		return fail(outcomeFor(err), err)
	}
	if fi, err = f.Stat(); err != nil {
		f.Close()
		return fail(outcomeFor(err), err)
	}
	if !sameFile(entryInfo(nil, fi), expect) {
		f.Close()
		return fail(domain.OutcomeChangedDuringObservation, ErrIdentityChanged)
	}
	return &portableFile{f: f, name: bytes.Clone(name)}, nil
}

func (d *portableDir) Readlink(name []byte) ([]byte, error) {
	const op = "Readlink"
	if err := checkName(op, name); err != nil {
		return nil, err
	}
	target, err := d.root.Readlink(string(name))
	if err != nil {
		return nil, &Error{Op: op, Name: bytes.Clone(name), Outcome: readlinkOutcome(err), Err: err}
	}
	return []byte(target), nil
}

// FSInfo reports the directory's device only: the portable backend has no
// statfs and no mount table.
func (d *portableDir) FSInfo() (FSInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return FSInfo{}, d.failure("FSInfo", domain.OutcomeUnavailable, os.ErrClosed)
	}
	return FSInfo{Dev: d.self.Dev}, nil
}

func (d *portableDir) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	f := d.f
	d.f = nil
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

// portableFile is a regular file opened by OpenFile.
type portableFile struct {
	f    *os.File // nil once closed
	name []byte
}

func (f *portableFile) failure(op string, outcome domain.AccessOutcome, err error) *Error {
	return &Error{Op: op, Name: bytes.Clone(f.name), Outcome: outcome, Err: err}
}

func (f *portableFile) Stat() (EntryInfo, error) {
	const op = "FileStat"
	if f.f == nil {
		return EntryInfo{}, f.failure(op, domain.OutcomeUnavailable, os.ErrClosed)
	}
	fi, err := f.f.Stat()
	if err != nil {
		return EntryInfo{}, f.failure(op, outcomeFor(err), err)
	}
	return entryInfo(bytes.Clone(f.name), fi), nil
}

func (f *portableFile) ReadAt(p []byte, off int64) (int, error) {
	const op = "ReadAt"
	if f.f == nil {
		return 0, f.failure(op, domain.OutcomeUnavailable, os.ErrClosed)
	}
	if off < 0 {
		return 0, f.failure(op, "", errNegativeOffset)
	}
	n, err := f.f.ReadAt(p, off)
	if err != nil && err != io.EOF {
		return n, f.failure(op, outcomeFor(err), err)
	}
	return n, err
}

func (f *portableFile) Close() error {
	if f.f == nil {
		return nil
	}
	err := f.f.Close()
	f.f = nil
	if err != nil {
		return f.failure("Close", outcomeFor(err), err)
	}
	return nil
}
