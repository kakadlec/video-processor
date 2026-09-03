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
// pending -> queued -> processing -> completed, and processing -> failed,
// plus one backwards edge, processing -> queued.
//
// That edge is the recovery path and it is the table's only exception. A job
// whose worker died mid-run is otherwise unreachable: the claim predicate
// names queued alone so it refuses the processing row, and the broker's
// redelivery arrives long before any lease can lapse, so nothing else can
// ever move it. Re-dispatching it is the only way back, and Requeue is the
// only transition that walks this edge.
//
// No other backwards transition exists and no state may be skipped.
// completed and failed remain terminal: nothing leaves them.
var validTransitions = map[JobStatus]map[JobStatus]bool{
	JobStatusPending:    {JobStatusQueued: true},
	JobStatusQueued:     {JobStatusProcessing: true},
	JobStatusProcessing: {JobStatusCompleted: true, JobStatusFailed: true, JobStatusQueued: true},
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
