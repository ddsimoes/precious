package content

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"maps"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
)

// row returns the index's Row of a path of src.
func (e *env) row(src domain.SourceID, path string) Row {
	e.t.Helper()
	r := Row{Path: []byte(path)}
	if err := e.st.Reader().QueryRow(`SELECT size, mtime_ns, ctime_ns, ino FROM entries
		WHERE source_id = ? AND path = ?`, string(src), []byte(path)).Scan(&r.Size, &r.MtimeNs, &r.CtimeNs,
		&r.Ino); err != nil {
		e.t.Fatalf("entry %s:%q: %v", src, path, err)
	}
	return r
}

// openRoot opens the root of src as a read would, closed at the test's end.
func (e *env) openRoot(src domain.SourceID) (fsaccess.Dir, fsaccess.Capabilities) {
	e.t.Helper()
	o, err := e.src.Open(context.Background(), src)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { o.Root.Close() })
	return o.Root, o.Source.Caps
}

// noWrites runs fn and fails the test when any connection committed to the
// database meanwhile: PRAGMA data_version, on a connection of its own,
// changes when another connection commits.
func (e *env) noWrites(fn func()) {
	e.t.Helper()
	ctx := context.Background()
	conn, err := e.st.Reader().Conn(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close()
	version := func() int64 {
		var v int64
		if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v); err != nil {
			e.t.Fatal(err)
		}
		return v
	}
	before := version()
	fn()
	if after := version(); after != before {
		e.t.Errorf("the database was written: data_version %d -> %d", before, after)
	}
}

// opensOf counts the successful opens of the file at path since the
// recorder was last reset.
func (e *env) opensOf(path string) int {
	n := 0
	for _, p := range e.opened() {
		if p == path {
			n++
		}
	}
	return n
}

// readsOf returns the ReadAt calls of the file at path since the recorder
// was last reset.
func (e *env) readsOf(path string) []instrument.Call {
	var out []instrument.Call
	for _, c := range e.rec.Calls() {
		if c.Op == instrument.OpReadAt && string(joinPath(c.Path)) == path {
			out = append(out, c)
		}
	}
	return out
}

// HashEntry reads a 20 MiB file whole, in read_chunk_bytes chunks and one
// read confirming its end, and returns its SHA-256 and the lstat it matched;
// it writes nothing. A cancel stops it between chunks.
func TestHashEntryReadsWholeFile(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	data := randomBytes(20, 20<<20)
	root.File("filme.mkv", 0, fileTime).Content(data)
	e.scan("fotos")
	dir, caps := e.openRoot("fotos")
	r := e.row("fotos", "filme.mkv")
	ctx := context.Background()

	e.rec.Reset()
	var (
		sum  [32]byte
		info fsaccess.EntryInfo
		err  error
	)
	e.noWrites(func() { sum, info, err = HashEntry(ctx, dir, r, caps) })
	if err != nil {
		t.Fatal(err)
	}
	if sum != sha256.Sum256(data) {
		t.Error("the digest is not the file's SHA-256")
	}
	if info.Size != int64(len(data)) || !r.Ino.Valid || info.Ino != uint64(r.Ino.Int64) ||
		info.ModTime.UnixNano() != r.MtimeNs.Int64 {
		t.Errorf("info %+v; want the lstat of row %+v", info, r)
	}
	chunk := e.h.ReadChunkBytes
	reads := e.readsOf("filme.mkv")
	if want := int(int64(len(data))/chunk) + 1; len(reads) != want {
		t.Errorf("%d reads, want %d chunks and one at the end", len(reads), want)
	}
	for _, c := range reads {
		if int64(c.N) > chunk {
			t.Errorf("a read of %d bytes, over read_chunk_bytes %d", c.N, chunk)
		}
	}
	if n := e.opensOf("filme.mkv"); n != 1 {
		t.Errorf("%d opens, want 1", n)
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	e.rec.Reset()
	n := 0
	e.rec.SetBeforeCall(func(c instrument.Call) {
		if c.Op == instrument.OpReadAt {
			if n++; n == 3 {
				cancel()
			}
		}
	})
	_, _, err = HashEntry(cctx, dir, r, caps)
	e.rec.SetBeforeCall(nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled read: %v; want context.Canceled", err)
	}
	if got := len(e.readsOf("filme.mkv")); got != 3 {
		t.Errorf("%d reads after a cancel during the third, want 3", got)
	}
}

// A file modified after its indexing, changed while it is read (even with
// the same bytes), grown, or gone is invalid_entry_state.
func TestHashEntryChangedFile(t *testing.T) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	modified := root.File("a.jpg", 3<<20, fileTime).Seed(1)
	patched := root.File("b.jpg", 3<<20, fileTime).Seed(2)
	grown := root.File("c.jpg", 3<<20, fileTime).Seed(3)
	root.File("d.jpg", 3<<20, fileTime).Seed(4)
	e.scan("fotos")
	dir, caps := e.openRoot("fotos")
	rows := map[string]Row{}
	for _, p := range []string{"a.jpg", "b.jpg", "c.jpg", "d.jpg"} {
		rows[p] = e.row("fotos", p)
	}
	modified.ModTime(fileTime.Add(time.Hour))
	root.Remove("d.jpg")
	ctx := context.Background()

	check := func(path string) {
		t.Helper()
		if _, _, err := HashEntry(ctx, dir, rows[path], caps); domain.CodeOf(err) != domain.CodeInvalidEntryState {
			t.Errorf("%s: %v; want invalid_entry_state", path, err)
		}
	}
	check("a.jpg")
	check("d.jpg")
	// A write in place keeping the size and modification time: only the
	// fstat after the read differs.
	e.onRead("b.jpg", func() { patched.Patch(0, []byte{0}) })
	check("b.jpg")
	e.onRead("c.jpg", func() { grown.Size(3<<20 + 10) })
	check("c.jpg")
}

