package archive

import (
	"archive/tar"
	"bytes"
	"compress/bzip2"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
)

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// m4b D3, D5: each tar format yields its members' bytes, kinds, and sizes
// in archive order, hard links naming their targets, the sparse member at
// its logical size, implied folders, the top level, and the pax global
// header not visited; and r is read to its end.
func TestStreamTarFormats(t *testing.T) {
	t.Parallel()
	raw := sampleTar(t)
	fixture := readFixture(t, "sample.tar.bz2")
	// The fixture is `bzip2 -9` of sampleTar's bytes.
	if b, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(fixture))); err != nil || !bytes.Equal(b, raw) {
		t.Fatalf("testdata/sample.tar.bz2 does not hold sampleTar (%v)", err)
	}
	for _, c := range []struct {
		f       domain.ArchiveFormat
		archive []byte
	}{
		{domain.ArchiveTar, raw},
		{domain.ArchiveTarGzip, gzipOf(t, raw)},
		{domain.ArchiveTarBzip2, fixture},
	} {
		r := &countingReader{r: bytes.NewReader(c.archive)}
		var got []seen
		err := Stream(context.Background(), c.f, []byte("sample"), r, limits, func(m Member, data io.Reader) error {
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
		if err != nil {
			t.Fatalf("%s: %v", c.f, err)
		}
		checkSeen(t, got, sampleMembers)
		if r.n != int64(len(c.archive)) {
			t.Errorf("%s: read %d of %d bytes", c.f, r.n, len(c.archive))
		}
	}
}

// m4b D2: a gzip or bzip2 archive holds one member named MemberName, of
// unknown size; multistream gzip and concatenated bzip2 read to the end.
func TestStreamSingleFile(t *testing.T) {
	t.Parallel()
	first := randomBytes(1, 70000)
	second := []byte("second stream\n")
	notes := readFixture(t, "notes.txt.bz2")
	for _, c := range []struct {
		f       domain.ArchiveFormat
		name    string
		archive []byte
		want    seen
	}{
		{domain.ArchiveGzip, "e.sql.gz", append(gzipOf(t, first), gzipOf(t, second)...),
			seen{path: "e.sql", kind: domain.MemberFile, size: -1, data: string(first) + string(second), mtime: t0, index: -1}},
		{domain.ArchiveGzip, "Dump.SQL.GZ.bak", gzipOf(t, []byte("x")),
			seen{path: "Dump.SQL", kind: domain.MemberFile, size: -1, data: "x", mtime: t0, index: -1}},
		{domain.ArchiveBzip2, "notes.txt.bz2", notes,
			seen{path: "notes.txt", kind: domain.MemberFile, size: -1, data: notesText, index: -1}},
		{domain.ArchiveBzip2, "\xe9t\xe9.bz2", append(append([]byte(nil), notes...), notes...),
			seen{path: "\xe9t\xe9", kind: domain.MemberFile, size: -1, data: notesText + notesText, index: -1}},
	} {
		got, err := collect(t, c.f, c.name, c.archive, limits)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		checkSeen(t, got, []seen{c.want})
	}
}

// m4b D2: the signature confirms the format; a mismatch is unsupported.
func TestStreamSignatures(t *testing.T) {
	t.Parallel()
	badSum := sampleTar(t)
	badSum[0] ^= 1 // the first header's checksum no longer matches
	notTar := gzipOf(t, []byte(strings.Repeat("not a tar header ", 64)))
	for _, c := range []struct {
		f       domain.ArchiveFormat
		name    string
		archive []byte
	}{
		{domain.ArchiveGzip, "e.sql.gz", []byte("PK\x03\x04 not gzip")},
		{domain.ArchiveTarGzip, "x.tgz", []byte("BZh91AY&SY")},
		{domain.ArchiveBzip2, "x.bz2", gzipOf(t, []byte("gzip"))},
		{domain.ArchiveTarBzip2, "x.tbz2", nil},
		{domain.ArchiveTar, "x.tar", badSum},
		{domain.ArchiveTar, "x.tar", []byte("short")},
		{domain.ArchiveTarGzip, "x.tgz", notTar},
	} {
		_, err := collect(t, c.f, c.name, c.archive, limits)
		checkStop(t, err, domain.ArchiveUnsupported, "not a "+string(c.f)+" archive", nil)
	}
	// An all-zero first block is an empty archive.
	got, err := collect(t, domain.ArchiveTar, "e.tar", make([]byte, 10240), limits)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty tar: %v, %v", got, err)
	}
}

