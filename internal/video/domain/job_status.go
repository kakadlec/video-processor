package domain

type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusQueued     JobStatus = "queued"
	JobStatusProcessing JobStatus = "processing"
	JobStatusCompleted  JobStatus = "completed"
	JobStatusFailed     JobStatus = "failed"
)

func (s JobStatus) CanTransitionTo(next JobStatus) bool {
	switch s {
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

func (s JobStatus) IsValid() bool {
	switch s {
	case JobStatusPending, JobStatusQueued, JobStatusProcessing, JobStatusCompleted, JobStatusFailed:
		return true
	default:
		return false
	}
}
