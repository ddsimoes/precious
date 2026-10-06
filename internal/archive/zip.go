package archive

import (
	"archive/zip"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"

	"precious/internal/domain"
)

// Zip is an opened zip archive (m4b D3): its members as listed by the
// central directory, each unpacked on demand. Its budgets count the listing
// and every member read together. Open and Section may be called
// concurrently.
type Zip struct {
	zr      *zip.Reader
	meter   *meter
	members []Member
	// r and size are the archive file, read raw by Section.
	r    io.ReaderAt
	size int64
}

const (
	// zipEndLen is the length of the end-of-central-directory record
	// without its comment.
	zipEndLen = 22
	// zipEndSearch is the most bytes searched for that record: the record
	// and the longest comment.
	zipEndSearch = zipEndLen + math.MaxUint16
	zip64LocLen  = 20
	zip64EndLen  = 56
	// maxLinkText is the longest symlink text a zip member may hold.
	maxLinkText = 4096
)

// msdosZero is what archive/zip makes of an MS-DOS date and time of zero.
var msdosZero = time.Date(1980, 0, 0, 0, 0, 0, 0, time.UTC)

// errCutOff marks a member whose packed data ends before its compressed
// stream does, which archive/zip would report as io.ErrUnexpectedEOF, like a
// size mismatch.
var errCutOff = errors.New("archive: member data cut off")

// OpenZip lists the zip archive r of size bytes (m4b D3–D6). It reads
// the end-of-central-directory record itself, in at most the last 65,557
// bytes, and checks the declared entry count against lim.MaxEntries and the
// disk number before archive/zip parses the central directory. It then
// applies, in central-directory order, the entry budget (each member and
// the folders it implies first), the encryption flag, the compression method
// (store or deflate), and the path rules to every member, and finally reads
// each symlink member's text under the budgets. It returns a *Stop for those
// outcomes, and a wrapped error for a failed read of r.
func OpenZip(r io.ReaderAt, size int64, lim Limits) (*Zip, error) {
	if err := lim.validate(); err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, fmt.Errorf("archive: zip size %d is negative", size)
	}
	m := newMeter(lim)
	src := &sourceAt{r: r, size: size, meter: m}
	end, err := readZipEnd(src, size)
	if err != nil {
		return nil, zipError(err, nil)
	}
	if end.records > uint64(lim.MaxEntries) {
		return nil, stop(domain.ArchivePartial, detailEntries, nil)
	}
	if end.disk != 0 || end.dirDisk != 0 {
		return nil, stop(domain.ArchiveUnsupported, detailMultiDisk, nil)
	}
	zr, err := zip.NewReader(src, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, zipError(err, nil)
	}
	zr.RegisterDecompressor(zip.Store, storeDecompressor)
	zr.RegisterDecompressor(zip.Deflate, deflateDecompressor)
	z := &Zip{zr: zr, meter: m, r: r, size: size}
	if err := z.list(); err != nil {
		return nil, err
	}
	if err := z.readLinks(); err != nil {
		return nil, err
	}
	return z, nil
}

// Members returns the members in central-directory order. Directory members
// are listed, folders only implied by deeper members are not, and neither is
// a directory member naming the top level. The caller must not modify the
// result. A nil Zip has no members.
func (z *Zip) Members() []Member {
	if z == nil {
		return nil
	}
	return z.members
}

// Open returns the unpacked bytes of the file member at central-directory
// index. archive/zip checks the size and CRC-32 at the end of the data.
// Reading reports a *Stop: corrupt on a CRC or size mismatch, an unexpected
// end, or a format error; partial at a budget, counted with the listing and
// every other read of z. A failed read of the archive is returned wrapped.
func (z *Zip) Open(index int) (io.ReadCloser, error) {
	m, err := z.member(index)
	if err != nil {
		return nil, err
	}
	if m.Kind != domain.MemberFile {
		return nil, fmt.Errorf("archive: zip member %d is a %s, not a file", index, m.Kind)
	}
	return z.open(index, m.Path)
}

