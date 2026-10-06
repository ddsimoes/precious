// Package domain holds the shared vocabulary of precious: identifiers,
// enumerations, stable error codes, and invariant-bearing helpers. It has no
// dependencies on storage, HTTP, filesystem access, or any provider SDK.
package domain

import (
	"fmt"
	"strconv"
)

// SourceID identifies a source: a slug made from its label when it is added,
// never reused (design D5).
type SourceID string

// EntryID is the stable application identifier of an entry (§6.2). It is
// never derived from a path and never reused.
type EntryID int64

// String renders the opaque decimal form exposed through the API.
func (id EntryID) String() string { return strconv.FormatInt(int64(id), 10) }

// ParseEntryID parses the opaque decimal form produced by EntryID.String. A
// malformed or non-positive ID is a not_found error.
func ParseEntryID(s string) (EntryID, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, Errorf(CodeNotFound, "invalid entry id %q", s)
	}
	return EntryID(n), nil
}

// GoString keeps IDs readable in test failure output.
func (id EntryID) GoString() string { return fmt.Sprintf("EntryID(%d)", int64(id)) }

// JobID identifies a durable job.
type JobID int64

// String renders the opaque decimal form exposed through the API.
func (id JobID) String() string { return strconv.FormatInt(int64(id), 10) }

// ParseJobID parses the opaque decimal form produced by JobID.String. A
// malformed or non-positive ID is a not_found error.
func ParseJobID(s string) (JobID, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, Errorf(CodeNotFound, "invalid job id %q", s)
	}
	return JobID(n), nil
}
