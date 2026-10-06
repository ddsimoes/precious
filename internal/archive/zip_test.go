package archive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/domain"
)

// zipZeros adds the member name holding mib MiB of zeros, deflated at about
// 1,000:1 without compressing them: one sync-flushed deflate block of 1 MiB
// of zeros repeated mib times, then a final empty block. The window holds
// zeros at every block start, so each copy decodes the same.
func zipZeros(t *testing.T, w *zip.Writer, name string, mib int) {
	t.Helper()
	zeros := make([]byte, 1<<20)
	var block, final bytes.Buffer
	fw, err := flate.NewWriter(&block, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(zeros); err != nil {
		t.Fatal(err)
	}
	if err := fw.Flush(); err != nil {
		t.Fatal(err)
	}
	if fw, err = flate.NewWriter(&final, flate.BestCompression); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := append(bytes.Repeat(block.Bytes(), mib), final.Bytes()...)
	var crc uint32
	for range mib {
		crc = crc32.Update(crc, crc32.IEEETable, zeros)
	}
	zipRaw(t, w, &zip.FileHeader{Name: name, Method: zip.Deflate, CRC32: crc,
		UncompressedSize64: uint64(mib) << 20}, raw)
}

// Spec scenario "A zip bomb stops early": a 1 MiB zip holding 1 GiB of
// zeros, with the ratio budget at 100, stops before 128 MiB is unpacked.
func TestZipBombStopsAtTheRatioBudget(t *testing.T) {
	t.Parallel()
	bomb := zipOf(t, func(w *zip.Writer) { zipZeros(t, w, "zeros.bin", 1024) })
	if len(bomb) > 1100<<10 {
		t.Fatalf("the bomb is %d bytes, want about 1 MiB", len(bomb))
	}
	lim := limits
	lim.MaxRatio = 100
	z, err := OpenZip(bytes.NewReader(bomb), int64(len(bomb)), lim)
	if err != nil {
		t.Fatal(err)
	}
	checkSeen(t, seenAll(z), []seen{{path: "zeros.bin", kind: domain.MemberFile, size: 1 << 30}})
	rc, err := z.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, rc)
	checkStop(t, err, domain.ArchivePartial, detailRatio, []byte("zeros.bin"))
	if n <= RatioGrace || n >= 128<<20 {
		t.Fatalf("unpacked %d bytes, want more than the grace and less than 128 MiB", n)
	}
}

// m4b D6: the ratio budget is per opening, not per member: two members
// of 40 MiB of zeros each pass it alone, and the second stops after the
// first was read.
func TestZipRatioBudgetIsCumulative(t *testing.T) {
	t.Parallel()
	archive := zipOf(t, func(w *zip.Writer) {
		zipZeros(t, w, "a.bin", 40)
		zipZeros(t, w, "b.bin", 40)
	})
	lim := limits
	lim.MaxRatio = 100
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), lim)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := readMember(z, 0); err != nil || len(b) != 40<<20 {
		t.Fatalf("a.bin: %d bytes, %v", len(b), err)
	}
	_, err = readMember(z, 1)
	checkStop(t, err, domain.ArchivePartial, detailRatio, []byte("b.bin"))
}

func seenAll(z *Zip) []seen {
	var s []seen
	for _, m := range z.Members() {
		s = append(s, seenOf(m, ""))
	}
	return s
}

// Entry budget: a declared count above the budget lists no member, and the
// central directory is never read: every read lies after its first header.
func TestEntryBudget(t *testing.T) {
	t.Parallel()
	archive := zipOf(t, func(w *zip.Writer) {
		for i := range 40 {
			zipFile(t, w, fmt.Sprintf("%s-%02d", strings.Repeat("n", 60), i), "x")
		}
	})
	size := int64(len(archive))
	dirOffset := int64(binary.LittleEndian.Uint32(archive[size-zipEndLen+16:]))
	lim := limits
	lim.MaxEntries = 39
	rec := &recordingReaderAt{r: bytes.NewReader(archive)}
	z, err := OpenZip(rec, size, lim)
	checkStop(t, err, domain.ArchivePartial, detailEntries, nil)
	if len(z.Members()) != 0 {
		t.Fatalf("%d members listed", len(z.Members()))
	}
	if len(rec.reads) == 0 {
		t.Fatal("no read recorded")
	}
	for _, r := range rec.reads {
		if r[0] <= dirOffset {
			t.Errorf("read %d bytes at %d; the central directory starts at %d", r[1], r[0], dirOffset)
		}
	}
	lim.MaxEntries = 40
	if z, err := OpenZip(bytes.NewReader(archive), size, lim); err != nil || len(z.Members()) != 40 {
		t.Fatalf("at the budget: %v", err)
	}
}

