package jobs

import (
	"errors"
	"fmt"

	"precious/internal/domain"
)

// StateCancelRequested is the displayed state of a non-terminal job whose
// cancellation was requested. It is never stored in jobs.state.
const StateCancelRequested domain.JobState = "cancel_requested"

// ErrIllegalTransition is returned when a change would move a job along an
// edge the lifecycle does not allow.
var ErrIllegalTransition = errors.New("jobs: illegal state transition")

// transitions lists every allowed edge of the durable lifecycle:
//
//	queued  -> running (claimed) | cancelled (cancel before start)
//	running -> succeeded | failed | cancelled | paused (handler outcome)
//	        -> queued (worker lost, lease expired, or interrupted by shutdown)
//	paused  -> queued (resume) | cancelled (cancel while no worker holds it)
//
// Terminal states have no outgoing edges.
var transitions = map[domain.JobState][]domain.JobState{
	domain.JobQueued:  {domain.JobRunning, domain.JobCancelled},
	domain.JobRunning: {domain.JobSucceeded, domain.JobFailed, domain.JobCancelled, domain.JobPaused, domain.JobQueued},
	domain.JobPaused:  {domain.JobQueued, domain.JobCancelled},
}

// CanTransition reports whether a job may move from one durable state to another.
func CanTransition(from, to domain.JobState) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

func checkTransition(from, to domain.JobState) error {
	if from == to || CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
}

// DisplayState is the state shown to clients: StateCancelRequested while a
// cancellation is pending on a non-terminal job, otherwise the durable state.
func DisplayState(state domain.JobState, cancelRequested bool) domain.JobState {
	if cancelRequested && !state.Terminal() {
		return StateCancelRequested
	}
	return state
}
