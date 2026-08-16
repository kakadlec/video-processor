package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// GetJobStatusInput identifies a job and the user requesting it.
type GetJobStatusInput struct {
	RequestingUserID domain.UserID
	JobID            string
}

// GetJobStatusResult describes the current observable state of a job.
type GetJobStatusResult struct {
	JobID       string
	Status      domain.JobStatus
	FrameCount  int
	ErrorReason string
	StorageKey  string
}

// GetJobStatus retrieves a job without revealing other users' jobs.
type GetJobStatus struct {
	jobs   domain.VideoJobRepository
	parser domain.VideoJobIDParser
}

// NewGetJobStatus wires the use case to its ports.
func NewGetJobStatus(
	jobs domain.VideoJobRepository,
	parser domain.VideoJobIDParser,
) *GetJobStatus {
	return &GetJobStatus{jobs: jobs, parser: parser}
}

// Execute parses the external ID, loads the job, and verifies ownership.
func (uc *GetJobStatus) Execute(
	ctx context.Context,
	input GetJobStatusInput,
) (GetJobStatusResult, error) {
	jobID, err := uc.parser.ParseVideoJobID(input.JobID)
	if err != nil {
		return GetJobStatusResult{}, err
	}

	job, err := uc.jobs.FindByID(ctx, jobID)
	if err != nil {
		return GetJobStatusResult{}, err
	}
	if !job.UserID().Equal(input.RequestingUserID) {
		return GetJobStatusResult{}, domain.ErrVideoJobNotFound
	}

	return GetJobStatusResult{
		JobID:       job.ID().String(),
		Status:      job.Status(),
		FrameCount:  job.FrameCount(),
		ErrorReason: job.ErrorReason(),
		StorageKey:  job.StorageKey().String(),
	}, nil
}
