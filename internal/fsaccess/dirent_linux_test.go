package fsaccess

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"golang.org/x/sys/unix"

	"precious/internal/domain"
)

// direntRecord encodes one linux_dirent64 record, padded to 8 bytes.
func direntRecord(ino uint64, typ uint8, name string) []byte {
	reclen := (direntNameOff + len(name) + 1 + 7) &^ 7
	rec := make([]byte, reclen)
	binary.NativeEndian.PutUint64(rec[0:], ino)
	binary.NativeEndian.PutUint16(rec[direntReclenOff:], uint16(reclen))
	rec[direntTypeOff] = typ
	copy(rec[direntNameOff:], name)
	return rec
}

func TestNextDirentKindsAndNames(t *testing.T) {
	var buf []byte
	for _, r := range []struct {
		typ  uint8
		name string
	}{
		{unix.DT_DIR, "."}, {unix.DT_DIR, ".."},
		{unix.DT_DIR, "d"}, {unix.DT_REG, "f\xe9.txt"}, {unix.DT_LNK, "l"},
		{unix.DT_FIFO, "p"}, {unix.DT_SOCK, "s"}, {unix.DT_CHR, "c"}, {unix.DT_BLK, "b"},
		{unix.DT_UNKNOWN, "u"}, {unix.DT_WHT, "w"},
	} {
		buf = append(buf, direntRecord(7, r.typ, r.name)...)
	}
	want := []DirEntry{
		{[]byte("d"), domain.EntryDirectory}, {[]byte("f\xe9.txt"), domain.EntryFile},
		{[]byte("l"), domain.EntrySymlink}, {[]byte("p"), domain.EntryFIFO},
		{[]byte("s"), domain.EntrySocket}, {[]byte("c"), domain.EntryCharDevice},
		{[]byte("b"), domain.EntryBlockDevice}, {[]byte("u"), domain.EntryUnknown},
		{[]byte("w"), domain.EntryUnknown},
	}
	var got []DirEntry
	for off := 0; off < len(buf); {
		e, skip, reclen, err := nextDirent(buf[off:])
		if err != nil {
			t.Fatalf("offset %d: %v", off, err)
		}
		off += reclen
		if !skip {
			got = append(got, e)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i].Name, want[i].Name) || got[i].Kind != want[i].Kind {
			t.Errorf("entry %d = {%q %s}, want {%q %s}", i, got[i].Name, got[i].Kind, want[i].Name, want[i].Kind)
		}
	}
	// Names are copies: reusing the buffer must not change them.
	clear(buf)
	if string(got[0].Name) != "d" {
		t.Errorf("name aliases the getdents buffer: %q", got[0].Name)
	}
}

func TestNextDirentMalformed(t *testing.T) {
	good := direntRecord(1, unix.DT_REG, "a")
	zero := bytes.Clone(good)
	binary.NativeEndian.PutUint16(zero[direntReclenOff:], 0)
	long := bytes.Clone(good)
	binary.NativeEndian.PutUint16(long[direntReclenOff:], uint16(len(good)+8))
	empty := direntRecord(1, unix.DT_REG, "")
	for name, buf := range map[string][]byte{
		"truncated header": good[:direntNameOff-1],
		"zero reclen":      zero,
		"reclen past end":  long,
		"empty name":       empty,
	} {
		if _, _, _, err := nextDirent(buf); !errors.Is(err, errMalformedDirent) {
			t.Errorf("%s: err = %v, want errMalformedDirent", name, err)
		}
	}
}
