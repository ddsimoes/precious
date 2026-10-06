package fsaccess

import (
	"io/fs"
	"strings"

	"precious/internal/domain"
)

// dirOnly appends "/." to a path so the kernel resolves it only if the last
// component is a directory. A FIFO, device, socket, or regular file then fails
// with ENOTDIR instead of being opened: os.Root's OpenRoot opens its final
// component without O_DIRECTORY, which blocks forever on a FIFO, whereas
// intermediate components (and "/." makes the name one) are opened as
// directories without following symlinks.
func dirOnly(name string) string {
	return strings.TrimSuffix(name, "/") + "/."
}

// entryInfo describes an lstat result. Identity, link count, allocation, and
// change time come from the platform's FileInfo.Sys() where it exposes them
// (sysInfo); elsewhere they stay zero.
func entryInfo(name []byte, fi fs.FileInfo) EntryInfo {
	info := EntryInfo{
		Name:    name,
		Kind:    kindOfMode(fi.Mode()),
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
		Mode:    fi.Mode(),
	}
	sysInfo(&info, fi.Sys())
	return info
}

func kindOfMode(m fs.FileMode) domain.EntryKind {
	switch {
	case m.IsRegular():
		return domain.EntryFile
	case m&fs.ModeDir != 0:
		return domain.EntryDirectory
	case m&fs.ModeSymlink != 0:
		return domain.EntrySymlink
	case m&fs.ModeNamedPipe != 0:
		return domain.EntryFIFO
	case m&fs.ModeSocket != 0:
		return domain.EntrySocket
	case m&fs.ModeCharDevice != 0:
		return domain.EntryCharDevice
	case m&fs.ModeDevice != 0:
		return domain.EntryBlockDevice
	default:
		return domain.EntryUnknown
	}
}

// sameObject reports whether a and b are one object: same kind, device, and
// inode. Where the platform exposes no identity both are zero, and only the
// kind is compared.
func sameObject(a, b EntryInfo) bool {
	return a.Kind == b.Kind && a.Dev == b.Dev && a.Ino == b.Ino
}

// sameFile reports whether got is the regular file expect describes, with
// unchanged content metadata (design D5, D6).
func sameFile(got, expect EntryInfo) bool {
	return got.Kind == domain.EntryFile && sameObject(got, expect) && got.Size == expect.Size &&
		got.ModTime.Equal(expect.ModTime) && got.Ctime.Equal(expect.Ctime)
}
