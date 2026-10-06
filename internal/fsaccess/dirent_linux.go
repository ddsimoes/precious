package fsaccess

import (
	"bytes"
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
)

// Directory listings are read with getdents64 on the directory's own file
// descriptor instead of (*os.File).ReadDir: for files opened through an
// os.Root, ReadDir lstats every entry and overwrites d_type with the result,
// doubling the syscalls and hiding DT_UNKNOWN, and it silently drops entries
// that vanish between the two calls. Parsing the records directly keeps the
// listing a pure, bounded read of the directory stream.

// direntBufSize bounds the memory a listing holds regardless of directory
// size. It is far above the largest record (19 + 256 bytes, padded).
const direntBufSize = 32 << 10

// linux_dirent64 layout: d_ino u64, d_off s64, d_reclen u16, d_type u8, d_name.
const (
	direntReclenOff = 16
	direntTypeOff   = 18
	direntNameOff   = 19
)

var errMalformedDirent = errors.New("fsaccess: malformed directory record")

// nextDirent decodes the record at the start of buf. It returns the record
// length and, for any entry other than "." and "..", a copy of its name and
// its kind (EntryUnknown when the filesystem reports DT_UNKNOWN).
func nextDirent(buf []byte) (entry DirEntry, skip bool, reclen int, err error) {
	if len(buf) < direntNameOff {
		return DirEntry{}, false, 0, errMalformedDirent
	}
	reclen = int(binary.NativeEndian.Uint16(buf[direntReclenOff:]))
	if reclen < direntNameOff || reclen > len(buf) {
		return DirEntry{}, false, 0, errMalformedDirent
	}
	name := buf[direntNameOff:reclen]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	if len(name) == 0 {
		return DirEntry{}, false, 0, errMalformedDirent
	}
	if bytes.Equal(name, []byte(".")) || bytes.Equal(name, []byte("..")) {
		return DirEntry{}, true, reclen, nil
	}
	return DirEntry{Name: bytes.Clone(name), Kind: kindOfDtype(buf[direntTypeOff])}, false, reclen, nil
}

func kindOfDtype(t uint8) domain.EntryKind {
	switch t {
	case unix.DT_DIR:
		return domain.EntryDirectory
	case unix.DT_REG:
		return domain.EntryFile
	case unix.DT_LNK:
		return domain.EntrySymlink
	case unix.DT_FIFO:
		return domain.EntryFIFO
	case unix.DT_SOCK:
		return domain.EntrySocket
	case unix.DT_CHR:
		return domain.EntryCharDevice
	case unix.DT_BLK:
		return domain.EntryBlockDevice
	default:
		return domain.EntryUnknown
	}
}