// m4b D6: a zip's entries are its members and the folders their paths
// imply first, counted as the directory is listed: x/y/z is three entries.
func TestZipEntryBudgetCountsImpliedFolders(t *testing.T) {
	t.Parallel()
	archive := zipOf(t, func(w *zip.Writer) { zipFile(t, w, "x/y/z", "data") })
	lim := limits
	lim.MaxEntries = 2
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), lim)
	checkStop(t, err, domain.ArchivePartial, detailEntries, nil)
	if len(z.Members()) != 0 {
		t.Fatalf("%d members listed", len(z.Members()))
	}
	lim.MaxEntries = 3
	if z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), lim); err != nil || len(z.Members()) != 1 {
		t.Fatalf("at the budget: %v", err)
	}
}

// zip64Tail replaces the end-of-central-directory record of a zip written
// without a comment by a zip64 record declaring records entries on disk,
// its locator, and a saturated record.
func zip64Tail(archive []byte, records uint64, disk uint32) []byte {
	le := binary.LittleEndian
	end := archive[len(archive)-zipEndLen:]
	body := append([]byte(nil), archive[:len(archive)-zipEndLen]...)
	rec := make([]byte, zip64EndLen)
	le.PutUint32(rec[0:], 0x06064b50)
	le.PutUint64(rec[4:], zip64EndLen-12)
	le.PutUint16(rec[12:], 45)
	le.PutUint16(rec[14:], 45)
	le.PutUint32(rec[16:], disk)
	le.PutUint32(rec[20:], disk)
	le.PutUint64(rec[24:], records)
	le.PutUint64(rec[32:], records)
	le.PutUint64(rec[40:], uint64(le.Uint32(end[12:])))
	le.PutUint64(rec[48:], uint64(le.Uint32(end[16:])))
	loc := make([]byte, zip64LocLen)
	le.PutUint32(loc[0:], 0x07064b50)
	le.PutUint64(loc[8:], uint64(len(body)))
	le.PutUint32(loc[16:], 1)
	eocd := make([]byte, zipEndLen)
	le.PutUint32(eocd[0:], 0x06054b50)
	le.PutUint16(eocd[8:], 0xffff)
	le.PutUint16(eocd[10:], 0xffff)
	le.PutUint32(eocd[12:], 0xffffffff)
	le.PutUint32(eocd[16:], 0xffffffff)
	return append(append(append(body, rec...), loc...), eocd...)
}

// m4b D6 and D5: the entry count and the disk number come from the zip64
// record when the record is saturated; the entry budget is checked first.
func TestZipEndRecords(t *testing.T) {
	t.Parallel()
	three := zipOf(t, func(w *zip.Writer) {
		zipFile(t, w, "a", "1")
		zipFile(t, w, "b", "2")
		zipFile(t, w, "c", "3")
	})
	lim := limits
	open := func(archive []byte, maxEntries int64) (*Zip, error) {
		lim.MaxEntries = maxEntries
		return OpenZip(bytes.NewReader(archive), int64(len(archive)), lim)
	}
	z64 := zip64Tail(three, 3, 0)
	_, err := open(z64, 2)
	checkStop(t, err, domain.ArchivePartial, detailEntries, nil)
	if z, err := open(z64, 3); err != nil || len(z.Members()) != 3 {
		t.Fatalf("zip64 at the budget: %v", err)
	}
	_, err = open(zip64Tail(three, 3, 1), 3)
	checkStop(t, err, domain.ArchiveUnsupported, detailMultiDisk, nil)

	split := append([]byte(nil), three...)
	split[len(split)-zipEndLen+4] = 1 // this disk's number
	_, err = open(split, 3)
	checkStop(t, err, domain.ArchiveUnsupported, detailMultiDisk, nil)
	_, err = open(split, 2)
	checkStop(t, err, domain.ArchivePartial, detailEntries, nil)
}

