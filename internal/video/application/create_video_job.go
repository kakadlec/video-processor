package application

import (
	"context"
	"time"

	"video-processor/internal/video/domain"
)

type CreateVideoJobInput struct {
	UserID           domain.UserID
	OriginalFilename domain.OriginalFilename
}

type CreateVideoJobResult struct {
	JobID            string
	UserID           string
	OriginalFilename string
	Status           domain.JobStatus
	FrameCount       int
	ErrorReason      string
	CreatedAt        time.Time
}

type CreateVideoJob struct {
	jobs  domain.VideoJobRepository
	ids   domain.VideoJobIDGenerator
	clock Clock
}

func NewCreateVideoJob(jobs domain.VideoJobRepository, ids domain.VideoJobIDGenerator, clock Clock) *CreateVideoJob {
	return &CreateVideoJob{jobs: jobs, ids: ids, clock: clock}
}

func (uc *CreateVideoJob) Execute(ctx context.Context, input CreateVideoJobInput) (CreateVideoJobResult, error) {
	job, err := domain.NewVideoJob(uc.ids, input.UserID, input.OriginalFilename, uc.clock.Now())
	if err != nil {
		return CreateVideoJobResult{}, err
	}
	if err := uc.jobs.Create(ctx, job); err != nil {
		return CreateVideoJobResult{}, err
	}

	return CreateVideoJobResult{
		JobID:            job.ID().String(),
		UserID:           job.UserID().String(),
		OriginalFilename: job.OriginalFilename().String(),
		Status:           job.Status(),
		FrameCount:       job.FrameCount(),
		ErrorReason:      job.ErrorReason(),
		CreatedAt:        job.CreatedAt(),
	}, nil
}
