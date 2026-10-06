package archive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"precious/internal/domain"
)

// streamBufSize is the size of Stream's reads of r: [hashing]
// read_chunk_bytes' default, so that a chunked reader of the archive file
// needs one call per read.
const streamBufSize = 1 << 20

// typeGNUVolume is a GNU tar volume label: metadata, not a member.
const typeGNUVolume = 'V'

// errDataDone is a read of a member's data after visit returned.
var errDataDone = errors.New("archive: member data read after visit returned")

// Stream reads the streamed-format archive r, which yields the packed bytes
// in order, once from start to end (m4b D3–D6). It first checks the
// format's signature: 1f 8b for gzip, BZh for bzip2, and for tar a first
// 512-byte block that is a header with a valid checksum or all zero. It
// calls visit once per member in archive order. A tar holds files (regular
// and sparse, unpacked to their logical size), directories, symlinks, hard
// links to an earlier file member or hard link, and special members; pax
// global headers and volume labels are not members. A hard link is visited
// as a file member of its target's size, with LinkTo set and no data: its
// content is its target's. A gzip or bzip2 archive holds one file member
// named MemberName(name), of Size -1. Folders only implied by deeper members
// are not visited, nor is a directory naming the top level.
//
// data holds a file member's unpacked bytes and is nil for a hard link and
// every other kind; it is valid until visit returns, and Stream drains what
// visit leaves unread. When a budget is reached, or the data is damaged,
// while visit reads data, data.Read returns the *Stop, which visit is
// expected to return.
//
// Stream returns nil when the archive is complete and r read to its end, a
// *Stop for an archive outcome, ctx.Err() when ctx is done (checked at every
// read of r), visit's error as is, and a failed read of r wrapped.
func Stream(ctx context.Context, f domain.ArchiveFormat, name []byte, r io.Reader, lim Limits,
	visit func(m Member, data io.Reader) error) error {
	if err := lim.validate(); err != nil {
		return err
	}
	if !f.Streamed() {
		return fmt.Errorf("archive: format %q is not streamed", f)
	}
	s := &streamer{format: f, meter: newMeter(lim), paths: newPathSet(), visit: visit,
		entries: entryCount{max: lim.MaxEntries}}
	s.src = &source{ctx: ctx, r: r, meter: s.meter}
	tarred := f == domain.ArchiveTar || f == domain.ArchiveTarGzip || f == domain.ArchiveTarBzip2
	var member []byte
	if !tarred {
		if member = MemberName(name); member == nil {
			return fmt.Errorf("archive: %q is not named as a %s archive", name, f)
		}
	}
	br := bufio.NewReaderSize(s.src, streamBufSize)
	var dec io.Reader = br
	var mtime time.Time
	switch f {
	case domain.ArchiveTarGzip, domain.ArchiveGzip:
		if err := s.signature(br, "\x1f\x8b"); err != nil {
			return err
		}
		zr, err := gzip.NewReader(br)
		if err != nil {
			return s.fail(err, nil)
		}
		dec, mtime = zr, zr.ModTime
	case domain.ArchiveTarBzip2, domain.ArchiveBzip2:
		if err := s.signature(br, "BZh"); err != nil {
			return err
		}
		dec = bzip2.NewReader(br)
	}
	s.out = &counter{r: dec, s: s}
	var err error
	if tarred {
		err = s.tar()
	} else {
		err = s.single(member, mtime)
	}
	if err != nil {
		return err
	}
	// The rest: a tar's padding, and every compressed stream to its end, so
	// that its checksum is verified and r is read to its end.
	if _, err := io.Copy(io.Discard, s.out); err != nil {
		return s.fail(err, nil)
	}
	return nil
}

type streamer struct {
	format domain.ArchiveFormat
	meter  *meter
	paths  *pathSet
	visit  func(Member, io.Reader) error
	src    *source
	// out yields the unpacked bytes, counted against the budgets.
	out     *counter
	entries entryCount
	// cur is the cleaned path of the file member whose data is being read,
	// nil between members.
	cur []byte
	// stop is the budget stop reached, if any; it sticks.
	stop *Stop
}