// Section returns the bytes of the stored file member at central-directory
// index, read raw from the archive file at any offset, for a viewer's
// ranges (R2 design D17). It reports false for a member that is not a
// stored file, and for one whose local header is unreadable or whose data
// does not fit in the archive file: Open reads such a member, and reports
// its outcome. Reads of the section are not counted against the budgets,
// nor checked against the CRC-32.
func (z *Zip) Section(index int) (*io.SectionReader, bool) {
	m, err := z.member(index)
	if err != nil || m.Kind != domain.MemberFile || !m.Stored {
		return nil, false
	}
	if f := z.zr.File[index]; f.CompressedSize64 == f.UncompressedSize64 {
		off, err := f.DataOffset()
		if err == nil && 0 <= off && off <= z.size && m.Size <= z.size-off {
			return io.NewSectionReader(z.r, off, m.Size), true
		}
	}
	return nil, false
}

// member returns the listed member at central-directory index.
func (z *Zip) member(index int) (*Member, error) {
	i := sort.Search(len(z.members), func(i int) bool { return z.members[i].Index >= index })
	if i == len(z.members) || z.members[i].Index != index {
		return nil, fmt.Errorf("archive: zip has no member %d", index)
	}
	return &z.members[i], nil
}

// list applies the member rules to every central-directory entry.
func (z *Zip) list() error {
	paths := newPathSet()
	entries := entryCount{max: z.meter.lim.MaxEntries}
	z.members = make([]Member, 0, len(z.zr.File))
	for i, f := range z.zr.File {
		if err := entries.add(1); err != nil {
			return err
		}
		comps, abs, leaves := cleanPath([]byte(f.Name))
		switch {
		case f.Flags&1 != 0:
			return stop(domain.ArchiveEncrypted, detailEncrypted, joinPath(comps, abs))
		case f.Method != zip.Store && f.Method != zip.Deflate:
			return stop(domain.ArchiveUnsupported,
				fmt.Sprintf("compression method %d is not supported", f.Method), joinPath(comps, abs))
		case leaves:
			return stop(domain.ArchiveRejected, detailLeaves, joinPath(comps, abs))
		}
		dir := strings.HasSuffix(f.Name, "/")
		_, implied, detail := paths.add(comps, dir)
		if detail != "" {
			return stop(domain.ArchiveRejected, detail, joinPath(comps, false))
		}
		if err := entries.add(implied); err != nil {
			return err
		}
		if dir && len(comps) == 0 {
			continue // the top level
		}
		m := Member{Path: comps, Kind: domain.MemberFile, Mtime: zipMtime(f), Index: i}
		switch {
		case dir:
			m.Kind = domain.MemberDirectory
		case f.Mode()&fs.ModeSymlink != 0:
			m.Kind = domain.MemberSymlink
		case f.UncompressedSize64 > math.MaxInt64:
			return formatError(domain.ArchiveZip,
				fmt.Errorf("member size %d is out of range", f.UncompressedSize64), joinPath(comps, false))
		default:
			m.Size, m.Stored = int64(f.UncompressedSize64), f.Method == zip.Store
		}
		z.members = append(z.members, m)
	}
	return nil
}

// readLinks reads every symlink member's text, at most maxLinkText bytes.
func (z *Zip) readLinks() error {
	for i := range z.members {
		m := &z.members[i]
		if m.Kind != domain.MemberSymlink {
			continue
		}
		if n := z.zr.File[m.Index].UncompressedSize64; n > maxLinkText {
			return formatError(domain.ArchiveZip,
				fmt.Errorf("link text of %d bytes is longer than %d", n, maxLinkText), joinPath(m.Path, false))
		}
		rc, err := z.open(m.Index, m.Path)
		if err != nil {
			return err
		}
		text, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		m.LinkText = text
	}
	return nil
}

