package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"precious/internal/domain"
)

// t0 is every fixture member's modification time.
var t0 = time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)

// limits are budgets no small fixture reaches.
var limits = Limits{MaxEntries: 1_000_000, MaxBytes: 1 << 40, MaxRatio: 100}

// seen is one visited or listed member, its path and its hard link's
// target joined by '/'.
type seen struct {
	path   string
	kind   domain.MemberKind
	size   int64
	link   string
	linkTo string
	stored bool
	data   string
	mtime  time.Time
	index  int
}

func seenOf(m Member, data string) seen {
	return seen{path: string(bytes.Join(m.Path, []byte("/"))), kind: m.Kind, size: m.Size,
		link: string(m.LinkText), linkTo: string(bytes.Join(m.LinkTo, []byte("/"))), stored: m.Stored,
		data: data, mtime: m.Mtime, index: m.Index}
}

func checkSeen(t *testing.T, got, want []seen) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%d members, want %d:\n got %+v\nwant %+v", len(got), len(want), got, want)
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.path != w.path || g.kind != w.kind || g.size != w.size || g.link != w.link ||
			g.linkTo != w.linkTo || g.stored != w.stored || g.data != w.data || !g.mtime.Equal(w.mtime) ||
			g.index != w.index {
			t.Errorf("member %d = %+v, want %+v", i, g, w)
		}
	}
}

// collect streams archive and returns every member, reading all data.
func collect(t *testing.T, f domain.ArchiveFormat, name string, archive []byte, lim Limits) ([]seen, error) {
	t.Helper()
	var got []seen
	err := Stream(context.Background(), f, []byte(name), bytes.NewReader(archive), lim,
		func(m Member, data io.Reader) error {
			if (data != nil) != (m.Kind == domain.MemberFile && m.LinkTo == nil) {
				t.Errorf("member %q of kind %s, link to %q: data present %v", m.Path, m.Kind, m.LinkTo, data != nil)
			}
			var b []byte
			if data != nil {
				var err error
				if b, err = io.ReadAll(data); err != nil {
					return err
				}
			}
			got = append(got, seenOf(m, string(b)))
			return nil
		})
	return got, err
}

// checkStop fails unless err is a *Stop with state, detail, and member; a
// nil member wants Stop.Member nil.
func checkStop(t *testing.T, err error, state domain.ArchiveState, detail string, member []byte) *Stop {
	t.Helper()
	s, ok := err.(*Stop)
	if !ok {
		t.Fatalf("error %v (%T), want a *Stop %s %q", err, err, state, detail)
	}
	if s.State != state || s.Detail != detail || !bytes.Equal(s.Member, member) || (s.Member == nil) != (member == nil) {
		t.Fatalf("stop = %s %q member %q (nil %v), want %s %q member %q (nil %v)",
			s.State, s.Detail, s.Member, s.Member == nil, state, detail, member, member == nil)
	}
	return s
}

