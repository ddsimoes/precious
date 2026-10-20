package content

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/sources"
	"precious/internal/store"
)

// Row is what the index records of a file, which an open must find
// unchanged (design D4, D17): its raw source-relative path, its size, and
// the times and inode a scan read.
type Row struct {
	Path                  []byte
	Size                  int64
	MtimeNs, CtimeNs, Ino sql.NullInt64
}

// Matches reports whether info, an lstat of the row's path, is the regular
// file the row describes, by the scanner's test of an unchanged entry (R1
// design D8, R2 design D4): a regular file and not a mount boundary, the
// row's size, its modification time within the filesystem's resolution (or
// an hour off on a local-time filesystem), its change time likewise when
// both are known, and its inode where identity is stable.
func Matches(r Row, info fsaccess.EntryInfo, caps fsaccess.Capabilities) bool {
	if info.Kind != domain.EntryFile || info.MountBoundary || info.Size != r.Size || !r.MtimeNs.Valid {
		return false
	}
	if !sameTime(r.MtimeNs.Int64, info.ModTime.UnixNano(), caps) {
		return false
	}
	if r.CtimeNs.Valid && r.CtimeNs.Int64 != 0 && !info.Ctime.IsZero() &&
		!sameTime(r.CtimeNs.Int64, info.Ctime.UnixNano(), caps) {
		return false
	}
	return !caps.StableIdentity || (r.Ino.Valid && uint64(r.Ino.Int64) == info.Ino)
}

// sameTime is the unchanged-time test of R1 design D8.
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

// sameFile reports whether an open file's fstat still shows the file its
// lstat described: same device, inode, size, modification time, and change
// time.
func sameFile(got, want fsaccess.EntryInfo) bool {
	return got.Kind == domain.EntryFile && got.Dev == want.Dev && got.Ino == want.Ino && got.Size == want.Size &&
		got.ModTime.Equal(want.ModTime) && got.Ctime.Equal(want.Ctime)
}

// FSCaller brackets each filesystem call for a job's watchdog;
// jobs.Runtime satisfies it.
type FSCaller interface {
	FSCall(op string) (done func())
}

// noCalls watches nothing, for opens outside a job.
type noCalls struct{}

func (noCalls) FSCall(string) func() { return func() {} }

// Opener is the folder chain open below a source root: walking a path
// reuses the prefix it shares with the previous walk. Every folder is
// reached by Lstat then OpenDir with that lstat, so nothing is followed
// through a symlink and no mount is crossed. OpenAt, the hashing job, and
// the media job (r5 design D4) open files through it. It is not safe for
// concurrent use.
type Opener struct {
	root  fsaccess.Dir
	calls FSCaller
	names [][]byte
	dirs  []fsaccess.Dir
}

// NewOpener returns an Opener below root, bracketing each filesystem call
// with calls (a job's Runtime); nil watches nothing. Close closes the
// folders it opened; root stays open.
func NewOpener(root fsaccess.Dir, calls FSCaller) *Opener {
	if calls == nil {
		calls = noCalls{}
	}
	return &Opener{root: root, calls: calls}
}

// Open opens r's file as OpenAt does, with OpenAt's errors: a path that is
// gone, replaced, changed, or unreadable is a domain invalid_entry_state
// error (an unreadable one carries fsaccess.OutcomeUnreadable, and a gone or
// replaced one its outcome), and any other failure is returned as is. The
// caller closes the file.
func (c *Opener) Open(r Row, caps fsaccess.Capabilities) (fsaccess.File, fsaccess.EntryInfo, error) {
	f, info, err := c.open(r, caps)
	if err != nil {
		return nil, fsaccess.EntryInfo{}, entryStateError(err, r.Path)
	}
	return f, info, nil
}

// Close closes the folders of the chain.
func (c *Opener) Close() { c.closeFrom(0) }

// walkError is a failure of the walk to a file: the operation that failed
// and its error (an *fsaccess.Error with an outcome), or errMismatch.
type walkError struct {
	op  string
	err error
}

func (e *walkError) Error() string { return e.op + ": " + e.err.Error() }
func (e *walkError) Unwrap() error { return e.err }

// errMismatch is a folder on the way that is no longer a plain folder, or a
// file that no longer matches its row.
var errMismatch = errors.New("content: the path no longer matches the index")

// dir returns the open folder holding path's last component, and that
// component.
func (c *Opener) dir(path []byte) (fsaccess.Dir, []byte, error) {
	comps := bytes.Split(path, []byte{'/'})
	name, folders := comps[len(comps)-1], comps[:len(comps)-1]
	k := 0
	for k < len(c.names) && k < len(folders) && bytes.Equal(c.names[k], folders[k]) {
		k++
	}
	c.closeFrom(k)
	parent := c.root
	if k > 0 {
		parent = c.dirs[k-1]
	}
	for _, f := range folders[k:] {
		done := c.calls.FSCall("Lstat")
		info, err := parent.Lstat(f)
		done()
		if err != nil {
			return nil, nil, &walkError{"Lstat", err}
		}
		if info.Kind != domain.EntryDirectory || info.MountBoundary {
			return nil, nil, &walkError{"Lstat", errMismatch}
		}
		done = c.calls.FSCall("OpenDir")
		next, err := parent.OpenDir(f, info)
		done()
		if err != nil {
			return nil, nil, &walkError{"OpenDir", err}
		}
		c.names, c.dirs = append(c.names, f), append(c.dirs, next)
		parent = next
	}
	return parent, name, nil
}

// closeFrom closes the folders of the chain from depth k down.
func (c *Opener) closeFrom(k int) {
	for i := len(c.dirs) - 1; i >= k; i-- {
		done := c.calls.FSCall("Close")
		_ = c.dirs[i].Close()
		done()
	}
	if k < len(c.dirs) {
		c.names, c.dirs = c.names[:k], c.dirs[:k]
	}
}

