package executor

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
)

// errChanged reports that a folder on the way, or the entry, is not what the
// index says: the item ends changed.
var errChanged = errors.New("executor: changed on disk since the last scan")

// folder is an open folder of the source, reached by rooted descent, with
// the handle of its parent, to lstat it again after a step.
type folder struct {
	id     int64
	path   []byte
	dir    fsaccess.Dir
	parent fsaccess.Dir // nil for the source root
	name   []byte
	// owned are the handles this folder closes (never the source root).
	owned []fsaccess.Dir
}

func (f *folder) close() {
	for _, d := range f.owned {
		d.Close()
	}
}

// link is one folder of a chain from the source root, as the index holds it.
type link struct {
	id         int64
	name, path []byte
	kind       string
	state      string
	dev, ino   sql.NullInt64
	boundary   bool
}

// chain returns the folders from the source root down to id.
func (r *run) chain(id int64) ([]link, error) {
	rows, err := r.e.st.Reader().QueryContext(r.bg, `WITH RECURSIVE up(id, parent_id, depth) AS (
			SELECT id, parent_id, 0 FROM entries WHERE id = ?1 AND source_id = ?2
			UNION ALL
			SELECT e.id, e.parent_id, up.depth + 1 FROM entries e JOIN up ON e.id = up.parent_id)
		SELECT e.id, e.name, e.path, e.kind, e.state, e.dev, e.ino, e.mount_boundary, up.parent_id IS NULL
		FROM up JOIN entries e ON e.id = up.id ORDER BY up.depth DESC`, id, string(r.src))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []link
	first := true
	for rows.Next() {
		var (
			l    link
			root bool
		)
		if err := rows.Scan(&l.id, &l.name, &l.path, &l.kind, &l.state, &l.dev, &l.ino, &l.boundary, &root); err != nil {
			return nil, err
		}
		if first && !root {
			return nil, errChanged
		}
		first = false
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errChanged
	}
	return out, nil
}

// openFolder opens the folder id by descent from the source root, one
// component at a time, each lstat'd and then opened against that lstat (as
// the scan does). Each must still be a folder and not a mount boundary, and
// where identity is stable, the object the index holds. Anything else is
// errChanged, or the fsaccess error (ErrMountBoundary for a mount).
func (r *run) openFolder(id int64) (*folder, error) {
	chain, err := r.chain(id)
	if err != nil {
		return nil, err
	}
	root := chain[0]
	if r.caps.StableIdentity && root.dev.Valid && root.ino.Valid {
		self := r.root.Self()
		if self.Dev != uint64(root.dev.Int64) || self.Ino != uint64(root.ino.Int64) {
			return nil, errChanged
		}
	}
	f := &folder{id: root.id, path: root.path, dir: r.root}
	for _, l := range chain[1:] {
		if l.state != "present" || l.kind != "directory" {
			f.close()
			return nil, errChanged
		}
		if l.boundary {
			f.close()
			return nil, &fsaccess.Error{Op: "OpenDir", Name: l.name, Err: fsaccess.ErrMountBoundary}
		}
		info, err := r.lstat(f.dir, l.name)
		if err != nil {
			f.close()
			return nil, err
		}
		switch {
		case info.MountBoundary:
			f.close()
			return nil, &fsaccess.Error{Op: "OpenDir", Name: l.name, Err: fsaccess.ErrMountBoundary}
		case info.Kind != domain.EntryDirectory,
			r.caps.StableIdentity && (!l.dev.Valid || !l.ino.Valid || uint64(l.dev.Int64) != info.Dev ||
				uint64(l.ino.Int64) != info.Ino):
			f.close()
			return nil, errChanged
		}
		done := r.rt.FSCall("open_dir")
		next, err := f.dir.OpenDir(l.name, info)
		done()
		if err != nil {
			f.close()
			return nil, err
		}
		// Keep the new folder and its parent; close the grandparent.
		var keep []fsaccess.Dir
		for _, d := range f.owned {
			if d == f.dir {
				keep = append(keep, d)
			} else {
				d.Close()
			}
		}
		f = &folder{id: l.id, path: l.path, dir: next, parent: f.dir, name: l.name, owned: append(keep, next)}
	}
	return f, nil
}

func (r *run) lstat(d fsaccess.Dir, name []byte) (fsaccess.EntryInfo, error) {
	done := r.rt.FSCall("lstat")
	defer done()
	return d.Lstat(name)
}

// sync fsyncs a folder the step changed.
func (r *run) sync(f *folder) error {
	w, ok := fsaccess.AsWriter(f.dir)
	if !ok {
		return fsaccess.ErrNoReplaceUnsupported
	}
	done := r.rt.FSCall("sync")
	defer done()
	return w.Sync()
}

// kindColumn is the entries.kind of an observed kind.
func kindColumn(k domain.EntryKind) string {
	switch k {
	case domain.EntryDirectory, domain.EntryFile, domain.EntrySymlink:
		return string(k)
	}
	return "special"
}