// Checksum mismatch: a member whose stored CRC-32 does not match its
// unpacked bytes reads as corrupt.
func TestChecksumMismatch(t *testing.T) {
	t.Parallel()
	data := []byte("the plan, version 1\n")
	archive := zipOf(t, func(w *zip.Writer) {
		zipRaw(t, w, &zip.FileHeader{Name: "docs/plan.txt", Method: zip.Deflate,
			CRC32: crc32.ChecksumIEEE(data) ^ 0x5a5a5a5a, UncompressedSize64: uint64(len(data))}, deflateOf(t, data))
	})
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readMember(z, 0)
	checkStop(t, err, domain.ArchiveCorrupt, detailChecksum, []byte("docs/plan.txt"))
}

// Name says zip, bytes do not: unsupported, naming no member.
func TestNameSaysZipBytesDoNot(t *testing.T) {
	t.Parallel()
	truncatedComment := append([]byte("PK\x05\x06"), make([]byte, 18)...)
	truncatedComment[20] = 10 // a 10-byte comment, missing
	for _, b := range [][]byte{nil, []byte("hello"), randomBytes(8, 100_000), gzipOf(t, []byte("x")), truncatedComment} {
		z, err := OpenZip(bytes.NewReader(b), int64(len(b)), limits)
		checkStop(t, err, domain.ArchiveUnsupported, "not a zip archive", nil)
		if z != nil {
			t.Fatal("a Zip returned with a Stop")
		}
	}
}

// zipMember is a member of the rule cases, without data.
type zipMember struct {
	name   string
	flags  uint16
	method uint16
}

// The spec's unsafe and damaged archives (the zip cases), and m4b D4, D5: each
// rule gives its state and detail and names the first offending member;
// encryption and the method are checked before the path.
func TestZipMemberRules(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		members []zipMember
		state   domain.ArchiveState
		detail  string
		member  string
	}{
		{"traversal", []zipMember{{name: "ok.txt"}, {name: "../../etc/passwd"}}, domain.ArchiveRejected, detailLeaves, "../../etc/passwd"},
		{"absolute", []zipMember{{name: "/etc/passwd"}}, domain.ArchiveRejected, detailLeaves, "/etc/passwd"},
		{"absolute, cleaned", []zipMember{{name: "//a/./b"}}, domain.ArchiveRejected, detailLeaves, "/a/b"},
		{"dot dot inside", []zipMember{{name: "a/./../b"}}, domain.ArchiveRejected, detailLeaves, "a/../b"},
		{"duplicate", []zipMember{{name: "a/b.txt"}, {name: "a//b.txt"}}, domain.ArchiveRejected, detailSharedPath, "a/b.txt"},
		{"file then folder", []zipMember{{name: "a"}, {name: "a/b"}}, domain.ArchiveRejected, detailFileFolder, "a/b"},
		{"folder then file", []zipMember{{name: "a/b"}, {name: "a"}}, domain.ArchiveRejected, detailFileFolder, "a"},
		{"directory then file", []zipMember{{name: "x/"}, {name: "x"}}, domain.ArchiveRejected, detailFileFolder, "x"},
		{"file then directory", []zipMember{{name: "x"}, {name: "x/"}}, domain.ArchiveRejected, detailFileFolder, "x"},
		{"file at the top level", []zipMember{{name: "./."}}, domain.ArchiveRejected, detailFileFolder, ""},
		{"encrypted", []zipMember{{name: "a.txt"}, {name: "b.txt", flags: 1}}, domain.ArchiveEncrypted, detailEncrypted, "b.txt"},
		{"LZMA", []zipMember{{name: "a.txt"}, {name: "b.bin", method: 14}}, domain.ArchiveUnsupported,
			"compression method 14 is not supported", "b.bin"},
		{"first offending member", []zipMember{{name: "../x"}, {name: "y", flags: 1}}, domain.ArchiveRejected, detailLeaves, "../x"},
		{"encryption before the path", []zipMember{{name: "../x", flags: 1}}, domain.ArchiveEncrypted, detailEncrypted, "../x"},
		{"method before the path", []zipMember{{name: "a//b", method: 14}, {name: "a/b"}}, domain.ArchiveUnsupported,
			"compression method 14 is not supported", "a/b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			archive := zipOf(t, func(w *zip.Writer) {
				for _, m := range c.members {
					zipRaw(t, w, &zip.FileHeader{Name: m.name, Flags: m.flags, Method: m.method}, nil)
				}
			})
			z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
			checkStop(t, err, c.state, c.detail, []byte(c.member))
			if len(z.Members()) != 0 {
				t.Fatal("members listed with a Stop")
			}
		})
	}
}

