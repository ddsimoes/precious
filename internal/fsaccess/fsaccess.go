// Package fsaccess is the only way precious touches source filesystems (§5.4,
// §12.2, design D3): rooted, read-only, bounded access that never follows
// symlinks, never opens special files, never crosses mounts, and preserves
// filename bytes exactly.
//
// The interfaces below are a shared contract. They deliberately have no method
// that creates, writes, renames, removes, or changes metadata of any entry:
// nothing writes to a source. File content is read only through Dir.OpenFile,
// which opens a regular file for reading after checking it is the one the
// caller observed, and only copy searches call it (M4 design D5). Every other
// caller observes metadata alone.
//
// Names are raw bytes exactly as returned by the operating system. A name passed
// to a Dir method must be a single path component: non-empty, not "." or "..",
// and free of '/' and NUL. Anything else fails with an *Error whose Op is the
// method name and whose Err is ErrInvalidName, without touching the filesystem.
package fsaccess

import (
	"errors"
	"io/fs"
	"time"

	"precious/internal/domain"
)

// FS opens configured source roots and describes the volumes holding them
// (design D3). Implementations: the Linux backend (NewOS on Linux), the
// portable os.Root backend (NewPortable), the instrumented wrapper, and the
// synthetic in-memory tree used by tests.
type FS interface {
	// OpenRoot opens the directory at the absolute operator-configured path and
	// snapshots the process mount table for later boundary decisions.
	OpenRoot(path string) (Dir, error)
	// Mounts returns the current mount table in a portable shape, one Mount
	// per mounted filesystem the backend knows of. Each call reads the table
	// afresh.
	Mounts() ([]Mount, error)
	// Capabilities describes the filesystem holding the absolute path. A
	// filesystem the backend cannot classify gets UnknownCapabilities.
	Capabilities(path string) (Capabilities, error)
}

// VolumeKind names how a volume's identity was established (design D4).
type VolumeKind string

const (
	// VolumeUUID is a block-device filesystem identified by its UUID.
	VolumeUUID VolumeKind = "uuid"
	// VolumeZFS is a ZFS dataset identified by its name.
	VolumeZFS VolumeKind = "zfs"
	// VolumeFSID is a filesystem identified by its statfs f_fsid.
	VolumeFSID VolumeKind = "fsid"
	// VolumePath is the weak fallback: a volume known only by its mount point.
	VolumePath VolumeKind = "path"
)

// Volume identifies the filesystem a source lives on, independent of where it
// is mounted (§6.1, design D4).
type Volume struct {
	Kind VolumeKind
	// ID is the kind's identifier: the UUID, the dataset name, the f_fsid, or
	// the mount point.
	ID     string
	Label  string
	FSType string
	// DeviceKey groups volumes that share a physical device; the job runner
	// claims work by it.
	DeviceKey string
	// Strong reports whether ID survives remounting elsewhere; a path volume
	// is weak.
	Strong bool
}

// Mount is one mounted filesystem.
type Mount struct {
	// Point is the absolute mount point.
	Point string
	// Root is the directory of the filesystem mounted at Point ("/" unless
	// Point is a bind mount of a subdirectory).
	Root     []byte
	Volume   Volume
	ReadOnly bool
}

// Capabilities are the behaviours of a filesystem that comparisons of names
// and times must respect (§6.1, design D3). The JSON form is what a source
// records.
type Capabilities struct {
	// Known is false for the conservative set given to an unclassified
	// filesystem.
	Known                  bool `json:"known"`
	ReadOnly               bool `json:"read_only"`
	CaseSensitive          bool `json:"case_sensitive"`
	NormalizationSensitive bool `json:"normalization_sensitive"`
	// StableIdentity reports whether EntryInfo.Dev and Ino survive remounts;
	// they are meaningful only when it is true.
	StableIdentity bool `json:"stable_identity"`
	// LocalTime reports timestamps stored in local time, which shift with
	// daylight saving.
	LocalTime bool `json:"local_time"`
	HardLinks bool `json:"hard_links"`
	// TimeResolution is the granularity of stored modification times.
	TimeResolution time.Duration `json:"time_resolution_ns"`
}

// unknownTimeResolution is the coarsest resolution of a common filesystem
// (FAT), so equal times on any filesystem compare equal under it.
const unknownTimeResolution = 2 * time.Second

// UnknownCapabilities returns the conservative set for a filesystem whose type
// is not recognised: case- and normalization-insensitive, no stable identity,
// no hard links, UTC times, and 2-second resolution. readOnly comes from the
// mount when it is known; pass false otherwise.
func UnknownCapabilities(readOnly bool) Capabilities {
	return Capabilities{ReadOnly: readOnly, TimeResolution: unknownTimeResolution}
}

// pathVolume is the weak Volume of a filesystem known only by its mount point.
func pathVolume(point, fsType string) Volume {
	return Volume{Kind: VolumePath, ID: point, FSType: fsType, DeviceKey: "mount:" + point}
}

