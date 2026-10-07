// Package archive reads the archives Precious opens (R2 design D1, D7; m4b
// D2–D6; ADR 0007): zip by random access, and tar, tar gzip, tar bzip2,
// gzip, and bzip2 as one sequential stream. Hashing uses it to list
// archives and hash their members, and the viewer to serve a member. It
// parses untrusted input under budgets, rejects member paths that leave the
// archive or collide, and unpacks in memory only: nothing is extracted to
// disk. Archives inside archives are not opened. It imports only the
// standard library and domain: no database and no filesystem.
package archive

import "precious/internal/domain"

// keptSuffixes may follow an archive extension: one of them is stripped
// before the extension is matched, so `backup.tgz.old` is a tar gzip.
var keptSuffixes = []string{".old", ".bak", ".orig"}

type extension struct {
	ext    string
	format domain.ArchiveFormat
}

// extensions are matched in order, so the tar variants come before the plain
// compression suffixes they end with.
var extensions = []extension{
	{".tar.gz", domain.ArchiveTarGzip},
	{".tgz", domain.ArchiveTarGzip},
	{".tar.bz2", domain.ArchiveTarBzip2},
	{".tbz2", domain.ArchiveTarBzip2},
	{".tbz", domain.ArchiveTarBzip2},
	{".tar", domain.ArchiveTar},
	{".zip", domain.ArchiveZip},
	{".gz", domain.ArchiveGzip},
	{".bz2", domain.ArchiveBzip2},
}

// Classify applies the name rule (m4b D2) to a raw file name: one trailing
// `.old`, `.bak`, or `.orig` is stripped, then the extension is matched
// ASCII case-insensitively. A name needs at least one byte before its
// extension. It reports whether the file is opened as an archive of the
// format, once its first bytes confirm it; any other file, a 7z or rar
// archive included, is a plain file, and the format is "".
func Classify(name []byte) (domain.ArchiveFormat, bool) {
	_, e, ok := match(name)
	if !ok {
		return "", false
	}
	return e.format, true
}

// MemberName returns the name of the single member of a gzip or bzip2
// archive (m4b D2): the file name without the stripped suffix and the
// compression extension, so `e.sql.gz` holds `e.sql`. It returns nil for a
// name that Classify does not make a gzip or bzip2 archive. The gzip
// header's stored name is never used: it is untrusted.
func MemberName(archiveName []byte) []byte {
	stem, e, ok := match(archiveName)
	if !ok || (e.format != domain.ArchiveGzip && e.format != domain.ArchiveBzip2) {
		return nil
	}
	return append([]byte(nil), stem...)
}

// match strips one kept suffix and finds the extension; stem is the name
// before the extension.
func match(name []byte) (stem []byte, e extension, ok bool) {
	for _, s := range keptSuffixes {
		if hasSuffixFold(name, s) && len(name) > len(s) {
			name = name[:len(name)-len(s)]
			break
		}
	}
	for _, x := range extensions {
		if hasSuffixFold(name, x.ext) && len(name) > len(x.ext) {
			return name[:len(name)-len(x.ext)], x, true
		}
	}
	return nil, extension{}, false
}

// hasSuffixFold reports whether name ends with the lower-case ASCII suffix,
// comparing ASCII letters case-insensitively and every other byte exactly.
func hasSuffixFold(name []byte, suffix string) bool {
	if len(name) < len(suffix) {
		return false
	}
	tail := name[len(name)-len(suffix):]
	for i := range len(suffix) {
		c := tail[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != suffix[i] {
			return false
		}
	}
	return true
}
