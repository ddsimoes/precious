// Package instrument wraps an fsaccess.FS to count and log every filesystem
// call, so tests can prove scalability and safety invariants by counting calls
// and bytes rather than by timing (§13.1, design D3). It also offers hooks to
// interleave test actions with calls and to inject failures.
package instrument

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"

	"precious/internal/fsaccess"
)

// Op names one fsaccess operation. Self is not an operation: it returns data
// captured when the directory was opened.
type Op string

const (
	OpOpenRoot  Op = "OpenRoot"
	OpReadBatch Op = "ReadBatch"
	OpLstat     Op = "Lstat"
	OpOpenDir   Op = "OpenDir"
	OpReadlink  Op = "Readlink"
	OpFSInfo    Op = "FSInfo"
	OpOpenFile  Op = "OpenFile"
	OpReadAt    Op = "ReadAt"
	OpFileStat  Op = "FileStat"
	// OpClose closes a directory or a file; Path tells which.
	OpClose Op = "Close"
	// OpMounts and OpCapabilities are the FS-level queries; neither touches
	// a source tree.
	OpMounts       Op = "Mounts"
	OpCapabilities Op = "Capabilities"
)

// Call is one logged call.
type Call struct {
	Op Op
	// Root is the cleaned root path the call belongs to: the path asked about
	// for Capabilities, and empty for Mounts.
	Root string
	// Path holds the raw components below Root that the call addresses: the
	// directory itself for ReadBatch, FSInfo, and a directory's Close; the
	// directory plus the name for Lstat, OpenDir, Readlink, and OpenFile; the
	// file's path for ReadAt, FileStat, and a file's Close; nil for OpenRoot,
	// Mounts, and Capabilities.
	Path [][]byte
	// N is the batch size requested by ReadBatch, or len(p) of a ReadAt.
	N int
	// Off is the offset requested by ReadAt.
	Off int64
	// Entries is the number of entries ReadBatch returned.
	Entries int
	// Bytes is the number of content bytes ReadAt returned.
	Bytes int
	// Err is the returned error (io.EOF ends a listing). It is nil in the
	// Call passed to hooks, which run before the call.
	Err error
}

// Depth is the number of components below the root the call addresses: 0 for
// OpenRoot and for listing the root, 1 for an Lstat of a root entry, and so on.
func (c Call) Depth() int { return len(c.Path) }

// FullPath is Root joined with Path by "/". Components cannot contain "/", so
// the result is unambiguous; it is the key used by InjectError.
func (c Call) FullPath() string { return joinPath(c.Root, c.Path) }

func joinPath(root string, comps [][]byte) string {
	if len(comps) == 0 {
		return root
	}
	var b strings.Builder
	b.WriteString(root)
	if !strings.HasSuffix(root, "/") {
		b.WriteByte('/')
	}
	for i, c := range comps {
		if i > 0 {
			b.WriteByte('/')
		}
		b.Write(c)
	}
	return b.String()
}

type injectKey struct {
	op   Op
	path string
}

// Recorder is an fsaccess.FS that delegates to another FS and records every
// call made through it and through the directories it returns. It is safe for
// concurrent use.
type Recorder struct {
	inner fsaccess.FS

	mu            sync.Mutex
	counts        map[Op]int
	entries       int64
	bytesRead     int64
	calls         []Call
	inject        map[injectKey]error
	beforeCall    func(Call)
	beforeOpenDir func(Call)
}

var _ fsaccess.FS = (*Recorder)(nil)

// Wrap returns a Recorder around inner.
func Wrap(inner fsaccess.FS) *Recorder {
	return &Recorder{inner: inner, counts: make(map[Op]int), inject: make(map[injectKey]error)}
}

// Count returns how many calls of op completed (including failed ones).
func (r *Recorder) Count(op Op) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[op]
}

// Counts returns a copy of the per-operation counters.
func (r *Recorder) Counts() map[Op]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[Op]int, len(r.counts))
	for op, n := range r.counts {
		out[op] = n
	}
	return out
}

// Entries returns the total number of entries returned by ReadBatch.
func (r *Recorder) Entries() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries
}

// BytesRead returns the total number of content bytes ReadAt returned.
func (r *Recorder) BytesRead() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytesRead
}

// Calls returns a copy of the call log in completion order.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