// m4b D4, D5: members are listed as found, in central-directory order:
// directories (a repeated one too) but not implied folders or the top level;
// a backslash is an ordinary byte; raw names come back byte for byte; a
// symlink's data is its link text.
func TestZipMembers(t *testing.T) {
	t.Parallel()
	archive := zipOf(t, func(w *zip.Writer) {
		zipFile(t, w, "./", "")
		zipFile(t, w, "d/", "")
		zipFile(t, w, "d/", "")
		zipFile(t, w, "d/e/f.txt", "deep")
		zipFile(t, w, `dir\file.txt`, "backslash")
		zipFile(t, w, "\xff\xfe/\x80.bin", "raw")
		zipRaw(t, w, &zip.FileHeader{Name: "notime.txt", Method: zip.Store, CRC32: crc32.ChecksumIEEE([]byte("stored")),
			UncompressedSize64: 6}, []byte("stored"))
		fh := &zip.FileHeader{Name: "link", Method: zip.Store, Modified: t0}
		fh.SetMode(fs.ModeSymlink | 0o777)
		fw, err := w.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(fw, "d/e/f.txt"); err != nil {
			t.Fatal(err)
		}
	})
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	if err != nil {
		t.Fatal(err)
	}
	checkSeen(t, seenAll(z), []seen{
		{path: "d", kind: domain.MemberDirectory, mtime: t0, index: 1},
		{path: "d", kind: domain.MemberDirectory, mtime: t0, index: 2},
		{path: "d/e/f.txt", kind: domain.MemberFile, size: 4, mtime: t0, index: 3},
		{path: `dir\file.txt`, kind: domain.MemberFile, size: 9, mtime: t0, index: 4},
		{path: "\xff\xfe/\x80.bin", kind: domain.MemberFile, size: 3, mtime: t0, index: 5},
		{path: "notime.txt", kind: domain.MemberFile, size: 6, stored: true, index: 6},
		{path: "link", kind: domain.MemberSymlink, link: "d/e/f.txt", mtime: t0, index: 7},
	})
	ms := z.Members()
	if len(ms[3].Path) != 1 || len(ms[4].Path) != 2 || string(ms[4].Path[0]) != "\xff\xfe" {
		t.Errorf("components %q and %q", ms[3].Path, ms[4].Path)
	}
	for index, want := range map[int]string{3: "deep", 4: "backslash", 5: "raw", 6: "stored"} {
		if b, err := readMember(z, index); err != nil || string(b) != want {
			t.Errorf("member %d = %q, %v; want %q", index, b, err, want)
		}
	}
	for _, index := range []int{0, 1, 7, 8, -1} {
		if _, err := z.Open(index); err == nil || errors.As(err, new(*Stop)) {
			t.Errorf("Open(%d) = %v, want an error that is not a Stop", index, err)
		}
	}
}

// m4b D5: a symlink's text is at most 4,096 bytes.
func TestZipLongSymlink(t *testing.T) {
	t.Parallel()
	archive := zipOf(t, func(w *zip.Writer) {
		fh := &zip.FileHeader{Name: "long", Method: zip.Deflate}
		fh.SetMode(fs.ModeSymlink | 0o777)
		fw, err := w.CreateHeader(fh)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(fw, strings.Repeat("x", 5000)); err != nil {
			t.Fatal(err)
		}
	})
	_, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	checkStop(t, err, domain.ArchiveCorrupt, "zip format error: link text of 5000 bytes is longer than 4096", []byte("long"))
}

// m4b D5: unpacked bytes that disagree with the stored size, or packed data
// that ends inside its deflate stream, are corrupt.
func TestZipSizeMismatch(t *testing.T) {
	t.Parallel()
	data := []byte("twelve bytes")
	crc := crc32.ChecksumIEEE(data)
	long := randomBytes(9, 64<<10)
	packed := deflateOf(t, long)
	archive := zipOf(t, func(w *zip.Writer) {
		zipRaw(t, w, &zip.FileHeader{Name: "more", Method: zip.Store, CRC32: crc, UncompressedSize64: 11}, data)
		zipRaw(t, w, &zip.FileHeader{Name: "fewer", Method: zip.Store, CRC32: crc, UncompressedSize64: 13}, data)
		zipRaw(t, w, &zip.FileHeader{Name: "fewer-deflated", Method: zip.Deflate, CRC32: crc, UncompressedSize64: 13},
			deflateOf(t, data))
		zipRaw(t, w, &zip.FileHeader{Name: "cut", Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(long),
			UncompressedSize64: uint64(len(long))}, packed[:len(packed)/2])
	})
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []struct{ detail, member string }{
		{detailSize, "more"}, {detailSize, "fewer"}, {detailSize, "fewer-deflated"}, {detailEOF, "cut"},
	} {
		_, err := readMember(z, index)
		checkStop(t, err, domain.ArchiveCorrupt, want.detail, []byte(want.member))
	}
}

