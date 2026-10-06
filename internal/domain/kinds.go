package domain

// EntryKind is the observed type of a directory entry, including the special
// files that are recorded and never opened (§6.2).
type EntryKind string

const (
	EntryDirectory   EntryKind = "directory"
	EntryFile        EntryKind = "file"
	EntrySymlink     EntryKind = "symlink"
	EntryFIFO        EntryKind = "fifo"
	EntrySocket      EntryKind = "socket"
	EntryCharDevice  EntryKind = "char_device"
	EntryBlockDevice EntryKind = "block_device"
	EntryUnknown     EntryKind = "unknown"
)

// IsSpecial reports whether entries of this kind must never be opened.
func (k EntryKind) IsSpecial() bool {
	switch k {
	case EntryDirectory, EntryFile, EntrySymlink:
		return false
	default:
		return true
	}
}

// AccessOutcome classifies a failed filesystem access. A failure is never an
// empty or absent result unless its outcome is OutcomeAbsent.
type AccessOutcome string

const (
	OutcomeAbsent                   AccessOutcome = "absent"
	OutcomeUnreadable               AccessOutcome = "unreadable"
	OutcomeUnavailable              AccessOutcome = "unavailable"
	OutcomeChangedDuringObservation AccessOutcome = "changed_during_observation"
)

// JobState is the durable lifecycle state of a job. Cancellation requests are a
// separate flag; a job is shown as cancel_requested while that flag is set and
// the job has not yet reached a terminal state.
type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobPaused    JobState = "paused"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

// Terminal reports whether no further transitions are allowed.
func (s JobState) Terminal() bool {
	return s == JobSucceeded || s == JobFailed || s == JobCancelled
}

// Active reports whether the job still holds its place (for example, the single
// active scan per source).
func (s JobState) Active() bool {
	return s == JobQueued || s == JobRunning || s == JobPaused
}