// tarEntry is one member of a test tar: a file with data, a folder (name
// ending in '/'), a hard link (link set), or a symlink (symlink set).
type tarEntry struct {
	name, link, symlink string
	data                []byte
}

func makeTarGz(t *testing.T, members ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: 0o644, ModTime: fileTime, Typeflag: tar.TypeReg, Size: int64(len(m.data))}
		switch {
		case m.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeLink, m.link, 0
		case m.symlink != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, m.symlink, 0
		case m.name[len(m.name)-1] == '/':
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
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

// digestOf is a member's digest and size.
type digestOf struct {
	sum  [32]byte
	size int64
}

func digestOfBytes(b []byte) digestOf { return digestOf{sha256.Sum256(b), int64(len(b))} }

// archiveFixture is a source with a zip and a tar.gz, scanned and listed:
// its root, and the contents of the archives' file members by archive path
// and member path.
func archiveFixture(t *testing.T) (*env, *synthfs.Node, map[string]map[string][]byte) {
	e := newEnv(t)
	root := e.disk("fotos", "/mnt/fotos", posix)
	video := randomBytes(9, 300<<10)
	photo := bytes.Repeat([]byte("jpeg "), 4000)
	text := randomBytes(10, 50<<10)
	root.File("coisas.zip", 0, fileTime).Content(makeZip(t, zipEntry{name: "video.avi", data: video, stored: true},
		zipEntry{name: "docs/"}, zipEntry{name: "fotos/foto.jpg", data: photo},
		zipEntry{name: "vazio.txt", data: nil}, zipEntry{name: "texto.txt", data: text}))
	note := []byte("a note inside a tar.gz")
	big := randomBytes(11, 200<<10)
	root.File("site.tar.gz", 0, fileTime).Content(makeTarGz(t, tarEntry{name: "a/"},
		tarEntry{name: "a/nota.txt", data: note}, tarEntry{name: "a/grande.bin", data: big},
		tarEntry{name: "a/ligacao.txt", link: "a/nota.txt"}, tarEntry{name: "a/atalho", symlink: "nota.txt"},
		tarEntry{name: "b/vazio", data: nil}))
	e.scan("fotos")
	e.hash("fotos")
	for _, p := range []string{"coisas.zip", "site.tar.gz"} {
		if n := e.count(`SELECT count(*) FROM archives WHERE entry_id = ? AND state = 'complete'`,
			int64(e.id("fotos", p))); n != 1 {
			t.Fatalf("%s is not listed complete", p)
		}
	}
	return e, root, map[string]map[string][]byte{
		"coisas.zip":  {"video.avi": video, "fotos/foto.jpg": photo, "vazio.txt": {}, "texto.txt": text},
		"site.tar.gz": {"a/nota.txt": note, "a/grande.bin": big, "a/ligacao.txt": note, "b/vazio": {}},
	}
}

// HashArchive gives every file member of a zip and of a tar.gz, hard link
// included, folders and symlinks left out, with its digest and size, from
// one open of the archive file; it writes nothing.
func TestHashArchiveReadsEachArchiveOnce(t *testing.T) {
	e, _, files := archiveFixture(t)
	ctx := context.Background()
	for path, members := range files {
		want := map[int64]digestOf{}
		for mpath, data := range members {
			want[int64(e.memberRef("fotos", path, mpath).Member)] = digestOfBytes(data)
		}
		got := map[int64]digestOf{}
		e.rec.Reset()
		var err error
		e.noWrites(func() {
			err = e.svc.HashArchive(ctx, e.st.Reader(), e.id("fotos", path), func(member int64, sum [32]byte,
				size int64) error {
				if _, dup := got[member]; dup {
					t.Errorf("%s: member %d given twice", path, member)
				}
				got[member] = digestOf{sum, size}
				return nil
			})
		})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s: members %v; want %v", path, got, want)
		}
		if opened := e.opened(); len(opened) != 1 || opened[0] != path {
			t.Errorf("%s: opened %q; want the archive once", path, opened)
		}
		for _, c := range e.readsOf(path) {
			if int64(c.N) > e.h.ReadChunkBytes {
				t.Errorf("%s: a read of %d bytes, over read_chunk_bytes", path, c.N)
			}
		}
	}

	// fn's error stops the calls and is returned.
	stop := errors.New("stop")
	calls := 0
	err := e.svc.HashArchive(ctx, e.st.Reader(), e.id("fotos", "coisas.zip"), func(int64, [32]byte, int64) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("fn failing: %v after %d calls; want its error after 1", err, calls)
	}
}