// signature checks the first bytes of the packed stream.
func (s *streamer) signature(br *bufio.Reader, magic string) error {
	b, err := br.Peek(len(magic))
	switch {
	case s.src.err != nil:
		return s.src.failure(s.format)
	case err != nil, string(b) != magic:
		return notFormat(s.format)
	}
	return nil
}

// tar checks the first block's signature, then reads every member.
func (s *streamer) tar() error {
	var first [512]byte
	for n := 0; n < len(first); {
		m, err := s.out.Read(first[n:])
		n += m
		if err == io.EOF && n < len(first) {
			return notFormat(s.format)
		}
		if err != nil && err != io.EOF {
			return s.fail(err, nil)
		}
	}
	if !tarSignature(&first) {
		return notFormat(s.format)
	}
	tr := tar.NewReader(io.MultiReader(bytes.NewReader(first[:]), s.out))
	for {
		hdr, err := tr.Next()
		switch {
		case err == io.EOF:
			return nil
		case err != nil && !errors.Is(err, tar.ErrInsecurePath):
			// The path rules apply instead of tarinsecurepath's.
			return s.fail(err, nil)
		case hdr.Typeflag == tar.TypeXGlobalHeader, hdr.Typeflag == typeGNUVolume:
			continue
		}
		if err := s.tarMember(tr, hdr); err != nil {
			return err
		}
	}
}

// tarMember applies the member rules to one tar header and visits it. The
// reader turns a legacy '\x00' type into a file, or a directory for a name
// ending in '/'.
func (s *streamer) tarMember(tr *tar.Reader, hdr *tar.Header) error {
	if err := s.entries.add(1); err != nil {
		return err
	}
	comps, abs, leaves := cleanPath([]byte(hdr.Name))
	if leaves {
		return stop(domain.ArchiveRejected, detailLeaves, joinPath(comps, abs))
	}
	m := Member{Path: comps, Mtime: hdr.ModTime, Index: -1}
	hardlink := false
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeGNUSparse:
		m.Kind, m.Size = domain.MemberFile, hdr.Size
	case tar.TypeDir:
		m.Kind = domain.MemberDirectory
	case tar.TypeSymlink:
		m.Kind, m.LinkText = domain.MemberSymlink, []byte(hdr.Linkname)
	case tar.TypeLink:
		m.Kind, hardlink = domain.MemberFile, true
	default:
		m.Kind = domain.MemberSpecial
	}
	key, implied, detail := s.paths.add(comps, m.Kind == domain.MemberDirectory)
	if detail != "" {
		return stop(domain.ArchiveRejected, detail, joinPath(comps, false))
	}
	if err := s.entries.add(implied); err != nil {
		return err
	}
	switch {
	case m.Kind == domain.MemberDirectory:
		if len(comps) == 0 {
			return nil // the top level
		}
	case hardlink:
		target, _, out := cleanPath([]byte(hdr.Linkname))
		size, ok := s.paths.target(target)
		if out || !ok {
			return stop(domain.ArchiveCorrupt, detailHardlink, joinPath(comps, false))
		}
		m.Size, m.LinkTo = size, target
		s.paths.markContent(key, size)
	case m.Kind == domain.MemberFile:
		s.paths.markContent(key, m.Size)
		return s.visitData(m, tr)
	}
	return s.visit(m, nil)
}

// single visits the one member of a gzip or bzip2 archive.
func (s *streamer) single(name []byte, mtime time.Time) error {
	if err := s.entries.add(1); err != nil {
		return err
	}
	comps, abs, leaves := cleanPath(name)
	if leaves {
		return stop(domain.ArchiveRejected, detailLeaves, joinPath(comps, abs))
	}
	if _, _, detail := s.paths.add(comps, false); detail != "" {
		return stop(domain.ArchiveRejected, detail, joinPath(comps, false))
	}
	return s.visitData(Member{Path: comps, Kind: domain.MemberFile, Size: -1, Mtime: mtime, Index: -1}, s.out)
}

// visitData visits a file member whose unpacked bytes r yields, then drains
// what visit left unread.
func (s *streamer) visitData(m Member, r io.Reader) error {
	s.cur = joinPath(m.Path, false)
	data := &memberData{s: s, r: r, member: s.cur}
	if err := s.visit(m, data); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, data); err != nil {
		return err
	}
	data.done, s.cur = true, nil
	return nil
}

