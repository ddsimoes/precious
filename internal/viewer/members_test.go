package viewer_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index/indextest"
)

// contentDisk is the corpus disk indexed as a scan and a complete hashing
// run leave it: every file with its lstat facts, and every archive of the
// ground truth listed (SeedContent).
type contentDisk struct {
	*corpusDisk
	seed *indextest.Seeded
	arcs map[string]*indextest.SeededArchive
}

func newContentDisk(t *testing.T) *contentDisk {
	t.Helper()
	e := corpusEnv(t)
	nodes := indextest.CorpusNodes(t, e.gt)
	root, err := e.fs.OpenRoot(diskPoint)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := range nodes {
		if nodes[i].Kind == domain.EntryFile {
			info := lstatPath(t, root, nodes[i].Path)
			nodes[i].Size, nodes[i].MTime, nodes[i].Lstat = 0, time.Time{}, &info
		}
	}
	seed := indextest.Seed(t, e.st, indextest.Tree{Source: diskID, Nodes: nodes})
	return &contentDisk{corpusDisk: e, seed: seed, arcs: seed.SeedContent(e.st, e.gt)}
}

// member is the ref of a member of a corpus archive.
func (e *contentDisk) member(archive, path string) string {
	e.t.Helper()
	a, ok := e.arcs[archive]
	if !ok {
		e.t.Fatalf("no archive %q", archive)
	}
	return domain.Ref{Member: a.Member(path)}.String()
}

// readSource reads the file at p below the disk's root.
func readSource(t *testing.T, fsys fsaccess.FS, p string) []byte {
	t.Helper()
	root, err := fsys.OpenRoot(diskPoint)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dir := root
	names := strings.Split(p, "/")
	for _, n := range names[:len(names)-1] {
		info, err := dir.Lstat([]byte(n))
		if err != nil {
			t.Fatal(err)
		}
		next, err := dir.OpenDir([]byte(n), info)
		if err != nil {
			t.Fatal(err)
		}
		if dir != root {
			dir.Close()
		}
		dir = next
	}
	if dir != root {
		defer dir.Close()
	}
	name := []byte(names[len(names)-1])
	info, err := dir.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := dir.OpenFile(name, info)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, info.Size)
	if _, err := f.ReadAt(b, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return b
}

// The viewer serves archive members (R2 design D17): a JPEG member of a zip
// with its type and the sandbox policy and with ranges, a range of the
// stored video in Midia/videos.zip, a member of a streamed tar.gz whole and
// without ranges, and the text of a deflated member and of a gzip file's
// member. A member folder is invalid_entry_state; an unknown member is
// not_found.
func TestMembersServed(t *testing.T) {
	e := newContentDisk(t)

	photo := e.member("Downloads/fotos_2005_do_pendrive.zip", "Carnaval/DSC01001.JPG")
	rec := e.getRaw(photo, "content")
	checkProtected(t, "photo member", rec)
	want := readSource(t, e.fs, "Downloads/fotos_2005_do_pendrive/Carnaval/DSC01001.JPG")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" ||
		rec.Header().Get("Content-Security-Policy") != sandbox || rec.Header().Get("Content-Disposition") != "" ||
		rec.Header().Get("Accept-Ranges") != "bytes" || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("photo member: %d %v, %d bytes", rec.Code, rec.Header(), rec.Body.Len())
	}

	video := e.member("Midia/videos.zip", "ferias/video.mp4")
	full := readSource(t, e.fs, "Midia/video.mp4")
	rec = e.getRaw(video, "content", "Range", "bytes=100-199")
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Type") != "video/mp4" ||
		!bytes.Equal(rec.Body.Bytes(), full[100:200]) {
		t.Errorf("video range: %d %v, %d bytes", rec.Code, rec.Header(), rec.Body.Len())
	}

	php := e.member("Projetos/site_antigo_2006.tar.gz", "index.php")
	rec = e.getRaw(php, "content", "Range", "bytes=0-9")
	if rec.Code != http.StatusOK || rec.Header().Get("Accept-Ranges") != "" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") ||
		!bytes.Equal(rec.Body.Bytes(), readSource(t, e.fs, "Projetos/site_antigo/index.php")) {
		t.Errorf("tar.gz member: %d %v, %q", rec.Code, rec.Header(), rec.Body.Bytes())
	}

	leia := decodeText(t, e.getRaw(e.member("Midia/videos.zip", "ferias/leia-me.txt"), "text"))
	if leia.Encoding != "UTF-8" || leia.Truncated || leia.Text == "" {
		t.Errorf("leia-me.txt: %+v", leia)
	}
	notas := decodeText(t, e.getRaw(e.member("Documentos/notas_2007.txt.gz", "notas_2007.txt"), "text"))
	if notas.Text == "" || notas.Truncated {
		t.Errorf("notas_2007.txt: %+v", notas)
	}

	checkError(t, "member folder", e.getRaw(e.member("Midia/videos.zip", "ferias"), "content"),
		http.StatusConflict, domain.CodeInvalidEntryState)
	checkError(t, "unknown member", e.getRaw("m999999", "content"), http.StatusNotFound, domain.CodeNotFound)
	checkError(t, "malformed member", e.getRaw("m01", "text"), http.StatusNotFound, domain.CodeNotFound)
}