// randomBytes returns n incompressible bytes, the same for a seed.
func randomBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func gzipOf(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Name = "stored-name-is-ignored"
	w.ModTime = t0
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tarOf builds a tar archive; add writes members through tw, or raw blocks
// to buf after tw.Flush.
func tarOf(t *testing.T, add func(tw *tar.Writer, buf *bytes.Buffer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add(tw, &buf)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarFile(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644,
		Size: int64(len(data)), ModTime: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
}

func tarHeader(t *testing.T, tw *tar.Writer, h *tar.Header) {
	t.Helper()
	h.ModTime = t0
	if err := tw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
}

// rawTarHeader returns a header block written by hand: v7 (no magic) or
// GNU. sealTarHeader sets its checksum.
func rawTarHeader(name string, typeflag byte, size int64, gnu bool) *[512]byte {
	var b [512]byte
	copy(b[0:100], name)
	copy(b[100:108], "0000644\x00")
	copy(b[108:116], "0000000\x00")
	copy(b[116:124], "0000000\x00")
	copy(b[124:136], fmt.Sprintf("%011o\x00", size))
	copy(b[136:148], fmt.Sprintf("%011o\x00", t0.Unix()))
	b[156] = typeflag
	if gnu {
		copy(b[257:265], "ustar  \x00")
	}
	return &b
}

func sealTarHeader(b *[512]byte) {
	copy(b[148:156], "        ")
	var sum int64
	for _, c := range b {
		sum += int64(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
}

// writeRaw appends a hand-made header and its data, padded, after the
// members tw wrote.
func writeRaw(t *testing.T, tw *tar.Writer, buf *bytes.Buffer, hdr *[512]byte, data []byte) {
	t.Helper()
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	sealTarHeader(hdr)
	buf.Write(hdr[:])
	buf.Write(data)
	buf.Write(make([]byte, (512-len(data)%512)%512))
}

// sparseImage is the logical content of the sparse member disk.img: "abc"
// at 0 and "wxyz" at 9000, in 10,000 bytes.
func sparseImage() string {
	b := make([]byte, 10000)
	copy(b, "abc")
	copy(b[9000:], "wxyz")
	return string(b)
}

// sampleTar is the tar archive of every member kind. testdata/sample.tar.bz2
// is `bzip2 -9` of exactly these bytes (TestSampleTarBzip2Fixture).
func sampleTar(t *testing.T) []byte {
	return tarOf(t, func(tw *tar.Writer, buf *bytes.Buffer) {
		// A v7 header with the legacy '\x00' type comes first: the
		// signature accepts a v7 header without "ustar".
		writeRaw(t, tw, buf, rawTarHeader("readme", '\x00', 6, false), []byte("hello\n"))
		tarHeader(t, tw, &tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
		if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader,
			PAXRecords: map[string]string{"comment": "0123456789abcdef"}}); err != nil {
			t.Fatal(err)
		}
		tarHeader(t, tw, &tar.Header{Name: "docs/", Typeflag: tar.TypeDir, Mode: 0o755})
		tarFile(t, tw, "docs/plan.txt", []byte("plan v1\n"))
		tarHeader(t, tw, &tar.Header{Name: "docs/latest", Typeflag: tar.TypeSymlink, Linkname: "plan.txt"})
		tarHeader(t, tw, &tar.Header{Name: "docs/plan-copy.txt", Typeflag: tar.TypeLink, Linkname: "./docs//plan.txt"})
		tarHeader(t, tw, &tar.Header{Name: "docs/plan-copy2.txt", Typeflag: tar.TypeLink, Linkname: "docs/plan-copy.txt"})
		tarFile(t, tw, "a/b/c.txt", []byte("deep\n"))
		tarHeader(t, tw, &tar.Header{Name: "dev/tty0", Typeflag: tar.TypeChar, Devmajor: 4})
		tarHeader(t, tw, &tar.Header{Name: "pipe", Typeflag: tar.TypeFifo})
		// An old GNU sparse member: 7 bytes of data in two runs.
		sparse := rawTarHeader("disk.img", tar.TypeGNUSparse, 7, true)
		copy(sparse[386:], fmt.Sprintf("%011o\x00%011o\x00%011o\x00%011o\x00", 0, 3, 9000, 4))
		copy(sparse[483:495], fmt.Sprintf("%011o\x00", 10000))
		writeRaw(t, tw, buf, sparse, []byte("abcwxyz"))
		tarFile(t, tw, `dir\file.txt`, []byte("backslash\n"))
		if err := tw.WriteHeader(&tar.Header{Name: "\xff\xfe.bin", Typeflag: tar.TypeReg, Size: 3,
			ModTime: t0, Format: tar.FormatGNU}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("raw")); err != nil {
			t.Fatal(err)
		}
	})
}

// sampleMembers are sampleTar's members as visited: no top level, no pax
// global header, no implied folder.
var sampleMembers = []seen{
	{path: "readme", kind: domain.MemberFile, size: 6, data: "hello\n", mtime: t0, index: -1},
	{path: "docs", kind: domain.MemberDirectory, mtime: t0, index: -1},
	{path: "docs/plan.txt", kind: domain.MemberFile, size: 8, data: "plan v1\n", mtime: t0, index: -1},
	{path: "docs/latest", kind: domain.MemberSymlink, link: "plan.txt", mtime: t0, index: -1},
	{path: "docs/plan-copy.txt", kind: domain.MemberFile, size: 8, linkTo: "docs/plan.txt", mtime: t0, index: -1},
	{path: "docs/plan-copy2.txt", kind: domain.MemberFile, size: 8, linkTo: "docs/plan-copy.txt", mtime: t0, index: -1},
	{path: "a/b/c.txt", kind: domain.MemberFile, size: 5, data: "deep\n", mtime: t0, index: -1},
	{path: "dev/tty0", kind: domain.MemberSpecial, mtime: t0, index: -1},
	{path: "pipe", kind: domain.MemberSpecial, mtime: t0, index: -1},
	{path: "disk.img", kind: domain.MemberFile, size: 10000, data: sparseImage(), mtime: t0, index: -1},
	{path: `dir\file.txt`, kind: domain.MemberFile, size: 10, data: "backslash\n", mtime: t0, index: -1},
	{path: "\xff\xfe.bin", kind: domain.MemberFile, size: 3, data: "raw", mtime: t0, index: -1},
}

// notesText is the content of testdata/notes.txt.bz2.
var notesText = strings.Repeat("bzip2 member\n", 100)

// zipOf builds a zip archive with the members add writes.
func zipOf(t *testing.T, add func(w *zip.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipFile adds a deflated member, or a directory for a name ending in '/'.
func zipFile(t *testing.T, w *zip.Writer, name, data string) {
	t.Helper()
	fw, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: t0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(fw, data); err != nil {
		t.Fatal(err)
	}
}

// zipRaw adds a member with the header's sizes, CRC, flags, and method, and
// raw as its packed data.
func zipRaw(t *testing.T, w *zip.Writer, fh *zip.FileHeader, raw []byte) {
	t.Helper()
	if fh.CompressedSize64 == 0 {
		fh.CompressedSize64 = uint64(len(raw))
	}
	fw, err := w.CreateRaw(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatal(err)
	}
}

func deflateOf(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readMember opens a zip member and reads it to its end or its first error.
func readMember(z *Zip, index int) ([]byte, error) {
	rc, err := z.Open(index)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// recordingReaderAt records every read.
type recordingReaderAt struct {
	r     io.ReaderAt
	mu    sync.Mutex
	reads [][2]int64 // offset, length
}

func (r *recordingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	r.reads = append(r.reads, [2]int64{off, int64(len(p))})
	r.mu.Unlock()
	return r.r.ReadAt(p, off)
}

// fakeClock advances by step at every call.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	step  time.Duration
	calls int
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	now := c.now
	c.now = c.now.Add(c.step)
	return now
}