// open opens the member at index, whose cleaned path is path.
func (z *Zip) open(index int, path [][]byte) (io.ReadCloser, error) {
	f := z.zr.File[index]
	rc, err := f.Open()
	if err != nil {
		return nil, zipError(err, joinPath(path, false))
	}
	return &zipData{z: z, rc: rc, size: int64(f.UncompressedSize64), path: path}, nil
}

// zipData reads one member's unpacked bytes, counting them against the
// Zip's budgets, and turns errors into outcomes. Its first error sticks.
type zipData struct {
	z    *Zip
	rc   io.ReadCloser
	size int64 // declared unpacked size
	got  int64 // unpacked bytes returned by archive/zip
	path [][]byte
	err  error
}

func (r *zipData) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.rc.Read(p)
	r.got += int64(n)
	r.z.meter.unpacked.Add(int64(n))
	if err != nil && err != io.EOF {
		r.err = r.fail(err)
		return 0, r.err
	}
	if detail := r.z.meter.reached(); detail != "" {
		r.err = stop(domain.ArchivePartial, detail, joinPath(r.path, false))
		return 0, r.err
	}
	return n, err
}

func (r *zipData) Close() error { return r.rc.Close() }

// fail maps an error of archive/zip's member reader. archive/zip returns
// ErrFormat for more bytes than declared, and io.ErrUnexpectedEOF for fewer
// (having then returned fewer than declared) or for a cut-off data
// descriptor (having returned all of them).
func (r *zipData) fail(err error) error {
	detail := ""
	switch {
	case errors.Is(err, zip.ErrChecksum):
		detail = detailChecksum
	case errors.Is(err, zip.ErrFormat):
		detail = detailSize
	case errors.Is(err, errCutOff):
		detail = detailEOF
	case errors.Is(err, io.ErrUnexpectedEOF):
		detail = detailEOF
		if r.got < r.size {
			detail = detailSize
		}
	default:
		return zipError(err, joinPath(r.path, false))
	}
	return stop(domain.ArchiveCorrupt, detail, joinPath(r.path, false))
}

// zipError returns a failed read of the archive wrapped, and any other error
// as corrupt.
func zipError(err error, member []byte) error {
	var re *readError
	switch {
	case errors.As(err, &re):
		return fmt.Errorf("archive: read zip archive: %w", re.err)
	case errors.As(err, new(*Stop)):
		return err
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF), errors.Is(err, errCutOff):
		return stop(domain.ArchiveCorrupt, detailEOF, member)
	}
	return formatError(domain.ArchiveZip, err, member)
}

func zipMtime(f *zip.File) time.Time {
	if f.ModifiedDate == 0 && f.ModifiedTime == 0 && f.Modified.Equal(msdosZero) {
		return time.Time{}
	}
	return f.Modified
}

// zipEnd holds what the end-of-central-directory records declare.
type zipEnd struct {
	records       uint64 // total entries in the central directory
	disk, dirDisk uint32 // this disk; the disk where the directory starts
}