// An HTML member is only ever saved: it is served as an attachment of
// type application/octet-stream. A changed archive is refused with
// invalid_entry_state until a rescan.
func TestMemberHTMLAndChangedArchive(t *testing.T) {
	e, root := newEnv(t, ext4Caps)
	page := []byte("<html><script>alert(1)</script></html>")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: "site/pagina.html", Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(page); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2011, 5, 5, 10, 0, 0, 0, time.UTC)
	node := root.Dir("web").File("site.zip", 0, mtime).Content(buf.Bytes())
	seed := e.seed("web/site.zip")
	locator := 0
	a := seed.SeedArchive(e.st, "web/site.zip", indextest.Archive{Format: domain.ArchiveZip, Members: []indextest.Member{
		{Path: "site/pagina.html", Size: int64(len(page)), Locator: &locator,
			Content: indextest.Content{State: domain.ContentUniqueSize}},
	}})
	ref := domain.Ref{Member: a.Member("site/pagina.html")}.String()

	rec := e.getRaw(ref, "content")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/octet-stream" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename=pagina.html` ||
		rec.Header().Get("Content-Security-Policy") != sandbox || !bytes.Equal(rec.Body.Bytes(), page) {
		t.Errorf("html member: %d %v %q", rec.Code, rec.Header(), rec.Body.Bytes())
	}

	node.ModTime(mtime.Add(time.Minute))
	for _, what := range []string{"content", "text"} {
		checkError(t, "changed archive "+what, e.getRaw(ref, what), http.StatusConflict, domain.CodeInvalidEntryState)
	}
}

// R2.8 Viewing a member touches no disk: the viewer serves the photo inside
// Downloads/fotos_2005_do_pendrive.zip, its content and its range, and the
// state directory, TMPDIR, and the source list exactly the same files
// before and after.
func TestR2_8ViewingAMemberWritesNothing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	e := newContentDisk(t)
	stateDir := filepath.Dir(e.st.Path())
	photo := e.member("Downloads/fotos_2005_do_pendrive.zip", "Carnaval/DSC01001.JPG")

	snapshot := func() []string {
		var out []string
		for _, dir := range []string{stateDir, tmp} {
			err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				out = append(out, "local:"+p)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		return append(out, listSource(t, e.fs)...)
	}
	before := snapshot()
	want := readSource(t, e.fs, "Downloads/fotos_2005_do_pendrive/Carnaval/DSC01001.JPG")
	for _, header := range [][]string{nil, {"Range", "bytes=10-99"}} {
		rec := e.getRaw(photo, "content", header...)
		if rec.Code != http.StatusOK && rec.Code != http.StatusPartialContent {
			t.Fatalf("photo: %d %s", rec.Code, rec.Body)
		}
		if header == nil && !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("photo: %d bytes, want the unpacked copy's %d", rec.Body.Len(), len(want))
		}
	}
	after := snapshot()
	if !slices.Equal(before, after) {
		t.Errorf("files changed:\nbefore %q\nafter  %q", before, after)
	}
	if !slices.ContainsFunc(before, func(s string) bool { return strings.Contains(s, "/fotos_2005_do_pendrive.zip file ") }) {
		t.Errorf("the source listing lacks the archive: %q", before)
	}
}

// listSource lists every entry below the disk's root with its kind, size,
// and modification time; unreadable folders as such.
func listSource(t *testing.T, fsys fsaccess.FS) []string {
	t.Helper()
	root, err := fsys.OpenRoot(diskPoint)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var out []string
	var walk func(dir fsaccess.Dir, prefix string)
	walk = func(dir fsaccess.Dir, prefix string) {
		for {
			batch, err := dir.ReadBatch(256)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				out = append(out, prefix+" unreadable")
				return
			}
			for _, de := range batch {
				info, err := dir.Lstat(de.Name)
				if err != nil {
					t.Fatal(err)
				}
				p := prefix + "/" + string(de.Name)
				out = append(out, fmt.Sprintf("%s %s %d %d", p, info.Kind, info.Size, info.ModTime.UnixNano()))
				if info.Kind == domain.EntryDirectory {
					sub, err := dir.OpenDir(de.Name, info)
					if err != nil {
						out = append(out, p+" unopened")
						continue
					}
					walk(sub, p)
					sub.Close()
				}
			}
		}
	}
	walk(root, "")
	slices.Sort(out)
	return out
}