// drainBy reads r to its end or first error with reads of size bytes.
func drainBy(r io.Reader, size int) error {
	buf := make([]byte, size)
	for {
		if _, err := r.Read(buf); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// m4b D6: a zip's budgets count the listing and every member read
// together: a member under the budget alone stops after another was read.
func TestZipBudgetsAreCumulative(t *testing.T) {
	t.Parallel()
	a, b := randomBytes(10, 600<<10), randomBytes(11, 600<<10)
	archive := zipOf(t, func(w *zip.Writer) {
		zipRaw(t, w, &zip.FileHeader{Name: "a.bin", Method: zip.Store, CRC32: crc32.ChecksumIEEE(a),
			UncompressedSize64: uint64(len(a))}, a)
		zipRaw(t, w, &zip.FileHeader{Name: "b.bin", Method: zip.Store, CRC32: crc32.ChecksumIEEE(b),
			UncompressedSize64: uint64(len(b))}, b)
	})
	read := func(z *Zip, index int) error {
		rc, err := z.Open(index)
		if err != nil {
			return err
		}
		defer rc.Close()
		return drainBy(rc, 1<<20)
	}
	lim := limits
	lim.MaxBytes = 1 << 20
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), lim)
	if err != nil {
		t.Fatal(err)
	}
	if err := read(z, 0); err != nil {
		t.Fatal(err)
	}
	checkStop(t, read(z, 1), domain.ArchivePartial, detailBytes, []byte("b.bin"))

	// Each member takes two reads, the data then the end: four clock reads
	// pass the deadline at the fourth, in the second member.
	clock := &fakeClock{now: t0, step: time.Second}
	lim = limits
	lim.Now, lim.Deadline = clock.Now, t0.Add(2*time.Second)
	if z, err = OpenZip(bytes.NewReader(archive), int64(len(archive)), lim); err != nil {
		t.Fatal(err)
	}
	if err := read(z, 0); err != nil {
		t.Fatal(err)
	}
	checkStop(t, read(z, 1), domain.ArchivePartial, detailTime, []byte("b.bin"))
}

// failingReaderAt fails every read covering the byte at.
type failingReaderAt struct {
	r   io.ReaderAt
	at  int64
	err error
}

func (f *failingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off <= f.at && f.at < off+int64(len(p)) {
		return 0, f.err
	}
	return f.r.ReadAt(p, off)
}

// The OpenZip contract: a failed read of the archive is returned wrapped,
// not as a *Stop, by OpenZip and by a member's reader.
func TestZipReadErrors(t *testing.T) {
	t.Parallel()
	data := randomBytes(12, 64<<10)
	archive := zipOf(t, func(w *zip.Writer) { zipFile(t, w, "a.bin", string(data)) })
	size := int64(len(archive))
	errDisk := errors.New("disk on fire")
	notStop := func(what string, err error) {
		if !errors.Is(err, errDisk) || errors.As(err, new(*Stop)) {
			t.Errorf("%s: %v (%T), want a wrapped %v", what, err, err, errDisk)
		}
	}
	_, err := OpenZip(&failingReaderAt{r: bytes.NewReader(archive), at: size - 1, err: errDisk}, size, limits)
	notStop("tail", err)
	// The member's data lies before the central directory, which is read.
	dir := int64(binary.LittleEndian.Uint32(archive[size-zipEndLen+16:]))
	z, err := OpenZip(&failingReaderAt{r: bytes.NewReader(archive), at: dir - 1000, err: errDisk}, size, limits)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readMember(z, 0)
	notStop("member", err)
}

