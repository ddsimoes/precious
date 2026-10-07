package archive

import (
	"fmt"
	"time"

	"precious/internal/domain"
)

// Member is one member of an archive, as found.
type Member struct {
	// Path holds the cleaned components (m4b D4), each its raw bytes.
	Path [][]byte
	// Kind is never a hard link: a tar hard link is a file member that
	// names its target in LinkTo.
	Kind domain.MemberKind
	// Size is the unpacked size of a file member (a hard link's is its
	// target's), 0 for every other kind, and -1 for the member of a gzip or
	// bzip2 archive, whose size is known only once read.
	Size int64
	// Mtime is zero when the archive records none.
	Mtime time.Time
	// LinkText is a symlink's text, as stored, never followed.
	LinkText []byte
	// Index is a zip member's central-directory index, -1 otherwise.
	Index int
	// Stored reports a zip file member kept with method store: its packed
	// bytes are its content, so Zip.Section serves them with ranges (R2
	// design D17).
	Stored bool
	// LinkTo is a tar hard link's target: the cleaned path of an earlier
	// file member, or of an earlier hard link, whose content it has. It is
	// nil for every other member.
	LinkTo [][]byte
}

// Stop ends an opening with a state other than complete (m4b D5, D6).
type Stop struct {
	State domain.ArchiveState
	// Detail is one of the fixed texts below, so pages and tests may rely
	// on it; a format error carries the decoder's error text.
	Detail string
	// Member is the offending member's cleaned path joined by '/', with a
	// leading '/' when its raw path is absolute, or nil when no member is
	// involved.
	Member []byte
}

func (s *Stop) Error() string {
	if s.Member == nil {
		return fmt.Sprintf("archive %s: %s", s.State, s.Detail)
	}
	return fmt.Sprintf("archive %s: %s: member %q", s.State, s.Detail, s.Member)
}

// The fixed Stop details.
const (
	// partial
	detailEntries = "entry budget"
	detailBytes   = "unpacked byte budget"
	detailRatio   = "ratio budget"
	detailTime    = "time budget"
	// rejected
	detailLeaves     = "member path leaves the archive"
	detailSharedPath = "two members share a path"
	detailFileFolder = "a member is both a file and a folder"
	// encrypted
	detailEncrypted = "member is encrypted"
	// unsupported; also "not a <format> archive" and
	// "compression method <n> is not supported"
	detailMultiDisk = "multi-disk zip"
	// corrupt; also "<format> format error: <error text>"
	detailChecksum = "checksum mismatch"
	detailSize     = "size mismatch"
	detailEOF      = "unexpected end of data"
	detailHardlink = "hard link to an unknown member"
)

func stop(state domain.ArchiveState, detail string, member []byte) *Stop {
	return &Stop{State: state, Detail: detail, Member: member}
}

func notFormat(f domain.ArchiveFormat) *Stop {
	return stop(domain.ArchiveUnsupported, fmt.Sprintf("not a %s archive", f), nil)
}

func formatError(f domain.ArchiveFormat, err error, member []byte) *Stop {
	return stop(domain.ArchiveCorrupt, fmt.Sprintf("%s format error: %v", f, err), member)
}