// fail maps an error of the decompressor or the tar reader. A failed read of
// r, or a cancel, wins over what a decoder made of it.
func (s *streamer) fail(err error, member []byte) error {
	var st *Stop
	switch {
	case s.src.err != nil:
		return s.src.failure(s.format)
	case s.stop != nil:
		return s.stop
	case errors.As(err, &st):
		return st
	case errors.Is(err, io.ErrNoProgress):
		return fmt.Errorf("archive: read %s archive: %w", s.format, err)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return stop(domain.ArchiveCorrupt, detailEOF, member)
	case errors.Is(err, gzip.ErrChecksum), isBzip2Checksum(err):
		return stop(domain.ArchiveCorrupt, detailChecksum, member)
	}
	return formatError(s.format, err, member)
}

// isBzip2Checksum reports a bzip2 block or file checksum mismatch, which
// compress/bzip2 reports only by its error text.
func isBzip2Checksum(err error) bool {
	var se bzip2.StructuralError
	return errors.As(err, &se) && strings.HasSuffix(string(se), "checksum mismatch")
}

// memberData is the data visit reads: a file member's unpacked bytes, with
// errors mapped to outcomes naming the member. Its first error sticks.
type memberData struct {
	s      *streamer
	r      io.Reader
	member []byte
	err    error
	done   bool
}

func (d *memberData) Read(p []byte) (int, error) {
	if d.done {
		return 0, errDataDone
	}
	if d.err != nil {
		return 0, d.err
	}
	n, err := d.r.Read(p)
	if err != nil && err != io.EOF {
		d.err = d.s.fail(err, d.member)
		return 0, d.err
	}
	return n, err
}

// counter counts the decompressor's output against the budgets, checked at
// every read. A reached budget sticks: every later read returns its *Stop,
// which names the file member whose data was being read.
type counter struct {
	r io.Reader
	s *streamer
}

func (c *counter) Read(p []byte) (int, error) {
	s := c.s
	if s.stop != nil {
		return 0, s.stop
	}
	n, err := c.r.Read(p)
	s.meter.unpacked.Add(int64(n))
	if err != nil && err != io.EOF {
		return n, err
	}
	if detail := s.meter.reached(); detail != "" {
		s.stop = stop(domain.ArchivePartial, detail, s.cur)
		return 0, s.stop
	}
	return n, err
}

// source reads the archive file, counting the packed bytes, with ctx checked
// before every read. Its first failure sticks.
type source struct {
	ctx    context.Context
	r      io.Reader
	meter  *meter
	err    error
	cancel bool // err is ctx.Err()
}

func (s *source) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if err := s.ctx.Err(); err != nil {
		s.err, s.cancel = err, true
		return 0, err
	}
	n, err := s.r.Read(p)
	s.meter.packed.Add(int64(n))
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}

// failure returns the source's failure: ctx.Err() as is, a read error
// wrapped.
func (s *source) failure(f domain.ArchiveFormat) error {
	if s.cancel {
		return s.err
	}
	return fmt.Errorf("archive: read %s archive: %w", f, s.err)
}

// tarSignature reports whether b is a tar header with a valid checksum, as
// archive/tar computes it (unsigned or signed, the checksum field counted as
// spaces), v7 headers without "ustar" included, or an all-zero block: an
// empty archive.
func tarSignature(b *[512]byte) bool {
	if *b == [512]byte{} {
		return true
	}
	field := bytes.Trim(b[148:156], " \x00")
	if i := bytes.IndexByte(field, 0); i >= 0 {
		field = field[:i]
	}
	want := int64(0)
	if len(field) > 0 {
		v, err := strconv.ParseUint(string(field), 8, 63)
		if err != nil {
			return false
		}
		want = int64(v)
	}
	var unsigned, signed int64
	for i, c := range b {
		if 148 <= i && i < 156 {
			c = ' '
		}
		unsigned += int64(c)
		signed += int64(int8(c))
	}
	return want == unsigned || want == signed
}
