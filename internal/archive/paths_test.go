package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"precious/internal/domain"
)

// m4b D4: a path splits on '/' only, empty and "." components are
// dropped, and a ".." component or a leading '/' leaves the archive.
func TestCleanPath(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		raw    string
		comps  string // joined by '|'
		leaves bool
		joined string // as a Stop names it
	}{
		{"a/b", "a|b", false, "a/b"},
		{"a//b/./c/", "a|b|c", false, "a/b/c"},
		{"", "", false, ""},
		{".", "", false, ""},
		{"./", "", false, ""},
		{"/", "", true, "/"},
		{"//a/./b", "a|b", true, "/a/b"},
		{"a/../b", "a|..|b", true, "a/../b"},
		{"..", "..", true, ".."},
		{"..a/b..", "..a|b..", false, "..a/b.."},
		{`a\b/c\`, `a\b|c\`, false, `a\b/c\`},
		{"\xff/\x80\xfe", "\xff|\x80\xfe", false, "\xff/\x80\xfe"},
	} {
		comps, abs, leaves := cleanPath([]byte(c.raw))
		if got := string(bytes.Join(comps, []byte("|"))); got != c.comps || leaves != c.leaves {
			t.Errorf("cleanPath(%q) = %q, leaves %v; want %q, %v", c.raw, got, leaves, c.comps, c.leaves)
		}
		if got := joinPath(comps, abs); string(got) != c.joined || got == nil {
			t.Errorf("joinPath of %q = %q, want %q", c.raw, got, c.joined)
		}
	}
}

// m4b D4: where GODEBUG makes archive/tar and archive/zip report
// ErrInsecurePath, their values are used and the path rules apply: a
// backslash name is listed, and a traversal is rejected naming its member.
func TestInsecurePathGODEBUG(t *testing.T) {
	t.Setenv("GODEBUG", "tarinsecurepath=0,zipinsecurepath=0")
	archive := zipOf(t, func(w *zip.Writer) { zipFile(t, w, `dir\file.txt`, "x") })
	z, err := OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	if err != nil || len(z.Members()) != 1 {
		t.Fatalf("backslash zip: %v", err)
	}
	archive = zipOf(t, func(w *zip.Writer) { zipFile(t, w, "../evil", "x") })
	_, err = OpenZip(bytes.NewReader(archive), int64(len(archive)), limits)
	checkStop(t, err, domain.ArchiveRejected, detailLeaves, []byte("../evil"))

	tarred := tarEntries(t, tarEntry{"ok.txt", tar.TypeReg, ""}, tarEntry{"../evil", tar.TypeReg, ""})
	got, err := collect(t, domain.ArchiveTar, "x.tar", tarred, limits)
	checkStop(t, err, domain.ArchiveRejected, detailLeaves, []byte("../evil"))
	if len(got) != 1 {
		t.Fatalf("visited %d members before the traversal, want 1", len(got))
	}
	if !strings.Contains(err.Error(), "../evil") {
		t.Errorf("Stop text %q does not name the member", err)
	}
}