// readZipEnd finds the end-of-central-directory record as archive/zip does:
// in the last 1 KiB, else in the last 65,557 bytes, the record nearest the
// end. When the record's count, size, or offset is saturated, a zip64
// locator before it names the zip64 record, which then holds the values.
func readZipEnd(r io.ReaderAt, size int64) (zipEnd, error) {
	var buf []byte
	at := int64(-1)
	for _, n := range []int64{1024, zipEndSearch} {
		n = min(n, size)
		buf = make([]byte, n)
		if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
			return zipEnd{}, err
		}
		if p := findZipEnd(buf); p >= 0 {
			buf, at = buf[p:], size-n+int64(p)
			break
		}
		if n == size {
			break
		}
	}
	if at < 0 {
		return zipEnd{}, notFormat(domain.ArchiveZip)
	}
	le := binary.LittleEndian
	end := zipEnd{
		disk:    uint32(le.Uint16(buf[4:])),
		dirDisk: uint32(le.Uint16(buf[6:])),
		records: uint64(le.Uint16(buf[10:])),
	}
	if end.records != 0xffff && le.Uint32(buf[12:]) != 0xffffffff && le.Uint32(buf[16:]) != 0xffffffff {
		return end, nil
	}
	// The zip64 locator, as archive/zip reads it: on disk 0, of 1 disk.
	if at < zip64LocLen {
		return end, nil
	}
	var loc [zip64LocLen]byte
	if _, err := r.ReadAt(loc[:], at-zip64LocLen); err != nil {
		return zipEnd{}, err
	}
	if le.Uint32(loc[0:]) != 0x07064b50 || le.Uint32(loc[4:]) != 0 || le.Uint32(loc[16:]) != 1 {
		return end, nil
	}
	p := le.Uint64(loc[8:])
	if p > uint64(size) || uint64(size)-p < zip64EndLen {
		return zipEnd{}, zip.ErrFormat
	}
	var rec [zip64EndLen]byte
	if _, err := r.ReadAt(rec[:], int64(p)); err != nil {
		return zipEnd{}, err
	}
	if le.Uint32(rec[0:]) != 0x06064b50 {
		return zipEnd{}, zip.ErrFormat
	}
	end.disk = le.Uint32(rec[16:])
	end.dirDisk = le.Uint32(rec[20:])
	end.records = le.Uint64(rec[32:])
	return end, nil
}

// findZipEnd returns the offset of the last end-of-central-directory
// signature in b, or -1 when there is none or its comment overruns b, as
// archive/zip does.
func findZipEnd(b []byte) int {
	for i := len(b) - zipEndLen; i >= 0; i-- {
		if b[i] == 'P' && b[i+1] == 'K' && b[i+2] == 0x05 && b[i+3] == 0x06 {
			n := int(b[i+zipEndLen-2]) | int(b[i+zipEndLen-1])<<8
			if n+zipEndLen+i > len(b) {
				return -1
			}
			return i
		}
	}
	return -1
}

// sourceAt reads the archive file as a file of size bytes, counting the
// packed bytes read. A read beyond size is io.EOF and a negative offset an
// error, as for a section, so a hostile offset is a format error and not a
// failed read. A failed read of r is a *readError.
type sourceAt struct {
	r     io.ReaderAt
	size  int64
	meter *meter
}

func (s *sourceAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("zip: negative offset")
	}
	if off >= s.size {
		return 0, io.EOF
	}
	short := false
	if rest := s.size - off; int64(len(p)) > rest {
		p, short = p[:rest], true
	}
	n, err := s.r.ReadAt(p, off)
	s.meter.packed.Add(int64(n))
	switch {
	case err != nil && err != io.EOF:
		return n, &readError{err}
	case short && n == len(p):
		return n, io.EOF
	}
	return n, err
}

// readError is a failed read of the archive file.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// storeDecompressor reads a stored member's data. Its data ends early only
// when the archive file is shorter than its directory says: errCutOff.
func storeDecompressor(r io.Reader) io.ReadCloser {
	want := int64(-1)
	if s, ok := r.(interface{ Size() int64 }); ok {
		want = s.Size()
	}
	return &packedData{r: r, want: want}
}

// deflateDecompressor inflates a member's data. A deflate stream that needs
// more data than the member holds is cut off: errCutOff.
func deflateDecompressor(r io.Reader) io.ReadCloser {
	return inflater{flate.NewReader(storeDecompressor(r))}
}

type packedData struct {
	r         io.Reader
	want, got int64
}

func (d *packedData) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	d.got += int64(n)
	if err == io.EOF && d.got < d.want {
		err = errCutOff
	}
	return n, err
}

func (d *packedData) Close() error { return nil }

type inflater struct{ io.ReadCloser }

func (f inflater) Read(p []byte) (int, error) {
	n, err := f.ReadCloser.Read(p)
	if err == io.ErrUnexpectedEOF {
		err = errCutOff
	}
	return n, err
}