// Spec scenario "Damaged archive": a .tar.gz cut off in the middle is
// corrupt, naming the member being read.
func TestTarGzCutOffInTheMiddle(t *testing.T) {
	t.Parallel()
	archive := gzipOf(t, tarOf(t, func(tw *tar.Writer, _ *bytes.Buffer) {
		tarFile(t, tw, "first.txt", []byte("one"))
		tarFile(t, tw, "big.bin", randomBytes(2, 256<<10))
		tarFile(t, tw, "last.txt", []byte("z"))
	}))
	_, err := collect(t, domain.ArchiveTarGzip, "b.tar.gz", archive[:len(archive)/2], limits)
	checkStop(t, err, domain.ArchiveCorrupt, detailEOF, []byte("big.bin"))
}

// tarEntry is a tar member for the path-rule cases.
type tarEntry struct {
	name     string
	typeflag byte
	link     string
}

func tarEntries(t *testing.T, entries ...tarEntry) []byte {
	return tarOf(t, func(tw *tar.Writer, _ *bytes.Buffer) {
		for _, e := range entries {
			if e.typeflag == tar.TypeReg {
				tarFile(t, tw, e.name, []byte("data"))
				continue
			}
			tarHeader(t, tw, &tar.Header{Name: e.name, Typeflag: e.typeflag, Linkname: e.link})
		}
	})
}

// m4b D4, D5: tar members that leave the archive or collide are rejected,
// and a hard link must name an earlier file member; each names the member.
func TestStreamMemberRules(t *testing.T) {
	t.Parallel()
	const (
		reg  = tar.TypeReg
		dir  = tar.TypeDir
		link = tar.TypeLink
	)
	for _, c := range []struct {
		name    string
		entries []tarEntry
		state   domain.ArchiveState
		detail  string
		member  string
	}{
		{"traversal", []tarEntry{{"ok.txt", reg, ""}, {"../evil", reg, ""}}, domain.ArchiveRejected, detailLeaves, "../evil"},
		{"absolute", []tarEntry{{"/etc/passwd", reg, ""}}, domain.ArchiveRejected, detailLeaves, "/etc/passwd"},
		{"duplicate", []tarEntry{{"a/b.txt", reg, ""}, {"a//b.txt", reg, ""}}, domain.ArchiveRejected, detailSharedPath, "a/b.txt"},
		{"file then folder", []tarEntry{{"a", reg, ""}, {"a/b", reg, ""}}, domain.ArchiveRejected, detailFileFolder, "a/b"},
		{"folder then file", []tarEntry{{"a/b", reg, ""}, {"a", reg, ""}}, domain.ArchiveRejected, detailFileFolder, "a"},
		{"directory then symlink", []tarEntry{{"x/", dir, ""}, {"x", tar.TypeSymlink, "y"}}, domain.ArchiveRejected, detailFileFolder, "x"},
		{"file at the top level", []tarEntry{{".", reg, ""}}, domain.ArchiveRejected, detailFileFolder, ""},
		{"hard link to a later member", []tarEntry{{"early", link, "late"}, {"late", reg, ""}}, domain.ArchiveCorrupt, detailHardlink, "early"},
		{"hard link to a directory", []tarEntry{{"d/", dir, ""}, {"l", link, "d"}}, domain.ArchiveCorrupt, detailHardlink, "l"},
		{"hard link to a symlink", []tarEntry{{"s", tar.TypeSymlink, "x"}, {"l", link, "s"}}, domain.ArchiveCorrupt, detailHardlink, "l"},
		{"hard link to itself", []tarEntry{{"l", link, "l"}}, domain.ArchiveCorrupt, detailHardlink, "l"},
		{"hard link out of the archive", []tarEntry{{"f", reg, ""}, {"l", link, "../f"}}, domain.ArchiveCorrupt, detailHardlink, "l"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := collect(t, domain.ArchiveTar, "x.tar", tarEntries(t, c.entries...), limits)
			checkStop(t, err, c.state, c.detail, []byte(c.member))
		})
	}
	// Two directory members may share a path.
	got, err := collect(t, domain.ArchiveTar, "x.tar",
		tarEntries(t, tarEntry{"d/", dir, ""}, tarEntry{"d/f", reg, ""}, tarEntry{"./d/", dir, ""}), limits)
	if err != nil || len(got) != 3 {
		t.Fatalf("duplicate directory: %v, %v", got, err)
	}
}