// m4b D6: Open may be called concurrently; the budgets are shared.
func TestZipConcurrentOpens(t *testing.T) {
	t.Parallel()
	const n = 8
	want := make([][]byte, n)
	archive := zipOf(t, func(w *zip.Writer) {
		for i := range n {
			want[i] = randomBytes(uint64(20+i), 32<<10)
			zipFile(t, w, fmt.Sprintf("m%d", i), string(want[i]))
		}
	})
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if b, err := readMember(z, i); err != nil || !bytes.Equal(b, want[i]) {
				t.Errorf("member %d: %d bytes, %v", i, len(b), err)
			}
		})
	}
	wg.Wait()
	if got := z.meter.unpacked.Load(); got != n*32<<10 {
		t.Errorf("counted %d unpacked bytes, want %d", got, n*32<<10)
	}
}

// R2 design D17: a stored file member is listed as Stored, and its Section
// reads its exact bytes from the archive file at any offset; a deflated
// member, a directory, a symlink, a stored member whose sizes disagree, and
// an unknown index have no section.
func TestZipSection(t *testing.T) {
	t.Parallel()
	stored := randomBytes(30, 100_000)
	twelve := []byte("twelve bytes")
	archive := zipOf(t, func(w *zip.Writer) {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: "stored.bin", Method: zip.Store, Modified: t0})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(stored); err != nil {
			t.Fatal(err)
		}
		zipFile(t, w, "deflated.bin", string(stored))
		zipFile(t, w, "d/", "")
		fh := &zip.FileHeader{Name: "link", Method: zip.Store, Modified: t0}
		fh.SetMode(fs.ModeSymlink | 0o777)
		if fw, err = w.CreateHeader(fh); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(fw, "stored.bin"); err != nil {
			t.Fatal(err)
		}
		zipRaw(t, w, &zip.FileHeader{Name: "empty", Method: zip.Store, CRC32: crc32.ChecksumIEEE(nil)}, nil)
		zipRaw(t, w, &zip.FileHeader{Name: "mismatch", Method: zip.Store, CRC32: crc32.ChecksumIEEE(twelve),
			UncompressedSize64: 11}, twelve)
	})
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range z.Members() {
		want := m.Kind == domain.MemberFile && (m.Index == 0 || m.Index == 4 || m.Index == 5)
		if m.Stored != want {
			t.Errorf("member %d %q: Stored = %v, want %v", m.Index, m.Path, m.Stored, want)
		}
	}
	sr, ok := z.Section(0)
	if !ok {
		t.Fatal("stored member has no section")
	}
	if sr.Size() != int64(len(stored)) {
		t.Fatalf("section of %d bytes, want %d", sr.Size(), len(stored))
	}
	if b, err := io.ReadAll(sr); err != nil || !bytes.Equal(b, stored) {
		t.Fatalf("section read %d bytes, %v; want the member's %d bytes", len(b), err, len(stored))
	}
	n := int64(len(stored))
	for _, r := range [][2]int64{{0, 1}, {1, 4096}, {n / 2, 1000}, {n - 1, 1}, {n - 4096, 4096}, {12_345, 54_321}} {
		buf := make([]byte, r[1])
		if got, err := sr.ReadAt(buf, r[0]); err != nil && !(err == io.EOF && got == len(buf)) {
			t.Errorf("ReadAt(%d, %d) = %d, %v", r[0], r[1], got, err)
		} else if !bytes.Equal(buf, stored[r[0]:r[0]+r[1]]) {
			t.Errorf("ReadAt(%d, %d) differs from the member's bytes", r[0], r[1])
		}
	}
	if got, err := sr.ReadAt(make([]byte, 10), n-4); got != 4 || err != io.EOF {
		t.Errorf("ReadAt past the end = %d, %v; want 4, EOF", got, err)
	}
	if sr, ok := z.Section(4); !ok || sr.Size() != 0 {
		t.Errorf("empty stored member: section %v", ok)
	}
	for _, index := range []int{1, 2, 3, 5, 6, -1} {
		if sr, ok := z.Section(index); ok || sr != nil {
			t.Errorf("Section(%d) = %v, %v; want none", index, sr, ok)
		}
	}
	// Open still reads the members Section declines.
	if b, err := readMember(z, 1); err != nil || !bytes.Equal(b, stored) {
		t.Errorf("deflated member: %d bytes, %v", len(b), err)
	}
	_, err = readMember(z, 5)
	checkStop(t, err, domain.ArchiveCorrupt, detailSize, []byte("mismatch"))
}
