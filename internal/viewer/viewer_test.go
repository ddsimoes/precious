package viewer_test

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/unicode"

	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
)

const (
	sandbox = "sandbox; default-src 'none'"
	svgCSP  = "sandbox; default-src 'none'; style-src 'unsafe-inline'"
	pdfCSP  = "default-src 'none'; frame-ancestors 'self'"
	octet   = "application/octet-stream"
)

// Each row of the type table (design D12): the type comes from the
// extension, whatever the content, and anything else downloads.
func TestContentTypeTable(t *testing.T) {
	e, root := newEnv(t, ext4Caps)
	html := []byte("<!doctype html><script>alert(1)</script>")
	cases := []struct {
		name, mime, csp, disposition string
	}{
		{"foto.JPG", "image/jpeg", sandbox, ""},
		{"a.jpeg", "image/jpeg", sandbox, ""},
		{"b.png", "image/png", sandbox, ""},
		{"c.gif", "image/gif", sandbox, ""},
		{"d.webp", "image/webp", sandbox, ""},
		{"e.avif", "image/avif", sandbox, ""},
		{"f.bmp", "image/bmp", sandbox, ""},
		{"g.svg", "image/svg+xml", svgCSP, ""},
		{"h.mp4", "video/mp4", sandbox, ""},
		{"i.m4v", "video/mp4", sandbox, ""},
		{"j.webm", "video/webm", sandbox, ""},
		{"k.MOV", "video/quicktime", sandbox, ""},
		{"l.mp3", "audio/mpeg", sandbox, ""},
		{"m.m4a", "audio/mp4", sandbox, ""},
		{"n.aac", "audio/aac", sandbox, ""},
		{"o.ogg", "audio/ogg", sandbox, ""},
		{"p.opus", "audio/ogg", sandbox, ""},
		{"q.wav", "audio/wav", sandbox, ""},
		{"r.flac", "audio/flac", sandbox, ""},
		{"s.pdf", "application/pdf", pdfCSP, ""},
		{"Setup.exe", octet, sandbox, `attachment; filename=Setup.exe`},
		{"pagina.html", octet, sandbox, `attachment; filename=pagina.html`},
		{"dados.xml", octet, sandbox, `attachment; filename=dados.xml`},
		{"LEIAME", octet, sandbox, `attachment; filename=LEIAME`},
		{".jpg", octet, sandbox, `attachment; filename=.jpg`},
		{"or\xe7amento 2010.htm", octet, sandbox, `attachment; filename="or\\xE7amento 2010.htm"`},
	}
	m := root.Dir("m")
	paths := make([]string, len(cases))
	for i, c := range cases {
		// Every file holds HTML: the content never changes the type.
		m.File(c.name, 0, time.Time{}).Content(html)
		paths[i] = "m/" + c.name
	}
	seed := e.seed(paths...)
	for _, c := range cases {
		rec := e.get(seed.ID("m/"+c.name), "content")
		h := rec.Header()
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), html) {
			t.Errorf("%s: %d %q", c.name, rec.Code, rec.Body)
			continue
		}
		checkProtected(t, c.name, rec)
		if got := h.Get("Content-Type"); got != c.mime {
			t.Errorf("%s: Content-Type %q, want %q", c.name, got, c.mime)
		}
		if got := h.Get("Content-Security-Policy"); got != c.csp {
			t.Errorf("%s: Content-Security-Policy %q, want %q", c.name, got, c.csp)
		}
		if got := h.Get("Content-Disposition"); got != c.disposition {
			t.Errorf("%s: Content-Disposition %q, want %q", c.name, got, c.disposition)
		}
		if got := h.Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("%s: Accept-Ranges %q", c.name, got)
		}
	}
}

// Media seeks with a range request.
func TestContentRange(t *testing.T) {
	e, root := newEnv(t, ext4Caps)
	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	root.File("video.mp4", 0, time.Time{}).Content(data)
	seed := e.seed("video.mp4")

	rec := e.get(seed.ID("video.mp4"), "content", "Range", "bytes=1000-1999")
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range: %d %s", rec.Code, rec.Body)
	}
	checkProtected(t, "range", rec)
	if got := rec.Header().Get("Content-Range"); got != "bytes 1000-1999/5000" {
		t.Errorf("Content-Range %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), data[1000:2000]) {
		t.Errorf("range body has %d bytes, not data[1000:2000]", rec.Body.Len())
	}
}

