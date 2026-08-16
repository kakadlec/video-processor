package application

import (
	"context"
	"time"

	"video-processor/internal/video/domain"
)

// CreateVideoJobInput carries the validated job owner and original filename.
type CreateVideoJobInput struct {
	UserID           domain.UserID
	OriginalFilename domain.OriginalFilename
}

// CreateVideoJobResult describes the newly persisted job.
type CreateVideoJobResult struct {
	JobID            string
	UserID           string
	OriginalFilename string
	Status           domain.JobStatus
	FrameCount       int
	ErrorReason      string
	StorageKey       string
	CreatedAt        time.Time
}

// CreateVideoJob creates and persists a pending video job.
type CreateVideoJob struct {
	jobs  domain.VideoJobRepository
	ids   domain.VideoJobIDGenerator
	clock Clock
}

// NewCreateVideoJob wires the use case to its ports.
func NewCreateVideoJob(
	jobs domain.VideoJobRepository,
	ids domain.VideoJobIDGenerator,
	clock Clock,
) *CreateVideoJob {
	return &CreateVideoJob{jobs: jobs, ids: ids, clock: clock}
}

// Execute creates a pending job and persists it before reporting success.
func (uc *CreateVideoJob) Execute(
	ctx context.Context,
	input CreateVideoJobInput,
) (CreateVideoJobResult, error) {
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
		StorageKey:       job.StorageKey().String(),
		CreatedAt:        job.CreatedAt(),
	}, nil
}
