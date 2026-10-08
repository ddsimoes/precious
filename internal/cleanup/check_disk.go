package cleanup

import (
	"bytes"
	"context"
	"errors"

	"precious/internal/content"
	"precious/internal/domain"
	"precious/internal/fsaccess"
)

// openSource is a source opened for the check: its root, its
// capabilities, and a walker for lstats below it.
type openSource struct {
	root fsaccess.Dir
	caps fsaccess.Capabilities
	w    *walker
}

func (o *openSource) close() {
	o.w.close()
	_ = o.root.Close()
}

// walker lstats entries below a source root by rooted descent, one
// component at a time: every folder on the way is reached by Lstat, then
// OpenDir with that lstat, so nothing is followed through a link and no
// mount is crossed. It keeps the folders of the last path open, so entries
// read in path order reuse them.
type walker struct {
	root  fsaccess.Dir
	names [][]byte
	dirs  []fsaccess.Dir
}

// errMismatch is an entry, or a folder on the way to it, that is no longer
// what the index holds.
var errMismatch = errors.New("cleanup: the entry no longer matches the index")

// lstat describes the entry at path below the root.
func (w *walker) lstat(path []byte) (fsaccess.EntryInfo, error) {
	if len(path) == 0 {
		return w.root.Self(), nil
	}
	comps := bytes.Split(path, []byte{'/'})
	name, folders := comps[len(comps)-1], comps[:len(comps)-1]
	k := 0
	for k < len(w.names) && k < len(folders) && bytes.Equal(w.names[k], folders[k]) {
		k++
	}
	w.closeFrom(k)
	parent := w.root
	if k > 0 {
		parent = w.dirs[k-1]
	}
	for _, f := range folders[k:] {
		info, err := parent.Lstat(f)
		if err != nil {
			return fsaccess.EntryInfo{}, err
		}
		if info.Kind != domain.EntryDirectory || info.MountBoundary {
			return fsaccess.EntryInfo{}, errMismatch
		}
		next, err := parent.OpenDir(f, info)
		if err != nil {
			return fsaccess.EntryInfo{}, err
		}
		w.names, w.dirs = append(w.names, f), append(w.dirs, next)
		parent = next
	}
	return parent.Lstat(name)
}

// closeFrom closes the open folders from depth k down.
func (w *walker) closeFrom(k int) {
	for i := len(w.dirs) - 1; i >= k; i-- {
		_ = w.dirs[i].Close()
	}
	if k < len(w.dirs) {
		w.names, w.dirs = w.names[:k], w.dirs[:k]
	}
}

func (w *walker) close() { w.closeFrom(0) }

// rowOf is the identity the index holds of the file e, as the content
// openers check it.
func rowOf(e *setEntry) content.Row {
	return content.Row{Path: e.path, Size: e.size, MtimeNs: e.mtime, CtimeNs: e.ctime, Ino: e.ino}
}

// sameEntry reports whether info, an lstat of e's path, is the entry the
// index holds: a present file by the scanner's unchanged test
// (content.Matches); anything else by its kind, its link text's length,
// and its inode where identity is stable. A mount point never is.
func sameEntry(e *setEntry, info fsaccess.EntryInfo, caps fsaccess.Capabilities) bool {
	if e.kind == domain.EntryFile && e.state == "present" {
		return content.Matches(rowOf(e), info, caps)
	}
	if info.Kind != e.kind || info.MountBoundary {
		return false
	}
	if e.kind == domain.EntrySymlink && info.Size != e.size {
		return false
	}
	return !caps.StableIdentity || e.ino.Valid && uint64(e.ino.Int64) == info.Ino
}

// changedError is the error of an entry of the set the disk no longer
// shows as the index holds it: the check fails, and a rescan updates the
// index.
func changedError(err error, path []byte) error {
	if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeUnreadable {
		return domain.Wrap(domain.CodeInvalidEntryState, err, "%s cannot be read", domain.DisplayName(path))
	}
	return domain.Wrap(domain.CodeInvalidEntryState, err,
		"%s no longer matches its entry; a rescan updates it", domain.DisplayName(path))
}

// hashEntry reads the file e of the opened source o in full.
func hashEntry(ctx context.Context, o *openSource, e *setEntry) ([32]byte, fsaccess.EntryInfo, error) {
	return content.HashEntry(ctx, o.root, rowOf(e), o.caps)
}
