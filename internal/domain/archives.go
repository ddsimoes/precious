package domain

// ArchiveFormat is the format of an archive Precious opens (R2 design D7,
// ADR 0007). Its values are the archives.format column's.
type ArchiveFormat string

const (
	ArchiveZip      ArchiveFormat = "zip"
	ArchiveTar      ArchiveFormat = "tar"
	ArchiveTarGzip  ArchiveFormat = "tar_gzip"
	ArchiveTarBzip2 ArchiveFormat = "tar_bzip2"
	// ArchiveGzip and ArchiveBzip2 are single-file compressed archives: one
	// member, named after the file without its compression suffix.
	ArchiveGzip  ArchiveFormat = "gzip"
	ArchiveBzip2 ArchiveFormat = "bzip2"
)

// ArchiveFormats lists every ArchiveFormat.
var ArchiveFormats = []ArchiveFormat{ArchiveZip, ArchiveTar, ArchiveTarGzip, ArchiveTarBzip2, ArchiveGzip, ArchiveBzip2}

// Valid reports whether f is a known format.
func (f ArchiveFormat) Valid() bool {
	for _, v := range ArchiveFormats {
		if f == v {
			return true
		}
	}
	return false
}

// Streamed reports whether f is read once from start to end, hashing every
// member in that pass (R2 design D7). Only zip is read by random access.
func (f ArchiveFormat) Streamed() bool { return f.Valid() && f != ArchiveZip }

// UnsupportedArchiveExtensions are the extensions, in lower case and without
// the dot, of archive formats that Precious recognizes but does not open
// (r2b design D10): the detail says why such a file has no listing. None of
// them is an extension the archive package opens.
var UnsupportedArchiveExtensions = []string{
	"7z", "rar", "xz", "txz", "lz", "lzma", "zst", "z", "cab", "arj", "lzh", "lha", "ace", "cpio", "jar", "war",
}

// UnsupportedArchive reports whether a raw file name ends with one of the
// UnsupportedArchiveExtensions, comparing ASCII letters case-insensitively.
// A name needs at least one byte before the dot.
func UnsupportedArchive(name []byte) bool {
	dot := -1
	for i := len(name) - 1; i > 0; i-- {
		if name[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 1 {
		return false
	}
	ext := name[dot+1:]
	for _, u := range UnsupportedArchiveExtensions {
		if len(ext) != len(u) {
			continue
		}
		match := true
		for i := range len(u) {
			c := ext[i]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != u[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ArchiveState is the state of an archive's listing (R2 design D6, D7). Its
// values are the archives.state column's.
type ArchiveState string

const (
	// ArchiveListing: the listing is being written in batches; readers
	// ignore it.
	ArchiveListing ArchiveState = "listing"
	// ArchiveComplete: every member listed (and, for streamed formats,
	// hashed).
	ArchiveComplete ArchiveState = "complete"
	// ArchivePartial: a budget stopped the reading; the archive has no
	// members.
	ArchivePartial ArchiveState = "partial"
	// ArchiveRejected: a member path leaves the archive or collides.
	ArchiveRejected    ArchiveState = "rejected"
	ArchiveEncrypted   ArchiveState = "encrypted"
	ArchiveCorrupt     ArchiveState = "corrupt"
	ArchiveUnsupported ArchiveState = "unsupported"
	// ArchiveChanged: the file changed while it was read; nothing is kept.
	ArchiveChanged ArchiveState = "changed"
	// ArchiveUnreadable: the file could not be read.
	ArchiveUnreadable ArchiveState = "unreadable"
)

// ArchiveStates lists every ArchiveState.
var ArchiveStates = []ArchiveState{ArchiveListing, ArchiveComplete, ArchivePartial, ArchiveRejected,
	ArchiveEncrypted, ArchiveCorrupt, ArchiveUnsupported, ArchiveChanged, ArchiveUnreadable}

// Valid reports whether s is a known state.
func (s ArchiveState) Valid() bool {
	for _, v := range ArchiveStates {
		if s == v {
			return true
		}
	}
	return false
}

// MemberKind is the kind of one archive member (R2 design D7). Its values
// are the archive_members.kind column's. A tar hard link is a file member
// that names its target.
type MemberKind string

const (
	MemberDirectory MemberKind = "directory"
	MemberFile      MemberKind = "file"
	MemberSymlink   MemberKind = "symlink"
	// MemberSpecial is a device, FIFO, or any other member type.
	MemberSpecial MemberKind = "special"
)

// MemberKinds lists every MemberKind.
var MemberKinds = []MemberKind{MemberDirectory, MemberFile, MemberSymlink, MemberSpecial}

// Valid reports whether k is a known kind.
func (k MemberKind) Valid() bool {
	for _, v := range MemberKinds {
		if k == v {
			return true
		}
	}
	return false
}
