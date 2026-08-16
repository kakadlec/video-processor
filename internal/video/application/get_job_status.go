package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// GetJobStatusInput carries the caller-supplied job lookup fields.
type GetJobStatusInput struct {
	RequestingUserID string
	JobID            string
}

// GetJobStatusResult describes a job's current status.
type GetJobStatusResult struct {
	JobID       string
	Status      string
	FrameCount  int
	ErrorReason string
	StorageKey  string
}

// GetJobStatus retrieves a VideoJob's status, scoped to its owner. A
// nonexistent job and a job owned by a different user return the same
// not-found error, so a caller can never distinguish "doesn't exist" from
// "isn't yours" — mirroring main.go's existing artifact-ownership posture.
// A malformed JobID is rejected as invalid input before the repository is
// ever queried, since it reveals nothing about whether any job exists.
type GetJobStatus struct {
	jobs   domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewGetJobStatus wires the GetJobStatus use case to its ports.
func NewGetJobStatus(jobs domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *GetJobStatus {
	return &GetJobStatus{jobs: jobs, idsFor: idsFor}
}

// Execute runs the job status lookup use case.
func (uc *GetJobStatus) Execute(ctx context.Context, input GetJobStatusInput) (GetJobStatusResult, error) {
	jobID, err := uc.idsFor.ParseVideoJobID(input.JobID)
	if err != nil {
		return GetJobStatusResult{}, err
	}

	requestingUserID, err := domain.NewUserID(input.RequestingUserID)
	if err != nil {
		return GetJobStatusResult{}, err
	}

	job, err := uc.jobs.FindByID(ctx, jobID)
	if err != nil {
		return GetJobStatusResult{}, err
	}

	if !job.UserID().Equal(requestingUserID) {
		return GetJobStatusResult{}, domain.ErrVideoJobNotFound
	}

	return GetJobStatusResult{
		JobID:       job.ID().String(),
		Status:      string(job.Status()),
		FrameCount:  job.FrameCount(),
		ErrorReason: job.ErrorReason(),
		StorageKey:  job.StorageKey().String(),
	}, nil
}
