package domain

import (
	"fmt"
	"strconv"
)

// MemberID identifies a member of a listed archive (archive_members.id). Like
// EntryID it is opaque, never derived from a path, and never reused.
type MemberID int64

// Ref addresses what the API can name: an entry, or a member of an archive
// entry (R2 design D7). Member != 0 marks a member; Entry is then the archive
// that holds it when known, and zero when the ref was parsed from "m<id>".
type Ref struct {
	Entry  EntryID
	Member MemberID
}

// IsMember reports whether the ref names an archive member.
func (r Ref) IsMember() bool { return r.Member != 0 }

// String renders the opaque API form: "123" for an entry, "m45" for a member.
func (r Ref) String() string {
	if r.Member != 0 {
		return "m" + strconv.FormatInt(int64(r.Member), 10)
	}
	return r.Entry.String()
}

// GoString keeps refs readable in test failure output.
func (r Ref) GoString() string {
	if r.Member != 0 {
		return fmt.Sprintf("Ref(m%d)", int64(r.Member))
	}
	return fmt.Sprintf("Ref(%d)", int64(r.Entry))
}

// ParseRef parses the form produced by Ref.String: a positive decimal entry ID
// ("123") or "m" followed by a positive decimal member ID ("m45"). Only the
// canonical form is accepted (no sign, no leading zero, lowercase "m");
// anything else is an invalid_request error.
func ParseRef(s string) (Ref, error) {
	digits, member := s, false
	if len(s) > 0 && s[0] == 'm' {
		digits, member = s[1:], true
	}
	n, ok := parsePositive(digits)
	if !ok {
		return Ref{}, Errorf(CodeInvalidRequest, "invalid ref %q", s)
	}
	if member {
		return Ref{Member: MemberID(n)}, nil
	}
	return Ref{Entry: EntryID(n)}, nil
}

// parsePositive accepts a canonical positive decimal int64.
func parsePositive(s string) (int64, bool) {
	if s == "" || s[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
}

// ContentState is what hashing knows about a present non-empty regular file
// or a file member (R2 design D3; file_content.state, archive_members.state).
type ContentState string

const (
	// ContentUniqueSize: no other copy has the same size, so it is unique.
	ContentUniqueSize ContentState = "unique_size"
	// ContentPending: its size is shared and it has not been read yet.
	ContentPending ContentState = "pending"
	// ContentSampled: its 64 KiB samples are distinct within its size.
	ContentSampled ContentState = "sampled"
	// ContentHashed: every byte was read; content_id holds its digest.
	ContentHashed ContentState = "hashed"
	// ContentChanged: the file changed while it was read.
	ContentChanged ContentState = "changed"
	// ContentUnreadable: a permission or I/O failure stopped the read.
	ContentUnreadable ContentState = "unreadable"
)

// ContentStates lists every content state in schema order.
var ContentStates = []ContentState{
	ContentUniqueSize, ContentPending, ContentSampled, ContentHashed, ContentChanged, ContentUnreadable,
}

// Valid reports whether s is a known content state.
func (s ContentState) Valid() bool {
	for _, v := range ContentStates {
		if s == v {
			return true
		}
	}
	return false
}

// DupFilter is a value of Search's repeatable duplicate filter (R2 design D15).
type DupFilter string

const (
	// DupCopies: the file has another copy anywhere.
	DupCopies DupFilter = "copies"
	// DupElsewhere: the file has a copy outside the `within` folder or on
	// another source. It requires `within`.
	DupElsewhere DupFilter = "elsewhere"
	// DupUnique: the file is known to have no other copy.
	DupUnique DupFilter = "unique"
	// DupUnchecked: the file's size is shared but its content is not known.
	DupUnchecked DupFilter = "unchecked"
)

// DupFilters lists every duplicate filter value.
var DupFilters = []DupFilter{DupCopies, DupElsewhere, DupUnique, DupUnchecked}

// Valid reports whether f is a known duplicate filter.
func (f DupFilter) Valid() bool {
	for _, v := range DupFilters {
		if f == v {
			return true
		}
	}
	return false
}
