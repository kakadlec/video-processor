package application

import (
	"context"
	"fmt"
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
	// ContentHash is the hex-encoded SHA-256 of the uploaded bytes, and is
	// optional for the same reason SourceKey is: a job created from a
	// filename alone has no bytes to hash. It is persisted so a later
	// component holding only the job can rebuild the IdempotencyKey the
	// submitting request derived from it.
	ContentHash string
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
//
// It carries no Clock: CreatedAt is minted by PostgreSQL when jobs.Create
// persists the row, not by this use case, so there is nothing here for a
// clock port to supply.
type CreateVideoJob struct {
	jobs domain.VideoJobRepository
	ids  domain.VideoJobIDGenerator
}

// NewCreateVideoJob wires the CreateVideoJob use case to its ports.
func NewCreateVideoJob(jobs domain.VideoJobRepository, ids domain.VideoJobIDGenerator) *CreateVideoJob {
	return &CreateVideoJob{jobs: jobs, ids: ids}
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

	job, err := domain.NewVideoJob(uc.ids, userID, filename, sourceKey, input.ContentHash)
	if err != nil {
		return CreateVideoJobResult{}, err
	}

	if err := uc.jobs.Create(ctx, job); err != nil {
		return CreateVideoJobResult{}, err
	}

	// job.CreatedAt() is still the zero value NewVideoJob left it at: Create
	// persisted PostgreSQL's own now(), not the aggregate's field. Reading
	// the row back is how this use case learns the value the database
	// actually minted, rather than reporting a timestamp of its own.
	persisted, err := uc.jobs.FindByID(ctx, job.ID())
	if err != nil {
		return CreateVideoJobResult{}, fmt.Errorf("video: reload created video job: %w", err)
	}

	return CreateVideoJobResult{
		JobID:            job.ID().String(),
		UserID:           job.UserID().String(),
		OriginalFilename: job.OriginalFilename().String(),
		Status:           string(job.Status()),
		CreatedAt:        persisted.CreatedAt(),
	}, nil
}
