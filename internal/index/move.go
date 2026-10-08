package index

import "precious/internal/domain"

// The types below describe a step the organize executor has done on disk,
// for the functions that make the index follow it (r3 design D6). They are
// shared with internal/executor, which passes them through its Index
// interface.

// PostFacts are the lstat facts of an entry right after a step: renaming an
// entry changes its ctime, and changing a folder changes its mtime and ctime,
// so the index takes them from the disk instead of keeping stale ones.
type PostFacts struct {
	Dev, Ino         uint64
	MtimeNs, CtimeNs int64
}

// Move is a done rename: Entry, with everything below it, now sits in
// NewParent under NewName, inside the same source.
type Move struct {
	Source         domain.SourceID
	Entry          domain.EntryID
	NewParent      domain.EntryID
	NewName        []byte
	Facts          PostFacts // the moved entry
	OldParentFacts PostFacts // the folder it left
	NewParentFacts PostFacts // the folder it entered
}

// NewFolder is a done mkdir: an empty folder Name inside Parent.
type NewFolder struct {
	Source      domain.SourceID
	Parent      domain.EntryID
	Name        []byte
	Facts       PostFacts // the new folder
	ParentFacts PostFacts // the folder that holds it
}