// matches reports whether info is the entry the item expects, compared as
// the scan's unchanged test does under the source's capabilities: the same
// kind; device and inode only where identity is stable; for anything but a
// folder, the same size and a modification time within the resolution (an
// hour off on a local-time filesystem). A folder's size and time follow its
// contents, which the move carries along, so they are not compared.
func (r *run) matches(it item, info fsaccess.EntryInfo) bool {
	if kindColumn(info.Kind) != it.kind {
		return false
	}
	if r.caps.StableIdentity &&
		(!it.dev.Valid || !it.ino.Valid || uint64(it.dev.Int64) != info.Dev || uint64(it.ino.Int64) != info.Ino) {
		return false
	}
	if it.kind == "directory" {
		return true
	}
	return it.size.Valid && it.size.Int64 == info.Size && it.mtime.Valid &&
		sameTime(it.mtime.Int64, info.ModTime.UnixNano(), r.caps)
}

func sameTime(stored, observed int64, caps fsaccess.Capabilities) bool {
	res := int64(caps.TimeResolution)
	d := observed - stored
	if abs(d) <= res {
		return true
	}
	h := int64(time.Hour)
	return caps.LocalTime && (abs(d-h) <= res || abs(d+h) <= res)
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Findings about one name (the detail of a manual_recovery item).
const (
	foundAbsent = "absent"
	foundSame   = "same"
	foundOther  = "other"
)

// look classifies name in f: absent, the expected entry (same), or something
// else. An error means the name could not be looked at (I/O).
func (r *run) look(it item, f *folder, name []byte) (string, fsaccess.EntryInfo, error) {
	info, err := r.lstat(f.dir, name)
	if err != nil {
		if o, _ := fsaccess.OutcomeOf(err); o == domain.OutcomeAbsent {
			return foundAbsent, info, nil
		}
		return "", info, err
	}
	if !info.MountBoundary && r.matches(it, info) {
		return foundSame, info, nil
	}
	return foundOther, info, nil
}

// lookIn opens folder id and classifies name in it. A folder that is gone
// makes the name absent; one that changed makes it other.
func (r *run) lookIn(it item, id int64, name []byte) (string, *folder, fsaccess.EntryInfo, error) {
	f, err := r.openFolder(id)
	if err != nil {
		switch {
		case errors.Is(err, errChanged), errors.Is(err, fsaccess.ErrMountBoundary), errors.Is(err, fsaccess.ErrNotDirectory):
			return foundOther, nil, fsaccess.EntryInfo{}, nil
		}
		switch o, _ := fsaccess.OutcomeOf(err); o {
		case domain.OutcomeAbsent:
			return foundAbsent, nil, fsaccess.EntryInfo{}, nil
		case domain.OutcomeChangedDuringObservation:
			return foundOther, nil, fsaccess.EntryInfo{}, nil
		}
		return "", nil, fsaccess.EntryInfo{}, err
	}
	found, info, err := r.look(it, f, name)
	if err != nil {
		f.close()
		return "", nil, info, err
	}
	return found, f, info, nil
}

// emptyFolder reports whether name in f is a folder with nothing in it.
func (r *run) emptyFolder(f *folder, name []byte, info fsaccess.EntryInfo) (bool, error) {
	if info.Kind != domain.EntryDirectory || info.MountBoundary {
		return false, nil
	}
	done := r.rt.FSCall("open_dir")
	d, err := f.dir.OpenDir(name, info)
	done()
	if err != nil {
		return false, err
	}
	defer d.Close()
	done = r.rt.FSCall("read_batch")
	got, err := d.ReadBatch(1)
	done()
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return len(got) == 0 && err == nil, err
}

// findings is the manual_recovery detail.
func findings(from, to string) string {
	return fmt.Sprintf(`{"from":%q,"to":%q}`, from, to)
}

// postFacts are the index facts of an lstat after a step.
func postFacts(info fsaccess.EntryInfo) index.PostFacts {
	f := index.PostFacts{Dev: info.Dev, Ino: info.Ino, MtimeNs: info.ModTime.UnixNano()}
	if !info.Ctime.IsZero() {
		f.CtimeNs = info.Ctime.UnixNano()
	}
	return f
}

// folderFacts are a folder's facts after a step changed it: an lstat through
// its parent's handle, or, for the source root, the root opened again. When
// that fails, the facts of the handle are used.
func (r *run) folderFacts(f *folder) index.PostFacts {
	if f.parent != nil {
		if info, err := r.lstat(f.parent, f.name); err == nil {
			return postFacts(info)
		}
		return postFacts(f.dir.Self())
	}
	opened, err := r.e.src.Open(r.bg, r.src)
	if err != nil {
		r.e.log.Warn("executor: reopen the source root for its facts", "source", r.src, "err", err)
		return postFacts(f.dir.Self())
	}
	defer opened.Root.Close()
	return postFacts(opened.Root.Self())
}