// HashArchive refuses an archive changed since its listing, changed while
// read, or not completely listed, and an entry that is no archive, before
// calling fn.
func TestHashArchiveRefuses(t *testing.T) {
	e, root, _ := archiveFixture(t)
	root.File("quebrado.zip", 0, fileTime).Content([]byte("not a zip at all, just text"))
	root.File("fotos.7z", 0, fileTime).Content([]byte("7z\xbc\xaf\x27\x1c"))
	root.File("nota.txt", 0, fileTime).Content([]byte("plain"))
	e.scan("fotos")
	e.hash("fotos")
	ctx := context.Background()
	never := func(int64, [32]byte, int64) error {
		t.Error("fn called for a refused archive")
		return nil
	}
	refuse := func(path string, code domain.ErrorCode) {
		t.Helper()
		err := e.svc.HashArchive(ctx, e.st.Reader(), e.id("fotos", path), never)
		if domain.CodeOf(err) != code {
			t.Errorf("%s: %v; want %s", path, err, code)
		}
	}
	refuse("quebrado.zip", domain.CodeInvalidEntryState)
	refuse("fotos.7z", domain.CodeNotFound)
	refuse("nota.txt", domain.CodeNotFound)

	// The same bytes written again while read: only the fstat differs.
	tgz := root.Child("site.tar.gz")
	e.onRead("site.tar.gz", func() { tgz.Patch(0, []byte{0x1f}) })
	refuse("site.tar.gz", domain.CodeInvalidEntryState)
	e.rec.SetBeforeCall(nil)

	root.Child("coisas.zip").ModTime(fileTime.Add(time.Hour))
	refuse("coisas.zip", domain.CodeInvalidEntryState)
}

// HashMember hashes one member of a zip, stored or deflated, and of a
// tar.gz, a hard link by the member it names, from one open of the archive;
// it refuses a folder, an unknown member, an entry, and a changed archive,
// and writes nothing.
func TestHashMember(t *testing.T) {
	e, root, files := archiveFixture(t)
	ctx := context.Background()
	for _, c := range []struct{ path, member string }{
		{"coisas.zip", "video.avi"}, {"coisas.zip", "fotos/foto.jpg"}, {"coisas.zip", "vazio.txt"},
		{"site.tar.gz", "a/grande.bin"}, {"site.tar.gz", "a/ligacao.txt"},
	} {
		e.rec.Reset()
		var (
			sum  [32]byte
			size int64
			err  error
		)
		e.noWrites(func() {
			err = e.st.Read(ctx, func(tx *sql.Tx) error {
				sum, size, err = e.svc.HashMember(ctx, tx, e.memberRef("fotos", c.path, c.member))
				return err
			})
		})
		if err != nil {
			t.Errorf("%s!%s: %v", c.path, c.member, err)
			continue
		}
		if got, want := (digestOf{sum, size}), digestOfBytes(files[c.path][c.member]); got != want {
			t.Errorf("%s!%s: %x, %d bytes; want %x, %d", c.path, c.member, got.sum, got.size, want.sum, want.size)
		}
		if opened := e.opened(); len(opened) != 1 || opened[0] != c.path {
			t.Errorf("%s!%s: opened %q; want the archive once", c.path, c.member, opened)
		}
	}

	refuse := func(what string, ref domain.Ref, code domain.ErrorCode) {
		t.Helper()
		if _, _, err := e.svc.HashMember(ctx, e.st.Reader(), ref); domain.CodeOf(err) != code {
			t.Errorf("%s: %v; want %s", what, err, code)
		}
	}
	refuse("a folder", e.memberRef("fotos", "coisas.zip", "docs"), domain.CodeInvalidEntryState)
	refuse("a symlink", e.memberRef("fotos", "site.tar.gz", "a/atalho"), domain.CodeInvalidEntryState)
	refuse("an unknown member", domain.Ref{Member: 999999}, domain.CodeNotFound)
	refuse("an entry", domain.Ref{Entry: e.id("fotos", "coisas.zip")}, domain.CodeInvalidRequest)
	root.Child("site.tar.gz").ModTime(fileTime.Add(time.Hour))
	refuse("a changed archive", e.memberRef("fotos", "site.tar.gz", "a/nota.txt"), domain.CodeInvalidEntryState)
}
