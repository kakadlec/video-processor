package domain

// JobStatus is a VideoJob's position in its processing lifecycle.
type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusQueued     JobStatus = "queued"
	JobStatusProcessing JobStatus = "processing"
	JobStatusCompleted  JobStatus = "completed"
	JobStatusFailed     JobStatus = "failed"
)

// validTransitions encodes the state machine's only allowed edges:
// pending -> queued -> processing -> completed, and processing -> failed.
// No backwards transitions, no skipping states.
var validTransitions = map[JobStatus]map[JobStatus]bool{
	JobStatusPending:    {JobStatusQueued: true},
	JobStatusQueued:     {JobStatusProcessing: true},
	JobStatusProcessing: {JobStatusCompleted: true, JobStatusFailed: true},
	JobStatusCompleted:  {},
	JobStatusFailed:     {},
}

// CanTransitionTo reports whether moving from s to next is a valid state
// machine transition. It is a pure function, independent of any VideoJob
// instance, so the transition table can be tested on its own.
func (s JobStatus) CanTransitionTo(next JobStatus) bool {
	return validTransitions[s][next]
}

// IsValid reports whether s is one of the five defined JobStatus values.
// RestoreVideoJob uses this to reject a corrupted or unrecognized status
// coming from storage.
func (s JobStatus) IsValid() bool {
	_, ok := validTransitions[s]
	return ok
}
