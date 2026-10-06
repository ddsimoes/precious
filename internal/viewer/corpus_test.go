package viewer_test

import (
	"bytes"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"precious/internal/corpus"
	"precious/internal/domain"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index/indextest"
)

// R1.13 The viewer shows the corpus files safely, at the HTTP level: the
// corpus's viewer fixtures under Midia/, indexed with the identity a scan
// reads, get the right type and decoding, seek, and never get a type that
// would run them as a page.
func TestR1_13ViewerShowsCorpusFilesSafely(t *testing.T) {
	e := corpusEnv(t)
	seed := e.seedMidia()
	id := func(name string) domain.EntryID { return seed.ID("Midia/" + name) }
	get := func(name, what string, header ...string) *httptest.ResponseRecorder {
		t.Helper()
		rec := e.get(id(name), what, header...)
		checkProtected(t, name+" "+what, rec) // every response carries nosniff
		if rec.Code != http.StatusOK && rec.Code != http.StatusPartialContent {
			t.Fatalf("%s %s: %d %s", name, what, rec.Code, rec.Body)
		}
		return rec
	}
	content := func(name, mime string) *httptest.ResponseRecorder {
		t.Helper()
		rec := get(name, "content")
		if got := rec.Header().Get("Content-Type"); got != mime {
			t.Errorf("%s: Content-Type %q, want %q", name, got, mime)
		}
		return rec
	}

	if rec := content("foto.jpg", "image/jpeg"); rec.Header().Get("Content-Security-Policy") != sandbox {
		t.Errorf("foto.jpg: Content-Security-Policy %q", rec.Header().Get("Content-Security-Policy"))
	} else if _, err := jpeg.Decode(rec.Body); err != nil {
		t.Errorf("foto.jpg: %v", err)
	}
	if rec := content("video.mp4", "video/mp4"); !bytes.Equal(rec.Body.Bytes()[4:8], []byte("ftyp")) {
		t.Error("video.mp4 is not served as an MP4")
	}
	content("musica.mp3", "audio/mpeg")
	for _, name := range []string{"video.mp4", "musica.mp3"} {
		full := get(name, "content").Body.Bytes()
		rec := get(name, "content", "Range", "bytes=100-199")
		if rec.Code != http.StatusPartialContent || !bytes.Equal(rec.Body.Bytes(), full[100:200]) {
			t.Errorf("%s: range gave %d with %d bytes", name, rec.Code, rec.Body.Len())
		}
	}
	if rec := content("documento.pdf", "application/pdf"); !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")) ||
		rec.Header().Get("Content-Security-Policy") != pdfCSP {
		t.Errorf("documento.pdf: %q, Content-Security-Policy %q", rec.Body.Bytes()[:min(8, rec.Body.Len())],
			rec.Header().Get("Content-Security-Policy"))
	}

	if got := decodeText(t, get("notas.md", "text")); got.Encoding != "UTF-8" || !got.Markdown ||
		!strings.HasPrefix(got.Text, "# Notas da mudança") {
		t.Errorf("notas.md: %+v", got)
	}
	if got := decodeText(t, get("script.py", "text")); got.Encoding != "UTF-8" || got.Markdown ||
		got.Language == nil || *got.Language != "python" || !strings.Contains(got.Text, "Renomeia as fotos da câmera pela data.") {
		t.Errorf("script.py: %+v", got)
	}
	if got := decodeText(t, get("carta_1252.txt", "text")); got.Encoding != "windows-1252" ||
		!strings.Contains(got.Text, "3 de março de 2010") ||
		!strings.Contains(got.Text, "“inesquecível”") || !strings.Contains(got.Text, "€ 50") {
		t.Errorf("carta_1252.txt: %+v", got)
	}

	if rec := content("pagina.html", "application/octet-stream"); !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("pagina.html: Content-Disposition %q", rec.Header().Get("Content-Disposition"))
	}
	if rec := content("desenho.svg", "image/svg+xml"); !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("desenho.svg: Content-Security-Policy %q", rec.Header().Get("Content-Security-Policy"))
	}
}

// corpusDisk is an env whose disk holds the regression corpus, with its
// ground truth.
type corpusDisk struct {
	*env
	gt corpus.GroundTruth
}

func corpusEnv(t *testing.T) *corpusDisk {
	t.Helper()
	fsys := synthfs.New()
	root, gt := corpus.BuildSynth(fsys, diskPoint, corpus.Corpus())
	return &corpusDisk{env: newEnvOn(t, fsys, root, ext4Caps), gt: gt}
}

// seedMidia indexes the files under Midia/ as the ground truth lists them,
// with their asserted file kinds and the identity facts of their lstat.
func (e *corpusDisk) seedMidia() *indextest.Seeded {
	e.t.Helper()
	var paths []string
	kinds := map[string]domain.FileKind{}
	for _, entry := range e.gt.Entries {
		raw, err := entry.RawPath()
		if err != nil {
			e.t.Fatal(err)
		}
		if p := string(raw); entry.Kind == domain.EntryFile && strings.HasPrefix(p, "Midia/") {
			paths = append(paths, p)
			kinds[p] = domain.FileKind(entry.FileKind)
		}
	}
	nodes := lstatNodes(e.t, e.fs, diskPoint, paths...)
	for i := range nodes {
		nodes[i].FileKind = kinds[nodes[i].Path]
	}
	return indextest.Seed(e.t, e.st, indextest.Tree{Source: diskID, Nodes: nodes})
}