// R2 design D1: a tar hard link is a file member of its target's size,
// without data, whose LinkTo names its earlier target by its cleaned path;
// a hard link to a hard link names that link, at the same size.
func TestStreamHardLinkNamesItsTarget(t *testing.T) {
	t.Parallel()
	archive := tarOf(t, func(tw *tar.Writer, _ *bytes.Buffer) {
		tarFile(t, tw, "a/x.bin", []byte("twelve bytes"))
		tarHeader(t, tw, &tar.Header{Name: "b/y.bin", Typeflag: tar.TypeLink, Linkname: "./a//x.bin"})
		tarHeader(t, tw, &tar.Header{Name: "z.bin", Typeflag: tar.TypeLink, Linkname: "b/y.bin"})
	})
	var got []Member
	err := Stream(context.Background(), domain.ArchiveTar, []byte("h.tar"), bytes.NewReader(archive), limits,
		func(m Member, data io.Reader) error {
			if (data == nil) != (m.LinkTo != nil) {
				t.Errorf("member %q, link to %q: data present %v", m.Path, m.LinkTo, data != nil)
			}
			got = append(got, m)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		path, linkTo string
		comps        int
	}{{"a/x.bin", "", 0}, {"b/y.bin", "a/x.bin", 2}, {"z.bin", "b/y.bin", 2}}
	if len(got) != len(want) {
		t.Fatalf("%d members, want %d", len(got), len(want))
	}
	for i, w := range want {
		m := got[i]
		path, linkTo := string(bytes.Join(m.Path, []byte("/"))), string(bytes.Join(m.LinkTo, []byte("/")))
		if path != w.path || linkTo != w.linkTo || len(m.LinkTo) != w.comps || m.Kind != domain.MemberFile ||
			m.Size != 12 || m.LinkText != nil || m.Stored {
			t.Errorf("member %d = %q kind %s size %d, link to %q (%d components), link text %q; want %q linking to %q",
				i, path, m.Kind, m.Size, linkTo, len(m.LinkTo), m.LinkText, w.path, w.linkTo)
		}
	}
}

// m4b D6: the entry budget stops a stream at the entry past it. Entries
// are the members and the folders their paths imply first: a/b/c/f1 is four
// entries, a/b/c/f2 one more; a directory member named before its content is
// one entry.
func TestStreamEntryBudget(t *testing.T) {
	t.Parallel()
	const reg, dir = tar.TypeReg, tar.TypeDir
	flat := tarEntries(t, tarEntry{"a", reg, ""}, tarEntry{"b", reg, ""}, tarEntry{"c", reg, ""})
	deep := tarEntries(t, tarEntry{"a/b/c/f1", reg, ""}, tarEntry{"a/b/c/f2", reg, ""})
	named := tarEntries(t, tarEntry{"d/", dir, ""}, tarEntry{"d/f", reg, ""})
	for _, c := range []struct {
		name       string
		archive    []byte
		maxEntries int64
		visited    int
		partial    bool
	}{
		{"flat", flat, 2, 2, true},
		{"flat", flat, 3, 3, false},
		{"deep", deep, 3, 0, true},
		{"deep", deep, 4, 1, true},
		{"deep", deep, 5, 2, false},
		{"named folder", named, 2, 2, false},
	} {
		lim := limits
		lim.MaxEntries = c.maxEntries
		got, err := collect(t, domain.ArchiveTar, "x.tar", c.archive, lim)
		if c.partial {
			checkStop(t, err, domain.ArchivePartial, detailEntries, nil)
		} else if err != nil {
			t.Fatalf("%s at %d entries: %v", c.name, c.maxEntries, err)
		}
		if len(got) != c.visited {
			t.Errorf("%s at %d entries: visited %d members, want %d", c.name, c.maxEntries, len(got), c.visited)
		}
	}
}

// m4b D6: the byte budget stops a stream while visit reads; data.Read
// returns the *Stop, and Stream returns the same one. A visit that leaves
// data unread meets it in Stream's drain.
func TestStreamByteBudget(t *testing.T) {
	t.Parallel()
	archive := tarOf(t, func(tw *tar.Writer, _ *bytes.Buffer) {
		tarFile(t, tw, "small", make([]byte, 1000))
		tarFile(t, tw, "big.bin", randomBytes(3, 1<<20))
	})
	lim := limits
	lim.MaxBytes = 512 << 10
	var fromData error
	err := Stream(context.Background(), domain.ArchiveTar, nil, bytes.NewReader(archive), lim,
		func(_ Member, data io.Reader) error {
			_, fromData = io.Copy(io.Discard, data)
			return fromData
		})
	s := checkStop(t, err, domain.ArchivePartial, detailBytes, []byte("big.bin"))
	if fromData != error(s) {
		t.Errorf("data.Read returned %v, Stream returned another error", fromData)
	}
	err = Stream(context.Background(), domain.ArchiveTar, nil, bytes.NewReader(archive), lim,
		func(Member, io.Reader) error { return nil })
	checkStop(t, err, domain.ArchivePartial, detailBytes, []byte("big.bin"))
}

// m4b D6: a bzip2 bomb (1 GiB of zeros in 785 bytes) stops at the ratio
// budget, after RatioGrace plus 100 times the packed bytes.
func TestStreamBzip2BombStopsAtTheRatioBudget(t *testing.T) {
	t.Parallel()
	bomb := readFixture(t, "zeros.bz2")
	lim := limits
	lim.MaxRatio = 100
	var n int64
	err := Stream(context.Background(), domain.ArchiveBzip2, []byte("zeros.bz2"), bytes.NewReader(bomb), lim,
		func(_ Member, data io.Reader) error {
			var err error
			n, err = io.Copy(io.Discard, data)
			return err
		})
	checkStop(t, err, domain.ArchivePartial, detailRatio, []byte("zeros"))
	if allowed := int64(RatioGrace + 100*len(bomb)); n <= RatioGrace || n > allowed {
		t.Fatalf("read %d unpacked bytes, want more than %d and at most %d", n, RatioGrace, allowed)
	}
}

// m4b D6: the time budget stops a slow stream, with a fake clock.
func TestStreamTimeBudget(t *testing.T) {
	t.Parallel()
	archive := tarOf(t, func(tw *tar.Writer, _ *bytes.Buffer) {
		tarFile(t, tw, "slow.bin", randomBytes(4, 1<<20))
	})
	clock := &fakeClock{now: t0, step: time.Second}
	lim := limits
	lim.Now, lim.Deadline = clock.Now, t0.Add(10*time.Second)
	_, err := collect(t, domain.ArchiveTar, "x.tar", archive, lim)
	checkStop(t, err, domain.ArchivePartial, detailTime, []byte("slow.bin"))
	if clock.calls != 12 {
		t.Errorf("the clock was read %d times, want 12: once per read until it passes the deadline", clock.calls)
	}
}

// m4b D6 and the Stream contract: a cancel mid-stream returns ctx.Err(),
// never a *Stop, whether visit returns data's error or leaves the drain to
// Stream; so does a cancel before the start.
func TestStreamCancel(t *testing.T) {
	t.Parallel()
	archive := tarOf(t, func(tw *tar.Writer, _ *bytes.Buffer) {
		tarFile(t, tw, "big.bin", randomBytes(5, 3<<20))
	})
	for _, propagate := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		err := Stream(ctx, domain.ArchiveTar, nil, bytes.NewReader(archive), limits,
			func(_ Member, data io.Reader) error {
				if _, err := io.ReadFull(data, make([]byte, 4096)); err != nil {
					return err
				}
				cancel()
				if !propagate {
					return nil
				}
				_, err := io.Copy(io.Discard, data)
				return err
			})
		if err == nil || err != ctx.Err() {
			t.Errorf("propagate %v: Stream returned %v, want ctx.Err() %v", propagate, err, ctx.Err())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Stream(ctx, domain.ArchiveTarGzip, nil, bytes.NewReader(gzipOf(t, archive)), limits,
		func(Member, io.Reader) error { t.Fatal("visited after a cancel"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream returned %v, want context.Canceled", err)
	}
}

// failingReader yields n bytes of r, then err.
type failingReader struct {
	r   io.Reader
	n   int
	err error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, f.err
	}
	p = p[:min(len(p), f.n)]
	n, err := f.r.Read(p)
	f.n -= n
	return n, err
}

// The Stream contract: a failed read of r is returned wrapped, not as a
// *Stop, and visit's error as is.
func TestStreamErrors(t *testing.T) {
	t.Parallel()
	archive := gzipOf(t, tarOf(t, func(tw *tar.Writer, _ *bytes.Buffer) {
		tarFile(t, tw, "big.bin", randomBytes(6, 256<<10))
	}))
	errDisk := errors.New("disk on fire")
	err := Stream(context.Background(), domain.ArchiveTarGzip, nil,
		&failingReader{r: bytes.NewReader(archive), n: len(archive) / 2, err: errDisk}, limits,
		func(_ Member, data io.Reader) error { _, err := io.Copy(io.Discard, data); return err })
	if !errors.Is(err, errDisk) || errors.As(err, new(*Stop)) {
		t.Errorf("failed read: Stream returned %v (%T), want a wrapped %v", err, err, errDisk)
	}
	errVisit := errors.New("visit failed")
	err = Stream(context.Background(), domain.ArchiveTarGzip, nil, bytes.NewReader(archive), limits,
		func(Member, io.Reader) error { return errVisit })
	if err != errVisit {
		t.Errorf("visit error: Stream returned %v, want %v", err, errVisit)
	}
}

// m4b D5: a stream checksum mismatch is corrupt, naming the member read
// when it is detected.
func TestStreamChecksumMismatch(t *testing.T) {
	t.Parallel()
	g := gzipOf(t, []byte("payload"))
	g[len(g)-8] ^= 0xff // the CRC-32 in the gzip trailer
	_, err := collect(t, domain.ArchiveGzip, "p.txt.gz", g, limits)
	checkStop(t, err, domain.ArchiveCorrupt, detailChecksum, []byte("p.txt"))

	bz := append([]byte(nil), readFixture(t, "notes.txt.bz2")...)
	bz[10] ^= 0xff // the first block's CRC
	_, err = collect(t, domain.ArchiveBzip2, "notes.txt.bz2", bz, limits)
	checkStop(t, err, domain.ArchiveCorrupt, detailChecksum, []byte("notes.txt"))

	// A tar's gzip trailer is read after the tar's end: no member is named.
	tgz := gzipOf(t, tarEntries(t, tarEntry{"a", tar.TypeReg, ""}))
	tgz[len(tgz)-8] ^= 0xff
	_, err = collect(t, domain.ArchiveTarGzip, "x.tgz", tgz, limits)
	checkStop(t, err, domain.ArchiveCorrupt, detailChecksum, nil)
}