// MaxDepth returns the largest Depth of any logged call.
func (r *Recorder) MaxDepth() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	maxDepth := 0
	for _, c := range r.calls {
		maxDepth = max(maxDepth, c.Depth())
	}
	return maxDepth
}

// Reset clears counters, byte counts, and the call log. Hooks and injected
// errors stay.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts = make(map[Op]int)
	r.entries = 0
	r.bytesRead = 0
	r.calls = nil
}

// SetBeforeCall installs fn to run before every call is delegated (nil
// removes it). fn runs on the caller's goroutine without any Recorder lock
// held, so it may block to simulate a hung filesystem call.
func (r *Recorder) SetBeforeCall(fn func(Call)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beforeCall = fn
}

// SetBeforeOpenDir installs fn to run before every OpenDir is delegated, after
// the BeforeCall hook (nil removes it). Tests use it to change the filesystem
// between the caller's observation and the open.
func (r *Recorder) SetBeforeOpenDir(fn func(Call)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beforeOpenDir = fn
}

// InjectError makes every later call of op whose FullPath equals path return
// err without being delegated (hooks still run). For OpenRoot and
// Capabilities, path is the cleaned path asked about; for Mounts it is empty.
// Tests normally pass an *fsaccess.Error with the outcome they want to
// exercise.
func (r *Recorder) InjectError(op Op, path string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inject[injectKey{op, path}] = err
}

// ClearErrors removes every injected error.
func (r *Recorder) ClearErrors() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inject = make(map[injectKey]error)
}

