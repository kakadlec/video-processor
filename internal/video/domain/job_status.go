package domain

// JobStatus is the lifecycle state of a VideoJob.
type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusQueued     JobStatus = "queued"
	JobStatusProcessing JobStatus = "processing"
	JobStatusCompleted  JobStatus = "completed"
	JobStatusFailed     JobStatus = "failed"
)

// CanTransitionTo reports whether next is a valid immediate successor.
func (status JobStatus) CanTransitionTo(next JobStatus) bool {
	switch status {
	case JobStatusPending:
		return next == JobStatusQueued
	case JobStatusQueued:
		return next == JobStatusProcessing
	case JobStatusProcessing:
		return next == JobStatusCompleted || next == JobStatusFailed
	default:
		return false
	}
}

func (status JobStatus) isValid() bool {
	switch status {
	case JobStatusPending, JobStatusQueued, JobStatusProcessing, JobStatusCompleted, JobStatusFailed:
		return true
	default:
		return false
	}
}
