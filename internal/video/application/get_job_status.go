package application

import (
	"context"

	"video-processor/internal/video/domain"
)

type GetJobStatusInput struct {
	RequestingUserID domain.UserID
	JobID            string
}

type GetJobStatusResult struct {
	JobID       string
	Status      domain.JobStatus
	FrameCount  int
	ErrorReason string
	StorageKey  string
}

type GetJobStatus struct {
	jobs   domain.VideoJobRepository
	parser domain.VideoJobIDParser
}

func NewGetJobStatus(jobs domain.VideoJobRepository, parser domain.VideoJobIDParser) *GetJobStatus {
	return &GetJobStatus{jobs: jobs, parser: parser}
}

func (uc *GetJobStatus) Execute(ctx context.Context, input GetJobStatusInput) (GetJobStatusResult, error) {
	id, err := uc.parser.ParseVideoJobID(input.JobID)
	if err != nil {
		return GetJobStatusResult{}, err
	}

	job, err := uc.jobs.FindByID(ctx, id)
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
