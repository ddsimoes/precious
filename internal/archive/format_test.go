package archive

import (
	"testing"

	"precious/internal/domain"
)

// The name rule (m4b D2) and the spec scenario "Other formats are plain
// files". The file is opened exactly when the format is not "".
func TestClassify(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		format domain.ArchiveFormat
		member string // MemberName; "" means nil
	}{
		// Formats opened and not opened.
		{"a.zip", domain.ArchiveZip, ""},
		{"b.tar.gz", domain.ArchiveTarGzip, ""},
		{"c.tgz.old", domain.ArchiveTarGzip, ""},
		{"d.tar.bz2", domain.ArchiveTarBzip2, ""},
		{"e.sql.gz", domain.ArchiveGzip, "e.sql"},
		{"f.7z", "", ""},
		{"g.rar", "", ""},
		{"h.jar", "", ""},
		{"i.docx", "", ""},
		// The other extensions and suffixes.
		{"plain.tar", domain.ArchiveTar, ""},
		{"x.tbz", domain.ArchiveTarBzip2, ""},
		{"x.tbz2", domain.ArchiveTarBzip2, ""},
		{"notes.txt.bz2", domain.ArchiveBzip2, "notes.txt"},
		{"dump.sql.gz.bak", domain.ArchiveGzip, "dump.sql"},
		{"site.zip.orig", domain.ArchiveZip, ""},
		{"x.tar.xz", "", ""},
		{"x.txz", "", ""},
		{"x.tar.zst", "", ""},
		{"x.tzst", "", ""},
		{"x.zst", "", ""},
		// Case-insensitive extensions keep the member name's own case.
		{"BACKUP.TAR.GZ", domain.ArchiveTarGzip, ""},
		{"Photos.ZIP", domain.ArchiveZip, ""},
		{"Log.TXT.GZ", domain.ArchiveGzip, "Log.TXT"},
		{"y.TGZ.OLD", domain.ArchiveTarGzip, ""},
		// Only one kept suffix is stripped, and only before an extension.
		{"x.zip.old.bak", "", ""},
		{"x.old", "", ""},
		{"x.zip.part", "", ""},
		{"x.zipx", "", ""},
		// A name needs a byte before its extension.
		{".zip", "", ""},
		{".gz", "", ""},
		{".old", "", ""},
		{"", "", ""},
		// Raw bytes other than ASCII letters compare exactly.
		{"\xff\xfe.zip", domain.ArchiveZip, ""},
		{"\xe9t\xe9.gz", domain.ArchiveGzip, "\xe9t\xe9"},
	} {
		format, ok := Classify([]byte(c.name))
		if format != c.format || ok != (c.format != "") {
			t.Errorf("Classify(%q) = %q, %v; want %q, %v", c.name, format, ok, c.format, c.format != "")
		}
		got := MemberName([]byte(c.name))
		if (got == nil) != (c.member == "") || string(got) != c.member {
			t.Errorf("MemberName(%q) = %q, want %q", c.name, got, c.member)
		}
	}
}

// MemberName returns a copy: changing it leaves the caller's name intact.
func TestMemberNameCopies(t *testing.T) {
	t.Parallel()
	name := []byte("e.sql.gz")
	m := MemberName(name)
	m[0] = 'X'
	if string(name) != "e.sql.gz" {
		t.Fatalf("name changed to %q", name)
	}
}

// The formats domain lists as not opened (r2b design D10) are never opened.
func TestUnsupportedAreNotOpened(t *testing.T) {
	t.Parallel()
	for _, ext := range domain.UnsupportedArchiveExtensions {
		name := []byte("backup." + ext)
		if f, ok := Classify(name); ok {
			t.Errorf("%s is opened as %q and listed as unsupported", name, f)
		}
		if !domain.UnsupportedArchive(name) {
			t.Errorf("%s is not unsupported", name)
		}
	}
}