// open walks r's path and opens its file when an lstat matches r under
// caps; the open itself checks the opened object is the one lstat saw. Its
// errors are the walk's (walkError), which the hashing job classifies.
func (c *Opener) open(r Row, caps fsaccess.Capabilities) (fsaccess.File, fsaccess.EntryInfo, error) {
	dir, name, err := c.dir(r.Path)
	if err != nil {
		return nil, fsaccess.EntryInfo{}, err
	}
	done := c.calls.FSCall("Lstat")
	info, err := dir.Lstat(name)
	done()
	if err != nil {
		return nil, fsaccess.EntryInfo{}, &walkError{"Lstat", err}
	}
	if !Matches(r, info, caps) {
		return nil, fsaccess.EntryInfo{}, &walkError{"Lstat", errMismatch}
	}
	done = c.calls.FSCall("OpenFile")
	f, err := dir.OpenFile(name, info)
	done()
	if err != nil {
		return nil, fsaccess.EntryInfo{}, &walkError{"OpenFile", err}
	}
	return f, info, nil
}

// OpenAt walks r's path below root, one rooted component at a time, and
// opens the file read-only (with O_NOATIME where the backend can) when it
// matches r (Matches); the open re-checks that the object opened is the one
// lstat described. A path that is gone, replaced, changed, or unreadable is
// a domain invalid_entry_state error, which a rescan clears; any other
// failure (an I/O error) is returned as is. The viewer and hashing share
// this walk (design D17). The caller closes the file; root stays open.
func OpenAt(root fsaccess.Dir, r Row, caps fsaccess.Capabilities) (fsaccess.File, fsaccess.EntryInfo, error) {
	o := NewOpener(root, nil)
	defer o.Close()
	return o.Open(r, caps)
}

// entryStateError maps a failed walk to invalid_entry_state where the path
// no longer matches its entry or cannot be read.
func entryStateError(err error, path []byte) error {
	if errors.Is(err, errMismatch) {
		return domain.Errorf(domain.CodeInvalidEntryState,
			"%s no longer matches its entry; a rescan updates it", domain.DisplayName(path))
	}
	switch o, _ := fsaccess.OutcomeOf(err); o {
	case domain.OutcomeAbsent, domain.OutcomeChangedDuringObservation:
		return domain.Wrap(domain.CodeInvalidEntryState, err,
			"%s no longer matches its entry; a rescan updates it", domain.DisplayName(path))
	case domain.OutcomeUnreadable:
		return domain.Wrap(domain.CodeInvalidEntryState, err, "%s cannot be read", domain.DisplayName(path))
	}
	return err
}

// Archive is a listed archive's file, open for reading after the identity
// checks of design D17.
type Archive struct {
	Entry  domain.EntryID
	Source domain.SourceID
	Format domain.ArchiveFormat
	// Name is the archive file's raw name, Row its indexed facts.
	Name []byte
	Row  Row
	File fsaccess.File
	Info fsaccess.EntryInfo
	root fsaccess.Dir
}

// Close closes the archive file and its source root.
func (a *Archive) Close() error {
	err := a.File.Close()
	if cerr := a.root.Close(); err == nil {
		err = cerr
	}
	return err
}

// OpenArchive opens the file of the complete archive entry id through
// OpenAt, after checking that the entry is present and that its row still
// has the identity the listing recorded. A missing entry is not_found; an
// archive that is not complete, an entry that changed since its listing,
// and a file on disk that no longer matches are invalid_entry_state; a
// source that is not online is source_offline. The caller closes it.
func OpenArchive(ctx context.Context, q store.Queryer, src *sources.Service, id domain.EntryID) (*Archive, error) {
	a := &Archive{Entry: id}
	var (
		state, kind, estate string
		source              string
		listed              Row
	)
	err := q.QueryRowContext(ctx, `SELECT e.source_id, e.name, e.path, e.kind, e.state, e.size, e.mtime_ns, e.ctime_ns,
			e.ino, a.format, a.state, a.size, a.mtime_ns, a.ctime_ns, a.ino
		FROM entries e JOIN archives a ON a.entry_id = e.id WHERE e.id = ?`, int64(id)).
		Scan(&source, &a.Name, &a.Row.Path, &kind, &estate, &a.Row.Size, &a.Row.MtimeNs, &a.Row.CtimeNs, &a.Row.Ino,
			&a.Format, &state, &listed.Size, &listed.MtimeNs, &listed.CtimeNs, &listed.Ino)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.Errorf(domain.CodeNotFound, "archive %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("content: read archive %s: %w", id, err)
	}
	a.Source = domain.SourceID(source)
	switch {
	case kind != string(domain.EntryFile) || estate != "present":
		return nil, domain.Errorf(domain.CodeInvalidEntryState, "entry %s is not a present file", id)
	case state != string(domain.ArchiveComplete):
		return nil, domain.Errorf(domain.CodeInvalidEntryState, "archive %s is %s, not completely read", id, state)
	case listed.Size != a.Row.Size || listed.MtimeNs != a.Row.MtimeNs || listed.CtimeNs != a.Row.CtimeNs ||
		listed.Ino != a.Row.Ino:
		return nil, domain.Errorf(domain.CodeInvalidEntryState, "archive %s changed since it was listed", id)
	}
	opened, err := src.Open(ctx, a.Source)
	if err != nil {
		return nil, err
	}
	f, info, err := OpenAt(opened.Root, a.Row, opened.Source.Caps)
	if err != nil {
		opened.Root.Close()
		return nil, err
	}
	a.File, a.Info, a.root = f, info, opened.Root
	return a, nil
}
