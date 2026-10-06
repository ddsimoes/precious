package viewer

import "testing"

// The edges of decode: characters cut by the cap are dropped, malformed
// input elsewhere becomes U+FFFD, and UTF-8 that is invalid only because the
// file really ends mid-character is Windows-1252.
func TestDecodeEdges(t *testing.T) {
	for _, c := range []struct {
		name      string
		in        string
		truncated bool
		enc, text string
	}{
		{"utf-8 cut by the cap", "abc\xc3", true, encUTF8, "abc"},
		{"utf-8 ending mid-character", "abc\xc3", false, encWindows1252, "abcÃ"},
		{"utf-8 bom cut by the cap", "\xef\xbb\xbfa\xe2\x82", true, encUTF8, "a"},
		{"utf-8 bom with invalid bytes", "\xef\xbb\xbfa\xffb", false, encUTF8, "a\uFFFDb"},
		{"utf-16le pair cut by the cap", "\xff\xfea\x00\x3d\xd8", true, encUTF16LE, "a"},
		{"utf-16le byte cut by the cap", "\xff\xfea\x00b", true, encUTF16LE, "a"},
		{"utf-16le odd final byte", "\xff\xfea\x00b", false, encUTF16LE, "a\uFFFD"},
		{"utf-16le unpaired surrogates", "\xff\xfe\x00\xdca\x00\x3d\xd8", false, encUTF16LE, "\uFFFDa\uFFFD"},
		{"windows-1252 undefined bytes", "\x81\x8d", false, encWindows1252, "\uFFFD\uFFFD"},
	} {
		enc, text := decode([]byte(c.in), c.truncated)
		if enc != c.enc || text != c.text {
			t.Errorf("%s: %s %q, want %s %q", c.name, enc, text, c.enc, c.text)
		}
	}
}

func TestExtOf(t *testing.T) {
	for name, want := range map[string]string{
		"foto.JPG": "jpg", "a.tar.GZ": "gz", ".bashrc": "", "LEIAME": "", "x.": "", "x.verylongext": "",
		"notas.Markdown": "markdown",
	} {
		if got := extOf([]byte(name)); got != want {
			t.Errorf("extOf(%q) = %q, want %q", name, got, want)
		}
	}
}
