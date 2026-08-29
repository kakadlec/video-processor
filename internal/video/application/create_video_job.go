package application

import (
	"context"
	"time"

	"video-processor/internal/video/domain"
)

// CreateVideoJobInput carries the caller-supplied job creation fields.
// SourceKey names the already-stored upload this job will process and is
// optional: POST /api/video-jobs creates a job from a filename alone, with
// no object behind it. Such a job simply cannot be enqueued — see
// domain.VideoJob.Enqueue.
type CreateVideoJobInput struct {
	UserID           string
	OriginalFilename string
	SourceKey        string
}

// CreateVideoJobResult describes the newly created job.
type CreateVideoJobResult struct {
	JobID            string
	UserID           string
	OriginalFilename string
	Status           string
	CreatedAt        time.Time
}

// CreateVideoJob creates a new VideoJob in pending state and persists it. It
// depends only on domain ports, so it can be tested with fakes and is
// agnostic to the concrete ID scheme and storage engine.
type CreateVideoJob struct {
	jobs  domain.VideoJobRepository
	ids   domain.VideoJobIDGenerator
	clock Clock
}

// NewCreateVideoJob wires the CreateVideoJob use case to its ports.
func NewCreateVideoJob(jobs domain.VideoJobRepository, ids domain.VideoJobIDGenerator, clock Clock) *CreateVideoJob {
	return &CreateVideoJob{jobs: jobs, ids: ids, clock: clock}
}

// Execute runs the job creation use case.
func (uc *CreateVideoJob) Execute(ctx context.Context, input CreateVideoJobInput) (CreateVideoJobResult, error) {
	userID, err := domain.NewUserID(input.UserID)
	if err != nil {
		return CreateVideoJobResult{}, err
	}

	filename, err := domain.NewOriginalFilename(input.OriginalFilename)
	if err != nil {
		return CreateVideoJobResult{}, err
	}

	// Parsed only when present: NewStorageKey rejects the empty string, so
	// an unconditional parse here would reject every job created without a
	// source object.
	var sourceKey domain.StorageKey
	if input.SourceKey != "" {
		sourceKey, err = domain.NewStorageKey(input.SourceKey)
		if err != nil {
			return CreateVideoJobResult{}, err
		}
	}

	job, err := domain.NewVideoJob(uc.ids, userID, filename, sourceKey, uc.clock.Now())
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
		Status:           string(job.Status()),
		CreatedAt:        job.CreatedAt(),
	}, nil
}