// before runs the hooks for c and returns the injected error, if any.
func (r *Recorder) before(c Call) error {
	r.mu.Lock()
	beforeCall, beforeOpenDir := r.beforeCall, r.beforeOpenDir
	r.mu.Unlock()
	if beforeCall != nil {
		beforeCall(c)
	}
	if c.Op == OpOpenDir && beforeOpenDir != nil {
		beforeOpenDir(c)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inject[injectKey{c.Op, c.FullPath()}]
}

func (r *Recorder) record(c Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[c.Op]++
	switch c.Op {
	case OpReadBatch:
		r.entries += int64(c.Entries)
	case OpReadAt:
		r.bytesRead += int64(c.Bytes)
	}
	r.calls = append(r.calls, c)
}

// cleanRoot is the Root of a call that names an absolute path; any other path
// is kept as given.
func cleanRoot(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return path
}

// Mounts delegates to the wrapped FS.
func (r *Recorder) Mounts() ([]fsaccess.Mount, error) {
	c := Call{Op: OpMounts}
	if err := r.before(c); err != nil {
		c.Err = err
		r.record(c)
		return nil, err
	}
	mounts, err := r.inner.Mounts()
	c.Err = err
	r.record(c)
	return mounts, err
}

// Capabilities delegates to the wrapped FS.
func (r *Recorder) Capabilities(path string) (fsaccess.Capabilities, error) {
	c := Call{Op: OpCapabilities, Root: cleanRoot(path)}
	if err := r.before(c); err != nil {
		c.Err = err
		r.record(c)
		return fsaccess.Capabilities{}, err
	}
	caps, err := r.inner.Capabilities(path)
	c.Err = err
	r.record(c)
	return caps, err
}

// OpenRoot delegates to the wrapped FS and wraps the returned directory.
func (r *Recorder) OpenRoot(path string) (fsaccess.Dir, error) {
	root := cleanRoot(path)
	c := Call{Op: OpOpenRoot, Root: root}
	if err := r.before(c); err != nil {
		c.Err = err
		r.record(c)
		return nil, err
	}
	d, err := r.inner.OpenRoot(path)
	c.Err = err
	r.record(c)
	if err != nil {
		return nil, err
	}
	return &dir{r: r, inner: d, root: root}, nil
}

type dir struct {
	r     *Recorder
	inner fsaccess.Dir
	root  string
	path  [][]byte
}

func (d *dir) child(name []byte) [][]byte {
	p := make([][]byte, len(d.path)+1)
	copy(p, d.path)
	p[len(d.path)] = bytes.Clone(name)
	return p
}

func (d *dir) Self() fsaccess.EntryInfo { return d.inner.Self() }

func (d *dir) ReadBatch(n int) ([]fsaccess.DirEntry, error) {
	c := Call{Op: OpReadBatch, Root: d.root, Path: d.path, N: n}
	if err := d.r.before(c); err != nil {
		c.Err = err
		d.r.record(c)
		return nil, err
	}
	entries, err := d.inner.ReadBatch(n)
	c.Entries, c.Err = len(entries), err
	d.r.record(c)
	return entries, err
}

func (d *dir) Lstat(name []byte) (fsaccess.EntryInfo, error) {
	c := Call{Op: OpLstat, Root: d.root, Path: d.child(name)}
	if err := d.r.before(c); err != nil {
		c.Err = err
		d.r.record(c)
		return fsaccess.EntryInfo{}, err
	}
	info, err := d.inner.Lstat(name)
	c.Err = err
	d.r.record(c)
	return info, err
}

func (d *dir) OpenDir(name []byte, expect fsaccess.EntryInfo) (fsaccess.Dir, error) {
	c := Call{Op: OpOpenDir, Root: d.root, Path: d.child(name)}
	if err := d.r.before(c); err != nil {
		c.Err = err
		d.r.record(c)
		return nil, err
	}
	child, err := d.inner.OpenDir(name, expect)
	c.Err = err
	d.r.record(c)
	if err != nil {
		return nil, err
	}
	return &dir{r: d.r, inner: child, root: d.root, path: c.Path}, nil
}

func (d *dir) OpenFile(name []byte, expect fsaccess.EntryInfo) (fsaccess.File, error) {
	c := Call{Op: OpOpenFile, Root: d.root, Path: d.child(name)}
	if err := d.r.before(c); err != nil {
		c.Err = err
		d.r.record(c)
		return nil, err
	}
	f, err := d.inner.OpenFile(name, expect)
	c.Err = err
	d.r.record(c)
	if err != nil {
		return nil, err
	}
	return &file{r: d.r, inner: f, root: d.root, path: c.Path}, nil
}

func (d *dir) Readlink(name []byte) ([]byte, error) {
	c := Call{Op: OpReadlink, Root: d.root, Path: d.child(name)}
	if err := d.r.before(c); err != nil {
		c.Err = err
		d.r.record(c)
		return nil, err
	}
	target, err := d.inner.Readlink(name)
	c.Err = err
	d.r.record(c)
	return target, err
}

func (d *dir) FSInfo() (fsaccess.FSInfo, error) {
	c := Call{Op: OpFSInfo, Root: d.root, Path: d.path}
	if err := d.r.before(c); err != nil {
		c.Err = err
		d.r.record(c)
		return fsaccess.FSInfo{}, err
	}
	info, err := d.inner.FSInfo()
	c.Err = err
	d.r.record(c)
	return info, err
}

func (d *dir) Close() error {
	c := Call{Op: OpClose, Root: d.root, Path: d.path}
	if err := d.r.before(c); err != nil {
		c.Err = err
		d.r.record(c)
		return err
	}
	err := d.inner.Close()
	c.Err = err
	d.r.record(c)
	return err
}

type file struct {
	r     *Recorder
	inner fsaccess.File
	root  string
	path  [][]byte
}

func (f *file) Stat() (fsaccess.EntryInfo, error) {
	c := Call{Op: OpFileStat, Root: f.root, Path: f.path}
	if err := f.r.before(c); err != nil {
		c.Err = err
		f.r.record(c)
		return fsaccess.EntryInfo{}, err
	}
	info, err := f.inner.Stat()
	c.Err = err
	f.r.record(c)
	return info, err
}

// ReadAt is logged with N = len(p), Off = off, and Bytes = the bytes returned,
// which BytesRead sums.
func (f *file) ReadAt(p []byte, off int64) (int, error) {
	c := Call{Op: OpReadAt, Root: f.root, Path: f.path, N: len(p), Off: off}
	if err := f.r.before(c); err != nil {
		c.Err = err
		f.r.record(c)
		return 0, err
	}
	n, err := f.inner.ReadAt(p, off)
	c.Bytes, c.Err = n, err
	f.r.record(c)
	return n, err
}

func (f *file) Close() error {
	c := Call{Op: OpClose, Root: f.root, Path: f.path}
	if err := f.r.before(c); err != nil {
		c.Err = err
		f.r.record(c)
		return err
	}
	err := f.inner.Close()
	c.Err = err
	f.r.record(c)
	return err
}