func encode(t *testing.T, enc encoding.Encoding, s string) []byte {
	t.Helper()
	b, err := enc.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each decoding path of /text, the 1 MiB cap, and the language and Markdown
// hints.
func TestText(t *testing.T) {
	e, root := newEnv(t, ext4Caps)
	const phrase = "Meu orçamento “de 2010”: € 50 😀"
	ascii := "Meu or\xe7amento \x93de 2010\x94: \x80 50"
	// A 5 MiB log whose 'ç' straddles the cap: the cut character is dropped.
	big := bytes.Repeat([]byte("x"), 5<<20)
	copy(big[1<<20-1:], "ç")
	files := map[string][]byte{
		"bom8.txt":    append([]byte{0xEF, 0xBB, 0xBF}, phrase...),
		"utf16le.txt": encode(t, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), phrase),
		"utf16be.txt": encode(t, unicode.UTF16(unicode.BigEndian, unicode.UseBOM), phrase),
		"plain.txt":   []byte(phrase),
		"carta.txt":   []byte(ascii),
		"big.log":     big,
		"main.go":     []byte("package main\n"),
		"README.md":   []byte("# Leia-me\n"),
	}
	var paths []string
	for name, b := range files {
		root.File(name, 0, time.Time{}).Content(b)
		paths = append(paths, name)
	}
	seed := e.seed(paths...)
	text := func(name string) textResponse {
		rec := e.get(seed.ID(name), "text")
		checkProtected(t, name, rec)
		return decodeText(t, rec)
	}
	str := func(s *string) string {
		if s == nil {
			return "<nil>"
		}
		return *s
	}

	for name, want := range map[string]textResponse{
		"bom8.txt":    {Encoding: "UTF-8", Text: phrase},
		"utf16le.txt": {Encoding: "UTF-16LE", Text: phrase},
		"utf16be.txt": {Encoding: "UTF-16BE", Text: phrase},
		"plain.txt":   {Encoding: "UTF-8", Text: phrase},
		"carta.txt":   {Encoding: "windows-1252", Text: "Meu orçamento “de 2010”: € 50"},
	} {
		got := text(name)
		if got.Encoding != want.Encoding || got.Text != want.Text || got.Truncated || got.Language != nil || got.Markdown {
			t.Errorf("%s: %+v (language %s), want %+v", name, got, str(got.Language), want)
		}
	}

	got := text("big.log")
	if got.Encoding != "UTF-8" || !got.Truncated || got.Text != string(big[:1<<20-1]) {
		t.Errorf("big.log: encoding %s, truncated %v, %d bytes of text; want UTF-8, truncated, the first MiB less the cut 'ç'",
			got.Encoding, got.Truncated, len(got.Text))
	}
	if got := text("main.go"); str(got.Language) != "go" || got.Markdown || got.Text != "package main\n" {
		t.Errorf("main.go: %+v (language %s)", got, str(got.Language))
	}
	if got := text("README.md"); !got.Markdown || str(got.Language) != "markdown" {
		t.Errorf("README.md: %+v (language %s)", got, str(got.Language))
	}
}

// An entry that is not a present file matching its row is refused with
// invalid_entry_state; an unknown one is not_found; one on an unmounted
// volume is source_offline.
func TestErrors(t *testing.T) {
	e, root := newEnv(t, ext4Caps)
	mtime := time.Date(2010, 3, 3, 10, 0, 0, 0, time.UTC)
	content := []byte("conteúdo")
	names := []string{"ok.txt", "missing.txt", "grown.txt", "touched.txt", "replaced.txt", "deleted.txt", "locked.txt"}
	pasta := root.Dir("pasta")
	nodes := map[string]*synthfs.Node{}
	for _, n := range names {
		nodes[n] = pasta.File(n, 0, mtime).Content(content)
	}
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = "pasta/" + n
	}
	seed := e.seed(paths...)
	id := func(n string) domain.EntryID { return seed.ID("pasta/" + n) }

	if _, err := e.st.Writer().Exec(`UPDATE entries SET state = 'missing' WHERE id = ?`, int64(id("missing.txt"))); err != nil {
		t.Fatal(err)
	}
	nodes["grown.txt"].Content(append(content, '!'))
	nodes["touched.txt"].ModTime(mtime.Add(time.Second))
	pasta.Remove("replaced.txt")
	pasta.File("replaced.txt", 0, mtime).Content(content) // same size and time, another inode
	pasta.Remove("deleted.txt")
	nodes["locked.txt"].Unreadable()

	for _, what := range []string{"content", "text"} {
		if rec := e.get(id("ok.txt"), what); rec.Code != http.StatusOK {
			t.Errorf("ok.txt %s: %d %s", what, rec.Code, rec.Body)
		}
		checkError(t, "malformed "+what, e.getRaw("abc", what), http.StatusNotFound, domain.CodeNotFound)
		checkError(t, "unknown "+what, e.get(999999, what), http.StatusNotFound, domain.CodeNotFound)
		checkError(t, "folder "+what, e.get(seed.ID("pasta"), what), http.StatusConflict, domain.CodeInvalidEntryState)
		for _, n := range names[1:] {
			checkError(t, n+" "+what, e.get(id(n), what), http.StatusConflict, domain.CodeInvalidEntryState)
		}
	}

	e.fs.Unmount(e.dev)
	for _, what := range []string{"content", "text"} {
		checkError(t, "offline "+what, e.get(id("ok.txt"), what), http.StatusConflict, domain.CodeSourceOffline)
	}
}

// On a read-only FAT card, files stay viewable after the volume is mounted
// again with new inode numbers and an hour away (daylight saving), as a
// rescan would leave their rows unchanged (design D8).
func TestLocalTimeVolume(t *testing.T) {
	fat := fsaccess.Capabilities{Known: true, ReadOnly: true, NormalizationSensitive: true, LocalTime: true,
		TimeResolution: 2 * time.Second}
	e, root := newEnv(t, fat)
	root.Dir("DCIM").File("IMG_0001.JPG", 0, time.Date(2009, 7, 4, 15, 30, 10, 0, time.UTC)).Content([]byte("jpeg"))
	seed := e.seed("DCIM/IMG_0001.JPG")
	id := seed.ID("DCIM/IMG_0001.JPG")

	e.fs.Remount(e.dev)
	e.fs.SetTimeZone(e.dev, time.FixedZone("summer", 3600))
	rec := e.get(id, "content")
	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("after remount and an hour's shift: %d %s", rec.Code, rec.Body)
	}

	e.fs.SetTimeZone(e.dev, time.FixedZone("far", 2*3600))
	checkError(t, "two hours away", e.get(id, "content"), http.StatusConflict, domain.CodeInvalidEntryState)
}
