package content

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand/v2"
	"testing"

	"precious/internal/domain"
)

// zipEntry is one member of a test zip.
type zipEntry struct {
	name   string
	data   []byte
	stored bool
}

func makeZip(t *testing.T, members ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name, Method: zip.Deflate, Modified: fileTime}
		if m.stored {
			h.Method = zip.Store
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// randomBytes returns n reproducible random bytes.
func randomBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewChaCha8([32]byte{byte(seed), byte(seed >> 8)}))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func makeTarGzip(t *testing.T, members ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.data)), ModTime: fileTime,
			Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// archiveRow is an archives row.
type archiveRow struct {
	state   domain.ArchiveState
	detail  string
	members int
}

func (e *env) archive(src domain.SourceID, path string) archiveRow {
	e.t.Helper()
	var a archiveRow
	var detail []byte
	if err := e.st.Reader().QueryRow(`SELECT a.state, coalesce(a.detail, ''), a.members FROM archives a
		JOIN entries e ON e.id = a.entry_id WHERE e.source_id = ? AND e.path = ?`, string(src), []byte(path)).
		Scan(&a.state, &detail, &a.members); err != nil {
		e.t.Fatalf("archive %s: %v", path, err)
	}
	a.detail = string(detail)
	return a
}

// memberStates returns the archive's members by path with their state
// (directories "").
func (e *env) memberStates(src domain.SourceID, path string) map[string]string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT m.path, coalesce(m.state, '') FROM archive_members m
		JOIN entries e ON e.id = m.archive_id WHERE e.source_id = ? AND e.path = ?`, string(src), []byte(path))
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p []byte
		var s string
		if err := rows.Scan(&p, &s); err != nil {
			e.t.Fatal(err)
		}
		out[string(p)] = s
	}
	return out
}

// A changed archive is listed again after a rescan, with its new member.
func TestChangedArchiveListedAgain(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	z := root.File("fotos.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "a.jpg", data: []byte("aaaa")}))
	e.scan("fotos")
	e.hash("fotos")
	if a := e.archive("fotos", "fotos.zip"); a.state != domain.ArchiveComplete || a.members != 1 {
		t.Fatalf("archive %+v", a)
	}
	z.Content(makeZip(t, zipEntry{name: "a.jpg", data: []byte("aaaa")}, zipEntry{name: "b.jpg", data: []byte("bbbbb")}))
	e.scan("fotos")
	e.rec.Reset()
	e.hash("fotos")
	if a := e.archive("fotos", "fotos.zip"); a.state != domain.ArchiveComplete || a.members != 2 {
		t.Errorf("archive after the change %+v", a)
	}
	if m := e.memberStates("fotos", "fotos.zip"); len(m) != 2 || m["b.jpg"] == "" {
		t.Errorf("members %v", m)
	}
	if got := e.opened(); len(got) != 1 || got[0] != "fotos.zip" {
		t.Errorf("opened %q", got)
	}
}

// A 64 MiB tar.gz with 1,000 members is read once from start to end, and
// every member is listed and hashed in that pass.
func TestLargeTarGzipReadOnce(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(gz)
	data := randomBytes(7, 66<<10) // with headers, 1,000 members pass 64 MiB
	for i := range 1000 {
		data[0], data[1] = byte(i), byte(i>>8) // every member its own content
		if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("d%02d/f%04d.bin", i%10, i), Mode: 0o644,
			Size: int64(len(data)), ModTime: fileTime, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	size := int64(buf.Len())
	if size < 64<<20 {
		t.Fatalf("the tar.gz is %d bytes", size)
	}
	root := e.disk("fotos", "/mnt/fotos", posix)
	root.File("big.tar.gz", 0, fileTime).Content(buf.Bytes())
	e.scan("fotos")
	e.rec.Reset()
	e.hash("fotos")
	if n := e.readOf("big.tar.gz"); n != size {
		t.Errorf("read %d bytes of the %d-byte archive", n, size)
	}
	if got := e.opened(); len(got) != 1 {
		t.Errorf("opened %q", got)
	}
	if a := e.archive("fotos", "big.tar.gz"); a.state != domain.ArchiveComplete || a.members != 1010 {
		t.Errorf("archive %+v; want complete with 1,000 files and 10 folders", a)
	}
	if n := e.count(`SELECT count(*) FROM archive_members WHERE kind = 'file' AND state = 'hashed'`); n != 1000 {
		t.Errorf("%d members hashed, want 1000", n)
	}
}

// A budget leaves the archive partial with no members.
func TestBudgetLeavesArchivePartial(t *testing.T) {
	e := newEnv(t)
	e.a.MaxMembers = 2
	e.service()
	root := e.disk("fotos", "/mnt/fotos", posix)
	root.File("tres.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "a", data: []byte("1")},
		zipEntry{name: "b", data: []byte("22")}, zipEntry{name: "c", data: []byte("333")}))
	e.scan("fotos")
	e.hash("fotos")
	a := e.archive("fotos", "tres.zip")
	if a.state != domain.ArchivePartial || a.members != 0 || a.detail != `{"budget":"entry"}` {
		t.Errorf("archive %+v; want partial at the entry budget", a)
	}
	if m := e.memberStates("fotos", "tres.zip"); len(m) != 0 {
		t.Errorf("members %v", m)
	}
}

// A zip inside a zip is a member hashed as a file; its own members are not
// listed.
func TestNestedZipStaysClosed(t *testing.T) {
	e := newEnv(t)
	inner := makeZip(t, zipEntry{name: "x.txt", data: []byte("inner")})
	root := e.disk("fotos", "/mnt/fotos", posix)
	root.File("outer.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "inner.zip", data: inner}))
	root.File("inner.zip", 0, fileTime).Content(inner) // a copy outside, so the member is hashed
	e.scan("fotos")
	e.hash("fotos")
	if m := e.memberStates("fotos", "outer.zip"); len(m) != 1 || m["inner.zip"] != string(domain.ContentHashed) {
		t.Errorf("outer.zip members %v; want inner.zip hashed", m)
	}
	if a := e.archive("fotos", "inner.zip"); a.members != 1 {
		t.Errorf("the plain inner.zip %+v", a)
	}
	if n := e.count(`SELECT count(*) FROM archive_members`); n != 2 {
		t.Errorf("%d member rows; the nested zip's members were listed", n)
	}
	if g := e.groups(); len(g) != 1 || len(g[0]) != 2 {
		t.Errorf("groups %q; want inner.zip with its copy inside outer.zip", g)
	}
}

// Zip members of unique sizes cost no reads: only the central directory is
// read.
func TestUniqueSizeZipMembersCostNoReads(t *testing.T) {
	e := newEnv(t)
	e.h.ReadChunkBytes = 64 << 10
	e.service()
	root := e.disk("fotos", "/mnt/fotos", posix)
	z := makeZip(t, zipEntry{name: "a.bin", data: randomBytes(1, 300<<10), stored: true},
		zipEntry{name: "b.bin", data: randomBytes(2, 301<<10), stored: true},
		zipEntry{name: "c.bin", data: randomBytes(3, 302<<10), stored: true})
	root.File("dados.zip", 0, fileTime).Content(z)
	e.scan("fotos")
	e.rec.Reset()
	e.hash("fotos")
	for p, s := range e.memberStates("fotos", "dados.zip") {
		if s != string(domain.ContentUniqueSize) {
			t.Errorf("%s is %s, want unique_size", p, s)
		}
	}
	if n := e.readOf("dados.zip"); n == 0 || n >= int64(len(z))/2 {
		t.Errorf("read %d of %d bytes; want only the central directory", n, len(z))
	}
}

// Sizes that a tar-family listing adds make candidates of a zip member and
// of a smaller archive file listed earlier in the same job: the job reads
// them before it ends, rather than leaving them unchecked.
func TestCandidatesFromAStreamedListingReadInTheSameJob(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	copied := randomBytes(1, 5000)
	lib := makeTarGzip(t, zipEntry{name: "lib/a.c", data: randomBytes(2, 3000)})
	root.File("fotos.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "a.txt", data: copied}))
	root.File("lib.tar.gz", 0, fileTime).Content(lib)
	root.File("bundle.tar.gz", 0, fileTime).Content(makeTarGzip(t, zipEntry{name: "copy.txt", data: copied},
		zipEntry{name: "lib.tar.gz", data: lib}))
	e.scan("fotos")
	e.hash("fotos")
	if c := e.coverage("fotos"); c.UncheckedFiles != 0 {
		t.Errorf("coverage %+v; want every candidate checked", c)
	}
	diffGroups(t, e.groups(), [][]string{
		{"fotos:bundle.tar.gz!copy.txt", "fotos:fotos.zip!a.txt"},
		{"fotos:bundle.tar.gz!lib.tar.gz", "fotos:lib.tar.gz"},
	})
}