// pathMount is the weak Mount of a filesystem known only by its mount point.
func pathMount(point string, root []byte, fsType string, readOnly bool) Mount {
	return Mount{Point: point, Root: root, Volume: pathVolume(point, fsType), ReadOnly: readOnly}
}

// Dir is an opened directory handle confined to its source.
type Dir interface {
	// Self describes the opened directory itself (identity of the handle, not of
	// whatever now sits at its path).
	Self() EntryInfo
	// ReadBatch returns at most n entries in directory order. It returns io.EOF
	// (with no entries) after the last entry. Implementations must not load the
	// whole directory before returning.
	ReadBatch(n int) ([]DirEntry, error)
	// Lstat describes the named immediate child without following symlinks.
	// MountBoundary is set when the child is a directory or a regular file on
	// a different device or a mount point in the snapshotted mount table (a
	// bind-mounted file).
	Lstat(name []byte) (EntryInfo, error)
	// OpenDir opens the named immediate child directory. It fails with outcome
	// changed_during_observation when the opened object's device, inode, or type
	// differs from expect, and refuses mount boundaries and non-directories.
	OpenDir(name []byte, expect EntryInfo) (Dir, error)
	// OpenFile opens the named immediate child regular file for reading
	// (design D5). It never follows a symlink, never blocks on or opens a
	// special file, and never opens for writing. Before touching the
	// filesystem it refuses an invalid name (ErrInvalidName), an expected kind
	// other than a regular file (ErrNotRegular), and an expected mount boundary
	// (ErrMountBoundary), all with an empty Outcome. The opened object must be a
	// regular file with expect's device, inode, size, modification time, and
	// change time, on this directory's device and not a mount point; otherwise
	// OpenFile fails with outcome changed_during_observation.
	OpenFile(name []byte, expect EntryInfo) (File, error)
	// Readlink returns the link text of the named immediate symlink child. The
	// target is never resolved.
	Readlink(name []byte) ([]byte, error)
	// FSInfo describes the filesystem holding this directory.
	FSInfo() (FSInfo, error)
	// Close releases the handle.
	Close() error
}

// DirEntry is one raw directory entry as listed (no lstat yet). Kind may be
// EntryUnknown when the filesystem does not report a type; callers then Lstat.
type DirEntry struct {
	Name []byte
	Kind domain.EntryKind
}

// File is a regular file opened for reading content. Only copy searches use it
// (design D5). A File is not safe for concurrent use; it may be used from the
// goroutine that opened it.
type File interface {
	// Stat is fstat of the open handle. Name is the name the file was opened by.
	Stat() (EntryInfo, error)
	// ReadAt has io.ReaderAt semantics: it returns len(p) bytes, or fewer with
	// a non-nil error, which is io.EOF at or past the end of the file. Other
	// failures are *Error values with an outcome.
	ReadAt(p []byte, off int64) (int, error)
	// Close releases the handle.
	Close() error
}

// EntryInfo is lstat-level metadata of one entry.
type EntryInfo struct {
	Name    []byte
	Kind    domain.EntryKind
	Size    int64
	ModTime time.Time
	Mode    fs.FileMode
	Dev     uint64
	Ino     uint64
	Nlink   uint64
	// Blocks is st_blocks: allocated storage in 512-byte units, as reported by
	// the filesystem. Allocated bytes are Blocks*512 (design D11).
	Blocks        int64
	MountBoundary bool
	// Ctime is st_ctim, the inode change time (design D6). It is the zero time
	// when the platform or a test filesystem has none.
	Ctime time.Time
}

// FSInfo carries statfs and mount-table facts used for source identity (design
// D14) and read-only reporting.
type FSInfo struct {
	Type     int64 // statfs f_type magic
	FSID     uint64
	Dev      uint64 // st_dev of the directory
	ReadOnly bool   // statfs ST_RDONLY
	// Mount is nil when the mount table had no entry for this filesystem.
	Mount *MountInfo
}

// MountInfo is one parsed /proc/self/mountinfo row (evidence only; mount IDs
// change on every mount).
type MountInfo struct {
	MountID    int
	MajorMinor string
	Root       string
	MountPoint string
	FSType     string
	Source     string
	// ReadOnly reports "ro" in the per-mount or the superblock options.
	ReadOnly bool
}

// ErrInvalidName reports a name that is not a single safe path component.
var ErrInvalidName = errors.New("fsaccess: name is not a single path component")

// Error is returned by every failed Dir/FS operation.
type Error struct {
	Op      string
	Name    []byte
	Outcome domain.AccessOutcome
	Err     error
}

func (e *Error) Error() string {
	return "fsaccess " + e.Op + " " + domain.DisplayName(e.Name) + ": " + string(e.Outcome) + ": " + errString(e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// OutcomeOf returns the AccessOutcome of the first *Error in err's chain, and
// false when err carries none.
func OutcomeOf(err error) (domain.AccessOutcome, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Outcome, true
	}
	return "", false
}
